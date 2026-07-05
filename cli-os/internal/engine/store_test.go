package engine

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "engine.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{DB: db}
}

func intPtr(v int) *int { return &v }

// TestCreateRunDefaultsAndRoundTrip covers default application, JSON config round-trip through
// GetRun, and the config validation errors (unknown objective / gate class / gate policy).
func TestCreateRunDefaultsAndRoundTrip(t *testing.T) {
	s := newStore(t)

	// Defaults: empty objective -> balanced; zero iters -> 25; zero timeout -> 900; zero threshold -> 3.
	run, err := s.CreateRun("proj", "/repo/root", RunConfig{RepoID: "repoA", Goal: "build it"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.Status != StatusDraft {
		t.Fatalf("status = %q, want draft", run.Status)
	}
	if run.Config.Objective != ObjectiveBalanced {
		t.Fatalf("objective = %q, want balanced", run.Config.Objective)
	}
	if run.Config.MaxIterations != 25 || run.Config.ApprovalTimeoutSec != 900 || run.Config.NoProgressThreshold != 3 {
		t.Fatalf("defaults not applied: %+v", run.Config)
	}
	if run.Config.Gates == nil {
		t.Fatalf("gates should be non-nil empty map")
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil {
		t.Fatal("GetRun returned nil for an existing run")
	}
	if got.Project != "proj" || got.RepoRoot != "/repo/root" || got.Config.RepoID != "repoA" || got.Config.Goal != "build it" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Config.MaxIterations != 25 || got.Config.ApprovalTimeoutSec != 900 || got.Config.NoProgressThreshold != 3 {
		t.Fatalf("config round-trip lost defaults: %+v", got.Config)
	}
	if got.CreatedAt == "" {
		t.Fatal("created_at not set")
	}

	// Clamp: max iterations above 100 clamps to 100; gates + allowlist round-trip.
	run2, err := s.CreateRun("proj", "/r2", RunConfig{
		RepoID: "repoB", Objective: ObjectiveQuality, MaxIterations: 500,
		Gates:            map[string]string{GatePush: PolicyDeny, GateMerge: PolicyRequireApproval},
		CommandAllowlist: []string{"go test ./...", "go build ./..."},
	})
	if err != nil {
		t.Fatalf("CreateRun clamp: %v", err)
	}
	if run2.Config.MaxIterations != 100 {
		t.Fatalf("clamp: max iters = %d, want 100", run2.Config.MaxIterations)
	}
	got2, _ := s.GetRun(run2.ID)
	if got2.Config.Gates[GatePush] != PolicyDeny || got2.Config.Gates[GateMerge] != PolicyRequireApproval {
		t.Fatalf("gates round-trip mismatch: %+v", got2.Config.Gates)
	}
	if len(got2.Config.CommandAllowlist) != 2 || got2.Config.CommandAllowlist[0] != "go test ./..." {
		t.Fatalf("allowlist round-trip mismatch: %+v", got2.Config.CommandAllowlist)
	}

	// Validation errors.
	if _, err := s.CreateRun("proj", "/r", RunConfig{Objective: "wizardry"}); err == nil {
		t.Fatal("expected error for unknown objective")
	}
	if _, err := s.CreateRun("proj", "/r", RunConfig{Gates: map[string]string{"teleport": PolicyDeny}}); err == nil {
		t.Fatal("expected error for unknown gate class")
	}
	if _, err := s.CreateRun("proj", "/r", RunConfig{Gates: map[string]string{GatePush: "auto_allow"}}); err == nil {
		t.Fatal("expected error for invalid gate policy")
	}

	// Missing run -> (nil, nil).
	missing, err := s.GetRun("run_does_not_exist")
	if err != nil || missing != nil {
		t.Fatalf("GetRun(missing) = (%v, %v), want (nil, nil)", missing, err)
	}
}

// TestMarkStartedGuard verifies the ready+preflight atomic guard and single-use semantics.
func TestMarkStartedGuard(t *testing.T) {
	s := newStore(t)
	run, err := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoA", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}

	// From draft, without a preflight: rejected.
	if err := s.MarkStarted(run.ID, "tok_1", "l00prite/run-x"); !errors.Is(err, ErrBadState) {
		t.Fatalf("MarkStarted from draft = %v, want ErrBadState", err)
	}

	// Save a pre-flight -> ready, then Start succeeds.
	if err := s.SavePreflight(run.ID, `{"run_id":"x"}`, StatusReady); err != nil {
		t.Fatalf("SavePreflight: %v", err)
	}
	if err := s.MarkStarted(run.ID, "tok_1", "l00prite/run-x"); err != nil {
		t.Fatalf("MarkStarted after ready: %v", err)
	}
	got, _ := s.GetRun(run.ID)
	if got.Status != StatusRunning || got.ConfirmedBy != "tok_1" || got.Branch != "l00prite/run-x" {
		t.Fatalf("post-start row wrong: %+v", got)
	}
	if got.ConfirmedAt == "" || got.StartedAt == "" {
		t.Fatal("confirmed_at/started_at not set")
	}

	// Second Start (now running) is rejected.
	if err := s.MarkStarted(run.ID, "tok_2", "b"); !errors.Is(err, ErrBadState) {
		t.Fatalf("second MarkStarted = %v, want ErrBadState", err)
	}
	// SavePreflight refuses while running.
	if err := s.SavePreflight(run.ID, "{}", StatusReady); !errors.Is(err, ErrBadState) {
		t.Fatalf("SavePreflight while running = %v, want ErrBadState", err)
	}
	// MarkStarted on a missing run -> ErrNotFound.
	if err := s.MarkStarted("run_missing", "t", "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkStarted(missing) = %v, want ErrNotFound", err)
	}
}

// TestEventsAppendAndCursor covers monotonic seqs, payload round-trip, and afterSeq cursor paging.
func TestEventsAppendAndCursor(t *testing.T) {
	s := newStore(t)
	run, _ := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoA"})

	seq1, err := s.AppendEvent(run.ID, EvArmed, map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	seq2, _ := s.AppendEvent(run.ID, EvIteration, nil)
	seq3, _ := s.AppendEvent(run.ID, EvPersisted, map[string]any{"k": "v"})
	if !(seq1 < seq2 && seq2 < seq3) {
		t.Fatalf("seqs not increasing: %d %d %d", seq1, seq2, seq3)
	}

	all, err := s.ListEvents(run.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListEvents(0) len = %d, want 3", len(all))
	}
	if all[0].Kind != EvArmed || all[2].Kind != EvPersisted {
		t.Fatalf("ordering wrong: %+v", all)
	}
	if all[1].Payload == nil || len(all[1].Payload) != 0 {
		t.Fatalf("nil payload should round-trip as empty object, got %+v", all[1].Payload)
	}
	if all[2].Payload["k"] != "v" {
		t.Fatalf("payload round-trip mismatch: %+v", all[2].Payload)
	}

	// Cursor: after seq1 -> only the two later events.
	after, _ := s.ListEvents(run.ID, seq1, 200)
	if len(after) != 2 || after[0].Seq != seq2 {
		t.Fatalf("cursor after seq1 = %+v, want [seq2, seq3]", after)
	}
}

// TestDecideApprovalCAS verifies the compare-and-set: the first decision wins and is preserved.
func TestDecideApprovalCAS(t *testing.T) {
	s := newStore(t)
	run, _ := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoA"})

	appr, err := s.CreateApproval(run.ID, GateDestructive, `run_command: "rm -rf build"`, map[string]any{"cmd": "rm -rf build"})
	if err != nil {
		t.Fatal(err)
	}
	if appr.Status != ApprovalPending {
		t.Fatalf("new approval status = %q, want pending", appr.Status)
	}

	first, err := s.DecideApproval(appr.ID, "allow", "alice", "looks fine")
	if err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if first.Status != ApprovalAllowed || first.DecidedBy != "alice" || first.DecidedAt == "" {
		t.Fatalf("first decision wrong: %+v", first)
	}

	// Second decision must not overwrite; returns the preserved first decision + ErrAlreadyDecided.
	second, err := s.DecideApproval(appr.ID, "deny", "bob", "no")
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second decide err = %v, want ErrAlreadyDecided", err)
	}
	if second == nil || second.Status != ApprovalAllowed || second.DecidedBy != "alice" {
		t.Fatalf("first decision not preserved: %+v", second)
	}

	// Invalid decision string.
	if _, err := s.DecideApproval(appr.ID, "maybe", "x", ""); !errors.Is(err, ErrBadState) {
		t.Fatalf("invalid decision = %v, want ErrBadState", err)
	}
	// Missing approval.
	if _, err := s.DecideApproval("appr_missing", "allow", "x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("decide(missing) = %v, want ErrNotFound", err)
	}

	// A second pending approval should still surface via PendingApprovals; the decided one should not.
	if _, err := s.CreateApproval(run.ID, GatePush, "git push", nil); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingApprovals(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Class != GatePush {
		t.Fatalf("PendingApprovals = %+v, want only the push gate", pending)
	}
}

// TestReconcileOrphans flips running/waiting_approval rows to interrupted and leaves others alone.
func TestReconcileOrphans(t *testing.T) {
	s := newStore(t)

	// One running, one waiting_approval, one left in draft.
	running, _ := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoA"})
	_ = s.SavePreflight(running.ID, `{"x":1}`, StatusReady)
	_ = s.MarkStarted(running.ID, "t", "b")

	waiting, _ := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoB"})
	_ = s.SavePreflight(waiting.ID, `{"x":1}`, StatusReady)
	_ = s.MarkStarted(waiting.ID, "t", "b")
	_ = s.SetStatus(waiting.ID, StatusWaitingApproval)

	draft, _ := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoC"})

	n, err := s.ReconcileOrphans()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ReconcileOrphans count = %d, want 2", n)
	}
	for _, id := range []string{running.ID, waiting.ID} {
		got, _ := s.GetRun(id)
		if got.Status != StatusInterrupted {
			t.Fatalf("run %s status = %q, want interrupted", id, got.Status)
		}
	}
	if got, _ := s.GetRun(draft.ID); got.Status != StatusDraft {
		t.Fatalf("draft run should be untouched, got %q", got.Status)
	}
}

// TestActiveRunForRepo returns only a running/waiting run for the (project, repo) pair.
func TestActiveRunForRepo(t *testing.T) {
	s := newStore(t)

	run, _ := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoA"})
	// Draft is not active.
	if got, _ := s.ActiveRunForRepo("proj", "repoA"); got != nil {
		t.Fatalf("draft counted as active: %+v", got)
	}
	_ = s.SavePreflight(run.ID, `{"x":1}`, StatusReady)
	_ = s.MarkStarted(run.ID, "t", "b")

	got, err := s.ActiveRunForRepo("proj", "repoA")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != run.ID {
		t.Fatalf("ActiveRunForRepo = %+v, want run %s", got, run.ID)
	}
	// Different repo -> nil.
	if other, _ := s.ActiveRunForRepo("proj", "repoZ"); other != nil {
		t.Fatalf("wrong repo returned a run: %+v", other)
	}

	// Finishing the run clears it from active.
	if err := s.FinishRun(run.ID, StatusDone, BoundaryDone, "all green"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ActiveRunForRepo("proj", "repoA"); got != nil {
		t.Fatalf("finished run still active: %+v", got)
	}
	fin, _ := s.GetRun(run.ID)
	if fin.Status != StatusDone || fin.Boundary != BoundaryDone || fin.Summary != "all green" || fin.EndedAt == "" {
		t.Fatalf("FinishRun row wrong: %+v", fin)
	}
}

// TestSaveIterationAccumulatesCost verifies counters update and cost accumulates in one place.
func TestSaveIterationAccumulatesCost(t *testing.T) {
	s := newStore(t)
	run, _ := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoA"})

	if err := s.SaveIteration(run.ID, 1, 0, nil, 1.5); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIteration(run.ID, 2, 1, intPtr(2), 2.25); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRun(run.ID)
	if got.CurrentIteration != 2 || got.IterationsSinceProgress != 1 {
		t.Fatalf("counters wrong: iter=%d isp=%d", got.CurrentIteration, got.IterationsSinceProgress)
	}
	if got.LastProgressIteration == nil || *got.LastProgressIteration != 2 {
		t.Fatalf("last_progress_iteration = %v, want 2", got.LastProgressIteration)
	}
	if diff := got.CostUSD - 3.75; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost_usd = %v, want 3.75", got.CostUSD)
	}
	// nil last-progress pointer persists as SQL NULL and scans back to nil.
	if err := s.SaveIteration(run.ID, 3, 2, nil, 0); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetRun(run.ID)
	if got2.LastProgressIteration != nil {
		t.Fatalf("last_progress_iteration should be nil, got %v", *got2.LastProgressIteration)
	}
	if diff := got2.CostUSD - 3.75; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost after zero add = %v, want 3.75", got2.CostUSD)
	}
}

// TestExpireApproval covers the pending->expired CAS and its sentinels.
func TestExpireApproval(t *testing.T) {
	s := newStore(t)
	run, _ := s.CreateRun("proj", "/r", RunConfig{RepoID: "repoA"})
	appr, _ := s.CreateApproval(run.ID, GatePush, "git push", nil)

	if err := s.ExpireApproval(appr.ID); err != nil {
		t.Fatalf("ExpireApproval: %v", err)
	}
	got, _ := s.GetApproval(appr.ID)
	if got.Status != ApprovalExpired {
		t.Fatalf("status = %q, want expired", got.Status)
	}
	// Re-expiring an already-decided row.
	if err := s.ExpireApproval(appr.ID); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("re-expire = %v, want ErrAlreadyDecided", err)
	}
	if err := s.ExpireApproval("appr_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expire(missing) = %v, want ErrNotFound", err)
	}
}
