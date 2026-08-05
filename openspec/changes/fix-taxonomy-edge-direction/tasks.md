# Tasks: fix-taxonomy-edge-direction

All Go commands need `GOWORK=off`. CGO builds against ladybug 0.19 work on `tinyryzen`
(`/tmp/lb019` holds `liblbug` + headers). Production graphs are opened **read-only, on the box**;
graph data is never copied off it. Analysis scripts are shipped TO the box, not data FROM it.

---

## 0. [DONE] Gate: establish where the corruption comes from

**Outcome: the builder is exonerated; the corruption is upstream in the ingested corpus.**

- Source topic graph `/data/topic-graphs/ladybug/thyroid.ladybug` contains an identical
  **5,028 total / 3,640 bidirectional** `IS_PARENT_OF` — the same as the generalization graph.
- `pkg/generalization/builder.go` `addRelation` writes one relation per ID, deduped, with no
  inverse emission. It copies; it does not symmetrize.
- `RelatesToNode_.fact` is a serialization of the triple (`"X is parent of Y"`), not an
  independently extracted sentence — no provenance signal available there.

**Second outcome, which revised this whole change:** stored direction is a coin flip
(72 correct / 67 backwards on adjudicable unidirectional edges, 51.8%). The original plan's
`assumed` tier — "unidirectional, therefore probably fine" — is refuted and has been removed
from the spec. Direction must be *derived*, not *repaired*.

**Third outcome:** the edges are genuine hierarchy. Where GO can adjudicate a pair, it confirms a
true ancestor relation in 257 of 264 cases (97.3%). The hypothesis that these were sibling
edges mislabelled as hierarchy is refuted. The information is recoverable.

---

## 1. Ontology alignment — the authoritative direction source

**Implementation notes**
- New package `pkg/taxonomy/` (or `pkg/generalization/direction/`): load one or more OBO files,
  index `name` + `EXACT` synonyms → term ID, build the `is_a` ancestor closure, expose
  `Orient(a, b) (parent, child string, ok bool)`.
- **Start with GO, then add MONDO, HPO, CHEBI.** GO alone matches only 375 of 3,147 distinct
  entity names (11.9%) and orients 257 of 3,209 pairs (8.0%) — high precision, low coverage.
  Coverage is the binding constraint and the disease/phenotype/chemical ontologies are where it
  comes from.
- Ontology files are a build/runtime input, not vendored into the repo. Pin a version and record
  it in the report; a silently-updated ontology changes derived graphs.
- Matching is exact on normalized name/synonym. **Do not fuzzy-match ontology terms** — a wrong
  term match yields a confidently wrong hierarchy, the exact failure this change exists to
  prevent.

**Test plan**
- Unit: fixture OBO with a known 3-level chain; assert `Orient` returns the right parent in both
  argument orders and `ok=false` for unrelated terms.
- Unit: assert a name matching no term returns `ok=false` rather than a nearest match.
- Integration on the box: reproduce the measured **257 oriented / 7 unrelated / 52 stored-only-
  backwards**. A mismatch means the alignment is wrong, not the data.

---

## 2. Lexical token-subset — secondary signal, precision measured before use

**Implementation notes**
- `tokens(parent) ⊂ tokens(child)`, strict subset, using `normalizeEntityTokens` from
  `pkg/reasoning/entity_resolve.go` — not a second divergent normalizer.
- **Substring containment is insufficient and must not be used**: GO inserts qualifiers
  mid-string, so `regulation of positive thymic T cell selection` is a child of
  `regulation of T cell selection` while failing `contains()`. Measured: substring resolves 476
  bidirectional edges, token-subset resolves 611.
- Equal token sets → `undetermined` (12 such pairs measured).

**Test plan**
- Table test including the mid-string-qualifier case above and the three backwards instances
  from the proposal.
- Assert it does NOT fire on `"Hypothyroidism"` vs `"Autoimmune hypothyroidism"` — distinct
  clinical entities, and the same trap `entityResolveMinJaccard = 0.8` exists to avoid.
- **Precision measurement (blocking):** run the lexical signal on the GO-adjudicable subset and
  compare to GO's answer. Record the agreement rate in the change. Per the spec, a signal whose
  precision has not been measured SHALL NOT be enabled by default. If it disagrees with GO
  materially, it does not get promoted to a default signal.

---

## 3. `pensiero taxonomy-report` — the measurement instrument

**Implementation notes**
- New subcommand in `cmd/pensiero/`. Args: `--graph` (repeatable), `--ontology` (repeatable),
  `--predicate` (default: everything aliasing to `subsumes`/`is_a` via the registry — do not
  hardcode `IS_PARENT_OF`), `--sample-n` (default 20), `--json`.
- Opens read-only. Uses the **reified** match pattern from `reason_check.go` — a direct
  `(s)-[r]->(o)` match silently returns 0 rows against this schema and has already cost one
  wrong conclusion in this project.
- Emits per-predicate tier counts, per-signal breakdown, stored-direction contradiction count,
  signal-disagreement count, and the hand-checkable sample.

**Test plan**
- Unit: counts sum to total; per-signal breakdown sums to `oriented`.
- Unit: determinism — shuffle input order, assert identical output.
- Integration: reproduce **5,029 edges / 3,209 pairs / 3,640 bidirectional / 0 self-loops** on
  `thyroid.ladybug`; assert source checksum unchanged and no `.wal` created.

---

## 4. Wire direction derivation into the builder

**Implementation notes**
- Lifting consults the derived direction per pair; `undetermined` pairs are unreachable, with no
  flag to enable them (spec requirement).
- Ontology wins over lexical on disagreement; disagreements are counted and reported.
- Destination-equals-source check runs **before** either graph is opened. Sources open read-only:
  a 0.19 read-write open irreversibly migrates a 0.17 file.
- Builder errors out when zero taxonomy pairs are orientable rather than emitting an empty graph.

**Test plan**
- Unit: fixture graph with an undetermined pair produces no lifted relation across it.
- Unit: fixture where ontology and lexical disagree — assert ontology wins and the disagreement
  is counted.
- Unit: destination == source fails before open; source bytes unchanged.
- Unit: derived taxonomy edge count strictly less than source; every retained edge maps to a
  source pair with no edges invented.

---

## 5. Re-derive and compare

Only after task 2's precision figure is recorded.

**Implementation notes**
- Re-derive one topic to a NEW path on the box. Do not touch the deployed graphs.
- Diff against the current generalization graph: total edges, taxonomy edges, and whether lifted
  relations land on plausible ancestors.

**Test plan**
- Assert the derived graph is a small subgraph relative to its source (the `e8ea803` intent);
  record the ratio.
- Spot-check ~20 lifted relations for clinical plausibility. **A lifted relation whose target is
  a descendant of its source is a blocker**, not a rounding error — it is the exact failure this
  change exists to prevent.
- Re-run `pensiero reason-check` with the medical pack against the new graph; confirm entailments
  are produced without reintroducing the fabricated-citation class that directed traversal fixed.

---

## 6. Record the outcome honestly

- Update the proposal with final coverage and measured signal precision.
- If coverage stays low, **say so plainly** and lift only where direction is known, reporting the
  rest as ungrounded. Do not widen the signal until coverage looks acceptable — that trades a
  measurable gap for an unmeasurable error rate.
- File the upstream ingest defect separately: whatever produced 72% bidirectional, 50/50-oriented
  `IS_PARENT_OF` will keep doing so on the next ingest.
