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

func (openaiCompatAdapter) BuildRequest(model string, req map[string]any, stream bool) map[string]any {
	body := copyMap(req)
	body["model"] = model
	delete(body, "l00prite") // strip any gateway-only hints
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
