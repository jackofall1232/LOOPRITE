package server_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- test-only `gh` stub ----
//
// These tests exercise the REAL push+PR flow end to end against a real local git remote (a bare
// repo on disk -- pushing to a plain filesystem path needs no network and no real GitHub account),
// but there is no real `gh` binary or GitHub network access available in this sandbox, so `gh`
// itself is stubbed: a tiny shell script prepended to PATH that mimics `gh auth status` and
// `gh pr create`'s observable contract (exit code, and for pr create, printing a PR URL on
// stdout) without touching the network. What this CANNOT verify live in this environment: that a
// REAL gh binary authenticated against a REAL GitHub account behaves identically -- see this
// file's package-level doc comment below and the final report for the explicit statement of that
// gap.

// writeStubGH writes a fake `gh` executable to a fresh temp dir and returns that dir (to be
// prepended to PATH). authFails makes `gh auth status` exit non-zero (simulating a host where gh
// is installed but never signed in); prURL is what `gh pr create` prints on stdout on success.
func writeStubGH(t *testing.T, authFails bool, prURL string) string {
	t.Helper()
	dir := t.TempDir()
	authExit := "0"
	if authFails {
		authExit = "1"
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"auth\" ] && [ \"$2\" = \"status\" ]; then exit " + authExit + "; fi\n" +
		"if [ \"$1\" = \"pr\" ] && [ \"$2\" = \"create\" ]; then echo \"" + prURL + "\"; exit 0; fi\n" +
		"exit 1\n"
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh: %v", err)
	}
	return dir
}

// prependToPATH puts dir first on PATH for the duration of the test, keeping the rest of PATH
// (so the real `git` binary this whole suite depends on is still found).
func prependToPATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// pathWithOnlyGit isolates PATH down to just the directory containing the real `git` binary --
// used by the fail-closed "no gh installed" test so it's deterministic regardless of whether this
// dev/CI machine happens to have a real gh installed somewhere else on PATH. Skips (not fails) if
// git itself isn't available (matches every other git-dependent test in this file) or if, after
// isolating, gh is STILL found (an unusual host that colocates git and gh in the same directory --
// rare, but the test must not silently pass for the wrong reason in that case).
func pathWithOnlyGit(t *testing.T) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	t.Setenv("PATH", filepath.Dir(gitPath))
	if _, err := exec.LookPath("gh"); err == nil {
		t.Skip("a real gh binary is colocated with git on this host's PATH -- cannot isolate for this test")
	}
}

// initBareOrigin creates a bare git repository -- a real, valid git push target that needs no
// network access, letting the happy-path test below exercise a REAL `git push` end to end in this
// sandbox. Returns its filesystem path (usable directly as a git remote URL).
func initBareOrigin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRunSrv(t, dir, "init", "--bare", "-b", "main")
	return dir
}

// scaffoldRepoWithOrigin registers repoDir (created with initGitRepoWithCommit) under id and
// points its "origin" remote at a fresh bare repo, returning the bare repo's path. Shared setup
// for every test below that needs a real, pushable remote.
func scaffoldRepoWithOrigin(t *testing.T, base, token, id, repoDir string) (bareDir string) {
	t.Helper()
	bareDir = initBareOrigin(t)
	gitRunSrv(t, repoDir, "remote", "add", "origin", bareDir)
	if resp, body := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": id, "root": repoDir, "project": "ops"}); resp.StatusCode != 200 {
		t.Fatalf("register must be 200, got %d (%v)", resp.StatusCode, body)
	}
	return bareDir
}

// TestRepoScaffoldBranchOpenPRHappyPath (plan step 4a): a real branch push against a real local
// bare-repo remote, then a real (stubbed) `gh pr create` call, then a SECOND commit+push recording
// the PR URL in the repo's own ledger.md -- end to end, with the response, the remote's actual
// refs, the working tree's ledger content, and the working tree's cleanliness all checked
// independently rather than trusting the JSON response alone.
func TestRepoScaffoldBranchOpenPRHappyPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)

	const wantPRURL = "https://github.com/example/l00prite-test/pull/7"
	prependToPATH(t, writeStubGH(t, false, wantPRURL))

	bareDir := scaffoldRepoWithOrigin(t, base, token, "prhappy", repoDir)

	resp, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "prhappy", "open_pr": true})
	if resp.StatusCode != 200 {
		t.Fatalf("scaffold-branch must be 200, got %d (%v)", resp.StatusCode, body)
	}
	if body["pushed"] != true {
		t.Fatalf("expected pushed=true, got %v", body)
	}
	if body["pr_url"] != wantPRURL {
		t.Fatalf("expected pr_url=%q, got %v", wantPRURL, body["pr_url"])
	}
	if body["capability_gap"] != nil {
		t.Fatalf("expected no capability_gap on the happy path, got %v", body["capability_gap"])
	}
	branch, _ := body["branch"].(string)
	if branch == "" {
		t.Fatalf("expected a real branch name, got %v", body)
	}

	// The branch must be REAL on the remote, not just claimed in the response.
	branches := gitRunSrv(t, bareDir, "branch", "--list", branch)
	if !strings.Contains(branches, branch) {
		t.Fatalf("branch %q was not actually pushed to origin: %q", branch, branches)
	}
	// TWO commits must have landed on the remote: the protocol commit AND the follow-up ledger
	// commit recording the PR URL -- the ledger append is its own commit, not folded into the
	// first (see repo_scaffold.go's comment on why that ordering matters).
	log := gitRunSrv(t, bareDir, "log", "--oneline", branch)
	if !strings.Contains(log, "Add l00prite protocol") {
		t.Fatalf("expected the protocol commit on the pushed branch, got log: %q", log)
	}
	if !strings.Contains(log, "Record PR URL in l00prite ledger") {
		t.Fatalf("expected a SEPARATE ledger-URL commit on the pushed branch, got log: %q", log)
	}

	// The local working tree's ledger.md must actually contain the PR URL, and the tree must be
	// clean -- an uncommitted ledger write here would be exactly the dirty-tree-on-return bug
	// class this repo has already fixed three times.
	ledgerBytes, err := os.ReadFile(filepath.Join(repoDir, ".l00prite", "ledger.md"))
	if err != nil {
		t.Fatalf("read ledger.md: %v", err)
	}
	if !strings.Contains(string(ledgerBytes), wantPRURL) {
		t.Fatalf("ledger.md does not mention the PR URL %q:\n%s", wantPRURL, ledgerBytes)
	}
	status := gitRunSrv(t, repoDir, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("working tree should be clean after both commits, got: %q", status)
	}
}

// TestRepoScaffoldBranchOpenPRFailsClosedWithoutGH (plan step 4b): gh not on PATH must produce a
// fixed capability_gap and, critically, must NEVER push anything to the remote -- a half-pushed,
// no-PR state is exactly what this design is required to avoid.
func TestRepoScaffoldBranchOpenPRFailsClosedWithoutGH(t *testing.T) {
	pathWithOnlyGit(t) // isolates PATH to just git -- also skips if git itself is unavailable
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	bareDir := scaffoldRepoWithOrigin(t, base, token, "prnogh", repoDir)

	resp, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "prnogh", "open_pr": true})
	if resp.StatusCode != 200 {
		t.Fatalf("scaffold-branch must still be 200 (local scaffold succeeded even though push+PR did not), got %d (%v)", resp.StatusCode, body)
	}
	if body["pushed"] != false {
		t.Fatalf("expected pushed=false with no gh on PATH, got %v", body)
	}
	if body["pr_url"] != nil {
		t.Fatalf("expected no pr_url with no gh on PATH, got %v", body["pr_url"])
	}
	gap, _ := body["capability_gap"].(string)
	if gap == "" {
		t.Fatalf("expected a non-empty capability_gap, got %v", body)
	}
	if !strings.Contains(gap, "GitHub CLI") {
		t.Fatalf("expected the fixed no-gh sentence, got %q", gap)
	}
	assertNoRawGitTextLeaked(t, body)

	// Nothing must have been pushed to the remote at all.
	branches := gitRunSrv(t, bareDir, "branch", "--list")
	if strings.TrimSpace(branches) != "" {
		t.Fatalf("expected NO branches pushed to origin when gh is unavailable, got: %q", branches)
	}
}

// TestRepoScaffoldBranchOpenPRFailsClosedOnBadOrigin (plan step 4c): a repo with no (or a broken)
// "origin" remote must fail closed at the dry-run push probe, with a fixed message -- never a raw
// git error, and never an actual push attempt.
func TestRepoScaffoldBranchOpenPRFailsClosedOnBadOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	// Deliberately NO `git remote add origin` -- gh itself is stubbed to succeed so the flow
	// reaches (and fails at) the push --dry-run step specifically, not an earlier gh check.
	prependToPATH(t, writeStubGH(t, false, "https://github.com/example/unused/pull/1"))
	if resp, body := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "prbadorigin", "root": repoDir, "project": "ops"}); resp.StatusCode != 200 {
		t.Fatalf("register must be 200, got %d (%v)", resp.StatusCode, body)
	}

	resp, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "prbadorigin", "open_pr": true})
	if resp.StatusCode != 200 {
		t.Fatalf("scaffold-branch must still be 200, got %d (%v)", resp.StatusCode, body)
	}
	if body["pushed"] != false {
		t.Fatalf("expected pushed=false with no origin remote, got %v", body)
	}
	gap, _ := body["capability_gap"].(string)
	if gap == "" {
		t.Fatalf("expected a non-empty capability_gap, got %v", body)
	}
	if !strings.Contains(gap, "Pushing to origin isn't possible") {
		t.Fatalf("expected the fixed dry-run-gap sentence, got %q", gap)
	}
	assertNoRawGitTextLeaked(t, body)
}

// TestRepoScaffoldBranchOpenPROmittedIsUnchanged (plan step 4d): omitting open_pr entirely must
// produce a response with EXACTLY the same keys as before this feature existed -- no pushed/
// pr_url/capability_gap/pr_command keys at all -- so any pre-existing automation hitting this
// endpoint (which never sent open_pr and never will) sees zero shape change.
func TestRepoScaffoldBranchOpenPROmittedIsUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	if resp, body := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "proff", "root": repoDir, "project": "ops"}); resp.StatusCode != 200 {
		t.Fatalf("register must be 200, got %d (%v)", resp.StatusCode, body)
	}

	resp, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "proff"})
	if resp.StatusCode != 200 {
		t.Fatalf("scaffold-branch must be 200, got %d (%v)", resp.StatusCode, body)
	}
	for _, forbidden := range []string{"pushed", "pr_url", "capability_gap", "pr_command"} {
		if _, present := body[forbidden]; present {
			t.Fatalf("open_pr omitted must never add %q to the response, got: %v", forbidden, body)
		}
	}
	wantKeys := []string{"already_complete", "branch", "branched_from", "commit", "files_created", "claude_md_skipped", "push_instructions", "notes"}
	if len(body) != len(wantKeys) {
		t.Fatalf("expected exactly %d keys (today's response shape), got %d: %v", len(wantKeys), len(body), body)
	}
	for _, k := range wantKeys {
		if _, present := body[k]; !present {
			t.Fatalf("expected key %q in today's response shape, missing from: %v", k, body)
		}
	}

	// A repeat with open_pr explicitly false must behave identically (both are "no permission").
	repoDir2 := t.TempDir()
	initGitRepoWithCommit(t, repoDir2)
	if resp, body := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "proff2", "root": repoDir2, "project": "ops"}); resp.StatusCode != 200 {
		t.Fatalf("register must be 200, got %d (%v)", resp.StatusCode, body)
	}
	resp2, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "proff2", "open_pr": false})
	if resp2.StatusCode != 200 {
		t.Fatalf("scaffold-branch must be 200, got %d (%v)", resp2.StatusCode, body2)
	}
	for _, forbidden := range []string{"pushed", "pr_url", "capability_gap", "pr_command"} {
		if _, present := body2[forbidden]; present {
			t.Fatalf("open_pr:false must never add %q to the response, got: %v", forbidden, body2)
		}
	}
}

// assertNoRawGitTextLeaked checks the response body's JSON-encoded form for git/gh's own
// distinctive stderr vocabulary -- the exact leak class humanizeStartError (gateway/runs.go) and
// preflight.go's Blocker strings already close for the run-engine path; this pins the same
// property for the new capability_gap field.
func assertNoRawGitTextLeaked(t *testing.T, body map[string]any) {
	t.Helper()
	blob := ""
	for k, v := range body {
		if s, ok := v.(string); ok {
			blob += k + "=" + s + " "
		}
	}
	for _, rawFragment := range []string{"fatal:", "exit status", "does not appear to be a git repository"} {
		if strings.Contains(blob, rawFragment) {
			t.Fatalf("response leaked raw git/gh error text (%q) to the client: %v", rawFragment, body)
		}
	}
}
