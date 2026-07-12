package server_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover the "Add l00prite" RETRY story (repo_scaffold.go + repo_scaffold_state.go): a
// first attempt creates+commits a branch locally and then hits a push/PR capability gap; a later
// click must be able to retry the push/PR instead of no-opping on the already_complete check. Each
// test's doc comment states the exact pre-fix symptom it was CONFIRMED to fail against (by reverting
// the fix locally, observing that symptom, then restoring) — see the final report for the record.
//
// They reuse the harness in repo_scaffold_branch_pr_test.go (writeStubGH, writeStubGHPRCreateFails,
// prependToPATH, pathWithOnlyGit, scaffoldRepoWithOrigin, initGitRepoWithCommit, gitRunSrv) and skip
// when git is unavailable, matching every git-dependent test in this package.

// writeStubGHWithPRView is a `gh` whose `auth status` succeeds (so the capability probe passes) and
// whose `pr view` prints prViewURL (simulating an already-open PR for the branch). Its `pr create`
// prints a DELIBERATELY DIFFERENT sentinel URL so a test can prove the existing-PR short-circuit
// ran (pr_url == prViewURL) rather than a fresh create — and prove no second PR was created.
func writeStubGHWithPRView(t *testing.T, prViewURL string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"auth\" ] && [ \"$2\" = \"status\" ]; then exit 0; fi\n" +
		"if [ \"$1\" = \"pr\" ] && [ \"$2\" = \"view\" ]; then echo \"" + prViewURL + "\"; exit 0; fi\n" +
		"if [ \"$1\" = \"pr\" ] && [ \"$2\" = \"create\" ]; then echo \"https://github.com/SHOULD/NOT/pull/999\"; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh: %v", err)
	}
	return dir
}

// TestRepoScaffoldBranchRetryAfterCapabilityGapReachesPushPR is THE core regression: a first
// open_pr=true attempt with no gh gaps out AFTER the local branch+commit; fixing the gap and
// clicking again must actually push+PR the SAME branch, not no-op.
//
// Pre-fix symptom (confirmed): call 2 returns {"already_complete": true} — the protocol files from
// attempt 1 make FullProtocolGaps() report the repo complete, so the handler short-circuited before
// ever re-reaching the push/PR logic.
func TestRepoScaffoldBranchRetryAfterCapabilityGapReachesPushPR(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	bareDir := scaffoldRepoWithOrigin(t, base, token, "retrygap", repoDir)

	// Call 1: open_pr=true but gh is not on PATH -> a gap AFTER the local branch+commit.
	pathWithOnlyGit(t)
	resp, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "retrygap", "open_pr": true})
	if resp.StatusCode != 200 {
		t.Fatalf("call 1 must be 200, got %d (%v)", resp.StatusCode, body)
	}
	gap1, _ := body["capability_gap"].(string)
	if !strings.Contains(gap1, "GitHub CLI") {
		t.Fatalf("call 1 expected the no-gh gap, got %q (%v)", gap1, body)
	}
	b1, _ := body["branch"].(string)
	if b1 == "" {
		t.Fatalf("call 1 must report a branch, got %v", body)
	}

	// Call 2: fix the gap (a working stub gh now on PATH) and retry.
	const wantPRURL = "https://github.com/example/retry/pull/7"
	prependToPATH(t, writeStubGH(t, false, wantPRURL))
	resp2, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "retrygap", "open_pr": true})
	if resp2.StatusCode != 200 {
		t.Fatalf("call 2 must be 200, got %d (%v)", resp2.StatusCode, body2)
	}
	if body2["already_complete"] == true {
		t.Fatalf("PRE-FIX SYMPTOM: call 2 no-op'd with already_complete=true instead of retrying: %v", body2)
	}
	if body2["retry"] != true {
		t.Fatalf("call 2 must be marked retry=true, got %v", body2)
	}
	if body2["branch"] != b1 {
		t.Fatalf("call 2 must REUSE branch %q, got %v", b1, body2["branch"])
	}
	if body2["pushed"] != true {
		t.Fatalf("call 2 must have pushed, got %v", body2)
	}
	if body2["pr_url"] != wantPRURL {
		t.Fatalf("call 2 must report pr_url=%q, got %v", wantPRURL, body2["pr_url"])
	}
	if body2["capability_gap"] != nil {
		t.Fatalf("call 2 must have no capability_gap on success, got %v", body2["capability_gap"])
	}
	// branched_from must be nil on a retry (the branch already existed).
	if body2["branched_from"] != nil {
		t.Fatalf("retry must report branched_from=nil, got %v", body2["branched_from"])
	}
	// The branch REALLY reached origin.
	if out := gitRunSrv(t, bareDir, "branch", "--list", b1); !strings.Contains(out, b1) {
		t.Fatalf("branch %q was not actually pushed to origin: %q", b1, out)
	}
}

// TestRepoScaffoldBranchRetryWithoutOpenPRNeverPushes: a pending attempt plus open_pr OMITTED must
// return a read-only note (naming the branch + a push command), perform zero git writes and zero
// network calls, and KEEP the pending row so a later CONSENTED click can still retry — persisted
// state is never standing push permission.
//
// Pre-fix symptom (confirmed): call 2's branch is null and its note is the generic "nothing to add"
// text — the pending branch (and the fact a push is still needed) is completely hidden.
func TestRepoScaffoldBranchRetryWithoutOpenPRNeverPushes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	bareDir := scaffoldRepoWithOrigin(t, base, token, "retrynopush", repoDir)

	// Call 1: gap (no gh) -> pending row on B1.
	pathWithOnlyGit(t)
	_, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "retrynopush", "open_pr": true})
	b1, _ := body["branch"].(string)
	if b1 == "" {
		t.Fatalf("call 1 must report a branch, got %v", body)
	}

	// Call 2: open_pr OMITTED entirely.
	resp2, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "retrynopush"})
	if resp2.StatusCode != 200 {
		t.Fatalf("call 2 must be 200, got %d (%v)", resp2.StatusCode, body2)
	}
	if body2["already_complete"] != true {
		t.Fatalf("call 2 (no permission) must report already_complete=true, got %v", body2)
	}
	if body2["branch"] != b1 {
		t.Fatalf("PRE-FIX SYMPTOM: call 2 must NAME the pending branch %q, got %v", b1, body2["branch"])
	}
	note, _ := body2["note"].(string)
	if !strings.Contains(note, b1) || !strings.Contains(note, "git push -u origin") {
		t.Fatalf("call 2 note must name the branch and give a push command, got %q", note)
	}
	// No push key / no pr_url on a read-only pending response.
	if _, present := body2["pushed"]; present {
		t.Fatalf("no-permission pending response must not carry a pushed key, got %v", body2)
	}
	// Nothing must have reached origin.
	if out := gitRunSrv(t, bareDir, "branch", "--list"); strings.TrimSpace(out) != "" {
		t.Fatalf("open_pr omitted must push NOTHING, but origin has: %q", out)
	}

	// Call 3: the row must have survived call 2 — a consented retry still works.
	prependToPATH(t, writeStubGH(t, false, "https://github.com/example/retrynopush/pull/3"))
	_, body3 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "retrynopush", "open_pr": true})
	if body3["retry"] != true || body3["branch"] != b1 {
		t.Fatalf("call 3 must still retry the same branch %q, got %v", b1, body3)
	}
	if body3["pr_url"] == nil {
		t.Fatalf("call 3 must open the PR, got %v", body3)
	}
}

// TestRepoScaffoldBranchRetryFindsExistingPRWithoutPushing: if the operator opened the PR by hand
// (via the fallback command) between attempts, a retry must find it (read-only `gh pr view`),
// report success with the REAL URL, and never push again or run `gh pr create` (which would produce
// a duplicate/error).
//
// Pre-fix symptom (confirmed): call 2 returns already_complete and never surfaces the existing PR.
func TestRepoScaffoldBranchRetryFindsExistingPRWithoutPushing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	scaffoldRepoWithOrigin(t, base, token, "retryexisting", repoDir)

	// Call 1: push succeeds but `gh pr create` fails -> gapPRCreate, row marked pushed=1.
	prependToPATH(t, writeStubGHPRCreateFails(t))
	_, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "retryexisting", "open_pr": true})
	if body["pushed"] != true {
		t.Fatalf("call 1 push must have landed, got %v", body)
	}
	gap1, _ := body["capability_gap"].(string)
	if !strings.Contains(gap1, "pushed to origin") {
		t.Fatalf("call 1 expected the pushed-but-PR-create-failed gap, got %q", gap1)
	}
	b1, _ := body["branch"].(string)

	// Call 2: a PR for the branch now exists (operator opened it by hand). The stub's `pr view`
	// returns the real URL; its `pr create` returns a sentinel we must NEVER see.
	const existingURL = "https://github.com/example/retryexisting/pull/11"
	prependToPATH(t, writeStubGHWithPRView(t, existingURL))
	resp2, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "retryexisting", "open_pr": true})
	if resp2.StatusCode != 200 {
		t.Fatalf("call 2 must be 200, got %d (%v)", resp2.StatusCode, body2)
	}
	if body2["already_complete"] == true {
		t.Fatalf("PRE-FIX SYMPTOM: call 2 no-op'd with already_complete instead of surfacing the existing PR: %v", body2)
	}
	if body2["retry"] != true {
		t.Fatalf("call 2 must be a retry, got %v", body2)
	}
	if body2["pr_url"] != existingURL {
		t.Fatalf("call 2 must surface the EXISTING PR url %q (never the create sentinel), got %v", existingURL, body2["pr_url"])
	}
	if body2["pushed"] != false {
		t.Fatalf("call 2 must NOT push again when a PR already exists, got pushed=%v", body2["pushed"])
	}
	if body2["capability_gap"] != nil {
		t.Fatalf("call 2 must have no capability_gap (mutually exclusive with pr_url), got %v", body2["capability_gap"])
	}

	// Call 3: the row was cleared on success in call 2 -> plain already_complete, no retry.
	prependToPATH(t, writeStubGH(t, false, "unused"))
	_, body3 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "retryexisting", "open_pr": true})
	if body3["already_complete"] != true {
		t.Fatalf("call 3 must be plain already_complete once the PR was found, got %v", body3)
	}
	if _, present := body3["retry"]; present {
		t.Fatalf("call 3 must carry no retry key (row was cleared), got %v", body3)
	}
	if body3["branch"] != nil {
		t.Fatalf("call 3 (plain already_complete, no pending) must report branch=nil, got %v", body3["branch"])
	}
	_ = b1
}

// TestRepoScaffoldBranchRetryLooksUpExistingPRExactlyOnce: lookupExistingPR fails OPEN to "" on any
// error, so the handler must call it exactly once and bind that one result — a
// call-in-the-case-condition-then-call-again-to-bind shape (the bug this test pins) would, when the
// second call flakes, clear the pending row and return a "found" response carrying pr_url=null.
// The stub's `pr view` succeeds only on its FIRST invocation and fails afterwards, so the buggy
// double-call shape reproduces deterministically.
//
// Pre-fix symptom (confirmed by reverting to the double-call shape): pr_url comes back nil and the
// view-count file shows 2 invocations.
func TestRepoScaffoldBranchRetryLooksUpExistingPRExactlyOnce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	scaffoldRepoWithOrigin(t, base, token, "onceview", repoDir)

	// Call 1: gap (no gh) -> pending row on B1.
	pathWithOnlyGit(t)
	_, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "onceview", "open_pr": true})
	b1, _ := body["branch"].(string)
	if b1 == "" {
		t.Fatalf("call 1 must report a branch, got %v", body)
	}

	// Call 2: a stub gh whose `pr view` prints the URL on the FIRST call and exits 1 on every later
	// call (flag-file latch). `auth status` passes; `pr create` prints a sentinel we must never see.
	const existingURL = "https://github.com/example/onceview/pull/21"
	stubDir := t.TempDir()
	flag := filepath.Join(stubDir, "viewed")
	countFile := filepath.Join(stubDir, "viewcount")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"auth\" ] && [ \"$2\" = \"status\" ]; then exit 0; fi\n" +
		"if [ \"$1\" = \"pr\" ] && [ \"$2\" = \"view\" ]; then\n" +
		"  echo x >> " + countFile + "\n" +
		"  if [ -e " + flag + " ]; then exit 1; fi\n" +
		"  touch " + flag + "\n" +
		"  echo \"" + existingURL + "\"; exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"pr\" ] && [ \"$2\" = \"create\" ]; then echo \"https://github.com/SHOULD/NOT/pull/999\"; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh: %v", err)
	}
	prependToPATH(t, stubDir)

	_, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "onceview", "open_pr": true})
	if body2["pr_url"] != existingURL {
		t.Fatalf("PRE-FIX SYMPTOM: the found PR's URL must be bound from the ONE lookup call, got pr_url=%v (%v)", body2["pr_url"], body2)
	}
	count, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("stub `pr view` was never invoked: %v", err)
	}
	if got := strings.Count(string(count), "x"); got != 1 {
		t.Fatalf("`gh pr view` must be invoked exactly once per retry, got %d", got)
	}
}

// TestRepoScaffoldBranchPendingClearedWhenCompletedOutOfBand: if the operator merged/completed the
// protocol by hand on a DIFFERENT branch, a later click must recognize the goal is achieved, stop
// tracking the abandoned branch, and NAME it so it is never silently lost.
//
// Pre-fix symptom (confirmed): the already_complete note never mentions the abandoned branch (no
// tracking existed), so the operator has no idea a stray branch was left behind.
func TestRepoScaffoldBranchPendingClearedWhenCompletedOutOfBand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	scaffoldRepoWithOrigin(t, base, token, "oob", repoDir)

	// Call 1: gap (no gh) -> pending row on B1, repo left checked out on B1.
	pathWithOnlyGit(t)
	_, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "oob", "open_pr": true})
	b1, _ := body["branch"].(string)
	if b1 == "" {
		t.Fatalf("call 1 must report a branch, got %v", body)
	}

	// Operator finishes out of band: merge B1 into main by hand. Now main has the full protocol and
	// HEAD is on main (NOT the tracked branch).
	gitRunSrv(t, repoDir, "checkout", "main")
	gitRunSrv(t, repoDir, "merge", "--no-edit", b1)

	// Call 2: goal already achieved on a different branch -> clear tracking + name B1.
	resp2, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "oob", "open_pr": true})
	if resp2.StatusCode != 200 {
		t.Fatalf("call 2 must be 200, got %d (%v)", resp2.StatusCode, body2)
	}
	if body2["already_complete"] != true {
		t.Fatalf("call 2 must report already_complete=true, got %v", body2)
	}
	note2, _ := body2["note"].(string)
	if !strings.Contains(note2, b1) {
		t.Fatalf("PRE-FIX SYMPTOM: call 2 note must name the abandoned branch %q, got %q", b1, note2)
	}

	// Call 3: the row was really cleared -> the note no longer mentions B1.
	_, body3 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "oob", "open_pr": true})
	note3, _ := body3["note"].(string)
	if strings.Contains(note3, b1) {
		t.Fatalf("call 3 must NOT mention B1 (tracking was cleared in call 2), got %q", note3)
	}
}

// TestRepoScaffoldBranchSupersedesWithoutMovingOldBranch: when a pending branch was abandoned and
// the protocol is NOT complete on the current checkout, a fresh attempt must generate a NEW branch
// and leave the old one's commit exactly where it was — never reuse the tracked name (which
// EnsureRunBranch's `checkout -B` reset would move, orphaning attempt 1's commit).
//
// Pre-fix (fail-first) check: hardcoding `branch := pendingBranch` unconditionally moves B1 to the
// current HEAD; this test's rev-parse-unchanged assertion catches exactly that.
func TestRepoScaffoldBranchSupersedesWithoutMovingOldBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	scaffoldRepoWithOrigin(t, base, token, "supersede", repoDir)

	// Call 1: gap (no gh) -> pending row on B1 with a real protocol commit.
	pathWithOnlyGit(t)
	_, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "supersede", "open_pr": true})
	b1, _ := body["branch"].(string)
	if b1 == "" {
		t.Fatalf("call 1 must report a branch, got %v", body)
	}
	c1 := strings.TrimSpace(gitRunSrv(t, repoDir, "rev-parse", b1))

	// Abandon B1: check out main, where the protocol files are absent from the worktree (they only
	// exist on B1's commit).
	gitRunSrv(t, repoDir, "checkout", "main")

	// Call 2 (open_pr=false): protocol incomplete here + not on B1 -> a fresh superseding branch.
	resp2, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "supersede", "open_pr": false})
	if resp2.StatusCode != 200 {
		t.Fatalf("call 2 must be 200, got %d (%v)", resp2.StatusCode, body2)
	}
	b2, _ := body2["branch"].(string)
	if b2 == "" || b2 == b1 {
		t.Fatalf("call 2 must generate a NEW branch, not reuse %q; got %q", b1, b2)
	}
	if body2["retry"] != nil {
		t.Fatalf("a superseding fresh attempt is not a retry, got %v", body2["retry"])
	}
	notes2, _ := body2["notes"].([]any)
	named := false
	for _, n := range notes2 {
		if s, _ := n.(string); strings.Contains(s, b1) {
			named = true
		}
	}
	if !named {
		t.Fatalf("call 2 notes must name the superseded branch %q, got %v", b1, notes2)
	}
	// THE guard: B1's commit must be exactly where it was — never moved by a checkout -B reset.
	if c2 := strings.TrimSpace(gitRunSrv(t, repoDir, "rev-parse", b1)); c2 != c1 {
		t.Fatalf("branch %q was moved (%s -> %s) — a superseding attempt must never touch the old branch", b1, c1, c2)
	}
}

// TestRepoScaffoldBranchRetryRescaffoldsDriftOnTrackedBranch: if the protocol went incomplete ON the
// tracked branch (a committed deletion inside .l00prite/), a retry must re-scaffold the missing
// files on the SAME branch and still push+PR — not spin up a brand-new branch.
//
// Pre-fix symptom (confirmed): with the protocol incomplete, the pre-fix handler fell through the
// already_complete check and generated a brand-new branch name instead of reusing B1.
func TestRepoScaffoldBranchRetryRescaffoldsDriftOnTrackedBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	bareDir := scaffoldRepoWithOrigin(t, base, token, "drift", repoDir)

	// Call 1: gap (no gh) -> pending row on B1, repo on B1.
	pathWithOnlyGit(t)
	_, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "drift", "open_pr": true})
	b1, _ := body["branch"].(string)
	if b1 == "" {
		t.Fatalf("call 1 must report a branch, got %v", body)
	}

	// Introduce committed drift INSIDE .l00prite/ (so EnsureRunBranch's clean-tree guard, which
	// exempts .l00prite/, still passes on the next call): delete the prompts and commit.
	gitRunSrv(t, repoDir, "rm", "-r", ".l00prite/prompts")
	gitRunSrv(t, repoDir, "commit", "-m", "drift: remove prompts")

	// Call 2: retry with a working gh. Must reuse B1, re-scaffold, and push+PR.
	const wantURL = "https://github.com/example/drift/pull/5"
	prependToPATH(t, writeStubGH(t, false, wantURL))
	resp2, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "drift", "open_pr": true})
	if resp2.StatusCode != 200 {
		t.Fatalf("call 2 must be 200, got %d (%v)", resp2.StatusCode, body2)
	}
	if body2["branch"] != b1 {
		t.Fatalf("PRE-FIX SYMPTOM: drift retry must REUSE branch %q, got %v", b1, body2["branch"])
	}
	if body2["retry"] != true {
		t.Fatalf("call 2 must be a retry, got %v", body2)
	}
	files2, _ := body2["files_created"].([]any)
	if len(files2) == 0 {
		t.Fatalf("drift retry must have re-scaffolded missing files, got files_created=%v", body2["files_created"])
	}
	if body2["commit"] == nil {
		t.Fatalf("drift retry must have made a real commit, got commit=nil (%v)", body2)
	}
	if body2["pr_url"] != wantURL {
		t.Fatalf("drift retry must push+PR, got pr_url=%v", body2["pr_url"])
	}
	if out := gitRunSrv(t, bareDir, "branch", "--list", b1); !strings.Contains(out, b1) {
		t.Fatalf("branch %q was not actually pushed to origin: %q", b1, out)
	}
}

// TestRepoRemoveClearsPendingScaffoldAttempt: removing a repo must drop its pending row, so
// re-registering the same id against a DIFFERENT root never reuses a stale branch name.
//
// Pre-fix (fail-first) check: without the clearScaffoldAttempt in HandleRepoRemove, the stale B1 row
// survives removal and the re-registered repo's scaffold reports B1 as a "superseded" branch it
// never had — this test's "no note mentions B1" assertion catches that.
func TestRepoRemoveClearsPendingScaffoldAttempt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	srv, _, _, _, token := configured(t)
	base := srv.URL
	repoDir := t.TempDir()
	initGitRepoWithCommit(t, repoDir)
	scaffoldRepoWithOrigin(t, base, token, "reuseid", repoDir)

	// Call 1: gap (no gh) -> pending row on B1.
	pathWithOnlyGit(t)
	_, body := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "reuseid", "open_pr": true})
	b1, _ := body["branch"].(string)
	if b1 == "" {
		t.Fatalf("call 1 must report a branch, got %v", body)
	}

	// Remove the repo, then re-register the SAME id against a fresh repo on a different root.
	if resp, rb := doJSON(t, "POST", base+"/v1/repos/remove", token, map[string]any{"id": "reuseid"}); resp.StatusCode != 200 {
		t.Fatalf("remove must be 200, got %d (%v)", resp.StatusCode, rb)
	}
	freshDir := t.TempDir()
	initGitRepoWithCommit(t, freshDir)
	if resp, rb := doJSON(t, "POST", base+"/v1/repos", token, map[string]any{"id": "reuseid", "root": freshDir, "project": "ops"}); resp.StatusCode != 200 {
		t.Fatalf("re-register must be 200, got %d (%v)", resp.StatusCode, rb)
	}

	// A scaffold on the fresh repo must be a clean fresh attempt: a new branch, no retry, and no note
	// referencing the months-old B1 from the removed repo.
	_, body2 := doJSON(t, "POST", base+"/v1/repos/scaffold-branch", token, map[string]any{"id": "reuseid", "open_pr": false})
	if b2, _ := body2["branch"].(string); b2 == b1 {
		t.Fatalf("re-registered repo must NOT reuse the removed repo's branch %q, got %q", b1, b2)
	}
	if _, present := body2["retry"]; present {
		t.Fatalf("re-registered repo scaffold must not be a retry, got %v", body2)
	}
	notes2, _ := body2["notes"].([]any)
	for _, n := range notes2 {
		if s, _ := n.(string); strings.Contains(s, b1) {
			t.Fatalf("PRE-FIX SYMPTOM: re-registered repo note references the stale branch %q: %v", b1, notes2)
		}
	}
}
