package generalization

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const droppedEdgeBackupMarkerPrefix = "dropped_by:build-generalization"

var droppedEdgeBackupSequence atomic.Uint64

type CypherDroppedEdgeBackup struct {
	target reasoning.GraphQuerier
}

func NewCypherDroppedEdgeBackup(target reasoning.GraphQuerier) *CypherDroppedEdgeBackup {
	return &CypherDroppedEdgeBackup{target: target}
}

func AllDirectRelations(ctx context.Context, source reasoning.GraphQuerier, reg *reasoning.PredicateRegistry, cfg Config) ([]Relation, error) {
	return NewBuilder(source, reg, cfg).AllDirectRelations(ctx)
}

func (b *Builder) AllDirectRelations(ctx context.Context) ([]Relation, error) {
	if b == nil || b.source == nil {
		return nil, fmt.Errorf("generalization: nil source")
	}
	if err := checkBuildContext(ctx); err != nil {
		return nil, err
	}
	scope, err := b.scopeEntities(ctx)
	if err != nil {
		return nil, err
	}
	predicates, err := b.sourcePredicates(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := b.directRelations(ctx, scope, predicates)
	if err != nil {
		return nil, err
	}
	return directRowsToRelations(ctx, b.cfg.Scope, b.reg, rows)
}

func DroppedRelations(ctx context.Context, source reasoning.GraphQuerier, reg *reasoning.PredicateRegistry, cfg Config, graph *Graph) ([]Relation, error) {
	return NewBuilder(source, reg, cfg).DroppedRelations(ctx, graph)
}

func (b *Builder) DroppedRelations(ctx context.Context, graph *Graph) ([]Relation, error) {
	sourceRelations, err := b.AllDirectRelations(ctx)
	if err != nil {
		return nil, err
	}
	return DiffDroppedRelations(ctx, sourceRelations, graph)
}

func DiffDroppedRelations(ctx context.Context, sourceRelations []Relation, graph *Graph) ([]Relation, error) {
	if graph == nil {
		return nil, fmt.Errorf("generalization dropped relation diff: nil graph")
	}
	kept := map[string]bool{}
	for _, rel := range graph.Relations {
		if err := checkBuildContext(ctx); err != nil {
			return nil, err
		}
		kept[relationDiffKey(rel)] = true
	}
	dropped := make([]Relation, 0, len(sourceRelations))
	for _, rel := range sourceRelations {
		if err := checkBuildContext(ctx); err != nil {
			return nil, err
		}
		if !kept[relationDiffKey(rel)] {
			dropped = append(dropped, rel)
		}
	}
	sortRelations(dropped)
	return dropped, nil
}

func (b *CypherDroppedEdgeBackup) Record(ctx context.Context, scope string, dropped []Relation) error {
	if b == nil || b.target == nil {
		return fmt.Errorf("generalization dropped-edge backup: nil target")
	}
	if err := checkBuildContext(ctx); err != nil {
		return err
	}
	if len(dropped) == 0 {
		return nil
	}
	graph, err := droppedEdgeBackupGraph(ctx, scope, dropped)
	if err != nil {
		return err
	}
	return NewCypherEmitter(b.target, scope).Emit(ctx, graph)
}

func directRowsToRelations(ctx context.Context, scope string, reg *reasoning.PredicateRegistry, rows []directRow) ([]Relation, error) {
	if reg == nil {
		reg = reasoning.DefaultGeneralRegistry()
	}
	out := make([]Relation, 0, len(rows))
	for _, row := range rows {
		if err := checkBuildContext(ctx); err != nil {
			return nil, err
		}
		meta, _ := reg.Canonical(row.predicate)
		predicate := meta.Canonical
		id := strings.TrimSpace(row.id)
		if id == "" {
			id = stableRelationID(scope, row.source, predicate, row.target, false)
		}
		rel := Relation{
			ID:         id,
			SourceID:   nodeID(row.source),
			SourceName: nodeName(row.source),
			Predicate:  predicate,
			TargetID:   nodeID(row.target),
			TargetName: nodeName(row.target),
			Sources:    compactStrings([]string{row.id}),
			Confidence: positiveOr(row.confidence, 1),
			Support:    1,
		}
		if rel.SourceID == "" || rel.Predicate == "" || rel.TargetID == "" {
			continue
		}
		out = append(out, rel)
	}
	sortRelations(out)
	return out, nil
}

func droppedEdgeBackupGraph(ctx context.Context, scope string, dropped []Relation) (*Graph, error) {
	nodes := map[string]Node{}
	relations := make([]Relation, 0, len(dropped))
	marker := droppedEdgeBackupMarker(scope)
	batch := droppedEdgeBackupBatch()
	for i, rel := range dropped {
		if err := checkBuildContext(ctx); err != nil {
			return nil, err
		}
		rel.ID = droppedEdgeBackupRelationID(scope, batch, i, rel)
		rel.Sources = compactStrings(append(append([]string{}, rel.Sources...), marker))
		if rel.Confidence <= 0 {
			rel.Confidence = 1
		}
		if rel.Support <= 0 {
			rel.Support = 1
		}
		addDroppedEndpoint(nodes, rel.SourceID, rel.SourceName)
		addDroppedEndpoint(nodes, rel.TargetID, rel.TargetName)
		relations = append(relations, rel)
	}
	out := &Graph{
		Scope:     scope,
		Nodes:     sortedDroppedEndpoints(nodes),
		Relations: relations,
	}
	sortRelations(out.Relations)
	return out, nil
}

func addDroppedEndpoint(nodes map[string]Node, id string, name string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	name = strings.TrimSpace(name)
	node := nodes[id]
	if node.ID == "" {
		nodes[id] = Node{
			ID:      id,
			Name:    name,
			Kind:    NodeEndpoint,
			Support: 1,
		}
		return
	}
	if node.Name == "" {
		node.Name = name
		nodes[id] = node
	}
}

func sortedDroppedEndpoints(nodes map[string]Node) []Node {
	out := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func droppedEdgeBackupMarker(scope string) string {
	return droppedEdgeBackupMarkerPrefix + ":" + strings.TrimSpace(scope)
}

func droppedEdgeBackupBatch() string {
	sequence := droppedEdgeBackupSequence.Add(1)
	return strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + strconv.FormatUint(sequence, 10)
}

func droppedEdgeBackupRelationID(scope string, batch string, index int, rel Relation) string {
	backupScope := strings.Join([]string{
		"dropped-edge-backup",
		scope,
		batch,
		strconv.Itoa(index),
		strings.TrimSpace(rel.ID),
	}, "\x00")
	return stableRelationID(backupScope, EntityRef{ID: rel.SourceID, Name: rel.SourceName}, rel.Predicate, EntityRef{ID: rel.TargetID, Name: rel.TargetName}, rel.Lifted)
}

func relationDiffKey(rel Relation) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(rel.SourceName)),
		strings.ToLower(strings.TrimSpace(rel.Predicate)),
		strings.ToLower(strings.TrimSpace(rel.TargetName)),
	}, "|")
}

func sortRelations(relations []Relation) {
	sort.Slice(relations, func(i, j int) bool {
		left := relationDiffKey(relations[i])
		right := relationDiffKey(relations[j])
		if left != right {
			return left < right
		}
		return relations[i].ID < relations[j].ID
	})
}
