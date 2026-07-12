package engine

// Regression tests for the Auto-PR gate-classification hardening: classifyCommand's new
// "gh pr create" case (the only loosening it grants) and the force-push hardening on both
// classifyCommand's "git push" case and classifyGitSub, which must both stay GateDestructive no
// matter where a force/delete/mirror/prune flag appears in the command -- because GatePush is one
// of the two classes (engine.GateClassAutoApprovable) the project Auto-PR toggle may set to
// auto_approve, so a misclassification here would make a force-push auto-approvable.

import "testing"

// TestClassifyCommandGhPrCreate pins the gh-pr-create rule: only the exact "gh pr create" prefix
// (any flags after it, any order) reaches the new PR-create class; every near-miss and every
// shell-metacharacter-bearing variant falls to the GateDestructive default -- i.e. every failure
// direction is MORE restrictive than today, never less.
func TestClassifyCommandGhPrCreate(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{`gh pr create --title t --body b --head h`, GatePRCreate},
		{`gh pr create --draft --base main`, GatePRCreate},
		{`gh pr create`, GatePRCreate},
		// Shell-chaining or substitution anywhere in the command stays maximally gated.
		{`gh pr create --title x; rm -rf /`, GateDestructive},
		{`gh pr create --body "$(cat secrets)"`, GateDestructive},
		{`gh pr create --title x | tee out`, GateDestructive},
		{`gh pr create --title "a` + "\n" + `b"`, GateDestructive},
		// Near-misses: none of these are "gh pr create" and must never be reclassified.
		{`gh pr view`, GateDestructive},
		{`gh pr merge`, GateDestructive},
		{`gh pr close`, GateDestructive},
		{`gh repo create`, GateDestructive},
		{`gh pr createx`, GateDestructive},
		{`ghx pr create`, GateDestructive},
		{`gh  pr create`, GateDestructive}, // double space
	}
	for _, c := range cases {
		got := classifyCommand(c.cmd, "")
		if got != c.want {
			t.Errorf("classifyCommand(%q) = %s, want %s", c.cmd, got, c.want)
		}
	}
}

// TestClassifyCommandRejectsFileReadingPRFlags (PR review, chatgpt-codex-connector): -F/--body-file
// and -T/--template both read an ARBITRARY LOCAL FILE off the gateway host -- not necessarily
// inside the repo -- and paste its content into a PUBLIC pull request. Neither is a shell
// metacharacter, so the shellChainChars check alone doesn't catch them; confirmed to fail against
// the pre-containsFileReadingPRFlag code, which returned GatePRCreate for every case below.
func TestClassifyCommandRejectsFileReadingPRFlags(t *testing.T) {
	cases := []string{
		`gh pr create --title t --body-file /etc/passwd --head h`,
		`gh pr create --title t -F /etc/passwd --head h`,
		`gh pr create --title t --body-file=/etc/passwd --head h`,
		`gh pr create --title t -F=/etc/passwd --head h`,
		`gh pr create --template /etc/passwd --head h`,
		`gh pr create -T /etc/passwd --head h`,
	}
	for _, cmd := range cases {
		if got := classifyCommand(cmd, ""); got != GateDestructive {
			t.Errorf("classifyCommand(%q) = %s, want %s (reads an arbitrary local file into a public PR)", cmd, got, GateDestructive)
		}
	}
	// Near-miss: "template" alone (without a leading "-T"/"--template") never matches -- pins
	// that the check is a whole-token match, not a substring match that would false-positive on
	// ordinary prose mentioning "template".
	if got := classifyCommand(`gh pr create --title "update the template docs" --head h`, ""); got != GatePRCreate {
		t.Errorf("classifyCommand(...) = %s, want %s (no real file-reading flag present)", got, GatePRCreate)
	}
	// This command's classification is intentionally the OVER-inclusive direction, not a bug:
	// classifyCommand tokenizes with strings.Fields (whitespace-only, no shell-quote awareness),
	// so a quoted --body value that happens to contain the standalone word "-F" is indistinguishable
	// from the real flag at this layer and gets gated too. Safe, not unsafe: worst case a run's PR
	// body needs a human's one-time approval instead of auto-approving; it can never go the other
	// way (a real -F/--body-file slipping through unnoticed because it was "quoted").
	if got := classifyCommand(`gh pr create --title t --body "see -F flag in the readme" --head h`, ""); got != GateDestructive {
		t.Errorf("classifyCommand(...) = %s, want %s (over-inclusive due to whitespace-only tokenization, the safe direction)", got, GateDestructive)
	}
}

// TestClassifyCommandForcePushIsDestructive is the run_command/sh-path regression: a force/
// delete/mirror/prune flag anywhere in a "git push" command must classify as GateDestructive,
// never GatePush -- confirmed (see comment below) to fail against the pre-fix classifier, which
// only checked for --force/-f as the token immediately after "push".
func TestClassifyCommandForcePushIsDestructive(t *testing.T) {
	cases := []string{
		"git push origin main --force",
		"git push --force-with-lease origin main",
		"git push origin --delete stale-branch",
		"git push --mirror origin",
		"git push origin refs/heads/*:refs/heads/* --prune",
		// PR review (gemini-code-assist): --force-with-lease/--force-if-includes accept an
		// "=<value>" suffix, which is a single whitespace-free token that never exact-matched
		// forcePushTokens -- a real bypass, since GatePush is auto-approvable. Confirmed to fail
		// against the pre-isForcePushToken code (exact-match only), which read GatePush for these.
		"git push --force-with-lease=main origin main",
		"git push origin main --force-if-includes=true",
	}
	for _, cmd := range cases {
		// branch="" here: this test is scoped to force-flag detection specifically, independent
		// of the separate branch-target restriction (see TestClassifyCommandRestrictsPushToRunBranch).
		if got := classifyCommand(cmd, ""); got != GateDestructive {
			t.Errorf("classifyCommand(%q) = %s, want %s (pre-fix classifier returned %s for this)", cmd, got, GateDestructive, GatePush)
		}
	}
	// A plain push (no force-ish flag) is unchanged: still GatePush when branch=="" (no run
	// branch to restrict against -- see pushTargetsRunBranch's doc comment).
	if got := classifyCommand("git push origin main", ""); got != GatePush {
		t.Errorf("classifyCommand(plain push) = %s, want %s", got, GatePush)
	}
}

// TestClassifyCommandRestrictsPushToRunBranch is the run_command/sh-path regression for the
// branch-target restriction (PR review, chatgpt-codex-connector): GatePush previously covered ANY
// non-force push, so "git push origin main" or "git push origin HEAD:main" would auto-approve
// exactly as readily as pushing the run's own branch -- letting an auto-approved run write
// directly to an unrelated branch instead of only ever pushing its own for a human-reviewed PR.
// Confirmed to fail against the pre-pushTargetsRunBranch code, which returned GatePush for every
// case below except the force-push ones (already covered by the test above).
func TestClassifyCommandRestrictsPushToRunBranch(t *testing.T) {
	const branch = "l00prite/run-abc123"
	safe := []string{
		"git push origin " + branch,
		"git push -u origin " + branch,
		"git push origin HEAD",
		"git push origin HEAD:" + branch,
		"git push origin " + branch + ":" + branch,
		"git push origin HEAD:refs/heads/" + branch,
		// The SOURCE side of a refspec doesn't matter, only the destination: pushing arbitrary
		// local content (here, whatever "main" happens to point to locally) onto the run's own
		// branch specifically is no more of an escalation than the commits the run can already
		// make to that branch with no gate at all -- the risk this function exists to catch is
		// writing to a DIFFERENT branch than the run's own, not what feeds the run's own branch.
		"git push origin main:" + branch,
	}
	for _, cmd := range safe {
		if got := classifyCommand(cmd, branch); got != GatePush {
			t.Errorf("classifyCommand(%q, branch) = %s, want %s (pushes only the run's own branch)", cmd, got, GatePush)
		}
	}
	unsafe := []string{
		"git push",                                       // no explicit destination at all
		"git push origin",                                // remote only, no ref
		"git push origin main",                           // a DIFFERENT branch than the run's own
		"git push origin HEAD:main",                      // explicit refspec targeting a different branch
		"git push upstream " + branch,                    // not the "origin" remote every push this codebase generates uses
		"git push origin " + branch + " " + branch + "2", // more than one ref in a single push
	}
	for _, cmd := range unsafe {
		if got := classifyCommand(cmd, branch); got != GateDestructive {
			t.Errorf("classifyCommand(%q, branch) = %s, want %s (does not confidently target only the run's own branch)", cmd, got, GateDestructive)
		}
	}
}

// TestClassifyGitSubForcePushIsDestructive is the git_command-tool-array-path regression, the
// gitCommand-gate call site's counterpart to the test above: classifyGitSub("push", rest) must
// read GateDestructive when rest carries any force/delete/mirror/prune token, confirmed (see
// comment below) to fail against the pre-fix single-argument classifyGitSub(sub), which returned
// GatePush unconditionally for sub=="push" regardless of flags.
func TestClassifyGitSubForcePushIsDestructive(t *testing.T) {
	cases := []struct {
		sub  string
		rest []string
		want string
	}{
		{"push", []string{"--force"}, GateDestructive},
		{"push", []string{"origin", "--delete", "stale"}, GateDestructive},
		{"push", []string{"--mirror", "origin"}, GateDestructive},
		// Same "=<value>" bypass as the classifyCommand test above, for the git_command tool-array
		// path: rest[i] arrives as one token, e.g. "--force-with-lease=main".
		{"push", []string{"--force-with-lease=main"}, GateDestructive},
		{"push", []string{"origin", "--force-if-includes=true"}, GateDestructive},
		{"push", nil, GatePush},
		{"push", []string{"origin", "main"}, GatePush},
		{"merge", nil, GateMerge},
		{"fetch", nil, GateDestructive},
	}
	for _, c := range cases {
		// branch="" here: scoped to force-flag detection specifically -- see
		// TestClassifyGitSubRestrictsPushToRunBranch for the separate branch-target check, which
		// (with a real branch configured) would gate several of these "GatePush" cases above too.
		got := classifyGitSub(c.sub, c.rest, "")
		if got != c.want {
			t.Errorf("classifyGitSub(%q, %v) = %s, want %s", c.sub, c.rest, got, c.want)
		}
	}
}

// TestClassifyGitSubRestrictsPushToRunBranch is the git_command-tool-array-path counterpart to
// TestClassifyCommandRestrictsPushToRunBranch (PR review, chatgpt-codex-connector): a
// git_command ["push","origin","main"] must not auto-approve just as readily as pushing the run's
// own branch. Confirmed to fail against the pre-pushTargetsRunBranch code, which returned GatePush
// for every "unsafe" case below.
func TestClassifyGitSubRestrictsPushToRunBranch(t *testing.T) {
	const branch = "l00prite/run-abc123"
	safeCases := [][]string{
		{"origin", branch},
		{"origin", "HEAD"},
		{"origin", "HEAD:" + branch},
		{"origin", branch + ":" + branch},
	}
	for _, rest := range safeCases {
		if got := classifyGitSub("push", rest, branch); got != GatePush {
			t.Errorf("classifyGitSub(push, %v, branch) = %s, want %s", rest, got, GatePush)
		}
	}
	unsafeCases := [][]string{
		nil,
		{"origin"},
		{"origin", "main"},
		{"origin", "HEAD:main"},
		{"upstream", branch},
		{"origin", branch, branch + "2"},
	}
	for _, rest := range unsafeCases {
		if got := classifyGitSub("push", rest, branch); got != GateDestructive {
			t.Errorf("classifyGitSub(push, %v, branch) = %s, want %s", rest, got, GateDestructive)
		}
	}
}
