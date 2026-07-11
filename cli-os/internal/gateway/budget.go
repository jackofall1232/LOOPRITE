// Per-project daily budget management for the dashboard's "Set budget" modal. GET returns the
// acting token's project budget + today's spend; POST upserts it. Scope is principal.Project
// ONLY — the same trust boundary as /v1/repos and /v1/providers (no cross-project admin here).
package gateway

import (
	"fmt"
	"math"
	"net/http"

	pep "github.com/jackofall1232/l00prite/cli-os/internal/policy"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// budgetSetReq is the POST /v1/budget body.
type budgetSetReq struct {
	DailyUSD   *float64 `json:"daily_usd"`
	OveragePct *float64 `json:"overage_pct"`
	Unlimited  bool     `json:"unlimited"`
}

// budgetView is the shared GET/POST response body.
func (app *App) budgetView(project string) map[string]any {
	s := pep.GetSpend(app.DB, project, app.Cfg.DefaultDailyCapUsd)
	var eff any // null when unlimited — NEVER marshal +Inf (json.Marshal errors on it)
	if !s.Unlimited {
		eff = s.EffectiveCap
	}
	return map[string]any{
		"object": "l00prite.budget", "project": project, "day": s.Day,
		"daily_usd": s.Cap, "overage_pct": s.OveragePct, "unlimited": s.Unlimited,
		"explicit": s.Explicit, "effective_cap_usd": eff,
		"spent_today_usd": s.Committed, "reserved_today_usd": s.Reserved,
		"default_daily_cap_usd": app.Cfg.DefaultDailyCapUsd,
	}
}

// HandleBudgetGet is GET /v1/budget.
func (app *App) HandleBudgetGet(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	sendJSON(w, 200, app.budgetView(principal.Project))
}

// HandleBudgetSet is POST /v1/budget. Body: {daily_usd, overage_pct, unlimited}.
func (app *App) HandleBudgetSet(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body budgetSetReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	overage := 0.0
	if body.OveragePct != nil {
		overage = *body.OveragePct
	}
	if math.IsNaN(overage) || math.IsInf(overage, 0) || overage < 0 || overage > 1000 {
		oaiError(w, 400, "overage_pct must be between 0 and 1000.", "invalid_request_error", "invalid_overage_pct")
		return
	}
	if body.Unlimited {
		overage = 0 // an uncapped project has no meaningful overage
	}
	daily, hasDaily := 0.0, false
	if body.DailyUSD != nil {
		daily, hasDaily = *body.DailyUSD, true
	}
	if hasDaily && (math.IsNaN(daily) || math.IsInf(daily, 0) || daily <= 0 || daily > 1e9) {
		oaiError(w, 400, "daily_usd must be a positive dollar amount.", "invalid_request_error", "invalid_daily_usd")
		return
	}
	if !hasDaily {
		if !body.Unlimited {
			oaiError(w, 400, "daily_usd is required unless unlimited is true.", "invalid_request_error", "invalid_daily_usd")
			return
		}
		// Unlimited with no amount: keep the existing limit for display continuity, else the default.
		daily = pep.GetSpend(app.DB, principal.Project, app.Cfg.DefaultDailyCapUsd).Cap
	}
	unl := 0
	if body.Unlimited {
		unl = 1
	}
	if _, err := app.DB.ExecContext(state.Ctx(),
		`INSERT INTO caps(project,window,limit_usd,overage_pct,unlimited) VALUES(?,'daily',?,?,?)
		 ON CONFLICT(project,window) DO UPDATE SET limit_usd=excluded.limit_usd, overage_pct=excluded.overage_pct, unlimited=excluded.unlimited`,
		principal.Project, daily, overage, unl); err != nil {
		oaiError(w, 500, "Could not save the budget: "+err.Error(), "api_error", "")
		return
	}
	app.auditAs(principal, "budget.set", fmt.Sprintf("%s daily=$%.2f overage=%.0f%% unlimited=%v", principal.Project, daily, overage, body.Unlimited))
	sendJSON(w, 200, app.budgetView(principal.Project))
}
