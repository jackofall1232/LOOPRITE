# Project Memory

Durable project facts and decisions that future agents should preserve.

## Decisions
- `.l00prite/` is the shared source of truth across all agents.
- Events are protocol objects, not vendor-specific features.
- PR reviews are first-class events.
- Verification must happen before response.
- Process one event per loop by default.
- l00prite has two operating modes (maintainer direction, 2026-07-02): Planning Mode
  (scaffold and stop — unchanged default) and Execution Mode (autonomous run until a run
  boundary). Execution is the product, and it is intentional: entered only through
  execute-loop's pre-flight display plus explicit in-session human confirmation, every run.
- The pre-flight confirmation is per-run and session-local. Persisted
  `preflight_confirmed`/`enabled` values in `heartbeat.json` are audit records, never
  authorization — any agent can write them, so honoring them would be a forgeable blanket
  grant. Headless sessions cannot enter Execution Mode.
- `--execute` on build-loop is a handoff offer, never a pre-arm: the scaffold always ships
  `execution.enabled: false`, and the gate runs in-session after scaffolding.
- A running loop may never raise its own limits: `execution.max_iterations`,
  `run_boundaries`, `human_review_gates`, and the protocol files
  (`.l00prite/prompts/`, `AGENTS.md`, adapters, `LOCKING.md`) are off-limits during a run;
  within Execution Mode, `should_continue` moves false→true only via a confirmed
  pre-flight (heartbeat checks in supervised/planning loops may still set it).
- The execution block's boundary list is named `run_boundaries`, not `stop_conditions`, to
  avoid colliding with heartbeat.json's existing top-level `stop_conditions`; execution has
  its own iteration counters and the top-level pair is untouched by execute-loop.
- The six loop prompts have ONE canonical source, `templates/l00prite/prompts/`; all other
  copies are byte-identical mirrors enforced by the validator. Edit canonical, re-copy,
  validate.
- Vendor support is data (`templates/vendors.json`); adapters are self-sufficient (six
  rules inline, never a bare pointer) because some Copilot surfaces can't open other files
  and Zed loads only its first match, where `copilot-instructions.md` outranks `AGENTS.md`.
- Never ship loaded vendor config (`.aider.conf.yml`, `.gemini/settings.json`) into a
  target repo — repo-root config silently overrides a user's own per-key; document the
  snippet instead.
- Heartbeat state is JSON for machine readability; the ledger stays Markdown for
  human-readable narrative context.
- Borderline scaffold scope should choose the smaller complexity tier.
- Lock/lease `status: "expired"` is acquirable/reclaimable the same way a stale `active`
  lock is (documented gap found by Codex during PR review, fixed in `LOCKING.md`).
- No change to `.claude/commands/build-loop.md` or `scripts/validate-l00prite.js` without
  human review. The 2026-07-02 changes to both were made at the maintainer's explicit
  direction on the review branch and still require review before merge.

## Facts
- l00prite ships no backend, hosted service, or install script; setup is manual (clone,
  copy prompts/templates).
- Prompt parity is byte-exact across seven locations per prompt (canonical + 6 mirrors),
  mechanically enforced — `node scripts/validate-l00prite.js` fails on any drift.
- `scripts/validate-l00prite.js` has no external dependencies; as of the v1.1 pass it runs
  519 checks (structural, byte-parity, adapter, execution-invariant), not full semantic
  correctness.
- Execution Mode's invariants are validator-enforced prompt text, not a runtime harness —
  a non-compliant model can still ignore them; the harness is roadmap.
- `heartbeat.json`/`state.json` are schema_version 2; a file without the `execution` block
  is v1 and means execution disabled until execute-loop migrates it under lock.

## Avoid
- Do not store random temporary notes, speculative ideas, or stale debugging output here.
