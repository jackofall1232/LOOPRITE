// Package engine is the L00prite OS run engine: the runtime that mechanically embodies the
// l00prite Execution Mode protocol (templates/l00prite/prompts/execute-loop.md) — pre-flight
// gate, Start-as-in-session-confirmation, one-unit iterations, the nine run boundaries,
// Autonomous-Edit Denylist enforcement, per-action approval gates, and dual persistence
// (engine SQLite + the target repo's .l00prite/ files). Design: cli-os/docs/os-architecture.md.
//
// The engine deliberately does NOT import the gateway. Every model call goes through the
// ModelCaller seam (implemented by the gateway over its runTurn primitive), so routing, PEP
// budget reservation, metering, and request-ledgering apply to autonomous work with no bypass
// path — and the engine stays vendor-neutral by construction: it names roles and objectives,
// never providers.
package engine

import "context"

// ---- run lifecycle ----

// Run statuses. Transitions: draft -> ready -> running <-> waiting_approval -> done|stopped;
// blocked (foreign lock at pre-flight) and interrupted (crash, recovered at next pre-flight)
// are re-enterable via a fresh pre-flight. Nothing moves ready -> running except StartRun
// with a fresh pre-flight (the protocol's explicit in-session confirmation).
const (
	StatusDraft           = "draft"
	StatusReady           = "ready"
	StatusBlocked         = "blocked"
	StatusRunning         = "running"
	StatusWaitingApproval = "waiting_approval"
	StatusDone            = "done"
	StatusStopped         = "stopped"
	StatusInterrupted     = "interrupted"
)

// The nine run boundaries, verbatim from execute-loop.md (ids are protocol vocabulary — the
// list is run_boundaries, never "stop_conditions", which is a different heartbeat field).
const (
	BoundaryDone           = "definition_of_done_met"
	BoundaryIterationLimit = "iteration_limit_reached"
	BoundaryHumanReview    = "human_review_gate"
	BoundaryDestructive    = "destructive_operation_required"
	BoundaryAmbiguous      = "ambiguous_requirements"
	BoundaryUnfixableTests = "unfixable_failing_tests"
	BoundaryMissingSecrets = "missing_secrets_or_credentials"
	BoundaryLockConflict   = "lock_lease_conflict"
	BoundaryStopSignal     = "stop_signal"
)

// RunBoundaries is the ordered protocol list (mirrored into heartbeat.json when arming).
var RunBoundaries = []string{
	BoundaryDone, BoundaryIterationLimit, BoundaryHumanReview, BoundaryDestructive,
	BoundaryAmbiguous, BoundaryUnfixableTests, BoundaryMissingSecrets, BoundaryLockConflict,
	BoundaryStopSignal,
}

// ---- approval gates ----

// Gate classes: the per-action-permission surface of execute-loop.md, as configurable gates.
const (
	GatePush             = "push"
	GateMerge            = "merge"
	GateDeploy           = "deploy"
	GateCredentialChange = "credential_change"
	GateDestructive      = "destructive" // denylist hit, non-allowlisted command, dependency install, history rewrite
	GateOutsideRepo      = "outside_repo"
)

// GateClasses in display order.
var GateClasses = []string{GatePush, GateMerge, GateDeploy, GateCredentialChange, GateDestructive, GateOutsideRepo}

// Gate policies. There is deliberately NO "auto_allow": the strongest available grant is a
// per-action human approval. Fail-closed: an unknown/absent policy reads as require_approval,
// and an approval that times out is a deny.
const (
	PolicyRequireApproval = "require_approval"
	PolicyDeny            = "deny"
)

// Approval statuses.
const (
	ApprovalPending = "pending"
	ApprovalAllowed = "allowed"
	ApprovalDenied  = "denied"
	ApprovalExpired = "expired"
)

// ---- objectives and roles ----

// Objectives: the user states an outcome preference; the engine assembles the team. Each
// resolves roles to auto-routing profiles (deterministic, explainable — never ML).
const (
	ObjectiveBalanced = "balanced"
	ObjectiveQuality  = "quality"
	ObjectiveCost     = "cost"
	ObjectiveSpeed    = "speed"
	ObjectivePrivacy  = "privacy"
)

// Objectives in display order.
var Objectives = []string{ObjectiveBalanced, ObjectiveQuality, ObjectiveCost, ObjectiveSpeed, ObjectivePrivacy}

// Roles the engine staffs per run.
const (
	RolePlan      = "plan"
	RoleCode      = "code"
	RoleReview    = "review"
	RoleSummarize = "summarize"
)

// TeamMember is one resolved role assignment, shown in the pre-flight display so the human
// confirms WHO will do the work, not just what.
type TeamMember struct {
	Role     string `json:"role"`
	Profile  string `json:"profile"`  // the auto-routing profile the role resolves through
	Provider string `json:"provider"` // resolved at pre-flight time (may change on resume — that is the point)
	Model    string `json:"model"`
	Reason   string `json:"reason"` // the router's own explanation line
}

// ---- run configuration ----

// RunConfig is everything the human sets at creation and confirms at pre-flight. Immutable
// for the life of a run (the self-modification guard): no code path may raise MaxIterations,
// loosen Gates, or extend CommandAllowlist after Start.
type RunConfig struct {
	RepoID    string `json:"repo"`
	Goal      string `json:"goal"`
	Objective string `json:"objective"` // one of Objectives; "" -> balanced
	// Gates maps gate class -> policy. Missing classes read as require_approval (fail-closed).
	Gates map[string]string `json:"gates"`
	// CommandAllowlist: the only commands run_command may execute without a destructive-gate
	// approval. Prefix-matched on whole tokens (see tools.go). Shown verbatim at pre-flight.
	CommandAllowlist []string `json:"command_allowlist"`
	// MaxIterations is this run's iteration budget (protocol default 25, clamped 1..100).
	MaxIterations int `json:"max_iterations"`
	// ApprovalTimeoutSec: how long a pending approval may wait before it expires and the run
	// fail-closes (deny + boundary stop). Default 900.
	ApprovalTimeoutSec int `json:"approval_timeout_s"`
	// NoProgressThreshold mirrors heartbeat execution.no_progress_threshold (default 3).
	NoProgressThreshold int `json:"no_progress_threshold"`
}

// Run is the persisted aggregate (engine store row). The target repo's .l00prite/ files are
// the protocol-authoritative record; this row is the operational/queryable one.
type Run struct {
	ID       string    `json:"id"`
	Project  string    `json:"project"`
	RepoRoot string    `json:"repo_root"`
	Config   RunConfig `json:"config"`

	Status   string `json:"status"`
	Boundary string `json:"boundary,omitempty"` // set when Status is done/stopped

	CurrentIteration        int  `json:"current_iteration"`
	IterationsSinceProgress int  `json:"iterations_since_progress"`
	LastProgressIteration   *int `json:"last_progress_iteration"`

	Branch      string  `json:"branch,omitempty"` // l00prite/run-<id suffix>
	ConfirmedBy string  `json:"confirmed_by,omitempty"`
	ConfirmedAt string  `json:"confirmed_at,omitempty"`
	CostUSD     float64 `json:"cost_usd"`

	CreatedAt string `json:"created_at"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	Summary   string `json:"summary,omitempty"`

	// PreflightJSON is the last built pre-flight display (serialized Preflight), kept so
	// start can verify it confirmed a FRESH pre-flight, and the UI can re-render it.
	PreflightJSON string `json:"-"`
	PreflightAt   string `json:"preflight_at,omitempty"`

	// ---- in-memory loop state (not persisted; rebuilt each run) ----
	lastOutcome      string              // fed to the next planner turn
	lastVerification *VerificationRecord // most recent verify result, for the ledger
	unitFailures     map[string]int      // failure signature -> attempt count (unfixable_failing_tests)
	CostThisTurn     float64             // cost accumulated within the current iteration (folded in at persist)
}

// ---- events (append-only feed) ----

// Event kinds (payloads are small JSON objects; never secrets, never provider keys).
const (
	EvPreflight    = "preflight_built"
	EvArmed        = "armed"
	EvIteration    = "iteration_started"
	EvUnit         = "unit_selected"
	EvModelTurn    = "model_turn" // provider/model/cost per turn — the collaboration record
	EvToolCall     = "tool_call"
	EvToolDenied   = "tool_denied"
	EvVerify       = "verify_result"
	EvReview       = "review_result"
	EvPersisted    = "persisted"
	EvApprovalReq  = "approval_requested"
	EvApprovalDone = "approval_decided"
	EvBoundary     = "boundary"
	EvStatus       = "status"
	EvError        = "error"
)

// Event is one row of the append-only run feed (dashboard polls it by seq cursor). It is also
// the machine-parseable run log the protocol roadmap asks for, scoped to the engine store.
type Event struct {
	Seq     int64          `json:"seq"`
	RunID   string         `json:"run_id"`
	TS      string         `json:"ts"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

// Approval is one pending/decided per-action permission request.
type Approval struct {
	ID        string         `json:"id"`
	RunID     string         `json:"run_id"`
	Class     string         `json:"class"`
	Action    string         `json:"action"` // human-readable, e.g. `run_command: "rm -rf build"`
	Args      map[string]any `json:"args"`
	Status    string         `json:"status"`
	DecidedBy string         `json:"decided_by,omitempty"`
	Note      string         `json:"note,omitempty"`
	CreatedAt string         `json:"created_at"`
	DecidedAt string         `json:"decided_at,omitempty"`
}

// ---- pre-flight ----

// PlannedUnit is one work item named in the pre-flight display (from todos.md and pending
// events — each pending event named explicitly, per the protocol).
type PlannedUnit struct {
	Source string `json:"source"` // "todos" | "event" | "goal"
	Text   string `json:"text"`
}

// Preflight is the complete §2.2 display. The dashboard renders it verbatim; Start confirms
// exactly this content (a stale pre-flight cannot be confirmed).
type Preflight struct {
	RunID     string `json:"run_id"`
	BuiltAt   string `json:"built_at"`
	Goal      string `json:"goal"`
	DoD       string `json:"definition_of_done"`
	Objective string `json:"objective"`

	PlannedUnits []PlannedUnit `json:"planned_units"`
	Team         []TeamMember  `json:"team"`

	CurrentIteration    int               `json:"current_iteration"` // previous run's counter; resets on confirm
	MaxIterations       int               `json:"max_iterations"`
	RunBoundaries       []string          `json:"run_boundaries"`
	LikelyChangedPaths  []string          `json:"likely_changed_paths"`
	PerActionPermission []string          `json:"per_action_permission"` // push/merge/deploy/... always separately gated
	Denylist            []string          `json:"denylist"`              // the constraints.md Autonomous-Edit Denylist in effect
	NoProgressThreshold int               `json:"no_progress_threshold"`
	CommandAllowlist    []string          `json:"command_allowlist"` // the verification commands that will be used
	Gates               map[string]string `json:"gates"`

	Branch string `json:"branch"` // the run branch that will be created

	// Notes surface pre-flight audit results the human should read before Start:
	// stale-run recovery performed, schema migration performed, scaffold created, warnings.
	Notes []string `json:"notes,omitempty"`
	// Blockers make Start impossible until resolved (foreign lock, dirty worktree, no commits).
	Blockers []string `json:"blockers,omitempty"`
}

// ---- the gateway seam ----

// TurnInput is one model call. The engine addresses models ONLY via Model (typically
// "auto:<profile>") — never a provider name — so vendor neutrality is structural.
type TurnInput struct {
	Project  string
	RepoID   string
	RepoRoot string
	// Req is a full OpenAI-shaped chat.completions request (model, messages, tools,
	// tool_choice, max_tokens). The gateway routes/reserves/meters/ledgers it.
	Req map[string]any
	// InjectMemory asks the gateway's memory track to inject the repo's .l00prite context
	// (as delimited untrusted blocks) — used on planning turns.
	InjectMemory bool
	// Bridge arms the cross-provider delegation loop for this turn (the l00prite_bridge
	// tool), enabling in-flight multi-provider collaboration inside one unit.
	Bridge bool
	// Meta lands on the gateway ledger rows (kind, run id, iteration, role).
	Meta map[string]any
}

// TurnResult is the engine-facing outcome of one model call.
type TurnResult struct {
	// Message is choices[0].message of the OpenAI-shaped response (content + tool_calls).
	Message  map[string]any
	Provider string
	Model    string
	CostUSD  float64
	// Unconfirmed marks cost computed from an unconfirmed/unpriced rate (never silently $0).
	Unconfirmed bool
	// DeniedReason is non-empty when the PEP denied the spend (budget cap) — the run must
	// stop at a boundary rather than retry past the cap.
	DeniedReason string
}

// RoutePreview is a dry-run routing decision for the pre-flight team display.
type RoutePreview struct {
	Provider string
	Model    string
	Reason   string
}

// ModelCaller is implemented by the gateway (over runTurn / RunBridge / the router). The
// engine never talks to providers, budgets, or the request ledger except through this.
type ModelCaller interface {
	Turn(ctx context.Context, in TurnInput) (TurnResult, error)
	// PreviewRoute resolves what "model" (e.g. "auto:code") would route to right now for the
	// pre-flight team display, without spending anything.
	PreviewRoute(project, repoID, model string, sampleReq map[string]any) (RoutePreview, error)
}
