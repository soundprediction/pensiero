package reasoning

import (
	"context"
	"sort"
	"strings"
)

// assumedFactsContextKey carries per-request ground facts (e.g. a patient's
// findings) through the reasoning call. They satisfy rule conditions for one
// request only and are never written to the graph.
type assumedFactsContextKey struct{}

// WithAssumedFacts returns a context carrying per-request ground facts the
// conditional layer may use to satisfy rule conditions. Hosts (e.g. humn DDx)
// set these from the patient's findings before calling Entails; the gRPC client
// forwards them and the server re-attaches them server-side.
func WithAssumedFacts(ctx context.Context, facts []Claim) context.Context {
	if len(facts) == 0 {
		return ctx
	}
	return context.WithValue(ctx, assumedFactsContextKey{}, facts)
}

// AssumedFactsFromContext returns the per-request ground facts, if any.
func AssumedFactsFromContext(ctx context.Context) []Claim {
	if ctx == nil {
		return nil
	}
	facts, _ := ctx.Value(assumedFactsContextKey{}).([]Claim)
	return facts
}

// AssumedFactsOracle wraps a base condition oracle and additionally satisfies
// conditions from the per-request assumed facts in context. It is checked before
// the base oracle, so patient context can ground a rule without any graph write.
type AssumedFactsOracle struct {
	base conditionOracle
	reg  *PredicateRegistry
}

// NewAssumedFactsOracle wraps base so rule conditions can be met by the
// per-request facts in context (see WithAssumedFacts) as well as the graph.
func NewAssumedFactsOracle(base conditionOracle, reg *PredicateRegistry) *AssumedFactsOracle {
	return &AssumedFactsOracle{base: base, reg: reg}
}

func (o *AssumedFactsOracle) Holds(ctx context.Context, claim Claim) (bool, float64, *Proof, error) {
	want := canonicalClaim(claim, o.reg)
	for _, f := range AssumedFactsFromContext(ctx) {
		got := canonicalClaim(f, o.reg)
		if sameEntity(got.Subject, want.Subject) &&
			normKey(got.Predicate) == normKey(want.Predicate) &&
			sameEntity(got.Object, want.Object) {
			proof := &Proof{
				Source:     want.Subject,
				Target:     want.Object,
				Predicate:  want.Predicate,
				RuleClass:  "fact",
				Hops:       1,
				Confidence: 1.0,
				Steps: []ProofStep{{
					EdgeID:     "assumed:" + want.Subject + ":" + want.Predicate + ":" + want.Object,
					Rule:       "fact",
					Predicate:  want.Predicate,
					Source:     want.Subject,
					Target:     want.Object,
					Confidence: 1.0,
				}},
			}
			return true, 1.0, proof, nil
		}
	}
	if o.base == nil {
		return false, 0, nil, nil
	}
	return o.base.Holds(ctx, claim)
}

func (o *AssumedFactsOracle) Bindings(ctx context.Context, predicate, boundSubject, boundObject string, limit int) ([]string, error) {
	boundSubject = strings.TrimSpace(boundSubject)
	boundObject = strings.TrimSpace(boundObject)
	seen := map[string]bool{}
	var out []string
	for _, f := range AssumedFactsFromContext(ctx) {
		got := canonicalClaim(f, o.reg)
		if normKey(got.Predicate) != normKey(canonicalPredicate(o.reg, predicate)) {
			continue
		}
		var cand string
		switch {
		case boundSubject != "" && sameEntity(got.Subject, boundSubject):
			cand = got.Object
		case boundObject != "" && sameEntity(got.Object, boundObject):
			cand = got.Subject
		default:
			continue
		}
		key := strings.ToLower(strings.TrimSpace(cand))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, cand)
	}
	sort.Strings(out)
	if o.base != nil {
		baseVals, err := o.base.Bindings(ctx, predicate, boundSubject, boundObject, limit)
		if err != nil {
			return nil, err
		}
		for _, v := range baseVals {
			key := strings.ToLower(strings.TrimSpace(v))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, v)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
