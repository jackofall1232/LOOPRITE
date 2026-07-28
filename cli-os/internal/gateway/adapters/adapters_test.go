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

// TestOpenAICompatReasoningModelsUseMaxCompletionTokens pins the fix for the real upstream error
// "Unsupported parameter: 'max_tokens' is not supported with this model. Use
// 'max_completion_tokens' instead." — both gateway.TestProviderKey's validation ping and every
// real inference call build their request through this one BuildRequest, so this single rename
// point fixes both paths at once.
func TestOpenAICompatReasoningModelsUseMaxCompletionTokens(t *testing.T) {
	oc := openaiCompatAdapter{}
	for _, model := range []string{"o1-preview", "o1-mini", "o3-mini", "o4-mini", "gpt-5", "gpt-5-mini", "GPT-5-Turbo"} {
		body := oc.BuildRequest(model, map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": "ping"}}, "max_tokens": float64(1),
		}, false)
		if _, ok := body["max_tokens"]; ok {
			t.Fatalf("%s: max_tokens must be renamed away, got %v", model, body["max_tokens"])
		}
		if numToInt(body["max_completion_tokens"]) != 1 {
			t.Fatalf("%s: max_completion_tokens want 1 got %v", model, body["max_completion_tokens"])
		}
	}
}

// TestOpenAICompatNonReasoningModelsKeepMaxTokens guards against an over-broad rename: a
// pre-reasoning-family OpenAI id (and every non-OpenAI openai-compat upstream, e.g. Zhipu/xAI/
// Venice ids) must keep sending `max_tokens` unchanged.
func TestOpenAICompatNonReasoningModelsKeepMaxTokens(t *testing.T) {
	oc := openaiCompatAdapter{}
	for _, model := range []string{"gpt-4-turbo", "gpt-3.5-turbo", "glm-5.2", "grok-4.5"} {
		body := oc.BuildRequest(model, map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": "ping"}}, "max_tokens": float64(1),
		}, false)
		if _, ok := body["max_completion_tokens"]; ok {
			t.Fatalf("%s: must not gain max_completion_tokens, got %v", model, body["max_completion_tokens"])
		}
		if numToInt(body["max_tokens"]) != 1 {
			t.Fatalf("%s: max_tokens want 1 got %v", model, body["max_tokens"])
		}
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

// ---- venice + gemini manifests ----

func TestVeniceManifestLoadsAndRoutes(t *testing.T) {
	if got := DefaultBaseURL("venice"); got != "https://api.venice.ai/api/v1" {
		t.Fatalf("venice base_url want https://api.venice.ai/api/v1 got %q", got)
	}
	if got := DefaultAdapterKind("venice"); got != "openai-compat" {
		t.Fatalf("venice adapter want openai-compat got %q", got)
	}
	want := []string{
		"venice-uncensored-1-2", "llama-3.3-70b", "qwen3-235b-a22b-instruct-2507",
		"qwen3-coder-480b-a35b-instruct-turbo", "qwen3-next-80b", "deepseek-v4-pro",
		"deepseek-v4-flash", "kimi-k2-7-code", "zai-org-glm-5", "minimax-m27",
		"claude-sonnet-5", "claude-fable-5", "openai-gpt-52", "openai-gpt-52-codex",
		"gemini-3-5-flash",
	}
	got := ModelsFor("venice")
	if len(got) != len(want) {
		t.Fatalf("venice ModelsFor want %d models got %d: %v", len(want), len(got), got)
	}
	gotSet := map[string]bool{}
	for _, m := range got {
		gotSet[m] = true
	}
	for _, m := range want {
		if !gotSet[m] {
			t.Fatalf("venice ModelsFor missing %q, got %v", m, got)
		}
	}
}

func TestVeniceSonnetPriceConfident(t *testing.T) {
	p := PriceFor("venice", "claude-sonnet-5")
	if p == nil || p.Input == nil || p.Output == nil {
		t.Fatalf("venice/claude-sonnet-5 must have a resolved price, got %+v", p)
	}
	if *p.Input != 3.00 || *p.Output != 15.00 {
		t.Fatalf("venice/claude-sonnet-5 price want 3.00/15.00 got %v/%v", *p.Input, *p.Output)
	}
	if !p.Confident {
		t.Fatalf("venice/claude-sonnet-5 price must be first-party confident (price_confidence high)")
	}
	if tier := PriceTierFor("venice", "claude-sonnet-5"); tier != 0 {
		t.Fatalf("venice/claude-sonnet-5 price tier want 0 (priced+confirmed) got %d", tier)
	}
}

func TestVeniceContextUnverified(t *testing.T) {
	// context/max_output are deliberately omitted from every venice model so the router treats them
	// as unverified (fail-closed), not as a guessed number.
	if got := ContextFor("venice", "claude-sonnet-5"); got != nil {
		t.Fatalf("venice/claude-sonnet-5 context must be unverified (nil), got %v", *got)
	}
	if got := MaxOutputFor("venice", "claude-sonnet-5"); got != nil {
		t.Fatalf("venice/claude-sonnet-5 max_output must be unverified (nil), got %v", *got)
	}
}

func TestVeniceToolsCapabilitySubset(t *testing.T) {
	toolsExpected := map[string]bool{
		"qwen3-coder-480b-a35b-instruct-turbo": true, "qwen3-235b-a22b-instruct-2507": true,
		"llama-3.3-70b": true, "kimi-k2-7-code": true, "zai-org-glm-5": true,
		"claude-sonnet-5": true, "claude-fable-5": true, "openai-gpt-52": true,
		"openai-gpt-52-codex": true, "deepseek-v4-pro": true,
	}
	for _, m := range ModelsFor("venice") {
		caps := CapabilitiesFor("venice", m)
		got, _ := caps["tools"].(bool)
		if got != toolsExpected[m] {
			t.Fatalf("venice/%s tools capability want %v got %v (caps=%v)", m, toolsExpected[m], got, caps)
		}
	}
}

func TestGeminiManifest(t *testing.T) {
	if got := DefaultBaseURL("gemini"); got != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("gemini base_url want the openai-compat endpoint got %q", got)
	}
	if got := DefaultAdapterKind("gemini"); got != "openai-compat" {
		t.Fatalf("gemini adapter want openai-compat got %q", got)
	}
	models := ModelsFor("gemini")
	if len(models) != 3 {
		t.Fatalf("gemini ModelsFor want 3 models got %d: %v", len(models), models)
	}
	// gemini-2.5-pro/gemini-2.5-flash removed 2026-07-12 (no longer available); gemini-3.1-pro-preview
	// is the current flagship and carries first-party pricing the same way 2.5-pro used to.
	p := PriceFor("gemini", "gemini-3.1-pro-preview")
	if p == nil || p.Input == nil || p.Output == nil {
		t.Fatalf("gemini/gemini-3.1-pro-preview must have a resolved price, got %+v", p)
	}
	if *p.Input != 2.00 || *p.Output != 12.00 {
		t.Fatalf("gemini/gemini-3.1-pro-preview price want 2.00/12.00 got %v/%v", *p.Input, *p.Output)
	}
	if !p.Confident {
		t.Fatalf("gemini/gemini-3.1-pro-preview price must be first-party confident (price_confidence high)")
	}
	if tier := PriceTierFor("gemini", "gemini-3.1-pro-preview"); tier != 0 {
		t.Fatalf("gemini/gemini-3.1-pro-preview price tier want 0 (priced+confirmed) got %d", tier)
	}
	if tier := PriceTierFor("gemini", "gemini-3.5-flash"); tier != 0 {
		t.Fatalf("gemini/gemini-3.5-flash price tier want 0 (priced+confirmed) got %d", tier)
	}
	// context/max_output stay omitted (fail-closed): the 1M/65K figures are secondary-source-only.
	if got := ContextFor("gemini", "gemini-3.1-pro-preview"); got != nil {
		t.Fatalf("gemini/gemini-3.1-pro-preview context must be unverified (nil), got %v", *got)
	}
	caps := CapabilitiesFor("gemini", "gemini-3.1-pro-preview")
	if b, _ := caps["tools"].(bool); !b {
		t.Fatalf("gemini/gemini-3.1-pro-preview must declare tools:true, got %v", caps)
	}
	if b, _ := caps["vision"].(bool); !b {
		t.Fatalf("gemini/gemini-3.1-pro-preview must declare vision:true, got %v", caps)
	}
}

func TestGoogleAliasResolvesToGemini(t *testing.T) {
	if got := DefaultBaseURL("google"); got != DefaultBaseURL("gemini") {
		t.Fatalf(`alias "google" must resolve to the gemini manifest, got base_url %q vs gemini %q`, got, DefaultBaseURL("gemini"))
	}
	if got := ModelsFor("google"); len(got) != len(ModelsFor("gemini")) {
		t.Fatalf(`alias "google" ModelsFor must match gemini's catalog, got %v`, got)
	}
}

// ---- xai (Grok) manifest ----

func TestXaiManifest(t *testing.T) {
	if got := DefaultBaseURL("xai"); got != "https://api.x.ai/v1" {
		t.Fatalf("xai base_url want https://api.x.ai/v1 got %q", got)
	}
	if got := DefaultAdapterKind("xai"); got != "openai-compat" {
		t.Fatalf("xai adapter want openai-compat got %q", got)
	}
	models := ModelsFor("xai")
	if len(models) == 0 || models[0] != "grok-4.3" {
		// models[0] is the validation probe (pickValidationModel); grok-4.3 is the safest id (SDK-verbatim).
		t.Fatalf("xai ModelsFor[0] want grok-4.3 (validation-probe ordering) got %v", models)
	}
	for _, m := range models {
		if tier := PriceTierFor("xai", m); tier != 2 {
			t.Fatalf("xai/%s must be unpriced (tier 2 — all xai prices null/unconfirmed), got %d", m, tier)
		}
	}
	for _, m := range models {
		if m == "grok-code-fast-1" {
			t.Fatalf("grok-code-fast-1 was retired 2026-05-15 and must never be listed (silent redirect billing)")
		}
	}
}

func TestGrokAliasResolvesToXai(t *testing.T) {
	if got := DefaultBaseURL("grok"); got != DefaultBaseURL("xai") {
		t.Fatalf(`alias "grok" must resolve to the xai manifest, got base_url %q vs xai %q`, got, DefaultBaseURL("xai"))
	}
	if got := ModelsFor("grok"); len(got) != len(ModelsFor("xai")) {
		t.Fatalf(`alias "grok" ModelsFor must match xai's catalog, got %v`, got)
	}
}

// ---- kimi (Moonshot AI) manifest ----

func TestKimiManifest(t *testing.T) {
	if got := DefaultBaseURL("kimi"); got != "https://api.moonshot.ai/v1" {
		t.Fatalf("kimi base_url want https://api.moonshot.ai/v1 got %q", got)
	}
	if got := DefaultAdapterKind("kimi"); got != "openai-compat" {
		t.Fatalf("kimi adapter want openai-compat got %q", got)
	}
	models := ModelsFor("kimi")
	if len(models) == 0 || models[0] != "kimi-k3" {
		// models[0] is the validation probe (pickValidationModel); kimi-k3 is Moonshot's
		// documented default/flagship id (platform.moonshot.ai Quickstart, 2026-07-28).
		t.Fatalf("kimi ModelsFor[0] want kimi-k3 (validation-probe ordering) got %v", models)
	}
	for _, m := range models {
		if tier := PriceTierFor("kimi", m); tier != 2 {
			t.Fatalf("kimi/%s must be unpriced (tier 2 — all kimi prices null/unconfirmed), got %d", m, tier)
		}
	}
	caps := CapabilitiesFor("kimi", "kimi-k3")
	if b, _ := caps["tools"].(bool); !b {
		t.Fatalf("kimi/kimi-k3 must declare tools:true, got %v", caps)
	}
	if b, _ := caps["vision"].(bool); !b {
		t.Fatalf("kimi/kimi-k3 must declare vision:true, got %v", caps)
	}
	if got := ContextFor("kimi", "kimi-k3"); got == nil || *got != 1000000 {
		t.Fatalf("kimi/kimi-k3 context want 1000000 (docs-stated 1M), got %v", got)
	}
}

func TestMoonshotAliasResolvesToKimi(t *testing.T) {
	if got := DefaultBaseURL("moonshot"); got != DefaultBaseURL("kimi") {
		t.Fatalf(`alias "moonshot" must resolve to the kimi manifest, got base_url %q vs kimi %q`, got, DefaultBaseURL("kimi"))
	}
	if got := ModelsFor("moonshot"); len(got) != len(ModelsFor("kimi")) {
		t.Fatalf(`alias "moonshot" ModelsFor must match kimi's catalog, got %v`, got)
	}
}

// ---- Add-provider UI presets ----

func TestPresets(t *testing.T) {
	for _, key := range presetOrder {
		if key == "mock" {
			t.Fatalf("the mock adapter must never be exposed as a preset: %v", presetOrder)
		}
	}
	presets := Presets()
	wantKeys := []string{"anthropic", "openai", "gemini", "xai", "kimi", "venice", "zhipu"}
	if len(presets) != len(wantKeys) {
		t.Fatalf("Presets want %d entries got %d: %+v", len(wantKeys), len(presets), presets)
	}
	byKey := map[string]Preset{}
	for i, p := range presets {
		if p.Key != wantKeys[i] {
			t.Fatalf("Presets order mismatch at %d: want %q got %q", i, wantKeys[i], p.Key)
		}
		if p.BaseURL == "" {
			t.Fatalf("preset %q must carry a non-empty base_url", p.Key)
		}
		if p.DisplayName == "" {
			t.Fatalf("preset %q must carry a non-empty display_name", p.Key)
		}
		byKey[p.Key] = p
	}
	// anthropic: native adapter, sample model is its first routable id.
	if a := byKey["anthropic"]; a.Adapter != "native-messages" || a.SampleModel != "claude-fable-5" {
		t.Fatalf("anthropic preset want native-messages/claude-fable-5, got %q/%q", a.Adapter, a.SampleModel)
	}
	// openai: native->compat mapping, and its only model is a PENDING placeholder (filtered) -> no sample.
	if o := byKey["openai"]; o.Adapter != "openai-compat" || o.SampleModel != "" {
		t.Fatalf("openai preset want openai-compat/empty-sample (PENDING filtered), got %q/%q", o.Adapter, o.SampleModel)
	}
}
