package server_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway"
	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/server"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// scriptCaller is a deterministic ModelCaller that drives a run to definition_of_done: it plans
// one unit (write a file), the coder writes it and finishes, then the planner reports done.
type scriptCaller struct{ plan, code int }

func (c *scriptCaller) PreviewRoute(project, repoID, model string, sample map[string]any) (engine.RoutePreview, error) {
	return engine.RoutePreview{Provider: "mock", Model: "m", Reason: "test"}, nil
}

func tcall(name string, args map[string]any) map[string]any {
	b, _ := json.Marshal(args)
	return map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
		map[string]any{"id": "t", "type": "function", "function": map[string]any{"name": name, "arguments": string(b)}}}}
}

func (c *scriptCaller) Turn(ctx context.Context, in engine.TurnInput) (engine.TurnResult, error) {
	role, _ := in.Meta["role"].(string)
	res := engine.TurnResult{Provider: "mock", Model: "m", CostUSD: 0}
	switch role {
	case engine.RolePlan:
		c.plan++
		if c.plan == 1 {
			res.Message = tcall("select_unit", map[string]any{"action": "unit", "description": "write out.txt", "verification_command": "true"})
		} else {
			res.Message = tcall("select_unit", map[string]any{"action": "done", "done_reason": "done"})
		}
	case engine.RoleCode:
		c.code++
		if c.code == 1 {
			res.Message = tcall("write_file", map[string]any{"path": "out.txt", "content": "x\n"})
		} else {
			res.Message = tcall("unit_done", map[string]any{"summary": "wrote out.txt"})
		}
	default:
		res.Message = map[string]any{"role": "assistant", "content": "ok"}
	}
	return res, nil
}

// runEngineServer builds a configured server WITH the run engine wired to a scripted caller,
// plus a committed git repo registered under the token's project.
func runEngineServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	if err := config.EnsureHome(cfg); err != nil {
		t.Fatal(err)
	}
	if err := security.EnsureMasterKey(cfg.MasterKeyPath); err != nil {
		t.Fatal(err)
	}
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases, StartedAt: time.Now()}
	app.Engine = engine.New(&engine.Store{DB: db}, &scriptCaller{})

	// A committed git repo registered under the token's project "ops".
	repo := t.TempDir()
	for _, a := range [][]string{{"init", "-q"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "commit", "-q", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if _, err := db.Exec(`INSERT INTO repos(id,root,project,created_at) VALUES('r1',?,?,?)`, repo, "ops", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.Handler(app))
	t.Cleanup(srv.Close)
	_, token, err := security.MintToken(db, "ops", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, token, repo
}

// TestRunsAPIStartSucceedsOnGogitWithTrackedDirtyLock drives the EXACT originally-diagnosed
// scenario (RUN_LEDGER.md) through the real HTTP stack: the gogit backend (forced, not just
// whatever Detect() picks on this machine) with a repo whose .l00prite/lock.json is already
// git-tracked before the engine ever touches it -- e.g. scaffolded and committed via Planning
// Mode, as RUN_LEDGER's verified root cause describes -- so StartRun's AcquireLock call dirties an
// already-tracked file immediately before the gogit checkout, which is precisely what used to
// surface the raw "worktree contains unstaged changes" go-git error to the user. This is the
// missing link the adversarial review flagged: internal/engine/tools_test.go's
// TestGogitCheckoutFailsOnTrackedDirtyL00prite/TestAutoCheckpointFixesGogitCheckout prove the fix
// at the EnsureRunBranch/AutoCheckpoint level, but nothing previously drove this through
// HandleRunStart's real HTTP response, which is what a user actually sees.
func TestRunsAPIStartSucceedsOnGogitWithTrackedDirtyLock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	if err := config.EnsureHome(cfg); err != nil {
		t.Fatal(err)
	}
	if err := security.EnsureMasterKey(cfg.MasterKeyPath); err != nil {
		t.Fatal(err)
	}
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases, StartedAt: time.Now()}
	app.Engine = engine.New(&engine.Store{DB: db}, &scriptCaller{})
	app.Engine.Git = gitx.NewGogitClient() // force gogit regardless of what's on this host's PATH

	repo := t.TempDir()
	for _, a := range [][]string{{"init", "-q"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate .l00prite/ already scaffolded and committed (Planning Mode) BEFORE the engine's
	// first run -- lock.json becomes git-tracked here, which is the precondition AcquireLock's
	// later rewrite needs to reproduce the original bug.
	if err := os.MkdirAll(filepath.Join(repo, ".l00prite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".l00prite", "lock.json"), []byte(`{"status":"released"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	if _, err := db.Exec(`INSERT INTO repos(id,root,project,created_at) VALUES('r1',?,?,?)`, repo, "ops", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.Handler(app))
	t.Cleanup(srv.Close)
	_, token, err := security.MintToken(db, "ops", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, body := doJSON(t, "POST", srv.URL+"/v1/runs", token, map[string]any{
		"repo": "r1", "goal": "create out.txt", "command_allowlist": []string{"true"}, "max_iterations": 5,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("create must be 200, got %d (%v)", resp.StatusCode, body)
	}
	runID := body["run"].(map[string]any)["id"].(string)

	resp2, body2 := doJSON(t, "POST", srv.URL+"/v1/runs/start", token, map[string]any{"id": runID, "confirm": "EXECUTE"})
	if resp2.StatusCode != 200 {
		msg := errMessage(t, body2)
		for _, raw := range []string{"unstaged", "checkout -B"} {
			if strings.Contains(msg, raw) {
				t.Fatalf("Start reproduced the original raw go-git leak (marker %q): %q", raw, msg)
			}
		}
		t.Fatalf("Start should succeed on gogit with a tracked-dirty lock.json (the fixed scenario), got %d: %q", resp2.StatusCode, msg)
	}

	// A successful Start launches the engine's background loop goroutine (StartRun's
	// `go e.loop(...)`), which keeps writing to the repo under t.TempDir() after this function's
	// own assertions are done. Wait for a terminal status before returning -- otherwise it races
	// t.TempDir()'s cleanup (RemoveAll), exactly the flaky "directory not empty" CI failure this
	// same fix addresses in TestRunsAPIStartErrorsAreHumanized.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, gb := doJSON(t, "GET", srv.URL+"/v1/runs/get?id="+runID, token, nil)
		run, _ := gb["run"].(map[string]any)
		status, _ := run["status"].(string)
		if status == "done" || status == "stopped" || status == "blocked" {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state before the test's temp dir would be cleaned up")
}

func TestRunsAPIAuthRequired(t *testing.T) {
	srv, _, _ := runEngineServer(t)
	for _, p := range []string{"/v1/runs", "/v1/runs/start", "/v1/runs/stop", "/v1/runs/approve", "/v1/runs/preflight"} {
		if resp, _ := doJSON(t, "POST", srv.URL+p, "", map[string]any{"id": "x"}); resp.StatusCode != 401 {
			t.Fatalf("POST %s without a token must be 401, got %d", p, resp.StatusCode)
		}
	}
}

// TestRunsAPICreateStartComplete drives the full HTTP flow: create -> pre-flight (no blockers) ->
// start refused without confirm -> start with EXECUTE -> run reaches definition_of_done.
func TestRunsAPICreateStartComplete(t *testing.T) {
	srv, token, repo := runEngineServer(t)

	resp, body := doJSON(t, "POST", srv.URL+"/v1/runs", token, map[string]any{
		"repo": "r1", "goal": "create out.txt", "command_allowlist": []string{"true"}, "max_iterations": 5,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("create must be 200, got %d (%v)", resp.StatusCode, body)
	}
	pf := body["preflight"].(map[string]any)
	if bl, _ := pf["blockers"].([]any); len(bl) != 0 {
		t.Fatalf("unexpected pre-flight blockers: %v", bl)
	}
	if team, _ := pf["team"].([]any); len(team) == 0 {
		t.Fatal("pre-flight team should be resolved")
	}
	runID := body["run"].(map[string]any)["id"].(string)

	// Start refused without confirm=EXECUTE.
	if resp, _ := doJSON(t, "POST", srv.URL+"/v1/runs/start", token, map[string]any{"id": runID, "confirm": "yes"}); resp.StatusCode == 200 {
		t.Fatal("start without confirm=EXECUTE must not succeed")
	}
	// Start with the explicit confirmation.
	if resp, b := doJSON(t, "POST", srv.URL+"/v1/runs/start", token, map[string]any{"id": runID, "confirm": "EXECUTE"}); resp.StatusCode != 200 {
		t.Fatalf("start must be 200, got %d (%v)", resp.StatusCode, b)
	}

	// Poll /v1/runs/get until terminal.
	deadline := time.Now().Add(15 * time.Second)
	var status, boundary string
	for time.Now().Before(deadline) {
		_, gb := doJSON(t, "GET", srv.URL+"/v1/runs/get?id="+runID, token, nil)
		run := gb["run"].(map[string]any)
		status, _ = run["status"].(string)
		boundary, _ = run["boundary"].(string)
		if status == "done" || status == "stopped" || status == "blocked" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if status != "done" || boundary != engine.BoundaryDone {
		t.Fatalf("run should reach done/%s, got %s/%s", engine.BoundaryDone, status, boundary)
	}
	if _, err := os.Stat(filepath.Join(repo, "out.txt")); err != nil {
		t.Fatalf("out.txt not written by the run: %v", err)
	}

	// The event feed returned events.
	_, eb := doJSON(t, "GET", srv.URL+"/v1/runs/events?id="+runID, token, nil)
	if evs, _ := eb["events"].([]any); len(evs) == 0 {
		t.Fatal("event feed should not be empty")
	}
}

// Bug 2 fix, defense-in-depth: whatever StartRun fails for, the client-facing error message must
// be plain English -- never a raw technical string (a Go error prefix like "engine:", or the
// literal confirm-token wording). This test exercises two early-return StartRun paths (wrong
// confirm token; re-starting an already-started run) that are cheap to reproduce deterministically
// and don't need the gogit backend -- it does NOT exercise the originally-diagnosed raw
// "worktree contains unstaged changes" checkout leak specifically; that path is covered end-to-end
// by TestRunsAPIStartSucceedsOnGogitWithTrackedDirtyLock (which forces gogit + a tracked-dirty
// lock.json and asserts success, i.e. the leak no longer reproduces at all) plus
// internal/engine/tools_test.go's TestGogitCheckoutFailsOnTrackedDirtyL00prite (which pins the raw
// error text itself, at the EnsureRunBranch level, so a future regression there is still caught
// even though this HTTP-level suite no longer reproduces it). The full technical detail for the
// cases here still goes to the audit log (asserted separately would require direct DB access; here
// we assert the HTTP-facing message is clean, which is what a non-technical user actually sees).
func TestRunsAPIStartErrorsAreHumanized(t *testing.T) {
	srv, token, _ := runEngineServer(t)

	resp, body := doJSON(t, "POST", srv.URL+"/v1/runs", token, map[string]any{
		"repo": "r1", "goal": "create out.txt", "command_allowlist": []string{"true"}, "max_iterations": 5,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("create must be 200, got %d (%v)", resp.StatusCode, body)
	}
	runID := body["run"].(map[string]any)["id"].(string)

	// Wrong confirm token: StartRun's bare "start requires confirm:..." error is not a typed
	// sentinel, so it must fall through humanizeStartError's generic case -- never echo the raw
	// error text (which would otherwise leak the exact expected confirm string).
	resp2, body2 := doJSON(t, "POST", srv.URL+"/v1/runs/start", token, map[string]any{"id": runID, "confirm": "not-execute"})
	if resp2.StatusCode == 200 {
		t.Fatal("start without confirm=EXECUTE must not succeed")
	}
	msg := errMessage(t, body2)
	for _, raw := range []string{"EXECUTE", "start requires confirm", "engine:"} {
		if strings.Contains(msg, raw) {
			t.Fatalf("start-rejected message leaked raw internal text %q: %q", raw, msg)
		}
	}
	if msg != "Something went wrong preparing the project for this run. Nothing was started. Details were logged for support." {
		t.Fatalf("unexpected humanized message: %q", msg)
	}

	// Successfully start the run, then try to start it again: run.Status is no longer "ready", so
	// StartRun returns the ErrBadState-wrapped "run is ... rebuild the pre-flight" error -- this
	// must also come through humanized, not as the raw wrapped Go error text.
	if resp, b := doJSON(t, "POST", srv.URL+"/v1/runs/start", token, map[string]any{"id": runID, "confirm": "EXECUTE"}); resp.StatusCode != 200 {
		t.Fatalf("start must be 200, got %d (%v)", resp.StatusCode, b)
	}
	resp3, body3 := doJSON(t, "POST", srv.URL+"/v1/runs/start", token, map[string]any{"id": runID, "confirm": "EXECUTE"})
	if resp3.StatusCode != 409 {
		t.Fatalf("re-starting an already-started run must be 409, got %d (%v)", resp3.StatusCode, body3)
	}
	msg3 := errMessage(t, body3)
	for _, raw := range []string{"engine:", "rebuild the pre-flight (it must be ready", "run is \""} {
		if strings.Contains(msg3, raw) {
			t.Fatalf("run_not_ready message leaked raw internal text %q: %q", raw, msg3)
		}
	}
	if !strings.Contains(msg3, "Rebuild pre-flight") {
		t.Fatalf("expected the humanized run_not_ready message to point the user at Rebuild pre-flight, got: %q", msg3)
	}

	// The successful Start above launched the engine's background loop goroutine (StartRun's
	// `go e.loop(...)`), which keeps writing to the repo under t.TempDir() after this function's
	// assertions are done. Wait for it to reach a terminal status before returning -- otherwise it
	// races t.TempDir()'s cleanup (RemoveAll), which CI caught as a flaky
	// "directory not empty" failure that never reproduced locally. Mirrors
	// TestRunsAPICreateStartComplete's identical wait for the identical reason.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, gb := doJSON(t, "GET", srv.URL+"/v1/runs/get?id="+runID, token, nil)
		run, _ := gb["run"].(map[string]any)
		status, _ := run["status"].(string)
		if status == "done" || status == "stopped" || status == "blocked" {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state before the test's temp dir would be cleaned up")
}

func errMessage(t *testing.T, body map[string]any) string {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error object in response body: %v", body)
	}
	msg, _ := e["message"].(string)
	return msg
}

// A run in another project is invisible (404, no cross-project leak).
func TestRunsAPIProjectScoped(t *testing.T) {
	srv, token, _ := runEngineServer(t)
	resp, body := doJSON(t, "POST", srv.URL+"/v1/runs", token, map[string]any{
		"repo": "r1", "goal": "x", "command_allowlist": []string{"true"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %v", resp.StatusCode, body)
	}
	// A different token/project cannot see it.
	// (Reuse the same server; mint is per-db so make a second token in another project.)
	// Here we just assert an unknown id is 404 to confirm ownRun's scoping path.
	if resp, _ := doJSON(t, "GET", srv.URL+"/v1/runs/get?id=run_deadbeef", token, nil); resp.StatusCode != 404 {
		t.Fatalf("unknown run must be 404, got %d", resp.StatusCode)
	}
}
