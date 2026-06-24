package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const defaultQueryTelemetryLimit = 4096

type queryCacheStatus string

const (
	queryCacheStatusHit          queryCacheStatus = "hit"
	queryCacheStatusMiss         queryCacheStatus = "miss"
	queryCacheStatusSingleflight queryCacheStatus = "singleflight"
)

type QueryEvent struct {
	Method      string  `json:"method"`
	KeyHash     string  `json:"key_hash"`
	Generation  string  `json:"generation_id"`
	Predicate   string  `json:"predicate"`
	DurationMS  int64   `json:"duration_ms"`
	DeadlineMS  int64   `json:"deadline_ms"`
	TimedOut    bool    `json:"timed_out"`
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	ProofHops   int     `json:"proof_hops"`
	ResultCount int     `json:"result_count"`
	CacheStatus string  `json:"cache_status"`
	ErrClass    string  `json:"err_class"`
	SubjectHash string  `json:"subject_hash,omitempty"`
	ObjectHash  string  `json:"object_hash,omitempty"`

	at         time.Time
	rawSubject string
	rawObject  string
}

type QueryHotKey struct {
	Method      string `json:"method"`
	KeyHash     string `json:"key_hash"`
	Generation  string `json:"generation_id"`
	Predicate   string `json:"predicate"`
	SubjectHash string `json:"subject_hash,omitempty"`
	ObjectHash  string `json:"object_hash,omitempty"`
	Count       int    `json:"count"`

	rawSubject string
	rawObject  string
}

type QueryUnresolvedClaim struct {
	Claim   reasoning.Claim `json:"claim"`
	Verdict string          `json:"verdict"`
	Count   int             `json:"count"`
}

type QueryTelemetrySnapshot struct {
	StartedAt     time.Time        `json:"started_at"`
	Total         int64            `json:"total"`
	WindowEvents  int              `json:"window_events"`
	RecentCount   int64            `json:"recent_count"`
	QPS1m         float64          `json:"qps_1m"`
	CacheHits     int64            `json:"cache_hits"`
	CacheMisses   int64            `json:"cache_misses"`
	Singleflight  int64            `json:"singleflight"`
	CacheHitRatio float64          `json:"cache_hit_ratio"`
	Timeouts      int64            `json:"timeouts"`
	Errors        int64            `json:"errors"`
	ByMethod      map[string]int64 `json:"by_method"`
	ByCacheStatus map[string]int64 `json:"by_cache_status"`
	ByVerdict     map[string]int64 `json:"by_verdict"`
	ByErrClass    map[string]int64 `json:"by_err_class"`
}

type queryTelemetry struct {
	mu            sync.Mutex
	startedAt     time.Time
	events        []QueryEvent
	next          int
	full          bool
	total         int64
	cacheHits     int64
	cacheMisses   int64
	singleflight  int64
	timeouts      int64
	errors        int64
	byMethod      map[string]int64
	byCacheStatus map[string]int64
	byVerdict     map[string]int64
	byErrClass    map[string]int64
}

type telemetryReasoner struct {
	inner     reasoning.Reasoner
	telemetry *queryTelemetry
	load      *LoadTracker
}

type queryProbe struct {
	mu          sync.Mutex
	key         proofCacheKey
	hasKey      bool
	cacheStatus queryCacheStatus
}

type queryProbeContextKey struct{}

func newQueryTelemetry(limit int) *queryTelemetry {
	if limit <= 0 {
		limit = defaultQueryTelemetryLimit
	}
	return &queryTelemetry{
		startedAt:     time.Now(),
		events:        make([]QueryEvent, 0, limit),
		byMethod:      map[string]int64{},
		byCacheStatus: map[string]int64{},
		byVerdict:     map[string]int64{},
		byErrClass:    map[string]int64{},
	}
}

func newTelemetryReasoner(inner reasoning.Reasoner, telemetry *queryTelemetry) reasoning.Reasoner {
	return newTelemetryReasonerWithLoad(inner, telemetry, nil)
}

func newTelemetryReasonerWithLoad(inner reasoning.Reasoner, telemetry *queryTelemetry, load *LoadTracker) reasoning.Reasoner {
	if telemetry == nil && load == nil {
		return inner
	}
	return &telemetryReasoner{
		inner:     inner,
		telemetry: telemetry,
		load:      load,
	}
}

func (r *telemetryReasoner) Derive(ctx context.Context, req reasoning.DeriveRequest) ([]reasoning.Proof, error) {
	endLoad := r.beginLoad()
	defer endLoad()
	start, deadlineMS := queryStart(ctx)
	probe := &queryProbe{}
	ctx = context.WithValue(ctx, queryProbeContextKey{}, probe)
	proofs, err := r.inner.Derive(ctx, req)
	key, status := probe.snapshot()
	event := baseQueryEvent(ctx, start, deadlineMS, "Derive", key, status, err)
	event.Predicate = firstNonEmpty(key.predicate, strings.TrimSpace(req.Predicate))
	event.rawSubject = firstNonEmpty(key.subject, strings.TrimSpace(req.Source))
	event.rawObject = firstNonEmpty(key.object, strings.TrimSpace(req.Target))
	event.SubjectHash = hashEntityName(event.rawSubject)
	event.ObjectHash = hashEntityName(event.rawObject)
	event.ResultCount = len(proofs)
	if len(proofs) > 0 {
		event.Verdict = "derived"
		event.Confidence = proofs[0].Confidence
		event.ProofHops = proofs[0].Hops
	} else if err == nil {
		event.Verdict = string(reasoning.VerdictUnsupported)
	}
	r.telemetry.Record(event)
	return proofs, err
}

func (r *telemetryReasoner) Entails(ctx context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
	endLoad := r.beginLoad()
	defer endLoad()
	start, deadlineMS := queryStart(ctx)
	probe := &queryProbe{}
	ctx = context.WithValue(ctx, queryProbeContextKey{}, probe)
	result, err := r.inner.Entails(ctx, claim)
	key, status := probe.snapshot()
	event := baseQueryEvent(ctx, start, deadlineMS, "Entails", key, status, err)
	event.Predicate = firstNonEmpty(key.predicate, strings.TrimSpace(claim.Predicate))
	event.rawSubject = firstNonEmpty(key.subject, strings.TrimSpace(claim.Subject))
	event.rawObject = firstNonEmpty(key.object, strings.TrimSpace(claim.Object))
	event.SubjectHash = hashEntityName(event.rawSubject)
	event.ObjectHash = hashEntityName(event.rawObject)
	event.Verdict = string(result.Verdict)
	event.Confidence = result.Confidence
	event.ResultCount = len(result.All)
	if result.Best != nil {
		event.ProofHops = result.Best.Hops
		if event.ResultCount == 0 {
			event.ResultCount = 1
		}
	}
	r.telemetry.Record(event)
	return result, err
}

func (r *telemetryReasoner) Contradicts(ctx context.Context, claim reasoning.Claim) (bool, *reasoning.Proof, error) {
	endLoad := r.beginLoad()
	defer endLoad()
	start, deadlineMS := queryStart(ctx)
	probe := &queryProbe{}
	ctx = context.WithValue(ctx, queryProbeContextKey{}, probe)
	ok, proof, err := r.inner.Contradicts(ctx, claim)
	key, status := probe.snapshot()
	event := baseQueryEvent(ctx, start, deadlineMS, "Contradicts", key, status, err)
	event.Predicate = firstNonEmpty(key.predicate, strings.TrimSpace(claim.Predicate))
	event.rawSubject = firstNonEmpty(key.subject, strings.TrimSpace(claim.Subject))
	event.rawObject = firstNonEmpty(key.object, strings.TrimSpace(claim.Object))
	event.SubjectHash = hashEntityName(event.rawSubject)
	event.ObjectHash = hashEntityName(event.rawObject)
	if ok {
		event.Verdict = string(reasoning.VerdictContradicted)
		event.ResultCount = 1
	}
	if proof != nil {
		event.Confidence = proof.Confidence
		event.ProofHops = proof.Hops
	}
	if !ok && err == nil {
		event.Verdict = string(reasoning.VerdictUnsupported)
	}
	r.telemetry.Record(event)
	return ok, proof, err
}

func (r *telemetryReasoner) Name() string {
	if r == nil || r.inner == nil {
		return "telemetry"
	}
	return r.inner.Name()
}

func (r *telemetryReasoner) beginLoad() func() {
	if r == nil || r.load == nil {
		return func() {}
	}
	return r.load.Begin()
}

func (t *queryTelemetry) Record(event QueryEvent) {
	if t == nil {
		return
	}
	if event.at.IsZero() {
		event.at = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.startedAt.IsZero() {
		t.startedAt = event.at
	}
	t.total++
	t.byMethod[event.Method]++
	if event.CacheStatus != "" {
		t.byCacheStatus[event.CacheStatus]++
	}
	switch queryCacheStatus(event.CacheStatus) {
	case queryCacheStatusHit:
		t.cacheHits++
	case queryCacheStatusMiss:
		t.cacheMisses++
	case queryCacheStatusSingleflight:
		t.singleflight++
	}
	if event.TimedOut {
		t.timeouts++
	}
	if event.ErrClass != "" {
		t.errors++
		t.byErrClass[event.ErrClass]++
	}
	if event.Verdict != "" {
		t.byVerdict[event.Verdict]++
	}
	if len(t.events) < cap(t.events) {
		t.events = append(t.events, event)
		return
	}
	if cap(t.events) == 0 {
		return
	}
	t.events[t.next] = event
	t.next = (t.next + 1) % cap(t.events)
	t.full = true
}

func (t *queryTelemetry) HotKeys(n int) []QueryHotKey {
	if t == nil || n <= 0 {
		return nil
	}
	t.mu.Lock()
	events := t.retainedEventsLocked()
	t.mu.Unlock()
	type aggregate struct {
		event QueryEvent
		count int
	}
	byKey := map[string]*aggregate{}
	for _, event := range events {
		if event.KeyHash == "" {
			continue
		}
		agg := byKey[event.KeyHash]
		if agg == nil {
			agg = &aggregate{event: event}
			byKey[event.KeyHash] = agg
		}
		agg.count++
	}
	aggregates := make([]*aggregate, 0, len(byKey))
	for _, agg := range byKey {
		aggregates = append(aggregates, agg)
	}
	sort.Slice(aggregates, func(i, j int) bool {
		if aggregates[i].count != aggregates[j].count {
			return aggregates[i].count > aggregates[j].count
		}
		return aggregates[i].event.KeyHash < aggregates[j].event.KeyHash
	})
	if len(aggregates) > n {
		aggregates = aggregates[:n]
	}
	out := make([]QueryHotKey, 0, len(aggregates))
	for _, agg := range aggregates {
		event := agg.event
		out = append(out, QueryHotKey{
			Method:      event.Method,
			KeyHash:     event.KeyHash,
			Generation:  event.Generation,
			Predicate:   event.Predicate,
			SubjectHash: event.SubjectHash,
			ObjectHash:  event.ObjectHash,
			Count:       agg.count,
			rawSubject:  event.rawSubject,
			rawObject:   event.rawObject,
		})
	}
	return out
}

func (t *queryTelemetry) UnresolvedClaims(n int) []QueryUnresolvedClaim {
	if t == nil || n <= 0 {
		return nil
	}
	t.mu.Lock()
	events := t.retainedEventsLocked()
	t.mu.Unlock()
	type aggregate struct {
		claim   reasoning.Claim
		verdict string
		count   int
	}
	byClaim := map[string]*aggregate{}
	for _, event := range events {
		switch reasoning.Verdict(event.Verdict) {
		case reasoning.VerdictUnsupported, reasoning.VerdictContradicted:
		default:
			continue
		}
		claim := reasoning.Claim{
			Subject:   strings.TrimSpace(event.rawSubject),
			Predicate: strings.TrimSpace(event.Predicate),
			Object:    strings.TrimSpace(event.rawObject),
		}
		key := claimDedupeKey(claim)
		if key == "" {
			continue
		}
		agg := byClaim[key]
		if agg == nil {
			agg = &aggregate{claim: claim, verdict: event.Verdict}
			byClaim[key] = agg
		}
		agg.count++
	}
	aggregates := make([]*aggregate, 0, len(byClaim))
	for _, agg := range byClaim {
		aggregates = append(aggregates, agg)
	}
	sort.Slice(aggregates, func(i, j int) bool {
		if aggregates[i].count != aggregates[j].count {
			return aggregates[i].count > aggregates[j].count
		}
		return claimDedupeKey(aggregates[i].claim) < claimDedupeKey(aggregates[j].claim)
	})
	if len(aggregates) > n {
		aggregates = aggregates[:n]
	}
	out := make([]QueryUnresolvedClaim, 0, len(aggregates))
	for _, agg := range aggregates {
		out = append(out, QueryUnresolvedClaim{
			Claim:   agg.claim,
			Verdict: agg.verdict,
			Count:   agg.count,
		})
	}
	return out
}

func (t *queryTelemetry) Snapshot() QueryTelemetrySnapshot {
	if t == nil {
		return QueryTelemetrySnapshot{}
	}
	now := time.Now()
	t.mu.Lock()
	events := t.retainedEventsLocked()
	snapshot := QueryTelemetrySnapshot{
		StartedAt:     t.startedAt,
		Total:         t.total,
		WindowEvents:  len(events),
		CacheHits:     t.cacheHits,
		CacheMisses:   t.cacheMisses,
		Singleflight:  t.singleflight,
		Timeouts:      t.timeouts,
		Errors:        t.errors,
		ByMethod:      cloneCounters(t.byMethod),
		ByCacheStatus: cloneCounters(t.byCacheStatus),
		ByVerdict:     cloneCounters(t.byVerdict),
		ByErrClass:    cloneCounters(t.byErrClass),
	}
	t.mu.Unlock()
	for _, event := range events {
		if now.Sub(event.at) <= time.Minute {
			snapshot.RecentCount++
		}
	}
	snapshot.QPS1m = float64(snapshot.RecentCount) / 60.0
	cacheLookups := snapshot.CacheHits + snapshot.CacheMisses + snapshot.Singleflight
	if cacheLookups > 0 {
		snapshot.CacheHitRatio = float64(snapshot.CacheHits) / float64(cacheLookups)
	}
	return snapshot
}

func (t *queryTelemetry) retainedEventsLocked() []QueryEvent {
	if len(t.events) == 0 {
		return nil
	}
	if !t.full {
		out := make([]QueryEvent, len(t.events))
		copy(out, t.events)
		return out
	}
	out := make([]QueryEvent, 0, len(t.events))
	out = append(out, t.events[t.next:]...)
	out = append(out, t.events[:t.next]...)
	return out
}

func setQueryCacheKey(ctx context.Context, key proofCacheKey) {
	probe := queryProbeFromContext(ctx)
	if probe == nil {
		return
	}
	probe.mu.Lock()
	probe.key = key
	probe.hasKey = true
	probe.mu.Unlock()
}

func setQueryCacheStatus(ctx context.Context, status queryCacheStatus) {
	probe := queryProbeFromContext(ctx)
	if probe == nil {
		return
	}
	probe.mu.Lock()
	probe.cacheStatus = status
	probe.mu.Unlock()
}

func queryProbeFromContext(ctx context.Context) *queryProbe {
	probe, _ := ctx.Value(queryProbeContextKey{}).(*queryProbe)
	return probe
}

func (p *queryProbe) snapshot() (proofCacheKey, queryCacheStatus) {
	if p == nil {
		return proofCacheKey{}, ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.hasKey {
		return proofCacheKey{}, p.cacheStatus
	}
	return p.key, p.cacheStatus
}

func queryStart(ctx context.Context) (time.Time, int64) {
	start := time.Now()
	deadline, ok := ctx.Deadline()
	if !ok {
		return start, 0
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	return start, remaining.Milliseconds()
}

func baseQueryEvent(ctx context.Context, start time.Time, deadlineMS int64, method string, key proofCacheKey, status queryCacheStatus, err error) QueryEvent {
	errClass := classifyQueryError(ctx, err)
	return QueryEvent{
		Method:      method,
		KeyHash:     key.hash,
		Generation:  key.generation,
		DurationMS:  time.Since(start).Milliseconds(),
		DeadlineMS:  deadlineMS,
		TimedOut:    errClass == "deadline",
		CacheStatus: string(status),
		ErrClass:    errClass,
		at:          time.Now(),
	}
}

func classifyQueryError(ctx context.Context, err error) string {
	switch {
	case err == nil && ctx.Err() == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return "canceled"
	case errors.Is(err, errNoGeneration):
		return "no_generation"
	case err != nil:
		return "error"
	default:
		return ""
	}
}

func hashEntityName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

func cloneCounters(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
