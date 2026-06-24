package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	g                   GraphQuerier
	reg                 *PredicateRegistry
	cfg                 Config
	hasProvenanceStatus bool
	EnforcePredicate    bool
}

func NewNativeReasoner(g GraphQuerier, reg *PredicateRegistry, cfg Config) *NativeReasoner {
	return &NativeReasoner{
		g:                   g,
		reg:                 reg,
		cfg:                 cfg.withDefaults(),
		hasProvenanceStatus: probeProvenanceStatus(context.Background(), g),
	}
}

func (n *NativeReasoner) Name() string { return NativeBackendName }

// SetEnforcePredicate opts the native backend into passing an accepted-predicate
// set to REASON_ENTAILS. The zero value is false for legacy path-existence calls.
func (n *NativeReasoner) SetEnforcePredicate(enforce bool) *NativeReasoner {
	n.EnforcePredicate = enforce
	return n
}

// EnsureLoaded loads the reasoning extension into the current session (idempotent
// at the driver level). Call once per connection before reasoning.
func (n *NativeReasoner) EnsureLoaded(ctx context.Context) error {
	_, err := n.g.Query(ctx, "LOAD EXTENSION 'reasoning'", nil)
	return err
}

// Entails checks the native contradiction query first, then delegates path
// support to REASON_ENTAILS. Provenance quarantine is opt-in at the native arity
// level and only passed when RelatesToNode_.status exists.
func (n *NativeReasoner) Entails(ctx context.Context, c Claim) (EntailResult, error) {
	if conflict, proof, err := n.Contradicts(ctx, c); err == nil && conflict {
		return EntailResult{Verdict: VerdictContradicted, Confidence: 1.0, Best: proof}, nil
	}
	accepted := ""
	if n.EnforcePredicate {
		accepted = encodeAcceptedPredicates(nativeAcceptedPredicates(n.reg, c.Predicate))
	}
	var q string
	switch {
	case n.excludeDeduced():
		q = fmt.Sprintf(
			"CALL REASON_ENTAILS(%s, %s, %s, %d, %s, true) YIELD verdict, confidence, proof RETURN verdict, confidence, proof",
			cyStr(c.Subject), cyStr(c.Predicate), cyStr(c.Object), n.cfg.MaxHops, cyStr(accepted))
	case n.EnforcePredicate:
		q = fmt.Sprintf(
			"CALL REASON_ENTAILS(%s, %s, %s, %d, %s) YIELD verdict, confidence, proof RETURN verdict, confidence, proof",
			cyStr(c.Subject), cyStr(c.Predicate), cyStr(c.Object), n.cfg.MaxHops, cyStr(accepted))
	default:
		q = fmt.Sprintf(
			"CALL REASON_ENTAILS(%s, %s, %s, %d) YIELD verdict, confidence, proof RETURN verdict, confidence, proof",
			cyStr(c.Subject), cyStr(c.Predicate), cyStr(c.Object), n.cfg.MaxHops)
	}
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

func nativeAcceptedPredicates(reg *PredicateRegistry, predicate string) []string {
	target := canonicalPredicate(reg, predicate)
	seen := map[string]bool{}
	var out []string
	add := func(pred string) {
		pred = canonicalPredicate(reg, pred)
		key := normKey(pred)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, pred)
	}
	for _, pred := range predicatesEntailing(reg, target) {
		add(pred)
	}
	if inverse, ok := reg.Inverse(target); ok {
		for _, pred := range predicatesEntailing(reg, inverse) {
			add(pred)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return normKey(out[i]) < normKey(out[j])
	})
	return out
}

func encodeAcceptedPredicates(preds []string) string {
	var b strings.Builder
	for i, pred := range preds {
		if i > 0 {
			b.WriteByte(',')
		}
		for _, r := range pred {
			if r == '\\' || r == ',' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Derive delegates to REASON_DERIVE. Provenance quarantine is passed as a
// trailing opt-in flag only when enabled and supported by the graph schema.
func (n *NativeReasoner) Derive(ctx context.Context, req DeriveRequest) ([]Proof, error) {
	req = n.applyDefaults(req)
	q := fmt.Sprintf(
		"CALL REASON_DERIVE(%s, %s, %d, %g) YIELD target, confidence, hops, proof "+
			"RETURN target, confidence, hops, proof ORDER BY confidence DESC LIMIT %d",
		cyStr(req.Source), cyStr(req.Target), req.MaxHops, req.MinConf, req.Limit)
	if n.excludeDeduced() {
		q = fmt.Sprintf(
			"CALL REASON_DERIVE(%s, %s, %d, %g, true) YIELD target, confidence, hops, proof "+
				"RETURN target, confidence, hops, proof ORDER BY confidence DESC LIMIT %d",
			cyStr(req.Source), cyStr(req.Target), req.MaxHops, req.MinConf, req.Limit)
	}
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
	// predicate contradicts the claim. The object is matched exactly OR by base name
	// (its dosage/variant qualifier stripped, e.g. "polyethylene glycol" matches
	// "polyethylene glycol 400") so a conflicting record for any variant of the same
	// drug still fires — conservative on the safe side for contraindications.
	obj := strings.ToLower(strings.TrimSpace(c.Object))
	base := baseName(obj)
	q := `MATCH (s:Entity)-[:RELATES_TO]->(r:RelatesToNode_)-[:RELATES_TO]->(o:Entity)
		WHERE lower(s.name) = $s AND (lower(o.name) = $o OR lower(o.name) = $base OR lower(o.name) STARTS WITH $basesp) AND lower(r.name) IN $preds`
	if n.excludeDeduced() {
		q += ` AND lower(coalesce(r.status,'')) NOT IN ['deduced','speculative']`
	}
	q += `
		RETURN r.name AS pred, o.name AS obj LIMIT 1`
	rows, err := n.g.Query(ctx, q, map[string]any{
		"s":      strings.ToLower(strings.TrimSpace(c.Subject)),
		"o":      obj,
		"base":   base,
		"basesp": base + " ",
		"preds":  lc,
	})
	if err != nil {
		return false, nil, fmt.Errorf("contradiction query: %w", err)
	}
	if len(rows) == 0 {
		return false, nil, nil
	}
	conflictPred := asString(rows[0]["pred"])
	target := asString(rows[0]["obj"])
	if strings.TrimSpace(target) == "" {
		target = c.Object
	}
	proof := &Proof{
		Source:    c.Subject,
		Target:    target,
		Predicate: conflictPred,
		RuleClass: "disjoint",
		Steps:     []ProofStep{{Source: c.Subject, Predicate: conflictPred, Target: target}},
	}
	return true, proof, nil
}

func (n *NativeReasoner) excludeDeduced() bool {
	return n.cfg.ExcludeDeduced && n.hasProvenanceStatus
}

// baseName strips a trailing dosage/variant qualifier (trailing purely-numeric
// tokens) from an entity name, so drug variants share a base — e.g.
// "polyethylene glycol 3350" -> "polyethylene glycol".
func baseName(s string) string {
	toks := strings.Fields(s)
	for len(toks) > 1 {
		last := toks[len(toks)-1]
		numeric := last != ""
		for _, r := range last {
			if r < '0' || r > '9' {
				numeric = false
				break
			}
		}
		if !numeric {
			break
		}
		toks = toks[:len(toks)-1]
	}
	return strings.Join(toks, " ")
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
