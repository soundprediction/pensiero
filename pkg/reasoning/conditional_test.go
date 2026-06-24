package reasoning

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/soundprediction/predicato/pkg/ruleschema"
)

func TestConditionalReasonerThreeConditionsEntailsWithProof(t *testing.T) {
	rule := testConditionalRule("r-three",
		[]ruleschema.Pattern{
			testPattern("?x", "has", "?m"),
			testPattern("?m", "causes", "?n"),
			testPattern("?n", "part_of", "?y"),
		},
		testPattern("?x", "supports", "?y"),
	)
	reasoner := newTestConditionalReasoner(t, []ruleschema.Rule{rule}, &fakeConditionOracle{
		facts: []fakeFact{
			{subject: "alpha", predicate: "has", object: "mid-1", confidence: 0.9},
			{subject: "mid-1", predicate: "causes", object: "mid-2", confidence: 0.8},
			{subject: "mid-2", predicate: "part_of", object: "omega", confidence: 0.7},
		},
	}, ConditionalConfig{})

	result, err := reasoner.Entails(context.Background(), Claim{Subject: "alpha", Predicate: "supports", Object: "omega"})
	if err != nil {
		t.Fatalf("Entails error: %v", err)
	}
	if result.Verdict != VerdictEntailed {
		t.Fatalf("verdict = %s, want %s", result.Verdict, VerdictEntailed)
	}
	if result.Best == nil {
		t.Fatalf("missing proof")
	}
	if result.Best.RuleClass != "conditional" {
		t.Fatalf("rule class = %q, want conditional", result.Best.RuleClass)
	}
	if len(result.Best.Steps) != 4 {
		t.Fatalf("steps = %d, want 4: %#v", len(result.Best.Steps), result.Best.Steps)
	}
	last := result.Best.Steps[len(result.Best.Steps)-1]
	if last.Rule != "conditional" || last.EdgeID != "rule:r-three" || last.Predicate != "supports" {
		t.Fatalf("last step = %#v, want conditional rule step", last)
	}
	if result.Confidence <= 0 || result.Confidence > 1 {
		t.Fatalf("confidence = %f, want (0,1]", result.Confidence)
	}
}

func TestConditionalReasonerExceptionVeto(t *testing.T) {
	rule := testConditionalRule("r-veto",
		[]ruleschema.Pattern{testPattern("?x", "qualifies", "?y")},
		testPattern("?x", "supports", "?y"),
		testPattern("?x", "blocked", "?y"),
	)
	reasoner := newTestConditionalReasoner(t, []ruleschema.Rule{rule}, &fakeConditionOracle{
		facts: []fakeFact{
			{subject: "alpha", predicate: "qualifies", object: "omega", confidence: 1},
			{subject: "alpha", predicate: "blocked", object: "omega", confidence: 1},
		},
	}, ConditionalConfig{})

	result, err := reasoner.Entails(context.Background(), Claim{Subject: "alpha", Predicate: "supports", Object: "omega"})
	if err != nil {
		t.Fatalf("Entails error: %v", err)
	}
	if result.Verdict != VerdictUnsupported {
		t.Fatalf("verdict = %s, want %s", result.Verdict, VerdictUnsupported)
	}
}

func TestConditionalReasonerConditionSatisfiedByAnotherRule(t *testing.T) {
	rules := []ruleschema.Rule{
		testConditionalRule("p-from-q",
			[]ruleschema.Pattern{testPattern("?x", "q", "?y")},
			testPattern("?x", "p", "?y"),
		),
		testConditionalRule("q-from-base",
			[]ruleschema.Pattern{testPattern("?x", "base", "?y")},
			testPattern("?x", "q", "?y"),
		),
	}
	reasoner := newTestConditionalReasoner(t, rules, &fakeConditionOracle{
		facts: []fakeFact{{subject: "alpha", predicate: "base", object: "omega", confidence: 1}},
	}, ConditionalConfig{})

	result, err := reasoner.Entails(context.Background(), Claim{Subject: "alpha", Predicate: "p", Object: "omega"})
	if err != nil {
		t.Fatalf("Entails error: %v", err)
	}
	if result.Verdict != VerdictEntailed || result.Best == nil {
		t.Fatalf("result = %#v, want entailed with proof", result)
	}
	var conditionalSteps []string
	for _, step := range result.Best.Steps {
		if step.Rule == "conditional" {
			conditionalSteps = append(conditionalSteps, step.EdgeID)
		}
	}
	want := []string{"rule:q-from-base", "rule:p-from-q"}
	if strings.Join(conditionalSteps, ",") != strings.Join(want, ",") {
		t.Fatalf("conditional steps = %v, want %v", conditionalSteps, want)
	}
}

func TestConditionalReasonerCycleDetectionStops(t *testing.T) {
	rules := []ruleschema.Rule{
		testConditionalRule("p-from-q",
			[]ruleschema.Pattern{testPattern("?x", "q", "?y")},
			testPattern("?x", "p", "?y"),
		),
		testConditionalRule("q-from-p",
			[]ruleschema.Pattern{testPattern("?x", "p", "?y")},
			testPattern("?x", "q", "?y"),
		),
	}
	reasoner := newTestConditionalReasoner(t, rules, &fakeConditionOracle{}, ConditionalConfig{})

	result, err := reasoner.Entails(context.Background(), Claim{Subject: "alpha", Predicate: "p", Object: "omega"})
	if err != nil {
		t.Fatalf("Entails error: %v", err)
	}
	if result.Verdict != VerdictUnsupported {
		t.Fatalf("verdict = %s, want unsupported", result.Verdict)
	}
}

func TestConditionalReasonerCapsStopRunaway(t *testing.T) {
	rule := testConditionalRule("p-from-join",
		[]ruleschema.Pattern{
			testPattern("alpha", "link", "?mid"),
			testPattern("?mid", "target", "omega"),
		},
		testPattern("alpha", "p", "omega"),
	)
	oracle := &fakeConditionOracle{
		facts: []fakeFact{
			{subject: "alpha", predicate: "link", object: "x1", confidence: 1},
			{subject: "alpha", predicate: "link", object: "x2", confidence: 1},
			{subject: "alpha", predicate: "link", object: "x3", confidence: 1},
			{subject: "x3", predicate: "target", object: "omega", confidence: 1},
		},
	}

	cappedBindings := newTestConditionalReasoner(t, []ruleschema.Rule{rule}, oracle, ConditionalConfig{
		MaxBindingsPerCondition: 2,
	})
	result, err := cappedBindings.Entails(context.Background(), Claim{Subject: "alpha", Predicate: "p", Object: "omega"})
	if err != nil {
		t.Fatalf("Entails with binding cap error: %v", err)
	}
	if result.Verdict != VerdictUnsupported {
		t.Fatalf("binding-capped verdict = %s, want unsupported", result.Verdict)
	}

	enoughBindings := newTestConditionalReasoner(t, []ruleschema.Rule{rule}, oracle, ConditionalConfig{
		MaxBindingsPerCondition: 3,
	})
	result, err = enoughBindings.Entails(context.Background(), Claim{Subject: "alpha", Predicate: "p", Object: "omega"})
	if err != nil {
		t.Fatalf("Entails without binding cap error: %v", err)
	}
	if result.Verdict != VerdictEntailed {
		t.Fatalf("uncapped verdict = %s, want entailed", result.Verdict)
	}

	cappedWork := newTestConditionalReasoner(t, []ruleschema.Rule{rule}, oracle, ConditionalConfig{
		MaxBindingsPerCondition: 3,
		MaxWork:                 1,
	})
	result, err = cappedWork.Entails(context.Background(), Claim{Subject: "alpha", Predicate: "p", Object: "omega"})
	if err != nil {
		t.Fatalf("Entails with work cap error: %v", err)
	}
	if result.Verdict != VerdictUnsupported {
		t.Fatalf("work-capped verdict = %s, want unsupported", result.Verdict)
	}
}

func TestLoadAndCompileRulesSkipMissingAndInvalidStructuredPayloads(t *testing.T) {
	valid := testConditionalRule("valid",
		[]ruleschema.Pattern{testPattern("?x", "q", "?y")},
		testPattern("?x", "p", "?y"),
	)
	validJSON, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid rule: %v", err)
	}
	graph := &fakeRuleLoadGraph{
		rows: []map[string]any{
			{"uuid": "missing", "attributes": `{"rule_type":"conditional"}`},
			{"uuid": "invalid-json", "attributes": `{"structured_rule":"{not-json"}`},
			{"uuid": "valid", "attributes": map[string]any{"structured_rule": string(validJSON)}},
		},
	}
	loaded, stats, err := LoadRulesFromGraph(context.Background(), graph)
	if err != nil {
		t.Fatalf("LoadRulesFromGraph error: %v", err)
	}
	if len(loaded) != 1 || stats.Loaded != 1 || stats.SkippedNoStructured != 1 || stats.SkippedInvalid != 1 {
		t.Fatalf("loaded=%d stats=%#v, want one loaded, one missing, one invalid", len(loaded), stats)
	}

	invalid := testConditionalRule("invalid",
		[]ruleschema.Pattern{testPattern("?x", "q", "?y")},
		testPattern("?z", "p", "?y"),
	)
	compiled, err := CompileRules(append(loaded, invalid), nil)
	if err != nil {
		t.Fatalf("CompileRules error: %v", err)
	}
	if compiled.Len() != 1 || compiled.SkippedInvalid != 1 {
		t.Fatalf("compiled len=%d skipped=%d, want len=1 skipped=1", compiled.Len(), compiled.SkippedInvalid)
	}
}

func newTestConditionalReasoner(t *testing.T, rules []ruleschema.Rule, oracle conditionOracle, cfg ConditionalConfig) *ConditionalReasoner {
	t.Helper()
	ruleSet, err := CompileRules(rules, nil)
	if err != nil {
		t.Fatalf("CompileRules error: %v", err)
	}
	if ruleSet.Len() == 0 {
		t.Fatalf("no valid rules compiled")
	}
	return NewConditionalReasoner(nil, oracle, ruleSet, nil, cfg)
}

func testConditionalRule(id string, conditions []ruleschema.Pattern, consequent ruleschema.Pattern, exceptions ...ruleschema.Pattern) ruleschema.Rule {
	return ruleschema.Rule{
		ID:         id,
		RuleType:   "conditional",
		Confidence: 1,
		Conditions: conditions,
		Consequent: consequent,
		Exceptions: exceptions,
	}
}

func testPattern(subject string, predicate string, object string) ruleschema.Pattern {
	return ruleschema.Pattern{
		Subject:   testTerm(subject),
		Predicate: predicate,
		Object:    testTerm(object),
	}
}

func testTerm(value string) ruleschema.Term {
	if strings.HasPrefix(value, "?") {
		return ruleschema.Term{Var: strings.TrimPrefix(value, "?")}
	}
	return ruleschema.Term{Entity: value}
}

type fakeFact struct {
	subject    string
	predicate  string
	object     string
	confidence float64
}

type fakeConditionOracle struct {
	facts []fakeFact
}

func (o *fakeConditionOracle) Holds(_ context.Context, claim Claim) (bool, float64, *Proof, error) {
	for _, fact := range o.facts {
		if sameEntity(fact.subject, claim.Subject) &&
			normKey(fact.predicate) == normKey(claim.Predicate) &&
			sameEntity(fact.object, claim.Object) {
			confidence := fact.confidence
			if confidence <= 0 {
				confidence = 1
			}
			proof := &Proof{
				Source:     fact.subject,
				Target:     fact.object,
				Predicate:  claim.Predicate,
				RuleClass:  "fact",
				Hops:       1,
				Confidence: confidence,
				Steps: []ProofStep{{
					EdgeID:     "fact:" + fact.subject + ":" + fact.predicate + ":" + fact.object,
					Rule:       "fact",
					Predicate:  claim.Predicate,
					Source:     fact.subject,
					Target:     fact.object,
					Confidence: confidence,
				}},
			}
			return true, confidence, proof, nil
		}
	}
	return false, 0, nil, nil
}

func (o *fakeConditionOracle) Bindings(_ context.Context, predicate string, boundSubject string, boundObject string, limit int) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, fact := range o.facts {
		if normKey(fact.predicate) != normKey(predicate) {
			continue
		}
		var candidate string
		switch {
		case strings.TrimSpace(boundSubject) != "" && sameEntity(fact.subject, boundSubject):
			candidate = fact.object
		case strings.TrimSpace(boundObject) != "" && sameEntity(fact.object, boundObject):
			candidate = fact.subject
		default:
			continue
		}
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate)
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type fakeRuleLoadGraph struct {
	rows []map[string]any
}

func (g *fakeRuleLoadGraph) Query(_ context.Context, query string, _ map[string]any) ([]map[string]any, error) {
	switch {
	case strings.Contains(query, "TABLE_INFO('Rule')"):
		return []map[string]any{
			{"name": "uuid"},
			{"name": "attributes"},
		}, nil
	case strings.Contains(query, "MATCH (r:Rule)"):
		return g.rows, nil
	default:
		return nil, nil
	}
}
