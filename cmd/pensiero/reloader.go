package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type generationBuilder func(ctx context.Context, path string) (*generation, error)
type generationValidator func(ctx context.Context, candidate *generation) error

type snapshotFingerprint struct {
	target  string
	modTime time.Time
	size    int64
	mode    os.FileMode
}

type snapshotReloader struct {
	path     string
	interval time.Duration
	build    generationBuilder
	validate generationValidator
	store    *generationStore
	logger   *log.Logger

	reloadMu  sync.Mutex
	mu        sync.Mutex
	last      snapshotFingerprint
	hasLast   bool
	stop      chan struct{}
	done      chan struct{}
	started   bool
	startOnce sync.Once
	stopOnce  sync.Once
}

func newSnapshotReloader(path string, interval time.Duration, build generationBuilder, validate generationValidator, store *generationStore, logger *log.Logger) *snapshotReloader {
	if interval <= 0 {
		interval = time.Minute
	}
	return &snapshotReloader{
		path:     path,
		interval: interval,
		build:    build,
		validate: validate,
		store:    store,
		logger:   logger,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (r *snapshotReloader) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		r.mu.Lock()
		r.started = true
		r.mu.Unlock()
		go r.run(ctx)
	})
}

func (r *snapshotReloader) Close() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stop)
		r.mu.Lock()
		started := r.started
		r.mu.Unlock()
		if started {
			<-r.done
		}
	})
}

func (r *snapshotReloader) Reload(ctx context.Context) error {
	if r == nil {
		return nil
	}
	fp, err := snapshotFingerprintForPath(r.path)
	if err != nil {
		return err
	}
	if err := r.reloadFingerprint(ctx, fp); err != nil {
		return err
	}
	r.setLast(fp)
	return nil
}

func (r *snapshotReloader) setLast(fp snapshotFingerprint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = fp
	r.hasLast = true
}

func (r *snapshotReloader) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			r.reloadIfChanged(ctx)
		}
	}
}

func (r *snapshotReloader) reloadIfChanged(ctx context.Context) {
	fp, err := snapshotFingerprintForPath(r.path)
	if err != nil {
		r.log("reload path=%s fingerprint error=%v", r.path, err)
		return
	}
	r.mu.Lock()
	changed := !r.hasLast || !sameSnapshotFingerprint(r.last, fp)
	r.mu.Unlock()
	if !changed {
		return
	}
	if err := r.reloadFingerprint(ctx, fp); err != nil {
		r.log("reload path=%s rejected error=%v", r.path, err)
		return
	} else {
		r.log("reload path=%s accepted", r.path)
	}
	r.setLast(fp)
}

func (r *snapshotReloader) reloadFingerprint(ctx context.Context, fp snapshotFingerprint) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if r.build == nil {
		return fmt.Errorf("reload %s: nil generation builder", r.path)
	}
	if r.store == nil {
		return fmt.Errorf("reload %s: nil generation store", r.path)
	}
	candidatePath := fp.target
	if candidatePath == "" {
		candidatePath = r.path
	}
	candidate, err := r.build(ctx, candidatePath)
	if err != nil {
		return fmt.Errorf("build candidate: %w", err)
	}
	if candidate == nil {
		return fmt.Errorf("build candidate: nil generation")
	}
	swapped := false
	defer func() {
		if !swapped {
			closeGeneration(candidate)
		}
	}()
	if r.validate != nil {
		if err := r.validate(ctx, candidate); err != nil {
			return err
		}
	}
	if !r.store.Swap(candidate) {
		return fmt.Errorf("generation store is closed")
	}
	swapped = true
	r.log("generation swap id=%s path=%s target=%s", candidate.id, candidate.path, fp.target)
	return nil
}

func (r *snapshotReloader) log(format string, args ...any) {
	if r != nil && r.logger != nil {
		r.logger.Printf(format, args...)
	}
}

func closeGeneration(gen *generation) {
	if gen != nil && gen.pool != nil {
		_ = gen.pool.Close()
	}
}

func snapshotFingerprintForPath(path string) (snapshotFingerprint, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return snapshotFingerprint{}, err
	}
	target := path
	info := linkInfo
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		rawTarget, err := os.Readlink(path)
		if err != nil {
			return snapshotFingerprint{}, err
		}
		target = rawTarget
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		info, err = os.Stat(path)
		if err != nil {
			return snapshotFingerprint{}, err
		}
	}
	return snapshotFingerprint{
		target:  filepath.Clean(target),
		modTime: info.ModTime(),
		size:    info.Size(),
		mode:    info.Mode().Type(),
	}, nil
}

func sameSnapshotFingerprint(a, b snapshotFingerprint) bool {
	return a.target == b.target && a.modTime.Equal(b.modTime) && a.size == b.size && a.mode == b.mode
}
