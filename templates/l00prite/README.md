# `.l00prite/` Memory Protocol

This folder is the shared, file-based memory for a project using l00prite. Any agent —
Claude, Codex, GPT, Gemini, Copilot, Cursor, Windsurf, Aider, or a future agent — should
treat these files as the source of truth for project state, and update them before stopping.

New here? `prompts/README.md` has the agent quickstart: the operating loop in six steps,
plus which prompt to use for which job.

## Files

| File | Purpose |
|------|---------|
| `blueprint.md` | Mission, architecture, requirements, and definition of done for the project. |
| `ledger.md` | Rich, human-readable run history, including evidence-backed verification. |
| `memory.md` | Durable decisions and facts only. |
| `heartbeat.json` | Machine-readable loop control, including the Execution Mode block. |
| `constraints.md` | Hard rules, user preferences, security boundaries. |
| `failures.md` | Approaches that already failed. |
| `todos.md` | Prioritized next actions. |
| `state.json` | Current machine-readable state. |
| `lock.json` | Lock/lease for safe mutation of protected memory files — see `LOCKING.md`. |
| `prompts/` | Canonical loop prompts (resume, heartbeat, event, review, handoff, execute) any agent can use — see `prompts/README.md`. |
| `events/` | Pending, processing, and completed events. |
| `reviews/` | Review-specific records. |
| `sessions/` | Session log conventions. |

## Two operating modes

- **Planning Mode** — clarify, blueprint, scaffold, initialize this folder, stop. Never
  executes the project.
- **Execution Mode** — an autonomous multi-iteration run entered only through
  `prompts/execute-loop.md`, behind a pre-flight display and an explicit, in-session human
  confirmation. It runs until a run boundary is reached and every stop is resumable.

In `heartbeat.json`, the top-level `max_iterations`/`current_iteration`/`stop_conditions`
govern planning-mode and supervised loops; the `execution` block (its own
`max_iterations`, `current_iteration`, and `run_boundaries`) governs Execution Mode runs;
other loops never arm it and may at most disarm it (`execution.enabled` true-to-false,
e.g. a heartbeat check delivering a stop) — only execute-loop's confirmed pre-flight ever
sets `execution.enabled: true`. A `heartbeat.json` without an `execution` block is a
schema-version-1 file: execution is simply disabled until `execute-loop` migrates it (under
lock, recorded in the ledger).

## Precedence rules

When signals disagree, resolve in this order:

1. **An active, non-expired lock wins over all mutation attempts.** If `lock.json` shows
   `status: "active"` and `expires_at` is in the future and you are not the owner, do not
   write to any protected path — see `LOCKING.md`.
2. **Blocked state wins over "should continue."** If `state.json.blocked` is `true`, that
   overrides `heartbeat.json.should_continue`, even if `should_continue` is `true`. Resolve
   the block before continuing roadmap work.
3. **Human review gates win over normal roadmap work.** If a `heartbeat.json`
   `human_review_gates` condition applies, stop and wait for review rather than continuing.
4. **Failed CI or review-blocker events outrank normal roadmap tasks.** Process pending
   blocker-priority events (failed CI, PR review requests, security alerts) before picking
   up the next `todos.md` item — see the priority order in `prompts/heartbeat.md`.

## Not a distributed system

This protocol is a set of file conventions and agent instructions, not a database with
transactions. It works well for one agent at a time, and reduces — but does not eliminate —
risk when multiple agents operate close together in time. See `LOCKING.md` for the
lock/lease convention and its limits.
