// Gateway-side tracking for the "Add l00prite" retry story (repo_scaffold.go's
// HandleRepoScaffoldBranch). A single "Add l00prite" attempt can create + commit a branch locally
// and THEN hit a push/PR capability gap (no gh, gh not signed in, a bad remote). Before this
// tracking existed, a retry click no-op'd: FullProtocolGaps() reported the repo already complete
// (the protocol files from attempt 1 are committed on that branch), so the handler returned
// {"already_complete": true} without ever re-reaching the push/PR logic. These helpers persist just
// enough — which branch that pending attempt is on — for a later click to retry the push/PR.
//
// This state lives in the GATEWAY's own SQLite DB (the repo_scaffold_attempts table), NOT in the
// target repo's cooperative .l00prite/lock.json — the two are unrelated (see repo_scaffold_state's
// db.go table comment). All four helpers use direct SQL on app.DB with state.Ctx(), matching
// auditAs's precedent in providers.go rather than the Tx[] path (no cross-row atomicity is needed).
package gateway

import (
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// pendingScaffoldAttempt reports the tracked pending branch for repoID, if any. A read failure of
// ANY kind (sql.ErrNoRows OR a genuine DB error) returns ok=false: a failed tracking read must
// degrade to EXACTLY today's pre-fix behavior — no retry offered, which is always safe because the
// handler then never pushes without fresh open_pr consent — never to a 500 or a stray push. That is
// why this deliberately does not distinguish ErrNoRows from other errors: both mean "act as if there
// is no pending attempt", which is the safe default in every case.
func (app *App) pendingScaffoldAttempt(repoID string) (branch string, pushed bool, ok bool) {
	var pushedInt int
	err := app.DB.QueryRowContext(state.Ctx(),
		`SELECT branch, pushed FROM repo_scaffold_attempts WHERE repo_id = ?`, repoID).
		Scan(&branch, &pushedInt)
	if err != nil {
		// sql.ErrNoRows (the common "no pending attempt" case) and any other error (a corrupt/locked
		// DB) are deliberately folded into the SAME branch: both mean "act as if there is no pending
		// attempt", which is the safe pre-fix default — retryability is a convenience, never safety.
		return "", false, false
	}
	return branch, pushedInt == 1, true
}

// recordScaffoldAttempt writes (or supersedes in place) the pending row for repoID after a real
// scaffold commit lands, BEFORE any push/PR is attempted, so a crash mid-PR still leaves the row for
// a later retry. Best-effort (`_, _ =`), matching auditAs: a failed write only costs retryability on
// this repo, never safety — the push/PR attempt this call is about to make proceeds regardless.
func (app *App) recordScaffoldAttempt(repoID, branch, hash string) {
	now := util.NowISO()
	_, _ = app.DB.ExecContext(state.Ctx(),
		`INSERT INTO repo_scaffold_attempts(repo_id,branch,commit_hash,pushed,created_at,updated_at)
		 VALUES(?,?,?,0,?,?)
		 ON CONFLICT(repo_id) DO UPDATE SET
		   branch=excluded.branch, commit_hash=excluded.commit_hash, pushed=0, updated_at=excluded.updated_at`,
		repoID, branch, hash, now, now)
}

// markScaffoldAttemptPushed records that the tracked branch's push landed but no PR was confirmed
// opened (the gapPRCreate outcome). Used only for honest note wording on a subsequent retry; never
// used for a git decision. Best-effort, same rationale as recordScaffoldAttempt.
func (app *App) markScaffoldAttemptPushed(repoID string) {
	_, _ = app.DB.ExecContext(state.Ctx(),
		`UPDATE repo_scaffold_attempts SET pushed = 1, updated_at = ? WHERE repo_id = ?`,
		util.NowISO(), repoID)
}

// clearScaffoldAttempt drops the pending row for repoID — called when a PR is confirmed opened/found,
// when the protocol is complete out of band, when a fresh attempt supersedes it, or when the repo is
// removed. Best-effort: a failed clear at worst offers a stale retry that then finds the PR already
// exists (lookupExistingPR) and clears the row on that next pass — never a wrong push or a 500.
func (app *App) clearScaffoldAttempt(repoID string) {
	_, _ = app.DB.ExecContext(state.Ctx(),
		`DELETE FROM repo_scaffold_attempts WHERE repo_id = ?`, repoID)
}
