package main

import (
	"context"
	"errors"
	"sync"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const generationRollbackSlots = 1

var errNoGeneration = errors.New("no live reasoning generation")

type generationRoute struct {
	Text string
}

type generationProvider interface {
	AcquireGeneration(context.Context, generationRoute) (*generation, func(), error)
	ProviderName() string
}

type generationAcquirer interface {
	Acquire() (*generation, func())
}

type generation struct {
	id       string
	pool     *pooledGraphQuerier
	reasoner reasoning.Reasoner
	path     string
}

type generationRef struct {
	gen       *generation
	refs      int
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

type generationStore struct {
	mu       sync.Mutex
	current  *generationRef
	rollback []*generationRef
	closed   bool
}

func newGenerationStore(initial *generation) *generationStore {
	store := &generationStore{}
	if initial != nil {
		store.current = newGenerationRef(initial)
	}
	return store
}

func newGenerationRef(gen *generation) *generationRef {
	return &generationRef{
		gen:  gen,
		refs: 1,
		done: make(chan struct{}),
	}
}

func (s *generationStore) Acquire() (*generation, func()) {
	if s == nil {
		return nil, func() {}
	}
	s.mu.Lock()
	if s.closed || s.current == nil {
		s.mu.Unlock()
		return nil, func() {}
	}
	ref := s.current
	ref.refs++
	gen := ref.gen
	s.mu.Unlock()

	var once sync.Once
	return gen, func() {
		once.Do(func() {
			s.release(ref)
		})
	}
}

func (s *generationStore) AcquireGeneration(_ context.Context, _ generationRoute) (*generation, func(), error) {
	gen, release := s.Acquire()
	if gen == nil || gen.reasoner == nil {
		release()
		return nil, func() {}, errNoGeneration
	}
	return gen, release, nil
}

func (s *generationStore) ProviderName() string {
	gen, release := s.Acquire()
	if gen == nil || gen.reasoner == nil {
		release()
		return "generation-store"
	}
	defer release()
	return gen.reasoner.Name()
}

func (s *generationStore) Swap(next *generation) bool {
	if next == nil {
		return false
	}
	nextRef := newGenerationRef(next)
	var evicted *generationRef
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = nextRef.close()
		return false
	}
	if s.current != nil {
		s.rollback = append([]*generationRef{s.current}, s.rollback...)
		if len(s.rollback) > generationRollbackSlots {
			evicted = s.rollback[len(s.rollback)-1]
			s.rollback = s.rollback[:generationRollbackSlots]
		}
	}
	s.current = nextRef
	s.mu.Unlock()
	if evicted != nil {
		s.release(evicted)
	}
	return true
}

func (s *generationStore) Rollback() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.current == nil || len(s.rollback) == 0 {
		return false
	}
	previous := s.rollback[0]
	s.rollback[0] = s.current
	s.current = previous
	return true
}

func (s *generationStore) Close() error {
	if s == nil {
		return nil
	}
	var refs []*generationRef
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.current != nil {
		refs = append(refs, s.current)
		s.current = nil
	}
	refs = append(refs, s.rollback...)
	s.rollback = nil
	s.mu.Unlock()

	var err error
	for _, ref := range refs {
		_ = s.release(ref)
	}
	for _, ref := range refs {
		<-ref.done
		if ref.closeErr != nil {
			err = errors.Join(err, ref.closeErr)
		}
	}
	return err
}

func (s *generationStore) release(ref *generationRef) error {
	if s == nil || ref == nil {
		return nil
	}
	s.mu.Lock()
	if ref.refs == 0 {
		s.mu.Unlock()
		return nil
	}
	ref.refs--
	shouldClose := ref.refs == 0
	s.mu.Unlock()
	if shouldClose {
		return ref.close()
	}
	return nil
}

func (r *generationRef) close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.gen != nil && r.gen.pool != nil {
			r.closeErr = r.gen.pool.Close()
		}
		close(r.done)
	})
	return r.closeErr
}

type storeReasoner struct {
	store *generationStore
}

func (r *storeReasoner) Derive(ctx context.Context, req reasoning.DeriveRequest) ([]reasoning.Proof, error) {
	reasoner, release, err := r.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	return reasoner.Derive(ctx, req)
}

func (r *storeReasoner) Entails(ctx context.Context, claim reasoning.Claim) (reasoning.EntailResult, error) {
	reasoner, release, err := r.acquire()
	if err != nil {
		return reasoning.EntailResult{}, err
	}
	defer release()
	return reasoner.Entails(ctx, claim)
}

func (r *storeReasoner) Contradicts(ctx context.Context, claim reasoning.Claim) (bool, *reasoning.Proof, error) {
	reasoner, release, err := r.acquire()
	if err != nil {
		return false, nil, err
	}
	defer release()
	return reasoner.Contradicts(ctx, claim)
}

func (r *storeReasoner) Name() string {
	reasoner, release, err := r.acquire()
	if err != nil {
		return "generation-store"
	}
	defer release()
	return reasoner.Name()
}

func (r *storeReasoner) acquire() (reasoning.Reasoner, func(), error) {
	if r == nil || r.store == nil {
		return nil, func() {}, errNoGeneration
	}
	gen, release := r.store.Acquire()
	if gen == nil || gen.reasoner == nil {
		release()
		return nil, func() {}, errNoGeneration
	}
	return gen.reasoner, release, nil
}
