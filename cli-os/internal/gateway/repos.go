// Post-setup repository management. Registering a repo used to be CLI-only (`repo register`);
// these AUTHENTICATED endpoints expose the same primitive to the dashboard so a new user can
// connect a repository — and get its .l00prite memory injected — without ever leaving the browser.
//
// SECURITY:
//   - Same single-tier auth as every other management/data endpoint: a gateway Bearer token.
//   - The root is a path on the MACHINE RUNNING THE GATEWAY. It is resolved to an absolute path
//     and must exist as a directory before the row is written, so a typo fails here with a clear
//     message instead of surfacing later as a silent "no memory" on every request.
//   - Registration stores a path + project mapping only; nothing inside the repo is read here
//     beyond an os.Stat freshness snapshot (memory content stays untrusted input at request time).
//   - A repo can only be registered into the ACTING token's own project (explicit mismatch → 403),
//     so this endpoint can never be used to re-home a directory across the request-time
//     project-scope gate; cross-project registration remains a CLI (gateway-host) operation.
//   - Every register/remove is audit-logged with the acting token id.
package gateway

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/memory"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// validRepoID keeps repo ids to a charset that is safe in the x-l00prite-repo header, token repo
// scoping, logs, and file names — an id can never smuggle separators or control characters.
var validRepoID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type repoRegisterReq struct {
	ID      string `json:"id"`
	Root    string `json:"root"`
	Project string `json:"project"`
}

// HandleRepoRegister is POST /v1/repos — register a repository through the same row shape as the
// CLI's `repo register`. Unlike the CLI (INSERT OR REPLACE), an existing id is rejected with 409 so
// a browser form can never silently repoint a repo another token is already using; remove it first.
func (app *App) HandleRepoRegister(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body repoRegisterReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		oaiError(w, 400, "A repo id is required (a short name like \"myrepo\").", "invalid_request_error", "")
		return
	}
	if !validRepoID.MatchString(id) {
		oaiError(w, 400, `Repo id must match [A-Za-z0-9._-]+ (letters, digits, ".", "-" or "_").`, "invalid_request_error", "")
		return
	}
	root := strings.TrimSpace(body.Root)
	if root == "" {
		oaiError(w, 400, "A repo root path is required — the absolute path of the repository on the machine running this gateway.", "invalid_request_error", "")
		return
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		oaiError(w, 400, "Could not resolve the repo root path: "+err.Error(), "invalid_request_error", "")
		return
	}
	if st, err := os.Stat(absRoot); err != nil || !st.IsDir() {
		oaiError(w, 400, `"`+absRoot+`" is not a directory on the machine running this gateway. The root must be a local path on the gateway host — a git URL or a path from another machine won't work.`, "invalid_request_error", "repo_root_not_found")
		return
	}
	// Project scoping: the repo lands in the ACTING TOKEN's project (which is also what makes it
	// usable by that token — /v1/chat/completions rejects a repo whose project differs from the
	// token's). An explicit different project is refused: otherwise any token could re-home a
	// directory into its own project (or park one in another project) and sidestep that gate.
	// Cross-project registration stays a CLI (gateway-host) operation.
	project := strings.TrimSpace(body.Project)
	if project == "" {
		project = principal.Project
	}
	if project != principal.Project {
		oaiError(w, 403, `This token belongs to project "`+principal.Project+`", so it can only register repos into that project. Use the CLI on the gateway host to register a repo under a different project.`, "permission_error", "project_mismatch")
		return
	}
	// Duplicate check + insert as ONE transaction so two concurrent registers of the same id
	// can't both pass the pre-check — the loser sees the row and gets the same 409 as a plain
	// duplicate, never a 500 from the PRIMARY KEY constraint.
	errRepoExists := errors.New("repo_exists")
	if _, err := state.Tx(app.DB, func(q state.Querier) (any, error) {
		var existing int
		if err := q.QueryRowContext(state.Ctx(), `SELECT COUNT(*) FROM repos WHERE id = ?`, id).Scan(&existing); err != nil {
			return nil, err
		}
		if existing > 0 {
			return nil, errRepoExists
		}
		_, err := q.ExecContext(state.Ctx(), `INSERT INTO repos(id,root,project,created_at) VALUES(?,?,?,?)`,
			id, absRoot, project, util.NowISO())
		return nil, err
	}); err != nil {
		if errors.Is(err, errRepoExists) || strings.Contains(err.Error(), "UNIQUE constraint failed") {
			oaiError(w, 409, `A repo named "`+id+`" is already registered. Remove it first to point the id somewhere else.`, "invalid_request_error", "repo_exists")
			return
		}
		oaiError(w, 500, "Failed to register repo: "+err.Error(), "configuration_error", "")
		return
	}
	app.auditAs(principal, "repo.register", id)
	// A real freshness snapshot so the UI can immediately say whether .l00prite memory was found —
	// registering a repo with no memory folder yet is fine, but it shouldn't look like success at
	// injecting memory.
	fr := memory.RepoFreshness(absRoot)
	sendJSON(w, 200, map[string]any{
		"repo": map[string]any{"id": id, "root": absRoot, "project": project},
		"memory": map[string]any{
			"status": fr.Status, "present_count": fr.PresentCount,
			"stale_count": fr.StaleCount, "total_files": fr.TotalFiles,
		},
	})
}

type repoRemoveReq struct {
	ID string `json:"id"`
}

// HandleRepoRemove is POST /v1/repos/remove — unregister a repository. This only deletes the
// id→path mapping (nothing on disk is touched), so it needs no type-to-confirm gate; the response
// reports how many tokens were scoped to the repo so the UI can surface the consequence.
func (app *App) HandleRepoRemove(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body repoRemoveReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		oaiError(w, 400, "A repo id is required.", "invalid_request_error", "")
		return
	}
	// A DB error here must not masquerade as "repo not found" (or as "0 scoped tokens") — report
	// it as the server fault it is.
	var existing int
	if err := app.DB.QueryRowContext(state.Ctx(), `SELECT COUNT(*) FROM repos WHERE id = ?`, id).Scan(&existing); err != nil {
		oaiError(w, 500, "Database error: "+err.Error(), "api_error", "")
		return
	}
	if existing == 0 {
		oaiError(w, 404, `No repo named "`+id+`".`, "invalid_request_error", "repo_not_found")
		return
	}
	var scopedTokens int
	if err := app.DB.QueryRowContext(state.Ctx(), `SELECT COUNT(*) FROM tokens WHERE repo = ? AND revoked = 0`, id).Scan(&scopedTokens); err != nil {
		oaiError(w, 500, "Database error: "+err.Error(), "api_error", "")
		return
	}
	if _, err := app.DB.ExecContext(state.Ctx(), `DELETE FROM repos WHERE id = ?`, id); err != nil {
		oaiError(w, 500, "Failed to remove repo: "+err.Error(), "configuration_error", "")
		return
	}
	app.auditAs(principal, "repo.remove", id)
	sendJSON(w, 200, map[string]any{"removed": true, "id": id, "tokens_scoped": scopedTokens})
}
