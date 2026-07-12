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
		got := classifyCommand(c.cmd)
		if got != c.want {
			t.Errorf("classifyCommand(%q) = %s, want %s", c.cmd, got, c.want)
		}
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
	}
	for _, cmd := range cases {
		if got := classifyCommand(cmd); got != GateDestructive {
			t.Errorf("classifyCommand(%q) = %s, want %s (pre-fix classifier returned %s for this)", cmd, got, GateDestructive, GatePush)
		}
	}
	// A plain push (no force-ish flag) is unchanged: still GatePush.
	if got := classifyCommand("git push origin main"); got != GatePush {
		t.Errorf("classifyCommand(plain push) = %s, want %s", got, GatePush)
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
		{"push", nil, GatePush},
		{"push", []string{"origin", "main"}, GatePush},
		{"merge", nil, GateMerge},
		{"fetch", nil, GateDestructive},
	}
	for _, c := range cases {
		got := classifyGitSub(c.sub, c.rest)
		if got != c.want {
			t.Errorf("classifyGitSub(%q, %v) = %s, want %s", c.sub, c.rest, got, c.want)
		}
	}
}
