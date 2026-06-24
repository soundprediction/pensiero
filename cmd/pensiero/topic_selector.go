package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const (
	defaultTopicHotKeyLimit       = 16
	defaultTopicRandomSampleLimit = 8
	defaultTopicSemanticSample    = 16
)

type cognitionEmbedder interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type TopicSelectorConfig struct {
	QueryHotWeight    int
	RandomWeight      int
	UnresolvedWeight  int
	SemanticWeight    int
	HotKeyLimit       int
	RandomSampleLimit int
	SemanticSample    int
	Random            *rand.Rand
}

// TODO: keep cognition topic selection on fixed weights; multi-armed bandits are deferred.
type TopicSelector struct {
	mu           sync.Mutex
	rand         *rand.Rand
	sources      []weightedThoughtSource
	sinceRandom  int
	randomSource int
}

type weightedThoughtSource struct {
	Weight int
	Source thoughtSource
}

type thoughtSource interface {
	Name() string
	Next(context.Context) (Thought, bool, error)
}

func NewTopicSelector(store *generationStore, telemetry *queryTelemetry, reg *reasoning.PredicateRegistry, embedder cognitionEmbedder, cfg TopicSelectorConfig) *TopicSelector {
	cfg = normalizeTopicSelectorConfig(cfg)
	randomSource := newRandomThoughtSource(store, reg, cfg.RandomSampleLimit, cfg.Random)
	sources := []weightedThoughtSource{
		{Weight: cfg.QueryHotWeight, Source: &queryHotThoughtSource{telemetry: telemetry, limit: cfg.HotKeyLimit}},
		{Weight: cfg.UnresolvedWeight, Source: &unresolvedThoughtSource{telemetry: telemetry, limit: cfg.HotKeyLimit}},
		{Weight: cfg.RandomWeight, Source: randomSource},
	}
	if embedder != nil && cfg.SemanticWeight > 0 {
		sources = append(sources, weightedThoughtSource{
			Weight: cfg.SemanticWeight,
			Source: &semanticThoughtSource{
				store:       store,
				telemetry:   telemetry,
				reg:         reg,
				embedder:    embedder,
				hotLimit:    cfg.HotKeyLimit,
				sampleLimit: cfg.SemanticSample,
				random:      cfg.Random,
			},
		})
	}
	return newTopicSelectorFromSources(sources, cfg.Random)
}

func newTopicSelectorFromSources(sources []weightedThoughtSource, rnd *rand.Rand) *TopicSelector {
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	kept := make([]weightedThoughtSource, 0, len(sources))
	for _, source := range sources {
		if source.Source == nil || source.Weight <= 0 {
			continue
		}
		kept = append(kept, source)
	}
	randomSource := -1
	for i, source := range kept {
		if source.Source.Name() == "random" {
			randomSource = i
			break
		}
	}
	return &TopicSelector{rand: rnd, sources: kept, randomSource: randomSource}
}

func normalizeTopicSelectorConfig(cfg TopicSelectorConfig) TopicSelectorConfig {
	if cfg.QueryHotWeight < 0 {
		cfg.QueryHotWeight = 0
	}
	if cfg.UnresolvedWeight < 0 {
		cfg.UnresolvedWeight = 0
	}
	if cfg.SemanticWeight < 0 {
		cfg.SemanticWeight = 0
	}
	if cfg.RandomWeight <= 0 {
		cfg.RandomWeight = 1
	}
	if cfg.HotKeyLimit <= 0 {
		cfg.HotKeyLimit = defaultTopicHotKeyLimit
	}
	if cfg.RandomSampleLimit <= 0 {
		cfg.RandomSampleLimit = defaultTopicRandomSampleLimit
	}
	if cfg.SemanticSample <= 0 {
		cfg.SemanticSample = defaultTopicSemanticSample
	}
	if cfg.Random == nil {
		cfg.Random = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return cfg
}

func (s *TopicSelector) Next(ctx context.Context) (Thought, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return Thought{}, false, ctx.Err()
	}
	order := s.pickOrder()
	if len(order) == 0 {
		return Thought{}, false, ctx.Err()
	}
	var firstErr error
	for _, idx := range order {
		if err := ctx.Err(); err != nil {
			return Thought{}, false, err
		}
		source := s.sources[idx].Source
		thought, ok, err := source.Next(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			if thought.Source == "" {
				thought.Source = source.Name()
			}
			return thought, true, nil
		}
	}
	return Thought{}, false, firstErr
}

func (s *TopicSelector) pickOrder() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, source := range s.sources {
		total += source.Weight
	}
	if total <= 0 {
		return nil
	}
	target := s.rand.Intn(total)
	selected := 0
	seen := 0
	for i, source := range s.sources {
		seen += source.Weight
		if target < seen {
			selected = i
			break
		}
	}
	if s.randomSource >= 0 && s.sinceRandom >= total-1 {
		selected = s.randomSource
	}
	if selected == s.randomSource {
		s.sinceRandom = 0
	} else {
		s.sinceRandom++
	}
	order := []int{selected}
	for i := range s.sources {
		if i != selected {
			order = append(order, i)
		}
	}
	return order
}

func (s *TopicSelector) SourceWeights() map[string]int {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	weights := map[string]int{}
	for _, source := range s.sources {
		name := source.Source.Name()
		if name == "" {
			continue
		}
		weights[name] += source.Weight
	}
	return weights
}

type queryHotThoughtSource struct {
	mu        sync.Mutex
	telemetry *queryTelemetry
	limit     int
	next      int
}

func (s *queryHotThoughtSource) Name() string { return "query-hot" }

func (s *queryHotThoughtSource) Next(ctx context.Context) (Thought, bool, error) {
	if err := ctx.Err(); err != nil {
		return Thought{}, false, err
	}
	if s == nil || s.telemetry == nil {
		return Thought{}, false, nil
	}
	keys := s.telemetry.HotKeys(s.limit)
	if len(keys) == 0 {
		return Thought{}, false, nil
	}
	start := s.advance(len(keys))
	for i := 0; i < len(keys); i++ {
		key := keys[(start+i)%len(keys)]
		claim := claimFromHotKey(key)
		if claimDedupeKey(claim) == "" {
			continue
		}
		return Thought{
			Type:      ThoughtProofPrecompute,
			Claim:     claim,
			Source:    s.Name(),
			Rationale: "prewarm proof cache for hot query",
			HotKey:    &key,
		}, true, nil
	}
	return Thought{}, false, nil
}

func (s *queryHotThoughtSource) advance(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		return 0
	}
	start := s.next % n
	s.next++
	return start
}

type unresolvedThoughtSource struct {
	mu        sync.Mutex
	telemetry *queryTelemetry
	limit     int
	next      int
}

func (s *unresolvedThoughtSource) Name() string { return "unresolved" }

func (s *unresolvedThoughtSource) Next(ctx context.Context) (Thought, bool, error) {
	if err := ctx.Err(); err != nil {
		return Thought{}, false, err
	}
	if s == nil || s.telemetry == nil {
		return Thought{}, false, nil
	}
	claims := s.telemetry.UnresolvedClaims(s.limit)
	if len(claims) == 0 {
		return Thought{}, false, nil
	}
	start := s.advance(len(claims))
	for i := 0; i < len(claims); i++ {
		item := claims[(start+i)%len(claims)]
		if claimDedupeKey(item.Claim) == "" {
			continue
		}
		return Thought{
			Type:      ThoughtContradictionHunt,
			Claim:     item.Claim,
			Source:    s.Name(),
			Rationale: "check unresolved or contradicted hot claim",
			Meta: map[string]any{
				"count":   item.Count,
				"verdict": item.Verdict,
			},
		}, true, nil
	}
	return Thought{}, false, nil
}

func (s *unresolvedThoughtSource) advance(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		return 0
	}
	start := s.next % n
	s.next++
	return start
}

type randomThoughtSource struct {
	store       *generationStore
	reg         *reasoning.PredicateRegistry
	sampleLimit int
	mu          sync.Mutex
	random      *rand.Rand
}

func newRandomThoughtSource(store *generationStore, reg *reasoning.PredicateRegistry, sampleLimit int, rnd *rand.Rand) *randomThoughtSource {
	if sampleLimit <= 0 {
		sampleLimit = defaultTopicRandomSampleLimit
	}
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &randomThoughtSource{
		store:       store,
		reg:         reg,
		sampleLimit: sampleLimit,
		random:      rnd,
	}
}

func (s *randomThoughtSource) Name() string { return "random" }

func (s *randomThoughtSource) Next(ctx context.Context) (Thought, bool, error) {
	if err := ctx.Err(); err != nil {
		return Thought{}, false, err
	}
	if s == nil || s.store == nil {
		return Thought{}, false, nil
	}
	s.mu.Lock()
	skip := s.random.Intn(randomSampleSkipMax + 1)
	s.mu.Unlock()
	names, err := sampleEntityNames(ctx, s.store, s.sampleLimit, skip)
	if err != nil || len(names) < 2 {
		return Thought{}, false, err
	}
	subject, object, ok := s.pickPair(names)
	if !ok {
		return Thought{}, false, nil
	}
	predicate, ok := s.pickPredicate()
	if !ok {
		return Thought{}, false, nil
	}
	return Thought{
		Type:      ThoughtHypothesisTest,
		Claim:     reasoning.Claim{Subject: subject, Predicate: predicate, Object: object},
		Source:    s.Name(),
		Rationale: "random bounded graph sample",
	}, true, nil
}

func (s *randomThoughtSource) pickPair(names []string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(names) < 2 {
		return "", "", false
	}
	first := s.random.Intn(len(names))
	second := s.random.Intn(len(names) - 1)
	if second >= first {
		second++
	}
	return names[first], names[second], true
}

func (s *randomThoughtSource) pickPredicate() (string, bool) {
	if s == nil || s.reg == nil {
		return "", false
	}
	preds := s.reg.AllCanonical()
	if len(preds) == 0 {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return preds[s.random.Intn(len(preds))], true
}

type semanticThoughtSource struct {
	store       *generationStore
	telemetry   *queryTelemetry
	reg         *reasoning.PredicateRegistry
	embedder    cognitionEmbedder
	hotLimit    int
	sampleLimit int
	mu          sync.Mutex
	next        int
	random      *rand.Rand
}

func (s *semanticThoughtSource) Name() string { return "semantic" }

// TODO: embedding-based type-models and analogy-driven edge prediction are deferred.
// This source only compares hot query text with a bounded read-only entity sample.
func (s *semanticThoughtSource) Next(ctx context.Context) (Thought, bool, error) {
	if err := ctx.Err(); err != nil {
		return Thought{}, false, err
	}
	if s == nil || s.embedder == nil || s.telemetry == nil || s.store == nil {
		return Thought{}, false, nil
	}
	keys := s.telemetry.HotKeys(s.hotLimit)
	if len(keys) == 0 {
		return Thought{}, false, nil
	}
	start := s.advance(len(keys))
	for i := 0; i < len(keys); i++ {
		key := keys[(start+i)%len(keys)]
		hotText := strings.TrimSpace(firstNonEmpty(key.rawSubject, key.rawObject))
		if hotText == "" {
			continue
		}
		s.mu.Lock()
		skip := 0
		if s.random != nil {
			skip = s.random.Intn(randomSampleSkipMax + 1)
		}
		s.mu.Unlock()
		names, err := sampleEntityNames(ctx, s.store, s.sampleLimit, skip)
		if err != nil || len(names) == 0 {
			return Thought{}, false, err
		}
		neighbor, ok := s.nearestNeighbor(ctx, hotText, names)
		if !ok {
			continue
		}
		predicate := strings.TrimSpace(key.Predicate)
		if predicate == "" {
			var ok bool
			predicate, ok = randomPredicate(s.reg, s.random, &s.mu)
			if !ok {
				continue
			}
		}
		claim := reasoning.Claim{
			Subject:   hotText,
			Predicate: predicate,
			Object:    neighbor,
		}
		if claimDedupeKey(claim) == "" {
			continue
		}
		return Thought{
			Type:      ThoughtHypothesisTest,
			Claim:     claim,
			Source:    s.Name(),
			Rationale: "semantic neighbor of hot topic",
			HotKey:    &key,
		}, true, nil
	}
	return Thought{}, false, nil
}

func (s *semanticThoughtSource) advance(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		return 0
	}
	start := s.next % n
	s.next++
	return start
}

func (s *semanticThoughtSource) nearestNeighbor(ctx context.Context, hotText string, names []string) (string, bool) {
	texts := make([]string, 0, len(names)+1)
	texts = append(texts, hotText)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" && !strings.EqualFold(name, hotText) {
			texts = append(texts, name)
		}
	}
	if len(texts) < 2 {
		return "", false
	}
	embeddings, err := s.embedder.Embed(ctx, texts)
	if err != nil || len(embeddings) != len(texts) {
		return "", false
	}
	bestIndex := -1
	bestScore := math.Inf(-1)
	for i := 1; i < len(embeddings); i++ {
		score := cosineFloat32(embeddings[0], embeddings[i])
		if score > bestScore {
			bestScore = score
			bestIndex = i
		}
	}
	if bestIndex <= 0 {
		return "", false
	}
	return texts[bestIndex], true
}

func sampleEntityNames(ctx context.Context, store *generationStore, limit, skip int) ([]string, error) {
	if store == nil {
		return nil, nil
	}
	if limit < 2 {
		limit = 2
	}
	gen, release := store.Acquire()
	if gen == nil || gen.pool == nil {
		release()
		return nil, errNoGeneration
	}
	defer release()
	rows, err := gen.pool.Query(ctx, randomEntitySampleQuery(skip, limit), nil)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 && skip > 0 {
		// Random offset overshot a smaller graph; fall back to the head so the
		// random floor still yields a topic.
		rows, err = gen.pool.Query(ctx, randomEntitySampleQuery(0, limit), nil)
		if err != nil {
			return nil, err
		}
	}
	seen := map[string]struct{}{}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		name := rowString(row, "name")
		if name == "" {
			name = rowString(row, "n.name")
		}
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, strings.TrimSpace(name))
	}
	sort.Strings(names)
	return names, nil
}

// randomSampleSkipMax bounds the Go-computed random offset for entity sampling.
// ladybug has no rand() Cypher function, so randomness comes from a random SKIP
// offset (with a fallback to 0 when it overshoots a smaller graph).
const randomSampleSkipMax = 20000

func randomEntitySampleQuery(skip, limit int) string {
	if limit < 2 {
		limit = 2
	}
	if skip < 0 {
		skip = 0
	}
	// SKIP/LIMIT without ORDER BY: ladybug-compatible (no rand()); the offset is
	// chosen randomly in Go so successive samples cover different regions.
	return fmt.Sprintf("MATCH (n:Entity) WHERE n.name IS NOT NULL RETURN n.name AS name SKIP %d LIMIT %d", skip, limit)
}

func claimFromHotKey(key QueryHotKey) reasoning.Claim {
	return reasoning.Claim{
		Subject:   strings.TrimSpace(key.rawSubject),
		Predicate: strings.TrimSpace(key.Predicate),
		Object:    strings.TrimSpace(key.rawObject),
	}
}

func rowString(row map[string]any, key string) string {
	if row == nil {
		return ""
	}
	value, ok := row[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func randomPredicate(reg *reasoning.PredicateRegistry, rnd *rand.Rand, mu *sync.Mutex) (string, bool) {
	if reg == nil {
		return "", false
	}
	preds := reg.AllCanonical()
	if len(preds) == 0 {
		return "", false
	}
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	return preds[rnd.Intn(len(preds))], true
}

func cosineFloat32(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return math.Inf(-1)
	}
	var dot, aa, bb float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		aa += av * av
		bb += bv * bv
	}
	if aa == 0 || bb == 0 {
		return math.Inf(-1)
	}
	return dot / (math.Sqrt(aa) * math.Sqrt(bb))
}
