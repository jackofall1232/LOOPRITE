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

// bareRemote creates a bare repo to serve as a push target and returns its path.
func bareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitFixture(t, dir, "init", "--bare", "-q")
	return dir
}

// srcWithCommit makes a working repo with one commit on a feature branch wired to a bare origin,
// and returns (srcPath, bareRemotePath, branchName).
func srcWithCommit(t *testing.T) (string, string, string) {
	t.Helper()
	src := t.TempDir()
	initGitRepo(t, src)
	writeFile(t, src, "README.md", "# push test\n")
	gitFixture(t, src, "add", "-A")
	gitFixture(t, src, "commit", "-q", "-m", "init")
	branch := "l00prite/feature-x"
	gitFixture(t, src, "checkout", "-q", "-B", branch)
	writeFile(t, src, "unit.txt", "work\n")
	gitFixture(t, src, "add", "-A")
	gitFixture(t, src, "commit", "-q", "-m", "unit")
	bare := bareRemote(t)
	gitFixture(t, src, "remote", "add", "origin", bare)
	return src, bare, branch
}

// TestPushToLocalBareRemote exercises Push (nil auth, local remote) on every available backend and
// asserts the branch ref actually landed on the remote — the core "the AI can push" guarantee.
func TestPushToLocalBareRemote(t *testing.T) {
	for kind, c := range clients(t) {
		t.Run(kind, func(t *testing.T) {
			src, bare, branch := srcWithCommit(t)
			if err := c.Push(context.Background(), src, "origin", branch, nil); err != nil {
				t.Fatalf("Push: %v", err)
			}
			out := strings.TrimSpace(gitFixture(t, bare, "rev-parse", "refs/heads/"+branch))
			if len(out) != 40 {
				t.Fatalf("branch %q did not land on the remote (rev-parse = %q)", branch, out)
			}
			// A second push with nothing new is "already up to date" -> nil, not an error.
			if err := c.Push(context.Background(), src, "origin", branch, nil); err != nil {
				t.Fatalf("second Push (already up to date) should be nil, got %v", err)
			}
		})
	}
}

// TestRemoteURL asserts both backends read origin's configured URL.
func TestRemoteURL(t *testing.T) {
	for kind, c := range clients(t) {
		t.Run(kind, func(t *testing.T) {
			src, bare, _ := srcWithCommit(t)
			got, err := c.RemoteURL(src, "origin")
			if err != nil {
				t.Fatalf("RemoteURL: %v", err)
			}
			if got != bare {
				t.Fatalf("RemoteURL = %q, want %q", got, bare)
			}
			if _, err := c.RemoteURL(src, "nope"); err == nil {
				t.Fatal("RemoteURL for a missing remote should error")
			}
		})
	}
}

// TestGogitPushRefusesShallow pins the load-bearing Android guard: a shallow clone cannot push via
// go-git, so Push must fail closed with ErrShallowPush WITHOUT touching the remote.
func TestGogitPushRefusesShallow(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available to build the shallow fixture")
	}
	// Build a real remote with two commits, then shallow-clone it (depth 1) with the git binary.
	origin := t.TempDir()
	initGitRepo(t, origin)
	writeFile(t, origin, "a.txt", "1\n")
	gitFixture(t, origin, "add", "-A")
	gitFixture(t, origin, "commit", "-q", "-m", "c1")
	writeFile(t, origin, "a.txt", "2\n")
	gitFixture(t, origin, "add", "-A")
	gitFixture(t, origin, "commit", "-q", "-m", "c2")

	shallow := filepath.Join(t.TempDir(), "shallow")
	gitFixture(t, ".", "clone", "-q", "--depth", "1", "file://"+origin, shallow)
	// Sanity: the fixture really is shallow.
	if !isShallow(shallow) {
		t.Fatal("fixture is not shallow; the test cannot prove the guard")
	}

	err := gogitClient{}.Push(context.Background(), shallow, "origin", "master", &PushAuth{Username: "x", Token: "unused"})
	if !errors.Is(err, ErrShallowPush) {
		t.Fatalf("shallow Push should return ErrShallowPush, got %v", err)
	}
}

// TestExecExtraHeaderEnvKeepsTokenOutOfArgvAndCleartext pins the token-leak-avoidance design:
// the credential is injected only through env-only git config, base64'd into the header value,
// and never appears as cleartext (in argv, the config KEY, or anywhere a `-c` flag or a rewritten
// remote URL would put it). Written to fail against a version that used `-c http.extraheader=...`.
func TestExecExtraHeaderEnvKeepsTokenOutOfArgvAndCleartext(t *testing.T) {
	token := "ghp_SECRETtokenVALUE123"
	env, b64 := extraHeaderEnv(0, "https://github.com/", "x-access-token", token)
	if len(env) != 3 || env[0] != "GIT_CONFIG_COUNT=1" {
		t.Fatalf("unexpected env shape: %v", env)
	}
	if env[1] != "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader" {
		t.Fatalf("key not host-scoped: %q", env[1])
	}
	// Appends after host-set entries: startIndex 2 -> COUNT=3, KEY_2/VALUE_2 (never clobbers 0/1).
	env2, _ := extraHeaderEnv(2, "https://github.com/", "x-access-token", token)
	if env2[0] != "GIT_CONFIG_COUNT=3" || env2[1] != "GIT_CONFIG_KEY_2=http.https://github.com/.extraheader" {
		t.Fatalf("append-at-index shape wrong: %v", env2)
	}
	for _, e := range env {
		if strings.Contains(e, token) {
			t.Fatalf("raw token leaked in env entry %q — must be base64 in the value only", e)
		}
	}
	if !strings.Contains(env[2], b64) {
		t.Fatalf("value entry %q does not carry the base64 credential", env[2])
	}
	// The push argv is fixed and names only the remote + refspec — never the token.
	argv := []string{"push", "-u", "origin", pushRefspec("l00prite/x")}
	for _, a := range argv {
		if strings.Contains(a, token) || strings.Contains(a, b64) {
			t.Fatalf("credential leaked into argv element %q", a)
		}
	}
}

// TestExecPushFallsBackToAmbientForNonMatchingRemote: supplying a github.com-scoped token for a
// remote that is NOT an https github.com URL (here a local path) must NOT error and must NOT attach
// the token — the push proceeds with ambient credentials. Pins that connecting a token never breaks
// pushing to a remote the token isn't for (and never leaks it there). Written to fail against the
// prior version, which returned an error for a non-http(s) remote.
func TestExecPushFallsBackToAmbientForNonMatchingRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src, bare, branch := srcWithCommit(t) // origin is a local filesystem path
	c := execClient{}
	if err := c.Push(context.Background(), src, "origin", branch,
		&PushAuth{Username: "x-access-token", Token: "ghp_secret", Host: "github.com"}); err != nil {
		t.Fatalf("token push to a non-matching (local) remote should fall back to ambient and succeed, got %v", err)
	}
	if len(strings.TrimSpace(gitFixture(t, bare, "rev-parse", "refs/heads/"+branch))) != 40 {
		t.Fatal("branch did not land on the local remote via the ambient fallback")
	}
}

// TestExecPushInjectsTokenViaEnvNeverArgv drives a REAL exec.Push against a fake `git` on PATH that
// records its argv and GIT_CONFIG_* env on every invocation. It pins the core security property at
// the Push level (not just the extraHeaderEnv helper): for an https github.com remote the token is
// injected ONLY as a host-scoped GIT_CONFIG extraheader, and the RAW token appears in NEITHER the
// argv NOR anywhere in cleartext. Written to fail against a version that put the token in argv
// (e.g. `-c http.extraheader=...` or a token-in-URL remote).
func TestExecPushInjectsTokenViaEnvNeverArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script fake not portable to Windows")
	}
	dir := t.TempDir()
	logf := filepath.Join(dir, "git.log")
	// The fake dumps the extraheader env via `printf` (a shell builtin — no PATH dependency, since
	// PATH below is narrowed so `git` resolves to this fake) rather than `env|grep`.
	script := "#!/bin/sh\n" +
		"echo \"ARGV: $*\" >> " + logf + "\n" +
		"printf 'CFG COUNT=%s KEY0=%s VALUE0=%s\\n' \"$GIT_CONFIG_COUNT\" \"$GIT_CONFIG_KEY_0\" \"$GIT_CONFIG_VALUE_0\" >> " + logf + "\n" +
		"case \"$*\" in\n" +
		"  *get-url*) echo 'https://github.com/o/r.git' ;;\n" + // matches `remote get-url --push origin`
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// dir first so `git` resolves to the fake; keep the rest of PATH so /bin/sh's own lookups work.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Some CI/sandboxes pre-set GIT_CONFIG_COUNT; pin it to 0 so the extraheader lands at index 0
	// deterministically (the fake dumps KEY_0). The append behavior itself is covered by the
	// extraHeaderEnv unit test.
	t.Setenv("GIT_CONFIG_COUNT", "0")

	token := "ghp_SUPERSECRET_value_123"
	if err := (execClient{}).Push(context.Background(), t.TempDir(), "origin", "l00prite/run-x",
		&PushAuth{Username: "x-access-token", Token: token, Host: "github.com"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	b, err := os.ReadFile(logf)
	if err != nil {
		t.Fatalf("fake git never ran: %v", err)
	}
	out := string(b)
	if strings.Contains(out, token) {
		t.Fatalf("raw token leaked into git argv/env:\n%s", out)
	}
	if !strings.Contains(out, "KEY0=http.https://github.com/.extraheader") {
		t.Fatalf("host-scoped extraheader was not injected via env:\n%s", out)
	}
	if !strings.Contains(out, "push -u origin refs/heads/l00prite/run-x:refs/heads/l00prite/run-x") {
		t.Fatalf("unexpected push argv:\n%s", out)
	}
	// Push must never persist the credential: it runs no `git config` (the extraheader is env-only).
	if strings.Contains(out, " config ") {
		t.Fatalf("Push ran a `git config` — the token must never be written to .git/config:\n%s", out)
	}
}

// TestBuildPushEnvStripsDuplicateCount pins the fix for a duplicate GIT_CONFIG_COUNT: when the host
// env already sets one, buildPushEnv must remove it and emit exactly ONE (the new value) — git (C
// getenv) reads the FIRST match, so a leftover would win and the injected extraheader be ignored.
// Existing GIT_CONFIG_KEY_*/VALUE_* are preserved; the header appends at the next index.
func TestBuildPushEnvStripsDuplicateCount(t *testing.T) {
	base := []string{
		"PATH=/x", "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.interactive", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=http.proxy", "GIT_CONFIG_VALUE_1=p",
	}
	out, b64 := buildPushEnv(base, "https://github.com/", "x-access-token", "ghp_tok")
	countN, countVal := 0, ""
	for _, e := range out {
		if strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			countN++
			countVal = e
		}
	}
	if countN != 1 || countVal != "GIT_CONFIG_COUNT=3" {
		t.Fatalf("want exactly one GIT_CONFIG_COUNT=3, got %d (%q): %v", countN, countVal, out)
	}
	joined := strings.Join(out, "\n")
	// host git-config-env preserved, and the header appended at the next free index (2)
	for _, want := range []string{
		"GIT_CONFIG_KEY_0=credential.interactive", "GIT_CONFIG_KEY_1=http.proxy",
		"GIT_CONFIG_KEY_2=http.https://github.com/.extraheader",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in built env: %v", want, out)
		}
	}
	if strings.Contains(joined, "ghp_tok") || b64 == "" {
		t.Fatal("raw token must not appear in cleartext; b64 must be set")
	}
}

// TestExecPushRedactsTokenInError proves redactSecrets is actually WIRED INTO Push's returned error
// (not just unit-tested in isolation): a fake git whose push exits non-zero while echoing the token
// (as a hostile or verbose remote might) must yield a Push error with the token scrubbed out.
func TestExecPushRedactsTokenInError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script fake not portable to Windows")
	}
	token := "ghp_leakcheck_TOKEN_9z"
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *get-url*) echo 'https://github.com/o/r.git'; exit 0 ;;\n" + // remote get-url --push
		"  *) echo 'remote: rejected (token " + token + ")'; exit 1 ;;\n" + // the actual push
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := (execClient{}).Push(context.Background(), t.TempDir(), "origin", "b",
		&PushAuth{Username: "x-access-token", Token: token, Host: "github.com"})
	if err == nil {
		t.Fatal("expected the failed push to return an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked into the push error: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("expected the token to be replaced with [redacted], got: %v", err)
	}
}

// TestCredentialScope pins the token-host-scoping guard (the fix for a github.com token being
// transmitted to a foreign origin): the credential attaches ONLY to an https URL on its own host —
// never to a foreign https host, an ssh remote, a cleartext http URL, or with an empty Host.
func TestCredentialScope(t *testing.T) {
	gh := &PushAuth{Username: "x", Token: "t", Host: "github.com"}
	cases := []struct {
		url  string
		auth *PushAuth
		ok   bool
	}{
		{"https://github.com/o/r.git", gh, true},
		{"https://GitHub.com/o/r.git", gh, true},                     // case-insensitive host
		{"https://github.com:443/o/r.git", gh, true},                 // explicit default port still matches
		{"https://gitlab.com/o/r.git", gh, false},                    // foreign host — the leak vector
		{"https://evil.example/o/r.git", gh, false},                  // attacker-controlled host
		{"http://github.com/o/r.git", gh, false},                     // cleartext http refused
		{"git@github.com:o/r.git", gh, false},                        // ssh scp-like
		{"ssh://git@github.com/o/r.git", gh, false},                  // ssh
		{"https://github.com/o/r.git", &PushAuth{Token: "t"}, false}, // empty Host fail-closed
		{"https://github.com/o/r.git", nil, false},
	}
	for _, c := range cases {
		scope, ok := credentialScope(c.url, c.auth)
		if ok != c.ok {
			t.Errorf("credentialScope(%q) ok=%v, want %v (scope %q)", c.url, ok, c.ok, scope)
		}
		if ok && !strings.Contains(strings.ToLower(scope), "github.com") {
			t.Errorf("credentialScope(%q) scope=%q not host-scoped to github.com", c.url, scope)
		}
	}
}

// TestRedactSecrets: the redaction backstop replaces every non-empty secret and ignores empties.
func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("push failed: token=ghp_abc header basic Zm9v", "ghp_abc", "Zm9v", "")
	if strings.Contains(got, "ghp_abc") || strings.Contains(got, "Zm9v") {
		t.Fatalf("secret survived redaction: %q", got)
	}
	if got != "push failed: token=[redacted] header basic [redacted]" {
		t.Fatalf("unexpected redaction: %q", got)
	}
}
