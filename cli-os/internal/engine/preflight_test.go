package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
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

// Bug 2 fix: a dirty worktree at pre-flight time must surface as a Note, not a Blocker (a Blocker
// disables the dashboard's Start button entirely, per dashboard.html's hasBlockers gate — making
// StartRun's auto-checkpoint unreachable for exactly the users it exists for). StartRun must then
// actually auto-checkpoint the dirty file rather than failing or leaving it for the user to handle.
func TestDirtyWorktreeIsANoteNotABlockerAndAutoCheckpoints(t *testing.T) {
	e := newEngine(t, &scriptedCaller{})
	root := newRepo(t)

	// Dirty a file OUTSIDE .l00prite/ -- the user's own unsaved work.
	if err := os.WriteFile(filepath.Join(root, "draft.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run, err := e.Store.CreateRun("proj", root, RunConfig{RepoID: "r1", Goal: "test", CommandAllowlist: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	pf, err := e.BuildPreflight(run)
	if err != nil {
		t.Fatalf("BuildPreflight: %v", err)
	}
	for _, b := range pf.Blockers {
		if strings.Contains(b, "uncommitted") || strings.Contains(b, "draft.txt") {
			t.Fatalf("a dirty worktree must not block Start (it should auto-checkpoint instead), got blocker: %q", b)
		}
	}
	foundNote := false
	for _, n := range pf.Notes {
		if strings.Contains(n, "draft.txt") && strings.Contains(n, "checkpoint") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Fatalf("expected a Note about the pending auto-checkpoint mentioning draft.txt, got notes: %v", pf.Notes)
	}
	if run.Status != StatusReady {
		t.Fatalf("a dirty-but-checkpointable worktree should leave the run ready to Start, got status %q", run.Status)
	}

	if err := e.StartRun(context.Background(), run.ID, "tok_1", "EXECUTE"); err != nil {
		t.Fatalf("StartRun should auto-checkpoint the dirty file and proceed, got: %v", err)
	}

	// draft.txt must now be committed (checkpointed), not lost and not left dirty.
	if out := strings.TrimSpace(gitRun(t, root, "status", "--porcelain", "draft.txt")); out != "" {
		t.Fatalf("expected draft.txt to be checkpointed (clean), got status: %q", out)
	}
	log := gitRun(t, root, "log", "--oneline", "--all")
	if !strings.Contains(log, "WIP: auto-checkpoint before run-"+run.ID) {
		t.Fatalf("expected a WIP auto-checkpoint commit in history, got log: %s", log)
	}

	ledgerBytes, err := os.ReadFile(filepath.Join(root, ".l00prite", "ledger.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ledgerBytes), "auto-checkpoint before run "+run.ID) {
		t.Fatalf("expected the checkpoint to be logged in ledger.md, got:\n%s", string(ledgerBytes))
	}
}

// rawErrGit is a gitx.Client whose RevParseHead/StatusPorcelain return an error carrying raw
// technical text (as a real git failure or a corrupted-repo condition might), to prove
// checkGitReady never lets that text reach a Blocker string. Every other method panics: this test
// only exercises checkGitReady, which never calls them.
type rawErrGit struct{ revErr, statusErr error }

func (rawErrGit) Kind() string { return "fake" }
func (rawErrGit) Clone(ctx context.Context, url, dest string, depth int) error {
	panic("not used by checkGitReady")
}
func (g rawErrGit) RevParseHead(repo string) (string, error) { return "", g.revErr }
func (g rawErrGit) StatusPorcelain(repo string) (string, error) {
	if g.statusErr != nil {
		return "", g.statusErr
	}
	return "", nil
}
func (rawErrGit) CheckoutNewBranch(repo, name string) error { panic("not used by checkGitReady") }
func (rawErrGit) AddAll(repo string) error                  { panic("not used by checkGitReady") }
func (rawErrGit) Commit(repo, message string) (string, error) {
	panic("not used by checkGitReady")
}
func (rawErrGit) DiffHead(repo string) (string, error) { panic("not used by checkGitReady") }
func (rawErrGit) Log(repo string, limit int) (string, error) {
	panic("not used by checkGitReady")
}
func (rawErrGit) Show(repo string, ref string) (string, error) {
	panic("not used by checkGitReady")
}
func (rawErrGit) Raw(ctx context.Context, repo string, args ...string) (string, error) {
	panic("not used by checkGitReady")
}

var _ gitx.Client = rawErrGit{}

// Adversarial-review finding: checkGitReady's own Blockers ("repository has no commits...", "git
// status failed...") used to concatenate the raw error text, bypassing the plain-English
// translation the rest of Bug 2's fix (gateway/runs.go's humanizeStartError) added for StartRun's
// errors. Both failure paths must now be fixed, plain-English strings only.
func TestCheckGitReadyNeverLeaksRawErrorText(t *testing.T) {
	rawMarkers := []string{"fatal:", "exit status", "unstaged", "checkout -B", "\n"}

	t.Run("RevParseHead failure", func(t *testing.T) {
		git := rawErrGit{revErr: errors.New("fatal: not a git repository (or any of the parent directories): .git\nexit status 128")}
		blockers, notes := checkGitReady(git, t.TempDir(), "run-x")
		if len(notes) != 0 {
			t.Fatalf("expected no notes on a hard git-repo failure, got: %v", notes)
		}
		if len(blockers) != 1 {
			t.Fatalf("expected exactly one blocker, got: %v", blockers)
		}
		for _, marker := range rawMarkers {
			if strings.Contains(blockers[0], marker) {
				t.Fatalf("blocker leaked raw git error text (marker %q): %q", marker, blockers[0])
			}
		}
	})

	t.Run("StatusPorcelain failure", func(t *testing.T) {
		git := rawErrGit{statusErr: errors.New("fatal: index file corrupt\nexit status 128")}
		blockers, notes := checkGitReady(git, t.TempDir(), "run-x")
		if len(notes) != 0 {
			t.Fatalf("expected no notes on a hard git-status failure, got: %v", notes)
		}
		if len(blockers) != 1 {
			t.Fatalf("expected exactly one blocker, got: %v", blockers)
		}
		for _, marker := range rawMarkers {
			if strings.Contains(blockers[0], marker) {
				t.Fatalf("blocker leaked raw git error text (marker %q): %q", marker, blockers[0])
			}
		}
	})
}
