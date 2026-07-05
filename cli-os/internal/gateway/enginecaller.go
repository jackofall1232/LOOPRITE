// EngineCaller bridges the L00prite OS run engine to the gateway. It implements
// engine.ModelCaller so every autonomous model call flows through the SAME runTurn/RunBridge
// primitives as a client request — routing, PEP budget reservation, metering, and request
// ledgering all apply to autonomous work with no bypass path. The engine never imports the
// gateway or names a provider; it hands us an OpenAI-shaped request whose model is typically
// "auto:<profile>", and we route it.
package gateway

import (
	"context"

	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// EngineCaller is the gateway-backed implementation of engine.ModelCaller.
type EngineCaller struct{ App *App }

// NewEngineCaller wires an engine to this gateway App.
func NewEngineCaller(app *App) *EngineCaller { return &EngineCaller{App: app} }

// Turn issues one completion. When Bridge is set it runs the bounded cross-provider delegation
// loop (in-flight multi-provider collaboration); otherwise it is a single runTurn. A PEP denial
// (budget cap) is surfaced as TurnResult.DeniedReason so the engine stops at a boundary rather
// than retrying past the cap — never as a hard error.
func (c *EngineCaller) Turn(ctx context.Context, in engine.TurnInput) (engine.TurnResult, error) {
	requestID := util.RID("run")
	if in.Bridge {
		maxHops := c.App.Cfg.Routing.Bridge.MaxHops
		if maxHops <= 0 {
			maxHops = 3
		}
		br, err := RunBridge(c.App, requestID, in.Project, in.RepoID, in.RepoRoot, in.Req, "", ctx, maxHops, nil)
		if err != nil {
			return engine.TurnResult{}, err
		}
		if br.Denied != nil {
			return engine.TurnResult{DeniedReason: br.Denied.Reason}, nil
		}
		// The bridge reports the primary route on its first hop; surface it for the feed.
		prov, model := primaryHop(br.Hops)
		return engine.TurnResult{
			Message:     messageOf(br.Response),
			Provider:    prov,
			Model:       model,
			CostUSD:     br.TotalCostUsd,
			Unconfirmed: br.Unconfirmed,
		}, nil
	}

	res, err := runTurn(c.App, TurnOpts{
		Project: in.Project, RepoID: in.RepoID, RepoRoot: in.RepoRoot,
		OpenaiReq: in.Req, ClientCtx: ctx, RequestID: requestID,
		Depth: 0, InjectMemory: in.InjectMemory, Meta: in.Meta,
	})
	if err != nil {
		return engine.TurnResult{}, err
	}
	if !res.OK {
		reason := "request denied"
		if res.Denial != nil {
			reason = res.Denial.Reason
		}
		return engine.TurnResult{DeniedReason: reason}, nil
	}
	return engine.TurnResult{
		Message:     messageOf(res.Response),
		Provider:    res.Route.Provider,
		Model:       res.Route.Model,
		CostUSD:     res.Cost.USD,
		Unconfirmed: res.Cost.Unconfirmed,
	}, nil
}

// PreviewRoute resolves what a model spec (e.g. "auto:code") would route to right now, without
// spending anything — used for the pre-flight team display. It runs only the router, no provider
// call, so an unroutable role surfaces as a pre-flight blocker.
func (c *EngineCaller) PreviewRoute(project, repoID, model string, sampleReq map[string]any) (engine.RoutePreview, error) {
	req := map[string]any{}
	for k, v := range sampleReq {
		req[k] = v
	}
	req["model"] = model
	route, err := Pick(providerInfos(ListProviders(c.App.DB)), c.App.Cfg.Aliases, req, "", c.App.Cfg)
	if err != nil {
		return engine.RoutePreview{}, err
	}
	reason := ""
	if route.Decision != nil {
		if s, ok := route.Decision["reason"].(string); ok {
			reason = s
		}
	}
	if reason == "" {
		reason = "routed to " + route.Provider + "/" + route.Model
	}
	return engine.RoutePreview{Provider: route.Provider, Model: route.Model, Reason: reason}, nil
}

func primaryHop(hops []map[string]any) (provider, model string) {
	for _, h := range hops {
		if asStr(h["kind"]) == "primary" {
			return asStr(h["provider"]), asStr(h["model"])
		}
	}
	return "", ""
}
