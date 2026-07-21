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
	target := canonicalPredicate(r.reg, c.Predicate)
	validAll := make([]Proof, 0, len(res.All))
	for _, proof := range res.All {
		if valid, ok := r.proofEntailing(proof, target); ok {
			validAll = append(validAll, valid)
		}
	}
	if len(res.All) > 0 {
		res.All = validAll
	}
	if res.Best != nil {
		if best, ok := r.proofEntailing(*res.Best, target); ok {
			res.Best = &best
			return res, nil
		}
	}
	if len(validAll) == 0 {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}
	best := validAll[0]
	for _, proof := range validAll[1:] {
		if proof.Confidence > best.Confidence {
			best = proof
		}
	}
	res.Best = &best
	res.Confidence = best.Confidence
	return res, nil
}

func (r *predicateConstrainedReasoner) proofEntailing(proof Proof, target string) (Proof, bool) {
	var predicate string
	var ok bool
	if normKey(proof.RuleClass) == "conditional" {
		predicate, ok = conditionalConsequentPredicate(r.reg, proof.Steps)
		if !ok || !predicateEntails(r.reg, predicate, target) {
			return Proof{}, false
		}
	} else {
		predicate, ok = effectivePredicate(r.reg, proof.Steps)
		if !ok || !proofEntailsPredicate(r.reg, predicate, target, true) {
			return Proof{}, false
		}
	}
	proof.Predicate = predicate
	return proof, true
}

func conditionalConsequentPredicate(reg *PredicateRegistry, steps []ProofStep) (string, bool) {
	for i := len(steps) - 1; i >= 0; i-- {
		if normKey(steps[i].Rule) != "conditional" {
			continue
		}
		predicate := canonicalPredicate(reg, steps[i].Predicate)
		return predicate, predicate != ""
	}
	return "", false
}

func (r *predicateConstrainedReasoner) Contradicts(ctx context.Context, c Claim) (bool, *Proof, error) {
	return r.inner.Contradicts(ctx, c)
}

func (r *predicateConstrainedReasoner) Name() string {
	return r.inner.Name() + "+predicate-constrained"
}
