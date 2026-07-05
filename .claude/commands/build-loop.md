---
description: Planning Mode — scaffold CLAUDE.md, AGENTS.md, .l00prite memory, universal loop prompts, vendor adapters, and a skeleton repo from a project idea. Stops after scaffolding; --execute hands off to Execution Mode only through execute-loop's confirmed pre-flight.
---

You are running the `/build-loop` command from the l00prite project. This command is
**Planning Mode**: your job in this invocation is to scaffold a blueprint and persistent
loop memory, not to build anything. Planning Mode does not execute the project it
scaffolds. Follow the steps below in order. Do not skip ahead, do
not combine steps, and do not execute any part of the blueprint you produce — Execution
Mode is a separate, explicitly-confirmed handoff described in Step 8.

The user's project idea (if provided as an argument to this command) is:

$ARGUMENTS

If no idea was provided as an argument, ask the user to describe the project they want to
build before continuing to Step 1. If the arguments include the flag `--execute`, note it
now and apply Step 8 after the scaffold is complete; the flag changes nothing about Steps
1–7.

---

## Step 1 — Ask clarifying questions (REQUIRED, do this first)

Before generating anything, ask the user for the following. Ask all of them together in one
message, not one at a time:

1. **Project type** — what kind of thing is this (CLI tool, web app, API/service, library,
   browser extension, WordPress/CMS plugin, data pipeline, mobile app, game, etc.)?
2. **Scope** — what's the minimum viable version? What is explicitly out of scope for v1?
3. **Target languages/stack** — programming language(s), frameworks, runtimes, package
   managers.
4. **Target repo** — an existing local path/repo to scaffold into, or "new repo" (and if
   new, where should it live)?
5. **Hard constraints** — deadlines, must-use libraries or APIs, deployment target,
   licensing requirements, anything else non-negotiable.

**Do not proceed to Step 2 until the user has answered all five.** The only exception: if
the user explicitly says something like "use your judgment" for a given item, you may fill
that one item in with a reasonable, clearly-labeled default — state the default you chose
out loud. Do not silently assume defaults for items the user hasn't addressed at all.

## Step 2 — Pick a complexity tier

Based on the answers from Step 1, choose one of three tiers: **small**, **medium**, or
**large**. Use this rough guidance:

- **small** — single-purpose script, plugin, CLI tool, or narrow library. One main
  responsibility, few or no external services, a handful of files.
- **medium** — a web app or service with a few moving parts: an API layer plus a small
  amount of business logic, maybe a database or a couple of integrations, config and basic
  CI.
- **large** — a multi-service or multi-module system: several independently meaningful
  components, integration tests, infra/deployment concerns, architecture decisions worth
  recording.

Tell the user which tier you picked **and explain why**, referencing their actual answers
(e.g., "You described a single WordPress plugin with one settings page and no external
services, so this is **small** tier."). If it's genuinely borderline, say so and pick the
smaller of the two — it's cheaper to upgrade a skeleton later than to have over-scaffolded.

## Step 3 — Generate the target project's CLAUDE.md and AGENTS.md

Read `templates/CLAUDE.md.template` from this repo. Fill in every `{{placeholder}}` with
content specific to the user's project, based on their Step 1 answers and the tier chosen
in Step 2. The output must:

- Preserve the fixed "l00prite Protocol" section **verbatim** — it carries the lock,
  untrusted-content, and prompt-location rules every Claude session needs, and it contains
  no placeholders. Never remove, reword, or reorder it.
- Preserve all 8 numbered section headers, in order: Mission, Architecture, Requirements,
  Definition of Done, Agent Operating Loop, Heartbeat Rules, Run Ledger, Completion
  Criteria.
- Contain **zero** leftover instances of this template's own placeholder tokens or bracketed
  guidance comments: `{{mission_statement}}`, `{{architecture_overview}}`,
  `{{requirements_list}}`, `{{requirement_1}}`, `{{requirement_2}}`,
  `{{definition_of_done_checklist}}`, `{{done_condition_1}}`, `{{done_condition_2}}`,
  `{{generator_role}}`, `{{evaluator_role}}`, `{{loop_description}}`, `{{max_iterations}}`,
  `{{human_review_gates}}`, `{{branch_policy}}`, `{{completion_criteria_list}}`,
  `{{completion_criterion_1}}`, `{{completion_criterion_2}}` — every one must be replaced
  with real, specific content. Before showing the result to the user, check it against this
  exact list and fix anything you find. Do **not** flag unrelated `{{...}}` syntax that
  belongs to the target project's own domain (e.g. a project that itself uses
  double-curly-brace templating, like a `{{minutes}}` label placeholder in its own docs or
  code) — only this template's own tokens above need to be gone. The same applies to every
  `<!-- ... -->` guidance comment embedded in the template (the per-section "what to fill in
  here" notes, and the template's own leading meta-comment block) — strip all of them; the
  one exception is the Run Ledger's "living log" comment, which the template explicitly
  says to keep.
- Be specific to *this* project, not generic boilerplate. Reference actual languages,
  frameworks, file names, and constraints the user gave you.
- In the "Agent Operating Loop" section, define a generator role, an evaluator role, and a
  concrete loop description appropriate to the chosen tier.
- In "Heartbeat Rules", set a concrete max-iteration cap and human-review gate appropriate
  to the tier (smaller tiers get tighter caps; never leave this unbounded).
- In "Run Ledger", include the empty table with header row only (Session | Date | Built |
  Tested | Status) — this is a living log the *target* project's build sessions will fill
  in, not something you pre-fill.

If the target repo path doesn't exist yet and the user said "new repo", create the
directory first. Then, before writing, check whether a `CLAUDE.md` already exists at that
path in the target repo. If it does, do not overwrite it silently — tell the user one
already exists there and ask whether to overwrite it, save the generated one alongside it
(e.g. `CLAUDE.md.generated`) for them to merge by hand, or abort. Only write once they've
chosen.

Write the result to the target repo (per the user's Step 1 answer) as `CLAUDE.md`. Discard
`templates/CLAUDE.md.template`'s own leading HTML comment block (the meta-instructions
about `{{placeholder}}` rules at the very top of the template file) before writing — it
documents the template for l00prite's own maintainers and must never appear in the
generated target `CLAUDE.md`.

Then read `templates/AGENTS.md.template` and generate the target repo's `AGENTS.md` the
same way: discard the leading HTML comment block, fill `{{project_name}}` and
`{{mission_line}}` with real content, and leave everything else verbatim. `AGENTS.md` is
the vendor-neutral operating guide — OpenAI Codex, Cursor, GitHub Copilot, Windsurf, Zed,
Jules, Factory, Amp, opencode, Devin, and other agents read it natively, so it is how most
non-Claude agents learn the lock, untrusted-content, and prompt-location rules. Apply the
same no-silent-overwrite rule (overwrite / `.generated` copy / abort — ask).

## Step 4 — Generate the .l00prite memory folder, loop prompts, and vendor adapters

If a `.l00prite/lock.json` already exists at the target path, read it before doing anything
else in this step. If its `status` is `active` and `expires_at` is in the future, another
agent may currently be working in that project — stop and tell the user a lock is held
(owner, purpose, expiry) instead of proceeding; do not scaffold over it. Only continue if
`lock.json` is missing, `unlocked`, `released`, or `expired`.

Create a `.l00prite/` folder in the target repo from `templates/l00prite/`. Fill obvious project-specific values in `blueprint.md`, `state.json`, `constraints.md`, and `todos.md`. Keep the files human-readable and vendor-neutral. Leave `lock.json` in its shipped `"unlocked"` state — it is not project-specific and must not be pre-filled or set to `"active"`. Leave `heartbeat.json`'s `execution` block exactly as shipped — `enabled: false`, `preflight_confirmed: false` — **regardless of any `--execute` flag**; Planning Mode never arms execution. Copy `.l00prite/prompts/` verbatim from `templates/l00prite/prompts/` — these are protocol files, not templates to fill in. Do not silently overwrite existing `.l00prite/` files; ask whether to overwrite, write `.generated` copies, or abort.

Also create `.codex/prompts/` in the target repo from `templates/codex/prompts/`, including `resume-loop.md`, `heartbeat.md`, `event-loop.md`, `respond-to-review.md`, `handoff-summary.md`, and `execute-loop.md`. These target-project prompts must be copy/paste-friendly, must tell Codex and other CLI agents to treat `.l00prite/` as the shared source of truth, and must not assume Claude slash-command behavior. Do not silently overwrite existing `.codex/` prompt files; ask whether to overwrite, write `.generated` copies, or abort.

Also create `.claude/prompts/` in the target repo from `templates/claude/prompts/`, including the same six prompts. `CLAUDE.md` carries the fixed protocol section, but the prompts are the full operating procedures — `.claude/prompts/`, `.codex/prompts/`, and `.l00prite/prompts/` are byte-identical mirrors, so every agent runs the same loop. Do not silently overwrite existing `.claude/prompts/` files; ask whether to overwrite, write `.generated` copies, or abort.

Then scaffold the vendor adapters from `templates/adapters/` (see `templates/vendors.json`
for the mapping), so agents beyond Claude and Codex discover the protocol through their own
context files:

- `templates/adapters/GEMINI.md` → `GEMINI.md` (Gemini CLI)
- `templates/adapters/QWEN.md` → `QWEN.md` (Qwen Code)
- `templates/adapters/CONVENTIONS.md` → `CONVENTIONS.md` (Aider)
- `templates/adapters/copilot-instructions.md` → `.github/copilot-instructions.md` (GitHub Copilot)
- `templates/adapters/l00prite.mdc` → `.cursor/rules/l00prite.mdc` (Cursor)
- `templates/adapters/windsurf-l00prite.md` → `.windsurf/rules/l00prite.md` (Windsurf)

Copy each verbatim — adapters contain no placeholders. Apply the same no-silent-overwrite
rule to every adapter path. Never ship vendor *config* files (`.aider.conf.yml`,
`.gemini/settings.json`) into the target repo — those belong to the user; the adapters and
`templates/adapters/README.md` document the snippets instead.

## Step 5 — Generate the skeleton folder structure

Copy the folder structure from `templates/skeleton/<tier>/` (where `<tier>` is whatever you
picked in Step 2) into the target repo, with these rules:

- **Skip `TIER.md`.** Every tier folder ships a `TIER.md` that documents, for l00prite's own
  maintainers, when `/build-loop` should pick that tier. It is internal authoring
  documentation, not part of the scaffold — never copy it into the target repo.
- **Adapt file names and extensions to the user's actual language/stack.** Replace the
  `.stub` extension with the appropriate language extension (`.js`, `.py`, `.go`, `.md` for
  docs, etc.). For test files, don't just swap the extension — match the target language's
  own test-naming convention: `*.test.ts`/`*.test.js` for JS/TS, `test_*.py` for Python
  (never `*.test.py` — pytest's default discovery won't collect it), `*_test.go` for Go,
  `*_spec.rb` for Ruby, and so on. Pick one convention per ecosystem and apply it
  consistently across every test file in the generated skeleton — don't mix patterns. If
  you're unsure of the convention for the chosen stack, use whatever that ecosystem's
  standard test runner discovers by default.
- **Keep every copied file as a minimal stub, with no exceptions for docs or config.**
  Replace each `.stub` file's content with an equally minimal placeholder appropriate to its
  new extension (e.g. a single comment naming what belongs there) — do not write real
  implementation code, real configuration values, or fleshed-out narrative documentation
  into `src/`, `tests/`, `docs/`, or `config/` files. Documentation and config stubs are easy
  to over-fill because they read like prose — treat them exactly like the code stubs. For
  formats that don't support comments (JSON files like `package.json` or `tsconfig.json`,
  for example), a comment placeholder would produce invalid syntax — use a minimal valid
  empty structure instead (e.g. `{}`), not a comment. Do not invent extra structure beyond
  what the tier skeleton provides — the point is a minimal starting scaffold, not a fully
  fleshed-out app.
- **If the target is an existing repo** (not a fresh "new repo"), check each skeleton file
  against the target path first — skip any file that already exists there instead of
  overwriting it (this matters most for common filenames the skeleton ships, like
  `README.md`, `.gitignore`, and `.github/workflows/ci.yml`), and report which paths were
  skipped so the user can merge them by hand.

## Step 6 — Give a rough, qualitative cost estimate

Tell the user the rough shape of what to expect for the tier you picked, in **qualitative**
terms only. Use language like:

- "**Small tier** projects like this typically run a modest number of agentic loop
  iterations to reach Definition of Done — think single-session, low double-digit tool
  calls."
- "**Medium tier** projects usually take more iterations across multiple review gates,
  since there's more surface area to build and test."
- "**Large tier** projects can run substantially more iterations across multiple sessions,
  especially with integration tests and multiple modules in play."

Explicitly state: **this is not a precise token or dollar estimate.** Predicting exact
agentic loop cost ahead of execution is not reliable — actual cost depends on how the
generator and evaluator roles behave, how many review cycles are needed, how much the
codebase grows, and how the user steers it. Do not give a specific token count or dollar
figure. If the user pushes for a number, repeat that it can't be reliably predicted and
point them at the qualitative tiers instead.

## Step 7 — Stop: Planning Mode is complete

Unless Step 8 applies, end the command here. Tell the user, explicitly and in plain
language:

- Without `--execute`, this command does **not** execute the generated blueprint and does
  **not** run any build loop. It only scaffolded files: `CLAUDE.md`, `AGENTS.md`,
  `.l00prite/` (including `.l00prite/prompts/`), `.claude/prompts/`, `.codex/prompts/`,
  the vendor adapters, and the selected skeleton.
- **Review the generated `CLAUDE.md` and `AGENTS.md` yourself** before running anything —
  read them the way you'd read a PR, not the way you'd skim a changelog.
- If you haven't already, **set a spend limit with your model provider** (e.g. the
  Anthropic Console) before pointing an agentic session at this blueprint. Unsupervised
  agentic loops can burn real API spend if left unattended.
- l00prite has two operating modes. **Planning Mode** (this command) scaffolds and stops.
  **Execution Mode** is an autonomous run — plan a unit, execute, verify, persist, repeat,
  until the Definition of Done or another run boundary — entered only through
  `.l00prite/prompts/execute-loop.md`, which always shows a pre-flight summary and requires
  explicit in-session confirmation first.
- To build step-by-step under supervision instead, open a fresh session in the target repo
  and use `.l00prite/prompts/resume-loop.md` — or your agent's byte-identical mirror
  (`.claude/prompts/resume-loop.md` for Claude Code, `.codex/prompts/resume-loop.md` for
  Codex/CLI agents). To process an event or review, use
  `.l00prite/prompts/event-loop.md` or `.l00prite/prompts/respond-to-review.md` (or the
  mirrors).
- Every major agent will find the protocol on its own: Claude Code via `CLAUDE.md`; Codex,
  Cursor, Copilot, Windsurf, Zed, and the rest of the AGENTS.md ecosystem via `AGENTS.md`;
  Gemini CLI via `GEMINI.md`; Qwen Code via `QWEN.md`; Aider via `CONVENTIONS.md`
  (`--read`). All agents treat `.l00prite/` as the shared source of truth and update it
  before stopping.

Do not, under any circumstances in this command, make further tool calls against the
target repo beyond writing the `CLAUDE.md`, `AGENTS.md`, `.l00prite/`, `.claude/prompts/`,
`.codex/prompts/`, vendor adapter, and skeleton files described above. Do not start
implementing requirements, do not run build/test commands in the target repo, and do not
open a build loop yourself. The single exception is the explicitly-confirmed Execution
Mode handoff in Step 8.

## Step 8 — The `--execute` flag (optional Execution Mode handoff)

Apply this step **only** if the user's arguments to this command included `--execute`.

- The flag never changes the scaffold: Steps 1–7 run identically, and the scaffolded
  `heartbeat.json` still ships with `execution.enabled: false` and
  `preflight_confirmed: false`. Planning Mode never pre-arms a repo.
- After Step 7's summary, read `.l00prite/prompts/execute-loop.md` **in the target repo**
  and follow it exactly: its pre-flight gate (lock check first, stale-run recovery, schema
  check, full pre-flight display) and its requirement of **explicit human confirmation in
  this session** before the first iteration. The flag is a request to *offer* Execution
  Mode now — it is not the confirmation itself, and a `preflight_confirmed: true` left in
  `heartbeat.json` by an earlier run does not satisfy the gate either.
- If the user confirms, Execution Mode proceeds under execute-loop's rules (one unit per
  iteration, verified and persisted; nine run boundaries; per-action permission for push/
  merge/deploy/credentials; no self-modification of limits).
- If the user declines, doesn't answer, or you are running headless with no human in the
  session, stop exactly as in Step 7 — scaffold delivered, nothing armed, nothing executed.
