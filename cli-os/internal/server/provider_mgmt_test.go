package server_test

import (
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

// configured builds a server with a vault + one authenticating token but NO provider yet — the state a
// user is in right after setup when they open the dashboard to manage providers. Returns the server,
// config, db, the token id (== audit actor), and the full token.
func configured(t *testing.T) (*httptest.Server, config.Config, *sql.DB, string, string) {
	t.Helper()
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	if err := config.EnsureHome(cfg); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := security.EnsureMasterKey(cfg.MasterKeyPath); err != nil {
		t.Fatalf("master key: %v", err)
	}
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases, StartedAt: time.Now()}
	srv := httptest.NewServer(server.Handler(app))
	t.Cleanup(srv.Close)
	id, token, err := security.MintToken(db, "ops", nil, nil)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return srv, cfg, db, id, token
}

// fakeOpenAI is an OpenAI-shaped upstream that 200s only for the listed bearer keys, 401s otherwise —
// so validation and real routed calls exercise the actual key the gateway stored.
func fakeOpenAI(t *testing.T, accept ...string) *httptest.Server {
	t.Helper()
	ok := map[string]bool{}
	for _, k := range accept {
		ok["Bearer "+k] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if ok[r.Header.Get("authorization")] {
			w.WriteHeader(200)
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			return
		}
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jstr(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }

func providerRow(t *testing.T, db *sql.DB, name string) (encKey string, enabled, isDefault, verified int, createdAt string, found bool) {
	t.Helper()
	var enc sql.NullString
	err := db.QueryRow(`SELECT COALESCE(enc_key,''), enabled, is_default, verified, created_at FROM providers WHERE name = ?`, name).
		Scan(&enc, &enabled, &isDefault, &verified, &createdAt)
	if err == sql.ErrNoRows {
		return "", 0, 0, 0, "", false
	}
	if err != nil {
		t.Fatalf("provider row %q: %v", name, err)
	}
	return enc.String, enabled, isDefault, verified, createdAt, true
}

// TestProviderMgmtAuthRequired: every management endpoint rejects a missing/invalid token — there is no
// unauthenticated management or emergency-revoke path.
func TestProviderMgmtAuthRequired(t *testing.T) {
	srv, _, _, _, _ := configured(t)
	base := srv.URL
	paths := []string{"/v1/providers", "/v1/providers/test", "/v1/providers/rotate", "/v1/providers/remove", "/v1/providers/update", "/v1/providers/models"}
	for _, p := range paths {
		if resp, _ := doJSON(t, "POST", base+p, "", map[string]any{"name": "x"}); resp.StatusCode != 401 {
			t.Fatalf("POST %s without token must be 401, got %d", p, resp.StatusCode)
		}
		if resp, _ := doJSON(t, "POST", base+p, "l00p_bogus_nope", map[string]any{"name": "x"}); resp.StatusCode != 401 {
			t.Fatalf("POST %s with a bad token must be 401, got %d", p, resp.StatusCode)
		}
	}
}

// TestProviderAddReusesWizardCore: the authenticated add path validates with a REAL call (a bad key is
// rejected and stored nowhere — same behavior as the wizard), enforces the name charset, and refuses to
// clobber an existing provider.
func TestProviderAddReusesWizardCore(t *testing.T) {
	srv, cfg, db, _, token := configured(t)
	base := srv.URL
	fake := fakeOpenAI(t, "goodkey")

	body := func(key string) map[string]any {
		return map[string]any{"name": "openai", "adapter": "openai-compat", "base_url": fake.URL, "api_key": key, "model": "gpt-x", "default": true}
	}

	// bad key rejected AND stored nowhere (identical to the wizard's validate-then-store).
	if resp, _ := doJSON(t, "POST", base+"/v1/providers", token, body("badkey")); resp.StatusCode != 400 {
		t.Fatalf("bad-key add must be 400, got %d", resp.StatusCode)
	}
	if n := countProviders(t, db); n != 0 {
		t.Fatalf("a failed-validation provider must not be stored; count=%d", n)
	}
	// invalid name charset rejected.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "Bad Name", "adapter": "mock"}); resp.StatusCode != 400 {
		t.Fatalf("invalid provider name must be 400, got %d", resp.StatusCode)
	}
	// good key stored encrypted, unverified.
	resp, m := doJSON(t, "POST", base+"/v1/providers", token, body("goodkey"))
	if resp.StatusCode != 200 {
		t.Fatalf("good-key add must be 200, got %d %v", resp.StatusCode, m)
	}
	if strings.Contains(jstr(m), "goodkey") {
		t.Fatalf("add response must not echo the key: %s", jstr(m))
	}
	enc, _, isDef, verified, _, found := providerRow(t, db, "openai")
	if !found || isDef != 1 || verified != 0 {
		t.Fatalf("stored provider must be default + unverified: isDef=%d verified=%d found=%v", isDef, verified, found)
	}
	if enc == "goodkey" || enc == "" {
		t.Fatalf("key must be encrypted at rest, got %q", enc)
	}
	if dec, err := security.DecryptSecret(cfg.MasterKeyPath, enc); err != nil || dec != "goodkey" {
		t.Fatalf("stored key must decrypt to the original: dec=%q err=%v", dec, err)
	}
	// adding the same name again is refused (409) — no silent clobber.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers", token, body("goodkey")); resp.StatusCode != 409 {
		t.Fatalf("duplicate add must be 409, got %d", resp.StatusCode)
	}
}

// TestProviderRotatePreservesIdentityAndKillsOldKey is Fable 5's headline guard: rotation replaces only
// the key (identity — default/enabled/created_at — is preserved), the OLD ciphertext is truly gone from
// the vault, verified resets to 0, a real routed request re-flips it to 1, and a second rotation resets
// it again (cache-free correctness).
func TestProviderRotatePreservesIdentityAndKillsOldKey(t *testing.T) {
	srv, cfg, db, _, token := configured(t)
	base := srv.URL
	fake := fakeOpenAI(t, "key-old", "key-new") // accept both so add and rotate both validate

	add := map[string]any{"name": "openai", "adapter": "openai-compat", "base_url": fake.URL, "api_key": "key-old", "model": "gpt-x", "default": true}
	if resp, m := doJSON(t, "POST", base+"/v1/providers", token, add); resp.StatusCode != 200 {
		t.Fatalf("add: %d %v", resp.StatusCode, m)
	}
	_, enabled0, isDef0, _, created0, _ := providerRow(t, db, "openai")

	// rotate to a new key.
	resp, m := doJSON(t, "POST", base+"/v1/providers/rotate", token, map[string]any{"name": "openai", "api_key": "key-new", "model": "gpt-x"})
	if resp.StatusCode != 200 {
		t.Fatalf("rotate: %d %v", resp.StatusCode, m)
	}
	if strings.Contains(jstr(m), "key-new") || strings.Contains(jstr(m), "key-old") {
		t.Fatalf("rotate response must not echo any key: %s", jstr(m))
	}
	enc, enabled1, isDef1, verified1, created1, _ := providerRow(t, db, "openai")
	// identity preserved.
	if enabled1 != enabled0 || isDef1 != isDef0 || created1 != created0 {
		t.Fatalf("rotate must preserve identity: enabled %d->%d default %d->%d created %q->%q", enabled0, enabled1, isDef0, isDef1, created0, created1)
	}
	if verified1 != 0 {
		t.Fatalf("rotate must reset verified to 0, got %d", verified1)
	}
	// the OLD key is really gone: the single enc_key cell now decrypts to the NEW key, so the old
	// ciphertext was overwritten in place (a real overwrite, not a second stored copy) — unrecoverable.
	dec, err := security.DecryptSecret(cfg.MasterKeyPath, enc)
	if err != nil || dec != "key-new" {
		t.Fatalf("stored key must decrypt to the rotated key (old key gone): dec=%q err=%v", dec, err)
	}

	// a real routed request flips verified to 1.
	if resp := post(t, base, token, map[string]any{"model": "gpt-x", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, nil); resp.StatusCode != 200 {
		t.Fatalf("routed request after rotate must succeed (new key valid), got %d", resp.StatusCode)
	}
	if _, _, _, v, _, _ := providerRow(t, db, "openai"); v != 1 {
		t.Fatalf("a successful routed request must flip verified to 1, got %d", v)
	}
	// rotating again resets verified — proves there is no stale verified cache.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers/rotate", token, map[string]any{"name": "openai", "api_key": "key-new", "model": "gpt-x"}); resp.StatusCode != 200 {
		t.Fatalf("second rotate: %d", resp.StatusCode)
	}
	if _, _, _, v, _, _ := providerRow(t, db, "openai"); v != 0 {
		t.Fatalf("second rotate must reset verified to 0, got %d", v)
	}
}

// TestProviderRemoveLastProviderLifecycle exercises the confirmed product decision end-to-end: the
// only/default provider CAN be removed, but only behind a server-side type-to-confirm gate; afterward
// System Health flips to a specific broken headline, requests fail with a specific dashboard-pointing
// error, the setup endpoints stay locked, and re-adding through the NEW endpoint restores routing.
func TestProviderRemoveLastProviderLifecycle(t *testing.T) {
	srv, _, db, _, token := configured(t)
	base := srv.URL

	// one provider (mock, default), plus a stray model-selection row to prove the cascade delete.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "mock", "adapter": "mock", "default": true}); resp.StatusCode != 200 {
		t.Fatalf("add mock: %d", resp.StatusCode)
	}
	db.Exec(`INSERT INTO provider_models(provider,model,enabled) VALUES('mock','ghost',0)`)
	// latch setup-complete (vault + provider + token) so the removal cannot re-open setup endpoints.
	if _, st := doJSON(t, "GET", base+"/v1/setup/status", "", nil); st["setup_complete"] != true {
		t.Fatalf("setup should be complete once vault+provider+token exist: %v", st)
	}

	// wrong confirm → 409 with the SPECIFIC only-provider warning; nothing removed.
	resp, m := doJSON(t, "POST", base+"/v1/providers/remove", token, map[string]any{"name": "mock", "confirm": "nope"})
	if resp.StatusCode != 409 {
		t.Fatalf("mismatched confirm must be 409, got %d", resp.StatusCode)
	}
	impact, _ := m["impact"].(map[string]any)
	if impact == nil || impact["is_only"] != true {
		t.Fatalf("409 must carry impact.is_only=true: %v", m["impact"])
	}
	if !warningsContain(impact, "only configured provider") {
		t.Fatalf("only-provider warning must be specific: %v", impact["warnings"])
	}
	if countProviders(t, db) != 1 {
		t.Fatalf("a mismatched confirm must not remove the provider")
	}

	// correct confirm → removed; provider row AND its model-selection rows gone.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers/remove", token, map[string]any{"name": "mock", "confirm": "mock"}); resp.StatusCode != 200 {
		t.Fatalf("correct confirm must remove, got %d", resp.StatusCode)
	}
	if countProviders(t, db) != 0 {
		t.Fatalf("provider must be gone after confirmed removal")
	}
	var ghosts int
	db.QueryRow(`SELECT COUNT(*) FROM provider_models WHERE provider='mock'`).Scan(&ghosts)
	if ghosts != 0 {
		t.Fatalf("provider_models rows must be deleted with the provider, got %d", ghosts)
	}

	// System Health flips to a specific broken headline (not silently "Operational").
	_, sum := doJSON(t, "GET", base+"/v1/dashboard/summary", token, nil)
	sys, _ := sum["system"].(map[string]any)
	if sys == nil || sys["status"] != "degraded" || sys["status_label"] != "No providers configured" {
		t.Fatalf("System Health must reflect the broken state: %v", sum["system"])
	}

	// a request now fails with the SPECIFIC dashboard-pointing error, not a generic 500/timeout.
	resp = post(t, base, token, map[string]any{"model": "x", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, nil)
	if resp.StatusCode != 503 {
		t.Fatalf("request with zero providers must be 503, got %d", resp.StatusCode)
	}
	cj := bodyJSON(t, resp)
	e, _ := cj["error"].(map[string]any)
	if e == nil || e["code"] != "no_providers_configured" {
		t.Fatalf("zero-provider error must carry code no_providers_configured: %v", cj["error"])
	}
	if !strings.Contains(strings.ToLower(asString(e["message"])), "dashboard") {
		t.Fatalf("zero-provider error must point back to the dashboard: %v", e["message"])
	}

	// setup endpoints stay locked (the durable latch survives removing the last provider).
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/provider", "", map[string]any{"name": "evil", "adapter": "mock"}); resp.StatusCode != 403 {
		t.Fatalf("setup endpoints must stay locked after removal, got %d", resp.StatusCode)
	}

	// re-adding through the authenticated endpoint restores routing.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "mock", "adapter": "mock", "default": true}); resp.StatusCode != 200 {
		t.Fatalf("re-add via the management endpoint must work, got %d", resp.StatusCode)
	}
	if resp := post(t, base, token, map[string]any{"model": "x", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, nil); resp.StatusCode != 200 {
		t.Fatalf("routing must work again after re-adding a provider, got %d", resp.StatusCode)
	}
}

// TestProviderDisableStopsRouting: the /v1/providers/update endpoint's enabled flip actually affects
// routing — a disabled provider stops serving requests, and disabling the LAST enabled provider yields
// the distinct "All providers are disabled" no_providers_configured 503 (a provider still exists, unlike
// the remove-to-zero case). Re-enabling restores routing (and clears any breaker).
func TestProviderDisableStopsRouting(t *testing.T) {
	srv, _, _, _, token := configured(t)
	base := srv.URL
	msg := map[string]any{"model": "x", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "mocka", "adapter": "mock", "default": true})
	doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "mockb", "adapter": "mock"})

	// disable mockb — a request must route to the still-enabled default, never to the disabled provider.
	if resp, m := doJSON(t, "POST", base+"/v1/providers/update", token, map[string]any{"name": "mockb", "enabled": false}); resp.StatusCode != 200 {
		t.Fatalf("disable mockb: %d %v", resp.StatusCode, m)
	}
	resp := post(t, base, token, msg, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("a request must still route to the enabled default, got %d", resp.StatusCode)
	}
	if p := resp.Header.Get("x-l00prite-provider"); p == "mockb" {
		t.Fatalf("a disabled provider must not serve requests, but mockb did")
	}
	resp.Body.Close()

	// disable the remaining provider: zero ENABLED but the rows still exist (distinct from removal).
	if resp, _ := doJSON(t, "POST", base+"/v1/providers/update", token, map[string]any{"name": "mocka", "enabled": false}); resp.StatusCode != 200 {
		t.Fatalf("disable mocka: %d", resp.StatusCode)
	}
	resp = post(t, base, token, msg, nil)
	if resp.StatusCode != 503 {
		t.Fatalf("zero enabled providers must be 503, got %d", resp.StatusCode)
	}
	e, _ := bodyJSON(t, resp)["error"].(map[string]any)
	if e == nil || e["code"] != "no_providers_configured" {
		t.Fatalf("all-disabled must carry code no_providers_configured: %v", e)
	}
	if !strings.Contains(asString(e["message"]), "disabled") {
		t.Fatalf("the all-disabled message must be distinct (mention disabled): %v", e["message"])
	}

	// re-enabling restores routing.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers/update", token, map[string]any{"name": "mocka", "enabled": true}); resp.StatusCode != 200 {
		t.Fatalf("re-enable mocka: %d", resp.StatusCode)
	}
	if resp := post(t, base, token, msg, nil); resp.StatusCode != 200 {
		t.Fatalf("re-enabling a provider must restore routing, got %d", resp.StatusCode)
	}
}

// TestProviderKeyNeverReturnedAndAudit: no add/rotate/test/summary response ever contains a plaintext
// key, and every mutating action is attributed in the audit log to the acting token (never "setup"/
// "cli"), with the provider name as detail (never a key) and a real timestamp.
func TestProviderKeyNeverReturnedAndAudit(t *testing.T) {
	srv, _, db, tokenID, token := configured(t)
	base := srv.URL
	fake := fakeOpenAI(t, "SECRET-ONE", "SECRET-TWO")

	// add.
	_, m := doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "openai", "adapter": "openai-compat", "base_url": fake.URL, "api_key": "SECRET-ONE", "model": "gpt-x", "default": true})
	assertNoSecret(t, "add", jstr(m), "SECRET-ONE")
	// rotate.
	_, m = doJSON(t, "POST", base+"/v1/providers/rotate", token, map[string]any{"name": "openai", "api_key": "SECRET-TWO", "model": "gpt-x"})
	assertNoSecret(t, "rotate", jstr(m), "SECRET-ONE", "SECRET-TWO")
	// validate a wrong key — the failure text must not echo the ATTEMPTED key ("wrong-guess") either.
	_, m = doJSON(t, "POST", base+"/v1/providers/test", token, map[string]any{"name": "openai", "adapter": "openai-compat", "base_url": fake.URL, "api_key": "wrong-guess", "model": "gpt-x"})
	assertNoSecret(t, "test", jstr(m), "SECRET-ONE", "SECRET-TWO", "wrong-guess")
	// dashboard summary never leaks the key and reports has_key/verified only.
	_, sum := doJSON(t, "GET", base+"/v1/dashboard/summary", token, nil)
	assertNoSecret(t, "summary", jstr(sum), "SECRET-ONE", "SECRET-TWO")
	if !strings.Contains(jstr(sum), `"has_key":true`) {
		t.Fatalf("summary must report has_key for the configured provider")
	}

	// audit: each action attributed to the acting token id, detail = provider name, no key, real ts.
	rows, err := db.Query(`SELECT actor, action, detail, ts FROM audit ORDER BY ts`)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var actor, action, ts string
		var detail sql.NullString
		rows.Scan(&actor, &action, &detail, &ts)
		if containsAnySecret(detail.String) {
			t.Fatalf("audit detail must never contain key material: %q", detail.String)
		}
		if strings.HasPrefix(action, "provider.") {
			if actor != tokenID {
				t.Fatalf("action %q must be attributed to the acting token %q, got actor %q", action, tokenID, actor)
			}
			if ts == "" {
				t.Fatalf("audit row must carry a real timestamp")
			}
			seen[action] = true
		}
	}
	for _, want := range []string{"provider.add", "provider.rotate"} {
		if !seen[want] {
			t.Fatalf("audit log must record %q attributed to the token", want)
		}
	}
}

// TestProviderModelSelectionRouting: disabling a model hides it from /v1/models, removes it from
// auto-routing selection, and blocks it from routing to that provider — and re-enabling restores it.
func TestProviderModelSelectionRouting(t *testing.T) {
	srv, _, _, _, token := configured(t)
	base := srv.URL

	// a provider named "anthropic" served by the mock adapter: it carries the anthropic manifest catalog
	// (so it has real model ids to select) but needs no key/network to route.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "anthropic", "adapter": "mock", "default": true}); resp.StatusCode != 200 {
		t.Fatalf("add anthropic(mock): %d", resp.StatusCode)
	}

	models := func() string { _, s := getRaw(t, base+"/v1/models"); return s }
	if !strings.Contains(models(), "claude-opus-4-8") {
		t.Fatalf("opus must be listed before it is disabled")
	}

	// disable opus.
	if resp, m := doJSON(t, "POST", base+"/v1/providers/models", token, map[string]any{"name": "anthropic", "disabled_models": []any{"claude-opus-4-8"}}); resp.StatusCode != 200 {
		t.Fatalf("disable model: %d %v", resp.StatusCode, m)
	}
	if strings.Contains(models(), "claude-opus-4-8") {
		t.Fatalf("a disabled model must not appear in /v1/models")
	}

	// auto-routing must not pick the disabled model.
	resp := post(t, base, token, map[string]any{"model": "auto:quality", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, map[string]string{"x-l00prite-dry-run": "1"})
	if resp.StatusCode != 200 {
		t.Fatalf("auto dry-run: %d", resp.StatusCode)
	}
	plan := bodyJSON(t, resp)
	wr, _ := plan["would_route"].(map[string]any)
	if wr == nil || asString(wr["model"]) == "claude-opus-4-8" {
		t.Fatalf("auto:quality must not select the disabled opus: %v", plan["would_route"])
	}

	// an explicit bare request for the disabled model must not route to that provider (it's the only one).
	resp = post(t, base, token, map[string]any{"model": "claude-opus-4-8", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, map[string]string{"x-l00prite-dry-run": "1"})
	if resp.StatusCode == 200 {
		t.Fatalf("a bare request for a disabled model must not route (got 200 %v)", bodyJSON(t, resp))
	}

	// re-enable (empty selection) restores it.
	if resp, _ := doJSON(t, "POST", base+"/v1/providers/models", token, map[string]any{"name": "anthropic", "disabled_models": []any{}}); resp.StatusCode != 200 {
		t.Fatalf("re-enable models: %d", resp.StatusCode)
	}
	if !strings.Contains(models(), "claude-opus-4-8") {
		t.Fatalf("re-enabling must restore the model to /v1/models")
	}
}

// TestProviderRemovalImpactSignals: the pre-removal warning enumerates the SPECIFIC config-file routing
// rules (auto-routing quality ranks) that will start failing — not a vague "are you sure?".
func TestProviderRemovalImpactSignals(t *testing.T) {
	srv, _, _, _, token := configured(t)
	base := srv.URL
	// second provider so "anthropic" isn't the only one — isolates the quality-rank signal.
	doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "mock", "adapter": "mock"})
	doJSON(t, "POST", base+"/v1/providers", token, map[string]any{"name": "anthropic", "adapter": "mock", "default": true})

	_, m := doJSON(t, "POST", base+"/v1/providers/remove", token, map[string]any{"name": "anthropic", "confirm": "wrong"})
	impact, _ := m["impact"].(map[string]any)
	if impact == nil {
		t.Fatalf("409 must carry impact: %v", m)
	}
	if impact["is_default"] != true {
		t.Fatalf("anthropic is the default provider: %v", impact)
	}
	refs, _ := impact["quality_rank_refs"].([]any)
	if len(refs) == 0 {
		t.Fatalf("removing anthropic must flag its auto-routing quality ranks: %v", impact)
	}
	if !warningsContain(impact, "auto-routing quality ranks") {
		t.Fatalf("the auto-routing warning must be specific: %v", impact["warnings"])
	}
}

// ---- helpers ----

func asString(v any) string { s, _ := v.(string); return s }

func containsAnySecret(s string) bool {
	for _, sec := range []string{"SECRET-ONE", "SECRET-TWO"} {
		if strings.Contains(s, sec) {
			return true
		}
	}
	return false
}

// assertNoSecret fails if any of the given key values appears in body. Callers pass every key that
// endpoint touched — including a REJECTED key on the validation path — so the test actually proves the
// attempted key isn't echoed back, not just the stored ones.
func assertNoSecret(t *testing.T, where, body string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if k != "" && strings.Contains(body, k) {
			t.Fatalf("%s response leaked key material (%q): %s", where, k, body)
		}
	}
}

func warningsContain(impact map[string]any, substr string) bool {
	ws, _ := impact["warnings"].([]any)
	for _, w := range ws {
		if wm, ok := w.(map[string]any); ok && strings.Contains(asString(wm["message"]), substr) {
			return true
		}
	}
	return false
}
