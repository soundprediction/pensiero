package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soundprediction/pensiero/pkg/generalization"
)

func TestIGLSchedulerRunsRunOnceWhenIdle(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{Now: clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	runner := &schedulerFakeRunner{
		run: func(context.Context) (generalization.PassResult, error) {
			cancel()
			return publishedPassResult(), nil
		},
	}
	scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
		BaseInterval:       time.Second,
		QuietFor:           0,
		MinPublishInterval: 0,
		Now:                clock.Now,
		Jitter:             identityJitter,
	})
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := runner.Count(); got != 1 {
		t.Fatalf("RunOnce calls=%d, want 1", got)
	}
}

func TestIGLSchedulerSkipsRunOnceWhenNotLeader(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{Now: clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	sleeper := newSchedulerRecordingSleep(clock, cancel, 1)
	leader := newSchedulerFakeLeader()
	runner := &schedulerFakeRunner{}
	scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
		BaseInterval:       time.Second,
		QuietFor:           0,
		MinPublishInterval: 0,
		Leader:             leader,
		LeaderScopes:       []string{"alpha"},
		Now:                clock.Now,
		Sleep:              sleeper.Sleep,
		Jitter:             identityJitter,
	})
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := runner.Count(); got != 0 {
		t.Fatalf("RunOnce calls=%d, want 0", got)
	}
	if got := leader.TryCount("alpha"); got != 1 {
		t.Fatalf("leader TryAcquire calls=%d, want 1", got)
	}
}

func TestIGLSchedulerRunsRunOnceWhenLeaderHolds(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{Now: clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	leader := newSchedulerFakeLeader()
	leader.SetHeld("alpha", true)
	runner := &schedulerFakeRunner{
		run: func(context.Context) (generalization.PassResult, error) {
			cancel()
			return publishedPassResult(), nil
		},
	}
	scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
		BaseInterval:       time.Second,
		QuietFor:           0,
		MinPublishInterval: 0,
		Leader:             leader,
		LeaderScopes:       []string{"alpha"},
		Now:                clock.Now,
		Jitter:             identityJitter,
	})
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := runner.Count(); got != 1 {
		t.Fatalf("RunOnce calls=%d, want 1", got)
	}
	if got := leader.TryCount("alpha"); got != 0 {
		t.Fatalf("leader TryAcquire calls=%d, want 0 for already-held scope", got)
	}
}

func TestIGLSchedulerReattemptsLeadershipBeforePass(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{Now: clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	leader := newSchedulerFakeLeader()
	leader.SetAcquire("alpha", true)
	runner := &schedulerFakeRunner{
		run: func(context.Context) (generalization.PassResult, error) {
			cancel()
			return publishedPassResult(), nil
		},
	}
	scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
		BaseInterval:       time.Second,
		QuietFor:           0,
		MinPublishInterval: 0,
		Leader:             leader,
		LeaderScopes:       []string{"alpha"},
		Now:                clock.Now,
		Jitter:             identityJitter,
	})
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := runner.Count(); got != 1 {
		t.Fatalf("RunOnce calls=%d, want 1", got)
	}
	if got := leader.TryCount("alpha"); got != 1 {
		t.Fatalf("leader TryAcquire calls=%d, want 1", got)
	}
	if !leader.Holds("alpha") {
		t.Fatal("leader did not hold scope after successful re-attempt")
	}
}

func TestIGLSchedulerBacksOffWhenNotIdle(t *testing.T) {
	t.Run("in flight", func(t *testing.T) {
		clock := newSchedulerFakeClock()
		load := NewLoadTracker(LoadTrackerConfig{Now: clock.Now})
		end := load.Begin()
		defer end()
		ctx, cancel := context.WithCancel(context.Background())
		sleeper := newSchedulerRecordingSleep(clock, cancel, 1)
		runner := &schedulerFakeRunner{}
		scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
			BaseInterval:       time.Second,
			QuietFor:           0,
			MinPublishInterval: 0,
			Now:                clock.Now,
			Sleep:              sleeper.Sleep,
			Jitter:             identityJitter,
		})
		if err := scheduler.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if got := runner.Count(); got != 0 {
			t.Fatalf("RunOnce calls=%d, want 0", got)
		}
		if delays := sleeper.Delays(); len(delays) != 1 || delays[0] != time.Second {
			t.Fatalf("backoff delays=%v, want [1s]", delays)
		}
	})

	t.Run("high qps", func(t *testing.T) {
		clock := newSchedulerFakeClock()
		load := NewLoadTracker(LoadTrackerConfig{
			QPSEWMAWindow: time.Second,
			QPSIdleLimit:  0.5,
			LatencySLO:    time.Hour,
			Now:           clock.Now,
		})
		for i := 0; i < 2; i++ {
			end := load.Begin()
			end()
		}
		ctx, cancel := context.WithCancel(context.Background())
		sleeper := newSchedulerRecordingSleep(clock, cancel, 1)
		runner := &schedulerFakeRunner{}
		scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
			BaseInterval:       time.Second,
			QuietFor:           0,
			MinPublishInterval: 0,
			Now:                clock.Now,
			Sleep:              sleeper.Sleep,
			Jitter:             identityJitter,
		})
		if err := scheduler.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if got := runner.Count(); got != 0 {
			t.Fatalf("RunOnce calls=%d, want 0", got)
		}
		if delays := sleeper.Delays(); len(delays) != 1 || delays[0] != time.Second {
			t.Fatalf("backoff delays=%v, want [1s]", delays)
		}
	})
}

func TestIGLSchedulerCancelsPassWhenLoadAppears(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{
		Now:       clock.Now,
		YieldPoll: time.Millisecond,
	})
	loadStarted := make(chan struct{})
	runnerStarted := make(chan struct{})
	source := loadAwareSource{
		inner: schedulerQuerySource{},
		load:  load,
	}
	runner := &schedulerFakeRunner{
		run: func(ctx context.Context) (generalization.PassResult, error) {
			close(runnerStarted)
			select {
			case <-loadStarted:
			case <-ctx.Done():
				return generalization.PassResult{}, ctx.Err()
			}
			_, err := source.Query(ctx, "query", nil)
			if err != nil {
				return generalization.PassResult{}, err
			}
			return publishedPassResult(), nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	sleeper := newSchedulerRecordingSleep(clock, cancel, 1)
	scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
		BaseInterval:       time.Second,
		QuietFor:           0,
		MinPublishInterval: 0,
		LoadPoll:           time.Millisecond,
		Now:                clock.Now,
		Sleep:              sleeper.Sleep,
		Jitter:             identityJitter,
	})
	done := make(chan error, 1)
	go func() {
		done <- scheduler.Run(ctx)
	}()
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("RunOnce did not start")
	}
	end := load.Begin()
	close(loadStarted)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	end()
	if !errors.Is(runner.LastError(), context.Canceled) {
		t.Fatalf("RunOnce error=%v, want context canceled", runner.LastError())
	}
	if passPublished(runner.LastResult()) {
		t.Fatalf("pass published despite cancellation: %#v", runner.LastResult())
	}
}

func TestIGLSchedulerCancelsPassImmediatelyWhenInFlightRises(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{
		Now:       clock.Now,
		YieldPoll: time.Hour,
	})
	runnerStarted := make(chan struct{})
	runner := &schedulerFakeRunner{
		run: func(ctx context.Context) (generalization.PassResult, error) {
			close(runnerStarted)
			<-ctx.Done()
			return generalization.PassResult{}, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	sleeper := newSchedulerRecordingSleep(clock, cancel, 1)
	scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
		BaseInterval:       time.Second,
		QuietFor:           0,
		MinPublishInterval: 0,
		LoadPoll:           time.Hour,
		Now:                clock.Now,
		Sleep:              sleeper.Sleep,
		Jitter:             identityJitter,
	})
	done := make(chan error, 1)
	go func() {
		done <- scheduler.Run(ctx)
	}()
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("RunOnce did not start")
	}
	end := load.Begin()
	defer end()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	if !errors.Is(runner.LastError(), context.Canceled) {
		t.Fatalf("RunOnce error=%v, want context canceled", runner.LastError())
	}
	if delays := sleeper.Delays(); len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("backoff delays=%v, want [1s]", delays)
	}
}

func TestIGLSchedulerPublishRateLimitPreventsBackToBackPublishes(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{Now: clock.Now})
	ctx, cancel := context.WithCancel(context.Background())
	sleeper := newSchedulerRecordingSleep(clock, cancel, 2)
	runner := &schedulerFakeRunner{
		run: func(context.Context) (generalization.PassResult, error) {
			return publishedPassResult(), nil
		},
	}
	scheduler := NewIGLScheduler(runner, load, IGLSchedulerConfig{
		BaseInterval:       time.Second,
		QuietFor:           0,
		MinPublishInterval: 30 * time.Second,
		Now:                clock.Now,
		Sleep:              sleeper.Sleep,
		Jitter:             identityJitter,
	})
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := runner.Count(); got != 1 {
		t.Fatalf("RunOnce calls=%d, want 1", got)
	}
	if delays := sleeper.Delays(); len(delays) != 2 || delays[0] != time.Second || delays[1] != time.Second {
		t.Fatalf("delays=%v, want [1s 1s]", delays)
	}
}

type schedulerFakeRunner struct {
	calls  atomic.Int64
	mu     sync.Mutex
	result generalization.PassResult
	err    error
	run    func(context.Context) (generalization.PassResult, error)
}

func (r *schedulerFakeRunner) RunOnce(ctx context.Context) (generalization.PassResult, error) {
	r.calls.Add(1)
	var result generalization.PassResult
	var err error
	if r.run != nil {
		result, err = r.run(ctx)
	}
	r.mu.Lock()
	r.result = result
	r.err = err
	r.mu.Unlock()
	return result, err
}

func (r *schedulerFakeRunner) Count() int64 {
	return r.calls.Load()
}

func (r *schedulerFakeRunner) LastResult() generalization.PassResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

func (r *schedulerFakeRunner) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

type schedulerQuerySource struct{}

func (schedulerQuerySource) Query(ctx context.Context, _ string, _ map[string]any) ([]map[string]any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type schedulerFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSchedulerFakeClock() *schedulerFakeClock {
	return &schedulerFakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *schedulerFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *schedulerFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type schedulerRecordingSleep struct {
	mu       sync.Mutex
	clock    *schedulerFakeClock
	cancel   context.CancelFunc
	cancelAt int
	delays   []time.Duration
}

func newSchedulerRecordingSleep(clock *schedulerFakeClock, cancel context.CancelFunc, cancelAt int) *schedulerRecordingSleep {
	return &schedulerRecordingSleep{clock: clock, cancel: cancel, cancelAt: cancelAt}
}

func (s *schedulerRecordingSleep) Sleep(ctx context.Context, delay time.Duration) error {
	s.mu.Lock()
	s.delays = append(s.delays, delay)
	count := len(s.delays)
	s.mu.Unlock()
	if s.clock != nil {
		s.clock.Advance(delay)
	}
	if s.cancel != nil && count >= s.cancelAt {
		s.cancel()
		return ctx.Err()
	}
	return ctx.Err()
}

func (s *schedulerRecordingSleep) Delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.delays))
	copy(out, s.delays)
	return out
}

func publishedPassResult() generalization.PassResult {
	return generalization.PassResult{
		Scopes: []generalization.ScopeResult{{Scope: "alpha", Published: true}},
	}
}

func identityJitter(delay time.Duration) time.Duration {
	return delay
}

type schedulerFakeLeader struct {
	mu       sync.Mutex
	held     map[string]bool
	acquire  map[string]bool
	tryCount map[string]int
}

func newSchedulerFakeLeader() *schedulerFakeLeader {
	return &schedulerFakeLeader{
		held:     map[string]bool{},
		acquire:  map[string]bool{},
		tryCount: map[string]int{},
	}
}

func (l *schedulerFakeLeader) TryAcquire(scope string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tryCount[scope]++
	if !l.acquire[scope] {
		return false, nil
	}
	l.held[scope] = true
	return true, nil
}

func (l *schedulerFakeLeader) Holds(scope string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held[scope]
}

func (l *schedulerFakeLeader) Release(scope string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.held, scope)
	return nil
}

func (l *schedulerFakeLeader) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = map[string]bool{}
	return nil
}

func (l *schedulerFakeLeader) SetHeld(scope string, held bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held[scope] = held
}

func (l *schedulerFakeLeader) SetAcquire(scope string, acquire bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquire[scope] = acquire
}

func (l *schedulerFakeLeader) TryCount(scope string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tryCount[scope]
}
