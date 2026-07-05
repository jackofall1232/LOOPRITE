# Run Ledger

Append one entry per agent run. Do not overwrite prior runs.

## Entry Template

### Run YYYY-MM-DDTHH:MM:SSZ — <agent name>
- **Goal:** What this run attempted.
- **Triggering event:** Event id/type/source, or `none` for normal roadmap work.
- **Reviewer/comment reference:** PR, issue, CI run, reviewer, URL, file/line, or `none`.
- **Decision:** Valid, already fixed, unclear, unsafe, blocked, deferred, stale-lock-recovery, or normal work; include why.
- **Completed work:** What changed or was learned.
- **Fix implemented:** The smallest fix made for the event, or `none` with reason.
- **Changed files:** Files created, modified, deleted, or intentionally left untouched.
- **Tests run / Verification:** One entry per check run, each with `command`, `exit_code`,
  `summary`, `evidence_path` (optional), and `timestamp`. Do not write vague statements like
  "tests passed" without at least `command`, `exit_code`, and `summary`.
- **Response drafted/sent:** Reviewer, issue, or human response status and summary.
- **Event status:** Pending, processing, completed, blocked, deferred, or not applicable.
- **Failures:** Errors, blockers, failed approaches, or skipped checks.
- **Decisions:** Durable decisions made during the run.
- **Confidence:** Low/medium/high plus a short reason.
- **Next action:** The next smallest useful step.
- **Do-not-retry notes:** Failed approaches that should not be repeated unless conditions change.
- **Lock:** `lock_id` acquired/released this run, or `none` if no protected-path write occurred. Note stale-lock reclamation here if applicable.

## Runs

### Run 2026-07-04T02:00:00Z — Claude (Opus 4.8), branch claude/looprite-cli-os-jntwqi
- **Goal:** Implement l00prite CLI-OS v1.0.0 as a runnable, tested product ("make it ready to
  ship") — a real OpenAI-compatible gateway with provider adapters, repo memory, explainable
  routing, real cost tracking, a Policy Enforcement Point, security, a CLI control surface, the
  served dashboard, tests, and Docker/install packaging.
- **Triggering event:** none — direct maintainer instruction in-session ("go ahead and make this
  the full production release make it ready to ship").
- **Reviewer/comment reference:** none.
- **Decision:** Normal work, large build. The maintainer's "ship it" authorized proceeding on
  the recommended defaults; recorded the one transparent change (runtime = zero-dep Node instead
  of the recommended Go) because the build environment blocks module fetch + live-provider egress
  (Go not buildable/testable here) while Node runs natively, matches the existing validator, and
  gives real ACID via built-in node:sqlite. Decisions logged in `cli-os/docs/open-questions.md`
  and `cli-os/RELEASE.md`.
- **Completed work:** Built the full `cli-os/` runtime (~20 modules): OpenAI-compatible ingress
  (streaming + non-streaming, idempotency-aware retry), Anthropic native `/v1/messages`
  translator (SSE blocks → OpenAI chunks) + OpenAI-compatible passthrough + zero-key mock
  upstream, explainable router + circuit breaker, real-usage cost meter, Policy Enforcement Point
  (atomic $ caps reserve→commit/refund, leases) on node:sqlite WAL, AES-256-GCM key vault, opaque
  hashed tokens, `.l00prite/` memory retrieval + untrusted-injection, run ledger + audit, admin
  CLI (init/provider/token/repo/cap/route-explain/serve), served dashboard, Dockerfile + compose
  + install script + `.env.example`, and a `node:test` suite.
- **Fix implemented:** not applicable — new implementation. Incidental: switched ledger insert to
  positional binding; added a re-exec launcher so the node:sqlite ExperimentalWarning never
  reaches operators; added `LOOPRITE_ALLOW_INSECURE_BIND` explicit opt-in for container binds.
- **Changed files:** created `cli-os/{package.json,bin/,src/,test/,public/,install/,Dockerfile,
  docker-compose.yml,.env.example,.gitignore,.dockerignore,RELEASE.md}`; modified
  `cli-os/README.md`, `cli-os/docs/open-questions.md`, and this ledger. No existing protocol
  files, prompts, templates, `.claude/commands/build-loop.md`, or `scripts/validate-l00prite.js`
  touched.
- **Tests run / Verification:**
  - `command`: `npm test` (node:test — vault, tokens, PEP cap enforcement, meter, Anthropic
    request+SSE translation, memory, full e2e server run over the mock upstream)
  - `exit_code`: 0
  - `summary`: 12 pass, 0 fail. e2e covers auth 401, non-stream 200 + ledger, streaming SSE +
    [DONE], cost-cap 402, /healthz, /v1/models.
  - `evidence_path`: `cli-os/test/`
  - `timestamp`: 2026-07-04T02:00:00Z
  - `command`: `node scripts/validate-l00prite.js` (protocol regression guard)
  - `exit_code`: 0
  - `summary`: 0 FAIL — CLI-OS subtree does not affect the prompt-protocol invariants.
  - `timestamp`: 2026-07-04T02:00:00Z
  - `command`: manual smoke — init → provider add → repo register → token mint → serve → curl
    /v1/chat/completions (non-stream + stream), /healthz, dashboard, ledger; safe-bind refusal +
    opt-in
  - `exit_code`: 0
  - `summary`: full operator flow works; server refuses non-loopback bind without TLS and serves
    only under the explicit opt-in.
  - `timestamp`: 2026-07-04T02:00:00Z
- **Response drafted/sent:** implementation summary + honest ship caveats to the maintainer; no
  PR opened (not requested).
- **Event status:** not applicable.
- **Failures:** Live-provider round-trips could NOT be executed — the build environment blocks
  egress to provider domains (403). Adapter translation is unit-tested and the pipeline is
  e2e-tested against the mock upstream, but a real-key smoke test must run in a networked
  environment before production traffic. Provider pricing (except Anthropic) remains unconfirmed.
- **Decisions:** runtime = zero-dep Node; providers = framework + Anthropic native + OpenAI-compat
  + mock; quality = static config rank; `/v1/responses` deferred to v2; memory = naive v1;
  cost cap = hard-block. Recorded in `cli-os/RELEASE.md` and `docs/open-questions.md`.
- **Confidence:** High for the offline-provable surface (tests + validator + smoke). Medium for
  production-at-scale until a networked live-provider smoke test and first-party pricing pass run.
- **Next action:** Maintainer runs a live-provider smoke test with real keys in a networked env,
  confirms provider pricing (Q7), and decides on a PR / release tag. Optional follow-ups: wire the
  dashboard to live `/healthz`+ledger data; `/v1/responses`; embeddings.
- **Do-not-retry notes:** Do not claim live-provider readiness without an egress-enabled smoke
  test; do not backfill provider pricing from training-data memory (manifests keep unconfirmed
  prices null and cost is flagged estimated).
- **Lock:** none acquired. CLI-OS work is in the `cli-os/` subtree (not a lease-protected path);
  the only protected-path write was this `.l00prite/ledger.md` append in a single-agent session.

### Run 2026-07-04T00:00:00Z — Claude (Opus 4.8), branch claude/looprite-cli-os-jntwqi
- **Goal:** Design pass for l00prite CLI-OS — turn the scaffold-only memory protocol into a
  self-hostable coding gateway (OpenAI-compatible endpoint + repo memory + routing + cost
  tracking + safety policy). Deliver an architecture doc, module layout, scoped v1 plan, and
  open questions; verify provider API specs (especially "GLM 5.2") before building against
  them. Report back before writing implementation code beyond adapter-approach validation.
- **Triggering event:** none — direct maintainer build brief in-session (CLI-OS).
- **Reviewer/comment reference:** none.
- **Decision:** Normal work, design-only deliverable. Confirmed the repo is prompt-files +
  JSON + the dependency-free validator with no server/agent runtime, so CLI-OS is greenfield
  runtime code placed in a new non-interfering `cli-os/` subtree. Ran a fan-out research pass
  (Opus researchers; Fable 5 assigned adversarial-verify) to verify current provider specs
  from primary sources.
- **Completed work:** Wrote `cli-os/` docs — `architecture.md` (two-track Gateway/Memory
  design, request lifecycle, PEP enforcement, module boundaries), `interface-contract.md`
  (`MemoryQuery`/`MemoryContext`), `provider-adapters.md` (verified specs + caveats),
  `routing-rules-v1.md`, `security-model.md`, `v1-scope.md`, `open-questions.md`; `README.md`
  with the module tree; module stubs (`gateway/`, `memory/`, `policy/`); and verified example
  provider manifests (`anthropic.json`, `openai.json`, `zhipu.json`). Verified **GLM 5.2 is
  real** (`glm-5.2` in Zhipu's official SDK). Confirmed Anthropic needs a full native
  `/v1/messages` adapter (its OpenAI-compat endpoint is test/eval-only) and OpenAI's dual
  `/v1/chat/completions` vs `/v1/responses` surfaces.
- **Fix implemented:** not applicable — design deliverable, no triggering defect.
- **Changed files:** created `cli-os/**` (docs, README, module stubs, provider manifests);
  modified `.l00prite/ledger.md` (this entry) and `.l00prite/todos.md`. No existing protocol
  files, templates, prompts, `.claude/commands/build-loop.md`, or
  `scripts/validate-l00prite.js` were touched (human-review-gated files left untouched).
- **Tests run / Verification:**
  - `command`: `node scripts/validate-l00prite.js`
  - `exit_code`: 0
  - `summary`: still passes with zero FAIL — CLI-OS lives in a separate subtree the validator
    does not inspect, so the protocol's invariants are unaffected.
  - `evidence_path`: none (console output only).
  - `timestamp`: 2026-07-04T00:00:00Z
  - `command`: provider-spec verification (fan-out web research over vendor OpenAPI specs + SDKs)
  - `exit_code`: n/a
  - `summary`: API shapes high-confidence (from first-party OpenAPI/SDKs on GitHub); pricing for
    several providers third-party/unconfirmed because their first-party doc domains were
    egress-blocked and the proxy denials were respected, not routed around.
  - `evidence_path`: `cli-os/docs/provider-adapters.md` (verification caveats section).
  - `timestamp`: 2026-07-04T00:00:00Z
- **Response drafted/sent:** architecture summary + open questions returned to the maintainer;
  no PR opened (not requested).
- **Event status:** not applicable.
- **Failures:** Fable 5 adversarial-verify verdicts did not complete — the verifiers hit the
  same egress blocks and ran long; the pass was stopped and the researchers' own confidence
  self-assessments used instead. Provider pricing remains unconfirmed pending a first-party pass.
- **Decisions:** two-track (Gateway/Memory) with a typed latency-bounded interface as the
  seam; PEP enforces cost/retry/destructive gates outside the deciding process (dollars, not
  tokens); explainable non-ML routing v1; Anthropic full native adapter vs thin shims for
  OpenAI-compatible providers; CLI-OS supersedes the "no backend" constraint for the new
  subtree only (needs maintainer blessing — recorded as assumption A1).
- **Confidence:** High for architecture/module boundaries and for the GLM 5.2 existence
  finding (primary-source SDK). Medium on provider pricing (third-party, unconfirmed).
- **Next action:** Maintainer answers the open questions (esp. Q1 provider set, Q2 "quality"
  definition, Q3 runtime language) before implementation of the Gateway/Memory tracks begins.
- **Do-not-retry notes:** Do not hardcode provider pricing from training-data memory — the
  manifests deliberately leave unconfirmed prices null/flagged pending a first-party pass.
- **Lock:** none acquired. All CLI-OS work is in the new `cli-os/` subtree, which is not a
  lease-protected path. The only protected-path write was appending this `.l00prite/ledger.md`
  entry (and a `todos.md` line) in a single-agent session with no concurrent writer.

### Run 2026-07-02T00:00:00Z — Claude (Fable), branch claude/powerful-helper-agent-pfsyj1
- **Goal:** v1.1 — make l00prite the most powerful helper protocol for all AI models, per
  the maintainer's direction: evolve from scaffold-and-stop into a two-mode execution
  protocol ("an operating system for autonomous software engineering") with a universal
  vendor layer, while keeping the discipline inside the execution protocol itself.
- **Triggering event:** none — direct maintainer request in-session (initial request plus a
  mid-session direction message setting the execution-first vision).
- **Reviewer/comment reference:** none.
- **Decision:** Normal work, large scope, executed as one reviewed branch. The maintainer's
  direction message explicitly authorized touching the two review-gated files
  (`.claude/commands/build-loop.md`, `scripts/validate-l00prite.js`) on this branch;
  maintainer review before merge still applies. An adversarial three-critic design review
  ran before implementation; its blockers reshaped the design (see `failures.md` for the
  rejected shapes and `memory.md` for the decisions kept).
- **Completed work:** Canonical prompt layer (`templates/l00prite/prompts/`, 7 files) with
  byte-identical mirrors in six locations; new `execute-loop.md` (pre-flight gate, nine run
  boundaries, iteration protocol, resumable exits, self-modification guard) plus
  `/execute-loop` command; schema v2 (`execution` block in `heartbeat.json`,
  execution-run fields in `state.json`, all three copies each); `AGENTS.md.template` +
  fixed protocol section in `CLAUDE.md.template`; vendor adapters
  (Gemini/Qwen/Copilot/Cursor/Windsurf/Aider) + `templates/vendors.json`, dogfooded at repo
  root and mirrored in the example output; both build-loop variants reframed as Planning
  Mode with the `--execute` gate-only handoff (Codex variant strengthened to Claude
  parity); validator extended 209 → 519 checks (byte-parity, adapter integrity,
  execution invariants, both build-loops); README/AGENTS.md/CLAUDE.md/HANDOFF.md/RELEASE.md
  reframed around the two operating modes; `.l00prite/` memory updated (this entry,
  todos, memory, failures, blueprint, state, heartbeat).
- **Fix implemented:** not applicable — feature pass, no triggering defect. Incidental
  fixes: dangling bare-filename prompt references in `.l00prite/README.md` and
  `reviews/README.md`; hardcoded `.codex/prompts/` next-prompt paths inside all
  heartbeat.md copies.
- **Changed files:** see the branch's commit series (each commit carries its own
  verification note); summary in `HANDOFF.md`.
- **Tests run / Verification:**
  - `command`: `node scripts/validate-l00prite.js`
  - `exit_code`: 0
  - `summary`: 519 PASS, 0 FAIL (was 209 PASS before this pass; 498 before the post-review fix round).
  - `evidence_path`: none (console output only).
  - `timestamp`: 2026-07-02T00:00:00Z
  - `command`: `cmp` across all 6 mirror locations × 6 prompts (+ README × 3)
  - `exit_code`: 0
  - `summary`: all mirrors byte-identical to canonical.
  - `evidence_path`: none.
  - `timestamp`: 2026-07-02T00:00:00Z
  - `command`: negative tests — injected drift into a prompt mirror, an adapter dogfood
    copy, and `.l00prite/heartbeat.json` (`enabled: true`)
  - `exit_code`: 1 (expected) then 0 after restore
  - `summary`: byte-parity, adapter-parity, and disarmed-schema checks each FAIL on the
    injected drift and recover after restore.
  - `evidence_path`: none.
  - `timestamp`: 2026-07-02T00:00:00Z
- **Response drafted/sent:** session summary to the maintainer; no PR opened (not
  requested).
- **Event status:** not applicable.
- **Failures:** none in this run; rejected design shapes recorded in `failures.md` as
  do-not-retry.
- **Decisions:** two operating modes; per-run session-local pre-flight; `--execute` never
  pre-arms; `run_boundaries` naming; byte-parity as the parity mechanism; self-sufficient
  adapters; no vendor config shipped. Details in `memory.md`.
- **Confidence:** High for structural correctness (validator + negative tests + byte-parity
  verification); medium for prose-level consistency across the many rewritten docs — an
  adversarial review pass over the full diff runs before push.
- **Next action:** Maintainer reviews branch `claude/powerful-helper-agent-pfsyj1`
  (including the two review-gated files) and merges to `main` if satisfied.
- **Do-not-retry notes:** see `failures.md` (2026-07-02 entries).
- **Lock:** `lock-20260702-000000-claude-v1.1-memory-update` acquired for this
  memory-update phase and released at its end. Earlier writes in this run touched protocol
  files, templates, and docs — none of them lease-protected paths; the protected-path
  writes (`heartbeat.json`, `state.json` schema bumps and this memory update) happened in a
  single-agent session with no concurrent writer, under this lock where the LOCKING.md
  rules require it.

### Run 2026-07-01T00:00:00Z — Claude/Codex
- **Goal:** Pre-release polish pass — correct `CLAUDE.md` to describe the repo's actual
  state instead of an unbuilt execution-mode feature, update `HANDOFF.md`/`README.md`,
  scaffold a real `.l00prite/` for this repo, add `RELEASE.md`, and confirm the validator
  passes clean before this release is considered mergeable.
- **Triggering event:** none — normal pre-release roadmap work requested by the maintainer.
- **Reviewer/comment reference:** none.
- **Decision:** Normal work. `CLAUDE.md` was found to describe an execution-mode design
  (`--execute` flag, pre-flight confirmation, 8 stop conditions) as though it were this
  session's mission, but no corresponding files exist anywhere in the repo — the design was
  written directly into `CLAUDE.md`'s text in an earlier commit (`87384b4`) without any code
  following it. Corrected rather than left as-is, since an inaccurate `CLAUDE.md` would
  mislead the next agent into thinking execution mode already exists.
- **Completed work:** Rewrote `CLAUDE.md` Sections 1-4, 7, and 8 to describe the four
  protocol layers that actually exist (scaffold, memory, event, handoff) and moved
  execution mode to an explicitly-labeled "not yet built" design note. Appended a new update
  section to `HANDOFF.md` documenting the execution-mode design decision, the lock/lease
  `expired`-state gap Codex found and the Option 1 fix already applied, the ASCII banner
  update, and the new `.l00prite/`. Added execution mode to `README.md`'s roadmap and
  verified the ASCII banner fence and SVG logo line are intact. Scaffolded a real
  `.l00prite/` at repo root (previously only `templates/l00prite/` and
  `examples/vendor-neutral-output/.l00prite/` existed) with this repo's actual blueprint,
  constraints, memory, failures, heartbeat, state, todos, and this ledger entry. Added
  `RELEASE.md` describing v1 scope, what's excluded, getting-started steps, and feedback
  channel.
- **Fix implemented:** Documentation correction and `.l00prite/` scaffolding; no protocol
  code, validator, or prompt logic changed.
- **Changed files:** `CLAUDE.md`, `HANDOFF.md`, `README.md` (modified); `.l00prite/README.md`,
  `blueprint.md`, `constraints.md`, `failures.md`, `heartbeat.json`, `ledger.md`,
  `LOCKING.md`, `lock.json`, `memory.md`, `state.json`, `todos.md`,
  `events/README.md` + `pending/README.md` + `processing/README.md` + `completed/README.md`
  + `example-event.json`, `reviews/README.md` + `github/README.md`, `sessions/README.md`,
  `RELEASE.md` (created). `.claude/commands/build-loop.md` and
  `scripts/validate-l00prite.js` intentionally left untouched per the human-review gate.
- **Tests run / Verification:**
  - `command`: `node scripts/validate-l00prite.js`
  - `exit_code`: 0
  - `summary`: 209 PASS, 0 FAIL.
  - `evidence_path`: none (console output only).
  - `timestamp`: 2026-07-01T00:00:00Z
- **Response drafted/sent:** not applicable — no reviewer/PR event.
- **Event status:** not applicable.
- **Failures:** none.
- **Decisions:** Execution mode is the primary next milestone but out of scope for this
  release; recorded in `CLAUDE.md`, `HANDOFF.md`, and `todos.md` consistently.
- **Confidence:** High — validator passes clean, and all changes are documentation/memory,
  not protocol logic, so regression risk is low.
- **Next action:** Maintainer reviews this pass and the pending changes; if satisfied, merge
  to `main`. After merge, the next roadmap item is designing and building execution mode.
- **Do-not-retry notes:** none.
- **Lock:** none acquired — this run is the bootstrap that creates `.l00prite/lock.json`
  itself, so there was no lock file yet to check before the initial writes to `ledger.md`,
  `todos.md`, `state.json`, `heartbeat.json`, `memory.md`, and `failures.md` in this same
  run. Single-agent session, no concurrent writer to guard against. Lock/lease enforcement
  (check-before-write per `LOCKING.md`) applies starting with the next run, now that
  `lock.json` exists.

### Run 2026-07-04T09:00:00Z — Claude (Opus 4.8, Fable 5 advising), branch claude/l00prite-gaps-analysis-hbv4ej
- **Goal:** Analyze the loop-engineering repo, identify meaningful gaps in l00prite, and
  implement a coherent subset — with Fable 5 as advisor and Opus as the execution model.
- **Triggering event:** none — direct maintainer instruction in-session ("analyze l00prite …
  identify any meaningful gaps and implement them").
- **Reviewer/comment reference:** none.
- **Decision:** Normal work. A multi-agent gap-analysis workflow (Opus mapping + synthesis,
  Fable 5 advisory + prioritization, Opus design) ranked the candidate gaps. Adopted Fable's
  recommended scope: the four highest-value, philosophy-native gaps that touch **zero**
  review-gated files. Fable's key reframes were followed exactly: reuse the existing
  `destructive_operation_required` boundary for path enforcement instead of minting a tenth
  boundary; ship no-progress as additive telemetry now and defer the formal boundary; treat
  agent-self-reported token spend as non-measurable (wall-clock only) and defer budget.
- **Completed work:** Added a readiness/health doctor; loop failure-mode/anti-pattern/concepts
  catalogs; a seeded inherited-failure catalog in `failures.md`; a machine-readable
  Autonomous-Edit Denylist enforced via the existing destructive-operation boundary;
  additive no-progress telemetry fields + execute-loop maintenance + doctor stall check.
  Deferred gated work captured as a single v1.2 batch in `todos.md`.
- **Fix implemented:** n/a — additive capability pass, not an event fix.
- **Changed files:** Added `scripts/l00prite-doctor.js`, `docs/README.md`,
  `docs/failure-modes.md`, `docs/anti-patterns.md`, `docs/concepts.md`. Modified
  `constraints.md` + `failures.md` + `heartbeat.json` (template + example + dogfood each);
  `templates/l00prite/prompts/execute-loop.md` (canonical) re-mirrored byte-identically to all
  7 locations; `README.md`, `AGENTS.md`, `CLAUDE.md`, `HANDOFF.md`, `.l00prite/todos.md`, this
  ledger. Zero-line diff to `.claude/commands/build-loop.md` and `scripts/validate-l00prite.js`.
- **Tests run / Verification:**
  - `command: node scripts/validate-l00prite.js` · `exit_code: 0` · `summary: 519 PASS, 0 FAIL`
    · `timestamp: 2026-07-04T09:00:00Z` (re-run after every file, and after the mirror pass).
  - `command: node scripts/l00prite-doctor.js .` · `exit_code: 0` · `summary: 25 ok, 0 warn,
    0 fail — HEALTHY` · `timestamp: 2026-07-04T09:00:00Z`.
  - `command: node scripts/l00prite-doctor.js examples/vendor-neutral-output` · `exit_code: 0`
    · `summary: 25 ok, HEALTHY (prompt self-parity skipped — example ships no vendor mirrors)`.
  - `command: cmp` across all 7 execute-loop copies · `exit_code: 0` · `summary: single unique
    md5 — byte-parity holds`.
  - Negative test: broke a scaffolded copy 5 ways (armed-without-lock, prompt drift, stall,
    pending mismatch, missing denylist) → doctor reported 3 FAIL + 2 WARN, exit 1, as intended.
- **Response drafted/sent:** none.
- **Event status:** not applicable.
- **Failures:** none. One workflow mapping agent hit a structured-output retry cap; its
  subsystem was covered by the other mappers, so the analysis was unaffected.
- **Decisions:** Do not mint new run boundaries or edit gated files in an ungated pass; reuse
  existing boundaries and additive schema fields, and quarantine all gated work into one v1.2
  batch. Never build a stop condition on self-reported token counts.
- **Confidence:** High — validator passes clean, the doctor self-tests green on this repo and
  the example, and every change is additive or byte-mirror-verified.
- **Next action:** Maintainer reviews this branch. When the ungated pass is accepted, schedule
  the v1.2 gated batch (`todos.md`) as its own review.
- **Do-not-retry notes:** Do not add `budget_exceeded`/`no_progress_detected` as formal
  boundaries without the gated batch (it edits both validator arrays + the gated build-loop).
  Do not add token-spend fields that pretend an agent can measure its own usage.
- **Lock:** none acquired — single-agent session with no concurrent writer; `lock.json`
  remains `released`. Next multi-agent run should acquire before writing protected paths.

### Run 2026-07-04T11:30:00Z — Claude (Opus 4.8), branch claude/l00prite-gaps-analysis-hbv4ej
- **Goal:** Address the PR #16 review from gemini-code-assist, Copilot, and Codex — all
  bot reviewers — without touching either review-gated file.
- **Triggering event:** GitHub PR review events on #16 (webhook subscription).
- **Reviewer/comment reference:** PR #16 review comments from gemini-code-assist[bot],
  Copilot, and chatgpt-codex-connector[bot].
- **Decision:** Valid findings, fixed. Evaluated each against the actual code; all were real
  correctness/robustness gaps, well-scoped, aligned with the protocol's own principles.
- **Completed work / Fix implemented:** Doctor hardening — reject non-object control JSON;
  guard `events/pending` against being a file/unreadable (report, don't crash); validate
  `lock.json` `acquired_at`/`expires_at` as ISO dates; surface `state.blocked`+`blocker_reason`
  and any active unexpired (foreign) lock; ignore the ledger entry-template when checking
  verification evidence (require a real `exit_code`/`evidence_path`, not the field label);
  fail on missing loop-prompt files once a mirror dir exists; validate the denylist's fenced
  block + critical patterns, not just its heading. Execute-loop protocol — stale-run recovery
  now disarms *both* sides (state + heartbeat, incl. `should_continue`); the pre-flight
  backfills no-progress telemetry into an existing older-v2 `execution` block; arming resets
  the no-progress counters so each run starts fresh. Doc: corrected a `failure-modes.md`
  claim about the doctor.
- **Changed files:** `scripts/l00prite-doctor.js`; `docs/failure-modes.md`;
  `templates/l00prite/prompts/execute-loop.md` re-mirrored byte-identically to all 7 copies.
  Zero-line diff to `.claude/commands/build-loop.md` and `scripts/validate-l00prite.js` held.
- **Tests run / Verification:**
  - `command: node scripts/validate-l00prite.js` · `exit_code: 0` · `summary: 519 PASS, 0 FAIL`
    · `timestamp: 2026-07-04T11:30:00Z`.
  - `command: node scripts/l00prite-doctor.js .` · `exit_code: 0` · `summary: HEALTHY (25/0/0)`.
  - `command: node scripts/l00prite-doctor.js examples/vendor-neutral-output` · `exit_code: 0`
    · `summary: HEALTHY (24/0/0)`.
  - `command: cmp across 7 execute-loop copies` · `exit_code: 0` · `summary: single md5`.
  - Negative tests: null heartbeat, pending-as-file, invalid lock dates, foreign active lock,
    gutted denylist, missing prompt mirror, evidence-free ledger run → each reported FAIL/WARN,
    exit 1, no crash.
- **Response drafted/sent:** none posted on GitHub (fixes pushed; commit maps to comments).
- **Event status:** completed for this review round; subscription remains active until merge/close.
- **Failures:** First evidence-regex attempt matched the "Tests run" field label — tightened
  to require `exit_code`/`evidence_path`.
- **Confidence:** High — validator clean, doctor self-tests green, each fix has a negative test.
- **Next action:** Await further review or merge; keep the ~1h self check-in armed.
- **Lock:** none acquired — single-agent session; `lock.json` remains `released`.

### Run 2026-07-04T21:40:00Z — Claude (Fable 5), branch claude/os-setup-onboarding-x23ohp
- **Goal:** CLI-OS onboarding/friction pass per maintainer: make the OS easy to install, remove
  the demo path, make entering a repo and adding providers easy, fix illegible (black-on-black)
  text boxes, put clear instructions in the repo root, and give new devs a place to prompt models.
- **Triggering event:** direct maintainer instruction in-session.
- **Reviewer/comment reference:** none.
- **Decision:** Normal work. Root cause of the illegible inputs: dashboard modal fields kept
  `background:rgba(0,0,0,.3)` while `--text` flips to near-black under
  `prefers-color-scheme:light` — themed all form controls (incl. select options + autofill) via
  scheme-aware variables in both HTML files. Demo/mock removed from every user-facing flow but
  kept as an internal adapter for the offline test suite. Repo registration exposed to the
  dashboard through new authenticated endpoints reusing the CLI primitive; duplicate ids 409
  instead of the CLI's silent replace. Playground added as a thin client of the existing
  authenticated `/v1/chat/completions` (no new server surface), with a custom-model entry for
  catalog-less providers.
- **Completed work:** `internal/gateway/repos.go` (`POST /v1/repos`, `POST /v1/repos/remove`,
  path-existence validation, audit, freshness snapshot) + routes + `repo_mgmt_test.go`;
  dashboard: form-control theming, Register-repo modal + repo Remove, Playground (model/repo
  pickers, custom model, chat log), demo option removed; setup wizard: theming, mock option
  removed, default project `default`; `init` hints, `install/install.sh` next-steps,
  `docker-entrypoint.sh` no longer seeds a mock provider; `cli-os/README.md` + `INSTALL.md`
  reworked (real-provider quickstarts, new endpoints, accuracy note); root `GETTING_STARTED.md`
  (new) + root `README.md` quickstart callout, layout + install pointers; mock reply string no
  longer says "demo upstream".
- **Changed files:** `cli-os/{public/dashboard.html,public/setup.html,internal/gateway/repos.go,
  internal/gateway/adapters/mock.go,internal/server/server.go,internal/server/repo_mgmt_test.go,
  cmd/l00prite/main.go,install/install.sh,install/docker-entrypoint.sh,README.md,INSTALL.md}`;
  root `GETTING_STARTED.md` (new), `README.md`, `CLAUDE.md` (§7 row), this ledger. Zero-line
  diff to `.claude/commands/build-loop.md` and `scripts/validate-l00prite.js` held.
- **Tests run / Verification:**
  - `command: go test ./...` · `exit_code: 0` · `summary: all packages pass incl. new repo-mgmt tests`.
  - `command: node scripts/validate-l00prite.js` · `exit_code: 0` · `summary: 519 PASS, 0 FAIL`.
  - `command: node uitest.js (Playwright end-to-end against the real binary)` · `exit_code: 0` ·
    `summary: 18/18 — wizard+dashboard input contrast ≥4.5:1 in dark AND light schemes (measured
    ~17:1), no mock option in either add-provider path, repo registered through the UI modal,
    playground prompt round-tripped through /v1/chat/completions with a rendered reply`.
- **Response drafted/sent:** none.
- **Event status:** not applicable.
- **Failures:** none blocking; UI test initially flaky from a stale server + a token-regex that
  truncated base64url secrets containing `-` — both fixed in the test harness.
- **Confidence:** High — validator clean, Go suite green, UI verified end-to-end in both schemes.
- **Next action:** maintainer review of this branch.
- **Lock:** none acquired — single-agent session; `lock.json` remains `released`.

### Run 2026-07-04T23:30:00Z — Claude (Fable 5), branch claude/os-setup-onboarding-x23ohp (review round, PR #22)
- **Goal:** Address PR #22 review feedback from gemini-code-assist, Copilot, and Codex, plus the
  confirmed findings of this session's own adversarial review workflow (16 agents, 10 confirmed).
- **Triggering event:** PR #22 review webhooks + completed internal review workflow.
- **Decision / fixes:** `repos.go` — duplicate-check+INSERT made one transaction (concurrent
  duplicate now 409, never a constraint 500); Scan errors surfaced as 500 instead of masquerading
  as "not found"/0; blank remove id → 400; **register now lands in the acting token's project and
  an explicit different project is 403** (closes the workflow's major security finding — the
  endpoint could otherwise re-home a host directory across the request-time project gate; also
  fixes Codex's project-mismatch UX findings at the root). Dashboard — Playground repo picker
  filtered to repos the token can actually use (project match, repo-scoped tokens); register modal
  prefills the token's project and says project = access scope; remove modal warns how many active
  tokens are scoped to the repo; Clear-during-send can no longer poison the next conversation
  (generation counter); registered-but-no-memory branch refreshes immediately so Esc/backdrop can't
  leave stale UI; 20s auto-refresh no longer rebuilds unchanged chat/selects (text selection +
  open dropdowns survive); non-header-safe repo ids get a clear error instead of "network error";
  model-to-test relabeled per adapter (openai-compat: "usually required", since its catalog is
  intentionally PENDING). Docs — removed the false "Claude Code works unchanged via OPENAI_*"
  claim (no /v1/messages ingress) in GETTING_STARTED/README/INSTALL/install.sh/wizard done-screen;
  INSTALL no longer implies the CLI verifies paths; stale Dockerfile seed comment fixed.
- **Tests run / Verification:**
  - `command: go test ./...` · `exit_code: 0` · `summary: all pass incl. new 403 cross-project +
    blank-id 400 cases`.
  - `command: node scripts/validate-l00prite.js` · `exit_code: 0` · `summary: 519 PASS, 0 FAIL`.
  - `command: node uitest.js (Playwright end-to-end, rebuilt binary)` · `exit_code: 0` ·
    `summary: 18/18`.
- **Response drafted/sent:** none — fixes pushed; the diff is the reply.
- **Failures:** none.
- **Next action:** watch PR #22 (subscription active, ~1h self check-ins) until merge/close.
- **Lock:** none acquired — single-agent session; `lock.json` remains `released`.

### Run 2026-07-05T19:10:34Z — Claude (Fable 5), branch OS-APK (L00prite OS build pass)
- **Goal:** Maintainer brief "L00prite OS": evolve CLI-OS into the installable autonomous
  software-engineering OS — build on the existing gateway, follow the protocol, zero edits to
  the two review-gated files, maintainer merges the PR.
- **Triggering event:** none — direct maintainer brief in-session.
- **Reviewer/comment reference:** none.
- **Decision:** normal roadmap work; the run engine realizes the "runtime harness" roadmap item
  by mechanically embodying `execute-loop.md`. Fable 5 authored the design, the engine
  loop/pre-flight/exec core, and all reviews; Opus subagents wrote the peripheral units to
  file-level specs (routing, store, protocol-file IO, tools, roles, packaging).
- **Completed work:** `cli-os/docs/os-architecture.md` (v2 design); role-aware auto-routing
  (`roleRanks`, profile `rankMap`/`providers`, built-in plan/code/review/summarize profiles);
  `internal/engine/` (pre-flight steps 1-5 in code incl. scaffold/stale-run recovery/schema
  migration, Start-as-in-session-confirmation with confirm:EXECUTE, one-unit iteration loop,
  nine run boundaries as code, repo-jailed tools with protocol-file hard-deny + Autonomous-Edit
  Denylist + command-allowlist gates, per-action approvals fail-closed on timeout, dual
  persistence: engine SQLite runs/run_events/run_approvals + target-repo .l00prite files);
  gateway seam (`EngineCaller` over runTurn/RunBridge — every autonomous call routed, PEP
  budget-reserved, metered, ledgered; engine names only `auto:<role-profile>`); `/v1/runs*`
  API (create/preflight/start/list/get/events/approve/stop) + `/v1/repos/clone`;
  cross-platform packaging (`scripts/dist.sh` 5-target static matrix + SHA256SUMS, stamped
  `gateway.Version`, `l00prite version`, `install/install.ps1`, install.sh updates).
- **Fix implemented:** not applicable (feature pass).
- **Changed files:** cli-os/{docs/os-architecture.md, internal/engine/* (new pkg),
  internal/gateway/{enginecaller.go,runs.go,repos_clone.go,turn.go,routerauto.go,dashboard.go},
  internal/config/config.go, internal/server/server.go, internal/state/db.go,
  cmd/l00prite/main.go, scripts/dist.sh, install/{install.sh,install.ps1}}; .l00prite/
  {state.json,todos.md,ledger.md,lock.json}; CLAUDE.md (§7 row). Zero-line diff to
  `.claude/commands/build-loop.md` and `scripts/validate-l00prite.js` held.
- **Tests run / Verification:**
  - `command: go test ./...` · `exit_code: 0` · `summary: all packages pass incl. new engine
    suite (store, protocol-file IO, denylist matcher, tools jail, roles) and 4 end-to-end run
    tests: definition-of-done with real writes + disarmed exit + released lock, denylist gate
    fail-closing to destructive_operation_required, Start refused without fresh
    pre-flight/confirm, crash reconciliation` · `timestamp: 2026-07-05T19:09Z`.
  - `command: node scripts/validate-l00prite.js` · `exit_code: 0` · `summary: 519 PASS, 0 FAIL` ·
    `timestamp: 2026-07-05T19:10Z`.
  - `command: node scripts/l00prite-doctor.js .` · `exit_code: 0` · `summary: HEALTHY, 25 ok /
    0 warn / 0 fail` · `timestamp: 2026-07-05T19:10Z`.
  - `command: bash scripts/dist.sh vtest` · `exit_code: 0` · `summary: 5 static artifacts
    (linux/darwin amd64+arm64, windows/amd64, ~4.4-4.9MB) + SHA256SUMS; dist/ removed after` ·
    `timestamp: 2026-07-05T19:07Z`.
- **Response drafted/sent:** PR opened at the maintainer's request ("PR please").
- **Event status:** not applicable.
- **Failures:** the 16-agent adversarial review workflow and the dashboard-Runs-view writer
  were cut off by a session usage limit — the multi-agent adversarial pass did NOT complete
  (its empty findings list is an artifact of the failure, not a clean bill); review coverage =
  Fable line-by-line review of every unit + the full test suite. Dashboard Runs UI not built.
- **Decisions:** Start-click = the protocol's explicit in-session confirmation (anticipated in
  todos.md); engine scaffolds memory files only, never the 7-way-mirrored prompts; clean-tree
  gate exempts .l00prite/ (protects user work, not engine memory); approval timeout = deny +
  boundary stop; "privacy" objective requires an operator-defined providers-restricted profile,
  never a silent cloud fallback.
- **Confidence:** High on engine/API/routing/packaging (validator + full suite + e2e tests);
  the incomplete adversarial pass is queued to re-run.
- **Next action:** maintainer reviews the OS-APK PR; next build units queued in todos.md
  (dashboard Runs view first).
- **Do-not-retry notes:** none.
- **Lock:** lock-20260705-134733 (session start) and lock-20260705-191034 (session end)
  acquired/released for the protected-path writes; no stale reclamation needed.

### Run 2026-07-05T19:16:00Z to 2026-07-05T20:15:00Z — Claude (Fable 5), PR #24 review-response round
- **Goal:** Address automated review findings on PR #24 (jackofall1232/l00prite#24) from three
  bots (gemini-code-assist, copilot-pull-request-reviewer, chatgpt-codex-connector) across two
  review passes, without weakening any protocol invariant the engine exists to enforce.
- **Triggering event:** GitHub PR review webhook activity (21 review comments across the two
  passes).
- **Decision:** Valid — every finding was verified against the actual current code before
  fixing; none were speculative or already-stale by the time they were read.
- **Completed work — round 1 (Gemini + Copilot, commit `ce0b11c`):** fail-closed on
  previously-ignored errors that could compromise the lock/mutual-exclusion guarantee
  (`ActiveRunForRepo`, `ReadSnapshot` in `StartRun` and `iterate`, `ReadLock`); fixed a
  nil-pointer panic in `awaitApproval` when a run's handle is no longer registered;
  `parseArgs` now accepts a tool-call `arguments` value as either a JSON string or an
  already-decoded object; `search_files` skips non-UTF-8 (binary) files; `repos_clone.go`
  rejects credential-bearing `https://user:token@host/...` clone URLs and replaces the
  Windows-incompatible `GIT_ASKPASS` with a cross-platform `GIT_SSH_COMMAND` BatchMode config;
  `install.ps1` handles a null User Path on a fresh Windows account; a test-setup nit fixed.
- **Completed work — round 2 (Codex, commit `ff24aac`):** three genuine security-critical
  gate bypasses closed — (1) the command allowlist's prefix match let a command append shell
  metacharacters past an allowlisted prefix and run unapproved (`"go test ./..."` allowlisted
  → `"go test ./... ; rm -rf /"` ran silently); now the appended suffix must be free of
  chaining/redirection/substitution characters, while an exact match against a compound
  allowlisted string is unaffected. (2) `.l00prite/constraints.md` — which carries the
  Autonomous-Edit Denylist itself — was neither hard-denied nor covered by the default
  denylist, so a run could loosen its own denylist and exploit that the next iteration; it is
  now hard-denied like heartbeat/state/lock/prompts, never gate-then-approvable. (3)
  `search_files` followed a symlink outside the repo root via `os.ReadFile`, unlike
  `read_file`'s `resolvePath` containment; symlinked entries are now skipped. Also fixed:
  destructive `git branch` flags (`-D`/`-f`/`-m`/etc.) now require approval instead of running
  in the always-safe set; a failed unit commit now stops the run for review instead of being
  reported as a successfully progressed unit; `Decide()` now rejects an approval that doesn't
  belong to the given run (closing a cross-run, even cross-project, authorization gap); an
  interrupted run's own still-unexpired lease is now refreshed instead of failing
  `AcquireLock` (which correctly refuses to re-acquire an already-owned lock), which previously
  blocked crash recovery until the TTL lapsed.
- **Changed files:** `cli-os/internal/engine/{engine.go,exec.go,preflight.go,tools.go,
  helpers.go}`, `cli-os/internal/gateway/{dashboard.go,repos_clone.go}`,
  `cli-os/install/install.ps1`, `cli-os/internal/server/runs_api_test.go`; new tests
  `cli-os/internal/engine/{helpers_test.go,preflight_test.go}` plus additions to
  `tools_test.go` and `run_integration_test.go` (7 new regression tests targeting exactly
  these scenarios, verified they would have failed against the pre-fix code).
- **Tests run / Verification:**
  - `command: go build ./...` · `exit_code: 0` · `summary: clean after both rounds`.
  - `command: go vet ./...` · `exit_code: 0` · `summary: clean`.
  - `command: gofmt -l .` · `exit_code: 0` · `summary: no output (clean), incl. a pre-existing
    comment-reflow nit in dashboard.go fixed opportunistically`.
  - `command: go test ./...` · `exit_code: 0` · `summary: all packages pass both rounds,
    including the engine suite with 7 new regression tests`.
  - `command: node scripts/validate-l00prite.js` · `exit_code: 0` · `summary: 519 PASS, 0 FAIL`.
  - `command: node scripts/l00prite-doctor.js .` · `exit_code: 0` · `summary: HEALTHY`.
- **Response drafted/sent:** none — fixes pushed directly; the diff and this ledger entry are
  the reply. All 22 review threads are bot-authored; none required a human-facing reply.
- **Event status:** completed — both review rounds addressed, no further bot activity pending.
- **Failures:** none blocking. One judgement call flagged for the maintainer: the shell-chaining
  fix to `commandAllowed` is a conservative metachar denylist (`;&|` + backtick + `$<>` +
  newline) on the appended suffix only — an exact match against the allowlist string itself is
  never blocked, even if that string itself contains metacharacters (a human pre-approved that
  literal compound command at pre-flight).
- **Decisions:** `.l00prite/constraints.md` is now unconditionally hard-denied (not
  gate-then-approvable) during a run, matching heartbeat/state/lock/prompts — since the whole
  point of a loop-immutable denylist is that nothing inside the run, approved or not, can
  loosen it; only a human editing it outside the run is legitimate.
- **Confidence:** High — every finding verified against the actual code before fixing (not
  taken on faith from the bot text), each has a dedicated regression test, and the full
  verification suite is green.
- **Next action:** none pending on this PR — it merged to `main` (see the following entry).
  Next build units queued in `todos.md` (dashboard Runs view first).
- **Do-not-retry notes:** none.
- **Lock:** lock-20260705-134733 (session start) and lock-20260705-191034 (session end)
  cover this run's writes too — no protected-path lock was acquired mid-review-response since
  all writes in this run were to `cli-os/` source/test files, not `.l00prite/` protected paths.

### Run 2026-07-05T20:17:23Z — Claude (Fable 5), PR #24 merge close-out
- **Goal:** Record PR #24's merge to `main`, close out the OS-APK build session, and restart
  the `OS-APK` branch for the next unit of work.
- **Triggering event:** GitHub webhook — PR #24 merged (squash-merge into `main` as `e6c9e2e`).
- **Decision:** Valid — confirmed via `git fetch origin main` that `e6c9e2e` is a
  single-parent (squash) commit, and `git diff origin/main origin/OS-APK` was empty, so no
  commit on `OS-APK` was orphaned by the squash.
- **Completed work:** Cancelled the stale ~hourly PR-watch check-in trigger
  (`trig_017SpfMLCSA21pjS9toFXTvz`), now unnecessary since the PR is closed. Confirmed GitHub
  had auto-deleted the `OS-APK` head branch on merge (`git ls-remote` returned nothing); per
  the merged-branch protocol, recreated it fresh from `origin/main`
  (`git checkout -B OS-APK origin/main && git push -u origin OS-APK`) rather than
  force-pushing over a branch that no longer existed. Updated `state.json` (phase back to
  `planning`, goal reflects the merge) and `todos.md` (Active section notes the merge and the
  branch recreation; unchanged item list otherwise — dashboard Runs view remains the next
  unit).
- **Changed files:** `.l00prite/{state.json,todos.md,ledger.md,lock.json}`; `OS-APK` branch
  ref (recreated from `main`, no source changes).
- **Tests run / Verification:**
  - `command: git diff origin/main origin/OS-APK --stat` (before restart) · `exit_code: 0` ·
    `summary: empty diff, confirming no orphaned commits before restarting the branch`.
- **Response drafted/sent:** none — this is bookkeeping only, no PR is open.
- **Event status:** completed.
- **Failures:** none. One transient hiccup: the first `git push --force-with-lease` was
  rejected as "stale info" because the local remote-tracking ref for `OS-APK` predated GitHub's
  auto-delete; a re-fetch surfaced the real state (ref gone), and a plain `push -u` (no force
  needed, nothing to overwrite) succeeded.
- **Confidence:** High — branch-restart protocol followed exactly (fetch main, checkout -B,
  verify no orphaned work, push), per the merged-branch handling rule.
- **Next action:** start the dashboard Runs view (todos.md Active section) on the freshly
  restarted `OS-APK` branch.
- **Do-not-retry notes:** none.
- **Lock:** lock-20260705-201723 acquired for this entry plus the `state.json`/`todos.md`
  writes above; released immediately after.

### Run 2026-07-05T21:24:23Z — Claude (Fable 5), branch claude/looprite-android-apk-4mth8g — unit 1: recon + Android architecture
- **Goal:** Start the Android APK pass (maintainer brief 2026-07-05): recon the full repo +
  build environment, decide the Android packaging strategy, and write the architecture
  plan / feasibility decision / phased roadmap (deliverables 1–3).
- **Triggering event:** Maintainer brief — evolve the repo into a self-contained L00prite
  OS Android APK (device = local control plane; no hosted server).
- **Decision:** Packaging Option A chosen — cross-compile the existing Go gateway to an
  android/arm64 PIE binary, ship it as lib/arm64-v8a/libl00prite.so, exec from
  nativeLibraryDir under a thin no-AndroidX Java wrapper (foreground service + WebView on
  http://127.0.0.1:8787). gomobile, Termux-dependence, and native rewrites rejected — see
  cli-os/docs/android-architecture.md §2.
- **Completed work:** 7-reader parallel recon of every cli-os subsystem + protocol layer;
  environment feasibility probes; no-Google APK toolchain proven end-to-end (signed v2+v3
  hello-world APK with WebView activity + service + native-lib payload, built from apt
  aapt/zipalign/apksigner/dalvik-exchange + android-framework-res + Maven Central
  robolectric android-all); Venice AI pricing verified first-party (docs mirror,
  2026-07-05); wrote cli-os/docs/android-architecture.md (plan, feasibility evidence,
  G1–G11 gap analysis, provider/role expansion, security model, dual build pipeline,
  Phase 0–3 roadmap); blueprint.md Android section.
- **Changed files:** cli-os/docs/android-architecture.md (new), .l00prite/blueprint.md,
  .l00prite/{lock,state,todos} (session start, prior commit 7936abd).
- **Tests run / Verification:**
  - `command: GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/l00prite` ·
    `exit_code: 0` · `summary: PIE ELF aarch64, interpreter /system/bin/linker64, pure-Go
    SQLite — no NDK/gomobile needed` · 2026-07-05T21:19Z.
  - `command: apksigner verify --verbose signed.apk (toolchain POC, scratchpad)` ·
    `exit_code: 0` · `summary: v2=true v3=true; aapt dump badging shows launchable
    activity + service + native-code arm64-v8a` · 2026-07-05T21:30Z.
  - `command: curl https://dl.google.com/... via agent proxy` · `exit_code: 56` ·
    `summary: dl.google.com CONNECT 403 — Google-hosted SDK/AGP unavailable in this build
    container; motivated the no-Google local chain + real-SDK CI dual pipeline` ·
    2026-07-05T21:18Z.
- **Response drafted/sent:** none — design unit; the PR at pass end is the response.
- **Event status:** n/a (no event).
- **Failures:** none blocking. Recorded for reuse: dl.google.com and maven.google.com
  (301→dl.google.com) are proxy-blocked here; curl.se and api.venice.ai and
  docs.venice.ai also blocked; Venice docs reachable via their public GitHub docs mirror.
- **Decisions:** (1) gitx seam with pure-Go go-git fallback instead of bundling a static
  git binary (self-contained, keeps exec-git byte-identical on desktop). (2) Master key
  via Android-Keystore-wrapped LOOPRITE_MASTER_KEY env, never a key file on flash.
  (3) New optional LOOPRITE_SETUP_SECRET gate closes the loopback first-run setup race
  unique to multi-app devices. (4) dist.sh untouched; android builds live in
  scripts/build-apk.sh + CI workflow (workflow addition = denylisted path, deliberately
  shipped via human-reviewed PR).
- **Confidence:** High on feasibility (both pillars proven by direct experiment before
  design commit); medium on Venice capability metadata (pricing first-party-verified,
  context/tools flags to be marked at source-supported confidence in the manifest).
- **Next action:** implement per android-architecture.md §4/§5/§7 — three parallel writer
  units (Go platform enablement; providers/roles; android app + build pipeline), each
  reviewed by the architect and committed separately.
- **Do-not-retry notes:** do not attempt Android SDK/AGP/NDK downloads from Google hosts
  in this environment; do not fetch docs.venice.ai directly (use the GitHub docs mirror).
- **Lock:** lock-20260705-212423-claude-android-apk-pass held (acquired at session start
  after prior lock showed released; expires 2026-07-06T01:24:23Z — refresh before expiry
  per LOCKING.md rule 7).

### Run 2026-07-05T22:05:00Z — Claude (Fable 5 architect + Sonnet 5 writer), branch claude/looprite-android-apk-4mth8g — unit 2: Android app + APK pipeline
- **Goal:** Implement android-architecture.md §3/§7: the android/ wrapper app (Java,
  no-AndroidX), the hermetic no-Google build script, and the real-SDK CI workflow.
- **Triggering event:** Unit 1 (architecture) committed; parallel writer dispatch.
- **Decision:** Written by a Sonnet 5 writer agent to the architect's spec; reviewed
  line-by-line by the architect (Fable 5) before commit.
- **Completed work:** android/{AndroidManifest.xml,README.md,res/values/strings.xml,
  res/xml/network_security_config.xml,src/com/l00prite/os/{MainActivity,GatewayService,
  Keys}.java}; cli-os/scripts/build-apk.sh; .github/workflows/android-apk.yml;
  .gitignore entry for cli-os/dist-android/. Writer deviations accepted by the architect:
  ACCESS_NETWORK_STATE permission (required for LinkProperties DNS discovery, G1),
  allowBackup=false (keeps the wrapped vault key out of Android Auto Backup),
  android/amd64 emulator ABI skipped as best-effort (genuine Go toolchain limit:
  android/amd64 requires external cgo linking — arm64 does not; both pipelines degrade
  identically and pick the ABI back up automatically if a future Go adds internal
  linking). Architect fixes on review: session-specific scratchpad path stripped from
  build-apk.sh (replaced with a cache-glob under ~/.cache/l00prite-apk, cache seeded);
  dist-android/ gitignored.
- **Changed files:** android/** (new), cli-os/scripts/build-apk.sh (new),
  .github/workflows/android-apk.yml (new — NOTE: matches the Autonomous-Edit Denylist
  glob .github/workflows/**; shipped deliberately via this human-reviewed PR, flagged in
  the PR description), .gitignore.
- **Tests run / Verification:**
  - `command: bash cli-os/scripts/build-apk.sh v0-verify3 (writer run, isolated worktree
    of dc38b74 because the Go tree was concurrently mid-edit by unit 3)` · `exit_code: 0`
    · `summary: signed APK produced; apksigner verify v2=true v3=true; badging shows
    com.l00prite.os / launchable MainActivity / INTERNET / native-code arm64-v8a;
    lib/arm64-v8a/libl00prite.so Stored (11.9MB PIE aarch64, /system/bin/linker64);
    assets/cacert.pem present (146 Mozilla certs, built only from
    /usr/share/ca-certificates/mozilla, never the proxy-tainted system store)` ·
    2026-07-05T21:58Z.
  - `command: python3 yaml.safe_load(android-apk.yml) + bash -n on run blocks` ·
    `exit_code: 0` · `summary: workflow parses; CI run itself pending first PR run` ·
    2026-07-05T21:59Z.
  - `command: bash -n cli-os/scripts/build-apk.sh (post architect edit)` · `exit_code: 0`
    · `summary: syntax clean after cache-glob refactor` · 2026-07-05T22:04Z.
- **Response drafted/sent:** none.
- **Event status:** n/a.
- **Failures:** android/amd64 cross-build impossible without NDK (external-cgo-linking
  requirement) — recorded as a known limitation, NOT retried; do not burn time on
  -linkmode/-tags workarounds (all confirmed ineffective).
- **Decisions:** master key wrapped by Android Keystore, injected via LOOPRITE_MASTER_KEY
  env only (never a file on flash); setup secret in plain app-private prefs is an
  accepted trade-off (dies at setup-latch time); WebView load deferred behind /healthz
  poll; full APK rebuild against the final merged tree is owed in the verification pass.
- **Confidence:** High for everything executed here; medium for the CI workflow until its
  first real Actions run (sdkmanager/d8 paths are standard but unexecuted from this
  sandbox).
- **Next action:** review + commit units 3 (Go platform enablement) and 4
  (providers/roles) when their writers finish; then integrated verification.
- **Do-not-retry notes:** see Failures (android/amd64).
- **Lock:** lock-20260705-212423-claude-android-apk-pass still held.
