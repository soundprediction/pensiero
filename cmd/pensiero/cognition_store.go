package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const (
	defaultQuestionLimit    = 256
	defaultUnconfirmedLimit = 256
)

// cognitionLabelsEnabled, when true, includes the raw entity labels alongside
// their hashes in cognition introspection (/thinking, /questions). It defaults
// to false so the endpoints stay privacy-preserving (hash-only); operators
// serving non-sensitive knowledge graphs can opt in via --cognition-show-labels.
var cognitionLabelsEnabled atomic.Bool

func setCognitionLabels(enabled bool) { cognitionLabelsEnabled.Store(enabled) }

type hashedClaim struct {
	Subject     string `json:"subject,omitempty"`
	SubjectHash string `json:"subject_hash,omitempty"`
	Predicate   string `json:"predicate,omitempty"`
	Object      string `json:"object,omitempty"`
	ObjectHash  string `json:"object_hash,omitempty"`
}

type questionRecord struct {
	At           time.Time       `json:"at"`
	Claim        reasoning.Claim `json:"claim"`
	Rationale    string          `json:"rationale"`
	ExpectedGain float64         `json:"expected_gain"`
}

type QuestionRecord struct {
	At           time.Time   `json:"at"`
	Claim        hashedClaim `json:"claim"`
	Rationale    string      `json:"rationale"`
	ExpectedGain float64     `json:"expected_gain"`
}

type QuestionSnapshot struct {
	Limit     int              `json:"limit"`
	Questions []QuestionRecord `json:"questions"`
}

type questionStore struct {
	mu      sync.Mutex
	limit   int
	records []questionRecord
	seen    map[string]struct{}
	logger  interface{ Printf(string, ...any) }
	now     func() time.Time
}

func newQuestionStore(limit int, logger interface{ Printf(string, ...any) }) *questionStore {
	if limit <= 0 {
		limit = defaultQuestionLimit
	}
	return &questionStore{
		limit:  limit,
		seen:   map[string]struct{}{},
		logger: logger,
		now:    time.Now,
	}
}

func (s *questionStore) Emit(ctx context.Context, questions []reasoning.Question) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || len(questions) == 0 {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, question := range questions {
		key := claimDedupeKey(question.Claim)
		if key == "" {
			continue
		}
		if _, ok := s.seen[key]; ok {
			continue
		}
		record := questionRecord{
			At:           s.now(),
			Claim:        normalizeQuestionClaim(question.Claim),
			Rationale:    strings.TrimSpace(question.Rationale),
			ExpectedGain: clamp01(question.ExpectedGain),
		}
		s.records = append(s.records, record)
		s.seen[key] = struct{}{}
		if len(s.records) > s.limit {
			evicted := s.records[0]
			delete(s.seen, claimDedupeKey(evicted.Claim))
			copy(s.records, s.records[1:])
			s.records = s.records[:len(s.records)-1]
		}
		if s.logger != nil {
			s.logger.Printf("question claim=%s %s %s expected_gain=%.3f rationale=%s",
				record.Claim.Subject, record.Claim.Predicate, record.Claim.Object, record.ExpectedGain, record.Rationale)
		}
	}
	return ctx.Err()
}

func (s *questionStore) Snapshot() QuestionSnapshot {
	if s == nil {
		return QuestionSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]QuestionRecord, len(s.records))
	for i, record := range s.records {
		out[i] = QuestionRecord{
			At:           record.At,
			Claim:        hashClaim(record.Claim),
			Rationale:    record.Rationale,
			ExpectedGain: record.ExpectedGain,
		}
	}
	return QuestionSnapshot{Limit: s.limit, Questions: out}
}

func (s *questionStore) Count() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

type SpeculativeFact struct {
	At         time.Time       `json:"at"`
	Claim      reasoning.Claim `json:"claim"`
	Confidence float64         `json:"confidence"`
	Provenance string          `json:"provenance"`
}

type UnconfirmedSnapshot struct {
	Limit int                     `json:"limit"`
	Facts []UnconfirmedFactRecord `json:"facts"`
}

type UnconfirmedFactRecord struct {
	At         time.Time   `json:"at"`
	Claim      hashedClaim `json:"claim"`
	Confidence float64     `json:"confidence"`
	Provenance string      `json:"provenance"`
}

type unconfirmedStore struct {
	mu     sync.Mutex
	limit  int
	facts  []SpeculativeFact
	seen   map[string]int
	now    func() time.Time
	logger interface{ Printf(string, ...any) }
}

// TODO: persisting speculative facts to the graph with a status column is deferred.
func newUnconfirmedStore(limit int, logger interface{ Printf(string, ...any) }) *unconfirmedStore {
	if limit <= 0 {
		limit = defaultUnconfirmedLimit
	}
	return &unconfirmedStore{
		limit:  limit,
		seen:   map[string]int{},
		now:    time.Now,
		logger: logger,
	}
}

func (s *unconfirmedStore) Record(ctx context.Context, fact SpeculativeFact) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := claimDedupeKey(fact.Claim)
	if key == "" {
		return ctx.Err()
	}
	fact.At = s.now()
	fact.Claim = normalizeQuestionClaim(fact.Claim)
	fact.Confidence = clamp01(fact.Confidence)
	fact.Provenance = strings.TrimSpace(fact.Provenance)

	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.seen[key]; ok && idx >= 0 && idx < len(s.facts) {
		s.facts[idx] = fact
		return ctx.Err()
	}
	s.facts = append(s.facts, fact)
	s.seen[key] = len(s.facts) - 1
	if len(s.facts) > s.limit {
		evicted := s.facts[0]
		delete(s.seen, claimDedupeKey(evicted.Claim))
		copy(s.facts, s.facts[1:])
		s.facts = s.facts[:len(s.facts)-1]
		for key, idx := range s.seen {
			if idx > 0 {
				s.seen[key] = idx - 1
			}
		}
	}
	if s.logger != nil {
		s.logger.Printf("unconfirmed claim=%s %s %s confidence=%.3f provenance=%s",
			fact.Claim.Subject, fact.Claim.Predicate, fact.Claim.Object, fact.Confidence, fact.Provenance)
	}
	return ctx.Err()
}

func (s *unconfirmedStore) Snapshot() UnconfirmedSnapshot {
	if s == nil {
		return UnconfirmedSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UnconfirmedFactRecord, len(s.facts))
	for i, fact := range s.facts {
		out[i] = UnconfirmedFactRecord{
			At:         fact.At,
			Claim:      hashClaim(fact.Claim),
			Confidence: fact.Confidence,
			Provenance: fact.Provenance,
		}
	}
	return UnconfirmedSnapshot{Limit: s.limit, Facts: out}
}

func (s *unconfirmedStore) Count() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.facts)
}

func normalizeQuestionClaim(claim reasoning.Claim) reasoning.Claim {
	return reasoning.Claim{
		Subject:   strings.TrimSpace(claim.Subject),
		Predicate: strings.TrimSpace(claim.Predicate),
		Object:    strings.TrimSpace(claim.Object),
	}
}

func claimDedupeKey(claim reasoning.Claim) string {
	claim = normalizeQuestionClaim(claim)
	if claim.Subject == "" || claim.Predicate == "" || claim.Object == "" {
		return ""
	}
	return strings.ToLower(claim.Subject) + "\x00" + strings.ToLower(claim.Predicate) + "\x00" + strings.ToLower(claim.Object)
}

func hashClaim(claim reasoning.Claim) hashedClaim {
	claim = normalizeQuestionClaim(claim)
	hc := hashedClaim{
		SubjectHash: hashEntityName(claim.Subject),
		Predicate:   claim.Predicate,
		ObjectHash:  hashEntityName(claim.Object),
	}
	if cognitionLabelsEnabled.Load() {
		hc.Subject = claim.Subject
		hc.Object = claim.Object
	}
	return hc
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
