package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

type testReasoner struct {
	name        string
	derive      func(context.Context, reasoning.DeriveRequest) ([]reasoning.Proof, error)
	entails     func(context.Context, reasoning.Claim) (reasoning.EntailResult, error)
	contradicts func(context.Context, reasoning.Claim) (bool, *reasoning.Proof, error)
}

func (r testReasoner) Derive(ctx context.Context, req reasoning.DeriveRequest) ([]reasoning.Proof, error) {
	if r.derive != nil {
		return r.derive(ctx, req)
	}
	return nil, nil
}

func (r testReasoner) Entails(ctx context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
	if r.entails != nil {
		return r.entails(ctx, claim)
	}
	return reasoning.EntailResult{Verdict: reasoning.VerdictUnsupported}, nil
}

func (r testReasoner) Contradicts(ctx context.Context, claim reasoning.Claim) (bool, *reasoning.Proof, error) {
	if r.contradicts != nil {
		return r.contradicts(ctx, claim)
	}
	return false, nil, nil
}

func (r testReasoner) Name() string {
	if r.name == "" {
		return "test"
	}
	return r.name
}

func newTestGeneration(id string, reasoner reasoning.Reasoner, closeCount *int64) *generation {
	handle := &fakeGraphHandle{
		query: func(_ context.Context, query string, _ map[string]any) ([]map[string]any, error) {
			if strings.Contains(query, "MATCH (n:Entity)") {
				return []map[string]any{{"count": int64(1)}}, nil
			}
			return []map[string]any{{"ok": int64(1)}}, nil
		},
		close: func() error {
			if closeCount != nil {
				atomic.AddInt64(closeCount, 1)
			}
			return nil
		},
	}
	return &generation{
		id:       id,
		pool:     newTestPool(handle),
		reasoner: reasoner,
		path:     id,
	}
}

func TestGenerationStoreAcquireSwapRollbackRefcount(t *testing.T) {
	var close1, close2, close3 int64
	gen1 := newTestGeneration("g1", testReasoner{name: "g1"}, &close1)
	gen2 := newTestGeneration("g2", testReasoner{name: "g2"}, &close2)
	gen3 := newTestGeneration("g3", testReasoner{name: "g3"}, &close3)
	store := newGenerationStore(gen1)

	held, releaseHeld := store.Acquire()
	if held == nil || held.id != "g1" {
		t.Fatalf("held generation=%v, want g1", held)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gen, release := store.Acquire()
			if gen == nil {
				t.Error("Acquire returned nil before close")
				return
			}
			time.Sleep(time.Millisecond)
			release()
		}()
	}
	store.Swap(gen2)
	wg.Wait()

	current, releaseCurrent := store.Acquire()
	if current == nil || current.id != "g2" {
		t.Fatalf("current generation=%v, want g2", current)
	}
	releaseCurrent()
	if atomic.LoadInt64(&close1) != 0 {
		t.Fatal("rollback generation closed while retained")
	}
	if !store.Rollback() {
		t.Fatal("Rollback returned false")
	}
	rolledBack, releaseRolledBack := store.Acquire()
	if rolledBack == nil || rolledBack.id != "g1" {
		t.Fatalf("rolled back generation=%v, want g1", rolledBack)
	}
	releaseRolledBack()

	store.Swap(gen3)
	if atomic.LoadInt64(&close2) != 1 {
		t.Fatalf("evicted rollback close count=%d, want 1", atomic.LoadInt64(&close2))
	}
	if _, err := gen2.pool.Query(context.Background(), "after close", nil); !errors.Is(err, errPooledGraphQuerierClosed) {
		t.Fatalf("evicted generation query error=%v, want pool closed", err)
	}
	if atomic.LoadInt64(&close1) != 0 {
		t.Fatal("active/rollback generation closed before release/store close")
	}
	releaseHeld()
	if atomic.LoadInt64(&close1) != 0 {
		t.Fatal("rollback generation closed after active release while retained")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if atomic.LoadInt64(&close1) != 1 || atomic.LoadInt64(&close3) != 1 {
		t.Fatalf("close counts g1=%d g3=%d, want 1 each", atomic.LoadInt64(&close1), atomic.LoadInt64(&close3))
	}
}

func TestGenerationStoreConcurrentAcquireSwapClose(t *testing.T) {
	var created int64
	var closed int64
	nextGeneration := func(id string) *generation {
		atomic.AddInt64(&created, 1)
		return newTestGeneration(id, testReasoner{name: id}, &closed)
	}
	store := newGenerationStore(nextGeneration("initial"))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				gen, release := store.Acquire()
				if gen != nil {
					_ = gen.id
				}
				release()
			}
		}()
	}
	for i := 0; i < 40; i++ {
		store.Swap(nextGeneration("swap"))
		if i%7 == 0 {
			_ = store.Rollback()
		}
	}
	close(stop)
	wg.Wait()
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if got, want := atomic.LoadInt64(&closed), atomic.LoadInt64(&created); got != want {
		t.Fatalf("closed generations=%d, want %d", got, want)
	}
	if gen, release := store.Acquire(); gen != nil {
		release()
		t.Fatalf("Acquire after Close returned generation %s", gen.id)
	} else {
		release()
	}
}

func TestSnapshotValidatorRejectsGoldenFlipWithoutSwap(t *testing.T) {
	path := t.TempDir() + "/served.ladybug"
	if err := os.WriteFile(path, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	var candidateClosed int64
	current := newTestGeneration("current", testReasoner{name: "current"}, nil)
	candidate := newTestGeneration("candidate", testReasoner{
		name: "candidate",
		entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
			proof := reasoning.Proof{
				Steps: []reasoning.ProofStep{{Predicate: "is_a"}},
			}
			return reasoning.EntailResult{Verdict: reasoning.VerdictEntailed, Best: &proof}, nil
		},
	}, &candidateClosed)
	store := newGenerationStore(current)
	validator := snapshotValidator{
		Golden: fileGoldenSet{cases: []GoldenCase{{
			Claim: reasoning.Claim{
				Subject:   "a",
				Predicate: "is_a",
				Object:    "b",
			},
			WantVerdictClass: reasoning.VerdictUnsupported,
		}}},
		Registry: reasoning.DefaultGeneralRegistry(),
	}
	reloader := newSnapshotReloader(path, time.Hour, func(context.Context, string) (*generation, error) {
		return candidate, nil
	}, validator.Validate, store, nil)

	if err := reloader.Reload(context.Background()); err == nil {
		t.Fatal("Reload returned nil error for golden flip")
	}
	if atomic.LoadInt64(&candidateClosed) != 1 {
		t.Fatalf("candidate close count=%d, want 1", atomic.LoadInt64(&candidateClosed))
	}
	gen, release := store.Acquire()
	if gen == nil || gen.id != "current" {
		release()
		t.Fatalf("current generation=%v, want current retained", gen)
	}
	release()
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestSnapshotValidatorRequiresEntityProbe(t *testing.T) {
	candidate := newTestGeneration("empty", testReasoner{}, nil)
	_ = candidate.pool.Close()
	candidate.pool = newTestPool(&fakeGraphHandle{
		query: func(context.Context, string, map[string]any) ([]map[string]any, error) {
			return []map[string]any{{"count": int64(0)}}, nil
		},
	})
	validator := snapshotValidator{}

	err := validator.Validate(context.Background(), candidate)
	if err == nil || !strings.Contains(err.Error(), "no Entity nodes") {
		t.Fatalf("Validate error=%v, want no Entity nodes", err)
	}
	_ = candidate.pool.Close()
}

func TestSnapshotValidatorNilGoldenSetDoesNotRequireReasoner(t *testing.T) {
	candidate := newTestGeneration("candidate", nil, nil)
	validator := snapshotValidator{}

	if err := validator.Validate(context.Background(), candidate); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	_ = candidate.pool.Close()
}

func TestSnapshotReloaderRejectedCandidateKeepsLastAndClosesCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "served.ladybug")
	if err := os.WriteFile(path, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	var candidateClosed int64
	store := newGenerationStore(newTestGeneration("current", testReasoner{}, nil))
	reloader := newSnapshotReloader(path, time.Hour, func(context.Context, string) (*generation, error) {
		return newTestGeneration("candidate", testReasoner{}, &candidateClosed), nil
	}, func(context.Context, *generation) error {
		return errors.New("golden rejected")
	}, store, nil)
	old := snapshotFingerprint{target: "accepted", modTime: time.Unix(1, 0), size: 1}
	reloader.setLast(old)

	if err := reloader.Reload(context.Background()); err == nil {
		t.Fatal("Reload returned nil error for rejected candidate")
	}
	reloader.mu.Lock()
	got := reloader.last
	reloader.mu.Unlock()
	if !sameSnapshotFingerprint(got, old) {
		t.Fatalf("last fingerprint=%+v, want retained %+v", got, old)
	}
	if atomic.LoadInt64(&candidateClosed) != 1 {
		t.Fatalf("candidate close count=%d, want 1", atomic.LoadInt64(&candidateClosed))
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestSnapshotReloaderBuildsResolvedSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.ladybug")
	link := filepath.Join(dir, "served.ladybug")
	if err := os.WriteFile(target, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	store := newGenerationStore(newTestGeneration("current", testReasoner{}, nil))
	var builtPath string
	reloader := newSnapshotReloader(link, time.Hour, func(_ context.Context, path string) (*generation, error) {
		builtPath = path
		return newTestGeneration("candidate", testReasoner{}, nil), nil
	}, nil, store, nil)

	if err := reloader.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if builtPath != filepath.Clean(target) {
		t.Fatalf("built path=%q, want resolved target %q", builtPath, filepath.Clean(target))
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestStoreReasonerDelegatesCurrentAcrossConcurrentSwap(t *testing.T) {
	entered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan reasoning.EntailResult, 1)
	gen1 := newTestGeneration("g1", testReasoner{
		name: "g1",
		entails: func(ctx context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
			close(entered)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return reasoning.EntailResult{}, ctx.Err()
			}
			return reasoning.EntailResult{Verdict: reasoning.VerdictUnsupported}, nil
		},
	}, nil)
	gen2 := newTestGeneration("g2", testReasoner{
		name: "g2",
		entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
			return reasoning.EntailResult{Verdict: reasoning.VerdictEntailed}, nil
		},
	}, nil)
	store := newGenerationStore(gen1)
	reasoner := &storeReasoner{store: store}

	go func() {
		res, err := reasoner.Entails(context.Background(), reasoning.Claim{})
		if err != nil {
			t.Errorf("first Entails returned error: %v", err)
		}
		firstDone <- res
	}()
	waitForClosed(t, entered, "first entailment to enter old generation")
	store.Swap(gen2)
	close(releaseFirst)

	first := <-firstDone
	if first.Verdict != reasoning.VerdictUnsupported {
		t.Fatalf("first verdict=%s, want old generation unsupported", first.Verdict)
	}
	second, err := reasoner.Entails(context.Background(), reasoning.Claim{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Verdict != reasoning.VerdictEntailed {
		t.Fatalf("second verdict=%s, want new generation entailed", second.Verdict)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestStoreReasonerReleasesGenerationAfterPanic(t *testing.T) {
	var closed int64
	gen := newTestGeneration("panic", testReasoner{
		entails: func(context.Context, reasoning.Claim) (reasoning.EntailResult, error) {
			panic("boom")
		},
	}, &closed)
	store := newGenerationStore(gen)
	reasoner := &storeReasoner{store: store}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Entails did not panic")
			}
		}()
		_, _ = reasoner.Entails(context.Background(), reasoning.Claim{})
	}()

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- store.Close()
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close timed out; generation ref was not released after panic")
	}
	if atomic.LoadInt64(&closed) != 1 {
		t.Fatalf("close count=%d, want 1", atomic.LoadInt64(&closed))
	}
}
