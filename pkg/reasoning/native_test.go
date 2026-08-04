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
	// Capture EVERY call: an unsupported forward result is now followed by an
	// inverse re-ask in the stored direction (see
	// TestNativeReasonerRecoversInverseClaimInStoredDirection), so asserting on a
	// single captured query would test the wrong call.
	var entailsQueries []string
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if strings.Contains(q, "REASON_ENTAILS") {
			entailsQueries = append(entailsQueries, q)
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
	if len(entailsQueries) == 0 {
		t.Fatal("no REASON_ENTAILS call was made")
	}
	want := "CALL REASON_ENTAILS('flu', 'has symptom', 'fever', 4, 'has_phenotype,has_symptom')"
	if !strings.Contains(entailsQueries[0], want) {
		t.Fatalf("forward query=%q, want to contain %q", entailsQueries[0], want)
	}
	// And the inverse re-ask must carry the same accepted set, swapped ends.
	wantInv := "CALL REASON_ENTAILS('fever', 'symptom_of', 'flu', 4, 'phenotype_of,symptom_of')"
	if len(entailsQueries) < 2 || !strings.Contains(entailsQueries[1], wantInv) {
		t.Fatalf("inverse re-ask missing or wrong; queries=%v", entailsQueries)
	}
}

func TestNativeReasonerEntailsEmptyAcceptedPredicateSetFailsClosed(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "known"}}, nil, nil)
	entailsCalls := 0
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if rows, ok := answerProvenanceStatusProbe(q, false); ok {
			return rows, nil
		}
		if strings.Contains(q, "REASON_ENTAILS") {
			entailsCalls++
			return []map[string]any{{
				"verdict":    string(VerdictEntailed),
				"confidence": 0.9,
				"proof":      `[{"rule":"composition","predicate":"known","source":"s","target":"o"}]`,
			}}, nil
		}
		return nil, nil
	}}
	n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))

	res, err := n.Entails(context.Background(), Claim{Subject: "s", Predicate: "unknown", Object: "o"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictUnsupported {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictUnsupported)
	}
	if entailsCalls != 0 {
		t.Fatalf("REASON_ENTAILS calls=%d, want 0 for empty accepted-predicate set", entailsCalls)
	}
}

func TestNativeReasonerEntailsRejectsMismatchedPathPredicate(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "treats"},
		{Canonical: "causes"},
	}, nil, nil)
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if rows, ok := answerProvenanceStatusProbe(q, false); ok {
			return rows, nil
		}
		if strings.Contains(q, "REASON_ENTAILS") {
			return []map[string]any{{
				"verdict":    string(VerdictEntailed),
				"confidence": 0.9,
				"proof":      `[{"rule":"composition","predicate":"causes","source":"aspirin","target":"headache"}]`,
			}}, nil
		}
		return nil, nil
	}}
	n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))

	res, err := n.Entails(context.Background(), Claim{
		Subject: "aspirin", Predicate: "treats", Object: "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictUnsupported {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictUnsupported)
	}
}

func TestNativeReasonerEntailsKeepsGenuinePredicateMatch(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{{Canonical: "treats"}}, nil, nil)
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if rows, ok := answerProvenanceStatusProbe(q, false); ok {
			return rows, nil
		}
		if strings.Contains(q, "REASON_ENTAILS") {
			return []map[string]any{{
				"verdict":    string(VerdictEntailed),
				"confidence": 0.9,
				"proof":      `[{"rule":"composition","predicate":"treats","source":"aspirin","target":"headache"}]`,
			}}, nil
		}
		return nil, nil
	}}
	n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))

	res, err := n.Entails(context.Background(), Claim{
		Subject: "aspirin", Predicate: "treats", Object: "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "treats" {
		t.Fatalf("best=%+v, want verified treats proof", res.Best)
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
	n.SetLegacyPathExistence(true)

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
	t.Run("without status keeps accepted-predicate arity", func(t *testing.T) {
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
		want := "CALL REASON_ENTAILS('s', 'p', 'o', 4, 'p') YIELD"
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q, want accepted-predicate arity containing %q", gotQuery, want)
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
	want := "CALL REASON_ENTAILS('s', 'p', 'o', 4, 'p', true) YIELD"
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

// Path traversal in the reasoning extension is DIRECTED: it previously walked
// edges undirected, which entailed a claim AND its reverse from a single stored
// edge and emitted a proof for a relationship the graph does not contain. The
// cost of directedness is that a claim stated in the inverse direction of how the
// graph stores it no longer proves by walking backwards — so Entails re-asks in
// the STORED direction using the registry's declared inverse.
func TestNativeReasonerRecoversInverseClaimInStoredDirection(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "treats", InverseOf: "treated_by"},
		{Canonical: "treated_by", InverseOf: "treats"},
	}, nil, nil)

	// The graph stores only: drug -treats-> disease.
	var asked []string
	g := mockGraph{query: func(q string, _ map[string]any) ([]map[string]any, error) {
		if !strings.Contains(q, "REASON_ENTAILS") {
			return nil, nil
		}
		asked = append(asked, q)
		if strings.Contains(q, "'drug', 'treats', 'disease'") {
			return []map[string]any{{"verdict": string(VerdictEntailed), "confidence": 0.8,
				"proof": `[{"edge_id":"e1","rule":"composition","predicate":"treats","source":"drug","target":"disease","confidence":0.8}]`}}, nil
		}
		return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
	}}
	n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))

	// "disease treated_by drug" is the legitimate inverse — it must still prove,
	// via a re-ask in the stored direction.
	res, err := n.Entails(context.Background(), Claim{Subject: "disease", Predicate: "treated_by", Object: "drug"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("legitimate inverse claim should still entail, got %q", res.Verdict)
	}
	if len(asked) != 2 {
		t.Fatalf("want a forward attempt then an inverse re-ask, got %d queries: %v", len(asked), asked)
	}
	// The proof must describe the STORED direction, not the claim's direction —
	// that honesty is the entire point of the change.
	if res.Best == nil || res.Best.Source != "drug" || res.Best.Target != "disease" {
		t.Fatalf("proof should cite the stored direction drug->disease, got %+v", res.Best)
	}
}

// The inverse re-ask must not become a back door to the old direction-blind
// behaviour: a REVERSED claim (not an inverse one) must stay unsupported.
func TestNativeReasonerRejectsReversedClaim(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Canonical: "treats", InverseOf: "treated_by"},
		{Canonical: "treated_by", InverseOf: "treats"},
	}, nil, nil)

	g := mockGraph{query: func(q string, _ map[string]any) ([]map[string]any, error) {
		if !strings.Contains(q, "REASON_ENTAILS") {
			return nil, nil
		}
		// Only drug -treats-> disease exists. Directed traversal finds nothing else.
		if strings.Contains(q, "'drug', 'treats', 'disease'") {
			return []map[string]any{{"verdict": string(VerdictEntailed), "confidence": 0.8,
				"proof": `[{"edge_id":"e1","rule":"composition","predicate":"treats","source":"drug","target":"disease","confidence":0.8}]`}}, nil
		}
		return []map[string]any{{"verdict": string(VerdictUnsupported), "confidence": 0.0, "proof": "[]"}}, nil
	}}
	n := NewNativeReasoner(g, reg, Config{}.WithExcludeDeduced(false))

	// "disease treats drug" is the REVERSE of the stored edge, not its inverse.
	// Its inverse re-ask is "drug treated_by disease", which the graph also does
	// not assert — so this must stay unsupported.
	res, err := n.Entails(context.Background(), Claim{Subject: "disease", Predicate: "treats", Object: "drug"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict == VerdictEntailed {
		t.Fatal("a reversed claim must not entail — that is the fabricated-citation bug")
	}
}
