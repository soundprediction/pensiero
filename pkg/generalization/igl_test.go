package generalization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishIsAtomicForReaders(t *testing.T) {
	ctx := context.Background()
	outDir := t.TempDir()
	finalPath, err := SnapshotPath(outDir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath, []byte(`{"version":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	writer := &blockingFileWriter{started: started, release: release}
	publisher := testPublisher(writer)

	done := make(chan error, 1)
	go func() {
		_, err := publisher.Publish(ctx, outDir, testScope("alpha"), nil)
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	for i := 0; i < 20; i++ {
		data, err := os.ReadFile(finalPath)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("partial")) {
			t.Fatalf("reader observed partial snapshot: %s", data)
		}
		if string(data) != `{"version":"old"}` {
			t.Fatalf("reader observed unexpected snapshot before publish: %s", data)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"scope":"alpha"`)) {
		t.Fatalf("published snapshot missing scope: %s", data)
	}
}

func TestRunOnceProducesValidSnapshot(t *testing.T) {
	ctx := context.Background()
	outDir := t.TempDir()
	loop := testLoop(outDir, &jsonFileWriter{})

	result, err := loop.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Scopes) != 1 || !result.Scopes[0].Published {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Scopes[0].Stats.NodeCount == 0 || result.Scopes[0].Stats.RelationCount == 0 {
		t.Fatalf("empty graph stats: %#v", result.Scopes[0].Stats)
	}
	finalPath, err := SnapshotPath(outDir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotFile(t, finalPath, "alpha")
}

func TestLoopTicksAndRepublishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outDir := t.TempDir()
	writer := &countingFileWriter{published: make(chan struct{}, 8)}
	loop := testLoop(outDir, writer)
	loop.Interval = 10 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- loop.Run(ctx)
	}()

	waitWrites(t, writer.published, 2)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("loop did not stop")
	}
	if got := atomic.LoadInt64(&writer.count); got < 2 {
		t.Fatalf("writes = %d, want at least 2", got)
	}
}

func TestShutdownRemovesTempSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	outDir := t.TempDir()
	started := make(chan struct{})
	writer := &cancelWriter{started: started}
	publisher := testPublisher(writer)
	done := make(chan error, 1)

	go func() {
		_, err := publisher.Publish(ctx, outDir, testScope("alpha"), nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	matches, err := filepath.Glob(filepath.Join(outDir, "*.tmp.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp paths remain: %v", matches)
	}
}

func testLoop(outDir string, writer SnapshotWriter) *Loop {
	return &Loop{
		Publisher: testPublisher(writer),
		Metrics:   NewMetrics(),
		Logger:    discardLogger{},
		Scopes:    []Scope{testScope("alpha")},
		Interval:  time.Minute,
		OutDir:    outDir,
	}
}

func testPublisher(writer SnapshotWriter) *Publisher {
	return &Publisher{
		Source:   testIGLSource(),
		Registry: testRegistry(),
		Writer:   writer,
		Validate: nonEmptyPath,
	}
}

func testScope(name string) Scope {
	return Scope{
		Name: name,
		Config: Config{
			ScopeEntities:    []string{"A", "B", "C"},
			Predicates:       []string{"R"},
			MaxParentLevel:   2,
			MinParentSupport: 1,
			MinSupport:       2,
		},
	}
}

func testIGLSource() fakeSource {
	return fakeSource{
		taxonomy: []map[string]any{
			taxRow("A", "P", 1),
			taxRow("B", "P", 1),
			taxRow("C", "Q", 1),
		},
		direct: []map[string]any{
			relRow("e1", "A", "R", "Y"),
			relRow("e2", "B", "R", "Y"),
		},
	}
}

type snapshotSummary struct {
	Scope     string `json:"scope"`
	Nodes     int    `json:"nodes"`
	Relations int    `json:"relations"`
}

type jsonFileWriter struct{}

func (w *jsonFileWriter) Write(_ context.Context, path string, scope string, graph *Graph) error {
	return writeSnapshotFile(path, scope, graph)
}

type blockingFileWriter struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (w *blockingFileWriter) Write(ctx context.Context, path string, scope string, graph *Graph) error {
	if err := os.WriteFile(path, []byte("partial"), 0o644); err != nil {
		return err
	}
	w.once.Do(func() { close(w.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.release:
	}
	return writeSnapshotFile(path, scope, graph)
}

type countingFileWriter struct {
	published chan struct{}
	count     int64
}

func (w *countingFileWriter) Write(_ context.Context, path string, scope string, graph *Graph) error {
	atomic.AddInt64(&w.count, 1)
	if err := writeSnapshotFile(path, scope, graph); err != nil {
		return err
	}
	select {
	case w.published <- struct{}{}:
	default:
	}
	return nil
}

type cancelWriter struct {
	once    sync.Once
	started chan struct{}
}

func (w *cancelWriter) Write(ctx context.Context, path string, _ string, _ *Graph) error {
	if err := os.WriteFile(path, []byte("partial"), 0o644); err != nil {
		return err
	}
	w.once.Do(func() { close(w.started) })
	<-ctx.Done()
	return ctx.Err()
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

func writeSnapshotFile(path string, scope string, graph *Graph) error {
	data, err := json.Marshal(snapshotSummary{
		Scope:     scope,
		Nodes:     graph.Stats.NodeCount,
		Relations: graph.Stats.RelationCount,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func nonEmptyPath(_ context.Context, path string, _ *Graph) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return errors.New("empty snapshot")
	}
	return nil
}

func assertSnapshotFile(t *testing.T, path string, scope string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var summary snapshotSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Scope != scope || summary.Nodes == 0 || summary.Relations == 0 {
		t.Fatalf("invalid snapshot summary: %#v", summary)
	}
}

func waitWrites(t *testing.T, ch <-chan struct{}, want int) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for write %d", i+1)
		}
	}
}
