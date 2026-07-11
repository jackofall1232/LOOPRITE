package gateway

import "testing"

// TestStripChatToolsClearsToolChoiceReferencingRemovedTool pins a PR #6 review finding: a request
// that pins tool_choice to one of the chat tools (e.g.
// {"type":"function","function":{"name":"read_file"}}) still had that tool_choice intact after
// stripChatTools removed read_file from the tools array on a forced-final round -- forcing the
// model to call a tool that no longer exists in its own tools list, which most upstream providers
// reject as invalid input. Mirrors stripBridgeTool's existing tool_choice handling (bridge.go).
func TestStripChatToolsClearsToolChoiceReferencingRemovedTool(t *testing.T) {
	activeNames := map[string]bool{"read_file": true, "list_dir": true, "search_files": true}

	t.Run("tool_choice naming a stripped chat tool is cleared", func(t *testing.T) {
		req := map[string]any{
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}},
			},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}},
		}
		out := stripChatTools(req, activeNames)
		if _, ok := out["tool_choice"]; ok {
			t.Fatalf("expected tool_choice to be cleared, got %v", out["tool_choice"])
		}
		if _, ok := out["tools"]; ok {
			t.Fatalf("expected tools to be fully removed (only chat tools were present), got %v", out["tools"])
		}
	})

	t.Run("tool_choice naming a surviving client tool is preserved", func(t *testing.T) {
		req := map[string]any{
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}},
				map[string]any{"type": "function", "function": map[string]any{"name": "client_tool"}},
			},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "client_tool"}},
		}
		out := stripChatTools(req, activeNames)
		tc := asMap(out["tool_choice"])
		if asStr(asMap(tc["function"])["name"]) != "client_tool" {
			t.Fatalf("expected tool_choice naming a surviving client tool to be preserved, got %v", out["tool_choice"])
		}
		tools := asArr(out["tools"])
		if len(tools) != 1 || asStr(asMap(asMap(tools[0])["function"])["name"]) != "client_tool" {
			t.Fatalf("expected only client_tool to survive in tools, got %v", tools)
		}
	})

	t.Run("string tool_choice (auto/required/none) is left untouched", func(t *testing.T) {
		req := map[string]any{
			"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}}},
			"tool_choice": "auto",
		}
		out := stripChatTools(req, activeNames)
		if out["tool_choice"] != "auto" {
			t.Fatalf("expected string tool_choice to be left untouched, got %v", out["tool_choice"])
		}
	})
}
