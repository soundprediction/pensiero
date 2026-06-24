package reasoning

import (
	"context"
	"testing"
)

// goldAsthmaRuleJSON is a real rule converted verbatim from the IE training data
// (rule_extraction/data, StatPearls-derived medical split), via
// convert_gold_rules_to_ruleschema.py. It is the clause-atom ruleschema form of
// the gold annotation:
//
//	IF "moderate-to-severe asthma"
//	THEN "recommend fluticasone/salmeterol (Advair)"
//	UNLESS "concomitant COPD"
//
// This test proves pensiero processes the gold rules through the exact production
// path — LoadRulesFromGraph (reading attributes.structured_rule) → CompileRules →
// ConditionalReasoner.Entails — with NO extractor inference, LLM, or predicato
// ingestion pipeline involved.
const goldAsthmaRuleJSON = `{"schema_version":1,"id":"gold-5-2","rule_type":"conditional","confidence":1.0,` +
	`"conditions":[{"subject":{"entity":"moderate-to-severe asthma"},"predicate":"holds","object":{"entity":"true"}}],` +
	`"consequent":{"subject":{"entity":"recommend fluticasone/salmeterol (Advair)"},"predicate":"holds","object":{"entity":"true"}},` +
	`"exceptions":[{"subject":{"entity":"concomitant COPD"},"predicate":"holds","object":{"entity":"true"}}],` +
	`"provenance":{"source_id":"gold-5-2","chunk_index":0,"model":"gold","source_attribution":"StatPearls IE training data"}}`

func goldReasoner(t *testing.T, facts []fakeFact) *ConditionalReasoner {
	t.Helper()
	// Production graph-load path: rules live as Rule nodes with the ruleschema JSON
	// under attributes.structured_rule.
	graph := &fakeRuleLoadGraph{rows: []map[string]any{
		{"uuid": "gold-5-2", "attributes": map[string]any{"structured_rule": goldAsthmaRuleJSON}},
	}}
	loaded, stats, err := LoadRulesFromGraph(context.Background(), graph)
	if err != nil {
		t.Fatalf("LoadRulesFromGraph error: %v", err)
	}
	if stats.Loaded != 1 || len(loaded) != 1 {
		t.Fatalf("loaded=%d stats=%#v, want exactly one gold rule loaded", len(loaded), stats)
	}
	return newTestConditionalReasoner(t, loaded, &fakeConditionOracle{facts: facts}, ConditionalConfig{})
}

// The consequent fires when the condition clause is a known fact.
func TestGoldRuleFiresWhenConditionHolds(t *testing.T) {
	reasoner := goldReasoner(t, []fakeFact{
		{subject: "moderate-to-severe asthma", predicate: "holds", object: "true", confidence: 1},
	})

	result, err := reasoner.Entails(context.Background(), Claim{
		Subject: "recommend fluticasone/salmeterol (Advair)", Predicate: "holds", Object: "true",
	})
	if err != nil {
		t.Fatalf("Entails error: %v", err)
	}
	if result.Verdict != VerdictEntailed || result.Best == nil {
		t.Fatalf("verdict=%s best=%v, want entailed with proof", result.Verdict, result.Best)
	}
	if result.Best.RuleClass != "conditional" {
		t.Fatalf("rule class=%q, want conditional", result.Best.RuleClass)
	}
	last := result.Best.Steps[len(result.Best.Steps)-1]
	if last.EdgeID != "rule:gold-5-2" {
		t.Fatalf("last step=%#v, want the gold rule step", last)
	}
}

// The exception clause vetoes the rule even though the condition holds.
func TestGoldRuleVetoedByException(t *testing.T) {
	reasoner := goldReasoner(t, []fakeFact{
		{subject: "moderate-to-severe asthma", predicate: "holds", object: "true", confidence: 1},
		{subject: "concomitant COPD", predicate: "holds", object: "true", confidence: 1},
	})

	result, err := reasoner.Entails(context.Background(), Claim{
		Subject: "recommend fluticasone/salmeterol (Advair)", Predicate: "holds", Object: "true",
	})
	if err != nil {
		t.Fatalf("Entails error: %v", err)
	}
	if result.Verdict != VerdictUnsupported {
		t.Fatalf("verdict=%s, want unsupported (exception veto)", result.Verdict)
	}
}

// Without the condition fact, the rule does not fire.
func TestGoldRuleInertWithoutCondition(t *testing.T) {
	reasoner := goldReasoner(t, nil)

	result, err := reasoner.Entails(context.Background(), Claim{
		Subject: "recommend fluticasone/salmeterol (Advair)", Predicate: "holds", Object: "true",
	})
	if err != nil {
		t.Fatalf("Entails error: %v", err)
	}
	if result.Verdict == VerdictEntailed {
		t.Fatalf("verdict=%s, want non-entailed without the condition fact", result.Verdict)
	}
}
