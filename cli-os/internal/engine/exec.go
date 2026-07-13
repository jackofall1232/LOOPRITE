// The per-role model calls, the coder tool-loop with approval gating, mechanical verification,
// and the persist/exit steps. These are the parts of the loop that touch the gateway (via the
// ModelCaller seam), the filesystem/shell (via Toolbox), and the protocol memory files.
package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// callRole issues one model turn through the gateway seam and records the collaboration event
// (provider/model/cost) — the multi-provider record the OS vision asks for. The engine names
// only a role's model (auto:<profile>); the gateway resolves the provider.
func (e *Engine) callRole(ctx context.Context, run *Run, role string, req map[string]any, injectMemory, bridge bool) (TurnResult, error) {
	res, err := e.Caller.Turn(ctx, TurnInput{
		Project: run.Project, RepoID: run.Config.RepoID, RepoRoot: run.RepoRoot,
		Req: req, InjectMemory: injectMemory, Bridge: bridge,
		Meta: map[string]any{"kind": "engine_unit", "run": run.ID, "iteration": run.CurrentIteration, "role": role},
	})
	if err != nil {
		return res, err
	}
	// Accumulate across every turn in this iteration (planner + coder loop + reviewer); the loop
	// folds this into the run total and resets it at persist time. The gateway meters/commits each
	// call's real cost independently through the PEP — this aggregate is the run's displayed total.
	run.CostThisTurn += res.CostUSD
	_, _ = e.Store.AppendEvent(run.ID, EvModelTurn, map[string]any{
		"role": role, "provider": res.Provider, "model": res.Model,
		"cost_usd": res.CostUSD, "cost_unconfirmed": res.Unconfirmed,
	})
	return res, nil
}

// runCoder drives the bounded coder tool-loop for one unit. Returns ("", "") on a completed
// unit; otherwise (message, boundary) when the unit forces a run boundary (denied approval on a
// destructive action, missing credential, budget cap, or stuck loop -> human_review_gate).
func (e *Engine) runCoder(ctx context.Context, run *Run, f Files, tb *Toolbox, plan TeamPlan, sel *UnitSelection) (string, string) {
	model := ModelForRole(plan, RoleCode)
	messages := []any{
		map[string]any{"role": "system", "content": coderSystem},
		map[string]any{"role": "user", "content": coderUnitBrief(run, sel)},
	}
	// Per-unit tool-call budget: the run's frozen config value (human-set at creation, clamped
	// 1..200, immutable for the run's life), or the engine default for a legacy pre-v0.7 row
	// (effectiveMaxToolCalls). It counts INDIVIDUAL tool-call executions -- enforced per call in the
	// inner loop below, because a provider may return several tool_calls in ONE response, and
	// max_tool_calls:1 must mean one action, not one turn. The outer loop bounds model turns as a
	// backstop (an empty/nudge turn can't spin forever); the real cap is toolCallsUsed < budget.
	budget := e.effectiveMaxToolCalls(run)
	toolCallsUsed := 0
	for turn := 0; turn < budget; turn++ {
		select {
		case <-ctx.Done():
			return "stopped mid-unit", BoundaryStopSignal
		default:
		}
		req := map[string]any{"model": model, "messages": messages, "tools": tb.Definitions(), "max_tokens": 4096}
		// Coder turns are STRUCTURALLY non-bridge, never plan.Bridge. THIS loop is the only place
		// tb.Execute runs, and it only receives tool calls when the turn goes through runTurn
		// (EngineCaller.Turn's non-bridge branch). A bridged turn routes through RunBridge, which
		// answers every non-bridge tool call (write_file, git_command, push_branch, ...) with a
		// "nobody owns this call" capability-gap string and NEVER executes it — so a bridged coder
		// can neither edit files nor push (the 2026-07 "the only tool is l00prite_bridge / cannot
		// git push" run). Delegation is a chat-time feature incompatible with the engine's own
		// tool-execution loop; the coder must always run non-bridge regardless of the objective.
		turn, err := e.callRole(ctx, run, RoleCode, req, false, false)
		if err != nil {
			return "coder call failed: " + err.Error(), BoundaryHumanReview
		}
		if turn.DeniedReason != "" {
			return "budget cap reached: " + turn.DeniedReason, BoundaryHumanReview
		}
		toolCalls := asArray(turn.Message["tool_calls"])
		if len(toolCalls) == 0 {
			// No tool call — treat the content as a note and nudge once toward finishing.
			messages = append(messages, assistantMsg(turn.Message))
			messages = append(messages, map[string]any{"role": "user",
				"content": "Continue using the tools. Call unit_done when the unit's own check would pass, or unit_blocked if you cannot proceed."})
			continue
		}
		messages = append(messages, assistantMsg(turn.Message))
		for _, tcRaw := range toolCalls {
			tc := asMapEng(tcRaw)
			id := asStrEng(tc["id"])
			fn := asMapEng(tc["function"])
			name := asStrEng(fn["name"])
			args := parseArgs(fn["arguments"])

			switch name {
			case "unit_done":
				return "", ""
			case "unit_blocked":
				kind := asStrEng(args["kind"])
				reason := asStrEng(args["reason"])
				switch kind {
				case "missing_credentials":
					return reason, BoundaryMissingSecrets
				case "ambiguous":
					return reason, BoundaryAmbiguous
				default:
					return reason, BoundaryHumanReview
				}
			}

			// Enforce the budget per INDIVIDUAL tool call, so a batched response carrying several
			// tool_calls can't slip many mutating actions through on one turn's allowance. Terminal
			// calls (unit_done/unit_blocked) returned above and are never charged.
			if toolCallsUsed >= budget {
				return fmt.Sprintf("coder exceeded the per-unit tool-call budget (%d) without finishing — "+
					"create the run with a larger \"Max tool calls per unit\" to give each unit more room", budget), BoundaryHumanReview
			}
			toolCallsUsed++

			outcome := tb.Execute(ctx, name, args, false)
			if outcome.Gate != nil {
				// Per-action permission: suspend this action, ask the human, resume on allow.
				allowed, boundary := e.awaitApproval(ctx, run, *outcome.Gate)
				if boundary != "" {
					return fmt.Sprintf("approval flow ended the run at %s (%s)", boundary, outcome.Gate.Action), boundary
				}
				if !allowed {
					_, _ = e.Store.AppendEvent(run.ID, EvToolDenied, map[string]any{"action": outcome.Gate.Action, "class": outcome.Gate.Class})
					messages = append(messages, toolMsg(id, fmt.Sprintf(`{"status":"denied","note":"A human denied this %s action. Do not retry or work around it — adapt within the unit or call unit_blocked."}`, outcome.Gate.Class)))
					continue
				}
				outcome = tb.Execute(ctx, name, args, true) // approved: execute the exact action
			}
			_, _ = e.Store.AppendEvent(run.ID, EvToolCall, map[string]any{"tool": name, "gated": outcome.Gate != nil})
			messages = append(messages, toolMsg(id, outcome.Result))
		}
	}
	return fmt.Sprintf("coder exceeded the per-unit tool-call budget (%d) without finishing — "+
		"create the run with a larger \"Max tool calls per unit\" to give each unit more room", budget), BoundaryHumanReview
}

// awaitApproval creates a pending approval, transitions the run to waiting_approval, and blocks
// until a decision arrives, the run is cancelled, or the approval timeout fires (fail-closed:
// timeout is a deny that stops the run at destructive_operation_required).
func (e *Engine) awaitApproval(ctx context.Context, run *Run, gate GateRequest) (bool, string) {
	// A gate class configured "deny" never even asks.
	if run.Config.Gates[gate.Class] == PolicyDeny {
		_, _ = e.Store.AppendEvent(run.ID, EvToolDenied, map[string]any{"action": gate.Action, "class": gate.Class, "policy": "deny"})
		return false, ""
	}
	// A gate class configured "auto_approve" (only ever push / pr_create, validated at CreateRun
	// and re-checked here via GateClassAutoApprovable) executes without the human wait -- but
	// never silently: the decision is recorded as a first-class run event carrying the same
	// class/action/args the human-approval path records, under a distinct event kind
	// (EvAutoApproved) so an audit query for human decisions is never polluted. The
	// GateClassAutoApprovable re-check is defense in depth: if a corrupted or hand-edited DB row
	// ever carried auto_approve on a class that was never eligible (merge, destructive, deploy,
	// credential_change, outside_repo), this condition is false and control falls straight
	// through to the unmodified CreateApproval + waiting_approval + timeout-fail-closed flow
	// below -- the failure mode of a broken invariant here is require_approval, never
	// auto-execution.
	if run.Config.Gates[gate.Class] == PolicyAutoApprove && GateClassAutoApprovable(gate.Class) {
		_, _ = e.Store.AppendEvent(run.ID, EvAutoApproved, map[string]any{
			"class": gate.Class, "action": gate.Action, "args": gate.Args,
			"policy": PolicyAutoApprove, "source": "project_auto_pr_setting_confirmed_at_preflight",
		})
		return true, ""
	}
	appr, err := e.Store.CreateApproval(run.ID, gate.Class, gate.Action, gate.Args)
	if err != nil {
		return false, BoundaryHumanReview
	}
	_ = e.Store.SetStatus(run.ID, StatusWaitingApproval)
	_, _ = e.Store.AppendEvent(run.ID, EvApprovalReq, map[string]any{
		"approval_id": appr.ID, "class": gate.Class, "action": gate.Action, "args": gate.Args,
	})

	e.mu.Lock()
	h := e.active[run.ID]
	e.mu.Unlock()
	if h == nil {
		// The run is no longer registered as active (stopped/removed out from under us) —
		// there is nowhere to deliver a decision, so stop rather than block on a nil channel.
		return false, BoundaryStopSignal
	}

	timeout := time.Duration(run.Config.ApprovalTimeoutSec) * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		var sig approvalSignal
		select {
		case <-ctx.Done():
			return false, BoundaryStopSignal
		case <-timer.C:
			_ = e.Store.ExpireApproval(appr.ID)
			_, _ = e.Store.AppendEvent(run.ID, EvApprovalDone, map[string]any{"approval_id": appr.ID, "decision": ApprovalExpired})
			return false, BoundaryDestructive
		case sig = <-h.approve:
		}
		if sig.approvalID != appr.ID {
			continue // a decision for a different approval; keep waiting
		}
		_ = e.Store.SetStatus(run.ID, StatusRunning)
		return sig.allow, ""
	}
}

// runVerification executes one allowlisted verification command through the Toolbox (so the same
// jail + allowlist apply) and records it honestly — command, exit code, summary, timestamp.
func (e *Engine) runVerification(ctx context.Context, run *Run, f Files, command string) VerificationRecord {
	tb := &Toolbox{Root: run.RepoRoot, Allowlist: run.Config.CommandAllowlist, Git: e.Git}
	out := tb.Execute(ctx, "run_command", map[string]any{"command": command}, false)
	rec := VerificationRecord{Command: command, Timestamp: util.NowISO()}
	if out.Gate != nil {
		// Not on the allowlist — a verification command must be pre-approved; record as not run.
		rec.ExitCode = -2
		rec.Summary = "verification command is not on the allowlist; not executed"
		_, _ = e.Store.AppendEvent(run.ID, EvVerify, map[string]any{"command": command, "exit_code": -2, "note": "not allowlisted"})
		return rec
	}
	rec.ExitCode = parseExitCode(out.Result)
	rec.Summary = truncate(firstLines(out.Result, 12), 600)
	_, _ = e.Store.AppendEvent(run.ID, EvVerify, map[string]any{"command": command, "exit_code": rec.ExitCode, "summary": truncate(rec.Summary, 200)})
	return rec
}

// persistIteration is the protocol's "persist before anything else": counters, no-progress
// telemetry, cost, the engine store, and the repo's heartbeat/state/ledger — all before the next
// unit begins. Returns an error if any of these durable writes failed; the caller must stop the
// run rather than advance to the next iteration believing state was recorded when it wasn't —
// otherwise a disk-full or read-only .l00prite/ silently breaks persist-before-continue and
// leaves any other agent reading the repo with stale counters.
func (e *Engine) persistIteration(run *Run, f Files, out iterOutcome) error {
	now := util.NowISO()
	if out.progressed {
		iter := run.CurrentIteration
		run.LastProgressIteration = &iter
		run.IterationsSinceProgress = 0
	} else {
		run.IterationsSinceProgress++
	}
	if err := e.Store.SaveIteration(run.ID, run.CurrentIteration, run.IterationsSinceProgress, run.LastProgressIteration, run.CostThisTurn); err != nil {
		return fmt.Errorf("engine store: %w", err)
	}
	run.CostUSD += run.CostThisTurn
	run.CostThisTurn = 0

	// Repo heartbeat tick (only the whitelisted counters) + ledger append.
	snap, err := f.ReadSnapshot()
	if err != nil {
		return fmt.Errorf("could not read .l00prite memory: %w", err)
	}
	if snap.Heartbeat != nil {
		TickHeartbeat(snap.Heartbeat, run.CurrentIteration, run.IterationsSinceProgress, run.LastProgressIteration, now)
		if err := f.WriteHeartbeat(snap.Heartbeat); err != nil {
			return fmt.Errorf("could not write heartbeat.json: %w", err)
		}
	}
	var verifs []VerificationRecord
	if run.lastVerification != nil {
		verifs = []VerificationRecord{*run.lastVerification}
		run.lastVerification = nil
	}
	if err := f.AppendLedger(LedgerEntry{
		Timestamp: now, RunID: run.ID,
		Goal:            run.Config.Goal,
		TriggeringEvent: "none",
		Decision:        "autonomous execution iteration " + fmt.Sprint(run.CurrentIteration),
		CompletedWork:   out.summary,
		ChangedFiles:    "(see run branch " + run.Branch + " and the run feed)",
		Verifications:   verifs,
		EventStatus:     "not applicable",
		Confidence:      "recorded by the l00prite OS engine",
		NextAction:      nextActionFor(out),
		LockNote:        "held under lease " + run.ID + " (refreshed per iteration)",
	}); err != nil {
		return fmt.Errorf("could not append ledger.md: %w", err)
	}
	_, _ = e.Store.AppendEvent(run.ID, EvPersisted, map[string]any{
		"iteration": run.CurrentIteration, "progressed": out.progressed,
		"iterations_since_progress": run.IterationsSinceProgress, "cost_usd_total": run.CostUSD,
	})
	return nil
}

func nextActionFor(out iterOutcome) string {
	if out.boundary != "" {
		return "run ended at boundary " + out.boundary
	}
	return "continue to the next unit"
}

// exitRun is the mode exit (every boundary except lock_lease_conflict, handled inline in the
// loop): run-summary ledger entry, disarm both sides, release the lease.
func (e *Engine) exitRun(run *Run, f Files, boundary, summary, status string) {
	now := util.NowISO()
	completion := "in_progress"
	if boundary == BoundaryDone {
		completion = "complete"
	}
	snap, _ := f.ReadSnapshot()
	if snap.Heartbeat != nil {
		DisarmHeartbeat(snap.Heartbeat, boundary, summary, completion, now)
		_ = f.WriteHeartbeat(snap.Heartbeat)
	}
	if snap.State != nil {
		blockedNext := "review the run summary and decide whether to resume (a resume is a fresh pre-flight + Start)"
		if boundary == BoundaryDone {
			blockedNext = "run complete — definition of done met"
		}
		SetStateRun(snap.State, false, boundary, status, "planning", "", blockedNext, "", now)
		_ = f.WriteState(snap.State)
	}
	_ = f.AppendLedger(LedgerEntry{
		Timestamp: now, RunID: run.ID,
		Goal:            run.Config.Goal,
		TriggeringEvent: "none",
		Decision:        "run boundary reached: " + boundary,
		CompletedWork:   fmt.Sprintf("run ended after %d iteration(s); %s", run.CurrentIteration, summary),
		ChangedFiles:    "(run branch " + run.Branch + ")",
		EventStatus:     "not applicable",
		Confidence:      "boundary evaluated mechanically by the engine",
		NextAction:      "human review of run " + run.ID,
		LockNote:        "released at run exit",
	})
	_ = f.ReleaseLock(run.ID)

	e.Store.FinishRun(run.ID, status, boundary, summary)
	_, _ = e.Store.AppendEvent(run.ID, EvBoundary, map[string]any{"boundary": boundary, "summary": summary, "status": status, "iterations": run.CurrentIteration})
}

// ---- small shaping helpers ----

func coderUnitBrief(run *Run, sel *UnitSelection) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL (context, do not exceed): %s\n\n", run.Config.Goal)
	fmt.Fprintf(&b, "THIS UNIT — implement exactly this and nothing else:\n%s\n", sel.Description)
	if sel.Rationale != "" {
		fmt.Fprintf(&b, "\nWhy: %s\n", sel.Rationale)
	}
	if len(sel.TargetPaths) > 0 {
		fmt.Fprintf(&b, "\nLikely files: %s\n", strings.Join(sel.TargetPaths, ", "))
	}
	if sel.VerificationCommand != "" {
		fmt.Fprintf(&b, "\nThe engine will verify with: %s\n", sel.VerificationCommand)
	}
	b.WriteString("\nUse the tools. Finish with unit_done once the unit's own check would pass, or unit_blocked if you cannot proceed.")
	return b.String()
}

func assistantMsg(m map[string]any) map[string]any {
	out := map[string]any{"role": "assistant", "content": m["content"]}
	if tc := m["tool_calls"]; tc != nil {
		out["tool_calls"] = tc
	}
	return out
}

func toolMsg(id, content string) map[string]any {
	return map[string]any{"role": "tool", "tool_call_id": id, "content": content}
}
