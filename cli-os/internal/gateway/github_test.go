package gateway

// Tests for the "Connect GitHub" control plane (github.go + ghapi.go): the token is verified before
// it is ever stored, never returned by any endpoint, sealed in the vault, and reachable only with a
// dashboard token. Plus the small REST client (ownerRepoFromURL / ghCreatePR / ghFindOpenPR).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// stubGitHub stands in for api.github.com. token "goodtok" authenticates as "octocat"; anything
// else is 401. It records the last Authorization header so a test can assert the token traveled as
// a bearer header and never in a URL.
func stubGitHub(t *testing.T) (base string, lastAuth *string) {
	t.Helper()
	auth := ""
	lastAuth = &auth
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		bearer := strings.TrimPrefix(auth, "Bearer ")
		if bearer != "goodtok" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/octocat/hello":
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		case r.Method == "POST" && r.URL.Path == "/repos/octocat/hello/pulls":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/octocat/hello/pull/7"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/octocat/hello/pulls":
			if r.URL.Query().Get("state") == "open" {
				_, _ = w.Write([]byte(`[{"html_url":"https://github.com/octocat/hello/pull/3"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	oldBase := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = oldBase })
	return srv.URL, lastAuth
}

func newGitHubTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("LOOPRITE_MASTER_KEY", "") // deterministic: use the file, not an ambient env key
	db, err := state.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mk := filepath.Join(t.TempDir(), "master.key")
	if err := security.EnsureMasterKey(mk); err != nil {
		t.Fatal(err)
	}
	return &App{DB: db, Cfg: config.Config{MasterKeyPath: mk}}
}

func TestGitHubConnectVerifiesBeforeStoringAndNeverReturnsToken(t *testing.T) {
	_, lastAuth := stubGitHub(t)
	app := newGitHubTestApp(t)
	tok := mintToken(t, app, "proj")

	// A bad token is rejected and NOTHING is written to the vault.
	w := doAuthed(app, "POST", "/v1/github/connect", tok, map[string]any{"token": "wrongtok"}, app.HandleGitHubConnect)
	if w.Code != 400 {
		t.Fatalf("bad token: want 400, got %d (%s)", w.Code, w.Body.String())
	}
	if auth, _ := app.GitHubAuthFor("proj"); auth != nil {
		t.Fatal("a rejected token must not be stored")
	}

	// A good token verifies, seals, and reports the login — but never echoes the token.
	w = doAuthed(app, "POST", "/v1/github/connect", tok, map[string]any{"token": "goodtok"}, app.HandleGitHubConnect)
	if w.Code != 200 {
		t.Fatalf("good token: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "goodtok") {
		t.Fatalf("connect response leaked the token: %s", w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["connected"] != true || resp["login"] != "octocat" {
		t.Fatalf("unexpected connect response: %v", resp)
	}
	if !strings.HasPrefix(*lastAuth, "Bearer ") {
		t.Fatalf("token did not travel as a bearer header: %q", *lastAuth)
	}

	// GitHubAuthFor decrypts the real token back at call time.
	auth, err := app.GitHubAuthFor("proj")
	if err != nil || auth == nil {
		t.Fatalf("GitHubAuthFor: %v / %v", auth, err)
	}
	if auth.Token != "goodtok" || auth.Username != githubUsername {
		t.Fatalf("decrypted credential wrong: %+v", auth)
	}

	// Status returns login/host but never the token.
	w = doAuthed(app, "GET", "/v1/github/status", tok, nil, app.HandleGitHubStatus)
	if strings.Contains(w.Body.String(), "goodtok") {
		t.Fatalf("status leaked the token: %s", w.Body.String())
	}

	// Disconnect removes it, immediately reflected by GitHubAuthFor.
	w = doAuthed(app, "POST", "/v1/github/disconnect", tok, nil, app.HandleGitHubDisconnect)
	if w.Code != 200 {
		t.Fatalf("disconnect: want 200, got %d", w.Code)
	}
	if auth, _ := app.GitHubAuthFor("proj"); auth != nil {
		t.Fatal("credential should be gone after disconnect")
	}
}

func TestGitHubConnectRequiresAuthAndVault(t *testing.T) {
	stubGitHub(t)
	// Unauthenticated -> 401.
	app := newGitHubTestApp(t)
	if w := doAuthed(app, "POST", "/v1/github/connect", "", map[string]any{"token": "goodtok"}, app.HandleGitHubConnect); w.Code != 401 {
		t.Fatalf("unauthenticated connect: want 401, got %d", w.Code)
	}
	// No vault -> 400 vault_uninitialized, and still nothing stored.
	db, err := state.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	novault := &App{DB: db, Cfg: config.Config{MasterKeyPath: filepath.Join(t.TempDir(), "absent.key")}}
	tok := mintToken(t, novault, "proj")
	w := doAuthed(novault, "POST", "/v1/github/connect", tok, map[string]any{"token": "goodtok"}, novault.HandleGitHubConnect)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "vault") {
		t.Fatalf("no-vault connect: want 400 vault, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestGitHubCredentialIsProjectScoped(t *testing.T) {
	stubGitHub(t)
	app := newGitHubTestApp(t)
	tokA := mintToken(t, app, "projA")
	if w := doAuthed(app, "POST", "/v1/github/connect", tokA, map[string]any{"token": "goodtok"}, app.HandleGitHubConnect); w.Code != 200 {
		t.Fatalf("connect projA: %d", w.Code)
	}
	// projB has its own (empty) credential state — projA's connection must not leak into it.
	if auth, _ := app.GitHubAuthFor("projB"); auth != nil {
		t.Fatal("a credential connected under projA must not be visible to projB")
	}
}

func TestOwnerRepoFromURL(t *testing.T) {
	cases := []struct {
		url         string
		owner, repo string
		ok          bool
	}{
		{"https://github.com/octocat/hello.git", "octocat", "hello", true},
		{"https://github.com/octocat/hello", "octocat", "hello", true},
		{"https://github.com:443/octocat/hello.git", "octocat", "hello", true}, // explicit port

		{"git@github.com:octocat/hello.git", "octocat", "hello", true},
		{"ssh://git@github.com/octocat/hello.git", "octocat", "hello", true},
		{"https://gitlab.com/octocat/hello.git", "", "", false},
		{"https://github.com/octocat", "", "", false},
		{"not a url", "", "", false},
	}
	for _, c := range cases {
		o, rp, ok := ownerRepoFromURL(c.url)
		if ok != c.ok || o != c.owner || rp != c.repo {
			t.Errorf("ownerRepoFromURL(%q) = (%q,%q,%v), want (%q,%q,%v)", c.url, o, rp, ok, c.owner, c.repo, c.ok)
		}
	}
}

func TestGHCreateAndFindPR(t *testing.T) {
	stubGitHub(t)
	ctx := context.Background()
	url, err := ghCreatePR(ctx, "goodtok", "octocat", "hello", "l00prite/x", "main", "title", "body")
	if err != nil {
		t.Fatalf("ghCreatePR: %v", err)
	}
	if url != "https://github.com/octocat/hello/pull/7" {
		t.Fatalf("ghCreatePR url = %q", url)
	}
	if got := ghFindOpenPR(ctx, "goodtok", "octocat", "hello", "l00prite/x"); got != "https://github.com/octocat/hello/pull/3" {
		t.Fatalf("ghFindOpenPR = %q", got)
	}
	// A bad token fails PR creation with a client-safe message and no token in it.
	if _, err := ghCreatePR(ctx, "badtok", "octocat", "hello", "h", "main", "t", "b"); err == nil {
		t.Fatal("ghCreatePR with a bad token should error")
	} else if strings.Contains(err.Error(), "badtok") {
		t.Fatalf("error leaked the token: %v", err)
	}
	// Default-branch lookup.
	if b, err := ghDefaultBranch(ctx, "goodtok", "octocat", "hello"); err != nil || b != "main" {
		t.Fatalf("ghDefaultBranch = %q / %v", b, err)
	}
}
