// github.go is the "Connect GitHub" control plane: three authenticated dashboard endpoints
// (connect / status / disconnect) plus GitHubAuthFor, the decrypt-at-call-time helper the push
// paths use to turn a stored credential into a gitx.PushAuth.
//
// SECURITY INVARIANTS (see also ghapi.go, gitx.Push, scaffold_pr.go):
//   - The token is verified live against GitHub BEFORE it is ever written, so a bad paste never
//     lands in the vault.
//   - It is sealed with the same AES-256-GCM vault as provider keys; git_credentials never stores
//     it in the clear.
//   - It is NEVER returned by any endpoint (status returns login/host/created_at only) and never
//     logged (audit records the login, never the token).
//   - These endpoints require a dashboard token. The run engine and chat toolboxes are in-process Go
//     code with no HTTP client, so a model can never reach connect/disconnect or read the token —
//     the same "the loop can never grant itself capability" family as the self-modification guard.
package gateway

import (
	"net/http"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// githubHost is the only host supported today. The git_credentials table is keyed (project, host)
// so a second host can be added later without a schema change.
const githubHost = "github.com"

// githubConnectReq is the POST /v1/github/connect body.
type githubConnectReq struct {
	Token string `json:"token"`
}

// githubStatus is the shape both the connect response and GET /v1/github/status return — never any
// token material.
func (app *App) githubStatus(project string) map[string]any {
	var login, createdAt string
	var verified int
	err := app.DB.QueryRowContext(state.Ctx(),
		`SELECT login, verified, created_at FROM git_credentials WHERE project=? AND host=?`,
		project, githubHost).Scan(&login, &verified, &createdAt)
	if err != nil {
		return map[string]any{"object": "l00prite.github", "connected": false, "host": githubHost}
	}
	return map[string]any{
		"object": "l00prite.github", "connected": true, "host": githubHost,
		"login": login, "verified": verified == 1, "created_at": createdAt,
	}
}

// HandleGitHubStatus is GET /v1/github/status.
func (app *App) HandleGitHubStatus(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	sendJSON(w, 200, app.githubStatus(principal.Project))
}

// HandleGitHubConnect is POST /v1/github/connect. Body: {token}. Verifies the token against GitHub,
// then vault-seals and upserts it for the acting token's project.
func (app *App) HandleGitHubConnect(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	if !config.MasterKeyPresent(app.Cfg) {
		oaiError(w, 400, "The vault is not initialized on this gateway, so a GitHub token can't be stored securely. Finish setup first.", "invalid_request_error", "vault_uninitialized")
		return
	}
	var body githubConnectReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		oaiError(w, 400, "A GitHub token is required.", "invalid_request_error", "missing_token")
		return
	}

	// Verify live BEFORE storing — a rejected token never touches the vault.
	login, err := ghVerifyToken(r.Context(), token)
	if err != nil {
		if ge, ok := err.(*ghError); ok {
			app.auditAs(principal, "github.connect_failed", ge.Raw)
			oaiError(w, 400, ge.Msg, "invalid_request_error", "github_token_rejected")
			return
		}
		oaiError(w, 502, "Could not verify the token with GitHub.", "api_error", "")
		return
	}

	enc, err := security.EncryptSecret(app.Cfg.MasterKeyPath, token)
	if err != nil {
		oaiError(w, 500, "Could not seal the token in the vault.", "api_error", "")
		return
	}
	if _, err := app.DB.ExecContext(state.Ctx(),
		`INSERT INTO git_credentials(project,host,username,enc_token,login,verified,created_at)
		 VALUES(?,?,?,?,?,1,?)
		 ON CONFLICT(project,host) DO UPDATE SET username=excluded.username, enc_token=excluded.enc_token, login=excluded.login, verified=1, created_at=excluded.created_at`,
		principal.Project, githubHost, githubUsername, enc, login, util.NowISO()); err != nil {
		oaiError(w, 500, "Could not save the GitHub connection: "+err.Error(), "api_error", "")
		return
	}
	app.auditAs(principal, "github.connect", "github.com as "+login) // login only — never the token
	sendJSON(w, 200, app.githubStatus(principal.Project))
}

// HandleGitHubDisconnect is POST /v1/github/disconnect. Deletes the stored credential for the
// acting token's project.
func (app *App) HandleGitHubDisconnect(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	if _, err := app.DB.ExecContext(state.Ctx(),
		`DELETE FROM git_credentials WHERE project=? AND host=?`, principal.Project, githubHost); err != nil {
		oaiError(w, 500, "Could not remove the GitHub connection: "+err.Error(), "api_error", "")
		return
	}
	app.auditAs(principal, "github.disconnect", githubHost)
	sendJSON(w, 200, map[string]any{"object": "l00prite.github", "connected": false, "host": githubHost})
}

// githubUsername is the HTTP basic username paired with a GitHub PAT. Any non-empty value works
// (GitHub authenticates on the token); "x-access-token" is the documented convention.
const githubUsername = "x-access-token"

// GitHubAuthFor returns a *gitx.PushAuth for project's github.com credential, decrypting the token
// AT CALL TIME (never cached), or (nil, nil) when no connection exists. Decrypting per call means a
// disconnect (or a token rotation) takes effect immediately, even mid-run. The engine reaches this
// through a closure injected at boot (see EngineCaller/runs wiring) so the engine package never
// imports security/state — the seam discipline the rest of the engine keeps.
func (app *App) GitHubAuthFor(project string) (*gitx.PushAuth, error) {
	var username, enc string
	err := app.DB.QueryRowContext(state.Ctx(),
		`SELECT username, enc_token FROM git_credentials WHERE project=? AND host=?`,
		project, githubHost).Scan(&username, &enc)
	if err != nil {
		return nil, nil // no connection is not an error — the caller falls back to ambient/none
	}
	token, derr := security.DecryptSecret(app.Cfg.MasterKeyPath, enc)
	if derr != nil {
		return nil, derr
	}
	return &gitx.PushAuth{Username: username, Token: token, Host: githubHost}, nil
}
