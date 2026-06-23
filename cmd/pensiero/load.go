package main

import (
	"context"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const (
	defaultLoadQPSEWMAWindow  = 10 * time.Second
	defaultLoadQPSIdleLimit   = 0.2
	defaultLoadLatencyWindow  = time.Minute
	defaultLoadLatencySLO     = 750 * time.Millisecond
	defaultLoadLatencySamples = 128
	defaultLoadYieldPoll      = 10 * time.Millisecond
)

type LoadTrackerConfig struct {
	QPSEWMAWindow  time.Duration
	QPSIdleLimit   float64
	LatencyWindow  time.Duration
	LatencySLO     time.Duration
	LatencySamples int
	YieldPoll      time.Duration
	Now            func() time.Time
}

type LoadTracker struct {
	inFlight  atomic.Int64
	zeroSince atomic.Int64

	passMu           sync.Mutex
	activePassID     uint64
	activePassCancel context.CancelFunc

	mu          sync.Mutex
	now         func() time.Time
	qpsWindow   time.Duration
	qpsLimit    float64
	latencyTTL  time.Duration
	latencySLO  time.Duration
	yieldPoll   time.Duration
	qpsEWMA     float64
	qpsUpdated  time.Time
	latencies   []loadLatencySample
	latencyNext int
	latencyFull bool
}

type loadLatencySample struct {
	at       time.Time
	duration time.Duration
}

func NewLoadTracker(cfg LoadTrackerConfig) *LoadTracker {
	if cfg.QPSEWMAWindow <= 0 {
		cfg.QPSEWMAWindow = defaultLoadQPSEWMAWindow
	}
	if cfg.QPSIdleLimit <= 0 {
		cfg.QPSIdleLimit = defaultLoadQPSIdleLimit
	}
	if cfg.LatencyWindow <= 0 {
		cfg.LatencyWindow = defaultLoadLatencyWindow
	}
	if cfg.LatencySLO <= 0 {
		cfg.LatencySLO = defaultLoadLatencySLO
	}
	if cfg.LatencySamples <= 0 {
		cfg.LatencySamples = defaultLoadLatencySamples
	}
	if cfg.YieldPoll <= 0 {
		cfg.YieldPoll = defaultLoadYieldPoll
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	tracker := &LoadTracker{
		now:        cfg.Now,
		qpsWindow:  cfg.QPSEWMAWindow,
		qpsLimit:   cfg.QPSIdleLimit,
		latencyTTL: cfg.LatencyWindow,
		latencySLO: cfg.LatencySLO,
		yieldPoll:  cfg.YieldPoll,
		latencies:  make([]loadLatencySample, 0, cfg.LatencySamples),
	}
	tracker.zeroSince.Store(cfg.Now().UnixNano())
	return tracker
}

func (l *LoadTracker) Begin() func() {
	if l == nil {
		return func() {}
	}
	start := l.nowTime()
	var cancelPass context.CancelFunc
	l.passMu.Lock()
	if l.inFlight.Add(1) == 1 {
		l.zeroSince.Store(0)
	}
	cancelPass = l.activePassCancel
	l.passMu.Unlock()
	if cancelPass != nil {
		cancelPass()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			l.end(start)
		})
	}
}

func (l *LoadTracker) InFlight() int {
	if l == nil {
		return 0
	}
	n := l.inFlight.Load()
	if n <= 0 {
		return 0
	}
	return int(n)
}

func (l *LoadTracker) Idle(quietFor time.Duration) bool {
	if l == nil {
		return true
	}
	if quietFor < 0 {
		quietFor = 0
	}
	now := l.nowTime()
	if l.InFlight() != 0 {
		return false
	}
	zeroSince := l.zeroSince.Load()
	if zeroSince == 0 {
		return false
	}
	if now.Sub(time.Unix(0, zeroSince)) < quietFor {
		return false
	}
	qps, p95 := l.queryLoad(now)
	if qps >= l.qpsLimit {
		return false
	}
	return p95 == 0 || p95 < l.latencySLO
}

func (l *LoadTracker) BeginIdlePass(ctx context.Context, quietFor time.Duration) (context.Context, func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	passCtx, cancel := context.WithCancel(ctx)
	if l == nil {
		return passCtx, cancel, true
	}
	l.passMu.Lock()
	if !l.Idle(quietFor) {
		l.passMu.Unlock()
		cancel()
		return passCtx, func() {}, false
	}
	l.activePassID++
	passID := l.activePassID
	l.activePassCancel = cancel
	l.passMu.Unlock()

	stop := func() {
		l.passMu.Lock()
		if l.activePassID == passID {
			l.activePassCancel = nil
		}
		l.passMu.Unlock()
		cancel()
	}
	return passCtx, stop, true
}

func (l *LoadTracker) Yield(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil {
		return ctx.Err()
	}
	for l.loaded() {
		timer := time.NewTimer(l.yieldPoll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (l *LoadTracker) end(start time.Time) {
	now := l.nowTime()
	if now.Before(start) {
		now = start
	}
	duration := now.Sub(start)
	if n := l.inFlight.Add(-1); n <= 0 {
		if n < 0 {
			l.inFlight.Store(0)
		}
		l.storeZeroSinceMax(now)
	}
	l.recordQuery(now, duration)
}

func (l *LoadTracker) storeZeroSinceMax(now time.Time) {
	next := now.UnixNano()
	for {
		current := l.zeroSince.Load()
		if current >= next && current != 0 {
			return
		}
		if l.zeroSince.CompareAndSwap(current, next) {
			return
		}
	}
}

func (l *LoadTracker) loaded() bool {
	if l == nil {
		return false
	}
	if l.InFlight() != 0 {
		return true
	}
	qps, p95 := l.queryLoad(l.nowTime())
	if qps >= l.qpsLimit {
		return true
	}
	return p95 != 0 && p95 >= l.latencySLO
}

func (l *LoadTracker) recordQuery(now time.Time, duration time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.decayQPSLocked(now)
	l.qpsEWMA += 1.0 / l.qpsWindow.Seconds()
	l.qpsUpdated = now
	sample := loadLatencySample{at: now, duration: duration}
	if len(l.latencies) < cap(l.latencies) {
		l.latencies = append(l.latencies, sample)
		return
	}
	if cap(l.latencies) == 0 {
		return
	}
	l.latencies[l.latencyNext] = sample
	l.latencyNext = (l.latencyNext + 1) % cap(l.latencies)
	l.latencyFull = true
}

func (l *LoadTracker) queryLoad(now time.Time) (float64, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	qps := l.qpsEWMA
	if !l.qpsUpdated.IsZero() {
		elapsed := now.Sub(l.qpsUpdated)
		if elapsed > 0 {
			qps *= math.Exp(-elapsed.Seconds() / l.qpsWindow.Seconds())
		}
	}
	return qps, l.recentP95Locked(now)
}

func (l *LoadTracker) decayQPSLocked(now time.Time) {
	if l.qpsUpdated.IsZero() {
		return
	}
	elapsed := now.Sub(l.qpsUpdated)
	if elapsed <= 0 {
		return
	}
	l.qpsEWMA *= math.Exp(-elapsed.Seconds() / l.qpsWindow.Seconds())
}

func (l *LoadTracker) recentP95Locked(now time.Time) time.Duration {
	if len(l.latencies) == 0 {
		return 0
	}
	cutoff := now.Add(-l.latencyTTL)
	values := make([]time.Duration, 0, len(l.latencies))
	for _, sample := range l.retainedLatencySamplesLocked() {
		if sample.at.IsZero() || sample.at.Before(cutoff) {
			continue
		}
		values = append(values, sample.duration)
	}
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	idx := int(math.Ceil(float64(len(values))*0.95)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

func (l *LoadTracker) retainedLatencySamplesLocked() []loadLatencySample {
	if len(l.latencies) == 0 {
		return nil
	}
	if !l.latencyFull {
		out := make([]loadLatencySample, len(l.latencies))
		copy(out, l.latencies)
		return out
	}
	out := make([]loadLatencySample, 0, len(l.latencies))
	out = append(out, l.latencies[l.latencyNext:]...)
	out = append(out, l.latencies[:l.latencyNext]...)
	return out
}

func (l *LoadTracker) nowTime() time.Time {
	if l == nil || l.now == nil {
		return time.Now()
	}
	return l.now()
}

type loadAwareSource struct {
	inner reasoning.GraphQuerier
	load  *LoadTracker
}

func (s loadAwareSource) Query(ctx context.Context, query string, params map[string]any) ([]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.load != nil {
		if err := s.load.Yield(ctx); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.inner.Query(ctx, query, params)
}
