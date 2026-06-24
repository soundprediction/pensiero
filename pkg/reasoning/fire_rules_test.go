package reasoning

import (
	"context"
	"testing"

	"github.com/soundprediction/predicato/pkg/ruleschema"
)

// FireRules forward-chains a management rule from the patient's assumed facts:
// IF asthma THEN patient -recommended-> Advair UNLESS COPD.
func TestFireRulesManagement(t *testing.T) {
	rules := []ruleschema.Rule{
		testConditionalRule("asthma-advair",
			[]ruleschema.Pattern{testPattern("moderate-to-severe asthma", "present", "patient")},
			testPattern("patient", "recommended", "Advair"),
			testPattern("COPD", "present", "patient"),
		),
		// An unrelated rule that should NOT fire (its condition isn't present).
		testConditionalRule("htn-lisinopril",
			[]ruleschema.Pattern{testPattern("hypertension", "present", "patient")},
			testPattern("patient", "recommended", "lisinopril"),
		),
	}
	ruleSet, err := CompileRules(rules, nil)
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	reasoner := NewConditionalReasoner(nil, NewAssumedFactsOracle(&fakeConditionOracle{}, nil), ruleSet, nil, ConditionalConfig{})

	// Patient has asthma → asthma-advair fires; htn-lisinopril does not.
	ctx := WithAssumedFacts(context.Background(), []Claim{{Subject: "moderate-to-severe asthma", Predicate: "present", Object: "patient"}})
	fired, err := reasoner.FireRules(ctx, 100)
	if err != nil {
		t.Fatalf("FireRules: %v", err)
	}
	if len(fired) != 1 {
		t.Fatalf("fired %d rules, want 1: %#v", len(fired), fired)
	}
	f := fired[0]
	if f.RuleID != "asthma-advair" || f.Consequent.Predicate != "recommended" || f.Consequent.Object != "Advair" {
		t.Fatalf("unexpected fired rule: %#v", f)
	}
	if f.Proof == nil || f.Proof.RuleClass != "conditional" {
		t.Fatalf("missing conditional proof: %#v", f.Proof)
	}

	// Add the COPD exception → the rule is vetoed, nothing fires.
	ctxVeto := WithAssumedFacts(context.Background(), []Claim{
		{Subject: "moderate-to-severe asthma", Predicate: "present", Object: "patient"},
		{Subject: "COPD", Predicate: "present", Object: "patient"},
	})
	fired, err = reasoner.FireRules(ctxVeto, 100)
	if err != nil {
		t.Fatalf("FireRules veto: %v", err)
	}
	if len(fired) != 0 {
		t.Fatalf("fired %d rules, want 0 (vetoed): %#v", len(fired), fired)
	}
}
