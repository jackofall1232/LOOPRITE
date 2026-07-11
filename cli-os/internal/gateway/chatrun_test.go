package gateway

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// previewOnlyCaller is a minimal engine.ModelCaller good enough for BuildPreflight's team-assembly
// dry-run (PreviewRoute); Turn is never expected to be called from these tests because they never
// reach engine.StartRun.
type previewOnlyCaller struct{}

func (previewOnlyCaller) PreviewRoute(project, repoID, model string, sample map[string]any) (engine.RoutePreview, error) {
	return engine.RoutePreview{Provider: "mock", Model: "m", Reason: "test"}, nil
}

func (previewOnlyCaller) Turn(ctx context.Context, in engine.TurnInput) (engine.TurnResult, error) {
	panic("previewOnlyCaller.Turn must never be called: these tests never reach engine.StartRun")
}

// newDraftRunTestRepo makes a temp git repo with one commit -- a blocker-free BuildPreflight
// requires at least one commit and a clean tree outside .l00prite.
func newDraftRunTestRepo(t *testing.T) string {
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
	if out, err := exec.Command("git", "-C", root, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", root, "commit", "-q", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	return root
}

// newDraftRunTestApp wires an App with a real engine (backed by a temp sqlite DB) good enough to
// exercise createDraftRun/executeProposeRun end to end, with no HTTP layer involved.
func newDraftRunTestApp(t *testing.T) *App {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app := &App{DB: db}
	app.Engine = engine.New(&engine.Store{DB: db}, previewOnlyCaller{})
	return app
}

var testPrincipal = &security.Principal{TokenID: "tok_test", Project: "proj"}

// TestExecuteProposeRunDraftsOnly is the core safety-invariant test: a successful propose_run call
// must go through createDraftRun (CreateRun + BuildPreflight) ONLY. It must never leave the run in
// any state past "ready"/"blocked" -- in particular NEVER "running" -- since executeProposeRun has
// no way to call engine.StartRun at all.
func TestExecuteProposeRunDraftsOnly(t *testing.T) {
	app := newDraftRunTestApp(t)
	root := newDraftRunTestRepo(t)

	result, view := app.executeProposeRun(testPrincipal, "proj", "r1", root, map[string]any{
		"goal":              "add a hello endpoint",
		"command_allowlist": []any{"true"},
	})

	if view == nil {
		t.Fatalf("expected a proposedRun view, got nil (result=%s)", result)
	}
	if view.ID == "" {
		t.Fatal("expected a non-empty drafted run id")
	}
	if view.Repo != "r1" || view.Goal != "add a hello endpoint" {
		t.Fatalf("view mismatch: %+v", view)
	}
	// Never "running": createDraftRun can only reach draft/ready/blocked via CreateRun+BuildPreflight.
	if view.Status == engine.StatusRunning {
		t.Fatalf("a drafted run must never be running, got status %q", view.Status)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("tool result must be valid JSON: %v (%s)", err, result)
	}
	if parsed["status"] != "drafted" {
		t.Fatalf(`expected status "drafted", got %v (full: %s)`, parsed["status"], result)
	}
	if parsed["run_id"] != view.ID {
		t.Fatalf("tool result run_id %v does not match view id %v", parsed["run_id"], view.ID)
	}
	note, _ := parsed["note"].(string)
	if !strings.Contains(strings.ToLower(note), "review") || !strings.Contains(note, "EXECUTE") {
		t.Fatalf("expected the note to tell the user to review and type EXECUTE in the dashboard, got: %q", note)
	}

	// Confirm against the store directly: status persisted is never "running", and the run really
	// exists with the drafted config.
	stored, err := app.Engine.Store.GetRun(view.ID)
	if err != nil || stored == nil {
		t.Fatalf("expected the drafted run to be persisted: err=%v stored=%v", err, stored)
	}
	if stored.Status == engine.StatusRunning || stored.ConfirmedBy != "" || stored.StartedAt != "" {
		t.Fatalf("drafted run must never carry any Start-confirmation fields, got: %+v", stored)
	}
}

// TestExecuteProposeRunSurfacesBlockersWithoutStarting: a foreign, unexpired lock makes
// BuildPreflight report a blocker. The tool must surface it (mirroring the dashboard's pre-flight
// view) and the run must land in "blocked", never anywhere further along -- still just a draft the
// human must act on in the dashboard.
func TestExecuteProposeRunSurfacesBlockersWithoutStarting(t *testing.T) {
	app := newDraftRunTestApp(t)
	root := newDraftRunTestRepo(t)

	lockDir := filepath.Join(root, ".l00prite")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockJSON := `{
		"status": "active",
		"owner_agent": "other-agent",
		"owner_session": "run_other",
		"purpose": "unit test",
		"expires_at": "2999-01-01T00:00:00.000Z"
	}`
	if err := os.WriteFile(filepath.Join(lockDir, "lock.json"), []byte(lockJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	result, view := app.executeProposeRun(testPrincipal, "proj", "r1", root, map[string]any{
		"goal":              "add a hello endpoint",
		"command_allowlist": []any{"true"},
	})
	if view == nil {
		t.Fatalf("expected a proposedRun view even when pre-flight has blockers, got nil (result=%s)", result)
	}
	if view.Status != engine.StatusBlocked {
		t.Fatalf("expected the store's status to be %q with a foreign lock present, got %q", engine.StatusBlocked, view.Status)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("tool result must be valid JSON: %v (%s)", err, result)
	}
	if parsed["status"] != "drafted" {
		t.Fatalf(`expected status "drafted" (the DRAFT itself still succeeded), got %v`, parsed["status"])
	}
	blockers, _ := parsed["blockers"].([]any)
	if len(blockers) == 0 {
		t.Fatalf("expected blockers to be surfaced in the tool result, got: %s", result)
	}
	found := false
	for _, b := range blockers {
		if strings.Contains(strings.ToLower(asStr(b)), "lock") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a lock-related blocker, got: %v", blockers)
	}

	stored, err := app.Engine.Store.GetRun(view.ID)
	if err != nil || stored == nil {
		t.Fatalf("expected the drafted (blocked) run to be persisted: err=%v stored=%v", err, stored)
	}
	if stored.Status != engine.StatusBlocked {
		t.Fatalf("store must record status %q, got %q", engine.StatusBlocked, stored.Status)
	}
}

// TestExecuteProposeRunValidatesInput pins the required-field errors: no run is created for either
// a missing goal or a missing/empty command_allowlist (whose first entry is documented as the
// done-check), and the failure is reported as tool-result JSON, not a panic or an HTTP error.
func TestExecuteProposeRunValidatesInput(t *testing.T) {
	app := newDraftRunTestApp(t)
	root := newDraftRunTestRepo(t)

	t.Run("missing goal", func(t *testing.T) {
		result, view := app.executeProposeRun(testPrincipal, "proj", "r1", root, map[string]any{
			"command_allowlist": []any{"true"},
		})
		if view != nil {
			t.Fatalf("expected no run drafted for a missing goal, got %+v", view)
		}
		if !strings.Contains(result, `"error"`) || !strings.Contains(result, "goal") {
			t.Fatalf("expected a goal-required error, got: %s", result)
		}
	})

	t.Run("missing command_allowlist", func(t *testing.T) {
		result, view := app.executeProposeRun(testPrincipal, "proj", "r1", root, map[string]any{
			"goal": "do something",
		})
		if view != nil {
			t.Fatalf("expected no run drafted for a missing command_allowlist, got %+v", view)
		}
		if !strings.Contains(result, `"error"`) || !strings.Contains(result, "command_allowlist") {
			t.Fatalf("expected a command_allowlist-required error, got: %s", result)
		}
	})

	// No run should exist in the store after either failure.
	runs, err := app.Engine.Store.ListRuns("proj", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected zero runs created by invalid propose_run calls, got %d", len(runs))
	}
}

// TestExecuteProposeRunEngineUnavailable pins the defensive nil-engine guard (activeNames in
// RunChatTools already excludes propose_run when app.Engine is nil, but executeProposeRun must
// never panic or create a run if it is somehow still reached).
func TestExecuteProposeRunEngineUnavailable(t *testing.T) {
	app := &App{}
	result, view := app.executeProposeRun(testPrincipal, "proj", "r1", "/nonexistent", map[string]any{
		"goal": "x", "command_allowlist": []any{"true"},
	})
	if view != nil {
		t.Fatalf("expected no run drafted with no engine wired, got %+v", view)
	}
	if !strings.Contains(result, "engine_unavailable") {
		t.Fatalf("expected an engine_unavailable error, got: %s", result)
	}
}

// TestActiveChatToolDefinitionsIncludesProposeRunOnlyWhenActive pins RunChatTools' gating
// contract: activeChatToolDefinitions must return propose_run's definition ONLY for names present
// in the active set (mirroring exactly how the three read-only tools are already gated), so
// RunChatTools' repoRoot=="" / no-Engine / client-collision paths -- which all withhold
// "propose_run" from activeNames -- correctly withhold the tool definition too.
func TestActiveChatToolDefinitionsIncludesProposeRunOnlyWhenActive(t *testing.T) {
	withRun := activeChatToolDefinitions(map[string]bool{"read_file": true, "list_dir": true, "search_files": true, chatRunToolName: true})
	names := map[string]bool{}
	for _, d := range withRun {
		names[asStr(asMap(d.(map[string]any)["function"])["name"])] = true
	}
	if !names[chatRunToolName] {
		t.Fatalf("expected %q among active definitions when active, got %v", chatRunToolName, names)
	}

	withoutRun := activeChatToolDefinitions(map[string]bool{"read_file": true, "list_dir": true, "search_files": true})
	for _, d := range withoutRun {
		if asStr(asMap(d.(map[string]any)["function"])["name"]) == chatRunToolName {
			t.Fatalf("propose_run must not be offered when it is not in the active set (e.g. no engine, or a client-owned collision)")
		}
	}
}
