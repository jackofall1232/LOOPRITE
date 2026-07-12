// OpenAI-compatible passthrough adapter. Covers OpenAI itself and every provider exposing an
// OpenAI-shaped /chat/completions (GLM/Zhipu, DeepSeek, Gemini compat, Groq, Mistral, OpenRouter,
// local Ollama/vLLM). The request is already canonical, so the work is: force streaming usage on,
// forward, and normalize usage out. Ported from openaiCompat.js.
package adapters

import (
	"encoding/json"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
)

type openaiCompatAdapter struct{}

func (openaiCompatAdapter) Kind() string { return "openai-compat" }
func (openaiCompatAdapter) Direct() bool { return false }

// reasoningModelPrefixes are OpenAI Chat Completions model families that reject the legacy
// `max_tokens` field outright ("Unsupported parameter: 'max_tokens' is not supported with this
// model. Use 'max_completion_tokens' instead.") and require `max_completion_tokens` in its place.
// This is confirmed for the o-series reasoning models (o1, o3, o4-mini) and is reported for the
// gpt-5.x family under /chat/completions as well (distinct from the newer /responses endpoint's
// unrelated `max_output_tokens` field, which this adapter does not target). Every other
// openai-compat upstream this adapter also serves (Zhipu/GLM, xAI, Gemini-compat, custom
// endpoints) uses its own model ids and will never match these prefixes, so the rename stays
// scoped to real OpenAI reasoning-family models.
var reasoningModelPrefixes = []string{"o1", "o3", "o4", "gpt-5"}

func requiresMaxCompletionTokens(model string) bool {
	m := strings.ToLower(model)
	for _, p := range reasoningModelPrefixes {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

func (openaiCompatAdapter) BuildRequest(model string, req map[string]any, stream bool) map[string]any {
	body := copyMap(req)
	body["model"] = model
	delete(body, "l00prite") // strip any gateway-only hints
	// Both the pre-save validation ping (gateway.TestProviderKey) and every real inference call
	// funnel through this one BuildRequest, so fixing the param name here covers both paths.
	if _, hasMaxTokens := body["max_tokens"]; hasMaxTokens && requiresMaxCompletionTokens(model) {
		body["max_completion_tokens"] = body["max_tokens"]
		delete(body, "max_tokens")
	}
	if stream {
		body["stream"] = true
		so := map[string]any{}
		if existing := asMap(body["stream_options"]); existing != nil {
			so = copyMap(existing)
		}
		so["include_usage"] = true
		body["stream_options"] = so
	} else {
		// stream_options is only valid alongside stream:true; leaving it on a non-stream request makes
		// strict providers (OpenAI) 400. Drop both.
		delete(body, "stream")
		delete(body, "stream_options")
	}
	return body
}

func (openaiCompatAdapter) URL(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/") + "/chat/completions"
}

func (openaiCompatAdapter) Headers(apiKey string) map[string]string {
	return map[string]string{"content-type": "application/json", "authorization": "Bearer " + apiKey}
}

func normUsage(u map[string]any) oai.Usage {
	usage := oai.Usage{
		PromptTokens:     numToInt(u["prompt_tokens"]),
		CompletionTokens: numToInt(u["completion_tokens"]),
	}
	if details := asMap(u["prompt_tokens_details"]); details != nil {
		usage.CacheReadTokens = numToInt(details["cached_tokens"])
	}
	return usage
}

func (openaiCompatAdapter) ParseFull(resp map[string]any, model string) FullResult {
	if _, ok := resp["model"]; !ok || asStr(resp["model"]) == "" {
		resp["model"] = model
	}
	return FullResult{Response: resp, Usage: normUsage(asMap(resp["usage"]))}
}

func (openaiCompatAdapter) NewStream(model string, forwardUsage bool) StreamTranslator {
	return &openaiCompatStream{model: model, forwardUsage: forwardUsage}
}

type openaiCompatStream struct {
	model        string
	forwardUsage bool
	usage        *oai.Usage
}

func (s *openaiCompatStream) OnEvent(ev SSEEvent) StreamOut {
	if ev.Data == "[DONE]" {
		return StreamOut{Done: true, Usage: s.usage}
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
		return StreamOut{}
	}
	var evtUsage *oai.Usage
	if u := asMap(chunk["usage"]); u != nil {
		nu := normUsage(u)
		s.usage = &nu
		evtUsage = &nu
	}
	hasChoice := len(asArr(chunk["choices"])) > 0
	forward := hasChoice || (s.forwardUsage && evtUsage != nil)
	var deltas []map[string]any
	if forward {
		deltas = []map[string]any{chunk}
	}
	return StreamOut{Deltas: deltas, Usage: evtUsage}
}

// Usage returns the last usage seen (fallback when a stream ends before its terminal usage event).
func (s *openaiCompatStream) Usage() oai.Usage {
	if s.usage != nil {
		return *s.usage
	}
	return oai.Usage{}
}
