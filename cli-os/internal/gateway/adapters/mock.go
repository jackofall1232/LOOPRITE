// Mock upstream adapter. Requires no key and no network, so a fresh install can demo the full
// request path immediately and the test suite can exercise the pipeline offline. Reports realistic
// usage so the cost meter has real numbers. Ported from mock.js, including the bridge-aware test
// hooks: a last user message of
//
//	"/bridge <target> :: <task>"      -> emit ONE bridge tool_call, then finalize on the next turn
//	"/bridgeloop <target> :: <task>"  -> emit a bridge tool_call on EVERY turn (drives the hop cap)
package adapters

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
)

type mockAdapter struct{}

func (mockAdapter) Kind() string { return "mock" }
func (mockAdapter) Direct() bool { return true }

const bridgeToolName = "l00prite_bridge"

var bridgeDirectiveRE = regexp.MustCompile(`^/(bridge|bridgeloop)\s+(\S+)\s*::\s*([\s\S]+)$`)
var wordRE = regexp.MustCompile(`\S+\s*`)

func mockLastUserText(req map[string]any) string {
	msgs := asArr(req["messages"])
	for i := len(msgs) - 1; i >= 0; i-- {
		m := asMap(msgs[i])
		if m != nil && asStr(m["role"]) == "user" {
			if s, ok := m["content"].(string); ok {
				return s
			}
			return ""
		}
	}
	return ""
}

func mockHasBridgeTool(req map[string]any) bool {
	for _, t := range asArr(req["tools"]) {
		if asStr(asMap(asMap(t)["function"])["name"]) == bridgeToolName {
			return true
		}
	}
	return false
}

func mockHasToolResult(req map[string]any) bool {
	for _, mm := range asArr(req["messages"]) {
		if asStr(asMap(mm)["role"]) == "tool" {
			return true
		}
	}
	return false
}

func mockHadBridgeCall(req map[string]any) bool {
	for _, mm := range asArr(req["messages"]) {
		m := asMap(mm)
		if asStr(m["role"]) != "assistant" {
			continue
		}
		for _, tc := range asArr(m["tool_calls"]) {
			if asStr(asMap(asMap(tc)["function"])["name"]) == bridgeToolName {
				return true
			}
		}
	}
	return false
}

type mockDirective struct {
	loop         bool
	target, task string
}

func mockBridgeDirective(req map[string]any) *mockDirective {
	m := bridgeDirectiveRE.FindStringSubmatch(strings.TrimSpace(mockLastUserText(req)))
	if m == nil {
		return nil
	}
	return &mockDirective{loop: m[1] == "bridgeloop", target: m[2], task: strings.TrimSpace(m[3])}
}

// truncate caps s at n characters (runes), never splitting a multibyte rune — closer to JS
// String.slice than a raw byte slice (which could emit invalid UTF-8).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func mockReply(req map[string]any) string {
	q := mockLastUserText(req)
	if q == "" {
		q = "your request"
	}
	return `Mock provider response. I received "` + truncate(q, 120) + `". ` +
		`This is the l00prite CLI-OS mock upstream (offline testing) — configure a real provider key to route to Anthropic, OpenAI, GLM, and others.`
}

func mockUsage(promptChars int, text string) oai.Usage {
	pt := int(math.Ceil(float64(promptChars) / 4))
	if pt < 1 {
		pt = 1
	}
	return oai.Usage{PromptTokens: pt, CompletionTokens: int(math.Ceil(float64(len(text)) / 4))}
}

func mockPromptChars(req map[string]any) int {
	msgs := req["messages"]
	if msgs == nil {
		msgs = []any{}
	}
	b, _ := json.Marshal(msgs)
	return len(b)
}

func (mockAdapter) DirectFull(req map[string]any, model string) FullResult {
	promptChars := mockPromptChars(req)
	var directive *mockDirective
	if mockHasBridgeTool(req) {
		directive = mockBridgeDirective(req)
	}

	// Emit a bridge tool_call: always for /bridgeloop; for /bridge only until a delegate answer
	// has been folded in.
	if directive != nil && (directive.loop || !mockHasToolResult(req)) {
		cid := oai.CmplID()
		callID := "call_" + cid[len(cid)-10:]
		args := jsonStringify(map[string]any{"target": directive.target, "task": directive.task})
		message := map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
			"id": callID, "type": "function", "function": map[string]any{"name": bridgeToolName, "arguments": args},
		}}}
		u := mockUsage(promptChars, strings.Repeat("x", len(args)))
		return FullResult{Response: oai.Response(oai.CmplID(), model, message, "tool_calls", u), Usage: u}
	}

	// Finalization turn: incorporate the delegate result(s) — ONLY when this conversation actually
	// went through a bridge delegation (not a plain client-side tool loop).
	if mockHasToolResult(req) && mockHadBridgeCall(req) {
		var excerpt string
		msgs := asArr(req["messages"])
		for i := len(msgs) - 1; i >= 0; i-- {
			m := asMap(msgs[i])
			if asStr(m["role"]) == "tool" {
				excerpt = truncate(collapseSpaces(asStr(m["content"])), 120)
				break
			}
		}
		text := "Mock final answer, composed after delegating. Delegate said: " + excerpt
		u := mockUsage(promptChars, text)
		return FullResult{Response: oai.Response(oai.CmplID(), model, map[string]any{"role": "assistant", "content": text}, "stop", u), Usage: u}
	}

	text := mockReply(req)
	u := mockUsage(promptChars, text)
	return FullResult{Response: oai.Response(oai.CmplID(), model, map[string]any{"role": "assistant", "content": text}, "stop", u), Usage: u}
}

func (mockAdapter) DirectStream(req map[string]any, model string) []DirectStreamChunk {
	id := oai.CmplID()
	text := mockReply(req)
	promptChars := mockPromptChars(req)
	var out []DirectStreamChunk
	out = append(out, DirectStreamChunk{Chunk: oai.Chunk(id, model, map[string]any{"role": "assistant", "content": ""}, nil)})
	words := wordRE.FindAllString(text, -1)
	if len(words) == 0 {
		words = []string{text}
	}
	for _, w := range words {
		out = append(out, DirectStreamChunk{Chunk: oai.Chunk(id, model, map[string]any{"content": w}, nil)})
	}
	u := mockUsage(promptChars, text)
	out = append(out, DirectStreamChunk{Chunk: oai.Chunk(id, model, map[string]any{}, "stop"), Usage: &u, Done: true})
	return out
}

var wsRE = regexp.MustCompile(`\s+`)

func collapseSpaces(s string) string { return wsRE.ReplaceAllString(s, " ") }
