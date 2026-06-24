package generalization

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

func TestBuildSelectsBoundedSupportedParents(t *testing.T) {
	ctx := context.Background()
	src := fakeSource{
		taxonomy: []map[string]any{
			taxRow("A", "P", 1),
			taxRow("B", "P", 1),
			taxRow("C", "Q", 1),
			taxRow("P", "Root", 1),
		},
	}
	g, err := Build(ctx, src, testRegistry(), Config{
		ScopeEntities:    []string{"A", "B", "C"},
		Predicates:       []string{"R"},
		MaxParentLevel:   1,
		MinParentSupport: 2,
		MinSupport:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasNode(g, "P", NodeConcept) {
		t.Fatalf("expected P concept node, got %#v", g.Nodes)
	}
	if hasNode(g, "Q", NodeConcept) {
		t.Fatalf("did not expect Q concept node")
	}
	if hasNode(g, "Root", NodeConcept) {
		t.Fatalf("did not expect Root concept node")
	}
	if got := g.Stats.ParentLevelCounts[1]; got != 1 {
		t.Fatalf("level count = %d, want 1", got)
	}
}

func TestBuildDerivesMultiLevelAncestorsFromDirectTaxonomy(t *testing.T) {
	ctx := context.Background()
	src := fakeSource{
		taxonomy: []map[string]any{
			taxRow("A", "P", 1),
			taxRow("P", "Root", 1),
		},
	}
	g, err := Build(ctx, src, testRegistry(), Config{
		ScopeEntities:    []string{"A"},
		Predicates:       []string{"R"},
		MaxParentLevel:   2,
		MinParentSupport: 1,
		MinSupport:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := findNode(g, "Root", NodeConcept)
	if root == nil {
		t.Fatalf("expected Root concept node, got %#v", g.Nodes)
	}
	if root.Depth != 2 {
		t.Fatalf("Root depth = %d, want 2", root.Depth)
	}
	if got := g.Stats.ParentLevelCounts[2]; got != 1 {
		t.Fatalf("level 2 count = %d, want 1", got)
	}
	if findRelation(g, "A", "is_a", "P", false) == nil {
		t.Fatalf("expected direct A is_a P relation, got %#v", g.Relations)
	}
	if findRelation(g, "P", "is_a", "Root", false) == nil {
		t.Fatalf("expected direct P is_a Root relation, got %#v", g.Relations)
	}
	if findRelation(g, "A", "is_a", "Root", false) != nil {
		t.Fatalf("did not expect precomputed A is_a Root closure edge")
	}

	engine := reasoning.NewEngine(newReasonerFake(g), testRegistry(), reasoning.Config{MaxHops: 3, MinConf: 0.01})
	res, err := engine.Entails(ctx, reasoning.Claim{Subject: "A", Predicate: "is_a", Object: "Root"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != reasoning.VerdictEntailed {
		t.Fatalf("A is_a Root verdict = %s, want entailed", res.Verdict)
	}
}

func TestBuildDerivesAncestorsFromParentToChildTaxonomy(t *testing.T) {
	ctx := context.Background()
	src := parentToChildSource{
		taxonomy: []parentChildEdge{
			{parent: "P", child: "A", predicate: "IS_PARENT_OF"},
			{parent: "Root", child: "P", predicate: "IS_PARENT_OF"},
		},
	}
	g, err := Build(ctx, src, testRegistry(), Config{
		ScopeEntities:       []string{"A"},
		TaxonomicPredicates: []string{"IS_PARENT_OF"},
		TaxonomicDirection:  TaxonomicDirectionParentToChild,
		Predicates:          []string{"R"},
		MaxParentLevel:      2,
		MinParentSupport:    1,
		MinSupport:          1,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := findNode(g, "Root", NodeConcept)
	if root == nil {
		t.Fatalf("expected Root concept node, got %#v", g.Nodes)
	}
	if root.Depth != 2 {
		t.Fatalf("Root depth = %d, want 2", root.Depth)
	}
	if got := g.Stats.ParentLevelCounts[2]; got != 1 {
		t.Fatalf("level 2 count = %d, want 1", got)
	}
	if findRelation(g, "P", "IS_PARENT_OF", "Root", false) == nil {
		t.Fatalf("expected direct P hierarchy Root relation, got %#v", g.Relations)
	}
	if findRelation(g, "A", "IS_PARENT_OF", "Root", false) != nil {
		t.Fatalf("did not expect precomputed A hierarchy Root closure edge")
	}
}

func TestTaxonomyCypherUsesDirectOneHopLadybugSyntax(t *testing.T) {
	query := taxonomyCypher(TaxonomicDirectionChildToParent)
	if strings.Contains(query, "RELATES_TO*") {
		t.Fatalf("taxonomy query must not use variable-length paths: %s", query)
	}
	if strings.Contains(query, "[n IN") || strings.Contains(query, "| n.name") {
		t.Fatalf("taxonomy query must not use list comprehensions: %s", query)
	}
	if !strings.Contains(query, "rel.name IN $taxonomic") {
		t.Fatalf("taxonomy query missing direct predicate filter: %s", query)
	}
	parentToChildQuery := taxonomyCypher(TaxonomicDirectionParentToChild)
	if !strings.Contains(parentToChildQuery, "MATCH (parent:Entity)-[:RELATES_TO]->(rel:RelatesToNode_)-[:RELATES_TO]->(child:Entity)") {
		t.Fatalf("parent-to-child taxonomy query has wrong edge direction: %s", parentToChildQuery)
	}
}

func TestBuildHonorsContextCancellationBetweenTaxonomyChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	src := &cancelAfterFirstTaxonomyQuerySource{cancel: cancel}
	entities := make([]string, taxonomyQueryChunkSize+1)
	for i := range entities {
		entities[i] = fmt.Sprintf("E%d", i)
	}
	_, err := Build(ctx, src, testRegistry(), Config{
		ScopeEntities:    entities,
		Predicates:       []string{"R"},
		MaxParentLevel:   1,
		MinParentSupport: 1,
		MinSupport:       1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build error=%v, want context canceled", err)
	}
	if got := src.TaxonomyCalls(); got != 1 {
		t.Fatalf("taxonomy calls=%d, want 1", got)
	}
}

func TestBuildLiftsSharedRelationAtMinSupport(t *testing.T) {
	ctx := context.Background()
	src := fakeSource{
		taxonomy: []map[string]any{
			taxRow("A", "P", 1),
			taxRow("B", "P", 1),
			taxRow("C", "P", 1),
			taxRow("D", "Q", 1),
		},
		direct: []map[string]any{
			relRow("e1", "A", "R", "Y"),
			relRow("e2", "B", "R", "Y"),
			relRow("e3", "C", "R", "Z"),
		},
	}
	g, err := Build(ctx, src, testRegistry(), Config{
		ScopeEntities:    []string{"A", "B", "C", "D"},
		Predicates:       []string{"R"},
		MaxParentLevel:   2,
		MinParentSupport: 1,
		MinSupport:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	lifted := findRelation(g, "P", "R", "Y", true)
	if lifted == nil {
		t.Fatalf("expected lifted P R Y relation, got %#v", g.Relations)
	}
	if lifted.Support != 2 {
		t.Fatalf("lifted support = %d, want 2", lifted.Support)
	}
	if findRelation(g, "P", "R", "Z", true) != nil {
		t.Fatalf("did not expect P R Z below support")
	}
	if findRelation(g, "A", "R", "Y", false) == nil {
		t.Fatalf("expected direct A R Y relation to be retained")
	}
}

func TestBuildLiftsRelationThroughDirectHierarchy(t *testing.T) {
	ctx := context.Background()
	src := fakeSource{
		taxonomy: []map[string]any{
			taxRow("A", "P", 1),
			taxRow("B", "Q", 1),
			taxRow("P", "Root", 1),
			taxRow("Q", "Root", 1),
		},
		direct: []map[string]any{
			relRow("e1", "A", "R", "Y"),
			relRow("e2", "B", "R", "Y"),
		},
	}
	g, err := Build(ctx, src, testRegistry(), Config{
		ScopeEntities:    []string{"A", "B"},
		Predicates:       []string{"R"},
		MaxParentLevel:   2,
		MinParentSupport: 2,
		MinSupport:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if findRelation(g, "A", "is_a", "Root", false) != nil {
		t.Fatalf("did not expect precomputed A is_a Root closure edge")
	}
	if findRelation(g, "A", "is_a", "P", false) == nil || findRelation(g, "P", "is_a", "Root", false) == nil {
		t.Fatalf("expected direct hierarchy chain through P, got %#v", g.Relations)
	}
	lifted := findRelation(g, "Root", "R", "Y", true)
	if lifted == nil {
		t.Fatalf("expected lifted Root R Y relation, got %#v", g.Relations)
	}
	if lifted.Support != 2 {
		t.Fatalf("lifted support = %d, want 2", lifted.Support)
	}
}

func TestBuildSkipsCyclicHierarchyEdges(t *testing.T) {
	ctx := context.Background()
	src := fakeSource{
		taxonomy: []map[string]any{
			taxRow("A", "P", 1),
			taxRow("P", "Q", 1),
			taxRow("Q", "P", 1),
		},
	}
	g, err := Build(ctx, src, testRegistry(), Config{
		ScopeEntities:    []string{"A"},
		Predicates:       []string{"R"},
		MaxParentLevel:   4,
		MinParentSupport: 1,
		MinSupport:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if findRelation(g, "P", "is_a", "Q", false) != nil || findRelation(g, "Q", "is_a", "P", false) != nil {
		t.Fatalf("did not expect cyclic hierarchy relations, got %#v", g.Relations)
	}
	if findRelation(g, "A", "is_a", "P", false) == nil {
		t.Fatalf("expected acyclic A is_a P relation, got %#v", g.Relations)
	}
}

func TestBuiltGraphSupportsInheritedRelation(t *testing.T) {
	ctx := context.Background()
	src := fakeSource{
		taxonomy: []map[string]any{
			taxRow("A", "P", 1),
			taxRow("B", "P", 1),
			taxRow("D", "Q", 1),
		},
		direct: []map[string]any{
			relRow("e1", "A", "R", "Y"),
			relRow("e2", "B", "R", "Y"),
		},
	}
	g, err := Build(ctx, src, testRegistry(), Config{
		ScopeEntities:    []string{"A", "B", "D"},
		Predicates:       []string{"R"},
		MaxParentLevel:   2,
		MinParentSupport: 1,
		MinSupport:       2,
	})
	if err != nil {
		t.Fatal(err)
	}

	engine := reasoning.NewEngine(newReasonerFake(g), testRegistry(), reasoning.Config{MaxHops: 3, MinConf: 0.01})
	res, err := engine.Entails(ctx, reasoning.Claim{Subject: "A", Predicate: "R", Object: "Y"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != reasoning.VerdictEntailed {
		t.Fatalf("A R Y verdict = %s, want entailed", res.Verdict)
	}
	if res.Best == nil || res.Best.Hops < 1 {
		t.Fatalf("expected proof, got %#v", res.Best)
	}

	res, err = engine.Entails(ctx, reasoning.Claim{Subject: "D", Predicate: "R", Object: "Y"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != reasoning.VerdictUnsupported {
		t.Fatalf("D R Y verdict = %s, want unsupported", res.Verdict)
	}
}

func testRegistry() *reasoning.PredicateRegistry {
	return reasoning.NewPredicateRegistry(
		[]reasoning.PredicateMeta{
			{Canonical: "is_a", Chars: reasoning.Transitive},
			{Canonical: "R"},
		},
		[]reasoning.CompositionRule{
			{First: "is_a", Second: "R", Result: "R"},
		},
		nil,
	)
}

func taxRow(child, parent string, depth int) map[string]any {
	return map[string]any{
		"child_id":    child,
		"child_name":  child,
		"parent_id":   parent,
		"parent_name": parent,
		"depth":       depth,
		"predicate":   "is_a",
		"confidence":  1.0,
	}
}

func relRow(id, source, predicate, target string) map[string]any {
	return map[string]any{
		"edge_id":     id,
		"source_id":   source,
		"source_name": source,
		"target_id":   target,
		"target_name": target,
		"predicate":   predicate,
		"confidence":  1.0,
	}
}

type fakeSource struct {
	taxonomy []map[string]any
	direct   []map[string]any
}

func (f fakeSource) Query(_ context.Context, query string, params map[string]any) ([]map[string]any, error) {
	switch {
	case hasParam(params, "taxonomic"):
		return filterTaxonomy(f.taxonomy, params), nil
	case strings.Contains(query, "RelatesToNode_"):
		return filterDirect(f.direct, params), nil
	case strings.Contains(query, "MATCH (n:Entity)"):
		return nil, nil
	default:
		return nil, nil
	}
}

type cancelAfterFirstTaxonomyQuerySource struct {
	cancel context.CancelFunc
	calls  atomic.Int64
}

func (s *cancelAfterFirstTaxonomyQuerySource) Query(_ context.Context, _ string, params map[string]any) ([]map[string]any, error) {
	if hasParam(params, "taxonomic") {
		if s.calls.Add(1) == 1 {
			s.cancel()
		}
		return nil, nil
	}
	return nil, nil
}

func (s *cancelAfterFirstTaxonomyQuerySource) TaxonomyCalls() int64 {
	return s.calls.Load()
}

type parentChildEdge struct {
	parent    string
	child     string
	predicate string
}

type parentToChildSource struct {
	taxonomy []parentChildEdge
}

func (f parentToChildSource) Query(_ context.Context, query string, params map[string]any) ([]map[string]any, error) {
	switch {
	case hasParam(params, "taxonomic"):
		if !strings.Contains(query, "MATCH (parent:Entity)-[:RELATES_TO]->(rel:RelatesToNode_)-[:RELATES_TO]->(child:Entity)") {
			return nil, nil
		}
		return filterParentToChildTaxonomy(f.taxonomy, params), nil
	case hasParam(params, "predicates"):
		return nil, nil
	default:
		return nil, nil
	}
}

func filterParentToChildTaxonomy(edges []parentChildEdge, params map[string]any) []map[string]any {
	refs := paramSet(params, "entity_refs")
	keys := paramSet(params, "entity_keys")
	preds := paramSet(params, "taxonomic")
	out := make([]map[string]any, 0, len(edges))
	for _, edge := range edges {
		child := EntityRef{ID: edge.child, Name: edge.child}
		if !matchesParamRef(refs, keys, child) {
			continue
		}
		if len(preds) > 0 && !preds[edge.predicate] {
			continue
		}
		out = append(out, map[string]any{
			"child_id":    edge.child,
			"child_name":  edge.child,
			"parent_id":   edge.parent,
			"parent_name": edge.parent,
			"predicate":   edge.predicate,
			"confidence":  1.0,
		})
	}
	return out
}

func filterTaxonomy(rows []map[string]any, params map[string]any) []map[string]any {
	refs := paramSet(params, "entity_refs")
	keys := paramSet(params, "entity_keys")
	preds := paramSet(params, "taxonomic")
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		child := EntityRef{
			ID:   firstString(row, "child_id", "source_id"),
			Name: firstString(row, "child_name", "source_name"),
		}
		if !matchesParamRef(refs, keys, child) {
			continue
		}
		if len(preds) > 0 && !preds[firstString(row, "predicate", "name")] {
			continue
		}
		out = append(out, row)
	}
	return out
}

func filterDirect(rows []map[string]any, params map[string]any) []map[string]any {
	refs := paramSet(params, "entity_refs")
	keys := paramSet(params, "entity_keys")
	preds := paramSet(params, "predicates")
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		source := EntityRef{
			ID:   firstString(row, "source_id", "child_id"),
			Name: firstString(row, "source_name", "child_name"),
		}
		if !matchesParamRef(refs, keys, source) || !preds[firstString(row, "predicate")] {
			continue
		}
		out = append(out, row)
	}
	return out
}

func paramSet(params map[string]any, key string) map[string]bool {
	out := map[string]bool{}
	values, _ := params[key].([]string)
	for _, value := range values {
		out[value] = true
	}
	return out
}

func hasParam(params map[string]any, key string) bool {
	_, ok := params[key]
	return ok
}

func matchesParamRef(refs map[string]bool, keys map[string]bool, ref EntityRef) bool {
	for _, key := range refKeys(ref) {
		if refs[key] || keys[key] {
			return true
		}
	}
	return false
}

func hasNode(g *Graph, id string, kind NodeKind) bool {
	for _, node := range g.Nodes {
		if node.ID == id && node.Kind == kind {
			return true
		}
	}
	return false
}

func findNode(g *Graph, id string, kind NodeKind) *Node {
	for i := range g.Nodes {
		node := &g.Nodes[i]
		if node.ID == id && node.Kind == kind {
			return node
		}
	}
	return nil
}

func findRelation(g *Graph, source, predicate, target string, lifted bool) *Relation {
	for i := range g.Relations {
		rel := &g.Relations[i]
		if rel.SourceID == source && rel.Predicate == predicate && rel.TargetID == target && rel.Lifted == lifted {
			return rel
		}
	}
	return nil
}

type reasonerFake struct {
	adj map[string][]fakeStep
}

type fakeStep struct {
	id         string
	predicate  string
	target     string
	confidence float64
}

func newReasonerFake(g *Graph) *reasonerFake {
	r := &reasonerFake{adj: map[string][]fakeStep{}}
	for _, rel := range g.Relations {
		source := strings.ToLower(rel.SourceID)
		r.adj[source] = append(r.adj[source], fakeStep{
			id:         rel.ID,
			predicate:  rel.Predicate,
			target:     rel.TargetID,
			confidence: positiveOr(rel.Confidence, 1),
		})
	}
	return r
}

func (r *reasonerFake) Query(_ context.Context, query string, params map[string]any) ([]map[string]any, error) {
	if strings.Contains(query, "OntologyDisjoint") {
		return nil, nil
	}
	source := anyString(params["source"])
	allowed := paramSet(params, "preds")
	paths := r.paths(strings.ToLower(source), allowed, 4)
	out := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		out = append(out, map[string]any{
			"predicates": path.predicates,
			"step_ids":   path.ids,
			"confs":      path.confidences,
			"target":     path.target,
			"hops":       len(path.predicates),
		})
	}
	return out, nil
}

type fakePath struct {
	target      string
	ids         []string
	predicates  []string
	confidences []float64
}

func (r *reasonerFake) paths(source string, allowed map[string]bool, maxHops int) []fakePath {
	var out []fakePath
	var walk func(string, fakePath, map[string]bool)
	walk = func(current string, path fakePath, seen map[string]bool) {
		if len(path.predicates) >= maxHops {
			return
		}
		for _, step := range r.adj[current] {
			if len(allowed) > 0 && !allowed[step.predicate] {
				continue
			}
			if seen[strings.ToLower(step.target)] {
				continue
			}
			next := fakePath{
				target:      step.target,
				ids:         append(append([]string{}, path.ids...), step.id),
				predicates:  append(append([]string{}, path.predicates...), step.predicate),
				confidences: append(append([]float64{}, path.confidences...), step.confidence),
			}
			out = append(out, next)
			nextSeen := map[string]bool{}
			for key, value := range seen {
				nextSeen[key] = value
			}
			nextSeen[strings.ToLower(step.target)] = true
			walk(strings.ToLower(step.target), next, nextSeen)
		}
	}
	walk(source, fakePath{}, map[string]bool{source: true})
	return out
}
