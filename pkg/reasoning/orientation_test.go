package reasoning

import "testing"

func orientReg() *PredicateRegistry {
	return NewPredicateRegistry([]PredicateMeta{
		{Canonical: "has_symptom", InverseOf: "symptom_of", Domain: []string{"DISEASE"}, Range: []string{"SYMPTOM"}},
		{Canonical: "symptom_of", InverseOf: "has_symptom", Domain: []string{"SYMPTOM"}, Range: []string{"DISEASE"}},
		{Canonical: "has_phenotype", InverseOf: "phenotype_of", SubPropertyOf: []string{"has_symptom"},
			Domain: []string{"DISEASE"}, Range: []string{"SYMPTOM"}},
		{Canonical: "phenotype_of", InverseOf: "has_phenotype", SubPropertyOf: []string{"symptom_of"},
			Domain: []string{"SYMPTOM"}, Range: []string{"DISEASE"}},
	}, nil, nil)
}

// The measured defect: ~42% of HAS_PHENOTYPE edges are stored symptom->DISEASE
// rather than DISEASE->symptom. Such an edge means phenotype_of, and reading it
// that way is what makes those edges usable again after traversal became
// directed.
func TestOrientedPredicateRepairsReversedStorage(t *testing.T) {
	got := orientedPredicate(orientReg(), "has_phenotype",
		[]string{"SYMPTOM"}, []string{"DISEASE"})
	if got != "phenotype_of" {
		t.Fatalf("reversed-storage edge should read as phenotype_of, got %q", got)
	}
}

// A correctly stored edge must be left completely alone.
func TestOrientedPredicateLeavesCorrectStorageAlone(t *testing.T) {
	got := orientedPredicate(orientReg(), "has_phenotype",
		[]string{"DISEASE"}, []string{"SYMPTOM"})
	if got != "has_phenotype" {
		t.Fatalf("correctly stored edge must not be relabelled, got %q", got)
	}
}

// DISEASE->DISEASE fits neither shape. Those exist (36 of 20,172 in the measured
// graph) and must be left untouched rather than guessed at — repairing direction
// is only licensed when the types make the reading unambiguous.
func TestOrientedPredicateLeavesAmbiguousEdgesAlone(t *testing.T) {
	got := orientedPredicate(orientReg(), "has_phenotype",
		[]string{"DISEASE"}, []string{"DISEASE"})
	if got != "has_phenotype" {
		t.Fatalf("ambiguous same-type edge must not be relabelled, got %q", got)
	}
}

// Absent or partial type information must be a no-op, never a guess: an untyped
// endpoint ("OTHER", or a graph with no labels at all) is exactly the case where
// relabelling would be least justified.
func TestOrientedPredicateNoOpsWithoutTypes(t *testing.T) {
	reg := orientReg()
	for _, tc := range []struct{ head, tail []string }{
		{nil, []string{"DISEASE"}},
		{[]string{"SYMPTOM"}, nil},
		{[]string{"OTHER"}, []string{"OTHER"}},
	} {
		if got := orientedPredicate(reg, "has_phenotype", tc.head, tc.tail); got != "has_phenotype" {
			t.Fatalf("head=%v tail=%v: want no-op, got %q", tc.head, tc.tail, got)
		}
	}
}

// A predicate that declares no domain/range cannot be oriented, so it must pass
// through — this is what keeps the repair opt-in per predicate.
func TestOrientedPredicateNoOpsWithoutDeclaredShape(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "associated_with", Chars: Symmetric},
	}, nil, nil)
	if got := orientedPredicate(reg, "associated_with", []string{"SYMPTOM"}, []string{"DISEASE"}); got != "associated_with" {
		t.Fatalf("undeclared shape must pass through, got %q", got)
	}
}

// These graphs contain duplicate entities with conflicting types — the same
// "Hypothyroidism" appears as both [DISEASE] and [SYMPTOM], so entityTypes takes
// the union. Ambiguity means BOTH readings fit; then the data does not say which
// is intended and nothing should be relabelled on its behalf.
func TestOrientedPredicateTreatsFullyDualTypedEdgeAsAmbiguous(t *testing.T) {
	dual := []string{"DISEASE", "SYMPTOM"}
	if got := orientedPredicate(orientReg(), "has_phenotype", dual, dual); got != "has_phenotype" {
		t.Fatalf("both endpoints dual-typed: both readings fit, so this must no-op; got %q", got)
	}
}

// A dual-typed endpoint does NOT block orientation when only one reading is
// possible: head [DISEASE,SYMPTOM] -> tail [DISEASE] cannot be has_phenotype
// (a DISEASE tail is outside its range), so the inverse is the only coherent
// reading and repairing to it is correct.
func TestOrientedPredicateStillRepairsWhenOnlyOneReadingFits(t *testing.T) {
	got := orientedPredicate(orientReg(), "has_phenotype",
		[]string{"DISEASE", "SYMPTOM"}, []string{"DISEASE"})
	if got != "phenotype_of" {
		t.Fatalf("only the inverse reading fits, so it should repair; got %q", got)
	}
}
