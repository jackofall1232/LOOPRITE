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
	if len(missing) == 0 && !claudeMissing {
		sendJSON(w, 200, map[string]any{
			"already_complete": true, "branch": nil,
			"note": "This repo already has the full l00prite protocol (AGENTS.md, the loop prompts, and the vendor adapters) — nothing to add.",
		})
		return
	}

	git := gitx.Detect()
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
	branch := "l00prite/add-protocol-" + strings.TrimPrefix(util.RID("x"), "x_")
	if err := engine.EnsureRunBranch(git, root, branch); err != nil {
		oaiError(w, 400, "Could not create a branch: "+err.Error()+". Commit or stash any other uncommitted changes first — files already in .l00prite/ don't block this.", "invalid_request_error", "branch_failed")
		return
	}

	if _, _, aerr := files.AcquireLock(scaffoldBranchSession, "full protocol scaffold branch", 120); aerr != nil {
		oaiError(w, 409, "Created branch \""+branch+"\" but could not acquire the lock: "+aerr.Error()+". Another agent must have started a run between the check above and now — try again.", "invalid_request_error", "lock_held")
		return
	}
	// No `defer` release: releasing writes lock.json, and a write that lands AFTER CommitUnit
	// leaves the new branch's working tree dirty the moment this handler returns (the same class
	// of bug referenced above, just for the release write instead of the branch-vs-acquire order).
	// Every path below releases explicitly BEFORE the commit, so the released lock.json state is
	// part of that single commit, not stranded after it.
	created, claudeSkipped, serr := files.ScaffoldFull(filepath.Base(root), "")
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

	var hash string
	if len(created) > 0 {
		var cerr error
		hash, cerr = engine.CommitUnit(git, root, "Add l00prite protocol (AGENTS.md, loop prompts, vendor adapters)")
		if cerr != nil {
			oaiError(w, 500, "Wrote the protocol files on branch \""+branch+"\" but failed to commit them: "+cerr.Error(), "configuration_error", "")
			return
		}
	}
	app.auditAs(principal, "repo.scaffold_branch", id)

	// open_pr=true is this action's per-action permission for pushing and opening a PR (see this
	// file's package comment). Only attempted when a real commit was actually made this call —
	// see this handler's earlier lock-release-before-commit comment for why "nothing new to
	// push" must never trigger a git operation at all, matching engine.EnsureRunBranch/CommitUnit
	// callers' convention of treating an empty diff as a no-op rather than a degenerate action.
	openPR := body.OpenPR != nil && *body.OpenPR
	attemptedPR := openPR && len(created) > 0
	var pushed bool
	var prURL, capabilityGap, prCommand string
	if attemptedPR {
		ctx := r.Context()
		ok, gapMsg, rawErr := probePushPRCapability(ctx, git, root, branch)
		if !ok {
			capabilityGap = gapMsg
			auditDetail := gapMsg
			if rawErr != nil {
				auditDetail = rawErr.Error() // raw text: audit log only, never the client — see probePushPRCapability's doc comment.
			}
			app.auditAs(principal, "repo.scaffold_branch.pr_gap", auditDetail)
		} else {
			var perr error
			pushed, prURL, perr = openScaffoldPR(ctx, git, root, branch, scaffoldPRTitle, scaffoldPRBody(branch))
			switch {
			case perr != nil && pushed:
				// The consented push already landed on origin — never rolled back, never retried
				// blindly. Only PR creation itself needs a human to finish by hand.
				capabilityGap = gapPRCreate
				prCommand = ghPRCreateCommand(branch)
				app.auditAs(principal, "repo.scaffold_branch.pr_gap", perr.Error())
			case perr != nil:
				capabilityGap = gapPushFailed
				app.auditAs(principal, "repo.scaffold_branch.pr_gap", perr.Error())
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
				app.auditAs(principal, "repo.scaffold_branch.pr_gap", "gh pr create succeeded (exit 0) but printed no parseable PR URL")
			default:
				app.auditAs(principal, "repo.scaffold_branch.pr", prURL)
				// Record the PR URL in the repo's own ledger as its OWN commit, after the PR
				// already exists — writing it before the PR exists would have no URL to record,
				// and leaving it uncommitted after this handler returns recreates the exact
				// dirty-tree-on-return bug class this repo has already fixed three times (PR #6's
				// ledger append, PR #7's lease write, this handler's own lock-release-before-
				// commit ordering above). Best-effort: a failure here never unwinds the real PR
				// that already exists on GitHub.
				_ = files.AppendLedger(engine.LedgerEntry{
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
				})
				if aerr := git.AddPaths(root, []string{".l00prite/ledger.md"}); aerr == nil {
					if _, cerr := engine.CommitUnit(git, root, "Record PR URL in l00prite ledger"); cerr == nil {
						if perr2 := pushLedgerUpdate(ctx, git, root, branch); perr2 != nil {
							app.auditAs(principal, "repo.scaffold_branch.pr_gap", perr2.Error())
						}
					} else {
						app.auditAs(principal, "repo.scaffold_branch.pr_gap", cerr.Error())
					}
				} else {
					app.auditAs(principal, "repo.scaffold_branch.pr_gap", aerr.Error())
				}
			}
		}
	}

	notes := []string{
		"This repository's working copy on the gateway host is now checked out on \"" + branch + "\".",
	}
	switch {
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
	resp := map[string]any{
		"already_complete": false,
		"branch":           branch, "branched_from": nilIfEmpty(originalBranch),
		"commit": nilIfEmpty(hash), "files_created": strSliceAny(created),
		"claude_md_skipped": claudeSkipped,
		"push_instructions": "git push -u origin " + branch,
		"notes":             strSliceAny(notes),
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
