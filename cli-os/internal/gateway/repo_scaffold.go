// The maintainer-requested "add l00prite to this repo" action. Registering or cloning a repo
// already auto-scaffolds a MINIMAL .l00prite/ memory subset silently (see repos.go's
// repoRegisteredResponse) — no prompts, no AGENTS.md, no CLAUDE.md protocol section, no vendor
// adapters, and nothing branched or committed. Without those, a registered repo does not get
// "the full benefit of the l00prite methodology": no .l00prite/prompts/ means no execute-loop,
// and no AGENTS.md/vendor adapters means no cross-vendor discovery.
//
// This is the opposite of that: an explicit, consent-gated action (a dashboard checkbox or
// button — NEVER triggered automatically) that creates a LOCAL branch, writes the full protocol
// (engine.Files.ScaffoldFull), and commits it. Nothing is ever pushed or PR'd automatically —
// AGENTS.md.template's own hard rule ("never push... without explicit per-action permission")
// applies just as much to l00prite's own automation as to a model acting inside a scaffolded
// project — so the response instead carries copy-paste instructions for the user to do that
// themselves.
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

	if _, _, aerr := files.AcquireLock(scaffoldBranchSession, "full protocol scaffold branch", 120); aerr != nil {
		oaiError(w, 409, "Could not scaffold: another agent currently holds this repo's lock ("+aerr.Error()+"). Try again once its run finishes.", "invalid_request_error", "lock_held")
		return
	}
	// No `defer` release: releasing writes lock.json, and a write that lands AFTER CommitUnit
	// leaves the new branch's working tree dirty the moment this handler returns (the same class
	// of bug this codebase's own PR #6/#7 review rounds fixed for the ledger append and the lease
	// write — see CLAUDE.md's Run Ledger). Every path below releases explicitly BEFORE the commit,
	// so the released lock.json state is part of that single commit, not stranded after it.
	branch := "l00prite/add-protocol-" + strings.TrimPrefix(util.RID("x"), "x_")
	if err := engine.EnsureRunBranch(git, root, branch); err != nil {
		_ = files.ReleaseLock(scaffoldBranchSession)
		oaiError(w, 400, "Could not create a branch: "+err.Error()+". Commit or stash any other uncommitted changes first — files already in .l00prite/ don't block this.", "invalid_request_error", "branch_failed")
		return
	}

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

	notes := []string{
		"This repository's working copy on the gateway host is now checked out on \"" + branch +
			"\". Nothing was pushed automatically — push this branch and open a pull request against " +
			"your default branch to bring the full l00prite methodology in.",
	}
	if claudeSkipped {
		notes = append(notes, "CLAUDE.md already existed and was left untouched (never overwritten). "+
			"Add the fixed \"l00prite Protocol\" section by hand if you want the full benefit there too.")
	}
	sendJSON(w, 200, map[string]any{
		"already_complete": false,
		"branch":           branch, "branched_from": nilIfEmpty(originalBranch),
		"commit": nilIfEmpty(hash), "files_created": strSliceAny(created),
		"claude_md_skipped": claudeSkipped,
		"push_instructions": "git push -u origin " + branch,
		"notes":             strSliceAny(notes),
	})
}
