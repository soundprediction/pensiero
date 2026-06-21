package generalization

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

// DefaultEmbeddingDim is the fixed embedding width emitted for the name/fact
// embedding columns. A FIXED-size FLOAT[N] (not an unbounded FLOAT[]) is required
// for CREATE_VECTOR_INDEX to build an HNSW index over the column, so hosts can
// resolve names by embedding similarity over the emitted subgraph itself.
const DefaultEmbeddingDim = 1024

type CypherEmitter struct {
	target       reasoning.GraphQuerier
	scope        string
	embeddingDim int
}

func NewCypherEmitter(target reasoning.GraphQuerier, scope string) *CypherEmitter {
	return &CypherEmitter{target: target, scope: scope, embeddingDim: DefaultEmbeddingDim}
}

// WithEmbeddingDim overrides the emitted embedding width (default 1024).
func (e *CypherEmitter) WithEmbeddingDim(dim int) *CypherEmitter {
	if dim > 0 {
		e.embeddingDim = dim
	}
	return e
}

func (e *CypherEmitter) Emit(ctx context.Context, g *Graph) error {
	if e == nil || e.target == nil {
		return fmt.Errorf("generalization emit: nil target")
	}
	if g == nil {
		return fmt.Errorf("generalization emit: nil graph")
	}
	if err := e.createSchema(ctx); err != nil {
		return err
	}
	for _, node := range g.Nodes {
		if err := e.createNode(ctx, node); err != nil {
			return err
		}
	}
	for _, rel := range g.Relations {
		if err := e.createRelation(ctx, rel); err != nil {
			return err
		}
	}
	return nil
}

func (e *CypherEmitter) createSchema(ctx context.Context) error {
	dim := e.embeddingDim
	if dim <= 0 {
		dim = DefaultEmbeddingDim
	}
	_, err := e.target.Query(ctx, fmt.Sprintf(`
CREATE NODE TABLE IF NOT EXISTS Entity (
    uuid STRING PRIMARY KEY,
    name STRING,
    group_id STRING,
    labels STRING[],
    created_at TIMESTAMP,
    name_embedding FLOAT[%d],
    summary STRING,
    attributes STRING
);
CREATE NODE TABLE IF NOT EXISTS RelatesToNode_ (
    uuid STRING PRIMARY KEY,
    group_id STRING,
    created_at TIMESTAMP,
    name STRING,
    fact STRING,
    fact_embedding FLOAT[%d],
    episodes STRING[],
    attributes STRING,
    confidence DOUBLE,
    support INT64
);
CREATE REL TABLE IF NOT EXISTS RELATES_TO(
    FROM Entity TO RelatesToNode_,
    FROM RelatesToNode_ TO Entity
);
`, dim, dim), nil)
	if err != nil {
		return fmt.Errorf("generalization schema emit: %w", err)
	}
	return nil
}

func (e *CypherEmitter) createNode(ctx context.Context, node Node) error {
	attrs, err := json.Marshal(map[string]any{
		"kind":    node.Kind,
		"depth":   node.Depth,
		"support": node.Support,
	})
	if err != nil {
		return err
	}
	params := map[string]any{
		"attributes": string(attrs),
		"created_at": time.Now(),
		"labels":     []string{string(node.Kind)},
		"name":       node.Name,
		"scope":      firstOr(e.scope, []string{"generalization"}),
		"summary":    "",
		"uuid":       node.ID,
	}
	// name_embedding (FLOAT[]) is intentionally omitted: ladybug rejects an empty
	// LIST value and the native reasoner resolves nodes by name/PK, not by embedding.
	_, err = e.target.Query(ctx, `
CREATE (n:Entity {
    uuid: $uuid,
    name: $name,
    group_id: $scope,
    labels: $labels,
    created_at: $created_at,
    summary: $summary,
    attributes: $attributes
})
`, params)
	if err != nil {
		return fmt.Errorf("generalization node emit %q: %w", node.ID, err)
	}
	return nil
}

func (e *CypherEmitter) createRelation(ctx context.Context, rel Relation) error {
	attrs, err := json.Marshal(map[string]any{
		"lifted":  rel.Lifted,
		"sources": rel.Sources,
		"support": rel.Support,
	})
	if err != nil {
		return err
	}
	params := map[string]any{
		"attributes":  string(attrs),
		"confidence":  rel.Confidence,
		"created_at":  time.Now(),
		"fact":        fmt.Sprintf("%s %s %s", rel.SourceName, rel.Predicate, rel.TargetName),
		"name":        rel.Predicate,
		"scope":       firstOr(e.scope, []string{"generalization"}),
		"source_uuid": rel.SourceID,
		"support":     int64(rel.Support),
		"target_uuid": rel.TargetID,
		"uuid":        rel.ID,
	}
	// fact_embedding (FLOAT[]) and episodes (STRING[]) are omitted: ladybug rejects
	// empty LIST values and the native reasoner does not use them.
	_, err = e.target.Query(ctx, `
CREATE (r:RelatesToNode_ {
    uuid: $uuid,
    group_id: $scope,
    created_at: $created_at,
    name: $name,
    fact: $fact,
    attributes: $attributes,
    confidence: $confidence,
    support: $support
})
`, params)
	if err != nil {
		return fmt.Errorf("generalization relation node emit %q: %w", rel.ID, err)
	}
	_, err = e.target.Query(ctx, `
MATCH (source:Entity {uuid: $source_uuid})
MATCH (rel:RelatesToNode_ {uuid: $uuid})
CREATE (source)-[:RELATES_TO]->(rel)
`, params)
	if err != nil {
		return fmt.Errorf("generalization relation source emit %q: %w", rel.ID, err)
	}
	_, err = e.target.Query(ctx, `
MATCH (rel:RelatesToNode_ {uuid: $uuid})
MATCH (target:Entity {uuid: $target_uuid})
CREATE (rel)-[:RELATES_TO]->(target)
`, params)
	if err != nil {
		return fmt.Errorf("generalization relation target emit %q: %w", rel.ID, err)
	}
	return nil
}
