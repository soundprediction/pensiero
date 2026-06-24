# Pensiero — self-optimizing reasoner daemon (design)

*Design plan, 2026-06-23. Synthesized from a code review of pensiero + humn and a
parallel design consult with codex. Status: proposed, not yet implemented.*

## Code-review corrections (authoritative — supersedes conflicting text below)

A read-only codex review against the actual code (see
[`SELF_OPTIMIZING_DAEMON_review.md`](SELF_OPTIMIZING_DAEMON_review.md)) found that the
first draft assumed capabilities the code does not yet have. Corrections, verified:

- **`cmd/pensiero serve` does not serve gRPC** — it opens a source graph, runs the IGL
  `Loop`, and exposes HTTP health/metrics only. Serving the reasoner over gRPC is **new
  Phase-1 work** (`--grpc-addr`, load a generation, register `grpcsvc`, readiness, shutdown).
- **`pkg/connector/predicato.go` only calls `/api/v1/extract`** (ingest). It is **not** a
  `GraphQuerier` and offers no embeddings/hybrid search. The actual serving graph is
  ladybug (`cmd/pensiero/ladybug_system.go`). The predicato **GraphQuerier / Embedder /
  Search adapters must be built** — they don't exist. (Intent stands: pensiero uses its
  sibling predicato; the doc just can't claim it already does.)
- **No `Engine` hot-swap setter.** `Engine` holds a private `GraphQuerier` from `NewEngine`.
  Hot-swap = swap the **whole immutable `Reasoner` generation** (which `GenerationStore`
  already does) or wrap the querier in an atomic holder — never mutate a live `Engine`.
- **Publish is not temp→rename of a file** — it stages a `.snap` dir and atomically swaps a
  **symlink**. Reload/watch/cleanup must be designed around versioned dirs + symlinks.
- **Predicate-correct entailment is a PREREQUISITE (Phase 0).** *Both* backends discard
  `Claim.Predicate` — the Go `Engine` derives only source→target, and the native ladybug
  `REASON_ENTAILS` ignores the predicate argument. (My earlier "pick `NativeReasoner`" was
  wrong — native is also unsound here.) Caching, validation, questions, and serving are all
  unsound until the chosen serving backend honors the predicate. Fix it + tests first.
- **Speculative-overlay quarantine is not yet real.** Queries don't filter by
  status/provenance and `ExcludeDeduced` is defined but unused. Storage-level provenance +
  default exclusion of speculative/deduced edges is a **prerequisite** before any thinking
  layer can be safe.
- **Live serving serializes on one ladybug handle mutex.** A per-generation **read-only
  handle pool belongs in Phase 1**, not Phase 7, or gRPC concurrency bottlenecks immediately.
- **pensiero is NOT domain-agnostic today** — `pkg/reasoning/predicate_primitives.go` hard-codes
  medical predicates (`symptom_of`, `has_phenotype`, …) and `cypher.go` filters a "clinical
  subgraph." **Resolved** via the predicate-packs design above: the engine stays generic and
  medical becomes a **preloaded data pack** (operators define extra predicates at run time).
  Remaining work: extract `medicalPredicates` into a registered pack and move the
  `cypher.go` "clinical subgraph" filter into pack/config (not core).
- Smaller: cancellation can still publish (check `ctx` immediately before publish); builder
  CPU loops lack cooperative `ctx` checks; cache key needs backend+`Registry.Fingerprint()`+
  scope route+config and must **never cache errors/timeouts**; idle detection needs an atomic
  load epoch/lease + cooldown + publish rate-limit to avoid livelock; the serve snapshot
  writer hard-codes 1024-dim embeddings; start with **fixed scoring weights, not a bandit**.

**Revised phase order (safety before autonomy):**
1. **Static gRPC daemon** — one loaded generation, readiness, **read-only handle pool**,
   graceful shutdown. Choose & pin the serving backend (`NativeReasoner` vs `Engine`).
2. **Telemetry + cross-request cache** — generation-aware, never caches errors/timeouts;
   add `Registry.Fingerprint()`.
3. **Provenance + manual validated hot-swap** — storage-level status, default-exclude
   speculative/deduced, composite validator, generation store, atomic symlink swap, rollback.
4. **Idle scheduler** — atomic load lease + cooldown + rate-limit; drive `Loop.RunOnce`;
   ctx checks in builder loops.
5. **Telemetry-driven IGL** — scope scoring with fixed weights.
6. **predicato adapters** (GraphQuerier/Embedder/Search) **+ background cognition** — the
   thought-types, embedding-biased exploration, and question emission. Last, because they
   are the least code-ready and the highest-risk.
7. **Pooling + leader election** — multi-instance scale.

Everything below is the original design intent; where it conflicts with this section, this
section wins. humn-specific numbers (800 ms, 24 queries/DDx, `ReasoningMS`) are illustrative
of one consumer, not requirements baked into pensiero.

## Predicate packs & operator-defined predicates (domain decision — RESOLVED)

The domain-agnosticism question is resolved as a **middle path**: the *engine* stays generic
(it reasons only over the general characteristic primitives — transitive, inverse,
sub-property, composition, disjoint), and **domain vocabulary lives in predicate packs that
are loaded, not compiled into reasoning behavior**. pensiero **preloads the medical pack by
default**, and an operator can **define extra predicates when running**. This honors "the
engine is domain-agnostic" while keeping medical batteries-included.

The code is already 80% shaped for this — `DefaultGeneralRegistry()` (generic),
`medicalPredicates` + `DefaultMedicalRegistry()` (general + medical), and a JSON registry
loader (`cmd/pensiero/registry.go`). The change is to make the layering first-class:

```go
// pkg/reasoning — a pack is pure data (no engine behavior).
type PredicatePack struct {
    Name         string
    Predicates   []PredicateMeta
    Compositions []CompositionRule
    Disjoints    []DisjointPair
}

func RegisterPack(p PredicatePack)              // built-ins register at init: "general", "medical"
func Packs() []string                            // discoverable
func BuildRegistry(packs []string, extra ...PredicatePack) (*PredicateRegistry, error)
```

- **`general`** is always the base. **`medical`** is the existing `medicalPredicates`
  (+ its compositions/disjoints) extracted out of the reasoning path into a registered pack.
  `DefaultMedicalRegistry()` becomes `BuildRegistry([]string{"medical"})` — **medical
  preloaded by default**.
- **Defining extra predicates at run time** — two interchangeable ways, both reusing the
  existing loader:
  1. **Flags/config:** `--predicate-packs medical,legal` + `--predicates-file extra.json`.
  2. **One self-describing registry file** that names the packs to preload and adds its own:
     ```json
     { "extends": ["general","medical"],
       "predicates": [ {"canonical":"interacts_with","inverse_of":"interacts_with",
                        "chars":["symmetric"],"sub_property_of":["associated_with"]} ],
       "compositions": [], "disjoints": [], "aliases": {"contraindicated_in":"contraindication_of"} }
     ```
     `loadRegistry` resolves `extends` to registered packs, then layers the file's own
     predicates/aliases on top.
- **Merge order** = general → named packs → file extras; **later layers override earlier**
  by canonical key (overriding a built-in's characteristics is allowed but logged). Build
  **validates**: a predicate referencing an unknown inverse / super-property / disjoint
  partner is a hard error, so a bad operator pack can't silently weaken reasoning.

### Relationships between predicates, and conditions on head/tail

A predicate definition expresses exactly these relationships (an OWL-property-axiom subset,
kept bounded so reasoning stays a tractable path search, not full OWL-DL):

- **inverse_of** — `P(a,b) ⟹ inverse(b,a)`
- **sub_property_of** — `P ⊑ Q` : `P(a,b) ⟹ Q(a,b)` (hierarchy)
- **characteristics** — the predicate's own algebra: transitive, symmetric, asymmetric,
  reflexive, irreflexive, functional, inverse-functional
- (global) **compositions** — `First∘Second ⊑ Result` (role chaining; transitivity is the
  `First==Second==Result` special case)
- (global) **disjoint** — `A,B` cannot both relate the same ordered pair → drives
  contradiction detection
- (global) **aliases** — surface form → canonical (the normalization functor)

**Conditions on head (subject) / tail (object).** Two tiers:

1. *Algebraic / cardinality* (already supported, via characteristics): functional (each head
   has ≤1 tail), inverse-functional (each tail has ≤1 head), reflexive/irreflexive
   (`a==b` required/forbidden), symmetric/asymmetric (head/tail swap algebra).
2. *Type constraints* — **new optional `domain` / `range`** on a predicate, naming the
   allowed entity types of head and tail:

   ```json
   { "canonical": "symptom_of",
     "domain": ["Finding","Symptom"],     // allowed head types
     "range":  ["Disease","Condition"],   // allowed tail types
     "inverse_of": "has_symptom", "sub_property_of": ["associated_with"] }
   ```

   The engine uses domain/range three ways, each of which also strengthens the
   self-optimizing design:
   - **Validate** a query/claim — reject or down-weight an ill-typed claim before searching.
   - **Guard generalization** — IGL must **not lift** a relation onto a parent whose type
     violates **`domain`** (the lifted parent is the relation's *head*, so this is a domain
     check, not range) — a primary over-generalization guard. **Soft** (validate-and-warn)
     until closed-world type provenance exists.
   - **Contradiction signal** — a stored triple violating domain/range is an inconsistency →
     emits a resolution **question**.

   *Prerequisite:* entity **types/labels must be available from the graph** (predicato must
   expose node labels via the GraphQuerier adapter). *Bound:* we cap head/tail conditions at
   `domain`/`range` **type sets** — arbitrary guards / n-ary conditional rules push into
   Datalog/OWL-DL and break the bounded-search cost model, so they are out of scope.
   `domain`/`range` are **optional**: a predicate with neither is unconstrained (today's
   behavior), so this is purely additive.

### Predicate inventory

pensiero maintains a live **inventory of predicate types** — the union of what it *knows*
(declared in packs/extras) and what it *observes* (distinct relationship types actually
present in the graph) — with per-predicate stats. It is built at startup and refreshed by a
low-priority background **inventory thought**, and exposed for introspection (gRPC method +
HTTP `/inventory`).

```go
type PredicateInfo struct {
    Canonical string
    Declared  bool                       // is it in any loaded pack/extra?
    Meta      *PredicateMeta              // declared relationships/characteristics, if any
    Count     int64                       // observed edges in the graph
    HeadTypes map[string]int64            // observed head (subject) entity-type distribution
    TailTypes map[string]int64            // observed tail (object) entity-type distribution
    Suggested *PredicateMeta              // inductively proposed inverse/chars/domain/range
}
type Inventory interface {
    List() []PredicateInfo
    Get(canonical string) (PredicateInfo, bool)
    Refresh(ctx context.Context) error    // background scan via the GraphQuerier
}
```

What the inventory powers:

- **Gap detection** — predicates **observed but undeclared** (no pack defines them → no
  inverse/characteristics → weak reasoning) and **under-specified** declared ones are
  flagged. Each becomes either an operator action or an emitted **question** ("is
  `interacts_with` symmetric?", "what is the inverse of `X`?").
- **Inductive domain/range** — from the observed head/tail type distributions the inventory
  **proposes** `domain`/`range` (e.g. 98% of `symptom_of` heads are `Finding` → suggest
  `domain:[Finding]`). A proposal is a generalization candidate and goes through the same
  validation gates before it constrains live reasoning.
- **Steering cognition** — high-frequency, under-characterized predicates are high-value
  thinking targets; the inventory feeds the topic selector and the question generator.

*Prerequisite:* a `GraphQuerier` method to enumerate distinct predicates and their head/tail
type distributions (part of the predicato GraphQuerier adapter). Domain-agnostic — it only
counts and types what is already in the graph.

### Entity-type models (coverage guarantee)

domain/range and the inventory only mean something if pensiero has a **model for every
entity type it uses**. A *type model* is `{name, supertypes (is_a hierarchy), status,
centroid?, confidence}`. pensiero guarantees coverage of every type referenced by a
predicate's domain/range or observed in the graph, by one of three means — in order of
preference:

1. **Explicit** — declared in a **type pack** (the entity-type analogue of predicate packs:
   a type + its `is_a` supertypes), shipped/preloaded the same way and extendable at run time.
2. **Approximate (embedding)** — for an *observed-but-undeclared* type, pensiero builds a
   model with the **embedding model (predicato's `/v1/embeddings`)**: embed a sample of the
   type's member entities, take the centroid, and place the type relative to known type
   centroids (nearest explicit supertype/siblings by cosine); cluster genuinely-unknown
   entities into provisional types. Marked `approximate` with a confidence.
3. **Ask** — when a type is only approximate, or its placement is **ambiguous** (centroid
   near a boundary between known types, or low intra-cluster cohesion / low confidence),
   pensiero **emits a clarification question** rather than committing — e.g. *"type `X`
   (≈ `Finding`, conf 0.62): confirm supertype or define it."*

```go
type TypeStatus int // Explicit | Approximate
type TypeModel struct {
    Name       string
    Supertypes []string   // is_a hierarchy
    Status     TypeStatus
    Centroid   []float32   // embedding centroid (approximate models)
    Confidence float64
}
type TypeModels interface {
    Get(name string) (TypeModel, bool)
    // EnsureCoverage builds approximate models for any uncovered types and returns the
    // ones that need operator clarification (approximate or ambiguous).
    EnsureCoverage(ctx context.Context, types []string) (needClarification []TypeModel, err error)
}
```

**Discipline (same as speculative output):** an **approximate** type model is provisional —
it *informs* reasoning (soft typing, ranking, suggestions) but does **not** harden a
domain/range constraint, block a query, or fire a contradiction until it is **confirmed**
(operator answer, or promotion through validation). So embedding-based guesses never
silently corrupt logic; they raise questions and wait. A startup/inventory pass runs
`EnsureCoverage` over all in-use types, so "every type has at least an approximate model,
and every approximate one has an outstanding clarification question" is an invariant, not a
hope.

Net effect: pensiero core imports no domain content into its *logic*; medical is a default
data pack it ships and preloads; any consumer adds its own predicates with one file or flag.
This supersedes the "domain decontamination" decision flagged in the review.

## Goal

Run pensiero as a long-lived **daemon** that (1) serves reasoning queries
(`Entails`/`Contradicts`/`Derive`) over gRPC, and (2) **continuously improves its own
logic efficiency by generalization** in the background — at low priority when idle,
steered by the queries it actually receives — and hot-swaps the improved logic into the
live reasoner with no downtime and no regression.

## Current state (what already exists)

- **Reasoner** — `pkg/reasoning`: `Engine.Derive/Entails/Contradicts` over a
  `GraphQuerier` interface + a `PredicateRegistry`. Pure Go; the graph is abstracted, so
  it can be **swapped behind the interface**.
- **IGL** — `pkg/generalization`: *Inductive Generalization Learning* lifts relations
  shared by many children of a taxonomic parent up to the parent (above `min_support`),
  i.e. it **compresses many specific facts into fewer general rules** — shorter proof
  paths. `Loop.Run/RunOnce`, `Publisher.Publish` (stages a `.snap` dir + atomic symlink swap), and a
  `SnapshotValidator` hook already exist.
- **Daemon shell** — `cmd/pensiero/serve.go`: runs IGL on a fixed `--interval`, with
  scopes and a health endpoint.
- **gRPC** — `pkg/grpcsvc`: serves the reasoner (`Entails/Contradicts/Derive/Health`).
- **humn** already supports a **remote** reasoner via `reasoning.grpc_endpoint`.

**The gaps:** in production humn runs pensiero **in-process over *static* generalization
graphs**; the IGL loop is **not running**, reasoning and IGL are **separate**, there is no
idle-aware scheduling, no query-driven optimization, no cross-request cache, and no
validated hot-swap. Measured impact today: a DDx issues **up to 24 `Entails` queries at an
800 ms budget each**, and reasoning is **~14–17 s of a ~50 s DDx** — so this is a real
latency lever, not just elegance.

## Dependencies & boundaries

pensiero depends only on its **sibling, predicato** — never on humn or any application.
*(The engine is domain-agnostic; domain vocabulary loads as **predicate packs** — medical
is preloaded by default, operators add extras at run time. See "Predicate packs" above.)*
The intent is for predicato to supply pensiero's graph and its embeddings + hybrid search,
but the **adapters must be built** — `pkg/connector/predicato.go` today only ingests via
`/api/v1/extract` and is not a `GraphQuerier`. The current serving graph is ladybug. Everything an application
needs to plug in — the golden/canary set used for validation, the sink for emitted
questions, the consumer's per-query latency budget — is a **generic interface** that any
caller populates. humn is one such caller (it already points `reasoning.grpc_endpoint` at
pensiero and supplies a per-query budget); it is referenced below only as the *motivating
example*, not a dependency. Nothing in pensiero imports humn.

## Target architecture — one process, three planes

1. **Query plane** — the existing `grpcsvc.Server` wraps a new `DaemonReasoner` that still
   satisfies `reasoning.Reasoner`. Per call: load-track → normalize via registry → route to
   active scope(s) → check generation-aware cache → delegate to the live `Engine` → record
   telemetry. **No humn API change** — point `reasoning.grpc_endpoint` at the daemon.
2. **Cognition plane** — an idle-aware scheduler runs a stream of self-generated
   **thoughts** (reasoning tasks). IGL is one thought-type; others actively explore the
   logic space. *What* it thinks about comes from a topic selector that blends
   query-driven exploitation with randomized/heuristic exploration (see "Background
   cognition" below). Drives the existing `generalization.Loop.RunOnce` /
   `Publisher.Publish` for the IGL thought-type. Never `Loop.Run` (no blind ticking).
3. **Snapshot plane** — validates, publishes, loads, and atomically swaps serving
   "generations" with refcounted teardown of the old one.

### Core interfaces (bind to existing types)

```go
type GraphHandle interface { reasoning.GraphQuerier; Close() error }

type Generation struct {                 // an immutable, loaded logic snapshot
    ID, Scope, Path string
    Reasoner reasoning.Reasoner           // built on this generation's graph
    Graph    GraphHandle
    Stats    generalization.Stats
    CreatedAt time.Time
}

type GenerationStore interface {          // live pointer per scope, refcounted
    Acquire(scope string) (*Generation, func(), bool)  // release() drops refcount
    Swap(scope string, next *Generation) (prev *Generation)
    Scopes() []string
}

type LoadTracker interface {              // idle signal from the query path
    Begin(method string) func(QueryEvent)
    Snapshot() LoadSnapshot
    WaitIdle(ctx context.Context, quietFor time.Duration) error
    Yield(ctx context.Context) error      // background work calls this between units
}

type QueryTelemetry interface {           // steers IGL
    Observe(QueryEvent)
    HotKeys(n int) []QueryKey
    PlanScopes(base []generalization.Scope) []generalization.Scope
}
```

## Idle-aware low-priority scheduler

Run an IGL pass only when **eligible**: `in_flight == 0` continuously for `quiet_for`
(2–5 s), EWMA QPS below threshold, recent p95 under the SLO guard (the 800 ms budget), and
optionally system CPU/IO pressure low. **Backoff** with jitter if busy at wake time;
**cancel the in-flight pass** if load appears mid-pass (`Publisher.Publish` already cleans
its temp output on failure); reset backoff on a successful publish. Only one pass at a time.

**Yielding:** wrap `Publisher.Source` in a `LoadAwareGraphQuerier` that calls
`LoadTracker.Yield` before each source query, and add cooperative yield points inside the
builder's CPU loops. IGL uses **separate** graph handles from serving (never the live
handles), with Ladybug `MaxNumThreads=1` and smaller buffers.

**OS priority (codex's correct caveat):** cgroups are *process*-scoped, so they can't
deprioritize one goroutine. Best effort: pin the IGL worker to a locked OS thread with
`nice`/`ioprio`/idle scheduling. If stronger isolation is needed, the daemon supervises an
**optimizer child process** in a lower-priority cgroup while keeping the same external API.
(On the single box today, pensiero shares CPU with humn — so idle-awareness should key off
*system* load until pensiero runs on its own pooled instance per the 3-tier topology.)

## Query-driven optimization + cross-request cache

Record per query (bounded in-memory; hashed entity names in exported metrics, raw names
kept in memory only for scope building — keeps pensiero **domain-agnostic**):

```go
type QueryEvent struct {
    Method string; Key QueryKey; Scope, GenerationID string
    Duration time.Duration; DeadlineMS int64; TimedOut bool
    Verdict reasoning.Verdict; Confidence float64
    ProofHops, ProofSteps, ResultCount int; CacheStatus, ErrClass string
}
```

**Steer IGL** by scope score:
`query_rate + slow_query + timeout + cache_miss + hot_unsupported_pair + long_proof`.
The planner reorders `Loop.Scopes`, adds hot entities to `ScopeEntities` for focused
overlays, narrows `Config.Predicates` to observed predicates (+ registry-inheritable), and
prewarms the cache for hot keys after a swap (idle only).

**Proof cache** — process-local weighted LRU + `singleflight`, keyed by
`hash(method, queryKey, routeScopes, generationIDs, registryFingerprint, reasonerConfig)`.
**Invalidation is free via generation IDs:** a hot-swap mints a new generation ID, so new
requests miss old entries automatically; in-flight requests finish on the old generation;
the old cache namespace is dropped once its refcount hits zero. This is the **single
biggest near-term win** — humn only caches *within* one DDx today, so a cross-request cache
of hot `(condition, finding)` entailments directly attacks the measured ~14–17 s.

## Background cognition — what pensiero "thinks" about

The cognition plane runs a stream of **thoughts** — self-generated reasoning tasks — at low
priority when idle. Each thought is `(thought_type, topic, budget)`. A **topic selector**
decides *what* to think about by blending exploitation (what we query) with exploration
(randomness + heuristics) — so pensiero keeps thinking even with zero live traffic.

**Thought types** (all generic graph/logic ops — pensiero stays domain-agnostic):

- **Generalize (IGL)** — find lift candidates over a scope (the existing pass).
- **Derive-explore** — pick a seed entity, enumerate reachable proof paths; materialize the
  useful derived edges as speculative facts.
- **Hypothesis-test** — generate candidate claims (subject × predicate × object from
  sampling, analogy, or a lift candidate) and run `Entails`/`Contradicts`; confirmed →
  candidate facts, conflicts → findings.
- **Contradiction-hunt** — proactively scan for ontology-disjointness conflicts (consistency
  checking). A detected contradiction is not just logged — it **raises resolution questions**
  (see below) and quarantines the implicated edges from generalization until resolved.
- **Gap-fill** — deepen reasoning exactly where live queries timed out or returned
  `unsupported`.
- **Proof-precompute** — precompute and cache proofs for likely-hot claims so future live
  queries are cache hits.
- **Ask** — identify the highest-information-gain gaps (near-miss generalizations) and
  emit them as questions (see "Emitting questions" below).

**Topic selection — a multi-armed bandit over thought-sources.** Each source proposes
topics; an ε-greedy / softmax allocator splits the idle budget across them and learns from
reward (did the thought yield something useful — a validated derivation, a new lift, a
filled gap, or a later cache hit):

| Source | Mode | Picks topics by… |
|---|---|---|
| **Query-hot** | exploit | telemetry hot keys / scopes (the `QueryTelemetry` above) |
| **Random** | explore | uniform or degree-weighted sampling of entities & predicate pairs — *the floor when there is no query signal* |
| **Novelty / recency** | explore | the recently added/changed subgraph (the frontier) |
| **Uncertainty / curiosity** | explore | borderline-confidence or previously-timed-out claims — highest information gain |
| **Structural** | explore | hubs, dense neighborhoods (rich generalization soil), bridge nodes |
| **Semantic (embedding)** | both | embedding-space neighbors of hot topics, analogy pairs, and a configurable interest vector (see below) |
| **Unresolved** | exploit | known contradictions, hot `unsupported` pairs |

Cold start or quiet traffic → weight shifts to **random + structural + novelty** (this is
the "randomize / other heuristics" the product calls for); under load the optimizer is
asleep anyway, and when it wakes with recent traffic it leans **query-hot + unresolved**.
All weights are config; random is the guaranteed floor so it never stalls.

```go
type Thought struct { Type ThoughtType; Topic Topic; Budget Budget }

type ThoughtSource interface {        // one per strategy above
    Name() string
    Propose(ctx context.Context, n int, snap LoadSnapshot) []Thought
    Reward(t Thought, outcome ThoughtOutcome)   // bandit feedback
}

type TopicSelector interface {        // ε-greedy/softmax over sources
    Next(ctx context.Context, budget Budget) (Thought, bool)
    Observe(Thought, ThoughtOutcome)
}
```

### Embedding-biased exploration (via predicato)

Thinking can be **directionally biased by an embedding model** — sourced from **predicato**
(the sibling already backing pensiero's graph), whose `/v1/embeddings` + hybrid search
pensiero calls. It lazily embeds the entity/predicate labels it encounters (queries +
sampled frontier), caches the vectors, and uses an ANN index — the graph is far too large
to embed up front. Uses:

- **Semantic exploitation** — expand query-hot topics to their embedding neighbors, so it
  thinks *around* what is asked, not only the exact entities.
- **Analogy-driven hypotheses** — entity pairs that are embedding-near but graph-distant
  are candidate claims to test (predict missing edges from semantic similarity); confirmed
  → new facts/lifts.
- **Directional bias** — a configurable **interest vector** (a seed entity set, or a phrase
  embedded on the fly) defines a region of embedding space; topic sampling is weighted by
  cosine to it, steering what pensiero dwells on toward a chosen theme.
- **Coverage / novelty** — track centroids of recently-thought topics and bias exploration
  toward under-covered regions for diversity.

```go
type Embedder interface { Embed(ctx context.Context, texts []string) ([][]float32, error) } // predicato /v1/embeddings
type SemanticIndex interface {
    Neighbors(vec []float32, k int) []Topic
    FarFrom(centroids [][]float32, k int) []Topic   // novelty / coverage
}
```

Domain-agnostic: pensiero consumes only predicato's embedding endpoint; the semantics live
in the model + data, not in pensiero. Self-hosted, so no API keys or token fees.

**What thoughts produce, and how it stays safe.** A thought that derives new facts must
**never affect live grounding until validated** — random exploration cannot pollute
answers:

- *Materialized derivations* → a **speculative overlay**, quarantined from live reasoning.
  It is promoted into a published generation only after the same structural + canary +
  latency gates as an IGL snapshot. Until then pensiero exposes it on an **unconfirmed**
  output stream that any consumer may treat as facts-to-confirm.
- *Generalization candidates* → feed IGL.
- *Precomputed proofs* → seed the cross-request cache (idle-only prewarm).
- *Questions* → near-miss generalizations become emitted questions (below).
- *Contradictions / surprises* → a findings log for review.
- *Difficulty stats* (what was hard, what timed out) → steer the next round — closing the
  loop so thinking concentrates where reasoning is weakest.

**Discipline:** thoughts are just more low-priority work units on the idle scheduler — they
yield to queries (`LoadTracker.Yield`) and are cancellable mid-thought. The bandit bounds
total idle spend; speculative output is gated exactly like IGL snapshots.

### Emitting questions (active learning toward generalization)

A thought often finds a generalization that is *almost* justified but blocked by a missing
fact — a lift candidate whose support is a sibling or two short, or a property that holds
for k-1 of k members of a class. pensiero turns those gaps into **questions**: the specific
claims whose answers would most raise support for a generalization (maximum information
gain toward a lift). It does **not** wait passively — asking is a first-class output.

- **Generation** — from (a) near-miss lift candidates, sibling-coverage gaps, and
  high-uncertainty hypotheses, ranked by *expected generalization gain*; and (b)
  **detected contradictions**, where the question is *disambiguating* — which of the
  conflicting facts is wrong, or what distinction (sense, time, context) reconciles them —
  ranked by how much inconsistency the answer clears. Both are **deduped semantically via
  predicato embeddings**.
- **Shape** — generic, claim-shaped (`reasoning.Claim`): "does relation R hold between A
  and B?" / "is X a P?" — domain-agnostic, so any consumer maps them to its domain.
- **Emission** — a generic `QuestionSink` (gRPC stream / log / queue). A consumer can
  answer from its own sources, a human, or an ingestion pipeline, and may also try to
  answer from **predicato's hybrid search** directly before escalating.
- **Closing the loop** — answers become source facts in predicato → the next IGL pass lifts
  the now-supported generalization → pensiero got smarter *because it asked*. This is the
  active-learning counterpart to passive IGL.

```go
type Question struct {
    Claim       reasoning.Claim
    Rationale   string    // which generalization it would unlock
    ExpectedGain float64
}
type QuestionSink interface { Emit(ctx context.Context, qs []Question) error } // consumer-supplied
```

## Validation, atomic hot-swap, rollback

Reuse `generalization.Publisher` with a `compositeSnapshotValidator` that extends the
existing hook and **must pass before publish**:

1. **Structural** — opens read-only, non-empty, endpoint integrity, stats sanity.
2. **Correctness replay** — run a golden/canary claim set on current vs. candidate:
   entailed/contradicted verdicts must not flip, confidence must not drop below a floor,
   **negative canaries must not become entailed**. The set is supplied by the consumer
   through a generic `GoldenSet` interface (plus pensiero's own recent hot queries as an
   auto-canary) — pensiero stays generic and carries no domain content.
3. **Latency replay** — candidate p95 ≤ current × 1.10 and no query over the 800 ms budget.
4. Candidate must load as a real `reasoning.Reasoner` before it's swap-eligible.

Swap: publish (atomic rename) → open as a new generation → `GenerationStore.Swap` flips the
live pointer → old generation closes when no in-flight request holds it. **Rollback:** keep
a small ring of prior generations; if post-swap health shows error/latency regression,
swap the pointer back. (No runtime proof that a lifted edge is semantically correct is
possible — the guarantee is "no publish without passing structural + canary + latency
gates + post-swap rollback monitoring.")

## Metrics / observability

HTTP `/healthz` `/readyz` `/metrics` + gRPC `Health`. Track: query inflight/QPS/latency
histogram by method·scope·verdict·cache-status, timeouts; cache hit ratio/evictions/
singleflight; generation id/swaps/rollbacks; IGL pass duration, direct vs. lifted relation
counts, deltas; scheduler idle/active/backoff/cancellations; validation replay pass/fail
and candidate vs. current p50/p95; **logic-efficiency**: graph time per call, proof hops,
proof steps, cache contribution, and the 800 ms deadline success rate.

## Horizontal scaling (fits the 3-tier topology)

A pool of identical daemons behind humn's existing gRPC pool target. Two modes:
**local snapshots** (each instance optimizes independently while idle) or **shared
snapshot dir** (one optimizer *leader* per scope writes; all instances watch the
symlink/manifest and hot-reload). Leader election stays domain-agnostic: POSIX `flock` per
scope, or a Kubernetes Lease. Atomic rename needs a POSIX-like shared FS; for object
storage later, replace with a versioned manifest pointer + compare-and-swap.

## Phased plan (converged — supersedes any earlier ordering in this doc)

Two codex reviews + an independent pass converged on **semantics & safety before autonomy**,
and on treating the knowledge-model layer as **non-blocking advisory** until the foundations
are real. Order:

0. **Predicate-correct entailment** *(prerequisite for everything claim-shaped)*. Today
   **both** backends discard `Claim.Predicate` — the Go `Engine` derives only source→target,
   and the native ladybug `REASON_ENTAILS` ignores the predicate arg. Caching, validation,
   questions, and serving are all unsound until entailment honors the predicate. Fix the
   chosen serving backend + tests first.
1. **Static gRPC daemon** — `--grpc-addr`, load ONE immutable generation, wrap a
   `reasoning.Reasoner` in `grpcsvc`, readiness, graceful shutdown, and a **read-only handle
   pool** (the ladybug handle serializes on a per-handle mutex, so the pool is required here,
   not later). *Deliverable: a consumer points its `reasoning.grpc_endpoint` at the daemon.*
2. **Registry packs + fingerprint** — `RegisterPack`/`BuildRegistry`/`extends`, **build
   validation** (and **repair the medical pack** — `contraindicated`/`indicated`/`prevents`
   are used in disjoints but undeclared), built-in overrides **off by default**,
   `Registry.Fingerprint()` + `All()`. Packs are **validated registry DATA**; only the
   primitives the engine actually exercises (composition, transitivity, sub-property,
   inverse, disjoint) are active — cardinality/reflexivity characteristics are inert metadata
   today, so don't promise them.
3. **Provenance + validated hot-swap** — storage-level status/provenance, **wire
   `ExcludeDeduced` and default-exclude speculative/deduced edges**, composite validator,
   immutable generation store, **`.snap` + atomic symlink swap**, refcounted close, rollback
   ring. This is the safety floor every later phase needs.
4. **Telemetry + cross-request cache** — generation-aware, keyed incl.
   `Registry.Fingerprint()`; **never cache errors/timeouts**. (The cross-request cache is the
   first real latency win.)
5. **Idle IGL scheduler** — drive `Loop.RunOnce` behind an atomic **load lease + cooldown +
   publish rate-limit**; ctx checks in builder CPU loops; **fixed scoring weights, no bandit**
   yet.
6. **Advisory knowledge-model layer** *(non-blocking)* — explicit **type packs** + **soft**
   `domain`/`range` (domain=head/subject, range=tail/object; validate-and-warn, **no
   contradictions without closed-world type provenance**); then the **inventory** as an
   **incremental/offline materialized stats job** (sampled/watermarked, not a startup full
   scan).
7. **predicato adapters + cognition** — build the `GraphQuerier`/`Embedder`/`Search` adapters
   (batching, persistent cache, rate limits, failure behavior), then the thought-types,
   embedding-biased exploration, **embedding type-models (suggestions/questions only, keyed
   by model+version+confidence, never harden unless confirmed)**, and **question emission**.
   Last, because least code-ready and highest-risk.
8. **Pooling + leader election** — multi-instance horizontal scale.

Phase 0–1 make it serve correctly; 2–3 make it safe to change; 4–5 make it self-optimize;
6–7 are the advisory "thinking" layer; 8 scales it.

## Measuring "logic efficiency" improvement

Fixed replay set of production-shaped `Entails`/`Derive` traffic, before/after:
p50/p95/p99 latency, **timeout rate at the consumer's budget**, graph time per logical
query, **proof hops & steps**, cache hit ratio, the consumer's reasoning-time and
completed-queries-per-request (e.g. humn's `ReasoningMS` / Entails-per-DDx), and
snapshot compression (direct vs. lifted relation counts, total serving-graph size). A good
release **preserves verdicts on the golden suite, no p95 regression, lower timeout rate,
fewer hops per accepted proof.**

## Risks

- **Over-generalization → false positives.** Conservative support floors, negative
  canaries, staged rollout + rollback.
- **Background IGL still competes for IO.** Cancellation, separate handles, low thread
  count, OS best-effort priority, or an isolated child process.
- **Wrong-scope routing** without a topic hint. Entity-presence routing + bounded fallback;
  optional `pensiero-scope` gRPC metadata later.
- **Cache memory growth** under diverse queries. Weighted LRU + per-generation eviction.
- **Replay misses a rare regression.** Keep post-swap rollback monitoring live.
- **Speculative "thoughts" pollute live answers.** Quarantine all derive/hypothesis output
  in the speculative overlay; it reaches grounding only via the same validation gates, and
  is surfaced as *unconfirmed* until then. Random exploration is read-only against the
  source graph.
- **Thinking runs away (cost / churn).** The bandit has a hard idle budget; a thought that
  never yields reward is down-weighted; snapshot publishes are rate-limited so background
  curiosity can't thrash the live generation.

## Open questions for review

- Single global scope first, or multi-scope routing from day one? (a consumer like humn
  routes per topic snapshot locally today; the daemon needs equivalent server-side routing.)
- Golden/canary set ownership — supplied by the consumer via the `GoldenSet` interface
  (pensiero ships none); where does it live and how is it versioned?
- On the single box, gate IGL on *system* load; once pensiero is pooled, gate on per-
  instance load. Which deployment do we target first?
