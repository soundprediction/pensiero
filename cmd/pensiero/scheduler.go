package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/soundprediction/pensiero/pkg/generalization"
)

const (
	defaultIGLQuiet          = 3 * time.Second
	defaultIGLMinPublish     = 30 * time.Second
	defaultIGLLoadPoll       = 10 * time.Millisecond
	defaultIGLJitterFraction = 0.2
)

type iglPassRunner interface {
	RunOnce(context.Context) (generalization.PassResult, error)
}

type IGLSchedulerConfig struct {
	BaseInterval       time.Duration
	QuietFor           time.Duration
	MinPublishInterval time.Duration
	MaxBackoff         time.Duration
	LoadPoll           time.Duration
	Leader             scopeLeader
	LeaderScopes       []string
	Now                func() time.Time
	Sleep              func(context.Context, time.Duration) error
	Jitter             func(time.Duration) time.Duration
	Logger             interface{ Printf(string, ...any) }
}

type IGLScheduler struct {
	runner      iglPassRunner
	load        *LoadTracker
	cfg         IGLSchedulerConfig
	lastPublish time.Time
}

func NewIGLScheduler(runner iglPassRunner, load *LoadTracker, cfg IGLSchedulerConfig) *IGLScheduler {
	cfg = normalizeIGLSchedulerConfig(cfg)
	return &IGLScheduler{runner: runner, load: load, cfg: cfg}
}

func (s *IGLScheduler) Run(ctx context.Context) error {
	if s == nil || s.runner == nil {
		return fmt.Errorf("generalization IGL scheduler: nil runner")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	busyStreak := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !s.leadsAnyScope() {
			if err := s.sleep(ctx, s.cfg.BaseInterval); err != nil {
				return nil
			}
			continue
		}
		if !s.idle() {
			delay := s.backoffDelay(busyStreak)
			busyStreak++
			if err := s.sleep(ctx, delay); err != nil {
				return nil
			}
			continue
		}
		if wait := s.publishWait(); wait > 0 {
			if wait > s.cfg.BaseInterval {
				wait = s.cfg.BaseInterval
			}
			if err := s.sleep(ctx, wait); err != nil {
				return nil
			}
			continue
		}
		result, err, canceledByLoad := s.runPass(ctx)
		if passPublished(result) {
			s.lastPublish = s.now()
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			if canceledByLoad || errors.Is(err, context.Canceled) && s.load != nil && s.load.loaded() {
				delay := s.backoffDelay(busyStreak)
				busyStreak++
				if err := s.sleep(ctx, delay); err != nil {
					return nil
				}
				continue
			}
			s.log("generalization IGL pass error: %v", err)
			if err := s.sleep(ctx, s.cfg.BaseInterval); err != nil {
				return nil
			}
			continue
		}
		busyStreak = 0
		if err := s.sleep(ctx, s.cfg.BaseInterval); err != nil {
			return nil
		}
	}
}

func (s *IGLScheduler) runPass(ctx context.Context) (generalization.PassResult, error, bool) {
	var (
		passCtx context.Context
		cancel  context.CancelFunc
		ok      bool
	)
	if s.load != nil {
		passCtx, cancel, ok = s.load.BeginIdlePass(ctx, s.cfg.QuietFor)
		if !ok {
			return generalization.PassResult{}, context.Canceled, true
		}
	} else {
		passCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	var canceledByLoad atomic.Bool
	if s.load != nil {
		go func() {
			ticker := time.NewTicker(s.cfg.LoadPoll)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-passCtx.Done():
					return
				case <-ticker.C:
					if s.load.loaded() {
						canceledByLoad.Store(true)
						cancel()
						return
					}
				}
			}
		}()
	}
	result, err := s.runner.RunOnce(passCtx)
	if passCtx.Err() != nil && ctx.Err() == nil {
		canceledByLoad.Store(true)
		if err == nil {
			err = passCtx.Err()
		}
	}
	return result, err, canceledByLoad.Load()
}

func (s *IGLScheduler) idle() bool {
	return s.load == nil || s.load.Idle(s.cfg.QuietFor)
}

func (s *IGLScheduler) leadsAnyScope() bool {
	if s.cfg.Leader == nil || len(s.cfg.LeaderScopes) == 0 {
		return true
	}
	leads := false
	for _, scope := range s.cfg.LeaderScopes {
		if s.cfg.Leader.Holds(scope) {
			leads = true
			continue
		}
		acquired, err := s.cfg.Leader.TryAcquire(scope)
		if err != nil {
			s.log("leader election scope=%s error=%v", scope, err)
			continue
		}
		if acquired && s.cfg.Leader.Holds(scope) {
			leads = true
		}
	}
	return leads
}

func (s *IGLScheduler) publishWait() time.Duration {
	if s.cfg.MinPublishInterval <= 0 || s.lastPublish.IsZero() {
		return 0
	}
	elapsed := s.now().Sub(s.lastPublish)
	if elapsed >= s.cfg.MinPublishInterval {
		return 0
	}
	return s.cfg.MinPublishInterval - elapsed
}

func (s *IGLScheduler) backoffDelay(streak int) time.Duration {
	delay := s.cfg.BaseInterval
	for i := 0; i < streak; i++ {
		if delay >= s.cfg.MaxBackoff {
			delay = s.cfg.MaxBackoff
			break
		}
		if delay > s.cfg.MaxBackoff/2 {
			delay = s.cfg.MaxBackoff
			break
		}
		delay *= 2
	}
	if delay > s.cfg.MaxBackoff {
		delay = s.cfg.MaxBackoff
	}
	return s.cfg.Jitter(delay)
}

func (s *IGLScheduler) sleep(ctx context.Context, delay time.Duration) error {
	return s.cfg.Sleep(ctx, delay)
}

func (s *IGLScheduler) now() time.Time {
	return s.cfg.Now()
}

func (s *IGLScheduler) log(format string, args ...any) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Printf(format, args...)
	}
}

func normalizeIGLSchedulerConfig(cfg IGLSchedulerConfig) IGLSchedulerConfig {
	if cfg.BaseInterval <= 0 {
		cfg.BaseInterval = time.Minute
	}
	if cfg.QuietFor < 0 {
		cfg.QuietFor = defaultIGLQuiet
	}
	if cfg.MinPublishInterval < 0 {
		cfg.MinPublishInterval = defaultIGLMinPublish
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = cfg.BaseInterval * 8
	}
	if cfg.MaxBackoff < cfg.BaseInterval {
		cfg.MaxBackoff = cfg.BaseInterval
	}
	if cfg.LoadPoll <= 0 {
		cfg.LoadPoll = defaultIGLLoadPoll
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}
	if cfg.Jitter == nil {
		cfg.Jitter = defaultIGLJitter
	}
	return cfg
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultIGLJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return delay
	}
	span := time.Duration(float64(delay) * defaultIGLJitterFraction)
	if span <= 0 {
		return delay
	}
	max := int64(span)*2 + 1
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		return delay
	}
	offset := time.Duration(n.Int64() - int64(span))
	jittered := delay + offset
	if jittered <= 0 {
		return time.Millisecond
	}
	return jittered
}

func passPublished(result generalization.PassResult) bool {
	for _, scope := range result.Scopes {
		if scope.Published {
			return true
		}
	}
	return false
}
