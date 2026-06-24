package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	leaderModeFlock    = "flock"
	leaderModeNone     = "none"
	leaderModeK8sLease = "k8s-lease"
)

var (
	errLeaderLockHeld         = errors.New("leader lock already held")
	errLeaderFlockUnsupported = errors.New("flock leader election is not supported on this platform")
)

type scopeLeader interface {
	TryAcquire(scope string) (bool, error)
	Holds(scope string) bool
	Release(scope string) error
	Close() error
}

type leaderElector struct {
	outDir string
	mu     sync.Mutex
	locks  map[string]*leaderLock
	closed bool
}

type leaderLock struct {
	path string
	file *os.File
}

func newLeaderElector(outDir string) (*leaderElector, error) {
	outDir = strings.TrimSpace(outDir)
	if outDir == "" {
		return nil, fmt.Errorf("leader election: empty output dir")
	}
	if err := flockLeaderSupported(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("leader election: create output dir: %w", err)
	}
	return &leaderElector{
		outDir: outDir,
		locks:  map[string]*leaderLock{},
	}, nil
}

func (e *leaderElector) TryAcquire(scope string) (bool, error) {
	if e == nil {
		return false, fmt.Errorf("leader election: nil elector")
	}
	name, err := cleanLeaderScope(scope)
	if err != nil {
		return false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false, fmt.Errorf("leader election: elector closed")
	}
	if _, ok := e.locks[name]; ok {
		return true, nil
	}
	path := filepath.Join(e.outDir, name+".igl.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, fmt.Errorf("leader election: open lock %s: %w", path, err)
	}
	if err := tryLockLeaderFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errLeaderLockHeld) {
			return false, nil
		}
		return false, fmt.Errorf("leader election: flock %s: %w", path, err)
	}
	e.locks[name] = &leaderLock{
		path: path,
		file: file,
	}
	return true, nil
}

func (e *leaderElector) Holds(scope string) bool {
	if e == nil {
		return false
	}
	name, err := cleanLeaderScope(scope)
	if err != nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.locks[name]
	return ok
}

func (e *leaderElector) Release(scope string) error {
	if e == nil {
		return nil
	}
	name, err := cleanLeaderScope(scope)
	if err != nil {
		return err
	}
	e.mu.Lock()
	lock := e.locks[name]
	delete(e.locks, name)
	e.mu.Unlock()
	return closeLeaderLock(lock)
}

func (e *leaderElector) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	locks := make([]*leaderLock, 0, len(e.locks))
	for name, lock := range e.locks {
		locks = append(locks, lock)
		delete(e.locks, name)
	}
	e.closed = true
	e.mu.Unlock()

	var err error
	for _, lock := range locks {
		err = errors.Join(err, closeLeaderLock(lock))
	}
	return err
}

func closeLeaderLock(lock *leaderLock) error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unlockLeaderFile(lock.file)
	closeErr := lock.file.Close()
	if unlockErr != nil {
		unlockErr = fmt.Errorf("leader election: unlock %s: %w", lock.path, unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("leader election: close %s: %w", lock.path, closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}

type noneLeaderElector struct{}

func (noneLeaderElector) TryAcquire(scope string) (bool, error) {
	if _, err := cleanLeaderScope(scope); err != nil {
		return false, err
	}
	return true, nil
}

func (noneLeaderElector) Holds(scope string) bool {
	_, err := cleanLeaderScope(scope)
	return err == nil
}

func (noneLeaderElector) Release(string) error {
	return nil
}

func (noneLeaderElector) Close() error {
	return nil
}

func newLeaderForMode(mode string, outDir string) (scopeLeader, error) {
	switch normalizeLeaderMode(mode) {
	case leaderModeFlock:
		return newLeaderElector(outDir)
	case leaderModeNone:
		return noneLeaderElector{}, nil
	case leaderModeK8sLease:
		return nil, fmt.Errorf("--leader-mode=%s is not implemented yet", leaderModeK8sLease)
	default:
		return nil, fmt.Errorf("unsupported --leader-mode %q (supported: %s, %s, %s)", mode, leaderModeFlock, leaderModeNone, leaderModeK8sLease)
	}
}

func normalizeLeaderMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return leaderModeFlock
	}
	return mode
}

func cleanLeaderScope(scope string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", fmt.Errorf("leader election: empty scope")
	}
	if scope == "." || scope == ".." || strings.ContainsAny(scope, `/\`) {
		return "", fmt.Errorf("leader election: invalid scope %q", scope)
	}
	return scope, nil
}
