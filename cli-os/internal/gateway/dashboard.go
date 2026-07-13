// Read-only operator dashboard state. GET /v1/dashboard/summary authenticates exactly like every
// other data endpoint (a gateway Bearer token) and returns a snapshot assembled ENTIRELY from real
// state: the providers table + live circuit-breaker health, the repos table + real mtime-based memory
// freshness, the tokens table (never a secret), the spend/caps tables, and the run ledger + audit log.
// Anything not actually tracked (per-provider latency, historical availability %) is OMITTED rather
// than fabricated — the UI renders an explicit "not tracked" state for those, never a plausible fake.
package gateway

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
	"github.com/jackofall1232/l00prite/cli-os/internal/ledger"
	"github.com/jackofall1232/l00prite/cli-os/internal/memory"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// Version is the single source of truth for the reported server version (healthz + dashboard).
// It is a var (not a const) so a release build can override it via
//
//	-ldflags "-X 'github.com/jackofall1232/l00prite/cli-os/internal/gateway.Version=<version>'"
//
// A plain `go build` / `go test` keeps this in-source default.
var Version = "0.7.0-beta"

// provAgg is one provider's real, ledger-derived activity for the current UTC day.
type provAgg struct {
	requests    int
	ok          int
	errors      int
	promptTok   int
	complTok    int
	cost        float64
	unconfirmed bool
	lastTS      string
}

// HandleDashboardSummary is GET /v1/dashboard/summary. Same auth as /v1/chat/completions.
func (app *App) HandleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(app, r)
	if principal == nil {
		oaiError(w, 401, "Missing or invalid l00prite token", "authentication_error", "")
		return
	}
	sendJSON(w, 200, app.buildSummary(principal))
}

// buildSummary assembles the whole dashboard snapshot from real state.
func (app *App) buildSummary(principal *security.Principal) map[string]any {
	db := app.DB
	day := util.UTCDay()
	todayAgg := app.providerAggToday(day)
	providers := ListProviders(db)

	// ---- providers ----
	var provOut []any
	var byProviderToday []any
	provTotal, provEnabled, provHealthy, circuitOpen := 0, 0, 0, 0
	anyPricingGap := false
	for _, p := range providers {
		provTotal++
		hasKey := p.EncKey != "" || p.Adapter == "mock"
		tripped := IsTripped(p.Name)
		if p.Enabled {
			provEnabled++
		}
		if tripped {
			circuitOpen++
		}
		if p.Enabled && hasKey && !tripped {
			provHealthy++
		}
		agg := todayAgg[p.Name]
		if agg.unconfirmed {
			anyPricingGap = true
		}
		var lastUsed any
		if agg.lastTS != "" {
			lastUsed = agg.lastTS
		}
		provOut = append(provOut, map[string]any{
			"name": p.Name, "adapter": p.Adapter, "base_url": p.BaseURL,
			"enabled": p.Enabled, "is_default": p.IsDefault, "has_key": hasKey,
			"verified": p.Verified, "circuit_open": tripped, "models": modelsOrEmpty(p.Name),
			"disabled_models": strSliceAny(sortedDisabled(p.DisabledModels)),
			// Pre-removal impact, computed server-side so the dashboard's warning matches the confirm gate.
			"removal": app.removalImpact(p.Name, providers),
			"today": map[string]any{
				"cost_usd": agg.cost, "requests": agg.requests, "ok": agg.ok, "errors": agg.errors,
				"prompt_tokens": agg.promptTok, "completion_tokens": agg.complTok, "cost_unconfirmed": agg.unconfirmed,
			},
			"last_used_at": lastUsed,
			// latency is NOT tracked historically — the UI shows "—", never a fabricated number.
		})
		if agg.requests > 0 || agg.cost > 0 {
			byProviderToday = append(byProviderToday, map[string]any{
				"provider": p.Name, "cost_usd": agg.cost, "requests": agg.requests, "cost_unconfirmed": agg.unconfirmed,
			})
		}
	}

	// ---- repos + real memory freshness ----
	var repoOut []any
	staleRepos := 0
	for _, repo := range listRepos(db) {
		fr := memory.RepoFreshness(repo.root)
		if fr.StaleCount > 0 {
			staleRepos++
		}
		var files []any
		for _, f := range fr.Files {
			files = append(files, map[string]any{
				"name": f.Name, "present": f.Present, "as_of": nilIfEmpty(f.AsOf), "stale": f.Stale, "size_bytes": f.SizeBytes,
			})
		}
		repoOut = append(repoOut, map[string]any{
			"id": repo.id, "root": repo.root, "project": repo.project, "created_at": repo.createdAt,
			"memory": map[string]any{
				"status": fr.Status, "present_count": fr.PresentCount, "stale_count": fr.StaleCount,
				"total_files": fr.TotalFiles, "files": files,
			},
		})
	}

	// ---- tokens (never a secret) ----
	tokens := security.ListTokens(db)
	activeTok, revokedTok := 0, 0
	var tokenList []any
	for _, t := range tokens {
		if t.Revoked {
			revokedTok++
		} else {
			activeTok++
		}
		tokenList = append(tokenList, map[string]any{
			"id": t.ID, "project": t.Project, "repo": nilIfEmpty(t.Repo), "revoked": t.Revoked,
			"expires_at": nilIfEmpty(t.ExpiresAt), "created_at": t.CreatedAt,
		})
	}

	// ---- spend (real reserved/committed + caps) ----
	spendByProject, todayCommitted, todayReserved := app.spendToday(day)
	history := app.spendHistory(14)

	// ---- activity (recent ledger) + audit ----
	var activity []any
	for _, row := range ledger.Recent(db, 15) {
		activity = append(activity, map[string]any{
			"ts": row.TS, "provider": nilIfEmpty(row.Provider), "model": nilIfEmpty(row.Model),
			"outcome": row.Outcome, "cost_usd": nullFloat(row.CostUSD), "cost_unconfirmed": row.CostUnconfirmed,
			"request_id": nilIfEmpty(row.RequestID), "rule_id": nilIfEmpty(row.RuleID), "project": nilIfEmpty(row.Project),
		})
	}
	audit := app.recentAudit(15)

	// ---- derived alerts (all real signals) ----
	alerts := app.deriveAlerts(providers, spendByProject, staleRepos, circuitOpen)

	// ---- uptime (real process start) ----
	var uptime map[string]any
	if app.StartedAt.IsZero() {
		uptime = map[string]any{"started_at": nil, "seconds": 0, "tracked": false}
	} else {
		uptime = map[string]any{"started_at": util.ISOFromTime(app.StartedAt), "seconds": int(time.Since(app.StartedAt).Seconds()), "tracked": true}
	}

	dbOK := app.dbPing()
	// A specific, honest headline so the average user sees exactly what broke after removing/disabling
	// their last provider — not a green "Operational" over an empty provider list, and not a vague
	// "Degraded" when the real problem is "there is nothing to route to."
	sysStatus, statusLabel := "ok", "Operational"
	switch {
	case provTotal == 0:
		sysStatus, statusLabel = "degraded", "No providers configured"
	case provEnabled == 0:
		sysStatus, statusLabel = "degraded", "All providers disabled"
	case !dbOK:
		sysStatus, statusLabel = "degraded", "Database error"
	case circuitOpen > 0:
		sysStatus, statusLabel = "degraded", "Degraded"
	}

	return map[string]any{
		"object":       "l00prite.dashboard_summary",
		"version":      Version,
		"generated_at": util.NowISO(),
		"principal":    map[string]any{"token_id": principal.TokenID, "project": principal.Project, "repo": nilIfEmpty(principal.Repo)},
		"uptime":       uptime,
		"system": map[string]any{
			"status": sysStatus, "status_label": statusLabel,
			"providers_total": provTotal, "providers_enabled": provEnabled,
			"providers_healthy": provHealthy, "circuit_open": circuitOpen,
			"vault_initialized": config.MasterKeyPresent(app.Cfg), "db_ok": dbOK,
			"bridge": map[string]any{"enabled": app.Cfg.Routing.Bridge.Enabled, "max_hops": app.Cfg.Routing.Bridge.MaxHops},
		},
		"providers": orEmpty(provOut),
		"repos":     orEmpty(repoOut),
		"tokens":    map[string]any{"active": activeTok, "revoked": revokedTok, "total": len(tokens), "list": orEmpty(tokenList)},
		"spend": map[string]any{
			"day": day, "today_committed_usd": todayCommitted, "today_reserved_usd": todayReserved,
			"by_project": orEmpty(spendByProject), "by_provider_today": orEmpty(byProviderToday),
			"history": orEmpty(history), "has_pricing_gaps": anyPricingGap,
			"default_daily_cap_usd": app.Cfg.DefaultDailyCapUsd,
		},
		"activity": orEmpty(activity),
		"audit":    orEmpty(audit),
		"alerts":   orEmpty(alerts),
	}
}

// providerAggToday returns per-provider, ledger-derived activity for the UTC day (real, not sampled).
func (app *App) providerAggToday(day string) map[string]provAgg {
	out := map[string]provAgg{}
	rows, err := app.DB.QueryContext(state.Ctx(),
		`SELECT provider,
		        COUNT(*),
		        SUM(CASE WHEN outcome='ok' THEN 1 ELSE 0 END),
		        SUM(CASE WHEN outcome IS NULL OR outcome <> 'ok' THEN 1 ELSE 0 END),
		        COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		        COALESCE(SUM(cost_usd),0), MAX(cost_unconfirmed), MAX(ts)
		   FROM ledger
		  WHERE provider IS NOT NULL AND substr(ts,1,10) = ?
		  GROUP BY provider`, day)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name           sql.NullString
			reqs, ok, errs sql.NullInt64
			pt, ct         sql.NullInt64
			cost           sql.NullFloat64
			unconf         sql.NullInt64
			lastTS         sql.NullString
		)
		if rows.Scan(&name, &reqs, &ok, &errs, &pt, &ct, &cost, &unconf, &lastTS) != nil {
			continue
		}
		out[name.String] = provAgg{
			requests: int(reqs.Int64), ok: int(ok.Int64), errors: int(errs.Int64),
			promptTok: int(pt.Int64), complTok: int(ct.Int64), cost: cost.Float64,
			unconfirmed: unconf.Int64 != 0, lastTS: lastTS.String,
		}
	}
	return out
}

// dbPing is a real, cheap liveness check for the store so the dashboard's "Database" signal reflects
// actual state rather than a hardcoded value.
func (app *App) dbPing() bool {
	var one int
	err := app.DB.QueryRowContext(state.Ctx(), `SELECT 1`).Scan(&one)
	return err == nil && one == 1
}

type repoRow struct{ id, root, project, createdAt string }

func listRepos(db *sql.DB) []repoRow {
	rows, err := db.QueryContext(state.Ctx(), `SELECT id, root, project, created_at FROM repos ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []repoRow
	for rows.Next() {
		var r repoRow
		if rows.Scan(&r.id, &r.root, &r.project, &r.createdAt) == nil {
			out = append(out, r)
		}
	}
	return out
}

// spendToday returns per-project spend (reserved/committed/cap) for the day plus the day totals. It
// unions the caps table (projects with a cap but no spend yet) with the spend table (projects that
// have spent), so a configured cap is visible even before its first request.
func (app *App) spendToday(day string) (byProject []any, committedTotal, reservedTotal float64) {
	type capRow struct {
		limit, overage float64
		unlimited      bool
	}
	caps := map[string]capRow{}
	if rows, err := app.DB.QueryContext(state.Ctx(), `SELECT project, limit_usd, overage_pct, unlimited FROM caps WHERE window='daily'`); err == nil {
		for rows.Next() {
			var p string
			var c capRow
			var unl int
			if rows.Scan(&p, &c.limit, &c.overage, &unl) == nil {
				c.unlimited = unl != 0
				caps[p] = c
			}
		}
		rows.Close()
	}
	type sp struct{ reserved, committed float64 }
	spends := map[string]sp{}
	if rows, err := app.DB.QueryContext(state.Ctx(), `SELECT project, reserved_usd, committed_usd FROM spend WHERE day = ?`, day); err == nil {
		for rows.Next() {
			var p string
			var s sp
			if rows.Scan(&p, &s.reserved, &s.committed) == nil {
				spends[p] = s
				committedTotal += s.committed
				reservedTotal += s.reserved
			}
		}
		rows.Close()
	}
	seen := map[string]bool{}
	emit := func(project string) {
		if seen[project] {
			return
		}
		seen[project] = true
		s := spends[project]
		capUSD, over, unl, hasCap := app.Cfg.DefaultDailyCapUsd, 0.0, false, false
		if c, ok := caps[project]; ok {
			capUSD, over, unl, hasCap = c.limit, c.overage, c.unlimited, true
		}
		var eff any // null when unlimited — never marshal +Inf
		if !unl {
			eff = capUSD * (1 + over/100)
		}
		byProject = append(byProject, map[string]any{
			"project": project, "committed_usd": s.committed, "reserved_usd": s.reserved,
			"cap_usd": capUSD, "cap_explicit": hasCap,
			"overage_pct": over, "unlimited": unl, "effective_cap_usd": eff,
		})
	}
	for p := range spends {
		emit(p)
	}
	for p := range caps {
		emit(p)
	}
	return byProject, committedTotal, reservedTotal
}

// spendHistory returns the last n UTC days of real ledger cost, oldest first.
func (app *App) spendHistory(n int) []any {
	rows, err := app.DB.QueryContext(state.Ctx(),
		`SELECT substr(ts,1,10) AS day, COALESCE(SUM(cost_usd),0), MAX(cost_unconfirmed), COUNT(*)
		   FROM ledger WHERE ts IS NOT NULL
		  GROUP BY day ORDER BY day DESC LIMIT ?`, n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var rev []any
	for rows.Next() {
		var day sql.NullString
		var cost sql.NullFloat64
		var unconf, cnt sql.NullInt64
		if rows.Scan(&day, &cost, &unconf, &cnt) != nil {
			continue
		}
		rev = append(rev, map[string]any{"day": day.String, "cost_usd": cost.Float64, "cost_unconfirmed": unconf.Int64 != 0, "requests": int(cnt.Int64)})
	}
	// reverse to chronological (oldest -> newest) for the chart
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

func (app *App) recentAudit(n int) []any {
	rows, err := app.DB.QueryContext(state.Ctx(), `SELECT ts, actor, action, detail FROM audit ORDER BY ts DESC LIMIT ?`, n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []any
	for rows.Next() {
		var ts, action string
		var actor, detail sql.NullString
		if rows.Scan(&ts, &actor, &action, &detail) == nil {
			out = append(out, map[string]any{"ts": ts, "actor": actor.String, "action": action, "detail": nilIfEmpty(detail.String)})
		}
	}
	return out
}

// deriveAlerts turns real state into operator alerts — no invented notifications.
func (app *App) deriveAlerts(providers []ProviderRow, spendByProject []any, staleRepos, circuitOpen int) []any {
	var alerts []any
	add := func(level, msg string) { alerts = append(alerts, map[string]any{"level": level, "message": msg}) }

	if len(providers) == 0 {
		add("warn", "No providers configured — add one to route requests.")
	}
	for _, p := range providers {
		if IsTripped(p.Name) {
			add("error", "Provider \""+p.Name+"\" circuit breaker is open (recent failures).")
		}
		if p.Enabled && p.Adapter != "mock" && p.EncKey == "" {
			add("warn", "Provider \""+p.Name+"\" is enabled but has no API key — real calls will fail.")
		}
		if p.Enabled && len(p.DisabledModels) > 0 {
			if manifest := adapters.ModelsFor(p.Name); len(manifest) > 0 {
				allDisabled := true
				for _, m := range manifest {
					if !p.DisabledModels[m] {
						allDisabled = false
						break
					}
				}
				if allDisabled {
					add("warn", "Provider \""+p.Name+"\" has all of its models disabled — it can't serve bare or auto-routed requests.")
				}
			}
		}
	}
	for _, rawp := range spendByProject {
		p := rawp.(map[string]any)
		if unl, _ := p["unlimited"].(bool); unl {
			continue // no cap to approach — cap alerts are meaningless for an uncapped project
		}
		cap, _ := p["cap_usd"].(float64)
		eff, _ := p["effective_cap_usd"].(float64)
		if eff <= 0 {
			eff = cap
		}
		committed, _ := p["committed_usd"].(float64)
		reserved, _ := p["reserved_usd"].(float64)
		used := committed + reserved
		project := asStr(p["project"])
		if op, _ := p["overage_pct"].(float64); op > 0 && cap > 0 {
			// Overage-enabled project: alert as soon as the BASE limit is crossed, independent of
			// the 80%-of-effective-ceiling threshold below — the dashboard's own budget modal
			// promises "you'll see an alert the moment you cross the base limit" (dashboard.html's
			// "Allow up to 100% over" copy), which a gate keyed on 80% of the DOUBLED effective
			// ceiling would otherwise miss entirely for the first 60 points of overage.
			switch {
			case used >= eff:
				add("error", "Project \""+project+"\" has reached its hard stop, including the allowed overage.")
			case used >= cap:
				pct := int(used / eff * 100)
				add("warn", "Project \""+project+"\" is past its base budget and running in the allowed overage ("+strconv.Itoa(pct)+"% of the hard stop).")
			}
			continue
		}
		if eff > 0 && used >= eff*0.8 {
			pct := int(used / eff * 100)
			lvl := "warn"
			if used >= eff {
				lvl = "error"
			}
			add(lvl, "Daily budget "+strconv.Itoa(pct)+"% used on project \""+project+"\".")
		}
	}
	if staleRepos > 0 {
		add("warn", strconv.Itoa(staleRepos)+" repo(s) have stale .l00prite memory (older than 30 days).")
	}
	return alerts
}

// ---- small helpers ----

func modelsOrEmpty(name string) []any {
	out := []any{}
	for _, m := range adapters.ModelsFor(name) {
		out = append(out, m)
	}
	return out
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(f sql.NullFloat64) any {
	if !f.Valid {
		return nil
	}
	return f.Float64
}

func orEmpty(a []any) []any {
	if a == nil {
		return []any{}
	}
	return a
}
