// Anthropic native adapter. Anthropic has no production OpenAI-compatible endpoint, so this is a
// real bidirectional translator: OpenAI Chat Completions <-> /v1/messages, including the
// system-as-field difference, input_schema tools, tool_use/tool_result blocks, and the typed SSE
// event stream rebuilt into OpenAI chunk deltas. Ported from anthropic.js.
package adapters

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
)

type anthropicAdapter struct{}

func (anthropicAdapter) Kind() string { return "native-messages" }
func (anthropicAdapter) Direct() bool { return false }

var stopMap = map[string]string{
	"end_turn": "stop", "max_tokens": "length", "stop_sequence": "stop",
	"tool_use": "tool_calls", "refusal": "content_filter", "pause_turn": "stop",
}

var dataURIRE = regexp.MustCompile(`^data:([^;]+);base64,(.*)$`)

func imageBlock(url string) map[string]any {
	if m := dataURIRE.FindStringSubmatch(url); m != nil {
		return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": m[1], "data": m[2]}}
	}
	return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}}
}

// toAnthropicContent converts an OpenAI message content into Anthropic content blocks.
func toAnthropicContent(content any) []any {
	if arr := asArr(content); arr != nil {
		out := make([]any, 0, len(arr))
		for _, p := range arr {
			pm := asMap(p)
			switch {
			case pm != nil && asStr(pm["type"]) == "text":
				out = append(out, map[string]any{"type": "text", "text": pm["text"]})
			case pm != nil && asStr(pm["type"]) == "image_url":
				out = append(out, imageBlock(asStr(asMap(pm["image_url"])["url"])))
			default:
				if s, ok := p.(string); ok {
					out = append(out, map[string]any{"type": "text", "text": s})
				} else {
					out = append(out, map[string]any{"type": "text", "text": jsonStringify(p)})
				}
			}
		}
		return out
	}
	// non-array content: single text block (nil -> "")
	text := ""
	if content != nil {
		text = asStr(content)
	}
	return []any{map[string]any{"type": "text", "text": text}}
}

func (anthropicAdapter) BuildRequest(model string, req map[string]any, stream bool) map[string]any {
	var systemParts []string
	var messages []any
	for _, mm := range asArr(req["messages"]) {
		m := asMap(mm)
		if m == nil {
			continue
		}
		role := asStr(m["role"])
		switch {
		case role == "system":
			if s, ok := m["content"].(string); ok {
				systemParts = append(systemParts, s)
			} else {
				var texts []string
				for _, b := range toAnthropicContent(m["content"]) {
					bm := asMap(b)
					if asStr(bm["type"]) == "text" {
						texts = append(texts, asStr(bm["text"]))
					}
				}
				systemParts = append(systemParts, strings.Join(texts, "\n"))
			}
		case role == "tool":
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": m["tool_call_id"], "content": asStrOrEmpty(m["content"]),
				}},
			})
		case role == "assistant" && len(asArr(m["tool_calls"])) > 0:
			var blocks []any
			if m["content"] != nil && asStr(m["content"]) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": asStr(m["content"])})
			}
			for _, tcRaw := range asArr(m["tool_calls"]) {
				tc := asMap(tcRaw)
				fn := asMap(tc["function"])
				var input any = map[string]any{}
				args := asStr(fn["arguments"])
				if args == "" {
					args = "{}"
				}
				var parsed any
				if err := json.Unmarshal([]byte(args), &parsed); err == nil {
					input = parsed
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc["id"], "name": fn["name"], "input": input})
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
		default:
			r := "user"
			if role == "assistant" {
				r = "assistant"
			}
			messages = append(messages, map[string]any{"role": r, "content": toAnthropicContent(m["content"])})
		}
	}

	maxTokens := numToInt(req["max_tokens"])
	if maxTokens == 0 {
		maxTokens = numToInt(req["max_completion_tokens"])
	}
	if maxTokens == 0 {
		maxTokens = 1024
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens, // REQUIRED by Anthropic
		"messages":   messages,
	}
	if len(systemParts) > 0 {
		body["system"] = strings.Join(systemParts, "\n\n")
	}
	if stream {
		body["stream"] = true
	}
	if req["temperature"] != nil {
		body["temperature"] = req["temperature"]
	}
	if stop := req["stop"]; jsTruthy(stop) {
		if arr := asArr(stop); arr != nil {
			body["stop_sequences"] = arr
		} else {
			body["stop_sequences"] = []any{stop}
		}
	}
	if tools := asArr(req["tools"]); len(tools) > 0 {
		outTools := []any{} // matches Node: a client `tools` present but all-non-function yields []
		for _, tRaw := range tools {
			t := asMap(tRaw)
			if asStr(t["type"]) != "function" || asMap(t["function"]) == nil {
				continue
			}
			fn := asMap(t["function"])
			desc := asStr(fn["description"])
			var schema any = fn["parameters"]
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			outTools = append(outTools, map[string]any{"name": fn["name"], "description": desc, "input_schema": schema})
		}
		body["tools"] = outTools
		if tcRaw := req["tool_choice"]; jsTruthy(tcRaw) {
			body["tool_choice"] = translateToolChoice(tcRaw)
		}
	}
	return body
}

func translateToolChoice(tc any) map[string]any {
	if s, ok := tc.(string); ok {
		switch s {
		case "auto":
			return map[string]any{"type": "auto"}
		case "none":
			return map[string]any{"type": "none"}
		case "required":
			return map[string]any{"type": "any"}
		default:
			return map[string]any{"type": "auto"}
		}
	}
	if m := asMap(tc); m != nil && asStr(m["type"]) == "function" {
		return map[string]any{"type": "tool", "name": asMap(m["function"])["name"]}
	}
	return map[string]any{"type": "auto"}
}

func (anthropicAdapter) URL(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/") + "/v1/messages"
}

func (anthropicAdapter) Headers(apiKey string) map[string]string {
	return map[string]string{
		"content-type":      "application/json",
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	}
}

func usageFrom(u map[string]any) oai.Usage {
	return oai.Usage{
		PromptTokens:     numToInt(u["input_tokens"]),
		CompletionTokens: numToInt(u["output_tokens"]),
		CacheReadTokens:  numToInt(u["cache_read_input_tokens"]),
		CacheWriteTokens: numToInt(u["cache_creation_input_tokens"]),
	}
}

func (anthropicAdapter) ParseFull(resp map[string]any, model string) FullResult {
	content := asArr(resp["content"])
	var textParts []string
	var toolUses []map[string]any
	for _, b := range content {
		bm := asMap(b)
		switch asStr(bm["type"]) {
		case "text":
			textParts = append(textParts, asStr(bm["text"]))
		case "tool_use":
			toolUses = append(toolUses, bm)
		}
	}
	text := strings.Join(textParts, "")
	message := map[string]any{"role": "assistant"}
	if text != "" {
		message["content"] = text
	} else {
		message["content"] = nil
	}
	if len(toolUses) > 0 {
		var calls []any
		for _, b := range toolUses {
			input := b["input"]
			if input == nil {
				input = map[string]any{}
			}
			calls = append(calls, map[string]any{
				"id": b["id"], "type": "function",
				"function": map[string]any{"name": b["name"], "arguments": jsonStringify(input)},
			})
		}
		message["tool_calls"] = calls
	}
	u := usageFrom(asMap(resp["usage"]))
	finish := stopMap[asStr(resp["stop_reason"])]
	if finish == "" {
		finish = "stop"
	}
	return FullResult{
		Response: oai.Response(oai.CmplID(), model, message, finish, u),
		Usage:    u,
	}
}

func (anthropicAdapter) NewStream(model string, _ bool) StreamTranslator {
	return &anthropicStream{id: oai.CmplID(), model: model, usage: oai.ZeroUsage(), toolIndex: -1}
}

type anthropicStream struct {
	id        string
	model     string
	usage     oai.Usage
	finish    string
	toolIndex int
	blockType string
}

func (s *anthropicStream) OnEvent(ev SSEEvent) StreamOut {
	var data map[string]any
	if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
		return StreamOut{}
	}
	var out []map[string]any
	switch asStr(data["type"]) {
	case "message_start":
		if msg := asMap(data["message"]); msg != nil {
			if u := asMap(msg["usage"]); u != nil {
				s.usage = usageFrom(u)
			}
		}
		out = append(out, oai.Chunk(s.id, s.model, map[string]any{"role": "assistant", "content": ""}, nil))
	case "content_block_start":
		cb := asMap(data["content_block"])
		s.blockType = asStr(cb["type"])
		if s.blockType == "tool_use" {
			s.toolIndex++
			out = append(out, oai.Chunk(s.id, s.model, map[string]any{"tool_calls": []any{map[string]any{
				"index": s.toolIndex, "id": cb["id"], "type": "function",
				"function": map[string]any{"name": cb["name"], "arguments": ""},
			}}}, nil))
		}
	case "content_block_delta":
		d := asMap(data["delta"])
		switch asStr(d["type"]) {
		case "text_delta":
			out = append(out, oai.Chunk(s.id, s.model, map[string]any{"content": d["text"]}, nil))
		case "input_json_delta":
			pj := asStr(d["partial_json"])
			out = append(out, oai.Chunk(s.id, s.model, map[string]any{"tool_calls": []any{map[string]any{
				"index": s.toolIndex, "function": map[string]any{"arguments": pj},
			}}}, nil))
		}
	case "message_delta":
		if u := asMap(data["usage"]); u != nil {
			// Mirror `output_tokens ?? st.usage.completion_tokens`: a null/absent value keeps the prior
			// count (set at message_start) rather than resetting it to 0.
			if v := u["output_tokens"]; v != nil {
				s.usage.CompletionTokens = numToInt(v)
			}
		}
		if d := asMap(data["delta"]); d != nil {
			if sr := asStr(d["stop_reason"]); sr != "" {
				if mapped, ok := stopMap[sr]; ok {
					s.finish = mapped
				} else {
					s.finish = "stop"
				}
			}
		}
	case "message_stop":
		fin := s.finish
		if fin == "" {
			fin = "stop"
		}
		out = append(out, oai.Chunk(s.id, s.model, map[string]any{}, fin))
		u := s.usage
		return StreamOut{Deltas: out, Usage: &u, Done: true}
	}
	return StreamOut{Deltas: out}
}

// Usage returns the token counts accumulated so far (fallback when a stream ends before message_stop).
func (s *anthropicStream) Usage() oai.Usage { return s.usage }

// asStrOrEmpty mirrors String(content ?? ”) — null/undefined becomes "".
func asStrOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return jsonStringify(v)
}
