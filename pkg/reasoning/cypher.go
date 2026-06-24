package reasoning

import (
	"fmt"
	"strings"
)

// compositionCypher builds the anchored, reified, bounded variable-length path
// query (SYMBOLIC_GRAPH_LOGIC.md §2.1). Variable-length bounds must be literals in
// Cypher, so the physical depth (2x logical, for the reified model) and the row
// cap are interpolated; entity/predicate values are passed as params.
//
// Per matched path it returns the ordered predicate names (the RelatesToNode_
// nodes — those with no Entity label), their ids and confidences, the reached
// target, and the logical hop count.
func compositionCypher(req DeriveRequest, excludeDeduced bool) string {
	physMax := 2 * req.MaxHops
	if physMax < 2 {
		physMax = 2
	}
	rel := fmt.Sprintf("-[:RELATES_TO*2..%d]-", physMax)
	if len(req.Preds) > 0 && req.Target == "" {
		// transitive closure restricted to specific predicates is directed
		rel = fmt.Sprintf("-[:RELATES_TO*2..%d]->", physMax)
	}

	var w strings.Builder
	w.WriteString("MATCH (a:Entity) WHERE lower(a.name) = $source\n")
	w.WriteString("MATCH p = (a)" + rel + "(b:Entity)\n")
	w.WriteString("WHERE b.uuid <> a.uuid\n")
	// keep the search in the clinical subgraph: drop molecular intermediates
	w.WriteString("  AND none(n IN nodes(p) WHERE 'GENE' IN coalesce(n.labels,[]) OR 'PROTEIN' IN coalesce(n.labels,[]))\n")
	if excludeDeduced {
		w.WriteString("  AND none(n IN nodes(p) WHERE size(coalesce(n.labels,[])) = 0 AND lower(coalesce(n.status,'')) IN ['deduced','speculative'])\n")
	}
	if len(req.Preds) > 0 {
		// every predicate node along the path must be one of $preds (transitive closure)
		w.WriteString("  AND all(n IN nodes(p) WHERE size(coalesce(n.labels,[])) > 0 OR n.name IN $preds)\n")
	}
	w.WriteString("RETURN\n")
	w.WriteString("  [n IN nodes(p) WHERE size(coalesce(n.labels,[])) = 0 | n.name] AS predicates,\n")
	w.WriteString("  [n IN nodes(p) WHERE size(coalesce(n.labels,[])) = 0 | n.uuid] AS step_ids,\n")
	w.WriteString("  [n IN nodes(p) WHERE size(coalesce(n.labels,[])) = 0 | coalesce(n.confidence, 1.0)] AS confs,\n")
	w.WriteString("  b.name AS target,\n")
	w.WriteString("  length(p) / 2 AS hops\n")
	fmt.Fprintf(&w, "ORDER BY hops ASC\nLIMIT %d", req.Limit*4)
	return w.String()
}

func compositionParams(req DeriveRequest) map[string]any {
	p := map[string]any{"source": strings.ToLower(strings.TrimSpace(req.Source))}
	if len(req.Preds) > 0 {
		p["preds"] = req.Preds
	}
	return p
}

// disjointCypher checks the OntologyDisjoint table for an unordered {a,b} pair.
func disjointCypher() string {
	return "MATCH (d:OntologyDisjoint)\n" +
		"WHERE (d.class_a = $a AND d.class_b = $b) OR (d.class_a = $b AND d.class_b = $a)\n" +
		"RETURN d.source AS source LIMIT 1"
}

// --- result coercion helpers (driver rows arrive as map[string]any) ---

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case int32:
		return int(t)
	case float64:
		return int(t)
	case float32:
		return int(t)
	default:
		return 0
	}
}

func asStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, asString(x))
		}
		return out
	default:
		return nil
	}
}

func asFloatSlice(v any) []float64 {
	switch t := v.(type) {
	case []float64:
		return t
	case []any:
		out := make([]float64, 0, len(t))
		for _, x := range t {
			switch f := x.(type) {
			case float64:
				out = append(out, f)
			case float32:
				out = append(out, float64(f))
			case int64:
				out = append(out, float64(f))
			case int:
				out = append(out, float64(f))
			default:
				out = append(out, 0)
			}
		}
		return out
	default:
		return nil
	}
}

func sameEntity(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
