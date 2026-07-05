# Resume Loop Prompt

You are resuming a l00prite-managed project. Treat `.l00prite/` as the shared source of truth across Claude, Codex, GPT, Gemini, and future agents.

This prompt performs **one** supervised loop iteration: one smallest useful step, verified,
persisted, then stop. For an autonomous multi-iteration run that continues until a run
boundary, use `.l00prite/prompts/execute-loop.md` instead — it requires its own pre-flight
confirmation before starting.

## Required context read

Before changing files, read:

- `.l00prite/blueprint.md`
- `.l00prite/ledger.md`
- `.l00prite/memory.md`
- `.l00prite/constraints.md`
- `.l00prite/failures.md`
- `.l00prite/todos.md`
- `.l00prite/state.json`
- `.l00prite/heartbeat.json`
- `.l00prite/lock.json`

## Lock check (required before any write)

- Read `.l00prite/lock.json` before mutating any protected path (`ledger.md`, `memory.md`,
  `state.json`, `heartbeat.json`, `failures.md`, `todos.md`, `events/`, `reviews/`,
  `sessions/`).
- If `status` is `unlocked`, `released`, or `expired`, acquire the lock: set
  `status: "active"`, a new `lock_id`, `owner_agent`, `owner_session`, `acquired_at`,
  `expires_at` (`acquired_at` + `ttl_seconds`), and `purpose`.
- If `status` is `active`, not expired, and owned by a different agent or session, do
  **not** write. Stop and report the lock as a blocker instead of proceeding.
- If `status` is `active`, not expired, and already owned by you (matching
  `owner_agent`/`owner_session`), continue writing — you do not need to re-acquire before
  each write within the same run.
- If `status` is `active` but `expires_at` has passed, or `status` is explicitly `expired`,
  treat it as stale: you may reclaim it (acquire as above), but record a `ledger.md` entry
  noting the reclaimed `lock_id` and why it was judged stale.
- If a step is likely to run longer than `ttl_seconds` (e.g. a slow test suite), refresh
  `expires_at` partway through rather than letting a still-running step look stale.
- Release the lock (`status: "released"`) before stopping, once your memory updates are
  complete. See `.l00prite/LOCKING.md` for the full rules.

## Precedence rules

- An active, non-expired lock you don't own wins over any write you were about to make.
- `state.json.blocked: true` overrides `heartbeat.json.should_continue`.
- Human review gates in `heartbeat.json` win over normal roadmap work.
- Failed CI, PR review, or other blocker-priority events outrank normal roadmap `todos.md` items.

## Required loop behavior

1. State your understanding of the current project, current goal, status, blocker state, and next recommended action.
2. State what you will **not** retry, based on `.l00prite/failures.md` and ledger do-not-retry notes.
3. Check heartbeat limits and the precedence rules above before implementation. Stop if blocked, human review is required, completion is already reached, max iterations are reached, or the lock is held by a different agent or session.
4. Pick the next smallest useful step from `.l00prite/todos.md` or `.l00prite/state.json`.
5. Execute only that step. Do not expand scope without human approval.
6. Verify the step with the smallest meaningful test/check available.
7. Update `.l00prite/ledger.md` with goal, completed work, changed files, tests run (command, exit code, summary, evidence path if available, timestamp), failures, decisions, confidence, next action, do-not-retry notes, and lock status (lock_id acquired/released or none).
8. Update `.l00prite/state.json` with current goal, phase, active/last agent, last_updated, status, blocked, blocker_reason, and next_recommended_action.
9. Update `.l00prite/todos.md` to reflect completed and next work.
10. Update `.l00prite/failures.md` if anything failed or should not be retried.
11. Update `.l00prite/heartbeat.json` before stopping.
12. Release the lock (`.l00prite/lock.json`) before stopping.

Stop after the chosen step and memory updates. Every loop must update memory and release the lock before stopping.
