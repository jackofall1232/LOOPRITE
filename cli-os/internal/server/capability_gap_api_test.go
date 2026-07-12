package server_test

// End-to-end tests for the capability-gap fix (Issue 1 of the 2026-07-12 control-plane pass): a
// chat completion whose model emits a tool call naming an execution capability (git, gh, shell)
// that nobody in the session owns must get a short, fixed capability-gap note back -- never
// deferredClientTool's "RE-EMIT this tool call in your FINAL message" instruction, which has no
// client waiting to run it on a path like the dashboard Playground (no tools defined at all) and
// previously sent the model spiraling into free-text reasoning about a capability that would never
// appear. Drives the real HTTP path through RunChatTools via the mock adapter's "/chattool <name>
// <json-args>" directive (adapters/mock.go), which can emit a tool_call for ANY name -- including
// one, like git_command, that RunChatTools never actually owns or executes -- so the tool result
// that reaches the model's next round is genuine proof of the real dispatch decision, not a
// hand-rolled fixture.

import (
	"strings"
	"testing"
)

// A model that calls git_command with no client-defined tools gets the fixed capability-gap note,
// not the old "RE-EMIT" deferral text that has no client to act on it.
func TestChatToolsCapabilityGapForUnownedExecutionTool(t *testing.T) {
	base, token, _ := chatToolsServer(t)
	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool git_command {"args":["push"]}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	content := chatContent(t, body)
	if !strings.Contains(content, "capability_gap") {
		t.Fatalf("expected the finalization round to have echoed the capability_gap tool result, got: %q", content)
	}
	if strings.Contains(content, "RE-EMIT") {
		t.Fatalf("must never fall back to deferredClientTool's re-emit instruction for an unowned execution-shaped tool, got: %q", content)
	}
	// chatToolsServer's test app wires no engine.Engine, so propose_run is never active here --
	// the note must fall back to the by-hand-in-a-terminal next step, not the propose_run clause
	// (that combination is pinned separately at the unit level by TestCapabilityGapNote).
	if strings.Contains(content, "propose_run") {
		t.Fatalf("expected no propose_run clause (no engine wired in this test app), got: %q", content)
	}
	if !strings.Contains(content, "by hand in a terminal") {
		t.Fatalf("expected the split-the-work-by-hand next step, got: %q", content)
	}
}

// A CLIENT that defines its own tool literally named "git_command" must still get today's plain
// deferral -- the gateway must never hijack a client-owned name into a capability-gap answer the
// client never asked for, even though the name itself looks execution-shaped.
func TestChatToolsClientOwnedGitCommandNameNeverHijacked(t *testing.T) {
	base, token, _ := chatToolsServer(t)
	clientGitTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "git_command", "description": "the CLIENT's own sandboxed git tool",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool git_command {"args":["push"]}`}},
		"tools":    []any{clientGitTool},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	choices, _ := body["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in response: %v", body)
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	toolCalls, _ := msg["tool_calls"].([]any)
	if len(toolCalls) == 0 {
		t.Fatalf("expected the unexecuted \"git_command\" tool_call to pass through to the client, got message: %v", msg)
	}
	fn, _ := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "git_command" {
		t.Fatalf("expected the passed-through tool_call to be named git_command, got: %v", fn)
	}
	content, _ := msg["content"].(string)
	if strings.Contains(content, "capability_gap") {
		t.Fatalf("a client-owned tool name must never produce a capability_gap answer, got: %q", content)
	}
}
