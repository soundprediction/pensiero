# Specification: Symbolic Graph Logic (Ladybug backend)

**Component:** Symbolic Graph Logic / in-graph multi-hop reasoning
**Engine:** Ladybug (Kuzu-derived embedded property graph; Cypher) via the `go-predicato` driver
**Consumes:** the humn medical knowledge graph (ingested from predicato)
**Status:** Draft v1 (synthesis of the EGRE rule-class design, re-targeted from CozoDB to ladybug)

> This is the **deductive** counterpart to the inductive IGL in `DESIGN.md`. It answers specific
> verification queries — is a claim **entailed**, **contradicted**, or **unsupported** by the graph —
> and returns an **explainable proof path** with composed confidence. It is what the humn DDx verifier
> calls for "in-graph reasoning," upgrading today's single-hop NLI (premise = one best fact) to sound,
> bounded, multi-hop symbolic derivation.

---

## 0. Why ladybug (and what changes vs. the Cozo design)

`DESIGN.md` grounds the reasoning in CozoDB Datalog (declarative recursive rules + stratified negation +
fixpoint). We are instead backing the symbolic logic with **ladybug**, because:

- The medical graph already lives in ladybug (the humn canonical graph, built by predicato). No
  re-modelling of triples into a second store's relations.
- Ladybug/Kuzu natively supports **variable-length paths** `-[:R*min..max]-`, shortest-path, and
  bounded recursive traversal — enough to express multi-hop composition, transitive closure along one
  predicate, and subsumption.
- One graph engine across humn + pensiero (operational simplicity, identical provenance ids).

**The consequence:** ladybug is Cypher, **not Datalog**. It does *not* give us declarative fixpoint rule
materialization or stratified negation for free. So the rule classes split into two layers:

| Layer | Cozo design | Ladybug design |
|---|---|---|
| **Traversal** (composition, transitive closure, subsumption, reachability) | recursive Datalog rules | **parameterized recursive Cypher** (variable-length paths) |
| **Rule logic** (functorial normalization, symmetry/inverse mapping, disjointness/negation, confidence composition, proof assembly, closure caching) | Datalog rules / stratification | **Go orchestration** in `pkg/reasoning` over Cypher results |

The conceptual rule classes, soundness conditions, provenance model, confidence monoid, the Go API
surface, and the humn integration are unchanged from the EGRE design; only the *implementation engine*
moves.

---

## 1. Backend, data model, and the reified-edge convention

### 1.1 Storage ownership (critical)

Ladybug is a single-writer embedded engine: **only one process may open a database file** (a second
open hits a WAL/lock error). The live humn service already holds its graph open. Therefore pensiero MUST
**own its own ladybug database**, populated by ingesting from predicato (the existing
`pkg/connector/predicato.go` path), rather than opening humn's live graph file.

Two deployment shapes (pick per §5):
- **Embedded-in-humn:** the `pkg/reasoning` engine is linked into the humn process and reasons over the
  graph handle humn already has open. No second open, no ingest. Preferred when humn is the only caller.
- **Standalone pensiero service:** pensiero ingests predicato into its own ladybug and serves
  `/v1/reason/*`. Preferred when pensiero owns the graph, runs the background Thinking Loop, and serves
  multiple consumers. Reasoning runs read-only against pensiero's snapshot.

### 1.2 Graph schema (as built by predicato)

The medical graph uses a **reified-edge** model (Kuzu cannot put arbitrary properties on relationships
in all versions, so predicato represents `(n)-[P]->(m)` as a node):

```
(a:Entity) -[:RELATES_TO]-> (e:RelatesToNode_) -[:RELATES_TO]-> (b:Entity)
```

- `Entity.name`, `Entity.uuid` (PK, indexed), `Entity.labels` (`DISEASE`, `SYMPTOM`, `DRUG`,
  `EXPOSURE`, `THERAPEUTIC_CLASS`, …), `Entity.attributes` (JSON, incl. `canonical_id`, `entity_type`,
  `sources`).
- `RelatesToNode_.name` = the **predicate** (raw, e.g. `"is a symptom of"`, `"has phenotype"`,
  `"causes"`, `"associated with"`, `"is_a"`), `RelatesToNode_.fact` = the human-readable fact sentence,
  plus provenance attributes (`sources`, `upstream_sources`, optional `confidence`/`weight`).

**Consequence for every rule below:** one *logical* hop = **two** physical `RELATES_TO` edges, and the
predicate is read from the intermediate `RelatesToNode_`. A K-logical-hop path is `*2..2K`, with the
`RelatesToNode_` nodes along the path carrying the predicate chain and the proof.

### 1.3 Predicate registry & ontology metadata (the functor `F` + logical classes)

Ladybug has no `predicate_registry` table semantics out of the box; we hold this metadata in a small,
fast-loading structure (a Go-side map loaded at startup, optionally persisted as a side node table):

```go
type LogicalClass string
const (
    ClassPlain      LogicalClass = "plain"
    ClassTransitive LogicalClass = "transitive" // is_a, subClassOf
    ClassSymmetric  LogicalClass = "symmetric"  // associated_with, co_occurs_with
    ClassInverse    LogicalClass = "inverse"    // treats <-> treated_by, is_a <-> subsumes
)

type PredicateMeta struct {
    Raw       string       // surface form ("is a symptom of")
    Canonical string       // canonical predicate ("symptom_of")
    Class     LogicalClass
    InverseOf string       // for ClassInverse
    Domain    string       // expected subject label (e.g. SYMPTOM)
    Range     string       // expected object label (e.g. DISEASE)
}

type PredicateRegistry struct{ byRaw map[string]PredicateMeta; byCanon map[string]PredicateMeta }
func (r *PredicateRegistry) Canonical(raw string) (PredicateMeta, bool)
```

Two small ladybug node tables hold the ontology facts the traversal cannot infer:

```cypher
// Disjointness (conflict detection). Loader inserts lexicographically-ordered pairs.
CREATE NODE TABLE OntologyDisjoint(class_a STRING, class_b STRING, source STRING,
                                   PRIMARY KEY(class_a, class_b));
// Derived-closure cache (memoized proofs; status='deduced', never an axiom).
CREATE NODE TABLE DerivedClosure(key STRING PRIMARY KEY, source STRING, target STRING,
                                 predicate STRING, confidence DOUBLE, hops INT64,
                                 proof STRING /*JSON*/, rule_class STRING, computed_at STRING);
```

`is_a`/`subClassOf` taxonomic edges live in the graph itself (as `RelatesToNode_.name='is_a'`), so
subsumption is a traversal, not a side table.

**Functorial normalization (`F`)** runs in Go at the query boundary and per proof step: a raw predicate
from the graph (or from a caller's claim) is mapped to its canonical via the registry; unregistered
predicates pass through (identity functor). Because `F` only rewrites the *label* and never the
endpoints, `F(g∘f) = F(g)∘F(f)` holds by construction — normalizing each step then chaining is identical
to chaining then normalizing. Every rewrite `raw→canonical` is recorded as a proof annotation.

---

## 2. Rule classes — Cypher templates + Go orchestration

Notation: `$from`, `$to`, `$maxHops` (logical), `$preds` (allowed predicate set), `$minConf` are query
params. Physical depth is `2*$maxHops`. Entities are resolved to `uuid` first (indexed) so traversal is
anchored, never a full scan.

### 2.1 (a) Morphism / path composition — bounded, anchored, acyclic

The general multi-hop "is `from` connected to `to`, and how" backbone.

```cypher
// resolve anchors once (uuid is the PK index); name match is a cheap scan otherwise
MATCH (a:Entity), (b:Entity)
WHERE a.uuid = $fromUuid AND b.uuid = $toUuid
MATCH p = (a)-[:RELATES_TO* 2 .. $physMax]-(b)          // $physMax = 2*$maxHops
WHERE all(r IN nodes(p) WHERE
            (NOT 'GENE' IN coalesce(r.labels,[])) )       // drop molecular bloat mid-path
RETURN
  [n IN nodes(p) WHERE n.name IS NOT NULL AND size(coalesce(n.labels,[]))=0 | n.name] AS predicates,
  [n IN nodes(p) WHERE 'Entity' IN labels(n) | n.uuid]   AS entity_uuids,
  [n IN nodes(p) | coalesce(n.uuid, n.name)]             AS step_ids,
  length(p)/2                                            AS hops
ORDER BY hops ASC
LIMIT $limit
```

- **Acyclicity / termination:** Kuzu variable-length paths use **trail** semantics (no repeated
  relationship) and the `*2..$physMax` upper bound guarantees termination. For node-level acyclicity
  (no repeated Entity) we additionally enforce it in Go when assembling the proof, or use Kuzu
  `ACYCLIC`/shortest-path mode where available.
- **Anchoring:** both endpoints are bound by `uuid` (PK index) before the variable-length match — this
  is what keeps it tractable on the dense graph (vs. the unanchored traversal that was slow).
- **Predicate / label filtering** in the `WHERE` prunes the molecular bloat (GENE/PROTEIN/SUPPLEMENT
  intermediates) so the search stays in the clinical subgraph.
- **Go does:** normalize each `RelatesToNode_.name` via `F`, compose confidence (§3), build the
  `Proof`, and drop node-cyclic paths. Direction handling (symmetric/inverse) via the undirected `-`
  match plus §2.3 mapping.

### 2.2 (b) Transitive closure along a single predicate (`is_a`, `subClassOf`)

For a transitive predicate, derive `from —P→ … —P→ to`. In the reified model this is a variable-length
path where **every** intermediate `RelatesToNode_.name = P`:

```cypher
MATCH (a:Entity {uuid:$fromUuid})
MATCH p = (a)-[:RELATES_TO* 2 .. $physMax]->(b:Entity)
WHERE all(n IN nodes(p) WHERE
            (size(coalesce(n.labels,[]))>0)               // it's an Entity, ok
            OR n.name = $pred)                            // intermediate predicate node must be $pred
RETURN b.uuid AS target, [n IN nodes(p) | coalesce(n.uuid,n.name)] AS step_ids, length(p)/2 AS hops
```

- Directed (`->`) because `is_a` is directed. `$pred` is the canonical transitive predicate.
- **Soundness** requires `$pred` to be genuinely transitive (registry `Class=transitive`); the loader
  must only mark true taxonomic relations.
- Because `is_a` taxonomies are small and stable, the engine **pre-materializes** the `is_a` closure
  into `DerivedClosure` (§6) at ingest/refresh time, so subsumption checks (§2.4) are O(1) lookups
  rather than repeated traversals — the single biggest speedup.

### 2.3 (c) Symmetry & inverse

Ladybug edges are reified and traversed undirected with `-[:RELATES_TO*..]-`, so a stored
`A —symptom_of→ B` is already reachable from `B`. The **semantics** are fixed in Go:

- **Symmetric** predicates (registry `Class=symmetric`): an undirected traversal is sound; the derived
  reverse edge keeps the same canonical predicate.
- **Inverse** predicates (registry `Class=inverse`, `InverseOf=Q`): when a step is traversed against its
  stored direction, Go rewrites the predicate to its inverse `Q` in the proof (e.g. crossing a stored
  `treats` edge backwards is reported as `treated_by`). Directional, non-symmetric predicates
  (`causes`, `treats`) must **not** be marked symmetric; the inverse mapping preserves correctness when
  a path needs the reverse direction.

This is what lets `fatigue` reach `hypothyroidism` from a stored `hypothyroidism —has_symptom→ fatigue`.

### 2.4 (d) Ontology subsumption (subclass support)

The clinically central rule: a finding/condition of subclass `S` supports diagnosis `T` if `S is_a* T`.

```go
// supportsDx: finding ⇝ S (composition, §2.1) AND S is_a* T (closure, §2.2 / cache).
func (e *Engine) supportsDx(finding, target string, maxHops int) (*Proof, bool, error) {
    // 1. direct subsumption: finding is_a* target
    if pf, ok := e.isAClosure(finding, target); ok { return pf.tag("subsumption"), true, nil }
    // 2. path-to-subclass: finding ⇝ S, S is_a* target
    for _, p := range e.derivePaths(finding, "", maxHops) { // any endpoint S
        if sub, ok := e.isAClosure(p.Target, target); ok {
            return p.compose(sub).tag("composition+subsumption"), true, nil
        }
    }
    return nil, false, nil
}
```

- Derives `supports_dx(finding, diagnosis, confidence, proof)` — the verdict signal humn consumes.
- "Hashimoto's thyroiditis" supports "hypothyroidism" via `Hashimoto's —is_a→ hypothyroidism`.

### 2.5 (e) Disjointness / conflict detection (negation, done in Go)

A **contradiction** arises when an entity provably belongs to two ontologically disjoint classes.
Ladybug has no stratified negation, so the engine runs it as two positive membership queries + a
disjoint-table check in Go:

```go
func (e *Engine) Contradicts(c Claim) (bool, *Proof, error) {
    // classes the subject provably belongs to (is_a* closure incl. reflexive)
    held := e.memberOf(c.Subject)                 // []ClassWithProof
    // the claim asserts subject is_a object → would add membership in `object`
    for _, h := range held {
        if e.disjoint(h.Class, c.Object) {         // OntologyDisjoint lookup (both orderings)
            return true, conflictProof(h, c.Object), nil  // conf = min(h.conf, claim.conf)
        }
    }
    return false, nil, nil
}
```

- **Disjointness is logically dispositive**: a contradiction **hard-fails** the claim regardless of NLI.
- Confidence = `min` of the two membership legs (a conflict is only as strong as its weaker leg).
- Negation lives entirely in Go (positive Cypher + set logic), sidestepping ladybug's lack of
  stratified negation while preserving the semantics.

### 2.6 (f) Functorial normalization — §1.3

Applied in Go to every predicate read from the graph and to the caller's claim predicate. Records
`F:raw→canonical` proof annotations. Identity on unregistered predicates.

---

## 3. Derivation & provenance

### 3.1 Proof

```go
type ProofStep struct {
    EdgeID     string  `json:"edge_id"`    // RelatesToNode_.uuid (or "F:..", ":refl")
    Rule       string  `json:"rule"`       // composition|trans|sym|inv|subsumption|F|refl
    Predicate  string  `json:"predicate"`  // canonical predicate applied
    Source     string  `json:"source"`     // Entity uuid/name
    Target     string  `json:"target"`
    Confidence float64 `json:"confidence"` // this edge's confidence (pre-decay)
}
type Proof struct {
    Source, Target, Predicate string
    Hops       int
    Confidence float64       // composed (Context Monoid, §3.2)
    Steps      []ProofStep
    RuleClass  string
}
```

The Cypher returns the ordered `RelatesToNode_` ids (`step_ids`) and the predicate chain. Go **hydrates**
each step (one batched `MATCH (r:RelatesToNode_) WHERE r.uuid IN $ids RETURN r.uuid, r.name, r.fact,
r.confidence` query — not per-step), normalizes predicates, and assembles the `Proof`. The path *is* the
explanation returned to humn.

### 3.2 Confidence — Context Monoid

`conf(path) = (∏ conf(eᵢ)) · decay^(hops-1)`, default `decay=0.9`. Identity (reflexive/empty) = 1.0.
If any step's `RelatesToNode_` carries no confidence, use a per-source-type prior (axiom 1.0, statpearls
0.9, correlate 0.6, …). **Conflict short-circuit:** if two steps carry contradictory `context` conditions
(temporal validity, mutually exclusive qualifiers) the proof's confidence → 0 (checked in Go during
hydration, since condition JSON is opaque to Cypher). Decay makes long dense-graph chains rankable and
droppable.

---

## 4. Go API (`pkg/reasoning`)

Wraps a ladybug `*driver.LadybugDriver` (the go-predicato driver). Idiomatic: build a parameterized
Cypher string, `driver.ExecuteQuery(ctx, q, params)`, map rows to typed structs.

```go
type Engine struct { g GraphQuerier; reg *PredicateRegistry; cfg Config }

type Claim struct { Subject, Predicate, Object string }
type Verdict string
const ( VerdictEntailed Verdict="entailed"; VerdictContradicted Verdict="contradicted"; VerdictUnsupported Verdict="unsupported" )

type DeriveRequest struct {
    Source, Target string
    MaxHops        int      // logical hops, default 4 (physical 2N)
    Decay          float64  // default 0.9
    IncludeInverse bool     // default true for DDx
    Predicate      string   // optional: require derived predicate
    Preds          []string // optional: restrict intermediates to these canonical predicates
    MinConf        float64  // early prune
    Limit          int      // top-k proofs, default 8
}

func NewEngine(g GraphQuerier, reg *PredicateRegistry, cfg Config) *Engine
func (e *Engine) Derive(ctx, DeriveRequest) ([]Proof, error)              // §2.1 (+inverse/sym)
func (e *Engine) Entails(ctx, Claim) (Verdict, *Proof, error)            // §2.2-2.4, then §2.5 guard
func (e *Engine) Contradicts(ctx, Claim) (bool, *Proof, error)           // §2.5
func (e *Engine) ReachableWithin(ctx, node string, hops int) (map[string]Proof, error)
```

`Entails`: normalize the claim predicate (`F`); resolve `Subject`/`Object` to uuids; run `supportsDx`
(§2.4); if a proof exists → check `Contradicts` (§2.5) — a conflict **overrides** to `Contradicted`;
else `Entailed`(best proof) ; else `Unsupported`. Entity status is filtered in Cypher (`WHERE
NOT 'deduced' IN ...` by default) to avoid circular self-support from previously-derived edges.

---

## 5. Integration with humn DDx

humn's verifier is single-hop NLI (premise = one best fact). Layer the symbolic engine **first**:

1. **Symbolic (this engine):** `Entails(claim)`.
   - `Contradicted` → **hard fail**, return the conflict proof. NLI cannot find this.
   - `Entailed`, conf ≥ τ_high → **pass**, attach the proof path as the explanation. No NLI needed.
   - `Entailed` low-conf, or `Unsupported` → **defer to NLI**.
2. **NLI tiebreaker:** for deferred claims, run the existing DeBERTa NLI — but with the symbolic engine
   supplying the **proof path's hydrated fact sentences as the premise set**, upgrading humn's premise
   from "one best fact" to "the chained facts that symbolically support the claim."
3. **Fusion:** final trust = f(symbolic_confidence, nli_score).

**Interface.** Two shapes (per §1.1):
- **Embedded library** (`pkg/reasoning`) linked into humn — reasons over the graph handle humn already
  has open; zero ingest, zero network, no lock conflict. Recommended for the DDx verifier hot path.
- **Service** `POST /v1/reason/verify` (entities+predicate in → verdict+proof+confidence out) when
  pensiero owns the graph and runs the Thinking Loop.

```json
// POST /v1/reason/verify  →
{ "verdict":"entailed", "confidence":0.74,
  "best_proof":{ "source":"fatigue","target":"hypothyroidism","predicate":"symptom_of","hops":2,
    "rule_class":"composition+subsumption",
    "steps":[
      {"edge_id":"r-9912","rule":"comp","predicate":"symptom_of","source":"fatigue","target":"hashimoto","confidence":0.88},
      {"edge_id":"r-4471","rule":"subsumption","predicate":"is_a","source":"hashimoto","target":"hypothyroidism","confidence":0.94}]}}
```

In humn's DDx pipeline this slots into `recheck.go`/the verifier: where `groundCandidatesByName` /
`RecheckUnverified` today do a flat graph search + single-fact NLI, they instead (or additionally) call
`Entails(finding, "presenting_feature_of", candidate)` and, on `Entailed`, attach a
`VerifiedBy="graph-reasoning"` evidence ref whose citation is the proof path.

---

## 6. Performance & safety (dense-graph realities)

| Concern | Mechanism |
|---|---|
| **Anchoring** | Resolve both endpoints to `uuid` (PK index) before any variable-length match. Never run unanchored traversal (that was the slow path). |
| **Bounded depth** | `*2..2N`, logical `N` default 4, hard cap configurable. |
| **Confidence cutoff** | Prune mid-traversal when composed conf < `$minConf` (apply in Go as paths stream back, or as a `WHERE` on accumulated weight where Kuzu supports it). Most effective blowup control on a dense graph. |
| **Predicate/label filtering** | Drop GENE/PROTEIN/MOLECULE/SUPPLEMENT intermediates in the path `WHERE`; restrict to clinical predicates via `$preds`. Keeps the search in the clinical subgraph. |
| **Precomputed `is_a` closure** | Materialize the (small, stable) taxonomic closure into `DerivedClosure` at ingest/refresh; subsumption becomes a lookup, not a traversal. |
| **Closure caching** | `DerivedClosure` memoizes `(source,target,predicate)→proof`; invalidate entries whose proof references a retracted/modified `RelatesToNode_` (hook the ingest/refresh path). |
| **Cycle safety** | Kuzu trail semantics (no repeated edge) + depth bound + Go node-acyclicity check on proof assembly. |
| **Single-writer lock** | pensiero owns its ladybug (or embeds in humn’s handle); never co-open humn's live graph file (WAL/lock). Reasoning is read-only against a snapshot. |
| **Negation** | Done in Go (positive Cypher + disjoint-set check), avoiding ladybug's lack of stratified negation. |
| **Module segmentation** | If endpoints share a precomputed community/hypernode, constrain the traversal to that module's entities (a label/property filter), shrinking fanout. |

---

## 7. Worked examples (Cypher + derivation)

### 7.1 Multi-hop support (composition + subsumption) — `Entailed`
Claim: *"fatigue is a presenting feature of hypothyroidism."*
Graph: `fatigue —"is a symptom of"→ hashimoto_thyroiditis` (r-9912, 0.88) ; `hashimoto_thyroiditis —"is a"→ hypothyroidism` (r-4471, 0.94).
- `F`: `"is a symptom of"→symptom_of`, `"is a"→is_a`.
- `supportsDx`: path `fatigue ⇝ hashimoto_thyroiditis` (§2.1) ∧ `hashimoto_thyroiditis is_a* hypothyroidism` (§2.2 cache).
- Derived: `supports_dx(fatigue, hypothyroidism)`, conf `0.88·0.94·0.9 ≈ 0.74`.
- Proof: `[r-9912:comp:symptom_of] → [r-4471:subsumption:is_a]`. **Verdict: Entailed.**

### 7.2 Contradiction via disjointness — `Contradicted`
Claim: *"patient condition is hyperthyroidism"* while the graph supports `hypothyroidism`.
`OntologyDisjoint(hyperthyroidism, hypothyroidism, SNOMED)`. `memberOf(patient_cond)` ⊇ `{hypothyroidism}`.
`Contradicts` finds `hyperthyroidism ⊥ hypothyroidism` → **hard fail**, conf `min(...)`; NLI skipped; humn returns the conflict proof.

### 7.3 Pure subsumption — `Entailed`
Claim: *"patient has a thyroid disorder."* `hypothyroidism —is_a→ thyroid_disorder` (0.97). `patient_cond is_a* hypothyroidism is_a* thyroid_disorder` via the `is_a` closure. **Verdict: Entailed**, proof = the `is_a` chain.

---

## 8. Phased plan & tests

| Step | Deliverable |
|---|---|
| **S1** | `pkg/reasoning` skeleton + ladybug `GraphQuerier` adapter (go-predicato driver) + `PredicateRegistry` loader (`F`). |
| **S2** | §2.1 anchored bounded composition `Derive`; proof hydration + confidence monoid. |
| **S3** | §2.2 `is_a` transitive closure + precompute into `DerivedClosure`; §2.4 `supportsDx`. |
| **S4** | §2.3 symmetry/inverse mapping; §2.5 disjointness `Contradicts`; `Entails` verdict resolution. |
| **S5** | §6 caching/invalidation, `$minConf` cutoff, label/predicate filters, module scoping. |
| **S6** | humn integration: embedded `Entails` in `recheck.go` + NLI premise hand-off; optional `/v1/reason/verify`. |

**Tests** (in-memory ladybug graph, table-driven, mirroring the existing `db_test.go` style):
1. `F` normalization incl. unregistered-predicate identity.
2. Reified 1-hop and N-hop composition; `$maxHops` boundary (a 5-hop path excluded at maxHops=4).
3. `is_a` transitive closure + cycle guard (insert an `is_a` cycle, assert termination).
4. Symmetry vs. inverse (assert `treats` is NOT symmetric; reverse-cross reports `treated_by`).
5. Subsumption support (the §7.1 chain end-to-end → `Entailed`, hops=2, decayed confidence).
6. Disjointness (§7.2) → `Contradicted`, NLI short-circuited.
7. Confidence decay monotonicity (longer path ⇒ lower confidence).
8. `DerivedClosure` cache hit + invalidation on edge retraction.
9. humn integration: `Entails`→defer→NLI-premise hand-off (mock NLI); contradiction hard-fail.

---

## 9. Open questions for the lead

1. **Embedded vs. service** (§1.1/§5): does humn link `pkg/reasoning` over its open graph handle, or call
   a standalone pensiero service over its own ingested ladybug? (Recommend embedded for the hot path.)
2. **Edge confidence source:** do `RelatesToNode_` nodes carry a usable `confidence`/`weight`, or do we
   derive priors from `sources` (axiom/statpearls/correlate)? Affects §3.2.
3. **`is_a` coverage:** how complete is the taxonomic (`is_a`/`subClassOf`) layer in the current graph?
   Subsumption (§2.4) and disjointness membership (§2.5) depend on it; if sparse, we may need an
   ontology-load pass (DESIGN §3.3) before symbolic support is broadly useful.
4. **Kuzu version features:** confirm trail/acyclic semantics and whether intermediate-node predicate
   filtering in variable-length paths (§2.2) is supported in the pinned ladybug, else fall back to the
   precomputed `is_a` closure exclusively.
5. **Disjointness source:** which ontology populates `OntologyDisjoint` (SNOMED/MONDO)? Required for the
   contradiction rule to fire at all.

*End — ladybug-targeted symbolic graph logic spec.*
