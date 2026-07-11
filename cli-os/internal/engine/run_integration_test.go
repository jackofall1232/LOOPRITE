package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// scriptedCaller is a deterministic ModelCaller that plays a fixed script keyed by role and
// call index, so a full engine run can be exercised end-to-end without any provider. It records
// every model spec it was asked to route so tests can assert the engine only ever names roles.
type scriptedCaller struct {
	planner []map[string]any // one select_unit args object per planner call
	coder   [][]step         // per unit index: the sequence of coder tool_calls to emit
	review  map[string]any   // review_verdict args (nil -> approve)
	preview func(model string) (RoutePreview, error)

	planCalls  int
	coderUnit  int
	coderStep  int
	seenModels []string
}

type step struct {
	name string
	args map[string]any
}

func (c *scriptedCaller) PreviewRoute(project, repoID, model string, sample map[string]any) (RoutePreview, error) {
	if c.preview != nil {
		return c.preview(model)
	}
	return RoutePreview{Provider: "mock", Model: strings.TrimPrefix(model, "auto:") + "-model", Reason: "test preview"}, nil
}

func toolCallMsg(name string, args map[string]any) map[string]any {
	b, _ := json.Marshal(args)
	return map[string]any{
		"role": "assistant", "content": "",
		"tool_calls": []any{map[string]any{
			"id": "tc_" + name, "type": "function",
			"function": map[string]any{"name": name, "arguments": string(b)},
		}},
	}
}

func (c *scriptedCaller) Turn(ctx context.Context, in TurnInput) (TurnResult, error) {
	role, _ := in.Meta["role"].(string)
	model, _ := in.Req["model"].(string)
	c.seenModels = append(c.seenModels, model)
	res := TurnResult{Provider: "mock", Model: role + "-model", CostUSD: 0.001}
	switch role {
	case RolePlan:
		i := c.planCalls
		if i >= len(c.planner) {
			i = len(c.planner) - 1
		}
		c.planCalls++
		res.Message = toolCallMsg("select_unit", c.planner[i])
	case RoleCode:
		steps := c.coder[c.coderUnit]
		st := steps[c.coderStep]
		c.coderStep++
		if c.coderStep >= len(steps) {
			c.coderStep = 0
			c.coderUnit++
		}
		res.Message = toolCallMsg(st.name, st.args)
	case RoleReview:
		rv := c.review
		if rv == nil {
			rv = map[string]any{"approve": true, "must_fix": false}
		}
		res.Message = toolCallMsg("review_verdict", rv)
	default:
		res.Message = map[string]any{"role": "assistant", "content": "ok"}
	}
	return res, nil
}

// newRepo makes a temp git repo with an initial commit and returns its root.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# test repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(t, root)
	return root
}

func commit(t *testing.T, root string) {
	t.Helper()
	exec.Command("git", "-C", root, "add", "-A").Run()
	if out, err := exec.Command("git", "-C", root, "commit", "-q", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
}

func newEngine(t *testing.T, caller ModelCaller) *Engine {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := New(&Store{DB: db}, caller)
	e.LeaseTTLSec = 60
	e.IterationTimeout = 30 * time.Second
	return e
}

// waitTerminal polls the run until it reaches a terminal status or the deadline.
func waitTerminal(t *testing.T, e *Engine, id string) *Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, err := e.Store.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		switch run.Status {
		case StatusDone, StatusStopped, StatusBlocked, StatusInterrupted:
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state in time")
	return nil
}

func startRun(t *testing.T, e *Engine, root, goal string, cfg func(*RunConfig)) *Run {
	t.Helper()
	rc := RunConfig{RepoID: "r1", Goal: goal, CommandAllowlist: []string{"true"}, MaxIterations: 6}
	if cfg != nil {
		cfg(&rc)
	}
	run, err := e.Store.CreateRun("proj", root, rc)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := e.BuildPreflight(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Blockers) > 0 {
		t.Fatalf("unexpected pre-flight blockers: %v", pf.Blockers)
	}
	if err := e.StartRun(context.Background(), run.ID, "tok_1", "EXECUTE"); err != nil {
		t.Fatalf("start: %v", err)
	}
	return run
}

// A happy-path run: one implement unit (write a file), verification passes, then done.
func TestRunReachesDefinitionOfDone(t *testing.T) {
	caller := &scriptedCaller{
		planner: []map[string]any{
			{"action": "unit", "description": "create greeting.txt", "target_paths": []string{"greeting.txt"}, "verification_command": "true"},
			{"action": "done", "done_reason": "greeting.txt exists and the done-check passes"},
		},
		coder: [][]step{
			{{name: "write_file", args: map[string]any{"path": "greeting.txt", "content": "hello\n"}}, {name: "unit_done", args: map[string]any{"summary": "wrote greeting.txt", "files_changed": []string{"greeting.txt"}}}},
		},
	}
	e := newEngine(t, caller)
	root := newRepo(t)
	run := startRun(t, e, root, "add a greeting file", nil)

	final := waitTerminal(t, e, run.ID)
	if final.Status != StatusDone || final.Boundary != BoundaryDone {
		t.Fatalf("want done/%s, got %s/%s (%s)", BoundaryDone, final.Status, final.Boundary, final.Summary)
	}
	// The file was actually written on the run branch.
	if _, err := os.Stat(filepath.Join(root, "greeting.txt")); err != nil {
		t.Fatalf("greeting.txt not written: %v", err)
	}
	// Vendor neutrality: the engine only ever named roles (auto:<profile>), never a provider.
	for _, m := range caller.seenModels {
		if !strings.HasPrefix(m, "auto:") {
			t.Fatalf("engine named a non-role model %q — vendor neutrality broken", m)
		}
	}
	// Protocol persistence: the repo disarmed on exit and the ledger recorded the run.
	f := Files{Root: root}
	snap, _ := f.ReadSnapshot()
	if en := dig(snap.Heartbeat, "execution", "enabled"); en != false {
		t.Fatalf("heartbeat left armed: execution.enabled=%v", en)
	}
	if !strings.Contains(snap.Ledger, run.ID) {
		t.Fatalf("ledger did not record run %s", run.ID)
	}
	if av := LockAvailability(snap.Lock, run.ID); av != "free" {
		t.Fatalf("lock not released on exit: availability=%s", av)
	}
}

// TestStartRunDoesNotLeakActiveLeaseOntoSourceBranch pins a Codex review finding on PR #7:
// StartRun used to call AcquireLock (which rewrites lock.json with an ACTIVE lease) before
// AutoCheckpoint's commit-everything-dirty step, so that active lease landed in the checkpoint
// commit made on whatever branch was checked out BEFORE the run branch existed -- polluting the
// user's own source branch with a WIP commit carrying a run's active lease on every clean Start,
// and making that branch look locked if checked back out before the lease's TTL expired. The fix
// defers the AcquireLock write until after EnsureRunBranch, so the lease write lands on the run
// branch instead.
func TestStartRunDoesNotLeakActiveLeaseOntoSourceBranch(t *testing.T) {
	caller := &scriptedCaller{
		planner: []map[string]any{
			{"action": "unit", "description": "create greeting.txt", "target_paths": []string{"greeting.txt"}, "verification_command": "true"},
			{"action": "done", "done_reason": "greeting.txt exists and the done-check passes"},
		},
		coder: [][]step{
			{{name: "write_file", args: map[string]any{"path": "greeting.txt", "content": "hello\n"}}, {name: "unit_done", args: map[string]any{"summary": "wrote greeting.txt", "files_changed": []string{"greeting.txt"}}}},
		},
	}
	e := newEngine(t, caller)
	root := newRepo(t)
	sourceBranch := strings.TrimSpace(gitRun(t, root, "rev-parse", "--abbrev-ref", "HEAD"))

	run := startRun(t, e, root, "add a greeting file", nil)
	final := waitTerminal(t, e, run.ID)
	if final.Status != StatusDone {
		t.Fatalf("want done, got %s/%s (%s)", final.Status, final.Boundary, final.Summary)
	}

	// Inspect what actually got committed on the SOURCE branch (not the run branch the loop left
	// HEAD on) -- this must never show this run's session holding an active lease. Read via `git
	// show <branch>:<path>` rather than checking the branch out, since the run branch HEAD is left
	// with its own uncommitted .l00prite/ changes that a checkout would otherwise collide with.
	lockJSON := gitRun(t, root, "show", sourceBranch+":.l00prite/lock.json")
	var lock map[string]any
	if err := json.Unmarshal([]byte(lockJSON), &lock); err != nil {
		t.Fatalf("lock.json on the source branch did not parse: %v\n%s", err, lockJSON)
	}
	if lock["status"] == "active" && lock["owner_session"] == run.ID {
		t.Fatalf("source branch %q carries this run's ACTIVE lease in its committed lock.json: %v", sourceBranch, lock)
	}
}

// A denylist-protected write requires approval; a deny stops at destructive_operation_required.
func TestRunDenylistWriteBlocksOnDeny(t *testing.T) {
	caller := &scriptedCaller{
		planner: []map[string]any{
			{"action": "unit", "description": "write the env file", "target_paths": []string{".env"}, "verification_command": "true"},
		},
		coder: [][]step{
			{{name: "write_file", args: map[string]any{"path": ".env", "content": "SECRET=x\n"}}},
		},
	}
	e := newEngine(t, caller)
	root := newRepo(t)
	run := startRun(t, e, root, "write secrets", func(rc *RunConfig) { rc.ApprovalTimeoutSec = 1 })

	// The write to .env matches the scaffolded Autonomous-Edit Denylist -> approval requested;
	// nobody approves, so after the 1s timeout the run fail-closes at destructive_operation_required.
	final := waitTerminal(t, e, run.ID)
	if final.Boundary != BoundaryDestructive {
		t.Fatalf("want %s, got %s/%s (%s)", BoundaryDestructive, final.Status, final.Boundary, final.Summary)
	}
	if _, err := os.Stat(filepath.Join(root, ".env")); err == nil {
		t.Fatal(".env was written despite the denied/expired approval")
	}
}

// Start is refused without a fresh ready pre-flight and without the confirm token.
func TestStartRequiresFreshPreflightAndConfirm(t *testing.T) {
	e := newEngine(t, &scriptedCaller{})
	root := newRepo(t)
	run, err := e.Store.CreateRun("proj", root, RunConfig{RepoID: "r1", Goal: "x", CommandAllowlist: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	// No pre-flight yet -> not ready.
	if err := e.StartRun(context.Background(), run.ID, "tok", "EXECUTE"); err == nil {
		t.Fatal("start should fail without a pre-flight")
	}
	if _, err := e.BuildPreflight(run); err != nil {
		t.Fatal(err)
	}
	// Ready, but wrong confirm token.
	if err := e.StartRun(context.Background(), run.ID, "tok", "yes"); err == nil {
		t.Fatal("start should fail without confirm=EXECUTE")
	}
}

// A crashed run is reconciled to interrupted at boot.
func TestReconcileOrphansAfterCrash(t *testing.T) {
	e := newEngine(t, &scriptedCaller{})
	root := newRepo(t)
	run, _ := e.Store.CreateRun("proj", root, RunConfig{RepoID: "r1", Goal: "x", CommandAllowlist: []string{"true"}})
	_, _ = e.BuildPreflight(run)
	// Simulate an armed-but-crashed run by forcing the store row to running.
	if err := e.Store.SetStatus(run.ID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	n, err := e.Store.ReconcileOrphans()
	if err != nil || n != 1 {
		t.Fatalf("want 1 orphan reconciled, got %d (%v)", n, err)
	}
	got, _ := e.Store.GetRun(run.ID)
	if got.Status != StatusInterrupted {
		t.Fatalf("want interrupted, got %s", got.Status)
	}
}

// A caller must not be able to decide an approval that belongs to a different run (PR #24
// review): approval ids are global, so without this check a token could both authorize an
// action outside its own run and strand the actual waiting run until its timeout.
func TestDecideRejectsCrossRunApproval(t *testing.T) {
	caller := &scriptedCaller{
		planner: []map[string]any{
			{"action": "unit", "description": "write the env file", "target_paths": []string{".env"}, "verification_command": "true"},
		},
		coder: [][]step{
			{{name: "write_file", args: map[string]any{"path": ".env", "content": "SECRET=x\n"}}},
		},
	}
	e := newEngine(t, caller)
	root := newRepo(t)
	run := startRun(t, e, root, "write secrets", func(rc *RunConfig) { rc.ApprovalTimeoutSec = 30 })

	var approvalID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, _ := e.Store.PendingApprovals(run.ID)
		if len(pending) > 0 {
			approvalID = pending[0].ID
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if approvalID == "" {
		t.Fatal("expected a pending approval for the denylist-gated write")
	}

	otherRoot := newRepo(t)
	other, err := e.Store.CreateRun("proj", otherRoot, RunConfig{RepoID: "r2", Goal: "unrelated", CommandAllowlist: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Decide(other.ID, approvalID, "allow", "attacker", ""); err == nil {
		t.Fatal("Decide must reject an approval that does not belong to the given run")
	}

	appr, err := e.Store.GetApproval(approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if appr.Status != ApprovalPending {
		t.Fatalf("a cross-run Decide attempt must not change the real approval's status, got %s", appr.Status)
	}

	// The owning run can still decide its own approval afterward.
	if err := e.Decide(run.ID, approvalID, "deny", "owner", ""); err != nil {
		t.Fatalf("the owning run should be able to decide its own approval: %v", err)
	}
}
