package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/soundprediction/predicato/pkg/ruleschema"
)

const loadRulesCypher = "MATCH (r:Rule) WHERE r.attributes IS NOT NULL RETURN %s AS uuid, r.attributes AS attributes"

// RuleLoadStats records non-fatal skips while loading structured rules from the
// graph.
type RuleLoadStats struct {
	Scanned             int
	Loaded              int
	SkippedNoStructured int
	SkippedInvalid      int
}

// LoadRulesFromGraph reads Rule.attributes.structured_rule payloads from the
// graph. Rows with missing or malformed structured payloads are skipped; semantic
// validation is left to CompileRules.
func LoadRulesFromGraph(ctx context.Context, g GraphQuerier) ([]ruleschema.Rule, RuleLoadStats, error) {
	var stats RuleLoadStats
	if g == nil || !probeGraphColumn(ctx, g, "Rule", "attributes") {
		return nil, stats, nil
	}
	uuidExpr := "''"
	if probeGraphColumn(ctx, g, "Rule", "uuid") {
		uuidExpr = "r.uuid"
	}
	rows, err := g.Query(ctx, fmt.Sprintf(loadRulesCypher, uuidExpr), nil)
	if err != nil {
		return nil, stats, fmt.Errorf("load conditional rules: %w", err)
	}
	rules := make([]ruleschema.Rule, 0, len(rows))
	for _, row := range rows {
		stats.Scanned++
		attrs, ok := decodeRuleAttributes(row["attributes"])
		if !ok {
			stats.SkippedInvalid++
			continue
		}
		payload, ok := structuredRulePayload(attrs["structured_rule"])
		if !ok {
			stats.SkippedNoStructured++
			continue
		}
		var rule ruleschema.Rule
		if err := json.Unmarshal(payload, &rule); err != nil {
			stats.SkippedInvalid++
			continue
		}
		if strings.TrimSpace(rule.ID) == "" {
			rule.ID = strings.TrimSpace(asString(row["uuid"]))
		}
		rules = append(rules, rule)
		stats.Loaded++
	}
	return rules, stats, nil
}

func decodeRuleAttributes(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case map[string]string:
		out := make(map[string]any, len(v))
		for key, value := range v {
			out[key] = value
		}
		return out, true
	case string:
		var attrs map[string]any
		if err := json.Unmarshal([]byte(v), &attrs); err != nil {
			return nil, false
		}
		return attrs, true
	case []byte:
		var attrs map[string]any
		if err := json.Unmarshal(v, &attrs); err != nil {
			return nil, false
		}
		return attrs, true
	default:
		return nil, false
	}
}

func structuredRulePayload(value any) ([]byte, bool) {
	switch v := value.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, false
		}
		return []byte(v), true
	case []byte:
		if len(v) == 0 {
			return nil, false
		}
		return v, true
	case map[string]any:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		return data, true
	default:
		return nil, false
	}
}

// GraphConditionOracle checks conditions against a GraphQuerier. Bindings are
// direct and directed over the reified graph. Holds delegates grounded claims to
// the base reasoner, which lets the conditional layer share existing proof logic.
type GraphConditionOracle struct {
	g              GraphQuerier
	base           Reasoner
	reg            *PredicateRegistry
	excludeDeduced bool
}

// NewGraphConditionOracle creates a graph-backed condition oracle.
func NewGraphConditionOracle(g GraphQuerier, base Reasoner, reg *PredicateRegistry, cfg Config) *GraphConditionOracle {
	cfg = cfg.withDefaults()
	hasStatus := probeGraphColumn(context.Background(), g, "RelatesToNode_", "status")
	return &GraphConditionOracle{
		g:              g,
		base:           base,
		reg:            reg,
		excludeDeduced: cfg.ExcludeDeduced && hasStatus,
	}
}

func (o *GraphConditionOracle) Holds(ctx context.Context, claim Claim) (bool, float64, *Proof, error) {
	if o == nil || o.base == nil {
		return false, 0, nil, nil
	}
	result, err := o.base.Entails(ctx, claim)
	if err != nil {
		return false, 0, nil, err
	}
	if result.Verdict != VerdictEntailed {
		return false, 0, nil, nil
	}
	return true, result.Confidence, result.Best, nil
}

func (o *GraphConditionOracle) Bindings(ctx context.Context, predicate string, boundSubject, boundObject string, limit int) ([]string, error) {
	if o == nil || o.g == nil {
		return nil, nil
	}
	boundSubject = strings.TrimSpace(boundSubject)
	boundObject = strings.TrimSpace(boundObject)
	if (boundSubject == "" && boundObject == "") || (boundSubject != "" && boundObject != "") {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultConditionalMaxBindingsPerCondition
	}
	preds := predicatesEntailing(o.reg, predicate)
	if len(preds) == 0 {
		preds = []string{canonicalPredicate(o.reg, predicate)}
	}
	predKeys := make([]string, 0, len(preds))
	for _, pred := range preds {
		if key := normKey(pred); key != "" {
			predKeys = append(predKeys, key)
		}
	}
	if len(predKeys) == 0 {
		return nil, nil
	}

	var (
		query string
		param string
		value string
	)
	if boundSubject != "" {
		query = graphBindingCypher("lower(s.name) = $bound", "o.name", o.excludeDeduced, limit)
		value = strings.ToLower(boundSubject)
		param = "subject"
	} else {
		query = graphBindingCypher("lower(o.name) = $bound", "s.name", o.excludeDeduced, limit)
		value = strings.ToLower(boundObject)
		param = "object"
	}
	rows, err := o.g.Query(ctx, query, map[string]any{"bound": value, "preds": predKeys})
	if err != nil {
		return nil, fmt.Errorf("conditional %s bindings: %w", param, err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		entity := strings.TrimSpace(asString(row["entity"]))
		if entity != "" {
			out = append(out, entity)
		}
	}
	return out, nil
}

func graphBindingCypher(boundWhere string, returnExpr string, excludeDeduced bool, limit int) string {
	if limit <= 0 {
		limit = defaultConditionalMaxBindingsPerCondition
	}
	var b strings.Builder
	b.WriteString("MATCH (s:Entity)-[:RELATES_TO]->(r:RelatesToNode_)-[:RELATES_TO]->(o:Entity)\n")
	b.WriteString("WHERE ")
	b.WriteString(boundWhere)
	b.WriteString(" AND lower(r.name) IN $preds\n")
	if excludeDeduced {
		b.WriteString("  AND lower(coalesce(r.status,'')) NOT IN ['deduced','speculative']\n")
	}
	b.WriteString("RETURN DISTINCT ")
	b.WriteString(returnExpr)
	b.WriteString(" AS entity\n")
	fmt.Fprintf(&b, "LIMIT %d", limit)
	return b.String()
}

func probeGraphColumn(ctx context.Context, g GraphQuerier, table string, column string) bool {
	if g == nil {
		return false
	}
	table = strings.ReplaceAll(strings.TrimSpace(table), "'", "\\'")
	rows, err := g.Query(ctx, fmt.Sprintf("CALL TABLE_INFO('%s') RETURN *", table), nil)
	if err != nil {
		return false
	}
	return tableInfoHasColumn(rows, column)
}
