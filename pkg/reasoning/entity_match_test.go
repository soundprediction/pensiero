package reasoning

import (
	"context"
	"testing"

	"github.com/soundprediction/predicato/pkg/ruleschema"
)

func TestNormalizeEntityForMatch(t *testing.T) {
	cases := map[string]string{
		"Pulmonary Embolism (PE)":         "pulmonary embolism",
		"Pulmonary Embolism":              "pulmonary embolism",
		"second-trimester pregnant woman": "second trimester pregnant woman",
		"Second-Trimester Pregnant Woman": "second trimester pregnant woman",
		"  Acute  PE!  ":                  "acute pe",
	}
	for in, want := range cases {
		if got := normalizeEntityForMatch(in); got != want {
			t.Errorf("normalizeEntityForMatch(%q)=%q want %q", in, got, want)
		}
	}
	if !entityMatches("Pulmonary Embolism (PE)", "pulmonary embolism") {
		t.Error("PE variant should match")
	}
	if entityMatches("pulmonary embolism", "pneumonia") {
		t.Error("distinct entities must not match")
	}
}

// A rule authored with the bare name fires for a claim that uses a parenthetical
// qualifier (and vice versa), via the rule-layer entity normalization.
func TestConditionalRuleMatchesEntityVariant(t *testing.T) {
	rule := testConditionalRule("pe-variant",
		[]ruleschema.Pattern{testPattern("pregnancy", "present", "patient")},
		testPattern("Pulmonary Embolism", "has_symptom", "pleuritic chest pain"),
	)
	ruleSet, err := CompileRules([]ruleschema.Rule{rule}, nil)
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	reasoner := NewConditionalReasoner(nil, NewAssumedFactsOracle(&fakeConditionOracle{}, nil), ruleSet, nil, ConditionalConfig{})
	ctx := WithAssumedFacts(context.Background(), []Claim{{Subject: "Pregnancy", Predicate: "present", Object: "patient"}})

	// Claim uses the parenthetical form; rule uses the bare form.
	res, err := reasoner.Entails(ctx, Claim{Subject: "Pulmonary Embolism (PE)", Predicate: "has_symptom", Object: "Pleuritic chest pain"})
	if err != nil {
		t.Fatalf("Entails: %v", err)
	}
	if res.Verdict != VerdictEntailed {
		t.Fatalf("verdict=%s, want entailed across the entity-name variant", res.Verdict)
	}
}
