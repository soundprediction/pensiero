package db

import (
	"encoding/json"
	"testing"

	"github.com/soundprediction/pensiero/pkg/models"
)

func TestCozoFoundation(t *testing.T) {
	// Initialize in-memory CozoDB
	client, err := NewClient("mem", "", nil)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Initialize Schema
	if err := client.InitSchema(); err != nil {
		t.Fatalf("Failed to initialize schema: %v", err)
	}

	repo := NewRepository(client)

	// Create a test edge
	ctx := models.Context{
		Confidence: 0.95,
		Conditions: []models.Condition{
			{Type: "test", Value: "smoke"},
		},
	}
	ctxRaw, _ := json.Marshal(ctx)

	edge := &models.EpistemicEdge{
		ID:           "e-1",
		Source:       "A",
		Target:       "B",
		Predicate:    "connected_to",
		RawPredicate: "is connected to",
		Status:       models.StatusObservation,
		Confidence:   0.95,
		Context:      json.RawMessage(ctxRaw),
	}

	// Save
	if err := repo.SaveEdge(edge); err != nil {
		t.Fatalf("Failed to save edge: %v", err)
	}

	// Retrieve
	retrieved, err := repo.GetEdge("e-1")
	if err != nil {
		t.Fatalf("Failed to get edge: %v", err)
	}

	if retrieved.Source != "A" || retrieved.Target != "B" {
		t.Errorf("Retrieved edge mismatch: got %+v", retrieved)
	}

	// List
	edges, err := repo.ListEdgesBySource("A")
	if err != nil {
		t.Fatalf("Failed to list edges: %v", err)
	}

	if len(edges) != 1 {
		t.Errorf("Expected 1 edge, got %d", len(edges))
	}
}
