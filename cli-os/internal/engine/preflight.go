// Pre-flight: execute-loop.md mode-entry steps 1-5, mechanically. Reads all memory (scaffolding
// the .l00prite memory files first when absent), checks the lock BEFORE any write, performs
// stale-run recovery and the schema migration under the engine's own lease, and assembles the
// complete display the human confirms with Start. A foreign active lease, a dirty worktree, or
// an unroutable role blocks Start rather than degrading silently.
package engine

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// perActionPermissions is the protocol's always-separately-gated action list, displayed verbatim.
var perActionPermissions = []string{
	"git push (any remote)",
	"merge",
	"deploy / publish",
	"changing credentials or secrets",
	"deleting anything outside the repository",
	"installing or upgrading dependencies not named in this pre-flight",
	"editing any Autonomous-Edit Denylist path",
	"running any command not on the allowlist below",
}

// checkGitReady verifies the target is a git repo with at least one commit — read-only; the run
// branch itself is created at arming time. It goes through the engine's gitx seam (exec git, or
// the pure-Go go-git fallback when no git binary is on PATH — see docs/android-architecture.md §4
// G4), so this can only ever report a genuine "not a git repository" / "git status failed"
// problem — never "git is not installed": with go-git compiled in, some implementation can always
// at least read repository state. A dirty worktree is NOT a blocker: StartRun's AutoCheckpoint
// commits it automatically, so it is surfaced here as a Note (a Blocker would disable the Start
// button entirely — see dashboard.html's hasBlockers gate — making that checkpoint unreachable).
func checkGitReady(git gitx.Client, root, runID string) (blockers, notes []string) {
	git = gitOrDetect(git)
	// Plain-English, fixed strings only -- never the raw git/go-git error text (adversarial-review
	// finding: these two Blockers flow verbatim into the pre-flight display and from there straight
	// into the dashboard, the same class of leak humanizeStartError (gateway/runs.go) closes for
	// StartRun's own errors).
	if _, err := git.RevParseHead(root); err != nil {
		blockers = append(blockers, "this project doesn't look like a ready git repository yet — it needs at least one commit before an autonomous run can start (and it must actually be a git repository)")
		return blockers, notes
	}
	out, err := git.StatusPorcelain(root)
	if err != nil {
		blockers = append(blockers, "l00prite couldn't check this project's git status — the repository may be corrupted, or the gateway may not have permission to read it")
		return blockers, notes
	}
	if dirty := dirtyPathsOutsideL00prite(out); len(dirty) > 0 {
		notes = append(notes, fmt.Sprintf(
			"this project has %d unsaved file(s); l00prite will save them as a checkpoint commit (\"WIP: auto-checkpoint before run-%s\") when you press Start, so nothing is lost: %s",
			len(dirty), runID, strings.Join(dirty, ", ")))
	}
	return blockers, notes
}

// extractSection returns the body of a "## <heading>" markdown section (until the next ## or EOF).
func extractSection(doc, heading string) string {
	lines := strings.Split(doc, "\n")
	var out []string
	in := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "## ") {
			if in {
				break
			}
			if strings.Contains(strings.ToLower(t), strings.ToLower(heading)) {
				in = true
				continue
			}
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// plannedUnitsFrom lists open todos (capped) plus each pending event by name, per the protocol's
// "planned units of work, listed individually" requirement.
func plannedUnitsFrom(snap MemorySnapshot, goal string) []PlannedUnit {
	var units []PlannedUnit
	for _, l := range strings.Split(snap.Todos, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "- [ ]") {
			units = append(units, PlannedUnit{Source: "todos", Text: strings.TrimSpace(strings.TrimPrefix(t, "- [ ]"))})
			if len(units) >= 12 {
				break
			}
		}
	}
	for _, ev := range snap.PendingEvents {
		units = append(units, PlannedUnit{Source: "event", Text: "pending event: " + ev})
	}
	if len(units) == 0 {
		units = append(units, PlannedUnit{Source: "goal", Text: goal + " (units selected per-iteration by the planning role)"})
	}
	return units
}

// samplePlannerReq / sampleCoderReq are minimal representative requests for the dry-run team
// preview — the coder sample carries a tool so the capability filter enforces tool support.
func sampleReqForRole(role string) map[string]any {
	req := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "preflight team preview"}},
	}
	if role == RoleCode {
		req["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "write_file", "parameters": map[string]any{"type": "object"},
		}}}
	}
	return req
}

// BuildPreflight performs mode-entry steps 1-5 for a run and persists the resulting display.
// It never arms anything. Recovery/migration writes happen only under the engine's own lease
// and are surfaced in Notes; anything that must stop Start lands in Blockers.
func (e *Engine) BuildPreflight(run *Run) (*Preflight, error) {
	f := Files{Root: run.RepoRoot}
	now := util.NowISO()
	pf := &Preflight{
		RunID:               run.ID,
		BuiltAt:             now,
		Goal:                run.Config.Goal,
		Objective:           run.Config.Objective,
		MaxIterations:       run.Config.MaxIterations,
		RunBoundaries:       RunBoundaries,
		PerActionPermission: perActionPermissions,
		NoProgressThreshold: run.Config.NoProgressThreshold,
		CommandAllowlist:    run.Config.CommandAllowlist,
		Branch:              runBranch(run.ID),
		LikelyChangedPaths: []string{
			"(selected per unit by the planning role — every change is shown live in the run feed and committed on branch " + runBranch(run.ID) + ")",
		},
	}
	// Display the gate table fully resolved: absent classes read as require_approval.
	gates := map[string]string{}
	for _, c := range GateClasses {
		gates[c] = PolicyRequireApproval
	}
	for k, v := range run.Config.Gates {
		gates[k] = v
	}
	pf.Gates = gates

	// Step 1 — read all memory; scaffold the memory files first when absent (never overwrites).
	snap, err := f.ReadSnapshot()
	if err != nil {
		pf.Blockers = append(pf.Blockers, "could not read .l00prite memory: "+err.Error())
	}
	if !snap.HasL00prite {
		created, serr := f.Scaffold(filepath.Base(run.RepoRoot), run.Config.Goal)
		if serr != nil {
			pf.Blockers = append(pf.Blockers, "failed to scaffold .l00prite memory: "+serr.Error())
		} else {
			pf.Notes = append(pf.Notes, fmt.Sprintf("scaffolded .l00prite memory files (%d created; loop prompts deliberately not copied — see .l00prite/README.md)", len(created)))
			if snap, err = f.ReadSnapshot(); err != nil {
				pf.Blockers = append(pf.Blockers, "could not re-read scaffolded memory: "+err.Error())
			}
		}
	}

	// Step 2 — check the lock FIRST. A foreign active unexpired lease blocks: write nothing.
	avail := LockAvailability(snap.Lock, run.ID)
	if avail == "foreign" {
		l := snap.Lock
		pf.Blockers = append(pf.Blockers, fmt.Sprintf(
			"another agent holds the .l00prite lock (owner %s/%s, purpose %q, expires %s) — lock_lease_conflict; nothing was written",
			l.OwnerAgent, l.OwnerSession, l.Purpose, l.ExpiresAt))
		return e.finishPreflight(run, pf, snap, false)
	}

	// Steps 3+4 — stale-run recovery and schema migration, under our own short lease.
	staleArmed := isTruthy(dig(snap.State, "execution_active")) || isTruthy(dig(snap.Heartbeat, "execution", "enabled"))
	needsMigration := snap.Heartbeat != nil && dig(snap.Heartbeat, "execution") == nil
	needsBackfill := snap.Heartbeat != nil && dig(snap.Heartbeat, "execution") != nil &&
		dig(snap.Heartbeat, "execution", "iterations_since_progress") == nil
	if staleArmed || needsMigration || needsBackfill {
		// If we already hold this exact lease (re-pre-flighting the SAME run — e.g. after a
		// crash-and-restart reconciliation, well within its own TTL — avail is "mine"),
		// AcquireLock correctly refuses to re-acquire a lock its caller already owns; refresh
		// it instead. Anything else (free/stale) goes through Acquire as before, which also
		// yields `prior` for the stale-reclamation note below.
		var prior *LockInfo
		var lerr error
		if avail == "mine" {
			lerr = f.RefreshLock(run.ID, 300)
		} else {
			_, prior, lerr = f.AcquireLock(run.ID, "execute-loop pre-flight recovery/migration (l00prite OS engine)", 300)
		}
		if lerr != nil {
			pf.Blockers = append(pf.Blockers, "could not acquire/refresh the .l00prite lock for recovery: "+lerr.Error())
		} else {
			var did []string
			if prior != nil && (prior.Status == "expired" || prior.Status == "active") {
				did = append(did, fmt.Sprintf("reclaimed stale lock %s (owner %s/%s, expired %s)", prior.LockID, prior.OwnerAgent, prior.OwnerSession, prior.ExpiresAt))
			}
			hb := snap.Heartbeat
			if hb == nil {
				hb = map[string]any{}
			}
			if EnsureExecutionBlock(hb) {
				did = append(did, "migrated heartbeat.json to schema v2 (execution block added/backfilled)")
			}
			if staleArmed {
				DisarmHeartbeat(hb, "", "stale execution arming recovered by l00prite OS engine pre-flight (previous run crashed or was interrupted)", "in_progress", now)
				st := snap.State
				if st == nil {
					st = map[string]any{}
				}
				SetStateRun(st, false, "", "recovered", "recovered", "", "re-run pre-flight and confirm to resume", "", now)
				if werr := f.WriteState(st); werr != nil {
					pf.Blockers = append(pf.Blockers, "failed to write state.json during recovery: "+werr.Error())
				}
				did = append(did, "disarmed stale execution run (state.execution_active and heartbeat.execution.enabled cleared)")
			}
			if werr := f.WriteHeartbeat(hb); werr != nil {
				pf.Blockers = append(pf.Blockers, "failed to write heartbeat.json during recovery/migration: "+werr.Error())
			}
			if len(did) > 0 {
				if lederr := f.AppendLedger(LedgerEntry{
					Timestamp: now, RunID: run.ID,
					Goal:            "pre-flight recovery/migration for run " + run.ID,
					TriggeringEvent: "none",
					Decision:        "stale-lock-recovery / schema migration",
					CompletedWork:   strings.Join(did, "; "),
					ChangedFiles:    ".l00prite/heartbeat.json, .l00prite/state.json, .l00prite/lock.json",
					EventStatus:     "not applicable",
					Confidence:      "high — mechanical recovery per execute-loop.md steps 3-4",
					NextAction:      "human reviews this pre-flight and confirms with Start",
					LockNote:        "acquired and released for recovery under the engine's own lease",
				}); lederr != nil {
					pf.Blockers = append(pf.Blockers, "failed to record recovery in ledger.md: "+lederr.Error())
				}
				pf.Notes = append(pf.Notes, did...)
			}
			// Never hold the lease while waiting for the human (step 4's release rule).
			if rerr := f.ReleaseLock(run.ID); rerr != nil {
				pf.Blockers = append(pf.Blockers, "failed to release the recovery lease: "+rerr.Error())
			}
			if snap, err = f.ReadSnapshot(); err != nil {
				pf.Blockers = append(pf.Blockers, "could not re-read memory after recovery: "+err.Error())
			}
		}
	}

	// Step 5 — assemble the display.
	pf.DoD = extractSection(snap.Blueprint, "definition of done")
	if pf.DoD == "" {
		pf.DoD = "The stated goal is implemented and the designated done-check (first allowlisted command) passes: " + run.Config.Goal
	}
	pf.PlannedUnits = plannedUnitsFrom(snap, run.Config.Goal)
	pf.Denylist = ParseDenylist(snap.Constraints)
	if len(pf.Denylist) == 0 {
		pf.Notes = append(pf.Notes, "constraints.md has no Autonomous-Edit Denylist — only the engine's protocol-file hard-deny applies; consider adding one")
	}
	pf.CurrentIteration = intOr(dig(snap.Heartbeat, "execution", "current_iteration"), 0)

	if len(run.Config.CommandAllowlist) == 0 {
		pf.Blockers = append(pf.Blockers, "the command allowlist is empty — name at least one verification command (its first entry is the done-check)")
	} else {
		pf.Notes = append(pf.Notes, fmt.Sprintf("done-check convention: %q (the first allowlisted command) must pass before definition_of_done_met", run.Config.CommandAllowlist[0]))
	}

	// Team assembly: resolve each role through the router, dry-run. Unroutable roles block.
	plan, perr := PlanForObjective(run.Config.Objective)
	if perr != nil {
		pf.Blockers = append(pf.Blockers, perr.Error())
	} else {
		roles := []string{RolePlan, RoleCode}
		if plan.Review {
			roles = append(roles, RoleReview)
		}
		roles = append(roles, RoleSummarize)
		for _, role := range roles {
			model := ModelForRole(plan, role)
			prev, rerr := e.Caller.PreviewRoute(run.Project, run.Config.RepoID, model, sampleReqForRole(role))
			if rerr != nil {
				msg := fmt.Sprintf("role %q cannot be routed (%s): %s", role, model, rerr.Error())
				if run.Config.Objective == ObjectivePrivacy {
					msg += ` — the privacy objective requires an operator-defined routing profile "privacy" with a providers allowlist (e.g. local/self-hosted); it is never silently downgraded to a cloud provider`
				}
				pf.Blockers = append(pf.Blockers, msg)
				continue
			}
			pf.Team = append(pf.Team, TeamMember{Role: role, Profile: strings.TrimPrefix(model, "auto:"), Provider: prev.Provider, Model: prev.Model, Reason: prev.Reason})
		}
	}

	gitBlockers, gitNotes := checkGitReady(e.Git, run.RepoRoot, run.ID)
	pf.Blockers = append(pf.Blockers, gitBlockers...)
	pf.Notes = append(pf.Notes, gitNotes...)
	return e.finishPreflight(run, pf, snap, true)
}

// finishPreflight persists the display + status and emits the feed event.
func (e *Engine) finishPreflight(run *Run, pf *Preflight, snap MemorySnapshot, _ bool) (*Preflight, error) {
	status := StatusReady
	if len(pf.Blockers) > 0 {
		status = StatusBlocked
	}
	raw, err := json.Marshal(pf)
	if err != nil {
		return nil, err
	}
	if err := e.Store.SavePreflight(run.ID, string(raw), status); err != nil {
		return nil, err
	}
	_, _ = e.Store.AppendEvent(run.ID, EvPreflight, map[string]any{
		"status": status, "blockers": pf.Blockers, "notes": pf.Notes, "team": pf.Team,
	})
	run.Status = status
	run.PreflightJSON = string(raw)
	run.PreflightAt = pf.BuiltAt
	return pf, nil
}

// dirtyPathsOutsideL00prite returns the porcelain paths that are NOT under .l00prite/. The
// clean-tree requirement protects the user's uncommitted work; the engine's own .l00prite memory
// (scaffolded/armed at pre-flight, committed onto the run branch) is expected to be dirty and is
// exempt. A porcelain line is "XY <path>" with the path at column 3.
func dirtyPathsOutsideL00prite(porcelain string) []string {
	var dirty []string
	for _, line := range strings.Split(strings.TrimRight(porcelain, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path := strings.TrimSpace(line[min(len(line), 3):])
		// Renamed entries render as "old -> new"; check the destination.
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		path = strings.Trim(path, `"`)
		if path == ".l00prite" || strings.HasPrefix(path, ".l00prite/") {
			continue
		}
		dirty = append(dirty, path)
	}
	return dirty
}

func runBranch(runID string) string {
	suffix := runID
	if i := strings.LastIndex(runID, "_"); i >= 0 && i+1 < len(runID) {
		suffix = runID[i+1:]
	}
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return "l00prite/run-" + suffix
}

// ---- small map helpers (heartbeat/state are surgical map[string]any documents) ----

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func isTruthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func intOr(v any, def int) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return def
}
