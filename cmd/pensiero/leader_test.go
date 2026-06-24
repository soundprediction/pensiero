package main

import (
	"errors"
	"runtime"
	"syscall"
	"testing"

	"github.com/soundprediction/pensiero/pkg/generalization"
)

func TestLeaderElectorFlockExclusivePerScope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("flock leader election is verified on Linux")
	}
	outDir := t.TempDir()
	first, err := newLeaderElector(outDir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := newLeaderElector(outDir)
	if err != nil {
		t.Fatal(err)
	}
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
	if runtime.GOOS != "linux" {
		t.Skip("flock leader election is verified on Linux")
	}
	outDir := t.TempDir()
	first, err := newLeaderElector(outDir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := newLeaderElector(outDir)
	if err != nil {
		t.Fatal(err)
	}
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

func TestNoneLeaderElectorLeadsAllScopes(t *testing.T) {
	leader, err := newLeaderForMode(leaderModeNone, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := leader.TryAcquire("alpha"); err != nil || !ok {
		t.Fatalf("TryAcquire returned ok=%v err=%v, want ok", ok, err)
	}
	if !leader.Holds("alpha") || !leader.Holds("beta") {
		t.Fatal("none leader mode should hold every valid scope")
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
		if errors.Is(err, syscall.ENOSYS) {
			t.Skipf("flock unavailable: %v", err)
		}
		t.Fatal(err)
	}
	return acquired
}
