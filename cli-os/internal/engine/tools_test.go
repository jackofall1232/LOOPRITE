package engine

import (
	"context"
	"errors"
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

// ---- push_branch ----

func newRepoOnBranch(t *testing.T, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFileRaw(t, filepath.Join(dir, "f.txt"), "x\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")
	gitRun(t, dir, "checkout", "-B", branch)
	return dir
}

// TestPushBranchGatesOnGatePush: an unapproved push_branch on a capable (exec) backend suspends on
// the GatePush class, naming the run's own branch — nothing is pushed until approval.
func TestPushBranchGatesOnGatePush(t *testing.T) {
	dir := newRepoOnBranch(t, "l00prite/run-x")
	tb := &Toolbox{Root: dir, Branch: "l00prite/run-x", Git: gitx.Detect()}
	o := tb.Execute(context.Background(), "push_branch", map[string]any{}, false)
	if o.Gate == nil || o.Gate.Class != GatePush {
		t.Fatalf("push_branch should gate on GatePush, got gate=%+v result=%q", o.Gate, o.Result)
	}
	if o.Gate.Args["branch"] != "l00prite/run-x" || o.Gate.Args["remote"] != "origin" {
		t.Fatalf("gate must be pinned to origin + the run branch, got %v", o.Gate.Args)
	}
}

// TestPushBranchGogitNoCredentialIsCapabilityGap: on a host with no git binary AND no GitHub
// connection there is no way to push, so push_branch returns a plain capability gap — NOT a gate
// (no human approval could conjure a credential).
func TestPushBranchGogitNoCredentialIsCapabilityGap(t *testing.T) {
	dir := newGogitTestRepo(t)
	tb := &Toolbox{Root: dir, Branch: "l00prite/run-x", Git: gitx.NewGogitClient()} // PushCred nil
	o := tb.Execute(context.Background(), "push_branch", map[string]any{}, false)
	if o.Gate != nil {
		t.Fatalf("a no-credential gogit push has nothing to approve; it must not gate, got %+v", o.Gate)
	}
	if !strings.Contains(o.Result, "Connect GitHub") {
		t.Fatalf("expected a connect-GitHub capability gap, got %q", o.Result)
	}
}

// TestPushBranchWithCredentialGates: a gogit backend WITH a credential is capable, so push_branch
// consults PushCred and then gates (rather than reporting a capability gap).
func TestPushBranchWithCredentialGates(t *testing.T) {
	dir := newGogitTestRepo(t)
	called := false
	tb := &Toolbox{Root: dir, Branch: "l00prite/run-x", Git: gitx.NewGogitClient(),
		PushCred: func() (*gitx.PushAuth, error) { called = true; return &gitx.PushAuth{Username: "x", Token: "t"}, nil }}
	o := tb.Execute(context.Background(), "push_branch", map[string]any{}, false)
	if !called {
		t.Fatal("push_branch must consult PushCred")
	}
	if o.Gate == nil || o.Gate.Class != GatePush {
		t.Fatalf("with a credential present the push is capable and should gate, got gate=%+v result=%q", o.Gate, o.Result)
	}
}

// TestPushBranchApprovedSchedulesThenPushes: an approved push_branch does NOT push inline (that
// would ship the pre-unit HEAD, before CommitUnit) — it SCHEDULES the push, and PerformPendingPush
// (which the engine calls after committing the unit) is what actually lands the branch on origin.
func TestPushBranchApprovedSchedulesThenPushes(t *testing.T) {
	dir := newRepoOnBranch(t, "l00prite/run-x")
	bare := t.TempDir()
	gitRun(t, bare, "init", "--bare")
	gitRun(t, dir, "remote", "add", "origin", bare)

	tb := &Toolbox{Root: dir, Branch: "l00prite/run-x", Git: gitx.Detect()}
	o := tb.Execute(context.Background(), "push_branch", map[string]any{}, true) // approved
	if o.Gate != nil {
		t.Fatalf("approved push should not gate, got %+v", o.Gate)
	}
	if !strings.Contains(o.Result, `"status":"push_scheduled"`) || !tb.PushRequested {
		t.Fatalf("approved push_branch must SCHEDULE (not push inline): result=%q PushRequested=%v", o.Result, tb.PushRequested)
	}
	// Nothing on origin yet — the push is deferred until the engine commits the unit.
	if out := gitRun(t, bare, "branch", "--list", "l00prite/run-x"); strings.TrimSpace(out) != "" {
		t.Fatalf("branch must not be on origin before the deferred push: %q", out)
	}
	// The engine's post-commit step performs the pending push.
	pushed, err := tb.PerformPendingPush(context.Background())
	if err != nil || !pushed {
		t.Fatalf("PerformPendingPush: pushed=%v err=%v", pushed, err)
	}
	if got := strings.TrimSpace(gitRun(t, bare, "rev-parse", "refs/heads/l00prite/run-x")); len(got) != 40 {
		t.Fatalf("run branch did not land on the bare origin after the deferred push: %q", got)
	}
	// A second PerformPendingPush with the flag still set is idempotent (already up to date -> nil).
	if _, err := tb.PerformPendingPush(context.Background()); err != nil {
		t.Fatalf("second PerformPendingPush should be a no-op/nil, got %v", err)
	}
}

// ---- git_command under the gogit fallback (Kind()!="exec") ----

// gogitRefusal is the exact, unchanged hard-refusal message any out-of-contract git_command call
// still gets under the gogit fallback.
const gogitRefusal = "ERROR: git passthrough requires the git binary on this host"

func newGogitTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")
	writeFileRaw(t, filepath.Join(dir, "extra.txt"), "brand new content\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add extra.txt")
	return dir
}

func TestGitCommandGogitStatusReachesStatusPorcelain(t *testing.T) {
	dir := newGogitTestRepo(t)
	writeFileRaw(t, filepath.Join(dir, "dirty.txt"), "x")
	tb := &Toolbox{Root: dir, Git: gitx.NewGogitClient()}

	o := tb.Execute(context.Background(), "git_command", map[string]any{"args": []string{"status"}}, false)
	if o.Gate != nil {
		t.Fatalf("gogit status should not gate, got %+v", o.Gate)
	}
	if !strings.Contains(o.Result, "exit_code: 0") || !strings.Contains(o.Result, "dirty.txt") {
		t.Fatalf("gogit status result unexpected (want StatusPorcelain output): %q", o.Result)
	}
}

func TestGitCommandGogitDiffReachesDiffHead(t *testing.T) {
	dir := newGogitTestRepo(t)
	writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\nmodified\n")
	tb := &Toolbox{Root: dir, Git: gitx.NewGogitClient()}

	for _, args := range [][]string{{"diff"}, {"diff", "HEAD"}} {
		o := tb.Execute(context.Background(), "git_command", map[string]any{"args": toAnySlice(args)}, false)
		if o.Gate != nil {
			t.Fatalf("gogit %v should not gate, got %+v", args, o.Gate)
		}
		if !strings.Contains(o.Result, "exit_code: 0") {
			t.Fatalf("gogit %v result unexpected: %q", args, o.Result)
		}
	}
}

func TestGitCommandGogitLogReachesLog(t *testing.T) {
	dir := newGogitTestRepo(t)
	tb := &Toolbox{Root: dir, Git: gitx.NewGogitClient()}
	ctx := context.Background()

	// Every accepted shape reaches gitx.Client.Log and succeeds without gating.
	for _, args := range [][]string{
		{"log"},
		{"log", "-n", "1"},
		{"log", "--max-count=1"},
		{"log", "-1"},
	} {
		o := tb.Execute(ctx, "git_command", map[string]any{"args": toAnySlice(args)}, false)
		if o.Gate != nil {
			t.Fatalf("gogit %v should not gate, got %+v", args, o.Gate)
		}
		if !strings.Contains(o.Result, "exit_code: 0") || !strings.Contains(o.Result, "add extra.txt") {
			t.Fatalf("gogit %v result unexpected: %q", args, o.Result)
		}
	}

	// -n 1 / --max-count=1 / -1 must all cap the result to exactly the single most recent commit.
	o := tb.Execute(ctx, "git_command", map[string]any{"args": []any{"log", "-n", "1"}}, false)
	if strings.Contains(o.Result, "\ninit\n") || strings.Count(o.Result, "\n") > 2 {
		t.Fatalf("gogit log -n 1 should return exactly one commit line, got: %q", o.Result)
	}
}

func TestGitCommandGogitShowReachesShow(t *testing.T) {
	dir := newGogitTestRepo(t)
	tb := &Toolbox{Root: dir, Git: gitx.NewGogitClient()}

	o := tb.Execute(context.Background(), "git_command", map[string]any{"args": []string{"show", "HEAD"}}, false)
	if o.Gate != nil {
		t.Fatalf("gogit show HEAD should not gate, got %+v", o.Gate)
	}
	if !strings.Contains(o.Result, "exit_code: 0") || !strings.Contains(o.Result, "add extra.txt") {
		t.Fatalf("gogit show HEAD result unexpected: %q", o.Result)
	}
	if !strings.Contains(o.Result, "+brand new content") {
		t.Fatalf("gogit show HEAD should show the added file's content as an addition: %q", o.Result)
	}
}

func TestGitCommandGogitShowUnknownRefIsGitxError(t *testing.T) {
	dir := newGogitTestRepo(t)
	tb := &Toolbox{Root: dir, Git: gitx.NewGogitClient()}

	o := tb.Execute(context.Background(), "git_command", map[string]any{"args": []string{"show", "not-a-real-ref"}}, false)
	if o.Gate != nil {
		t.Fatalf("gogit show of an unknown ref should not gate, got %+v", o.Gate)
	}
	if !strings.HasPrefix(o.Result, "ERROR:") || strings.Contains(o.Result, "exit_code") {
		t.Fatalf("gogit show of an unknown ref should be a plain gitx-error ERROR, got: %q", o.Result)
	}
}

// TestGitCommandGogitOutOfContractFallsThrough proves that argument shapes outside the narrow
// gogit contract still get the exact, unchanged hard-refusal — never a panic, never a silent
// "probably fine" execution.
func TestGitCommandGogitOutOfContractFallsThrough(t *testing.T) {
	dir := newGogitTestRepo(t)
	tb := &Toolbox{Root: dir, Git: gitx.NewGogitClient()}
	ctx := context.Background()

	cases := [][]string{
		{"status", "--porcelain"}, // status takes no extra args in the gogit contract
		{"diff", "--stat"},        // only bare diff / diff HEAD are accepted
		{"diff", "main"},
		{"log", "--oneline"},       // unsupported flag shape
		{"log", "-n", "-5"},        // non-positive count
		{"log", "-n", "abc"},       // unparseable count
		{"log", "-0"},              // leading-zero-equivalent / non-positive shorthand
		{"show"},                   // 0 refs
		{"show", "HEAD", "HEAD~1"}, // 2 refs
		{"show", "--stat", "HEAD"}, // a flag
		{"add", "-A"},              // not in the subset at all
		{"commit", "-m", "x"},      // not in the subset at all
	}
	for _, args := range cases {
		o := tb.Execute(ctx, "git_command", map[string]any{"args": toAnySlice(args)}, false)
		if o.Gate != nil {
			t.Fatalf("gogit %v should not gate (it's refused outright, not gated), got %+v", args, o.Gate)
		}
		if !strings.HasPrefix(o.Result, gogitRefusal) {
			t.Fatalf("gogit %v should get the unchanged hard refusal, got: %q", args, o.Result)
		}
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

// TestCommitUnitResetsIndexOnFailedCommit pins a PR #6 review finding: CommitUnit's AddAll
// (`git add -A`) stages everything before Commit runs, but a Commit failure for any reason other
// than "nothing to commit" or a missing identity (both already handled inside Commit itself) --
// e.g. a rejecting pre-commit hook, a full disk -- used to leave that staging in place with no
// commit to show for it, a surprising git state for whoever looks at the repo next. Only the exec
// backend can be proven here: it's the only implementation with a working Raw() passthrough to
// issue the best-effort `git reset` (gogit's Raw is unconditionally unsupported).
func TestCommitUnitResetsIndexOnFailedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")

	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	writeFileRaw(t, hookPath, "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(hookPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileRaw(t, filepath.Join(dir, "new.txt"), "x\n")

	hash, err := CommitUnit(gitx.Detect(), dir, "should be rejected by the hook")
	if err == nil {
		t.Fatalf("expected the pre-commit hook to reject the commit, got hash=%q", hash)
	}

	status := gitRun(t, dir, "status", "--porcelain")
	if strings.Contains(status, "A  new.txt") {
		t.Fatalf("expected new.txt to be unstaged (reset) after the failed commit, got status: %q", status)
	}
	if !strings.Contains(status, "?? new.txt") {
		t.Fatalf("expected new.txt to still be present as untracked after the reset, got status: %q", status)
	}
}

// ---- AutoCheckpoint (Bug 2 regression: raw "worktree contains unstaged changes" on Start) ----

// setupTrackedDirtyL00prite builds a repo whose .l00prite/lock.json is already git-tracked — as if
// scaffolded and committed via Planning Mode before the engine's first run — then dirties it,
// exactly what StartRun's own AcquireLock call does immediately before EnsureRunBranch.
func setupTrackedDirtyL00prite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")

	lockPath := filepath.Join(dir, ".l00prite", "lock.json")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileRaw(t, lockPath, `{"status":"released"}`)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "scaffold .l00prite")

	writeFileRaw(t, lockPath, `{"status":"active","lock_id":"lck_test"}`)
	return dir
}

// TestGogitCheckoutFailsOnTrackedDirtyL00prite pins the bug this diagnosis found: under the gogit
// backend, EnsureRunBranch's own .l00prite/ exemption (dirtyPathsOutsideL00prite) is invisible to
// go-git's internal Checkout call, which runs its own whole-tree, unconditional dirty check with no
// path exemption. A dirty-but-already-tracked .l00prite/ file — exactly what StartRun's
// AcquireLock produces by rewriting lock.json — makes the checkout fail with the raw go-git
// sentinel error, even though EnsureRunBranch itself considers the tree clean enough to proceed.
// This test must keep failing (i.e. keep reproducing the bug) if AutoCheckpoint is ever removed
// from StartRun's call path without a replacement.
func TestGogitCheckoutFailsOnTrackedDirtyL00prite(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := setupTrackedDirtyL00prite(t)
	gogit := gitx.NewGogitClient()
	err := EnsureRunBranch(gogit, dir, "l00prite/run-repro")
	if err == nil {
		t.Fatal("expected the unfixed gogit checkout to fail on a dirty tracked .l00prite/ file")
	}
	if !strings.Contains(err.Error(), "worktree contains unstaged changes") {
		t.Fatalf("expected the raw go-git ErrUnstagedChanges text, got: %v", err)
	}
}

// TestAutoCheckpointFixesGogitCheckout proves the fix: committing everything dirty — including
// .l00prite/ — before EnsureRunBranch's checkout call makes it succeed on the gogit backend,
// mirroring StartRun's real call order (engine.go: AutoCheckpoint runs right after AcquireLock,
// before EnsureRunBranch).
func TestAutoCheckpointFixesGogitCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := setupTrackedDirtyL00prite(t)
	gogit := gitx.NewGogitClient()

	hash, err := AutoCheckpoint(gogit, dir, "repro", nil)
	if err != nil {
		t.Fatalf("AutoCheckpoint: %v", err)
	}
	if hash == "" {
		t.Fatal("expected a checkpoint commit hash for a dirty tree")
	}
	if err := EnsureRunBranch(gogit, dir, "l00prite/run-repro"); err != nil {
		t.Fatalf("EnsureRunBranch after checkpoint should succeed, got: %v", err)
	}
	msg := strings.TrimSpace(gitRun(t, dir, "log", "-1", "--format=%s"))
	if msg != "WIP: auto-checkpoint before run-repro" {
		t.Fatalf("expected the checkpoint commit message on HEAD, got: %q", msg)
	}

	// AutoCheckpoint on an already-clean tree is a no-op (both gitx Commit contracts treat
	// "nothing to commit" as success) -- never a duplicate/empty commit.
	hash2, err := AutoCheckpoint(gogit, dir, "repro", nil)
	if err != nil {
		t.Fatalf("AutoCheckpoint on a clean tree: %v", err)
	}
	if hash2 != "" {
		t.Fatalf("expected no-op on a clean tree, got a new commit hash %q", hash2)
	}
}

// setupTrackedDirtyL00priteWithLedger is setupTrackedDirtyL00prite plus an already-tracked,
// already-committed ledger.md — the shape of a repo whose .l00prite/ was scaffolded and committed
// (by Planning Mode, or by any run before this one) before StartRun's own AcquireLock dirties
// lock.json.
func setupTrackedDirtyL00priteWithLedger(t *testing.T) string {
	t.Helper()
	dir := setupTrackedDirtyL00prite(t)
	ledgerPath := filepath.Join(dir, ".l00prite", "ledger.md")
	writeFileRaw(t, ledgerPath, "# Run Ledger\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "scaffold ledger.md")
	// re-dirty lock.json the same way the helper's caller expects (setupTrackedDirtyL00prite
	// already left it dirty; the commit above only touched ledger.md, so lock.json is still dirty).
	return dir
}

// TestLedgerAppendBeforeCheckoutReDirtiesTrackedFile pins the exact failure mode PR #6 review
// caught: if a ledger.md entry documenting the checkpoint is appended to an already-tracked
// ledger.md BEFORE EnsureRunBranch's gogit checkout runs, it re-dirties the tree that
// AutoCheckpoint just cleaned -- reproducing the identical "worktree contains unstaged changes"
// failure this whole checkpoint mechanism exists to prevent, just with ledger.md as the trigger
// instead of lock.json. This must keep failing (i.e. keep proving the ordering matters) for as
// long as StartRun writes the checkpoint's ledger entry via Files.AppendLedger.
func TestLedgerAppendBeforeCheckoutReDirtiesTrackedFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := setupTrackedDirtyL00priteWithLedger(t)
	gogit := gitx.NewGogitClient()

	if _, err := AutoCheckpoint(gogit, dir, "repro", nil); err != nil {
		t.Fatalf("AutoCheckpoint: %v", err)
	}

	// Simulate the OLD (buggy) ordering: append the checkpoint's ledger entry before the checkout,
	// exactly as StartRun used to.
	if err := (Files{Root: dir}).AppendLedger(LedgerEntry{
		Timestamp: "2026-07-11T00:00:00Z", RunID: "repro", Goal: "auto-checkpoint before run repro",
	}); err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}

	err := EnsureRunBranch(gogit, dir, "l00prite/run-repro")
	if err == nil {
		t.Fatal("expected the gogit checkout to fail once the ledger append re-dirtied a tracked file")
	}
	if !strings.Contains(err.Error(), "worktree contains unstaged changes") {
		t.Fatalf("expected the raw go-git ErrUnstagedChanges text, got: %v", err)
	}
}

// TestLedgerAppendAfterCheckoutSucceeds proves the fix: appending the checkpoint's ledger entry
// AFTER EnsureRunBranch's checkout (StartRun's current order) never re-dirties the tree the
// checkout depends on, so the checkout succeeds even when ledger.md is already tracked.
func TestLedgerAppendAfterCheckoutSucceeds(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := setupTrackedDirtyL00priteWithLedger(t)
	gogit := gitx.NewGogitClient()

	if _, err := AutoCheckpoint(gogit, dir, "repro", nil); err != nil {
		t.Fatalf("AutoCheckpoint: %v", err)
	}
	if err := EnsureRunBranch(gogit, dir, "l00prite/run-repro"); err != nil {
		t.Fatalf("EnsureRunBranch after checkpoint should succeed, got: %v", err)
	}
	if err := (Files{Root: dir}).AppendLedger(LedgerEntry{
		Timestamp: "2026-07-11T00:00:00Z", RunID: "repro", Goal: "auto-checkpoint before run repro",
	}); err != nil {
		t.Fatalf("AppendLedger after checkout: %v", err)
	}
	cur := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))
	if cur != "l00prite/run-repro" {
		t.Fatalf("expected to be on the run branch, got %q", cur)
	}
}

// A dirty path that matches the project's Denylist, or looks like it may hold credentials, must
// never be silently committed by AutoCheckpoint (adversarial-review finding: the pre-run
// checkpoint was a second, ungoverned write path that bypassed the same protection write_file
// goes through mid-run). Nothing is committed when refused.
func TestAutoCheckpointRefusesDenylistedAndSecretPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	git := gitx.Detect()

	t.Run("Denylist match", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-m", "init")
		writeFileRaw(t, filepath.Join(dir, "prod.config.js"), "module.exports = {}\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-m", "add config")
		writeFileRaw(t, filepath.Join(dir, "prod.config.js"), "module.exports = { changed: true }\n")

		hash, err := AutoCheckpoint(git, dir, "repro", []string{"prod.config.js"})
		if !errors.Is(err, ErrCheckpointRefused) {
			t.Fatalf("expected ErrCheckpointRefused, got hash=%q err=%v", hash, err)
		}
		if out := strings.TrimSpace(gitRun(t, dir, "status", "--porcelain")); out == "" {
			t.Fatal("the denylisted file must remain uncommitted after a refused checkpoint")
		}
	})

	t.Run("secret-shaped path, no Denylist entry needed", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-m", "init")
		writeFileRaw(t, filepath.Join(dir, ".env"), "API_KEY=one\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-m", "add env")
		writeFileRaw(t, filepath.Join(dir, ".env"), "API_KEY=two\n")

		hash, err := AutoCheckpoint(git, dir, "repro", nil) // empty Denylist -- secret pattern alone must still refuse
		if !errors.Is(err, ErrCheckpointRefused) {
			t.Fatalf("expected ErrCheckpointRefused for a secret-shaped path even with no Denylist entry, got hash=%q err=%v", hash, err)
		}
	})

	t.Run("non-matching dirty file still checkpoints normally", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		writeFileRaw(t, filepath.Join(dir, "README.md"), "hello\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-m", "init")
		writeFileRaw(t, filepath.Join(dir, "notes.txt"), "unrelated work\n")

		hash, err := AutoCheckpoint(git, dir, "repro", []string{"prod.config.js"})
		if err != nil || hash == "" {
			t.Fatalf("expected a normal checkpoint for a non-matching file, got hash=%q err=%v", hash, err)
		}
	})
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
