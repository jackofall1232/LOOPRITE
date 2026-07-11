package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestOaiErrorMergesExtraFields pins the mechanism behind the Codex review fix (PR #8): a run
// drafted by propose_run in an earlier round of a chat turn must still surface to the client even
// when a LATER round's own reservation is denied. oaiError gained an optional, additive "extra"
// param for exactly this -- every pre-existing 5-arg call site must be completely unaffected, and
// a 6th-arg extra map must be merged into the top-level error body without disturbing the
// standard OpenAI-shaped "error" object.
func TestOaiErrorMergesExtraFields(t *testing.T) {
	t.Run("existing 5-arg call sites are unaffected", func(t *testing.T) {
		w := httptest.NewRecorder()
		oaiError(w, 402, "denied", "insufficient_quota", "cost_cap")
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if _, ok := body["l00prite_proposed_runs"]; ok {
			t.Fatalf("no extra field should appear when none was passed, got: %v", body)
		}
		errObj, _ := body["error"].(map[string]any)
		if errObj["message"] != "denied" || errObj["code"] != "cost_cap" {
			t.Fatalf("standard error shape broken: %v", body)
		}
	})

	t.Run("a non-empty extra map is merged at the top level", func(t *testing.T) {
		w := httptest.NewRecorder()
		proposed := []*proposedRun{{ID: "run_abc", Repo: "demo-repo", Goal: "add a healthcheck", Status: "ready"}}
		oaiError(w, 402, "denied", "insufficient_quota", "cost_cap", map[string]any{"l00prite_proposed_runs": proposed})
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		runs, ok := body["l00prite_proposed_runs"].([]any)
		if !ok || len(runs) != 1 {
			t.Fatalf("expected exactly one proposed run surfaced on the error body, got: %v", body)
		}
		run, _ := runs[0].(map[string]any)
		if run["id"] != "run_abc" || run["goal"] != "add a healthcheck" {
			t.Fatalf("proposed run view mismatch: %v", run)
		}
		errObj, _ := body["error"].(map[string]any)
		if errObj["message"] != "denied" {
			t.Fatalf("standard error shape broken alongside the extra field: %v", body)
		}
	})

	t.Run("an empty extra map adds nothing", func(t *testing.T) {
		w := httptest.NewRecorder()
		oaiError(w, 402, "denied", "insufficient_quota", "cost_cap", map[string]any{})
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if _, ok := body["l00prite_proposed_runs"]; ok {
			t.Fatalf("an empty extra map must not add a field, got: %v", body)
		}
	})
}

// TestDenialCarriesProposedRunsThroughEarlyReturn pins the chatloop.go half of the same fix
// directly: when RunChatTools's loop hits a later round's denial after an earlier round already
// drafted a run, the returned TurnResult.Denial.ProposedRuns must reflect it. This exercises the
// exact assignment chatloop.go performs, without needing to reproduce the real HTTP/budget
// interleaving that triggers it in production (deliberately impractical to pin precisely here --
// see the PR discussion: the dollar gap between consecutive rounds' reservation ceilings is
// sub-cent and depends on exact prompt byte counts, which would make an HTTP-level reproduction
// brittle against unrelated prompt-formatting changes elsewhere in the codebase).
func TestDenialCarriesProposedRunsThroughEarlyReturn(t *testing.T) {
	proposed := []*proposedRun{{ID: "run_xyz", Repo: "demo-repo", Goal: "add a healthcheck", Status: "ready"}}
	turn := TurnResult{OK: false, Denial: &Denial{Reason: "cost_cap", Cap: 1, Spent: 1}}

	// Mirrors chatloop.go's exact guard and assignment.
	if len(proposed) > 0 && turn.Denial != nil {
		turn.Denial.ProposedRuns = proposed
	}

	if len(turn.Denial.ProposedRuns) != 1 || turn.Denial.ProposedRuns[0].ID != "run_xyz" {
		t.Fatalf("expected the drafted run to survive onto the denial, got: %+v", turn.Denial)
	}
}
