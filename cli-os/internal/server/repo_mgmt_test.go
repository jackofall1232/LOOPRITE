package server_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRow(t *testing.T, db *sql.DB, id string) (root, project string, found bool) {
	t.Helper()
	err := db.QueryRow(`SELECT root, project FROM repos WHERE id = ?`, id).Scan(&root, &project)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	if err != nil {
		t.Fatalf("repo row %q: %v", id, err)
	}
	return root, project, true
}

// TestRepoMgmtAuthRequired: the repo endpoints share the exact auth of every other management
// endpoint — no unauthenticated registration path.
func TestRepoMgmtAuthRequired(t *testing.T) {
	srv, _, _, _, _ := configured(t)
	for _, p := range []string{"/v1/repos", "/v1/repos/remove"} {
		if resp, _ := doJSON(t, "POST", srv.URL+p, "", map[string]any{"id": "x"}); resp.StatusCode != 401 {
			t.Fatalf("POST %s without token must be 401, got %d", p, resp.StatusCode)
		}
		if resp, _ := doJSON(t, "POST", srv.URL+p, "l00p_bogus_nope", map[string]any{"id": "x"}); resp.StatusCode != 401 {
			t.Fatalf("POST %s with a bad token must be 401, got %d", p, resp.StatusCode)
		}
	}
}

// TestRepoRegisterValidatesAndStores: a real directory registers (row identical in shape to CLI
// `repo register`, project defaulted to the ACTING TOKEN's project so the registering token can
// actually use the repo), the response reports a real memory-freshness snapshot, and bad input —
// missing id/root, unsafe id charset, a path that doesn't exist on the gateway host, a project
// other than the token's — is rejected with the row stored nowhere.
func TestRepoRegisterValidatesAndStores(t *testing.T) {
	srv, _, db, tokID, token := configured(t) // token project is "ops"
	base := srv.URL

	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".l00prite"), 0o755); err != nil {
		t.Fatalf("mk memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".l00prite", "memory.md"), []byte("# memory\n"), 0o644); err != nil {
		t.Fatalf("write memory file: %v", err)
	}

	// happy path: registered, project defaulted to the token's own project, memory snapshot sees
	// the file.
	resp, body := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "myrepo", "root": repoDir})
	if resp.StatusCode != 200 {
		t.Fatalf("register must be 200, got %d (%v)", resp.StatusCode, body)
	}
	repo := body["repo"].(map[string]any)
	if repo["project"] != "ops" {
		t.Fatalf("project must default to the acting token's project (\"ops\"), got %v", repo["project"])
	}
	mem := body["memory"].(map[string]any)
	if mem["present_count"].(float64) < 1 {
		t.Fatalf("freshness snapshot must see the memory file, got %v", mem)
	}
	if root, project, found := repoRow(t, db, "myrepo"); !found || root != repoDir || project != "ops" {
		t.Fatalf("stored row mismatch: root=%q project=%q found=%v", root, project, found)
	}

	// a project OTHER than the token's is refused (403) — the register endpoint must not be able
	// to re-home a directory across the request-time project-scope gate.
	if resp, _ := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "other", "root": repoDir, "project": "someone-else"}); resp.StatusCode != 403 {
		t.Fatalf("cross-project register must be 403, got %d", resp.StatusCode)
	}
	if _, _, found := repoRow(t, db, "other"); found {
		t.Fatal("a 403 cross-project register must store nothing")
	}
	// the register is audit-logged with the acting token id (and the rejected cross-project
	// attempt above was not).
	var auditN int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action='repo.register' AND actor=?`, tokID).Scan(&auditN); err != nil || auditN != 1 {
		t.Fatalf("register must be audited by token id (n=%d, err=%v)", auditN, err)
	}

	// ... while explicitly naming the token's own project is fine.
	if resp, _ := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "explicit", "root": repoDir, "project": "ops"}); resp.StatusCode != 200 {
		t.Fatalf("same-project explicit register must be 200, got %d", resp.StatusCode)
	}

	// duplicate id → 409, row untouched.
	if resp, _ := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "myrepo", "root": t.TempDir()}); resp.StatusCode != 409 {
		t.Fatalf("duplicate id must be 409, got %d", resp.StatusCode)
	}
	if root, _, _ := repoRow(t, db, "myrepo"); root != repoDir {
		t.Fatalf("409 must not repoint the repo; root=%q", root)
	}

	// invalid input: missing id, unsafe id, missing root, nonexistent root — nothing stored.
	bad := []map[string]any{
		{"root": repoDir},
		{"id": "../evil", "root": repoDir},
		{"id": "norepo"},
		{"id": "norepo", "root": filepath.Join(repoDir, "does-not-exist")},
	}
	for _, b := range bad {
		if resp, _ := doJSON(t, "POST", base+"/v1/repos", token, b); resp.StatusCode != 400 {
			t.Fatalf("bad register %v must be 400, got %d", b, resp.StatusCode)
		}
	}
	if _, _, found := repoRow(t, db, "norepo"); found {
		t.Fatal("a rejected register must store nothing")
	}
}

// TestRepoRegisterAutoScaffoldsL00prite (Bug 1 of the 2026-07-11 control-plane diagnosis): linking
// a repo that has no .l00prite/ folder at all gets one scaffolded immediately, so memory injection
// has something to inject from the moment of registration instead of silently no-op'ing until the
// repo's first engine Run. Scaffolding is idempotent: an existing file (e.g. a hand-authored or
// Planning-Mode-committed memory.md) is never overwritten, only the files that were actually
// missing get created.
func TestRepoRegisterAutoScaffoldsL00prite(t *testing.T) {
	srv, _, _, _, token := configured(t)
	base := srv.URL

	t.Run("no .l00prite/ at all: fully scaffolded", func(t *testing.T) {
		repoDir := t.TempDir()
		resp, body := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "bare", "root": repoDir, "project": "ops"})
		if resp.StatusCode != 200 {
			t.Fatalf("register must be 200, got %d (%v)", resp.StatusCode, body)
		}
		for _, rel := range []string{"blueprint.md", "memory.md", "todos.md", "failures.md", "constraints.md", "heartbeat.json", "state.json", "lock.json"} {
			if _, err := os.Stat(filepath.Join(repoDir, ".l00prite", rel)); err != nil {
				t.Fatalf(".l00prite/%s should have been scaffolded, got: %v", rel, err)
			}
		}
		note, _ := body["note"].(string)
		if !strings.Contains(note, "memory folder was added") {
			t.Fatalf("expected a plain-language note about the new memory folder, got: %q", note)
		}
		mem := body["memory"].(map[string]any)
		if mem["present_count"].(float64) < 1 {
			t.Fatalf("freshness snapshot must see the freshly scaffolded files, got %v", mem)
		}

		// Adversarial-review finding: Scaffold's project-identity argument must be the repo's OWN
		// name (matching the engine's own preflight.go convention), not the gateway's multi-tenant
		// project scope -- otherwise every repo registered under the same project (here "ops")
		// gets the identical, indistinguishable project_name baked into its own state.json.
		stateBytes, err := os.ReadFile(filepath.Join(repoDir, ".l00prite", "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(stateBytes, &state); err != nil {
			t.Fatal(err)
		}
		wantName := filepath.Base(repoDir)
		if state["project_name"] != wantName {
			t.Fatalf("state.json project_name = %v, want the repo's own name %q (not the gateway project \"ops\")", state["project_name"], wantName)
		}
	})

	t.Run("existing .l00prite/ file is never overwritten", func(t *testing.T) {
		repoDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repoDir, ".l00prite"), 0o755); err != nil {
			t.Fatal(err)
		}
		custom := "# My Own Memory\nHand-written before linking.\n"
		if err := os.WriteFile(filepath.Join(repoDir, ".l00prite", "memory.md"), []byte(custom), 0o644); err != nil {
			t.Fatal(err)
		}
		resp, body := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "haspartial", "root": repoDir, "project": "ops"})
		if resp.StatusCode != 200 {
			t.Fatalf("register must be 200, got %d (%v)", resp.StatusCode, body)
		}
		got, err := os.ReadFile(filepath.Join(repoDir, ".l00prite", "memory.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != custom {
			t.Fatalf("scaffold must never overwrite an existing file; memory.md changed to: %q", string(got))
		}
		// The files that WERE missing still get filled in alongside the untouched one.
		if _, err := os.Stat(filepath.Join(repoDir, ".l00prite", "blueprint.md")); err != nil {
			t.Fatalf("blueprint.md should still have been scaffolded, got: %v", err)
		}
	})
}

// TestRepoRemove: removing unregisters the mapping (files untouched), 404s on an unknown id, and
// reports how many active tokens were scoped to the repo.
func TestRepoRemove(t *testing.T) {
	srv, _, db, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()

	if resp, _ := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "gone", "root": repoDir, "project": "ops"}); resp.StatusCode != 200 {
		t.Fatalf("register failed: %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", base+"/v1/repos/remove", token, map[string]any{"id": "nope"}); resp.StatusCode != 404 {
		t.Fatalf("unknown id must be 404, got %d", resp.StatusCode)
	}
	// malformed input (blank id) is a 400, not a confusing `404 No repo named ""`.
	if resp, _ := doJSON(t, "POST", base+"/v1/repos/remove", token, map[string]any{"id": "  "}); resp.StatusCode != 400 {
		t.Fatalf("blank id must be 400, got %d", resp.StatusCode)
	}
	resp, body := doJSON(t, "POST", base+"/v1/repos/remove", token, map[string]any{"id": "gone"})
	if resp.StatusCode != 200 || body["removed"] != true {
		t.Fatalf("remove must succeed, got %d (%v)", resp.StatusCode, body)
	}
	if _, _, found := repoRow(t, db, "gone"); found {
		t.Fatal("removed repo must be gone from the table")
	}
	if _, err := os.Stat(repoDir); err != nil {
		t.Fatalf("remove must not touch the directory on disk: %v", err)
	}
}
