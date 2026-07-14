// First-run setup API. These endpoints are a THIN layer over the exact primitives the CLI uses —
// the vault (security.EnsureMasterKey / EncryptSecret), the providers table (same INSERT as
// `provider add`), and token minting (security.MintToken) — so the wizard and the CLI can never
// diverge: both write the same rows through the same code.
//
// SECURITY: the mutating/action endpoints (vault, provider, provider/test, token) are reachable ONLY
// while the system is genuinely unconfigured (SetupComplete() == false). The moment setup completes
// (vault initialized AND ≥1 provider AND ≥1 active token) they are DISABLED — every call returns 403
// and performs no action — so a setup endpoint can never linger as an unauthenticated back door. The
// server also refuses to boot a non-loopback bind without TLS, so first-run setup is never exposed by
// accident. GET /v1/setup/status stays readable (booleans/counts only, no secrets), like /healthz.
//
// LOOPRITE_SETUP_SECRET (docs/android-architecture.md §4 G7): on a shared loopback host — any
// co-installed Android app can also reach 127.0.0.1 — the setup-incomplete window above is
// otherwise unauthenticated by design (that's the whole point: a fresh install has no token yet).
// A malicious co-installed app could race first-run setup and mint itself the admin token before
// the legitimate user does. When the operator sets LOOPRITE_SETUP_SECRET, every mutating setup
// endpoint additionally requires a matching x-l00prite-setup-secret header (requireSetupSecret,
// below); unset (the desktop default), this is a complete no-op — behavior is byte-identical to
// before G7 existed.
package gateway

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// setupLatchKey marks, durably, that the system has completed first-run setup at least once.
const setupLatchKey = "setup_completed_at"

// SetupComplete reports whether first-run setup is finished. The FIRST time the system is fully
// configured (vault initialized AND ≥1 provider AND ≥1 non-revoked token) it writes a durable latch
// to the meta table; from then on this stays true even if the operator later revokes every token or
// removes every provider. Without the latch, dropping active tokens/providers back to zero would
// re-open the unauthenticated setup endpoints as an auth-bypass back door — so the latch is what makes
// the lockdown permanent. The latch is set by first-run (wizard or CLI); it is never cleared except by
// an explicit reset (wiping the data dir / deleting the meta row).
func (app *App) SetupComplete() bool {
	if app.setupLatched() {
		return true
	}
	if config.MasterKeyPresent(app.Cfg) && app.providerCount() >= 1 && app.activeTokenCount() >= 1 {
		app.latchSetup()
		return true
	}
	return false
}

func (app *App) setupLatched() bool {
	var v string
	err := app.DB.QueryRowContext(state.Ctx(), `SELECT value FROM meta WHERE key = ?`, setupLatchKey).Scan(&v)
	return err == nil && v != ""
}

func (app *App) latchSetup() {
	_, _ = app.DB.ExecContext(state.Ctx(), `INSERT OR IGNORE INTO meta(key,value) VALUES(?,?)`, setupLatchKey, util.NowISO())
}

func (app *App) providerCount() int {
	var n int
	_ = app.DB.QueryRowContext(state.Ctx(), `SELECT COUNT(*) FROM providers`).Scan(&n)
	return n
}

func (app *App) activeTokenCount() int {
	var n int
	_ = app.DB.QueryRowContext(state.Ctx(), `SELECT COUNT(*) FROM tokens WHERE revoked = 0`).Scan(&n)
	return n
}

func setupAudit(app *App, action, detail string) {
	var d any
	if detail != "" {
		d = detail
	}
	_, _ = app.DB.ExecContext(state.Ctx(), `INSERT INTO audit(id,ts,actor,action,detail) VALUES(?,?,?,?,?)`,
		util.RID("aud"), util.NowISO(), "setup", action, d)
}

// requireSetupSecret enforces LOOPRITE_SETUP_SECRET (G7) when the operator has set one: the caller
// must present the identical value in the x-l00prite-setup-secret header, compared in constant
// time via the same primitive token verification uses (util.TimingSafeEqual) so this gate leaks no
// timing information either. Returns true (and writes nothing) when the env var is unset — the
// desktop default — so every existing caller of the setup wizard is completely unaffected.
func requireSetupSecret(w http.ResponseWriter, r *http.Request) bool {
	secret := os.Getenv("LOOPRITE_SETUP_SECRET")
	if secret == "" {
		return true
	}
	got := r.Header.Get("x-l00prite-setup-secret")
	if got == "" || !util.TimingSafeEqual(got, secret) {
		sendJSON(w, 403, map[string]any{"error": map[string]any{
			"message": "Missing or invalid x-l00prite-setup-secret header.",
			"type":    "authentication_error", "code": "setup_secret_required"}})
		return false
	}
	return true
}

// setupGate returns true (and writes a 403) when setup is already complete, so mutating handlers can
// short-circuit. This is the lockdown that closes the first-run window permanently.
func (app *App) setupGate(w http.ResponseWriter) bool {
	if app.SetupComplete() {
		sendJSON(w, 403, map[string]any{"error": map[string]any{
			"message": "Setup is already complete; the setup endpoints are disabled. Use the CLI or an authenticated endpoint.",
			"type":    "permission_error", "code": "setup_complete"}})
		return true
	}
	return false
}

func decodeSetupBody(r *http.Request, v any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil // empty body is allowed (all-defaults)
	}
	return json.Unmarshal(data, v)
}

// networkInfo describes the current bind for the wizard's network step — real, not guessed.
func (app *App) networkInfo() map[string]any {
	cfg := app.Cfg
	loopback := config.IsLoopbackHost(cfg.Host)
	tls := cfg.TLS != nil
	scheme := "http"
	if tls {
		scheme = "https"
	}
	displayHost := cfg.Host
	if cfg.Host == "0.0.0.0" || cfg.Host == "::" || cfg.Host == "" {
		displayHost = "127.0.0.1" // a wildcard bind is reachable locally at loopback
	}
	port := cfg.Port
	if port == 0 {
		port = 8787
	}
	base := scheme + "://" + displayHost + ":" + strconv.Itoa(port)
	return map[string]any{
		"host": cfg.Host, "display_host": displayHost, "port": port, "scheme": scheme,
		"loopback": loopback, "tls": tls, "allow_insecure": config.AllowInsecureBind(),
		"exposed":       !loopback && !tls,
		"chat_url":      base + "/v1/chat/completions",
		"base_url":      base + "/v1",
		"dashboard_url": base + "/",
	}
}

// HandleSetupStatus is GET /v1/setup/status — always readable (no secrets), even when
// LOOPRITE_SETUP_SECRET is set (G7): it leaks only booleans/counts (vault initialized?, provider
// count, active token count, which step is next), nothing an attacker can act on directly, so it
// is exempt from requireSetupSecret exactly like /healthz is exempt from Bearer auth.
func (app *App) HandleSetupStatus(w http.ResponseWriter, r *http.Request) {
	vault := config.MasterKeyPresent(app.Cfg)
	provCount := app.providerCount()
	tokCount := app.activeTokenCount()
	complete := app.SetupComplete() // honors the durable latch, not just live counts
	next := "done"
	switch {
	case complete:
		next = "done"
	case !vault:
		next = "vault"
	case provCount == 0:
		next = "provider"
	case tokCount == 0:
		next = "token"
	}
	sendJSON(w, 200, map[string]any{
		"object": "l00prite.setup_status", "version": Version,
		"setup_complete": complete, "vault_initialized": vault,
		"provider_count": provCount, "active_token_count": tokCount,
		"next_step": next, "network": app.networkInfo(),
	})
}

type vaultReq struct {
	MasterKey string `json:"master_key"` // optional base64-of-32; omitted -> generate
}

// HandleSetupVault is POST /v1/setup/vault — initialize the vault master key.
func (app *App) HandleSetupVault(w http.ResponseWriter, r *http.Request) {
	if !requireSetupSecret(w, r) {
		return
	}
	if app.setupGate(w) {
		return
	}
	if config.MasterKeyPresent(app.Cfg) {
		sendJSON(w, 409, map[string]any{"error": map[string]any{
			"message": "Vault already initialized.", "type": "invalid_request_error", "code": "vault_already_initialized"}})
		return
	}
	var body vaultReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	generated := true
	if masterKey := strings.TrimSpace(body.MasterKey); masterKey != "" {
		raw, ok := security.DecodeBase64Key(masterKey)
		if !ok {
			oaiError(w, 400, "master_key must be base64 of exactly 32 bytes", "invalid_request_error", "invalid_master_key")
			return
		}
		if err := writeMasterKey(app.Cfg.MasterKeyPath, raw); err != nil {
			oaiError(w, 500, "Failed to write master key: "+err.Error(), "configuration_error", "")
			return
		}
		generated = false
	} else if err := security.EnsureMasterKey(app.Cfg.MasterKeyPath); err != nil {
		oaiError(w, 500, "Failed to create master key: "+err.Error(), "configuration_error", "")
		return
	}
	setupAudit(app, "setup.vault", "generated="+boolStr(generated))
	sendJSON(w, 200, map[string]any{"vault_initialized": true, "generated": generated})
}

// writeMasterKey persists a caller-supplied 32-byte key in the same base64 form EnsureMasterKey uses.
func writeMasterKey(path string, key []byte) error {
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

type providerTestReq struct {
	Name    string `json:"name"`
	Adapter string `json:"adapter"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// HandleSetupProviderTest is POST /v1/setup/provider/test — validate a key with a REAL upstream call
// (stores nothing). Returns a clear pass/fail so the wizard never accepts a bad key silently.
func (app *App) HandleSetupProviderTest(w http.ResponseWriter, r *http.Request) {
	if !requireSetupSecret(w, r) {
		return
	}
	if app.setupGate(w) {
		return
	}
	var body providerTestReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	baseURL := strings.TrimSpace(body.BaseURL)
	apiKey := strings.TrimSpace(body.APIKey)
	adapterKind := resolveAdapterKind(name, body.Adapter)
	if errMsg := setupBaseURLProblem(name, baseURL); errMsg != "" {
		oaiError(w, 400, errMsg, "invalid_request_error", "setup_custom_base_url_disabled")
		return
	}
	res := TestProviderKey(app.Cfg, adapterKind, name, baseURL, apiKey, strings.TrimSpace(body.Model))
	// A failed VALIDATION is still a well-formed REQUEST (HTTP 200); ok:false carries the verdict.
	sendJSON(w, 200, map[string]any{"ok": res.OK, "error": nilIfEmpty(res.Error), "model_used": nilIfEmpty(res.ModelUsed)})
}

type providerReq struct {
	Name           string `json:"name"`
	Adapter        string `json:"adapter"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	Model          string `json:"model"` // optional model to probe during validation
	Default        bool   `json:"default"`
	SkipValidation bool   `json:"skip_validation"` // allow adding a keyless/unvalidated provider deliberately
}

// HandleSetupProvider is POST /v1/setup/provider — validate (real call) then persist a provider through
// the SAME storeProvider core the authenticated dashboard "add provider" endpoint (Part E) uses, so the
// first-run and ongoing paths can never diverge. A bad key is rejected 400 and stored nowhere.
func (app *App) HandleSetupProvider(w http.ResponseWriter, r *http.Request) {
	if !requireSetupSecret(w, r) {
		return
	}
	if app.setupGate(w) {
		return
	}
	var body providerReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	name := strings.TrimSpace(body.Name)
	baseURL := strings.TrimSpace(body.BaseURL)
	if errMsg := setupBaseURLProblem(name, baseURL); errMsg != "" {
		oaiError(w, 400, errMsg, "invalid_request_error", "setup_custom_base_url_disabled")
		return
	}
	res, e := app.storeProvider(providerParams{
		Name: name, Adapter: body.Adapter, BaseURL: baseURL, APIKey: strings.TrimSpace(body.APIKey),
		Model: strings.TrimSpace(body.Model), Default: body.Default, SkipValidation: body.SkipValidation,
	})
	if e != nil {
		writeProviderErr(w, e)
		return
	}
	setupAudit(app, "provider.add", res.Name)
	sendJSON(w, 200, map[string]any{"provider": providerPublic(res)})
}

type tokenReq struct {
	Project     string `json:"project"`
	Repo        string `json:"repo"`
	ExpiresDays *int   `json:"expires_days"`
}

// HandleSetupToken is POST /v1/setup/token — mint the first token via the same primitive as
// `token mint`. Returned ONCE; never stored in plaintext. Minting typically finalizes setup.
func (app *App) HandleSetupToken(w http.ResponseWriter, r *http.Request) {
	if !requireSetupSecret(w, r) {
		return
	}
	if app.setupGate(w) {
		return
	}
	if !config.MasterKeyPresent(app.Cfg) {
		oaiError(w, 409, "Initialize the vault before minting the first token.", "invalid_request_error", "setup_vault_required")
		return
	}
	if app.providerCount() == 0 {
		oaiError(w, 409, "Add at least one provider before minting the first token.", "invalid_request_error", "setup_provider_required")
		return
	}
	var body tokenReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	project := strings.TrimSpace(body.Project)
	if project == "" {
		oaiError(w, 400, "project is required to mint a token", "invalid_request_error", "")
		return
	}
	// A repo scope is matched EXACTLY against a registered repo id on every request, so a scope
	// that will never match must not mint silently: it used to produce a token whose every prompt
	// failed with `Repository "…" is not registered` — a dead end no GitHub-side permission can
	// fix, and the wizard runs before any repo is normally registered. Reject strings that can
	// never be a repo id (URLs, owner/repo); warn when the id is well-formed but not registered yet.
	var repo *string
	repoWarning := ""
	if r := strings.TrimSpace(body.Repo); r != "" {
		var repoProject string
		err := app.DB.QueryRowContext(state.Ctx(), `SELECT project FROM repos WHERE id = ?`, r).Scan(&repoProject)
		switch {
		case err == sql.ErrNoRows:
			if !validRepoID.MatchString(r) {
				oaiError(w, 400, `"`+r+`" can't be used as a repo scope: a repo scope must exactly match a registered repo id — a short name like "myrepo" (letters, digits, ".", "-", "_"), not a URL or owner/repo. Leave the field empty; you can register repositories and mint repo-scoped tokens from the dashboard after setup.`, "invalid_request_error", "invalid_repo_scope")
				return
			}
			repoWarning = `No repository named "` + r + `" is registered yet. Every request with this token will fail with "repo not registered" until you register a repo with exactly that id (dashboard → Repositories → Register repo). If unsure, mint the token without a repo scope.`
		case err != nil:
			oaiError(w, 500, "Database error: "+err.Error(), "api_error", "")
			return
		case repoProject != project:
			// Repo ids are globally unique, so a scope registered under ANOTHER project can never
			// be healed from this token's side (chat rejects the project mismatch, and the id
			// can't be re-registered into this project) — refuse rather than mint a dead token.
			oaiError(w, 400, `Repository "`+r+`" is registered under project "`+repoProject+`", not "`+project+`" — a token in project "`+project+`" could never use it. Mint the token with project "`+repoProject+`", or leave the repo scope empty.`, "invalid_request_error", "repo_project_mismatch")
			return
		}
		repo = &r
	}
	id, token, err := security.MintToken(app.DB, project, repo, body.ExpiresDays)
	if err != nil {
		oaiError(w, 500, "Failed to mint token: "+err.Error(), "configuration_error", "")
		return
	}
	setupAudit(app, "token.mint", id)
	sendJSON(w, 200, map[string]any{
		"id": id, "token": token, "project": project, "repo": nilIfEmpty(strings.TrimSpace(body.Repo)),
		"warning":        nilIfEmpty(repoWarning),
		"setup_complete": app.SetupComplete(),
	})
}

// resolveAdapterKind maps a UI adapter choice to a concrete adapter kind, defaulting via the manifest
// when unspecified (same resolution the CLI's `provider add` uses).
func setupCustomBaseURLEnabled() bool {
	return os.Getenv("LOOPRITE_SETUP_ALLOW_CUSTOM_BASE_URL") == "1"
}

func setupBaseURLProblem(providerName, baseURL string) string {
	if baseURL == "" || setupCustomBaseURLEnabled() {
		return ""
	}
	def := strings.TrimSpace(adapters.DefaultBaseURL(providerName))
	if (def != "" && baseURL == def) || adapters.KnownBaseURL(baseURL) {
		return ""
	}
	return "Custom setup provider base URLs are disabled by default. Choose a built-in provider default or set LOOPRITE_SETUP_ALLOW_CUSTOM_BASE_URL=1 before first-run setup if this gateway is intentionally validating against a private/custom upstream."
}

func resolveAdapterKind(name, adapter string) string {
	adapter = strings.TrimSpace(adapter)
	if adapter == "" {
		return adapters.DefaultAdapterKind(name)
	}
	if adapter == "openai-native" {
		return "openai-compat"
	}
	return adapter
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
