// Provider key validation — a REAL upstream round-trip used to prove a provider's key/base URL work
// before the gateway commits them. Both the CLI (`provider test`) and the first-run setup wizard call
// this exact function, so a key that the wizard accepts is a key the gateway can actually route to,
// and neither surface can drift from the other. It reuses callNonStream (the same idempotency-aware,
// timeout-bounded call path as a live request), so validation exercises the production code path, not
// a bespoke one.
package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
)

// validateTimeoutMs caps a validation round-trip so a wrong base URL / hung host fails fast in the UI
// rather than blocking on the 120s request timeout a real completion would use.
const validateTimeoutMs = 15000

// ValidateResult is the outcome of a provider-key validation round-trip.
type ValidateResult struct {
	OK        bool   `json:"ok"`
	ModelUsed string `json:"model_used,omitempty"`
	Error     string `json:"error,omitempty"`
	Status    int    `json:"status,omitempty"` // upstream HTTP status when the failure carried one
}

// pickValidationModel resolves the model to probe with: the caller's choice, else the provider's
// first manifest model. Returns "" when neither is available (unknown provider + no model given).
func pickValidationModel(providerName, model string) string {
	if model != "" {
		return model
	}
	if models := adapters.ModelsFor(providerName); len(models) > 0 {
		return models[0]
	}
	return ""
}

// TestProviderKey makes a minimal real completion call (max_tokens: 1) through the given adapter to
// prove the credentials work. A Direct adapter (mock) needs no key or network and always passes.
// Returns a structured result; ValidateResult.OK is false with a human-readable Error on any failure
// (bad key, unreachable host, unknown model), so the caller can surface a clear pass/fail — never a
// silent accept. It stores nothing: callers persist the provider only after OK.
func TestProviderKey(cfg config.Config, adapterKind, providerName, baseURL, apiKey, model string) ValidateResult {
	adapter := adapters.AdapterFor(adapterKind)
	if adapter.Direct() {
		// mock upstream: no credentials, no network — the request path always succeeds.
		return ValidateResult{OK: true, ModelUsed: pickValidationModel(providerName, model)}
	}
	if baseURL == "" {
		baseURL = adapters.DefaultBaseURL(providerName)
	}
	if baseURL == "" {
		return ValidateResult{OK: false, Error: "no base URL configured for this provider — supply one"}
	}
	if apiKey == "" {
		return ValidateResult{OK: false, Error: "an API key is required to validate this provider"}
	}
	probeModel := pickValidationModel(providerName, model)
	if probeModel == "" {
		return ValidateResult{OK: false, Error: "a model id is required to validate this provider (none known for it)"}
	}

	// Snappier settings for validation: short timeout, no long backoff chain. A real request keeps the
	// operator's configured retry/timeout; here we want a fast pass/fail for the UI.
	vcfg := cfg
	vcfg.RequestTimeoutMs = validateTimeoutMs
	if vcfg.Retry.MaxAttempts > 2 {
		vcfg.Retry.MaxAttempts = 2
	}

	prov := ProviderRow{Name: providerName, Adapter: adapterKind, BaseURL: baseURL, Enabled: true}
	req := map[string]any{
		"messages":   []any{map[string]any{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(validateTimeoutMs+2000)*time.Millisecond)
	defer cancel()

	_, err := callNonStream(adapter, prov, probeModel, req, apiKey, vcfg, ctx)
	if err != nil {
		return ValidateResult{OK: false, ModelUsed: probeModel, Error: validationMessage(err), Status: HTTPStatusOf(err)}
	}
	return ValidateResult{OK: true, ModelUsed: probeModel}
}

// validationMessage turns an upstream error into a short, user-facing reason.
func validationMessage(err error) string {
	if err == nil {
		return ""
	}
	switch HTTPStatusOf(err) {
	case 401, 403:
		return "the provider rejected this API key (unauthorized)"
	case 404:
		return "the provider or model was not found at this base URL"
	case 429:
		return "the provider rate-limited the validation call — the key looks valid; try again shortly"
	}
	if msg := err.Error(); msg != "" {
		return msg
	}
	return errors.New("provider validation failed").Error()
}
