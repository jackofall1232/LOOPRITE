package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// ---- gogit-specific ----

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
