package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

const (
	defaultPredicateInventorySample   = 10000
	defaultPredicateInventoryInterval = 5 * time.Minute
)

type PredicateInventoryConfig struct {
	SampleLimit     int
	RefreshInterval time.Duration
	QuietFor        time.Duration
	Load            *LoadTracker
	Now             func() time.Time
	Logger          *log.Logger
}

type PredicateInventoryStat struct {
	Predicate     string           `json:"predicate"`
	Canonical     string           `json:"canonical"`
	Declared      bool             `json:"declared"`
	ObservedCount int64            `json:"observed_count"`
	HeadTypeDist  map[string]int64 `json:"head_type_dist"`
	TailTypeDist  map[string]int64 `json:"tail_type_dist"`
}

type PredicateInventorySnapshot struct {
	UpdatedAt   time.Time                `json:"updated_at"`
	Watermark   string                   `json:"watermark"`
	SampleLimit int                      `json:"sample_limit"`
	Predicates  []PredicateInventoryStat `json:"predicates"`
	LastError   string                   `json:"last_error,omitempty"`
}

type predicateInventory struct {
	graph           reasoning.GraphQuerier
	reg             *reasoning.PredicateRegistry
	sampleLimit     int
	refreshInterval time.Duration
	quietFor        time.Duration
	load            *LoadTracker
	now             func() time.Time
	logger          *log.Logger

	startOnce sync.Once
	refreshMu sync.Mutex
	mu        sync.Mutex
	snapshot  PredicateInventorySnapshot
	disabled  bool
}

func newPredicateInventory(graph reasoning.GraphQuerier, reg *reasoning.PredicateRegistry, cfg PredicateInventoryConfig) *predicateInventory {
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = defaultPredicateInventoryInterval
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	sampleLimit := cfg.SampleLimit
	if sampleLimit < 0 {
		sampleLimit = 0
	}
	inv := &predicateInventory{
		graph:           graph,
		reg:             reg,
		sampleLimit:     sampleLimit,
		refreshInterval: cfg.RefreshInterval,
		quietFor:        cfg.QuietFor,
		load:            cfg.Load,
		now:             cfg.Now,
		logger:          cfg.Logger,
		disabled:        sampleLimit <= 0 || graph == nil,
		snapshot: PredicateInventorySnapshot{
			SampleLimit: sampleLimit,
		},
	}
	if inv.disabled {
		return inv
	}
	return inv
}

func (p *predicateInventory) Start(ctx context.Context) {
	if p == nil || p.disabled {
		return
	}
	p.startOnce.Do(func() {
		go p.run(ctx)
	})
}

func (p *predicateInventory) run(ctx context.Context) {
	_, _ = p.RefreshIfIdle(ctx)
	ticker := time.NewTicker(p.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = p.RefreshIfIdle(ctx)
		}
	}
}

func (p *predicateInventory) RefreshIfIdle(ctx context.Context) (bool, error) {
	if p == nil || p.disabled {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if p.load != nil {
		passCtx, stop, ok := p.load.BeginIdlePass(ctx, p.quietFor)
		if !ok {
			return false, nil
		}
		defer stop()
		ctx = passCtx
	}
	if err := p.Refresh(ctx); err != nil {
		p.log("predicate inventory refresh error=%v", err)
		return true, err
	}
	return true, nil
}

func (p *predicateInventory) Refresh(ctx context.Context) error {
	if p == nil || p.disabled {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()

	schema, err := p.loadSchema(ctx)
	if err != nil {
		p.setError(err)
		return err
	}

	params := map[string]any{"limit": p.sampleLimit}
	query := predicateInventoryQuery(schema, p.sampleLimit)
	rows, err := p.graph.Query(ctx, query, params)
	if err != nil {
		p.setError(err)
		return err
	}
	snapshot := buildPredicateInventorySnapshot(p.reg, rows, p.sampleLimit, p.now())
	p.mu.Lock()
	p.snapshot = snapshot
	p.mu.Unlock()
	p.log("predicate inventory predicates=%d undeclared=%d sample_limit=%d", len(snapshot.Predicates), undeclaredPredicateCount(snapshot.Predicates), p.sampleLimit)
	return nil
}

func (p *predicateInventory) Snapshot() PredicateInventorySnapshot {
	if p == nil {
		return PredicateInventorySnapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return clonePredicateInventorySnapshot(p.snapshot)
}

func (p *predicateInventory) setError(err error) {
	if p == nil || err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.snapshot = PredicateInventorySnapshot{
		UpdatedAt:   now,
		Watermark:   now.UTC().Format(time.RFC3339Nano),
		SampleLimit: p.sampleLimit,
		LastError:   err.Error(),
	}
}

func (p *predicateInventory) log(format string, args ...any) {
	if p != nil && p.logger != nil {
		p.logger.Printf(format, args...)
	}
}

type predicateInventorySchema struct {
	predicateColumn    string
	entityLabelsColumn string
}

func (p *predicateInventory) loadSchema(ctx context.Context) (predicateInventorySchema, error) {
	relColumns, err := p.tableColumns(ctx, "RelatesToNode_")
	if err != nil {
		return predicateInventorySchema{}, fmt.Errorf("predicate inventory schema RelatesToNode_: %w", err)
	}
	predicateColumn := firstInventoryColumn(relColumns, "name", "predicate", "canonical")
	if predicateColumn == "" {
		return predicateInventorySchema{}, fmt.Errorf("predicate inventory schema RelatesToNode_: no predicate column among name, predicate, canonical")
	}
	entityColumns, err := p.tableColumns(ctx, "Entity")
	if err != nil {
		return predicateInventorySchema{}, fmt.Errorf("predicate inventory schema Entity: %w", err)
	}
	return predicateInventorySchema{
		predicateColumn:    predicateColumn,
		entityLabelsColumn: firstInventoryColumn(entityColumns, "labels"),
	}, nil
}

func (p *predicateInventory) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := p.graph.Query(ctx, predicateInventoryTableInfoQuery(table), nil)
	if err != nil {
		return nil, err
	}
	return inventoryColumnSet(rows), nil
}

func predicateInventoryTableInfoQuery(table string) string {
	table = strings.ReplaceAll(table, `'`, `\'`)
	return fmt.Sprintf("CALL TABLE_INFO('%s') RETURN *", table)
}

func predicateInventoryQuery(schema predicateInventorySchema, limit int) string {
	if limit < 0 {
		limit = 0
	}
	returns := []string{fmt.Sprintf("rel.%s AS predicate", schema.predicateColumn)}
	if schema.entityLabelsColumn != "" {
		returns = append(returns,
			fmt.Sprintf("head.%s AS head_types", schema.entityLabelsColumn),
			fmt.Sprintf("tail.%s AS tail_types", schema.entityLabelsColumn),
		)
	}
	return fmt.Sprintf(`MATCH (head:Entity)-[:RELATES_TO]->(rel:RelatesToNode_)-[:RELATES_TO]->(tail:Entity)
RETURN %s
LIMIT %d`, strings.Join(returns, ",\n       "), limit)
}

func buildPredicateInventorySnapshot(reg *reasoning.PredicateRegistry, rows []map[string]any, sampleLimit int, now time.Time) PredicateInventorySnapshot {
	if sampleLimit < 0 {
		sampleLimit = 0
	}
	if sampleLimit > 0 && len(rows) > sampleLimit {
		rows = rows[:sampleLimit]
	}
	byPredicate := map[string]*PredicateInventoryStat{}
	for _, row := range rows {
		raw := strings.TrimSpace(firstInventoryString(row, "predicate", "name", "canonical"))
		if raw == "" {
			continue
		}
		canonical, declared := canonicalInventoryPredicate(reg, raw)
		key := inventoryKey(canonical)
		stat := byPredicate[key]
		if stat == nil {
			stat = &PredicateInventoryStat{
				Predicate:    canonical,
				Canonical:    canonical,
				Declared:     declared,
				HeadTypeDist: map[string]int64{},
				TailTypeDist: map[string]int64{},
			}
			byPredicate[key] = stat
		}
		stat.ObservedCount++
		if declared {
			stat.Declared = true
		}
		for _, typ := range inventoryTypes(row, "head_types", "head_labels", "head_type", "head_label") {
			stat.HeadTypeDist[typ]++
		}
		for _, typ := range inventoryTypes(row, "tail_types", "tail_labels", "tail_type", "tail_label") {
			stat.TailTypeDist[typ]++
		}
	}

	predicates := make([]PredicateInventoryStat, 0, len(byPredicate))
	for _, stat := range byPredicate {
		predicates = append(predicates, clonePredicateInventoryStat(*stat))
	}
	sort.Slice(predicates, func(i, j int) bool {
		if inventoryKey(predicates[i].Canonical) == inventoryKey(predicates[j].Canonical) {
			return predicates[i].Predicate < predicates[j].Predicate
		}
		return inventoryKey(predicates[i].Canonical) < inventoryKey(predicates[j].Canonical)
	})
	watermark := now.UTC().Format(time.RFC3339Nano)
	return PredicateInventorySnapshot{
		UpdatedAt:   now,
		Watermark:   watermark,
		SampleLimit: sampleLimit,
		Predicates:  predicates,
	}
}

func canonicalInventoryPredicate(reg *reasoning.PredicateRegistry, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if reg == nil {
		return raw, false
	}
	meta, ok := reg.Canonical(raw)
	if !ok || strings.TrimSpace(meta.Canonical) == "" {
		return raw, false
	}
	return strings.TrimSpace(meta.Canonical), true
}

func inventoryTypes(row map[string]any, keys ...string) []string {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			return distinctInventoryTypes(value)
		}
	}
	return nil
}

func distinctInventoryTypes(value any) []string {
	values := inventoryStringSlice(value)
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := inventoryKey(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return inventoryKey(out[i]) < inventoryKey(out[j])
	})
	return out
}

func inventoryStringSlice(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []string:
		return append([]string{}, v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
			return []string{s}
		}
		return nil
	}
}

func firstInventoryString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key]; ok {
			if s := strings.TrimSpace(fmt.Sprint(value)); s != "" {
				return s
			}
		}
	}
	return ""
}

func inventoryColumnSet(rows []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, row := range rows {
		for _, value := range row {
			if key := inventorySchemaKey(fmt.Sprint(value)); key != "" {
				out[key] = true
			}
		}
	}
	return out
}

func firstInventoryColumn(columns map[string]bool, candidates ...string) string {
	for _, candidate := range candidates {
		if columns[inventorySchemaKey(candidate)] {
			return candidate
		}
	}
	return ""
}

func inventorySchemaKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.Trim(value, "`\"'")
	if dot := strings.LastIndexByte(value, '.'); dot >= 0 {
		value = value[dot+1:]
	}
	return value
}

func clonePredicateInventorySnapshot(snapshot PredicateInventorySnapshot) PredicateInventorySnapshot {
	out := snapshot
	out.Predicates = make([]PredicateInventoryStat, 0, len(snapshot.Predicates))
	for _, stat := range snapshot.Predicates {
		out.Predicates = append(out.Predicates, clonePredicateInventoryStat(stat))
	}
	return out
}

func clonePredicateInventoryStat(stat PredicateInventoryStat) PredicateInventoryStat {
	out := stat
	out.HeadTypeDist = cloneInventoryCounters(stat.HeadTypeDist)
	out.TailTypeDist = cloneInventoryCounters(stat.TailTypeDist)
	return out
}

func cloneInventoryCounters(values map[string]int64) map[string]int64 {
	if values == nil {
		return nil
	}
	out := make(map[string]int64, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func undeclaredPredicateCount(stats []PredicateInventoryStat) int {
	count := 0
	for _, stat := range stats {
		if !stat.Declared {
			count++
		}
	}
	return count
}

func inventoryKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
