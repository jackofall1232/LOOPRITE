package engine

import (
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// After a gateway crash, the interrupted run's OWN lease can still be active and unexpired in
// the repo's lock.json, owned by that same run id, when its pre-flight is rebuilt for recovery.
// AcquireLock correctly refuses to re-acquire a lock its caller already holds ("mine"); before
// the fix, BuildPreflight tried AcquireLock unconditionally and reported that refusal as a
// blocker — leaving recovery stuck until the lease's TTL naturally expired (PR #24 review).
func TestBuildPreflightRecoversOwnUnexpiredLease(t *testing.T) {
	e := newEngine(t, &scriptedCaller{})
	root := newRepo(t)

	run, err := e.Store.CreateRun("proj", root, RunConfig{RepoID: "r1", Goal: "recover me", CommandAllowlist: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the mid-run state a crash would leave behind: this run's own lease still active
	// and well within its TTL, heartbeat/state armed, and the engine store row "running" (as
	// ReconcileOrphans would have already flipped it to "interrupted" at boot in the real path —
	// force that directly here since we're not going through StartRun's live goroutine).
	f := Files{Root: root}
	if _, _, err := f.AcquireLock(run.ID, "execute-loop run (l00prite OS engine)", 1800); err != nil {
		t.Fatalf("simulated arm: acquire lock: %v", err)
	}
	now := util.NowISO()
	hb := map[string]any{}
	EnsureExecutionBlock(hb)
	ArmHeartbeat(hb, run.Config.MaxIterations, "tester", now)
	if err := f.WriteHeartbeat(hb); err != nil {
		t.Fatal(err)
	}
	st := map[string]any{}
	SetStateRun(st, true, "", "executing", "execution", run.Config.Goal, "in progress", "l00prite-os", now)
	if err := f.WriteState(st); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SetStatus(run.ID, StatusInterrupted); err != nil {
		t.Fatal(err)
	}

	// Confirm the lease really is "mine" and unexpired going in (the scenario this test targets).
	lock, err := f.ReadLock()
	if err != nil {
		t.Fatal(err)
	}
	if avail := LockAvailability(lock, run.ID); avail != "mine" {
		t.Fatalf("test setup: want lease availability \"mine\", got %q", avail)
	}

	pf, err := e.BuildPreflight(run)
	if err != nil {
		t.Fatalf("BuildPreflight: %v", err)
	}
	for _, b := range pf.Blockers {
		if strings.Contains(b, "could not acquire") {
			t.Fatalf("recovery should refresh its own unexpired lease, not fail to acquire it: blocker %q", b)
		}
	}

	snap, err := f.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if en := dig(snap.Heartbeat, "execution", "enabled"); en != false {
		t.Fatalf("recovery should have disarmed the stale execution arming, got execution.enabled=%v", en)
	}
	if active := dig(snap.State, "execution_active"); active != false {
		t.Fatalf("recovery should have cleared execution_active, got %v", active)
	}
}
