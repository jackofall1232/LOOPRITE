// Explainable routing (no ML). First-match-wins rules; every decision is logged and inspectable via
// `l00prite route explain`. Includes a simple circuit breaker so a flapping provider stops poisoning
// requests. Ported from router.js. The breaker map is guarded by a mutex (Go serves requests
// concurrently, unlike Node's single thread).
package gateway

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/apierr"
	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
)

// ProviderInfo is the minimal routing view of a provider row.
type ProviderInfo struct {
	Name      string
	Enabled   bool
	IsDefault bool
	// DisabledModels are models the operator switched off for this provider (Part C/E). A nil map means
	// nothing is disabled, so routing behaves exactly as before for every provider without a selection.
	DisabledModels map[string]bool
}

// serves reports whether this provider may auto/bare-route the given model — true unless the operator
// has explicitly disabled it. Explicit operator intent (route-header pins, provider/model pins, alias
// targets) deliberately bypasses this: naming a specific model by hand overrides the catalog toggle.
func (p ProviderInfo) serves(model string) bool { return !p.DisabledModels[model] }

// RouteResult is a routing decision: the chosen (provider, model) and the inspectable decision.
type RouteResult struct {
	Provider string
	Model    string
	Decision map[string]any
}

type breakerState struct {
	fails int
	until time.Time
}

var (
	breakerMu sync.Mutex
	breaker   = map[string]*breakerState{}
)

// MarkFailure records a provider failure, tripping the breaker (30s) after 3 fails.
func MarkFailure(provider string) {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	b := breaker[provider]
	if b == nil {
		b = &breakerState{}
		breaker[provider] = b
	}
	b.fails++
	if b.fails >= 3 {
		b.until = time.Now().Add(30 * time.Second)
	}
}

// MarkSuccess resets a provider's breaker.
func MarkSuccess(provider string) {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	breaker[provider] = &breakerState{}
}

// IsTripped reports whether a provider's circuit is currently open.
func IsTripped(provider string) bool {
	breakerMu.Lock()
	defer breakerMu.Unlock()
	b := breaker[provider]
	return b != nil && b.until.After(time.Now())
}

var targetRE = regexp.MustCompile(`(?i)^([a-z0-9_-]+)[:/](.+)$`)

func splitTarget(s string) (provider, model string, ok bool) {
	m := targetRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// HTTPError builds a typed API error (kept as a helper mirroring router.js httpError).
func HTTPError(status int, message, typ string) *apierr.Error {
	return apierr.New(status, message, typ)
}

// HTTPStatusOf extracts the HTTP status from a typed error (0 if untyped) — used by the CLI.
func HTTPStatusOf(err error) int { return httpStatusOf(err) }

// Pick resolves a request into a concrete (provider, model) + RoutingDecision, or a typed error.
func Pick(providers []ProviderInfo, aliases map[string]string, req map[string]any, routeHeader string, cfg config.Config) (RouteResult, error) {
	enabled := map[string]bool{}
	for _, p := range providers {
		if p.Enabled {
			enabled[p.Name] = true
		}
	}
	// Zero routable providers is the "removed the only provider" failure state: return a specific,
	// dashboard-pointing error (distinct code) instead of falling through to the generic "no enabled
	// providers available" 503 — so an average user sees exactly what to do, not a confusing routing
	// error. The all-tripped case (providers exist and are enabled but their breakers are open) is left
	// to the existing message downstream.
	if len(enabled) == 0 {
		msg := "No providers are configured. Open the l00prite dashboard to add a provider before this gateway can route requests."
		if len(providers) > 0 {
			msg = "All providers are disabled. Open the l00prite dashboard to enable or add a provider before this gateway can route requests."
		}
		e := apierr.New(503, msg, "service_unavailable")
		e.Code = "no_providers_configured"
		return RouteResult{}, e
	}
	var isDefault *ProviderInfo
	for i := range providers {
		if providers[i].Enabled && providers[i].IsDefault {
			isDefault = &providers[i]
			break
		}
	}
	wanted := asStr(req["model"])
	alternatives := []string{}

	// Auto tier is enabled only when routing is configured (Profiles present). A zero-value config
	// (as in the "no routing config" case) leaves auto inert, treating "auto:*" as a literal model.
	autoEnabled := len(cfg.Routing.Profiles) > 0
	var autoSig *autoSignal
	if autoEnabled {
		if h := parseAutoSignal(routeHeader); h != nil {
			autoSig = h
		} else if routeHeader == "" {
			autoSig = parseAutoSignal(wanted)
		}
	}
	if autoSig != nil {
		return selectAuto(providers, cfg.Routing, req, *autoSig, cfg.DefaultMaxTokens)
	}

	use := func(provider, model, ruleID, reason string) (RouteResult, bool) {
		if !enabled[provider] {
			return RouteResult{}, false
		}
		return RouteResult{Provider: provider, Model: model, Decision: map[string]any{
			"rule_id": ruleID, "chosen": provider + "/" + model, "alternatives": alternatives, "reason": reason,
		}}, true
	}

	// Rule 1a: explicit route header — operator intent, so an unknown provider is an error.
	if hp, hm, ok := splitTarget(routeHeader); ok {
		if r, found := use(hp, hm, "explicit_pin", "header pin "+hp+"/"+hm); found {
			return r, nil
		}
		return RouteResult{}, apierr.New(400, `Provider "`+hp+`" is not registered or enabled`, "invalid_request_error")
	}
	// Rule 1b: provider-qualified model — only a pin when the provider segment is a registered provider.
	if mp, mm, ok := splitTarget(wanted); ok && enabled[mp] {
		if r, found := use(mp, mm, "explicit_pin", "model pin "+mp+"/"+mm); found {
			return r, nil
		}
	}
	// Rule 2: alias map.
	if wanted != "" {
		if target, ok := aliases[wanted]; ok {
			if tp, tm, ok := splitTarget(target); ok {
				if r, found := use(tp, tm, "alias_map", `alias "`+wanted+`" -> `+tp+"/"+tm); found {
					return r, nil
				}
			}
		}
	}
	// Rule 3: bare model id owned by a registered provider's manifest.
	if wanted != "" {
		for _, p := range providers {
			if !enabled[p.Name] || IsTripped(p.Name) {
				if IsTripped(p.Name) {
					alternatives = append(alternatives, p.Name+" (circuit open)")
				}
				continue
			}
			if contains(adapters.ModelsFor(p.Name), wanted) && p.serves(wanted) {
				return mustUse(use, p.Name, wanted, "model_owner", `model "`+wanted+`" served by `+p.Name)
			}
		}
	}
	// Rule 4: default provider (+ requested model or its own default model), honoring the breaker AND
	// the operator's model selection — a model disabled on the default provider never routes to it.
	if isDefault != nil && !IsTripped(isDefault.Name) {
		model := wanted
		if model == "" {
			model = firstEnabledModel(*isDefault, "default")
		}
		if model != "" && isDefault.serves(model) {
			return mustUse(use, isDefault.Name, model, "default_provider", "no explicit target; default provider "+isDefault.Name)
		}
	}
	if isDefault != nil && IsTripped(isDefault.Name) {
		alternatives = append(alternatives, isDefault.Name+" (circuit open)")
	}
	// Rule 5 fallback: first enabled, non-tripped provider that can serve the model — skipping any that
	// have the requested model disabled, and any whose whole catalog is disabled for a bare request.
	for _, p := range providers {
		if enabled[p.Name] && !IsTripped(p.Name) {
			model := wanted
			if model == "" {
				model = firstEnabledModel(p, "default")
			}
			if model != "" && p.serves(model) {
				return mustUse(use, p.Name, model, "fallback_first", "fell back to first available provider "+p.Name)
			}
		}
	}
	return RouteResult{}, apierr.New(503, "No enabled providers available to route this request", "service_unavailable")
}

// mustUse rebuilds the decision with the CURRENT alternatives slice (which may have grown) so the
// returned decision reflects tripped-provider notes accumulated during the scan.
func mustUse(use func(string, string, string, string) (RouteResult, bool), provider, model, ruleID, reason string) (RouteResult, error) {
	r, _ := use(provider, model, ruleID, reason)
	return r, nil
}

// firstEnabledModel returns the provider's first manifest model the operator has NOT disabled. A
// provider with a catalog but every model disabled returns "" (unroutable for a bare/auto request — the
// caller skips it); a provider with NO manifest catalog returns the fallback, preserving the prior
// behavior of forwarding an arbitrary model id to a custom openai-compat upstream.
func firstEnabledModel(p ProviderInfo, fallback string) string {
	models := adapters.ModelsFor(p.Name)
	if len(models) == 0 {
		return fallback
	}
	for _, m := range models {
		if p.serves(m) {
			return m
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
