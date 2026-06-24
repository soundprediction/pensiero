package reasoning

import (
	"strings"
	"testing"
)

func TestDomainRangeCheckSatisfiedViolatedAndEmpty(t *testing.T) {
	types, err := BuildTypeRegistry(nil, TypePack{
		Name: "advisory_types",
		Types: []EntityType{
			{Name: "BIOLOGICAL_ENTITY"},
			{Name: "GENE", Supertypes: []string{"BIOLOGICAL_ENTITY"}},
			{Name: "PROTEIN", Supertypes: []string{"BIOLOGICAL_ENTITY"}},
			{Name: "CHEMICAL"},
			{Name: "DRUG", Supertypes: []string{"CHEMICAL"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := BuildRegistryWithTypes(nil, types, PredicatePack{
		Name: "advisory_predicates",
		Predicates: []PredicateMeta{
			{Canonical: "encodes", Domain: []string{"GENE"}, Range: []string{"PROTEIN"}},
			{Canonical: "acts_on", Domain: []string{"CHEMICAL"}, Range: []string{"BIOLOGICAL_ENTITY"}},
			{Canonical: "unbounded"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if ok, reason := DomainRangeCheck(reg, "encodes", []string{"GENE"}, []string{"PROTEIN"}); !ok || reason != "" {
		t.Fatalf("encodes check ok=%v reason=%q, want satisfied", ok, reason)
	}
	if ok, reason := DomainRangeCheck(reg, "acts_on", []string{"DRUG"}, []string{"GENE"}); !ok || reason != "" {
		t.Fatalf("acts_on subtype check ok=%v reason=%q, want satisfied", ok, reason)
	}
	if ok, reason := DomainRangeCheck(reg, "encodes", []string{"PROTEIN"}, []string{"GENE"}); ok || !strings.Contains(reason, "domain") || !strings.Contains(reason, "range") {
		t.Fatalf("violated check ok=%v reason=%q, want domain and range reason", ok, reason)
	}
	if ok, reason := DomainRangeCheck(reg, "unbounded", nil, nil); !ok || reason != "" {
		t.Fatalf("empty domain/range ok=%v reason=%q, want satisfied", ok, reason)
	}
}

func TestBuildRegistryUnknownDomainRangeTypeWarningIsSoft(t *testing.T) {
	types, err := BuildTypeRegistry(nil, TypePack{
		Name:  "known_types",
		Types: []EntityType{{Name: "KNOWN"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := BuildRegistryWithTypes(nil, types, PredicatePack{
		Name: "soft_domain_range",
		Predicates: []PredicateMeta{{
			Canonical: "soft_predicate",
			Domain:    []string{"KNOWN", "MISSING_HEAD"},
			Range:     []string{"MISSING_TAIL"},
		}},
	})
	if err != nil {
		t.Fatalf("BuildRegistryWithTypes returned hard error: %v", err)
	}
	warnings := strings.Join(reg.Warnings(), "\n")
	if !strings.Contains(warnings, `domain type "MISSING_HEAD" is undeclared`) {
		t.Fatalf("warnings %q missing domain warning", warnings)
	}
	if !strings.Contains(warnings, `range type "MISSING_TAIL" is undeclared`) {
		t.Fatalf("warnings %q missing range warning", warnings)
	}
}

func TestTypeRegistryIsATransitiveAndCycleSafe(t *testing.T) {
	reg, err := BuildTypeRegistry(nil, TypePack{
		Name: "type_closure",
		Types: []EntityType{
			{Name: "A", Supertypes: []string{"B"}},
			{Name: "B", Supertypes: []string{"C"}},
			{Name: "C"},
			{Name: "CycleA", Supertypes: []string{"CycleB"}},
			{Name: "CycleB", Supertypes: []string{"CycleA"}},
			{Name: "Warns", Supertypes: []string{"Missing"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reg.IsA("A", "C") {
		t.Fatal("A should be a transitive subtype of C")
	}
	if reg.IsA("C", "A") {
		t.Fatal("C should not be a subtype of A")
	}
	if !reg.IsA("CycleA", "CycleB") {
		t.Fatal("CycleA should resolve its direct cyclic supertype CycleB")
	}
	if reg.IsA("CycleA", "C") {
		t.Fatal("cycle traversal should terminate without inventing unrelated ancestors")
	}
	warnings := strings.Join(reg.Warnings(), "\n")
	if !strings.Contains(warnings, `type "Warns" supertype "Missing" is undeclared`) {
		t.Fatalf("warnings %q missing unknown supertype warning", warnings)
	}
}
