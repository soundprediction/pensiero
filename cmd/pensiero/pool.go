package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const defaultGRPCPoolSize = 4

var errPooledGraphQuerierClosed = errors.New("pooled graph querier closed")

type readinessGate struct {
	ready atomic.Bool
}

func newReadinessGate() *readinessGate {
	return &readinessGate{}
}

func (g *readinessGate) MarkReady() {
	if g != nil {
		g.ready.Store(true)
	}
}

func (g *readinessGate) Ready() bool {
	if g == nil {
		return true
	}
	return g.ready.Load()
}

// pooledGraphQuerier fans concurrent reasoning queries over independent
// read-only graph handles so a single ladybug handle's mutex does not serialize
// all gRPC requests.
type pooledGraphQuerier struct {
	available chan graphHandle
	handles   []graphHandle
	done      chan struct{}
	closeErr  error
	closeOnce sync.Once
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
}

func newPooledGraphQuerier(path string, size int) (*pooledGraphQuerier, error) {
	if size <= 0 {
		return nil, fmt.Errorf("pool size must be positive")
	}
	pool := &pooledGraphQuerier{
		available: make(chan graphHandle, size),
		handles:   make([]graphHandle, 0, size),
		done:      make(chan struct{}),
	}
	// Each pool entry is an independent read-only handle to the same embedded
	// graph. This assumes the backend supports concurrent read-only opens; if it
	// does not, the failed open is returned and handles opened so far are closed.
	for i := 0; i < size; i++ {
		handle, err := openLadybugGraph(path, true)
		if err != nil {
			_ = pool.Close()
			return nil, fmt.Errorf("open read-only handle %d/%d: %w", i+1, size, err)
		}
		pool.handles = append(pool.handles, handle)
		pool.available <- handle
	}
	return pool, nil
}

func (p *pooledGraphQuerier) Query(ctx context.Context, query string, params map[string]any) ([]map[string]any, error) {
	select {
	case <-p.done:
		return nil, errPooledGraphQuerierClosed
	default:
	}

	var handle graphHandle
	select {
	case handle = <-p.available:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, errPooledGraphQuerierClosed
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.returnHandle(handle)
		return nil, errPooledGraphQuerierClosed
	}
	p.wg.Add(1)
	p.mu.Unlock()
	defer p.wg.Done()
	defer p.returnHandle(handle)

	// Context cancellation is honored while waiting for a handle. Once the
	// backend query is running, cancellation depends on backend support.
	return handle.Query(ctx, query, params)
}

func (p *pooledGraphQuerier) returnHandle(handle graphHandle) {
	select {
	case p.available <- handle:
	default:
		panic("pooled graph querier: returned more handles than borrowed")
	}
}

func (p *pooledGraphQuerier) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		close(p.done)
		p.mu.Unlock()
		p.wg.Wait()

		errs := make([]error, 0, len(p.handles))
		for i, handle := range p.handles {
			if err := handle.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close read-only handle %d: %w", i+1, err))
			}
		}
		p.closeErr = errors.Join(errs...)
	})
	return p.closeErr
}
