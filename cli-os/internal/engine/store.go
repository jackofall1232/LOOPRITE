package engine

// store.go is the engine's SQLite persistence: runs, the append-only run_events feed, and
// run_approvals (cli-os/docs/os-architecture.md §2.1). Every mutation goes through state.Tx
// (BEGIN IMMEDIATE on the pool's single pinned connection), every timestamp comes only from
// util.NowISO(), and every id from util.RID — the same discipline the PEP relies on. This row
// store is the operational/queryable record; the target repo's .l00prite/ files remain the
// protocol-authoritative one.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// Exported sentinels so the API layer can map store failures to HTTP codes (404 / 409).
var (
	// ErrNotFound is returned by a mutation whose target run or approval does not exist.
	ErrNotFound = errors.New("engine: run or approval not found")
	// ErrBadState is returned when an operation is not allowed from the current status
	// (e.g. Start from a non-ready run, or a preflight save while running).
	ErrBadState = errors.New("engine: operation not allowed in current state")
	// ErrAlreadyDecided is returned by the approval compare-and-set when the row was already
	// decided by another actor; the caller receives the pre-existing decision unchanged.
	ErrAlreadyDecided = errors.New("engine: approval already decided")
	// ErrCheckpointFailed is returned by StartRun when the pre-run auto-checkpoint commit (the
	// run's only unprompted state mutation) fails. Start must not proceed to checkout when this
	// happens — nothing about the repository or the lock is left armed.
	ErrCheckpointFailed = errors.New("engine: pre-run auto-checkpoint failed")
	// ErrCheckpointRefused is returned by AutoCheckpoint (and, wrapping it, StartRun) when a dirty
	// path outside .l00prite/ matches the project's own Autonomous-Edit Denylist or looks like it
	// may contain credentials: the auto-checkpoint is the run's ONLY unprompted state mutation, and
	// it must not become a second, ungoverned bypass of the one piece of policy the rest of this
	// design treats as load-bearing (the same protection the autonomous coder's write_file goes
	// through — see Toolbox.writeFile). Distinct from ErrCheckpointFailed (a mechanical failure)
	// so the human-facing message can say "this needs your review," not "something broke."
	ErrCheckpointRefused = errors.New("engine: pre-run auto-checkpoint refused a sensitive file")
)

// Store persists runs, run events, and approvals over the shared state DB.
type Store struct{ DB *sql.DB }

// runCols is the full row projection (preflight JSON included). runColsList substitutes an empty
// literal for the preflight column so list rows stay small; both keep the same column order so a
// single scanRun handles either.
const runCols = `id, project, repo, repo_root, goal, objective, gates, command_allowlist,
	max_iterations, approval_timeout_s, no_progress_threshold, max_tool_calls, status, boundary,
	current_iteration, iterations_since_progress, last_progress_iteration, branch,
	preflight, preflight_at, confirmed_by, confirmed_at, cost_usd, created_at, started_at,
	ended_at, summary`

const runColsList = `id, project, repo, repo_root, goal, objective, gates, command_allowlist,
	max_iterations, approval_timeout_s, no_progress_threshold, max_tool_calls, status, boundary,
	current_iteration, iterations_since_progress, last_progress_iteration, branch,
	'' AS preflight, preflight_at, confirmed_by, confirmed_at, cost_usd, created_at, started_at,
	ended_at, summary`

const approvalCols = `id, run_id, class, action, args, status, decided_by, note, created_at, decided_at`

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// ---- runs ----

// CreateRun inserts a draft run, applying the protocol defaults and validating the config. It
// errors (before any write) on an unknown objective, an unknown gate class, or a gate policy that
// is not require_approval/deny.
func (s *Store) CreateRun(project, repoRoot string, cfg RunConfig) (*Run, error) {
	// Objective: "" -> balanced; anything else must be a known objective.
	if cfg.Objective == "" {
		cfg.Objective = ObjectiveBalanced
	}
	if !containsStr(Objectives, cfg.Objective) {
		return nil, fmt.Errorf("engine: unknown objective %q", cfg.Objective)
	}
	// Iteration budget: default 25, clamped to 1..100.
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 25
	}
	if cfg.MaxIterations < 1 {
		cfg.MaxIterations = 1
	}
	if cfg.MaxIterations > 100 {
		cfg.MaxIterations = 100
	}
	// Per-unit coder tool-call budget: default 40, clamped 1..200. Same human-set-then-frozen shape
	// as MaxIterations (the self-modification guard). 0 -> the protocol default here; the engine's
	// own Engine.MaxToolCalls fallback (exec.go) only ever applies to legacy pre-migration rows that
	// were persisted before this column existed and therefore read back as 0.
	if cfg.MaxToolCalls <= 0 {
		cfg.MaxToolCalls = 40
	}
	if cfg.MaxToolCalls > 200 {
		cfg.MaxToolCalls = 200
	}
	if cfg.ApprovalTimeoutSec <= 0 {
		cfg.ApprovalTimeoutSec = 900
	}
	if cfg.NoProgressThreshold <= 0 {
		cfg.NoProgressThreshold = 3
	}
	// Gates: nil -> empty map; absent classes read as require_approval implicitly (stored as given).
	// Provided entries must name a known class and a valid policy (fail-closed vocabulary).
	if cfg.Gates == nil {
		cfg.Gates = map[string]string{}
	}
	for class, policy := range cfg.Gates {
		if !containsStr(GateClasses, class) {
			return nil, fmt.Errorf("engine: unknown gate class %q", class)
		}
		switch policy {
		case PolicyRequireApproval, PolicyDeny:
			// always valid
		case PolicyAutoApprove:
			// auto_approve is structurally restricted to push/pr_create (GateClassAutoApprovable)
			// -- rejecting it here at creation, not just ignoring it at runtime, means a run's
			// persisted pre-flight display can never show an auto_approve entry for a class that
			// was never eligible to have one.
			if !GateClassAutoApprovable(class) {
				return nil, fmt.Errorf("engine: gate %q may not use auto_approve (only push and pr_create are eligible)", class)
			}
		default:
			return nil, fmt.Errorf("engine: gate %q has invalid policy %q (want require_approval|deny|auto_approve)", class, policy)
		}
	}

	gatesJSON, err := encodeJSON(cfg.Gates)
	if err != nil {
		return nil, err
	}
	if cfg.CommandAllowlist == nil {
		cfg.CommandAllowlist = []string{}
	}
	allowlistJSON, err := encodeJSON(cfg.CommandAllowlist)
	if err != nil {
		return nil, err
	}

	id := util.RID("run")
	now := util.NowISO()
	_, err = state.Tx(s.DB, func(q state.Querier) (struct{}, error) {
		_, e := q.ExecContext(state.Ctx(), `INSERT INTO runs
			(id, project, repo, repo_root, goal, objective, gates, command_allowlist,
			 max_iterations, approval_timeout_s, no_progress_threshold, max_tool_calls, status, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, project, cfg.RepoID, repoRoot, cfg.Goal, cfg.Objective, gatesJSON, allowlistJSON,
			cfg.MaxIterations, cfg.ApprovalTimeoutSec, cfg.NoProgressThreshold, cfg.MaxToolCalls, StatusDraft, now)
		return struct{}{}, e
	})
	if err != nil {
		return nil, err
	}

	return &Run{
		ID:        id,
		Project:   project,
		RepoRoot:  repoRoot,
		Config:    cfg,
		Status:    StatusDraft,
		CreatedAt: now,
	}, nil
}

// GetRun returns the full run (config, preflight JSON, all counters). A missing id is (nil, nil).
func (s *Store) GetRun(id string) (*Run, error) {
	row := s.DB.QueryRowContext(state.Ctx(), `SELECT `+runCols+` FROM runs WHERE id = ?`, id)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// ListRuns returns a project's runs newest-first. limit defaults to and is capped at 50; the
// preflight JSON is omitted (PreflightJSON stays empty) to keep list payloads small.
func (s *Store) ListRuns(project string, limit int) ([]*Run, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(state.Ctx(),
		`SELECT `+runColsList+` FROM runs WHERE project = ? ORDER BY created_at DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SavePreflight records a freshly built pre-flight and moves the run to the caller-decided status
// (StatusReady when there are no blockers, StatusBlocked otherwise). It refuses to run while the
// run is running or waiting_approval — a live run's pre-flight is immutable.
func (s *Store) SavePreflight(id, preflightJSON, newStatus string) error {
	if newStatus != StatusReady && newStatus != StatusBlocked {
		return fmt.Errorf("engine: SavePreflight newStatus must be ready or blocked, got %q: %w", newStatus, ErrBadState)
	}
	now := util.NowISO()
	var sentinel error
	_, err := state.Tx(s.DB, func(q state.Querier) (struct{}, error) {
		var cur string
		e := q.QueryRowContext(state.Ctx(), `SELECT status FROM runs WHERE id = ?`, id).Scan(&cur)
		if e == sql.ErrNoRows {
			sentinel = ErrNotFound
			return struct{}{}, nil
		}
		if e != nil {
			return struct{}{}, e
		}
		if cur == StatusRunning || cur == StatusWaitingApproval {
			sentinel = ErrBadState
			return struct{}{}, nil
		}
		_, e = q.ExecContext(state.Ctx(),
			`UPDATE runs SET preflight = ?, preflight_at = ?, status = ? WHERE id = ?`,
			preflightJSON, now, newStatus, id)
		return struct{}{}, e
	})
	if err != nil {
		return err
	}
	return sentinel
}

// MarkStarted is the protocol's explicit in-session confirmation: it moves a run ready -> running
// only when a fresh pre-flight exists, atomically, and resets the iteration/no-progress counters.
// It returns ErrBadState (so the API can 409) when the guard fails, ErrNotFound when the run is
// absent — nothing else may effect this transition.
func (s *Store) MarkStarted(id, confirmedBy, branch string) error {
	now := util.NowISO()
	var sentinel error
	_, err := state.Tx(s.DB, func(q state.Querier) (struct{}, error) {
		res, e := q.ExecContext(state.Ctx(), `UPDATE runs SET
			status = ?, confirmed_by = ?, confirmed_at = ?, started_at = ?, branch = ?,
			current_iteration = 0, iterations_since_progress = 0,
			last_progress_iteration = NULL, boundary = NULL, summary = NULL
			WHERE id = ? AND status = ? AND preflight IS NOT NULL AND preflight <> ''`,
			StatusRunning, confirmedBy, now, now, branch, id, StatusReady)
		if e != nil {
			return struct{}{}, e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if runExists(q, id) {
				sentinel = ErrBadState
			} else {
				sentinel = ErrNotFound
			}
		}
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	return sentinel
}

// SetStatus updates only the status (ended_at untouched) — e.g. running <-> waiting_approval.
func (s *Store) SetStatus(id, status string) error {
	return s.mutateOne(`UPDATE runs SET status = ? WHERE id = ?`, status, id)
}

// FinishRun records a terminal status (done|stopped) with its boundary, summary, and end time.
func (s *Store) FinishRun(id, status, boundary, summary string) error {
	return s.mutateOne(`UPDATE runs SET status = ?, boundary = ?, summary = ?, ended_at = ? WHERE id = ?`,
		status, boundary, summary, util.NowISO(), id)
}

// SaveIteration persists an iteration's counters and adds addCost to the running cost in one tx.
func (s *Store) SaveIteration(id string, iter, isp int, lpi *int, addCost float64) error {
	return s.mutateOne(`UPDATE runs SET
		current_iteration = ?, iterations_since_progress = ?, last_progress_iteration = ?,
		cost_usd = cost_usd + ? WHERE id = ?`,
		iter, isp, intPtrArg(lpi), addCost, id)
}

// ReconcileOrphans runs at boot: any run left running or waiting_approval belongs to a process
// that died mid-run, so it becomes interrupted. Returns how many rows were flipped. The repo-side
// stale-run recovery (disarm + reclaim the lease) happens at the next pre-flight per
// execute-loop.md step 3 — this only reconciles the engine's own operational record.
func (s *Store) ReconcileOrphans() (int, error) {
	return state.Tx(s.DB, func(q state.Querier) (int, error) {
		res, err := q.ExecContext(state.Ctx(),
			`UPDATE runs SET status = ? WHERE status IN (?, ?)`,
			StatusInterrupted, StatusRunning, StatusWaitingApproval)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return int(n), nil
	})
}

// ActiveRunForRepo returns the most recent run for (project, repo) that is running or
// waiting_approval, or (nil, nil) if none — the one-active-run-per-repo guard.
func (s *Store) ActiveRunForRepo(project, repo string) (*Run, error) {
	row := s.DB.QueryRowContext(state.Ctx(),
		`SELECT `+runCols+` FROM runs WHERE project = ? AND repo = ? AND status IN (?, ?)
		 ORDER BY created_at DESC LIMIT 1`,
		project, repo, StatusRunning, StatusWaitingApproval)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// mutateOne runs a single-row UPDATE inside a tx and reports ErrNotFound when it matches no row.
func (s *Store) mutateOne(query string, args ...any) error {
	_, err := state.Tx(s.DB, func(q state.Querier) (struct{}, error) {
		res, e := q.ExecContext(state.Ctx(), query, args...)
		if e != nil {
			return struct{}{}, e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	return err
}

// scanRun reads one runCols/runColsList row into a Run, decoding the JSON config fields and the
// nullable columns.
func scanRun(sc rowScanner) (*Run, error) {
	var (
		r                           Run
		repoID, gatesJSON, alJSON   string
		boundary, branch            sql.NullString
		preflight, preflightAt      sql.NullString
		confirmedBy, confirmedAt    sql.NullString
		startedAt, endedAt, summary sql.NullString
		lastProg                    sql.NullInt64
	)
	if err := sc.Scan(
		&r.ID, &r.Project, &repoID, &r.RepoRoot, &r.Config.Goal, &r.Config.Objective,
		&gatesJSON, &alJSON,
		&r.Config.MaxIterations, &r.Config.ApprovalTimeoutSec, &r.Config.NoProgressThreshold,
		&r.Config.MaxToolCalls,
		&r.Status, &boundary,
		&r.CurrentIteration, &r.IterationsSinceProgress, &lastProg, &branch,
		&preflight, &preflightAt, &confirmedBy, &confirmedAt, &r.CostUSD,
		&r.CreatedAt, &startedAt, &endedAt, &summary,
	); err != nil {
		return nil, err
	}
	r.Config.RepoID = repoID
	if gatesJSON != "" {
		if err := json.Unmarshal([]byte(gatesJSON), &r.Config.Gates); err != nil {
			return nil, err
		}
	}
	if alJSON != "" {
		if err := json.Unmarshal([]byte(alJSON), &r.Config.CommandAllowlist); err != nil {
			return nil, err
		}
	}
	r.Boundary = boundary.String
	r.Branch = branch.String
	r.PreflightJSON = preflight.String
	r.PreflightAt = preflightAt.String
	r.ConfirmedBy = confirmedBy.String
	r.ConfirmedAt = confirmedAt.String
	r.StartedAt = startedAt.String
	r.EndedAt = endedAt.String
	r.Summary = summary.String
	if lastProg.Valid {
		v := int(lastProg.Int64)
		r.LastProgressIteration = &v
	}
	return &r, nil
}

// runExists reports whether a run id is present (used to split ErrNotFound from ErrBadState).
func runExists(q state.Querier, id string) bool {
	var one int
	return q.QueryRowContext(state.Ctx(), `SELECT 1 FROM runs WHERE id = ?`, id).Scan(&one) == nil
}

// ---- events (append-only feed) ----

// AppendEvent appends one event to the run feed and returns its assigned monotonic seq (via
// RETURNING, which modernc.org/sqlite supports). A nil payload is stored as {}.
func (s *Store) AppendEvent(runID, kind string, payload map[string]any) (int64, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, err := encodeJSON(payload)
	if err != nil {
		return 0, err
	}
	ts := util.NowISO()
	return state.Tx(s.DB, func(q state.Querier) (int64, error) {
		var seq int64
		e := q.QueryRowContext(state.Ctx(),
			`INSERT INTO run_events (run_id, ts, kind, payload) VALUES (?,?,?,?) RETURNING seq`,
			runID, ts, kind, payloadJSON).Scan(&seq)
		return seq, e
	})
}

// ListEvents returns the run's events with seq > afterSeq in ascending seq order (cursor paging).
// limit defaults to 200 and is capped at 500.
func (s *Store) ListEvents(runID string, afterSeq int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.DB.QueryContext(state.Ctx(),
		`SELECT seq, run_id, ts, kind, payload FROM run_events
		 WHERE run_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			ev      Event
			payload string
		)
		if err := rows.Scan(&ev.Seq, &ev.RunID, &ev.TS, &ev.Kind, &payload); err != nil {
			return nil, err
		}
		if payload != "" {
			if err := json.Unmarshal([]byte(payload), &ev.Payload); err != nil {
				return nil, err
			}
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ---- approvals ----

// CreateApproval records a new pending per-action permission request. A nil args map is stored {}.
func (s *Store) CreateApproval(runID, class, action string, args map[string]any) (*Approval, error) {
	if args == nil {
		args = map[string]any{}
	}
	argsJSON, err := encodeJSON(args)
	if err != nil {
		return nil, err
	}
	id := util.RID("appr")
	now := util.NowISO()
	_, err = state.Tx(s.DB, func(q state.Querier) (struct{}, error) {
		_, e := q.ExecContext(state.Ctx(),
			`INSERT INTO run_approvals (id, run_id, class, action, args, status, created_at)
			 VALUES (?,?,?,?,?,?,?)`,
			id, runID, class, action, argsJSON, ApprovalPending, now)
		return struct{}{}, e
	})
	if err != nil {
		return nil, err
	}
	return &Approval{
		ID:        id,
		RunID:     runID,
		Class:     class,
		Action:    action,
		Args:      args,
		Status:    ApprovalPending,
		CreatedAt: now,
	}, nil
}

// GetApproval returns one approval, or (nil, nil) if absent.
func (s *Store) GetApproval(id string) (*Approval, error) {
	row := s.DB.QueryRowContext(state.Ctx(), `SELECT `+approvalCols+` FROM run_approvals WHERE id = ?`, id)
	a, err := scanApproval(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

// PendingApprovals returns a run's still-pending approvals, oldest first.
func (s *Store) PendingApprovals(runID string) ([]Approval, error) {
	rows, err := s.DB.QueryContext(state.Ctx(),
		`SELECT `+approvalCols+` FROM run_approvals WHERE run_id = ? AND status = ? ORDER BY created_at ASC`,
		runID, ApprovalPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// DecideApproval records a human decision ("allow"->allowed, "deny"->denied) with a compare-and-set
// on the pending status. If the row was already decided, the FIRST decision is preserved and the
// existing row is returned together with ErrAlreadyDecided; a missing row returns ErrNotFound.
func (s *Store) DecideApproval(id, decision, decidedBy, note string) (*Approval, error) {
	var status string
	switch decision {
	case "allow":
		status = ApprovalAllowed
	case "deny":
		status = ApprovalDenied
	default:
		return nil, fmt.Errorf("engine: invalid approval decision %q (want allow|deny): %w", decision, ErrBadState)
	}
	now := util.NowISO()
	var sentinel error
	appr, err := state.Tx(s.DB, func(q state.Querier) (*Approval, error) {
		res, e := q.ExecContext(state.Ctx(),
			`UPDATE run_approvals SET status = ?, decided_by = ?, note = ?, decided_at = ?
			 WHERE id = ? AND status = ?`,
			status, decidedBy, note, now, id, ApprovalPending)
		if e != nil {
			return nil, e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			cur, ge := approvalByID(q, id)
			if ge == sql.ErrNoRows {
				sentinel = ErrNotFound
				return nil, nil
			}
			if ge != nil {
				return nil, ge
			}
			sentinel = ErrAlreadyDecided
			return cur, nil
		}
		return approvalByID(q, id)
	})
	if err != nil {
		return nil, err
	}
	return appr, sentinel
}

// ExpireApproval moves a pending approval to expired (fail-closed on approval timeout), using the
// same compare-and-set. An already-decided row returns ErrAlreadyDecided; a missing one ErrNotFound.
func (s *Store) ExpireApproval(id string) error {
	now := util.NowISO()
	var sentinel error
	_, err := state.Tx(s.DB, func(q state.Querier) (struct{}, error) {
		res, e := q.ExecContext(state.Ctx(),
			`UPDATE run_approvals SET status = ?, decided_at = ? WHERE id = ? AND status = ?`,
			ApprovalExpired, now, id, ApprovalPending)
		if e != nil {
			return struct{}{}, e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if runApprovalExists(q, id) {
				sentinel = ErrAlreadyDecided
			} else {
				sentinel = ErrNotFound
			}
		}
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	return sentinel
}

// approvalByID reads one approval through a Querier (used inside the decision tx).
func approvalByID(q state.Querier, id string) (*Approval, error) {
	row := q.QueryRowContext(state.Ctx(), `SELECT `+approvalCols+` FROM run_approvals WHERE id = ?`, id)
	return scanApproval(row)
}

// runApprovalExists reports whether an approval id is present.
func runApprovalExists(q state.Querier, id string) bool {
	var one int
	return q.QueryRowContext(state.Ctx(), `SELECT 1 FROM run_approvals WHERE id = ?`, id).Scan(&one) == nil
}

// scanApproval reads one approvalCols row into an Approval, decoding args and nullable columns.
func scanApproval(sc rowScanner) (*Approval, error) {
	var (
		a                          Approval
		argsJSON                   string
		decidedBy, note, decidedAt sql.NullString
	)
	if err := sc.Scan(
		&a.ID, &a.RunID, &a.Class, &a.Action, &argsJSON, &a.Status,
		&decidedBy, &note, &a.CreatedAt, &decidedAt,
	); err != nil {
		return nil, err
	}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &a.Args); err != nil {
			return nil, err
		}
	}
	a.DecidedBy = decidedBy.String
	a.Note = note.String
	a.DecidedAt = decidedAt.String
	return &a, nil
}

// ---- small helpers ----

// containsStr reports whether list contains s.
func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// encodeJSON marshals v to a JSON string.
func encodeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// intPtrArg turns a *int into a nullable SQL argument (nil pointer -> SQL NULL).
func intPtrArg(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
