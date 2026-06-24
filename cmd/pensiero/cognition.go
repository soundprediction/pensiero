package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const (
	ThoughtProofPrecompute   ThoughtType = "proof-precompute"
	ThoughtContradictionHunt ThoughtType = "contradiction-hunt"
	ThoughtHypothesisTest    ThoughtType = "hypothesis-test"

	defaultCognitionInterval       = 10 * time.Second
	defaultCognitionWindowBudget   = 250 * time.Millisecond
	defaultCognitionThoughtTimeout = 150 * time.Millisecond
	defaultCognitionMaxThoughts    = 2
	defaultCognitionLoadPoll       = 10 * time.Millisecond
)

type ThoughtType string

type Thought struct {
	Type      ThoughtType     `json:"type"`
	Claim     reasoning.Claim `json:"claim"`
	Source    string          `json:"source"`
	Rationale string          `json:"rationale"`
	HotKey    *QueryHotKey    `json:"hot_key,omitempty"`
	Meta      map[string]any  `json:"meta,omitempty"`
}

type cognitionThoughtSelector interface {
	Next(context.Context) (Thought, bool, error)
}

type cognitionThoughtExecutor interface {
	Execute(context.Context, Thought) error
}

type CognitionSchedulerConfig struct {
	BaseInterval  time.Duration
	QuietFor      time.Duration
	WindowBudget  time.Duration
	ThoughtBudget time.Duration
	MaxThoughts   int
	LoadPoll      time.Duration
	Now           func() time.Time
	Sleep         func(context.Context, time.Duration) error
	Jitter        func(time.Duration) time.Duration
	Logger        interface{ Printf(string, ...any) }
	Thinking      *thinkingState
}

type CognitionScheduler struct {
	selector cognitionThoughtSelector
	executor cognitionThoughtExecutor
	load     *LoadTracker
	cfg      CognitionSchedulerConfig
}

func NewCognitionScheduler(selector cognitionThoughtSelector, executor cognitionThoughtExecutor, load *LoadTracker, cfg CognitionSchedulerConfig) *CognitionScheduler {
	return &CognitionScheduler{
		selector: selector,
		executor: executor,
		load:     load,
		cfg:      normalizeCognitionSchedulerConfig(cfg),
	}
}

func (s *CognitionScheduler) Run(ctx context.Context) error {
	if s == nil || s.selector == nil || s.executor == nil {
		return fmt.Errorf("cognition scheduler: nil selector or executor")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.cfg.Thinking.SetCognitionActive(true)
	defer s.cfg.Thinking.SetCognitionActive(false)
	busyStreak := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !s.idle() {
			s.cfg.Thinking.SetGatedWaitingForIdle(true)
			delay := s.backoffDelay(busyStreak)
			busyStreak++
			if err := s.sleep(ctx, delay); err != nil {
				return nil
			}
			continue
		}
		s.cfg.Thinking.SetGatedWaitingForIdle(false)
		ran, err, canceledByLoad := s.runIdleWindow(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			if canceledByLoad || errors.Is(err, context.Canceled) && s.load != nil && s.load.loaded() {
				s.cfg.Thinking.SetGatedWaitingForIdle(true)
				delay := s.backoffDelay(busyStreak)
				busyStreak++
				if err := s.sleep(ctx, delay); err != nil {
					return nil
				}
				continue
			}
			s.log("cognition error: %v", err)
		}
		if ran > 0 {
			busyStreak = 0
		}
		if err := s.sleep(ctx, s.cfg.BaseInterval); err != nil {
			return nil
		}
	}
}

func (s *CognitionScheduler) runIdleWindow(ctx context.Context) (int, error, bool) {
	var (
		passCtx context.Context
		cancel  context.CancelFunc
		ok      bool
	)
	if s.load != nil {
		passCtx, cancel, ok = s.load.BeginIdlePass(ctx, s.cfg.QuietFor)
		if !ok {
			s.cfg.Thinking.SetGatedWaitingForIdle(true)
			return 0, context.Canceled, true
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

	deadline := s.now().Add(s.cfg.WindowBudget)
	ran := 0
	for ran < s.cfg.MaxThoughts {
		if err := passCtx.Err(); err != nil {
			return ran, err, canceledByLoad.Load()
		}
		if !deadline.After(s.now()) {
			return ran, nil, canceledByLoad.Load()
		}
		if s.load != nil {
			if err := s.load.Yield(passCtx); err != nil {
				canceledByLoad.Store(true)
				return ran, err, true
			}
		}
		thought, ok, err := s.selector.Next(passCtx)
		if err != nil {
			return ran, err, canceledByLoad.Load()
		}
		if !ok {
			return ran, nil, canceledByLoad.Load()
		}
		budget := s.cfg.ThoughtBudget
		if remaining := deadline.Sub(s.now()); remaining > 0 && remaining < budget {
			budget = remaining
		}
		thoughtCtx, thoughtCancel := context.WithTimeout(passCtx, budget)
		startedAt := s.now()
		s.cfg.Thinking.BeginThought(thought, startedAt)
		err = s.executor.Execute(thoughtCtx, thought)
		duration := s.now().Sub(startedAt)
		outcome := classifyCognitionOutcome(thoughtCtx, err)
		s.cfg.Thinking.FinishThought(thought, outcome, startedAt, duration)
		thoughtCancel()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || passCtx.Err() != nil {
				return ran, err, canceledByLoad.Load() || passCtx.Err() != nil
			}
			s.log("cognition thought type=%s source=%s error=%v", thought.Type, thought.Source, err)
		}
		ran++
	}
	return ran, passCtx.Err(), canceledByLoad.Load()
}

func (s *CognitionScheduler) idle() bool {
	return s.load == nil || s.load.Idle(s.cfg.QuietFor)
}

func (s *CognitionScheduler) backoffDelay(streak int) time.Duration {
	delay := s.cfg.BaseInterval
	for i := 0; i < streak; i++ {
		if delay >= s.cfg.BaseInterval*8 {
			break
		}
		delay *= 2
	}
	return s.cfg.Jitter(delay)
}

func (s *CognitionScheduler) sleep(ctx context.Context, delay time.Duration) error {
	return s.cfg.Sleep(ctx, delay)
}

func (s *CognitionScheduler) now() time.Time {
	return s.cfg.Now()
}

func (s *CognitionScheduler) log(format string, args ...any) {
	if s.cfg.Logger != nil {
		s.cfg.Logger.Printf(format, args...)
	}
}

func normalizeCognitionSchedulerConfig(cfg CognitionSchedulerConfig) CognitionSchedulerConfig {
	if cfg.BaseInterval <= 0 {
		cfg.BaseInterval = defaultCognitionInterval
	}
	if cfg.QuietFor < 0 {
		cfg.QuietFor = defaultIGLQuiet
	}
	if cfg.WindowBudget <= 0 {
		cfg.WindowBudget = defaultCognitionWindowBudget
	}
	if cfg.ThoughtBudget <= 0 {
		cfg.ThoughtBudget = defaultCognitionThoughtTimeout
	}
	if cfg.MaxThoughts <= 0 {
		cfg.MaxThoughts = defaultCognitionMaxThoughts
	}
	if cfg.LoadPoll <= 0 {
		cfg.LoadPoll = defaultCognitionLoadPoll
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

type ThoughtEngine struct {
	Reasoner    reasoning.Reasoner
	Questions   reasoning.QuestionSink
	Unconfirmed *unconfirmedStore
	Logger      interface{ Printf(string, ...any) }
}

func (e *ThoughtEngine) Execute(ctx context.Context, thought Thought) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil || e.Reasoner == nil {
		return nil
	}
	claim := normalizeQuestionClaim(thought.Claim)
	if claimDedupeKey(claim) == "" {
		return nil
	}
	switch thought.Type {
	case ThoughtProofPrecompute:
		_, err := e.Reasoner.Entails(ctx, claim)
		return err
	case ThoughtContradictionHunt:
		ok, proof, err := e.Reasoner.Contradicts(ctx, claim)
		if err != nil || !ok {
			return err
		}
		return e.emitQuestion(ctx, claim, contradictionRationale(proof), expectedGainFromProof(proof, 0.9))
	case ThoughtHypothesisTest:
		result, err := e.Reasoner.Entails(ctx, claim)
		if err != nil {
			return err
		}
		switch result.Verdict {
		case reasoning.VerdictEntailed:
			confidence := result.Confidence
			if confidence == 0 && result.Best != nil {
				confidence = result.Best.Confidence
			}
			return e.recordSpeculative(ctx, claim, confidence, string(thought.Type))
		case reasoning.VerdictContradicted:
			return e.emitQuestion(ctx, claim, "candidate claim is contradicted by current evidence", expectedGainFromProof(result.Best, 0.85))
		default:
			return e.emitQuestion(ctx, claim, "candidate claim is unsupported and would improve coverage if resolved", unsupportedQuestionGain(thought, result))
		}
	default:
		return nil
	}
}

func (e *ThoughtEngine) emitQuestion(ctx context.Context, claim reasoning.Claim, rationale string, gain float64) error {
	if e == nil || e.Questions == nil {
		return ctx.Err()
	}
	return e.Questions.Emit(ctx, []reasoning.Question{{
		Claim:        claim,
		Rationale:    rationale,
		ExpectedGain: gain,
	}})
}

func (e *ThoughtEngine) recordSpeculative(ctx context.Context, claim reasoning.Claim, confidence float64, provenance string) error {
	if e == nil || e.Unconfirmed == nil {
		return ctx.Err()
	}
	return e.Unconfirmed.Record(ctx, SpeculativeFact{
		Claim:      claim,
		Confidence: confidence,
		Provenance: provenance,
	})
}

func contradictionRationale(proof *reasoning.Proof) string {
	if proof == nil || proof.Predicate == "" {
		return "claim conflicts with disjoint evidence; needs resolution"
	}
	return "claim conflicts with disjoint predicate " + proof.Predicate + "; needs resolution"
}

func expectedGainFromProof(proof *reasoning.Proof, fallback float64) float64 {
	if proof == nil || proof.Confidence == 0 {
		return fallback
	}
	return clamp01(proof.Confidence)
}

func expectedGainFromResult(result reasoning.EntailResult, fallback float64) float64 {
	if result.Confidence > 0 {
		return clamp01(result.Confidence)
	}
	if result.Best != nil && result.Best.Confidence > 0 {
		return clamp01(result.Best.Confidence)
	}
	return fallback
}

// unsupportedQuestionGain ranks an unsupported candidate. Unsupported claims
// have no proof confidence, so a flat fallback made every question look equally
// valuable. When the candidate source attached a connectivity-based gain
// (Thought.Meta["expected_gain"], e.g. the neighborhood source), use it so
// questions about central entities outrank peripheral ones.
func unsupportedQuestionGain(thought Thought, result reasoning.EntailResult) float64 {
	if result.Confidence > 0 {
		return clamp01(result.Confidence)
	}
	if result.Best != nil && result.Best.Confidence > 0 {
		return clamp01(result.Best.Confidence)
	}
	if thought.Meta != nil {
		if gain, ok := thought.Meta["expected_gain"].(float64); ok && gain > 0 {
			return clamp01(gain)
		}
	}
	return 0.35
}
