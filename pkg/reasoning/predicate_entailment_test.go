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

func TestNativeAcceptedPredicatesIncludesInverseEntailers(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "has_symptom", InverseOf: "symptom_of"},
		{Canonical: "has_phenotype", InverseOf: "phenotype_of", SubPropertyOf: []string{"has_symptom"}},
		{Canonical: "symptom_of", InverseOf: "has_symptom"},
		{Canonical: "phenotype_of", InverseOf: "has_phenotype", SubPropertyOf: []string{"symptom_of"}},
	}, nil, nil)

	got := nativeAcceptedPredicates(reg, "has_symptom")
	want := []string{"has_phenotype", "has_symptom", "phenotype_of", "symptom_of"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nativeAcceptedPredicates(has_symptom)=%v, want %v", got, want)
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
