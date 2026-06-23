package generalization

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

type Logger interface {
	Printf(format string, args ...any)
}

type SnapshotWriter interface {
	Write(ctx context.Context, path string, scope string, graph *Graph) error
}

type SnapshotValidator func(ctx context.Context, path string, graph *Graph) error

type Scope struct {
	Config Config
	Name   string
}

type StatsDelta struct {
	Nodes     int `json:"nodes"`
	Relations int `json:"relations"`
}

type ScopeResult struct {
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Duration   time.Duration `json:"duration"`
	Stats      Stats         `json:"stats"`
	Delta      StatsDelta    `json:"delta"`
	Scope      string        `json:"scope"`
	Path       string        `json:"path"`
	LastError  string        `json:"last_error,omitempty"`
	Published  bool          `json:"published"`
}

type PassResult struct {
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Duration   time.Duration `json:"duration"`
	Scopes     []ScopeResult `json:"scopes"`
	LastError  string        `json:"last_error,omitempty"`
}

type Publisher struct {
	Source   reasoning.GraphQuerier
	Registry *reasoning.PredicateRegistry
	Writer   SnapshotWriter
	Validate SnapshotValidator
	TempPath func(finalPath string) string
}

type Loop struct {
	Publisher *Publisher
	Metrics   *Metrics
	Logger    Logger
	Scopes    []Scope
	Interval  time.Duration
	OutDir    string
	previous  map[string]Stats
}

func (p *Publisher) Publish(ctx context.Context, outDir string, scope Scope, previous *Stats) (ScopeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	name, cfg, err := normalizeScope(scope)
	result := ScopeResult{StartedAt: start, Scope: name}
	if err != nil {
		return finishScopeResult(result, err), err
	}
	result.Scope = name
	if p == nil || p.Source == nil {
		err := fmt.Errorf("generalization IGL: nil source")
		return finishScopeResult(result, err), err
	}
	if p.Writer == nil {
		err := fmt.Errorf("generalization IGL: nil writer")
		return finishScopeResult(result, err), err
	}
	path, err := SnapshotPath(outDir, name)
	if err != nil {
		return finishScopeResult(result, err), err
	}
	result.Path = path
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		err = fmt.Errorf("generalization IGL: create output dir: %w", err)
		return finishScopeResult(result, err), err
	}

	graph, err := Build(ctx, p.Source, p.Registry, cfg)
	if err != nil {
		return finishScopeResult(result, err), err
	}
	if err := ctx.Err(); err != nil {
		return finishScopeResult(result, err), err
	}
	result.Stats = copyStats(graph.Stats)
	result.Delta = diffStats(graph.Stats, previous)

	tmpPath := p.tempPath(path)
	if err := ctx.Err(); err != nil {
		return finishScopeResult(result, err), err
	}
	if err := os.RemoveAll(tmpPath); err != nil {
		err = fmt.Errorf("generalization IGL: clear temp path: %w", err)
		return finishScopeResult(result, err), err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(tmpPath)
		}
	}()

	if err := p.Writer.Write(ctx, tmpPath, name, graph); err != nil {
		return finishScopeResult(result, err), err
	}
	if err := ctx.Err(); err != nil {
		return finishScopeResult(result, err), err
	}
	if p.Validate != nil {
		if err := p.Validate(ctx, tmpPath, graph); err != nil {
			return finishScopeResult(result, err), err
		}
	}
	if err := ctx.Err(); err != nil {
		return finishScopeResult(result, err), err
	}
	if err := publishSnapshot(ctx, tmpPath, path); err != nil {
		return finishScopeResult(result, err), err
	}
	published = true
	result.Published = true
	return finishScopeResult(result, nil), nil
}

func (l *Loop) RunOnce(ctx context.Context) (PassResult, error) {
	return l.runPass(ctx)
}

func (l *Loop) Run(ctx context.Context) error {
	if l.Interval <= 0 {
		l.Interval = time.Minute
	}
	if _, err := l.runPass(ctx); err != nil && ctx.Err() == nil {
		l.log("generalization IGL pass error: %v", err)
	}
	if ctx.Err() != nil {
		return nil
	}
	ticker := time.NewTicker(l.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := l.runPass(ctx); err != nil && ctx.Err() == nil {
				l.log("generalization IGL pass error: %v", err)
			}
			if ctx.Err() != nil {
				return nil
			}
		}
	}
}

func SnapshotPath(outDir string, scope string) (string, error) {
	name, err := cleanScopeName(scope)
	if err != nil {
		return "", err
	}
	outDir = strings.TrimSpace(outDir)
	if outDir == "" {
		return "", fmt.Errorf("generalization IGL: empty output dir")
	}
	return filepath.Join(outDir, name+".g_g.ladybug"), nil
}

func (l *Loop) runPass(ctx context.Context) (PassResult, error) {
	start := time.Now()
	result := PassResult{StartedAt: start}
	if l == nil {
		err := fmt.Errorf("generalization IGL: nil loop")
		return finishPassResult(result, err), err
	}
	if l.Publisher == nil {
		err := fmt.Errorf("generalization IGL: nil publisher")
		return finishPassResult(result, err), err
	}
	if len(l.Scopes) == 0 {
		err := fmt.Errorf("generalization IGL: no scopes")
		return finishPassResult(result, err), err
	}
	if l.previous == nil {
		l.previous = map[string]Stats{}
	}
	var errs []error
	for _, scope := range l.Scopes {
		name, _, scopeErr := normalizeScope(scope)
		var prior *Stats
		if scopeErr == nil {
			if stats, ok := l.previous[name]; ok {
				copy := stats
				prior = &copy
			}
		}
		scopeResult, err := l.Publisher.Publish(ctx, l.OutDir, scope, prior)
		result.Scopes = append(result.Scopes, scopeResult)
		if l.Metrics != nil {
			l.Metrics.RecordScope(scopeResult)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", scopeResult.Scope, err))
			l.log("generalization IGL scope=%s error=%v duration=%s", scopeResult.Scope, err, scopeResult.Duration)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		l.previous[scopeResult.Scope] = copyStats(scopeResult.Stats)
		l.log("generalization IGL scope=%s nodes=%d relations=%d delta_nodes=%+d delta_relations=%+d duration=%s path=%s",
			scopeResult.Scope,
			scopeResult.Stats.NodeCount,
			scopeResult.Stats.RelationCount,
			scopeResult.Delta.Nodes,
			scopeResult.Delta.Relations,
			scopeResult.Duration,
			scopeResult.Path,
		)
	}
	err := errors.Join(errs...)
	result = finishPassResult(result, err)
	if l.Metrics != nil {
		l.Metrics.RecordPass(result)
	}
	return result, err
}

func (l *Loop) log(format string, args ...any) {
	logger := l.Logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf(format, args...)
}

func normalizeScope(scope Scope) (string, Config, error) {
	cfg := scope.Config.withDefaults()
	name := strings.TrimSpace(scope.Name)
	if name == "" {
		name = strings.TrimSpace(cfg.Scope)
	}
	if name == "" {
		return "", Config{}, fmt.Errorf("generalization IGL: empty scope")
	}
	clean, err := cleanScopeName(name)
	if err != nil {
		return "", Config{}, err
	}
	if cfg.Scope == "" {
		cfg.Scope = clean
	}
	return clean, cfg, nil
}

func cleanScopeName(scope string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", fmt.Errorf("generalization IGL: empty scope")
	}
	if scope == "." || scope == ".." || strings.ContainsAny(scope, `/\`) {
		return "", fmt.Errorf("generalization IGL: invalid scope %q", scope)
	}
	return scope, nil
}

func (p *Publisher) tempPath(finalPath string) string {
	if p != nil && p.TempPath != nil {
		return p.TempPath(finalPath)
	}
	dir := filepath.Dir(finalPath)
	base := filepath.Base(finalPath)
	return filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d.%d", base, os.Getpid(), time.Now().UnixNano()))
}

func finishScopeResult(result ScopeResult, err error) ScopeResult {
	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	if err != nil {
		result.LastError = err.Error()
	}
	return result
}

func finishPassResult(result PassResult, err error) PassResult {
	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	if err != nil {
		result.LastError = err.Error()
	}
	return result
}

func publishSnapshot(ctx context.Context, tmpPath string, finalPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return fmt.Errorf("generalization IGL: stat temp snapshot: %w", err)
	}
	if !info.IsDir() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Rename(tmpPath, finalPath); err != nil {
			return fmt.Errorf("generalization IGL: publish snapshot: %w", err)
		}
		return nil
	}

	versionPath := strings.Replace(tmpPath, ".tmp.", ".snap.", 1)
	if versionPath == tmpPath {
		versionPath = tmpPath + ".snap"
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, versionPath); err != nil {
		return fmt.Errorf("generalization IGL: stage snapshot: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(versionPath)
		}
	}()

	linkPath := tmpPath + ".link"
	_ = os.Remove(linkPath)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Symlink(filepath.Base(versionPath), linkPath); err != nil {
		return fmt.Errorf("generalization IGL: create snapshot link: %w", err)
	}
	defer func() {
		if !published {
			_ = os.Remove(linkPath)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(linkPath, finalPath); err != nil {
		return fmt.Errorf("generalization IGL: publish snapshot link: %w", err)
	}
	published = true
	return nil
}

func diffStats(current Stats, previous *Stats) StatsDelta {
	if previous == nil {
		return StatsDelta{Nodes: current.NodeCount, Relations: current.RelationCount}
	}
	return StatsDelta{
		Nodes:     current.NodeCount - previous.NodeCount,
		Relations: current.RelationCount - previous.RelationCount,
	}
}

func copyStats(in Stats) Stats {
	out := in
	if in.ParentLevelCounts != nil {
		out.ParentLevelCounts = make(map[int]int, len(in.ParentLevelCounts))
		for key, value := range in.ParentLevelCounts {
			out.ParentLevelCounts[key] = value
		}
	}
	return out
}

type Metrics struct {
	mu        sync.RWMutex
	startedAt time.Time
	lastPass  PassResult
	scopes    map[string]ScopeResult
	passes    uint64
}

type MetricsSnapshot struct {
	StartedAt time.Time     `json:"started_at"`
	LastPass  PassResult    `json:"last_pass"`
	Scopes    []ScopeResult `json:"scopes"`
	Passes    uint64        `json:"passes"`
}

func NewMetrics() *Metrics {
	return &Metrics{startedAt: time.Now(), scopes: map[string]ScopeResult{}}
}

func (m *Metrics) RecordScope(result ScopeResult) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startedAt.IsZero() {
		m.startedAt = time.Now()
	}
	result.Stats = copyStats(result.Stats)
	m.scopes[result.Scope] = result
}

func (m *Metrics) RecordPass(result PassResult) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startedAt.IsZero() {
		m.startedAt = time.Now()
	}
	for i := range result.Scopes {
		result.Scopes[i].Stats = copyStats(result.Scopes[i].Stats)
	}
	m.lastPass = result
	m.passes++
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	scopes := make([]ScopeResult, 0, len(m.scopes))
	for _, result := range m.scopes {
		result.Stats = copyStats(result.Stats)
		scopes = append(scopes, result)
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].Scope < scopes[j].Scope })
	last := m.lastPass
	if m.lastPass.Scopes != nil {
		last.Scopes = make([]ScopeResult, len(m.lastPass.Scopes))
		copy(last.Scopes, m.lastPass.Scopes)
		for i := range last.Scopes {
			last.Scopes[i].Stats = copyStats(last.Scopes[i].Stats)
		}
	}
	return MetricsSnapshot{
		StartedAt: m.startedAt,
		LastPass:  last,
		Scopes:    scopes,
		Passes:    m.passes,
	}
}
