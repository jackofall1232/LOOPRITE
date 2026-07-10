package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/security"
)

func doJSONHeader(t *testing.T, url, token string, headers map[string]string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp, bodyJSON(t, resp)
}

func errField(m map[string]any, key string) string {
	e, _ := m["error"].(map[string]any)
	if e == nil {
		return ""
	}
	s, _ := e[key].(string)
	return s
}

// TestRepoScopeFootgun walks the exact new-user story that used to dead-end: type something
// GitHub-shaped into the wizard's repo field, register the real repo from the dashboard, and
// then watch every prompt fail with a bare "repo not registered". The wizard must now refuse
// scopes that can never match a repo id, warn on well-formed-but-unregistered ones, and the
// prompt-time error must say the actual repair (and that GitHub permissions are not involved).
func TestRepoScopeFootgun(t *testing.T) {
	srv, _, db := unconfigured(t)
	base := srv.URL

	if resp, _ := doJSON(t, "POST", base+"/v1/setup/vault", "", map[string]any{}); resp.StatusCode != 200 {
		t.Fatalf("vault init failed: %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", base+"/v1/setup/provider", "", map[string]any{"name": "mock", "adapter": "mock", "default": true}); resp.StatusCode != 200 {
		t.Fatalf("provider add failed: %d", resp.StatusCode)
	}

	// 1. A scope that can never be a repo id (owner/repo, URLs) must be refused at mint time,
	//    not discovered one failed prompt at a time.
	for _, bad := range []string{"jackofall1232/LOOPRITE", "https://github.com/you/myrepo.git"} {
		resp, m := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "demo", "repo": bad})
		if resp.StatusCode != 400 || errField(m, "code") != "invalid_repo_scope" {
			t.Fatalf("repo scope %q must 400/invalid_repo_scope, got %d %v", bad, resp.StatusCode, m)
		}
	}

	// 2. A well-formed id that isn't registered yet mints, but carries an explicit warning.
	resp, tk := doJSON(t, "POST", base+"/v1/setup/token", "", map[string]any{"project": "demo", "repo": "ghost"})
	if resp.StatusCode != 200 {
		t.Fatalf("mint with unregistered-but-valid scope failed: %d %v", resp.StatusCode, tk)
	}
	warning, _ := tk["warning"].(string)
	if !strings.Contains(warning, `"ghost"`) || !strings.Contains(warning, "not registered") {
		t.Fatalf("mint must warn that the scope is unregistered, got %q", warning)
	}
	token, _ := tk["token"].(string)

	chatBody := map[string]any{"model": "demo-model", "messages": []map[string]any{{"role": "user", "content": "hi"}}}

	// 3. Prompting with the ghost-scoped token fails 404 — with the repair spelled out.
	resp, m := doJSON(t, "POST", base+"/v1/chat/completions", token, chatBody)
	msg := errField(m, "message")
	if resp.StatusCode != 404 || errField(m, "code") != "repo_not_found" {
		t.Fatalf("ghost-scoped chat must 404/repo_not_found, got %d %v", resp.StatusCode, m)
	}
	for _, want := range []string{`scoped to repo "ghost"`, `exactly the id "ghost"`, "GitHub permissions are not involved"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("token-scope 404 must contain %q, got %q", want, msg)
		}
	}

	// 4. Registering a repo with EXACTLY the scoped id heals the token — the mock provider answers.
	repoDir := t.TempDir()
	if resp, m := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "ghost", "root": repoDir}); resp.StatusCode != 200 {
		t.Fatalf("register ghost failed: %d %v", resp.StatusCode, m)
	}
	if resp, m := doJSON(t, "POST", base+"/v1/chat/completions", token, chatBody); resp.StatusCode != 200 {
		t.Fatalf("chat after registering the scoped id must succeed, got %d %v", resp.StatusCode, m)
	}

	// 5. An unscoped token naming an unregistered repo via header gets the header-flavored 404.
	_, unscoped, err := security.MintToken(db, "demo", nil, nil)
	if err != nil {
		t.Fatalf("mint unscoped: %v", err)
	}
	req := map[string]any{"model": "demo-model", "messages": []map[string]any{{"role": "user", "content": "hi"}}}
	resp2, m2 := doJSONHeader(t, base+"/v1/chat/completions", unscoped, map[string]string{"x-l00prite-repo": "nope"}, req)
	msg2 := errField(m2, "message")
	if resp2.StatusCode != 404 || errField(m2, "code") != "repo_not_found" {
		t.Fatalf("header repo 404 wrong: %d %v", resp2.StatusCode, m2)
	}
	for _, want := range []string{"Register it in the dashboard", "GitHub permissions are not involved"} {
		if !strings.Contains(msg2, want) {
			t.Fatalf("header 404 must contain %q, got %q", want, msg2)
		}
	}
}
