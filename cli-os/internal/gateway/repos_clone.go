// Clone-from-GitHub repo connect. The OS's "connect a local repo or clone from GitHub" flow:
// POST /v1/repos/clone shallow-clones a Git URL into a managed workspace directory under the
// gateway's data home, then registers it exactly like a local-path repo. This is an explicit,
// authenticated, human-initiated action from the dashboard — never autonomous.
//
// SECURITY:
//   - The clone destination is ALWAYS inside <data home>/workspaces/<repo id>, never a
//     caller-supplied path, so a clone can't write outside the managed area.
//   - Only https:// and ssh (git@host:...) URLs are accepted; file://, ext::, and other
//     git transports that can execute local commands are rejected. An https URL carrying
//     embedded userinfo (a credential) is also rejected — it would otherwise be echoed back
//     in this endpoint's response.
//   - Same project-scope rule as /v1/repos: the repo lands in the acting token's project.
//   - Cloning goes through the gitx seam (internal/gitx) rather than shelling out directly here:
//     the exec implementation runs git fully non-interactively exactly as before
//     (GIT_TERMINAL_PROMPT=0 disables git's own https credential prompt, GIT_SSH_COMMAND forces
//     ssh into BatchMode with a short connect timeout, so an unreachable or credential-requiring
//     URL fails fast instead of hanging on a TTY prompt nothing here can ever answer), and the
//     pure-Go go-git fallback (Android — no git/ssh binary; see docs/android-architecture.md §4
//     G4) covers the https case and cleanly rejects ssh URLs instead of this endpoint 500ing for
//     want of a git binary.
package gateway

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
	"github.com/jackofall1232/l00prite/cli-os/internal/memory"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// allowedGitURL accepts https URLs and scp-style ssh (git@host:owner/repo). Everything else —
// file://, ext::, --upload-pack injection, bare local paths — is refused.
var httpsGitURL = regexp.MustCompile(`^https://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+$`)
var sshGitURL = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:[A-Za-z0-9._~/-]+$`)

func acceptableGitURL(u string) bool {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "-") { // never let the URL look like a git flag
		return false
	}
	if !httpsGitURL.MatchString(u) && !sshGitURL.MatchString(u) {
		return false
	}
	if strings.HasPrefix(u, "https://") {
		// Reject a credential embedded in the URL (https://user:token@host/...): the URL is
		// echoed back verbatim in this endpoint's response ("cloned_from") and could otherwise
		// leak a token into client-side logs or storage. Private repos should use SSH (already
		// supported below) or a credential helper configured on the gateway host.
		parsed, err := url.Parse(u)
		if err != nil || parsed.User != nil {
			return false
		}
	}
	return true
}

type repoCloneReq struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// HandleRepoClone is POST /v1/repos/clone — clone a Git URL into the managed workspace and register it.
func (app *App) HandleRepoClone(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body repoCloneReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" || !validRepoID.MatchString(id) {
		oaiError(w, 400, `A repo id is required and must match [A-Za-z0-9._-]+.`, "invalid_request_error", "")
		return
	}
	url := strings.TrimSpace(body.URL)
	if !acceptableGitURL(url) {
		oaiError(w, 400, "Provide an https:// or git@host:owner/repo URL. Other git transports are not accepted.", "invalid_request_error", "bad_url")
		return
	}

	// Refuse a duplicate id up front (the register step would 409 anyway, but we don't want to
	// clone into a dir for an id that's taken).
	var existing int
	if err := app.DB.QueryRowContext(state.Ctx(), `SELECT COUNT(*) FROM repos WHERE id = ?`, id).Scan(&existing); err != nil {
		oaiError(w, 500, "Database error: "+err.Error(), "api_error", "")
		return
	}
	if existing > 0 {
		oaiError(w, 409, `A repo named "`+id+`" is already registered. Remove it first.`, "invalid_request_error", "repo_exists")
		return
	}

	workspaces := filepath.Join(app.Cfg.Home, "workspaces")
	if err := os.MkdirAll(workspaces, 0o700); err != nil {
		oaiError(w, 500, "Could not create the workspace directory: "+err.Error(), "configuration_error", "")
		return
	}
	dest := filepath.Join(workspaces, id)
	if _, err := os.Stat(dest); err == nil {
		oaiError(w, 409, `A workspace directory for "`+id+`" already exists. Remove it or choose another id.`, "invalid_request_error", "workspace_exists")
		return
	}

	// Clone through the gitx seam: the exec implementation runs the identical git-clone-with-
	// prompting-disabled invocation this endpoint always has (see the package doc comment above);
	// the go-git fallback (no git binary on the host) handles https itself and returns a clear
	// error for an ssh URL instead of this endpoint 500ing for want of a git binary.
	if err := gitx.Detect().Clone(r.Context(), url, dest, 1); err != nil {
		_ = os.RemoveAll(dest) // don't leave a half-clone behind
		msg := "git clone failed: " + strings.TrimSpace(lastLine(err.Error()))
		// An auth-shaped failure sends new users hunting through GitHub permission screens; tell
		// them what actually matters: public repos need no credentials at all, so an auth error
		// means either a typo'd URL or a genuinely private repo.
		if looksLikeAuthFailure(err.Error()) {
			msg += ` — Public repositories need no GitHub permissions or login, so if this repo is public, check the URL for typos. If it's private, set up an SSH key or a git credential helper on the machine running this gateway, then use the ssh URL (git@github.com:owner/repo.git).`
		}
		oaiError(w, 400, msg, "invalid_request_error", "clone_failed")
		return
	}

	absRoot, _ := filepath.Abs(dest)
	if _, err := app.DB.ExecContext(state.Ctx(),
		`INSERT INTO repos(id,root,project,created_at) VALUES(?,?,?,?)`,
		id, absRoot, principal.Project, util.NowISO()); err != nil {
		_ = os.RemoveAll(dest)
		oaiError(w, 500, "Cloned but failed to register: "+err.Error(), "configuration_error", "")
		return
	}
	app.auditAs(principal, "repo.clone", id)
	fr := memory.RepoFreshness(absRoot)
	sendJSON(w, 200, map[string]any{
		"repo":   map[string]any{"id": id, "root": absRoot, "project": principal.Project, "cloned_from": url},
		"memory": map[string]any{"status": fr.Status, "present_count": fr.PresentCount, "total_files": fr.TotalFiles},
	})
}

// looksLikeAuthFailure matches the stderr shapes git (and go-git) produce when a clone needs
// credentials it doesn't have: https 401/403/404-on-private, disabled terminal prompts, and ssh
// key rejection. GitHub reports a private repo to an unauthenticated https client as 404/"not
// found", so that shape is included — for a PUBLIC repo the same text means a typo'd URL, which
// is exactly what the appended hint tells the user to check first.
func looksLikeAuthFailure(s string) bool {
	l := strings.ToLower(s)
	for _, m := range []string{
		"authentication failed", "authentication required", "could not read username",
		"could not read password", "terminal prompts disabled", "permission denied (publickey)",
		"invalid credentials", "http 401", "401 unauthorized", "403 forbidden",
		"repository not found", "not found",
	} {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
