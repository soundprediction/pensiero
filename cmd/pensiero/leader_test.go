package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soundprediction/pensiero/pkg/generalization"
)

func TestLeaderElectorFlockExclusivePerScope(t *testing.T) {
	outDir := t.TempDir()
	first := newLeaderElectorOrSkip(t, outDir)
	defer first.Close()
	second := newLeaderElectorOrSkip(t, outDir)
	defer second.Close()

	firstAcquired := tryAcquireOrSkip(t, first, "alpha")
	secondAcquired := tryAcquireOrSkip(t, second, "alpha")
	if firstAcquired == secondAcquired {
		t.Fatalf("first acquired=%v second acquired=%v, want exactly one leader", firstAcquired, secondAcquired)
	}
	if first.Holds("alpha") == second.Holds("alpha") {
		t.Fatalf("first holds=%v second holds=%v, want exactly one holder", first.Holds("alpha"), second.Holds("alpha"))
	}
}

func TestLeaderElectorReleaseAllowsOtherAcquire(t *testing.T) {
	outDir := t.TempDir()
	first := newLeaderElectorOrSkip(t, outDir)
	defer first.Close()
	second := newLeaderElectorOrSkip(t, outDir)
	defer second.Close()

	if !tryAcquireOrSkip(t, first, "alpha") {
		t.Fatal("first elector did not acquire free scope")
	}
	if tryAcquireOrSkip(t, second, "alpha") {
		t.Fatal("second elector acquired while first still held the scope")
	}
	if err := first.Release("alpha"); err != nil {
		t.Fatal(err)
	}
	if !tryAcquireOrSkip(t, second, "alpha") {
		t.Fatal("second elector did not acquire after first released")
	}
	if first.Holds("alpha") {
		t.Fatal("first elector still holds released scope")
	}
	if !second.Holds("alpha") {
		t.Fatal("second elector does not hold acquired scope")
	}
}

func TestLeaderElectorCloseReleasesLock(t *testing.T) {
	outDir := t.TempDir()
	first := newLeaderElectorOrSkip(t, outDir)
	second := newLeaderElectorOrSkip(t, outDir)
	defer second.Close()

	if !tryAcquireOrSkip(t, first, "alpha") {
		t.Fatal("first elector did not acquire free scope")
	}
	if tryAcquireOrSkip(t, second, "alpha") {
		t.Fatal("second elector acquired while first still held the scope")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if first.Holds("alpha") {
		t.Fatal("first elector still reports holding alpha after Close")
	}
	if !tryAcquireOrSkip(t, second, "alpha") {
		t.Fatal("second elector did not acquire after first closed")
	}
}

func TestLeaderElectorRepeatedTryAcquireDoesNotLeakFDs(t *testing.T) {
	outDir := t.TempDir()
	first := newLeaderElectorOrSkip(t, outDir)
	defer first.Close()
	second := newLeaderElectorOrSkip(t, outDir)
	defer second.Close()

	if !tryAcquireOrSkip(t, first, "alpha") {
		t.Fatal("first elector did not acquire free scope")
	}
	lockPath := filepath.Join(outDir, "alpha.igl.lock")
	before := countOpenFDsForPathOrSkip(t, lockPath)
	for i := 0; i < 25; i++ {
		if !tryAcquireOrSkip(t, first, "alpha") {
			t.Fatalf("already-held TryAcquire attempt %d returned false", i)
		}
		if tryAcquireOrSkip(t, second, "alpha") {
			t.Fatalf("contender acquired held scope on attempt %d", i)
		}
	}
	after := countOpenFDsForPathOrSkip(t, lockPath)
	if after != before {
		t.Fatalf("open fds for %s changed after repeated TryAcquire: before=%d after=%d", lockPath, before, after)
	}
}

func TestLeaderElectorTakeoverAfterLeaderProcessExit(t *testing.T) {
	if err := flockLeaderSupported(); err != nil {
		if errors.Is(err, errLeaderFlockUnsupported) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	outDir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaderElectorSubprocessLeader$")
	cmd.Env = append(os.Environ(),
		"PENSIERO_LEADER_CHILD=1",
		"PENSIERO_LEADER_OUT_DIR="+outDir,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})

	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	select {
	case line := <-ready:
		if line != "locked" {
			t.Fatalf("child ready line=%q stderr=%s", line, stderr.String())
		}
	case err := <-done:
		t.Fatalf("child exited before acquiring lock: err=%v stderr=%s", err, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for child lock stderr=%s", stderr.String())
	}

	contender := newLeaderElectorOrSkip(t, outDir)
	defer contender.Close()
	if tryAcquireOrSkip(t, contender, "alpha") {
		t.Fatal("contender acquired while child process still held the scope")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child exit error=%v stderr=%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for child exit stderr=%s", stderr.String())
	}
	if !tryAcquireOrSkip(t, contender, "alpha") {
		t.Fatal("contender did not acquire after leader process exited")
	}
}

func TestLeaderElectorSubprocessLeader(t *testing.T) {
	if os.Getenv("PENSIERO_LEADER_CHILD") != "1" {
		return
	}
	elector, err := newLeaderElector(os.Getenv("PENSIERO_LEADER_OUT_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := elector.TryAcquire("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("child did not acquire alpha")
	}
	fmt.Fprintln(os.Stdout, "locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestNoneLeaderElectorLeadsAllScopes(t *testing.T) {
	outDir := t.TempDir()
	leader, err := newLeaderForMode(leaderModeNone, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := leader.TryAcquire("alpha"); err != nil || !ok {
		t.Fatalf("TryAcquire returned ok=%v err=%v, want ok", ok, err)
	}
	if !leader.Holds("alpha") || !leader.Holds("beta") {
		t.Fatal("none leader mode should hold every valid scope")
	}
	matches, err := filepath.Glob(filepath.Join(outDir, "*.igl.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("none leader mode created lock files: %v", matches)
	}
}

func TestLeaderForModeK8sLeaseHasNoSideEffects(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "missing")
	leader, err := newLeaderForMode(leaderModeK8sLease, outDir)
	if err == nil {
		t.Fatal("newLeaderForMode k8s-lease succeeded, want error")
	}
	if leader != nil {
		t.Fatalf("newLeaderForMode k8s-lease returned leader %#v, want nil", leader)
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		t.Fatalf("k8s-lease created output dir or returned unexpected stat error: %v", statErr)
	}
}

func TestLeaderGatedIGLRunnerFiltersUnheldScopes(t *testing.T) {
	leader := newSchedulerFakeLeader()
	leader.SetHeld("alpha", true)
	runner := newLeaderGatedIGLRunner(&generalization.Loop{
		Scopes: []generalization.Scope{
			{Name: "alpha"},
			{Name: "beta"},
		},
	}, leader, nil)

	held, err := runner.heldScopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].Name != "alpha" {
		t.Fatalf("held scopes=%#v, want only alpha", held)
	}
	if got := leader.TryCount("beta"); got != 1 {
		t.Fatalf("beta TryAcquire calls=%d, want 1", got)
	}
}

func tryAcquireOrSkip(t *testing.T, elector *leaderElector, scope string) bool {
	t.Helper()
	acquired, err := elector.TryAcquire(scope)
	if err != nil {
		if errors.Is(err, errLeaderFlockUnsupported) {
			t.Skipf("flock unavailable: %v", err)
		}
		t.Fatal(err)
	}
	return acquired
}

func newLeaderElectorOrSkip(t *testing.T, outDir string) *leaderElector {
	t.Helper()
	elector, err := newLeaderElector(outDir)
	if err != nil {
		if errors.Is(err, errLeaderFlockUnsupported) {
			t.Skipf("flock unavailable: %v", err)
		}
		t.Fatal(err)
	}
	return elector
}

func countOpenFDsForPathOrSkip(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot inspect open fds: %v", err)
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		fdPath := filepath.Join("/proc/self/fd", entry.Name())
		fdTarget, err := os.Readlink(fdPath)
		if err != nil {
			continue
		}
		fdTarget, err = filepath.EvalSymlinks(fdTarget)
		if err != nil {
			continue
		}
		if fdTarget == target {
			count++
		}
	}
	return count
}
