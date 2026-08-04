package reasoning

import (
	"context"
	"strings"
)

// Stored-direction repair.
//
// Ingested graphs do not orient asymmetric clinical predicates consistently.
// Measured across a full deployed generalization graph, of 20,172 HAS_PHENOTYPE
// edges 11,613 run DISEASE->symptom (correct) and 8,523 run symptom->DISEASE
// (backwards); only 36 are DISEASE->DISEASE and therefore genuinely ambiguous.
// The same inconsistency appears, less severely, in treats and
// contraindicated_for.
//
// While path traversal was undirected this was invisible: walking an edge
// backwards accidentally compensated for it. Making traversal directed — which
// was necessary, because undirected traversal emitted proofs for relationships
// the graph does not contain — exposed it, and cost the ~42% of phenotype edges
// stored the wrong way round.
//
// Rather than discard those edges or revert to unsound traversal, orientation is
// repaired at query time from the ENTITY TYPES, which the graphs carry and which
// disambiguate 99.8% of cases. An edge whose endpoints violate its predicate's
// declared domain/range but satisfy the INVERSE's is treated as that inverse:
// "Fatigue -has_phenotype-> Hypothyroidism" is read as "Fatigue phenotype_of
// Hypothyroidism", which is what the data means.
//
// This never invents a relationship. It only relabels one the graph already
// asserts, and only when the types make the intended reading unambiguous.

// orientedPredicate returns the predicate a stored edge actually expresses,
// given its endpoint types. It returns the input unchanged when the predicate
// declares no domain/range, when the types are unknown, or when the edge is
// already correctly oriented — so an absent or partial type vocabulary is a
// no-op rather than a source of guesses.
func orientedPredicate(reg *PredicateRegistry, pred string, headTypes, tailTypes []string) string {
	if reg == nil || len(headTypes) == 0 || len(tailTypes) == 0 {
		return pred
	}
	meta, known := reg.Canonical(pred)
	if !known || len(meta.Domain) == 0 || len(meta.Range) == 0 {
		return pred
	}
	// Already correct: leave it alone.
	if typesIntersect(headTypes, meta.Domain) && typesIntersect(tailTypes, meta.Range) {
		return pred
	}
	inv := strings.TrimSpace(meta.InverseOf)
	if inv == "" {
		return pred
	}
	invMeta, invKnown := reg.Canonical(inv)
	if !invKnown || len(invMeta.Domain) == 0 || len(invMeta.Range) == 0 {
		return pred
	}
	// Reversed: the endpoints match the inverse's declared shape instead.
	if typesIntersect(headTypes, invMeta.Domain) && typesIntersect(tailTypes, invMeta.Range) {
		return invMeta.Canonical
	}
	// Neither shape fits (e.g. DISEASE->DISEASE, or an untyped OTHER endpoint):
	// ambiguous, so change nothing.
	return pred
}

func typesIntersect(got, want []string) bool {
	for _, g := range got {
		for _, w := range want {
			if strings.EqualFold(strings.TrimSpace(g), strings.TrimSpace(w)) {
				return true
			}
		}
	}
	return false
}

// entityTypes looks up an entity's declared labels. Returns nil on any failure,
// which makes orientedPredicate a no-op rather than a guess.
func (n *NativeReasoner) entityTypes(ctx context.Context, name string) []string {
	if n.g == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if v, ok := n.entityTypeCache.Load(key); ok {
		out, _ := v.([]string)
		return out
	}
	// Collect labels from EVERY entity with this name, not just the first. These
	// graphs contain duplicate entities carrying conflicting types — "Hypothyroidism"
	// exists both as [DISEASE] and as [SYMPTOM] in the same graph — so a LIMIT 1
	// lookup makes orientation depend on which row the engine happens to return.
	// Taking the union instead means a genuinely dual-typed entity satisfies both
	// the domain and the range, which orientedPredicate then treats as ambiguous
	// and leaves alone. That is the correct outcome: the data does not say which
	// reading is intended, so nothing should be relabelled on its behalf.
	rows, err := n.g.Query(ctx,
		"MATCH (e:Entity) WHERE lower(e.name) = $n RETURN coalesce(e.labels, []) AS labels",
		map[string]any{"n": strings.ToLower(strings.TrimSpace(name))})
	var out []string
	if err == nil {
		seen := map[string]bool{}
		for _, r := range rows {
			for _, l := range asStringSlice(r["labels"]) {
				k := strings.ToUpper(strings.TrimSpace(l))
				if k != "" && !seen[k] {
					seen[k] = true
					out = append(out, k)
				}
			}
		}
	}
	n.entityTypeCache.Store(key, out)
	return out
}

// orientedSteps returns the proof steps with each step's predicate replaced by
// the one its endpoint types show the edge actually expresses. Steps it cannot
// orient are returned unchanged.
func (n *NativeReasoner) orientedSteps(ctx context.Context, in []ProofStep) []ProofStep {
	if len(in) == 0 {
		return in
	}
	out := make([]ProofStep, len(in))
	copy(out, in)
	for i := range out {
		src, tgt := out[i].Source, out[i].Target
		if src == "" || tgt == "" {
			continue
		}
		out[i].Predicate = orientedPredicate(n.reg, out[i].Predicate,
			n.entityTypes(ctx, src), n.entityTypes(ctx, tgt))
	}
	return out
}
