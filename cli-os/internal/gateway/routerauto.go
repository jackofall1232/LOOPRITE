// Auto-routing: "pick the best provider for THIS task" and "the most efficient one". Realizes
// routing-rules-v1.md Rule 3 (capability filter) and Rule 4 (preference tiebreak). Deterministic and
// explainable — NO ML. A request opts in with model/header `auto` or `auto:<profile>`; explicit
// pins always win first (handled in router.go before this runs). Ported from router-auto.js.
package gateway

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackofall1232/l00prite/cli-os/internal/apierr"
	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

const (
	neutralQuality = 50
	imageTokens    = 1600 // coarse fixed cost per image part (never the base64 bytes)
)

func key(c adapters.Candidate) string { return c.Provider + "/" + c.Model }

// Requirements is the deterministic projection of a request the capability filter reads.
type Requirements struct {
	NeedsTools          bool `json:"needs_tools"`
	NeedsVision         bool `json:"needs_vision"`
	NeedsStreamingUsage bool `json:"needs_streaming_usage"`
	MinContextTokens    int  `json:"min_context_tokens"`
	PromptTokens        int  `json:"promptTokens"`
	MaxOut              int  `json:"maxOut"`
}

// estimatePromptTokens counts TEXT only, plus a fixed cost per image — never the base64 bytes.
func estimatePromptTokens(messages []any) int {
	chars, images := 0, 0
	for _, mm := range messages {
		m := asMap(mm)
		if m == nil {
			continue
		}
		if s, ok := m["content"].(string); ok {
			chars += utf8.RuneCountInString(s)
		} else if arr := asArr(m["content"]); arr != nil {
			for _, pp := range arr {
				p := asMap(pp)
				switch {
				case p != nil && asStr(p["type"]) == "text":
					chars += utf8.RuneCountInString(asStr(p["text"]))
				case p != nil && asStr(p["type"]) == "image_url":
					images++
				default:
					chars += jsonLenApprox(pp)
				}
			}
		}
		if tc := asArr(m["tool_calls"]); tc != nil {
			chars += jsonLenApprox(m["tool_calls"])
		}
	}
	return util.EstimateTokensFromChars(chars) + images*imageTokens
}

// DeriveRequirements projects an OpenAI request into hard routing requirements.
func DeriveRequirements(req map[string]any, defaultMaxTokens int) Requirements {
	messages := asArr(req["messages"])
	needsTools := len(asArr(req["tools"])) > 0
	needsVision := false
	for _, mm := range messages {
		m := asMap(mm)
		if m == nil {
			continue
		}
		for _, pp := range asArr(m["content"]) {
			if asStr(asMap(pp)["type"]) == "image_url" {
				needsVision = true
			}
		}
	}
	needsStreamingUsage := false
	if req["stream"] == true {
		if so := asMap(req["stream_options"]); so != nil {
			needsStreamingUsage = jsTruthy(so["include_usage"])
		}
	}
	maxOut := numToInt(req["max_tokens"])
	if maxOut == 0 {
		maxOut = numToInt(req["max_completion_tokens"])
	}
	if maxOut == 0 {
		maxOut = defaultMaxTokens
	}
	promptTokens := estimatePromptTokens(messages)
	return Requirements{
		NeedsTools: needsTools, NeedsVision: needsVision, NeedsStreamingUsage: needsStreamingUsage,
		MinContextTokens: promptTokens + maxOut, PromptTokens: promptTokens, MaxOut: maxOut,
	}
}

type autoSignal struct{ Profile string }

var autoRE = regexp.MustCompile(`(?i)^auto(?::(.+))?$`)

func parseAutoSignal(s string) *autoSignal {
	m := autoRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil
	}
	return &autoSignal{Profile: strings.TrimSpace(m[1])}
}

type profileResolved struct {
	Name       string
	Preference string
	Require    []string
	RankMap    string
	Providers  []string
}

func resolveProfile(profileName string, routing config.Routing) (profileResolved, error) {
	requested := profileName
	if requested == "" {
		requested = routing.AutoDefaultProfile
	}
	if requested == "" {
		requested = "balanced"
	}
	profiles := routing.Profiles
	name := requested
	p, ok := profiles[name]
	if !ok {
		for k := range profiles {
			if strings.EqualFold(k, requested) {
				name, p, ok = k, profiles[k], true
				break
			}
		}
	}
	if !ok {
		known := sortedKeys(profiles)
		list := strings.Join(known, ", ")
		if list == "" {
			list = "(none configured)"
		}
		return profileResolved{}, apierr.New(400, fmt.Sprintf(`Unknown auto profile "%s". Known profiles: %s.`, requested, list), "invalid_request_error")
	}
	preference := p.Preference
	if preference == "" {
		preference = "balanced"
	}
	if preference != "cost" && preference != "quality" && preference != "balanced" {
		return profileResolved{}, apierr.New(400, fmt.Sprintf(`Profile "%s" has invalid preference "%s" (cost|quality|balanced).`, name, preference), "invalid_request_error")
	}
	require := p.Require
	if require == nil {
		require = []string{}
	}
	providers := p.Providers
	if providers == nil {
		providers = []string{}
	}
	return profileResolved{Name: name, Preference: preference, Require: require, RankMap: p.RankMap, Providers: providers}, nil
}

func sortedKeys(m map[string]config.Profile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- capability helpers (fail-closed) ----

func capStrictTrue(caps map[string]any, key string) bool {
	v, ok := caps[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// capTruthy mirrors JS truthiness (used for streaming_usage, whose manifest value may be "opt-in").
func capTruthy(caps map[string]any, key string) bool {
	v, ok := caps[key]
	if !ok || v == nil {
		return false
	}
	switch n := v.(type) {
	case bool:
		return n
	case string:
		return n != ""
	case float64:
		return n != 0
	case int:
		return n != 0
	}
	return true
}

type rejection struct {
	Target  string
	Reasons []string
}

type scored struct {
	adapters.Candidate
	contextUnverified bool
	quality           int
	qualityRanked     bool
	blendedUsd        float64
	costOrdinal       int
	qualOrdinal       int
	blendedOrdinal    int
}

func filterByCapability(cand []adapters.Candidate, req Requirements, profile profileResolved) ([]scored, []rejection) {
	var capable []scored
	var rejected []rejection
	for _, c := range cand {
		var reasons []string
		caps := c.Capabilities
		if req.NeedsTools && !capStrictTrue(caps, "tools") {
			reasons = append(reasons, "needs tools; model lacks tool support")
		}
		if req.NeedsVision && !capStrictTrue(caps, "vision") {
			reasons = append(reasons, "needs vision; model is not multimodal")
		}
		if req.NeedsStreamingUsage && !capTruthy(caps, "streaming_usage") {
			reasons = append(reasons, "needs streaming usage; model does not report it")
		}
		for _, r := range profile.Require {
			if !capStrictTrue(caps, r) {
				reasons = append(reasons, fmt.Sprintf(`profile requires "%s"; model lacks it`, r))
			}
		}
		if len(profile.Providers) > 0 && !contains(profile.Providers, c.Provider) {
			reasons = append(reasons, fmt.Sprintf(`profile restricts providers to %s; "%s" is not allowed`, strings.Join(profile.Providers, "+"), c.Provider))
		}
		contextUnverified := false
		if c.Context != nil {
			if *c.Context < req.MinContextTokens {
				reasons = append(reasons, fmt.Sprintf("needs ~%d ctx tokens; model window is %d", req.MinContextTokens, *c.Context))
			}
		} else {
			contextUnverified = true
		}
		if c.MaxOutput != nil && *c.MaxOutput < req.MaxOut {
			reasons = append(reasons, fmt.Sprintf("needs %d output tokens; model max_output is %d", req.MaxOut, *c.MaxOutput))
		}
		if len(reasons) > 0 {
			rejected = append(rejected, rejection{Target: key(c), Reasons: reasons})
		} else {
			capable = append(capable, scored{Candidate: c, contextUnverified: contextUnverified})
		}
	}
	return capable, rejected
}

func blendedCost(c adapters.Candidate, promptTokens, maxOut int) float64 {
	p := c.Price
	if p == nil || p.Input == nil || p.Output == nil {
		return math.Inf(1)
	}
	return (float64(promptTokens)*(*p.Input) + float64(maxOut)*(*p.Output)) / 1e6
}

func lessByCost(a, b scored) bool {
	if a.PriceTier != b.PriceTier {
		return a.PriceTier < b.PriceTier
	}
	if a.blendedUsd != b.blendedUsd {
		return a.blendedUsd < b.blendedUsd
	}
	return key(a.Candidate) < key(b.Candidate)
}

func lessByQuality(a, b scored) bool {
	if a.quality != b.quality {
		return a.quality > b.quality
	}
	if a.PriceTier != b.PriceTier {
		return a.PriceTier < b.PriceTier
	}
	return key(a.Candidate) < key(b.Candidate)
}

func scoreCandidates(capable []scored, preference string, req Requirements, qualityRanks map[string]int) []scored {
	withMetrics := make([]scored, len(capable))
	copy(withMetrics, capable)
	for i := range withMetrics {
		k := key(withMetrics[i].Candidate)
		q, ranked := qualityRanks[k]
		if !ranked {
			q = neutralQuality
		}
		withMetrics[i].quality = q
		withMetrics[i].qualityRanked = ranked
		withMetrics[i].blendedUsd = blendedCost(withMetrics[i].Candidate, req.PromptTokens, req.MaxOut)
	}
	switch preference {
	case "cost":
		sort.Slice(withMetrics, func(i, j int) bool { return lessByCost(withMetrics[i], withMetrics[j]) })
	case "quality":
		sort.Slice(withMetrics, func(i, j int) bool { return lessByQuality(withMetrics[i], withMetrics[j]) })
	default:
		costOrder := make([]scored, len(withMetrics))
		copy(costOrder, withMetrics)
		sort.Slice(costOrder, func(i, j int) bool { return lessByCost(costOrder[i], costOrder[j]) })
		qualOrder := make([]scored, len(withMetrics))
		copy(qualOrder, withMetrics)
		sort.Slice(qualOrder, func(i, j int) bool { return lessByQuality(qualOrder[i], qualOrder[j]) })
		cr := map[string]int{}
		qr := map[string]int{}
		for i, c := range costOrder {
			cr[key(c.Candidate)] = i
		}
		for i, c := range qualOrder {
			qr[key(c.Candidate)] = i
		}
		for i := range withMetrics {
			k := key(withMetrics[i].Candidate)
			withMetrics[i].costOrdinal = cr[k]
			withMetrics[i].qualOrdinal = qr[k]
			withMetrics[i].blendedOrdinal = cr[k] + qr[k]
		}
		sort.Slice(withMetrics, func(i, j int) bool {
			a, b := withMetrics[i], withMetrics[j]
			if a.blendedOrdinal != b.blendedOrdinal {
				return a.blendedOrdinal < b.blendedOrdinal
			}
			if a.PriceTier != b.PriceTier {
				return a.PriceTier < b.PriceTier
			}
			return key(a.Candidate) < key(b.Candidate)
		})
	}
	return withMetrics
}

func reasonFor(w scored, preference string) string {
	tierNote := ""
	if w.PriceTier == 1 {
		tierNote = " (among unconfirmed-price candidates)"
	} else if w.PriceTier == 2 {
		tierNote = " (unpriced)"
	}
	usd := "unpriced"
	if !math.IsInf(w.blendedUsd, 0) {
		usd = fmt.Sprintf("$%.6f", w.blendedUsd)
	}
	switch preference {
	case "cost":
		return fmt.Sprintf("cheapest sufficient model%s; est %s/call", tierNote, usd)
	case "quality":
		def := ""
		if !w.qualityRanked {
			def = ", default"
		}
		return fmt.Sprintf("highest operator quality rank (%d%s)%s", w.quality, def, tierNote)
	default:
		return fmt.Sprintf("best blended cost+quality (quality %d, est %s/call)%s", w.quality, usd, tierNote)
	}
}

func describeReq(req Requirements, profile profileResolved) string {
	var parts []string
	if req.NeedsTools {
		parts = append(parts, "tools")
	}
	if req.NeedsVision {
		parts = append(parts, "vision")
	}
	if req.NeedsStreamingUsage {
		parts = append(parts, "streaming usage")
	}
	if len(profile.Require) > 0 {
		parts = append(parts, "profile requires "+strings.Join(profile.Require, "+"))
	}
	parts = append(parts, fmt.Sprintf("~%d context tokens", req.MinContextTokens))
	return strings.Join(parts, ", ")
}

func rejectedToAny(rejected []rejection) []any {
	out := make([]any, 0, len(rejected))
	for _, r := range rejected {
		reasons := make([]any, len(r.Reasons))
		for i, s := range r.Reasons {
			reasons[i] = s
		}
		out = append(out, map[string]any{"target": r.Target, "reasons": reasons})
	}
	return out
}

func joinKeys(list []scored) string {
	ks := make([]string, len(list))
	for i, c := range list {
		ks[i] = key(c.Candidate)
	}
	return strings.Join(ks, ", ")
}

// filterDisabledCandidates drops (provider, model) candidates the operator has disabled for that
// provider, so auto-routing never selects a model that's been switched off in the dashboard. When no
// provider has any disabled model it returns the input unchanged (zero overhead for the common case).
func filterDisabledCandidates(cand []adapters.Candidate, providers []ProviderInfo) []adapters.Candidate {
	disabled := map[string]map[string]bool{}
	for _, p := range providers {
		if len(p.DisabledModels) > 0 {
			disabled[p.Name] = p.DisabledModels
		}
	}
	if len(disabled) == 0 {
		return cand
	}
	var out []adapters.Candidate
	for _, c := range cand {
		if d := disabled[c.Provider]; d != nil && d[c.Model] {
			continue
		}
		out = append(out, c)
	}
	return out
}

func selectAuto(providers []ProviderInfo, routing config.Routing, req map[string]any, signal autoSignal, defaultMaxTokens int) (RouteResult, error) {
	profile, err := resolveProfile(signal.Profile, routing)
	if err != nil {
		return RouteResult{}, err
	}
	reqs := DeriveRequirements(req, defaultMaxTokens)
	var enabledNames []string
	for _, p := range providers {
		if p.Enabled {
			enabledNames = append(enabledNames, p.Name)
		}
	}
	cand := filterDisabledCandidates(adapters.Catalog(enabledNames), providers)
	if len(cand) == 0 {
		return RouteResult{}, apierr.New(400, "Auto routing found no routable models. Enable a provider that publishes a known model catalog (e.g. anthropic, zhipu), then retry.", "invalid_request_error")
	}

	capable, rejected := filterByCapability(cand, reqs, profile)
	if len(capable) == 0 {
		var details []string
		for _, r := range rejected {
			details = append(details, r.Target+": "+strings.Join(r.Reasons, "; "))
		}
		e := apierr.New(400, fmt.Sprintf("No enabled model can satisfy this request (%s). Rejections — %s", describeReq(reqs, profile), strings.Join(details, " | ")), "invalid_request_error")
		e.Code = "no_capable_model"
		e.Decision = map[string]any{"rule_id": "auto_no_capable", "profile": profile.Name, "requirements": reqs, "rejected": rejectedToAny(rejected)}
		return RouteResult{}, e
	}

	var healthy []scored
	for _, c := range capable {
		if !IsTripped(c.Provider) {
			healthy = append(healthy, c)
		}
	}
	if len(healthy) == 0 {
		e := apierr.New(503, fmt.Sprintf("All models capable of this request are currently circuit-tripped (%s). Retry shortly.", joinKeys(capable)), "service_unavailable")
		e.Code = "providers_unavailable"
		e.Decision = map[string]any{"rule_id": "auto_all_tripped", "profile": profile.Name, "requirements": reqs, "tripped": keysToAny(capable)}
		return RouteResult{}, e
	}

	pool := healthy
	if profile.Preference == "cost" {
		var priced []scored
		for _, c := range healthy {
			if c.PriceTier <= 1 {
				priced = append(priced, c)
			}
		}
		if len(priced) == 0 {
			e := apierr.New(400, fmt.Sprintf("auto:%s needs a model with a known price, but none of the capable models are priced (%s). Pin a model, add pricing to its manifest, or use auto:quality / auto:balanced.", profile.Name, joinKeys(healthy)), "invalid_request_error")
			e.Code = "no_priced_model"
			e.Decision = map[string]any{"rule_id": "auto_no_priced", "profile": profile.Name, "requirements": reqs, "unpriced": keysToAny(healthy)}
			return RouteResult{}, e
		}
		pool = priced
	}

	ranks := routing.QualityRanks
	rankSource := "qualityRanks"
	if profile.RankMap != "" {
		if rm := routing.RoleRanks[profile.RankMap]; len(rm) > 0 {
			// per-model fallback: a model absent from the role map falls back to its qualityRanks value, then neutral
			merged := make(map[string]int, len(routing.QualityRanks)+len(rm))
			for k, v := range routing.QualityRanks {
				merged[k] = v
			}
			for k, v := range rm {
				merged[k] = v
			}
			ranks = merged
			rankSource = "roleRanks." + profile.RankMap
		}
	}

	scoredList := scoreCandidates(pool, profile.Preference, reqs, ranks)
	winner := scoredList[0]

	candidates := make([]any, 0, len(scoredList))
	for _, c := range scoredList {
		item := map[string]any{"target": key(c.Candidate), "price_tier": c.PriceTier, "quality": c.quality}
		if math.IsInf(c.blendedUsd, 0) {
			item["est_usd"] = nil
		} else {
			item["est_usd"] = math.Round(c.blendedUsd*1e6) / 1e6
		}
		if c.contextUnverified {
			item["context_unverified"] = true
		}
		candidates = append(candidates, item)
	}
	alternatives := make([]string, 0, len(scoredList)-1)
	for _, c := range scoredList[1:] {
		alternatives = append(alternatives, key(c.Candidate))
	}

	decision := map[string]any{
		"rule_id":      "auto_select",
		"chosen":       key(winner.Candidate),
		"profile":      profile.Name,
		"preference":   profile.Preference,
		"rank_source":  rankSource,
		"requirements": reqs,
		"reason":       fmt.Sprintf("auto:%s — %s", profile.Name, reasonFor(winner, profile.Preference)),
		"candidates":   candidates,
		"alternatives": alternatives,
		"rejected":     rejectedToAny(rejected),
	}
	if len(profile.Providers) > 0 {
		decision["provider_restriction"] = profile.Providers
	}
	return RouteResult{Provider: winner.Provider, Model: winner.Model, Decision: decision}, nil
}

func keysToAny(list []scored) []any {
	out := make([]any, len(list))
	for i, c := range list {
		out[i] = key(c.Candidate)
	}
	return out
}
