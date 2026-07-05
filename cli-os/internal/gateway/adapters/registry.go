// Package adapters is the "adapters are data + code" registry: per-provider manifests (embedded
// JSON: base url, model ids, capabilities, per-model pricing) plus the translation code
// (anthropic native, openai-compat passthrough, mock). Ported from registry.js + the manifests.
package adapters

import (
	"embed"
	"encoding/json"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
)

//go:embed manifests/*.json
var manifestFS embed.FS

// ---- manifest data model ----

type priceSpec struct {
	Input        *float64 `json:"input"`
	Output       *float64 `json:"output"`
	CacheRead    *float64 `json:"cache_read"`
	CacheWrite5m *float64 `json:"cache_write_5m"`
	CacheWrite   *float64 `json:"cache_write"`
}

type modelSpec struct {
	ID           string         `json:"id"`
	Context      *int           `json:"context"`
	MaxOutput    *int           `json:"max_output"`
	Capabilities map[string]any `json:"capabilities"`
	Price        *priceSpec     `json:"price_per_mtok"`
	PriceConf    string         `json:"price_confidence"`
}

type manifest struct {
	Provider string      `json:"provider"`
	BaseURL  string      `json:"base_url"`
	Adapter  string      `json:"adapter"`
	Models   []modelSpec `json:"models"`
}

// Price is a resolved per-model price map (USD per 1M tokens). Input/Output are nil when unpriced.
type Price struct {
	Input, Output         *float64
	CacheRead, CacheWrite float64
	Confident             bool
}

// Candidate is a routable (provider, model) with the facts the auto-router needs.
type Candidate struct {
	Provider     string
	Model        string
	Capabilities map[string]any
	Context      *int
	MaxOutput    *int
	Price        *Price
	PriceTier    int
}

var aliases = map[string]string{"glm": "zhipu", "z.ai": "zhipu", "ollama": "local", "local-models": "local"}

var manifests = loadManifests()

func loadManifests() map[string]*manifest {
	out := map[string]*manifest{}
	entries, err := manifestFS.ReadDir("manifests")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := manifestFS.ReadFile("manifests/" + e.Name())
		if err != nil {
			continue
		}
		var m manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			continue // skip malformed manifest
		}
		if m.Provider != "" {
			out[m.Provider] = &m
		}
	}
	return out
}

func manifestFor(providerName string) *manifest {
	key := providerName
	if a, ok := aliases[providerName]; ok {
		key = a
	}
	return manifests[key]
}

// DefaultBaseURL returns a provider's manifest base URL, or "".
func DefaultBaseURL(providerName string) string {
	if m := manifestFor(providerName); m != nil {
		return m.BaseURL
	}
	return ""
}

// DefaultAdapterKind resolves the adapter kind for a provider from its manifest (openai-native maps
// to openai-compat), defaulting to native-messages for anthropic and openai-compat otherwise.
func DefaultAdapterKind(providerName string) string {
	if m := manifestFor(providerName); m != nil && m.Adapter != "" {
		if m.Adapter == "openai-native" {
			return "openai-compat"
		}
		return m.Adapter
	}
	if providerName == "anthropic" {
		return "native-messages"
	}
	return "openai-compat"
}

func modelRow(providerName, model string) *modelSpec {
	m := manifestFor(providerName)
	if m == nil {
		return nil
	}
	for i := range m.Models {
		if m.Models[i].ID == model {
			return &m.Models[i]
		}
	}
	return nil
}

// PriceFor returns the resolved price map for (provider, model), or nil when the model is unknown.
func PriceFor(providerName, model string) *Price {
	row := modelRow(providerName, model)
	if row == nil || row.Price == nil {
		return nil
	}
	p := row.Price
	cw := 0.0
	if p.CacheWrite5m != nil {
		cw = *p.CacheWrite5m
	} else if p.CacheWrite != nil {
		cw = *p.CacheWrite
	}
	cr := 0.0
	if p.CacheRead != nil {
		cr = *p.CacheRead
	}
	return &Price{Input: p.Input, Output: p.Output, CacheRead: cr, CacheWrite: cw, Confident: row.PriceConf == "high"}
}

// ModelsFor lists a provider's routable model ids (PENDING placeholders filtered out).
func ModelsFor(providerName string) []string {
	m := manifestFor(providerName)
	if m == nil {
		return nil
	}
	var out []string
	for _, ms := range m.Models {
		if strings.HasPrefix(ms.ID, "PENDING") {
			continue
		}
		out = append(out, ms.ID)
	}
	return out
}

// CapabilitiesFor returns a model's declared capabilities (empty map if unknown — fail-closed).
func CapabilitiesFor(providerName, model string) map[string]any {
	if row := modelRow(providerName, model); row != nil && row.Capabilities != nil {
		return row.Capabilities
	}
	return map[string]any{}
}

// ContextFor returns a model's declared context window, or nil when unverified.
func ContextFor(providerName, model string) *int {
	if row := modelRow(providerName, model); row != nil {
		return row.Context
	}
	return nil
}

// MaxOutputFor returns a model's declared max output tokens, or nil when unverified.
func MaxOutputFor(providerName, model string) *int {
	if row := modelRow(providerName, model); row != nil {
		return row.MaxOutput
	}
	return nil
}

// PriceTierFor: 0 = priced+confirmed, 1 = priced+unconfirmed, 2 = unpriced. The cost preference
// must sort tier-2 last so a $0 "unknown price" can never masquerade as cheapest.
func PriceTierFor(providerName, model string) int {
	p := PriceFor(providerName, model)
	if p == nil || p.Input == nil || p.Output == nil {
		return 2
	}
	if p.Confident {
		return 0
	}
	return 1
}

// Catalog enumerates every routable (provider, model) across the given enabled providers.
func Catalog(providerNames []string) []Candidate {
	var out []Candidate
	for _, name := range providerNames {
		for _, model := range ModelsFor(name) {
			out = append(out, Candidate{
				Provider:     name,
				Model:        model,
				Capabilities: CapabilitiesFor(name, model),
				Context:      ContextFor(name, model),
				MaxOutput:    MaxOutputFor(name, model),
				Price:        PriceFor(name, model),
				PriceTier:    PriceTierFor(name, model),
			})
		}
	}
	return out
}

// ---- adapter interface ----

// FullResult is a non-streaming provider call outcome.
type FullResult struct {
	Response map[string]any
	Usage    oai.Usage
}

// SSEEvent is a parsed server-sent event.
type SSEEvent struct {
	Event string
	Data  string
}

// StreamOut is the translation of one upstream SSE event into OpenAI chunk deltas.
type StreamOut struct {
	Deltas []map[string]any
	Usage  *oai.Usage
	Done   bool
}

// StreamTranslator folds a provider's SSE events into OpenAI chunks, holding per-stream state.
// Usage returns the usage accumulated so far — used as a fallback when a stream ends (or is cut)
// before its terminal usage event, mirroring the Node `usage || st.usage` behavior.
type StreamTranslator interface {
	OnEvent(ev SSEEvent) StreamOut
	Usage() oai.Usage
}

// DirectStreamChunk is one chunk from a direct (no-network) adapter's stream.
type DirectStreamChunk struct {
	Chunk map[string]any
	Usage *oai.Usage
	Done  bool
}

// Adapter is the common marker; concrete adapters also implement NetworkAdapter or DirectAdapter.
type Adapter interface {
	Kind() string
	Direct() bool
}

// NetworkAdapter translates to/from a real HTTP provider.
type NetworkAdapter interface {
	Adapter
	BuildRequest(model string, req map[string]any, stream bool) map[string]any
	URL(baseURL string) string
	Headers(apiKey string) map[string]string
	ParseFull(resp map[string]any, model string) FullResult
	NewStream(model string, forwardUsage bool) StreamTranslator
}

// DirectAdapter answers without a network call (mock upstream).
type DirectAdapter interface {
	Adapter
	DirectFull(req map[string]any, model string) FullResult
	DirectStream(req map[string]any, model string) []DirectStreamChunk
}

// AdapterFor resolves an adapter kind to its implementation (default: openai-compat).
func AdapterFor(kind string) Adapter {
	switch kind {
	case "native-messages":
		return anthropicAdapter{}
	case "openai-native", "openai-compat":
		return openaiCompatAdapter{}
	case "mock":
		return mockAdapter{}
	default:
		return openaiCompatAdapter{}
	}
}
