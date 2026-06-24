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
	n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))
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

func TestNativeReasonerEntailsEscapesAcceptedPredicateList(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "target"},
		{Canonical: "comma,pred", SubPropertyOf: []string{"target"}},
		{Canonical: "quote'pred", SubPropertyOf: []string{"target"}},
		{Canonical: `slash\pred`, SubPropertyOf: []string{"target"}},
	}, nil, nil)
	var entailsQuery string
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if strings.Contains(q, "REASON_ENTAILS") {
			entailsQuery = q
			return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
		}
		return nil, nil
	}}
	n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))
	n.EnforcePredicate = true

	_, err := n.Entails(context.Background(), Claim{
		Subject: "s'ub", Predicate: "target", Object: `o\bj`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `CALL REASON_ENTAILS('s\'ub', 'target', 'o\\bj', 4, 'comma\\,pred,quote\'pred,slash\\\\pred,target')`
	if !strings.Contains(entailsQuery, want) {
		t.Fatalf("query=%q, want to contain %q", entailsQuery, want)
	}
}

func TestNativeReasonerEntailsKeepsLegacyArityWhenQuarantineDisabled(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "p"}}, nil, nil)
	var entailsQuery string
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if rows, ok := answerProvenanceStatusProbe(q, true); ok {
			return rows, nil
		}
		if strings.Contains(q, "REASON_ENTAILS") {
			entailsQuery = q
			return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
		}
		return nil, nil
	}}
	n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))

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

func TestNativeReasonerEntailsSchemaGatesQuarantineFlag(t *testing.T) {
	t.Run("without status keeps legacy arity", func(t *testing.T) {
		reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "p"}}, nil, nil)
		var gotQuery string
		g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
			if rows, ok := answerProvenanceStatusProbe(q, false); ok {
				return rows, nil
			}
			gotQuery = q
			return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
		}}
		n := NewNativeReasoner(g, reg, Config{})

		_, err := n.Entails(context.Background(), Claim{Subject: "s", Predicate: "p", Object: "o"})
		if err != nil {
			t.Fatal(err)
		}
		want := "CALL REASON_ENTAILS('s', 'p', 'o', 4) YIELD"
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q, want legacy arity containing %q", gotQuery, want)
		}
		if strings.Contains(gotQuery, ", true) YIELD") {
			t.Fatalf("query=%q unexpectedly contained quarantine flag", gotQuery)
		}
	})

	t.Run("without status keeps accepted-predicate arity", func(t *testing.T) {
		reg := NewPredicateRegistry([]PredicateMeta{
			{Canonical: "p"},
			{Canonical: "child_p", SubPropertyOf: []string{"p"}},
		}, nil, nil)
		var gotQuery string
		g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
			if rows, ok := answerProvenanceStatusProbe(q, false); ok {
				return rows, nil
			}
			gotQuery = q
			return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
		}}
		n := NewNativeReasoner(g, reg, Config{})
		n.EnforcePredicate = true

		_, err := n.Entails(context.Background(), Claim{Subject: "s", Predicate: "p", Object: "o"})
		if err != nil {
			t.Fatal(err)
		}
		want := "CALL REASON_ENTAILS('s', 'p', 'o', 4, 'child_p,p') YIELD"
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q, want accepted-predicate arity containing %q", gotQuery, want)
		}
		if strings.Contains(gotQuery, ", true) YIELD") {
			t.Fatalf("query=%q unexpectedly contained quarantine flag", gotQuery)
		}
	})
}

func TestNativeReasonerEntailsDefaultsToNativeQuarantineFlag(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "p"}}, nil, nil)
	var gotQuery string
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if rows, ok := answerProvenanceStatusProbe(q, true); ok {
			return rows, nil
		}
		gotQuery = q
		return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
	}}
	n := NewNativeReasoner(g, reg, Config{})

	_, err := n.Entails(context.Background(), Claim{Subject: "s", Predicate: "p", Object: "o"})
	if err != nil {
		t.Fatal(err)
	}
	want := "CALL REASON_ENTAILS('s', 'p', 'o', 4, '', true) YIELD"
	if !strings.Contains(gotQuery, want) {
		t.Fatalf("query=%q, want native quarantine arity containing %q", gotQuery, want)
	}
	if strings.Contains(gotQuery, "[n IN nodes(p) WHERE") {
		t.Fatalf("query=%q unexpectedly used Go-engine list comprehension", gotQuery)
	}
}

func TestNativeReasonerEntailsCombinesPredicateEnforcementWithQuarantineFlag(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "p"},
		{Canonical: "child_p", SubPropertyOf: []string{"p"}},
	}, nil, nil)
	var gotQuery string
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if rows, ok := answerProvenanceStatusProbe(q, true); ok {
			return rows, nil
		}
		gotQuery = q
		return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
	}}
	n := NewNativeReasoner(g, reg, Config{})
	n.EnforcePredicate = true

	_, err := n.Entails(context.Background(), Claim{Subject: "s", Predicate: "p", Object: "o"})
	if err != nil {
		t.Fatal(err)
	}
	want := "CALL REASON_ENTAILS('s', 'p', 'o', 4, 'child_p,p', true) YIELD"
	if !strings.Contains(gotQuery, want) {
		t.Fatalf("query=%q, want accepted predicates plus quarantine flag containing %q", gotQuery, want)
	}
}

func TestNativeReasonerDeriveUsesNativeQuarantineFlag(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "p"}}, nil, nil)
	t.Run("default without status keeps legacy arity", func(t *testing.T) {
		var gotQuery string
		g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
			if rows, ok := answerProvenanceStatusProbe(q, false); ok {
				return rows, nil
			}
			gotQuery = q
			return nil, nil
		}}
		n := NewNativeReasoner(g, reg, Config{})

		if _, err := n.Derive(context.Background(), DeriveRequest{Source: "s", Target: "o"}); err != nil {
			t.Fatal(err)
		}
		want := "CALL REASON_DERIVE('s', 'o', 4, 0.05) YIELD"
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q, want legacy arity containing %q", gotQuery, want)
		}
		if strings.Contains(gotQuery, "true) YIELD") {
			t.Fatalf("query=%q unexpectedly contained quarantine flag", gotQuery)
		}
	})

	t.Run("default with status passes native flag", func(t *testing.T) {
		var gotQuery string
		g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
			if rows, ok := answerProvenanceStatusProbe(q, true); ok {
				return rows, nil
			}
			gotQuery = q
			return nil, nil
		}}
		n := NewNativeReasoner(g, reg, Config{})

		if _, err := n.Derive(context.Background(), DeriveRequest{Source: "s", Target: "o"}); err != nil {
			t.Fatal(err)
		}
		want := "CALL REASON_DERIVE('s', 'o', 4, 0.05, true) YIELD"
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q, want native quarantine arity containing %q", gotQuery, want)
		}
		if strings.Contains(gotQuery, "[n IN nodes(p) WHERE") {
			t.Fatalf("query=%q unexpectedly used Go-engine list comprehension", gotQuery)
		}
	})

	t.Run("explicit false keeps legacy arity", func(t *testing.T) {
		var gotQuery string
		g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
			if rows, ok := answerProvenanceStatusProbe(q, true); ok {
				return rows, nil
			}
			gotQuery = q
			return nil, nil
		}}
		n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))

		if _, err := n.Derive(context.Background(), DeriveRequest{Source: "s", Target: "o"}); err != nil {
			t.Fatal(err)
		}
		want := "CALL REASON_DERIVE('s', 'o', 4, 0.05) YIELD"
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q, want legacy arity containing %q", gotQuery, want)
		}
		if strings.Contains(gotQuery, "true) YIELD") {
			t.Fatalf("query=%q unexpectedly contained quarantine flag", gotQuery)
		}
	})
}

func TestNativeReasonerContradictsHonorsExcludeDeduced(t *testing.T) {
	reg := NewPredicateRegistry(
		[]PredicateMeta{{Canonical: "p"}, {Canonical: "q"}},
		nil,
		[]DisjointPair{{A: "p", B: "q"}},
	)
	t.Run("default omits status exclusion without schema support", func(t *testing.T) {
		var gotQuery string
		g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
			if rows, ok := answerProvenanceStatusProbe(q, false); ok {
				return rows, nil
			}
			gotQuery = q
			return nil, nil
		}}
		n := NewNativeReasoner(g, reg, Config{})

		_, _, err := n.Contradicts(context.Background(), Claim{Subject: "s", Predicate: "p", Object: "o"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(gotQuery, ".status") {
			t.Fatalf("query=%q unexpectedly contained status reference", gotQuery)
		}
	})

	t.Run("default excludes deduced and speculative predicate nodes when status exists", func(t *testing.T) {
		var gotQuery string
		g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
			if rows, ok := answerProvenanceStatusProbe(q, true); ok {
				return rows, nil
			}
			gotQuery = q
			return nil, nil
		}}
		n := NewNativeReasoner(g, reg, Config{})

		_, _, err := n.Contradicts(context.Background(), Claim{Subject: "s", Predicate: "p", Object: "o"})
		if err != nil {
			t.Fatal(err)
		}
		want := "lower(coalesce(r.status,'')) NOT IN ['deduced','speculative']"
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q, want status exclusion containing %q", gotQuery, want)
		}
	})

	t.Run("explicit false omits status exclusion", func(t *testing.T) {
		var gotQuery string
		g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
			if rows, ok := answerProvenanceStatusProbe(q, true); ok {
				return rows, nil
			}
			gotQuery = q
			return nil, nil
		}}
		n := NewNativeReasoner(g, reg, Config{MaxHops: 4}.WithExcludeDeduced(false))

		_, _, err := n.Contradicts(context.Background(), Claim{Subject: "s", Predicate: "p", Object: "o"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(gotQuery, "lower(coalesce(r.status,'')) NOT IN ['deduced','speculative']") {
			t.Fatalf("query=%q unexpectedly contained status exclusion", gotQuery)
		}
	})
}
