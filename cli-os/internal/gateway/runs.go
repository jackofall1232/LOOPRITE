// The L00prite OS run API: authenticated /v1/runs* endpoints that create a run, build/show the
// pre-flight, confirm it with Start, stream the event feed, decide approvals, and stop a run.
// Auth and project scoping are identical to every other management endpoint (a gateway bearer
// token; a run lives in the acting token's project). The confirmation gate lives in the engine —
// these handlers only carry the human's explicit Start/approve/stop decisions to it.
package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// engineReady writes a 503 and returns false when the run engine isn't wired (should not happen
// in a real server; guards tests/degraded boots).
func (app *App) engineReady(w http.ResponseWriter) bool {
	if app.Engine == nil {
		oaiError(w, 503, "The run engine is not available on this server.", "api_error", "engine_unavailable")
		return false
	}
	return true
}

// repoRootForToken resolves a repo id to its on-disk root, enforcing the acting token's project
// scope (same rule as /v1/chat/completions). Returns "" + writes an error on any failure.
func (app *App) repoRootForToken(w http.ResponseWriter, principal *security.Principal, repoID string) (string, bool) {
	repoID = strings.TrimSpace(repoID)
	if repoID == "" {
		oaiError(w, 400, "A repo id is required (register or clone a repository first).", "invalid_request_error", "")
		return "", false
	}
	if principal.Repo != "" && principal.Repo != repoID {
		oaiError(w, 403, `This token is scoped to repo "`+principal.Repo+`"; it cannot run against "`+repoID+`".`, "permission_error", "")
		return "", false
	}
	var root, project string
	if err := app.DB.QueryRowContext(state.Ctx(), `SELECT root, project FROM repos WHERE id = ?`, repoID).Scan(&root, &project); err != nil {
		oaiError(w, 404, `Repository "`+repoID+`" is not registered on this gateway. Register it in the dashboard (Repositories → Register repo) or clone it from a URL first — GitHub permissions are not involved.`, "invalid_request_error", "repo_not_found")
		return "", false
	}
	if project != principal.Project {
		oaiError(w, 403, `This token belongs to project "`+principal.Project+`"; repo "`+repoID+`" is not in it.`, "permission_error", "project_mismatch")
		return "", false
	}
	return root, true
}

// ownRun loads a run and enforces that it belongs to the acting token's project.
func (app *App) ownRun(w http.ResponseWriter, principal *security.Principal, id string) (*engine.Run, bool) {
	run, err := app.Engine.Store.GetRun(strings.TrimSpace(id))
	if err != nil {
		oaiError(w, 500, "Failed to load run: "+err.Error(), "api_error", "")
		return nil, false
	}
	if run == nil {
		oaiError(w, 404, "No such run.", "invalid_request_error", "run_not_found")
		return nil, false
	}
	if run.Project != principal.Project {
		oaiError(w, 404, "No such run.", "invalid_request_error", "run_not_found") // don't leak cross-project existence
		return nil, false
	}
	return run, true
}

type createRunReq struct {
	Repo                string            `json:"repo"`
	Goal                string            `json:"goal"`
	Objective           string            `json:"objective"`
	Gates               map[string]string `json:"gates"`
	CommandAllowlist    []string          `json:"command_allowlist"`
	MaxIterations       int               `json:"max_iterations"`
	ApprovalTimeoutSec  int               `json:"approval_timeout_s"`
	NoProgressThreshold int               `json:"no_progress_threshold"`
}

// HandleRunCreate is POST /v1/runs — create a draft run and return its first pre-flight.
func (app *App) HandleRunCreate(w http.ResponseWriter, r *http.Request) {
	if !app.engineReady(w) {
		return
	}
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body createRunReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	if strings.TrimSpace(body.Goal) == "" {
		oaiError(w, 400, "A goal is required (describe what you want built).", "invalid_request_error", "")
		return
	}
	root, ok := app.repoRootForToken(w, principal, body.Repo)
	if !ok {
		return
	}
	cfg := engine.RunConfig{
		RepoID: strings.TrimSpace(body.Repo), Goal: strings.TrimSpace(body.Goal),
		Objective: strings.TrimSpace(body.Objective), Gates: body.Gates,
		CommandAllowlist: body.CommandAllowlist, MaxIterations: body.MaxIterations,
		ApprovalTimeoutSec: body.ApprovalTimeoutSec, NoProgressThreshold: body.NoProgressThreshold,
	}
	run, pf, err := app.createDraftRun(principal.Project, root, cfg)
	if run == nil {
		oaiError(w, 400, "Could not create run: "+err.Error(), "invalid_request_error", "")
		return
	}
	app.auditAs(principal, "run.create", run.ID)
	if err != nil {
		oaiError(w, 500, "Run created but pre-flight failed: "+err.Error(), "api_error", "")
		return
	}
	sendJSON(w, 200, map[string]any{"run": runView(run), "preflight": pf})
}

// createDraftRun is the single code path that drafts a run and builds its first pre-flight. Both
// POST /v1/runs (HandleRunCreate above) and the Playground's propose_run chat tool
// (chatrun.go) call it, so the two surfaces can never drift. It NEVER starts anything: Start is a
// separate, human-confirmed endpoint (HandleRunStart), and neither this helper nor the chat path
// may call engine.StartRun. Caller must ensure app.Engine != nil.
func (app *App) createDraftRun(project, repoRoot string, cfg engine.RunConfig) (*engine.Run, *engine.Preflight, error) {
	// Project Auto-PR setting (internal/gateway/autopr.go): fills push/pr_create with
	// auto_approve when ON, but only for keys the caller didn't already set explicitly. Must run
	// before Store.CreateRun so the merged map is what gets validated, persisted, frozen by the
	// self-modification guard, and rendered in the pre-flight the human confirms with EXECUTE.
	cfg.Gates = app.applyAutoPRGates(project, cfg.Gates)
	run, err := app.Engine.Store.CreateRun(project, repoRoot, cfg)
	if err != nil {
		return nil, nil, err
	}
	pf, err := app.Engine.BuildPreflight(run)
	if err != nil {
		return run, nil, err // run exists but its pre-flight failed — caller decides how to surface
	}
	return run, pf, nil
}

// HandleRunPreflight is POST /v1/runs/preflight — rebuild the pre-flight (recovery/migration/lock
// check included). Required again before any Start; a stale pre-flight can never be confirmed.
func (app *App) HandleRunPreflight(w http.ResponseWriter, r *http.Request) {
	if !app.engineReady(w) {
		return
	}
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	run, ok := app.ownRun(w, principal, body.ID)
	if !ok {
		return
	}
	if run.Status == engine.StatusRunning || run.Status == engine.StatusWaitingApproval {
		oaiError(w, 409, "This run is active; stop it before rebuilding its pre-flight.", "invalid_request_error", "run_active")
		return
	}
	pf, err := app.Engine.BuildPreflight(run)
	if err != nil {
		oaiError(w, 500, "Pre-flight failed: "+err.Error(), "api_error", "")
		return
	}
	sendJSON(w, 200, map[string]any{"run": runView(run), "preflight": pf})
}

// HandleRunStart is POST /v1/runs/start — the confirmation gate. confirm must be "EXECUTE" against
// a fresh (ready) pre-flight. This is the explicit in-session human action; nothing persisted arms
// a run.
func (app *App) HandleRunStart(w http.ResponseWriter, r *http.Request) {
	if !app.engineReady(w) {
		return
	}
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body struct {
		ID      string `json:"id"`
		Confirm string `json:"confirm"`
	}
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	run, ok := app.ownRun(w, principal, body.ID)
	if !ok {
		return
	}
	confirmedBy := principal.TokenID
	if err := app.Engine.StartRun(r.Context(), run.ID, confirmedBy, body.Confirm); err != nil {
		status, code := 400, "start_rejected"
		switch {
		case errors.Is(err, engine.ErrCheckpointRefused):
			status, code = 409, "checkpoint_refused"
		case errors.Is(err, engine.ErrCheckpointFailed):
			status, code = 409, "checkpoint_failed"
		case errors.Is(err, engine.ErrBadState):
			status, code = 409, "run_not_ready"
		}
		// The full technical error (which can include raw git/go-git text, e.g. from a failed
		// checkout) goes to the audit log only — never to the client. See humanizeStartError.
		app.auditAs(principal, "run.start.error", err.Error())
		oaiError(w, status, humanizeStartError(err), "invalid_request_error", code)
		return
	}
	app.auditAs(principal, "run.start", run.ID)
	updated, _ := app.Engine.Store.GetRun(run.ID)
	sendJSON(w, 200, map[string]any{"run": runView(updated), "started": true})
}

// humanizeStartError translates a StartRun failure into a plain-English message safe to show a
// non-technical user. It deliberately has a generic fallback for any error class it does not
// specifically recognize, so an unanticipated git/go-git failure (a corrupted repo, a mid-rebase
// state, a permissions error) can never leak raw technical text to the dashboard the way a raw
// "worktree contains unstaged changes" checkout error once did. The full error is always logged
// separately by the caller via auditAs before this function is called.
func humanizeStartError(err error) string {
	switch {
	case errors.Is(err, engine.ErrCheckpointRefused):
		return "Some of your unsaved changes look sensitive (they match your project's Denylist, or look like they might contain a credential or key), so l00prite won't save them automatically. Review and commit or discard them yourself, then try Start again."
	case errors.Is(err, engine.ErrCheckpointFailed):
		return "l00prite tried to save your unsaved changes before starting, but the save failed. Nothing was changed and the run was not started. Details were logged for support."
	case errors.Is(err, engine.ErrBadState):
		return `This run can't start right now — the project isn't in a startable state. Try "Rebuild pre-flight"; if that doesn't help, details were logged for support.`
	default:
		return "Something went wrong preparing the project for this run. Nothing was started. Details were logged for support."
	}
}

// HandleRunGet is GET /v1/runs/get?id= — run detail + pending approvals + latest pre-flight.
func (app *App) HandleRunGet(w http.ResponseWriter, r *http.Request) {
	if !app.engineReady(w) {
		return
	}
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	run, ok := app.ownRun(w, principal, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	pending, _ := app.Engine.Store.PendingApprovals(run.ID)
	sendJSON(w, 200, map[string]any{"run": runView(run), "preflight": preflightRaw(run), "pending_approvals": pending})
}

// HandleRunList is GET /v1/runs/list — the token's project's runs, newest first.
func (app *App) HandleRunList(w http.ResponseWriter, r *http.Request) {
	if !app.engineReady(w) {
		return
	}
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	runs, err := app.Engine.Store.ListRuns(principal.Project, 50)
	if err != nil {
		oaiError(w, 500, "Failed to list runs: "+err.Error(), "api_error", "")
		return
	}
	// A repo-scoped token (principal.Repo != "") must only ever see its own repo's runs -- run
	// creation and chat already enforce this scope (repoRootForToken), but this list endpoint used
	// to return every run in the whole project regardless, leaking other repos' run ids, goals,
	// statuses, and costs to a token that was never granted access to them (Codex review, PR #7).
	// Note: the 50-run cap is applied by ListRuns BEFORE this filter, so a repo-scoped token in a
	// busy multi-repo project could see fewer than 50 of its own runs if others crowd the page --
	// an under-return, not an over-exposure; a repo-filtered query would need a new Store method.
	views := make([]any, 0, len(runs))
	for _, run := range runs {
		if principal.Repo != "" && run.Config.RepoID != principal.Repo {
			continue
		}
		views = append(views, runView(run))
	}
	sendJSON(w, 200, map[string]any{"runs": views})
}

// HandleRunEvents is GET /v1/runs/events?id=&after= — the append-only feed the dashboard polls.
func (app *App) HandleRunEvents(w http.ResponseWriter, r *http.Request) {
	if !app.engineReady(w) {
		return
	}
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	run, ok := app.ownRun(w, principal, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	events, err := app.Engine.Store.ListEvents(run.ID, after, 500)
	if err != nil {
		oaiError(w, 500, "Failed to read run events: "+err.Error(), "api_error", "")
		return
	}
	var cursor int64 = after
	if len(events) > 0 {
		cursor = events[len(events)-1].Seq
	}
	sendJSON(w, 200, map[string]any{
		"run": runView(run), "events": events, "cursor": cursor,
	})
}

// HandleRunApprove is POST /v1/runs/approve — a per-action permission decision.
func (app *App) HandleRunApprove(w http.ResponseWriter, r *http.Request) {
	if !app.engineReady(w) {
		return
	}
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body struct {
		ID         string `json:"id"`
		ApprovalID string `json:"approval_id"`
		Decision   string `json:"decision"`
		Note       string `json:"note"`
	}
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	run, ok := app.ownRun(w, principal, body.ID)
	if !ok {
		return
	}
	if body.Decision != "allow" && body.Decision != "deny" {
		oaiError(w, 400, `decision must be "allow" or "deny".`, "invalid_request_error", "")
		return
	}
	if err := app.Engine.Decide(run.ID, strings.TrimSpace(body.ApprovalID), body.Decision, principal.TokenID, body.Note); err != nil {
		status, code := 400, "approval_failed"
		if errors.Is(err, engine.ErrAlreadyDecided) {
			status, code = 409, "already_decided"
		}
		oaiError(w, status, "Could not record decision: "+err.Error(), "invalid_request_error", code)
		return
	}
	app.auditAs(principal, "run.approve."+body.Decision, run.ID+"/"+body.ApprovalID)
	sendJSON(w, 200, map[string]any{"decided": true, "decision": body.Decision})
}

// HandleRunStop is POST /v1/runs/stop — the stop_signal boundary.
func (app *App) HandleRunStop(w http.ResponseWriter, r *http.Request) {
	if !app.engineReady(w) {
		return
	}
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	run, ok := app.ownRun(w, principal, body.ID)
	if !ok {
		return
	}
	if err := app.Engine.Stop(run.ID); err != nil {
		oaiError(w, 409, "Run is not active: "+err.Error(), "invalid_request_error", "run_not_active")
		return
	}
	app.auditAs(principal, "run.stop", run.ID)
	sendJSON(w, 200, map[string]any{"stopping": true})
}

// runView is the safe run projection for the API (no internal-only fields).
func runView(run *engine.Run) map[string]any {
	if run == nil {
		return nil
	}
	return map[string]any{
		"id": run.ID, "project": run.Project, "repo": run.Config.RepoID,
		"goal": run.Config.Goal, "objective": run.Config.Objective,
		"status": run.Status, "boundary": run.Boundary,
		"current_iteration": run.CurrentIteration, "max_iterations": run.Config.MaxIterations,
		"iterations_since_progress": run.IterationsSinceProgress,
		"branch":                    run.Branch, "cost_usd": run.CostUSD,
		"confirmed_by": run.ConfirmedBy, "confirmed_at": run.ConfirmedAt,
		"created_at": run.CreatedAt, "started_at": run.StartedAt, "ended_at": run.EndedAt,
		"summary": run.Summary, "gates": run.Config.Gates, "command_allowlist": run.Config.CommandAllowlist,
	}
}

// preflightRaw returns the stored pre-flight as a parsed object (or nil).
func preflightRaw(run *engine.Run) any {
	if run.PreflightJSON == "" {
		return nil
	}
	var pf engine.Preflight
	if err := json.Unmarshal([]byte(run.PreflightJSON), &pf); err != nil {
		return nil
	}
	return pf
}
