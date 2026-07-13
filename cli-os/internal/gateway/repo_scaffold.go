// The maintainer-requested "add l00prite to this repo" action. Registering or cloning a repo
// already auto-scaffolds a MINIMAL .l00prite/ memory subset silently (see repos.go's
// repoRegisteredResponse) — no prompts, no AGENTS.md, no CLAUDE.md protocol section, no vendor
// adapters, and nothing branched or committed. Without those, a registered repo does not get
// "the full benefit of the l00prite methodology": no .l00prite/prompts/ means no execute-loop,
// and no AGENTS.md/vendor adapters means no cross-vendor discovery.
//
// This is the opposite of that: an explicit, consent-gated action (a dashboard checkbox or
// button — NEVER triggered automatically) that creates a LOCAL branch, writes the full protocol
// (engine.Files.ScaffoldFull), and commits it.
//
// Pushing and opening a pull request are NOT automatic either, but they ARE now possible: when the
// request carries open_pr=true, that is itself the explicit per-action permission
// AGENTS.md.template's hard rule requires — the same shape as a human clicking "Allow" on a
// pending git-push approval in the run engine (internal/engine/preflight.go's
// perActionPermissions), just granted from this handler's dashboard control (labeled "Create a
// branch, push it, and open a pull request") instead of the run engine's approvals inbox. See
// scaffold_pr.go for the fail-closed capability probe and push/PR mechanics. Nothing in this
// package ever merges a pull request — that stays a human decision, always. Absent that
// permission (open_pr omitted/false, e.g. any pre-existing headless API caller), the response
// still carries the copy-paste push instructions this handler always has.
package gateway

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// scaffoldBranchSession is this action's lock-session literal (LOCKING.md convention — see
// repos.go's registerScaffoldSession for the sibling auto-scaffold's identical pattern). A
// distinct literal so lock/audit history can tell the two actions apart.
const scaffoldBranchSession = "gateway-scaffold-full-branch"

type repoScaffoldBranchReq struct {
	ID string `json:"id"`
	// OpenPR requests that l00prite push the new branch and open a pull request for it, using
	// only ambient host git/gh credentials (the same trust model the model-facing
	// git_command/run_command engine tools already rely on) — never a stored token, and never a
	// merge. nil/false means today's local-only behavior: a branch and a commit on the gateway
	// host, nothing touches the remote.
	//
	// This defaults to false at the API level even though the dashboard's own checkbox defaults
	// to checked: a live click on a control explicitly labeled "Create a branch, push it, and
	// open a pull request" IS this action's per-action permission (the same shape as clicking
	// Allow on a pending git-push approval in the run engine — see AGENTS.md's per-action
	// permission invariant and this handler's package comment). A headless API caller that never
	// rendered that label has given no such permission, so pre-existing automation hitting this
	// endpoint must not silently start pushing just because this field shipped.
	OpenPR *bool `json:"open_pr"`
}

// HandleRepoScaffoldBranch is POST /v1/repos/scaffold-branch. Reused by both dashboard entry
// points (the Register-repo modal's consent checkbox, fired right after registration completes,
// and a standalone "Add l00prite" action on an already-registered repo) — same repo id, same
// endpoint, same behavior either way.
func (app *App) HandleRepoScaffoldBranch(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body repoScaffoldBranchReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body", "invalid_request_error", "")
		return
	}
	// Trim once, up front, and use the trimmed id consistently for lookup + audit — matching
	// repos.go's HandleRepoRegister, rather than relying on the reader to know
	// repoRootForToken trims internally too.
	id := strings.TrimSpace(body.ID)
	root, ok := app.repoRootForToken(w, principal, id)
	if !ok {
		return
	}

	files := engine.Files{Root: root}
	// Check what's missing BEFORE touching git: a repo that already has the full protocol (e.g.
	// this action was already run, or /build-loop already scaffolded it by hand) gets a plain
	// "nothing to do" response instead of a pointless empty branch.
	missing, claudeMissing := files.FullProtocolGaps()
	complete := len(missing) == 0 && !claudeMissing

	// open_pr is hoisted here (above the already_complete short-circuit) because the pending-branch
	// short-circuit below branches on it: a pending attempt WITHOUT this request's push permission
	// must return a read-only note, never a push.
	openPR := body.OpenPR != nil && *body.OpenPR

	// gitx.Detect() is a cheap resolution (no exec), hoisted above the already_complete short-circuit
	// so the pending-attempt / retry logic can read the current branch before we decide to no-op.
	git := gitx.Detect()

	// A stored GitHub connection (dashboard "Connect GitHub") lets the push + PR-create below work
	// on ANY backend — including a bare Android host with no git binary or gh — using a token
	// instead of ambient credentials. Nil when nothing is connected (a decrypt failure is treated as
	// nil too): every path below then behaves exactly as it did before this feature existed. The
	// token is resolved once per request and never logged.
	scaffoldAuth, _ := app.GitHubAuthFor(principal.Project)
	// The token transport only works for an https github.com origin (git.Push over HTTPS + a REST
	// PR). For an ssh github remote or ANY non-github remote, drop it to nil so every scaffold path
	// (probe/push/PR-lookup) falls back UNIFORMLY to the ambient git/gh path — otherwise the probe
	// would fall through to ambient while openScaffoldPR hard-failed on the token path, breaking
	// "Add l00prite" for a non-GitHub repo the moment a token is connected to its project.
	if scaffoldAuth != nil && !tokenUsableForOrigin(git, root, scaffoldAuth) {
		scaffoldAuth = nil
	}

	// Retry tracking (repo_scaffold_state.go): a prior "Add l00prite" attempt can create+commit a
	// branch locally and THEN hit a push/PR capability gap. The protocol files it committed make
	// FullProtocolGaps() report the repo "complete", so BEFORE this feature a retry click no-op'd on
	// the already_complete short-circuit below. The pending lookup MUST therefore happen before that
	// short-circuit — it is exactly what defeats the retry today.
	pendingBranch, pendingPushed, hasPending := app.pendingScaffoldAttempt(id)

	// isRetry is true ONLY when a pending row exists AND the working copy is currently checked out on
	// that exact branch. The CurrentBranch match is the load-bearing safety gate: it is the ONLY
	// state in which reusing the tracked name through engine.EnsureRunBranch is safe, because
	// CheckoutNewBranch has `git checkout -B` create-or-RESET-to-HEAD semantics — a self-reset no-op
	// when HEAD already IS that branch's tip, but a silent branch-move (orphaning attempt 1's commit)
	// from any other checkout. CurrentBranch returns "" on detached/unborn HEAD, which can never
	// equal a tracked name, so those degrade safely to non-retry.
	isRetry := false
	if hasPending {
		cur, _ := git.CurrentBranch(root) // best-effort, same discipline as the originalBranch read below.
		isRetry = cur != "" && cur == pendingBranch
	}

	if complete {
		switch {
		case hasPending && !isRetry:
			// Out-of-band success: the tracked branch was abandoned but the goal was achieved anyway
			// (operator pushed/merged it by hand, or scaffolded the default branch directly). Stop
			// tracking it, and NAME it in the note so it is never silently lost.
			app.clearScaffoldAttempt(id)
			app.auditAs(principal, "repo.scaffold_branch.pending_cleared",
				`completed out of band; stopped tracking branch "`+pendingBranch+`" for `+id)
			sendJSON(w, 200, map[string]any{
				"already_complete": true, "branch": nil,
				"note": `This repo already has the full l00prite protocol — nothing to add. (A previous attempt's branch "` + pendingBranch + `" is no longer being tracked; delete it if it still exists: git branch -D ` + pendingBranch + `.)`,
			})
			return
		case isRetry && !openPR:
			// A pending attempt exists and we're on its branch, but THIS request carried no push/PR
			// permission. Persisted state is NEVER standing consent (the execute-loop rule that
			// persisted flags never satisfy a gate): return a read-only note, perform zero git writes
			// and zero network calls, and KEEP the row so a later consented click can still retry.
			pushedNote := ""
			if pendingPushed {
				pushedNote = " It was already pushed to origin, but no pull request has been opened yet."
			}
			sendJSON(w, 200, map[string]any{
				"already_complete": true, "branch": pendingBranch,
				"note": `A previous "Add l00prite" attempt already created and committed branch "` + pendingBranch + `" locally.` + pushedNote + ` Push it with: git push -u origin ` + pendingBranch + ` — or re-run with the push-and-pull-request option to have l00prite retry the push and PR for you.`,
			})
			return
		case !hasPending:
			// The original already_complete case — byte-identical to before this feature existed.
			sendJSON(w, 200, map[string]any{
				"already_complete": true, "branch": nil,
				"note": "This repo already has the full l00prite protocol (AGENTS.md, the loop prompts, and the vendor adapters) — nothing to add.",
			})
			return
		}
		// The only remaining complete case, complete && isRetry && openPR, falls through: it is the
		// pure push/PR retry — the branch already exists and this request's open_pr=true is fresh
		// per-action permission to push it.
	}

	if _, err := git.RevParseHead(root); err != nil {
		oaiError(w, 400, "This repository has no commits yet — make an initial commit before scaffolding a branch.", "invalid_request_error", "no_commits")
		return
	}
	// Best-effort only: purely informational (what the new branch is "based on" in the response),
	// never used to restore/checkout back to it — CheckoutNewBranch's create-or-reset (`checkout
	// -B`) semantics mean re-checking out an existing branch name the same way would silently
	// fast-forward it to the new commit, moving a branch the user never asked to move. Leaving the
	// repo checked out on the new branch (like engine.EnsureRunBranch's own run-branch callers do)
	// is the safe, already-precedented choice here.
	originalBranch, _ := git.CurrentBranch(root)

	// Peek (read-only) before creating a branch at all: a genuinely foreign, unexpired lock should
	// 409 without ever touching git — same "reduce, not eliminate" race this codebase's other lock
	// checks accept (repos.go's registerScaffoldSession comment). The real acquire (a write) happens
	// AFTER the checkout below, never before it — see that comment for why.
	curLock, lerr := files.ReadLock()
	if lerr != nil {
		oaiError(w, 500, "Could not read this repo's lock state: "+lerr.Error(), "configuration_error", "")
		return
	}
	if engine.LockAvailability(curLock, scaffoldBranchSession) == "foreign" {
		oaiError(w, 409, "Could not scaffold: another agent currently holds this repo's lock. Try again once its run finishes.", "invalid_request_error", "lock_held")
		return
	}

	// Branch checkout happens BEFORE the lock acquire (a write to .l00prite/lock.json), not
	// after — reproduced via a real gogit repo: go-git's CheckoutNewBranch (unlike real git's
	// `checkout -B`) errors on ANY dirty TRACKED file, even one that's already committed on this
	// exact commit and would be byte-identical either way. Once lock.json has been committed once
	// (every repo this action has already run on), acquiring the lock first would dirty a tracked
	// file and break every subsequent call on the gogit/Android backend — the same class of
	// ordering bug this codebase's PR #6/#7 rounds already fixed for the ledger append and the run
	// engine's lease write (see engine.go's StartRun: checkout, THEN acquire, THEN scaffold).
	// Branch selection: reuse the tracked pending branch on a retry (isRetry guarantees we're
	// already checked out on it, so EnsureRunBranch's checkout -B is a self-reset no-op — see the
	// isRetry doc comment). Otherwise generate a fresh random name, exactly as before. A superseding
	// fresh attempt (a pending row exists but we're NOT on its branch) deliberately leaves the old
	// branch untouched and generates a new name here.
	branch := pendingBranch
	if !isRetry {
		branch = "l00prite/add-protocol-" + strings.TrimPrefix(util.RID("x"), "x_")
	}
	if err := engine.EnsureRunBranch(git, root, branch); err != nil {
		oaiError(w, 400, "Could not create a branch: "+err.Error()+". Commit or stash any other uncommitted changes first — files already in .l00prite/ don't block this.", "invalid_request_error", "branch_failed")
		return
	}

	// pureRetry: a retry with nothing new to scaffold (the protocol is already complete on the
	// tracked branch). The entire lock-acquire/ScaffoldFull/lock-release/AddPaths/CommitUnit block is
	// skipped — there is nothing to write, and acquiring the lock just to release it would add two
	// lock.json writes needing their own commit, recreating the dirty-tree bug class this handler
	// already fixed. A drift retry (isRetry but the protocol went incomplete on the branch, e.g. a
	// committed deletion of .l00prite/prompts) runs the block unchanged to re-scaffold + commit.
	pureRetry := isRetry && complete
	var created []string
	var claudeSkipped bool
	var hash string
	if !pureRetry {
		if _, _, aerr := files.AcquireLock(scaffoldBranchSession, "full protocol scaffold branch", 120); aerr != nil {
			oaiError(w, 409, "Created branch \""+branch+"\" but could not acquire the lock: "+aerr.Error()+". Another agent must have started a run between the check above and now — try again.", "invalid_request_error", "lock_held")
			return
		}
		// No `defer` release: releasing writes lock.json, and a write that lands AFTER CommitUnit
		// leaves the new branch's working tree dirty the moment this handler returns (the same class
		// of bug referenced above, just for the release write instead of the branch-vs-acquire order).
		// Every path below releases explicitly BEFORE the commit, so the released lock.json state is
		// part of that single commit, not stranded after it.
		var serr error
		created, claudeSkipped, serr = files.ScaffoldFull(filepath.Base(root), "")
		if serr != nil {
			_ = files.ReleaseLock(scaffoldBranchSession)
			oaiError(w, 500, "Created branch \""+branch+"\" but failed to write the protocol files: "+serr.Error(), "configuration_error", "")
			return
		}

		if rerr := files.ReleaseLock(scaffoldBranchSession); rerr != nil {
			oaiError(w, 500, "Wrote the protocol files on branch \""+branch+"\" but failed to release the lock: "+rerr.Error(), "configuration_error", "")
			return
		}

		// Force-add EVERY canonical protocol path (not just `created`) BEFORE the general AddAll
		// below: CommitUnit stages via `git add -A`, which respects the target repo's own
		// .gitignore, so a repo that ignores `.l00prite/` (or any scaffolded path) would otherwise
		// silently drop those files from the commit while this response still reports them as
		// created. Using the full canonical set — not just what THIS call wrote — matters because a
		// file can already exist uncommitted from an earlier partial scaffold (the baseline
		// auto-scaffold on register runs before ScaffoldFull ever does) and be just as gitignored;
		// force-adding an already-clean or already-staged path is a harmless no-op either way.
		// AddAll (inside CommitUnit) still picks up anything else dirty, e.g. the lock-release write
		// above.
		if aerr := git.AddPaths(root, files.FullProtocolPaths()); aerr != nil {
			oaiError(w, 500, "Wrote the protocol files on branch \""+branch+"\" but failed to stage them: "+aerr.Error(), "configuration_error", "")
			return
		}

		if len(created) > 0 {
			var cerr error
			hash, cerr = engine.CommitUnit(git, root, "Add l00prite protocol (AGENTS.md, loop prompts, vendor adapters)")
			if cerr != nil {
				oaiError(w, 500, "Wrote the protocol files on branch \""+branch+"\" but failed to commit them: "+cerr.Error(), "configuration_error", "")
				return
			}
		}
	}

	// Track this pending attempt AFTER the commit but BEFORE any push/PR, so a crash mid-PR still
	// leaves a row a later click can retry. ON CONFLICT supersedes any prior row in place — including
	// a superseding fresh attempt that abandoned a different tracked branch (recorded below).
	superseded := hasPending && !isRetry && hash != ""
	if hash != "" {
		app.recordScaffoldAttempt(id, branch, hash)
		if superseded {
			app.auditAs(principal, "repo.scaffold_branch.pending_cleared", "superseded "+pendingBranch+" -> "+branch+" for "+id)
		}
	}
	app.auditAs(principal, "repo.scaffold_branch", id)
	if isRetry {
		app.auditAs(principal, "repo.scaffold_branch.retry", id+" branch="+branch)
	}

	// open_pr=true is this action's per-action permission for pushing and opening a PR (see this
	// file's package comment). Attempted when a real commit was made this call (len(created)>0, the
	// fresh/drift path) OR this is a retry: a tracked pending attempt is the deliberate exception to
	// the empty-diff-is-a-no-op rule, because the thing to push already exists as a real committed
	// branch and THIS request's open_pr=true is fresh per-action permission to push it. A fresh call
	// with an empty diff still no-ops, matching engine.EnsureRunBranch/CommitUnit callers' convention.
	attemptedPR := openPR && (len(created) > 0 || isRetry)
	var pushed bool
	var prURL, capabilityGap, prCommand string
	var existingPRFound bool
	if attemptedPR {
		ctx := r.Context()
		ok, gapMsg, rawErr := probePushPRCapability(ctx, git, root, branch, scaffoldAuth)
		// Pure-retry only (nothing newly committed): the operator may have opened the PR by hand via
		// the fallback command between attempts, so look it up ONCE here, after the probe passed and
		// before any push. Exactly one call, bound to a variable: lookupExistingPR fails OPEN to ""
		// on any error, so calling it twice (once to test, once to bind) could clear the pending row
		// and claim "found" while binding an empty URL if the second call flaked — the URL that
		// decides the branch below must be the same one the response reports.
		existingPRURL := ""
		if ok && isRetry && len(created) == 0 {
			existingPRURL = lookupExistingPR(ctx, git, root, branch, scaffoldAuth)
		}
		switch {
		case !ok:
			capabilityGap = gapMsg
			auditDetail := gapMsg
			if rawErr != nil {
				auditDetail = rawErr.Error() // raw text: audit log only, never the client — see probePushPRCapability's doc comment.
			}
			app.auditAs(principal, "repo.scaffold_branch.pr_gap", auditDetail)
		case existingPRURL != "":
			// Report success with the REAL URL instead of pushing again and running `gh pr create`
			// (which would fail "already exists"). No push means pr_url and capability_gap stay
			// mutually exclusive, preserving the dashboard's documented invariant. The gh lookup is
			// read-only and CLOSED PRs are excluded, so a closed-without-merge PR still gets a
			// genuinely new one.
			prURL = existingPRURL
			existingPRFound = true
			app.clearScaffoldAttempt(id)
			app.auditAs(principal, "repo.scaffold_branch.pr", prURL)
		default:
			var perr error
			pushed, prURL, perr = openScaffoldPR(ctx, git, root, branch, scaffoldPRTitle, scaffoldPRBody(branch), scaffoldAuth)
			switch {
			case perr != nil && pushed:
				// The consented push already landed on origin — never rolled back, never retried
				// blindly. Only PR creation itself needs a human to finish by hand; keep the pending
				// row and mark it pushed so a later retry's note is honest ("already pushed").
				capabilityGap = gapPRCreate
				prCommand = ghPRCreateCommand(branch)
				app.markScaffoldAttemptPushed(id)
				app.auditAs(principal, "repo.scaffold_branch.pr_gap", perr.Error())
			case perr != nil:
				// The token path runs no `git push --dry-run` pre-check (probePushPRCapability
				// returns ok=true immediately for a connected github.com origin), so gapPushFailed's
				// "a dry run of the same push just succeeded" would be a false claim there. Use an
				// honest token-scoped message when the connection was in play.
				if scaffoldAuth != nil {
					capabilityGap = gapTokenPushFailed
				} else {
					capabilityGap = gapPushFailed
				}
				app.auditAs(principal, "repo.scaffold_branch.pr_gap", perr.Error())
				// No row change: the push never landed, so the pending attempt stays retryable as-is.
			case prURL == "":
				// `gh pr create` exited 0 -- the PR really was opened -- but parsePRURL found no
				// URL-looking line in its output (PR review finding: a future gh version could
				// change its success-output shape). Must NOT fall into the default branch below:
				// that would write a ledger entry claiming "opened pull request " with an empty
				// URL, and the notes switch below would say "opening the pull request failed" —
				// factually wrong on both counts, since it actually succeeded. `gh pr create`
				// itself is never offered as the fallback command here (unlike gapPRCreate above)
				// because the PR already exists — re-running it would just fail with "a pull
				// request for branch ... already exists"; `gh pr view` looks the real one up
				// instead.
				capabilityGap = gapPRURLUnknown
				prCommand = ghPRViewCommand(branch)
				// The PR really was opened (only its URL is unknown), so this attempt is done —
				// clear the pending row so a later click reports plain already_complete, not a retry.
				app.clearScaffoldAttempt(id)
				app.auditAs(principal, "repo.scaffold_branch.pr_gap", "gh pr create succeeded (exit 0) but printed no parseable PR URL")
			default:
				app.auditAs(principal, "repo.scaffold_branch.pr", prURL)
				// The PR is confirmed opened: clear the pending row BEFORE the best-effort ledger
				// block below, so clearing never depends on ledger success (a ledger failure must not
				// leave a stale row that offers a pointless retry of an already-open PR).
				app.clearScaffoldAttempt(id)
				// Record the PR URL in the repo's own ledger as its OWN commit, after the PR
				// already exists — writing it before the PR exists would have no URL to record,
				// and leaving it uncommitted after this handler returns recreates the exact
				// dirty-tree-on-return bug class this repo has already fixed three times (PR #6's
				// ledger append, PR #7's lease write, this handler's own lock-release-before-
				// commit ordering above). Best-effort: a failure here never unwinds the real PR
				// that already exists on GitHub.
				// Each step below only runs if the previous one actually succeeded (PR review,
				// gemini-code-assist): AppendLedger's error was previously discarded, so a failed
				// append (e.g. ledger.md unwritable) still fell through to AddPaths/CommitUnit on a
				// file that was never actually updated -- staging/committing an unchanged file is at
				// best a wasted no-op commit attempt and at worst papers over the real failure behind
				// a misleading "commit failed" audit line instead of the true "append failed" one.
				if lerr := files.AppendLedger(engine.LedgerEntry{
					Timestamp:       util.NowISO(),
					RunID:           "n/a (repo scaffold, not an engine run)",
					Goal:            "add the l00prite protocol to this repo",
					TriggeringEvent: `dashboard consent: "create a branch, push it, and open a pull request"`,
					Decision:        "opened pull request " + prURL,
					CompletedWork:   "pushed branch \"" + branch + "\" to origin and opened a pull request for a human to review and merge",
					ChangedFiles:    "(see branch " + branch + ")",
					EventStatus:     "not applicable",
					Confidence:      "recorded by l00prite OS after a real `gh pr create`",
					NextAction:      "human review and merge (or close) " + prURL,
					LockNote:        "not applicable — the scaffold lock was already released before the protocol commit above",
				}); lerr != nil {
					app.auditAs(principal, "repo.scaffold_branch.pr_gap", lerr.Error())
				} else if aerr := git.AddPaths(root, []string{".l00prite/ledger.md"}); aerr != nil {
					app.auditAs(principal, "repo.scaffold_branch.pr_gap", aerr.Error())
				} else if _, cerr := engine.CommitUnit(git, root, "Record PR URL in l00prite ledger"); cerr != nil {
					app.auditAs(principal, "repo.scaffold_branch.pr_gap", cerr.Error())
				} else if perr2 := pushLedgerUpdate(ctx, git, root, branch, scaffoldAuth); perr2 != nil {
					app.auditAs(principal, "repo.scaffold_branch.pr_gap", perr2.Error())
				}
			}
		}
	}

	notes := []string{
		"This repository's working copy on the gateway host is now checked out on \"" + branch + "\".",
	}
	switch {
	case existingPRFound:
		// Ordered FIRST: on a retry, a pull request for this branch already existed (found via a
		// read-only `gh pr view`), so nothing was pushed this call — never claim it was.
		notes[0] += " A pull request for this branch already existed and was found — a human still needs to review and merge it."
	case prURL != "":
		notes[0] += " It was pushed to origin and a pull request was opened — a human still needs to review and merge it."
	case pushed && capabilityGap == gapPRURLUnknown:
		// Ordered BEFORE the generic "pushed" case below: the PR really was opened here (gh
		// exited 0) — only its URL is unknown — so this must never share the "opening the pull
		// request failed" wording, which would be factually wrong for this specific gap.
		notes[0] += " It was pushed to origin and a pull request was opened, but l00prite couldn't read its URL: " + capabilityGap
	case pushed:
		// The branch DID reach origin here — only `gh pr create` itself failed (gapPRCreate) —
		// so this must never be worded as "nothing was pushed" (that would be exactly the raw-
		// vs-honest-state mismatch this whole design is meant to avoid).
		notes[0] += " It was pushed to origin, but opening the pull request failed: " + capabilityGap
	case attemptedPR:
		notes[0] += " Nothing was pushed: " + capabilityGap
	default:
		notes[0] += " Nothing was pushed automatically — push this branch and open a pull request against " +
			"your default branch to bring the full l00prite methodology in."
	}
	if claudeSkipped {
		notes = append(notes, "CLAUDE.md already existed and was left untouched (never overwritten). "+
			"Add the fixed \"l00prite Protocol\" section by hand if you want the full benefit there too.")
	}
	if superseded {
		// A different tracked branch was left behind (a fresh attempt superseded it): name it so it
		// is never silently lost, matching the out-of-band clear's discipline.
		notes = append(notes, `A previous attempt's branch "`+pendingBranch+`" was left untouched and is no longer tracked; delete it if it still exists: git branch -D `+pendingBranch+`.`)
	}
	// branched_from is meaningless on a retry (the branch already existed; it wasn't "based on" the
	// branch we're currently sitting on, which IS that same branch), so it is nil there.
	branchedFrom := originalBranch
	if isRetry {
		branchedFrom = ""
	}
	resp := map[string]any{
		"already_complete": false,
		"branch":           branch, "branched_from": nilIfEmpty(branchedFrom),
		"commit": nilIfEmpty(hash), "files_created": strSliceAny(created),
		"claude_md_skipped": claudeSkipped,
		"push_instructions": "git push -u origin " + branch,
		"notes":             strSliceAny(notes),
	}
	// retry is present ONLY when the call reused a tracked pending branch (pure push/PR retry OR a
	// re-scaffold-on-same-branch drift retry). An omitted/false open_pr fresh call keeps today's
	// byte-identical shape — this key never appears there.
	if isRetry {
		resp["retry"] = true
	}
	// These keys are only ever added when open_pr=true actually triggered an attempt — an
	// omitted/false open_pr must produce a response byte-identical to before this field existed,
	// so pre-existing automation hitting this endpoint sees no shape change at all.
	if attemptedPR {
		resp["pushed"] = pushed
		resp["pr_url"] = nilIfEmpty(prURL)
		resp["capability_gap"] = nilIfEmpty(capabilityGap)
		if prCommand != "" {
			resp["pr_command"] = prCommand
		}
	}
	sendJSON(w, 200, resp)
}
