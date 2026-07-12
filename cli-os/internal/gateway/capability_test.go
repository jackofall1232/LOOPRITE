package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExecutionShapedTool pins the fixed set/prefix boundary. This function's ONLY job is picking
// which of two fixed sentences a tool call nobody owns gets -- it must never mistake an ordinary
// client tool (e.g. a weather lookup) for something that needs execution access, and it must never
// miss the engine's own tool names or their common aliases.
func TestExecutionShapedTool(t *testing.T) {
	positive := []string{
		"git_command", "run_command", "write_file", "edit_file", "apply_patch",
		"bash", "shell", "exec_command", "terminal", "gh", "git", "create_pull_request",
		"gh_pr_create", "git_push", "push_to_remote", "git_",
	}
	for _, name := range positive {
		if !executionShapedTool(name) {
			t.Errorf("expected executionShapedTool(%q) = true", name)
		}
	}

	negative := []string{
		"", "read_file", "list_dir", "search_files", "propose_run",
		"get_weather", "lookup_customer", "send_email",
		// near-miss: shares a prefix substring with "git"/"gh" but is not in either prefix family
		// (no "git_"/"gh_"/"push_" boundary) -- pinning that the prefix match is exact, not a loose
		// substring check, so an unrelated client tool sharing a few leading letters is never swept in.
		"github_search", "githubapi", "ghost_write", "gitignore_helper",
	}
	for _, name := range negative {
		if executionShapedTool(name) {
			t.Errorf("expected executionShapedTool(%q) = false", name)
		}
	}
}

// TestCapabilityGapNote pins the fixed-string discipline (mirrors humanizeStartError, runs.go):
// the note is always one of exactly two fixed sentences, selected only by proposeRunAvailable,
// and the tool name is echoed back verbatim as data (never interpolated into prose that could be
// mistaken for reasoning text).
func TestCapabilityGapNote(t *testing.T) {
	t.Run("propose_run clause present when available", func(t *testing.T) {
		raw := capabilityGapNote("git_command", true)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("expected valid JSON, got %v: %s", err, raw)
		}
		if parsed["status"] != "capability_gap" {
			t.Fatalf("expected status=capability_gap, got %v", parsed["status"])
		}
		if parsed["tool"] != "git_command" {
			t.Fatalf("expected tool=git_command, got %v", parsed["tool"])
		}
		note, _ := parsed["note"].(string)
		if !strings.Contains(note, "propose_run") {
			t.Fatalf("expected the propose_run next-step clause, got: %q", note)
		}
		if !strings.Contains(note, "one or two plain sentences") {
			t.Fatalf("expected the short-plain-language instruction, got: %q", note)
		}
		if strings.Contains(note, "RE-EMIT") {
			t.Fatalf("must never contain deferredClientTool's re-emit instruction, got: %q", note)
		}
	})

	t.Run("propose_run clause absent when unavailable (RunBridge's case)", func(t *testing.T) {
		raw := capabilityGapNote("bash", false)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("expected valid JSON, got %v: %s", err, raw)
		}
		note, _ := parsed["note"].(string)
		if strings.Contains(note, "propose_run") {
			t.Fatalf("propose_run clause must be absent when unavailable, got: %q", note)
		}
		if !strings.Contains(note, "by hand in a terminal") {
			t.Fatalf("expected the split-the-work-by-hand next step, got: %q", note)
		}
	})

	t.Run("empty tool name falls back to a fixed placeholder, never an empty/blank field", func(t *testing.T) {
		raw := capabilityGapNote("", false)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("expected valid JSON, got %v: %s", err, raw)
		}
		if parsed["tool"] != "unknown" {
			t.Fatalf("expected tool=unknown for an empty name, got %v", parsed["tool"])
		}
	})
}

// TestUnownedToolResult is the direct regression test for the actual seam both RunChatTools
// (chatloop.go) and RunBridge (bridge.go) call at their "nobody owns this call" dispatch branch.
// Before this change, that branch called deferredClientTool(tc) unconditionally for every
// unowned name; this pins the corrected behavior and would fail against that old, unconditional
// call (confirmed manually below in the "pre-fix behavior" subtest).
func TestUnownedToolResult(t *testing.T) {
	gitCall := map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "git_command", "arguments": `{"args":["push"]}`}}
	weatherCall := map[string]any{"id": "call_2", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{}`}}

	t.Run("execution-shaped name nobody owns gets the capability-gap note", func(t *testing.T) {
		got := unownedToolResult(gitCall, false /* clientOwned */, true /* proposeRunAvailable */)
		var parsed map[string]any
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("invalid JSON: %v: %s", err, got)
		}
		if parsed["status"] != "capability_gap" {
			t.Fatalf("expected capability_gap, got: %s", got)
		}
	})

	t.Run("non-execution-shaped name nobody owns keeps today's deferral untouched", func(t *testing.T) {
		got := unownedToolResult(weatherCall, false, true)
		want := deferredClientTool(weatherCall)
		if got != want {
			t.Fatalf("expected unchanged deferredClientTool output for a non-execution-shaped name\ngot:  %s\nwant: %s", got, want)
		}
	})

	t.Run("client-owned execution-shaped name is NEVER hijacked into a capability-gap answer", func(t *testing.T) {
		// A client that defines its own tool literally named "git_command" (e.g. its own sandboxed
		// git wrapper) must still get deferredClientTool -- the clientOwned check wins over
		// executionShapedTool regardless of how execution-shaped the name looks.
		got := unownedToolResult(gitCall, true /* clientOwned */, true)
		want := deferredClientTool(gitCall)
		if got != want {
			t.Fatalf("a client-owned tool name must never be hijacked\ngot:  %s\nwant: %s", got, want)
		}
		if strings.Contains(got, "capability_gap") {
			t.Fatalf("client-owned call must never produce a capability_gap result, got: %s", got)
		}
	})

	t.Run("pre-fix behavior would have failed the execution-shaped case", func(t *testing.T) {
		// Reproduces the OLD unconditional call this file's dispatch branches used to make, to
		// confirm the new test above genuinely discriminates old from new behavior rather than
		// passing by coincidence.
		oldBehavior := deferredClientTool(gitCall)
		if strings.Contains(oldBehavior, "capability_gap") {
			t.Fatalf("sanity check failed: deferredClientTool alone was never supposed to produce capability_gap")
		}
		if !strings.Contains(oldBehavior, "RE-EMIT") {
			t.Fatalf("sanity check failed: expected the old re-emit instruction in deferredClientTool's own output")
		}
	})
}
