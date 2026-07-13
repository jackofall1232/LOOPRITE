package server_test

// End-to-end tests for the per-project Playground tool-call budget override, driven through the
// real HTTP stack:
//   - raising the budget lets a repeating propose_run turn draft MORE than the old 5-run ceiling;
//   - a lower-only request header still caps a raised project budget;
//   - hitting the cap surfaces the additive l00prite_chat_tools hint the dashboard renders;
//   - a run created with max_tool_calls round-trips into both the run view and its pre-flight.
// Reuses chatRunServer / countRuns (chatrun_api_test.go) and runEngineServer (runs_api_test.go).

import (
	"testing"
)

// TestRaisedChatBudgetDraftsMoreRuns: with the project budget raised to 10 rounds, the mock's
// never-ending propose_run directive drafts strictly more than the 5 runs the default 6-round budget
// allows — the direct proof the override takes effect. (Unbuildable against the pre-change tree,
// which had no /v1/chat-limits endpoint and a hardcoded 6-round cap.)
func TestRaisedChatBudgetDraftsMoreRuns(t *testing.T) {
	base, token, _, app := chatRunServer(t)

	if resp, _ := doJSON(t, "POST", base+"/v1/chat-limits", token,
		map[string]any{"max_tool_rounds": 10, "max_tool_calls": 48}); resp.StatusCode != 200 {
		t.Fatalf("POST /v1/chat-limits = %d", resp.StatusCode)
	}

	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattoolloop propose_run {"goal":"add a hello endpoint","command_allowlist":["true"]}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	if body := bodyJSON(t, resp); resp.StatusCode != 200 {
		t.Fatalf("chat = %d: %v", resp.StatusCode, body)
	}

	got := countRuns(t, app, "demo")
	if got <= 5 {
		t.Fatalf("raised budget (10 rounds) should draft more than the default cap of 5, got %d", got)
	}
	if got > 9 {
		t.Fatalf("10 rounds with a forced-final last round should draft at most 9, got %d", got)
	}
}

// TestChatBudgetHeaderCanOnlyLower: a raised PROJECT budget (10 rounds) is still capped by a
// lower-only per-request header (2 rounds) -> exactly one draft (round 0 drafts, round 1 is the
// forced-final answer). Pins the "header may only lower, never raise" contract end to end.
func TestChatBudgetHeaderCanOnlyLower(t *testing.T) {
	base, token, _, app := chatRunServer(t)

	if resp, _ := doJSON(t, "POST", base+"/v1/chat-limits", token,
		map[string]any{"max_tool_rounds": 10, "max_tool_calls": 48}); resp.StatusCode != 200 {
		t.Fatalf("POST /v1/chat-limits failed")
	}

	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattoolloop propose_run {"goal":"add a hello endpoint","command_allowlist":["true"]}`}},
	}, map[string]string{"x-l00prite-repo": "demo", "x-l00prite-chat-max-rounds": "2"})
	if resp.StatusCode != 200 {
		t.Fatalf("chat = %d", resp.StatusCode)
	}
	if got := countRuns(t, app, "demo"); got != 1 {
		t.Fatalf("header lowering to 2 rounds should draft exactly 1 run, got %d", got)
	}
}

// TestChatCapHitSurfacesHint: a repeating propose_run turn on the DEFAULT budget exhausts the round
// cap; the response must carry the additive l00prite_chat_tools object (cap_reached true, real
// counts) that the Playground turns into a "budget spent — raise it or say continue" hint.
func TestChatCapHitSurfacesHint(t *testing.T) {
	base, token, _, _ := chatRunServer(t)

	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattoolloop propose_run {"goal":"add a hello endpoint","command_allowlist":["true"]}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("chat = %d: %v", resp.StatusCode, body)
	}
	ct, ok := body["l00prite_chat_tools"].(map[string]any)
	if !ok {
		t.Fatalf("expected l00prite_chat_tools on a cap-hit reply, got: %v", body["l00prite_chat_tools"])
	}
	if ct["cap_reached"] != true {
		t.Fatalf("cap_reached should be true, got %v", ct["cap_reached"])
	}
	if ct["rounds_used"] != float64(6) || ct["max_rounds"] != float64(6) {
		t.Fatalf("expected rounds_used/max_rounds = 6, got %v", ct)
	}
	if ct["tool_calls_used"] != float64(5) {
		t.Fatalf("expected 5 drafts before the forced-final round, got tool_calls_used=%v", ct["tool_calls_used"])
	}
}

// TestRunCreateMaxToolCallsRoundTrips: POST /v1/runs with max_tool_calls surfaces the clamped value
// in both the run view and the pre-flight the human confirms.
func TestRunCreateMaxToolCallsRoundTrips(t *testing.T) {
	srv, token, _ := runEngineServer(t)

	resp, body := doJSON(t, "POST", srv.URL+"/v1/runs", token, map[string]any{
		"repo": "r1", "goal": "x", "command_allowlist": []string{"true"}, "max_tool_calls": 55,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("POST /v1/runs = %d: %v", resp.StatusCode, body)
	}
	run, _ := body["run"].(map[string]any)
	if run["max_tool_calls"] != float64(55) {
		t.Fatalf("run view max_tool_calls = %v, want 55", run["max_tool_calls"])
	}
	pf, _ := body["preflight"].(map[string]any)
	if pf["max_tool_calls"] != float64(55) {
		t.Fatalf("preflight max_tool_calls = %v, want 55", pf["max_tool_calls"])
	}
}
