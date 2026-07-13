package engine

// Tests for the per-run coder tool-call budget (RunConfig.MaxToolCalls): the clamp at creation
// (mirrors MaxIterations), the frozen budget actually bounding the coder loop, that a budget ABOVE
// the engine's historical hardcoded 40 is honored (the whole point of the override), and the
// legacy-row fallback to Engine.MaxToolCalls when a pre-migration row reads back as 0.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateRunMaxToolCallsClamp: 0 -> default 40, >200 -> 200, an in-range value is kept, and the
// value round-trips through GetRun (proving the new column is wired into INSERT/scanRun).
func TestCreateRunMaxToolCallsClamp(t *testing.T) {
	s := newStore(t)
	cases := []struct{ in, want int }{
		{0, 40},     // unset -> protocol default
		{1000, 200}, // above ceiling -> clamped
		{7, 7},      // in range -> kept
		{200, 200},  // exactly the ceiling
	}
	for _, c := range cases {
		run, err := s.CreateRun("proj", "/r", RunConfig{RepoID: "r1", Goal: "g", MaxToolCalls: c.in})
		if err != nil {
			t.Fatalf("CreateRun(MaxToolCalls=%d): %v", c.in, err)
		}
		if run.Config.MaxToolCalls != c.want {
			t.Fatalf("MaxToolCalls=%d clamped to %d, want %d", c.in, run.Config.MaxToolCalls, c.want)
		}
		got, err := s.GetRun(run.ID)
		if err != nil || got == nil {
			t.Fatalf("GetRun: %v", err)
		}
		if got.Config.MaxToolCalls != c.want {
			t.Fatalf("MaxToolCalls did not round-trip through GetRun: got %d, want %d", got.Config.MaxToolCalls, c.want)
		}
	}
}

// TestRunStopsAtBudgetWithNumberedMessage: a run created with MaxToolCalls:2 whose coder never
// finishes must stop at human_review_gate after exactly the budgeted number of calls, and the
// summary must carry the number so the human knows what knob to raise. Fails against the pre-change
// code, which ignored the config value and always used the hardcoded 40.
func TestRunStopsAtBudgetWithNumberedMessage(t *testing.T) {
	caller := &scriptedCaller{
		planner: []map[string]any{
			{"action": "unit", "description": "write two files", "target_paths": []string{"a.txt", "b.txt"}, "verification_command": "true"},
		},
		coder: [][]step{
			// Two writes, NO unit_done -> the coder never finishes the unit within its budget.
			{
				{name: "write_file", args: map[string]any{"path": "a.txt", "content": "1\n"}},
				{name: "write_file", args: map[string]any{"path": "b.txt", "content": "2\n"}},
			},
		},
	}
	e := newEngine(t, caller)
	root := newRepo(t)
	run := startRun(t, e, root, "endless unit", func(rc *RunConfig) { rc.MaxToolCalls = 2 })

	final := waitTerminal(t, e, run.ID)
	if final.Boundary != BoundaryHumanReview {
		t.Fatalf("want %s, got %s/%s (%s)", BoundaryHumanReview, final.Status, final.Boundary, final.Summary)
	}
	if !strings.Contains(final.Summary, "(2)") {
		t.Fatalf("summary should name the exhausted budget (2), got: %q", final.Summary)
	}
}

// TestRunHonorsBudgetAboveEngineDefault: the coder makes 45 tool calls (44 writes + unit_done) —
// more than the engine's historical hardcoded 40 — under a run created with MaxToolCalls:60. The
// unit must COMPLETE and the run reach done. This is the direct proof the override lets a run do
// more per unit than the old fixed cap allowed; it fails against the pre-change code, whose loop
// stopped at 40 and returned human_review_gate before the coder ever reached unit_done.
func TestRunHonorsBudgetAboveEngineDefault(t *testing.T) {
	steps := make([]step, 0, 45)
	for i := 0; i < 44; i++ {
		steps = append(steps, step{name: "write_file", args: map[string]any{"path": "a.txt", "content": "iteration\n"}})
	}
	steps = append(steps, step{name: "unit_done", args: map[string]any{"summary": "wrote a.txt", "files_changed": []string{"a.txt"}}})

	caller := &scriptedCaller{
		planner: []map[string]any{
			{"action": "unit", "description": "write a.txt over many calls", "target_paths": []string{"a.txt"}, "verification_command": "true"},
			{"action": "done", "done_reason": "a.txt exists"},
		},
		coder: [][]step{steps},
	}
	e := newEngine(t, caller)
	root := newRepo(t)
	run := startRun(t, e, root, "many-call unit", func(rc *RunConfig) { rc.MaxToolCalls = 60 })

	final := waitTerminal(t, e, run.ID)
	if final.Status != StatusDone || final.Boundary != BoundaryDone {
		t.Fatalf("want done/%s, got %s/%s (%s)", BoundaryDone, final.Status, final.Boundary, final.Summary)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("a.txt not written — the 45-call unit never completed: %v", err)
	}
}

// TestLegacyRowMaxToolCallsReadsZero: a run row persisted before the max_tool_calls column existed
// reads back as 0, which is exactly the sentinel runCoder falls back on (budget = e.MaxToolCalls).
// Simulated by writing the column to 0 directly (CreateRun itself clamps 0 -> 40, so a fresh run can
// never carry 0). Pins that scanRun tolerates the legacy value rather than mis-scanning the row.
func TestLegacyRowMaxToolCallsReadsZero(t *testing.T) {
	s := newStore(t)
	run, err := s.CreateRun("proj", "/r", RunConfig{RepoID: "r1", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE runs SET max_tool_calls = 0 WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(run.ID)
	if err != nil || got == nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Config.MaxToolCalls != 0 {
		t.Fatalf("a legacy 0 row should read back as 0 (runCoder then falls back to Engine.MaxToolCalls), got %d", got.Config.MaxToolCalls)
	}
}

// TestResolveMaxToolCalls pins the single fallback-resolution helper: a legacy 0 -> fallback, any
// positive value passes through unchanged.
func TestResolveMaxToolCalls(t *testing.T) {
	if got := ResolveMaxToolCalls(0, DefaultMaxToolCalls); got != DefaultMaxToolCalls {
		t.Fatalf("0 should resolve to the fallback %d, got %d", DefaultMaxToolCalls, got)
	}
	if got := ResolveMaxToolCalls(7, DefaultMaxToolCalls); got != 7 {
		t.Fatalf("a positive value must pass through, got %d", got)
	}
	if got := ResolveMaxToolCalls(-3, 40); got != 40 {
		t.Fatalf("a non-positive value should resolve to the fallback, got %d", got)
	}
}

// TestPreflightShowsEffectiveBudgetForLegacyRow pins Codex PR #17 finding 1: a run migrated from
// v0.6 carries max_tool_calls=0 (the legacy sentinel runCoder falls back on), but the pre-flight —
// the human confirmation contract — must display the EFFECTIVE budget the engine will actually run
// with, not the raw 0. Fails against the pre-fix code, which serialized run.Config.MaxToolCalls (0).
func TestPreflightShowsEffectiveBudgetForLegacyRow(t *testing.T) {
	e := newEngine(t, &scriptedCaller{})
	root := newRepo(t)
	run, err := e.Store.CreateRun("proj", root, RunConfig{RepoID: "r1", Goal: "x", CommandAllowlist: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-v0.7 migrated row: max_tool_calls backfilled to 0 (CreateRun itself clamps 0->40).
	if _, err := e.Store.DB.ExecContext(context.Background(), `UPDATE runs SET max_tool_calls=0 WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	reloaded, err := e.Store.GetRun(run.ID)
	if err != nil || reloaded.Config.MaxToolCalls != 0 {
		t.Fatalf("setup: expected a reloaded 0-budget row, got %v (err %v)", reloaded, err)
	}
	pf, err := e.BuildPreflight(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if pf.MaxToolCalls != e.MaxToolCalls {
		t.Fatalf("pre-flight must show the EFFECTIVE budget %d for a legacy 0 row, not the raw sentinel, got %d", e.MaxToolCalls, pf.MaxToolCalls)
	}
}

// multiToolCallMsg builds one assistant message carrying SEVERAL tool_calls (a batched response),
// which the single-call toolCallMsg (run_integration_test.go) cannot express.
func multiToolCallMsg(calls []step) map[string]any {
	var tcs []any
	for i, c := range calls {
		b, _ := json.Marshal(c.args)
		tcs = append(tcs, map[string]any{
			"id": fmt.Sprintf("tc_%d", i), "type": "function",
			"function": map[string]any{"name": c.name, "arguments": string(b)},
		})
	}
	return map[string]any{"role": "assistant", "content": "", "tool_calls": tcs}
}

// batchCoderCaller returns the SAME batch of coder tool_calls in a single response every coder turn,
// so a test can prove the per-unit budget is charged per individual tool call, not per turn.
type batchCoderCaller struct {
	unitSel map[string]any
	batch   []step
}

func (c *batchCoderCaller) PreviewRoute(project, repoID, model string, sample map[string]any) (RoutePreview, error) {
	return RoutePreview{Provider: "mock", Model: "m", Reason: "test"}, nil
}

func (c *batchCoderCaller) Turn(ctx context.Context, in TurnInput) (TurnResult, error) {
	role, _ := in.Meta["role"].(string)
	res := TurnResult{Provider: "mock", Model: role + "-model", CostUSD: 0.001}
	switch role {
	case RolePlan:
		res.Message = toolCallMsg("select_unit", c.unitSel)
	case RoleCode:
		res.Message = multiToolCallMsg(c.batch)
	case RoleReview:
		res.Message = toolCallMsg("review_verdict", map[string]any{"approve": true, "must_fix": false})
	default:
		res.Message = map[string]any{"role": "assistant", "content": "ok"}
	}
	return res, nil
}

// TestBatchedToolCallsCountedIndividually pins Codex PR #17 finding 2: when a coder response carries
// several tool_calls at once, the per-unit budget must be charged per INDIVIDUAL call, not once for
// the whole batch. With max_tool_calls=1 and a 3-write batch, only the FIRST write may execute and
// the run must stop at human_review_gate. Fails against the pre-fix loop, which executed all three
// (the outer loop counted turns, so one batched turn spent only one budget unit).
func TestBatchedToolCallsCountedIndividually(t *testing.T) {
	caller := &batchCoderCaller{
		unitSel: map[string]any{"action": "unit", "description": "batch writes", "target_paths": []string{"a.txt"}, "verification_command": "true"},
		batch: []step{
			{name: "write_file", args: map[string]any{"path": "a.txt", "content": "1\n"}},
			{name: "write_file", args: map[string]any{"path": "b.txt", "content": "2\n"}},
			{name: "write_file", args: map[string]any{"path": "c.txt", "content": "3\n"}},
		},
	}
	e := newEngine(t, caller)
	root := newRepo(t)
	run := startRun(t, e, root, "batched writes", func(rc *RunConfig) { rc.MaxToolCalls = 1 })

	final := waitTerminal(t, e, run.ID)
	if final.Boundary != BoundaryHumanReview {
		t.Fatalf("want %s, got %s/%s (%s)", BoundaryHumanReview, final.Status, final.Boundary, final.Summary)
	}
	if !strings.Contains(final.Summary, "(1)") {
		t.Fatalf("summary should name the exhausted budget (1), got: %q", final.Summary)
	}
	// Exactly the first of the three batched writes may have executed.
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("the first batched write should have run: %v", err)
	}
	for _, f := range []string{"b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			t.Fatalf("%s executed despite max_tool_calls=1 — a batched response bypassed the per-call cap", f)
		}
	}
}
