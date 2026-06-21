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

// Entails calls CALL REASON_ENTAILS(subject, predicate, object, max_hops).
func (n *NativeReasoner) Entails(ctx context.Context, c Claim) (EntailResult, error) {
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

// Contradicts calls CALL REASON_CONTRADICTS(subject, object).
func (n *NativeReasoner) Contradicts(ctx context.Context, c Claim) (bool, *Proof, error) {
	q := fmt.Sprintf(
		"CALL REASON_CONTRADICTS(%s, %s) YIELD contradicted, proof RETURN contradicted, proof",
		cyStr(c.Subject), cyStr(c.Object))
	rows, err := n.g.Query(ctx, q, nil)
	if err != nil {
		return false, nil, fmt.Errorf("REASON_CONTRADICTS: %w", err)
	}
	if len(rows) == 0 {
		return false, nil, nil
	}
	contradicted := asBool(rows[0]["contradicted"])
	var proof *Proof
	if p, ok := parseProofJSON(asString(rows[0]["proof"])); ok {
		proof = &p
	}
	return contradicted, proof, nil
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
