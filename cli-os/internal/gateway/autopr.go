// Per-project Auto-PR toggle. OFF by default for every project (existing and new — a missing
// project_settings row reads as auto_pr=0, see autoPREnabled). When ON, a run created for that
// project has its push and gh-pr-create actions (engine.GatePush / engine.GatePRCreate — the
// only two classes engine.GateClassAutoApprovable ever allows) pre-filled to auto_approve in the
// run's Gates map at creation time, so the human reviews the exact resolved gate table in the
// pre-flight display before ever typing EXECUTE. Merge is never affected — there is no toggle,
// setting, or code path anywhere that can auto-approve a merge. Mirrors budget.go's structure:
// requireToken principal, scope = principal.Project only, a shared GET/POST view, auditAs on
// write.
package gateway

import (
	"fmt"
	"net/http"

	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// autoPRSetReq is the POST /v1/auto-pr body.
type autoPRSetReq struct {
	Enabled bool `json:"enabled"`
}

// autoPRView is the shared GET/POST response body.
func (app *App) autoPRView(project string) map[string]any {
	return map[string]any{
		"object": "l00prite.auto_pr", "project": project, "enabled": app.autoPREnabled(project),
	}
}

// HandleAutoPRGet is GET /v1/auto-pr.
func (app *App) HandleAutoPRGet(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	sendJSON(w, 200, app.autoPRView(principal.Project))
}

// HandleAutoPRSet is POST /v1/auto-pr. Body: {"enabled": bool}.
func (app *App) HandleAutoPRSet(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body autoPRSetReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	val := 0
	if body.Enabled {
		val = 1
	}
	if _, err := app.DB.ExecContext(state.Ctx(),
		`INSERT INTO project_settings(project,auto_pr) VALUES(?,?)
		 ON CONFLICT(project) DO UPDATE SET auto_pr=excluded.auto_pr`,
		principal.Project, val); err != nil {
		oaiError(w, 500, "Could not save the Auto-PR setting: "+err.Error(), "api_error", "")
		return
	}
	app.auditAs(principal, "auto_pr.set", fmt.Sprintf("%s enabled=%v", principal.Project, body.Enabled))
	sendJSON(w, 200, app.autoPRView(principal.Project))
}

// autoPREnabled reads the project's Auto-PR setting. A missing row or a query error both read as
// OFF (fail-closed) -- so a fresh project, a project that never touched this setting, and a
// transient DB hiccup all behave identically to the toggle never having existed.
func (app *App) autoPREnabled(project string) bool {
	var v int
	err := app.DB.QueryRowContext(state.Ctx(), `SELECT auto_pr FROM project_settings WHERE project = ?`, project).Scan(&v)
	return err == nil && v == 1
}

// applyAutoPRGates fills the push and pr_create gates with auto_approve when the project's
// Auto-PR setting is ON -- but NEVER overwrites an entry the caller already supplied explicitly
// (a human who typed a stricter require_approval or deny into a specific run must never be
// silently loosened by a project-level setting), and NEVER touches any other class. The result
// is what gets validated and persisted by engine.Store.CreateRun and therefore rendered verbatim
// in the pre-flight display the human reviews before EXECUTE: there is no invisible or silent
// auto-approval. Called from createDraftRun, the single code path both POST /v1/runs and the
// Playground's propose_run chat tool go through, so both surfaces inherit this identically.
func (app *App) applyAutoPRGates(project string, gates map[string]string) map[string]string {
	if !app.autoPREnabled(project) {
		return gates
	}
	// Copy rather than mutate the caller's map in place (PR review, gemini-code-assist): maps are
	// reference types, and nothing here guarantees the caller's map is this call's to own -- a
	// future caller that reuses/shares a literal across requests would otherwise see one request's
	// fill leak into another's. No current caller does that (createDraftRun's cfg.Gates is always
	// freshly decoded per request), but the copy is cheap and removes the aliasing hazard outright.
	copied := make(map[string]string, len(gates)+2)
	for k, v := range gates {
		copied[k] = v
	}
	for _, cls := range []string{engine.GatePush, engine.GatePRCreate} {
		if _, explicit := copied[cls]; !explicit {
			copied[cls] = engine.PolicyAutoApprove
		}
	}
	return copied
}
