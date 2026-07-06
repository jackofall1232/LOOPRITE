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

- Android packaging (maintainer brief 2026-07-05): the APK bundles the UNMODIFIED Go
  gateway as an android/arm64 PIE binary exec'd from the APK's native-library dir under a
  thin no-AndroidX Java wrapper — never gomobile, never a Termux dependency, never a
  parallel native UI. One code path for desktop and Android. Design:
  `cli-os/docs/android-architecture.md`.
- On-device secrets: the vault master key exists only Keystore-wrapped in app prefs and in
  the gateway process env (`LOOPRITE_MASTER_KEY`); `master.key` must never be written on
  Android. `LOOPRITE_MASTER_KEY`/`LOOPRITE_SETUP_SECRET` are scrubbed from every child
  process the engine or clone path spawns.
- All git operations go through the `internal/gitx` seam: exec-git (verbatim legacy
  behavior) whenever a git binary exists, pure-Go go-git fallback otherwise. Never call
  `exec.Command("git", ...)` directly from engine/gateway code again. go-git is PINNED at
  v5.18.0 while the module targets go 1.24 (v5.19+ requires go >= 1.25).
- Role-policy routing (maintainer): Fable-5-class models architect/plan/review, Sonnet
  5-class models do the bulk writing. The writer/code profiles are `quality` preference on
  purpose — a balanced cost-blend demonstrably handed the writing role to the cheapest
  tools-capable catalog model, making the policy decorative. Cost control lives in PEP caps
  and the cheap/balanced profiles. A role-map rank that must beat an unmapped candidate has
  to EXCEED that candidate's qualityRanks fallback (exact ties break alphabetically).
- Provider manifests must carry honest provenance: Venice pricing is first-party (docs
  mirror github.com/veniceai/api-docs — use it; docs.venice.ai and api.venice.ai are
  egress-blocked from build containers); Gemini pricing stays null until first-party
  verifiable. Never backfill prices from training memory.
- Dashboard Runs UI (Phase 1, 2026-07-06): the command allowlist in the create-run form is
  REQUIRED, not optional — the engine's own pre-flight hard-blocks without at least one
  entry (its first line is the done-check), so a UI that lets it submit empty produces a
  dead-end blocked pre-flight with no way forward. Any future create-run field must be
  cross-checked against the engine's actual pre-flight blockers before being labeled
  optional. The "next recommended action" text shown in a run's Exit view is a CLIENT-SIDE
  static suggestion keyed on the boundary id — the Run API has no such field, and this must
  never be presented as if it came from the server.
- Offline UI/e2e testing against the mock adapter: the router keys model catalogs by
  PROVIDER NAME against the embedded manifests, so a provider literally named `mock` has no
  catalog and is unroutable (every role fails to route, pre-flight comes back blocked with
  an empty team). Name the mock-adapter provider after a real manifest instead (e.g.
  `anthropic`) — this repo's own `internal/server/e2e_test.go` already does this.
- The `gitx.Client` seam (Phase 2, 2026-07-06): any new git primitive added to this
  interface must be implemented in BOTH `execClient` (exec must stay a byte-identical
  passthrough to the real command — zero behavior change on desktop) and `gogitClient`
  (pure-Go, for git-less Android). Never fabricate diff-looking output that isn't real —
  `DiffHead`'s worktree-vs-HEAD case has no honest go-git equivalent so it's a labeled
  summary, but `Show`'s commit-vs-parent case DOES have one (`Commit.Patch`) and must use
  it. Patch direction is `parent.Patch(commit)`, not the reverse — verify by direction, not
  assumption, whenever touching this (an added file must render as an addition).
- The model-facing `git_command` tool's gogit subset (Phase 2) is EXACT-MATCH-ONLY by
  design: any unrecognized flag or extra argument on an otherwise-supported subcommand must
  fall through to the hard refusal, never be loosely interpreted as "probably fine" — a
  coding-agent's own tool silently answering a different question than the real command
  would is a correctness bug, not a convenience.
- `cli-os/internal/ledger.Append` is called from concurrent HTTP-request goroutines — any
  future change to its JSONL-mirror logic (rotation, format, etc.) must hold `jsonlMu` (or
  its successor) around the full check-then-act sequence; a naive check-size-then-rename
  race was a real hazard here, not hypothetical.
- Android has no AndroidX/Jetpack dependency anywhere in this app (deliberate, Phase 0
  decision, reconfirmed Phase 2): `DocumentFile` is AndroidX-only and does NOT exist in the
  platform framework jar (verified empirically against the actual `android-all` jar this
  repo's build uses) — SAF tree-walking must use `android.provider.DocumentsContract`
  directly, never assume `DocumentFile` is available.
- Any path built from an untrusted display name (SAF import, or similar future features)
  must validate the FINAL resolved canonical path is contained within its intended parent
  directory — per-segment string sanitization alone is not sufficient defense-in-depth.

## Facts
- l00prite ships no backend, hosted service, or install script; setup is manual (clone,
  copy prompts/templates). The Android APK (cli-os/dist-android via
  cli-os/scripts/build-apk.sh, or the android-apk CI workflow) is self-contained — the
  device is the control plane; still no hosted service.
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
