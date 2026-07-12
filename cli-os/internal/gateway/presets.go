// Provider presets for the Add-provider UI. UNAUTHENTICATED by design -- serves only static,
// compiled-in manifest facts (the same non-secret class as GET /v1/setup/status: no DB state,
// no secrets, nothing request-derived). Needed pre-auth because the first-run setup wizard
// renders the provider form before any token exists.
package gateway

import (
	"net/http"

	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
)

// HandleProviderPresets is GET /v1/providers/catalog.
func (app *App) HandleProviderPresets(w http.ResponseWriter, r *http.Request) {
	presets := adapters.Presets()
	items := make([]any, 0, len(presets))
	for _, p := range presets {
		items = append(items, map[string]any{
			"key": p.Key, "display_name": p.DisplayName, "adapter": p.Adapter,
			"base_url": p.BaseURL, "sample_model": nilIfEmpty(p.SampleModel),
		})
	}
	sendJSON(w, 200, map[string]any{"object": "l00prite.provider_presets", "presets": items})
}
