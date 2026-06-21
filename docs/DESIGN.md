# Design Document: Epistemic Graph Reasoning Engine (pensiero)

**Project:** pensiero
**Repository:** github.com/soundprediction/pensiero
**Package name:** pensiero
**Version:** 1.0  
**Status:** Draft  
**Date:** October 2023  

---

## 1. Executive Summary

The Epistemic Graph Reasoning Engine (EGRE) is a knowledge system designed to bridge the gap between unstructured text extractions and formal logical reasoning. Unlike traditional knowledge graphs that store static triples, EGRE manages an **Epistemic Base**—a protected set of axioms—and uses **Inductive Reasoning** to generalize observations, resolve conflicts, and identify knowledge gaps.

The system is built on **CozoDB** (a high-performance Datalog database) and implemented in **Golang**, leveraging algebraic concepts (Monoids) for context management and graph theory for modular optimization.

---

## 2. Design Goals

The architecture is driven by the following core objectives:

1.  **Protected Epistemic Base:** Maintain a distinct, immutable core of axioms that serve as the "source of truth," preventing corruption by derived or noisy data.
2.  **Context-Dependent Reasoning:** Store and reason with full conditional context (probabilities, temporal validity, constraints) using algebraic composition (Monoids).
3.  **Inductive Generalization:** Automatically identify patterns in noisy data to form generalized rules, cleaning the knowledge base through factorization and compression.
4.  **Ontology Integration:** Support the ingestion of formal ontologies, mapping raw text predicates to logical forms, and resolving conflicts between differing definitions.
5.  **Introspective Gap Analysis:** Analyze connectivity to the Epistemic Base to identify "Structural Holes" and missing information, enabling targeted human-in-the-loop solicitation.
6.  **Computational Efficiency:** Automatically segment the graph into modular structures (Hypernodes) to optimize query performance and reduce reasoning complexity.
7.  **Always-on Continuous Induction:** Maintain a background induction loop that constantly refines rules without blocking user interaction.
8.  **Deep Epistemic Thought Experiments:** Provide a sandbox for "What if" scenarios to evaluate the impact of hypothetical knowledge on current beliefs.

---

## 3. System Architecture

The system is divided into four logical layers:

### 3.1 Persistence Layer (CozoDB)
Handles storage, indexing, and recursive query processing.
- **Data Graph:** Stores instances (Subject, Predicate, Object) with context and epistemic status.
- **Meta Graph:** Stores ontology definitions, predicate properties, and logical rules.
- **Modular Structure:** Stores hierarchical abstractions (Hypernodes).

### 3.2 Logic Layer (Golang)
- **Epistemic Manager:** Enforces immutability of axioms; handles retraction and conflict resolution.
- **Context Algebra:** Implements Monoid interfaces for combining contextual metadata (confidence, conditions).
- **Inductive Engine:** Runs the background generalization loop (mining, factorization, crystallization) as a continuous service.
- **Thought Engine:** Orchestrates hypothetical simulations (thought experiments) in sandboxed context environments.

### 3.3 Ingestion Layer
- **Normalizer:** Maps raw NLP extractions to canonical predicates.
- **Ontology Loader:** Ingests class hierarchies and disjointness constraints.

### 3.4 Introspection Layer
- **Connectivity Resolver:** Calculates "Truth Energy" propagation from the Base to the periphery.
- **Gap Detector:** Identifies structural holes and dangling nodes for solicitation.

---

## 4. Theoretical Foundations

### 4.1 Context Monoids
Context is treated as an algebraic object.
- **Identity:** Empty context (Confidence=1.0).
- **Operation (`Mappend`):**
  - *Probability:* $P_{total} = P_a \times P_b$
  - *Conditions:* $C_{total} = C_a \cup C_b$
  - *Conflict:* If conditions contradict, $P_{total} \to 0$.

### 4.2 Categorical Framework

The system is grounded in the following category-theoretic definitions:

- **Graph Categories:**
    - `SyntacticGraph`: A category where objects are raw lexical tokens and morphisms are extracted text-level relations (e.g., from OpenIE).
    - `SemanticGraph`: A category where objects are canonical classes/entities and morphisms are logical predicates defined in the ontology.
- **The Normalization Functor ($F$):**
    - A functor $F: \text{SyntacticGraph} \to \text{SemanticGraph}$ that maps raw tokens to canonical entities and raw predicates to ontological relations. Functoriality ensures that if text states $A \xrightarrow{r1} B \xrightarrow{r2} C$, the semantic interpretation preserves the composition $F(r2 \circ r1) = F(r2) \circ F(r1)$.
- **Context Monoidal Category:**
    - Contexts form a symmetric monoidal category $(\mathcal{C}, \otimes, I)$, where $\otimes$ is the context composition (Monoid `Append`) and $I$ is the "Universal Truth" context (Confidence=1.0, no conditions).

### 4.3 The Inductive-Deductive Adjunction

The core logic of the Inductive Generalization Loop (IGL) is modeled as an adjunction between the category of Observations ($\mathcal{O}$) and the category of Rules ($\mathcal{R}$):

$$I: \mathcal{O} \rightleftarrows \mathcal{R} :D$$

- **Left Adjoint ($I$):** Induction. Maps a set of specific observations to a generalized rule (Crystallization).
- **Right Adjoint ($D$):** Deduction. Maps a rule back to the set of instances it explains (Instantiation).
- **Naturality:** The relationship $\text{hom}_{\mathcal{R}}(I(\text{obs}), \text{rule}) \cong \text{hom}_{\mathcal{O}}(\text{obs}, D(\text{rule}))$ formalizes that a rule is a valid generalization if and only if its instances cover the observations accurately.

### 4.4 Sheaf-Theoretic Consistency

To resolve conflicts across different modules or sources, we model the knowledge base as a **Sheaf** over a site of "Contextual Windows."
- **Local Consistency:** Data within a specific `Hypernode` or `Module` must be internally consistent (local sections).
- **Global Consistency:** When merging modules, we check for a "Global Section." Inconsistency (e.g., disjointness violations) is modeled as a failure of the glueing axiom, triggering the **Conflict Resolution** logic.

### 4.5 Categorical Thought Experiments

To "understand" knowledge beyond static facts, the system performs **Thought Experiments**—simulated reasoning in hypothetical categories.

- **Hypothetical Sections:** 
    A thought experiment is modeled as a section of a **Presheaf** $\mathcal{F}$ over a hypothetical site. We "lift" the current semantic graph into a sandbox state $\hat{\mathcal{G}}$ by injecting hypothetical morphisms (assumptions).
- **Slice Category Exploration:**
    "Thinking" about an entity $X$ involves examining the **Slice Category** $(\text{SemanticGraph} / X)$. By perturbing the objects and morphisms that map into $X$, the Thought Engine evaluates the stability and sensitivity of $X$'s epistemic status to hypothetical changes in its dependencies.
- **Counterfactual Functors:**
    Moving from a "Real" state to a "Hypothetical" state is a functorial mapping that preserves ontological constraints but relaxes connectivity requirements, allowing the system to explore "Structural Gaps" as if they were filled.

### 4.6 Datalog Grounding of Categorical Logic

The categorical definitions are implemented as recursive Datalog queries within the Go runtime:

- **Morphism Composition:** Logic rules $A \xrightarrow{f} B \xrightarrow{g} C$ are expressed as Datalog joins: `path(A, C) :- edge(A, B, f), edge(B, C, g)`.
- **Functorial Mapping:** The normalization functor $F$ is a set of Datalog rules mapping `syntactic_edge` to `semantic_edge` based on the `predicate_registry`.
- **Inductive Queries:** Identifying support for rules is performed via aggregation queries (counting supports) and crystallization is a write-back of new `meta_relation` records.
- **Thought Experiments:** Hypothetical sections are queried using **Context-Aware Rules**, where Datalog predicates are parameterized by a `ContextID` to isolate sandboxed knowledge from the Epistemic Base.

---

---

## 5. Data Model (Schema)

### 5.1 Primary Relations

#### `epistemic_edge`
The main store for knowledge facts.
```cozo
:create epistemic_edge {
    id: String,
    source: String,
    target: String,
    predicate: String,
    raw_predicate: String,
    status: String, # 'axiom', 'observation', 'induced', 'deduced'
    confidence: Float,
    context: Json,
    ^id
}
```

#### `predicate_registry`
Maps raw text to canonical logic.
```cozo
:create predicate_registry {
    raw: String,
    canonical: String,
    logical_class: String, # 'transitive', 'symmetric', etc.
    domain: String,
    range: String,
    ^raw
}
```

#### `ontology_disjoint`
Stores logical incompatibility for conflict detection.
```cozo
:create ontology_disjoint {
    class_a: String,
    class_b: String,
    ontology_source: String,
    ^class_a, ^class_b
}
```

### 5.2 Optimization Relations

#### `graph_modules`
Stores community detection results for segmentation.
```cozo
:create graph_modules {
    node_id: String,
    module_id: String,
    cohesion_score: Float,
    ^node_id
}
```

### 5.3 Introspection Relations

#### `node_epistemic_status`
Caches connectivity analysis results.
```cozo
:create node_epistemic_status {
    node_id: String,
    connectivity_score: Float,
    support_count: Int,
    gap_score: Float,
    ^node_id
}
```

### 5.4 Additional Relations

#### `meta_relation`
Holds induced/generalized rules produced by the Inductive Generalization Loop (IGL).
```cozo
:create meta_relation {
        id: String,
        head: String,           # canonical predicate or rule head
        body: Json,             # array of literals or conditions
        frequency: Int,         # how many supporting examples
        confidence: Float,      # rule confidence (0.0-1.0)
        provenance: Json,       # sources that supported induction
        created_at: String,
        ^id
}
```

#### `audit_log`
Immutable operational log for user/system actions (change history, human reviews).
```cozo
:create audit_log {
        entry_id: String,
        actor: String,         # user/service who performed action
        action: String,        # 'create', 'update', 'retract', 'review', etc.
        target_type: String,   # 'epistemic_edge', 'meta_relation', ...
        target_id: String,
        timestamp: String,     # ISO8601
        details: Json,
        ^entry_id
}
```

#### `archive_edges`
Holds edges moved out of the active store after compression or archival by IGL.
```cozo
:create archive_edges {
        id: String,
        original_edge: Json,
        archived_by: String,    # 'igl' or user id
        archived_at: String,     # ISO8601
        reason: String,
        ^id
}
```

#### `provenance` (relation / example)
Provenance can be embedded in `context` or stored separately for large traces.
```cozo
:create provenance {
        evidence_id: String,
        source_system: String,
        extractor: String,
        extraction_confidence: Float,
        evidence_ref: String,   # pointer to original artifact (url, doc id)
        timestamp: String,
        ^evidence_id
}
```

#### `context` JSON example
An example `context` payload showing expected fields and structure used throughout the system.
```json
{
    "confidence": 0.87,
    "valid_from": "2024-01-01T00:00:00Z",
    "valid_to": null,
    "conditions": [
        { "type": "location", "value": "US" },
        { "type": "tenor", "value": "public_statement" }
    ],
    "provenance": {
        "evidence_id": "ev-20240101-0001",
        "source_system": "ner-pipeline-v2",
        "extractor": "openie-v1",
        "extraction_confidence": 0.92,
        "timestamp": "2024-01-01T12:34:56Z"
    }
}
```

---

## 6. Core Algorithms

### 6.1 Inductive Generalization Loop (IGL)
A background process to clean and structure knowledge, modeled as the **Inductive-Deductive Adjoint Process**:
1.  **Canonicalization (Pre-Adjunction):** Clusters raw predicates by semantic similarity to prepare the observation category $\mathcal{O}$.
2.  **Crystallization (Left Adjoint $I$):** Promotion of patterns to formal rules in $\mathcal{R}$.
3.  **Instantiation (Right Adjoint $D$):** Verifies that induced rules appropriately cover the supporting edges (Context Factorization).
4.  **Compression:** Archives specific edges ($e \in \mathcal{O}$) that are fully explained by $D(\text{rule})$.

### 6.2 Conflict Resolution
1.  **Detection:** Check if an entity belongs to classes `A` and `B` where `ontology_disjoint(A, B)` exists.
2.  **Resolution:** 
    - If one edge is `axiom` and the other is `induced`: Retract `induced`.
    - If both are `axiom`: Flag for human review (System Logic Error).

### 6.3 Connectivity Resolution (Introspection)
Calculates how "rooted" a node is in the truth (Epistemic Base).
- **Categorical Interpretation:** Modeled as a **categorical flow** or a cumulative weight in a weighted category.
- **Logic:** Backward propagation of "Energy" from Axioms to Peripheral nodes.
- **Formula:** $Energy(Node) = \text{Colimit over predecessors } (\max(Energy(Pre) \times Confidence(Edge)))$.
- **Result:** Nodes with Score 0.0 are "Orphans" (disconnected from truth).

### 6.4 Gap Identification (Kan Extensions)
Identifies where knowledge is structurally incomplete.
- **Structural Hole:** Modeled as a missing composition in the category; where morphisms $A \to B$ and $B \to C$ exist, but $A \to C$ (the composite) is missing or has zero confidence despite support for a skip-link.
- **Dangling Node:** A high-connectivity node pointing to a low-connectivity node, representing a failure to extend the "Truth Functor" to the leaf.
- **Formal Theory:** Gaps are identified as failure cases for **Left Kan Extensions** ($\text{Lan}_p F$)—where we attempt to extend knowledge from a known sub-domain to a wider context.

### 6.5 The Thinking Loop (Continuous Understanding)

Unlike a request-response engine, Pensiero lives in a continuous **Thinking Loop**:

1.  **Background Induction:** The IGL process constantly scans for new patterns and promotes them to rules.
2.  **Proactive Simulation:** The Thought Engine periodically selects "low-connectivity" objects or "Gaps" and performs thought experiments (hypothetically filling the gap) to see if it stabilizes the surrounding graph modules.
3.  **Epistemic Alerting:** If a thought experiment reveals that a small hypothetical change (e.g., confirming one missing edge) would significantly increase the global connectivity of a module, the system proactively solicits that specific information via the **Introspection Layer**.

---

## 7. Optimization Strategy

### 7.1 Modularization (Hypernodes)
- **Detection:** Use Label Propagation (Louvain method) to find densely connected clusters.
- **Abstraction:** Represent a cluster as a `Hypernode`.
- **Query Pruning:** Queries check Module IDs first; if Subject and Object are in the same Module, search is constrained locally.

### 7.2 Segmented Execution
- Local queries operate on subgraphs.
- Global queries traverse `hyperedges` between modules.

---

## 8. Technology Stack

- **Core Language:** Golang (v1.21+)
- **Database:** CozoDB (Embedded via `github.com/cozodb/zoo`)
- **Reasoning Implementation:**
    - High-level orchestration, concurrency, and algebraic logic (Context Monoids) are implemented in **Golang**.
    - Recursive graph traversal, rule evaluation, and data manipulation are performed via **Datalog Queries** sent to CozoDB.
    - Grounding: Categorical morphisms are translated into Datalog rules where composition maps to query joins.

## 9. API & Transactional / Concurrency Model

This section defines the external API surface and the concurrency/transaction guarantees required to protect the Epistemic Base and ensure deterministic conflict resolution.

### 9.1 API Endpoints (REST examples)
- `POST /v1/edges` — ingest a single `epistemic_edge`. Body accepts `status`, `context`, and optional `provenance`.
- `POST /v1/edges/batch` — ingest stream (JSONL/ndjson) for high-throughput pipelines.
- `GET /v1/edges/{id}` — fetch edge with full `context` and `provenance`.
- `DELETE /v1/edges/{id}` — request retraction (subject to Epistemic Base protection rules).
- `POST /v1/retract` — transactional retraction request (accepts list of ids and justification).
- `GET /v1/nodes/{id}/introspect` — returns connectivity score, support examples, and detected gaps.
- `GET /v1/modules/{module_id}/query` — module-scoped query endpoint.
- `POST /v1/solicit/{gap_id}` — create a human task for gap resolution (returns `work_item_id`).
- `GET /v1/meta_relations` — list induced rules and their provenance/metrics.

API notes:
- Support both REST and gRPC adapters. Use JSON over REST for simple integration; gRPC for high-throughput internal services.
- Accepts `Content-Type: application/json` or `application/x-ndjson` for bulk ingest. Response codes follow standard semantics: `201` created, `202` accepted for async, `200` OK, `409` conflict when axioms protected.

### 9.2 Request/Response snippets
`POST /v1/edges` request body example:
```json
{
    "id": "e-0001",
    "source": "person:123",
    "target": "org:xyz",
    "predicate": "member_of",
    "raw_predicate": "is member of",
    "status": "observation",
    "confidence": 0.78,
    "context": { "confidence": 0.78, "conditions": [] },
    "provenance": { "evidence_id": "ev-0001" }
}
```

### 9.3 Authentication & Authorization
- Require OAuth2 (Bearer) or mTLS for service-to-service calls.
- RBAC roles: `ingester`, `analyst`, `axiom_admin`, `sys_admin`.
- Only `axiom_admin` may create/update/delete edges with `status: "axiom"`.

### 9.4 Transactional Rules & Axiom Protection
- Epistemic Base (edges with `status=='axiom'`) is protected by policy: modifications require `axiom_admin` claim and an audit entry.
- Retractions follow a two-step protocol:
    1. Submit `retract` request with justification (creates `audit_log` entry and optional `work_item`).
    2. If authorized and verified, system performs the retract within a single transactional operation that updates indices and introspection caches.
- For IGL-driven retractions (automatic): mark candidates, run a quarantined validation pass, then perform batched transactional retraction if policy thresholds are met.

### 9.5 Concurrency Model
- Use optimistic concurrency control for typical updates with an `etag` / `version` field on `epistemic_edge` and `meta_relation` records. Clients must supply `If-Match: <etag>` when updating.
- Use short-lived advisory locks for multi-step operations that must be serialized (e.g., module re-computation, IGL crystallization). Locks are scoped by resource type and id (`lock:igl:module:<id>`).
- **Query-while-Inducting Concurrency:**
    - Background loops (IGL, Thought Engine) operate on immutable **Snapshot Views** (MVCC).
    - User queries are served against the latest committed snapshot.
    - Write-backs from Thinking Loops are performed as atomic transactions, ensuring no partial rules are visible.
    - Deterministic tie-breakers (Axiom protection) prevent IGL background commits from corrupting manual user edits.
- Conflict detection during concurrent writes: deterministic tie-breaker order is applied (see Conflict Resolution policy): prefer higher `status` (`axiom` > `induced` > `observation`), then higher provenance score, then newer timestamp. Remaining ties create a `review` work item.

### 9.6 Retraction Safety & Audit
- All retractions, whether automated or manual, must write an `audit_log` entry containing actor, justification, and affected ids.
- Retractions of axioms must record `signed_by` and `axiom_version` to support rollback and forensic analysis.

### 9.7 Consistency Guarantees
- Strong consistency for single-edge CRUD operations and retractions affecting the Epistemic Base.
- Eventual consistency for derived indices, module assignments, and connectivity caches (updated by background jobs).

---

## 11. Implementation Roadmap

### Phase 1: Core Foundation (Go + CozoDB)
- [ ] Initialize CozoDB embedded instance in Go.
- [ ] Define the primary Datalog schema (`epistemic_edge`, `predicate_registry`).
- [ ] Implement atomic CRUD operations with Axiom protection rules.
- [ ] Set up the `audit_log` and MVCC-based transaction management.

### Phase 2: Ingestion & Functorial Mapping
- [ ] Implement the `Normalizer` to map raw text extractions to canonical predicates.
- [ ] Develop the `Ontology Loader` for class hierarchies and disjointness constraints.
- [ ] Verify Functoriality: ensure composition of text relations maps correctly to logical form.

### Phase 3: Context & Concurrency
- [ ] Implement the `ContextMonoid` interface for algebraic context composition.
- [ ] Integrate non-blocking background loops using MVCC snapshot isolation.
- [ ] Develop service adapters for high-throughput batch ingestion.

### Phase 4: Inductive Generalization (IGL)
- [ ] Implement background pattern mining to identify recurring relations.
- [ ] Model the Adjunction $I \dashv D$ (Induction/Deduction) in the Inductive Engine.
- [ ] Implement crystallization logic to promote patterns to formal `meta_relation` rules.
- [ ] Add compression algorithms to archive edges explained by induced rules.

### Phase 5: Introspection & Epistemic Flow
- [ ] Implement the Connectivity Scoring algorithm (Energy propagation).
- [ ] Develop the Gap Detection logic based on Left Kan Extensions.
- [ ] Create the Introspection API to expose node health and structural holes.

### Phase 6: Thought Engine (Sandbox Simulations)
- [ ] Implement sandboxed context environments for hypothetical simulations.
- [ ] Develop the Thinking Loop to explore Slice Categories $(\text{SemanticGraph} / X)$.
- [ ] Integrate Presheaf sections to model hypothetical knowledge exploration.

### Phase 7: Optimization & Modularization
- [ ] Implement community detection (Label Propagation) to identify `Hypernodes`.
- [ ] Develop the abstraction layer for segmenting queries at module boundaries.
- [ ] Optimize global query performance through Module ID pruning.

### Phase 8: Solicitation & Human-in-the-Loop
- [ ] Implement the proactive solicitation engine to create tasks for identified gaps.
- [ ] Build the review interface for human validation of Axiom-level conflicts.
- [ ] Create a feedback loop where human input refines the IGL induction weights.

---

## 10. Mathematical Appendix: Category Theory Glossary

To aid implementation, the following definitions are used throughout this design:

- **Category:** A collection of **Objects** (e.g., entity nodes) and **Morphisms** (e.g., predicates). Morphisms must be composable and associative, and every object must have an identity morphism.
- **Functor:** A mapping between categories that preserves structure (objects to objects, morphisms to morphisms). $F(g \circ f) = F(g) \circ F(f)$.
- **Adjunction ($L \dashv R$):** A relationship between two functors $L: \mathcal{C} \to \mathcal{D}$ and $R: \mathcal{D} \to \mathcal{C}$ such that there is a natural isomorphism between hom-sets $\mathcal{D}(L(X), Y) \cong \mathcal{C}(X, R(Y))$. In Pensiero, this formalizes the optimality of Induction.
- **Monoidal Category:** A category equipped with a bifunctor $\otimes$ (tensor product) and a unit object $I$. Used for combining Contexts.
- **Kan Extension:** The best possible approximation of a functor along another functor. Left Kan Extensions ($\text{Lan}$) model the generalization of specific facts to broader contexts; failures to find a valid extension point to "Structural Gaps."
- **Sheaf:** A tool for systematically tracking local data attached to open sets of a topological space (or a site). It ensures that local sections can be "glued" into a global section if they agree on overlaps.
- **Presheaf:** A functor $F: \mathcal{C}^{op} \to \text{Set}$. Used to model hypothetical sections of knowledge over simulated contexts.
- **Slice Category ($\mathcal{C} / X$):** A category of objects "over" a fixed object $X$. Used to evaluate the implications of incoming morphisms on a specific node's epistemic status.
```