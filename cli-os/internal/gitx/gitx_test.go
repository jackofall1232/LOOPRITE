package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// ---- fixture helpers (plain os/exec git — used to set up repos for BOTH client impls; gogit has
// no Init primitive in the Client interface, that's a test-fixture concern, not an engine one) ----

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// initGitRepo creates a repo with a configured identity and gpgsign disabled locally — this
// sandbox's global gitconfig signs commits (commit.gpgsign=true), which would otherwise make every
// fixture commit here fail or hang.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitFixture(t, dir, "init", "-q")
	gitFixture(t, dir, "config", "user.email", "fixture@example.com")
	gitFixture(t, dir, "config", "user.name", "gitx fixture")
	gitFixture(t, dir, "config", "commit.gpgsign", "false")
}

func clients(t *testing.T) map[string]Client {
	t.Helper()
	m := map[string]Client{"gogit": gogitClient{}}
	if _, err := exec.LookPath("git"); err == nil {
		m["exec"] = execClient{}
	} else {
		t.Log("git binary not on PATH; exec implementation not exercised in this run")
	}
	return m
}

// ---- Detect ----

func TestDetectPrefersExecWhenGitPresent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if got := Detect().Kind(); got != "exec" {
		t.Fatalf("Detect().Kind() = %q, want exec (git is on PATH)", got)
	}
}

func TestDetectFallsBackToGogitWithoutGitOnPath(t *testing.T) {
	// Detect() itself now returns a value cached once at process start (a redundant
	// exec.LookPath per call was wasteful — every clone request, every engine iteration), so
	// this test exercises the underlying detectOnce() directly rather than the process-lifetime
	// cache; restore the real cached value afterward so later tests in this package see the
	// actual host state again.
	t.Setenv("PATH", "")
	if got := detectOnce().Kind(); got != "gogit" {
		t.Fatalf("detectOnce().Kind() = %q, want gogit (PATH cleared)", got)
	}
}

// ---- shared behavior across both implementations ----

func TestClientsBasicFlow(t *testing.T) {
	for name, cl := range clients(t) {
		cl := cl
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)
			writeFile(t, dir, "README.md", "hello\n")
			gitFixture(t, dir, "add", "-A")
			gitFixture(t, dir, "commit", "-m", "init")

			// StatusPorcelain: clean tree.
			st, err := cl.StatusPorcelain(dir)
			if err != nil {
				t.Fatalf("StatusPorcelain (clean): %v", err)
			}
			if strings.TrimSpace(st) != "" {
				t.Fatalf("expected a clean status, got %q", st)
			}

			// Dirty the tree by MODIFYING a tracked file — `git diff HEAD` (unlike status) never
			// shows a brand-new untracked file unless it's staged, so a tracked-file edit is what
			// exercises DiffHead meaningfully for both implementations.
			writeFile(t, dir, "README.md", "hello\nmodified\n")
			st, err = cl.StatusPorcelain(dir)
			if err != nil {
				t.Fatalf("StatusPorcelain (dirty): %v", err)
			}
			if strings.TrimSpace(st) == "" {
				t.Fatal("expected a non-empty status after a tracked file was modified")
			}

			// DiffHead is non-empty after a change (a summary under gogit, a real diff under exec —
			// either way, non-empty is the load-bearing contract callers depend on).
			diff, err := cl.DiffHead(dir)
			if err != nil {
				t.Fatalf("DiffHead: %v", err)
			}
			if strings.TrimSpace(diff) == "" {
				t.Fatal("expected a non-empty diff after a change")
			}

			// Commit flow: AddAll -> Commit -> RevParseHead.
			if err := cl.AddAll(dir); err != nil {
				t.Fatalf("AddAll: %v", err)
			}
			hash, err := cl.Commit(dir, "modify README.md")
			if err != nil || hash == "" {
				t.Fatalf("Commit: hash=%q err=%v", hash, err)
			}
			head, err := cl.RevParseHead(dir)
			if err != nil {
				t.Fatalf("RevParseHead: %v", err)
			}
			if head != hash {
				t.Fatalf("RevParseHead = %q, want the just-created commit %q", head, hash)
			}

			// Nothing to commit -> ("", nil).
			hash2, err := cl.Commit(dir, "noop")
			if err != nil || hash2 != "" {
				t.Fatalf("nothing-to-commit Commit: hash=%q err=%v", hash2, err)
			}

			// CheckoutNewBranch, then again after a new commit -> the "-B" reset semantics: the
			// branch must move to point at the new HEAD, not refuse because it already exists.
			if err := cl.CheckoutNewBranch(dir, "feature-x"); err != nil {
				t.Fatalf("CheckoutNewBranch #1: %v", err)
			}
			cur := strings.TrimSpace(gitFixture(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))
			if cur != "feature-x" {
				t.Fatalf("expected to be on feature-x, got %q", cur)
			}
			writeFile(t, dir, "again.txt", "y")
			if err := cl.AddAll(dir); err != nil {
				t.Fatalf("AddAll #2: %v", err)
			}
			newHash, err := cl.Commit(dir, "again")
			if err != nil || newHash == "" {
				t.Fatalf("second Commit: hash=%q err=%v", newHash, err)
			}
			if err := cl.CheckoutNewBranch(dir, "feature-x"); err != nil {
				t.Fatalf("CheckoutNewBranch #2 (-B reset): %v", err)
			}
			resolved := strings.TrimSpace(gitFixture(t, dir, "rev-parse", "feature-x"))
			if resolved != newHash {
				t.Fatalf("feature-x was not reset to the new HEAD: got %q, want %q", resolved, newHash)
			}
		})
	}
}

func TestCloneFromLocalPath(t *testing.T) {
	for name, cl := range clients(t) {
		cl := cl
		t.Run(name, func(t *testing.T) {
			src := t.TempDir()
			initGitRepo(t, src)
			writeFile(t, src, "README.md", "hi\n")
			gitFixture(t, src, "add", "-A")
			gitFixture(t, src, "commit", "-m", "init")

			dest := filepath.Join(t.TempDir(), "cloned")
			if err := cl.Clone(context.Background(), src, dest, 1); err != nil {
				t.Fatalf("Clone: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
				t.Fatalf("cloned repo missing README.md: %v", err)
			}
		})
	}
}

func TestRevParseHeadOnUnbornRepo(t *testing.T) {
	for name, cl := range clients(t) {
		cl := cl
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)
			if _, err := cl.RevParseHead(dir); err == nil {
				t.Fatal("expected an error for a repository with no commits")
			}
		})
	}
}

// ---- Log ----

// logLineRE matches one Log output line: a 7-hex-char abbreviated hash, a space, then the rest of
// the line (the commit's first message line, which may itself contain spaces).
var logLineRE = regexp.MustCompile(`^[0-9a-f]{7} (.*)$`)

func TestLogFormatOrderAndLimit(t *testing.T) {
	for name, cl := range clients(t) {
		cl := cl
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)
			for _, msg := range []string{"first commit", "second commit", "third commit"} {
				writeFile(t, dir, "f.txt", msg+"\n")
				gitFixture(t, dir, "add", "-A")
				gitFixture(t, dir, "commit", "-m", msg)
			}

			// Bare call (limit <= 0): all 3 commits, most-recent-first, each line
			// "<7-char-abbrev-hash> <first line of message>".
			out, err := cl.Log(dir, 0)
			if err != nil {
				t.Fatalf("Log(0): %v", err)
			}
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) != 3 {
				t.Fatalf("Log(0) = %d lines, want 3: %q", len(lines), out)
			}
			wantOrder := []string{"third commit", "second commit", "first commit"}
			for i, line := range lines {
				m := logLineRE.FindStringSubmatch(line)
				if m == nil {
					t.Fatalf("line %d %q does not match <7-hex> <message>", i, line)
				}
				if m[1] != wantOrder[i] {
					t.Fatalf("line %d message = %q, want %q (order must be most-recent-first)", i, m[1], wantOrder[i])
				}
			}

			// limit clamps the result to the N most recent commits.
			out2, err := cl.Log(dir, 2)
			if err != nil {
				t.Fatalf("Log(2): %v", err)
			}
			lines2 := strings.Split(strings.TrimRight(out2, "\n"), "\n")
			if len(lines2) != 2 {
				t.Fatalf("Log(2) = %d lines, want 2: %q", len(lines2), out2)
			}
			if !strings.Contains(lines2[0], "third commit") || !strings.Contains(lines2[1], "second commit") {
				t.Fatalf("Log(2) = %q, want the 2 most recent commits (third, second)", out2)
			}
		})
	}
}

func TestLogOnEmptyRepo(t *testing.T) {
	for name, cl := range clients(t) {
		cl := cl
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)
			out, err := cl.Log(dir, 0)
			if err != nil {
				t.Fatalf("Log on empty repo should not be an error, got %v", err)
			}
			if out != "" {
				t.Fatalf("Log on empty repo = %q, want empty string", out)
			}
		})
	}
}

// ---- Show ----

func TestShowHeaderAndAddedFileDiff(t *testing.T) {
	for name, cl := range clients(t) {
		cl := cl
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)
			writeFile(t, dir, "README.md", "hello\n")
			gitFixture(t, dir, "add", "-A")
			gitFixture(t, dir, "commit", "-m", "root commit")

			writeFile(t, dir, "extra.txt", "brand new content\n")
			gitFixture(t, dir, "add", "-A")
			gitFixture(t, dir, "commit", "-m", "add extra.txt")
			head := strings.TrimSpace(gitFixture(t, dir, "rev-parse", "HEAD"))

			out, err := cl.Show(dir, "HEAD")
			if err != nil {
				t.Fatalf("Show(HEAD): %v", err)
			}
			if !strings.Contains(out, head) {
				t.Fatalf("Show output missing the resolved commit hash %q: %q", head, out)
			}
			if !strings.Contains(out, "fixture@example.com") {
				t.Fatalf("Show output missing the author email: %q", out)
			}
			if !strings.Contains(out, "add extra.txt") {
				t.Fatalf("Show output missing the commit message: %q", out)
			}
			// The added file must render as an ADDITION (a plus-prefixed line carrying its
			// content) in the patch, not merely "some non-empty diff-shaped text".
			added := false
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "+") && strings.Contains(line, "brand new content") {
					added = true
					break
				}
			}
			if !added {
				t.Fatalf("Show output has no plus-prefixed line with the added file's content: %q", out)
			}
		})
	}
}

// ---- gogit-specific ----

// TestGogitShowRootCommit proves Show does not fabricate a diff for a root commit (no parent to
// diff against): unlike real `git show`, which renders a genuine diff against the empty tree, the
// gogit implementation has no such primitive wired up here and — per its own never-fabricate
// discipline (see gogitClient.DiffHead) — omits the diff section in favor of a plain note instead
// of guessing. This is a deliberate, spec-permitted divergence from the exec implementation, so it
// is verified only against gogitClient, not through the shared clients(t) table.
func TestGogitShowRootCommit(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFile(t, dir, "README.md", "hello\n")
	gitFixture(t, dir, "add", "-A")
	gitFixture(t, dir, "commit", "-m", "root commit")
	root := strings.TrimSpace(gitFixture(t, dir, "rev-parse", "HEAD"))

	cl := gogitClient{}
	out, err := cl.Show(dir, root)
	if err != nil {
		t.Fatalf("Show(root): %v", err)
	}
	if !strings.Contains(out, "root commit") {
		t.Fatalf("Show(root) missing the commit message: %q", out)
	}
	if !strings.Contains(out, "root commit — no parent to diff against") {
		t.Fatalf("Show(root) should note there is no parent to diff against, got: %q", out)
	}
	if strings.Contains(out, "diff --git") {
		t.Fatalf("Show(root) should not fabricate a diff, got: %q", out)
	}
}

func TestGogitShowUnknownRef(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFile(t, dir, "README.md", "hello\n")
	gitFixture(t, dir, "add", "-A")
	gitFixture(t, dir, "commit", "-m", "init")

	cl := gogitClient{}
	if _, err := cl.Show(dir, "not-a-real-ref"); err == nil {
		t.Fatal("expected an error for a ref that does not resolve to any commit")
	}
}

func TestGogitRawUnsupported(t *testing.T) {
	_, err := (gogitClient{}).Raw(context.Background(), t.TempDir(), "status")
	if !errors.Is(err, ErrRawUnsupported) {
		t.Fatalf("expected ErrRawUnsupported, got %v", err)
	}
}

func TestGogitCloneRejectsSSH(t *testing.T) {
	cl := gogitClient{}
	if err := cl.Clone(context.Background(), "git@github.com:foo/bar.git", filepath.Join(t.TempDir(), "dest"), 1); err == nil {
		t.Fatal("expected gogit Clone to reject an ssh-style URL")
	}
}

// ---- exec-specific: the commit-identity fallback ----

// TestExecCommitIdentityFallback proves a host with NO git identity configured anywhere (the
// on-device Android first-boot scenario this fallback exists for) can still commit through the
// engine, via a synthetic l00prite-os identity, instead of stopping the run.
func TestExecCommitIdentityFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-specific env manipulation (/dev/null)")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q") // deliberately NO user.name/user.email/gpgsign config

	// Strip every source of ambient identity this sandbox's real environment would otherwise
	// supply (its own global ~/.gitconfig has user.name/email configured): a fresh empty HOME plus
	// GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM=/dev/null blank out global and system config entirely.
	// GIT_AUTHOR_*/GIT_COMMITTER_*/EMAIL must stay UNSET rather than set-to-empty — git treats a
	// present-but-empty GIT_AUTHOR_NAME as an authoritative (if empty) identity that overrides even
	// an explicit -c user.name, which would sabotage the very fallback this test exercises; they
	// are already unset in this process, so nothing further is needed here for them.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	writeFile(t, dir, "a.txt", "x")
	cl := execClient{}
	if err := cl.AddAll(dir); err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	hash, err := cl.Commit(dir, "no identity configured anywhere")
	if err != nil || hash == "" {
		t.Fatalf("Commit should succeed via the fallback identity, got hash=%q err=%v", hash, err)
	}
	who := strings.TrimSpace(gitFixture(t, dir, "log", "-1", "--format=%an <%ae>"))
	if who != "l00prite-os <l00prite-os@localhost>" {
		t.Fatalf("expected the synthetic fallback identity on the commit, got %q", who)
	}
}
