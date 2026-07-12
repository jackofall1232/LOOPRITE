package gateway

// Tests for the per-project Auto-PR toggle (autopr.go): default-OFF, the GET/POST /v1/auto-pr
// handlers (project-scoped, authenticated), and applyAutoPRGates -- the merge point that fills
// push/pr_create with auto_approve at run creation without ever overriding an explicit caller
// policy or touching any other gate class.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

func newAutoPRTestApp(t *testing.T) *App {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{DB: db}
}

// mintToken creates a real gateway token scoped to project and returns the raw bearer value.
func mintToken(t *testing.T, app *App, project string) string {
	t.Helper()
	_, raw, err := security.MintToken(app.DB, project, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func doAuthed(app *App, method, path, token string, body any, h http.HandlerFunc) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, strings.NewReader(string(b)))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// TestAutoPRDefaultsOffForNewAndExistingProjects pins that a project that has never touched the
// setting -- new or pre-existing -- reads as OFF, matching the missing-row-reads-0 DB contract.
func TestAutoPRDefaultsOffForNewAndExistingProjects(t *testing.T) {
	app := newAutoPRTestApp(t)
	for _, project := range []string{"brand-new-project", "some-old-project-never-touched"} {
		if app.autoPREnabled(project) {
			t.Fatalf("project %q should default to auto_pr OFF", project)
		}
	}
}

// TestHandleAutoPRGetSetRoundTrip drives the actual HTTP handlers: unauthenticated -> 401,
// default false, POST true persists and is reflected on the next GET, scoped per-project.
func TestHandleAutoPRGetSetRoundTrip(t *testing.T) {
	app := newAutoPRTestApp(t)
	tok := mintToken(t, app, "proj-a")
	otherTok := mintToken(t, app, "proj-b")

	// Unauthenticated request -> 401, never leaks the default.
	w := doAuthed(app, "GET", "/v1/auto-pr", "", nil, app.HandleAutoPRGet)
	if w.Code != 401 {
		t.Fatalf("unauthenticated GET /v1/auto-pr = %d, want 401", w.Code)
	}

	// Default: OFF.
	w = doAuthed(app, "GET", "/v1/auto-pr", tok, nil, app.HandleAutoPRGet)
	if w.Code != 200 {
		t.Fatalf("GET /v1/auto-pr = %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["enabled"] != false || got["project"] != "proj-a" {
		t.Fatalf("default view = %+v, want enabled=false project=proj-a", got)
	}

	// Turn it on.
	w = doAuthed(app, "POST", "/v1/auto-pr", tok, map[string]any{"enabled": true}, app.HandleAutoPRSet)
	if w.Code != 200 {
		t.Fatalf("POST /v1/auto-pr = %d: %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["enabled"] != true {
		t.Fatalf("POST response = %+v, want enabled=true", got)
	}

	// GET reflects the change.
	w = doAuthed(app, "GET", "/v1/auto-pr", tok, nil, app.HandleAutoPRGet)
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["enabled"] != true {
		t.Fatalf("GET after POST = %+v, want enabled=true", got)
	}

	// A DIFFERENT project's token must not see proj-a's setting -- scoping.
	w = doAuthed(app, "GET", "/v1/auto-pr", otherTok, nil, app.HandleAutoPRGet)
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["enabled"] != false || got["project"] != "proj-b" {
		t.Fatalf("proj-b token should see its own (OFF) setting, got %+v", got)
	}
}

// TestApplyAutoPRGatesOnlyFillsAbsentPushAndPRCreate is the precedence + scope test: toggle ON
// fills push/pr_create when absent, NEVER overwrites an explicit caller policy for those classes,
// and NEVER touches any other class; toggle OFF leaves the map completely untouched.
func TestApplyAutoPRGatesOnlyFillsAbsentPushAndPRCreate(t *testing.T) {
	app := newAutoPRTestApp(t)
	tok := mintToken(t, app, "proj-on")
	doAuthed(app, "POST", "/v1/auto-pr", tok, map[string]any{"enabled": true}, app.HandleAutoPRSet)

	// nil gates -> both eligible classes filled.
	gates := app.applyAutoPRGates("proj-on", nil)
	if gates[engine.GatePush] != engine.PolicyAutoApprove || gates[engine.GatePRCreate] != engine.PolicyAutoApprove {
		t.Fatalf("toggle ON + nil gates should fill both, got %+v", gates)
	}

	// Explicit caller policy on push survives; pr_create (absent) still gets filled.
	gates = app.applyAutoPRGates("proj-on", map[string]string{engine.GatePush: engine.PolicyRequireApproval})
	if gates[engine.GatePush] != engine.PolicyRequireApproval {
		t.Fatalf("explicit caller policy for push must survive the merge, got %+v", gates)
	}
	if gates[engine.GatePRCreate] != engine.PolicyAutoApprove {
		t.Fatalf("absent pr_create should still be filled, got %+v", gates)
	}

	// An explicit deny also survives.
	gates = app.applyAutoPRGates("proj-on", map[string]string{engine.GatePRCreate: engine.PolicyDeny})
	if gates[engine.GatePRCreate] != engine.PolicyDeny {
		t.Fatalf("explicit deny for pr_create must survive the merge, got %+v", gates)
	}

	// Other classes are never touched by the toggle.
	gates = app.applyAutoPRGates("proj-on", map[string]string{engine.GateMerge: engine.PolicyRequireApproval})
	if gates[engine.GateMerge] != engine.PolicyRequireApproval {
		t.Fatalf("merge entry must be untouched, got %+v", gates)
	}
	if _, ok := gates[engine.GateDestructive]; ok {
		t.Fatalf("toggle must never introduce a destructive entry, got %+v", gates)
	}

	// Toggle OFF (a fresh, never-configured project) -> map returned byte-identical (same nil-ness).
	tokOff := mintToken(t, app, "proj-off")
	_ = tokOff
	if got := app.applyAutoPRGates("proj-off", nil); got != nil {
		t.Fatalf("toggle OFF should return the caller's map untouched (nil stays nil), got %+v", got)
	}
	explicit := map[string]string{engine.GatePush: engine.PolicyRequireApproval}
	got := app.applyAutoPRGates("proj-off", explicit)
	if got[engine.GatePush] != engine.PolicyRequireApproval || len(got) != 1 {
		t.Fatalf("toggle OFF must leave an explicit map byte-identical, got %+v", got)
	}
}
