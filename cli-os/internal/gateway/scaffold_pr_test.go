package gateway

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
)

// TestProbePushPRCapabilityGogitBackendGapsWithoutExecing: the gogit backend (Android, or any host
// with no git binary) has no push/ssh transport at all -- probePushPRCapability must recognize
// this from Kind() alone and never even try exec.LookPath("gh") or a dry-run push. This is the
// "no git binary" branch from plan step 4(e); it's a package-internal unit test (not an HTTP-level
// one) specifically because HandleRepoScaffoldBranch always calls gitx.Detect() itself, which
// can't be forced to the gogit backend on a host that DOES have a git binary (every dev/CI
// machine running this test suite) -- gitx.NewGogitClient() is the documented seam for exactly
// this case (see gitx.go's doc comment on NewGogitClient).
//
// Pre-fix behavior this pins against: before probePushPRCapability existed, there was no
// capability check at all -- a caller would have had to actually attempt git.Raw (which always
// fails with gitx.ErrRawUnsupported on gogit) to discover this, rather than getting a fixed,
// friendly gap message up front.
func TestProbePushPRCapabilityGogitBackendGapsWithoutExecing(t *testing.T) {
	git := gitx.NewGogitClient()
	ok, gapMsg, rawErr := probePushPRCapability(context.Background(), git, t.TempDir(), "l00prite/add-protocol-test")
	if ok {
		t.Fatalf("expected ok=false for the gogit backend, got true")
	}
	if gapMsg != gapNoGitBinary {
		t.Fatalf("expected the fixed gapNoGitBinary sentence, got %q", gapMsg)
	}
	if rawErr != nil {
		t.Fatalf("expected no raw error for a purely structural (Kind()-based) gap, got %v", rawErr)
	}
}

// TestProbePushPRCapabilityNoGHOnPathGapsWithoutPushing: fail-closed when gh isn't installed --
// must never fall through to a dry-run push attempt. Forces PATH to a directory with no `gh` in
// it (rather than trusting whatever PATH this dev/CI machine happens to have) so the test is
// deterministic regardless of whether gh happens to be installed here.
func TestProbePushPRCapabilityNoGHOnPathGapsWithoutPushing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("PATH", t.TempDir()) // a directory with nothing in it -- no git, no gh
	git := gitx.Detect()
	if git.Kind() != "exec" {
		// gitx.Detect() is resolved once at process start (before this test mutated PATH), so it
		// still reports "exec" here -- this assertion documents that assumption rather than
		// silently passing for the wrong reason if that ever changes.
		t.Skip("gitx.Detect() did not resolve to the exec backend in this environment")
	}
	ok, gapMsg, rawErr := probePushPRCapability(context.Background(), git, t.TempDir(), "l00prite/add-protocol-test")
	if ok {
		t.Fatalf("expected ok=false with no gh on PATH, got true")
	}
	if gapMsg != gapNoGH {
		t.Fatalf("expected the fixed gapNoGH sentence, got %q", gapMsg)
	}
	if rawErr != nil {
		t.Fatalf("expected no raw error for a purely structural (LookPath-based) gap, got %v", rawErr)
	}
}

// TestParsePRURLTakesLastURLLine pins parsePRURL's contract: it scans from the end so a future gh
// version printing extra informational lines before the URL still parses correctly, and returns ""
// (not a panic or a garbage value) when nothing looks like a URL at all.
func TestParsePRURLTakesLastURLLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare url", "https://github.com/example/repo/pull/42\n", "https://github.com/example/repo/pull/42"},
		{"leading noise", "Creating pull request...\nhttps://github.com/example/repo/pull/42\n", "https://github.com/example/repo/pull/42"},
		{"no url", "something went wrong\n", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePRURL(tc.in); got != tc.want {
				t.Errorf("parsePRURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestGHPRCreateCommandNeverAutoMerges pins the never-auto-merge invariant at the one place this
// package emits a copy-paste gh command a human might run verbatim: it must never suggest --merge,
// --auto, or any merge-shaped flag.
func TestGHPRCreateCommandNeverAutoMerges(t *testing.T) {
	cmd := ghPRCreateCommand("l00prite/add-protocol-test")
	for _, bad := range []string{"--merge", "--auto", "merge"} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("ghPRCreateCommand must never suggest merging, but got %q", cmd)
		}
	}
	if !strings.Contains(cmd, "gh pr create") {
		t.Fatalf("expected a real gh pr create command, got %q", cmd)
	}
}
