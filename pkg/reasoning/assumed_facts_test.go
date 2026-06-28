package reasoning

import (
	"context"
	"testing"

	"github.com/soundprediction/predicato/pkg/ruleschema"
)

// A patient-context rule fires from per-request assumed facts alone — nothing in
// the graph — and its exception (also supplied as an assumed fact) vetoes it.
// This is the DDx seam: IF pregnancy THEN pulmonary embolism has_symptom dyspnea
// UNLESS asthma.
func TestAssumedFactsOracleFiresAndVetoes(t *testing.T) {
	rule := testConditionalRule("pe-dyspnea",
		[]ruleschema.Pattern{testPattern("pregnancy", "present", "patient")},
		testPattern("pulmonary embolism", "has_symptom", "dyspnea"),
		testPattern("asthma", "present", "patient"),
	)
	ruleSet, err := CompileRules([]ruleschema.Rule{rule}, nil)
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	// Empty base oracle: the only way conditions can hold is via assumed facts.
	oracle := NewAssumedFactsOracle(&fakeConditionOracle{}, nil)
	reasoner := NewConditionalReasoner(nil, oracle, ruleSet, nil, ConditionalConfig{})
	claim := Claim{Subject: "pulmonary embolism", Predicate: "has_symptom", Object: "dyspnea"}

	// No assumed facts → inert.
	if res, err := reasoner.Entails(context.Background(), claim); err != nil {
		t.Fatalf("entails(none): %v", err)
	} else if res.Verdict == VerdictEntailed {
		t.Fatalf("verdict=%s, want non-entailed without patient context", res.Verdict)
	}

	// pregnancy present → fires.
	ctx := WithAssumedFacts(context.Background(), []Claim{{Subject: "pregnancy", Predicate: "present", Object: "patient"}})
	res, err := reasoner.Entails(ctx, claim)
	if err != nil {
		t.Fatalf("entails(pregnancy): %v", err)
	}
	if res.Verdict != VerdictEntailed || res.Best == nil || res.Best.RuleClass != "conditional" {
		t.Fatalf("verdict=%s best=%v, want entailed conditional", res.Verdict, res.Best)
	}

	// pregnancy + asthma → exception vetoes.
	ctxVeto := WithAssumedFacts(context.Background(), []Claim{
		{Subject: "pregnancy", Predicate: "present", Object: "patient"},
		{Subject: "asthma", Predicate: "present", Object: "patient"},
	})
	if res, err := reasoner.Entails(ctxVeto, claim); err != nil {
		t.Fatalf("entails(veto): %v", err)
	} else if res.Verdict == VerdictEntailed {
		t.Fatalf("verdict=%s, want vetoed by asthma exception", res.Verdict)
	}
}
