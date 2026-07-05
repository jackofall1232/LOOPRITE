// Cost meter. Golden rule: trust provider-reported usage over any local estimate. Prices are
// per-model with SEPARATE input/output/cache-read/cache-write rates. Ported from meter.js and
// hardened per the pricing-confirmation brief: an unpriced or unconfirmed-price model NEVER
// produces a confident dollar figure — it is flagged both Estimated and Unconfirmed so a caller can
// refuse to present it as authoritative (the ledger + response metadata carry the flag).
package gateway

import (
	"math"

	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

const unknownPriceCeilingUSD = 0.25 // conservative reservation ceiling when price is unknown

// Cost is the outcome of pricing a request's real usage.
type Cost struct {
	USD         float64
	Estimated   bool // number should not be trusted as authoritative (unconfirmed price or none)
	Priced      bool // a real dollar figure was computed
	Unconfirmed bool // the price is null/unconfirmed — do not bill it as a confident figure
}

// CostOf prices real usage. Unknown/unconfirmed prices yield Priced=false / Unconfirmed=true and
// a $0 figure that the caller must treat as "unbilled, not free".
func CostOf(providerName, model string, usage oai.Usage) Cost {
	p := adapters.PriceFor(providerName, model)
	if p == nil || p.Input == nil || p.Output == nil {
		return Cost{USD: 0, Estimated: true, Priced: false, Unconfirmed: true}
	}
	usd := (float64(usage.PromptTokens)*(*p.Input) +
		float64(usage.CompletionTokens)*(*p.Output) +
		float64(usage.CacheReadTokens)*p.CacheRead +
		float64(usage.CacheWriteTokens)*p.CacheWrite) / 1e6
	return Cost{USD: usd, Estimated: !p.Confident, Priced: true, Unconfirmed: !p.Confident}
}

// ReservationCeiling is the pre-flight reservation ceiling in USD (NOT the billed amount): a rough
// token estimate of the prompt plus the requested max output at the model's output rate.
func ReservationCeiling(providerName, model string, req map[string]any) float64 {
	p := adapters.PriceFor(providerName, model)
	msgs := req["messages"]
	if msgs == nil {
		msgs = []any{}
	}
	promptTokens := util.EstimateTokensFromChars(jsonLenApprox(msgs))
	maxOut := numToInt(req["max_tokens"])
	if maxOut == 0 {
		maxOut = numToInt(req["max_completion_tokens"])
	}
	if maxOut == 0 {
		maxOut = 1024
	}
	if p == nil || p.Input == nil || p.Output == nil {
		return unknownPriceCeilingUSD
	}
	usd := (float64(promptTokens)*(*p.Input) + float64(maxOut)*(*p.Output)) / 1e6
	return math.Max(usd, 0.0005)
}
