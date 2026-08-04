package reasoning

import (
	"reflect"
	"testing"
)

func TestAllCanonicalSortedDeterministic(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Raw: "bee", Canonical: "b"},
		{Canonical: "c"},
		{Canonical: "a"},
		{Raw: "b surface", Canonical: "b"},
	}, nil, nil)

	got := reg.AllCanonical()
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllCanonical()=%v, want %v", got, want)
	}
}

func TestPredicatesEntailingIncludesMoreSpecificPredicates(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "parent"},
		{Canonical: "child", SubPropertyOf: []string{"parent"}},
		{Canonical: "grandchild", SubPropertyOf: []string{"child"}},
		{Canonical: "sibling"},
	}, nil, nil)

	got := predicatesEntailing(reg, "parent")
	want := []string{"child", "grandchild", "parent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("predicatesEntailing(parent)=%v, want %v", got, want)
	}
}

func TestPredicatesEntailingCycleSafe(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "parent"},
		{Canonical: "child", SubPropertyOf: []string{"parent", "cycle_a"}},
		{Canonical: "cycle_a", SubPropertyOf: []string{"cycle_b"}},
		{Canonical: "cycle_b", SubPropertyOf: []string{"cycle_a"}},
	}, nil, nil)

	got := predicatesEntailing(reg, "parent")
	want := []string{"child", "parent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("predicatesEntailing(parent)=%v, want %v", got, want)
	}
}

// The accepted set must NOT contain the inverse predicate's closure. It did while
// path traversal was undirected, and had to: a backward walk could arrive over
// either the predicate or its inverse. With directed traversal that is unsound —
// accepting "symptom_of" for a "has_symptom" claim in the SAME direction re-erases
// the direction the traversal fix restored. Verified on a real graph: with
// inverses accepted, "Dementia treats Geriatrics" still entailed against a stored
// "Geriatrics TREATS Dementia".
//
// Inverse claims are satisfied instead by swapping the endpoints and asking for
// the inverse predicate — see TestNativeReasonerRecoversInverseClaimInStoredDirection.
func TestNativeAcceptedPredicatesExcludesInverseEntailers(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "has_symptom", InverseOf: "symptom_of"},
		{Canonical: "has_phenotype", InverseOf: "phenotype_of", SubPropertyOf: []string{"has_symptom"}},
		{Canonical: "symptom_of", InverseOf: "has_symptom"},
		{Canonical: "phenotype_of", InverseOf: "has_phenotype", SubPropertyOf: []string{"symptom_of"}},
	}, nil, nil)

	got := nativeAcceptedPredicates(reg, "has_symptom")
	want := []string{"has_phenotype", "has_symptom"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nativeAcceptedPredicates(has_symptom)=%v, want %v (inverses must be reached by swapping endpoints, not by predicate-set membership)", got, want)
	}
}

func TestPredicatesEntailingIncludesUnknownTargetItself(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "known"}}, nil, nil)

	got := predicatesEntailing(reg, "external")
	want := []string{"external"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("predicatesEntailing(external)=%v, want %v", got, want)
	}
}

// Ingested graphs store the SOURCE vocabulary's predicate spelling, and registry
// lookup normalises only case — so a stored name differing from the canonical by
// more than case resolves as UNDECLARED and no inverse or sub-property entailment
// can fire for it. Verified live: TREATS/HAS_PHENOTYPE/CAUSES matched while
// CONTRAINDICATED and ASSOCIATION did not, which is why the DDx contradiction
// probe (a "treats" claim expected to conflict with a CONTRAINDICATED edge) never
// once returned "contradicted".
func TestGraphVocabularyPredicatesResolve(t *testing.T) {
	reg, err := BuildRegistry([]string{"medical"})
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]string{
		"CONTRAINDICATED": "contraindicated_for",
		"ASSOCIATION":     "associated_with",
		"IS_PARENT_OF":    "subsumes",
		"HAS_PHENOTYPE":   "has_phenotype",
		"TREATS":          "treats",
		"CAUSES":          "causes",
	} {
		meta, ok := reg.Canonical(raw)
		if !ok {
			t.Errorf("%s: unresolved — no inverse or sub-property entailment can fire for it", raw)
			continue
		}
		if meta.Canonical != want {
			t.Errorf("%s resolved to %q, want %q", raw, meta.Canonical, want)
		}
	}
}

// Predicates whose meaning would be CHANGED by a mapping must stay undeclared.
// NEGATIVELY_CORRELATES is the one that matters: folding it into correlated_with
// discards the negation and would let an inverse relationship read as supporting
// evidence. Silence is the safe answer; a wrong mapping is not.
func TestAmbiguousGraphPredicatesStayUnmapped(t *testing.T) {
	reg, err := BuildRegistry([]string{"medical"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"NEGATIVELY_CORRELATES", "POSITIVELY_CORRELATES", "EXPRESSES", "SYNERGIZES", "TARGETS"} {
		if meta, ok := reg.Canonical(raw); ok {
			t.Errorf("%s should stay undeclared rather than be guessed at, got %q", raw, meta.Canonical)
		}
	}
}
