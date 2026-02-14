package db

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/soundprediction/pensiero/pkg/models"
)

type Repository struct {
	client *Client
}

func NewRepository(client *Client) *Repository {
	return &Repository{client: client}
}

func (r *Repository) Client() *Client {
	return r.client
}

// SaveEdge inserts or updates an epistemic edge
func (r *Repository) SaveEdge(edge *models.EpistemicEdge) error {
	query := `
	?[id, source, target, predicate, raw_predicate, status, confidence, context] :=
	  id = $id, source = $source, target = $target, predicate = $predicate,
	  raw_predicate = $raw_predicate, status = $status, confidence = $confidence,
	  context = $context
	:put epistemic_edge {id, source, target, predicate, raw_predicate, status, confidence, context}
	`

	params := map[string]interface{}{
		"id":            edge.ID,
		"source":        edge.Source,
		"target":        edge.Target,
		"predicate":     edge.Predicate,
		"raw_predicate": edge.RawPredicate,
		"status":        string(edge.Status),
		"confidence":    edge.Confidence,
		"context":       string(edge.Context),
	}

	_, err := r.client.Run(query, params)
	return err
}

// GetEdge retrieves an edge by ID
func (r *Repository) GetEdge(id string) (*models.EpistemicEdge, error) {
	query := fmt.Sprintf(`
	?[id, source, target, predicate, raw_predicate, status, confidence, context] := 
	  *epistemic_edge{id, source, target, predicate, raw_predicate, status, confidence, context},
	  id = "%s"
	`, id)

	res, err := r.client.Run(query, nil)
	if err != nil {
		return nil, err
	}

	rows, err := r.client.ParseResult(res)
	if err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("edge not found: %s", id)
	}

	row := rows[0]
	edge := &models.EpistemicEdge{
		ID:           row["id"].(string),
		Source:       row["source"].(string),
		Target:       row["target"].(string),
		Predicate:    row["predicate"].(string),
		RawPredicate: row["raw_predicate"].(string),
		Status:       models.EpistemicStatus(row["status"].(string)),
		Confidence:   row["confidence"].(float64),
	}

	if ctx, ok := row["context"].(string); ok {
		edge.Context = json.RawMessage(ctx)
	}

	return edge, nil
}

// ListEdgesBySource retrieves all edges starting from a specific node
func (r *Repository) ListEdgesBySource(source string) ([]*models.EpistemicEdge, error) {
	query := fmt.Sprintf(`
	?[id, source, target, predicate, raw_predicate, status, confidence, context] := 
	  *epistemic_edge{id, source, target, predicate, raw_predicate, status, confidence, context},
	  source = "%s"
	`, source)

	res, err := r.client.Run(query, nil)
	if err != nil {
		return nil, err
	}

	rows, err := r.client.ParseResult(res)
	if err != nil {
		return nil, err
	}

	edges := make([]*models.EpistemicEdge, len(rows))
	for i, row := range rows {
		edge := &models.EpistemicEdge{
			ID:           row["id"].(string),
			Source:       row["source"].(string),
			Target:       row["target"].(string),
			Predicate:    row["predicate"].(string),
			RawPredicate: row["raw_predicate"].(string),
			Status:       models.EpistemicStatus(row["status"].(string)),
			Confidence:   row["confidence"].(float64),
		}
		if ctx, ok := row["context"].(string); ok {
			edge.Context = json.RawMessage(ctx)
		}
		edges[i] = edge
	}

	return edges, nil
}

// SaveMetaRelation inserts or updates a meta relation
func (r *Repository) SaveMetaRelation(meta *models.MetaRelation) error {
	query := `
	?[id, head, body, frequency, confidence, provenance, created_at] :=
	  id = $id, head = $head, body = $body, frequency = $frequency,
	  confidence = $confidence, provenance = $provenance, created_at = $created_at
	:put meta_relation {id, head, body, frequency, confidence, provenance, created_at}
	`

	params := map[string]interface{}{
		"id":         meta.ID,
		"head":       meta.Head,
		"body":       string(meta.Body),
		"frequency":  meta.Frequency,
		"confidence": meta.Confidence,
		"provenance": string(meta.Provenance),
		"created_at": meta.CreatedAt.Format(time.RFC3339),
	}

	_, err := r.client.Run(query, params)
	return err
}
