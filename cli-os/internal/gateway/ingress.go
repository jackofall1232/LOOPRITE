// OpenAI-compatible ingress. Dispatches a request to one of four paths, all sharing the same
// route/reserve/meter/commit/ledger machinery via runTurn and upstream:
//   - dry-run   (x-l00prite-dry-run): return the routing decision only, no spend, no upstream call
//   - bridge    (armed): buffered bounded delegation loop (bridge.go), then respond
//   - streaming (non-bridge): incremental SSE straight from the provider
//   - default   (non-bridge, non-stream): one runTurn
//
// Ported from ingress.js.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/apierr"
	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
	"github.com/jackofall1232/l00prite/cli-os/internal/ledger"
	"github.com/jackofall1232/l00prite/cli-os/internal/memory"
	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
	pep "github.com/jackofall1232/l00prite/cli-os/internal/policy"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// httpResponse pairs a streamed response with its context cancel.
type httpResponse struct {
	resp   *http.Response
	cancel context.CancelFunc
}

const maxBodyBytes = 8 * 1024 * 1024

func sendJSON(w http.ResponseWriter, status int, obj any) {
	b, _ := json.Marshal(obj)
	w.Header().Set("content-type", "application/json")
	w.Header().Set("content-length", fmt.Sprint(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}

// extra is optional, additive top-level fields merged into the error body (e.g.
// l00prite_proposed_runs) -- variadic so every existing 5-arg call site is unaffected.
func oaiError(w http.ResponseWriter, status int, message, typ, code string, extra ...map[string]any) {
	if typ == "" {
		typ = "invalid_request_error"
	}
	var codeVal any
	if code != "" {
		codeVal = code
	}
	body := map[string]any{"error": map[string]any{"message": message, "type": typ, "code": codeVal, "param": nil}}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	sendJSON(w, status, body)
}

var bearerRE = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

func principalFrom(app *App, r *http.Request) *security.Principal {
	m := bearerRE.FindStringSubmatch(r.Header.Get("Authorization"))
	if m != nil {
		return security.VerifyToken(app.DB, m[1])
	}
	return app.uiPrincipal(r)
}

// AuthorizeScope authenticates an API request and enforces one named capability. Admin-scoped
// legacy tokens remain compatible, while every new least-privilege token fails closed outside its
// explicit scope. HTTP routing calls this before dispatch; handlers retain their existing auth
// checks as defense in depth.
func (app *App) AuthorizeScope(w http.ResponseWriter, r *http.Request, scope string) bool {
	principal := principalFrom(app, r)
	if principal == nil {
		oaiError(w, 401, "Missing or invalid l00prite token", "authentication_error", "")
		return false
	}
	if !principal.HasScope(scope) {
		app.auditAs(principal, "auth.scope_denied", scope+" "+r.Method+" "+r.URL.Path)
		oaiError(w, 403, "Token lacks required scope: "+scope, "permission_error", "insufficient_scope")
		return false
	}
	return true
}

func truthyHeader(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

func splitPaths(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// logRouteError records a routing throw on the ledger so "every decision is logged" holds.
func logRouteError(app *App, requestID, project, repoID string, err error) {
	e := httpErr(err)
	decision := map[string]any{"rule_id": "route_error", "reason": err.Error()}
	ruleID := "route_error"
	if e != nil && e.Decision != nil {
		decision = e.Decision
		if rid, ok := e.Decision["rule_id"].(string); ok {
			ruleID = rid
		}
	}
	ledger.Append(app.DB, app.Cfg.LedgerPath, ledger.Entry{
		RequestID: requestID, Project: project, Repo: repoID, RuleID: ruleID, Decision: decision, Outcome: "denied_route",
	})
}

// HandleChatCompletion is POST /v1/chat/completions.
func (app *App) HandleChatCompletion(w http.ResponseWriter, r *http.Request) {
	cfg := app.Cfg
	requestID := util.RID("req")
	w.Header().Set("x-l00prite-request-id", requestID)
	clientCtx := r.Context() // cancelled when the client disconnects

	principal := principalFrom(app, r)
	if principal == nil {
		oaiError(w, 401, "Missing or invalid l00prite token", "authentication_error", "")
		return
	}

	limited := io.LimitReader(r.Body, maxBodyBytes+1)
	data, rerr := io.ReadAll(limited)
	if len(data) > maxBodyBytes {
		oaiError(w, 413, "Request payload too large", "invalid_request_error", "")
		return
	}
	// A mid-read failure (client disconnect / timeout) surfaces as 400, matching the Node original
	// (its readBody rejection lands in the same catch as a JSON parse error).
	if rerr != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	var openaiReq map[string]any
	if err := json.Unmarshal(data, &openaiReq); err != nil || openaiReq == nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	if asArr(openaiReq["messages"]) == nil {
		oaiError(w, 400, `"messages" is required`, "invalid_request_error", "")
		return
	}

	headerRepo := r.Header.Get("x-l00prite-repo")
	repoID := principal.Repo
	if headerRepo != "" {
		if principal.Repo != "" && headerRepo != principal.Repo {
			oaiError(w, 403, `Token is scoped to repo "`+principal.Repo+`"; it cannot request "`+headerRepo+`"`, "permission_error", "")
			return
		}
		repoID = headerRepo
	}
	repoRoot := ""
	if repoID != "" {
		var root, rproject string
		err := app.DB.QueryRowContext(state.Ctx(), `SELECT root, project FROM repos WHERE id = ?`, repoID).Scan(&root, &rproject)
		if err != nil {
			// This is a GATEWAY-side id mismatch, never a GitHub permission problem — say so, and
			// say how to fix it, because the two failure paths need different repairs: a token
			// permanently scoped to an id nothing was registered under (the wizard used to allow
			// that) can only be fixed by registering that exact id or re-minting the token.
			msg := `Repository "` + repoID + `" is not registered on this gateway. Register it in the dashboard (Repositories → Register repo) or pick a registered repo from the Playground's repo menu. This is a local repo-id lookup — GitHub permissions are not involved.`
			if headerRepo == "" && principal.Repo == repoID {
				msg = `This token is scoped to repo "` + repoID + `", but no repository with that id is registered on this gateway, so every request with this token is refused. Fix: register a repo with exactly the id "` + repoID + `" (dashboard → Repositories → Register repo), or mint a new token without a repo scope (CLI: l00prite token mint --project ` + principal.Project + `). This is a local repo-id lookup — GitHub permissions are not involved.`
			}
			oaiError(w, 404, msg, "invalid_request_error", "repo_not_found")
			return
		}
		if rproject != principal.Project {
			oaiError(w, 403, `Token not scoped to repo "`+repoID+`"`, "permission_error", "")
			return
		}
		repoRoot = root
	}

	project := principal.Project
	routeHeader := r.Header.Get("x-l00prite-route")
	paths := splitPaths(r.Header.Get("x-l00prite-paths"))
	dryRun := truthyHeader(r.Header.Get("x-l00prite-dry-run"))
	armed := !dryRun && IsBridgeArmed(r.Header, cfg)
	stream := openaiReq["stream"] == true

	// ---- Path 1: dry-run route plan ----
	if dryRun {
		route, err := Pick(providerInfos(ListProviders(app.DB)), app.Aliases, openaiReq, routeHeader, cfg)
		if err != nil {
			logRouteError(app, requestID, project, repoID, err)
			e := httpErr(err)
			oaiError(w, statusOr(e, 400), err.Error(), typeOr(e), codeOr(e))
			return
		}
		sendJSON(w, 200, map[string]any{
			"object": "l00prite.route_plan", "request_id": requestID,
			"would_route": map[string]any{"provider": route.Provider, "model": route.Model},
			"decision":    route.Decision,
			"bridge":      map[string]any{"armed": IsBridgeArmed(r.Header, cfg), "max_hops": BridgeMaxHops(r.Header, cfg)},
		})
		return
	}

	// ---- Path 2: bridge ----
	if armed {
		maxHops := BridgeMaxHops(r.Header, cfg)
		result, err := RunBridge(app, requestID, project, repoID, repoRoot, openaiReq, routeHeader, clientCtx, maxHops, paths)
		if err != nil {
			logRouteError(app, requestID, project, repoID, err)
			e := httpErr(err)
			oaiError(w, statusOr(e, 502), errMsgOr(err, "Bridge error"), typeOrDefault(e, "upstream_error"), codeOr(e))
			return
		}
		if result.Denied != nil {
			if result.Denied.Reason == "cost_cap" {
				w.Header().Set("x-l00prite-cap-usd", fmt.Sprint(result.Denied.Cap))
			}
			msg := "Request denied by policy"
			if result.Denied.Reason == "cost_cap" {
				msg = fmt.Sprintf("Daily budget reached mid-bridge after %d delegation(s) and $%.4f committed. Raise or change it in the dashboard (Costs → Set budget), or wait for the UTC-day reset.", result.SubCalls, result.TotalCostUsd)
			}
			oaiError(w, 402, msg, "insufficient_quota", firstNonEmpty(result.Denied.Code, result.Denied.Reason))
			return
		}
		w.Header().Set("x-l00prite-bridge-hops", fmt.Sprint(result.SubCalls))
		w.Header().Set("x-l00prite-cost-usd", fmt.Sprintf("%.6f", result.TotalCostUsd))
		if result.Unconfirmed { // a bridged $ figure that includes unpriced hops is not authoritative
			w.Header().Set("x-l00prite-cost-unconfirmed", "true")
		}
		for _, h := range result.Hops {
			if h["kind"] == "primary" {
				w.Header().Set("x-l00prite-provider", asStr(h["provider"]))
				break
			}
		}
		if stream {
			synthesizeStream(w, result.Response, openaiReqIncludeUsage(openaiReq), result.TotalUsage)
			return
		}
		sendJSON(w, 200, result.Response)
		return
	}

	// ---- Path 3: streaming (non-bridge) ----
	if stream {
		route, err := Pick(providerInfos(ListProviders(app.DB)), app.Aliases, openaiReq, routeHeader, cfg)
		if err != nil {
			logRouteError(app, requestID, project, repoID, err)
			e := httpErr(err)
			oaiError(w, statusOr(e, 400), err.Error(), typeOr(e), codeOr(e))
			return
		}
		provRow := getProvider(app.DB, route.Provider)
		if provRow == nil {
			oaiError(w, 500, `Routed provider "`+route.Provider+`" is not registered`, "configuration_error", "")
			return
		}
		adapter := adapters.AdapterFor(provRow.Adapter)
		apiKey := ""
		if !adapter.Direct() {
			if provRow.BaseURL == "" {
				oaiError(w, 500, `Provider "`+route.Provider+`" has no base URL configured`, "configuration_error", "")
				return
			}
			if provRow.EncKey == "" {
				oaiError(w, 500, `Provider "`+route.Provider+`" has no API key configured`, "configuration_error", "")
				return
			}
			k, derr := security.DecryptSecret(cfg.MasterKeyPath, provRow.EncKey)
			if derr != nil {
				oaiError(w, 500, "Failed to decrypt provider key", "configuration_error", "")
				return
			}
			apiKey = k
		}

		var mem memory.Context
		if repoRoot != "" {
			mem = memory.Query(repoRoot, DigestFrom(openaiReq, paths), cfg.Memory.ContextTokens, cfg.Memory.MaxFileBytes, false)
		} else {
			mem = memory.Context{Status: "empty"}
		}
		maxOut := numToInt(openaiReq["max_tokens"])
		if maxOut == 0 {
			maxOut = numToInt(openaiReq["max_completion_tokens"])
		}
		if maxOut == 0 {
			maxOut = cfg.DefaultMaxTokens
		}
		finalReq := InjectMemory(openaiReq, mem)
		finalReq["max_tokens"] = maxOut
		forwardUsage := openaiReqIncludeUsage(openaiReq)

		ceiling := ReservationCeiling(route.Provider, route.Model, finalReq)
		resv := pep.Reserve(app.DB, project, ceiling, cfg.DefaultDailyCapUsd)
		ruleID := asStr(route.Decision["rule_id"])
		if !resv.OK {
			if resv.Reason == "cost_cap" {
				w.Header().Set("x-l00prite-cap-usd", fmt.Sprint(resv.Cap))
			}
			ledger.Append(app.DB, cfg.LedgerPath, ledger.Entry{RequestID: requestID, Project: project, Repo: repoID, Provider: route.Provider, Model: route.Model, RuleID: ruleID, Decision: route.Decision, Outcome: "denied_" + resv.Reason})
			msg := "Request denied by policy"
			if resv.Reason == "cost_cap" {
				msg = fmt.Sprintf("Daily budget reached ($%.4f of $%.2f). Raise or change it in the dashboard (Costs → Set budget), or wait for the UTC-day reset.", resv.Spent, resv.Cap)
			}
			oaiError(w, 402, msg, "insufficient_quota", resv.Reason)
			return
		}
		usage, headWritten, serr := streamResponse(w, adapter, provRow, route.Model, finalReq, apiKey, cfg, clientCtx, forwardUsage)
		if serr != nil {
			MarkFailure(route.Provider)
			if headWritten {
				pep.Commit(app.DB, resv.ReservationID, ceiling)
				ledger.Append(app.DB, cfg.LedgerPath, ledger.Entry{RequestID: requestID, Project: project, Repo: repoID, Provider: route.Provider, Model: route.Model, RuleID: ruleID, Decision: route.Decision, CostUSD: &ceiling, CostEstimated: true, MemoryStatus: mem.Status, Outcome: "error_midstream"})
			} else {
				pep.Refund(app.DB, resv.ReservationID)
				ledger.Append(app.DB, cfg.LedgerPath, ledger.Entry{RequestID: requestID, Project: project, Repo: repoID, Provider: route.Provider, Model: route.Model, RuleID: ruleID, Decision: route.Decision, MemoryStatus: mem.Status, Outcome: "error"})
				e := httpErr(serr)
				oaiError(w, statusOr(e, 502), errMsgOr(serr, "Upstream error"), typeOrDefault(e, "upstream_error"), "")
			}
			return
		}
		cost := CostOf(route.Provider, route.Model, usage)
		pep.Commit(app.DB, resv.ReservationID, cost.USD)
		MarkSuccess(route.Provider)
		if !provRow.Verified { // first successful streamed use flips the provider to verified
			markProviderVerified(app.DB, route.Provider)
		}
		u := usage
		ledger.Append(app.DB, cfg.LedgerPath, ledger.Entry{RequestID: requestID, Project: project, Repo: repoID, Provider: route.Provider, Model: route.Model, RuleID: ruleID, Decision: route.Decision, Usage: &u, CostUSD: &cost.USD, CostEstimated: cost.Estimated, CostUnconfirmed: cost.Unconfirmed, MemoryStatus: mem.Status, Outcome: "ok"})
		return
	}

	// ---- Path 4: default (non-bridge, non-streaming) ----
	// RunChatTools attaches read-only repo-browsing tools (read_file/list_dir/search_files) when
	// repoRoot names a registered repo, and is a no-op passthrough to runTurn otherwise -- see
	// chatloop.go's doc comment for why streaming/bridging aren't wired to it yet.
	// Resolve the per-turn chat tool budget: project default/override, then any lower-only request
	// header. Human-set ceiling; the loop can never raise its own (see chatlimits.go).
	chatLimits := app.effectiveChatLimits(project, r.Header)
	turn, err := RunChatTools(app, principal, requestID, project, repoID, repoRoot, openaiReq, routeHeader, clientCtx, paths, chatLimits)
	if err != nil {
		logRouteError(app, requestID, project, repoID, err)
		e := httpErr(err)
		oaiError(w, statusOr(e, 502), errMsgOr(err, "Upstream error"), typeOrDefault(e, "upstream_error"), codeOr(e))
		return
	}
	if !turn.OK {
		if turn.Denial.Reason == "cost_cap" {
			w.Header().Set("x-l00prite-cap-usd", fmt.Sprint(turn.Denial.Cap))
		}
		msg := "Request denied by policy"
		if turn.Denial.Reason == "cost_cap" {
			msg = fmt.Sprintf("Daily budget reached ($%.4f of $%.2f). Raise or change it in the dashboard (Costs → Set budget), or wait for the UTC-day reset.", turn.Denial.Spent, turn.Denial.Cap)
		}
		// A run drafted by propose_run in an earlier round of THIS turn is a real, persisted side
		// effect even when a later round's own reservation is denied -- surface it here too, not
		// only on a successful reply (Codex review, PR #8).
		extra := map[string]any{}
		if len(turn.Denial.ProposedRuns) > 0 {
			extra["l00prite_proposed_runs"] = turn.Denial.ProposedRuns
		}
		oaiError(w, 402, msg, "insufficient_quota", firstNonEmpty(turn.Denial.Code, turn.Denial.Reason), extra)
		return
	}
	w.Header().Set("x-l00prite-provider", turn.Route.Provider)
	w.Header().Set("x-l00prite-cost-usd", fmt.Sprintf("%.6f", turn.Cost.USD))
	if turn.Cost.Unconfirmed {
		w.Header().Set("x-l00prite-cost-unconfirmed", "true")
	}
	sendJSON(w, 200, turn.Response)
}

func openaiReqIncludeUsage(req map[string]any) bool {
	so := asMap(req["stream_options"])
	return so != nil && jsTruthy(so["include_usage"])
}

// sortedProfileNames returns the routing profile names in a stable (sorted) order so /v1/models and
// /healthz emit deterministic output (Go map iteration is randomized).
func sortedProfileNames(cfg config.Config) []string {
	names := make([]string, 0, len(cfg.Routing.Profiles))
	for name := range cfg.Routing.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// streamResponse streams provider deltas as SSE. Returns (usage, headWritten, err).
func streamResponse(w http.ResponseWriter, a adapters.Adapter, provider *ProviderRow, model string, req map[string]any, apiKey string, cfg config.Config, clientCtx context.Context, forwardUsage bool) (oai.Usage, bool, error) {
	flusher, _ := w.(http.Flusher)
	writeHead := func() {
		w.Header().Set("content-type", "text/event-stream; charset=utf-8")
		w.Header().Set("cache-control", "no-cache, no-transform")
		w.Header().Set("connection", "keep-alive")
		w.WriteHeader(200)
	}
	writeChunk := func(chunk map[string]any) {
		b, _ := json.Marshal(chunk)
		w.Write([]byte("data: " + string(b) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	writeRaw := func(s string) {
		w.Write([]byte(s))
		if flusher != nil {
			flusher.Flush()
		}
	}

	if a.Direct() {
		writeHead()
		usage := oai.Usage{}
		for _, ev := range a.(adapters.DirectAdapter).DirectStream(req, model) {
			if ev.Chunk != nil {
				writeChunk(ev.Chunk)
			}
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		}
		writeRaw("data: [DONE]\n\n")
		return usage, true, nil
	}

	na := a.(adapters.NetworkAdapter)
	body := na.BuildRequest(model, req, true)
	url := na.URL(provider.BaseURL)
	headers := na.Headers(apiKey)

	// Connect (with retry) BEFORE writing the 200 — a pre-first-byte failure must surface as a
	// proper error + refund, not a silent empty 200 stream.
	var connected *httpResponse
	for attempt := 0; attempt < cfg.Retry.MaxAttempts; attempt++ {
		resp, cancel, err := providerFetch(clientCtx, url, headers, body, cfg.RequestTimeoutMs)
		if err != nil {
			if clientCtx.Err() != nil {
				return oai.Usage{}, false, HTTPError(499, "client closed request", "client_closed")
			}
		} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			connected = &httpResponse{resp: resp, cancel: cancel}
			break
		} else {
			t := readLimited(resp.Body, 2048)
			resp.Body.Close()
			cancel()
			if !isRetryable(resp.StatusCode) {
				return oai.Usage{}, false, HTTPError(resp.StatusCode, fmt.Sprintf("upstream %s %d: %s", provider.Name, resp.StatusCode, truncStr(t, 200)), "upstream_error")
			}
		}
		time.Sleep(backoff(cfg.Retry.BaseMs, attempt, cfg.Retry.MaxMs))
	}
	if connected == nil {
		return oai.Usage{}, false, HTTPError(502, "upstream "+provider.Name+" failed to connect", "upstream_error")
	}
	defer connected.cancel()
	defer connected.resp.Body.Close()

	writeHead()
	st := na.NewStream(model, forwardUsage)
	reader := connected.resp.Body
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 8192)
	var gotUsage *oai.Usage
	// finalUsage mirrors Node's `usage || st.usage`: the terminal usage event if seen, else the
	// accumulator — so a stream that ends before its terminal event still bills the real accrued cost.
	finalUsage := func() oai.Usage {
		if gotUsage != nil {
			return *gotUsage
		}
		return st.Usage()
	}
	for {
		n, rerr := reader.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			normalized := strings.ReplaceAll(string(buf), "\r\n", "\n")
			lastBreak := strings.LastIndex(normalized, "\n\n")
			if lastBreak != -1 {
				ready := normalized[:lastBreak+2]
				buf = []byte(normalized[lastBreak+2:])
				for _, ev := range parseSSE(ready) {
					out := st.OnEvent(ev)
					if out.Usage != nil {
						gotUsage = out.Usage
					}
					for _, chunk := range out.Deltas {
						writeChunk(chunk)
					}
					if out.Done {
						writeRaw("data: [DONE]\n\n")
						return finalUsage(), true, nil
					}
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break // clean end (upstream closed without a terminal event)
			}
			// A real mid-stream failure (network reset, timeout, client disconnect) must fail CLOSED:
			// return the error so the caller commits the reservation ceiling and records
			// error_midstream, instead of committing ~$0 and marking the provider healthy.
			return finalUsage(), true, rerr
		}
	}
	writeRaw("data: [DONE]\n\n")
	return finalUsage(), true, nil
}

// synthesizeStream turns an already-complete (buffered) response into a client SSE stream, used by
// the bridge path so intermediate delegation turns never leak — only the final answer is streamed.
func synthesizeStream(w http.ResponseWriter, finalResponse map[string]any, includeUsage bool, usage oai.Usage) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("content-type", "text/event-stream; charset=utf-8")
	w.Header().Set("cache-control", "no-cache, no-transform")
	w.Header().Set("connection", "keep-alive")
	w.WriteHeader(200)
	write := func(chunk map[string]any) {
		b, _ := json.Marshal(chunk)
		w.Write([]byte("data: " + string(b) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	choice := map[string]any{}
	if choices := asArr(finalResponse["choices"]); len(choices) > 0 {
		choice = asMap(choices[0])
	}
	msg := asMap(choice["message"])
	if msg == nil {
		msg = map[string]any{}
	}
	id := asStr(finalResponse["id"])
	if id == "" {
		id = oai.CmplID()
	}
	model := asStr(finalResponse["model"])
	write(oai.Chunk(id, model, map[string]any{"role": "assistant", "content": ""}, nil))
	if content := asStr(msg["content"]); content != "" {
		write(oai.Chunk(id, model, map[string]any{"content": content}, nil))
	}
	if tcs := asArr(msg["tool_calls"]); len(tcs) > 0 {
		for i, tcRaw := range tcs {
			tc := asMap(tcRaw)
			fn := asMap(tc["function"])
			write(oai.Chunk(id, model, map[string]any{"tool_calls": []any{map[string]any{
				"index": i, "id": tc["id"], "type": "function",
				"function": map[string]any{"name": fn["name"], "arguments": firstNonEmpty(asStr(fn["arguments"]), "")},
			}}}, nil))
		}
	}
	finish := asStr(choice["finish_reason"])
	if finish == "" {
		finish = "stop"
	}
	write(oai.Chunk(id, model, map[string]any{}, finish))
	if includeUsage {
		u := map[string]any{"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens, "total_tokens": usage.PromptTokens + usage.CompletionTokens}
		if usage.CacheReadTokens != 0 {
			u["prompt_tokens_details"] = map[string]any{"cached_tokens": usage.CacheReadTokens}
		}
		write(map[string]any{"id": id, "object": "chat.completion.chunk", "created": oai.Created(), "model": model, "choices": []any{}, "usage": u})
	}
	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

// HandleModels is GET /v1/models.
func (app *App) HandleModels(w http.ResponseWriter, r *http.Request) {
	var data []any
	for _, p := range ListProviders(app.DB) {
		if !p.Enabled {
			continue
		}
		for _, id := range adapters.ModelsFor(p.Name) {
			if p.DisabledModels[id] { // operator-disabled models are not advertised in the catalog
				continue
			}
			data = append(data, map[string]any{"id": id, "object": "model", "owned_by": p.Name})
			data = append(data, map[string]any{"id": p.Name + "/" + id, "object": "model", "owned_by": p.Name})
		}
	}
	for _, name := range sortedProfileNames(app.Cfg) {
		data = append(data, map[string]any{"id": "auto:" + name, "object": "model", "owned_by": "l00prite-auto"})
	}
	data = append(data, map[string]any{"id": "auto", "object": "model", "owned_by": "l00prite-auto"})
	sendJSON(w, 200, map[string]any{"object": "list", "data": data})
}

// HandleHealth is GET /healthz.
func (app *App) HandleHealth(w http.ResponseWriter, r *http.Request) {
	var providers []any
	for _, p := range ListProviders(app.DB) {
		providers = append(providers, map[string]any{
			"name": p.Name, "enabled": p.Enabled, "default": p.IsDefault,
			"circuit_open": IsTripped(p.Name), "has_key": p.EncKey != "" || p.Adapter == "mock",
		})
	}
	var profileNames []any
	for _, name := range sortedProfileNames(app.Cfg) {
		profileNames = append(profileNames, name)
	}
	sendJSON(w, 200, map[string]any{
		"status": "ok", "version": Version, "providers": providers,
		"bridge":        map[string]any{"enabled": app.Cfg.Routing.Bridge.Enabled, "max_hops": app.Cfg.Routing.Bridge.MaxHops},
		"auto_profiles": profileNames,
	})
}

// ---- small helpers ----

func httpErr(err error) *apierr.Error { return apierr.As(err) }

func statusOr(e *apierr.Error, d int) int {
	if e != nil && e.Status != 0 {
		return e.Status
	}
	return d
}
func typeOr(e *apierr.Error) string {
	if e != nil && e.Type != "" {
		return e.Type
	}
	return "invalid_request_error"
}
func typeOrDefault(e *apierr.Error, d string) string {
	if e != nil && e.Type != "" {
		return e.Type
	}
	return d
}
func codeOr(e *apierr.Error) string {
	if e != nil {
		return e.Code
	}
	return ""
}
func errMsgOr(err error, d string) string {
	if err != nil && err.Error() != "" {
		return err.Error()
	}
	return d
}
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
