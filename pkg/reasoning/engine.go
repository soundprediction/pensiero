package reasoning

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// Engine is the built-in ladybug/Cypher symbolic reasoner. It builds anchored,
// bounded, reified-aware path queries, runs them through the GraphQuerier, and
// assembles explainable proofs with Context-Monoid confidence. It contains no
// driver code — the GraphQuerier abstracts the graph — so it is pure Go and
// unit-testable with a mock.
type Engine struct {
	g   GraphQuerier
	reg *PredicateRegistry
	cfg Config
}

// NewEngine constructs the symbolic engine.
func NewEngine(g GraphQuerier, reg *PredicateRegistry, cfg Config) *Engine {
	return &Engine{g: g, reg: reg, cfg: cfg.withDefaults()}
}

// Name implements Reasoner.
func (e *Engine) Name() string { return BackendName }

// Derive returns ranked proof paths Source -> Target (or any target when Target
// is empty), within MaxHops logical hops. It maps to the reified composition
// query (SYMBOLIC_GRAPH_LOGIC.md §2.1); the GraphQuerier returns, per path:
//
//	step_ids   []string  // ordered RelatesToNode_ ids
//	predicates []string  // ordered raw predicates (one per logical hop)
//	confs      []float64 // optional per-hop confidence (defaults to 1.0)
//	target     string    // reached entity (name/uuid)
//	hops       int       // logical hops
func (e *Engine) Derive(ctx context.Context, req DeriveRequest) ([]Proof, error) {
	req = e.applyDefaults(req)
	rows, err := e.g.Query(ctx, compositionCypher(req), compositionParams(req))
	if err != nil {
		return nil, fmt.Errorf("reasoning.Derive: %w", err)
	}
	proofs := make([]Proof, 0, len(rows))
	for _, row := range rows {
		p, ok := e.rowToProof(req.Source, row, req.IncludeInverse)
		if !ok || p.Confidence < req.MinConf {
			continue
		}
		if req.Target != "" && !sameEntity(p.Target, req.Target) {
			continue
		}
		proofs = append(proofs, p)
	}
	sort.SliceStable(proofs, func(i, j int) bool { return proofs[i].Confidence > proofs[j].Confidence })
	if len(proofs) > req.Limit {
		proofs = proofs[:req.Limit]
	}
	return proofs, nil
}

// Entails decides whether the claim is symbolically supported, contradicted, or
// unsupported (SYMBOLIC_GRAPH_LOGIC.md §2.4/§2.5, §5). A disjointness conflict
// overrides any support.
func (e *Engine) Entails(ctx context.Context, c Claim) (EntailResult, error) {
	// Contradiction is logically dispositive — check it first.
	if conflict, cp, err := e.Contradicts(ctx, c); err != nil {
		return EntailResult{}, err
	} else if conflict {
		conf := 0.0
		if cp != nil {
			conf = cp.Confidence
		}
		return EntailResult{Verdict: VerdictContradicted, Confidence: conf, Best: cp}, nil
	}

	proofs, err := e.Derive(ctx, DeriveRequest{
		Source: c.Subject, Target: c.Object, IncludeInverse: true,
	})
	if err != nil {
		return EntailResult{}, err
	}
	if len(proofs) == 0 {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}
	return EntailResult{
		Verdict:    VerdictEntailed,
		Confidence: proofs[0].Confidence,
		Best:       &proofs[0],
		All:        proofs,
	}, nil
}

// Contradicts reports an ontology-disjointness conflict: the subject provably
// belongs (is_a*) to some class that is declared disjoint from the claim's object.
func (e *Engine) Contradicts(ctx context.Context, c Claim) (bool, *Proof, error) {
	// Classes the subject is_a* (its memberships), via the transitive taxonomic closure.
	members, err := e.Derive(ctx, DeriveRequest{
		Source: c.Subject, Preds: e.reg.TransitivePreds(),
	})
	if err != nil {
		return false, nil, err
	}
	// Reflexive membership: the subject is a member of itself.
	classes := map[string]Proof{c.Subject: {Source: c.Subject, Target: c.Subject, Confidence: 1.0, RuleClass: "refl"}}
	for _, p := range members {
		if _, seen := classes[p.Target]; !seen {
			classes[p.Target] = p
		}
	}
	for cls, mp := range classes {
		ok, err := e.disjoint(ctx, cls, c.Object)
		if err != nil {
			return false, nil, err
		}
		if ok {
			proof := mp
			proof.Predicate = "disjoint_with"
			proof.RuleClass = "disjointness"
			proof.Target = c.Object
			return true, &proof, nil
		}
	}
	return false, nil, nil
}

// disjoint checks the OntologyDisjoint table for an unordered {a,b} pair.
func (e *Engine) disjoint(ctx context.Context, a, b string) (bool, error) {
	rows, err := e.g.Query(ctx, disjointCypher(), map[string]any{"a": a, "b": b})
	if err != nil {
		return false, fmt.Errorf("reasoning.disjoint: %w", err)
	}
	return len(rows) > 0, nil
}

func (e *Engine) applyDefaults(req DeriveRequest) DeriveRequest {
	if req.MaxHops <= 0 {
		req.MaxHops = e.cfg.MaxHops
	}
	if req.Decay <= 0 || req.Decay > 1 {
		req.Decay = e.cfg.Decay
	}
	if req.MinConf <= 0 {
		req.MinConf = e.cfg.MinConf
	}
	if req.Limit <= 0 {
		req.Limit = e.cfg.Limit
	}
	return req
}

// rowToProof builds a Proof from one path row, applying functor F to each
// predicate and composing confidence (product of per-hop confidences with hop
// decay). Returns ok=false for malformed rows.
func (e *Engine) rowToProof(source string, row map[string]any, includeInverse bool) (Proof, bool) {
	preds := asStringSlice(row["predicates"])
	if len(preds) == 0 {
		return Proof{}, false
	}
	ids := asStringSlice(row["step_ids"])
	confs := asFloatSlice(row["confs"])
	target := asString(row["target"])
	hops := asInt(row["hops"])
	if hops <= 0 {
		hops = len(preds)
	}

	conf := 1.0
	steps := make([]ProofStep, 0, len(preds))
	for i, raw := range preds {
		meta, _ := e.reg.Canonical(raw)
		canon := meta.Canonical
		stepConf := 1.0
		if i < len(confs) && confs[i] > 0 {
			stepConf = confs[i]
		}
		conf *= stepConf
		id := ""
		if i < len(ids) {
			id = ids[i]
		}
		steps = append(steps, ProofStep{
			EdgeID:     id,
			Rule:       "composition",
			Predicate:  canon,
			Confidence: stepConf,
		})
	}
	// Context-Monoid hop decay.
	if hops > 1 {
		conf *= math.Pow(e.cfg.Decay, float64(hops-1))
	}
	pred := ""
	if len(steps) > 0 {
		pred = steps[len(steps)-1].Predicate
	}
	return Proof{
		Source:     source,
		Target:     target,
		Predicate:  pred,
		Hops:       hops,
		Confidence: conf,
		RuleClass:  ruleClass(hops),
		Steps:      steps,
	}, true
}

func ruleClass(hops int) string {
	if hops <= 1 {
		return "edge"
	}
	return "composition"
}
