// Device-owner ("workspace") sessions. The Android install secret (LOOPRITE_SETUP_SECRET, held
// only in the app-private storage of one install) doubles as the DEVICE-OWNER credential: the
// endpoints here let that one install list its workspaces (projects with active tokens), switch
// the dashboard session between them, and create new ones — so signing out (or the 8h UI-cookie
// expiring) is never a lockout, and several teams/workspaces can share one box.
//
// SECURITY:
//   - These routes are NOT under setupGate: they must stay open after the setup latch — that is
//     the entire point (post-latch recovery). They carry their own gate instead.
//   - The gate is the install secret itself (x-l00prite-setup-secret header, timing-safe) or a
//     valid setup cookie (which only that same secret can mint). When no install secret is
//     configured (desktop gateways), every endpoint here fails CLOSED with 403: nothing about
//     the desktop auth model changes, and `l00prite token mint` remains the recovery path.
//   - No project name is ever accepted as authentication: a name only SELECTS which existing
//     token's principal the session is issued for, after the owner credential has been proven.
//     (A bare "type a project name to sign in" endpoint would be an unauthenticated session
//     oracle to anything that can reach loopback — e.g. any app on the phone.)
//   - Token material is returned exactly once, only by POST /v1/owner/projects (mirroring
//     `token mint`'s shown-once rule). GET endpoints never return token ids' secret halves —
//     ids are public by design (see security/tokens.go).
package gateway

import (
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// ownerProjectRE bounds workspace names to the same safe alphabet as token projects/repo ids,
// with a length cap so a name can never become an injection or layout vector.
var ownerProjectRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

// requireOwner enforces the device-owner gate described in the package comment above. Returns
// true when the request may proceed.
func (app *App) requireOwner(w http.ResponseWriter, r *http.Request) bool {
	secret := os.Getenv("LOOPRITE_SETUP_SECRET")
	if secret == "" {
		// Desktop gateway: no install secret exists, so there is no device-owner credential to
		// check — fail closed rather than falling back to anything weaker.
		sendJSON(w, 403, map[string]any{"error": map[string]any{
			"message": "Device-owner endpoints require an install secret (app installs only). Use `l00prite token mint` on this gateway instead.",
			"type":    "permission_error", "code": "owner_unavailable"}})
		return false
	}
	if got := r.Header.Get("x-l00prite-setup-secret"); got != "" && util.TimingSafeEqual(got, secret) {
		return true
	}
	if app.validSetupSession(r) {
		return true
	}
	sendJSON(w, 403, map[string]any{"error": map[string]any{
		"message": "Missing or expired device-owner credential.",
		"type":    "authentication_error", "code": "owner_required"}})
	return false
}

// ownerTokenForProject resolves the principal for the newest active (unrevoked, unexpired)
// token of a project, or of any project when project is "". Returns nil when none matches.
// Repo-scoped tokens are excluded: a workspace session must not silently narrow to one repo.
func (app *App) ownerTokenForProject(project string) *security.Principal {
	query := `SELECT id, project, scopes, legacy, role FROM tokens
	          WHERE revoked = 0 AND (repo IS NULL OR repo = '')
	            AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)`
	args := []any{time.Now().UTC().Format(time.RFC3339Nano)}
	if project != "" {
		query += ` AND project = ?`
		args = append(args, project)
	}
	query += ` ORDER BY created_at DESC LIMIT 1`
	var (
		id, proj, scopesRaw, role string
		legacy                    int
	)
	if err := app.DB.QueryRowContext(state.Ctx(), query, args...).Scan(&id, &proj, &scopesRaw, &legacy, &role); err != nil {
		return nil
	}
	scopes, ok := security.ParseScopes(scopesRaw)
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, s := range scopes {
		set[s] = true
	}
	return &security.Principal{TokenID: id, Project: proj, Scopes: scopes, ScopeSet: set, Legacy: legacy != 0, Role: role}
}

// ownerProjects lists distinct projects that have at least one active unscoped token, newest
// activity first. Names and counts only — never token material.
func (app *App) ownerProjects() []map[string]any {
	rows, err := app.DB.QueryContext(state.Ctx(),
		`SELECT project, COUNT(*) AS n, MAX(created_at) AS latest FROM tokens
		 WHERE revoked = 0 AND (repo IS NULL OR repo = '')
		   AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
		 GROUP BY project ORDER BY latest DESC`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			project, latest string
			n               int
		)
		if err := rows.Scan(&project, &n, &latest); err != nil {
			continue
		}
		out = append(out, map[string]any{"project": project, "tokens": n, "created_latest": latest})
	}
	return out
}

// HandleOwnerProjects is GET /v1/owner/projects — the sign-in card's "This device" list.
func (app *App) HandleOwnerProjects(w http.ResponseWriter, r *http.Request) {
	if !app.requireOwner(w, r) {
		return
	}
	sendJSON(w, 200, map[string]any{"object": "l00prite.owner_projects", "projects": app.ownerProjects()})
}

// HandleOwnerSession is POST /v1/owner/session — switch (or restore) the dashboard session to a
// workspace. Body {"project": "name"}; omitted project picks the newest active token overall.
func (app *App) HandleOwnerSession(w http.ResponseWriter, r *http.Request) {
	if !app.requireOwner(w, r) {
		return
	}
	var body struct {
		Project string `json:"project"`
	}
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	p := app.ownerTokenForProject(body.Project)
	if p == nil {
		sendJSON(w, 404, map[string]any{"error": map[string]any{
			"message": "No active workspace" + func() string {
				if body.Project != "" {
					return ` named "` + body.Project + `"`
				}
				return ""
			}() + " on this install.",
			"type": "invalid_request_error", "code": "unknown_project"},
			"projects": app.ownerProjects()})
		return
	}
	token, err := app.issueUISession(p)
	if err != nil {
		oaiError(w, 500, "Could not create UI session", "api_error", "")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: uiSessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(uiSessionTTL.Seconds())})
	setupAudit(app, "owner.session", "project "+p.Project)
	sendJSON(w, 200, map[string]any{"ok": true, "project": p.Project, "expires_in_seconds": int(uiSessionTTL.Seconds())})
}

// HandleOwnerProjectCreate is POST /v1/owner/projects — the "new workspace" flow: mint an owner
// token for a NEW project name and sign the dashboard into it. The raw token is returned once
// (shown-once rule, for use from the CLI or another device) and never stored client-side by us.
func (app *App) HandleOwnerProjectCreate(w http.ResponseWriter, r *http.Request) {
	if !app.requireOwner(w, r) {
		return
	}
	var body struct {
		Project string `json:"project"`
	}
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	project := body.Project
	if !ownerProjectRE.MatchString(project) {
		oaiError(w, 400, "A workspace name must be 1-40 chars of letters, digits, '.', '-', '_'.", "invalid_request_error", "bad_project")
		return
	}
	if p := app.ownerTokenForProject(project); p != nil {
		sendJSON(w, 409, map[string]any{"error": map[string]any{
			"message": `Workspace "` + project + `" already exists — pick it from the list instead.`,
			"type":    "invalid_request_error", "code": "project_exists"}})
		return
	}
	id, raw, err := security.MintToken(app.DB, project, nil, nil)
	if err != nil {
		oaiError(w, 500, "Could not mint the workspace token: "+err.Error(), "api_error", "")
		return
	}
	p := app.ownerTokenForProject(project)
	if p == nil {
		oaiError(w, 500, "Workspace token minted but not resolvable", "api_error", "")
		return
	}
	cookie, err := app.issueUISession(p)
	if err != nil {
		oaiError(w, 500, "Could not create UI session", "api_error", "")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: uiSessionCookie, Value: cookie, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(uiSessionTTL.Seconds())})
	setupAudit(app, "owner.project.create", "project "+project)
	sendJSON(w, 200, map[string]any{
		"object": "l00prite.owner_project", "project": project,
		"token": map[string]any{"id": id, "token": raw},
		"note":  "This token is shown once — store it now if you want CLI or another-device access to this workspace.",
	})
}
