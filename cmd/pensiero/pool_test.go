package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeGraphHandle struct {
	query func(context.Context, string, map[string]any) ([]map[string]any, error)
	close func() error
}

func (h *fakeGraphHandle) Query(ctx context.Context, query string, params map[string]any) ([]map[string]any, error) {
	return h.query(ctx, query, params)
}

func (h *fakeGraphHandle) Close() error {
	if h.close == nil {
		return nil
	}
	return h.close()
}

func newTestPool(handles ...graphHandle) *pooledGraphQuerier {
	pool := &pooledGraphQuerier{
		available: make(chan graphHandle, len(handles)),
		handles:   append([]graphHandle{}, handles...),
		done:      make(chan struct{}),
	}
	for _, handle := range handles {
		pool.available <- handle
	}
	return pool
}

func TestPooledGraphQuerierConcurrentFanOut(t *testing.T) {
	const (
		poolSize = 3
		queries  = 6
	)
	var active int64
	var maxActive int64
	entered := make(chan int, queries)
	release := make(chan struct{})
	seen := map[int]bool{}
	var seenMu sync.Mutex

	handles := make([]graphHandle, 0, poolSize)
	for i := 0; i < poolSize; i++ {
		id := i
		handles = append(handles, &fakeGraphHandle{
			query: func(ctx context.Context, _ string, _ map[string]any) ([]map[string]any, error) {
				seenMu.Lock()
				seen[id] = true
				seenMu.Unlock()

				now := atomic.AddInt64(&active, 1)
				updateMax(&maxActive, now)
				entered <- id
				select {
				case <-release:
				case <-ctx.Done():
					atomic.AddInt64(&active, -1)
					return nil, ctx.Err()
				}
				atomic.AddInt64(&active, -1)
				return []map[string]any{{"handle": id}}, nil
			},
		})
	}
	pool := newTestPool(handles...)

	var wg sync.WaitGroup
	errs := make(chan error, queries)
	for i := 0; i < queries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := pool.Query(context.Background(), fmt.Sprintf("q%d", i), nil)
			errs <- err
		}(i)
	}

	waitForEntries(t, entered, poolSize)
	if got := atomic.LoadInt64(&maxActive); got != poolSize {
		t.Fatalf("max active queries = %d, want %d", got, poolSize)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Query returned error: %v", err)
		}
	}
	if got := atomic.LoadInt64(&maxActive); got > poolSize {
		t.Fatalf("max active queries = %d, want <= %d", got, poolSize)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) != poolSize {
		t.Fatalf("handles used = %d, want %d", len(seen), poolSize)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestPooledGraphQuerierContextCancelWhileWaiting(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	handle := &fakeGraphHandle{
		query: func(context.Context, string, map[string]any) ([]map[string]any, error) {
			close(entered)
			<-release
			return nil, nil
		},
	}
	pool := newTestPool(handle)

	firstDone := make(chan error, 1)
	go func() {
		_, err := pool.Query(context.Background(), "hold", nil)
		firstDone <- err
	}()
	waitForClosed(t, entered, "first query to start")

	ctx, cancel := context.WithCancel(context.Background())
	waitingDone := make(chan error, 1)
	go func() {
		_, err := pool.Query(ctx, "wait", nil)
		waitingDone <- err
	}()
	cancel()

	select {
	case err := <-waitingDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting Query error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting Query did not return after context cancellation")
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Query returned error: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestPooledGraphQuerierCloseDrainsInflightAndRejectsWaiters(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	closeActive := make(chan int64, 1)
	var active int64
	var queryCalls int64
	handle := &fakeGraphHandle{
		query: func(context.Context, string, map[string]any) ([]map[string]any, error) {
			atomic.AddInt64(&queryCalls, 1)
			atomic.AddInt64(&active, 1)
			close(entered)
			<-release
			atomic.AddInt64(&active, -1)
			return nil, nil
		},
		close: func() error {
			closeActive <- atomic.LoadInt64(&active)
			return nil
		},
	}
	pool := newTestPool(handle)

	firstDone := make(chan error, 1)
	go func() {
		_, err := pool.Query(context.Background(), "hold", nil)
		firstDone <- err
	}()
	waitForClosed(t, entered, "first query to start")

	waitingDone := make(chan error, 1)
	go func() {
		_, err := pool.Query(context.Background(), "wait", nil)
		waitingDone <- err
	}()

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- pool.Close()
	}()

	select {
	case err := <-waitingDone:
		if !errors.Is(err, errPooledGraphQuerierClosed) {
			t.Fatalf("waiting Query error = %v, want pool closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting Query did not return after Close")
	}
	if got := atomic.LoadInt64(&queryCalls); got != 1 {
		t.Fatalf("query calls = %d, want 1", got)
	}
	select {
	case got := <-closeActive:
		t.Fatalf("handle closed while %d queries were active", got)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Query returned error: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if got := <-closeActive; got != 0 {
		t.Fatalf("handle closed while %d queries were active, want 0", got)
	}
}

func TestPooledGraphQuerierReturnsHandleAfterPanic(t *testing.T) {
	var panicNext atomic.Bool
	panicNext.Store(true)
	handle := &fakeGraphHandle{
		query: func(context.Context, string, map[string]any) ([]map[string]any, error) {
			if panicNext.Swap(false) {
				panic("boom")
			}
			return []map[string]any{{"ok": true}}, nil
		},
	}
	pool := newTestPool(handle)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Query did not panic")
			}
		}()
		_, _ = pool.Query(context.Background(), "panic", nil)
	}()

	rows, err := pool.Query(context.Background(), "after", nil)
	if err != nil {
		t.Fatalf("second Query returned error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("second Query rows = %d, want 1", len(rows))
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func updateMax(dst *int64, value int64) {
	for {
		old := atomic.LoadInt64(dst)
		if value <= old || atomic.CompareAndSwapInt64(dst, old, value) {
			return
		}
	}
}

func waitForEntries(t *testing.T, ch <-chan int, want int) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("received %d entries, want %d", i, want)
		}
	}
}

func waitForClosed(t *testing.T, ch <-chan struct{}, reason string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", reason)
	}
}
