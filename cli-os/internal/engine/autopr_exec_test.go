package engine

// Regression tests for the auto_approve runtime path in exec.go's awaitApproval: a run whose
// Gates carry PolicyAutoApprove on an eligible class must execute the gated action without ever
// creating a human approval row or entering waiting_approval, and must record an auditable
// EvAutoApproved event; a gate class that was never eligible (bypassing store validation via a
// hand-built Run, simulating a corrupted/hand-edited DB row) must still fall through to the
// ordinary human-approval flow.

import (
	"context"
	"strings"
	"testing"
)

// TestAutoApprovePushSkipsApprovalAndExecutes confirms (against pre-fix exec.go, where this test
// blocks in waiting_approval and times out never reaching Done) that gates{push:auto_approve}
// lets a run_command-gated "git push" execute immediately: no run_approvals row is ever created
// for it, and the run reaches its terminal state via unit_done rather than being denied at the
// approval timeout.
func TestAutoApprovePushSkipsApprovalAndExecutes(t *testing.T) {
	caller := &scriptedCaller{
		planner: []map[string]any{
			{"action": "unit", "description": "push the branch", "target_paths": []string{}, "verification_command": "true"},
			{"action": "done", "done_reason": "pushed"},
		},
		coder: [][]step{
			{
				{name: "run_command", args: map[string]any{"command": "git push origin main"}},
				{name: "unit_done", args: map[string]any{"summary": "attempted push", "files_changed": []string{}}},
			},
		},
	}
	e := newEngine(t, caller)
	root := newRepo(t)
	run := startRun(t, e, root, "push the branch", func(rc *RunConfig) {
		rc.Gates = map[string]string{GatePush: PolicyAutoApprove}
		rc.ApprovalTimeoutSec = 2 // would time out fast if the auto-approve path were broken
	})

	final := waitTerminal(t, e, run.ID)
	if final.Status != StatusDone || final.Boundary != BoundaryDone {
		t.Fatalf("want done/%s (auto-approve should let the unit finish), got %s/%s (%s)",
			BoundaryDone, final.Status, final.Boundary, final.Summary)
	}

	// No human approval row was ever created for the push -- it was auto-approved, not decided.
	pending, err := e.Store.PendingApprovals(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected zero pending approvals under auto_approve, got %+v", pending)
	}

	// EvAutoApproved was recorded, carrying class/action/args -- the audit trail.
	events, err := e.Store.ListEvents(run.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	var sawApprovalReq, sawApprovalDone bool
	for _, ev := range events {
		switch ev.Kind {
		case EvAutoApproved:
			found = true
			if ev.Payload["class"] != GatePush {
				t.Fatalf("EvAutoApproved payload missing/wrong class: %+v", ev.Payload)
			}
			action, _ := ev.Payload["action"].(string)
			if !strings.Contains(action, "git push origin main") {
				t.Fatalf("EvAutoApproved payload missing the gated action: %+v", ev.Payload)
			}
		case EvApprovalReq:
			sawApprovalReq = true
		case EvApprovalDone:
			sawApprovalDone = true
		}
	}
	if !found {
		t.Fatal("expected an EvAutoApproved event in the run's event feed")
	}
	// The human-approval-specific events must NOT fire for an auto-approved action -- the two
	// audit trails stay disjoint (EvAutoApproved vs. EvApprovalReq/EvApprovalDone).
	if sawApprovalReq || sawApprovalDone {
		t.Fatalf("auto-approved action must not also emit human approval events (req=%v done=%v)", sawApprovalReq, sawApprovalDone)
	}
}

// TestAutoApproveIneligibleClassStillRequiresHumanApproval is the defense-in-depth check: even a
// hand-built Run whose Gates map was never validated by store.CreateRun (simulating a corrupted
// or hand-edited DB row) must NOT auto-execute a destructive-class action -- awaitApproval's own
// GateClassAutoApprovable re-check must catch it and fall through to creating a normal pending
// approval, exactly like the ordinary human-approval path.
func TestAutoApproveIneligibleClassStillRequiresHumanApproval(t *testing.T) {
	e := newEngine(t, &scriptedCaller{})
	// Deliberately bypasses Store.CreateRun's validation (which would reject this) to simulate a
	// corrupted/hand-edited persisted Gates map.
	run := &Run{
		ID:     "run_hand_built",
		Config: RunConfig{Gates: map[string]string{GateDestructive: PolicyAutoApprove}, ApprovalTimeoutSec: 1},
	}

	allowed, boundary := e.awaitApproval(context.Background(), run, GateRequest{
		Class: GateDestructive, Action: `run_command "rm -rf build"`, Args: map[string]any{"command": "rm -rf build"},
	})
	// No active run handle is registered for run_hand_built, so awaitApproval returns immediately
	// once it finds nowhere to deliver a decision (BoundaryStopSignal) -- but the important
	// assertion is what happened BEFORE that: a real pending approval row must have been created,
	// proving control fell through to the human-approval path rather than auto-executing.
	if allowed {
		t.Fatal("an ineligible gate class must never be auto-approved, even from a hand-built Run")
	}
	if boundary != BoundaryStopSignal {
		t.Fatalf("expected BoundaryStopSignal (no active handle registered), got %q", boundary)
	}
	pending, err := e.Store.PendingApprovals(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Class != GateDestructive {
		t.Fatalf("expected exactly one pending destructive approval, got %+v", pending)
	}

	// No EvAutoApproved should have been recorded for this run.
	events, err := e.Store.ListEvents(run.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Kind == EvAutoApproved {
			t.Fatalf("must not emit EvAutoApproved for an ineligible class, got %+v", ev)
		}
	}
}

// TestAutoApproveDenyStillWinsOverAutoApprove pins that PolicyDeny is checked first: a gate class
// cannot simultaneously carry two policies in this map (one string value per key), so this test
// is really about ordering discipline -- deny must short-circuit before the auto-approve branch
// is ever reached, exactly as it did before this feature existed.
func TestAutoApproveDenyStillWinsOverAutoApprove(t *testing.T) {
	e := newEngine(t, &scriptedCaller{})
	run := &Run{
		ID:     "run_deny_wins",
		Config: RunConfig{Gates: map[string]string{GatePush: PolicyDeny}, ApprovalTimeoutSec: 1},
	}
	allowed, boundary := e.awaitApproval(context.Background(), run, GateRequest{Class: GatePush, Action: "git push"})
	if allowed {
		t.Fatal("deny must win: push must not be allowed")
	}
	if boundary != "" {
		t.Fatalf("deny returns no boundary (soft stop within the unit), got %q", boundary)
	}
	pending, _ := e.Store.PendingApprovals(run.ID)
	if len(pending) != 0 {
		t.Fatalf("deny must not create a pending approval row, got %+v", pending)
	}
}
