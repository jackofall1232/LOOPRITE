package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// The device-owner endpoints (owner_session.go) exist so an APP INSTALL — the holder of the
// per-install LOOPRITE_SETUP_SECRET — can recover/switch/create workspaces after sign-out or
// UI-cookie expiry. They must fail closed on desktop (no secret configured) and never treat a
// bare project name as authentication.

func ownerTestApp(t *testing.T) *App {
	t.Helper()
	db, err := state.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{DB: db}
}

func doOwnerReq(t *testing.T, h http.HandlerFunc, method, path, body, secret string, cookies ...*http.Cookie) (int, map[string]any, http.Header) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if secret != "" {
		req.Header.Set("x-l00prite-setup-secret", secret)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	var data map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	return rec.Code, data, rec.Header()
}

func TestOwnerEndpointsFailClosedWithoutInstallSecret(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_SECRET", "")
	app := ownerTestApp(t)
	if _, _, err := security.MintToken(app.DB, "default", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		h      http.HandlerFunc
		method string
		path   string
	}{
		{app.HandleOwnerProjects, "GET", "/v1/owner/projects"},
		{app.HandleOwnerSession, "POST", "/v1/owner/session"},
		{app.HandleOwnerProjectCreate, "POST", "/v1/owner/projects"},
	} {
		code, data, _ := doOwnerReq(t, tc.h, tc.method, tc.path, `{}`, "")
		if code != 403 {
			t.Fatalf("%s %s: want 403 without an install secret, got %d (%v)", tc.method, tc.path, code, data)
		}
	}
}

func TestOwnerEndpointsRejectWrongSecret(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_SECRET", "correct-secret")
	app := ownerTestApp(t)
	code, _, _ := doOwnerReq(t, app.HandleOwnerProjects, "GET", "/v1/owner/projects", "", "wrong-secret")
	if code != 403 {
		t.Fatalf("want 403 for a wrong secret, got %d", code)
	}
}

func TestOwnerSessionDefaultAndNamedProject(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_SECRET", "s3cret")
	app := ownerTestApp(t)
	if _, _, err := security.MintToken(app.DB, "default", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := security.MintToken(app.DB, "team-b", nil, nil); err != nil {
		t.Fatal(err)
	}

	// Default: newest active token overall (team-b, minted last).
	code, data, hdr := doOwnerReq(t, app.HandleOwnerSession, "POST", "/v1/owner/session", `{}`, "s3cret")
	if code != 200 || data["project"] != "team-b" {
		t.Fatalf("default owner session want 200/team-b, got %d %v", code, data)
	}
	if !strings.Contains(hdr.Get("Set-Cookie"), uiSessionCookie+"=") {
		t.Fatalf("owner session must set the UI cookie, got %q", hdr.Get("Set-Cookie"))
	}

	// Named project.
	code, data, _ = doOwnerReq(t, app.HandleOwnerSession, "POST", "/v1/owner/session", `{"project":"default"}`, "s3cret")
	if code != 200 || data["project"] != "default" {
		t.Fatalf("named owner session want 200/default, got %d %v", code, data)
	}

	// Unknown project: 404 with the project list for recovery.
	code, data, _ = doOwnerReq(t, app.HandleOwnerSession, "POST", "/v1/owner/session", `{"project":"nope"}`, "s3cret")
	if code != 404 {
		t.Fatalf("unknown project want 404, got %d %v", code, data)
	}
	if projs, ok := data["projects"].([]any); !ok || len(projs) != 2 {
		t.Fatalf("404 must carry the 2 workspaces for recovery, got %v", data["projects"])
	}
}

func TestOwnerProjectsExcludesRevokedAndRepoScoped(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_SECRET", "s3cret")
	app := ownerTestApp(t)
	id, _, err := security.MintToken(app.DB, "default", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := "somerepo"
	if _, _, err := security.MintToken(app.DB, "scoped-only", &repo, nil); err != nil {
		t.Fatal(err)
	}
	security.RevokeToken(app.DB, id)

	code, data, _ := doOwnerReq(t, app.HandleOwnerProjects, "GET", "/v1/owner/projects", "", "s3cret")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	projs, _ := data["projects"].([]any)
	if len(projs) != 0 {
		t.Fatalf("revoked-default and repo-scoped-only projects must not appear, got %v", projs)
	}
}

func TestOwnerProjectCreateFlow(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_SECRET", "s3cret")
	app := ownerTestApp(t)
	if _, _, err := security.MintToken(app.DB, "default", nil, nil); err != nil {
		t.Fatal(err)
	}

	// Bad names rejected.
	for _, bad := range []string{"", "has space", "slash/name", strings.Repeat("x", 41)} {
		code, _, _ := doOwnerReq(t, app.HandleOwnerProjectCreate, "POST", "/v1/owner/projects", `{"project":"`+bad+`"}`, "s3cret")
		if code != 400 {
			t.Fatalf("project %q: want 400, got %d", bad, code)
		}
	}

	// Create "team-b": token shown once, UI cookie set, workspace listed.
	code, data, hdr := doOwnerReq(t, app.HandleOwnerProjectCreate, "POST", "/v1/owner/projects", `{"project":"team-b"}`, "s3cret")
	if code != 200 {
		t.Fatalf("create want 200, got %d %v", code, data)
	}
	tok, _ := data["token"].(map[string]any)
	raw, _ := tok["token"].(string)
	if !strings.HasPrefix(raw, "l00p_") {
		t.Fatalf("create must return the raw token once, got %v", data)
	}
	if security.VerifyToken(app.DB, raw) == nil {
		t.Fatal("the returned token must verify against the vault")
	}
	if !strings.Contains(hdr.Get("Set-Cookie"), uiSessionCookie+"=") {
		t.Fatal("create must also set the UI cookie (signed into the new workspace)")
	}

	// Duplicate rejected.
	code, _, _ = doOwnerReq(t, app.HandleOwnerProjectCreate, "POST", "/v1/owner/projects", `{"project":"team-b"}`, "s3cret")
	if code != 409 {
		t.Fatalf("duplicate want 409, got %d", code)
	}

	// Both workspaces now listed.
	code, data, _ = doOwnerReq(t, app.HandleOwnerProjects, "GET", "/v1/owner/projects", "", "s3cret")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	projs, _ := data["projects"].([]any)
	if len(projs) != 2 {
		t.Fatalf("want 2 workspaces, got %v", projs)
	}
}

func TestOwnerEndpointsStayOpenAfterSetupLatch(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_SECRET", "s3cret")
	app := ownerTestApp(t)
	if _, _, err := security.MintToken(app.DB, "default", nil, nil); err != nil {
		t.Fatal(err)
	}
	app.latchSetup()
	if !app.SetupComplete() {
		t.Fatal("latch must make setup complete")
	}
	code, _, _ := doOwnerReq(t, app.HandleOwnerSession, "POST", "/v1/owner/session", `{}`, "s3cret")
	if code != 200 {
		t.Fatalf("owner session must stay open post-latch, got %d", code)
	}
}

// The setup cookie (mintable only by the install secret) is the WebView-side owner credential.
func TestOwnerEndpointsAcceptSetupCookie(t *testing.T) {
	t.Setenv("LOOPRITE_SETUP_SECRET", "s3cret")
	app := ownerTestApp(t)
	if _, _, err := security.MintToken(app.DB, "default", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Mint a setup session natively (as MainActivity does at boot).
	req := httptest.NewRequest("POST", "/v1/setup/session", nil)
	req.Header.Set("x-l00prite-setup-secret", "s3cret")
	rec := httptest.NewRecorder()
	app.HandleSetupSession(rec, req)
	if rec.Code != 200 {
		t.Fatalf("setup session exchange failed: %d", rec.Code)
	}
	var setupCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == setupSessionCookie {
			setupCookie = c
		}
	}
	if setupCookie == nil {
		t.Fatal("no setup cookie issued")
	}
	code, _, _ := doOwnerReq(t, app.HandleOwnerProjects, "GET", "/v1/owner/projects", "", "", setupCookie)
	if code != 200 {
		t.Fatalf("valid setup cookie must satisfy the owner gate, got %d", code)
	}
}
