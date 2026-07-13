// scaffold_pr.go is the auto-push + PR-creation half of the "Add l00prite" action
// (repo_scaffold.go's HandleRepoScaffoldBranch). It has two transports, chosen per request by
// whether a *gitx.PushAuth was passed:
//   - auth == nil (no "Connect GitHub"): ambient host git/gh credentials ONLY — whatever the
//     gateway host's own git remote config, SSH agent, and `gh auth login` state provide, the same
//     trust model the model-facing git_command/run_command engine tools rely on. This is the
//     original, unchanged path; if the host can't already push and open a PR from a terminal, this
//     path can't either.
//   - auth != nil (a GitHub connection exists for the project): the vault-sealed token pushes over
//     HTTPS via gitx.Push and opens the PR via the REST API (ghapi.go), so this works on a bare
//     Android host with no git binary and no gh. gitx.Push host-scopes the token to github.com, so
//     it is never sent to a non-GitHub origin.
//
// Every check and action here is fail-closed: probePushPRCapability never mutates anything, so a
// gap is detected BEFORE any push is attempted, and a failure partway through (push succeeds, PR
// creation fails) is reported honestly rather than papered over or rolled back. This file never
// calls a merge API or a `gh ... --auto`/`--merge` flag — merging stays a human's decision, always.
package gateway

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// ghRepoFromRemote resolves the repo's origin URL and parses it to (owner, repo). ok=false when
// origin is missing or is not a github.com repository — the caller then treats the repo as not
// token-pushable and falls back to the ambient git/gh path (or reports the capability gap).
func ghRepoFromRemote(git gitx.Client, root string) (owner, repo string, ok bool) {
	url, err := git.RemoteURL(root, "origin")
	if err != nil {
		return "", "", false
	}
	return ownerRepoFromURL(url)
}

// tokenUsableForOrigin reports whether a connected token can actually serve as the push transport
// for this repo's origin — only an https github.com remote (git.Push injects the token over HTTPS
// and ghCreatePR opens the PR via REST). For an ssh github remote or ANY non-github remote it
// returns false, so the caller nils the auth and every scaffold path (probe/push/PR-lookup) falls
// back uniformly to the ambient git/gh path instead of one path taking the token route and another
// hard-failing. Mirrors exactly what gitx.Push itself would do for the same remote.
func tokenUsableForOrigin(git gitx.Client, root string, auth *gitx.PushAuth) bool {
	url, err := git.RemoteURL(root, "origin")
	if err != nil {
		return false
	}
	return gitx.TokenUsableFor(url, auth)
}

// ghExecTimeout bounds every gh/git-push exec in this file. gh and git can both try to prompt
// interactively for credentials (a device-flow login nudge, a credential-helper popup); with the
// prompt-disabling env below that should never happen, but a hard timeout is the backstop so a
// misbehaving or hung host process can never block the request goroutine indefinitely — the same
// concern gitx.execClient's own gitTimeoutSec addresses for the git operations that seam covers.
const ghExecTimeout = 20 * time.Second

// Fixed, hand-authored capability-gap sentences — the same discipline humanizeStartError
// (gateway/runs.go) and preflight.go's Blocker strings already use for exactly this reason: never
// show a client the raw git/gh error text, which can embed host file paths, remote URLs with
// credentials in them, or other operational detail nobody outside the gateway host needs to see.
// The raw error always goes to app.auditAs instead (see HandleRepoScaffoldBranch).
const (
	gapNoGitBinary = "This device has no git binary and no GitHub connection, so l00prite can't push to a real remote. Connect GitHub in the dashboard to push from this device, or push this branch and open the pull request yourself using the command below."
	gapNoGH        = "The GitHub CLI (gh) isn't installed on the gateway host, so l00prite can't open a pull request automatically. Push this branch and open the pull request yourself using the command below."
	gapGHNotAuthed = "gh isn't signed in on the gateway host — run `gh auth login` there, then try again. Push this branch and open the pull request yourself using the command below."
	gapPushDryRun  = "Pushing to origin isn't possible from this host — check the remote URL and credentials, then try again. Push this branch and open the pull request yourself using the command below."
	gapPushFailed  = "Pushing this branch to origin failed even though a dry run of the same push just succeeded (a race, or a rejected hook) — nothing else was attempted. Push this branch and open the pull request yourself using the command below."
	// gapTokenPushFailed is the token-path counterpart to gapPushFailed: the connected-GitHub push
	// path runs no dry run, so it must NOT claim one succeeded. The most common real cause is a
	// fine-grained token that authenticated (GET /user passed) but lacks Contents:write on THIS repo.
	gapTokenPushFailed = "Pushing this branch to origin with the connected GitHub token failed. Check that the token has Contents: Read and write permission on this repository — a fine-grained token is scoped to specific repositories. Push this branch and open the pull request yourself using the command below."
	gapPRCreate        = "The branch was pushed to origin, but opening the pull request failed. Open it yourself with the command below."
	// gapPRURLUnknown is distinct from gapPRCreate: `gh pr create` here exited 0 -- the pull
	// request really was opened -- but parsePRURL found no URL-looking line in its output (a
	// future gh version printing extra text, or a differently-shaped success message). Saying
	// "opening the pull request failed" here would be factually wrong (it succeeded), the same
	// class of raw-vs-honest-state mismatch this file's gapPushFailed/pushed-but-PR-create-failed
	// split already exists to avoid — see HandleRepoScaffoldBranch's switch.
	gapPRURLUnknown = "The branch was pushed and the pull request was opened, but l00prite couldn't read its URL from gh's output. Find it with the command below."
)

// scaffoldPRTitle is fixed, never derived from request input — see this file's package comment
// and the risk note in HandleRepoScaffoldBranch about never interpolating caller-controlled
// strings into an exec.Command argument vector.
const scaffoldPRTitle = "Add l00prite protocol (AGENTS.md, loop prompts, vendor adapters)"

// scaffoldPRBody is the fixed PR description. branch is the internally generated
// "l00prite/add-protocol-<random>" name (util.RID), never user input, so interpolating it here is
// safe — it still goes through gh as a single --body argument, never a shell.
func scaffoldPRBody(branch string) string {
	return "This pull request adds the l00prite protocol to this repository: AGENTS.md, the " +
		".l00prite memory folder, the six canonical loop prompts, and vendor adapters for other " +
		"AI coding agents.\n\n" +
		"Opened automatically by l00prite OS after explicit in-dashboard consent to \"create a " +
		"branch, push it, and open a pull request.\" l00prite never merges pull requests it opens " +
		"— a human should review and merge (or close) this one.\n\n" +
		"Branch: " + branch
}

// ghPRCreateCommand is the copy-paste fallback shown when a push succeeded but `gh pr create`
// itself failed (see gapPRCreate) — deliberately omits --body (a multi-line fixed string is not a
// pleasant thing to paste into a terminal; gh will prompt for a body interactively instead) and
// deliberately never includes an --auto/--merge flag, matching this file's never-auto-merge rule.
func ghPRCreateCommand(branch string) string {
	return `gh pr create --title "` + scaffoldPRTitle + `" --head ` + branch
}

// ghPRViewCommand is the copy-paste fallback for gapPRURLUnknown: unlike ghPRCreateCommand, this
// case already has a real, successfully-created pull request -- re-running `gh pr create` would
// just fail with "a pull request for branch ... already exists" instead of helping. `gh pr view`
// looks up the existing one by branch instead of creating a second.
func ghPRViewCommand(branch string) string {
	return `gh pr view ` + branch + ` --json url -q .url`
}

// probePushPRCapability checks, in fail-closed order, whether this gateway host can actually push
// branch and open a pull request for it — WITHOUT pushing or creating anything. Every check here
// is read-only: `gh auth status` reads local credential state, and `git push --dry-run` stages
// nothing. ok=false always comes paired with a fixed gapMsg safe to show a client; rawErr (nil for
// the two purely structural checks, which have no underlying process error) is for app.auditAs
// only — the caller must never send it to the client.
func probePushPRCapability(ctx context.Context, git gitx.Client, root, branch string, auth *gitx.PushAuth) (ok bool, gapMsg string, rawErr error) {
	// Token path: a stored GitHub connection ("Connect GitHub") pushes and opens the PR over HTTPS
	// on BOTH backends — including a bare Android host with no git binary and no gh — so the
	// no-git-binary gap below does not apply when connected to a github.com origin. gitx.Push and
	// ghCreatePR carry the credential; nothing here needs gh or an ambient credential helper.
	if auth != nil {
		if _, _, isGH := ghRepoFromRemote(git, root); isGH {
			return true, "", nil
		}
		// Connected, but origin isn't a github.com repo (e.g. an ssh remote or a non-GitHub host):
		// fall through to the ambient git/gh path, which handles those exactly as before.
	}
	// The gogit backend (Android, or any host with no git binary) has no push/ssh transport at
	// all — Client.Raw unconditionally returns ErrRawUnsupported there (see gitx.Client's doc
	// comment on Raw), so with no GitHub connection there is nothing further to probe.
	if git.Kind() != "exec" {
		return false, gapNoGitBinary, nil
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return false, gapNoGH, nil
	}

	authCtx, authCancel := context.WithTimeout(ctx, ghExecTimeout)
	defer authCancel()
	if _, err := runGH(authCtx, root, "auth", "status"); err != nil {
		return false, gapGHNotAuthed, err
	}

	dryCtx, dryCancel := context.WithTimeout(ctx, ghExecTimeout)
	defer dryCancel()
	if _, err := git.Raw(dryCtx, root, "push", "--dry-run", "-u", "origin", branch); err != nil {
		return false, gapPushDryRun, err
	}
	return true, "", nil
}

// openScaffoldPR pushes branch to origin, then runs `gh pr create` for it. Callers must only
// reach this after probePushPRCapability has already returned ok=true — even so, the real push can
// still fail here (a race with another push, a server-side hook rejection the dry run can't
// predict), which is why pushed is reported independently of prURL/err: a caller that sees
// pushed=true with a non-nil err knows the consented push already landed and must NEVER attempt to
// undo it, only report that PR creation itself needs to be done by hand.
func openScaffoldPR(ctx context.Context, git gitx.Client, root, branch, title, body string, auth *gitx.PushAuth) (pushed bool, prURL string, rawErr error) {
	// Token path: push via gitx (works on both backends), then open the PR via the REST API — no gh
	// binary needed. Like the ambient path below, `pushed` is reported independently of prURL/err so
	// a caller that sees pushed=true with a non-nil err knows the consented push already landed and
	// must never undo it. Never merges (ghCreatePR has no merge; there is no merge endpoint here).
	if auth != nil {
		owner, repo, isGH := ghRepoFromRemote(git, root)
		if !isGH {
			return false, "", errors.New("origin is not a github.com repository")
		}
		pushCtx, pushCancel := context.WithTimeout(ctx, ghExecTimeout)
		defer pushCancel()
		if err := git.Push(pushCtx, root, "origin", branch, auth); err != nil {
			return false, "", err
		}
		base, berr := ghDefaultBranch(ctx, auth.Token, owner, repo)
		if berr != nil {
			return true, "", berr // pushed, but couldn't determine the base branch to open against
		}
		url, cerr := ghCreatePR(ctx, auth.Token, owner, repo, branch, base, title, body)
		if cerr != nil {
			return true, "", cerr
		}
		return true, url, nil
	}

	pushCtx, pushCancel := context.WithTimeout(ctx, ghExecTimeout)
	defer pushCancel()
	if _, err := git.Raw(pushCtx, root, "push", "-u", "origin", branch); err != nil {
		return false, "", err
	}

	prCtx, prCancel := context.WithTimeout(ctx, ghExecTimeout)
	defer prCancel()
	// Arg vector, never a shell: title/body are fixed constants and branch is internally
	// generated (util.RID), but this is the discipline regardless — see the package comment.
	out, err := runGH(prCtx, root, "pr", "create", "--title", title, "--body", body, "--head", branch)
	if err != nil {
		return true, "", err
	}
	return true, parsePRURL(out), nil
}

// pushLedgerUpdate re-pushes branch after the post-PR ledger-URL commit lands. Best-effort by
// design: by the time this runs, the actually important human-facing artifact (the open PR) is
// already real on GitHub — a failure here just means the remote branch is one local commit behind
// until the next push, not a half-done or misleading state, so the caller logs this error rather
// than failing the whole request over it.
func pushLedgerUpdate(ctx context.Context, git gitx.Client, root, branch string, auth *gitx.PushAuth) error {
	pushCtx, cancel := context.WithTimeout(ctx, ghExecTimeout)
	defer cancel()
	if auth != nil {
		return git.Push(pushCtx, root, "origin", branch, auth)
	}
	_, err := git.Raw(pushCtx, root, "push", "-u", "origin", branch)
	return err
}

// lookupExistingPR asks gh whether a NON-CLOSED pull request already exists for branch and returns
// its URL, or "" if none is found OR anything goes wrong. It is read-only (`gh pr view`, never a
// create/merge) and is called ONLY on the pure-retry path, after the capability probe has already
// passed — never on a fresh attempt. Its whole purpose is to prevent a double-PR when an operator
// opened the PR by hand (via the fallback command) between attempts: finding the existing PR lets
// the handler report success with the real URL instead of pushing again and running `gh pr create`
// (which would just fail with "a pull request for branch ... already exists").
//
// It fails OPEN to "" on any error: a genuinely missing PR and a transient gh failure both mean the
// same safe thing — "proceed to the normal push+create, which then reports its own honest gap".
// CLOSED PRs are deliberately excluded (the --jq filter drops them) so a closed-without-merge PR
// gets a genuinely new one rather than being resurfaced as if still open. The raw gh output never
// reaches a client: only the parsed URL (or "") is returned, and every caller sends the URL, not
// gh's stderr.
func lookupExistingPR(ctx context.Context, git gitx.Client, root, branch string, auth *gitx.PushAuth) string {
	// Token path: the REST twin (ghFindOpenPR) with the same fail-open-to-"" contract, so this works
	// on a host with no gh binary.
	if auth != nil {
		if owner, repo, isGH := ghRepoFromRemote(git, root); isGH {
			return ghFindOpenPR(ctx, auth.Token, owner, repo, branch)
		}
		return ""
	}
	viewCtx, cancel := context.WithTimeout(ctx, ghExecTimeout)
	defer cancel()
	// Arg vector, never a shell (branch is internally generated util.RID, but this is the discipline
	// regardless — see the package comment). No --merge/--auto: this is a pure read.
	out, err := runGH(viewCtx, root, "pr", "view", branch, "--json", "url,state", "--jq", `select(.state != "CLOSED") | .url`)
	if err != nil {
		return "" // fail open: no PR, or a transient failure — both mean "attempt the normal push+create".
	}
	return parsePRURL(out)
}

// runGH execs `gh <args...>` with cwd=dir, credential prompts disabled, and the process-wide
// secrets scrubbed from its environment — the same discipline gitx.execClient's own run() already
// applies to every git child process this codebase execs on a model's or user's behalf (see
// internal/util/env.go's ScrubSecretEnv doc comment). GH_PROMPT_DISABLED is gh's own real
// interactive-prompt-disable env var; GIT_TERMINAL_PROMPT=0 covers git operations gh shells out to
// internally (e.g. during `gh auth status`'s credential-helper check).
func runGH(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(util.ScrubSecretEnv(os.Environ()),
		"GIT_TERMINAL_PROMPT=0",
		"GH_PROMPT_DISABLED=1",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// parsePRURL extracts the PR URL `gh pr create` prints on success — normally the only line on
// stdout. Scanning from the LAST non-empty line backward (rather than assuming there is exactly
// one line) tolerates a future gh version printing extra informational lines first without
// breaking parsing; returns "" if nothing that looks like a URL is found, which the caller treats
// the same as an empty/unknown PR URL rather than a hard failure (the PR still really was created).
func parsePRURL(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}
