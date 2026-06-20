# Design Objective: Generalization-Subgraph Reasoning

**Project:** pensiero
**Status:** Governing design principle (drives the symbolic graph logic and the IGL)

> **Objective.** pensiero should actively *seek generalization* so that **most reasoning
> happens between generalization nodes**. The generalization nodes form a small
> **subgraph** (the abstract backbone); most concrete nodes connect *into* that
> subgraph; and multi-hop reasoning steps run **mainly through the generalizations**.
> Because the generalization subgraph has far fewer nodes than the full graph,
> reasoning is faster. **Reducing the reasoning surface to this subgraph is the point.**

This is the principled form of an empirical finding: multi-hop traversal on the full
dense medical graph (~224k nodes / millions of edges) is slow even when anchored and
bounded (a 1-logical-hop path query ran >40s), while the same reasoning on a small
clinical graph (~15k nodes) is fast. Speed scales with the number of nodes traversed —
so we make the reasoning surface small *by construction*.

---

## 1. Two strata

The knowledge graph is viewed as two layers:

- **Concrete layer** `G_c` — specific entities and observations (individual diseases,
  findings, drugs, instances from ingestion). Large, dense, noisy.
- **Generalization subgraph** `G_g` — abstract nodes: classes, concepts, induced
  generalizations, and **hypernodes** (DESIGN.md §7.1). Small, curated, stable.

**Generalization edges** connect the two strata and within `G_g` — exactly the general
predicate primitives already defined: `is_a` / `subsumes`, `instance_of`, `part_of` /
`located_in`, `same_as` / `equivalent_to`. A concrete node "connects to the subgraph"
iff it has a (possibly multi-hop) generalization edge into some node of `G_g`.

**Design pressure (the two forces the IGL balances):**
- **Coverage** — *most* concrete nodes should connect to `G_g` (so reasoning can lift
  almost any query into the backbone). Target: maximize `|{c ∈ G_c : c ⤳ G_g}| / |G_c|`.
- **Compression** — `|G_g|` should be *small* (so reasoning is fast). Target: minimize
  `|G_g|` subject to the coverage and soundness constraints.

A good generalization subgraph is the smallest abstract backbone that almost every
concrete node hangs off of.

---

## 2. Reasoning = lift → reason-in-`G_g` → lower

Multi-hop derivation between two concrete nodes does **not** traverse `G_c`. It:

1. **Lift** — map each query endpoint (a concrete node) to its generalization(s) via the
   `is_a`/`instance_of` closure (a short, bounded climb into `G_g`). This is the only
   step that touches the concrete layer, and only locally around the endpoints.
2. **Reason** — perform the full multi-hop symbolic derivation (composition,
   transitivity, sub-property, disjointness — SYMBOLIC_GRAPH_LOGIC.md §2) **entirely
   within `G_g`**, between the lifted generalization nodes. This is where almost all
   hops happen, and `G_g` is small, so it is fast.
3. **Lower** — instantiate the abstract proof back to the concrete endpoints; attach the
   lift/lower steps to the proof path so the explanation remains end-to-end and
   confidence composes across the whole chain (Context Monoid).

A claim `Entails(c1, p, c2)` becomes: `lift(c1) ⤳ G_g`, derive `p` over `G_g`,
`lower → c2`. The expensive middle runs on the small graph.

**Why faster (node-count argument).** Bounded multi-hop search cost grows with the
branching factor and node count of the traversed graph. Restricting the middle steps to
`G_g` replaces traversal over `|G_c|` dense nodes with traversal over `|G_g| ≪ |G_c|`
abstract nodes; the only `G_c` work is the two short local lifts. Empirically this is the
difference between the slow full-canonical traversal and the fast small-graph traversal —
now guaranteed structurally rather than per-query.

---

## 3. How the generalization subgraph is built and kept (IGL)

The Inductive Generalization Loop (DESIGN.md §6.1) is the mechanism that *creates*
`G_g`, with the coverage/compression objective as its loss:

- **Crystallization** — when many concrete nodes share a pattern (same neighbors under a
  predicate), induce a generalization node and link them to it (`is_a`/`instance_of`),
  and lift their shared edges to the generalization (e.g. instead of N diseases each
  `has_symptom fatigue`, induce a class with the shared phenotype). This *both* raises
  coverage (the N nodes now connect to `G_g`) *and* compresses (N edges → 1 at the
  generalization, the rest become derivable by `is_a ∘ has_symptom → has_symptom`).
- **Factorization / compression** — replace redundant concrete structure with a
  hypernode + membership edges; archive the absorbed concrete edges (DESIGN.md
  `archive_edges`). The generalization carries the reasoning; concretes are recoverable
  by lowering.
- **Dangling / structural-hole detection** — concrete nodes that do *not* connect to
  `G_g` are the coverage gaps (DESIGN.md §6.4); the loop prioritizes inducing
  generalizations that attach them, or flags them for solicitation.
- **Always-on** — the loop runs continuously so `G_g` keeps tracking the concrete layer
  as new data is ingested.

The composition rules over generalizations (`is_a ∘ has_symptom → has_symptom`, etc.)
are what make reasoning *correct* when it runs on the backbone: a generalization's
properties soundly transfer to (and from) its members.

---

## 4. Implications for the symbolic graph logic + the ladybug extension

- The **"restricted canonical"** we discussed *is* `G_g`: build the generalization
  subgraph as a small, fast graph and run the reasoning extension over **it**, not the
  63.8 GB canonical. The reasoning extension's anchored bounded traversal then operates
  on a graph small enough to be fast (the §2 cost argument), with the concrete canonical
  only consulted for the local lift/lower at the endpoints.
- **Materialization** — the lift closures (`instance_of`/`is_a` per concrete node → its
  generalizations) and the within-`G_g` transitive closures are precomputed/cached
  (DESIGN.md `derived_closure`), so lift is O(1) and the middle reasoning is a small,
  warm graph.
- **Grounding for humn DDx** — a finding grounds a diagnosis when, after lifting both to
  `G_g`, a derivation connects them through generalizations (e.g. finding → its symptom
  class → diagnosis class → diagnosis). This is also why it sidesteps the current
  over-grounding noise: reasoning runs on the clean abstract backbone, not the noisy
  concrete edges (drug side-effects, cross-condition molecular edges) that pollute the
  dense layer.

---

## 5. Invariants / success criteria

1. **Coverage:** the fraction of concrete nodes connected to `G_g` stays high (target
   ≫ 50%; ideally most), tracked by the IGL.
2. **Compression:** `|G_g| ≪ |G_c|` (orders of magnitude), so reasoning is fast.
3. **Soundness:** every generalization edge and composition rule transfers properties
   correctly; lowering never invents concrete facts.
4. **Explainability:** every derivation's proof includes its lift and lower steps, so an
   abstract derivation is always traceable to the concrete entities it concerns.
5. **Speed:** multi-hop reasoning latency scales with `|G_g|`, not `|G_c|` — the design
   objective, made measurable.

---

## 6. Implementation: representing & maintaining `G_g`

The representation must let us (a) **search/match** the special nodes/edges that belong
to the subgraph, and (b) **traverse only them** so reasoning is actually fast, and (c)
**maintain** the subgraph as the IGL evolves it.

### 6.1 Type vs. attribute — why types (dedicated tables)

A first instinct is to *tag* generalization membership with an attribute
(`Entity.tier = 'generalization'`, or a `subgraph_id` property) and filter in queries.
**That does not deliver the speedup.** A variable-length path
`(a)-[:RELATES_TO*2..K]-(b) WHERE all(n IN nodes(p) WHERE n.tier='generalization')`
still expands over the *dense* `RELATES_TO` structure and filters afterward — the
traversal cost is unchanged. Filtering reduces the *result*, not the *work*.

To make traversal small, the subgraph must be its **own typed tables** that the engine
scans natively. So generalization membership is a **type (node/rel table)**, not an
attribute. (A `subgraph_id` attribute is still used, but only as a secondary key to
allow *multiple* named backbones within the typed layer — see 6.4.)

### 6.2 Schema — dedicated tables for `G_g`, within the same DB

Keep the concrete layer (`Entity`, reified `RelatesToNode_`, `RELATES_TO`) untouched, and
add a parallel **typed** generalization layer plus lift/lower edges:

```cypher
-- Generalization NODES (the small backbone)
CREATE NODE TABLE Concept(
    uuid STRING PRIMARY KEY,
    name STRING,
    kind STRING,             -- 'class' | 'concept' | 'hypernode'
    subgraph_id STRING,      -- named backbone; default 'core'
    summary STRING,
    name_embedding FLOAT[],
    support_count INT64      -- # concrete members (IGL bookkeeping)
);

-- Generalization EDGES among concepts (reified like RELATES_TO, but its OWN tables,
-- so a path query over them scans only the small backbone).
CREATE NODE TABLE GenRel_(uuid STRING PRIMARY KEY, name STRING, fact STRING,
                          confidence DOUBLE, subgraph_id STRING);
CREATE REL TABLE GEN(FROM Concept TO GenRel_, FROM GenRel_ TO Concept);

-- LIFT / LOWER edges: concrete Entity <-> Concept (is_a / instance_of into the backbone)
CREATE REL TABLE ABSTRACTS_TO(FROM Entity TO Concept, predicate STRING, weight DOUBLE);
```

### 6.3 The three reasoning steps map 1:1 to the schema

```cypher
-- LIFT (local, bounded; the only concrete-layer touch)
MATCH (e:Entity)-[:ABSTRACTS_TO]->(c:Concept)
WHERE e.uuid = $entityUuid  RETURN c.uuid

-- REASON entirely within G_g — scans ONLY Concept/GenRel_/GEN (small => fast)
MATCH (a:Concept {uuid:$fromConcept})
MATCH p = (a)-[:GEN* 2 .. $physMax]-(b:Concept {uuid:$toConcept})
RETURN [predicates from GenRel_ along p], length(p)/2 AS hops

-- LOWER
MATCH (c:Concept)<-[:ABSTRACTS_TO]-(e:Entity)
WHERE c.uuid = $conceptUuid  RETURN e.uuid
```

Because the middle `MATCH ... (:Concept)-[:GEN*..]-(:Concept)` is over the dedicated
tables, Kuzu never walks the dense `Entity/RELATES_TO` graph during reasoning. Cost ∝
`|Concept|`, exactly the objective.

### 6.4 Multiple / named subgraphs

`subgraph_id` on `Concept`/`GenRel_` lets several backbones coexist in the same tables —
e.g. a `core` clinical backbone plus per-domain or experimental ones, or sandboxed
backbones for thought experiments (DESIGN.md §4.5). Reasoning restricts with
`WHERE c.subgraph_id = $sg`. Membership is still *typed* (the Concept table); the
attribute only selects *which* backbone.

### 6.5 Within-DB typed tables vs. a separate `G_g` database

| | Typed tables, same DB (**recommended**) | Separate `G_g` database |
|---|---|---|
| Reasoning speed | Fast — traversal scans only `Concept/GEN` | Fast — open the small DB |
| Lift/lower | Single query across `ABSTRACTS_TO` | **Cross-DB**: needs a concept→entity id map; awkward, two opens |
| Maintenance | One store; IGL writes `Concept/GEN/ABSTRACTS_TO` atomically | Must sync two stores; projection step |
| Shipping/versioning `G_g` | Harder to ship alone | Easy to ship/snapshot independently |
| Ladybug single-writer lock | One lock | Two DBs / locks to coordinate |

**Recommendation: dedicated typed tables in the same DB.** It gives native small-graph
traversal *and* single-store, atomic maintenance, and keeps lift/lower a single query.
A separate `G_g` DB is the fallback only if we need to ship/version the backbone
independently or run reasoning on a host that can't open the full concrete graph — in
which case `Concept` carries `canonical_id` refs so lower can re-link to the concrete
graph by id.

### 6.6 Maintenance (IGL writes)

The IGL owns the generalization layer:
- **Crystallize:** create a `Concept`, link members `Entity-[:ABSTRACTS_TO]->Concept`,
  and lift shared concrete edges to `GenRel_`/`GEN`; bump `support_count`.
- **Compress:** when a concept fully covers a set of concrete edges, archive those
  concrete edges (DESIGN.md `archive_edges`); the `is_a ∘ R → R` composition rules
  recover them by lowering.
- **Coverage repair:** find `Entity` with no `ABSTRACTS_TO` path (dangling) and induce or
  attach a concept.
- **Cache:** materialize the within-`G_g` transitive closures and each concrete node's
  lift set into `derived_closure` so lift is O(1) and the backbone stays warm.

All writes touch only the `Concept/GenRel_/GEN/ABSTRACTS_TO` tables and `archive_edges` —
never the live concrete `RELATES_TO`, so the concrete graph and the reasoning backbone
evolve independently.

---

---

## 7. Decision: communities ARE one kind of generalization (unify under `Concept`)

Many special nodes we'd induce are exactly the **communities** the system already builds
(ladybug `Community` table: `uuid, name, group_id, name_embedding, summary`; populated by
`build-communities`, which clusters a diagnosis with its connected
symptoms/signs/risk-factors/treatments and gives it an embedding + summary). A community
*is* a generalization — it abstracts a diagnosis and its presentation into one
summarizable, embeddable unit. Crucially, **community search (matching a presentation to
communities) is exactly the `lift` step** of §2.

But communities are **not the whole** generalization backbone: they are diagnosis-centered
*presentation clusters* (flat, overlapping, retrieval-oriented), whereas reasoning also
needs **taxonomic** generalizations (an `is_a` hierarchy between concepts) and
**inter-generalization edges** for multi-hop composition — which communities don't carry.

**Decision — a single generalization node type `Concept` with a `kind` discriminator,
of which `community` is one kind.** Do *not* build a wholly separate parallel system, and
do *not* force everything into the existing `Community` table either:

- `kind = "community"` — diagnosis-centered presentation cluster. **Reuse the existing
  community machinery wholesale:** its embedding is the **lift vector** (community search =
  lift-by-similarity), its summary is the concept summary, and its members become the
  `ABSTRACTS_TO` lift edges (finding → community). `build-communities` is thus a
  *crystallization* mechanism (IGL §3) that produces `kind=community` concepts.
- `kind = "class"` — taxonomic generalization in the `is_a`/`subsumes` hierarchy. These +
  the `GEN` edges between concepts form the **hierarchical backbone** that multi-hop
  reasoning (composition, transitivity, subsumption) runs over. Communities link into this
  via `is_a` (a diagnosis community `is_a` its disease class).
- `kind = "hypernode"` — IGL compression modules (DESIGN.md §7.1).

So: **reuse community *aspects* (embedding, summary, membership, the build pipeline, and
community-search-as-lift) by modeling community as a *subtype* of the unified `Concept`
generalization node** — rather than a separate node type or overloading `Community`
directly. Two lift paths fall out naturally: lift-by-similarity (embedding → community
concept) and lift-by-subsumption (`is_a` climb → class concept); reasoning then runs over
`Concept/GEN` regardless of kind, and `lower` returns to concretes via `ABSTRACTS_TO`.

Migration: existing `Community` rows + the `communities.jsonl` index map onto
`Concept{kind:'community'}` (same uuid/embedding/summary; members → `ABSTRACTS_TO`); the
IGL adds `kind:'class'` concepts + `GEN` `is_a` edges to give the backbone its hierarchy.
(`codex` to sanity-check the `Community`→`Concept` field mapping and whether to keep
`Community` as a view over `Concept{kind:'community'}` for backward compat.)

---

---

## 8. Search strategy: avoid BFS — hybrid + guided + precomputed

Blind breadth-first traversal over the dense concrete graph is the thing that was slow
(it expands every neighbor before filtering). We avoid BFS wherever possible:

### 8.1 `lift` is a hybrid-search JUMP, not a traversal

Finding the generalization nodes a query connects to is the dominant "search for nodes to
connect to" operation — and it must **not** be done by walking the graph. Instead, jump
straight to candidate `Concept`s by **hybrid search**, then keep only the strong ones:

- **Vector / ANN** — embed the query (or the concrete endpoint) and HNSW-search the
  `Concept.name_embedding` index (ladybug `vector` extension). O(log N) into the backbone;
  this is exactly the existing community-search-as-lift, generalized to the `Concept`
  layer.
- **Keyword / BM25** — FTS over `Concept.name`/`summary` (ladybug `fts` extension) for
  exact/lexical hits the embedding misses.
- **Structural** — the concrete endpoint's own `ABSTRACTS_TO` edges (its declared
  generalizations) — a direct index lookup, not a search.
- **Fuse with RRF** (the pattern already used in `QueryForContentTraced`) → top-k lift
  targets. No frontier expansion at any point.

So lift = ANN ⊕ BM25 ⊕ direct-edge, fused — a constant-ish lookup, never BFS.

### 8.2 Reasoning over the backbone: precompute, then guide

Once lifted, the multi-hop derivation runs on the small `Concept/GEN` backbone (§2/§6).
Even there, prefer non-BFS:

- **Precomputed closures (the common case).** Materialize the `is_a`/`subsumes`/`part_of`
  transitive closures of the backbone into `derived_closure` (DESIGN.md). Then
  **subsumption / reachability / membership are O(1) index lookups**, not searches — most
  "reasoning" never traverses at all. The backbone is small and stable, so the closure is
  cheap to maintain.
- **When a path must actually be searched** (cross-predicate composition not covered by a
  closure): use **bidirectional, best-first (A\*) search guided by embedding distance to
  the target** — expand the most promising node first (heuristic = cosine to the goal
  concept), meet in the middle from both endpoints. This visits far fewer nodes than
  one-sided BFS and terminates early on the first/best proof.
- **Confidence-pruned** (§6): drop a frontier node once its composed confidence falls
  below `$minConf` — a strong cutoff on the dense tail.
- **Landmark / 2-hop labels** (optional, for a larger backbone): precompute distance
  labels on `Concept` so connectivity is answered without traversal.

### 8.3 Net

`lift` (hybrid jump) + precomputed backbone closures answer most queries with **no graph
walk at all**; only genuinely novel cross-predicate compositions trigger a search, and
that search is bidirectional/best-first/pruned on a small graph — never blind BFS over the
concrete layer. This is what makes "search for nodes to connect to" fast, and it reuses
the system's existing hybrid-search (ANN + BM25 + RRF) and community-embedding machinery.

---

---

## 9. Operational modes: continuous generalization daemon + live query service

pensiero runs in three modes; the daemon is the primary one.

- **`pensiero generalize` (one-shot / batch):** open a graph, run the IGL once to
  induce/refresh the generalization subgraph `G_g`, **persist it into the graph's typed
  `Concept/GEN/ABSTRACTS_TO` tables** (plus an optional snapshot/export), and exit. The
  generalizations are now available *for use later* by any subsequent run.
- **`pensiero serve` (daemon + service — primary):** a single long-running process that
  (a) holds the graph open, (b) runs the **always-on generalization loop** in the
  background (DESIGN.md §2.7), continuously generalizing newly-ingested facts into `G_g`,
  and (c) **serves live reasoning queries** (`/v1/reason/verify`, `/v1/reason/derive`,
  lift/lower) against the current backbone — concurrently with the loop.
- **Embedded library:** the same engine linked into a host (e.g. humn) when it already
  owns the graph handle.

### 9.1 Why a daemon resolves the single-writer lock

Ladybug is single-writer: only one process may open the DB. So make the **daemon the sole
owner** — it opens the graph once, the IGL is the only writer, and *clients never open the
DB*; they query the daemon over the service API. This dissolves the earlier
co-open/lock problem (humn + pensiero + builds contending for the file): there is exactly
one writer and the graph is reached only through the service (or the embedded handle).

### 9.2 Concurrency model (loop writes, queries read)

- **Single writer = the IGL.** All generalization writes (`Concept`, `GEN`, `GenRel_`,
  `ABSTRACTS_TO`, `archive_edges`) go through the loop. The concrete `RELATES_TO` graph is
  never mutated by reasoning, so writers touch only the backbone layer.
- **Queries are snapshot readers.** Reasoning runs against a consistent MVCC snapshot
  (DESIGN.md §9.5) and never blocks on the loop; the loop commits **incrementally** in
  bounded batches so a long generalization pass never stalls live queries.
- **Versioned warm state.** The hot artifacts queries depend on — the ANN index over
  `Concept` embeddings (for hybrid `lift`, §8.1), the materialized `is_a`/`part_of`
  closures, and the per-entity lift caches (`derived_closure`) — are rebuilt by the loop
  and swapped in copy-on-write/versioned, so a query always sees a coherent
  (closure, index, backbone) triple.

### 9.3 The loop (incremental, always-on)

Each tick, bounded so it yields to queries:
1. Read facts ingested since the last watermark (incremental, not a full rescan).
2. **Crystallize / compress / coverage-repair** (IGL §3) → upsert `Concept`/`GEN`/
   `ABSTRACTS_TO`; archive absorbed concrete edges.
3. Update the ANN index for new/changed `Concept` embeddings; recompute only the closures
   touched by the change.
4. Advance the watermark; commit; publish the new warm-state version.

So `G_g` keeps tracking the concrete layer as data arrives, and the live service always
answers against the best-available backbone — generalizations improve over time without
downtime.

### 9.4 How humn consumes it

humn's DDx verifier calls the daemon's `/v1/reason/verify` (or links the embedded engine)
for in-graph reasoning — lift (hybrid jump) → reason over `G_g` → lower → proof. The
daemon's continuous generalization means coverage/quality improve in the background while
humn keeps querying the same endpoint.

---

*This document governs how reasoning is structured in pensiero: induce generalizations,
keep most nodes attached to the small generalization subgraph, and do the reasoning
there.*
