package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/soundprediction/pensiero/pkg/generalization"
)

type leaderGatedIGLRunner struct {
	loop   *generalization.Loop
	leader scopeLeader
	scopes []generalization.Scope
	logger interface{ Printf(string, ...any) }
	mu     sync.Mutex
}

func newLeaderGatedIGLRunner(loop *generalization.Loop, leader scopeLeader, logger interface{ Printf(string, ...any) }) *leaderGatedIGLRunner {
	var scopes []generalization.Scope
	if loop != nil {
		scopes = append(scopes, loop.Scopes...)
	}
	return &leaderGatedIGLRunner{
		loop:   loop,
		leader: leader,
		scopes: scopes,
		logger: logger,
	}
}

func (r *leaderGatedIGLRunner) RunOnce(ctx context.Context) (generalization.PassResult, error) {
	if r == nil || r.loop == nil {
		return generalization.PassResult{}, fmt.Errorf("leader-gated IGL: nil loop")
	}
	held, err := r.heldScopes()
	if err != nil {
		return generalization.PassResult{}, err
	}
	if len(held) == 0 {
		r.log("generalization IGL skipped: this instance is not leader for any configured scope")
		return generalization.PassResult{}, nil
	}

	r.mu.Lock()
	original := r.loop.Scopes
	r.loop.Scopes = held
	defer func() {
		r.loop.Scopes = original
		r.mu.Unlock()
	}()
	return r.loop.RunOnce(ctx)
}

func (r *leaderGatedIGLRunner) heldScopes() ([]generalization.Scope, error) {
	if r.leader == nil {
		return append([]generalization.Scope{}, r.scopes...), nil
	}
	held := make([]generalization.Scope, 0, len(r.scopes))
	for _, scope := range r.scopes {
		name, err := leadershipScopeName(scope)
		if err != nil {
			return nil, err
		}
		if !r.leader.Holds(name) {
			if _, err := r.leader.TryAcquire(name); err != nil {
				return nil, err
			}
		}
		if r.leader.Holds(name) {
			held = append(held, scope)
		}
	}
	return held, nil
}

func (r *leaderGatedIGLRunner) log(format string, args ...any) {
	if r != nil && r.logger != nil {
		r.logger.Printf(format, args...)
	}
}

func leadershipScopeName(scope generalization.Scope) (string, error) {
	name := strings.TrimSpace(scope.Name)
	if name == "" {
		name = strings.TrimSpace(scope.Config.Scope)
	}
	return cleanLeaderScope(name)
}

func leadershipScopeNames(scopes []generalization.Scope) ([]string, error) {
	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		name, err := leadershipScopeName(scope)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}
