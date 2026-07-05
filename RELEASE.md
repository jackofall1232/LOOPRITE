# l00prite v1.1 (in review)

v1.1 adds the two things v1 explicitly deferred:

- **Execution Mode** — `execute-loop` prompts everywhere (canonical copy in
  `.l00prite/prompts/`), a `--execute` handoff on `build-loop`, a mandatory pre-flight
  display + explicit in-session confirmation gate (persisted flags never satisfy it), nine
  run boundaries, resumable exits, and schema v2 execution fields in
  `heartbeat.json`/`state.json`. Planning Mode's scaffold-and-stop default is unchanged and
  always ships execution disarmed.
- **Universal agent layer** — a generated `AGENTS.md` (read natively by OpenAI Codex,
  Cursor, GitHub Copilot, Windsurf, Zed, Jules, Factory, Amp, opencode, Devin, and more), a
  fixed protocol section in the generated `CLAUDE.md`, canonical loop prompts inside
  `.l00prite/prompts/` with byte-identical vendor mirrors (validator-enforced), and
  self-sufficient adapters for Gemini CLI, Qwen Code, Copilot, Cursor, Windsurf, and Aider
  (`templates/adapters/`, manifest in `templates/vendors.json`).

See `HANDOFF.md` and `CLAUDE.md` Sections 2–4 for the full description. The v1 notes below
are kept as shipped; where they say execution mode "does not exist yet," v1.1 is the
release that adds it.

# l00prite v1

## What l00prite is

l00prite is a vendor-neutral protocol for giving AI coding agents durable, file-based
project memory. Instead of relying on one vendor's chat history or hidden session state, it
scaffolds a project blueprint and a `.l00prite/` memory folder into your repo, along with
matching Claude Code and Codex/CLI prompts, so any agent — Claude, Codex, GPT, Gemini, or a
future CLI agent — can read the same files and pick up exactly where another agent (or a
human) left off.

## What's in v1

- **Scaffold layer** — `/build-loop` (Claude Code) and `.codex/prompts/build-loop.md`
  (Codex/CLI) ask clarifying questions, pick a complexity tier, and write a project
  `CLAUDE.md`, `.l00prite/` memory folder, `.codex/prompts/`, `.claude/prompts/`, and a
  tiered skeleton — then stop. Scaffolding never executes the generated project.
  (As of v1.1, the scaffold also writes `AGENTS.md`, `.l00prite/prompts/`, and the vendor
  adapters.)
- **Memory layer** — a `.l00prite/` folder (`blueprint.md`, `ledger.md`, `memory.md`,
  `constraints.md`, `failures.md`, `todos.md`, `heartbeat.json`, `state.json`) that any
  agent treats as the source of truth for project state.
- **Event layer** — PR review comments, failed CI runs, and other interrupts are modeled as
  first-class JSON events moving through `pending/ → processing/ → completed/`, handled one
  at a time via `Classify → Plan → Execute → Verify → Persist → Respond`.
- **Handoff layer** — resume-loop, heartbeat, respond-to-review, and handoff-summary
  prompts, mirrored for both Claude and Codex, so a new session can resume the next
  smallest useful step from shared files.
- **Lock/lease convention** — `.l00prite/lock.json` and `LOCKING.md` give agents working
  close together in time a cooperative way to avoid racing writes to shared memory files.
- **Validator** — `scripts/validate-l00prite.js`, a dependency-free Node script that checks
  structural and protocol invariants across prompts, templates, and examples.

## What's NOT in v1

- **Execution mode.** There is no `--execute` flag, no pre-flight confirmation gate, and no
  `execute-loop` prompt yet. `build-loop` always stops after scaffolding — it never
  continues into implementation on its own. Execution mode is designed (see `CLAUDE.md` and
  `.l00prite/todos.md`) but not built; it is the next planned milestone.
- **A GitHub bot.** l00prite does not watch your repo, auto-create events from real GitHub
  activity, or auto-push, merge, or deploy anything. Events are file conventions an agent
  fills in by hand today.
- **Auto-push / auto-merge.** Nothing in l00prite pushes, merges, or deploys without a human
  explicitly doing so themselves.

## How to get started

1. Clone this repo.
2. `build-loop` reads from this repo's `templates/` directory (`templates/CLAUDE.md.template`,
   `templates/l00prite/`, `templates/skeleton/`) while it scaffolds, so copying just
   `.claude/commands/build-loop.md` into another project isn't enough on its own — either run
   Claude Code/Codex from this cloned repo and point it at your target project's path, or
   copy `templates/` into your target project alongside `.claude/commands/build-loop.md` (or
   `.codex/prompts/build-loop.md` for Codex/other CLI agents).
3. In your target project, run `/build-loop <describe your project>` (Claude Code) or paste
   the Codex prompt and follow the clarifying questions. It scaffolds your project's
   `CLAUDE.md` and `.l00prite/` memory, then stops — open a separate session afterward to
   start implementing.

## What to test, and how to give feedback

Try scaffolding a real project with `/build-loop`, then a separate session resuming from
`.l00prite/`, then (if relevant) an event/review-response loop. Things worth checking:
whether the generated `CLAUDE.md` and `.l00prite/` files make sense for your project size,
whether a second agent can actually pick up where the first left off using only the files,
and whether `node scripts/validate-l00prite.js` still passes after you scaffold or extend a
project.

Please report bugs, confusing prompts, or protocol gaps as GitHub issues on this repo.

## What's coming next

**Execution mode** — an opt-in `--execute` flag that lets `build-loop` continue past
scaffolding into a real implementation loop, gated behind a mandatory pre-flight display
(goal, max iterations, stop conditions, files likely to change, forbidden destructive
actions, and verification checks) and explicit user confirmation, with hard stops on max
iterations, unfixable failing tests, destructive operations, missing secrets, ambiguous
requirements, human review gates, and lock/lease conflicts. See `CLAUDE.md` and
`.l00prite/todos.md` for the full design.
