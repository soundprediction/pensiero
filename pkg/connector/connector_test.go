package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/soundprediction/pensiero/pkg/db"
)

type mockCozo struct {
	runFunc func(query string, params map[string]interface{}) (interface{}, error)
}

func (m *mockCozo) Run(query string, params map[string]interface{}) (interface{}, error) {
	return m.runFunc(query, params)
}

func (m *mockCozo) Close() {}

func TestIngestFromPredicato(t *testing.T) {
	// Mock Predicato server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/extract" {
			t.Errorf("Expected path /api/v1/extract, got %s", r.URL.Path)
		}

		result := ExtendedExtractionResult{
			Triples: []ExtendedTriple{
				{
					Subject:    "John",
					Predicate:  "friend_of",
					Object:     "Jane",
					Confidence: 0.9,
				},
			},
			Rules: []Rule{
				{
					Antecedent: "human(X)",
					Consequent: "mortal(X)",
					Confidence: 1.0,
				},
			},
		}
		json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	// Mock CozoDB and Repository - this is a bit involved due to the embedded CGO dependency.
	// For this test, we'll try to use a real In-Memory CozoDB if possible,
	// or we will just test the mapping logic if we can decouple it.

	// Since pensiero has cozo-lib-go dependency, let's try to use it with 'mem' engine if it works in this env.
	client, err := db.NewClient("mem", "", nil)
	if err != nil {
		t.Skip("Skipping test: CozoDB 'mem' engine not available in test environment", err)
		return
	}
	defer client.Close()

	err = client.InitSchema()
	if err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	repo := db.NewRepository(client)
	connector := NewPredicatoClient(server.URL)

	err = connector.IngestFromPredicato(context.Background(), repo, "John is a friend of Jane. Humans are mortal.")
	if err != nil {
		t.Fatalf("IngestFromPredicato failed: %v", err)
	}

	// Verify triples
	// Since ID is random-ish (timestamp), let's list all edges
	edges, err := repo.ListEdgesBySource("John")
	if err != nil || len(edges) == 0 {
		t.Fatalf("Expected to find edge for John, got %v", err)
	}
	if edges[0].Target != "Jane" || edges[0].Predicate != "friend_of" {
		t.Errorf("Edge mismatch: %+v", edges[0])
	}
}
