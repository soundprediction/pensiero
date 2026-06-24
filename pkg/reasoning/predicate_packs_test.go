package reasoning

import (
	"strings"
	"testing"
)

func TestBuildRegistryBuiltInPacksValidate(t *testing.T) {
	for _, names := range [][]string{
		{"general"},
		{"medical"},
	} {
		if _, err := BuildRegistry(names); err != nil {
			t.Fatalf("BuildRegistry(%v) returned error: %v", names, err)
		}
	}
}

func TestDefaultRegistriesBuild(t *testing.T) {
	if DefaultGeneralRegistry() == nil {
		t.Fatal("DefaultGeneralRegistry returned nil")
	}
	if DefaultMedicalRegistry() == nil {
		t.Fatal("DefaultMedicalRegistry returned nil")
	}
}

func TestBuildRegistryLayeringAndExtraOverrides(t *testing.T) {
	reg, err := BuildRegistry([]string{"medical"}, PredicatePack{
		Name: "operator_override",
		Predicates: []PredicateMeta{
			{Canonical: "treats", Chars: Symmetric},
			{Canonical: "operator_relation"},
		},
		Aliases: map[string]string{
			"is a": "operator_relation",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Canonical("prevents"); !ok {
		t.Fatal("medical pack was not layered before extras")
	}
	if !reg.IsSymmetric("treats") {
		t.Fatal("extra predicate did not override built-in canonical treats")
	}
	meta, ok := reg.Canonical("is a")
	if !ok {
		t.Fatal("extra alias override was not registered")
	}
	if meta.Canonical != "operator_relation" {
		t.Fatalf("alias canonical=%q, want operator_relation", meta.Canonical)
	}
}

func TestBuildRegistryRejectsBuiltInDuplicateCanonical(t *testing.T) {
	_, err := BuildRegistry([]string{"medical", "medical"})
	if err == nil {
		t.Fatal("BuildRegistry returned nil error")
	}
	if !strings.Contains(err.Error(), "duplicates built-in predicate") {
		t.Fatalf("error %q does not report duplicate built-in predicate", err)
	}
}

func TestBuildRegistryRejectsUndeclaredReferences(t *testing.T) {
	cases := []struct {
		name string
		pack PredicatePack
		want string
	}{
		{
			name: "inverse",
			pack: PredicatePack{
				Name: "bad_inverse",
				Predicates: []PredicateMeta{{
					Canonical: "bad_inverse",
					InverseOf: "missing_inverse",
				}},
			},
			want: "inverse_of",
		},
		{
			name: "subproperty",
			pack: PredicatePack{
				Name: "bad_subproperty",
				Predicates: []PredicateMeta{{
					Canonical:     "bad_subproperty",
					SubPropertyOf: []string{"missing_parent"},
				}},
			},
			want: "sub_property_of",
		},
		{
			name: "composition",
			pack: PredicatePack{
				Name: "bad_composition",
				Predicates: []PredicateMeta{{
					Canonical: "composition_source",
				}},
				Compositions: []CompositionRule{{
					First:  "composition_source",
					Second: "missing_second",
					Result: "composition_source",
				}},
			},
			want: "second",
		},
		{
			name: "disjoint",
			pack: PredicatePack{
				Name: "bad_disjoint",
				Predicates: []PredicateMeta{{
					Canonical: "disjoint_source",
				}},
				Disjoints: []DisjointPair{{
					A: "disjoint_source",
					B: "missing_disjoint",
				}},
			},
			want: "disjoint",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildRegistry(nil, tc.pack)
			if err == nil {
				t.Fatal("BuildRegistry returned nil error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestBuildRegistryRejectsDuplicateAliasesDeterministically(t *testing.T) {
	_, err := BuildRegistry(nil, PredicatePack{
		Name: "duplicate_alias",
		Predicates: []PredicateMeta{
			{Canonical: "duplicate_alias_one"},
			{Canonical: "duplicate_alias_two"},
		},
		Aliases: map[string]string{
			"Surface Alias":  "duplicate_alias_one",
			" surface alias": "duplicate_alias_two",
		},
	})
	if err == nil {
		t.Fatal("BuildRegistry returned nil error")
	}
	if !strings.Contains(err.Error(), "duplicate alias") {
		t.Fatalf("error %q does not report duplicate alias", err)
	}
}

func TestFingerprintStableAndChangesWithRegistryInputs(t *testing.T) {
	base, err := BuildRegistry([]string{"general"})
	if err != nil {
		t.Fatal(err)
	}
	fp := base.Fingerprint()
	if fp == "" {
		t.Fatal("Fingerprint returned empty string")
	}
	if got := base.Fingerprint(); got != fp {
		t.Fatalf("Fingerprint not stable: %q then %q", fp, got)
	}

	withPredicate, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name: "fingerprint_predicate",
		Predicates: []PredicateMeta{{
			Canonical:     "fingerprint_predicate",
			SubPropertyOf: []string{"related_to"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := withPredicate.Fingerprint(); got == fp {
		t.Fatal("Fingerprint did not change after predicate change")
	}

	withCharsInverse, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name: "fingerprint_chars_inverse",
		Predicates: []PredicateMeta{
			{Canonical: "fingerprint_forward", Chars: Symmetric, InverseOf: "fingerprint_reverse"},
			{Canonical: "fingerprint_reverse", InverseOf: "fingerprint_forward"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutCharsInverse, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name: "fingerprint_no_chars_inverse",
		Predicates: []PredicateMeta{
			{Canonical: "fingerprint_forward"},
			{Canonical: "fingerprint_reverse"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := withCharsInverse.Fingerprint(); got == withoutCharsInverse.Fingerprint() {
		t.Fatal("Fingerprint did not change after chars/inverse change")
	}

	withAlias, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name: "fingerprint_alias",
		Predicates: []PredicateMeta{{
			Canonical: "fingerprint_alias_target",
		}},
		Aliases: map[string]string{
			"fingerprint surface": "fingerprint_alias_target",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutAlias, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name: "fingerprint_without_alias",
		Predicates: []PredicateMeta{{
			Canonical: "fingerprint_alias_target",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := withAlias.Fingerprint(); got == withoutAlias.Fingerprint() {
		t.Fatal("Fingerprint did not change after alias change")
	}

	compositionPredicates := []PredicateMeta{
		{Canonical: "fingerprint_first"},
		{Canonical: "fingerprint_second"},
		{Canonical: "fingerprint_result"},
	}
	withoutComposition, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name:       "fingerprint_composition_base",
		Predicates: compositionPredicates,
	})
	if err != nil {
		t.Fatal(err)
	}
	withComposition, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name:       "fingerprint_composition",
		Predicates: compositionPredicates,
		Compositions: []CompositionRule{{
			First:  "fingerprint_first",
			Second: "fingerprint_second",
			Result: "fingerprint_result",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := withComposition.Fingerprint(); got == withoutComposition.Fingerprint() {
		t.Fatal("Fingerprint did not change after composition change")
	}

	disjointPredicates := []PredicateMeta{
		{Canonical: "fingerprint_left"},
		{Canonical: "fingerprint_right"},
	}
	withoutDisjoint, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name:       "fingerprint_disjoint_base",
		Predicates: disjointPredicates,
	})
	if err != nil {
		t.Fatal(err)
	}
	withDisjoint, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name:       "fingerprint_disjoint",
		Predicates: disjointPredicates,
		Disjoints: []DisjointPair{{
			A: "fingerprint_left",
			B: "fingerprint_right",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := withDisjoint.Fingerprint(); got == withoutDisjoint.Fingerprint() {
		t.Fatal("Fingerprint did not change after disjoint change")
	}
}

func TestFingerprintIgnoresPackInputOrdering(t *testing.T) {
	first, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name: "fingerprint_order_a",
		Predicates: []PredicateMeta{
			{Canonical: "fingerprint_order_a", InverseOf: "fingerprint_order_b", SubPropertyOf: []string{"associated_with", "related_to"}},
			{Canonical: "fingerprint_order_b", InverseOf: "fingerprint_order_a"},
			{Canonical: "fingerprint_order_c"},
			{Canonical: "fingerprint_order_d"},
		},
		Compositions: []CompositionRule{
			{First: "fingerprint_order_a", Second: "fingerprint_order_b", Result: "fingerprint_order_c"},
			{First: "fingerprint_order_c", Second: "fingerprint_order_d", Result: "fingerprint_order_a"},
		},
		Disjoints: []DisjointPair{
			{A: "fingerprint_order_a", B: "fingerprint_order_b"},
			{A: "fingerprint_order_c", B: "fingerprint_order_d"},
		},
		Aliases: map[string]string{
			"fingerprint order": "fingerprint_order_a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildRegistry([]string{"general"}, PredicatePack{
		Name: "fingerprint_order_b",
		Predicates: []PredicateMeta{
			{Canonical: "fingerprint_order_d"},
			{Canonical: "fingerprint_order_c"},
			{Canonical: "fingerprint_order_b", InverseOf: "fingerprint_order_a"},
			{Canonical: "fingerprint_order_a", InverseOf: "fingerprint_order_b", SubPropertyOf: []string{"related_to", "associated_with"}},
		},
		Compositions: []CompositionRule{
			{First: "fingerprint_order_c", Second: "fingerprint_order_d", Result: "fingerprint_order_a"},
			{First: "fingerprint_order_a", Second: "fingerprint_order_b", Result: "fingerprint_order_c"},
		},
		Disjoints: []DisjointPair{
			{A: "fingerprint_order_d", B: "fingerprint_order_c"},
			{A: "fingerprint_order_b", B: "fingerprint_order_a"},
		},
		Aliases: map[string]string{
			"fingerprint order": "fingerprint_order_a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := first.Fingerprint(), second.Fingerprint(); got != want {
		t.Fatalf("fingerprints differ after reordering: %q != %q", got, want)
	}
}

func TestMedicalPackRepair(t *testing.T) {
	reg, err := BuildRegistry([]string{"medical"})
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := reg.Canonical("prevents")
	if !ok {
		t.Fatal("prevents is not declared")
	}
	if meta.InverseOf != "prevented_by" {
		t.Fatalf("prevents inverse=%q, want prevented_by", meta.InverseOf)
	}
	if !reg.DisjointWith("treats", "contraindicated_for") {
		t.Fatal("treats should be disjoint with contraindicated_for")
	}
	if reg.DisjointWith("treats", "contraindicated") {
		t.Fatal("treats should not be disjoint with undeclared contraindicated")
	}
	if !reg.DisjointWith("prevents", "causes") {
		t.Fatal("prevents should be disjoint with causes")
	}
	for _, pair := range reg.disjoint {
		if _, ok := reg.byCanon[normKey(pair.A)]; !ok {
			t.Fatalf("disjoint A %q is not declared canonical", pair.A)
		}
		if _, ok := reg.byCanon[normKey(pair.B)]; !ok {
			t.Fatalf("disjoint B %q is not declared canonical", pair.B)
		}
	}
}

func TestIsReflexivePredicateAware(t *testing.T) {
	reg := DefaultMedicalRegistry()
	// Reflexive-meaningful predicates: a self-loop is a real fact, not a tautology.
	for _, p := range []string{"interacts_with", "interacts", "inhibits", "activates", "INHIBITS"} {
		if !reg.IsReflexive(p) {
			t.Errorf("IsReflexive(%q) = false, want true", p)
		}
	}
	// Irreflexive predicates: a self-loop is a tautology.
	for _, p := range []string{"causes", "has_phenotype", "presents with", "associated_with", "treats", "is_a"} {
		if reg.IsReflexive(p) {
			t.Errorf("IsReflexive(%q) = true, want false", p)
		}
	}
	// Unknown predicate: not reflexive.
	if reg.IsReflexive("totally_unknown_predicate") {
		t.Errorf("IsReflexive(unknown) = true, want false")
	}
}

func TestIsReflexiveInherited(t *testing.T) {
	// A predicate inherits reflexivity from a reflexive super-property.
	reg, err := BuildRegistry(nil, PredicatePack{
		Name: "reflexive-inherit-test",
		Predicates: []PredicateMeta{
			{Canonical: "self_relatable", Chars: Reflexive},
			{Canonical: "child_rel", SubPropertyOf: []string{"self_relatable"}},
		},
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if !reg.IsReflexive("child_rel") {
		t.Errorf("child_rel should inherit reflexivity from self_relatable")
	}
}
