package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
)

// ---- helpers ----

func writeFileRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "l00prite test")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
}

// ---- jail ----

func TestToolJail(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	tb := &Toolbox{Root: root}

	// absolute path denied
	o := tb.Execute(ctx, "write_file", map[string]any{"path": "/etc/passwd_evil", "content": "x"}, false)
	if o.Gate != nil || !strings.Contains(o.Result, "ERROR") || !strings.Contains(o.Result, "absolute") {
		t.Fatalf("absolute path should be a plain ERROR, got %+v", o)
	}

	// ../ escape denied
	o = tb.Execute(ctx, "write_file", map[string]any{"path": "../evil.txt", "content": "x"}, false)
	if o.Gate != nil || !strings.Contains(o.Result, "escapes") {
		t.Fatalf("../ escape should be denied, got %+v", o)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); err == nil {
		t.Fatal("../evil.txt was written outside the jail")
	}

	// symlink-out-of-root escape denied
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	o = tb.Execute(ctx, "write_file", map[string]any{"path": "link/evil.txt", "content": "x"}, false)
	if o.Gate != nil || !strings.Contains(o.Result, "escapes") {
		t.Fatalf("symlink escape should be denied, got %+v", o)
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); err == nil {
		t.Fatal("write escaped through symlink to outside dir")
	}

	// normal nested write succeeds
	o = tb.Execute(ctx, "write_file", map[string]any{"path": "a/b/c.txt", "content": "hi"}, false)
	if o.Gate != nil || !strings.Contains(o.Result, "wrote a/b/c.txt (2 bytes)") {
		t.Fatalf("nested write should succeed, got %+v", o)
	}
	if b, err := os.ReadFile(filepath.Join(root, "a", "b", "c.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("nested file not written correctly: %q err=%v", b, err)
	}

	// unit_done / unit_blocked are safe no-ops
	if o := tb.Execute(ctx, "unit_done", map[string]any{"summary": "s", "files_changed": []string{}}, false); o.Result != "acknowledged" {
		t.Fatalf("unit_done should be acknowledged, got %q", o.Result)
	}
}

// ---- protocol hard-deny ----

func TestToolProtocolHardDeny(t *testing.T) {
	ctx := context.Background()
	tb := &Toolbox{Root: t.TempDir()}

	for _, p := range []string{".l00prite/heartbeat.json", ".l00prite/prompts/x.md"} {
		// Even with approved=true, protocol files are never writable (not gateable).
		o := tb.Execute(ctx, "write_file", map[string]any{"path": p, "content": "{}"}, true)
		if o.Gate != nil {
			t.Fatalf("%s must NOT be gateable, got gate %+v", p, o.Gate)
		}
		if !strings.Contains(o.Result, "DENIED") {
			t.Fatalf("%s must be DENIED, got %q", p, o.Result)
		}
		if _, err := os.Stat(filepath.Join(tb.Root, filepath.FromSlash(p))); err == nil {
			t.Fatalf("%s was written despite hard-deny", p)
		}
	}
}

// ---- denylist gating ----

func TestToolDenylistGating(t *testing.T) {
	ctx := context.Background()
	// Construct Denylist directly with pattern strings; MatchDenylist comes from l00pfiles.go.
	tb := &Toolbox{Root: t.TempDir(), Denylist: []string{".env"}}

	for _, p := range []string{".env", "sub/.env"} {
		o := tb.Execute(ctx, "write_file", map[string]any{"path": p, "content": "SECRET=1"}, false)
		if o.Gate == nil {
			t.Fatalf("write to %s should gate, got %+v", p, o)
		}
		if o.Gate.Class != GateDestructive {
			t.Fatalf("denylist gate class for %s should be %s, got %s", p, GateDestructive, o.Gate.Class)
		}
		if _, err := os.Stat(filepath.Join(tb.Root, filepath.FromSlash(p))); err == nil {
			t.Fatalf("%s was written without approval", p)
		}
	}

	// approved=true writes it.
	o := tb.Execute(ctx, "write_file", map[string]any{"path": ".env", "content": "SECRET=1"}, true)
	if o.Gate != nil || !strings.Contains(o.Result, "wrote .env") {
		t.Fatalf("approved denylist write should succeed, got %+v", o)
	}
	if b, err := os.ReadFile(filepath.Join(tb.Root, ".env")); err != nil || string(b) != "SECRET=1" {
		t.Fatalf(".env not written on approval: %q err=%v", b, err)
	}
}

// ---- command allowlist ----

func TestCommandAllowlist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	ctx := context.Background()
	tb := &Toolbox{Root: t.TempDir(), Allowlist: []string{"echo"}}

	// allowlisted command runs
	o := tb.Execute(ctx, "run_command", map[string]any{"command": "echo hi"}, false)
	if o.Gate != nil {
		t.Fatalf("allowlisted command should not gate, got %+v", o.Gate)
	}
	if !strings.Contains(o.Result, "exit_code: 0") || !strings.Contains(o.Result, "hi") {
		t.Fatalf("echo hi result unexpected: %q", o.Result)
	}

	// token boundary: "echox" is not "echo"
	o = tb.Execute(ctx, "run_command", map[string]any{"command": "echox hi"}, false)
	if o.Gate == nil {
		t.Fatalf("echox should gate (token boundary), got %+v", o)
	}
	if o.Gate.Class != GateDestructive {
		t.Fatalf("non-allowlisted command gate class should be %s, got %s", GateDestructive, o.Gate.Class)
	}

	// git push classifies as GatePush
	o = tb.Execute(ctx, "run_command", map[string]any{"command": "git push origin main"}, false)
	if o.Gate == nil || o.Gate.Class != GatePush {
		t.Fatalf("git push should gate as %s, got %+v", GatePush, o)
	}
}

// ---- command timeout ----

func TestCommandTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix only")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	ctx := context.Background()
	tb := &Toolbox{Root: t.TempDir(), Allowlist: []string{"sleep"}}

	o := tb.Execute(ctx, "run_command", map[string]any{"command": "sleep 2", "timeout_s": 1}, false)
	if o.Gate != nil {
		t.Fatalf("sleep is allowlisted; should not gate, got %+v", o.Gate)
	}
	if !strings.Contains(o.Result, "exit_code: -1") || !strings.Contains(o.Result, "timed out after 1s") {
		t.Fatalf("expected timeout result, got %q", o.Result)
	}
}

// ---- git_command ----

func TestGitCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")

	tb := &Toolbox{Root: dir}

	// status allowed
	o := tb.Execute(ctx, "git_command", map[string]any{"args": []string{"status", "--porcelain"}}, false)
	if o.Gate != nil {
		t.Fatalf("git status should not gate, got %+v", o.Gate)
	}
	if !strings.Contains(o.Result, "exit_code: 0") {
		t.Fatalf("git status result unexpected: %q", o.Result)
	}

	// log allowed
	o = tb.Execute(ctx, "git_command", map[string]any{"args": []string{"log", "--oneline"}}, false)
	if o.Gate != nil {
		t.Fatalf("git log should not gate, got %+v", o.Gate)
	}

	// push -> GatePush
	o = tb.Execute(ctx, "git_command", map[string]any{"args": []string{"push"}}, false)
	if o.Gate == nil || o.Gate.Class != GatePush {
		t.Fatalf("git push should gate as %s, got %+v", GatePush, o)
	}

	// reset --hard -> GateDestructive
	o = tb.Execute(ctx, "git_command", map[string]any{"args": []string{"reset", "--hard"}}, false)
	if o.Gate == nil || o.Gate.Class != GateDestructive {
		t.Fatalf("git reset should gate as %s, got %+v", GateDestructive, o)
	}

	// flag injection -> ERROR, no gate, not executed
	o = tb.Execute(ctx, "git_command", map[string]any{"args": []string{"-c", "core.pager=cat", "log"}}, false)
	if o.Gate != nil || !strings.Contains(o.Result, "ERROR") {
		t.Fatalf("git global flag should be a plain ERROR, got %+v", o)
	}
}

// ---- EnsureRunBranch / CommitUnit ----

func TestBranchAndCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// nil client defaults to gitx.Detect() (exercised throughout this test); a git client is also
	// passed explicitly in a couple of calls below to prove both spellings work identically.
	git := gitx.Detect()

	// no commits -> "repository has no commits"
	empty := t.TempDir()
	initGitRepo(t, empty)
	if err := EnsureRunBranch(nil, empty, "l00prite/run-x"); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Fatalf("expected no-commits error, got %v", err)
	}

	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")

	// dirty tree -> "working tree is not clean"
	writeFileRaw(t, filepath.Join(dir, "dirty.txt"), "x")
	if err := EnsureRunBranch(git, dir, "l00prite/run-x"); err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("expected not-clean error, got %v", err)
	}

	// clean it, then succeed and create the branch
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add dirty")
	if err := EnsureRunBranch(git, dir, "l00prite/run-x"); err != nil {
		t.Fatalf("EnsureRunBranch on clean tree: %v", err)
	}
	cur := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))
	if cur != "l00prite/run-x" {
		t.Fatalf("expected to be on run branch, got %q", cur)
	}

	// CommitUnit round-trip
	writeFileRaw(t, filepath.Join(dir, "new.go"), "package x\n")
	hash, err := CommitUnit(nil, dir, "add new.go")
	if err != nil || hash == "" {
		t.Fatalf("CommitUnit: hash=%q err=%v", hash, err)
	}

	// nothing to commit -> ("", nil)
	hash2, err := CommitUnit(git, dir, "noop")
	if err != nil || hash2 != "" {
		t.Fatalf("CommitUnit noop should be (\"\", nil), got hash=%q err=%v", hash2, err)
	}
}

// ---- caps ----

func TestToolCaps(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	tb := &Toolbox{Root: root}

	// search_files respects max_results
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("needle here\n")
	}
	writeFileRaw(t, filepath.Join(root, "f.txt"), sb.String())
	o := tb.Execute(ctx, "search_files", map[string]any{"query": "needle", "max_results": 3}, false)
	if n := strings.Count(o.Result, "f.txt:"); n != 3 {
		t.Fatalf("expected 3 capped matches, got %d in %q", n, o.Result)
	}
	if !strings.Contains(o.Result, "truncated") {
		t.Fatalf("expected truncation note, got %q", o.Result)
	}

	// read_file truncation note for a file over 256 KiB
	big := strings.Repeat("a", 300*1024)
	writeFileRaw(t, filepath.Join(root, "big.txt"), big)
	o = tb.Execute(ctx, "read_file", map[string]any{"path": "big.txt"}, false)
	if !strings.Contains(o.Result, "truncated") || !strings.Contains(o.Result, "256 KiB") {
		t.Fatalf("expected 256 KiB truncation note, got tail %q", tail(o.Result, 80))
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---- allowlist shell-chaining regression (PR #24 review) ----

func TestCommandAllowlistRejectsShellChaining(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	ctx := context.Background()
	tb := &Toolbox{Root: t.TempDir(), Allowlist: []string{"echo hi"}}

	// A benign extension with no shell metacharacters still runs without approval.
	o := tb.Execute(ctx, "run_command", map[string]any{"command": "echo hi there"}, false)
	if o.Gate != nil {
		t.Fatalf("plain-argument extension of an allowlisted command should not gate, got %+v", o.Gate)
	}

	// Smuggling an extra command past the allowlisted prefix must gate, not execute silently.
	for _, chained := range []string{
		"echo hi ; touch pwned",
		"echo hi && touch pwned",
		"echo hi | tee pwned",
		"echo hi `touch pwned`",
		"echo hi $(touch pwned)",
	} {
		o := tb.Execute(ctx, "run_command", map[string]any{"command": chained}, false)
		if o.Gate == nil {
			t.Fatalf("chained command %q must gate (not bypass the allowlist), got %+v", chained, o)
		}
		if _, err := os.Stat(filepath.Join(tb.Root, "pwned")); err == nil {
			t.Fatalf("chained command %q executed despite gating: pwned file exists", chained)
		}
	}

	// An exact match against a compound allowlisted entry is still honored regardless of
	// metacharacters IN THE APPROVED STRING ITSELF — a human pre-approved that literal command.
	tb2 := &Toolbox{Root: t.TempDir(), Allowlist: []string{"echo a && echo b"}}
	o = tb2.Execute(ctx, "run_command", map[string]any{"command": "echo a && echo b"}, false)
	if o.Gate != nil {
		t.Fatalf("an exact-match compound allowlist entry should run without gating, got %+v", o.Gate)
	}
}

// ---- constraints.md self-modification guard (PR #24 review) ----

func TestConstraintsMdIsProtocolProtected(t *testing.T) {
	ctx := context.Background()
	tb := &Toolbox{Root: t.TempDir()}

	// Even approved=true must not write it: the Autonomous-Edit Denylist lives in this file,
	// so gate-then-approve would let a run loosen its own denylist and then exploit that on the
	// next iteration.
	o := tb.Execute(ctx, "write_file", map[string]any{"path": ".l00prite/constraints.md", "content": "tampered"}, true)
	if o.Gate != nil {
		t.Fatalf("constraints.md must NOT be gateable, got gate %+v", o.Gate)
	}
	if !strings.Contains(o.Result, "DENIED") {
		t.Fatalf("constraints.md write must be DENIED, got %q", o.Result)
	}
	if _, err := os.Stat(filepath.Join(tb.Root, ".l00prite", "constraints.md")); err == nil {
		t.Fatal("constraints.md was written despite hard-deny")
	}
}

// ---- search_files symlink jail escape (PR #24 review) ----

func TestSearchFilesSkipsSymlinkedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	writeFileRaw(t, secret, "needle-outside-the-repo\n")

	if err := os.Symlink(secret, filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("could not create symlink: %v", err)
	}

	tb := &Toolbox{Root: root}
	o := tb.Execute(context.Background(), "search_files", map[string]any{"query": "needle-outside-the-repo"}, false)
	if strings.Contains(o.Result, "needle-outside-the-repo") {
		t.Fatalf("search_files followed a symlink outside the repo jail, got %q", o.Result)
	}
}

// ---- destructive git branch gating (PR #24 review) ----

func TestGitBranchDestructiveFlagsGate(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFileRaw(t, filepath.Join(dir, "f.txt"), "x")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")

	tb := &Toolbox{Root: dir}
	ctx := context.Background()

	// A bare listing and a plain single-name create are both safe (no existing ref touched).
	for _, args := range [][]string{{"branch"}, {"branch", "feature-x"}} {
		o := tb.Execute(ctx, "git_command", map[string]any{"args": toAnySlice(args)}, false)
		if o.Gate != nil {
			t.Fatalf("git %v should not gate, got %+v", args, o.Gate)
		}
	}

	// Any flag on branch — delete, force-move, etc. — can destroy or rewrite a ref, so it must gate.
	for _, args := range [][]string{
		{"branch", "-D", "feature-x"},
		{"branch", "-d", "feature-x"},
		{"branch", "-f", "main", "HEAD"},
		{"branch", "-m", "main", "renamed"},
	} {
		o := tb.Execute(ctx, "git_command", map[string]any{"args": toAnySlice(args)}, false)
		if o.Gate == nil {
			t.Fatalf("git %v must gate as destructive, got %+v", args, o)
		}
		if o.Gate.Class != GateDestructive {
			t.Fatalf("git %v gate class should be %s, got %s", args, GateDestructive, o.Gate.Class)
		}
	}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
