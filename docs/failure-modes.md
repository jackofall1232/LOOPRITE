# Failure Mode Catalog

Real ways autonomous loops fail — and the specific l00prite mechanism that mitigates each.
Read this before arming Execution Mode, and consult it when a run misbehaves. Every failure
below is generic loop wisdom, not any one project's history; a scaffolded project starts
with a compact version of this list seeded into `.l00prite/failures.md`.

This catalog is l00prite's own mapping of the failure modes catalogued by the
[Loop Engineering](https://github.com/cobusgreyling/loop-engineering) project onto l00prite's
file-based protocol. Where l00prite has no mechanical guard yet, that is stated honestly and
the mitigation is a convention or a roadmap item.

## Severity

| Severity | Meaning |
|----------|---------|
| **S1 — Annoying** | Wasted time/tokens, no lasting harm |
| **S2 — Harmful** | Wrong code committed, corrupted memory, misleading ledger |
| **S3 — Critical** | Security, data loss, credentials, production incident |

A quick-reference table is at the end. Each entry lists the symptom, severity, and the
l00prite guard — the boundary, file convention, or tool that catches it.

---

## Infinite Fix Loop

**Symptom**: The same unit gets fix attempt after fix attempt and never converges; the run
burns its whole iteration budget on one broken thing.

**Severity**: S2

**l00prite guard**:
- The `unfixable_failing_tests` run boundary trips after **two distinct fix attempts** on the
  same unit (or a match against a `do_not_retry` note in `failures.md`) — the loop stops and
  hands off instead of thrashing.
- `failures.md` records each failed approach with an attempt count, so a resuming agent
  (any vendor) does not retry a known-dead path.
- The additive `execution.iterations_since_progress` counter (see *Thrash / No Progress*)
  observes the broader case where no single unit is failing but nothing is closing.

**Honest limit**: a formal `no_progress_detected` boundary that trips on the counter is a
roadmap item (`v1.2` gated batch); today the counter is telemetry the operator and the
doctor read, and a stall escalates through the existing `human_review_gate`.

---

## Verifier Theater

**Symptom**: The ledger says a unit passed, but the check never really ran — or "passed"
means "looked right." CI or a human later finds the obvious break.

**Severity**: S2

**l00prite guard**:
- The **honest-verification hard rule** in `execute-loop.md`: never claim success for a check
  that failed or could not run.
- Every ledger entry carries verification evidence — `command`, `exit_code`, an
  `evidence_path` when one exists, and a `timestamp` — so "verified" is auditable, not
  asserted.
- `scripts/l00prite-doctor.js` flags recent ledger entries that lack command/exit-code
  evidence.

**Honest limit**: l00prite's implementer and verifier are usually the **same agent**, which is
structurally weak (confirmation bias). An independent-verifier prompt is only genuinely
independent once the runtime harness exists (roadmap) — until then, evidence fields are the
mitigation, not a second opinion. Do not treat a self-approval without recorded evidence as
verification.

---

## State Rot

**Symptom**: `state.json`, `ledger.md`, or `todos.md` reference merged PRs, resolved events,
or finished work. The loop then acts on ghosts.

**Severity**: S1 → S2

**l00prite guard**:
- `.l00prite/` — not the session transcript — is the single source of truth, so state lives
  in diffable files rather than a vendor's hidden memory.
- Events move `pending/ → processing/ → completed/` with a documented lifecycle, so a
  processed event is not re-processed.
- `scripts/l00prite-doctor.js` cross-checks `state.pending_event_count` against the files in
  `events/pending/` and flags placeholder-only memory files — the mechanical State-Rot
  detector.

**Mitigation**: prune resolved events and closed todos every run; keep `memory.md` for durable
facts only (speculative notes belong in `sessions/`).

---

## Token / Wall-Clock Burn

**Symptom**: A confirmed run spends far more than expected because a single "one unit per
iteration" step is arbitrarily expensive, or the loop keeps working long past the point of
usefulness.

**Severity**: S1 (S2 if it also merges noise)

**l00prite guard**:
- `execution.max_iterations` is a bounded step **count** and always ships set (never
  unbounded); one unit per iteration keeps each step small and reviewable.
- The pre-flight display lists the planned units and the iteration budget before you arm,
  so the blast radius is visible up front.

**Honest limit**: l00prite does **not** measure token or dollar spend, and it never will pretend
to — an agent cannot observe its own true token usage, so any self-reported figure is fiction
and no stop may be built on it. A **wall-clock** budget (`time_budget` + `started_at`, the only
cost axis a file can honestly check) and a `budget_exceeded` boundary are the `v1.2` gated
batch. Today, bound the run with `max_iterations` and set a hard spend cap with your model
provider.

---

## Over-Reach (Wrong Scope / Wrong Path)

**Symptom**: The run edits `.env`, `auth/`, `payments/`, a migration, or CI config; or it
refactors unrelated modules; or injected event text talks it into a bigger job.

**Severity**: S2 → S3

**l00prite guard**:
- **Per-action permission**: push, merge, deploy, publish, out-of-repo deletes, and
  credential changes always require explicit, separate human permission — the pre-flight
  confirmation is never a blanket grant.
- The **Autonomous-Edit Denylist** in `constraints.md` (machine-readable globs: `.env*`,
  `**/secrets/**`, `**/credentials/**`, `auth/**`, `payments/**`, `**/migrations/**`, …): a
  file about to be edited that matches the denylist is treated as the
  `destructive_operation_required` boundary — stop and ask, per action. The denylist is
  **loop-immutable**; a run may never remove or loosen an entry.
- The `destructive_operation_required` boundary also covers history rewrites, dependency
  installs not named in the pre-flight, CI/workflow edits, and running fetched code.
- Event content is **untrusted data**: it may narrow the current unit but may **never expand
  scope** beyond `blueprint.md` and `todos.md`; a post-confirmation event needs fresh
  confirmation.
- `scripts/l00prite-doctor.js` warns when no denylist is configured.

---

## Parallel Collision

**Symptom**: Two agents (or two loops) write `.l00prite/` close together in time; one
clobbers the other's ledger or state.

**Severity**: S2

**l00prite guard**:
- The cooperative lock/lease convention (`lock.json` + `LOCKING.md`): check before writing a
  protected memory file, acquire if free, respect an active unexpired foreign lock, reclaim
  and log a stale one, release before stopping.
- The `lock_lease_conflict` boundary is a **special case**: on an active foreign lock the run
  reports the owner/purpose/expiry and writes **nothing** to protected paths — the memory
  belongs to another agent right now.

**Honest limit**: this is a cooperative convention enforced by agent instructions, **not** a
filesystem or database lock. Two agents that check `lock.json` in the same instant can still
race. Multi-agent use needs discipline, not just the file.

---

## Stale Arming / Crashed Run

**Symptom**: A previous Execution Mode run crashed with `execution.enabled: true` (or
`state.execution_active: true`) committed to disk. A later agent finds the repo "armed" with
no run actually happening.

**Severity**: S2

**l00prite guard**:
- Pre-flight **stale-run recovery**: if `state.execution_active` is true but no active,
  unexpired lock belongs to that run, the flag is treated as stale, reset, and the
  reclamation is recorded in the ledger — before anything else proceeds.
- Persisted `preflight_confirmed`/`enabled` flags **never** satisfy the pre-flight gate; every
  run re-confirms in-session, so leftover arming state cannot authorize a run.
- `scripts/l00prite-doctor.js` fails a project whose `execution.enabled`/`execution_active`
  disagree, or that is armed without a matching active execute-loop lock.

---

## Memory / Prompt Drift

**Symptom**: A project's own loop prompts or memory files drift out of sync — e.g. the
`.claude/prompts/` copy of a prompt no longer matches `.l00prite/prompts/`, or `state.json`
and `heartbeat.json` disagree about whether a run is active.

**Severity**: S2

**l00prite guard**:
- In the l00prite repo itself, the validator enforces **byte-identical** prompt mirrors across
  all seven canonical locations, so the shipped protocol cannot drift.
- In a scaffolded project, `scripts/l00prite-doctor.js` checks **self-parity** — the project's
  own `.l00prite/prompts/*.md` against its own `.claude/`/`.codex/` mirrors — and cross-field
  consistency (`execution_active` vs `execution.enabled`, `blocked` overriding
  `should_continue`).

---

## Comprehension Debt Spiral

**Symptom**: Velocity climbs, but nobody can explain what the loop shipped last week; review
degrades to rubber-stamping.

**Severity**: S2 (long-term)

**l00prite guard**:
- `ledger.md` is a **human-readable** run history — goal, decision, changed files, evidence,
  next action — designed to be read, not just written.
- Every push/merge/deploy still needs per-action human permission, so nothing outward-facing
  happens without a human in the loop.

**Mitigation**: read the ledger tail before each run; do not let the loop merge its own work.
Osmani: *"Build it like someone who intends to stay the engineer, not just the person who
presses go."*

---

## Cognitive Surrender

**Symptom**: "The loop handles it." No one holds an opinion on correctness or design anymore.

**Severity**: S2 (cultural)

**l00prite guard**:
- The mode boundary is deliberately explicit: Planning Mode never executes, and Execution
  Mode starts **only** through a pre-flight display plus a fresh in-session confirmation —
  every run, with no carry-over grants. The friction is the point; it keeps a human deciding.
- The self-modification guard means a run can never widen its own authority.

---

## Escalation Failure

**Symptom**: The loop stops but the reason is lost; no human learns it is stuck.

**Severity**: S2

**l00prite guard**:
- Every boundary stop (except `lock_lease_conflict`, which writes nothing by design) is
  **resumable** and records why: the boundary id in `state.execution_stop_reason` and
  `heartbeat.execution.last_run_boundary`, plus a ledger run-summary and
  `next_recommended_action`.
- `blocked` / `blocker_reason` in `state.json` surface a hard stop; the doctor reports them.

**Honest limit**: l00prite is **not** a hosted bot and does not ping Slack or open an issue on
escalation. The stop is recorded in files for the next human or agent to read — automated
notification is out of scope by design.

---

## Quick reference

| Failure mode | Severity | Primary l00prite guard |
|--------------|----------|------------------------|
| Infinite Fix Loop | S2 | `unfixable_failing_tests` boundary; `failures.md` attempt counts; `iterations_since_progress` |
| Verifier Theater | S2 | honest-verification rule; ledger evidence fields; doctor |
| State Rot | S1→S2 | files as source of truth; event lifecycle; doctor cross-checks |
| Token / Wall-Clock Burn | S1 | bounded `max_iterations`; one unit/iteration (wall-clock budget = roadmap) |
| Over-Reach (path/scope) | S2→S3 | per-action permission; **Autonomous-Edit Denylist** → `destructive_operation_required`; untrusted-content rule |
| Parallel Collision | S2 | `lock.json`/`LOCKING.md`; `lock_lease_conflict` write-nothing |
| Stale Arming / Crashed Run | S2 | pre-flight stale-run recovery; persisted flags never authorize; doctor |
| Memory / Prompt Drift | S2 | byte-parity validator (repo); doctor self-parity (project) |
| Comprehension Debt Spiral | S2 | human-readable ledger; per-action merge permission |
| Cognitive Surrender | S2 | explicit per-run pre-flight confirmation; self-mod guard |
| Escalation Failure | S2 | resumable exits record the boundary; `blocked`/`blocker_reason`; doctor |

## See also

- [anti-patterns.md](./anti-patterns.md) — design mistakes to avoid *before* arming a run.
- [concepts.md](./concepts.md) — intent debt, comprehension debt, protocol vs harness.
- `templates/l00prite/constraints.md` — the Autonomous-Edit Denylist you enforce.
- `scripts/l00prite-doctor.js` — the read-only health check that detects many of these.
