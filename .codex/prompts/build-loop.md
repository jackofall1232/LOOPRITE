# Codex Build Loop Prompt (Planning Mode)

You are using l00prite to scaffold a vendor-neutral project loop. This prompt is **Planning
Mode**: create a blueprint, `AGENTS.md`, a `.l00prite/` memory folder, loop prompts, vendor
adapters, and a skeleton — then stop. Planning Mode does not execute the project it
scaffolds. Execution Mode is a separate, explicitly-confirmed handoff described in
section 8.

If the user's request included the flag `--execute`, note it now and apply section 8 after
the scaffold is complete; the flag changes nothing about sections 1–7.

## 1. Ask clarifying questions first

Ask for project type, MVP scope and out-of-scope items, target languages/stack, target repo
path, and hard constraints. Ask all five together, and do not continue until answered —
except where the user explicitly says to use judgment for an item, in which case state the
default you chose out loud.

## 2. Pick a complexity tier

Choose `small`, `medium`, or `large` from `templates/skeleton/`. If borderline, choose the
smaller tier. Tell the user which tier you picked and explain why, referencing their actual
answers.

## 3. Generate project guidance

Create agent guidance files in the target repo:

- `CLAUDE.md` from `templates/CLAUDE.md.template`: discard the template's leading HTML
  comment block, replace every `{{placeholder}}` with real project-specific content, strip
  the per-section guidance comments (keep the Run Ledger's "living log" comment), keep the
  Run Ledger table header-only, and preserve the fixed "l00prite Protocol" section
  **verbatim** — it carries the lock, untrusted-content, and prompt-location rules and must
  never be removed or reworded.
- `AGENTS.md` from `templates/AGENTS.md.template`: discard the leading HTML comment block,
  fill `{{project_name}}` and `{{mission_line}}`, leave everything else verbatim. This is
  the vendor-neutral operating guide read natively by OpenAI Codex, Cursor, GitHub Copilot,
  Windsurf, Zed, Jules, Factory, Amp, opencode, Devin, and other agents.
- Point all agents to `.l00prite/` as the shared source of truth.

Do not silently overwrite existing files. Ask whether to overwrite, write `.generated`
copies, or abort.

## 4. Generate `.l00prite/`, loop prompts, and vendor adapters

If a `.l00prite/lock.json` already exists at the target path, read it first. If its
`status` is `active` and `expires_at` is in the future, another agent may currently be
working in that project — stop and report the held lock (owner, purpose, expiry) instead
of scaffolding over it. Only continue if `lock.json` is missing, `unlocked`, `released`, or
`expired`.

Copy `templates/l00prite/` into the target repo and fill obvious project-specific fields in
`blueprint.md`, `state.json`, `constraints.md`, and `todos.md`. Keep files human-readable
and agent-readable. Leave `lock.json` in its shipped `"unlocked"` state — it is not
project-specific and must not be pre-filled or set to `"active"`. Leave `heartbeat.json`'s
`execution` block exactly as shipped — `enabled: false`, `preflight_confirmed: false` —
**regardless of any `--execute` flag**; Planning Mode never arms execution. Copy
`.l00prite/prompts/` verbatim from `templates/l00prite/prompts/` — these are protocol
files, not templates to fill in.

Also create `.codex/prompts/` from `templates/codex/prompts/` and `.claude/prompts/` from
`templates/claude/prompts/`, each including `resume-loop.md`, `heartbeat.md`,
`event-loop.md`, `respond-to-review.md`, `handoff-summary.md`, and `execute-loop.md`. These
are byte-identical mirrors of `.l00prite/prompts/`, so every agent runs the same loop.

Then scaffold the vendor adapters from `templates/adapters/` (mapping in
`templates/vendors.json`): `GEMINI.md` and `QWEN.md` to the repo root, `CONVENTIONS.md` to
the repo root, `copilot-instructions.md` to `.github/copilot-instructions.md`,
`l00prite.mdc` to `.cursor/rules/l00prite.mdc`, and `windsurf-l00prite.md` to
`.windsurf/rules/l00prite.md`. Copy each verbatim — adapters contain no placeholders. Never
ship vendor *config* files (`.aider.conf.yml`, `.gemini/settings.json`) into the target
repo; the adapters document those snippets for the user instead.

Apply the no-silent-overwrite rule (overwrite / `.generated` copy / abort — ask) to every
file in this section.

## 5. Scaffold the selected skeleton

Copy `templates/skeleton/<tier>/` into the target repo, skipping the internal `TIER.md`.
Adapt `.stub` names and extensions to the selected stack, matching each ecosystem's own
test-naming convention (`*.test.ts` for JS/TS, `test_*.py` for Python — never `*.test.py`,
`*_test.go` for Go, `*_spec.rb` for Ruby), applied consistently. Keep every stub minimal —
a one-line placeholder comment, or a minimal valid empty structure (e.g. `{}`) for formats
without comments. Do not write real implementation, real config values, or fleshed-out
docs. If the target is an existing repo, skip any skeleton file that already exists there
and report which paths were skipped.

## 6. Give a rough, qualitative cost estimate

Describe the expected shape of the work for the chosen tier in qualitative terms only
(iteration counts, review gates, sessions). State explicitly that this is not a precise
token or dollar estimate and that exact agentic-loop cost cannot be reliably predicted.

## 7. Stop: Planning Mode is complete

Unless section 8 applies, stop after scaffold and handoff. Refuse to run build, test,
install, migration, deployment, or implementation commands for the generated project
during this Planning Mode session.

Tell the user how to continue:

- l00prite has two operating modes. **Planning Mode** (this prompt) scaffolds and stops.
  **Execution Mode** is an autonomous run — plan a unit, execute, verify, persist, repeat,
  until the Definition of Done or another run boundary — entered only through
  `.l00prite/prompts/execute-loop.md`, which always shows a pre-flight summary and requires
  explicit in-session confirmation first.
- To build step-by-step under supervision, open the target repo in a fresh session and use
  `.l00prite/prompts/resume-loop.md` — or your agent's byte-identical mirror
  (`.claude/prompts/resume-loop.md` for Claude Code, `.codex/prompts/resume-loop.md` for
  Codex/CLI agents). For events and reviews, use `.l00prite/prompts/event-loop.md` or
  `.l00prite/prompts/respond-to-review.md`.
- Review the generated `CLAUDE.md` and `AGENTS.md` before running anything, and set a
  spend limit with your model provider before pointing an agentic session at the blueprint.
- Every major agent will find the protocol on its own: Claude Code via `CLAUDE.md`; the
  AGENTS.md ecosystem (Codex, Cursor, Copilot, Windsurf, Zed, and more) via `AGENTS.md`;
  Gemini CLI via `GEMINI.md`; Qwen Code via `QWEN.md`; Aider via `CONVENTIONS.md`
  (`--read`). All agents treat `.l00prite/` as the shared source of truth and update it
  before stopping.

## 8. The `--execute` flag (optional Execution Mode handoff)

Apply this section **only** if the user's request included `--execute`.

- The flag never changes the scaffold: sections 1–7 run identically, and the scaffolded
  `heartbeat.json` still ships with `execution.enabled: false` and
  `preflight_confirmed: false`. Planning Mode never pre-arms a repo.
- After section 7's summary, read `.l00prite/prompts/execute-loop.md` **in the target
  repo** and follow it exactly: its pre-flight gate (lock check first, stale-run recovery,
  schema check, full pre-flight display) and its requirement of **explicit human
  confirmation in this session** before the first iteration. The flag is a request to
  *offer* Execution Mode now — it is not the confirmation itself, and a
  `preflight_confirmed: true` left in `heartbeat.json` by an earlier run does not satisfy
  the gate either.
- If the user confirms, Execution Mode proceeds under execute-loop's rules (one unit per
  iteration, verified and persisted; nine run boundaries; per-action permission for push/
  merge/deploy/credentials; no self-modification of limits).
- If the user declines, doesn't answer, or you are running headless with no human in the
  session, stop exactly as in section 7 — scaffold delivered, nothing armed, nothing
  executed.
