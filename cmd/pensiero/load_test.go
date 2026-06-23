package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

func TestLoadTrackerBeginEndAccountingConcurrent(t *testing.T) {
	tracker := NewLoadTracker(LoadTrackerConfig{})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			end := tracker.Begin()
			time.Sleep(time.Microsecond)
			end()
			end()
		}()
	}
	close(start)
	wg.Wait()
	if got := tracker.InFlight(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
	}
}

func TestLoadTrackerIdleTransitions(t *testing.T) {
	clock := newSchedulerFakeClock()
	tracker := NewLoadTracker(LoadTrackerConfig{
		QPSEWMAWindow:  time.Second,
		QPSIdleLimit:   100,
		LatencyWindow:  time.Second,
		LatencySLO:     time.Hour,
		LatencySamples: 8,
		Now:            clock.Now,
	})
	quietFor := 3 * time.Second
	if tracker.Idle(quietFor) {
		t.Fatal("tracker is idle before quiet period elapses")
	}
	end := tracker.Begin()
	if got := tracker.InFlight(); got != 1 {
		t.Fatalf("in-flight=%d, want 1", got)
	}
	clock.Advance(10 * time.Millisecond)
	end()
	if got := tracker.InFlight(); got != 0 {
		t.Fatalf("in-flight=%d, want 0", got)
	}
	if tracker.Idle(quietFor) {
		t.Fatal("tracker is idle immediately after a query ends")
	}
	clock.Advance(quietFor)
	if !tracker.Idle(quietFor) {
		t.Fatal("tracker did not become idle after quiet period")
	}
}

func TestLoadTrackerIdleRejectsHighQPSAndSlowP95(t *testing.T) {
	clock := newSchedulerFakeClock()
	qpsTracker := NewLoadTracker(LoadTrackerConfig{
		QPSEWMAWindow:  time.Second,
		QPSIdleLimit:   0.5,
		LatencyWindow:  time.Second,
		LatencySLO:     time.Hour,
		LatencySamples: 8,
		Now:            clock.Now,
	})
	for i := 0; i < 2; i++ {
		end := qpsTracker.Begin()
		end()
	}
	if qpsTracker.Idle(0) {
		t.Fatal("tracker is idle while EWMA QPS is above the limit")
	}
	clock.Advance(5 * time.Second)
	if !qpsTracker.Idle(0) {
		t.Fatal("tracker did not become idle after QPS decayed")
	}

	latencyTracker := NewLoadTracker(LoadTrackerConfig{
		QPSEWMAWindow:  time.Second,
		QPSIdleLimit:   100,
		LatencyWindow:  time.Second,
		LatencySLO:     10 * time.Millisecond,
		LatencySamples: 8,
		Now:            clock.Now,
	})
	end := latencyTracker.Begin()
	clock.Advance(20 * time.Millisecond)
	end()
	if latencyTracker.Idle(0) {
		t.Fatal("tracker is idle while recent p95 exceeds the SLO")
	}
	clock.Advance(2 * time.Second)
	if !latencyTracker.Idle(0) {
		t.Fatal("tracker did not become idle after slow latency aged out")
	}
}

func TestLoadTrackerIdlePassCancelsWhenQueryBegins(t *testing.T) {
	clock := newSchedulerFakeClock()
	tracker := NewLoadTracker(LoadTrackerConfig{
		QPSIdleLimit: 100,
		LatencySLO:   time.Hour,
		Now:          clock.Now,
	})
	passCtx, stop, ok := tracker.BeginIdlePass(context.Background(), 0)
	if !ok {
		t.Fatal("idle pass was not admitted")
	}
	defer stop()

	end := tracker.Begin()
	defer end()

	select {
	case <-passCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle pass was not canceled when query began")
	}
	if !errors.Is(passCtx.Err(), context.Canceled) {
		t.Fatalf("pass context error=%v, want context canceled", passCtx.Err())
	}
}

func TestTelemetryReasonerReleasesLoadOnPanic(t *testing.T) {
	tracker := NewLoadTracker(LoadTrackerConfig{})
	reasoner := newTelemetryReasonerWithLoad(panicReasoner{}, nil, tracker)
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected panic")
		}
		if got := tracker.InFlight(); got != 0 {
			t.Fatalf("in-flight=%d after panic, want 0", got)
		}
	}()
	_, _ = reasoner.Entails(context.Background(), reasoning.Claim{Subject: "A", Predicate: "R", Object: "B"})
}

type panicReasoner struct{}

func (panicReasoner) Derive(context.Context, reasoning.DeriveRequest) ([]reasoning.Proof, error) {
	panic("derive panic")
}

func (panicReasoner) Entails(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
	panic("entails panic")
}

func (panicReasoner) Contradicts(context.Context, reasoning.Claim) (bool, *reasoning.Proof, error) {
	panic("contradicts panic")
}

func (panicReasoner) Name() string {
	return "panic"
}

func TestTelemetryReasonerTracksAllReasoningMethods(t *testing.T) {
	tracker := NewLoadTracker(LoadTrackerConfig{})
	inner := &loadObservingReasoner{load: tracker}
	reasoner := newTelemetryReasonerWithLoad(inner, newQueryTelemetry(8), tracker)

	if _, err := reasoner.Derive(context.Background(), reasoning.DeriveRequest{Source: "A", Predicate: "R", Target: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reasoner.Entails(context.Background(), reasoning.Claim{Subject: "A", Predicate: "R", Object: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reasoner.Contradicts(context.Background(), reasoning.Claim{Subject: "A", Predicate: "R", Object: "B"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(inner.observed, ","); got != "Derive=1,Entails=1,Contradicts=1" {
		t.Fatalf("observed load=%s", got)
	}
	if got := tracker.InFlight(); got != 0 {
		t.Fatalf("in-flight=%d after calls, want 0", got)
	}
}

type loadObservingReasoner struct {
	load     *LoadTracker
	observed []string
}

func (r *loadObservingReasoner) Derive(context.Context, reasoning.DeriveRequest) ([]reasoning.Proof, error) {
	r.observed = append(r.observed, "Derive="+strconv.Itoa(r.load.InFlight()))
	return nil, nil
}

func (r *loadObservingReasoner) Entails(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
	r.observed = append(r.observed, "Entails="+strconv.Itoa(r.load.InFlight()))
	return reasoning.EntailResult{}, nil
}

func (r *loadObservingReasoner) Contradicts(context.Context, reasoning.Claim) (bool, *reasoning.Proof, error) {
	r.observed = append(r.observed, "Contradicts="+strconv.Itoa(r.load.InFlight()))
	return false, nil, nil
}

func (r *loadObservingReasoner) Name() string {
	return "load-observing"
}
