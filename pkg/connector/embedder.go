package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	DefaultEmbeddingModel        = "text-embedding-3-small"
	DefaultEmbeddingBatchSize    = 64
	DefaultEmbeddingCacheEntries = 8192
	defaultEmbeddingHTTPTimeout  = 15 * time.Second
)

var ErrEmbedderDisabled = errors.New("embedder disabled")

type EmbedderConfig struct {
	BaseURL      string
	Model        string
	BatchSize    int
	MinInterval  time.Duration
	CacheEntries int
	HTTPClient   *http.Client
}

// OpenAIEmbedder calls an OpenAI-compatible /v1/embeddings endpoint. It keeps a
// process-local cache keyed by model+text so repeated cognition passes can reuse
// embeddings without changing the served graph.
type OpenAIEmbedder struct {
	baseURL      string
	model        string
	batchSize    int
	minInterval  time.Duration
	cacheEntries int
	httpClient   *http.Client

	cacheMu sync.Mutex
	cache   map[string][]float32
	order   []string

	rateMu sync.Mutex
	nextAt time.Time
}

func NewOpenAIEmbedder(cfg EmbedderConfig) *OpenAIEmbedder {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultEmbeddingModel
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultEmbeddingBatchSize
	}
	cacheEntries := cfg.CacheEntries
	if cacheEntries <= 0 {
		cacheEntries = DefaultEmbeddingCacheEntries
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultEmbeddingHTTPTimeout}
	}
	return &OpenAIEmbedder{
		baseURL:      baseURL,
		model:        model,
		batchSize:    batchSize,
		minInterval:  cfg.MinInterval,
		cacheEntries: cacheEntries,
		httpClient:   client,
		cache:        map[string][]float32{},
	}
}

func (e *OpenAIEmbedder) Enabled() bool {
	return e != nil && strings.TrimSpace(e.baseURL) != ""
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !e.Enabled() {
		return nil, ErrEmbedderDisabled
	}
	out := make([][]float32, len(texts))
	type miss struct {
		key       string
		text      string
		positions []int
	}
	missByKey := map[string]int{}
	var misses []miss
	for i, text := range texts {
		key := e.cacheKey(text)
		if embedding, ok := e.getCached(key); ok {
			out[i] = embedding
			continue
		}
		if idx, ok := missByKey[key]; ok {
			misses[idx].positions = append(misses[idx].positions, i)
			continue
		}
		missByKey[key] = len(misses)
		misses = append(misses, miss{key: key, text: text, positions: []int{i}})
	}
	for start := 0; start < len(misses); start += e.batchSize {
		end := start + e.batchSize
		if end > len(misses) {
			end = len(misses)
		}
		batch := misses[start:end]
		inputs := make([]string, len(batch))
		for i := range batch {
			inputs[i] = batch[i].text
		}
		embeddings, err := e.requestBatch(ctx, inputs)
		if err != nil {
			return nil, err
		}
		for i, embedding := range embeddings {
			item := batch[i]
			e.putCached(item.key, embedding)
			for _, pos := range item.positions {
				out[pos] = cloneEmbedding(embedding)
			}
		}
	}
	return out, nil
}

func (e *OpenAIEmbedder) requestBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	if err := e.waitRateLimit(ctx); err != nil {
		return nil, err
	}
	body, err := json.Marshal(embeddingRequest{
		Model: e.model,
		Input: inputs,
	})
	if err != nil {
		return nil, fmt.Errorf("embedder marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedder create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedder request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("embedder status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("embedder decode response: %w", err)
	}
	if len(decoded.Data) != len(inputs) {
		return nil, fmt.Errorf("embedder returned %d embeddings for %d inputs", len(decoded.Data), len(inputs))
	}
	out := make([][]float32, len(inputs))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(inputs) {
			return nil, fmt.Errorf("embedder returned out-of-range index %d", item.Index)
		}
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("embedder returned empty embedding at index %d", item.Index)
		}
		out[item.Index] = cloneEmbedding(item.Embedding)
	}
	for i := range out {
		if len(out[i]) == 0 {
			return nil, fmt.Errorf("embedder response missing index %d", i)
		}
	}
	return out, nil
}

func (e *OpenAIEmbedder) endpoint() string {
	base := strings.TrimRight(strings.TrimSpace(e.baseURL), "/")
	if strings.HasSuffix(base, "/v1/embeddings") {
		return base
	}
	return base + "/v1/embeddings"
}

func (e *OpenAIEmbedder) cacheKey(text string) string {
	return e.model + "\x00" + text
}

func (e *OpenAIEmbedder) getCached(key string) ([]float32, bool) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	value, ok := e.cache[key]
	if !ok {
		return nil, false
	}
	return cloneEmbedding(value), true
}

func (e *OpenAIEmbedder) putCached(key string, embedding []float32) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	if _, ok := e.cache[key]; !ok {
		e.order = append(e.order, key)
	}
	e.cache[key] = cloneEmbedding(embedding)
	for len(e.order) > e.cacheEntries {
		oldest := e.order[0]
		copy(e.order, e.order[1:])
		e.order = e.order[:len(e.order)-1]
		delete(e.cache, oldest)
	}
}

func (e *OpenAIEmbedder) waitRateLimit(ctx context.Context) error {
	if e.minInterval <= 0 {
		return ctx.Err()
	}
	e.rateMu.Lock()
	now := time.Now()
	wait := e.nextAt.Sub(now)
	if wait <= 0 {
		e.nextAt = now.Add(e.minInterval)
		e.rateMu.Unlock()
		return ctx.Err()
	}
	e.nextAt = e.nextAt.Add(e.minInterval)
	e.rateMu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type embeddingRequest struct {
	Model string   `json:"model,omitempty"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []embeddingData `json:"data"`
}

type embeddingData struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

func cloneEmbedding(in []float32) []float32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float32, len(in))
	copy(out, in)
	return out
}
