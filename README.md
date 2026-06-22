# pensiero

**A thinking layer for knowledge bases.**

Most knowledge graphs *store* facts. `pensiero` *reasons* over them. It sits on top of an existing graph and answers the questions a store cannot: *Does this follow from what we know? Does it contradict what we know? And if neither — what's missing?*

It is the companion to [**predicato**](https://github.com/soundprediction/predicato): predicato *builds and remembers* the knowledge base — a bi-temporal graph with hybrid retrieval — and pensiero *thinks* over it. Point pensiero at a predicato graph and you get entailment, contradiction detection, and gap analysis on top of what predicato has already ingested. Neither requires the other, but together they form an ingest-remember-reason loop.

The design draws on the language of **category theory**. A knowledge base is treated not as a bag of triples but as a category: entities are **objects**, typed relations are **morphisms**, and reasoning is the **composition** of those morphisms. Generalization — the heart of the engine — is a structure-preserving **functor** that lifts concrete facts onto an abstract backbone. This framing is not decoration; it is how the code is organized, down to the `Claim.Predicate` that is "normalized via the registry (functor *F*)."

```go
verdict, _ := reasoner.Entails(ctx, reasoning.Claim{
    Subject:   "warfarin",
    Predicate: "contraindicated_in",
    Object:    "pregnancy",
})
// → entailed | contradicted | unsupported   (+ a proof you can read)
```

---

## The categorical picture

| Category theory | In `pensiero` | Code |
| --- | --- | --- |
| **Objects** | Entities in the knowledge base | nodes in the graph |
| **Morphisms** | Typed, directed relations (predicates) | reified `(s)-[:RELATES_TO]->(r)-[:RELATES_TO]->(o)` |
| **A functor on arrows** | The predicate registry normalizes raw text predicates to a canonical signature | `PredicateRegistry.Canonical` |
| **Composition** | Chaining relations into a derivation (`a∘b ⇒ c`) | `CompositionRule{First, Second, Result}`, `Derive` |
| **Arrow laws** | Transitive / symmetric / functional / inverse predicates | `PredicateMeta` characteristics |
| **Disjointness (no such arrow)** | Ontology conflicts → contradiction | `DisjointPair`, `Contradicts` |
| **A structure-preserving functor between categories** | Generalization: lift a concrete graph onto its abstract backbone | `generalization.Build` |

A **proof** is a path of composable morphisms from a source object to a target. An **entailment** is the existence of such a path. A **contradiction** is a disjointness constraint that the path would violate. A **knowledge gap** is the honest third answer: no supporting composition *and* no conflict — the engine knows that it does not know.

---

## What it does

- **Entailment** — `Entails` decides whether a claim is symbolically supported, returning the best supporting derivation with per-hop confidence decay.
- **Contradiction detection** — `Contradicts` reports ontology-disjointness conflicts (e.g. a relation asserting the inverse of a known disjoint pair), so "logically inconsistent with the knowledge base" is distinguished from "merely unverified."
- **Derivation** — `Derive` returns ranked multi-hop proof paths between entities, honoring predicate algebra (transitivity, symmetry, inverses) and composition rules.
- **Generalization** — `generalization.Build` lifts a base graph into a small **generalization subgraph** (the abstract backbone) along a taxonomy, so most reasoning happens between a few general nodes rather than across millions of concrete ones.
- **Gap analysis** — when a claim is neither entailed nor contradicted, the `unsupported` verdict marks a structural hole for targeted follow-up.

The reasoner is domain-agnostic. It ships a `DefaultGeneralRegistry` for ordinary `is-a`/`part-of`/`causes` ontologies and a `DefaultMedicalRegistry` as one worked example; you can supply your own predicate signature as JSON.

---

## Architecture

```
pkg/reasoning       objects, morphisms, proofs — the reasoning core
  ├─ Reasoner        Derive · Entails · Contradicts (pluggable backends)
  ├─ PredicateRegistry   the categorical signature: predicate algebra,
  │                       composition rules, disjointness constraints
  └─ formula/refrange    quantitative primitives (units, comparisons, ranges)
pkg/generalization  the lifting functor: build the abstract backbone (G_g)
pkg/connector       graph driver adapters
pkg/db · pkg/models shared storage + types
cmd/pensiero        CLI: build-generalization, serve
extension/          native in-graph reasoning extension (Ladybug backend)
```

Backends register themselves with `reasoning.Register`, so the same `Reasoner` interface can be served by an in-process graph driver or a native graph-database extension (`REASON_ENTAILS` / `REASON_CONTRADICTS`) without changing callers.

---

## Quick start

### Library

```go
import "github.com/soundprediction/pensiero/pkg/reasoning"

reg := reasoning.DefaultGeneralRegistry()
r, err := reasoning.New("native", graph, reg, reasoning.Config{})
if err != nil { /* ... */ }

res, _ := r.Entails(ctx, reasoning.Claim{
    Subject: "aspirin", Predicate: "treats", Object: "headache",
})
fmt.Println(res.Verdict, res.Confidence)
for _, step := range res.Best.Steps {
    fmt.Printf("  %s --%s--> %s\n", step.Source, step.Predicate, step.Target)
}
```

### CLI — build the generalization backbone

```bash
go run ./cmd/pensiero build-generalization \
  --source   path/to/graph \
  --scope    cardiovascular \
  --out      path/to/cardiovascular.g_g \
  --taxonomic-predicates IS_PARENT_OF --taxonomic-direction parent-to-child \
  --predicates HAS_PHENOTYPE,CAUSES,TREATS,PREVENTS,CONTRAINDICATED \
  --registry general
```

This reads a concrete knowledge graph, lifts shared relations onto their generalized parents (the functor), and writes a compact generalization graph that the reasoner queries at run time.

---

## Design documents

The longer-form design lives in [`docs/`](docs/):

- [`DESIGN.md`](docs/DESIGN.md) — the epistemic base, inductive generalization, and gap analysis.
- [`GENERALIZATION_REASONING.md`](docs/GENERALIZATION_REASONING.md) — why most reasoning should happen between generalization nodes.
- [`SYMBOLIC_GRAPH_LOGIC.md`](docs/SYMBOLIC_GRAPH_LOGIC.md) — the deductive, in-graph multi-hop rule classes.
- [`GRPC_SERVER.md`](docs/GRPC_SERVER.md) — serve the reasoner over gRPC (separate process / machine, pooled for horizontal scale).

## Requirements

- Go 1.25.5+

## License

MIT — see [`LICENSE`](LICENSE).
