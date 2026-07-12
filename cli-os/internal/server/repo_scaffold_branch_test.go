package server_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- git test helpers (this package has no existing one; internal/engine's and internal/gitx's
// are unexported in different packages, so this mirrors their shape) ----

func gitRunSrv(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// initGitRepoWithCommit creates a real git repo with one commit — HandleRepoScaffoldBranch
// requires a repository with at least one commit (a fresh unborn HEAD cannot be branched).
// Skips (not fails) when no git binary is on PATH, matching every other git-dependent test in
// this codebase (internal/engine/tools_test.go, internal/gitx/gitx_test.go, runs_api_test.go,
// chatrun_api_test.go) — the gateway itself falls back to the pure-Go gogit client without a
// git binary (Android ships neither git nor ssh), but these tests shell out to the real binary
// directly to build fixtures, so a git-less CI/dev environment must not see them as failures.
func initGitRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	gitRunSrv(t, dir, "init", "-b", "main")
	gitRunSrv(t, dir, "config", "user.email", "test@example.com")
	gitRunSrv(t, dir, "config", "user.name", "l00prite test")
	gitRunSrv(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunSrv(t, dir, "add", "-A")
	gitRunSrv(t, dir, "commit", "-m", "initial commit")
}

// TestRepoScaffoldBranchAuthRequired: same single-tier auth as every other management endpoint.
func TestRepoScaffoldBranchAuthRequired(t *testing.T) {
	srv, _, _, _, _ := configured(t)
	if resp, _ := doJSON(t, "POST", srv.URL+"/v1/repos/scaffold-branch", "", map[string]any{"id": "x"}); resp.StatusCode != 401 {
		t.Fatalf("without token must be 401, got %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", srv.URL+"/v1/repos/scaffold-branch", "l00p_bogus_nope", map[string]any{"id": "x"}); resp.StatusCode != 401 {
		t.Fatalf("with a bad token must be 401, got %d", resp.StatusCode)
	}
}

func TestRepoScaffoldBranchUnregisteredRepo404s(t *testing.T) {
	srv, _, _, _, token := configured(t)
	resp, body := doJSON(t, "POST", srv.URL+"/v1/repos/scaffold-branch", token, map[string]any{"id": "never-registered"})
	if resp.StatusCode != 404 {
		t.Fatalf("unregistered repo id must 404, got %d (%v)", resp.StatusCode, body)
	}
}

// TestRepoScaffoldBranchWritesFullProtocolOnANewBranch: the maintainer-requested "full benefit"
// action — creates a real git branch, writes AGENTS.md/CLAUDE.md/prompts/adapters (everything
// the silent auto-scaffold on register does NOT write), commits it, and reports it as
// already_complete on a second call.
func TestRepoScaffoldBranchWritesFullProtocolOnANewBranch(t *testing.T) {
	srv, _, _, _, token := configured(t)
	base := srv.URL

	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	originalBranch := strings.TrimSpace(gitRunSrv(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))

	if resp, body := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "fullproto", "root": repoDir, "project": "ops"}); resp.StatusCode != 200 {
		t.Fatalf("register must be 200, got %d (%v)", resp.StatusCode, body)
	}

	resp, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "fullproto"})
	if resp.StatusCode != 200 {
		t.Fatalf("scaffold-branch must be 200, got %d (%v)", resp.StatusCode, body)
	}
	if body["already_complete"] != false {
		t.Fatalf("expected already_complete=false on first call, got %v", body)
	}
	branch, _ := body["branch"].(string)
	if branch == "" || !strings.HasPrefix(branch, "l00prite/add-protocol-") {
		t.Fatalf("expected a real l00prite/add-protocol-* branch name, got %v", body["branch"])
	}
	if body["branched_from"] != originalBranch {
		t.Fatalf("branched_from = %v, want %q", body["branched_from"], originalBranch)
	}
	if body["commit"] == nil || body["commit"] == "" {
		t.Fatalf("expected a real commit hash, got %v", body["commit"])
	}
	filesCreated, _ := body["files_created"].([]any)
	if len(filesCreated) == 0 {
		t.Fatalf("expected files_created to be non-empty, got %v", body)
	}
	pushInstr, _ := body["push_instructions"].(string)
	if !strings.Contains(pushInstr, "git push -u origin "+branch) {
		t.Fatalf("push_instructions should mention the real branch name, got %q", pushInstr)
	}

	// The branch and commit are REAL git state, not just a claim in the JSON response.
	branches := gitRunSrv(t, repoDir, "branch", "--list", branch)
	if !strings.Contains(branches, branch) {
		t.Fatalf("branch %q was not actually created in the repo: %q", branch, branches)
	}
	currentBranch := strings.TrimSpace(gitRunSrv(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))
	if currentBranch != branch {
		t.Fatalf("repo should be checked out on the new branch %q, got %q", branch, currentBranch)
	}
	for _, rel := range []string{"AGENTS.md", "CLAUDE.md", ".l00prite/prompts/execute-loop.md", "GEMINI.md", ".github/copilot-instructions.md"} {
		if _, err := os.Stat(filepath.Join(repoDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected %s to exist after scaffold-branch: %v", rel, err)
		}
	}
	status := gitRunSrv(t, repoDir, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("working tree should be clean after the commit, got: %q", status)
	}

	// Second call: nothing left to add, so it must be a no-op that never touches git again.
	resp2, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "fullproto"})
	if resp2.StatusCode != 200 {
		t.Fatalf("second scaffold-branch must be 200, got %d (%v)", resp2.StatusCode, body2)
	}
	if body2["already_complete"] != true {
		t.Fatalf("expected already_complete=true on the second call, got %v", body2)
	}
	branchList := gitRunSrv(t, repoDir, "branch", "--list", "l00prite/add-protocol-*")
	if strings.Count(branchList, "l00prite/add-protocol-") != 1 {
		t.Fatalf("expected exactly one add-protocol branch (no pointless second branch), got: %q", branchList)
	}
}
