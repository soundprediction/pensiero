package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

const defaultThinkingRecentLimit = 32

type ThinkingSnapshot struct {
	CognitionActive     bool                          `json:"cognitionActive"`
	GatedWaitingForIdle bool                          `json:"gatedWaitingForIdle"`
	CurrentThought      *ThinkingCurrentThought       `json:"currentThought"`
	RecentThoughts      []ThinkingRecentThought       `json:"recentThoughts"`
	TypeCounts          map[string]int64              `json:"typeCounts"`
	TopicSelector       ThinkingTopicSelectorSnapshot `json:"topicSelector"`
	Summary             ThinkingSummary               `json:"summary"`
}

type ThinkingCurrentThought struct {
	Type      ThoughtType `json:"type"`
	Topic     hashedClaim `json:"topic"`
	StartedAt time.Time   `json:"startedAt"`
}

type ThinkingRecentThought struct {
	Type       ThoughtType `json:"type"`
	Topic      hashedClaim `json:"topic"`
	Outcome    string      `json:"outcome"`
	StartedAt  time.Time   `json:"startedAt"`
	DurationMS int64       `json:"durationMS"`
}

type ThinkingTopicSelectorSnapshot struct {
	SourceWeights    map[string]int `json:"sourceWeights"`
	LastPickedSource string         `json:"lastPickedSource"`
}

type ThinkingSummary struct {
	PendingQuestions int `json:"pendingQuestions"`
	UnconfirmedFacts int `json:"unconfirmedFacts"`
}

type thinkingState struct {
	mu sync.Mutex

	active     bool
	gated      bool
	current    *thinkingThoughtRecord
	recent     []thinkingThoughtRecord
	next       int
	full       bool
	typeCounts map[string]int64

	sourceWeights    map[string]int
	lastPickedSource string
}

type thinkingThoughtRecord struct {
	Type      ThoughtType
	Claim     hashedClaim
	Outcome   string
	Source    string
	StartedAt time.Time
	Duration  time.Duration
}

func newThinkingState(limit int) *thinkingState {
	if limit <= 0 {
		limit = defaultThinkingRecentLimit
	}
	return &thinkingState{
		recent:        make([]thinkingThoughtRecord, 0, limit),
		typeCounts:    map[string]int64{},
		sourceWeights: map[string]int{},
	}
}

func (s *thinkingState) SetCognitionActive(active bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = active
	if !active {
		s.gated = false
		s.current = nil
	}
}

func (s *thinkingState) SetGatedWaitingForIdle(waiting bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gated = waiting
	if waiting {
		s.current = nil
	}
}

func (s *thinkingState) SetSourceWeights(weights map[string]int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceWeights = cloneIntMap(weights)
}

func (s *thinkingState) BeginThought(thought Thought, startedAt time.Time) {
	if s == nil {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	record := thinkingThoughtRecord{
		Type:      thought.Type,
		Claim:     hashClaim(thought.Claim),
		Source:    thought.Source,
		StartedAt: startedAt,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gated = false
	s.current = &record
	if thought.Source != "" {
		s.lastPickedSource = thought.Source
	}
}

func (s *thinkingState) FinishThought(thought Thought, outcome string, startedAt time.Time, duration time.Duration) {
	if s == nil {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if duration < 0 {
		duration = 0
	}
	if outcome == "" {
		outcome = "ok"
	}
	record := thinkingThoughtRecord{
		Type:      thought.Type,
		Claim:     hashClaim(thought.Claim),
		Outcome:   outcome,
		Source:    thought.Source,
		StartedAt: startedAt,
		Duration:  duration,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = nil
	s.typeCounts[string(thought.Type)]++
	if thought.Source != "" {
		s.lastPickedSource = thought.Source
	}
	if len(s.recent) < cap(s.recent) {
		s.recent = append(s.recent, record)
		return
	}
	if cap(s.recent) == 0 {
		return
	}
	s.recent[s.next] = record
	s.next = (s.next + 1) % cap(s.recent)
	s.full = true
}

func (s *thinkingState) Snapshot(pendingQuestions int, unconfirmedFacts int) ThinkingSnapshot {
	snapshot := ThinkingSnapshot{
		TypeCounts: map[string]int64{},
		TopicSelector: ThinkingTopicSelectorSnapshot{
			SourceWeights: map[string]int{},
		},
		Summary: ThinkingSummary{
			PendingQuestions: pendingQuestions,
			UnconfirmedFacts: unconfirmedFacts,
		},
	}
	if s == nil {
		return snapshot
	}
	s.mu.Lock()
	snapshot.CognitionActive = s.active
	snapshot.GatedWaitingForIdle = s.gated
	if s.current != nil {
		current := *s.current
		snapshot.CurrentThought = &ThinkingCurrentThought{
			Type:      current.Type,
			Topic:     current.Claim,
			StartedAt: current.StartedAt,
		}
	}
	for key, value := range s.typeCounts {
		snapshot.TypeCounts[key] = value
	}
	snapshot.TopicSelector.SourceWeights = cloneIntMap(s.sourceWeights)
	snapshot.TopicSelector.LastPickedSource = s.lastPickedSource
	recent := s.retainedThoughtsLocked()
	s.mu.Unlock()

	snapshot.RecentThoughts = make([]ThinkingRecentThought, len(recent))
	for i, record := range recent {
		snapshot.RecentThoughts[i] = ThinkingRecentThought{
			Type:       record.Type,
			Topic:      record.Claim,
			Outcome:    record.Outcome,
			StartedAt:  record.StartedAt,
			DurationMS: record.Duration.Milliseconds(),
		}
	}
	return snapshot
}

func (s *thinkingState) retainedThoughtsLocked() []thinkingThoughtRecord {
	if len(s.recent) == 0 {
		return nil
	}
	if !s.full {
		out := make([]thinkingThoughtRecord, len(s.recent))
		copy(out, s.recent)
		return out
	}
	out := make([]thinkingThoughtRecord, 0, len(s.recent))
	out = append(out, s.recent[s.next:]...)
	out = append(out, s.recent[:s.next]...)
	return out
}

func classifyCognitionOutcome(ctx context.Context, err error) string {
	switch {
	case err == nil && (ctx == nil || ctx.Err() == nil):
		return "ok"
	case errors.Is(err, context.DeadlineExceeded), ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled), ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		return "canceled"
	case err != nil:
		return "error"
	default:
		return "canceled"
	}
}

func cloneIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
