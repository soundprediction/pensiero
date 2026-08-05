# Generalization — taxonomy edge direction

## ADDED Requirements

### Requirement: Taxonomy direction MUST be derived from an external signal, never from storage

Stored orientation of taxonomy predicates (any predicate aliasing to `subsumes`/`is_a`,
including `IS_PARENT_OF`) SHALL be treated as carrying no information. Measured on the deployed
corpus, adjudicable unidirectional edges are stored 51.8% parent→child and 48.2% backwards.

Direction SHALL be derived per entity pair from an external signal. The stored orientation SHALL
NOT be used as evidence, as a tie-breaker, or as a default.

Each pair SHALL be assigned exactly one tier:

- `oriented` — an external signal determined the parent and the child.
- `undetermined` — no signal could determine direction.

There SHALL NOT be a tier that accepts an edge on the basis of its stored orientation.

#### Scenario: Stored direction alone never orients an edge

- **GIVEN** `hereditary connective tissue disorder IS_PARENT_OF Gordon syndrome` with no reverse
  edge, no ontology match for either endpoint, and no lexical signal
- **WHEN** direction is derived
- **THEN** the pair SHALL be tiered `undetermined`
- **AND** it SHALL NOT be emitted, regardless of being unidirectional in the source

#### Scenario: Ontology alignment determines direction against the stored orientation

- **GIVEN** a pair whose endpoints both match ontology terms
- **AND** the ontology places `kinase activity` as an ancestor of `AMP-thymidine kinase activity`
- **AND** the graph stores only `AMP-thymidine kinase activity IS_PARENT_OF kinase activity`
- **WHEN** direction is derived
- **THEN** the emitted edge SHALL be `kinase activity` → `AMP-thymidine kinase activity`
- **AND** the pair SHALL be tiered `oriented`
- **AND** the stored orientation SHALL NOT override the ontology

#### Scenario: Bidirectional pair collapses to a single oriented edge

- **GIVEN** both `A IS_PARENT_OF B` and `B IS_PARENT_OF A` are present
- **AND** a signal determines that A is the parent
- **WHEN** direction is derived
- **THEN** exactly one edge SHALL be emitted, A → B
- **AND** the opposing edge SHALL be dropped

#### Scenario: Bidirectional pair with no signal is dropped entirely

- **GIVEN** both orientations are present and no signal can adjudicate the pair
- **WHEN** direction is derived
- **THEN** the pair SHALL be tiered `undetermined` and neither edge SHALL be emitted

#### Scenario: Self-loop is never orientable

- **GIVEN** an edge whose head and tail resolve to the same entity name
- **WHEN** direction is derived
- **THEN** the pair SHALL be tiered `undetermined` and dropped

#### Scenario: Token-subset handles mid-string qualifiers

- **GIVEN** `regulation of T cell selection` and
  `regulation of positive thymic T cell selection`
- **WHEN** the lexical signal is evaluated
- **THEN** it SHALL determine the former is the parent
- **AND** the implementation SHALL NOT rely on substring containment, which fails on this pair

---

### Requirement: Lifting MUST fail closed

Generalization SHALL lift relations ONLY across pairs tiered `oriented`. `undetermined` pairs
SHALL NOT be traversed, and no configuration option SHALL enable traversing them.

The implementation SHALL NOT select an orientation by tie-break heuristic, insertion order, coin
flip, or engine row order.

#### Scenario: Undetermined pairs are never traversed

- **WHEN** generalization is built
- **THEN** no relation SHALL be lifted across a pair tiered `undetermined`
- **AND** no flag SHALL exist that permits it

#### Scenario: Signal precedence is authoritative-first

- **GIVEN** a pair that both an ontology and the lexical signal can orient
- **AND** the two disagree
- **WHEN** direction is derived
- **THEN** the ontology's direction SHALL win
- **AND** the disagreement SHALL be counted and reported

#### Scenario: Orientation is deterministic across runs

- **GIVEN** the same source graph
- **WHEN** direction classification runs twice
- **THEN** both runs SHALL produce identical tier assignments and identical retained edges
- **AND** the result SHALL NOT depend on the order rows are returned by the storage engine

---

### Requirement: Every direction repair MUST emit a measurable report

Any run that derives taxonomy direction SHALL emit a machine-readable report containing, per
predicate: total pairs, counts for `oriented` and `undetermined`, a breakdown of `oriented` by
which signal determined it, the count of pairs whose derived direction CONTRADICTS the stored
orientation, and the count of signal disagreements. Counts SHALL sum to the total.

The report SHALL include a deterministic, reproducible sample sufficient for a human to check
the signal's accuracy by hand.

#### Scenario: Report accounts for every pair

- **WHEN** a derivation run completes over a graph with N distinct taxonomy pairs
- **THEN** `oriented + undetermined` SHALL equal N
- **AND** the per-signal breakdown of `oriented` SHALL sum to `oriented`

#### Scenario: Report states measured precision, not assumed precision

- **WHEN** a signal other than ontology alignment is used
- **THEN** the report SHALL state that signal's precision measured against the
  ontology-adjudicable subset
- **AND** a signal whose precision has not been measured SHALL NOT be enabled by default

#### Scenario: Report is emitted before any derived graph is written

- **WHEN** a derivation run is invoked
- **THEN** the report SHALL be produced even if zero pairs qualify for lifting
- **AND** a run in which every pair is `undetermined` SHALL report that outcome and exit
  non-zero rather than writing an empty derived graph silently

#### Scenario: Sample is hand-checkable

- **WHEN** a report is generated
- **THEN** it SHALL include at least 20 sampled pairs per tier (or all of them, if fewer)
- **AND** each sample SHALL carry both endpoint names, the stored orientation, the derived
  orientation, the assigned tier, and the signal that determined it

---

### Requirement: Source graphs MUST NOT be modified

Direction classification and repair SHALL open every source graph read-only and SHALL write any
result to a new destination path. The implementation SHALL NOT open a source graph read-write.

This is a data-integrity requirement, not a preference: a ladybug 0.19 read-write open
irreversibly migrates a 0.17 file, after which 0.17 fails to open it with
`failed to open database with status 1`.

#### Scenario: Repair refuses to overwrite its source

- **GIVEN** a destination path equal to a source path
- **WHEN** a repair run is invoked
- **THEN** it SHALL fail with an explicit error before opening either graph
- **AND** the source file SHALL be byte-identical afterwards

#### Scenario: Classification leaves the source byte-identical

- **GIVEN** a source graph with a recorded checksum
- **WHEN** a classification run completes
- **THEN** the source file's checksum SHALL be unchanged
- **AND** no write-ahead-log file SHALL exist alongside it

---

### Requirement: Re-derivation MUST be gated on a cleared direction report

Generalization graphs SHALL NOT be re-derived from a corpus whose taxonomy direction report has
not been produced and reviewed. The builder SHALL refuse to run when the configured evidence
floor would retain zero taxonomy edges.

#### Scenario: Builder refuses a corpus with no orientable hierarchy

- **GIVEN** a source graph in which every taxonomy pair is tiered `undetermined`
- **WHEN** generalization is built
- **THEN** the build SHALL fail with an error naming the predicate and the tier counts
- **AND** SHALL NOT emit a derived graph

#### Scenario: Derived graph is a strict subgraph

- **GIVEN** a successful build at any evidence floor
- **WHEN** the derived graph is compared to its source
- **THEN** its taxonomy edge count SHALL be strictly less than the source's
- **AND** every retained taxonomy edge SHALL correspond to a source edge, in either the stored
  or the repaired orientation, with no edges invented
