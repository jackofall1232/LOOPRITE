package engine

// Regression tests for store.CreateRun's auto_approve gate-policy validation: push and pr_create
// are the only two classes GateClassAutoApprovable permits, so CreateRun must accept auto_approve
// on exactly those and reject it on every other class -- the safety invariant this feature must
// never regress (merge/destructive/deploy/credential_change/outside_repo can never be
// auto-approved, at any layer, including the very first one).

import "testing"

func TestCreateRunAcceptsAutoApproveOnEligibleClasses(t *testing.T) {
	s := newStore(t)
	for _, class := range []string{GatePush, GatePRCreate} {
		run, err := s.CreateRun("proj", "/r", RunConfig{Gates: map[string]string{class: PolicyAutoApprove}})
		if err != nil {
			t.Fatalf("CreateRun with %s=auto_approve should be accepted, got: %v", class, err)
		}
		if run.Config.Gates[class] != PolicyAutoApprove {
			t.Fatalf("gate %s did not round-trip as auto_approve: %+v", class, run.Config.Gates)
		}
	}
}

// TestCreateRunRejectsAutoApproveOnIneligibleClasses is the core safety assertion: every class
// other than push/pr_create must be refused at creation, not merely ignored at runtime, so an
// ineligible auto_approve entry can never even be persisted into a run's Config.
func TestCreateRunRejectsAutoApproveOnIneligibleClasses(t *testing.T) {
	s := newStore(t)
	ineligible := []string{GateMerge, GateDeploy, GateCredentialChange, GateDestructive, GateOutsideRepo}
	if len(ineligible) != 5 {
		t.Fatalf("expected exactly 5 ineligible classes, got %d: %v", len(ineligible), ineligible)
	}
	for _, class := range ineligible {
		if _, err := s.CreateRun("proj", "/r", RunConfig{Gates: map[string]string{class: PolicyAutoApprove}}); err == nil {
			t.Errorf("CreateRun with %s=auto_approve should be rejected, but succeeded", class)
		}
	}
}

// TestCreateRunStillRejectsUnknownPolicyAndClass pins that adding auto_approve did not loosen the
// pre-existing validation for a genuinely unknown policy string or gate class.
func TestCreateRunStillRejectsUnknownPolicyAndClass(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateRun("proj", "/r", RunConfig{Gates: map[string]string{GatePush: "auto_allow"}}); err == nil {
		t.Fatal("expected error for invalid gate policy")
	}
	if _, err := s.CreateRun("proj", "/r", RunConfig{Gates: map[string]string{"teleport": PolicyAutoApprove}}); err == nil {
		t.Fatal("expected error for unknown gate class even with an otherwise-valid policy")
	}
}

func TestGateClassAutoApprovableExactlyPushAndPRCreate(t *testing.T) {
	for _, c := range GateClasses {
		want := c == GatePush || c == GatePRCreate
		if got := GateClassAutoApprovable(c); got != want {
			t.Errorf("GateClassAutoApprovable(%s) = %v, want %v", c, got, want)
		}
	}
	if !containsStr(GateClasses, GatePRCreate) {
		t.Fatal("GateClasses must contain pr_create")
	}
}
