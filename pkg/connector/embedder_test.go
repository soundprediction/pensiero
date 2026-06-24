package connector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOpenAIEmbedderBatchingAndCache(t *testing.T) {
	var mu sync.Mutex
	var requests [][]string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("path=%s, want /v1/embeddings", r.URL.Path)
		}
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "test-model" {
			t.Fatalf("model=%s, want test-model", req.Model)
		}
		mu.Lock()
		requests = append(requests, append([]string{}, req.Input...))
		mu.Unlock()
		resp := embeddingResponse{Data: make([]embeddingData, len(req.Input))}
		for i, text := range req.Input {
			resp.Data[i] = embeddingData{
				Index:     i,
				Embedding: []float32{float32(len(text)), float32(i + 1)},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	embedder := NewOpenAIEmbedder(EmbedderConfig{
		BaseURL:   "http://embedder.test",
		Model:     "test-model",
		BatchSize: 2,
		HTTPClient: &http.Client{
			Transport: handlerRoundTripper{handler: handler},
		},
	})
	first, err := embedder.Embed(context.Background(), []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(first) != 3 || first[0][0] != 5 || first[1][0] != 4 || first[2][0] != 5 {
		t.Fatalf("unexpected embeddings: %#v", first)
	}
	mu.Lock()
	gotRequests := len(requests)
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("requests=%d, want 2 batches", gotRequests)
	}

	first[0][0] = 999
	second, err := embedder.Embed(context.Background(), []string{"alpha", "gamma"})
	if err != nil {
		t.Fatalf("cached Embed returned error: %v", err)
	}
	if second[0][0] != 5 || second[1][0] != 5 {
		t.Fatalf("cache returned mutated embeddings: %#v", second)
	}
	mu.Lock()
	gotRequests = len(requests)
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("requests=%d after cache hits, want 2", gotRequests)
	}
}

func TestOpenAIEmbedderFailureAndDisabled(t *testing.T) {
	disabled := NewOpenAIEmbedder(EmbedderConfig{})
	if _, err := disabled.Embed(context.Background(), []string{"alpha"}); !errors.Is(err, ErrEmbedderDisabled) {
		t.Fatalf("disabled Embed error=%v, want ErrEmbedderDisabled", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	embedder := NewOpenAIEmbedder(EmbedderConfig{
		BaseURL: "http://embedder.test",
		HTTPClient: &http.Client{
			Transport: handlerRoundTripper{handler: handler},
		},
	})
	if _, err := embedder.Embed(context.Background(), []string{"alpha"}); err == nil {
		t.Fatal("Embed returned nil error for HTTP failure")
	}
}

type handlerRoundTripper struct {
	handler http.Handler
}

func (t handlerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	if resp.Body == nil {
		resp.Body = io.NopCloser(strings.NewReader(""))
	}
	return resp, nil
}
