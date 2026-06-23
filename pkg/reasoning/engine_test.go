package reasoning

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

type mockGraph struct {
	query func(string, map[string]any) ([]map[string]any, error)
}

func (m mockGraph) Query(_ context.Context, q string, params map[string]any) ([]map[string]any, error) {
	return m.query(q, params)
}

func engineWithRegistryAndRows(reg *PredicateRegistry, rows ...map[string]any) *Engine {
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if strings.Contains(q, "OntologyDisjoint") {
			return nil, nil
		}
		if _, restricted := params["preds"]; restricted {
			return nil, nil
		}
		return rows, nil
	}}
	return NewEngine(g, reg, Config{})
}

func engineWithRows(rows ...map[string]any) *Engine {
	return engineWithRegistryAndRows(DefaultMedicalRegistry(), rows...)
}

func proofRow(target string, preds ...string) map[string]any {
	ids := make([]string, 0, len(preds))
	confs := make([]float64, 0, len(preds))
	for i := range preds {
		ids = append(ids, "e"+strconv.Itoa(i+1))
		confs = append(confs, 1.0)
	}
	return map[string]any{
		"predicates": preds,
		"step_ids":   ids,
		"confs":      confs,
		"target":     target,
		"hops":       len(preds),
	}
}

func TestEngineEntailsDirectPredicate(t *testing.T) {
	e := engineWithRows(proofRow("headache", "treats"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "aspirin", Predicate: "treats", Object: "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "treats" {
		t.Fatalf("best=%+v, want effective predicate treats", res.Best)
	}
}

func TestEngineEntailsSubPropertyOnlyUpward(t *testing.T) {
	t.Run("specific edge entails weaker claim", func(t *testing.T) {
		e := engineWithRows(proofRow("disease", "symptom_of"))

		res, err := e.Entails(context.Background(), Claim{
			Subject: "fever", Predicate: "associated_with", Object: "disease",
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Verdict != VerdictEntailed {
			t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
		}
		if res.Best == nil || res.Best.Predicate != "symptom_of" {
			t.Fatalf("best=%+v, want effective predicate symptom_of", res.Best)
		}
	})

	t.Run("weaker edge does not entail stronger claim", func(t *testing.T) {
		e := engineWithRows(proofRow("disease", "associated_with"))

		res, err := e.Entails(context.Background(), Claim{
			Subject: "fever", Predicate: "symptom_of", Object: "disease",
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Verdict != VerdictUnsupported {
			t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictUnsupported)
		}
	})
}

func TestEngineEntailsRejectsPathWithWrongPredicate(t *testing.T) {
	e := engineWithRows(proofRow("headache", "causes"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "aspirin", Predicate: "treats", Object: "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictUnsupported {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictUnsupported)
	}
}

func TestEngineEntailsRejectsUnreducedPath(t *testing.T) {
	e := engineWithRows(proofRow("condition", "symptom_of", "causes"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "finding", Predicate: "associated_with", Object: "condition",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictUnsupported {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictUnsupported)
	}
}

func TestEngineEntailsCompositionPredicate(t *testing.T) {
	e := engineWithRows(proofRow("fever", "is_a", "has_symptom"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "influenza", Predicate: "has_symptom", Object: "fever",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "has_symptom" {
		t.Fatalf("best=%+v, want effective predicate has_symptom", res.Best)
	}
}

func TestEngineEntailsCompositionResultSubProperty(t *testing.T) {
	e := engineWithRows(proofRow("rash", "is_a", "has_phenotype"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "influenza", Predicate: "has_symptom", Object: "rash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "has_phenotype" {
		t.Fatalf("best=%+v, want effective predicate has_phenotype", res.Best)
	}
}

func TestEngineEntailsTransitiveIsAChain(t *testing.T) {
	e := engineWithRows(proofRow("disease", "is_a", "is_a", "is_a"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "influenza", Predicate: "is_a", Object: "disease",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "is_a" {
		t.Fatalf("best=%+v, want effective predicate is_a", res.Best)
	}
}

func TestEngineEntailsTransitiveSubPropertyVariants(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Raw: "is_a", Canonical: "is_a", Chars: Transitive},
		{Raw: "specializes", Canonical: "specializes", SubPropertyOf: []string{"is_a"}},
		{Raw: "narrows", Canonical: "narrows", SubPropertyOf: []string{"is_a"}},
	}, nil, nil)
	e := engineWithRegistryAndRows(reg, proofRow("disease", "specializes", "narrows"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "influenza", Predicate: "is_a", Object: "disease",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "is_a" {
		t.Fatalf("best=%+v, want effective predicate is_a", res.Best)
	}
}

func TestEngineDeriveWithoutPredicateKeepsUnreducedProof(t *testing.T) {
	e := engineWithRows(proofRow("condition", "symptom_of", "causes"))

	proofs, err := e.Derive(context.Background(), DeriveRequest{
		Source: "finding", Target: "condition",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(proofs) != 1 {
		t.Fatalf("proofs=%d, want 1", len(proofs))
	}
	if proofs[0].Predicate != "" {
		t.Fatalf("predicate=%q, want empty for unreduced unfiltered path", proofs[0].Predicate)
	}
}

func TestEngineEntailsCanonicalizesClaimPredicateAlias(t *testing.T) {
	e := engineWithRows(proofRow("disease", "symptom_of"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "fever", Predicate: "associated with", Object: "disease",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "symptom_of" {
		t.Fatalf("best=%+v, want effective predicate symptom_of", res.Best)
	}
}

func TestEngineEntailsInversePredicate(t *testing.T) {
	e := engineWithRows(proofRow("headache", "treated_by"))

	res, err := e.Entails(context.Background(), Claim{
		Subject: "aspirin", Predicate: "treats", Object: "headache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictEntailed)
	}
	if res.Best == nil || res.Best.Predicate != "treated_by" {
		t.Fatalf("best=%+v, want effective predicate treated_by", res.Best)
	}
}

func TestEngineDeriveInversePredicateRequiresIncludeInverse(t *testing.T) {
	e := engineWithRows(proofRow("headache", "treated_by"))

	proofs, err := e.Derive(context.Background(), DeriveRequest{
		Source: "aspirin", Target: "headache", Predicate: "treats",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(proofs) != 0 {
		t.Fatalf("proofs=%d, want 0 without IncludeInverse", len(proofs))
	}

	proofs, err = e.Derive(context.Background(), DeriveRequest{
		Source: "aspirin", Target: "headache", Predicate: "treats", IncludeInverse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(proofs) != 1 {
		t.Fatalf("proofs=%d, want 1 with IncludeInverse", len(proofs))
	}
	if proofs[0].Predicate != "treated_by" {
		t.Fatalf("predicate=%q, want treated_by", proofs[0].Predicate)
	}
}

func TestPredicateEntailsCanonicalizesAndHandlesCycles(t *testing.T) {
	reg := NewPredicateRegistry([]PredicateMeta{
		{Raw: "p", Canonical: "p", SubPropertyOf: []string{"q"}},
		{Raw: "q", Canonical: "q", SubPropertyOf: []string{"p"}},
		{Raw: "surface r", Canonical: "r", SubPropertyOf: []string{"q"}},
	}, nil, nil)

	if !predicateEntails(reg, "surface r", "p") {
		t.Fatal("surface r should entail p through canonicalized cycle")
	}
	if predicateEntails(reg, "p", "r") {
		t.Fatal("p should not entail more-specific r")
	}
}

func TestEffectivePredicateRejectsEmptyPredicates(t *testing.T) {
	reg := DefaultMedicalRegistry()
	cases := [][]ProofStep{
		nil,
		{},
		{{Predicate: "  "}},
		{{Predicate: "is_a"}, {Predicate: "  "}},
	}
	for _, steps := range cases {
		if pred, ok := effectivePredicate(reg, steps); ok {
			t.Fatalf("effectivePredicate(%+v)=(%q,true), want false", steps, pred)
		}
	}
}

func TestEngineContradictionOverridesSupport(t *testing.T) {
	supportQueries := 0
	g := mockGraph{query: func(q string, params map[string]any) ([]map[string]any, error) {
		if strings.Contains(q, "OntologyDisjoint") {
			if params["a"] == "bad_class" && params["b"] == "bad_object" {
				return []map[string]any{{"source": "test"}}, nil
			}
			return nil, nil
		}
		if _, restricted := params["preds"]; restricted {
			return []map[string]any{proofRow("bad_class", "is_a")}, nil
		}
		supportQueries++
		return []map[string]any{proofRow("bad_object", "treats")}, nil
	}}
	e := NewEngine(g, DefaultMedicalRegistry(), Config{})

	res, err := e.Entails(context.Background(), Claim{
		Subject: "thing", Predicate: "treats", Object: "bad_object",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictContradicted {
		t.Fatalf("verdict=%s, want %s", res.Verdict, VerdictContradicted)
	}
	if supportQueries != 0 {
		t.Fatalf("support queries=%d, want contradiction to short-circuit support", supportQueries)
	}
	if res.Best == nil || res.Best.Predicate != "disjoint_with" {
		t.Fatalf("best=%+v, want disjoint proof", res.Best)
	}
}
