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

func TestPredicatesEntailingIncludesUnknownTargetItself(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "known"}}, nil, nil)

	got := predicatesEntailing(reg, "external")
	want := []string{"external"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("predicatesEntailing(external)=%v, want %v", got, want)
	}
}
