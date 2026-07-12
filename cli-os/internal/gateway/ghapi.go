// ghapi.go is a tiny, purpose-built GitHub REST client — just the four calls "Connect GitHub" and
// the scaffold-PR/push paths need (verify a token, read a repo's default branch, create a pull
// request, find an existing open one). It exists so those flows work on a host with NO `gh` binary
// (Android, and any bare server) using only a stored token, not ambient gh auth.
//
// Discipline mirrors scaffold_pr.go's: it NEVER calls a merge endpoint (not even defined here), and
// no response body or raw transport error ever reaches a client — callers surface a fixed humanized
// sentence and send the raw text (redacted of the token) to app.auditAs only. The token travels
// solely as an Authorization: Bearer header, never in a URL or log.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// githubAPIBase is the REST root, a var only so tests can point it at an httptest server. Production
// callers never change it.
var githubAPIBase = "https://api.github.com"

// ghHTTPTimeout bounds every GitHub API call (mirrors scaffold_pr.go's ghExecTimeout intent).
const ghHTTPTimeout = 20 * time.Second

var ghHTTPClient = &http.Client{Timeout: ghHTTPTimeout}

// ghError wraps a GitHub API failure. Msg is a fixed, client-safe sentence; Raw is diagnostic
// detail for the audit log ONLY (already token-redacted by the constructor) and must never be sent
// to a client.
type ghError struct {
	Msg string
	Raw string
}

func (e *ghError) Error() string { return e.Msg }

func ghErr(msg, raw, token string) *ghError {
	return &ghError{Msg: msg, Raw: redactGitToken(raw, token)}
}

// redactGitToken removes a token from diagnostic text before it is audited. Belt-and-suspenders:
// these calls put the token only in a header, never in a body or URL, but redact regardless.
func redactGitToken(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "[redacted]")
	}
	return s
}

// ownerRepoFromURL extracts (owner, repo) from a GitHub remote URL — both the https form
// (https://github.com/O/R(.git)) and the scp-like ssh form (git@github.com:O/R(.git)). ok=false for
// anything that is not a github.com repo URL.
func ownerRepoFromURL(raw string) (owner, repo string, ok bool) {
	raw = strings.TrimSpace(raw)
	var path string
	switch {
	case strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://"):
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Host, "github.com") {
			return "", "", false
		}
		path = strings.TrimPrefix(u.Path, "/")
	case strings.HasPrefix(raw, "git@github.com:"):
		path = strings.TrimPrefix(raw, "git@github.com:")
	case strings.HasPrefix(raw, "ssh://git@github.com/"):
		path = strings.TrimPrefix(raw, "ssh://git@github.com/")
	default:
		return "", "", false
	}
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ghDo issues one GitHub API request with the token as a bearer header and returns the status and
// body. The token is NEVER placed in the URL. A transport error becomes a ghError with a generic
// message.
func ghDo(ctx context.Context, method, token, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, ghErr("Could not build the GitHub request.", err.Error(), token)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, githubAPIBase+path, rdr)
	if err != nil {
		return 0, nil, ghErr("Could not reach GitHub.", err.Error(), token)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := ghHTTPClient.Do(req)
	if err != nil {
		return 0, nil, ghErr("Could not reach GitHub.", err.Error(), token)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, out, nil
}

// ghVerifyToken calls GET /user and returns the authenticated login. A 401/403 means the token is
// invalid or lacks scope; anything else is a transport/GitHub failure. The token is validated
// BEFORE it is ever stored, so a bad paste never lands in the vault.
func ghVerifyToken(ctx context.Context, token string) (login string, err error) {
	status, body, derr := ghDo(ctx, http.MethodGet, token, "/user", nil)
	if derr != nil {
		return "", derr
	}
	if status == 401 || status == 403 {
		return "", ghErr("GitHub rejected this token. Check that it is valid and has Contents and Pull requests permissions.", string(body), token)
	}
	if status != 200 {
		return "", ghErr("GitHub could not verify this token right now. Try again in a moment.", string(body), token)
	}
	var v struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Login == "" {
		return "", ghErr("GitHub returned an unexpected response verifying the token.", string(body), token)
	}
	return v.Login, nil
}

// ghDefaultBranch returns a repo's default branch (e.g. "main"/"master") — the base a PR opens
// against. One extra GET so the base is correct regardless of the repo's naming.
func ghDefaultBranch(ctx context.Context, token, owner, repo string) (string, error) {
	status, body, derr := ghDo(ctx, http.MethodGet, token, "/repos/"+owner+"/"+repo, nil)
	if derr != nil {
		return "", derr
	}
	if status != 200 {
		return "", ghErr("Could not read the repository from GitHub (is the token scoped to it?).", string(body), token)
	}
	var v struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.DefaultBranch == "" {
		return "", ghErr("GitHub returned an unexpected repository response.", string(body), token)
	}
	return v.DefaultBranch, nil
}

// ghCreatePR opens a pull request and returns its html_url. NEVER merges. head/base are branch
// names; title/body are the fixed scaffold strings (never caller-controlled free text).
func ghCreatePR(ctx context.Context, token, owner, repo, head, base, title, body string) (string, error) {
	payload := map[string]any{"title": title, "head": head, "base": base, "body": body}
	status, respBody, derr := ghDo(ctx, http.MethodPost, token, "/repos/"+owner+"/"+repo+"/pulls", payload)
	if derr != nil {
		return "", derr
	}
	if status != 201 {
		return "", ghErr("The branch was pushed, but opening the pull request on GitHub failed.", string(respBody), token)
	}
	var v struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &v); err != nil || v.HTMLURL == "" {
		return "", ghErr("The pull request was created but GitHub did not return its URL.", string(respBody), token)
	}
	return v.HTMLURL, nil
}

// ghFindOpenPR returns the html_url of an existing OPEN pull request for head (branch), or "" if
// none — the REST twin of scaffold_pr.go's lookupExistingPR. It fails OPEN to "" on any error (a
// missing PR and a transient failure both mean "proceed to the normal push+create"). CLOSED PRs are
// excluded by the state=open filter. Never sends any body/error to a client.
func ghFindOpenPR(ctx context.Context, token, owner, repo, head string) string {
	q := "/repos/" + owner + "/" + repo + "/pulls?state=open&head=" + url.QueryEscape(owner+":"+head)
	status, body, derr := ghDo(ctx, http.MethodGet, token, q, nil)
	if derr != nil || status != 200 {
		return ""
	}
	var prs []struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &prs); err != nil || len(prs) == 0 {
		return ""
	}
	return prs[0].HTMLURL
}
