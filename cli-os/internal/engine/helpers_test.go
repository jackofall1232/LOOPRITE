package engine

import "testing"

// parseArgs must accept a tool-call "arguments" value shaped either way — a JSON-encoded string
// (the OpenAI spec shape every adapter this repo ships produces) or an already-decoded object
// (some non-compliant openai-compat upstreams do this). Before this fix, a non-string value
// silently degraded into an empty args map (PR #24 review).
func TestParseArgsAcceptsStringOrObject(t *testing.T) {
	t.Run("json string", func(t *testing.T) {
		got := parseArgs(`{"path":"a.txt","content":"hi"}`)
		if got["path"] != "a.txt" || got["content"] != "hi" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("already-decoded object", func(t *testing.T) {
		got := parseArgs(map[string]any{"path": "a.txt", "content": "hi"})
		if got["path"] != "a.txt" || got["content"] != "hi" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("empty string", func(t *testing.T) {
		got := parseArgs("")
		if len(got) != 0 {
			t.Fatalf("want empty map, got %+v", got)
		}
	})
	t.Run("nil object", func(t *testing.T) {
		var m map[string]any
		got := parseArgs(m)
		if got == nil || len(got) != 0 {
			t.Fatalf("want non-nil empty map, got %+v", got)
		}
	})
	t.Run("garbage string", func(t *testing.T) {
		got := parseArgs("not json")
		if len(got) != 0 {
			t.Fatalf("want empty map on unparseable string, got %+v", got)
		}
	})
	t.Run("unsupported type", func(t *testing.T) {
		got := parseArgs(42)
		if len(got) != 0 {
			t.Fatalf("want empty map for an unsupported type, got %+v", got)
		}
	})
}
