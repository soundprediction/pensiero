# Proposal: Recover taxonomy edge direction by ontology alignment, not by trusting storage

## Summary

`IS_PARENT_OF` — the second-most-common predicate in the corpus — carries **no usable direction
information**. This was measured, not assumed: of unidirectional edges where an independent
signal can adjudicate, **51.8% are stored parent→child and 48.2% backwards**. That is a coin
flip. Generalization lifts relations by walking the hierarchy in one configured direction, so
re-deriving against this corpus would attach symptoms and findings to the wrong conditions with
full confidence.

The edges themselves are **genuine hierarchy assertions** — when an external ontology can
adjudicate a pair, it confirms a true ancestor relation 97.3% of the time. Only the direction is
scrambled. So the fix is not to repair stored direction but to **derive direction from an
authoritative ontology** and discard the stored orientation entirely.

## Measured evidence

All figures from the deployed Mumbai node, opened **read-only**, on
`/data/generalization-graphs/thyroid.ladybug` and its source
`/data/topic-graphs/ladybug/thyroid.ladybug`. Nothing was copied off the box.

### The direction is random

`IS_PARENT_OF`: **5,029 edges**, 3,209 distinct entity pairs.

| Class | Edges | Share |
|---|---:|---:|
| Bidirectional (both orientations stored) | 3,640 | 72.4% |
| Unidirectional | 1,389 | 27.6% |
| Self-loops | 0 | 0% |

Of the 1,389 unidirectional edges, 139 have a lexical signal that can adjudicate them:

| Stored orientation | Count | Share of adjudicable |
|---|---:|---:|
| Correct (parent→child) | 72 | **51.8%** |
| Backwards (child→parent) | 67 | **48.2%** |

Unambiguous instances of the backwards class:
`AMP-thymidine kinase activity IS_PARENT_OF kinase activity`,
`thyroid-hormone transaminase activity IS_PARENT_OF transaminase activity`.

**Conclusion: stored direction is statistically indistinguishable from random.** Any design that
trusts it — including a tier for "unidirectional, therefore probably fine" — is unsound. An
earlier draft of this proposal contained exactly that tier; the measurement refutes it.

### But the edges are real hierarchy

Aligning entity names against Gene Ontology (`go-basic.obo`, 125,131 indexed names and exact
synonyms), for pairs where **both** endpoints match a GO term:

- GO confirms an ancestor relation: **257 pairs**
- GO says the two are unrelated: **7 pairs (2.7%)**

So 97.3% of adjudicable pairs are genuine hierarchy. This refutes a second hypothesis this
change previously entertained — that the unresolvable majority were sibling/relatedness edges
mislabelled as hierarchy. They are not. They are true hierarchy with scrambled direction, which
means the information is recoverable rather than absent.

Of the 257 pairs GO can orient, **52 are stored only in the backwards orientation** — they would
be silently lifted the wrong way today.

### Coverage is the binding constraint

| Signal | Pairs oriented | Share of 3,209 | Precision |
|---|---:|---:|---|
| GO alignment (exact name/synonym) | 257 | 8.0% | High — authoritative DAG |
| Lexical token-subset | ~444 | 13.8% | Unmeasured; requires task 2 |
| Neither | ~2,600 | ~81% | — |

Only **375 of 3,147 distinct entity names (11.9%)** match GO. The corpus is mixed — GO biological
processes and molecular functions alongside diseases, chemicals, and phenotypes — so GO alone
cannot cover it. **Adding MONDO (disease), HPO (phenotype), and CHEBI (chemical) is the path to
coverage**, and each brings the same authoritative direction GO does.

### The builder is not at fault

The source topic graph contains an identical **5,028 total / 3,640 bidirectional**.
`pkg/generalization`'s `addRelation` writes one relation per ID with no inverse emission. The
corruption is upstream in the ingested corpus, not in our derivation. (This was the gating
question: it would have been wrong to build a repair for corruption we introduced ourselves.)

The `RelatesToNode_.fact` field is a serialization of the triple (`"X is parent of Y"`), not an
independently extracted source sentence, so it supplies no direction evidence.

## What

### 1. Ontology alignment as the primary direction source

Align entity names against published ontology DAGs (GO first, then MONDO/HPO/CHEBI) and take the
parent-child direction from the ontology. Stored orientation is **ignored, not repaired**.

This inverts the original framing. Because storage is random, there is nothing to repair — an
edge is either adjudicable by an authoritative source or it is not usable.

### 2. Lexical token-subset as a measured secondary signal

Token-subset containment (`tokens(parent) ⊂ tokens(child)`) catches GO's compositional naming
that substring matching misses — GO inserts qualifiers mid-string, so
`regulation of **positive thymic** T cell selection` is a child of `regulation of T cell
selection` despite failing `contains()`. This signal must have its precision measured against
the GO-adjudicable subset before being trusted on the non-adjudicable remainder.

### 3. Everything else is dropped

No tier trusts stored direction. An edge no signal can orient is dropped, not guessed.

### 4. Repair writes a NEW graph

Production opens graphs read-only, and a ladybug 0.19 read-write open irreversibly migrates a
0.17 file (verified: md5 changes, and 0.17 then fails with
`failed to open database with status 1`). Sources are opened read-only; results go to a new path.

## Constraints (non-negotiable)

- **Fail closed.** An unorientable edge is dropped, never guessed. A wrong hierarchy edge
  silently propagates wrong symptoms onto wrong conditions via lifting.
- **Never trust stored direction.** Measured at 51.8/48.2.
- **Measurable.** Every run reports edges oriented, dropped, and the signal's precision on the
  GO-adjudicable subset.
- **Never mutate a source graph in place.**
- Reuse the registry's `is_a`/`subsumes` inverse declaration
  (`pkg/reasoning/general_primitives.go`); do not introduce a parallel direction vocabulary.

## Expected yield, stated honestly

With GO alone: ~8% of pairs. With lexical added and assuming it validates: ~20%. With
MONDO/HPO/CHEBI added: unmeasured, plausibly a majority, and that measurement is task 4.

**A small hierarchy that is correct is usable; a complete one that is 50% inverted is not.**
If the final coverage is low, the correct response is to lift only where direction is known and
report the rest as ungrounded — not to widen the signal until coverage looks acceptable.

## Non-goals

- **Inventing hierarchy edges** not present in the source, including transitive closure.
- **Fixing the upstream ingest.** The ingest that scrambled this direction should be corrected,
  but that is a separate change against the ingest pipeline.
- **Changing runtime reasoning behaviour.** `pkg/reasoning` traversal, acceptance, and
  orientation are untouched; this is corpus construction.
- **Re-deriving the deployed generalization graphs.** Unblocked by this change, performed after.
