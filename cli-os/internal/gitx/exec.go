package gitx

// execClient shells out to the system "git" binary. It is byte-for-byte today's (pre-gitx)
// behavior — the ONLY implementation that existed before this package did — ported here unchanged
// as one seam so a desktop host with git installed sees no difference at all. It stays strictly
// more capable than gogitClient (arbitrary subcommands, ssh transport, the host's own gitconfig
// and credential helpers), which is exactly why Detect prefers it whenever a git binary exists.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
func (c execClient) run(ctx context.Context, repo string, args ...string) (string, error) {
	full := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = util.ScrubSecretEnv(os.Environ())
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
// same env — so cloning behavior never drifts by so much as a byte.
func (c execClient) Clone(ctx context.Context, url, dest string, depth int) error {
	args := []string{"clone", "--depth", fmt.Sprint(depth), "--", url, dest}
	cmd := exec.CommandContext(ctx, "git", args...)
	// Disable git's own https credential prompt and force ssh into non-interactive BatchMode with
	// a short connect timeout: an unreachable or credential-requiring URL fails fast instead of
	// hanging the request on a TTY nothing here can ever answer.
	cmd.Env = append(util.ScrubSecretEnv(os.Environ()),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
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

func (c execClient) StatusPorcelain(repo string) (string, error) {
	return c.runTimed(repo, "status", "--porcelain")
}

func (c execClient) CheckoutNewBranch(repo, name string) error {
	_, err := c.runTimed(repo, "checkout", "-B", name)
	return err
}

func (c execClient) AddAll(repo string) error {
	_, err := c.runTimed(repo, "add", "-A")
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

func (c execClient) Raw(ctx context.Context, repo string, args ...string) (string, error) {
	out, err := c.run(ctx, repo, args...)
	if err != nil {
		return out, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return out, nil
}
