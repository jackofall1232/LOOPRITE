package gateway

// Tests for the Auto-PR toggle's integration into run creation (createDraftRun, runs.go): a
// project with the toggle ON must have push/pr_create auto-filled at creation and visible in the
// SAME pre-flight the human reviews before EXECUTE -- no invisible auto-approval -- and this must
// hold identically whether the run was drafted via POST /v1/runs or the propose_run chat tool,
// since both go through createDraftRun.

import (
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
)

// newAutoPRRunTestApp and newGitRepo reuse chatrun_test.go's existing helpers
// (newDraftRunTestApp / newDraftRunTestRepo) -- same wiring (real engine over a temp sqlite DB,
// previewOnlyCaller for BuildPreflight's routing preview), just named for this file's readability.
func newAutoPRRunTestApp(t *testing.T) *App { return newDraftRunTestApp(t) }
func newGitRepo(t *testing.T) string        { return newDraftRunTestRepo(t) }

// TestCreateDraftRunFillsGatesWhenToggleOn is the no-invisible-auto-approval assertion: toggle ON
// + nil caller gates -> the STORED run and its returned pre-flight both show push/pr_create as
// auto_approve.
func TestCreateDraftRunFillsGatesWhenToggleOn(t *testing.T) {
	app := newAutoPRRunTestApp(t)
	tok := mintToken(t, app, "proj-run-on")
	doAuthed(app, "POST", "/v1/auto-pr", tok, map[string]any{"enabled": true}, app.HandleAutoPRSet)

	root := newGitRepo(t)
	run, pf, err := app.createDraftRun("proj-run-on", root, engine.RunConfig{
		RepoID: "r1", Goal: "test goal", CommandAllowlist: []string{"true"},
	})
	if run == nil {
		t.Fatalf("createDraftRun failed: %v", err)
	}
	if run.Config.Gates[engine.GatePush] != engine.PolicyAutoApprove {
		t.Fatalf("stored run.Config.Gates[push] = %q, want auto_approve: %+v", run.Config.Gates[engine.GatePush], run.Config.Gates)
	}
	if run.Config.Gates[engine.GatePRCreate] != engine.PolicyAutoApprove {
		t.Fatalf("stored run.Config.Gates[pr_create] = %q, want auto_approve: %+v", run.Config.Gates[engine.GatePRCreate], run.Config.Gates)
	}
	// The pre-flight display -- what the human actually reviews before EXECUTE -- must show it too.
	if pf == nil {
		t.Fatal("expected a built pre-flight")
	}
	if pf.Gates[engine.GatePush] != engine.PolicyAutoApprove || pf.Gates[engine.GatePRCreate] != engine.PolicyAutoApprove {
		t.Fatalf("pre-flight gates = %+v, want push/pr_create = auto_approve (no invisible auto-approval)", pf.Gates)
	}
	// Merge must never be affected, even implicitly, and every other class stays require_approval.
	if pf.Gates[engine.GateMerge] != engine.PolicyRequireApproval {
		t.Fatalf("merge must never be auto-approved by the toggle, got %q", pf.Gates[engine.GateMerge])
	}
}

// TestCreateDraftRunExplicitPolicySurvivesToggleOn confirms an explicit require_approval on push
// survives run creation even with the toggle ON, while pr_create (left absent) still gets filled.
func TestCreateDraftRunExplicitPolicySurvivesToggleOn(t *testing.T) {
	app := newAutoPRRunTestApp(t)
	tok := mintToken(t, app, "proj-run-explicit")
	doAuthed(app, "POST", "/v1/auto-pr", tok, map[string]any{"enabled": true}, app.HandleAutoPRSet)

	root := newGitRepo(t)
	run, _, err := app.createDraftRun("proj-run-explicit", root, engine.RunConfig{
		RepoID: "r1", Goal: "test goal", CommandAllowlist: []string{"true"},
		Gates: map[string]string{engine.GatePush: engine.PolicyRequireApproval},
	})
	if run == nil {
		t.Fatalf("createDraftRun failed: %v", err)
	}
	if run.Config.Gates[engine.GatePush] != engine.PolicyRequireApproval {
		t.Fatalf("explicit push policy must survive the toggle, got %q", run.Config.Gates[engine.GatePush])
	}
	if run.Config.Gates[engine.GatePRCreate] != engine.PolicyAutoApprove {
		t.Fatalf("absent pr_create should still be filled, got %q", run.Config.Gates[engine.GatePRCreate])
	}
}

// TestCreateDraftRunUntouchedWhenToggleOff confirms a project that never enabled Auto-PR gets a
// run byte-identical to today: no gates auto-filled at all.
func TestCreateDraftRunUntouchedWhenToggleOff(t *testing.T) {
	app := newAutoPRRunTestApp(t)
	root := newGitRepo(t)
	run, _, err := app.createDraftRun("proj-run-off", root, engine.RunConfig{
		RepoID: "r1", Goal: "test goal", CommandAllowlist: []string{"true"},
	})
	if run == nil {
		t.Fatalf("createDraftRun failed: %v", err)
	}
	if len(run.Config.Gates) != 0 {
		t.Fatalf("toggle OFF should leave Gates empty, got %+v", run.Config.Gates)
	}
}

// TestProposeRunChatPathInheritsAutoPRIdentically confirms the chat-drafted-run surface
// (executeProposeRun) needs zero propose_run-specific code to inherit the toggle: it flows
// through the same createDraftRun merge point as POST /v1/runs.
func TestProposeRunChatPathInheritsAutoPRIdentically(t *testing.T) {
	app := newAutoPRRunTestApp(t)
	tok := mintToken(t, app, "proj-chat-on")
	doAuthed(app, "POST", "/v1/auto-pr", tok, map[string]any{"enabled": true}, app.HandleAutoPRSet)
	root := newGitRepo(t)

	principal := &security.Principal{TokenID: "tok_test", Project: "proj-chat-on"}
	_, proposed := app.executeProposeRun(principal, "proj-chat-on", "r1", root, map[string]any{
		"goal":              "build it and open a PR",
		"command_allowlist": []any{"true"},
	})
	if proposed == nil {
		t.Fatal("expected a drafted run")
	}
	run, err := app.Engine.Store.GetRun(proposed.ID)
	if err != nil || run == nil {
		t.Fatalf("could not load the chat-drafted run: %v", err)
	}
	if run.Config.Gates[engine.GatePush] != engine.PolicyAutoApprove || run.Config.Gates[engine.GatePRCreate] != engine.PolicyAutoApprove {
		t.Fatalf("chat-drafted run should inherit the toggle identically, got gates=%+v", run.Config.Gates)
	}
	// Still just a draft: cannot have reached a startable/running state without a fresh
	// pre-flight + human EXECUTE -- the auto_approve gates are visible, not yet actionable.
	if run.Status == engine.StatusRunning {
		t.Fatalf("a chat-drafted run must never be running, got status=%q", run.Status)
	}
}
