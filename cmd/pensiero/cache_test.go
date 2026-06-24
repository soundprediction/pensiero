package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

func TestProofCacheEntailsHitDeepCopy(t *testing.T) {
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
	claim := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}

	first, err := cache.Entails(context.Background(), claim)
	if err != nil {
		t.Fatalf("first Entails returned error: %v", err)
	}
	second, err := cache.Entails(context.Background(), claim)
	if err != nil {
		t.Fatalf("second Entails returned error: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("inner calls=%d, want 1", got)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached result differs from first result:\nfirst=%#v\nsecond=%#v", first, second)
	}
	first.Best.Steps[0].Predicate = "mutated"
	first.All[0].Steps[0].Predicate = "mutated"
	if second.Best.Steps[0].Predicate == "mutated" {
		t.Fatal("second Best proof shares mutable steps with first result")
	}
	if second.All[0].Steps[0].Predicate == "mutated" {
		t.Fatal("second All proof shares mutable steps with first result")
	}
	third, err := cache.Entails(context.Background(), claim)
	if err != nil {
		t.Fatalf("third Entails returned error: %v", err)
	}
	if third.Best.Steps[0].Predicate == "mutated" || third.All[0].Steps[0].Predicate == "mutated" {
		t.Fatal("cached proof was mutated through a returned result")
	}
}

func TestProofCacheGenerationSwapInvalidates(t *testing.T) {
	var calls1, calls2 int64
	reg := reasoning.DefaultGeneralRegistry()
	store := newGenerationStore(newTestGeneration("g1", testReasoner{
		name: "backend",
		entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
			atomic.AddInt64(&calls1, 1)
			return reasoning.EntailResult{Verdict: reasoning.VerdictUnsupported}, nil
		},
	}, nil))
	cache := newProofCache(store, reg, serveReasoningConfig(), 16, 1<<20)
	defer store.Close()
	claim := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}

	first, err := cache.Entails(context.Background(), claim)
	if err != nil {
		t.Fatalf("first Entails returned error: %v", err)
	}
	if first.Verdict != reasoning.VerdictUnsupported {
		t.Fatalf("first verdict=%s, want unsupported", first.Verdict)
	}
	store.Swap(newTestGeneration("g2", testReasoner{
		name: "backend",
		entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
			atomic.AddInt64(&calls2, 1)
			proof := testProof()
			return reasoning.EntailResult{
				Best:       &proof,
				Verdict:    reasoning.VerdictEntailed,
				All:        []reasoning.Proof{proof},
				Confidence: proof.Confidence,
			}, nil
		},
	}, nil))

	second, err := cache.Entails(context.Background(), claim)
	if err != nil {
		t.Fatalf("second Entails returned error: %v", err)
	}
	if second.Verdict != reasoning.VerdictEntailed {
		t.Fatalf("second verdict=%s, want entailed after generation swap", second.Verdict)
	}
	if got := atomic.LoadInt64(&calls1); got != 1 {
		t.Fatalf("old generation calls=%d, want 1", got)
	}
	if got := atomic.LoadInt64(&calls2); got != 1 {
		t.Fatalf("new generation calls=%d, want 1", got)
	}
}

func TestProofCacheNeverCachesErrorsOrCanceledContexts(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		var calls int64
		wantErr := errors.New("backend failed")
		cache, store := newTestProofCache("g1", testReasoner{
			name: "backend",
			entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
				atomic.AddInt64(&calls, 1)
				return reasoning.EntailResult{}, wantErr
			},
		})
		defer store.Close()
		claim := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}
		for i := 0; i < 2; i++ {
			_, err := cache.Entails(context.Background(), claim)
			if !errors.Is(err, wantErr) {
				t.Fatalf("Entails error=%v, want %v", err, wantErr)
			}
		}
		if got := atomic.LoadInt64(&calls); got != 2 {
			t.Fatalf("inner calls=%d, want 2", got)
		}
	})

	t.Run("canceled verdict", func(t *testing.T) {
		var calls int64
		cache, store := newTestProofCache("g1", testReasoner{
			name: "backend",
			entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
				atomic.AddInt64(&calls, 1)
				return reasoning.EntailResult{Verdict: reasoning.VerdictUnsupported}, nil
			},
		})
		defer store.Close()
		claim := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := cache.Entails(ctx, claim); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Entails error=%v, want context.Canceled", err)
		}
		if _, err := cache.Entails(context.Background(), claim); err != nil {
			t.Fatalf("retry Entails returned error: %v", err)
		}
		if got := atomic.LoadInt64(&calls); got != 1 {
			t.Fatalf("inner calls=%d, want 1", got)
		}
	})
}

func TestProofCacheForwardsNormalizedRequests(t *testing.T) {
	t.Run("claim", func(t *testing.T) {
		got := make(chan reasoning.Claim, 1)
		cache, store := newTestProofCache("g1", testReasoner{
			name: "backend",
			entails: func(_ context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
				got <- claim
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

		_, err := cache.Entails(context.Background(), reasoning.Claim{
			Subject: " a ", Predicate: "is a", Object: " b ",
		})
		if err != nil {
			t.Fatalf("Entails returned error: %v", err)
		}
		if claim := <-got; claim != (reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}) {
			t.Fatalf("claim=%#v, want normalized claim", claim)
		}
	})

	t.Run("derive", func(t *testing.T) {
		got := make(chan reasoning.DeriveRequest, 1)
		cache, store := newTestProofCache("g1", testReasoner{
			name: "backend",
			derive: func(_ context.Context, req reasoning.DeriveRequest) ([]reasoning.Proof, error) {
				got <- req
				return []reasoning.Proof{testProof()}, nil
			},
		})
		defer store.Close()

		_, err := cache.Derive(context.Background(), reasoning.DeriveRequest{
			Source:         " a ",
			Target:         " b ",
			Predicate:      "is a",
			Preds:          []string{"is_a", "is a", " "},
			Decay:          2,
			IncludeInverse: true,
		})
		if err != nil {
			t.Fatalf("Derive returned error: %v", err)
		}
		req := <-got
		want := reasoning.DeriveRequest{
			Source:         "a",
			Target:         "b",
			Predicate:      "is_a",
			Preds:          []string{"is_a"},
			MaxHops:        4,
			Decay:          0.9,
			MinConf:        0.05,
			Limit:          8,
			IncludeInverse: true,
		}
		if !reflect.DeepEqual(req, want) {
			t.Fatalf("derive request=%#v, want %#v", req, want)
		}
	})
}

func TestProofCacheSingleflightConcurrentEntails(t *testing.T) {
	var calls int64
	entered := make(chan struct{})
	release := make(chan struct{})
	cache, store := newTestProofCache("g1", testReasoner{
		name: "backend",
		entails: func(ctx context.Context, _ reasoning.Claim) (reasoning.EntailResult, error) {
			if atomic.AddInt64(&calls, 1) == 1 {
				close(entered)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return reasoning.EntailResult{}, ctx.Err()
			}
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
	claim := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}
	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := cache.Entails(context.Background(), claim)
			if err != nil {
				errs <- err
				return
			}
			if result.Verdict != reasoning.VerdictEntailed {
				errs <- errors.New("unexpected verdict")
			}
		}()
	}
	close(start)
	waitForClosed(t, entered, "first cache miss to enter backend")
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("inner calls=%d, want 1", got)
	}
}

func TestProofCacheSingleflightCanceledLeaderDoesNotPoisonWaiter(t *testing.T) {
	var calls int64
	entered := make(chan struct{})
	cache, store := newTestProofCache("g1", testReasoner{
		name: "backend",
		entails: func(ctx context.Context, _ reasoning.Claim) (reasoning.EntailResult, error) {
			if atomic.AddInt64(&calls, 1) == 1 {
				close(entered)
				<-ctx.Done()
				return reasoning.EntailResult{Verdict: reasoning.VerdictUnsupported}, nil
			}
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
	claim := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderErr := make(chan error, 1)
	go func() {
		_, err := cache.Entails(leaderCtx, claim)
		leaderErr <- err
	}()
	waitForClosed(t, entered, "singleflight leader to enter backend")

	type waiterResult struct {
		result reasoning.EntailResult
		err    error
	}
	waiterDone := make(chan waiterResult, 1)
	go func() {
		result, err := cache.Entails(context.Background(), claim)
		waiterDone <- waiterResult{result: result, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	cancelLeader()

	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error=%v, want context.Canceled", err)
	}
	select {
	case got := <-waiterDone:
		if got.err != nil {
			t.Fatalf("waiter error=%v, want nil", got.err)
		}
		if got.result.Verdict != reasoning.VerdictEntailed {
			t.Fatalf("waiter verdict=%s, want entailed", got.result.Verdict)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not retry after canceled leader")
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("inner calls=%d, want 2", got)
	}
	if _, err := cache.Entails(context.Background(), claim); err != nil {
		t.Fatalf("cached retry returned error: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("inner calls after cached retry=%d, want 2", got)
	}
}

func TestQueryTelemetryRecordsCacheStatusAndHotKeys(t *testing.T) {
	var calls int64
	cache, store := newTestProofCache("g1", testReasoner{
		name: "backend",
		entails: func(_ context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
			atomic.AddInt64(&calls, 1)
			proof := testProof()
			proof.Source = claim.Subject
			proof.Target = claim.Object
			return reasoning.EntailResult{
				Best:       &proof,
				Verdict:    reasoning.VerdictEntailed,
				All:        []reasoning.Proof{proof},
				Confidence: proof.Confidence,
			}, nil
		},
	})
	defer store.Close()
	telemetry := newQueryTelemetry(8)
	reasoner := newTelemetryReasoner(cache, telemetry)
	claim := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "b"}
	other := reasoning.Claim{Subject: "a", Predicate: "is_a", Object: "c"}

	if _, err := reasoner.Entails(context.Background(), claim); err != nil {
		t.Fatalf("first Entails returned error: %v", err)
	}
	if _, err := reasoner.Entails(context.Background(), claim); err != nil {
		t.Fatalf("second Entails returned error: %v", err)
	}
	if _, err := reasoner.Entails(context.Background(), other); err != nil {
		t.Fatalf("third Entails returned error: %v", err)
	}

	telemetry.mu.Lock()
	events := telemetry.retainedEventsLocked()
	telemetry.mu.Unlock()
	if len(events) != 3 {
		t.Fatalf("events=%d, want 3", len(events))
	}
	gotStatuses := []string{events[0].CacheStatus, events[1].CacheStatus, events[2].CacheStatus}
	wantStatuses := []string{"miss", "hit", "miss"}
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("cache statuses=%v, want %v", gotStatuses, wantStatuses)
	}
	if events[0].SubjectHash == "" || events[0].ObjectHash == "" {
		t.Fatal("entity hashes were not recorded")
	}
	if events[0].rawSubject != "a" || events[0].rawObject != "b" {
		t.Fatalf("raw in-memory entities=%q/%q, want a/b", events[0].rawSubject, events[0].rawObject)
	}
	hot := telemetry.HotKeys(1)
	if len(hot) != 1 || hot[0].Count != 2 || hot[0].KeyHash != events[0].KeyHash {
		t.Fatalf("HotKeys=%#v, want first claim count 2", hot)
	}
	snapshot := telemetry.Snapshot()
	if snapshot.Total != 3 {
		t.Fatalf("snapshot total=%d, want 3", snapshot.Total)
	}
	if snapshot.CacheHits != 1 || snapshot.CacheMisses != 2 {
		t.Fatalf("cache hits/misses=%d/%d, want 1/2", snapshot.CacheHits, snapshot.CacheMisses)
	}
	if snapshot.CacheHitRatio < 0.33 || snapshot.CacheHitRatio > 0.34 {
		t.Fatalf("cache hit ratio=%f, want about 1/3", snapshot.CacheHitRatio)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("inner calls=%d, want 2", got)
	}
}

func newTestProofCache(id string, reasoner reasoning.Reasoner) (*proofCache, *generationStore) {
	store := newGenerationStore(newTestGeneration(id, reasoner, nil))
	return newProofCache(store, reasoning.DefaultGeneralRegistry(), serveReasoningConfig(), 16, 1<<20), store
}

func testProof() reasoning.Proof {
	return reasoning.Proof{
		Source:     "a",
		Target:     "b",
		Predicate:  "is_a",
		RuleClass:  "composition",
		Hops:       1,
		Confidence: 0.9,
		Steps: []reasoning.ProofStep{{
			EdgeID:     "edge-1",
			Rule:       "direct",
			Predicate:  "is_a",
			Source:     "a",
			Target:     "b",
			Confidence: 0.9,
		}},
	}
}
