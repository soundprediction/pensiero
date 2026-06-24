package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soundprediction/pensiero/pkg/generalization"
	"github.com/soundprediction/pensiero/pkg/reasoning"
	"google.golang.org/grpc/metadata"
)

type topicBackendStats struct {
	mu      sync.Mutex
	builds  map[string]int
	entails map[string]int
	closes  map[string]*int64
	delay   time.Duration
}

func newTopicBackendStats() *topicBackendStats {
	return &topicBackendStats{
		builds:  map[string]int{},
		entails: map[string]int{},
		closes:  map[string]*int64{},
	}
}

func (s *topicBackendStats) Build(ctx context.Context, path string) (*generation, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	topic := testTopicName(path)
	s.mu.Lock()
	s.builds[topic]++
	build := s.builds[topic]
	closeCount := s.closes[topic]
	if closeCount == nil {
		closeCount = new(int64)
		s.closes[topic] = closeCount
	}
	s.mu.Unlock()
	reasoner := testReasoner{
		name: "backend-" + topic,
		entails: func(_ context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
			s.mu.Lock()
			s.entails[topic]++
			s.mu.Unlock()
			proof := testProof()
			proof.Source = claim.Subject
			proof.Target = claim.Object
			proof.Predicate = claim.Predicate
			for i := range proof.Steps {
				proof.Steps[i].Predicate = claim.Predicate
				proof.Steps[i].Source = claim.Subject
				proof.Steps[i].Target = claim.Object
			}
			return reasoning.EntailResult{
				Best:       &proof,
				Verdict:    reasoning.VerdictEntailed,
				All:        []reasoning.Proof{proof},
				Confidence: proof.Confidence,
			}, nil
		},
	}
	return newTestGeneration(fmt.Sprintf("%s-%d", topic, build), reasoner, closeCount), nil
}

func (s *topicBackendStats) buildCount(topic string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.builds[topic]
}

func (s *topicBackendStats) entailCount(topic string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entails[topic]
}

func (s *topicBackendStats) closeCount(topic string) int64 {
	s.mu.Lock()
	closeCount := s.closes[topic]
	s.mu.Unlock()
	if closeCount == nil {
		return 0
	}
	return atomic.LoadInt64(closeCount)
}

func TestTopicGenerationManagerMetadataRoutesGeneration(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "cardiology.ladybug")
	writeTopicGraph(t, dir, "oncology.g_g.ladybug")
	stats := newTopicBackendStats()
	manager := newTestTopicManager(t, dir, "", 2, stats)
	defer manager.Close()
	cache := newProofCache(manager, reasoning.DefaultGeneralRegistry(), serveReasoningConfig(), 16, 1<<20)

	ctx := incomingTopicContext("oncology")
	_, err := cache.Entails(ctx, reasoning.Claim{Subject: "heart", Predicate: "is_a", Object: "disease"})
	if err != nil {
		t.Fatalf("Entails returned error: %v", err)
	}
	if got := stats.entailCount("oncology"); got != 1 {
		t.Fatalf("oncology entails=%d, want 1", got)
	}
	if got := stats.entailCount("cardiology"); got != 0 {
		t.Fatalf("cardiology entails=%d, want 0", got)
	}
}

func TestTopicGenerationManagerMetadataParsingAndUnknownFallback(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "cardiology.ladybug")
	writeTopicGraph(t, dir, "oncology.ladybug")
	if err := os.WriteFile(filepath.Join(dir, "cardiology.txt"), []byte("heart vessel disease"), 0o600); err != nil {
		t.Fatal(err)
	}
	stats := newTopicBackendStats()
	manager := newTestTopicManager(t, dir, "oncology", 2, stats)
	defer manager.Close()
	cache := newProofCache(manager, reasoning.DefaultGeneralRegistry(), serveReasoningConfig(), 16, 1<<20)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("Pensiero-Topic", " missing ", "PENSIERO-TOPIC", " OnCoLoGy "))
	_, err := cache.Entails(ctx, reasoning.Claim{Subject: "heart", Predicate: "is_a", Object: "disease"})
	if err != nil {
		t.Fatalf("metadata Entails returned error: %v", err)
	}
	if got := stats.entailCount("oncology"); got != 1 {
		t.Fatalf("oncology entails=%d, want 1 from known metadata value", got)
	}
	if got := stats.entailCount("cardiology"); got != 0 {
		t.Fatalf("cardiology entails=%d, want 0 while metadata overrides keyword route", got)
	}

	_, err = cache.Entails(incomingTopicContext("missing"), reasoning.Claim{Subject: "heart", Predicate: "is_a", Object: "vessel disease"})
	if err != nil {
		t.Fatalf("unknown metadata keyword fallback Entails returned error: %v", err)
	}
	if got := stats.entailCount("cardiology"); got != 1 {
		t.Fatalf("cardiology entails=%d, want 1 from keyword fallback", got)
	}

	_, err = cache.Entails(incomingTopicContext("missing"), reasoning.Claim{Subject: "unmatched subject", Predicate: "is_a", Object: "unmatched object"})
	if err != nil {
		t.Fatalf("unknown metadata default fallback Entails returned error: %v", err)
	}
	if got := stats.entailCount("oncology"); got != 2 {
		t.Fatalf("oncology entails=%d, want 2 after default fallback", got)
	}
}

func TestTopicGenerationManagerKeywordAndDefaultFallback(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "cardiology.ladybug")
	writeTopicGraph(t, dir, "oncology.ladybug")
	if err := os.WriteFile(filepath.Join(dir, "cardiology.txt"), []byte("heart vessel disease"), 0o600); err != nil {
		t.Fatal(err)
	}
	stats := newTopicBackendStats()
	manager := newTestTopicManager(t, dir, "oncology", 2, stats)
	defer manager.Close()
	cache := newProofCache(manager, reasoning.DefaultGeneralRegistry(), serveReasoningConfig(), 16, 1<<20)

	_, err := cache.Entails(context.Background(), reasoning.Claim{Subject: "heart", Predicate: "is_a", Object: "disease"})
	if err != nil {
		t.Fatalf("keyword Entails returned error: %v", err)
	}
	_, err = cache.Entails(context.Background(), reasoning.Claim{Subject: "unmatched subject", Predicate: "is_a", Object: "unmatched object"})
	if err != nil {
		t.Fatalf("default Entails returned error: %v", err)
	}
	if got := stats.entailCount("cardiology"); got != 1 {
		t.Fatalf("cardiology entails=%d, want 1", got)
	}
	if got := stats.entailCount("oncology"); got != 1 {
		t.Fatalf("oncology entails=%d, want 1", got)
	}
}

func TestTopicGenerationManagerLRUEvictsDrainsAndReopens(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "alpha.ladybug")
	writeTopicGraph(t, dir, "beta.ladybug")
	writeTopicGraph(t, dir, "gamma.ladybug")
	stats := newTopicBackendStats()
	manager := newTestTopicManager(t, dir, "", 2, stats)
	defer manager.Close()

	gen, release := mustAcquireTopic(t, manager, "alpha")
	if gen.id != "alpha-1" {
		t.Fatalf("alpha generation=%s, want alpha-1", gen.id)
	}
	release()
	_, releaseBeta := mustAcquireTopic(t, manager, "beta")
	_, releaseAlpha := mustAcquireTopic(t, manager, "alpha")
	releaseAlpha()

	openedGamma := make(chan struct{})
	go func() {
		_, releaseGamma := mustAcquireTopic(t, manager, "gamma")
		releaseGamma()
		close(openedGamma)
	}()
	waitForClosed(t, openedGamma, "gamma to open without waiting for beta ref drain")
	if got := stats.closeCount("beta"); got != 0 {
		t.Fatalf("beta close count while held=%d, want 0", got)
	}
	releaseBeta()
	waitForCondition(t, time.Second, func() bool {
		return stats.closeCount("beta") == 1
	}, "beta generation to close after held ref release")

	_, releaseBeta2 := mustAcquireTopic(t, manager, "beta")
	releaseBeta2()
	if got := stats.buildCount("beta"); got != 2 {
		t.Fatalf("beta builds=%d, want reopened build count 2", got)
	}
}

func TestTopicGenerationManagerCloseClosesAllOpenTopics(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "alpha.ladybug")
	writeTopicGraph(t, dir, "beta.ladybug")
	writeTopicGraph(t, dir, "gamma.ladybug")
	stats := newTopicBackendStats()
	manager := newTestTopicManager(t, dir, "", 3, stats)

	for _, topic := range []string{"alpha", "beta", "gamma"} {
		_, release := mustAcquireTopic(t, manager, topic)
		release()
	}
	if got := len(manager.TopicSnapshot().Open); got != 3 {
		t.Fatalf("open topics before close=%d, want 3", got)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	for _, topic := range []string{"alpha", "beta", "gamma"} {
		if got := stats.closeCount(topic); got != 1 {
			t.Fatalf("%s close count=%d, want 1", topic, got)
		}
	}
	if got := len(manager.TopicSnapshot().Open); got != 0 {
		t.Fatalf("open topics after close=%d, want 0", got)
	}
	gen, release, err := manager.AcquireGeneration(incomingTopicContext("alpha"), generationRoute{Text: "ignored"})
	release()
	if !errors.Is(err, errNoGeneration) || gen != nil {
		t.Fatalf("AcquireGeneration after close gen=%v err=%v, want no generation", gen, errNoGeneration)
	}
}

func TestProofCacheIsolatesSameClaimByTopicGeneration(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "alpha.ladybug")
	writeTopicGraph(t, dir, "beta.ladybug")
	stats := newTopicBackendStats()
	manager := newTestTopicManager(t, dir, "", 2, stats)
	defer manager.Close()
	cache := newProofCache(manager, reasoning.DefaultGeneralRegistry(), serveReasoningConfig(), 16, 1<<20)
	claim := reasoning.Claim{Subject: "same", Predicate: "is_a", Object: "claim"}

	if _, err := cache.Entails(incomingTopicContext("alpha"), claim); err != nil {
		t.Fatalf("alpha Entails returned error: %v", err)
	}
	if _, err := cache.Entails(incomingTopicContext("beta"), claim); err != nil {
		t.Fatalf("beta Entails returned error: %v", err)
	}
	if _, err := cache.Entails(incomingTopicContext("alpha"), claim); err != nil {
		t.Fatalf("alpha cached Entails returned error: %v", err)
	}
	if got := stats.entailCount("alpha"); got != 1 {
		t.Fatalf("alpha backend calls=%d, want 1", got)
	}
	if got := stats.entailCount("beta"); got != 1 {
		t.Fatalf("beta backend calls=%d, want 1", got)
	}
	cache.mu.Lock()
	entries := len(cache.entries)
	cache.mu.Unlock()
	if entries != 2 {
		t.Fatalf("cache entries=%d, want 2 per-topic entries", entries)
	}
}

func TestProofCacheSingleSourceIgnoresTopicMetadata(t *testing.T) {
	var calls int64
	cache, store := newTestProofCache("single", testReasoner{
		name: "single",
		entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
			atomic.AddInt64(&calls, 1)
			proof := testProof()
			return reasoning.EntailResult{Best: &proof, Verdict: reasoning.VerdictEntailed, All: []reasoning.Proof{proof}, Confidence: proof.Confidence}, nil
		},
	})
	defer store.Close()
	claim := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}

	if _, err := cache.Entails(incomingTopicContext("missing-topic"), claim); err != nil {
		t.Fatalf("first Entails returned error: %v", err)
	}
	if _, err := cache.Entails(incomingTopicContext("other-topic"), claim); err != nil {
		t.Fatalf("second Entails returned error: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("single-source backend calls=%d, want 1 cached call", got)
	}
}

func TestTopicGenerationManagerConcurrentLazyOpen(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "alpha.ladybug")
	stats := newTopicBackendStats()
	stats.delay = time.Millisecond
	manager := newTestTopicManager(t, dir, "", 4, stats)
	defer manager.Close()

	const workers = 64
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			gen, release, err := manager.AcquireGeneration(incomingTopicContext("alpha"), generationRoute{Text: "ignored"})
			if err != nil {
				errs <- err
				return
			}
			if gen == nil || gen.id != "alpha-1" {
				errs <- fmt.Errorf("generation=%v, want alpha-1", gen)
			}
			release()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := stats.buildCount("alpha"); got != 1 {
		t.Fatalf("alpha builds=%d, want 1", got)
	}
}

func TestHealthzTopicSnapshotReportsAvailableAndOpenOnly(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "alpha.ladybug")
	writeTopicGraph(t, dir, "beta.ladybug")
	stats := newTopicBackendStats()
	manager := newTestTopicManager(t, dir, "", 2, stats)
	defer manager.Close()

	topics := readHealthTopics(t, manager)
	if got := strings.Join(topics.Available, ","); got != "alpha,beta" {
		t.Fatalf("available topics=%q, want alpha,beta", got)
	}
	if len(topics.Open) != 0 {
		t.Fatalf("open topics before lazy open=%#v, want none", topics.Open)
	}
	if got := stats.buildCount("alpha") + stats.buildCount("beta"); got != 0 {
		t.Fatalf("healthz builds before lazy open=%d, want 0", got)
	}

	_, release := mustAcquireTopic(t, manager, "beta")
	release()
	topics = readHealthTopics(t, manager)
	if len(topics.Open) != 1 || topics.Open[0].Topic != "beta" {
		t.Fatalf("open topics after beta open=%#v, want only beta", topics.Open)
	}
	if got := stats.buildCount("alpha"); got != 0 {
		t.Fatalf("alpha builds after healthz=%d, want 0", got)
	}
}

func TestGenerationGraphQuerierDoesNotOpenTopics(t *testing.T) {
	dir := t.TempDir()
	writeTopicGraph(t, dir, "alpha.ladybug")
	writeTopicGraph(t, dir, "beta.ladybug")
	stats := newTopicBackendStats()
	manager := newTestTopicManager(t, dir, "", 2, stats)
	defer manager.Close()
	querier := generationGraphQuerier{source: manager}

	_, err := querier.Query(context.Background(), "MATCH (n:Entity) RETURN n.name AS name", nil)
	if !errors.Is(err, errNoGeneration) {
		t.Fatalf("Query without open topic error=%v, want %v", err, errNoGeneration)
	}
	if got := stats.buildCount("alpha") + stats.buildCount("beta"); got != 0 {
		t.Fatalf("query without open topic builds=%d, want 0", got)
	}

	_, release := mustAcquireTopic(t, manager, "beta")
	release()
	if _, err := querier.Query(context.Background(), "MATCH (n:Entity) RETURN n.name AS name", nil); err != nil {
		t.Fatalf("Query on open topic returned error: %v", err)
	}
	if got := stats.buildCount("alpha"); got != 0 {
		t.Fatalf("alpha builds=%d, want 0", got)
	}
	if got := stats.buildCount("beta"); got != 1 {
		t.Fatalf("beta builds=%d, want 1", got)
	}
}

func newTestTopicManager(t *testing.T, dir string, defaultTopic string, maxOpen int, stats *topicBackendStats) *topicGenerationManager {
	t.Helper()
	manager, err := newTopicGenerationManager(context.Background(), dir, defaultTopic, maxOpen, time.Hour, stats.Build, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func writeTopicGraph(t *testing.T, dir string, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("graph"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func incomingTopicContext(topic string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(topicMetadataKey, topic))
}

func readHealthTopics(t *testing.T, manager *topicGenerationManager) topicServingSnapshot {
	t.Helper()
	ready := newReadinessGate()
	ready.MarkReady()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	healthHandler(generalization.NewMetrics(), nil, ready, nil, nil, nil, nil, manager).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status=%d, want 200", rec.Code)
	}
	var payload struct {
		Topics topicServingSnapshot `json:"topics"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	return payload.Topics
}

func mustAcquireTopic(t *testing.T, manager *topicGenerationManager, topic string) (*generation, func()) {
	t.Helper()
	gen, release, err := manager.AcquireGeneration(incomingTopicContext(topic), generationRoute{Text: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	return gen, release
}

func testTopicName(path string) string {
	ext := supportedTopicGraphExt(filepath.Base(path))
	if ext == "" {
		ext = filepath.Ext(path)
	}
	aliases := topicGraphAliases(filepath.Base(path), ext)
	if len(aliases) == 0 {
		return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return aliases[0]
}

func waitForCondition(t *testing.T, timeout time.Duration, ok func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}
