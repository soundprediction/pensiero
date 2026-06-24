package reasoning

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/soundprediction/predicato/pkg/ruleschema"
)

const (
	defaultConditionalMaxRuleDepth            = 3
	defaultConditionalMaxRulesConsidered      = 32
	defaultConditionalMaxBindingsPerCondition = 32
	defaultConditionalMaxWork                 = 512
	defaultConditionalDecay                   = 0.9
	defaultConditionalExceptionThreshold      = 0.6
)

// conditionOracle is the rule layer's graph seam. Tests provide a fake oracle;
// production uses GraphConditionOracle over GraphQuerier.
type conditionOracle interface {
	// Holds reports whether a fully-grounded directed relation holds, with
	// confidence and an auditable proof when available.
	Holds(ctx context.Context, claim Claim) (ok bool, confidence float64, proof *Proof, err error)
	// Bindings enumerates entities satisfying a partially-bound directed pattern.
	// At most one endpoint may be bound; callers cap limit tightly.
	Bindings(ctx context.Context, predicate string, boundSubject, boundObject string, limit int) ([]string, error)
}

// ConditionalConfig bounds backward-chaining work. Zero values use defaults.
type ConditionalConfig struct {
	MaxRuleDepth            int
	MaxRulesConsidered      int
	MaxBindingsPerCondition int
	MaxWork                 int
	Decay                   float64
	ExceptionThreshold      float64
}

func (c ConditionalConfig) withDefaults() ConditionalConfig {
	if c.MaxRuleDepth <= 0 {
		c.MaxRuleDepth = defaultConditionalMaxRuleDepth
	}
	if c.MaxRulesConsidered <= 0 {
		c.MaxRulesConsidered = defaultConditionalMaxRulesConsidered
	}
	if c.MaxBindingsPerCondition <= 0 {
		c.MaxBindingsPerCondition = defaultConditionalMaxBindingsPerCondition
	}
	if c.MaxWork <= 0 {
		c.MaxWork = defaultConditionalMaxWork
	}
	if c.Decay <= 0 || c.Decay > 1 {
		c.Decay = defaultConditionalDecay
	}
	if c.ExceptionThreshold <= 0 || c.ExceptionThreshold > 1 {
		c.ExceptionThreshold = defaultConditionalExceptionThreshold
	}
	return c
}

// CompiledRule is a normalized ruleschema.Rule plus its indexed consequent
// predicate.
type CompiledRule struct {
	Rule                ruleschema.Rule
	ConsequentPredicate string
}

// RuleSet is the compiled conditional rule index.
type RuleSet struct {
	Rules          []CompiledRule
	SkippedInvalid int

	byConsequent map[string][]int
}

// Len returns the number of valid compiled rules.
func (rs *RuleSet) Len() int {
	if rs == nil {
		return 0
	}
	return len(rs.Rules)
}

func (rs *RuleSet) rulesFor(predicate string) []CompiledRule {
	if rs == nil || len(rs.Rules) == 0 {
		return nil
	}
	idxs := rs.byConsequent[normKey(predicate)]
	out := make([]CompiledRule, 0, len(idxs))
	for _, idx := range idxs {
		if idx >= 0 && idx < len(rs.Rules) {
			out = append(out, rs.Rules[idx])
		}
	}
	return out
}

// CompileRules validates, normalizes, canonicalizes, and indexes conditional
// rules. Invalid rules are skipped and counted rather than making the whole rule
// layer unusable.
func CompileRules(rules []ruleschema.Rule, reg *PredicateRegistry) (*RuleSet, error) {
	rs := &RuleSet{byConsequent: map[string][]int{}}
	for i, rule := range rules {
		ruleschema.Normalize(&rule)
		if strings.TrimSpace(rule.ID) == "" {
			rule.ID = fmt.Sprintf("rule-%d", i+1)
		}
		if err := ruleschema.Validate(rule); err != nil {
			rs.SkippedInvalid++
			continue
		}
		canonicalizeRule(&rule, reg)
		consequentPredicate := canonicalPatternPredicate(rule.Consequent, reg)
		if consequentPredicate == "" {
			rs.SkippedInvalid++
			continue
		}
		compiled := CompiledRule{
			Rule:                rule,
			ConsequentPredicate: consequentPredicate,
		}
		idx := len(rs.Rules)
		rs.Rules = append(rs.Rules, compiled)
		rs.byConsequent[normKey(consequentPredicate)] = append(rs.byConsequent[normKey(consequentPredicate)], idx)
	}
	return rs, nil
}

func canonicalizeRule(rule *ruleschema.Rule, reg *PredicateRegistry) {
	for i := range rule.Conditions {
		canonicalizePattern(&rule.Conditions[i], reg)
	}
	canonicalizePattern(&rule.Consequent, reg)
	for i := range rule.Exceptions {
		canonicalizePattern(&rule.Exceptions[i], reg)
	}
}

func canonicalizePattern(pattern *ruleschema.Pattern, reg *PredicateRegistry) {
	raw := strings.TrimSpace(pattern.PredicateCanonical)
	if raw == "" {
		raw = pattern.Predicate
	}
	canon := canonicalPredicate(reg, raw)
	pattern.PredicateCanonical = canon
	pattern.Predicate = canon
}

func canonicalPatternPredicate(pattern ruleschema.Pattern, reg *PredicateRegistry) string {
	if strings.TrimSpace(pattern.PredicateCanonical) != "" {
		return canonicalPredicate(reg, pattern.PredicateCanonical)
	}
	return canonicalPredicate(reg, pattern.Predicate)
}

// ConditionalReasoner augments a base reasoner with bounded backward-chaining
// over compiled conditional rules.
type ConditionalReasoner struct {
	base   Reasoner
	oracle conditionOracle
	rules  *RuleSet
	reg    *PredicateRegistry
	cfg    ConditionalConfig
}

// NewConditionalReasoner wraps base with the conditional-rule layer.
func NewConditionalReasoner(base Reasoner, oracle conditionOracle, rules *RuleSet, reg *PredicateRegistry, cfg ConditionalConfig) *ConditionalReasoner {
	return &ConditionalReasoner{
		base:   base,
		oracle: oracle,
		rules:  rules,
		reg:    reg,
		cfg:    cfg.withDefaults(),
	}
}

func (r *ConditionalReasoner) Name() string {
	if r == nil || r.base == nil {
		return "conditional"
	}
	return r.base.Name() + "+conditional"
}

func (r *ConditionalReasoner) Derive(ctx context.Context, req DeriveRequest) ([]Proof, error) {
	if r == nil || r.base == nil {
		return nil, nil
	}
	return r.base.Derive(ctx, req)
}

func (r *ConditionalReasoner) Contradicts(ctx context.Context, claim Claim) (bool, *Proof, error) {
	if r == nil || r.base == nil {
		return false, nil, nil
	}
	return r.base.Contradicts(ctx, claim)
}

func (r *ConditionalReasoner) Entails(ctx context.Context, claim Claim) (EntailResult, error) {
	if r == nil {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}
	state, ok := ctx.Value(conditionalStateContextKey{}).(*conditionalState)
	if !ok || state == nil {
		state = &conditionalState{active: map[string]bool{}}
	}
	ctx = context.WithValue(ctx, conditionalStateContextKey{}, state)
	return r.entails(ctx, canonicalClaim(claim, r.reg), state)
}

func (r *ConditionalReasoner) entails(ctx context.Context, claim Claim, state *conditionalState) (EntailResult, error) {
	if err := ctx.Err(); err != nil {
		return EntailResult{}, err
	}
	if r.base != nil {
		base, err := r.base.Entails(ctx, claim)
		if err != nil {
			return EntailResult{}, err
		}
		if base.Verdict == VerdictEntailed || base.Verdict == VerdictContradicted {
			return base, nil
		}
	}
	return r.entailsByRules(ctx, claim, state)
}

func (r *ConditionalReasoner) entailsByRules(ctx context.Context, claim Claim, state *conditionalState) (EntailResult, error) {
	if r.rules == nil || r.rules.Len() == 0 || !groundedClaim(claim) {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}
	candidates := r.rules.rulesFor(claim.Predicate)
	if len(candidates) == 0 {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}

	var proofs []Proof
	for _, rule := range candidates {
		if err := ctx.Err(); err != nil {
			return EntailResult{}, err
		}
		if !state.considerRule(r.cfg) {
			break
		}
		proof, ok, err := r.tryRule(ctx, rule, claim, state)
		if err != nil {
			return EntailResult{}, err
		}
		if ok {
			proofs = append(proofs, proof)
		}
	}
	if len(proofs) == 0 {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}
	sort.SliceStable(proofs, func(i, j int) bool {
		return proofs[i].Confidence > proofs[j].Confidence
	})
	best := proofs[0]
	return EntailResult{
		Verdict:    VerdictEntailed,
		Confidence: best.Confidence,
		Best:       &best,
		All:        proofs,
	}, nil
}

func (r *ConditionalReasoner) tryRule(ctx context.Context, compiled CompiledRule, claim Claim, state *conditionalState) (Proof, bool, error) {
	rule := compiled.Rule
	if rule.Consequent.Negated || state.depth >= r.cfg.MaxRuleDepth {
		state.capped = state.capped || state.depth >= r.cfg.MaxRuleDepth
		return Proof{}, false, nil
	}
	bindings := map[string]string{}
	if !unifyConsequent(rule.Consequent, claim, bindings, r.reg) {
		return Proof{}, false, nil
	}

	key := conditionalActiveKey(rule.ID, claim)
	if state.active[key] {
		return Proof{}, false, nil
	}
	state.active[key] = true
	state.depth++
	defer func() {
		state.depth--
		delete(state.active, key)
	}()

	branches, err := r.evalConditions(ctx, rule.Conditions, bindings, nil, state)
	if err != nil {
		return Proof{}, false, err
	}
	var best Proof
	ok := false
	for _, branch := range branches {
		veto, err := r.exceptionVeto(ctx, rule.Exceptions, branch.bindings, state)
		if err != nil {
			return Proof{}, false, err
		}
		if veto {
			continue
		}
		proof := r.composeProof(rule, claim, branch)
		if !ok || proof.Confidence > best.Confidence {
			best = proof
			ok = true
		}
	}
	return best, ok, nil
}

func (r *ConditionalReasoner) evalConditions(ctx context.Context, conditions []ruleschema.Pattern, bindings map[string]string, evidence []conditionEvidence, state *conditionalState) ([]conditionBranch, error) {
	if len(conditions) == 0 {
		return []conditionBranch{{
			bindings: copyBindings(bindings),
			evidence: append([]conditionEvidence{}, evidence...),
		}}, nil
	}
	if !state.consumeWork(1, r.cfg) {
		return nil, nil
	}
	idx := chooseCondition(conditions, bindings)
	condition := conditions[idx]
	rest := removePatternAt(conditions, idx)

	branches, err := r.evalCondition(ctx, condition, bindings, state)
	if err != nil || len(branches) == 0 {
		return branches, err
	}
	var out []conditionBranch
	for _, branch := range branches {
		nextEvidence := append(append([]conditionEvidence{}, evidence...), branch.evidence...)
		next, err := r.evalConditions(ctx, rest, branch.bindings, nextEvidence, state)
		if err != nil {
			return nil, err
		}
		out = append(out, next...)
		if !state.canContinue(r.cfg) {
			break
		}
	}
	return out, nil
}

func (r *ConditionalReasoner) evalCondition(ctx context.Context, pattern ruleschema.Pattern, bindings map[string]string, state *conditionalState) ([]conditionBranch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	subject, subjectBound, subjectVar := resolveTerm(pattern.Subject, bindings)
	object, objectBound, objectVar := resolveTerm(pattern.Object, bindings)
	predicate := canonicalPatternPredicate(pattern, r.reg)
	if predicate == "" {
		return nil, nil
	}

	switch {
	case subjectBound && objectBound:
		return r.evalGroundedCondition(ctx, pattern, Claim{Subject: subject, Predicate: predicate, Object: object}, bindings, state)
	case !subjectBound && !objectBound:
		return nil, nil
	case pattern.Negated:
		return nil, nil
	case r.oracle == nil:
		return nil, nil
	default:
		boundSubject := ""
		boundObject := ""
		freeVar := subjectVar
		if subjectBound {
			boundSubject = subject
			freeVar = objectVar
		} else {
			boundObject = object
		}
		if strings.TrimSpace(freeVar) == "" {
			return nil, nil
		}
		values, err := r.oracle.Bindings(ctx, predicate, boundSubject, boundObject, r.cfg.MaxBindingsPerCondition)
		if err != nil {
			return nil, err
		}
		if len(values) > r.cfg.MaxBindingsPerCondition {
			values = values[:r.cfg.MaxBindingsPerCondition]
		}
		var out []conditionBranch
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || !state.consumeWork(1, r.cfg) {
				continue
			}
			nextBindings := copyBindings(bindings)
			if !bindVariable(nextBindings, freeVar, value) {
				continue
			}
			groundedSubject, okSubject, _ := resolveTerm(pattern.Subject, nextBindings)
			groundedObject, okObject, _ := resolveTerm(pattern.Object, nextBindings)
			if !okSubject || !okObject {
				continue
			}
			branches, err := r.evalGroundedCondition(ctx, pattern, Claim{
				Subject:   groundedSubject,
				Predicate: predicate,
				Object:    groundedObject,
			}, nextBindings, state)
			if err != nil {
				return nil, err
			}
			out = append(out, branches...)
			if !state.canContinue(r.cfg) {
				break
			}
		}
		return out, nil
	}
}

func (r *ConditionalReasoner) evalGroundedCondition(ctx context.Context, pattern ruleschema.Pattern, claim Claim, bindings map[string]string, state *conditionalState) ([]conditionBranch, error) {
	ok, confidence, proof, err := r.holds(ctx, claim, state)
	if err != nil {
		return nil, err
	}
	if pattern.Negated {
		if ok {
			return nil, nil
		}
		return []conditionBranch{{
			bindings: copyBindings(bindings),
			evidence: []conditionEvidence{{
				Claim:      claim,
				Confidence: 1.0,
			}},
		}}, nil
	}
	if !ok {
		return nil, nil
	}
	return []conditionBranch{{
		bindings: copyBindings(bindings),
		evidence: []conditionEvidence{{
			Claim:      claim,
			Confidence: clamp01(confidence),
			Proof:      proof,
		}},
	}}, nil
}

func (r *ConditionalReasoner) holds(ctx context.Context, claim Claim, state *conditionalState) (bool, float64, *Proof, error) {
	claim = canonicalClaim(claim, r.reg)
	if !groundedClaim(claim) {
		return false, 0, nil, nil
	}
	ctx = context.WithValue(ctx, conditionalStateContextKey{}, state)
	res, err := r.entails(ctx, claim, state)
	if err != nil {
		return false, 0, nil, err
	}
	if res.Verdict == VerdictEntailed {
		return true, res.Confidence, res.Best, nil
	}
	if r.oracle == nil {
		return false, 0, nil, nil
	}
	return r.oracle.Holds(ctx, claim)
}

func (r *ConditionalReasoner) exceptionVeto(ctx context.Context, exceptions []ruleschema.Pattern, bindings map[string]string, state *conditionalState) (bool, error) {
	for _, exception := range exceptions {
		claim, ok := groundedPatternClaim(exception, bindings, r.reg)
		if !ok {
			continue
		}
		held, confidence, _, err := r.holds(ctx, claim, state)
		if err != nil {
			return false, err
		}
		if exception.Negated {
			if !held {
				return true, nil
			}
			continue
		}
		if held && confidence >= r.cfg.ExceptionThreshold {
			return true, nil
		}
	}
	return false, nil
}

func (r *ConditionalReasoner) composeProof(rule ruleschema.Rule, claim Claim, branch conditionBranch) Proof {
	var steps []ProofStep
	conditionProduct := 1.0
	for _, evidence := range branch.evidence {
		conditionProduct *= clamp01(evidence.Confidence)
		if evidence.Proof != nil {
			steps = append(steps, evidence.Proof.Steps...)
		}
	}
	ruleStep := ProofStep{
		EdgeID:     "rule:" + rule.ID,
		Rule:       "conditional",
		Predicate:  claim.Predicate,
		Source:     claim.Subject,
		Target:     claim.Object,
		Confidence: clamp01(rule.Confidence),
	}
	steps = append(steps, ruleStep)
	confidence := clamp01(clamp01(rule.Confidence) * conditionProduct * math.Pow(r.cfg.Decay, float64(len(steps))))
	return Proof{
		Source:     claim.Subject,
		Target:     claim.Object,
		Predicate:  claim.Predicate,
		RuleClass:  "conditional",
		Steps:      steps,
		Hops:       len(steps),
		Confidence: confidence,
	}
}

type conditionEvidence struct {
	Claim      Claim
	Confidence float64
	Proof      *Proof
}

type conditionBranch struct {
	bindings map[string]string
	evidence []conditionEvidence
}

type conditionalStateContextKey struct{}

type conditionalState struct {
	depth           int
	rulesConsidered int
	work            int
	capped          bool
	active          map[string]bool
}

func (s *conditionalState) considerRule(cfg ConditionalConfig) bool {
	if s.rulesConsidered >= cfg.MaxRulesConsidered {
		s.capped = true
		return false
	}
	s.rulesConsidered++
	return s.consumeWork(1, cfg)
}

func (s *conditionalState) consumeWork(n int, cfg ConditionalConfig) bool {
	if n <= 0 {
		return true
	}
	if s.work+n > cfg.MaxWork {
		s.capped = true
		return false
	}
	s.work += n
	return true
}

func (s *conditionalState) canContinue(cfg ConditionalConfig) bool {
	return !s.capped && s.work < cfg.MaxWork
}

func conditionalActiveKey(ruleID string, claim Claim) string {
	return strings.Join([]string{
		strings.TrimSpace(ruleID),
		strings.ToLower(strings.TrimSpace(claim.Subject)),
		normKey(claim.Predicate),
		strings.ToLower(strings.TrimSpace(claim.Object)),
	}, "\x00")
}

func canonicalClaim(claim Claim, reg *PredicateRegistry) Claim {
	return Claim{
		Subject:   strings.TrimSpace(claim.Subject),
		Predicate: canonicalPredicate(reg, claim.Predicate),
		Object:    strings.TrimSpace(claim.Object),
	}
}

func groundedClaim(claim Claim) bool {
	return strings.TrimSpace(claim.Subject) != "" &&
		strings.TrimSpace(claim.Predicate) != "" &&
		strings.TrimSpace(claim.Object) != ""
}

func unifyConsequent(pattern ruleschema.Pattern, claim Claim, bindings map[string]string, reg *PredicateRegistry) bool {
	if normKey(canonicalPatternPredicate(pattern, reg)) != normKey(claim.Predicate) {
		return false
	}
	return unifyTerm(pattern.Subject, claim.Subject, bindings) &&
		unifyTerm(pattern.Object, claim.Object, bindings)
}

func unifyTerm(term ruleschema.Term, value string, bindings map[string]string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if name, ok := termVariable(term); ok {
		return bindVariable(bindings, name, value)
	}
	entity := strings.TrimSpace(term.Entity)
	return entity != "" && entityMatches(entity, value)
}

func bindVariable(bindings map[string]string, name string, value string) bool {
	name = normalizeRuleVariable(name)
	value = strings.TrimSpace(value)
	if name == "" || value == "" {
		return false
	}
	if existing := strings.TrimSpace(bindings[name]); existing != "" {
		return entityMatches(existing, value)
	}
	bindings[name] = value
	return true
}

func resolveTerm(term ruleschema.Term, bindings map[string]string) (value string, bound bool, variable string) {
	if entity := strings.TrimSpace(term.Entity); entity != "" {
		return entity, true, ""
	}
	name, ok := termVariable(term)
	if !ok {
		return "", false, ""
	}
	value = strings.TrimSpace(bindings[name])
	return value, value != "", name
}

func termVariable(term ruleschema.Term) (string, bool) {
	name := normalizeRuleVariable(term.Var)
	return name, name != ""
}

func normalizeRuleVariable(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "?")
	return strings.TrimSpace(name)
}

func groundedPatternClaim(pattern ruleschema.Pattern, bindings map[string]string, reg *PredicateRegistry) (Claim, bool) {
	subject, subjectBound, _ := resolveTerm(pattern.Subject, bindings)
	object, objectBound, _ := resolveTerm(pattern.Object, bindings)
	if !subjectBound || !objectBound {
		return Claim{}, false
	}
	claim := Claim{Subject: subject, Predicate: canonicalPatternPredicate(pattern, reg), Object: object}
	return claim, groundedClaim(claim)
}

func chooseCondition(conditions []ruleschema.Pattern, bindings map[string]string) int {
	bestIdx := 0
	bestScore := int(^uint(0) >> 1)
	for i, condition := range conditions {
		score := freeEndpointScore(condition, bindings)
		if condition.Negated && score > 0 {
			score += 10
		}
		if score < bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	return bestIdx
}

func freeEndpointScore(pattern ruleschema.Pattern, bindings map[string]string) int {
	score := 0
	if _, bound, _ := resolveTerm(pattern.Subject, bindings); !bound {
		score++
	}
	if _, bound, _ := resolveTerm(pattern.Object, bindings); !bound {
		score++
	}
	return score
}

func removePatternAt(patterns []ruleschema.Pattern, idx int) []ruleschema.Pattern {
	out := make([]ruleschema.Pattern, 0, len(patterns)-1)
	out = append(out, patterns[:idx]...)
	return append(out, patterns[idx+1:]...)
}

func copyBindings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
