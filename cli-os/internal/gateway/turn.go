// runTurn — the single internal "one completion" primitive: route -> (memory) -> reserve -> call ->
// meter -> commit -> ledger, with NO HTTP concerns. Both the ingress non-streaming path and the
// bridge loop call this, so a delegated hop reuses the exact same money/ledger machinery as a
// top-level request. Ported from turn.js.
package gateway

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
	"github.com/jackofall1232/l00prite/cli-os/internal/ledger"
	"github.com/jackofall1232/l00prite/cli-os/internal/memory"
	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
	pep "github.com/jackofall1232/l00prite/cli-os/internal/policy"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// App is the per-server context (db + config + aliases) threaded through the request path.
type App struct {
	DB      *sql.DB
	Cfg     config.Config
	Aliases map[string]string
	// StartedAt is the process boot time, set by server.Start, and reported as real uptime by the
	// dashboard. A zero value (e.g. an App built directly in a test) is reported as "unknown", never
	// as a fabricated availability figure.
	StartedAt time.Time
	// Engine is the L00prite OS run engine (nil in tests that don't exercise runs). It drives
	// autonomous runs through this same App via the EngineCaller seam.
	Engine *engine.Engine
}

// ProviderRow is a full providers-table row.
type ProviderRow struct {
	Name      string
	Adapter   string
	BaseURL   string
	EncKey    string
	Enabled   bool
	IsDefault bool
	// Verified is true once a real routed request has succeeded against this provider's current key.
	// A freshly added provider, or one whose key was just rotated, is unverified until first use.
	Verified bool
	// DisabledModels is the set of this provider's manifest models the operator has switched OFF (Part
	// C/E model selection). Absence means enabled, so a nil map (no selection stored, and every test
	// literal that omits it) leaves the full catalog routable exactly as before.
	DisabledModels map[string]bool
}

// Denial is a reservation denial surfaced to the caller.
type Denial struct {
	Status int
	Reason string
	Code   string
	Cap    float64
	Spent  float64
	// ProposedRuns carries any run(s) genuinely drafted by propose_run in an EARLIER round of the
	// same RunChatTools turn, before a LATER round's reservation was denied (Codex review, PR #8).
	// A draft is a real, persisted side effect even though the reply that would have described it
	// got denied — it must not go invisible to the client just because the turn as a whole failed.
	ProposedRuns []*proposedRun
}

// TurnResult is the runTurn outcome.
type TurnResult struct {
	OK        bool
	Response  map[string]any
	Usage     oai.Usage
	Cost      Cost
	Route     RouteResult
	MemStatus string
	Denial    *Denial
}

// TurnOpts are the inputs to runTurn.
type TurnOpts struct {
	Project      string
	RepoID       string
	RepoRoot     string
	OpenaiReq    map[string]any
	RouteHeader  string
	ClientCtx    context.Context
	RequestID    string
	Paths        []string
	Depth        int
	InjectMemory bool
	Meta         map[string]any
}

// ListProviders returns the provider rows (default first, then name). The correlated subquery folds
// each provider's disabled-model set into the SAME query — one round trip, and (with the single-conn
// pool) no open-cursor ordering hazard from a second query. Model ids never contain commas, so the
// group_concat split is unambiguous.
func ListProviders(db *sql.DB) []ProviderRow {
	rows, err := db.QueryContext(state.Ctx(),
		`SELECT name, adapter, base_url, enc_key, enabled, is_default, verified,
		        (SELECT group_concat(model) FROM provider_models pm WHERE pm.provider = providers.name AND pm.enabled = 0)
		   FROM providers ORDER BY is_default DESC, name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ProviderRow
	for rows.Next() {
		var (
			baseURL, encKey       sql.NullString
			name, adapter         string
			enabled, isDef, verif int
			disabled              sql.NullString
		)
		if err := rows.Scan(&name, &adapter, &baseURL, &encKey, &enabled, &isDef, &verif, &disabled); err != nil {
			continue
		}
		out = append(out, ProviderRow{
			Name: name, Adapter: adapter, BaseURL: baseURL.String, EncKey: encKey.String,
			Enabled: enabled != 0, IsDefault: isDef != 0, Verified: verif != 0,
			DisabledModels: parseDisabledSet(disabled.String),
		})
	}
	return out
}

// parseDisabledSet turns a group_concat("a,b,c") list into a set; "" yields nil (nothing disabled).
func parseDisabledSet(csv string) map[string]bool {
	if csv == "" {
		return nil
	}
	set := map[string]bool{}
	for _, m := range strings.Split(csv, ",") {
		if m != "" {
			set[m] = true
		}
	}
	return set
}

func providerInfos(rows []ProviderRow) []ProviderInfo {
	out := make([]ProviderInfo, len(rows))
	for i, r := range rows {
		out[i] = ProviderInfo{Name: r.Name, Enabled: r.Enabled, IsDefault: r.IsDefault, DisabledModels: r.DisabledModels}
	}
	return out
}

// getProvider fetches a single provider row fully populated — including DisabledModels via the same
// correlated subquery ListProviders uses — so a caller can never trip over a silently-nil selection set
// (the subquery is one indexed lookup on the tiny provider_models table, negligible on the hot path).
func getProvider(db *sql.DB, name string) *ProviderRow {
	var (
		adapter               string
		baseURL, encKey       sql.NullString
		enabled, isDef, verif int
		disabled              sql.NullString
	)
	err := db.QueryRowContext(state.Ctx(),
		`SELECT adapter, base_url, enc_key, enabled, is_default, verified,
		        (SELECT group_concat(model) FROM provider_models pm WHERE pm.provider = providers.name AND pm.enabled = 0)
		   FROM providers WHERE name = ?`, name).
		Scan(&adapter, &baseURL, &encKey, &enabled, &isDef, &verif, &disabled)
	if err != nil {
		return nil
	}
	return &ProviderRow{
		Name: name, Adapter: adapter, BaseURL: baseURL.String, EncKey: encKey.String,
		Enabled: enabled != 0, IsDefault: isDef != 0, Verified: verif != 0,
		DisabledModels: parseDisabledSet(disabled.String),
	}
}

// markProviderVerified flips a provider to verified the first time a real routed request succeeds
// against its current key. It is a no-op once verified (the guard is the caller's provRow.Verified
// check plus the WHERE clause), so it never repeatedly writes on the hot path, and a key rotation —
// which resets verified to 0 — correctly re-arms it.
func markProviderVerified(db *sql.DB, name string) {
	_, _ = db.ExecContext(state.Ctx(), `UPDATE providers SET verified = 1 WHERE name = ? AND verified = 0`, name)
}

const intentCap = 2000

func userIntent(content any) string {
	if s, ok := content.(string); ok {
		return truncStr(s, intentCap)
	}
	if arr := asArr(content); arr != nil {
		var parts []string
		for _, pp := range arr {
			p := asMap(pp)
			switch asStr(p["type"]) {
			case "text":
				if t := asStr(p["text"]); t != "" {
					parts = append(parts, t)
				}
			case "image_url":
				parts = append(parts, "[image]")
			}
		}
		joined := joinNonEmpty(parts, " ")
		return truncStr(joined, intentCap)
	}
	return ""
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// DigestFrom builds the minimal safe projection for the memory layer (never the raw prompt).
func DigestFrom(req map[string]any, paths []string) memory.Digest {
	messages := asArr(req["messages"])
	var lastUserContent any
	for i := len(messages) - 1; i >= 0; i-- {
		m := asMap(messages[i])
		if m != nil && asStr(m["role"]) == "user" {
			lastUserContent = m["content"]
			break
		}
	}
	var toolNames []string
	for _, mm := range messages {
		m := asMap(mm)
		for _, tcRaw := range asArr(m["tool_calls"]) {
			if n := asStr(asMap(asMap(tcRaw)["function"])["name"]); n != "" {
				toolNames = append(toolNames, n)
			}
		}
	}
	return memory.Digest{UserIntent: userIntent(lastUserContent), ReferencedPaths: paths, RecentToolCalls: toolNames}
}

func mergeDecision(base map[string]any, depth int, meta map[string]any) map[string]any {
	d := copyMap(base)
	d["depth"] = depth
	for k, v := range meta {
		d[k] = v
	}
	return d
}

func runTurn(app *App, opts TurnOpts) (TurnResult, error) {
	db, cfg := app.DB, app.Cfg
	aliases := app.Aliases
	if aliases == nil {
		aliases = cfg.Aliases
	}

	route, err := Pick(providerInfos(ListProviders(db)), aliases, opts.OpenaiReq, opts.RouteHeader, cfg)
	if err != nil {
		return TurnResult{}, err
	}
	provRow := getProvider(db, route.Provider)
	if provRow == nil {
		return TurnResult{}, HTTPError(500, `Routed provider "`+route.Provider+`" is not registered`, "configuration_error")
	}
	adapter := adapters.AdapterFor(provRow.Adapter)

	apiKey := ""
	if !adapter.Direct() {
		if provRow.BaseURL == "" {
			return TurnResult{}, HTTPError(500, `Provider "`+route.Provider+`" has no base URL configured`, "configuration_error")
		}
		if provRow.EncKey == "" {
			return TurnResult{}, HTTPError(500, `Provider "`+route.Provider+`" has no API key configured`, "configuration_error")
		}
		k, derr := security.DecryptSecret(cfg.MasterKeyPath, provRow.EncKey)
		if derr != nil {
			return TurnResult{}, HTTPError(500, "Failed to decrypt provider key", "configuration_error")
		}
		apiKey = k
	}

	// Memory injection is a top-level (depth 0) concern; delegated sub-calls run without repo memory.
	var mem memory.Context
	if opts.InjectMemory && opts.RepoRoot != "" {
		mem = memory.Query(opts.RepoRoot, DigestFrom(opts.OpenaiReq, opts.Paths), cfg.Memory.ContextTokens, cfg.Memory.MaxFileBytes, false)
	} else if opts.InjectMemory {
		mem = memory.Context{Status: "empty"}
	} else {
		mem = memory.Context{Status: "skipped"}
	}

	maxOut := numToInt(opts.OpenaiReq["max_tokens"])
	if maxOut == 0 {
		maxOut = numToInt(opts.OpenaiReq["max_completion_tokens"])
	}
	if maxOut == 0 {
		maxOut = cfg.DefaultMaxTokens
	}
	// Never forward client transport flags on this non-streaming primitive.
	cleanReq := copyMap(opts.OpenaiReq)
	delete(cleanReq, "stream")
	delete(cleanReq, "stream_options")
	finalReq := InjectMemory(cleanReq, mem)
	finalReq["max_tokens"] = maxOut

	ceiling := ReservationCeiling(route.Provider, route.Model, finalReq)
	resv := pep.Reserve(db, opts.Project, ceiling, cfg.DefaultDailyCapUsd)
	decision := mergeDecision(route.Decision, opts.Depth, opts.Meta)
	ruleID := asStr(route.Decision["rule_id"])

	if !resv.OK {
		ledger.Append(db, cfg.LedgerPath, ledger.Entry{
			RequestID: opts.RequestID, Project: opts.Project, Repo: opts.RepoID,
			Provider: route.Provider, Model: route.Model, RuleID: ruleID, Decision: decision,
			MemoryStatus: mem.Status, Outcome: "denied_" + resv.Reason,
		})
		return TurnResult{OK: false, Denial: &Denial{Status: 402, Reason: resv.Reason, Code: resv.Reason, Cap: resv.Cap, Spent: resv.Spent}}, nil
	}

	result, cerr := callNonStream(adapter, *provRow, route.Model, finalReq, apiKey, cfg, opts.ClientCtx)
	if cerr != nil {
		MarkFailure(route.Provider)
		pep.Refund(db, resv.ReservationID)
		ledger.Append(db, cfg.LedgerPath, ledger.Entry{
			RequestID: opts.RequestID, Project: opts.Project, Repo: opts.RepoID,
			Provider: route.Provider, Model: route.Model, RuleID: ruleID, Decision: decision,
			MemoryStatus: mem.Status, Outcome: "error",
		})
		return TurnResult{}, cerr
	}

	cost := CostOf(route.Provider, route.Model, result.Usage)
	pep.Commit(db, resv.ReservationID, cost.USD)
	MarkSuccess(route.Provider)
	if !provRow.Verified { // first successful real use proves the key works "in anger" — flip verified
		markProviderVerified(db, route.Provider)
	}
	usage := result.Usage
	ledger.Append(db, cfg.LedgerPath, ledger.Entry{
		RequestID: opts.RequestID, Project: opts.Project, Repo: opts.RepoID,
		Provider: route.Provider, Model: route.Model, RuleID: ruleID, Decision: decision,
		Usage: &usage, CostUSD: &cost.USD, CostEstimated: cost.Estimated, CostUnconfirmed: cost.Unconfirmed,
		MemoryStatus: mem.Status, Outcome: "ok",
	})
	return TurnResult{OK: true, Response: result.Response, Usage: result.Usage, Cost: cost, Route: route, MemStatus: mem.Status}, nil
}
