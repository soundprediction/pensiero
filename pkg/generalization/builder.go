package generalization

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

func NewBuilder(source reasoning.GraphQuerier, reg *reasoning.PredicateRegistry, cfg Config) *Builder {
	if reg == nil {
		reg = reasoning.DefaultGeneralRegistry()
	}
	return &Builder{source: source, reg: reg, cfg: cfg.withDefaults()}
}

func Build(ctx context.Context, source reasoning.GraphQuerier, reg *reasoning.PredicateRegistry, cfg Config) (*Graph, error) {
	return NewBuilder(source, reg, cfg).Build(ctx)
}

func (b *Builder) Build(ctx context.Context) (*Graph, error) {
	if b.source == nil {
		return nil, fmt.Errorf("generalization: nil source")
	}
	if !b.cfg.TaxonomicDirection.valid() {
		return nil, fmt.Errorf("generalization: invalid taxonomic direction %q", b.cfg.TaxonomicDirection)
	}
	scope, err := b.scopeEntities(ctx)
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 {
		return nil, fmt.Errorf("generalization: empty scope")
	}
	taxonomic := canonicalList(b.reg, b.cfg.TaxonomicPredicates)
	if len(taxonomic) == 0 {
		taxonomic = []string{"is_a"}
	}
	predicates := canonicalList(b.reg, b.cfg.Predicates)
	if len(predicates) == 0 {
		predicates = InheritablePredicates(b.reg, taxonomic)
	}
	taxRows, err := b.taxonomy(ctx, scope, taxonomicQueryList(b.reg, b.cfg.TaxonomicPredicates, taxonomic))
	if err != nil {
		return nil, err
	}
	directRows, err := b.directRelations(ctx, scope, queryPredicateList(b.reg, b.cfg.Predicates, predicates))
	if err != nil {
		return nil, err
	}
	return assembleGraph(b.cfg, b.reg, scope, taxonomic, taxRows, directRows), nil
}

func (c Config) withDefaults() Config {
	if c.MinSupport <= 0 {
		c.MinSupport = DefaultMinSupport
	}
	if c.MaxParentLevel <= 0 {
		c.MaxParentLevel = DefaultMaxParentLevel
	}
	if c.MinParentSupport <= 0 {
		c.MinParentSupport = 1
	}
	if direction := strings.TrimSpace(string(c.TaxonomicDirection)); direction == "" {
		c.TaxonomicDirection = TaxonomicDirectionChildToParent
	} else {
		c.TaxonomicDirection = TaxonomicDirection(direction)
	}
	for i := range c.ScopeEntities {
		c.ScopeEntities[i] = strings.TrimSpace(c.ScopeEntities[i])
	}
	return c
}

func InheritablePredicates(reg *reasoning.PredicateRegistry, taxonomic []string) []string {
	if reg == nil {
		reg = reasoning.DefaultGeneralRegistry()
	}
	taxSet := stringSet(canonicalList(reg, taxonomic))
	out := map[string]bool{}
	for _, rule := range reg.Compositions() {
		first, _ := reg.Canonical(rule.First)
		second, _ := reg.Canonical(rule.Second)
		result, _ := reg.Canonical(rule.Result)
		if taxSet[first.Canonical] && second.Canonical != "" && second.Canonical == result.Canonical {
			out[second.Canonical] = true
		}
	}
	return sortedKeys(out)
}

func assembleGraph(cfg Config, reg *reasoning.PredicateRegistry, scope []EntityRef, taxonomic []string, taxRows []taxonomyRow, directRows []directRow) *Graph {
	cfg = cfg.withDefaults()
	scopeKeys := map[string]bool{}
	for _, ref := range scope {
		addRefKeys(scopeKeys, ref)
	}

	parents, childParents := selectParents(cfg, taxRows)
	g := &graphAssembler{
		graph: Graph{
			Scope: cfg.Scope,
			Stats: Stats{ParentLevelCounts: map[int]int{}},
		},
		nodes:     map[string]*Node{},
		relations: map[string]bool{},
	}
	for _, ref := range scope {
		g.addNode(ref, NodeScope, 0, 1)
	}
	for _, p := range parents {
		g.addNode(p.ref, NodeConcept, p.depth, len(p.children))
		g.graph.Stats.ParentLevelCounts[p.depth]++
	}
	for _, row := range taxRows {
		parentKey := refKey(row.parent)
		if _, ok := parents[parentKey]; !ok {
			continue
		}
		childKey := refKey(row.child)
		if !matchesRef(scopeKeys, row.child) {
			if _, ok := parents[childKey]; !ok {
				continue
			}
		}
		if childKey == "" || childKey == parentKey {
			continue
		}
		predicate := firstOr(row.predicate, taxonomic)
		meta, _ := reg.Canonical(predicate)
		predicate = meta.Canonical
		g.addRelation(Relation{
			ID:         stableRelationID(cfg.Scope, row.child, predicate, row.parent, false),
			SourceID:   nodeID(row.child),
			SourceName: nodeName(row.child),
			Predicate:  predicate,
			TargetID:   nodeID(row.parent),
			TargetName: nodeName(row.parent),
			Confidence: positiveOr(row.confidence, 1),
			Support:    1,
		})
	}

	directByChild := map[string][]directRow{}
	for _, row := range directRows {
		sourceKey := refKey(row.source)
		if !matchesRef(scopeKeys, row.source) {
			continue
		}
		meta, _ := reg.Canonical(row.predicate)
		row.predicate = meta.Canonical
		row.confidence = positiveOr(row.confidence, 1)
		directByChild[sourceKey] = append(directByChild[sourceKey], row)
		g.addNode(row.target, NodeEndpoint, 0, 0)
		id := strings.TrimSpace(row.id)
		if id == "" {
			id = stableRelationID(cfg.Scope, row.source, row.predicate, row.target, false)
		}
		g.addRelation(Relation{
			ID:         id,
			SourceID:   nodeID(row.source),
			SourceName: nodeName(row.source),
			Predicate:  row.predicate,
			TargetID:   nodeID(row.target),
			TargetName: nodeName(row.target),
			Sources:    compactStrings([]string{row.id}),
			Confidence: row.confidence,
			Support:    1,
		})
	}

	lifted := liftRelations(cfg, parents, childParents, directByChild)
	for _, rel := range lifted {
		g.addNode(EntityRef{ID: rel.TargetID, Name: rel.TargetName}, NodeEndpoint, 0, 0)
		g.addRelation(rel)
	}

	g.finish()
	return &g.graph
}

type parentState struct {
	ref      EntityRef
	children map[string]bool
	depth    int
}

func selectParents(cfg Config, rows []taxonomyRow) (map[string]*parentState, map[string][]string) {
	cfg = cfg.withDefaults()
	parents := map[string]*parentState{}
	childParents := map[string][]string{}
	childrenByParent := map[string][]string{}
	for _, row := range rows {
		childKey := refKey(row.child)
		parentKey := refKey(row.parent)
		if childKey == "" || parentKey == "" || childKey == parentKey {
			continue
		}
		if row.depth <= 0 || row.depth > cfg.MaxParentLevel {
			continue
		}
		state := parents[parentKey]
		if state == nil {
			state = &parentState{ref: row.parent, depth: row.depth, children: map[string]bool{}}
			parents[parentKey] = state
		}
		if row.depth > state.depth {
			state.depth = row.depth
		}
		state.children[childKey] = true
		childrenByParent[parentKey] = append(childrenByParent[parentKey], childKey)
	}

	kept := map[string]bool{}
	queue := []string{}
	for key, state := range parents {
		if len(state.children) >= cfg.MinParentSupport {
			kept[key] = true
			queue = append(queue, key)
		}
	}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range childrenByParent[parent] {
			if _, ok := parents[child]; !ok || kept[child] {
				continue
			}
			kept[child] = true
			queue = append(queue, child)
		}
	}

	for key := range parents {
		if !kept[key] {
			delete(parents, key)
		}
	}
	for key, state := range parents {
		for child := range state.children {
			childParents[child] = append(childParents[child], key)
		}
	}
	for child := range childParents {
		sort.Strings(childParents[child])
	}
	return parents, childParents
}

type liftBucket struct {
	parent    *parentState
	target    EntityRef
	sources   []string
	children  map[string]bool
	predicate string
	confSum   float64
	count     int
}

type liftInput struct {
	target     EntityRef
	sources    []string
	predicate  string
	confidence float64
}

func liftRelations(cfg Config, parents map[string]*parentState, childParents map[string][]string, directByChild map[string][]directRow) []Relation {
	cfg = cfg.withDefaults()
	current := map[string][]liftInput{}
	for childKey, rows := range directByChild {
		for _, row := range rows {
			current[childKey] = append(current[childKey], liftInput{
				target:     row.target,
				sources:    compactStrings([]string{row.id}),
				predicate:  row.predicate,
				confidence: positiveOr(row.confidence, 1),
			})
		}
	}

	out := []Relation{}
	emitted := map[string]bool{}
	for level := 1; level <= cfg.MaxParentLevel && len(current) > 0; level++ {
		buckets := map[string]*liftBucket{}
		for childKey, rows := range current {
			for _, parentKey := range childParents[childKey] {
				parent := parents[parentKey]
				if parent == nil {
					continue
				}
				for _, row := range rows {
					key := strings.Join([]string{parentKey, row.predicate, refKey(row.target)}, "\x00")
					b := buckets[key]
					if b == nil {
						b = &liftBucket{
							parent:    parent,
							target:    row.target,
							predicate: row.predicate,
							children:  map[string]bool{},
						}
						buckets[key] = b
					}
					b.children[childKey] = true
					b.sources = append(b.sources, row.sources...)
					b.confSum += positiveOr(row.confidence, 1)
					b.count++
				}
			}
		}
		if len(buckets) == 0 {
			break
		}
		next := map[string][]liftInput{}
		keys := make([]string, 0, len(buckets))
		for key := range buckets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			b := buckets[key]
			support := len(b.children)
			coverage := 1.0
			if len(b.parent.children) > 0 {
				coverage = float64(support) / float64(len(b.parent.children))
			}
			mean := 1.0
			if b.count > 0 {
				mean = b.confSum / float64(b.count)
			}
			confidence := mean * coverage
			sources := compactStrings(b.sources)
			rel := Relation{
				ID:         stableRelationID(cfg.Scope, b.parent.ref, b.predicate, b.target, true),
				SourceID:   nodeID(b.parent.ref),
				SourceName: nodeName(b.parent.ref),
				Predicate:  b.predicate,
				TargetID:   nodeID(b.target),
				TargetName: nodeName(b.target),
				Sources:    sources,
				Confidence: confidence,
				Support:    support,
				Lifted:     true,
			}
			if support >= cfg.MinSupport && !emitted[rel.ID] {
				emitted[rel.ID] = true
				out = append(out, rel)
			}
			parentKey := refKey(b.parent.ref)
			if len(childParents[parentKey]) == 0 {
				continue
			}
			next[parentKey] = append(next[parentKey], liftInput{
				target:     b.target,
				sources:    sources,
				predicate:  b.predicate,
				confidence: confidence,
			})
		}
		current = next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type graphAssembler struct {
	graph     Graph
	nodes     map[string]*Node
	relations map[string]bool
}

func (g *graphAssembler) addNode(ref EntityRef, kind NodeKind, depth, support int) {
	id := nodeID(ref)
	if id == "" {
		return
	}
	n := g.nodes[id]
	if n == nil {
		n = &Node{ID: id, Name: nodeName(ref), Kind: kind, Depth: depth, Support: support}
		g.nodes[id] = n
		return
	}
	if n.Name == "" {
		n.Name = nodeName(ref)
	}
	if nodeRank(kind) < nodeRank(n.Kind) {
		n.Kind = kind
	}
	if depth > 0 && (n.Depth == 0 || depth < n.Depth) {
		n.Depth = depth
	}
	if support > n.Support {
		n.Support = support
	}
}

func (g *graphAssembler) addRelation(rel Relation) {
	if rel.ID == "" || rel.SourceID == "" || rel.TargetID == "" || rel.Predicate == "" {
		return
	}
	if g.relations[rel.ID] {
		return
	}
	g.relations[rel.ID] = true
	g.graph.Relations = append(g.graph.Relations, rel)
	if rel.Lifted {
		g.graph.Stats.LiftedRelationCount++
	} else {
		g.graph.Stats.DirectRelationCount++
	}
}

func (g *graphAssembler) finish() {
	g.graph.Nodes = make([]Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		g.graph.Nodes = append(g.graph.Nodes, *n)
		switch n.Kind {
		case NodeScope:
			g.graph.Stats.ScopeEntityCount++
		case NodeConcept:
			g.graph.Stats.ConceptCount++
		case NodeEndpoint:
			g.graph.Stats.EndpointCount++
		}
	}
	sort.Slice(g.graph.Nodes, func(i, j int) bool { return g.graph.Nodes[i].ID < g.graph.Nodes[j].ID })
	sort.Slice(g.graph.Relations, func(i, j int) bool { return g.graph.Relations[i].ID < g.graph.Relations[j].ID })
	g.graph.Stats.NodeCount = len(g.graph.Nodes)
	g.graph.Stats.RelationCount = len(g.graph.Relations)
}

func nodeRank(kind NodeKind) int {
	switch kind {
	case NodeScope:
		return 0
	case NodeConcept:
		return 1
	default:
		return 2
	}
}

func canonicalList(reg *reasoning.PredicateRegistry, in []string) []string {
	if reg == nil {
		reg = reasoning.DefaultGeneralRegistry()
	}
	set := map[string]bool{}
	for _, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		meta, _ := reg.Canonical(raw)
		set[meta.Canonical] = true
	}
	return sortedKeys(set)
}

func queryPredicateList(reg *reasoning.PredicateRegistry, raw []string, canonical []string) []string {
	if values := exactList(raw); len(values) > 0 {
		return values
	}
	set := map[string]bool{}
	for _, value := range canonical {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = true
		}
	}
	return sortedKeys(set)
}

func taxonomicQueryList(reg *reasoning.PredicateRegistry, raw []string, canonical []string) []string {
	if values := exactList(raw); len(values) > 0 {
		return values
	}
	list := queryPredicateList(reg, raw, canonical)
	set := stringSet(list)
	if set["is_a"] {
		for _, value := range []string{"is_a", "is a", "isa", "subclass of", "subclass_of", "subClassOf", "type of", "subtype of"} {
			set[value] = true
		}
	}
	return sortedKeys(set)
}

func exactList(in []string) []string {
	set := map[string]bool{}
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = true
		}
	}
	return sortedKeys(set)
}

func stringSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}

func addRefKeys(keys map[string]bool, ref EntityRef) {
	for _, key := range refKeys(ref) {
		keys[key] = true
	}
}

func matchesRef(keys map[string]bool, ref EntityRef) bool {
	for _, key := range refKeys(ref) {
		if keys[key] {
			return true
		}
	}
	return false
}

func refKeys(ref EntityRef) []string {
	out := make([]string, 0, 4)
	if id := strings.TrimSpace(ref.ID); id != "" {
		out = append(out, id, strings.ToLower(id))
	}
	if name := strings.TrimSpace(ref.Name); name != "" {
		out = append(out, name, strings.ToLower(name))
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func compactStrings(in []string) []string {
	set := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			set[s] = true
		}
	}
	return sortedKeys(set)
}

func firstOr(value string, fallback []string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	if len(fallback) > 0 {
		return fallback[0]
	}
	return ""
}

func positiveOr(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func refKey(ref EntityRef) string {
	if strings.TrimSpace(ref.ID) != "" {
		return strings.TrimSpace(ref.ID)
	}
	return strings.ToLower(strings.TrimSpace(ref.Name))
}

func nodeID(ref EntityRef) string {
	if strings.TrimSpace(ref.ID) != "" {
		return strings.TrimSpace(ref.ID)
	}
	return strings.TrimSpace(ref.Name)
}

func nodeName(ref EntityRef) string {
	if strings.TrimSpace(ref.Name) != "" {
		return strings.TrimSpace(ref.Name)
	}
	return strings.TrimSpace(ref.ID)
}

func stableRelationID(scope string, source EntityRef, predicate string, target EntityRef, lifted bool) string {
	parts := []string{scope, nodeID(source), predicate, nodeID(target)}
	if lifted {
		parts = append(parts, "lifted")
	}
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return "gg-" + hex.EncodeToString(h[:10])
}
