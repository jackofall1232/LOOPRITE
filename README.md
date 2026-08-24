<p align="center">
  <img src="assets/l00prite-infinity.svg" alt="l00prite logo — an infinity loop next to the word l00prite" width="420" />
</p>


```
⣶⣶┐   ⣶⣶⣶⣶⣶┐  ⣶⣶⣶⣶⣶┐  ⣶⣶⣶⣶┐  ⣶⣶⣶⣶┐  ⣶⣶┐ ⣶⣶⣶⣶⣶┐   ⣶⣶⣶⣶⣶┐
⣿⣿│   ⣿⣿  ⣿⣿│ ⣿⣿  ⣿⣿│ ⣿⣿ ⣿⣿│ ⣿⣿ ⣿⣿│ ⣿⣿│ ╚═⣿⣿╔═╝ ⣿⣿╔════╝
⣿⣿│   ⣿⣿  ⣿⣿│ ⣿⣿  ⣿⣿│ ⣿⣿⣶⣶╯  ⣿⣿⣶⣶╯  ⣿⣿│   ⣿⣿│     ⣿⣿⣶⣶┐
⣿⣿│   ⣿⣿  ⣿⣿│ ⣿⣿  ⣿⣿│ ⣿⣿╔═╝  ⣿⣿ ⣿⣿┐ ⣿⣿│   ⣿⣿│   ⣿⣿╔══╝
⣿⣿⣶⣶┐╚⣶⣶⣶⣶╔╝ ╚⣶⣶⣶⣶╔╝ ⣿⣿│     ⣿⣿  ⣿⣿│⣿⣿│  ⣿⣿│    ⣿⣿⣶⣶⣶┐
╚═════╝ ╚════╝   ╚════╝   ╚═╝     ╚═╝     ╚═╝   ╚╚══════╝

█▓▓┐   ▓████▓┐ ▓████▓┐ █████┐  █████┐  ██┐ ███████┐ ███████┐
█▓▓│   ██┌─██│ ██┌─██│ ██┌██│  ██┌██│  ██│ └─██┌──╝ ██┌────╝
█▓▓│   ██│ ██│ ██│ ██│ █████┘  █████┘  ██│   ██│    █████┐
█▓▓│   ██│ ██│ ██│ ██│ ██┌──   ██┌██┐  ██│   ██│    ██┌──╝
██████┐└████┌╝└████┌╝ ██│     ██│ ██│ ██│   ██│    ███████┐
└─────╝ └───╝   └───╝  └─╝     └─╝ └─╝ └─╝   └─╝    └──────╝

```


> l00prite gives AI coding agents a shared memory layer and an execution protocol inside your repo.
> Claude can plan. Codex can execute. Gemini can verify, respond to review comments, and update the ledger before stopping — and a future agent can resume all of it from files.

# l00prite
* The operating system for AI software engineering.
* Connect every major AI coding agent.
* Give them shared memory.
* Route work to the models that do it best.
* Resume any project from where the last agent stopped.

**An operating system for autonomous software engineering — persistent loop memory and a
deterministic execution protocol for AI coding agents.**

> **🚀 New here? Start with [GETTING_STARTED.md](GETTING_STARTED.md).** Three commands get you a
> running gateway with a browser setup wizard, a dashboard, and a Playground to prompt your
> models — `cd cli-os && ./install/install.sh && ./l00prite serve`, then open
> <http://127.0.0.1:8787/>. The rest of this README describes the agent-memory protocol.

> **📱 Prefer a signed Android APK you can sideload?** Get the current release-signed
> build from **[looprite.click](https://looprite.click/)** (the marketing site), or see
> [`CHANGELOG.md`](CHANGELOG.md) for what's new in each release.

## What is l00prite?

l00prite is a **vendor-neutral protocol that lets AI coding agents build software across
sessions, across vendors, and across time** — with durable file-based memory, verified
progress, first-class event handling, and resumable execution. It is not tied to one model
or one CLI: everything is plain Markdown and JSON in your repo, readable and writable by
Claude, Codex, GPT, Gemini, Copilot, Cursor, Windsurf, Aider, and whatever ships next.

Think of it as a small operating system whose hardware is your repo:

- **Memory** — the `.l00prite/` folder: blueprint, ledger, durable decisions, constraints,
  failures, todos, machine state.
- **Processes** — the loop prompts in `.l00prite/prompts/`: resume, heartbeat, event,
  review, handoff, execute.
- **Interrupts** — events (`events/pending → processing → completed`): PR reviews, failed
  CI, security alerts.
- **Scheduler** — `heartbeat.json`: iteration budgets, continue/pause/stop, review gates,
  run boundaries.
- **Locks** — `lock.json` + `LOCKING.md`: a cooperative lease so concurrent agents don't
  corrupt shared memory.
- **The journal** — `ledger.md`: what actually happened, run by run, with verification
  evidence.

## Two operating modes

**Planning Mode** — `build-loop` clarifies requirements, generates a blueprint, scaffolds
the repo, initializes `.l00prite/` memory, and stops. Planning never executes the project
it scaffolds.

**Execution Mode** — `execute-loop` reads the blueprint, then iterates autonomously: plan
the next unit of work, execute it, verify it with recorded evidence, persist memory,
process events, repeat — until the Definition of Done or another run boundary is reached.
Execution is the product, and it is always **intentional**: the mode is entered only
through a pre-flight display (goal, planned units, iteration budget, run boundaries, files
likely to change, verification commands) and an explicit, in-session human confirmation —
every run, with no carry-over grants.

```
Idea
  → Blueprint          (Planning Mode)
    → Execute          (Execution Mode, explicitly entered)
      → Verify
        → Persist
          → Repeat
            → Definition of Done
```

Every run boundary stop is resumable — by the same agent or a different vendor's — because
the entire run state lives in files.

## Why it exists

AI coding agents are good at long, iterative work, but they have a structural weakness:
**they forget**, and their sessions end.

- Context resets between sessions, so agents lose track of what was already tried.
- Without memory of failed approaches, agents repeat the same broken fix.
- PR review comments and CI failures get answered once and then lost.
- An autonomous run that stops halfway leaves no trustworthy record of where it stopped or
  why.
- Project state ends up scattered across chat transcripts, TODO comments, and tribal
  knowledge instead of the repo itself.

l00prite's answer: **put project memory and the execution protocol in the repo, as files.**
Files survive context resets, are diffable, are readable by any agent or human, and don't
depend on any one vendor's session state.

## What l00prite does

- Generates a project blueprint (`CLAUDE.md`) and a vendor-neutral operating guide
  (`AGENTS.md`) from a clarifying-questions conversation.
- Scaffolds a `.l00prite/` memory folder — including `.l00prite/prompts/`, the canonical
  loop prompts, so every project is self-describing to any agent that finds it.
- Runs autonomous Execution Mode loops behind a pre-flight confirmation gate, with nine
  deterministic run boundaries and evidence-backed verification at every iteration.
- Supports run ledgers that record what actually happened, run by run.
- Supports heartbeat control (`heartbeat.json`) to decide whether a loop should continue,
  pause, or stop.
- Supports resume loops so a new session can pick up the next smallest useful step.
- Models PR comments, CI failures, and other signals as first-class, trackable events.
- Ships adapters so every major agent discovers the protocol through its own context file
  (see the matrix below).
- Supports handoffs between agents — Claude to Codex to Gemini to human — through shared
  files instead of hidden state.

## What l00prite does NOT do

- It does **not** execute anything during Planning Mode. `build-loop` scaffolds and stops.
- It does **not** enter Execution Mode silently. No flag, no persisted bit, and no leftover
  `preflight_confirmed` value can start a run — only a fresh, in-session human confirmation
  can, every run.
- It does **not** push, merge, deploy, publish, or change credentials on its own. Those
  always require explicit per-action permission, even mid-run — the pre-flight confirmation
  is not a blanket grant.
- It does **not** let a running loop raise its own limits. Iteration budgets, run
  boundaries, and review gates can't be modified by the loop they govern.
- It is **not** a hosted service or a GitHub bot. Nothing watches your repo; events are
  file conventions.
- It does **not** depend on one AI vendor, and it does **not** predict exact token or
  dollar cost.

## Works with every agent

The protocol travels through the files each tool already reads:

| Agent | How it finds the protocol |
|-------|---------------------------|
| Claude Code | `CLAUDE.md` (fixed protocol section) + `.claude/prompts/` mirrors (plus the `/build-loop`, `/execute-loop` commands when running from the l00prite repo or after copying its `.claude/` folder) |
| OpenAI Codex | `AGENTS.md` (native) + `.codex/prompts/` mirrors |
| GitHub Copilot (agent, CLI, review, chat) | `AGENTS.md` (native) + `.github/copilot-instructions.md` |
| Cursor | `AGENTS.md` (native) + `.cursor/rules/l00prite.mdc` (`alwaysApply`) |
| Windsurf / Devin Desktop | `AGENTS.md` (native) + `.windsurf/rules/l00prite.md` (`always_on`) |
| Google Gemini CLI | `GEMINI.md` (imports `@AGENTS.md`) |
| Qwen Code | `QWEN.md` (imports `@AGENTS.md`) |
| Aider | `CONVENTIONS.md` via `--read` (documented in the file) |
| Zed | `.github/copilot-instructions.md` — Zed loads only its first-match rules file, which outranks `AGENTS.md`; that adapter is self-sufficient by design |
| Jules, Factory, Amp, opencode, Devin, Warp, Roo Code, JetBrains Junie, … | `AGENTS.md` (native) |
| Anything else | `.l00prite/prompts/README.md` — the agent quickstart; paste any prompt into any session |

Every adapter is self-sufficient (the six load-bearing rules inline, never a bare pointer),
and `templates/vendors.json` is the machine-readable manifest of this mapping. The loop
prompts themselves exist once, canonically, in `templates/l00prite/prompts/` — the
`.claude/`, `.codex/`, and scaffolded copies are byte-identical mirrors enforced by the
validator, so vendor parity can't silently drift.

## Repository layout

```text
cli-os/                  The l00prite CLI-OS: self-hosted gateway server + dashboard + Playground
                         (see GETTING_STARTED.md — start here if you're new)
.claude/                 Claude Code slash commands (build-loop, execute-loop) and prompt mirrors
.codex/                  Codex/CLI-agent prompt mirrors
.l00prite/               This repo's own protocol instance (dogfooded)
templates/               Templates used to generate target-project files
templates/l00prite/      The .l00prite/ memory folder template, incl. prompts/ (canonical)
templates/adapters/      Vendor adapter files + how to add a vendor
templates/vendors.json   Machine-readable vendor manifest
examples/                Filled reference outputs
docs/                    Operating knowledge: failure modes, anti-patterns, concepts
scripts/                 Validation tooling (validate-l00prite.js) + the health doctor (l00prite-doctor.js)
AGENTS.md                Instructions for AI agents working in this repo
GEMINI.md / QWEN.md / CONVENTIONS.md / .github/ / .cursor/ / .windsurf/   Dogfooded adapters
HANDOFF.md               Living log of what changed and what's left
README.md                This file
```

## The `.l00prite` protocol

A generated target project gets a `.l00prite/` folder. Every file has one job:

| File | Purpose |
|------|---------|
| `blueprint.md` | Mission, architecture, requirements, and definition of done for the project. |
| `ledger.md` | Rich, human-readable run history — one entry per run: goal, triggering event, decision, what changed, tests run, response status, next action. |
| `memory.md` | Durable decisions and facts only. Speculative notes and stale debugging output don't belong here. |
| `heartbeat.json` | Machine-readable loop control: iteration budgets, stop conditions, review gates, continue/stop signal, and the Execution Mode block (`enabled`, pre-flight audit fields, `run_boundaries`). |
| `constraints.md` | Hard rules, user preferences, security boundaries, architecture constraints. |
| `failures.md` | Approaches that already failed and should not be retried unless conditions change. |
| `todos.md` | Prioritized next actions for whichever agent picks this up next. |
| `state.json` | Current machine-readable state: active event id, pending event count, CI status, execution-run state. |
| `lock.json` | Lock/lease state for safe mutation of protected memory files — see `LOCKING.md`. |
| `LOCKING.md` | The lock/lease protocol in full — fields, rules, and limits. |
| `prompts/` | The canonical loop prompts any agent can use — resume, heartbeat, event, review, handoff, execute — plus the agent quickstart. |
| `events/` | Pending, processing, and completed events (PR comments, CI failures, alerts) as JSON. |
| `reviews/` | Review-specific records, including GitHub PR review handling. |
| `sessions/` | Session log conventions for per-run notes that don't belong in `memory.md`. |

Any agent that reads these files has everything it needs to continue safely.

## Execution Mode in detail

`execute-loop` (`.l00prite/prompts/execute-loop.md`, `/execute-loop` in Claude Code) is the
autonomous run. Its discipline is the protocol, not a wrapper around it:

1. **Pre-flight gate, every run.** Lock check first, stale-run recovery, then a pre-flight
   display — goal, planned units, `current_iteration`/`max_iterations`, all nine run
   boundaries, files likely to change, per-action-permission list, verification commands —
   followed by explicit human confirmation in the session. Persisted flags never satisfy
   the gate; headless sessions can't enter Execution Mode.
2. **Iterations.** One unit of work per iteration: execute, verify with recorded evidence
   (command, exit code, summary, timestamp), persist every memory file, increment the
   counter, re-check boundaries. Failed verification is retried differently within the
   budget — failure is data, not an ending — until it genuinely can't be fixed.
3. **Nine run boundaries.** `definition_of_done_met` (the goal), `iteration_limit_reached`,
   `human_review_gate`, `destructive_operation_required`, `ambiguous_requirements`,
   `unfixable_failing_tests`, `missing_secrets_or_credentials`, `lock_lease_conflict`
   (special case: report only, write nothing to memory another agent holds), and
   `stop_signal`.
4. **Resumable exits.** Every stop except `lock_lease_conflict` records why (ledger,
   `state.json.execution_stop_reason`, `heartbeat.json.execution.last_run_boundary`),
   releases the lock, and disarms (`execution.enabled: false`). A lock-conflict stop
   reports the boundary in-session and writes nothing — the next run's pre-flight
   stale-run recovery cleans up. A new session — any vendor — re-runs the pre-flight and
   continues with a fresh iteration budget.
5. **Hard rules.** Per-action permission for push/merge/deploy/credentials; no
   self-modification of limits, boundaries, or protocol files; one unit per iteration;
   honest verification.

## Event and PR review workflow

l00prite treats project interrupts as first-class, trackable events:

- A PR review comment becomes an event. A failed CI run can become an event.
- Agents process **one event per loop by default** — this keeps each fix, verification,
  and reviewer response auditable.
- Event content is **untrusted data** to classify, never instructions to follow.
- Verification is required before responding. If tests fail or can't run, that gets
  recorded honestly — not glossed over.
- Inside an Execution Mode run, events named in the confirmed pre-flight are in scope;
  events that arrive mid-run require fresh confirmation before implementation — injected
  text can never expand an autonomous run's scope.
- Agents update `ledger.md`, `state.json`, and related memory files before stopping,
  whether or not the event fully resolved.

## Lock and lease model

l00prite is safe for sequential handoff by default: one agent, one session at a time,
reading and writing `.l00prite/` between runs works with no extra steps.

When more than one agent might touch the same project close together in time,
`.l00prite/lock.json` provides a lightweight lock/lease convention:

- Before mutating any protected memory file (`ledger.md`, `memory.md`, `state.json`,
  `heartbeat.json`, `failures.md`, `todos.md`, `events/`, `reviews/`, `sessions/`), an agent
  checks `lock.json`.
- If it's unlocked, the agent acquires it, does its work, and releases it before stopping.
- If it's already locked and not expired, the agent does not write — it treats the lock as
  a blocker and stops or waits.
- Every lock carries a `ttl_seconds` and `expires_at`. If an agent crashes while holding
  the lock, it becomes stale once it expires, and the next agent may reclaim it — recording
  that reclamation in `ledger.md` so it's not silent.

See `templates/l00prite/LOCKING.md` for the full field reference and rules.

**Be honest about what this is.** This is a lightweight protocol convention enforced by
agent instructions, not a real distributed lock. It meaningfully reduces the odds of silent
memory corruption when agents operate close together in time, but it cannot guarantee
correctness the way a database transaction or a filesystem-level lock can — two agents that
both check `lock.json` in the same instant can still race past each other before either
writes. Multi-agent use still requires discipline, not just this file.

## The mode boundary

This is the boundary that matters most, stated plainly:

- Planning Mode creates structure and memory — nothing else. It does not execute the
  generated project, and it does not silently overwrite existing files.
- Execution Mode is powerful *because* it is explicit: a pre-flight display and an
  in-session human confirmation sit between the modes, every run. `--execute` offers the
  handoff; it never skips the gate.
- A loop can never grant itself more: not more iterations, not fewer boundaries, not looser
  gates, not edited protocol files.

Discipline doesn't limit the loop — it's what makes a long autonomous run trustworthy
enough to actually let it run.

## Claude usage

Run Claude Code from this repo (or copy `.claude/` into your project). Then:

```text
/build-loop a CLI tool that scrapes RSS feeds and emails a daily digest
```

`/build-loop` asks clarifying questions, picks a complexity tier, writes `CLAUDE.md` and
`AGENTS.md`, scaffolds `.l00prite/` (including the canonical prompts), `.claude/prompts/`,
`.codex/prompts/`, the vendor adapters, and a tiered skeleton — then stops.

```text
/build-loop --execute a CLI tool that scrapes RSS feeds and emails a daily digest
```

Same scaffold, then an offer: the session shows execute-loop's pre-flight and, only on your
explicit confirmation, begins the autonomous run in place.

```text
/execute-loop
```

Enter Execution Mode for an already-scaffolded project — pre-flight, confirmation, run.

## Codex and CLI-agent usage

The same prompts, no special tooling — paste into any session from
`.codex/prompts/` (or use `.l00prite/prompts/` inside a scaffolded project):

- `build-loop.md` — Planning Mode: scaffold a target project and stop.
- `execute-loop.md` — Execution Mode: pre-flight, confirmation, autonomous run.
- `resume-loop.md` — one supervised implementation step from `.l00prite/`.
- `heartbeat.md` — decide whether a loop should continue, pause, or stop.
- `event-loop.md` — process one pending event end-to-end.
- `respond-to-review.md` — resolve one PR review event and draft a response.
- `handoff-summary.md` — write the cross-agent handoff summary.

## Install / setup

**The CLI-OS gateway** (a place to prompt models and point your coding tools at) installs in three
commands — see [GETTING_STARTED.md](GETTING_STARTED.md): `cd cli-os && ./install/install.sh &&
./l00prite serve`, then finish setup in the browser wizard at `http://127.0.0.1:8787/`.

**The protocol side** is not packaged as an installable CLI or extension yet. Setup today is manual:

1. Clone this repo.
2. For Claude Code: run Claude Code from this repo directly, or copy `.claude/` into your
   project (note `build-loop` reads `templates/` from this repo while scaffolding).
3. For Codex or other CLI agents: open `.codex/prompts/` and copy/paste the relevant prompt.
4. To generate a new project: run `/build-loop` (Claude) or paste
   `.codex/prompts/build-loop.md` (Codex/others), then follow the clarifying questions.
5. `templates/` files (skeletons, `.l00prite/` templates, adapters, prompt mirrors) can be
   copied directly into a target repo if you're scaffolding by hand.

> TODO: canonical repository URL for install instructions/badges — this README intentionally avoids inventing one.

## Validation

A dependency-free validator checks structural and protocol invariants:

```bash
node scripts/validate-l00prite.js
```

It checks: required files exist across all layers; the six loop prompts are **byte-identical**
across every mirror location (canonical: `templates/l00prite/prompts/`); the vendor
adapters match `templates/vendors.json` and stay self-sufficient and under size limits;
`execute-loop` carries the pre-flight gate, the re-confirmation rule, all nine run
boundaries, the lock-conflict no-write rule, and the self-modification guard; the
shipped `heartbeat.json`/`state.json` copies are disarmed (`execution.enabled: false`),
and this repo's own live copy may be armed only with a matching active execute-loop lock; both
build-loop variants keep Planning Mode non-executing and `--execute` gate-only; event and
review prompts keep the untrusted-content and one-event-per-loop rules; and all JSON
templates parse with their required fields.

The validator checks the l00prite repo itself. To health-check a **scaffolded target
project's** `.l00prite/` memory, run the read-only doctor against it:

```bash
node scripts/l00prite-doctor.js /path/to/your/project   # default: current directory
```

The doctor never writes and never "fixes" anything — it reports `ok`/`warn`/`fail` findings
and exits non-zero only on a fail. It catches the failures the protocol is built to prevent:
`state.json` ↔ `heartbeat.json` arming drift, a crashed run that left Execution Mode armed
without a lock, prompt-mirror drift (the project's own `.l00prite/prompts/` vs its
`.claude/`/`.codex/` copies), ledger entries missing verification evidence, a pending-event
count that no longer matches `events/pending/`, a missing Autonomous-Edit Denylist, and a
no-progress stall. The failure modes it detects are catalogued, with severities and
mitigations, in [`docs/`](docs/) ([failure-modes](docs/failure-modes.md) ·
[anti-patterns](docs/anti-patterns.md) · [concepts](docs/concepts.md)).

## Current maturity

**Alpha / protocol preview.**

- l00prite is Markdown- and JSON-driven — a protocol and a set of prompts, not a running
  service. Execution Mode is a protocol the agent follows (backed by validator-enforced
  prompt invariants), not a hosted runtime that forces compliance.
- The event engine is protocol-level (file conventions and prompts), not a hosted bot
  watching your repo.
- Automation integrations (GitHub webhooks, CI ingestion, auto-triggered event creation)
  are future work, not present today.

## Roadmap

- **v1.2 gated batch** (needs maintainer review of the two review-gated files): promote the
  no-progress telemetry and a **wall-clock-first** budget into formal `no_progress_detected`
  and `budget_exceeded` run boundaries (nine → eleven); a machine-parseable `run-log.jsonl`
  + appender; phased autonomy levels (`report_only`/`assisted`/`unattended`, designed as a
  restriction ladder); an independent verifier prompt (after the harness); and a pattern
  library. Tracked in `.l00prite/todos.md`. (Token/dollar spend stays out — an agent can't
  honestly measure its own token usage, so no stop is built on it.)
- GitHub event ingestion (turning real PR comments into `.l00prite/events/` entries automatically).
- CI failure capture as events.
- A runtime harness that mechanically enforces run boundaries and iteration budgets
  (today they're validator-checked prompt invariants; a harness would make them
  guarantees).
- Richer, filled-in examples (a real resolved PR review, a real multi-session Execution
  Mode run with a boundary stop and resume).
- Stack-specific skeleton packs (e.g. a Python/pytest pack, a Node/vitest pack).
- Cross-agent compatibility tests (verify Claude, Codex, Gemini, and Copilot genuinely
  hand off cleanly, including mid-execution resumes).
- Ledger growth management (archival/rotation conventions).
- Release packaging (so setup isn't fully manual).

## Contributing

- Preserve the mode boundary. Planning Mode must never execute a generated project, and
  Execution Mode must never start without its confirmed pre-flight.
- Edit loop prompts only at the canonical location (`templates/l00prite/prompts/`) and
  re-copy the mirrors — the validator fails on any drift.
- Keep the protocol vendor-neutral — new vendor support goes through
  `templates/adapters/README.md`'s inclusion rule and `templates/vendors.json`.
- Update `README.md`, `HANDOFF.md`, and `AGENTS.md` when you change protocol behavior, not
  just code/templates.
- Add an example when you add a new protocol concept — an unillustrated concept is hard for
  both agents and humans to apply correctly.

## License

MIT. See [LICENSE](LICENSE).
