package reasoning

import (
	"sort"
	"strings"
)

func canonicalPredicate(reg *PredicateRegistry, pred string) string {
	if reg == nil {
		return strings.TrimSpace(pred)
	}
	meta, _ := reg.Canonical(pred)
	return meta.Canonical
}

// effectivePredicate folds a proof path to the single predicate it establishes.
// Single-hop paths establish their edge predicate. Multi-hop paths must either
// reduce left-to-right through registered composition rules, or share a common
// transitive super-property.
func effectivePredicate(reg *PredicateRegistry, steps []ProofStep) (string, bool) {
	if len(steps) == 0 {
		return "", false
	}
	preds := make([]string, 0, len(steps))
	for _, step := range steps {
		preds = append(preds, canonicalPredicate(reg, step.Predicate))
	}
	if len(preds) == 1 {
		return preds[0], preds[0] != ""
	}

	acc := preds[0]
	reduced := true
	for _, next := range preds[1:] {
		result, ok := composePredicates(reg, acc, next)
		if !ok {
			reduced = false
			break
		}
		acc = result
	}
	if reduced && acc != "" {
		return acc, true
	}

	return commonTransitiveSuperProperty(reg, preds)
}

func commonTransitiveSuperProperty(reg *PredicateRegistry, preds []string) (string, bool) {
	if len(preds) == 0 {
		return "", false
	}
	candidates := transitiveSuperProperties(reg, preds[0])
	for _, pred := range preds[1:] {
		next := transitiveSuperProperties(reg, pred)
		if len(next) == 0 {
			return "", false
		}
		nextSet := map[string]bool{}
		for _, candidate := range next {
			nextSet[normKey(candidate)] = true
		}
		kept := candidates[:0]
		for _, candidate := range candidates {
			if nextSet[normKey(candidate)] {
				kept = append(kept, candidate)
			}
		}
		candidates = kept
		if len(candidates) == 0 {
			return "", false
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[0], true
}

func transitiveSuperProperties(reg *PredicateRegistry, pred string) []string {
	pred = canonicalPredicate(reg, pred)
	if pred == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	queue := []string{pred}
	for len(queue) > 0 {
		cur := canonicalPredicate(reg, queue[0])
		queue = queue[1:]
		key := normKey(cur)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if reg.IsTransitive(cur) {
			out = append(out, cur)
		}
		for _, sup := range reg.SuperPropertiesOf(cur) {
			sup = canonicalPredicate(reg, sup)
			if sup != "" && !seen[normKey(sup)] {
				queue = append(queue, sup)
			}
		}
	}
	return out
}

func composePredicates(reg *PredicateRegistry, first, second string) (string, bool) {
	first = canonicalPredicate(reg, first)
	second = canonicalPredicate(reg, second)
	for _, comp := range reg.Compositions() {
		compFirst := canonicalPredicate(reg, comp.First)
		compSecond := canonicalPredicate(reg, comp.Second)
		if normKey(compFirst) == normKey(first) && normKey(compSecond) == normKey(second) {
			return canonicalPredicate(reg, comp.Result), true
		}
	}
	return "", false
}

// predicateEntails reports whether effective ⊑* target through the registered
// sub-property hierarchy. Equality counts, and cycles are ignored safely.
func predicateEntails(reg *PredicateRegistry, effective, target string) bool {
	effective = canonicalPredicate(reg, effective)
	target = canonicalPredicate(reg, target)
	if effective == "" || target == "" {
		return false
	}

	seen := map[string]bool{}
	queue := []string{effective}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		key := normKey(cur)
		if seen[key] {
			continue
		}
		seen[key] = true
		if key == normKey(target) {
			return true
		}
		for _, sup := range reg.SuperPropertiesOf(cur) {
			sup = canonicalPredicate(reg, sup)
			if sup != "" && !seen[normKey(sup)] {
				queue = append(queue, sup)
			}
		}
	}
	return false
}

// proofEntailsPredicate reports whether a path whose effective predicate is
// `effective` supports a claim with predicate `target`.
//
// NOTE on direction: the Entails path query (compositionCypher) is UNDIRECTED, so
// the engine does not distinguish P(a,b) from P(b,a) here — entailment is over the
// undirected relationship. Accepting an inverse-predicate match under includeInverse
// is therefore consistent with that model (it adds no unsoundness beyond the
// pre-existing direction-agnosticism). Enforcing head/tail direction is a separate,
// larger change to the traversal, not part of predicate-correctness.
func proofEntailsPredicate(reg *PredicateRegistry, effective, target string, includeInverse bool) bool {
	target = canonicalPredicate(reg, target)
	if predicateEntails(reg, effective, target) {
		return true
	}
	if !includeInverse {
		return false
	}
	// A symmetric target's inverse is itself, already covered by the direct check
	// above; only a declared distinct inverse can add a match.
	if inverse, ok := reg.Inverse(target); ok && normKey(inverse) != normKey(target) {
		return predicateEntails(reg, effective, inverse)
	}
	return false
}

// predicatesEntailing returns every known canonical predicate P' such that
// P' ⊑* target. Equality counts; inverse matching is handled by callers that need
// it so this function remains a pure sub-property closure.
func predicatesEntailing(reg *PredicateRegistry, target string) []string {
	target = canonicalPredicate(reg, target)
	if strings.TrimSpace(target) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(pred string) {
		pred = canonicalPredicate(reg, pred)
		key := normKey(pred)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, pred)
	}
	for _, pred := range reg.AllCanonical() {
		if proofEntailsPredicate(reg, pred, target, false) {
			add(pred)
		}
	}
	add(target)
	sort.Slice(out, func(i, j int) bool {
		return normKey(out[i]) < normKey(out[j])
	})
	return out
}
