# HANDOFF

## Latest update: loop-maturity gap pass — doctor, failure catalogs, path denylist (in review)

This pass came from a maintainer request to *analyze the `loop-engineering` reference repo,
identify meaningful gaps in l00prite, and implement them*, using Fable 5 as advisor and Opus
as the execution model. A multi-agent gap-analysis workflow mapped both repos, synthesized
candidate gaps, and had Fable 5 rank them against l00prite's invariants; Opus implemented the
recommended scope. All work is on branch `claude/l00prite-gaps-analysis-hbv4ej`.

**The governing constraint: zero edits to the two review-gated files**
(`.claude/commands/build-loop.md`, `scripts/validate-l00prite.js`). Fable's key protective
reframes shaped the design and were followed exactly:

- **Path safety without a new boundary.** The single largest real safety gap was that
  Execution Mode gates by *action type* but never by *path* — a confirmed run could edit
  `.env`, `auth/`, or a migration. Rather than mint a tenth run boundary (which would edit
  both hardcoded validator arrays, the gated build-loop, and force a "nine → eleven" sweep),
  a denylisted-path edit now trips the **existing** `destructive_operation_required`
  boundary. The nine-boundary set is unchanged; the validator is untouched.
- **No-progress as telemetry now, boundary later.** Added additive, disarmed-neutral
  `heartbeat.execution` fields (`iterations_since_progress`, `last_progress_iteration`,
  `no_progress_threshold`) + execute-loop maintenance that escalates a stall through the
  existing `human_review_gate`, + a doctor stall check. The *formal* `no_progress_detected`
  boundary is deferred (it needs the gated validator arrays).
- **No budget-by-token fiction.** An agent cannot honestly measure its own token usage, so no
  stop is built on self-reported spend. A future budget boundary will be **wall-clock-first**
  (timestamps are file-checkable); it is deferred to the gated batch.

### What changed

- **`scripts/l00prite-doctor.js`** (new, anchor deliverable) — a read-only, dependency-free
  health check for a *scaffolded project's* `.l00prite/`, complementing the validator (which
  checks this repo). It reports `ok`/`warn`/`fail` and exits non-zero only on a fail. Checks:
  required memory files; JSON validity; **arming consistency** (mirrors the validator's rule —
  `enabled`/`execution_active` legal only under a matching active execute-loop lock);
  `state ↔ heartbeat` drift; `blocked` precedence; lock sanity; pending-count vs
  `events/pending/`; ledger verification evidence (Verifier Theater); seeded-catalog and
  denylist presence; no-progress stall; and **prompt-mirror self-parity** — the project's own
  `.l00prite/prompts/` vs its own `.claude/`/`.codex/` copies (never a baked canonical hash,
  so it survives legitimate protocol upgrades). Verified: HEALTHY on this repo and the
  example; a 5-way negative test produced 3 FAIL + 2 WARN, exit 1.
- **`docs/`** (new) — `failure-modes.md`, `anti-patterns.md`, `concepts.md`, `README.md`:
  l00prite-specific loop wisdom with an S1/S2/S3 severity taxonomy, each failure mapped to the
  boundary/lock/doctor/denylist that guards it. Adapted from the Loop Engineering project.
- **Seeded `failures.md`** (template + example + dogfood) — an "Inherited loop failure modes"
  section, clearly marked as generic wisdom, not project history, so a fresh scaffold warns an
  agent about known failure modes before it repeats them.
- **Autonomous-Edit Denylist** (`constraints.md`, all three copies) — a fenced, machine-readable
  glob block of protected paths + an auto-merge allowlist note, loop-immutable, enforced via
  `destructive_operation_required`. The dogfood copy encodes this repo's own review gates as
  globs.
- **`execute-loop.md`** (canonical + all 6 mirrors, one cp+cmp pass) — the denylist enforcement
  paragraph, the no-progress persist-step maintenance, and the extended self-modification guard
  (the loop may never raise `no_progress_threshold` or loosen the denylist). All 25
  validator-required substrings preserved; all 7 copies byte-identical.

### Files added / modified

Added: `scripts/l00prite-doctor.js`; `docs/README.md`, `docs/failure-modes.md`,
`docs/anti-patterns.md`, `docs/concepts.md`.
Modified: `constraints.md`, `failures.md`, `heartbeat.json` (template + example + dogfood each);
`templates/l00prite/prompts/execute-loop.md` + its 6 mirrors; `templates/AGENTS.md.template`;
`examples/vendor-neutral-output/AGENTS.md`; `README.md`, `AGENTS.md`, `CLAUDE.md`, this file;
`.l00prite/` memory (`ledger.md`, `todos.md`, `state.json`).
**Zero-line diff** to `.claude/commands/build-loop.md` and `scripts/validate-l00prite.js`.

### Remaining gaps

- The **v1.2 gated batch** (in `.l00prite/todos.md`): formal `no_progress_detected` +
  wall-clock-first `budget_exceeded` boundaries, a run-log substrate, phased autonomy levels
  (restriction ladder), an independent verifier prompt (after the harness), and a pattern
  library. Each needs a gated-file edit, so they are quarantined as one maintainer-review unit.
- The doctor's invariants (denylist present, failures seeded, progress fields exist) are not
  yet *validator-enforced* — asserting them edits the gated validator, so it is in the v1.2 batch.
- Everything the prior update's "Remaining gaps" lists (runtime harness, cooperative-not-enforced
  lock, no CI, no event ingestion) still stands.

## Latest update: universal agent layer + Execution Mode (v1.1, in review)

This pass, directed by the maintainer, evolves l00prite from a scaffold-and-stop protocol
into a two-mode execution protocol — "an operating system for autonomous software
engineering" — while making the protocol discoverable by every major AI coding agent, not
just Claude and Codex. All work is on branch `claude/powerful-helper-agent-pfsyj1`,
awaiting maintainer review; the two review-gated files
(`.claude/commands/build-loop.md`, `scripts/validate-l00prite.js`) were changed at the
maintainer's explicit direction and need that review before merge.

### The two-mode architecture

- **Planning Mode** (`build-loop`, unchanged default behavior): clarify → blueprint →
  scaffold → initialize `.l00prite/` → stop. Never executes; always ships Execution Mode
  disarmed (`execution.enabled: false`).
- **Execution Mode** (`execute-loop`, new): read blueprint → pre-flight gate → iterate
  (select one unit → execute → verify with evidence → persist → re-check boundaries) →
  stop at a run boundary → resumable by any vendor's agent. The gate is per-run and
  session-local: a pre-flight display plus explicit in-session human confirmation, which
  no persisted `preflight_confirmed`/`enabled` flag can substitute for. Headless sessions
  cannot enter Execution Mode. `build-loop --execute` offers the handoff after scaffolding
  but never pre-arms and never skips the gate.
- Nine run boundaries (heartbeat.json `execution.run_boundaries` — named to avoid
  colliding with the existing top-level `stop_conditions`): `definition_of_done_met`,
  `iteration_limit_reached`, `human_review_gate`, `destructive_operation_required`,
  `ambiguous_requirements`, `unfixable_failing_tests`, `missing_secrets_or_credentials`,
  `lock_lease_conflict` (special case: report only, write nothing to memory another agent
  holds), `stop_signal`.
- Discipline moved into the loop itself: per-action permission for
  push/merge/deploy/credentials; a self-modification guard (the loop may never raise
  `max_iterations`, edit `run_boundaries`/`human_review_gates`, or touch
  `.l00prite/prompts/`, `AGENTS.md`, adapters, `LOCKING.md`; within Execution Mode
  `should_continue` moves false→true only via a confirmed pre-flight); events arriving mid-run need fresh
  confirmation so injected text can't expand an autonomous run's scope; verification
  failures are retried differently within budget (failure is data), with attempt counts
  recorded in `failures.md`.

### The universal agent layer

- **Canonical prompts moved into the protocol**: `templates/l00prite/prompts/` now holds
  the single source for all six loop prompts (the five existing ones plus `execute-loop`),
  with byte-identical mirrors in `.claude/prompts/`, `.codex/prompts/`,
  `templates/claude/prompts/`, `templates/codex/prompts/`, this repo's own
  `.l00prite/prompts/`, and the example output. The old model (4 hand-maintained copies,
  keyword-only validation, a hardcoded `.codex/` path baked into even the Claude copies)
  is gone; the validator now fails on any byte drift. Every scaffolded `.l00prite/` is
  self-describing — an agent that finds the memory folder finds the procedures.
- **AGENTS.md standard**: `templates/AGENTS.md.template` generates a vendor-neutral
  operating guide into every target repo — read natively by OpenAI Codex, Cursor, GitHub
  Copilot (agent/CLI/VS Code/review), Windsurf, Zed, Jules, Factory, Amp, opencode, Devin,
  and others. `templates/CLAUDE.md.template` gained a fixed "l00prite Protocol" section so
  Claude Code (which reads CLAUDE.md, not AGENTS.md) finally gets the lock/untrusted/
  prompts rules in its default context file — previously only prompt-aware sessions
  learned about `lock.json`.
- **Vendor adapters** (`templates/adapters/`, manifest in `templates/vendors.json`,
  dogfooded at this repo's root, mirrored in the example output): `GEMINI.md` and
  `QWEN.md` (default context files; `@AGENTS.md` import), `.github/copilot-instructions.md`,
  `.cursor/rules/l00prite.mdc` (`alwaysApply`), `.windsurf/rules/l00prite.md`
  (`always_on`, sized far under Windsurf's ~6k/file truncation limit), `CONVENTIONS.md`
  (Aider, `--read` usage documented). Every adapter is self-sufficient — six protocol
  rules inline, never a bare pointer — because some Copilot surfaces can't open other
  files and Zed loads only its first match (where `copilot-instructions.md` outranks
  `AGENTS.md`). Deliberately **not** shipped: vendor config files (`.aider.conf.yml`,
  `.gemini/settings.json`) — a repo-root config can silently override a user's own; the
  snippets are documented instead.

### Schema v2

`heartbeat.json` gained the `execution` block (disarmed defaults, its own
`max_iterations`/`current_iteration`, `last_run_boundary`, `run_boundaries`);
`state.json` gained `execution_active`/`execution_stop_reason`. A file without the
`execution` block is a v1 file: execution is simply disabled until `execute-loop`
migrates it under lock, recorded in the ledger. Nothing in the repo checks
`schema_version === 1`, so the bump is safe.

### Validator

Extended (review-gated file) with: byte-parity checks for all prompt mirrors and adapter
copies; `templates/vendors.json`-driven adapter checks plus a reverse check that every
adapter file is in the manifest; execute-loop invariant checks (pre-flight language,
persisted-flag-never-satisfies rule, all nine boundary ids, lock-conflict no-write rule,
self-modification guard, per-action permission); disarmed-schema assertions
(`enabled === false`, `preflight_confirmed === false`, `execution_active === false`) on
the shipped template/example heartbeat/state copies, with the live dogfood copy allowed
to be armed only under a matching active execute-loop lock; both build-loop variants checked (previously only the
Claude one); template checks for the AGENTS.md/CLAUDE.md protocol sections; README
vendor-coverage and mode checks. All previous checks retained.

### Design decisions recorded (and rejected alternatives)

An adversarial three-critic design review ran before implementation; its blockers shaped
the final design. Rejected, do-not-retry (also in `.l00prite/failures.md`):
scaffold-time pre-arming (`--execute` writing `enabled: true` at scaffold — stale
confirmation, durable ambient arming); treating persisted `preflight_confirmed` as
authorization (forgeable via any memory write, transferable across sessions); bare-pointer
adapters (break on non-file-reading surfaces and Zed's first-match list); shipping
`.aider.conf.yml` (clobbers user config per-key; `.aider*` gitignore convention swallows
it); naming the new list `stop_conditions` (collides with the existing top-level field).

### Files added / modified

Added: `templates/l00prite/prompts/` (7 files) + 6 mirror sets; `.claude/commands/execute-loop.md`;
`templates/AGENTS.md.template`; `templates/adapters/` (7 files); `templates/vendors.json`;
dogfood adapters (`GEMINI.md`, `QWEN.md`, `CONVENTIONS.md`, `.github/copilot-instructions.md`,
`.cursor/rules/l00prite.mdc`, `.windsurf/rules/l00prite.md`) and their twins under
`examples/vendor-neutral-output/`;
`examples/vendor-neutral-output/AGENTS.md`.
Modified: both build-loop variants; `templates/CLAUDE.md.template`; heartbeat/state JSON
(all three copies each); `LOCKING.md`, `.l00prite/README.md`, `reviews/README.md` (all
three copies each); `scripts/validate-l00prite.js`; `README.md`, `AGENTS.md`, `CLAUDE.md`,
`RELEASE.md`, this file; `.l00prite/` memory files.

### Remaining gaps

- Execution Mode's invariants are validator-enforced prompt text, not a runtime harness —
  a non-compliant model can still ignore them. The harness is the next milestone.
- The lock/lease convention is still cooperative, not filesystem-enforced.
- No automated CI runs the validator on this repo yet.
- No event ingestion: events are still hand-authored JSON.

## Latest update: pre-release polish pass

This update prepares the repo for first public release. It does not add new protocol
capability — it corrects documentation that had drifted from reality and scaffolds
`.l00prite/` for l00prite's own repo so the protocol is dogfooded on itself, not just
described in examples.

### What changed

- **`CLAUDE.md` corrected.** The previous `CLAUDE.md` described an execution-mode feature
  (an `--execute` flag, pre-flight confirmation, `execute-loop` prompts, 8 stop conditions)
  as this session's mission. That feature was never built — commit `87384b4` ("Add
  execution mode to l00prite with safety features") only edited `CLAUDE.md`'s own text; no
  `execute-loop.md` file, `--execute` flag handling, or execution schema fields exist
  anywhere in the repo. `CLAUDE.md` now describes the four protocol layers that actually
  exist (scaffold, memory, event, handoff), with the execution-mode design preserved as an
  explicitly-labeled "not yet built" note rather than presented as current or in-progress
  work.
- **Execution-mode design decision (unchanged, still the plan).** Opt-in only, gated behind
  a `--execute` flag so default `build-loop` behavior never changes. Before any execution
  loop starts, the agent must display the selected goal, max iterations, stop conditions,
  files likely to change, forbidden destructive actions, and the tests/checks that will
  verify each step, then wait for explicit confirmation. The loop must stop immediately on:
  max iterations reached, unfixable failing tests, a destructive operation, missing
  secrets/credentials, an unclear or ambiguous requirement, a human review gate, or a
  lock/lease conflict. No push, merge, deploy, delete, or credential change without
  per-action permission — not a blanket grant at the start. This is now the top item in
  `.l00prite/todos.md` and the primary next milestone (see below).
- **Lock/lease gap Codex found, and the fix applied.** During the protocol-hardening PR
  (below), Codex flagged that `LOCKING.md` documented `status: "expired"` as a valid state
  but no rule actually permitted acquiring or reclaiming a lock in that state — only
  `unlocked`/`released` could be acquired, and stale-lock recovery only covered `active` +
  past-expiry, leaving `expired` locks unreclaimable by the letter of the rules. Fixed by
  Option 1: both the acquire rule and the stale-lock-recovery rule now explicitly cover
  `expired` alongside `unlocked`/`released` and `active`+past-expiry, so a lock that reads
  `status: "expired"` is reclaimable the same way a stale `active` lock is, with the
  reclamation still required to be logged in `ledger.md`.
- **ASCII banner updated.** The README's ASCII art banner was replaced with block letter art
  inside its existing fenced code block; the fence and the SVG logo `<img>` line above it
  were both verified intact (see README changes in this same pass).
- **Root `.l00prite/` scaffolded for l00prite's own repo.** l00prite previously had no
  `.l00prite/` of its own — only `templates/l00prite/` (a scaffolding template with
  placeholder values) and `examples/vendor-neutral-output/.l00prite/` (a filled
  demonstration, not this repo's real state). Added a real `.l00prite/` at repo root,
  populated with this repo's actual blueprint, todos, state, and a ledger entry for this
  session, so l00prite now dogfoods its own memory protocol instead of only documenting it.
- **`README.md` roadmap updated** to list execution mode as the next planned milestone.
- **`.l00prite/todos.md` updated**: v1 scaffold/memory/event/handoff work moved to Done,
  "first public release" recorded as Done with today's date, execution-mode build listed as
  the top Next item.
- **Current state of the repo as of this release:** scaffold layer, memory layer, event
  layer, and lock/lease convention are all built and validated; Claude and Codex have prompt
  parity; the validator (`node scripts/validate-l00prite.js`) passes with zero FAIL lines;
  execution mode is designed but not started.

### Files added

- `.l00prite/README.md`, `blueprint.md`, `constraints.md`, `failures.md`, `heartbeat.json`,
  `ledger.md`, `LOCKING.md`, `lock.json`, `memory.md`, `state.json`, `todos.md`,
  `events/README.md` and subfolders, `reviews/README.md` and subfolders, `sessions/README.md`
- `RELEASE.md`

### Files modified

- `CLAUDE.md`, `HANDOFF.md`, `README.md`

### Remaining gaps

- Execution mode is designed (see `CLAUDE.md` and `.l00prite/todos.md`) but not built —
  primary next milestone.
- The lock/lease convention is still cooperative, not filesystem-enforced.
- No automated CI runs the validator on this repo yet.

## Latest update: PR review fixes — lock state machine, event lifecycle, Claude parity in scaffolded projects

PR #7 (the protocol hardening PR below) went through review from gemini-code-assist,
Copilot, and Codex. All findings were verified against the actual files before acting —
every one was real. Fixes landed as three follow-up commits.

### gemini-code-assist findings (fixed)

- `resume-loop.md` / `respond-to-review.md` (all copies) now record lock status in
  `ledger.md`, matching the ledger template's `Lock` field.
- `handoff-summary.md` (all copies) now prefixes every listed memory file with `.l00prite/`,
  not just the first.
- The validator now checks the ledger template for the `Lock` field.

### Copilot findings (fixed)

- `LOCKING.md` was referenced as a bare filename in 13 places — prompts living outside
  `.l00prite/` (`.codex/prompts/`, `.claude/prompts/`, `templates/codex/prompts/`) and event
  docs nested inside `.l00prite/events/` and `.l00prite/events/processing/` — ambiguous from
  those locations. All now reference `.l00prite/LOCKING.md`. Also fixed the same bug in
  `heartbeat.md` (all copies), which had the identical issue but wasn't flagged.

### Codex findings

Three fixed directly (small, mechanical, non-architectural):

- `LOCKING.md` documented `status: "expired"` as valid but no rule permitted acquiring or
  reclaiming a lock in that state — only `unlocked`/`released` could be acquired, and stale
  recovery only covered `active` + past-expiry. Both rules now explicitly cover `expired`.
- `resume-loop.md`'s lock check told an agent to stop on *any* active, unexpired lock —
  including its own — so an agent updating several protected files in one run could block
  itself after its first write. Now scoped to locks owned by a different agent/session.
- `event-loop.md` and `respond-to-review.md` documented a `pending → processing →
  completed` event lifecycle but never actually moved the event file into `processing/`
  before executing, so an interrupted session left the event looking untouched instead of
  in-progress. Both now move the event into `processing/` before execution.

One required a human decision before fixing, since it touched `.claude/commands/build-loop.md`
and `templates/CLAUDE.md.template` — files CLAUDE.md itself flags as requiring mandatory
human review before merging:

- A generated target project's `CLAUDE.md` (the only file a resuming Claude session reads
  by default) had zero mention of the lock/lease protocol, so a Claude-only resume flow
  would mutate protected memory files without ever checking `lock.json` — unlike Codex,
  which gets lock-aware `.codex/prompts/`. Asked the user how to close this; they chose to
  ship `.claude/prompts/` to target repos (new `templates/claude/prompts/`, mirroring
  `templates/codex/prompts/`) rather than editing `CLAUDE.md.template` itself. `build-loop.md`
  (both Claude and Codex variants) now scaffolds `.claude/prompts/` into every target repo
  alongside `.codex/prompts/`, giving Claude the same lock-aware, event-aware prompts.
  `CLAUDE.md.template` was left untouched, per that decision.

### Files added

- `templates/claude/prompts/resume-loop.md`, `heartbeat.md`, `event-loop.md`,
  `respond-to-review.md`, `handoff-summary.md`

### Files modified

- All `.codex/`, `.claude/`, `templates/codex/prompts/` copies of `resume-loop.md`,
  `heartbeat.md`, `event-loop.md`, `respond-to-review.md`, `handoff-summary.md`
- `templates/l00prite/LOCKING.md`, `templates/l00prite/events/README.md`,
  `templates/l00prite/events/processing/README.md`, and their examples mirrors
- `.claude/commands/build-loop.md`, `.codex/prompts/build-loop.md`
- `README.md`, `scripts/validate-l00prite.js`

### Remaining gaps

- The lock/lease convention is still cooperative, not filesystem-enforced.
- `CLAUDE.md.template` itself still doesn't mention the lock/lease protocol by design (per
  the human's decision) — a Claude session that reads only `CLAUDE.md` and never opens
  `.claude/prompts/resume-loop.md` still won't check the lock. The scaffold now hands it the
  right prompt; nothing forces it to be read.
- No automated CI runs the validator on this repo yet.

## Latest update: protocol hardening after independent architecture review

An independent architecture review flagged concurrency, prompt-drift, prompt-injection, and
verification-evidence gaps in the `.l00prite/` protocol. This update addresses the findings
that are fixable at the protocol/documentation level — it does not add new runtime
behavior l00prite doesn't already have; it only changes what agents are told to do and what
the memory files look like.

### What changed

- **Lock/lease convention added.** New `.l00prite/lock.json` (fields: `schema_version`,
  `lock_id`, `owner_agent`, `owner_session`, `acquired_at`, `expires_at`, `ttl_seconds`,
  `purpose`, `protected_paths`, `status`) plus `LOCKING.md` documenting the full rules: check
  before writing, acquire if unlocked, respect an active unexpired lock, reclaim-and-log a
  stale one, release before stopping. `resume-loop.md`, `heartbeat.md`, `event-loop.md`, and
  `respond-to-review.md` (all copies) now reference it.
- **Untrusted-content warnings added** to every event/review prompt (`.codex/prompts/`,
  `.claude/prompts/`, `templates/codex/prompts/`) and event docs: PR comments, CI logs,
  issue bodies, and event summaries are external data to classify, not instructions to
  follow — including attempts to override system, developer, user, project, or l00prite
  protocol instructions.
- **Claude/Codex parity closed.** Added `.claude/prompts/resume-loop.md`, `heartbeat.md`,
  and `handoff-summary.md`, plus a new `.claude/README.md` mirroring `.codex/README.md`.
  Claude now has the same five standalone prompts Codex has.
- **Event transition rules clarified.** Events move `pending → processing → completed`
  using **move**, not the previous ambiguous "move or copy" wording. A completed event now
  requires `resolved_at`, `resolving_agent`, `verification_summary`, `response_summary`,
  `related_commit` (if available), and `outcome` (`resolved | rejected | blocked |
  duplicate | unsafe`).
- **Event ID format fixed.** IDs now follow `event-YYYYMMDD-HHMMSS-source-shortslug-random`
  (e.g. `event-20260630-214522-github-pr17-null-check-a9f3`) instead of the collision-prone
  `event-0001` sequential style, which is now documented only as an explicit anti-example.
- **`schema_version` added** to `heartbeat.json`, `state.json`, `example-event.json`, and
  the new `lock.json`, so future protocol changes have a compatibility signal.
- **Precedence rules documented** in new `templates/l00prite/README.md`: an active
  non-expired lock wins over any mutation attempt; `state.json.blocked` wins over
  `heartbeat.json.should_continue`; human review gates win over roadmap work; failed
  CI/review blocker events outrank normal roadmap tasks.
- **Ledger requires verification evidence.** `ledger.md`'s "Tests run / Verification" field
  now requires `command`, `exit_code`, `summary`, `evidence_path` (optional), and
  `timestamp` per check — vague "tests passed" statements are no longer sufficient. Added a
  `stale-lock-recovery` decision type and a `Lock` field to record lock acquire/release per
  run.
- **README** gained a "Lock and lease model" section stating plainly what the lock does and
  doesn't guarantee, plus `lock.json`/`LOCKING.md` rows in the protocol table.
- **Validator extended** to check: `lock.json` exists/parses/has required fields,
  `schema_version` on all four JSON templates, the event ID format is documented, event/review
  prompts contain the untrusted-content warning and don't use "move or copy," Claude parity
  prompts exist, ledger contains the new evidence fields, and README documents the lock/lease
  model. All checks pass (`node scripts/validate-l00prite.js`).

### Files added

- `templates/l00prite/lock.json`, `templates/l00prite/LOCKING.md`, `templates/l00prite/README.md`
- `examples/vendor-neutral-output/.l00prite/lock.json`, `LOCKING.md`, `README.md`
- `.claude/prompts/resume-loop.md`, `.claude/prompts/heartbeat.md`, `.claude/prompts/handoff-summary.md`
- `.claude/README.md`

### Files modified

- `.codex/prompts/resume-loop.md`, `heartbeat.md`, `event-loop.md`, `respond-to-review.md`
- `.claude/prompts/event-loop.md`, `.claude/prompts/respond-to-review.md`
- `templates/codex/prompts/resume-loop.md`, `heartbeat.md`, `event-loop.md`, `respond-to-review.md`
- `.claude/commands/build-loop.md`, `.codex/prompts/build-loop.md` (note that `lock.json`
  ships unlocked and isn't project-specific to fill in)
- `templates/l00prite/ledger.md`, `templates/l00prite/heartbeat.json`, `templates/l00prite/state.json`
- `templates/l00prite/events/README.md`, `events/pending/README.md`, `events/processing/README.md`,
  `events/completed/README.md`, `events/example-event.json`, `templates/l00prite/reviews/README.md`
- matching files under `examples/vendor-neutral-output/.l00prite/`
- `AGENTS.md`, `README.md`
- `scripts/validate-l00prite.js`

### Remaining gaps

- The lock/lease convention is cooperative, not enforced by the filesystem — it depends on
  every agent actually following the read-lock-before-write rule. Two agents writing at the
  exact same instant can still race.
- No automated CI runs `scripts/validate-l00prite.js` on this repo yet — the "PR + human
  review only" rule has no automated backstop.
- No event deduplication or cross-repo/monorepo memory scoping exists yet — both were
  flagged in the review as future scaling concerns and are unaddressed here.
- Ledger growth/rotation is still unbounded — no archival convention yet.
- Verification-evidence fields are structurally documented but not mechanically enforced;
  an agent can still write a vague ledger entry if it chooses to ignore the template.

## Latest update: README repositioning and visual identity

This update does not change protocol behavior. It rewrites `README.md` to match how the protocol actually works today (vendor-neutral memory, heartbeat, resume loops, Codex, and the event engine), and adds a minimal visual identity.

### What changed

- **README repositioned**: restructured around the requested section set (What is l00prite? / Why it exists / What it does / What it does NOT do / Core idea / Repository layout / The `.l00prite` protocol / Claude usage / Codex usage / Event and PR review workflow / Safety boundary / Install-setup / Validation / Current maturity / Roadmap / Contributing / License). Content is drawn from the existing protocol files (`AGENTS.md`, prompts, templates, validator) rather than invented — no new capabilities are claimed that the repo doesn't already have.
- **SVG logo added**: `assets/l00prite-infinity.svg` — a simple infinity-loop path plus the wordmark, text/paths only, no external or remote assets, sized to render cleanly in a GitHub-rendered README at both default and dark themes (mid-tone purple/blue chosen for contrast on both).
- **ASCII art added**: a small ASCII banner near the top of `README.md`, inside a fenced code block so it renders literally in Markdown instead of being interpreted.
- **Install/setup wording**: reframed as explicitly manual — clone, copy `.claude/commands/build-loop.md` or use `.codex/prompts/` directly, copy `templates/` by hand if scaffolding without a prompt. No install script or package exists, and the README says so instead of implying one does.
- **Canonical URL**: left as an explicit `TODO` rather than guessing an org/repo URL, since none was confirmed as canonical at the time of writing.

### Remaining branding/documentation gaps

- No canonical repository URL is wired into the README (badges, clone URL, issue links) — needs a maintainer decision.
- The SVG logo is a single static asset; no light/dark `<picture>` variant is provided (GitHub's `img`-embedded SVG doesn't inherit page theme via `currentColor`, so a true theme-aware logo would need two separate SVG files swapped via `<picture>` + `prefers-color-scheme`, which was judged out of scope for this pass).
- No favicon/social-preview image exists yet for the repo itself (separate from the README logo).

## Latest update: event and review response protocol

This update adds protocol-level support for event-driven work without turning l00prite into an autonomous GitHub bot.

## What changed in this update

- Added an event engine protocol: `Event → Classify → Plan → Execute → Verify → Persist → Respond`.
- Added PR review response protocol so review comments are first-class events.
- Added Codex/CLI prompts for event-loop and review-response workflows.
- Added Claude prompt mirrors for event-loop and review-response workflows.
- Updated heartbeat behavior to prioritize blockers, failed CI, PR reviews, security alerts, human TODOs, and then normal roadmap work.
- Updated ledger and state templates with event fields.
- Updated README and agent guidance around review events, verification, and non-autonomous push/merge behavior.
- Updated the validator to check event templates, event prompts, event schema JSON, and event-aware ledger/state fields.
- Added event/review template examples to the vendor-neutral example output.

## Files added in this update

- `.codex/prompts/event-loop.md`
- `.codex/prompts/respond-to-review.md`
- `.claude/prompts/event-loop.md`
- `.claude/prompts/respond-to-review.md`
- `templates/codex/prompts/event-loop.md`
- `templates/codex/prompts/respond-to-review.md`
- `templates/l00prite/events/README.md`
- `templates/l00prite/events/example-event.json`
- `templates/l00prite/events/pending/README.md`
- `templates/l00prite/events/processing/README.md`
- `templates/l00prite/events/completed/README.md`
- `templates/l00prite/reviews/README.md`
- `templates/l00prite/reviews/github/README.md`
- `examples/vendor-neutral-output/.l00prite/events/README.md`
- `examples/vendor-neutral-output/.l00prite/events/example-event.json`
- `examples/vendor-neutral-output/.l00prite/events/pending/README.md`
- `examples/vendor-neutral-output/.l00prite/events/processing/README.md`
- `examples/vendor-neutral-output/.l00prite/events/completed/README.md`
- `examples/vendor-neutral-output/.l00prite/reviews/README.md`
- `examples/vendor-neutral-output/.l00prite/reviews/github/README.md`

## Files modified in this update

- `README.md`
- `AGENTS.md`
- `HANDOFF.md`
- `.codex/README.md`
- `.codex/prompts/build-loop.md`
- `.codex/prompts/heartbeat.md`
- `.claude/commands/build-loop.md`
- `scripts/validate-l00prite.js`
- `templates/codex/prompts/heartbeat.md`
- `templates/l00prite/ledger.md`
- `templates/l00prite/state.json`
- `examples/vendor-neutral-output/.l00prite/ledger.md`
- `examples/vendor-neutral-output/.l00prite/state.json`

## Current architecture

l00prite now has four protocol layers:

1. **Scaffold layer** — Claude and Codex build-loop prompts create target project guidance, `.l00prite/` memory, and tiered skeleton files without executing implementation.
2. **Memory layer** — generated `.l00prite/` files persist blueprint, ledger, durable memory, constraints, failures, todos, heartbeat, state, events, reviews, and session-log conventions.
3. **Event layer** — pending protocol events can interrupt roadmap work and are processed through classify, plan, execute, verify, persist, and respond.
4. **Handoff layer** — resume, heartbeat, event, review-response, and handoff prompts let Claude, Codex, and other agents continue from shared files instead of vendor-specific hidden state.

## Remaining gaps

- l00prite now documents event-driven behavior but is not a full autonomous GitHub bot.
- No API ingestion exists yet for GitHub, CI providers, issue trackers, security tools, or dependency update services.
- Event movement is file-based and prompt-driven; future tooling may automate pending/processing/completed transitions.
- The validator is intentionally lightweight and does not parse every prompt for semantic consistency.
- Existing legacy example output remains Claude-focused; the new vendor-neutral example is separate.

## Recommended next steps

- Define optional event ingestion adapters without enabling automatic push, merge, or bot behavior by default.
- Expand examples with a filled PR review event and completed resolution notes.
- Consider validating generated prompts against all required `.l00prite/` files and event lifecycle steps.
- Keep build-loop scaffold-only; do not turn it into an executor without a separate explicit design.

## Decisions made

- `.l00prite/` is the shared source of truth across all agents.
- Events are protocol objects, not vendor-specific features.
- PR reviews are first-class events.
- Verification must happen before response.
- Process one event per loop by default.
- Build-loop remains non-executing and scaffold-first.
- Heartbeat state is JSON for machine readability.
- Ledger remains Markdown for human readability and rich narrative context.
- Borderline scope should choose the smaller skeleton tier.
