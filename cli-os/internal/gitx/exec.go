package gitx

// execClient shells out to the system "git" binary. It is byte-for-byte today's (pre-gitx)
// behavior — the ONLY implementation that existed before this package did — ported here unchanged
// as one seam so a desktop host with git installed sees no difference at all. It stays strictly
// more capable than gogitClient (arbitrary subcommands, ssh transport, the host's own gitconfig
// and credential helpers), which is exactly why Detect prefers it whenever a git binary exists.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// gitTimeoutSec mirrors the timeout the engine used for these operations before this seam existed
// (internal/engine/tools.go's gitTimeoutSec constant).
const gitTimeoutSec = 60

type execClient struct{}

func (execClient) Kind() string { return "exec" }

// run execs `git -C repo <args...>` under ctx, scrubbing the two secret env vars that must never
// reach a child process (Android G8 — see docs/android-architecture.md §4). It returns the raw
// combined output alongside whatever error exec.Cmd.CombinedOutput produced (untyped-error-safe:
// callers that need the *exec.ExitError, like the model-facing git_command tool, run their own
// exec.Command directly rather than going through this seam — see tools.go's gitCommand).
//
// LC_ALL=C forces git's own output to English regardless of the host's locale: Commit,
// identityMissing, and Log all recognize failure modes ("nothing to commit", "Please tell me who
// you are", the empty-repo log marker) by matching English substrings in this output, and a
// localized git build would silently break every one of those checks.
func (c execClient) run(ctx context.Context, repo string, args ...string) (string, error) {
	full := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(util.ScrubSecretEnv(os.Environ()), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runTimed wraps run with the shared gitTimeoutSec bound and folds the output into the error
// message on failure (so a caller inspecting err.Error() sees both the exit reason and stderr,
// matching the pre-gitx runGit helper's contract).
func (c execClient) runTimed(repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeoutSec*time.Second)
	defer cancel()
	out, err := c.run(ctx, repo, args...)
	if err != nil {
		return out, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return out, nil
}

// Clone shells out exactly as gateway/repos_clone.go did before this seam existed — same flags,
// same env — so cloning behavior never drifts by so much as a byte. A non-nil auth is injected
// the same way Push does it: an env-only, host-scoped http.extraheader (GIT_CONFIG_*), never in
// argv, never written to .git/config, and redacted from any error text (see buildPushEnv).
func (c execClient) Clone(ctx context.Context, url, dest string, depth int, auth *PushAuth) error {
	args := []string{"clone", "--depth", fmt.Sprint(depth), "--", url, dest}
	cmd := exec.CommandContext(ctx, "git", args...)
	// Disable git's own https credential prompt and force ssh into non-interactive BatchMode with
	// a short connect timeout: an unreachable or credential-requiring URL fails fast instead of
	// hanging the request on a TTY nothing here can ever answer.
	env := append(util.ScrubSecretEnv(os.Environ()),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15",
	)
	var b64 string
	if auth != nil && auth.Token != "" {
		// Attach the token ONLY to an https clone URL on the credential's own host (see
		// credentialScope); ssh URLs, foreign hosts and local paths clone anonymously.
		if scope, ok := credentialScope(url, auth); ok {
			env, b64 = buildPushEnv(env, scope, auth.Username, auth.Token)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := redactSecrets(strings.TrimSpace(string(out)), auth.token(), b64)
		return fmt.Errorf("%v: %s", err, msg)
	}
	return nil
}

func (c execClient) RevParseHead(repo string) (string, error) {
	out, err := c.runTimed(repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentBranch returns the name of the currently checked-out branch, or "" (with a nil error)
// on a detached HEAD — "rev-parse --abbrev-ref HEAD" prints the literal string "HEAD" in that
// case, which is not a branch name.
func (c execClient) CurrentBranch(repo string) (string, error) {
	out, err := c.runTimed(repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(out)
	if name == "HEAD" {
		return "", nil
	}
	return name, nil
}

// --untracked-files=all matters: real git's default ("normal") mode collapses an ENTIRELY
// untracked directory to one "?? dirname/" line instead of listing the files inside it (verified:
// `git status --porcelain` on a repo with a brand-new `config/` containing `prod.pem` reports only
// "?? config/"). Every caller of this method (engine.checkGitReady, EnsureRunBranch,
// AutoCheckpoint) walks the returned paths individually -- AutoCheckpoint's Denylist/secret-pattern
// gate in particular needs to see "config/prod.pem", not "config/", or a credential file sitting in
// any brand-new directory sails straight past the gate while `git add -A` (which does not consult
// this output at all) stages and commits it anyway. gogitClient's equivalent has no such collapsing
// (go-git's Worktree.Status() always keys by individual file), so this asymmetry was exec-backend
// -only -- and exec is the DEFAULT backend on every host with a git binary, not just an edge case.
func (c execClient) StatusPorcelain(repo string) (string, error) {
	return c.runTimed(repo, "status", "--porcelain", "--untracked-files=all")
}

func (c execClient) CheckoutNewBranch(repo, name string) error {
	_, err := c.runTimed(repo, "checkout", "-B", name)
	return err
}

func (c execClient) AddAll(repo string) error {
	_, err := c.runTimed(repo, "add", "-A")
	return err
}

// AddPaths force-stages specific repo-relative paths, bypassing .gitignore (`git add -f`) — for
// callers that must guarantee particular generated files land in the next commit regardless of
// the target repo's own ignore rules (e.g. scaffolding `.l00prite/` into a repo that gitignores
// it). A no-op for an empty paths slice, not an error.
func (c execClient) AddPaths(repo string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "-f", "--"}, paths...)
	_, err := c.runTimed(repo, args...)
	return err
}

// commitIdentityMissing are the git stderr substrings emitted when no user.name/user.email is
// configured anywhere (system/global/local) — exactly the failure a first-boot Android host hits
// with no gitconfig at all.
var commitIdentityMissing = []string{
	"Please tell me who you are",
	"empty ident",
	"unable to auto-detect email address",
}

func identityMissing(msg string) bool {
	for _, s := range commitIdentityMissing {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// Commit tries a plain commit first; if it fails for a missing-identity reason, it retries ONCE
// with an explicit throwaway identity rather than surfacing the failure. Android/first-boot hosts
// have no gitconfig at all (there's no interactive user to run `git config user.email` for them,
// and no HOME-writable ~/.gitconfig necessarily exists yet) — stopping every autonomous run at a
// human-review gate for this would make on-device runs impossible, so the engine supplies a
// clearly-synthetic identity instead. "Nothing to commit" is not an error: it returns ("", nil).
func (c execClient) Commit(repo, message string) (string, error) {
	out, err := c.runTimed(repo, "commit", "-m", message)
	if err == nil {
		return c.RevParseHead(repo)
	}
	if strings.Contains(out, "nothing to commit") || strings.Contains(err.Error(), "nothing to commit") {
		return "", nil
	}
	if !identityMissing(err.Error()) {
		return "", err
	}
	out2, err2 := c.runTimed(repo, "-c", "user.name=l00prite-os", "-c", "user.email=l00prite-os@localhost", "commit", "-m", message)
	if err2 != nil {
		if strings.Contains(out2, "nothing to commit") || strings.Contains(err2.Error(), "nothing to commit") {
			return "", nil
		}
		return "", err2
	}
	return c.RevParseHead(repo)
}

func (c execClient) DiffHead(repo string) (string, error) {
	return c.runTimed(repo, "diff", "HEAD")
}

// emptyRepoLogMarker is the stable substring in real git's stderr when `git log` is run on a
// repository with no commits yet ("fatal: your current branch '<name>' does not have any commits
// yet") — the branch name varies (master/main/whatever init.defaultBranch says), so only the tail
// is matched, mirroring the identityMissing substring-matching convention above.
const emptyRepoLogMarker = "does not have any commits yet"

// Log shells out to `git log --oneline -n <limit>`: --oneline already produces the exact
// "<abbrev-hash> <subject>" shape this seam promises, so it is passed straight through rather than
// reformatted, for byte-parity with what a human running the real command would see. An empty
// repository is not an error (see the Client.Log doc comment) even though real git itself exits
// non-zero for this case (`git log` on an empty repo is `fatal: ... does not have any commits
// yet`, exit 128) — that fatal is recognized and swallowed into ("", nil) here so the seam's
// contract holds for both implementations alike.
func (c execClient) Log(repo string, limit int) (string, error) {
	if limit <= 0 {
		limit = defaultLogLimit
	}
	out, err := c.runTimed(repo, "log", "--oneline", "-n", fmt.Sprint(limit))
	if err != nil {
		if strings.Contains(err.Error(), emptyRepoLogMarker) {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

// Show shells out to `git show <ref>` verbatim — this is the exec implementation, so it can pass
// through untouched rather than hand-building the header/patch format gogitClient.Show must; a
// desktop host with a git binary sees zero behavior change from what `git show` has always
// printed.
func (c execClient) Show(repo string, ref string) (string, error) {
	return c.runTimed(repo, "show", ref)
}

func (c execClient) Raw(ctx context.Context, repo string, args ...string) (string, error) {
	out, err := c.run(ctx, repo, args...)
	if err != nil {
		return out, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return out, nil
}

// gitConfigCount returns the current GIT_CONFIG_COUNT in env (0 if absent or unparseable). Push
// APPENDS its extraheader as the next index rather than clobbering GIT_CONFIG_* the host already set
// (a proxy, a credential helper, prompt config, ...). env is last-value-wins, so appending a higher
// GIT_CONFIG_COUNT is what git reads while the host's existing entries survive at their own indices.
func gitConfigCount(env []string) int {
	n := 0
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_CONFIG_COUNT="); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p >= 0 {
				n = p
			}
		}
	}
	return n
}

// buildPushEnv returns env with the credential extraheader appended at the next free GIT_CONFIG
// index, having FIRST removed any existing GIT_CONFIG_COUNT entry. That removal is load-bearing:
// Go's exec passes duplicate env keys through verbatim, and git (C getenv) reads the FIRST match,
// so a leftover host GIT_CONFIG_COUNT would win over an appended one and git would ignore the
// injected header entirely. Existing GIT_CONFIG_KEY_*/VALUE_* entries are preserved (the header is
// added at index = old count), so host-set git-config-env still applies.
func buildPushEnv(env []string, scope, username, token string) ([]string, string) {
	kv, b64 := extraHeaderEnv(gitConfigCount(env), scope, username, token)
	out := make([]string, 0, len(env)+len(kv))
	for _, e := range env {
		if !strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			out = append(out, e)
		}
	}
	return append(out, kv...), b64
}

// extraHeaderEnv builds the env-only git config (git >= 2.31) that injects an HTTP Authorization
// header scoped to exactly `scope` (a "<scheme>://<host>/" prefix), placed at GIT_CONFIG index
// startIndex (so it appends after any host-set entries — see gitConfigCount). The token is base64'd
// into the header VALUE and never appears as cleartext, in argv, or on disk. Returned separately
// (rather than inlined) so a test can assert the exact shape and that the raw token is nowhere in it.
func extraHeaderEnv(startIndex int, scope, username, token string) (env []string, b64 string) {
	b64 = base64.StdEncoding.EncodeToString([]byte(username + ":" + token))
	idx := strconv.Itoa(startIndex)
	return []string{
		"GIT_CONFIG_COUNT=" + strconv.Itoa(startIndex+1),
		"GIT_CONFIG_KEY_" + idx + "=http." + scope + ".extraheader",
		"GIT_CONFIG_VALUE_" + idx + "=AUTHORIZATION: basic " + b64,
	}, b64
}

// RemoteURL returns the PUSH URL for remote via `git remote get-url --push` (remote.<name>.pushurl
// if configured, else the fetch URL). Every caller uses this to decide credential scoping and to
// resolve the PR-target owner/repo — all of which must key off where a push actually GOES, not the
// (possibly different) fetch URL.
func (c execClient) RemoteURL(repo, remote string) (string, error) {
	out, err := c.runTimed(repo, "remote", "get-url", "--push", remote)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Push runs `git push -u <remote> refs/heads/<branch>:refs/heads/<branch>` with a fixed argument
// vector — the token, when supplied, is NEVER in argv. A non-nil auth is injected only as a
// host-scoped extra HTTP Authorization header via env-only git config (GIT_CONFIG_*, git >= 2.31):
// nothing is written to .git/config, nothing lands in argv, and the header is scoped to exactly the
// scheme://host the remote points at, so it can never leak to another origin. GIT_TERMINAL_PROMPT=0
// keeps a credential-less push failing fast instead of blocking on a prompt no TTY can answer. Any
// error text is scrubbed of the token (and its base64 form) before it leaves the package.
func (c execClient) Push(ctx context.Context, repo, remote, branch string, auth *PushAuth) error {
	cctx, cancel := context.WithTimeout(ctx, gitTimeoutSec*time.Second)
	defer cancel()

	args := []string{"-C", repo, "push", "-u", remote, pushRefspec(branch)}
	cmd := exec.CommandContext(cctx, "git", args...)
	env := append(util.ScrubSecretEnv(os.Environ()),
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15",
	)

	var b64 string
	if auth != nil && auth.Token != "" {
		remoteURL, err := c.RemoteURL(repo, remote)
		if err != nil {
			// Defensive: a remote-URL lookup failure shouldn't carry the token (the token is never
			// passed to `git remote get-url`), but scrub it before surfacing regardless.
			return errors.New(redactSecrets(err.Error(), auth.token()))
		}
		// Attach the token ONLY to an https remote on the credential's own host. For any other
		// remote (an ssh remote, a different/foreign https host, a cleartext http URL) the token is
		// NOT attached — the push falls back to ambient credentials instead of leaking the token or
		// failing just because one was supplied. See credentialScope.
		if scope, ok := credentialScope(remoteURL, auth); ok {
			env, b64 = buildPushEnv(env, scope, auth.Username, auth.Token)
		}
	}
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := redactSecrets(strings.TrimSpace(string(out)), auth.token(), b64)
		return fmt.Errorf("%v: %s", err, msg)
	}
	return nil
}
