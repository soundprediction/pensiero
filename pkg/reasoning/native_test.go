package reasoning

import (
	"context"
	"strings"
	"testing"
)

// The reasoning extension emits a proof as a JSON array of steps; parseProofJSON
// must decode that into a Proof (deriving Source/Target/RuleClass/Hops) so callers
// receive a populated proof rather than a silently-empty one.
func TestParseProofArrayForm(t *testing.T) {
	s := `[{"edge_id":"gg-1","rule":"composition","predicate":"is_parent_of","source":"A","target":"B","confidence":0.8},` +
		`{"edge_id":"x","rule":"composition","predicate":"has_phenotype","source":"B","target":"C","confidence":0.8}]`
	p, ok := parseProofJSON(s)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(p.Steps) != 2 {
		t.Fatalf("steps=%d", len(p.Steps))
	}
	if p.Source != "A" || p.Target != "C" {
		t.Fatalf("src=%q tgt=%q", p.Source, p.Target)
	}
	if p.RuleClass != "composition" || p.Hops != 2 {
		t.Fatalf("ruleClass=%q hops=%d", p.RuleClass, p.Hops)
	}
	if p.Steps[1].Predicate != "has_phenotype" {
		t.Fatalf("pred=%q", p.Steps[1].Predicate)
	}
}

// Object form (other backends) and empty/null inputs are also handled.
func TestParseProofObjectAndEmpty(t *testing.T) {
	obj := `{"source":"A","target":"C","rule_class":"composition","steps":[{"predicate":"p","source":"A","target":"C"}]}`
	if p, ok := parseProofJSON(obj); !ok || p.Source != "A" || len(p.Steps) != 1 {
		t.Fatalf("object form: ok=%v p=%+v", ok, p)
	}
	for _, s := range []string{"", "null", "[]", "  "} {
		if _, ok := parseProofJSON(s); ok {
			t.Fatalf("expected !ok for %q", s)
		}
	}
}

func TestNativeReasonerEntailsUsesAcceptedPredicatesWhenEnforced(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Raw: "has symptom", Canonical: "has_symptom", InverseOf: "symptom_of"},
		{Canonical: "has_phenotype", InverseOf: "phenotype_of", SubPropertyOf: []string{"has_symptom"}},
		{Canonical: "symptom_of", InverseOf: "has_symptom"},
		{Canonical: "phenotype_of", InverseOf: "has_phenotype", SubPropertyOf: []string{"symptom_of"}},
	}, nil, nil)
	var entailsQuery string
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if strings.Contains(q, "REASON_ENTAILS") {
			entailsQuery = q
			return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
		}
		return nil, nil
	}}
	n := NewNativeReasoner(g, reg, Config{})
	n.EnforcePredicate = true

	_, err := n.Entails(context.Background(), Claim{
		Subject: "flu", Predicate: "has symptom", Object: "fever",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "CALL REASON_ENTAILS('flu', 'has symptom', 'fever', 4, 'has_phenotype,has_symptom,phenotype_of,symptom_of')"
	if !strings.Contains(entailsQuery, want) {
		t.Fatalf("query=%q, want to contain %q", entailsQuery, want)
	}
}

func TestNativeReasonerEntailsKeepsLegacyArityByDefault(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "p"}}, nil, nil)
	var entailsQuery string
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if strings.Contains(q, "REASON_ENTAILS") {
			entailsQuery = q
			return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
		}
		return nil, nil
	}}
	n := NewNativeReasoner(g, reg, Config{})

	_, err := n.Entails(context.Background(), Claim{Subject: "s", Predicate: "p", Object: "o"})
	if err != nil {
		t.Fatal(err)
	}
	want := "CALL REASON_ENTAILS('s', 'p', 'o', 4) YIELD"
	if !strings.Contains(entailsQuery, want) {
		t.Fatalf("query=%q, want legacy arity containing %q", entailsQuery, want)
	}
	if strings.Contains(entailsQuery, "'p') YIELD") {
		t.Fatalf("query=%q unexpectedly used accepted predicate arity", entailsQuery)
	}
}
