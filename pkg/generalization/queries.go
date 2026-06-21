package generalization

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const taxonomyQueryChunkSize = 1000

func (b *Builder) scopeEntities(ctx context.Context) ([]EntityRef, error) {
	if len(b.cfg.ScopeEntities) > 0 {
		out := make([]EntityRef, 0, len(b.cfg.ScopeEntities))
		for _, raw := range b.cfg.ScopeEntities {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			out = append(out, EntityRef{ID: raw, Name: raw})
		}
		return out, nil
	}
	if strings.TrimSpace(b.cfg.Scope) == "" {
		return nil, fmt.Errorf("generalization: scope or scope entities required")
	}
	rows, err := b.source.Query(ctx, scopeEntitiesCypher(), map[string]any{"scope": b.cfg.Scope})
	if err != nil {
		return nil, fmt.Errorf("generalization scope read: %w", err)
	}
	out := make([]EntityRef, 0, len(rows))
	for _, row := range rows {
		ref := EntityRef{ID: anyString(row["id"]), Name: anyString(row["name"])}
		if ref.ID == "" {
			ref.ID = anyString(row["uuid"])
		}
		if nodeID(ref) == "" {
			continue
		}
		out = append(out, ref)
	}
	return out, nil
}

func (b *Builder) taxonomy(ctx context.Context, scope []EntityRef, taxonomic []string) ([]taxonomyRow, error) {
	if len(taxonomic) == 0 {
		return nil, nil
	}
	cfg := b.cfg.withDefaults()
	hierarchy := newTaxonomyHierarchy(scope)
	frontier := hierarchy.scopeRefs()
	queried := map[string]bool{}
	for level := 0; level < cfg.MaxParentLevel && len(frontier) > 0; level++ {
		frontier = unqueriedTaxonomyRefs(frontier, queried)
		if len(frontier) == 0 {
			break
		}
		scopeFilter := ""
		if level == 0 {
			scopeFilter = cfg.Scope
		}
		next := map[string]EntityRef{}
		for start := 0; start < len(frontier); start += taxonomyQueryChunkSize {
			end := start + taxonomyQueryChunkSize
			if end > len(frontier) {
				end = len(frontier)
			}
			edges, err := b.directTaxonomy(ctx, frontier[start:end], taxonomic, scopeFilter)
			if err != nil {
				return nil, err
			}
			for _, edge := range edges {
				if refsOverlap(edge.child, edge.parent) {
					continue
				}
				childKey := hierarchy.addNode(edge.child)
				parentKey := hierarchy.addNode(edge.parent)
				if childKey == "" || parentKey == "" || childKey == parentKey {
					continue
				}
				if hierarchy.addEdge(childKey, parentKey, edge) && !queried[parentKey] {
					if parent := hierarchy.nodes[parentKey]; nodeID(parent) != "" {
						next[parentKey] = parent
					}
				}
			}
		}
		frontier = sortedTaxonomyRefs(next)
	}
	levels, components := hierarchy.levels(cfg.MaxParentLevel)
	out := hierarchy.rows(levels, components, cfg.MaxParentLevel)
	sortTaxonomyRows(out)
	return out, nil
}

func (b *Builder) directTaxonomy(ctx context.Context, children []EntityRef, taxonomic []string, scopeFilter string) ([]taxonomyRow, error) {
	if len(children) == 0 {
		return nil, nil
	}
	params := map[string]any{
		"entity_keys": lowerRefs(children),
		"entity_refs": allRefs(children),
		"scope":       scopeFilter,
		"taxonomic":   taxonomic,
	}
	rows, err := b.source.Query(ctx, taxonomyCypher(b.cfg.TaxonomicDirection), params)
	if err != nil {
		return nil, fmt.Errorf("generalization hierarchy read: %w", err)
	}
	out := make([]taxonomyRow, 0, len(rows))
	for _, row := range rows {
		child := EntityRef{
			ID:   firstString(row, "child_id", "source_id", "child_uuid", "source_uuid"),
			Name: firstString(row, "child_name", "source_name"),
		}
		parent := EntityRef{
			ID:   firstString(row, "parent_id", "target_id", "parent_uuid", "target_uuid"),
			Name: firstString(row, "parent_name", "target_name"),
		}
		predicate := firstString(row, "predicate", "name")
		if predicate == "" && len(taxonomic) > 0 {
			predicate = taxonomic[0]
		}
		depth := anyInt(firstValue(row, "depth", "hops"))
		if depth <= 0 {
			depth = 1
		}
		out = append(out, taxonomyRow{
			child:      child,
			parent:     parent,
			predicate:  predicate,
			depth:      depth,
			confidence: anyFloat(firstValue(row, "confidence", "conf")),
		})
	}
	sortTaxonomyRows(out)
	return out, nil
}

func (b *Builder) directRelations(ctx context.Context, scope []EntityRef, predicates []string) ([]directRow, error) {
	if len(predicates) == 0 {
		return nil, nil
	}
	params := map[string]any{
		"entity_keys": lowerRefs(scope),
		"entity_refs": allRefs(scope),
		"predicates":  predicates,
		"scope":       b.cfg.Scope,
	}
	rows, err := b.source.Query(ctx, directRelationsCypher(), params)
	if err != nil {
		return nil, fmt.Errorf("generalization relation read: %w", err)
	}
	out := make([]directRow, 0, len(rows))
	for _, row := range rows {
		source := EntityRef{
			ID:   firstString(row, "source_id", "child_id", "source_uuid"),
			Name: firstString(row, "source_name", "child_name"),
		}
		target := EntityRef{
			ID:   firstString(row, "target_id", "object_id", "target_uuid"),
			Name: firstString(row, "target_name", "object_name"),
		}
		out = append(out, directRow{
			source:     source,
			target:     target,
			id:         firstString(row, "edge_id", "id", "rel_id", "uuid"),
			predicate:  firstString(row, "predicate", "name"),
			confidence: anyFloat(firstValue(row, "confidence", "conf")),
		})
	}
	return out, nil
}

func scopeEntitiesCypher() string {
	return `
MATCH (n:Entity)
WHERE n.group_id = $scope
RETURN n.uuid AS id, n.name AS name
`
}

func taxonomyCypher(direction TaxonomicDirection) string {
	if direction == TaxonomicDirectionParentToChild {
		return parentToChildTaxonomyCypher()
	}
	return childToParentTaxonomyCypher()
}

func childToParentTaxonomyCypher() string {
	return `
MATCH (child:Entity)-[:RELATES_TO]->(rel:RelatesToNode_)-[:RELATES_TO]->(parent:Entity)
WHERE (child.uuid IN $entity_refs OR child.name IN $entity_refs OR lower(child.name) IN $entity_keys)
  AND ($scope = '' OR child.group_id = $scope)
  AND rel.name IN $taxonomic
  AND child.uuid <> parent.uuid
RETURN child.uuid AS child_id,
       child.name AS child_name,
       parent.uuid AS parent_id,
       parent.name AS parent_name,
       1 AS depth,
       rel.name AS predicate,
       1.0 AS confidence
`
}

func parentToChildTaxonomyCypher() string {
	return `
MATCH (parent:Entity)-[:RELATES_TO]->(rel:RelatesToNode_)-[:RELATES_TO]->(child:Entity)
WHERE (child.uuid IN $entity_refs OR child.name IN $entity_refs OR lower(child.name) IN $entity_keys)
  AND ($scope = '' OR child.group_id = $scope)
  AND rel.name IN $taxonomic
  AND child.uuid <> parent.uuid
RETURN child.uuid AS child_id,
       child.name AS child_name,
       parent.uuid AS parent_id,
       parent.name AS parent_name,
       1 AS depth,
       rel.name AS predicate,
       1.0 AS confidence
`
}

func directRelationsCypher() string {
	return `
MATCH (source:Entity)-[:RELATES_TO]->(rel:RelatesToNode_)-[:RELATES_TO]->(target:Entity)
WHERE (source.uuid IN $entity_refs OR source.name IN $entity_refs OR lower(source.name) IN $entity_keys)
  AND ($scope = '' OR source.group_id = $scope)
  AND rel.name IN $predicates
RETURN source.uuid AS source_id,
       source.name AS source_name,
       target.uuid AS target_id,
       target.name AS target_name,
       rel.uuid AS edge_id,
       rel.name AS predicate,
       1.0 AS confidence
`
}

type taxonomyGraphEdge struct {
	childKey  string
	parentKey string
	row       taxonomyRow
}

type taxonomyHierarchy struct {
	nodes          map[string]EntityRef
	aliases        map[string]string
	scopeKeys      map[string]bool
	edges          map[string]taxonomyGraphEdge
	childParents   map[string][]string
	childParentSet map[string]map[string]bool
}

func newTaxonomyHierarchy(scope []EntityRef) *taxonomyHierarchy {
	h := &taxonomyHierarchy{
		nodes:          map[string]EntityRef{},
		aliases:        map[string]string{},
		scopeKeys:      map[string]bool{},
		edges:          map[string]taxonomyGraphEdge{},
		childParents:   map[string][]string{},
		childParentSet: map[string]map[string]bool{},
	}
	for _, ref := range scope {
		if key := h.addNode(ref); key != "" {
			h.scopeKeys[key] = true
		}
	}
	return h
}

func (h *taxonomyHierarchy) addNode(ref EntityRef) string {
	ref.ID = strings.TrimSpace(ref.ID)
	ref.Name = strings.TrimSpace(ref.Name)
	if nodeID(ref) == "" {
		return ""
	}
	for _, alias := range refKeys(ref) {
		if key := h.aliases[alias]; key != "" {
			h.mergeNode(key, ref)
			return key
		}
	}
	key := refKey(ref)
	if key == "" {
		return ""
	}
	h.nodes[key] = ref
	for _, alias := range refKeys(ref) {
		if _, ok := h.aliases[alias]; !ok {
			h.aliases[alias] = key
		}
	}
	return key
}

func (h *taxonomyHierarchy) mergeNode(key string, ref EntityRef) {
	current := h.nodes[key]
	if strings.TrimSpace(current.ID) == "" {
		current.ID = strings.TrimSpace(ref.ID)
	}
	if strings.TrimSpace(current.Name) == "" {
		current.Name = strings.TrimSpace(ref.Name)
	}
	h.nodes[key] = current
	for _, alias := range refKeys(ref) {
		if _, ok := h.aliases[alias]; !ok {
			h.aliases[alias] = key
		}
	}
}

func (h *taxonomyHierarchy) addEdge(childKey, parentKey string, row taxonomyRow) bool {
	predicate := strings.TrimSpace(row.predicate)
	edgeKey := strings.Join([]string{childKey, parentKey, predicate}, "\x00")
	row.child = h.nodes[childKey]
	row.parent = h.nodes[parentKey]
	row.predicate = predicate
	row.depth = 1
	row.confidence = positiveOr(row.confidence, 1)
	if existing, ok := h.edges[edgeKey]; ok {
		if row.confidence > existing.row.confidence {
			existing.row.confidence = row.confidence
			h.edges[edgeKey] = existing
		}
		return false
	}
	h.edges[edgeKey] = taxonomyGraphEdge{childKey: childKey, parentKey: parentKey, row: row}
	if h.childParentSet[childKey] == nil {
		h.childParentSet[childKey] = map[string]bool{}
	}
	if !h.childParentSet[childKey][parentKey] {
		h.childParentSet[childKey][parentKey] = true
		h.childParents[childKey] = append(h.childParents[childKey], parentKey)
	}
	return true
}

func (h *taxonomyHierarchy) scopeRefs() []EntityRef {
	refs := map[string]EntityRef{}
	for key := range h.scopeKeys {
		if ref := h.nodes[key]; nodeID(ref) != "" {
			refs[key] = ref
		}
	}
	return sortedTaxonomyRefs(refs)
}

func (h *taxonomyHierarchy) levels(maxLevel int) (map[string]int, map[string]int) {
	components := h.components()
	compParents := map[int]map[int]bool{}
	for child, parents := range h.childParents {
		childComp, ok := components[child]
		if !ok {
			continue
		}
		for _, parent := range parents {
			parentComp, ok := components[parent]
			if !ok || childComp == parentComp {
				continue
			}
			if compParents[childComp] == nil {
				compParents[childComp] = map[int]bool{}
			}
			compParents[childComp][parentComp] = true
		}
	}

	limit := maxLevel + 1
	if limit < 1 {
		limit = 1
	}
	compLevels := map[int]int{}
	queue := []int{}
	for key := range h.scopeKeys {
		if comp, ok := components[key]; ok {
			if _, seen := compLevels[comp]; !seen {
				compLevels[comp] = 0
				queue = append(queue, comp)
			}
		}
	}
	for len(queue) > 0 {
		comp := queue[0]
		queue = queue[1:]
		level := compLevels[comp]
		if level >= limit {
			continue
		}
		for parent := range compParents[comp] {
			next := level + 1
			if next > limit {
				next = limit
			}
			if old, ok := compLevels[parent]; ok && old >= next {
				continue
			}
			compLevels[parent] = next
			if next < limit {
				queue = append(queue, parent)
			}
		}
	}

	levels := map[string]int{}
	for key, comp := range components {
		if level, ok := compLevels[comp]; ok {
			levels[key] = level
		}
	}
	return levels, components
}

func (h *taxonomyHierarchy) components() map[string]int {
	index := 0
	component := 0
	stack := []string{}
	indexes := map[string]int{}
	lowlink := map[string]int{}
	onStack := map[string]bool{}
	components := map[string]int{}
	keys := sortedTaxonomyNodeKeys(h.nodes)

	var strongConnect func(string)
	strongConnect = func(node string) {
		indexes[node] = index
		lowlink[node] = index
		index++
		stack = append(stack, node)
		onStack[node] = true

		for _, parent := range h.childParents[node] {
			if _, ok := h.nodes[parent]; !ok {
				continue
			}
			if _, seen := indexes[parent]; !seen {
				strongConnect(parent)
				if lowlink[parent] < lowlink[node] {
					lowlink[node] = lowlink[parent]
				}
				continue
			}
			if onStack[parent] && indexes[parent] < lowlink[node] {
				lowlink[node] = indexes[parent]
			}
		}

		if lowlink[node] != indexes[node] {
			return
		}
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			components[last] = component
			if last == node {
				break
			}
		}
		component++
	}

	for _, key := range keys {
		if _, seen := indexes[key]; !seen {
			strongConnect(key)
		}
	}
	return components
}

func (h *taxonomyHierarchy) rows(levels map[string]int, components map[string]int, maxLevel int) []taxonomyRow {
	keys := make([]string, 0, len(h.edges))
	for key := range h.edges {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]taxonomyRow, 0, len(keys))
	for _, key := range keys {
		edge := h.edges[key]
		if components[edge.childKey] == components[edge.parentKey] {
			continue
		}
		childLevel, ok := levels[edge.childKey]
		if !ok {
			continue
		}
		parentLevel, ok := levels[edge.parentKey]
		if !ok || parentLevel <= 0 || parentLevel > maxLevel || parentLevel <= childLevel {
			continue
		}
		row := edge.row
		row.child = h.nodes[edge.childKey]
		row.parent = h.nodes[edge.parentKey]
		row.depth = parentLevel
		out = append(out, row)
	}
	return out
}

func unqueriedTaxonomyRefs(refs []EntityRef, queried map[string]bool) []EntityRef {
	out := map[string]EntityRef{}
	for _, ref := range refs {
		key := refKey(ref)
		if key == "" || queried[key] {
			continue
		}
		queried[key] = true
		out[key] = ref
	}
	return sortedTaxonomyRefs(out)
}

func sortedTaxonomyRefs(refs map[string]EntityRef) []EntityRef {
	keys := make([]string, 0, len(refs))
	for key := range refs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]EntityRef, 0, len(keys))
	for _, key := range keys {
		out = append(out, refs[key])
	}
	return out
}

func sortedTaxonomyNodeKeys(nodes map[string]EntityRef) []string {
	keys := make([]string, 0, len(nodes))
	for key := range nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func refsOverlap(a EntityRef, b EntityRef) bool {
	keys := map[string]bool{}
	for _, key := range refKeys(a) {
		keys[key] = true
	}
	for _, key := range refKeys(b) {
		if keys[key] {
			return true
		}
	}
	return false
}

func sortTaxonomyRows(rows []taxonomyRow) {
	sort.Slice(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.depth != right.depth {
			return left.depth < right.depth
		}
		if refKey(left.child) != refKey(right.child) {
			return refKey(left.child) < refKey(right.child)
		}
		if refKey(left.parent) != refKey(right.parent) {
			return refKey(left.parent) < refKey(right.parent)
		}
		return left.predicate < right.predicate
	})
}

func allRefs(refs []EntityRef) []string {
	set := map[string]bool{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.ID) != "" {
			set[strings.TrimSpace(ref.ID)] = true
		}
		if strings.TrimSpace(ref.Name) != "" {
			set[strings.TrimSpace(ref.Name)] = true
		}
	}
	return sortedKeys(set)
}

func lowerRefs(refs []EntityRef) []string {
	set := map[string]bool{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.ID) != "" {
			set[strings.ToLower(strings.TrimSpace(ref.ID))] = true
		}
		if strings.TrimSpace(ref.Name) != "" {
			set[strings.ToLower(strings.TrimSpace(ref.Name))] = true
		}
	}
	return sortedKeys(set)
}

func firstValue(row map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			return value
		}
	}
	return nil
}

func firstString(row map[string]any, keys ...string) string {
	return anyString(firstValue(row, keys...))
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case fmt.Stringer:
		return strings.TrimSpace(t.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

func anyInt(v any) int {
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

func anyFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case int32:
		return float64(t)
	default:
		return 0
	}
}
