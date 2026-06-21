package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NativeBackendName is the registry key for the in-engine ladybug extension
// backend: reasoning runs INSIDE ladybug via the `reasoning` extension's
// Cypher-callable functions (REASON_ENTAILS / REASON_DERIVE / REASON_CONTRADICTS).
// This is what "pensiero uses the native extension" means — the Go side is a thin
// caller; the multi-hop traversal happens in the engine.
const NativeBackendName = "ladybug-native"

func init() {
	Register(NativeBackendName, func(g GraphQuerier, reg *PredicateRegistry, cfg Config) (Reasoner, error) {
		return NewNativeReasoner(g, reg, cfg), nil
	})
}

// NativeReasoner implements Reasoner by invoking the `reasoning` ladybug extension
// over a GraphQuerier (the go-predicato ladybug driver adapter). The extension
// must be loaded in the session (LOAD EXTENSION 'reasoning'); EnsureLoaded does it.
type NativeReasoner struct {
	g   GraphQuerier
	reg *PredicateRegistry
	cfg Config
}

func NewNativeReasoner(g GraphQuerier, reg *PredicateRegistry, cfg Config) *NativeReasoner {
	return &NativeReasoner{g: g, reg: reg, cfg: cfg.withDefaults()}
}

func (n *NativeReasoner) Name() string { return NativeBackendName }

// EnsureLoaded loads the reasoning extension into the current session (idempotent
// at the driver level). Call once per connection before reasoning.
func (n *NativeReasoner) EnsureLoaded(ctx context.Context) error {
	_, err := n.g.Query(ctx, "LOAD EXTENSION 'reasoning'", nil)
	return err
}

// Entails calls CALL REASON_ENTAILS(subject, predicate, object, max_hops). A logical
// contradiction — a conflicting KB edge between the same ordered pair — takes
// precedence and short-circuits to VerdictContradicted.
func (n *NativeReasoner) Entails(ctx context.Context, c Claim) (EntailResult, error) {
	if conflict, proof, err := n.Contradicts(ctx, c); err == nil && conflict {
		return EntailResult{Verdict: VerdictContradicted, Confidence: 1.0, Best: proof}, nil
	}
	q := fmt.Sprintf(
		"CALL REASON_ENTAILS(%s, %s, %s, %d) YIELD verdict, confidence, proof RETURN verdict, confidence, proof",
		cyStr(c.Subject), cyStr(c.Predicate), cyStr(c.Object), n.cfg.MaxHops)
	rows, err := n.g.Query(ctx, q, nil)
	if err != nil {
		return EntailResult{}, fmt.Errorf("REASON_ENTAILS: %w", err)
	}
	if len(rows) == 0 {
		return EntailResult{Verdict: VerdictUnsupported}, nil
	}
	r := rows[0]
	res := EntailResult{
		Verdict:    Verdict(asString(r["verdict"])),
		Confidence: asFloat(r["confidence"]),
	}
	if p, ok := parseProofJSON(asString(r["proof"])); ok {
		res.Best = &p
	}
	if res.Verdict == "" {
		res.Verdict = VerdictUnsupported
	}
	return res, nil
}

// Derive calls CALL REASON_DERIVE(source, target, max_hops, min_conf).
func (n *NativeReasoner) Derive(ctx context.Context, req DeriveRequest) ([]Proof, error) {
	req = n.applyDefaults(req)
	q := fmt.Sprintf(
		"CALL REASON_DERIVE(%s, %s, %d, %g) YIELD target, confidence, hops, proof "+
			"RETURN target, confidence, hops, proof ORDER BY confidence DESC LIMIT %d",
		cyStr(req.Source), cyStr(req.Target), req.MaxHops, req.MinConf, req.Limit)
	rows, err := n.g.Query(ctx, q, nil)
	if err != nil {
		return nil, fmt.Errorf("REASON_DERIVE: %w", err)
	}
	out := make([]Proof, 0, len(rows))
	for _, r := range rows {
		p, ok := parseProofJSON(asString(r["proof"]))
		if !ok {
			p = Proof{Target: asString(r["target"])}
		}
		p.Confidence = asFloat(r["confidence"])
		p.Hops = asInt(r["hops"])
		if p.Target == "" {
			p.Target = asString(r["target"])
		}
		out = append(out, p)
	}
	return out, nil
}

// Contradicts reports a logical contradiction: the KB asserts, between the same
// ordered pair (subject, object), a predicate that is registered disjoint with the
// claimed predicate (e.g. CONTRAINDICATED vs TREATS, CAUSES vs PREVENTS). This is a
// real inconsistency with the knowledge base — distinct from a mere absence of
// support (a gap). Implemented in Go over the reified graph (the bundled extension's
// REASON_CONTRADICTS needs an ontology-disjointness side table that is not present).
func (n *NativeReasoner) Contradicts(ctx context.Context, c Claim) (bool, *Proof, error) {
	pred := strings.TrimSpace(c.Predicate)
	if m, ok := n.reg.Canonical(pred); ok {
		pred = m.Canonical
	}
	conflicts := n.reg.Conflicting(pred)
	if len(conflicts) == 0 {
		return false, nil, nil
	}
	lc := make([]string, 0, len(conflicts))
	for _, p := range conflicts {
		lc = append(lc, strings.ToLower(strings.TrimSpace(p)))
	}
	// Reified model: (s)-[:RELATES_TO]->(r:RelatesToNode_)-[:RELATES_TO]->(o), with the
	// predicate on r.name. A KB edge between the same pair carrying a conflicting
	// predicate contradicts the claim.
	const q = `MATCH (s:Entity)-[:RELATES_TO]->(r:RelatesToNode_)-[:RELATES_TO]->(o:Entity)
		WHERE lower(s.name) = $s AND lower(o.name) = $o AND lower(r.name) IN $preds
		RETURN r.name AS pred LIMIT 1`
	rows, err := n.g.Query(ctx, q, map[string]any{
		"s":     strings.ToLower(strings.TrimSpace(c.Subject)),
		"o":     strings.ToLower(strings.TrimSpace(c.Object)),
		"preds": lc,
	})
	if err != nil {
		return false, nil, fmt.Errorf("contradiction query: %w", err)
	}
	if len(rows) == 0 {
		return false, nil, nil
	}
	conflictPred := asString(rows[0]["pred"])
	proof := &Proof{
		Source:    c.Subject,
		Target:    c.Object,
		Predicate: conflictPred,
		RuleClass: "disjoint",
		Steps:     []ProofStep{{Source: c.Subject, Predicate: conflictPred, Target: c.Object}},
	}
	return true, proof, nil
}

func (n *NativeReasoner) applyDefaults(req DeriveRequest) DeriveRequest {
	if req.MaxHops <= 0 {
		req.MaxHops = n.cfg.MaxHops
	}
	if req.MinConf <= 0 {
		req.MinConf = n.cfg.MinConf
	}
	if req.Limit <= 0 {
		req.Limit = n.cfg.Limit
	}
	return req
}

// cyStr renders a Cypher single-quoted string literal with escaping (CALL
// table-function args are literals, not bind params).
func cyStr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

// parseProofJSON decodes the `proof` column emitted by the reasoning extension.
// The extension emits a proof as a JSON ARRAY of steps ([{edge_id,rule,predicate,
// source,target,confidence}, ...]); some callers/backends may instead emit the
// Proof object form ({source,target,steps,...}). Both are accepted.
func parseProofJSON(s string) (Proof, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || s == "[]" {
		return Proof{}, false
	}
	if strings.HasPrefix(s, "[") {
		var steps []ProofStep
		if err := json.Unmarshal([]byte(s), &steps); err != nil || len(steps) == 0 {
			return Proof{}, false
		}
		p := Proof{
			Steps:     steps,
			Source:    steps[0].Source,
			Target:    steps[len(steps)-1].Target,
			RuleClass: steps[0].Rule,
			Hops:      len(steps),
		}
		return p, true
	}
	var p Proof
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return Proof{}, false
	}
	if len(p.Steps) == 0 && p.Source == "" && p.Target == "" {
		return Proof{}, false
	}
	return p, true
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case int:
		return float64(t)
	default:
		return 0
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}
