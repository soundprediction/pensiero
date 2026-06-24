package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
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
	BridgeWeight      int
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

func NewTopicSelector(store generationAcquirer, telemetry *queryTelemetry, reg *reasoning.PredicateRegistry, embedder cognitionEmbedder, cfg TopicSelectorConfig) *TopicSelector {
	cfg = normalizeTopicSelectorConfig(cfg)
	randomSource := newRandomThoughtSource(store, cfg.RandomSampleLimit, cfg.Random)
	sources := []weightedThoughtSource{
		{Weight: cfg.QueryHotWeight, Source: &queryHotThoughtSource{telemetry: telemetry, limit: cfg.HotKeyLimit}},
		{Weight: cfg.UnresolvedWeight, Source: &unresolvedThoughtSource{telemetry: telemetry, limit: cfg.HotKeyLimit}},
		{Weight: cfg.RandomWeight, Source: randomSource},
		{Weight: cfg.BridgeWeight, Source: newBridgeThoughtSource(store, cfg.RandomSampleLimit, cfg.Random)},
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
	if cfg.BridgeWeight < 0 {
		cfg.BridgeWeight = 0
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
	store       generationAcquirer
	sampleLimit int
	mu          sync.Mutex
	random      *rand.Rand
}

func newRandomThoughtSource(store generationAcquirer, sampleLimit int, rnd *rand.Rand) *randomThoughtSource {
	if sampleLimit <= 0 {
		sampleLimit = defaultTopicRandomSampleLimit
	}
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &randomThoughtSource{
		store:       store,
		sampleLimit: sampleLimit,
		random:      rnd,
	}
}

func (s *randomThoughtSource) Name() string { return "random" }

// Next samples a plausible *missing-link* candidate instead of a globally
// random (entity, predicate, entity) triple. Pairing two arbitrary entities
// with a registry predicate that the graph may not even use produces nonsense
// questions ("disease is_a chemical") that are always unsupported. Instead it
// seeds on an entity that has edges, then proposes a claim to an entity two
// hops away (related, but not already directly connected) using a predicate the
// seed actually participates in. The expected gain reflects the seed's
// connectivity, so questions about central, well-connected entities rank above
// peripheral ones.
func (s *randomThoughtSource) Next(ctx context.Context) (Thought, bool, error) {
	if err := ctx.Err(); err != nil {
		return Thought{}, false, err
	}
	if s == nil || s.store == nil {
		return Thought{}, false, nil
	}
	seeds, err := s.sampleSeeds(ctx)
	if err != nil || len(seeds) == 0 {
		return Thought{}, false, err
	}
	for _, seed := range seeds {
		thought, ok, err := s.candidateForSeed(ctx, seed)
		if err != nil {
			return Thought{}, false, err
		}
		if ok {
			return thought, true, nil
		}
	}
	return Thought{}, false, nil
}

// sampleSeeds biases toward well-connected entities so questions concern central
// topics (e.g. conditions) rather than peripheral leaf nodes (e.g. drug IDs),
// which dominate the graph by count. Falls back to a uniform sample if the
// degree-ranked query yields nothing.
func (s *randomThoughtSource) sampleSeeds(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	degSkip := s.random.Intn(seedDegreeSkipMax + 1)
	uniSkip := s.random.Intn(randomSampleSkipMax + 1)
	s.mu.Unlock()
	seeds, err := sampleSeedsByDegree(ctx, s.store, s.sampleLimit, degSkip)
	if err != nil {
		return nil, err
	}
	if len(seeds) == 0 {
		if seeds, err = sampleEntityNames(ctx, s.store, s.sampleLimit, uniSkip); err != nil {
			return nil, err
		}
	}
	s.shuffleStrings(seeds)
	return seeds, nil
}

// candidateForSeed proposes a missing-link claim from a seed entity, or ok=false
// when the seed is isolated or has no unconnected two-hop neighbor.
func (s *randomThoughtSource) candidateForSeed(ctx context.Context, seed string) (Thought, bool, error) {
	gen, release := s.store.Acquire()
	if gen == nil || gen.pool == nil {
		release()
		return Thought{}, false, errNoGeneration
	}
	defer release()

	limit := s.sampleLimit * 4
	if limit < 8 {
		limit = 8
	}
	params := map[string]any{"seed": seed}
	oneHop, err := gen.pool.Query(ctx, seedOneHopQuery(limit), params)
	if err != nil {
		return Thought{}, false, err
	}
	predicates := make([]string, 0, len(oneHop))
	directNeighbors := map[string]struct{}{lowerKey(seed): {}}
	seenPred := map[string]struct{}{}
	for _, row := range oneHop {
		if n := lowerKey(rowString(row, "neighbor")); n != "" {
			directNeighbors[n] = struct{}{}
		}
		pred := normalizePredicateLabel(rowString(row, "predicate"))
		if pred == "" {
			continue
		}
		if _, ok := seenPred[pred]; ok {
			continue
		}
		seenPred[pred] = struct{}{}
		predicates = append(predicates, pred)
	}
	degree := len(directNeighbors) - 1 // exclude the seed itself
	if len(predicates) == 0 {
		return Thought{}, false, nil // isolated seed; no edges to generalize from
	}

	twoHop, err := gen.pool.Query(ctx, seedTwoHopQuery(limit), params)
	if err != nil {
		return Thought{}, false, err
	}
	candidates := make([]string, 0, len(twoHop))
	seenCand := map[string]struct{}{}
	for _, row := range twoHop {
		name := strings.TrimSpace(rowString(row, "twohop"))
		key := lowerKey(name)
		if key == "" {
			continue
		}
		if _, blocked := directNeighbors[key]; blocked {
			continue // already directly connected (or the seed) -> not a missing link
		}
		if _, dup := seenCand[key]; dup {
			continue
		}
		seenCand[key] = struct{}{}
		candidates = append(candidates, name)
	}
	if len(candidates) == 0 {
		return Thought{}, false, nil
	}

	s.mu.Lock()
	predicate := predicates[s.random.Intn(len(predicates))]
	object := candidates[s.random.Intn(len(candidates))]
	s.mu.Unlock()

	return Thought{
		Type:      ThoughtHypothesisTest,
		Claim:     reasoning.Claim{Subject: seed, Predicate: predicate, Object: object},
		Source:    s.Name(),
		Rationale: "neighborhood missing-link candidate",
		Meta:      map[string]any{"expected_gain": neighborhoodGain(degree)},
	}, true, nil
}

func (s *randomThoughtSource) shuffleStrings(values []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.random.Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
}

const (
	defaultBridgeHubLimit = 150
	bridgeHubTTL          = 10 * time.Minute
	// minSharedForFactor is the minimum shared-neighbour count for a pair to be
	// worth a factorization question: below this there is little dense structure
	// to absorb into a generalization.
	minSharedForFactor = 5
)

// bridgeThoughtSource ponders connections between two nodes that are
// semantically close (embedding-near, via the stored name_embedding) but not
// directly connected in the graph -- candidate long-distance / missing links,
// which are the highest-value gaps to resolve. It seeds on well-connected "hub"
// entities (cached, degree-ranked) so the pondered pairs are central, and uses
// a similarity *band* so it proposes related-but-distinct pairs rather than
// synonyms. Uses the embedding model's stored output; no external embedder call.
type bridgeThoughtSource struct {
	store       generationAcquirer
	random      *rand.Rand
	now         func() time.Time
	hubs        []string
	hubsAt      time.Time
	sampleLimit int
	hubLimit    int
	mu          sync.Mutex
}

func newBridgeThoughtSource(store generationAcquirer, sampleLimit int, rnd *rand.Rand) *bridgeThoughtSource {
	if sampleLimit <= 0 {
		sampleLimit = defaultTopicRandomSampleLimit
	}
	if rnd == nil {
		rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &bridgeThoughtSource{
		store:       store,
		random:      rnd,
		now:         time.Now,
		sampleLimit: sampleLimit,
		hubLimit:    defaultBridgeHubLimit,
	}
}

func (s *bridgeThoughtSource) Name() string { return "bridge" }

// ensureHubs returns the cached degree-ranked hub names, refreshing when stale.
// On error it keeps the last good set so transient failures don't stall.
func (s *bridgeThoughtSource) ensureHubs(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	fresh := len(s.hubs) > 0 && s.now().Sub(s.hubsAt) < bridgeHubTTL
	cached := s.hubs
	s.mu.Unlock()
	if fresh {
		return cached, nil
	}
	names, err := sampleSeedsByDegree(ctx, s.store, s.hubLimit, 0)
	if err != nil {
		return cached, err
	}
	if len(names) == 0 {
		return cached, nil
	}
	s.mu.Lock()
	s.hubs = names
	s.hubsAt = s.now()
	s.mu.Unlock()
	return names, nil
}

func (s *bridgeThoughtSource) Next(ctx context.Context) (Thought, bool, error) {
	if err := ctx.Err(); err != nil {
		return Thought{}, false, err
	}
	if s == nil || s.store == nil {
		return Thought{}, false, nil
	}
	hubs, err := s.ensureHubs(ctx)
	if err != nil {
		return Thought{}, false, err
	}
	if len(hubs) < 3 {
		return Thought{}, false, nil
	}
	s.mu.Lock()
	seed := hubs[s.random.Intn(len(hubs))]
	s.mu.Unlock()
	return s.bridgeFromSeed(ctx, seed)
}

func (s *bridgeThoughtSource) bridgeFromSeed(ctx context.Context, seed string) (Thought, bool, error) {
	gen, release := s.store.Acquire()
	if gen == nil || gen.pool == nil {
		release()
		return Thought{}, false, errNoGeneration
	}
	defer release()

	limit := s.sampleLimit * 2
	if limit < 8 {
		limit = 8
	}
	// The entity sharing the MOST neighbours with the seed is the strongest
	// factorization candidate: their many shared edges can be re-routed through
	// one shared generalization (an N x M block collapses to N + M), which forms
	// a local neighbourhood and lowers the graph's global density.
	rows, err := gen.pool.Query(ctx, sharedNeighborQuery(limit), map[string]any{"seed": seed})
	if err != nil {
		return Thought{}, false, err
	}
	var object string
	var shared int64
	for _, row := range rows { // ordered by shared count descending
		name := strings.TrimSpace(rowString(row, "name"))
		if name == "" || lowerKey(name) == lowerKey(seed) {
			continue
		}
		if sh := int64(rowFloat(row, "shared")); sh >= minSharedForFactor {
			object, shared = name, sh
			break
		}
	}
	if object == "" {
		return Thought{}, false, nil
	}
	return Thought{
		Type:      ThoughtGeneralizationBridge,
		Claim:     reasoning.Claim{Subject: seed, Predicate: "shares_generalization_with", Object: object},
		Source:    s.Name(),
		Rationale: fmt.Sprintf("densely co-connected (%d shared neighbours); factoring through a shared generalization would reduce global density", shared),
		Meta:      map[string]any{"expected_gain": factorGain(shared), "shared_neighbours": shared},
	}, true, nil
}

type semanticThoughtSource struct {
	store       generationAcquirer
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

func sampleEntityNames(ctx context.Context, store generationAcquirer, limit, skip int) ([]string, error) {
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

// seedDegreeSkipMax bounds the random offset into the degree-ranked entity list,
// keeping seeds among the most-connected entities while still varying which one.
const seedDegreeSkipMax = 128

// seedByDegreeQuery ranks entities by out-degree (edge count) so seed sampling
// favours central entities. ladybug has no rand(); a random SKIP within the top
// band varies the pick.
func seedByDegreeQuery(skip, limit int) string {
	if limit < 1 {
		limit = 1
	}
	if skip < 0 {
		skip = 0
	}
	return fmt.Sprintf("MATCH (a:Entity)-[:RELATES_TO]->(r:RelatesToNode_) RETURN a.name AS name, count(r) AS deg ORDER BY deg DESC SKIP %d LIMIT %d", skip, limit)
}

// sampleSeedsByDegree returns distinct entity names from the degree-ranked band.
func sampleSeedsByDegree(ctx context.Context, store generationAcquirer, limit, skip int) ([]string, error) {
	if store == nil {
		return nil, nil
	}
	if limit < 1 {
		limit = 1
	}
	gen, release := store.Acquire()
	if gen == nil || gen.pool == nil {
		release()
		return nil, errNoGeneration
	}
	defer release()
	rows, err := gen.pool.Query(ctx, seedByDegreeQuery(skip, limit), nil)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	seen := map[string]struct{}{}
	for _, row := range rows {
		name := strings.TrimSpace(rowString(row, "name"))
		key := lowerKey(name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

// seedOneHopQuery returns the seed's outgoing (predicate, neighbor) edges over
// the reified model. $seed is bound as a parameter so entity names containing
// quotes/apostrophes are safe.
func seedOneHopQuery(limit int) string {
	if limit < 1 {
		limit = 1
	}
	return fmt.Sprintf("MATCH (a:Entity)-[:RELATES_TO]->(r:RelatesToNode_)-[:RELATES_TO]->(b:Entity) WHERE a.name = $seed RETURN r.name AS predicate, b.name AS neighbor LIMIT %d", limit)
}

// seedTwoHopQuery returns entities two hops from the seed -- candidate missing
// links once the directly-connected ones are removed in Go.
func seedTwoHopQuery(limit int) string {
	if limit < 1 {
		limit = 1
	}
	return fmt.Sprintf("MATCH (a:Entity)-[:RELATES_TO]->(:RelatesToNode_)-[:RELATES_TO]->(:Entity)-[:RELATES_TO]->(:RelatesToNode_)-[:RELATES_TO]->(c:Entity) WHERE a.name = $seed AND c.name <> $seed RETURN DISTINCT c.name AS twohop LIMIT %d", limit)
}

// sharedNeighborQuery ranks entities by how many graph neighbours they share
// with the seed (over the reified model). A high shared count means a dense
// co-connection block that can be factored through a single shared
// generalization, lowering global density. $seed is a bound parameter.
func sharedNeighborQuery(limit int) string {
	if limit < 1 {
		limit = 1
	}
	return fmt.Sprintf("MATCH (a:Entity)-[:RELATES_TO]->(:RelatesToNode_)-[:RELATES_TO]->(m:Entity)<-[:RELATES_TO]-(:RelatesToNode_)<-[:RELATES_TO]-(b:Entity) WHERE a.name = $seed AND b.name <> $seed RETURN b.name AS name, count(DISTINCT m) AS shared ORDER BY shared DESC LIMIT %d", limit)
}

// factorGain maps the shared-neighbour count to an expected gain in [0.5, 0.9]:
// the denser the shared block, the more global density a factorization removes.
// Saturates at 50 shared neighbours.
func factorGain(shared int64) float64 {
	d := float64(shared)
	if d > 50 {
		d = 50
	}
	return clamp01(0.5 + 0.4*(d/50))
}

func rowFloat(row map[string]any, key string) float64 {
	value, ok := row[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	}
	return 0
}

// normalizePredicateLabel lowercases a graph predicate (stored upper-case, e.g.
// CAUSES) to the canonical form claims use; the reasoner matches case-folded.
func normalizePredicateLabel(p string) string {
	return strings.ToLower(strings.TrimSpace(p))
}

func lowerKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// neighborhoodGain maps a seed's out-degree to an expected gain in [0.3, 0.8]:
// resolving a missing link on a well-connected (central) entity is worth more
// than one on a peripheral entity. Saturates at degree 10.
func neighborhoodGain(degree int) float64 {
	if degree < 0 {
		degree = 0
	}
	d := float64(degree)
	if d > 10 {
		d = 10
	}
	return clamp01(0.3 + 0.5*(d/10))
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
