package server_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/server"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// unconfigured builds a fresh data dir + DB with NO master key, NO providers, NO tokens — a genuine
// zero-config first run — and returns the running server, its config, and its DB handle. It never
// calls EnsureMasterKey.
func unconfigured(t *testing.T) (*httptest.Server, config.Config, *sql.DB) {
	t.Helper()
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	if err := config.EnsureHome(cfg); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases, StartedAt: time.Now()}
	srv := httptest.NewServer(server.Handler(app))
	t.Cleanup(func() {
		srv.Close()
		db.Close()
	})
	return srv, cfg, db
}

func doJSON(t *testing.T, method, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("content-type", "application/json")
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp, bodyJSON(t, resp)
}

func getRaw(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp, bodyText(t, resp)
}

func countProviders(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&n); err != nil {
		t.Fatalf("count providers: %v", err)
	}
	return n
}

func toInt(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// TestFirstRunWizardE2E is the headline scenario: a fresh install with zero config walks the wizard's
// real endpoints to a working gateway, then the dashboard shows real data reflecting what was
// configured — and the setup endpoints lock down permanently afterward.
func TestFirstRunWizardE2E(t *testing.T) {
	srv, cfg, db := unconfigured(t)
	base := srv.URL

	// 1. GET / serves the setup wizard while unconfigured.
	if resp, html := getRaw(t, base+"/"); resp.StatusCode != 200 || !strings.Contains(html, "First-run setup") {
		t.Fatalf("GET / on a fresh install must serve the wizard; status=%d", resp.StatusCode)
	}

	// 2. status: nothing configured, next step is vault.
	_, st := doJSON(t, "GET", base+"/v1/setup/status", "", nil)
	if st["setup_complete"] != false || st["vault_initialized"] != false || st["next_step"] != "vault" {
		t.Fatalf("fresh status wrong: %v", st)
	}

	// 3. dashboard summary requires auth even before setup.
	if resp, _ := doJSON(t, "GET", base+"/v1/dashboard/summary", "", nil); resp.StatusCode != 401 {
		t.Fatalf("summary without token must be 401, got %d", resp.StatusCode)
	}

	// 4. initialize the vault (auto-generate).
	resp, v := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{})
	if resp.StatusCode != 200 || v["vault_initialized"] != true || v["generated"] != true {
		t.Fatalf("vault init failed: %d %v", resp.StatusCode, v)
	}
	if !config.MasterKeyPresent(cfg) {
		t.Fatalf("master key must exist on disk after vault init")
	}
	// re-initializing must be refused.
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{}); resp.StatusCode != 409 {
		t.Fatalf("second vault init must 409, got %d", resp.StatusCode)
	}

	// 5. add the mock provider (no key needed, no validation).
	resp, p := doJSON(t, "POST", base+"/v1/setup/provider", "", map[string]any{"name": "mock", "adapter": "mock", "default": true})
	if resp.StatusCode != 200 {
		t.Fatalf("provider add failed: %d %v", resp.StatusCode, p)
	}
	_, st = doJSON(t, "GET", base+"/v1/setup/status", "", nil)
	if st["next_step"] != "token" {
		t.Fatalf("after provider, next step must be token, got %v", st["next_step"])
	}

	// 6. mint the first token — this finalizes setup.
	resp, tk := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "demo"})
	if resp.StatusCode != 200 || tk["token"] == nil || tk["token"] == "" {
		t.Fatalf("token mint failed: %d %v", resp.StatusCode, tk)
	}
	token, _ := tk["token"].(string)
	if tk["setup_complete"] != true {
		t.Fatalf("minting the token should finalize setup: %v", tk)
	}

	// 7. setup is complete: / now serves the dashboard, not the wizard.
	if resp, html := getRaw(t, base+"/"); resp.StatusCode != 200 || strings.Contains(html, "First-run setup") {
		t.Fatalf("GET / after setup must serve the dashboard, not the wizard")
	}

	// 8. every mutating setup endpoint is now locked (403), performing no action.
	for _, path := range []string{"/v1/setup/vault", "/v1/setup/provider", "/v1/setup/provider/test", "/v1/setup/token"} {
		resp, m := doJSON(t, "POST", base+path, "", map[string]any{"name": "evil", "adapter": "mock", "project": "x"})
		if resp.StatusCode != 403 {
			t.Fatalf("POST %s after setup must be 403, got %d", path, resp.StatusCode)
		}
		if e, _ := m["error"].(map[string]any); e == nil || e["code"] != "setup_complete" {
			t.Fatalf("POST %s should carry code setup_complete, got %v", path, m["error"])
		}
	}
	// the lockdown really stored nothing: still exactly one provider.
	if got := countProviders(t, db); got != 1 {
		t.Fatalf("locked setup endpoints must not mutate; provider count=%d", got)
	}

	// 9. the dashboard summary, with the minted token, shows REAL data reflecting what we configured.
	resp, sum := doJSON(t, "GET", base+"/v1/dashboard/summary", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("summary with token must be 200, got %d", resp.StatusCode)
	}
	sys, _ := sum["system"].(map[string]any)
	if sys == nil || sys["vault_initialized"] != true {
		t.Fatalf("summary system.vault_initialized must be true: %v", sum["system"])
	}
	provs, _ := sum["providers"].([]any)
	foundMock := false
	for _, pr := range provs {
		if m, _ := pr.(map[string]any); m != nil && m["name"] == "mock" {
			foundMock = true
			if m["is_default"] != true {
				t.Fatalf("mock should be the default provider: %v", m)
			}
		}
	}
	if !foundMock {
		t.Fatalf("summary must list the mock provider we just added: %v", sum["providers"])
	}
	toks, _ := sum["tokens"].(map[string]any)
	if toks == nil || toInt(toks["active"]) < 1 {
		t.Fatalf("summary must report the minted token as active: %v", sum["tokens"])
	}
	if pr, _ := sum["principal"].(map[string]any); pr == nil || pr["project"] != "demo" {
		t.Fatalf("summary principal must reflect the authenticating token: %v", sum["principal"])
	}
	up, _ := sum["uptime"].(map[string]any)
	if up == nil || up["tracked"] != true {
		t.Fatalf("uptime must be tracked (real process start): %v", sum["uptime"])
	}
}

func TestSetupTokenRequiresVaultAndProvider(t *testing.T) {
	srv, _, _ := unconfigured(t)
	base := srv.URL

	if resp, m := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "demo"}); resp.StatusCode != 409 {
		t.Fatalf("token before vault must be 409, got %d %v", resp.StatusCode, m)
	}
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{}); resp.StatusCode != 200 {
		t.Fatalf("vault init: %d", resp.StatusCode)
	}
	if resp, m := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "demo"}); resp.StatusCode != 409 {
		t.Fatalf("token before provider must be 409, got %d %v", resp.StatusCode, m)
	}
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/provider", "", map[string]any{"name": "mock", "adapter": "mock", "default": true}); resp.StatusCode != 200 {
		t.Fatalf("provider add: %d", resp.StatusCode)
	}
	if resp, m := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "demo"}); resp.StatusCode != 200 || m["token"] == "" {
		t.Fatalf("token after vault+provider should succeed, got %d %v", resp.StatusCode, m)
	}
}

func TestSetupProviderRejectsCustomBaseURLByDefault(t *testing.T) {
	srv, _, _ := unconfigured(t)
	if resp, _ := doJSON(t, "POST", srv.URL+"/v1/setup/vault", "", map[string]any{}); resp.StatusCode != 200 {
		t.Fatalf("vault init: %d", resp.StatusCode)
	}
	resp, m := doJSON(t, "POST", srv.URL+"/v1/setup/provider/test", "", map[string]any{
		"name": "custom", "adapter": "openai-compat", "base_url": "http://127.0.0.1:9", "api_key": "sk-test", "model": "model",
	})
	if resp.StatusCode != 400 {
		t.Fatalf("custom setup base URL must be rejected by default, got %d %v", resp.StatusCode, m)
	}
	if e, _ := m["error"].(map[string]any); e == nil || e["code"] != "setup_custom_base_url_disabled" {
		t.Fatalf("expected setup_custom_base_url_disabled, got %v", m["error"])
	}
}

func TestDashboardRejectsRepoScopedToken(t *testing.T) {
	srv, _, db := unconfigured(t)
	base := srv.URL
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{}); resp.StatusCode != 200 {
		t.Fatalf("vault init: %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/provider", "", map[string]any{"name": "mock", "adapter": "mock", "default": true}); resp.StatusCode != 200 {
		t.Fatalf("provider add: %d", resp.StatusCode)
	}
	if _, err := db.Exec(`INSERT INTO repos(id,root,project,created_at) VALUES('repo1',?,?,?)`, t.TempDir(), "demo", time.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	resp, tk := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "demo", "repo": "repo1"})
	if resp.StatusCode != 200 {
		t.Fatalf("token mint: %d %v", resp.StatusCode, tk)
	}
	token, _ := tk["token"].(string)
	if resp, m := doJSON(t, "GET", base+"/v1/dashboard/summary", token, nil); resp.StatusCode != 403 {
		t.Fatalf("repo-scoped token must not access dashboard summary, got %d %v", resp.StatusCode, m)
	}
}

// TestSetupLockdownIsPersistent proves the lockdown is DURABLE: once setup completes, revoking the
// last token (or removing the last provider) must NOT re-open the unauthenticated setup endpoints.
// Without a persistent latch this would be a full auth-bypass back door.
func TestSetupLockdownIsPersistent(t *testing.T) {
	srv, _, db := unconfigured(t)
	base := srv.URL

	if resp, _ := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{}); resp.StatusCode != 200 {
		t.Fatalf("vault: %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/provider", "", map[string]any{"name": "mock", "adapter": "mock", "default": true}); resp.StatusCode != 200 {
		t.Fatalf("provider: %d", resp.StatusCode)
	}
	_, tk := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "demo"})
	tokenID, _ := tk["id"].(string)
	if tokenID == "" {
		t.Fatalf("no token id: %v", tk)
	}

	// setup is complete and locked.
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "x"}); resp.StatusCode != 403 {
		t.Fatalf("setup must be locked after completion, got %d", resp.StatusCode)
	}

	// revoke the ONLY token — live counts now say "0 active tokens".
	if !security.RevokeToken(db, tokenID) {
		t.Fatalf("revoke failed")
	}
	var active int
	db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE revoked = 0`).Scan(&active)
	if active != 0 {
		t.Fatalf("expected 0 active tokens after revoke, got %d", active)
	}

	// The latch must keep setup locked despite 0 active tokens — no auth-bypass back door.
	if resp, m := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "attacker"}); resp.StatusCode != 403 {
		t.Fatalf("SECURITY: setup endpoints re-opened after token revoke (got %d) — auth bypass: %v", resp.StatusCode, m)
	}
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{}); resp.StatusCode != 403 {
		t.Fatalf("SECURITY: vault endpoint re-opened after token revoke, got %d", resp.StatusCode)
	}
	// / still serves the dashboard, not the wizard.
	if _, html := getRaw(t, base+"/"); strings.Contains(html, "First-run setup") {
		t.Fatalf("/ reverted to the wizard after token revoke")
	}
	// setup status also reports complete (honors the latch).
	if _, st := doJSON(t, "GET", base+"/v1/setup/status", "", nil); st["setup_complete"] != true {
		t.Fatalf("status must stay setup_complete=true after revoke: %v", st)
	}
}

// TestSetupProviderRealValidation proves the wizard validates a provider key with a REAL upstream
// call before storing it: a bad key is rejected and nothing is persisted; a good key passes and is
// stored encrypted. A fake OpenAI-compatible upstream authorizes only "Bearer goodkey".
func TestSetupProviderRealValidation(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_ALLOW_CUSTOM_BASE_URL", "1")
	srv, cfg, db := unconfigured(t)
	base := srv.URL

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Header.Get("authorization") == "Bearer goodkey" {
			w.WriteHeader(200)
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			return
		}
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer fake.Close()

	// vault first (a keyed provider needs it to encrypt).
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{}); resp.StatusCode != 200 {
		t.Fatalf("vault init: %d", resp.StatusCode)
	}

	// mock always validates (no network / no key).
	if _, m := doJSON(t, "POST", base+"/v1/setup/provider/test", "", map[string]any{"name": "mock", "adapter": "mock"}); m["ok"] != true {
		t.Fatalf("mock validation should pass: %v", m)
	}

	testBody := func(key string) map[string]any {
		return map[string]any{"name": "openai", "adapter": "openai-compat", "base_url": fake.URL, "api_key": key, "model": "gpt-x"}
	}

	// bad key: validation reports failure.
	if _, m := doJSON(t, "POST", base+"/v1/setup/provider/test", "", testBody("badkey")); m["ok"] != false {
		t.Fatalf("bad key must fail validation: %v", m)
	}
	// good key: validation passes.
	if _, m := doJSON(t, "POST", base+"/v1/setup/provider/test", "", testBody("goodkey")); m["ok"] != true {
		t.Fatalf("good key must pass validation: %v", m)
	}

	// adding with a bad key is rejected AND stores nothing.
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/provider", "", testBody("badkey")); resp.StatusCode != 400 {
		t.Fatalf("adding a bad-key provider must be 400, got %d", resp.StatusCode)
	}
	if got := countProviders(t, db); got != 0 {
		t.Fatalf("a failed-validation provider must not be stored; count=%d", got)
	}

	// adding with a good key succeeds and persists an ENCRYPTED key (never plaintext).
	if resp, m := doJSON(t, "POST", base+"/v1/setup/provider", "", testBody("goodkey")); resp.StatusCode != 200 {
		t.Fatalf("adding a good-key provider must be 200, got %d %v", resp.StatusCode, m)
	}
	if got := countProviders(t, db); got != 1 {
		t.Fatalf("validated provider must be stored; count=%d", got)
	}
	var enc string
	if err := db.QueryRow(`SELECT COALESCE(enc_key,'') FROM providers WHERE name = 'openai'`).Scan(&enc); err != nil {
		t.Fatalf("read enc_key: %v", err)
	}
	if enc == "goodkey" || enc == "" {
		t.Fatalf("provider key must be encrypted at rest, got %q", enc)
	}
	if dec, err := security.DecryptSecret(cfg.MasterKeyPath, enc); err != nil || dec != "goodkey" {
		t.Fatalf("stored key must decrypt back to the original: dec=%q err=%v", dec, err)
	}
}

// doJSONHeaders is like doJSON but sets arbitrary extra headers instead of a bearer token — used
// here to exercise x-l00prite-setup-secret without dragging in a whole auth header type.
func doJSONHeaders(t *testing.T, method, url string, headers map[string]string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("content-type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp, bodyJSON(t, resp)
}

// TestSetupSecretGateBlocksWithoutHeader proves LOOPRITE_SETUP_SECRET (docs/android-architecture.md
// §4 G7): when the operator sets it, every mutating setup endpoint requires a matching
// x-l00prite-setup-secret header and rejects everything else, while GET /v1/setup/status stays
// open regardless (it leaks only booleans/counts).
func TestSetupSecretGateBlocksWithoutHeader(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_SECRET", "s3cr3t-install-value")
	srv, _, db := unconfigured(t)
	base := srv.URL

	if resp, _ := doJSON(t, "GET", base+"/v1/setup/status", "", nil); resp.StatusCode != 200 {
		t.Fatalf("GET /v1/setup/status must stay open even with LOOPRITE_SETUP_SECRET set, got %d", resp.StatusCode)
	}

	for _, path := range []string{"/v1/setup/vault", "/v1/setup/provider", "/v1/setup/provider/test", "/v1/setup/token"} {
		// no header at all
		resp, m := doJSON(t, "POST", base+path, "", map[string]any{"name": "mock", "adapter": "mock", "project": "x"})
		if resp.StatusCode != 403 {
			t.Fatalf("POST %s with no setup-secret header must 403, got %d", path, resp.StatusCode)
		}
		if e, _ := m["error"].(map[string]any); e == nil || e["code"] != "setup_secret_required" {
			t.Fatalf("POST %s should carry code setup_secret_required, got %v", path, m["error"])
		}
		// wrong header value
		resp, m = doJSONHeaders(t, "POST", base+path, map[string]string{"x-l00prite-setup-secret": "wrong"}, map[string]any{"name": "mock", "adapter": "mock", "project": "x"})
		if resp.StatusCode != 403 {
			t.Fatalf("POST %s with a wrong setup-secret header must 403, got %d", path, resp.StatusCode)
		}
		if e, _ := m["error"].(map[string]any); e == nil || e["code"] != "setup_secret_required" {
			t.Fatalf("POST %s (wrong header) should carry code setup_secret_required, got %v", path, m["error"])
		}
	}
	// none of the rejected calls mutated anything.
	if got := countProviders(t, db); got != 0 {
		t.Fatalf("a setup call rejected for a missing/wrong secret must not mutate; provider count=%d", got)
	}

	// the RIGHT header passes through to the existing logic unchanged.
	resp, v := doJSONHeaders(t, "POST", base+"/v1/setup/vault", map[string]string{"x-l00prite-setup-secret": "s3cr3t-install-value"}, map[string]any{})
	if resp.StatusCode != 200 || v["vault_initialized"] != true {
		t.Fatalf("vault init with the correct setup-secret header should succeed: %d %v", resp.StatusCode, v)
	}
}

// TestSetupSecretGateUnsetIsNoop proves the gate does nothing when LOOPRITE_SETUP_SECRET is unset
// (the desktop default): every existing setup call keeps working exactly as it did before G7.
func TestSetupSecretGateUnsetIsNoop(t *testing.T) {
	srv, _, _ := unconfigured(t)
	base := srv.URL
	resp, v := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{})
	if resp.StatusCode != 200 || v["vault_initialized"] != true {
		t.Fatalf("vault init with LOOPRITE_SETUP_SECRET unset should succeed unconditionally: %d %v", resp.StatusCode, v)
	}
}

// TestSetupVaultAcceptsProvidedKey verifies an operator-supplied 32-byte base64 master key is honored,
// and a malformed one is rejected.
func TestSetupVaultAcceptsProvidedKey(t *testing.T) {
	srv, cfg, _ := unconfigured(t)
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 zero bytes, base64 std
	resp, v := doJSON(t, "POST", srv.URL+"/v1/setup/vault", "", map[string]any{"master_key": key})
	if resp.StatusCode != 200 || v["generated"] != false {
		t.Fatalf("providing a key must be accepted with generated=false: %d %v", resp.StatusCode, v)
	}
	if !config.MasterKeyPresent(cfg) {
		t.Fatalf("provided key must be present after init")
	}
	// a malformed key is rejected (fresh instance, since re-init is blocked once a key exists).
	srv2, _, _ := unconfigured(t)
	if resp, _ := doJSON(t, "POST", srv2.URL+"/v1/setup/vault", "", map[string]any{"master_key": "not-base64!!"}); resp.StatusCode != 400 {
		t.Fatalf("malformed master key must be 400, got %d", resp.StatusCode)
	}
}
