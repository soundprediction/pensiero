package reasoning

import "context"

type predicateConstrainedReasoner struct {
	inner Reasoner
	reg   *PredicateRegistry
}

// NewPredicateConstrained wraps a backend that may return proof paths without
// enforcing the requested predicate and verifies each returned proof against the
// predicate registry.
func NewPredicateConstrained(inner Reasoner, reg *PredicateRegistry) Reasoner {
	return &predicateConstrainedReasoner{inner: inner, reg: reg}
}

func (r *predicateConstrainedReasoner) Derive(ctx context.Context, req DeriveRequest) ([]Proof, error) {
	proofs, err := r.inner.Derive(ctx, req)
	if err != nil || req.Predicate == "" {
		return proofs, err
	}
	target := canonicalPredicate(r.reg, req.Predicate)
	out := make([]Proof, 0, len(proofs))
	for _, proof := range proofs {
		effective, ok := effectivePredicate(r.reg, proof.Steps)
		if !ok || !proofEntailsPredicate(r.reg, effective, target, req.IncludeInverse) {
			continue
		}
		proof.Predicate = effective
		out = append(out, proof)
	}
	return out, nil
}

func (r *predicateConstrainedReasoner) Entails(ctx context.Context, c Claim) (EntailResult, error) {
	res, err := r.inner.Entails(ctx, c)
	if err != nil || res.Verdict != VerdictEntailed {
		return res, err
	}
	if res.Best == nil {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}
	// A conditional-rule proof already matches the claim's predicate by
	// construction (the rule consequent IS the claim). Its step chain mixes the
	// condition predicate(s) with the consequent predicate, so it is not a single
	// coherent predicate path and must not be subjected to the path-predicate
	// entailment check (which would reject the valid conclusion).
	if res.Best.RuleClass == "conditional" {
		return res, nil
	}
	effective, ok := effectivePredicate(r.reg, res.Best.Steps)
	target := canonicalPredicate(r.reg, c.Predicate)
	if !ok || !proofEntailsPredicate(r.reg, effective, target, true) {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}
	best := *res.Best
	best.Predicate = effective
	res.Best = &best
	return res, nil
}

func (r *predicateConstrainedReasoner) Contradicts(ctx context.Context, c Claim) (bool, *Proof, error) {
	return r.inner.Contradicts(ctx, c)
}

func (r *predicateConstrainedReasoner) Name() string {
	return r.inner.Name() + "+predicate-constrained"
}
