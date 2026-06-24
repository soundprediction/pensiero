//go:build system_ladybug

package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	ladybug "github.com/LadybugDB/go-ladybug"
)

type ladybugGraph struct {
	db   *ladybug.Database
	conn *ladybug.Connection
	mu   sync.Mutex
}

func openLadybugGraph(path string, readOnly bool) (graphHandle, error) {
	cfg := ladybug.SystemConfig{
		BufferPoolSize:    1024 * 1024 * 1024,
		MaxNumThreads:     1,
		EnableCompression: true,
		ReadOnly:          readOnly,
		// MaxDbSize pre-reserves this much *virtual* address space per open
		// handle (ladybug mmaps the region up front). The multi-topic serving
		// daemon keeps many read-only handles open at once (max-open-topics ×
		// grpc-pool-size), so an 8TB (1<<43) reservation per handle exhausts the
		// ~128TB x86-64 user address space after ~16 handles and OpenDatabase
		// fails with "status 1". Serving graphs are bounded and read-only, so a
		// 16GB cap (28× the largest current graph) is ample while keeping the
		// total virtual reservation tiny across dozens of handles.
		MaxDbSize: 1 << 34,
	}
	db, err := ladybug.OpenDatabase(path, cfg)
	if err != nil {
		return nil, err
	}
	conn, err := ladybug.OpenConnection(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &ladybugGraph{db: db, conn: conn}, nil
}

func (g *ladybugGraph) Close() error {
	if g.conn != nil {
		g.conn.Close()
	}
	if g.db != nil {
		g.db.Close()
	}
	return nil
}

func (g *ladybugGraph) Query(ctx context.Context, query string, params map[string]any) ([]map[string]any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		timeout := time.Until(deadline)
		if timeout > 0 {
			g.conn.SetTimeout(uint64(timeout / time.Millisecond))
		}
		defer g.conn.SetTimeout(0)
	}
	var (
		res *ladybug.QueryResult
		err error
	)
	if len(params) > 0 {
		stmt, prepErr := g.conn.Prepare(query)
		if prepErr != nil {
			return nil, prepErr
		}
		defer stmt.Close()
		res, err = g.conn.Execute(stmt, params)
	} else {
		res, err = g.conn.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer res.Close()
	cols := res.GetColumnNames()
	out := []map[string]any{}
	for res.HasNext() {
		row, nextErr := res.Next()
		if nextErr != nil {
			return nil, nextErr
		}
		values, valueErr := row.GetAsSlice()
		row.Close()
		if valueErr != nil {
			return nil, valueErr
		}
		m := make(map[string]any, len(cols))
		for i, col := range cols {
			if i < len(values) {
				m[col] = values[i]
			}
		}
		out = append(out, m)
	}
	if res.HasNextQueryResult() {
		if err := consumeRemainingResults(res); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func consumeRemainingResults(res *ladybug.QueryResult) error {
	for res.HasNextQueryResult() {
		next, err := res.NextQueryResult()
		if err != nil {
			return err
		}
		for next.HasNext() {
			row, err := next.Next()
			if err != nil {
				next.Close()
				return err
			}
			row.Close()
		}
		next.Close()
	}
	return nil
}

func (g *ladybugGraph) String() string {
	return fmt.Sprintf("%T", g)
}
