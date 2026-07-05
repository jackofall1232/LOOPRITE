package adapters

import (
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
)

func TestAnthropicRequestTranslation(t *testing.T) {
	a := anthropicAdapter{}
	req := map[string]any{
		"model": "x", "max_tokens": float64(256),
		"messages": []any{
			map[string]any{"role": "system", "content": "be terse"},
			map[string]any{"role": "user", "content": "hi"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "get_weather", "description": "w",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
		}}},
		"tool_choice": "auto",
	}
	body := a.BuildRequest("claude-x", req, false)
	if body["system"] != "be terse" {
		t.Fatalf("system must be a top-level field, got %v", body["system"])
	}
	if numToInt(body["max_tokens"]) != 256 {
		t.Fatalf("max_tokens want 256 got %v", body["max_tokens"])
	}
	if len(asArr(body["messages"])) != 1 {
		t.Fatalf("system message must not remain in messages, got %d", len(asArr(body["messages"])))
	}
	tool0 := asMap(asArr(body["tools"])[0])
	if tool0["name"] != "get_weather" {
		t.Fatalf("tool name want get_weather got %v", tool0["name"])
	}
	if tool0["input_schema"] == nil {
		t.Fatalf("tools must use input_schema, not function wrapper")
	}
	tc := asMap(body["tool_choice"])
	if tc["type"] != "auto" {
		t.Fatalf("tool_choice want {type:auto} got %v", tc)
	}
}

func TestAnthropicSSEFold(t *testing.T) {
	a := anthropicAdapter{}
	st := a.NewStream("claude-x", false)
	events := []SSEEvent{
		{Data: `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}`},
		{Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`},
		{Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
		{Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`},
		{Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
		{Data: `{"type":"message_stop"}`},
	}
	var text strings.Builder
	done := false
	var usage *oai.Usage
	for _, ev := range events {
		out := st.OnEvent(ev)
		for _, ch := range out.Deltas {
			d := asMap(asMap(asArr(ch["choices"])[0])["delta"])
			if c, ok := d["content"].(string); ok {
				text.WriteString(c)
			}
		}
		if out.Usage != nil {
			usage = out.Usage
		}
		if out.Done {
			done = true
		}
	}
	if text.String() != "Hello world" {
		t.Fatalf("text want %q got %q", "Hello world", text.String())
	}
	if !done {
		t.Fatalf("stream must finish")
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 2 {
		t.Fatalf("usage want in=10 out=2 got %+v", usage)
	}
}

func TestOpenAICompatDropsStreamOptions(t *testing.T) {
	oc := openaiCompatAdapter{}
	body := oc.BuildRequest("gpt-x", map[string]any{
		"messages": []any{}, "stream": true, "stream_options": map[string]any{"include_usage": true},
	}, false)
	if _, ok := body["stream"]; ok {
		t.Fatalf("stream must be dropped on a non-stream call")
	}
	if _, ok := body["stream_options"]; ok {
		t.Fatalf("stream_options must not accompany a non-stream request")
	}
}
