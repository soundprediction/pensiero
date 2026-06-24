package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

func TestCognitionSchedulerRunsOnlyWhenIdle(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{Now: clock.Now})
	end := load.Begin()
	defer end()
	ctx, cancel := context.WithCancel(context.Background())
	sleeper := newSchedulerRecordingSleep(clock, cancel, 1)
	executor := &fakeThoughtExecutor{}
	scheduler := NewCognitionScheduler(
		&fakeThoughtSelector{thoughts: []Thought{{Type: ThoughtProofPrecompute, Claim: testClaim()}}},
		executor,
		load,
		CognitionSchedulerConfig{
			BaseInterval: time.Second,
			QuietFor:     0,
			Now:          clock.Now,
			Sleep:        sleeper.Sleep,
			Jitter:       identityJitter,
		},
	)
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := executor.Count(); got != 0 {
		t.Fatalf("executed thoughts=%d, want 0 while load is active", got)
	}
}

func TestCognitionSchedulerCancelsOnLoad(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{Now: clock.Now})
	started := make(chan struct{})
	executor := &fakeThoughtExecutor{
		run: func(ctx context.Context, _ Thought) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	sleeper := newSchedulerRecordingSleep(clock, cancel, 1)
	scheduler := NewCognitionScheduler(
		&fakeThoughtSelector{thoughts: []Thought{{Type: ThoughtProofPrecompute, Claim: testClaim()}}},
		executor,
		load,
		CognitionSchedulerConfig{
			BaseInterval:  time.Second,
			QuietFor:      0,
			WindowBudget:  time.Second,
			ThoughtBudget: time.Second,
			LoadPoll:      time.Hour,
			Now:           clock.Now,
			Sleep:         sleeper.Sleep,
			Jitter:        identityJitter,
		},
	)
	done := make(chan error, 1)
	go func() {
		done <- scheduler.Run(ctx)
	}()
	waitForClosed(t, started, "cognition thought to start")
	end := load.Begin()
	defer end()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not return after load canceled cognition")
	}
	if !errors.Is(executor.LastError(), context.Canceled) {
		t.Fatalf("executor error=%v, want context canceled", executor.LastError())
	}
}

func TestTopicSelectorWeightingAndRandomFloor(t *testing.T) {
	hot := &countingThoughtSource{name: "hot", thought: Thought{Type: ThoughtProofPrecompute, Claim: testClaim()}}
	randomSource := &countingThoughtSource{name: "random", thought: Thought{Type: ThoughtHypothesisTest, Claim: testClaim()}}
	selector := newTopicSelectorFromSources([]weightedThoughtSource{
		{Weight: 3, Source: hot},
		{Weight: 1, Source: randomSource},
	}, rand.New(rand.NewSource(11)))
	for i := 0; i < 1000; i++ {
		if _, ok, err := selector.Next(context.Background()); err != nil || !ok {
			t.Fatalf("Next ok=%v err=%v", ok, err)
		}
	}
	if hot.Count() <= randomSource.Count() {
		t.Fatalf("weighted source counts hot=%d random=%d, want hot > random", hot.Count(), randomSource.Count())
	}

	reg := reasoning.NewPredicateRegistry([]reasoning.PredicateMeta{{Raw: "related_to", Canonical: "related_to"}}, nil, nil)
	store := newTopicTestStore([]string{"alpha", "beta"})
	defer store.Close()
	floor := NewTopicSelector(store, nil, reg, nil, TopicSelectorConfig{
		QueryHotWeight:   0,
		RandomWeight:     0,
		UnresolvedWeight: 0,
		SemanticWeight:   0,
		Random:           rand.New(rand.NewSource(3)),
	})
	thought, ok, err := floor.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("random floor Next ok=%v err=%v", ok, err)
	}
	if thought.Source != "random" || thought.Type != ThoughtHypothesisTest {
		t.Fatalf("floor thought=%#v, want random hypothesis-test", thought)
	}
}

func TestThoughtEngineProofPrecomputeWarmsCache(t *testing.T) {
	var calls int64
	cache, store := newTestProofCache("g1", testReasoner{
		name: "backend",
		entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
			atomic.AddInt64(&calls, 1)
			proof := testProof()
			return reasoning.EntailResult{
				Best:       &proof,
				Verdict:    reasoning.VerdictEntailed,
				All:        []reasoning.Proof{proof},
				Confidence: proof.Confidence,
			}, nil
		},
	})
	defer store.Close()
	engine := &ThoughtEngine{Reasoner: cache}
	if err := engine.Execute(context.Background(), Thought{Type: ThoughtProofPrecompute, Claim: testClaim()}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if _, err := cache.Entails(context.Background(), testClaim()); err != nil {
		t.Fatalf("Entails returned error: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("inner Entails calls=%d, want cache prewarm to make second call hit", got)
	}
}

func TestThoughtEngineContradictionHuntEmitsQuestion(t *testing.T) {
	questions := newQuestionStore(8, nil)
	engine := &ThoughtEngine{
		Reasoner: testReasoner{
			name: "backend",
			contradicts: func(context.Context, reasoning.Claim) (bool, *reasoning.Proof, error) {
				return true, &reasoning.Proof{Predicate: "conflicts_with", Confidence: 0.8}, nil
			},
		},
		Questions: questions,
	}
	if err := engine.Execute(context.Background(), Thought{Type: ThoughtContradictionHunt, Claim: testClaim()}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	snapshot := questions.Snapshot()
	if len(snapshot.Questions) != 1 {
		t.Fatalf("questions=%d, want 1", len(snapshot.Questions))
	}
	if !strings.Contains(snapshot.Questions[0].Rationale, "conflicts_with") {
		t.Fatalf("rationale=%q, want conflicting predicate", snapshot.Questions[0].Rationale)
	}
}

func TestThoughtEngineHypothesisTestRecordsSpeculativeAndNearMissQuestion(t *testing.T) {
	unconfirmed := newUnconfirmedStore(8, nil)
	entailed := &ThoughtEngine{
		Reasoner: testReasoner{
			name: "backend",
			entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
				return reasoning.EntailResult{Verdict: reasoning.VerdictEntailed, Confidence: 0.7}, nil
			},
		},
		Unconfirmed: unconfirmed,
	}
	if err := entailed.Execute(context.Background(), Thought{Type: ThoughtHypothesisTest, Claim: testClaim()}); err != nil {
		t.Fatalf("entailed Execute returned error: %v", err)
	}
	facts := unconfirmed.Snapshot().Facts
	if len(facts) != 1 || facts[0].Provenance != string(ThoughtHypothesisTest) || facts[0].Confidence != 0.7 {
		t.Fatalf("facts=%#v, want one hypothesis-test fact", facts)
	}

	questions := newQuestionStore(8, nil)
	nearMiss := &ThoughtEngine{
		Reasoner: testReasoner{
			name: "backend",
			entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
				proof := testProof()
				proof.Confidence = 0.4
				return reasoning.EntailResult{Verdict: reasoning.VerdictUnsupported, Best: &proof, Confidence: 0.4}, nil
			},
		},
		Questions: questions,
	}
	if err := nearMiss.Execute(context.Background(), Thought{Type: ThoughtHypothesisTest, Claim: testClaim()}); err != nil {
		t.Fatalf("near-miss Execute returned error: %v", err)
	}
	if got := len(questions.Snapshot().Questions); got != 1 {
		t.Fatalf("questions=%d, want 1", got)
	}
}

func TestQuestionSinkDedupeAndQuestionsEndpoint(t *testing.T) {
	questions := newQuestionStore(8, nil)
	claim := reasoning.Claim{Subject: "question-secret-subject", Predicate: "is_a", Object: "question-secret-object"}
	err := questions.Emit(context.Background(), []reasoning.Question{
		{Claim: claim, Rationale: "first", ExpectedGain: 0.9},
		{Claim: claim, Rationale: "duplicate", ExpectedGain: 0.1},
	})
	if err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/questions", nil)
	rec := httptest.NewRecorder()
	healthHandler(nil, nil, newReadinessGate(), nil, questions, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "question-secret-subject") || strings.Contains(body, "question-secret-object") {
		t.Fatalf("questions response leaked raw entity names: %s", body)
	}
	var snapshot QuestionSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode questions: %v", err)
	}
	if len(snapshot.Questions) != 1 || snapshot.Questions[0].Rationale != "first" {
		t.Fatalf("snapshot=%#v, want deduped first question", snapshot)
	}
}

func TestUnconfirmedEndpointSnapshot(t *testing.T) {
	unconfirmed := newUnconfirmedStore(8, nil)
	claim := reasoning.Claim{Subject: "unconfirmed-secret-subject", Predicate: "is_a", Object: "unconfirmed-secret-object"}
	if err := unconfirmed.Record(context.Background(), SpeculativeFact{
		Claim:      claim,
		Confidence: 0.6,
		Provenance: string(ThoughtHypothesisTest),
	}); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/unconfirmed", nil)
	rec := httptest.NewRecorder()
	healthHandler(nil, nil, newReadinessGate(), nil, nil, unconfirmed, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "unconfirmed-secret-subject") || strings.Contains(body, "unconfirmed-secret-object") {
		t.Fatalf("unconfirmed response leaked raw entity names: %s", body)
	}
	var snapshot UnconfirmedSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode unconfirmed: %v", err)
	}
	if len(snapshot.Facts) != 1 || snapshot.Facts[0].Provenance != string(ThoughtHypothesisTest) {
		t.Fatalf("snapshot=%#v, want one unconfirmed fact", snapshot)
	}
}

func TestThinkingEndpointReturnsCurrentAndRecentThoughtState(t *testing.T) {
	thinking := newThinkingState(4)
	thinking.SetCognitionActive(true)
	thinking.SetSourceWeights(map[string]int{"query-hot": 3, "random": 1})
	started := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	recent := Thought{
		Type:   ThoughtHypothesisTest,
		Claim:  reasoning.Claim{Subject: "recent-secret-subject", Predicate: "is_a", Object: "recent-secret-object"},
		Source: "random",
	}
	thinking.BeginThought(recent, started)
	thinking.FinishThought(recent, "ok", started, 15*time.Millisecond)
	current := Thought{
		Type:   ThoughtContradictionHunt,
		Claim:  reasoning.Claim{Subject: "current-secret-subject", Predicate: "related_to", Object: "current-secret-object"},
		Source: "query-hot",
	}
	thinking.BeginThought(current, started.Add(time.Second))

	questions := newQuestionStore(8, nil)
	if err := questions.Emit(context.Background(), []reasoning.Question{{Claim: testClaim(), Rationale: "resolve", ExpectedGain: 0.8}}); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	unconfirmed := newUnconfirmedStore(8, nil)
	if err := unconfirmed.Record(context.Background(), SpeculativeFact{Claim: testClaim(), Confidence: 0.7, Provenance: string(ThoughtHypothesisTest)}); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/thinking", nil)
	rec := httptest.NewRecorder()
	healthHandler(nil, nil, newReadinessGate(), nil, questions, unconfirmed, thinking).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, raw := range []string{"recent-secret-subject", "recent-secret-object", "current-secret-subject", "current-secret-object"} {
		if strings.Contains(body, raw) {
			t.Fatalf("thinking response leaked raw entity name %q: %s", raw, body)
		}
	}
	var snapshot ThinkingSnapshot
	if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
		t.Fatalf("decode thinking: %v", err)
	}
	if !snapshot.CognitionActive || snapshot.GatedWaitingForIdle {
		t.Fatalf("active=%v gated=%v, want active and not gated", snapshot.CognitionActive, snapshot.GatedWaitingForIdle)
	}
	if snapshot.CurrentThought == nil || snapshot.CurrentThought.Type != ThoughtContradictionHunt {
		t.Fatalf("current=%#v, want contradiction hunt", snapshot.CurrentThought)
	}
	if snapshot.CurrentThought.Topic.SubjectHash != hashEntityName("current-secret-subject") || snapshot.CurrentThought.Topic.ObjectHash != hashEntityName("current-secret-object") {
		t.Fatalf("current topic=%#v, want hashed current entities", snapshot.CurrentThought.Topic)
	}
	if len(snapshot.RecentThoughts) != 1 || snapshot.RecentThoughts[0].Outcome != "ok" || snapshot.RecentThoughts[0].DurationMS != 15 {
		t.Fatalf("recent=%#v, want one ok 15ms thought", snapshot.RecentThoughts)
	}
	if snapshot.TypeCounts[string(ThoughtHypothesisTest)] != 1 {
		t.Fatalf("type counts=%#v, want one hypothesis-test", snapshot.TypeCounts)
	}
	if snapshot.TopicSelector.SourceWeights["query-hot"] != 3 || snapshot.TopicSelector.SourceWeights["random"] != 1 || snapshot.TopicSelector.LastPickedSource != "query-hot" {
		t.Fatalf("topic selector=%#v, want weights and last query-hot", snapshot.TopicSelector)
	}
	if snapshot.Summary.PendingQuestions != 1 || snapshot.Summary.UnconfirmedFacts != 1 {
		t.Fatalf("summary=%#v, want one question and one unconfirmed fact", snapshot.Summary)
	}
}

func testClaim() reasoning.Claim {
	return reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}
}

type fakeThoughtSelector struct {
	mu       sync.Mutex
	thoughts []Thought
	next     int
}

func (s *fakeThoughtSelector) Next(context.Context) (Thought, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.thoughts) {
		return Thought{}, false, nil
	}
	thought := s.thoughts[s.next]
	s.next++
	return thought, true, nil
}

type fakeThoughtExecutor struct {
	calls atomic.Int64
	mu    sync.Mutex
	err   error
	run   func(context.Context, Thought) error
}

func (e *fakeThoughtExecutor) Execute(ctx context.Context, thought Thought) error {
	e.calls.Add(1)
	var err error
	if e.run != nil {
		err = e.run(ctx, thought)
	}
	e.mu.Lock()
	e.err = err
	e.mu.Unlock()
	return err
}

func (e *fakeThoughtExecutor) Count() int64 {
	return e.calls.Load()
}

func (e *fakeThoughtExecutor) LastError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

type countingThoughtSource struct {
	name    string
	thought Thought
	count   atomic.Int64
}

func (s *countingThoughtSource) Name() string {
	return s.name
}

func (s *countingThoughtSource) Next(context.Context) (Thought, bool, error) {
	s.count.Add(1)
	thought := s.thought
	thought.Source = s.name
	return thought, true, nil
}

func (s *countingThoughtSource) Count() int64 {
	return s.count.Load()
}

func newTopicTestStore(names []string) *generationStore {
	handle := &fakeGraphHandle{
		query: func(context.Context, string, map[string]any) ([]map[string]any, error) {
			rows := make([]map[string]any, 0, len(names))
			for _, name := range names {
				rows = append(rows, map[string]any{"name": name})
			}
			return rows, nil
		},
	}
	return newGenerationStore(&generation{
		id:       "topic-test",
		pool:     newTestPool(handle),
		reasoner: testReasoner{name: "topic-test"},
		path:     "topic-test",
	})
}
