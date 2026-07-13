// The run engine: arming (Start = the protocol's explicit in-session confirmation) and the
// autonomous iteration loop. Every invariant of execute-loop.md that a prompt can only ask a
// model to honor is enforced here in code the model cannot reach: the loop owns the iteration
// counter, the boundaries, the denylist/allowlist gates, and the persist-before-continue order.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// Engine owns run execution. One Engine per server; runs are keyed by id.
type Engine struct {
	Store  *Store
	Caller ModelCaller
	// Git is the gitx seam (exec-git, or the pure-Go go-git fallback when no git binary is on
	// PATH — see docs/android-architecture.md §4 G4) every git operation this engine performs
	// goes through. New defaults it to gitx.Detect().
	Git gitx.Client

	// PushCred resolves the GitHub push credential for a run's project — injected by the gateway at
	// boot (app.githubAuthFor) so the engine never imports the vault. Nil (the New default) means no
	// credential source; the push_branch tool then uses ambient credentials on exec and reports an
	// honest capability gap on gogit. Bound to each run's project when the Toolbox is built.
	PushCred func(project string) (*gitx.PushAuth, error)

	// LeaseTTLSec is the .l00prite lock TTL for an armed run (refreshed each iteration).
	LeaseTTLSec int
	// IterationTimeout bounds one unit's tool-loop wall-clock.
	IterationTimeout time.Duration
	// MaxToolCalls bounds the coder tool-loop per unit (backstop against a stuck model).
	MaxToolCalls int

	mu     sync.Mutex
	active map[string]*runHandle // runID -> live run control
}

type runHandle struct {
	cancel  context.CancelFunc
	approve chan approvalSignal
}

type approvalSignal struct {
	approvalID string
	allow      bool
}

// New builds an Engine with protocol-sane defaults.
func New(store *Store, caller ModelCaller) *Engine {
	return &Engine{
		Store: store, Caller: caller, Git: gitx.Detect(),
		LeaseTTLSec: 1800, IterationTimeout: 20 * time.Minute, MaxToolCalls: 40,
		active: map[string]*runHandle{},
	}
}

// StartRun is the confirmation gate (execute-loop.md steps 6-7). It requires a FRESH pre-flight
// (status ready) and an explicit confirm token; a persisted flag can never substitute. On
// success it arms the run — engine store + repo heartbeat/state — and launches the loop.
func (e *Engine) StartRun(ctx context.Context, runID, confirmedBy, confirm string) error {
	if strings.TrimSpace(confirm) != "EXECUTE" {
		return fmt.Errorf(`start requires confirm:"EXECUTE" against the displayed pre-flight`)
	}
	run, err := e.Store.GetRun(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return ErrNotFound
	}
	if run.Status != StatusReady || run.PreflightJSON == "" {
		return fmt.Errorf("%w: run is %q; rebuild the pre-flight (it must be ready and blocker-free) before Start", ErrBadState, run.Status)
	}
	// One active run per repo (the runtime realization of single-writer memory). A lookup
	// failure must not be treated as "no active run" — that would let two runs race the same
	// repo — so it fails closed as a plain error rather than falling through to arm.
	other, err := e.Store.ActiveRunForRepo(run.Project, run.Config.RepoID)
	if err != nil {
		return fmt.Errorf("could not check for an active run against this repo: %w", err)
	}
	if other != nil && other.ID != run.ID {
		return fmt.Errorf("%w: run %s is already active against repo %q", ErrBadState, other.ID, run.Config.RepoID)
	}

	f := Files{Root: run.RepoRoot}
	now := util.NowISO()

	// Lock availability check first (read-only) -- but the actual AcquireLock WRITE is deliberately
	// deferred until after EnsureRunBranch below, not done here. AcquireLock rewrites lock.json with
	// an active lease; if that write happened before the checkpoint+checkout (as it used to), the
	// checkpoint below commits it together with everything else onto whatever branch is CURRENTLY
	// checked out -- so every clean Start polluted the user's own source branch with a WIP commit
	// carrying an active run lease, and checking that branch back out later made it look locked by a
	// run that may already have finished (Codex review, PR #7). Writing the lease after the run
	// branch exists lands it there instead, uncommitted, exactly like the ledger append below.
	lock, err := f.ReadLock()
	if err != nil {
		return err
	}
	if LockAvailability(lock, run.ID) == "foreign" {
		return fmt.Errorf("%w: another agent holds the .l00prite lock", ErrBadState)
	}

	// Read the Denylist fresh (not from a possibly-stale pre-flight) so AutoCheckpoint's refusal
	// gate below is race-proof against a file dirtied after BuildPreflight last ran.
	denylistSnap, dlerr := f.ReadSnapshot()
	if dlerr != nil {
		return fmt.Errorf("could not read .l00prite memory to check the auto-checkpoint against your Denylist: %w", dlerr)
	}
	denylist := ParseDenylist(denylistSnap.Constraints)

	// Auto-checkpoint: commit everything dirty right now (the user's own uncommitted work, plus any
	// pre-existing .l00prite/ drift) onto whatever branch is currently checked out, so the run
	// branch is created from a clean tree. This is deliberately a commit, never a stash.
	// AutoCheckpoint itself refuses (ErrCheckpointRefused) rather than committing a Denylisted or
	// credential-shaped dirty path.
	checkpointHash, cerr := AutoCheckpoint(e.Git, run.RepoRoot, run.ID, denylist)
	if cerr != nil {
		if errors.Is(cerr, ErrCheckpointRefused) {
			return cerr // preserve the more specific sentinel for humanizeStartError
		}
		return fmt.Errorf("%w: %v", ErrCheckpointFailed, cerr)
	}

	branch := runBranch(run.ID)
	if err := EnsureRunBranch(e.Git, run.RepoRoot, branch); err != nil {
		return fmt.Errorf("%w: %v", ErrBadState, err)
	}

	// Only now claim the lease -- see the comment above LockAvailability's check for why this can't
	// happen any earlier. The write lands as an uncommitted change on the run branch just checked
	// out, not on the branch the checkpoint above just committed to.
	if _, _, err := f.AcquireLock(run.ID, "execute-loop run (l00prite OS engine)", e.LeaseTTLSec); err != nil {
		return fmt.Errorf("could not acquire .l00prite lease to arm the run: %w", err)
	}

	// Record the checkpoint in ledger.md only AFTER the checkout succeeds -- not before it, even
	// though the write logically documents what AutoCheckpoint just did. Load-bearing ordering
	// (PR #6 review finding, empirically reproduced): if ledger.md is already git-tracked (e.g. any
	// run after the very first one ever committed it, or a repo whose .l00prite/ was scaffolded and
	// committed via Planning Mode), appending here BEFORE the checkout would re-dirty an
	// already-tracked file immediately before EnsureRunBranch's gogit checkout -- reproducing the
	// EXACT "worktree contains unstaged changes" bug this whole checkpoint mechanism exists to
	// prevent, just with ledger.md as the trigger instead of lock.json. Writing it after the
	// checkout is safe: it lands as an uncommitted change on the (already checked-out) run branch,
	// which is exactly how every other ledger write in this engine already behaves (e.g.
	// persistIteration's per-unit entries, exec.go) -- it rides along with the run's next real
	// commit rather than needing to be committed immediately itself.
	if checkpointHash != "" {
		if lederr := f.AppendLedger(LedgerEntry{
			Timestamp: now, RunID: run.ID,
			Goal:            "auto-checkpoint before run " + run.ID,
			TriggeringEvent: "none",
			Decision:        "auto-commit dirty worktree before creating the run branch",
			CompletedWork:   "WIP checkpoint commit " + checkpointHash,
			ChangedFiles:    "all uncommitted changes at Start time (including .l00prite/)",
			EventStatus:     "not applicable",
			Confidence:      "high — mechanical checkpoint; user work preserved in commit " + checkpointHash,
			NextAction:      "run branch created on top of this checkpoint",
			LockNote:        "checkpointed under the run's own lease",
		}); lederr != nil {
			// A ledger write failure here must not abort an already-successful checkpoint+checkout
			// (the user's work is safely committed and the run branch exists either way) — record
			// it as a run event instead, mirroring BuildPreflight's tolerance for a failed
			// recovery-note write.
			_, _ = e.Store.AppendEvent(run.ID, EvStatus, map[string]any{
				"warning": "failed to record the auto-checkpoint in ledger.md: " + lederr.Error(),
			})
		}
	}

	// Arm the repo files exactly per step 7, then the engine store. A snapshot read failure
	// (e.g. a corrupt heartbeat.json/state.json) must stop here — treating it as "absent" would
	// silently clobber whatever the human needs to see, instead of failing closed as designed.
	snap, err := f.ReadSnapshot()
	if err != nil {
		_ = f.ReleaseLock(run.ID)
		return fmt.Errorf("could not read .l00prite memory to arm the run: %w", err)
	}
	hb := snap.Heartbeat
	if hb == nil {
		hb = map[string]any{}
	}
	EnsureExecutionBlock(hb)
	ArmHeartbeat(hb, run.Config.MaxIterations, confirmedBy, now)
	if err := f.WriteHeartbeat(hb); err != nil {
		_ = f.ReleaseLock(run.ID)
		return err
	}
	st := snap.State
	if st == nil {
		st = map[string]any{}
	}
	SetStateRun(st, true, "", "executing", "execution", run.Config.Goal,
		"autonomous run in progress (l00prite OS engine)", "l00prite-os", now)
	if err := f.WriteState(st); err != nil {
		_ = f.ReleaseLock(run.ID)
		return err
	}

	if err := e.Store.MarkStarted(run.ID, confirmedBy, branch); err != nil {
		// Roll the repo back to disarmed so we never leave a half-armed run.
		DisarmHeartbeat(hb, "", "arming aborted: engine store rejected the start", "not_started", util.NowISO())
		_ = f.WriteHeartbeat(hb)
		SetStateRun(st, false, "", "not_started", "planning", "", "re-run pre-flight", "", util.NowISO())
		_ = f.WriteState(st)
		_ = f.ReleaseLock(run.ID)
		return err
	}
	_, _ = e.Store.AppendEvent(run.ID, EvArmed, map[string]any{
		"confirmed_by": confirmedBy, "branch": branch, "max_iterations": run.Config.MaxIterations, "objective": run.Config.Objective,
	})

	run.Status = StatusRunning
	run.Branch = branch
	run.ConfirmedBy = confirmedBy
	run.unitFailures = map[string]int{}

	loopCtx, cancel := context.WithCancel(context.Background())
	h := &runHandle{cancel: cancel, approve: make(chan approvalSignal, 4)}
	e.mu.Lock()
	e.active[run.ID] = h
	e.mu.Unlock()

	go e.loop(loopCtx, run, h)
	return nil
}

// Stop signals the stop_signal boundary for a running run.
func (e *Engine) Stop(runID string) error {
	e.mu.Lock()
	h := e.active[runID]
	e.mu.Unlock()
	if h == nil {
		return fmt.Errorf("%w: run is not active", ErrBadState)
	}
	_, _ = e.Store.AppendEvent(runID, EvStatus, map[string]any{"stop_requested": true})
	h.cancel()
	return nil
}

// Decide records an approval decision and, if the run is waiting on it, releases the loop.
// The approval must belong to runID: without this check a caller could decide an approval
// belonging to a different run (even a different project, since approval ids are global),
// which would both authorize an action outside the caller's own run and leave the actual
// waiting run's approval undecided until it times out.
func (e *Engine) Decide(runID, approvalID, decision, decidedBy, note string) error {
	existing, err := e.Store.GetApproval(approvalID)
	if err != nil {
		return err
	}
	if existing == nil || existing.RunID != runID {
		return fmt.Errorf("%w: no such approval on this run", ErrNotFound)
	}
	appr, err := e.Store.DecideApproval(approvalID, decision, decidedBy, note)
	if err != nil {
		return err
	}
	_, _ = e.Store.AppendEvent(runID, EvApprovalDone, map[string]any{
		"approval_id": approvalID, "class": appr.Class, "decision": appr.Status, "by": decidedBy,
	})
	e.mu.Lock()
	h := e.active[runID]
	e.mu.Unlock()
	if h != nil {
		select {
		case h.approve <- approvalSignal{approvalID: approvalID, allow: appr.Status == ApprovalAllowed}:
		default:
		}
	}
	return nil
}

// ---- the loop ----

type iterOutcome struct {
	boundary   string // "" means continue
	progressed bool
	summary    string
}

func (e *Engine) loop(ctx context.Context, run *Run, h *runHandle) {
	f := Files{Root: run.RepoRoot}
	defer func() {
		e.mu.Lock()
		delete(e.active, run.ID)
		e.mu.Unlock()
		if r := recover(); r != nil {
			e.exitRun(run, f, BoundaryHumanReview, fmt.Sprintf("engine panic: %v — needs human review", r), StatusStopped)
		}
	}()

	for {
		// stop_signal (cancel) checked before each unit.
		select {
		case <-ctx.Done():
			e.exitRun(run, f, BoundaryStopSignal, "stop requested", StatusStopped)
			return
		default:
		}

		if run.CurrentIteration >= run.Config.MaxIterations {
			e.exitRun(run, f, BoundaryIterationLimit, fmt.Sprintf("reached iteration budget %d", run.Config.MaxIterations), StatusStopped)
			return
		}

		// Refresh the lease; a foreign lease appearing is lock_lease_conflict — write nothing.
		// A read failure must NOT be treated as "no foreign lock": that would let the loop keep
		// writing without a verified lease, exactly the mutual-exclusion violation the lock
		// exists to prevent — so it stops and asks for review instead of guessing.
		lock, err := f.ReadLock()
		if err != nil {
			e.exitRun(run, f, BoundaryHumanReview, "could not read .l00prite/lock.json: "+err.Error(), StatusStopped)
			return
		}
		if LockAvailability(lock, run.ID) == "foreign" {
			_, _ = e.Store.AppendEvent(run.ID, EvBoundary, map[string]any{
				"boundary": BoundaryLockConflict, "owner": lock.OwnerAgent, "note": "wrote nothing to protected memory",
			})
			e.Store.FinishRun(run.ID, StatusStopped, BoundaryLockConflict, "another agent acquired the .l00prite lock mid-run; stopped without writing")
			return
		}
		if err := f.RefreshLock(run.ID, e.LeaseTTLSec); err != nil {
			e.exitRun(run, f, BoundaryHumanReview, "could not refresh the .l00prite lease: "+err.Error(), StatusStopped)
			return
		}

		run.CurrentIteration++
		_, _ = e.Store.AppendEvent(run.ID, EvIteration, map[string]any{"iteration": run.CurrentIteration, "max": run.Config.MaxIterations})

		out := e.iterate(ctx, run, f)

		// Persist before anything else (iteration rule 5): counters, telemetry, ledger, repo files.
		// A failure here means the iteration's outcome was never durably recorded — continuing
		// (even to report the boundary iterate() computed) would advance the run past state
		// nothing else can see, so it stops and escalates instead.
		if err := e.persistIteration(run, f, out); err != nil {
			e.exitRun(run, f, BoundaryHumanReview, "failed to persist iteration "+fmt.Sprint(run.CurrentIteration)+": "+err.Error(), StatusStopped)
			return
		}

		if out.boundary != "" {
			status := StatusStopped
			if out.boundary == BoundaryDone {
				status = StatusDone
			}
			e.exitRun(run, f, out.boundary, out.summary, status)
			return
		}

		// No-progress stall -> human_review_gate (per iteration rule 5).
		if run.IterationsSinceProgress >= run.Config.NoProgressThreshold {
			e.exitRun(run, f, BoundaryHumanReview,
				fmt.Sprintf("no progress for %d iterations (threshold %d) — stopping to escalate rather than burn the budget", run.IterationsSinceProgress, run.Config.NoProgressThreshold),
				StatusStopped)
			return
		}
	}
}

// iterate runs one unit: plan -> execute (coder tool-loop) -> verify -> (review) and returns a
// boundary when the run should stop. It never writes protocol memory (persistIteration does).
func (e *Engine) iterate(ctx context.Context, run *Run, f Files) iterOutcome {
	ictx, cancel := context.WithTimeout(ctx, e.IterationTimeout)
	defer cancel()

	// A snapshot read failure (corrupt/malformed heartbeat.json or state.json) must stop the
	// run rather than plan against stale or half-populated memory.
	snap, err := f.ReadSnapshot()
	if err != nil {
		return iterOutcome{boundary: BoundaryHumanReview, summary: "could not read .l00prite memory: " + err.Error()}
	}
	plan, err := PlanForObjective(run.Config.Objective)
	if err != nil {
		return iterOutcome{boundary: BoundaryAmbiguous, summary: err.Error()}
	}

	// --- plan: select one unit ---
	planReq := BuildPlannerReq(ModelForRole(plan, RolePlan), run.Config.Goal, preflightDoD(run),
		snap.Todos, snap.Ledger, run.Config.CommandAllowlist, run.lastOutcome)
	planTurn, err := e.callRole(ictx, run, RolePlan, planReq, false, false)
	if err != nil {
		return iterOutcome{boundary: BoundaryHumanReview, summary: "planner call failed: " + err.Error()}
	}
	if planTurn.DeniedReason != "" {
		return iterOutcome{boundary: BoundaryHumanReview, summary: "budget cap reached during planning: " + planTurn.DeniedReason}
	}
	sel, err := ParseUnitSelection(planTurn.Message)
	if err != nil {
		return iterOutcome{boundary: BoundaryAmbiguous, summary: "could not parse a unit selection: " + err.Error()}
	}
	switch sel.Action {
	case "done":
		// definition_of_done_met requires the mechanical done-check to actually pass.
		if len(run.Config.CommandAllowlist) > 0 {
			vr := e.runVerification(ictx, run, f, run.Config.CommandAllowlist[0])
			run.lastVerification = &vr
			if vr.ExitCode != 0 {
				run.lastOutcome = fmt.Sprintf("planner declared done but the done-check %q failed (exit %d)", vr.Command, vr.ExitCode)
				return iterOutcome{summary: run.lastOutcome} // continue: not actually done
			}
		}
		return iterOutcome{boundary: BoundaryDone, summary: "definition of done met: " + sel.DoneReason, progressed: true}
	case "ambiguous":
		return iterOutcome{boundary: BoundaryAmbiguous, summary: sel.Question}
	case "missing_credentials":
		return iterOutcome{boundary: BoundaryMissingSecrets, summary: sel.Question}
	}
	_, _ = e.Store.AppendEvent(run.ID, EvUnit, map[string]any{
		"description": sel.Description, "target_paths": sel.TargetPaths, "verification": sel.VerificationCommand,
	})

	// --- execute: coder tool-loop ---
	// Bind the push credential to THIS run's project (decrypted at call time inside the closure, so
	// a disconnect mid-run takes effect immediately). Nil when the gateway wired no PushCred.
	var pushCred func() (*gitx.PushAuth, error)
	if e.PushCred != nil {
		project := run.Project
		pushCred = func() (*gitx.PushAuth, error) { return e.PushCred(project) }
	}
	tb := &Toolbox{Root: run.RepoRoot, Denylist: ParseDenylist(snap.Constraints), Allowlist: run.Config.CommandAllowlist, Branch: run.Branch, Git: e.Git, PushCred: pushCred}
	blocked, boundary := e.runCoder(ictx, run, f, tb, plan, sel)
	if boundary != "" {
		return iterOutcome{boundary: boundary, summary: blocked}
	}

	// --- verify ---
	vcmd := strings.TrimSpace(sel.VerificationCommand)
	if vcmd == "" && len(run.Config.CommandAllowlist) > 0 {
		vcmd = run.Config.CommandAllowlist[0]
	}
	var vr VerificationRecord
	if vcmd != "" {
		vr = e.runVerification(ictx, run, f, vcmd)
		run.lastVerification = &vr
		if vr.ExitCode != 0 {
			sig := failureSignature(sel.Description, vr.Command)
			run.unitFailures[sig]++
			_ = f.AppendFailure(run.ID, sig, fmt.Sprintf("%s (exit %d): %s", vr.Command, vr.ExitCode, truncate(vr.Summary, 300)), run.unitFailures[sig] >= 2)
			if run.unitFailures[sig] >= 2 || f.HasDoNotRetry(snap.Failures, sig) {
				return iterOutcome{boundary: BoundaryUnfixableTests, summary: fmt.Sprintf("unit %q still failing verification after %d attempts", sel.Description, run.unitFailures[sig])}
			}
			run.lastOutcome = fmt.Sprintf("verification failed (exit %d) for unit %q; retry with a different approach", vr.ExitCode, sel.Description)
			return iterOutcome{summary: run.lastOutcome} // continue: retry the unit next iteration
		}
	}

	// --- review (quality-sensitive objectives only) ---
	if plan.Review {
		diff, _ := CurrentDiff(e.Git, run.RepoRoot, 60000)
		if strings.TrimSpace(diff) != "" {
			rvReq := BuildReviewerReq(ModelForRole(plan, RoleReview), sel.Description, diff)
			rvTurn, rerr := e.callRole(ictx, run, RoleReview, rvReq, false, false)
			if rerr == nil && rvTurn.DeniedReason == "" {
				if verdict, perr := ParseReviewVerdict(rvTurn.Message); perr == nil {
					_, _ = e.Store.AppendEvent(run.ID, EvReview, map[string]any{"approve": verdict.Approve, "must_fix": verdict.MustFix, "issues": verdict.Issues})
					if verdict.MustFix {
						run.lastOutcome = "review flagged must-fix issues: " + strings.Join(verdict.Issues, "; ")
						return iterOutcome{summary: run.lastOutcome} // continue: address next iteration
					}
				}
			}
		}
	}

	// Commit the unit on the run branch (a local commit; pushing is a gated action). A commit
	// FAILURE (no author identity configured, a hook rejection, etc.) is not the same as
	// "nothing to commit" (hash=="", cerr==nil, a legitimate no-op for a docs-only/investigation
	// unit) — it means real changes are sitting uncommitted with no record on the run branch, so
	// it must stop for a human rather than being reported as a successfully progressed unit.
	hash, cerr := CommitUnit(e.Git, run.RepoRoot, "l00prite-os: "+truncate(sel.Description, 72))
	if cerr != nil {
		return iterOutcome{boundary: BoundaryHumanReview, summary: fmt.Sprintf("unit %q finished but committing its changes failed: %s", sel.Description, cerr.Error())}
	}
	if hash != "" {
		_, _ = e.Store.AppendEvent(run.ID, EvStatus, map[string]any{"committed": hash[:min(len(hash), 12)]})
	}
	// A push_branch approved during the coder loop is deferred to HERE — after the unit is committed
	// — so origin receives the committed work, not the pre-unit HEAD (and so committing never emptied
	// the reviewer's diff above). A push failure after a successful commit is surfaced for a human;
	// the commit itself stands.
	if tb.PushRequested {
		if pushed, perr := tb.PerformPendingPush(ictx); perr != nil {
			return iterOutcome{boundary: BoundaryHumanReview, summary: fmt.Sprintf("unit %q was committed but pushing the branch to origin failed: %s", sel.Description, perr.Error())}
		} else if pushed {
			_, _ = e.Store.AppendEvent(run.ID, EvToolCall, map[string]any{"tool": "push_branch", "phase": "post_commit", "branch": run.Branch})
		}
	}
	run.lastOutcome = "completed unit: " + sel.Description
	return iterOutcome{progressed: true, summary: sel.Description}
}

func preflightDoD(run *Run) string {
	var pf Preflight
	if run.PreflightJSON != "" {
		_ = json.Unmarshal([]byte(run.PreflightJSON), &pf)
	}
	if pf.DoD != "" {
		return pf.DoD
	}
	return "Implement the goal and make the done-check pass: " + run.Config.Goal
}

func failureSignature(unit, cmd string) string {
	return truncate(strings.ToLower(strings.TrimSpace(unit)), 60) + " :: " + truncate(cmd, 40)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
