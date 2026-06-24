package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soundprediction/pensiero/pkg/reasoning"
)

func TestPredicateInventoryAggregatesSampledRows(t *testing.T) {
	reg, err := reasoning.BuildRegistry(nil, reasoning.PredicatePack{
		Name:       "inventory_predicates",
		Predicates: []reasoning.PredicateMeta{{Canonical: "treats"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := &inventoryFakeGraph{rows: []map[string]any{
		{"predicate": "treats", "head_types": []string{"DRUG"}, "tail_types": []string{"DISEASE"}},
		{"predicate": "treats", "head_types": []string{"DRUG"}, "tail_types": []string{"DISEASE"}},
		{"predicate": "mystery", "head_types": []string{"GENE"}, "tail_types": []string{"PROTEIN"}},
		{"predicate": "ignored_after_limit", "head_types": []string{"A"}, "tail_types": []string{"B"}},
	}}
	inv := newPredicateInventory(graph, reg, PredicateInventoryConfig{
		SampleLimit: 3,
		Now:         fixedInventoryNow,
	})
	if err := inv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(graph.lastQuery(), "LIMIT 3") {
		t.Fatalf("inventory query %q is not bounded by LIMIT 3", graph.lastQuery())
	}
	if strings.Contains(graph.lastQuery(), "label(") || strings.Contains(graph.lastQuery(), "labels(") {
		t.Fatalf("inventory query %q used label functions", graph.lastQuery())
	}
	if got := graph.lastLimit(); got != 3 {
		t.Fatalf("limit param=%d, want 3", got)
	}

	snapshot := inv.Snapshot()
	if len(snapshot.Predicates) != 2 {
		t.Fatalf("predicate count=%d, want 2: %#v", len(snapshot.Predicates), snapshot.Predicates)
	}
	treats := inventoryStatByCanonical(snapshot, "treats")
	if treats == nil {
		t.Fatal("missing treats stat")
	}
	if !treats.Declared || treats.ObservedCount != 2 {
		t.Fatalf("treats declared=%v observed=%d, want declared count 2", treats.Declared, treats.ObservedCount)
	}
	if got := treats.HeadTypeDist["DRUG"]; got != 2 {
		t.Fatalf("treats head DRUG count=%d, want 2", got)
	}
	if got := treats.TailTypeDist["DISEASE"]; got != 2 {
		t.Fatalf("treats tail DISEASE count=%d, want 2", got)
	}
	mystery := inventoryStatByCanonical(snapshot, "mystery")
	if mystery == nil {
		t.Fatal("missing mystery stat")
	}
	if mystery.Declared || mystery.ObservedCount != 1 {
		t.Fatalf("mystery declared=%v observed=%d, want undeclared count 1", mystery.Declared, mystery.ObservedCount)
	}
	if inventoryStatByCanonical(snapshot, "ignored_after_limit") != nil {
		t.Fatal("inventory aggregated rows beyond sample limit")
	}
}

func TestPredicateInventoryRefreshGatedByIdle(t *testing.T) {
	clock := newSchedulerFakeClock()
	load := NewLoadTracker(LoadTrackerConfig{
		QPSIdleLimit: 100,
		LatencySLO:   time.Hour,
		Now:          clock.Now,
	})
	graph := &inventoryFakeGraph{rows: []map[string]any{{"predicate": "observed"}}}
	inv := newPredicateInventory(graph, nil, PredicateInventoryConfig{
		SampleLimit: 1,
		QuietFor:    time.Second,
		Load:        load,
		Now:         clock.Now,
	})

	end := load.Begin()
	refreshed, err := inv.RefreshIfIdle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if refreshed {
		t.Fatal("inventory refreshed while load tracker was not idle")
	}
	if got := graph.queryCount(); got != 0 {
		t.Fatalf("queries=%d, want 0", got)
	}

	end()
	clock.Advance(time.Second)
	refreshed, err = inv.RefreshIfIdle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed {
		t.Fatal("inventory did not refresh after idle period")
	}
	if got := graph.queryCount(); got != 3 {
		t.Fatalf("queries=%d, want 3 schema+data queries", got)
	}
}

func TestPredicateInventoryPropertyErrorClearsSnapshot(t *testing.T) {
	graph := &inventoryFakeGraph{rows: []map[string]any{{"predicate": "observed", "head_types": []string{"A"}, "tail_types": []string{"B"}}}}
	inv := newPredicateInventory(graph, nil, PredicateInventoryConfig{
		SampleLimit: 1,
		Now:         fixedInventoryNow,
	})
	if err := inv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(inv.Snapshot().Predicates) != 1 {
		t.Fatal("initial inventory refresh did not populate snapshot")
	}

	graph.setDataErr(errors.New("typed db: missing property RelatesToNode_.name"))
	err := inv.Refresh(context.Background())
	if err == nil {
		t.Fatal("Refresh returned nil error for typed DB property failure")
	}
	snapshot := inv.Snapshot()
	if len(snapshot.Predicates) != 0 {
		t.Fatalf("snapshot retained stale predicates after error: %#v", snapshot.Predicates)
	}
	if !strings.Contains(snapshot.LastError, "missing property") {
		t.Fatalf("last_error=%q, want typed DB property failure", snapshot.LastError)
	}
	if snapshot.SampleLimit != 1 {
		t.Fatalf("sample_limit=%d, want 1", snapshot.SampleLimit)
	}
}

func TestInventoryEndpointReturnsSnapshot(t *testing.T) {
	graph := &inventoryFakeGraph{rows: []map[string]any{{"predicate": "observed", "head_types": []string{"A"}, "tail_types": []string{"B"}}}}
	inv := newPredicateInventory(graph, nil, PredicateInventoryConfig{
		SampleLimit: 1,
		Now:         fixedInventoryNow,
	})
	if err := inv.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	healthHandler(nil, nil, newReadinessGate(), inv, nil, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var snapshot PredicateInventorySnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SampleLimit != 1 || len(snapshot.Predicates) != 1 {
		t.Fatalf("snapshot=%#v, want one sampled predicate", snapshot)
	}
	if snapshot.Predicates[0].Canonical != "observed" {
		t.Fatalf("canonical=%q, want observed", snapshot.Predicates[0].Canonical)
	}
}

type inventoryFakeGraph struct {
	mu      sync.Mutex
	rows    []map[string]any
	dataErr error
	queries []string
	params  []map[string]any
}

func (g *inventoryFakeGraph) Query(_ context.Context, query string, params map[string]any) ([]map[string]any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queries = append(g.queries, query)
	g.params = append(g.params, params)
	if strings.Contains(query, "TABLE_INFO('RelatesToNode_')") {
		return []map[string]any{{"name": "name"}}, nil
	}
	if strings.Contains(query, "TABLE_INFO('Entity')") {
		return []map[string]any{{"name": "labels"}}, nil
	}
	if g.dataErr != nil {
		return nil, g.dataErr
	}
	return append([]map[string]any{}, g.rows...), nil
}

func (g *inventoryFakeGraph) setDataErr(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dataErr = err
}

func (g *inventoryFakeGraph) queryCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.queries)
}

func (g *inventoryFakeGraph) lastQuery() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.queries) == 0 {
		return ""
	}
	return g.queries[len(g.queries)-1]
}

func (g *inventoryFakeGraph) lastLimit() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.params) == 0 {
		return 0
	}
	return int(countValue(g.params[len(g.params)-1]["limit"]))
}

func inventoryStatByCanonical(snapshot PredicateInventorySnapshot, canonical string) *PredicateInventoryStat {
	for i := range snapshot.Predicates {
		if snapshot.Predicates[i].Canonical == canonical {
			return &snapshot.Predicates[i]
		}
	}
	return nil
}

func fixedInventoryNow() time.Time {
	return time.Unix(1_700_000_000, 0)
}
