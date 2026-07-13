package engine

// Tests for the per-run coder tool-call budget (RunConfig.MaxToolCalls): the clamp at creation
// (mirrors MaxIterations), the frozen budget actually bounding the coder loop, that a budget ABOVE
// the engine's historical hardcoded 40 is honored (the whole point of the override), and the
// legacy-row fallback to Engine.MaxToolCalls when a pre-migration row reads back as 0.

import (
	"context"
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
