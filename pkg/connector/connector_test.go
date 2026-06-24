package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/soundprediction/pensiero/pkg/models"
)

type mockCozo struct {
	runFunc func(query string, params map[string]interface{}) (interface{}, error)
}

func (m *mockCozo) Run(query string, params map[string]interface{}) (interface{}, error) {
	return m.runFunc(query, params)
}

func (m *mockCozo) Close() {}

func TestIngestFromPredicato(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})

	repo := &fakePredicatoRepository{}
	connector := NewPredicatoClient("http://predicato.test")
	connector.HTTPClient = &http.Client{
		Transport: handlerRoundTripper{handler: handler},
	}

	if err := connector.IngestFromPredicato(context.Background(), repo, "John is a friend of Jane. Humans are mortal."); err != nil {
		t.Fatalf("IngestFromPredicato failed: %v", err)
	}

	if len(repo.edges) != 1 {
		t.Fatalf("edges=%d, want 1", len(repo.edges))
	}
	if repo.edges[0].Source != "John" || repo.edges[0].Target != "Jane" || repo.edges[0].Predicate != "friend_of" {
		t.Errorf("edge mismatch: %+v", repo.edges[0])
	}
	if len(repo.meta) != 1 || repo.meta[0].Head != "mortal(X)" {
		t.Fatalf("meta relations=%#v, want mortal rule", repo.meta)
	}
}

type fakePredicatoRepository struct {
	edges []*models.EpistemicEdge
	meta  []*models.MetaRelation
}

func (r *fakePredicatoRepository) SaveEdge(edge *models.EpistemicEdge) error {
	r.edges = append(r.edges, edge)
	return nil
}

func (r *fakePredicatoRepository) SaveMetaRelation(meta *models.MetaRelation) error {
	r.meta = append(r.meta, meta)
	return nil
}
