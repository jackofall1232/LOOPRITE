# Prioritized TODOs

## Active — Android APK pass (maintainer brief 2026-07-05, branch `claude/looprite-android-apk-4mth8g`)

Maintainer brief: evolve the repo into a self-contained **L00prite OS Android APK** — the
Android device is the local control plane (no hosted L00prite server). A user sideloads the
APK, adds provider keys (stored encrypted on-device), connects/clones a Git repo, enters a
prompt, and L00prite runs the project end-to-end on the phone. Preserve all existing
protocol functionality; build on `cli-os/` (gateway/dashboard/engine); providers: OpenAI,
Codex, Anthropic (Fable/Sonnet), Gemini, **Venice AI (dedicated path)**; keep bridging and
role routing (architect/writer/reviewer/advisor; Fable 5 = architect, Sonnet 5 = writer);
open a PR when the prototype is complete and verified. This pass takes precedence over the
previously queued dashboard Runs view (moved to Next, below).

- [x] Recon: repo map (7-reader fan-out), environment feasibility probes.
      Evidence so far: `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/l00prite`
      succeeds from current source (PIE ELF, `/system/bin/linker64` interpreter, pure-Go
      SQLite — no NDK/gomobile needed); a complete no-Google APK toolchain exists in this
      build container (apt: aapt/zipalign/apksigner/dalvik-exchange; Maven Central:
      robolectric android-all as compile-time android.jar, apksig) since dl.google.com is
      proxy-blocked here.
- [x] `cli-os/docs/android-architecture.md` — deliverables 1–3 committed (bc5a3a6):
      packaging decision (bundled android/arm64 PIE gateway + Java wrapper + WebView),
      G1–G11 gap analysis, provider/role expansion, dual build pipeline, Phase 0–3 roadmap.
- [x] Go: Android enablement (8d8b632) — `LOOPRITE_DNS` resolver override + android
      fallback; shell path resolution; `gitx` seam (exec git verbatim default, pure-Go
      go-git v5.18.0 fallback); `LOOPRITE_SETUP_SECRET` first-run gate; secret env
      scrubbing. android/arm64 builds clean; android/amd64 skipped (needs cgo/NDK —
      recorded in failures.md). `go test ./...` green.
- [x] Providers (82084e4): `manifests/venice.json` (15 models, first-party pricing) +
      `manifests/gemini.json`; architect/writer/reviewer/advisor profiles + seeded
      roleRanks incl. engine plan/code/review (fable-5 architect 98, sonnet-5 writer 97 —
      writer/code switched to quality preference so the policy actually routes; flagged
      for maintainer: this flips the pre-existing `code` default from balanced).
- [x] `android/` app (e74898c) — Java wrapper: GatewayService foreground exec of
      `libl00prite.so` with the full env contract, Keystore-wrapped master key, Mozilla CA
      asset, MainActivity WebView + /healthz poll, cleartext-to-127.0.0.1-only.
- [x] APK pipeline (e74898c) — `cli-os/scripts/build-apk.sh` (hermetic no-Google chain) +
      `.github/workflows/android-apk.yml` (real SDK; first live run pending on the PR).
- [x] Verification: go test all ok; validator 519 PASS 0 FAIL; e2e gateway smoke 15/15
      (setup-secret gate, wizard latch, venice models + auto:writer/architect in
      /v1/models, mock chat round-trip, dry-run auto:writer → venice/claude-sonnet-5 via
      roleRanks.writer); final APK from merged tree signed + verified (v2+v3,
      sha256 042c407e…, 15MB). Doctor HEALTHY check runs post lock-release.
- [x] Ledger/todos/memory/failures updated at every unit boundary; lock released at
      session end; PR opened for maintainer review.

Deferred to Phase 1+ (see android-architecture.md §8): dashboard Runs view (was already
queued), phone-nav polish, wizard copy de-CLI-ing, repo-register path picker, real-device
smoke test (no emulator ABI possible in this container), first live CI run of the
android-apk workflow, venice/gemini capability confirmation from an unblocked network.

## Previous Active — L00prite OS build pass (maintainer brief, branch `OS-APK`)

Maintainer brief: evolve the repo toward "L00prite OS" — an installable, vendor-neutral
autonomous software-engineering application (add keys → connect repo → prompt → Start).
Build on `cli-os/`; do not discard existing work; zero edits to the two review-gated files.

**PR #24 (design + engine + API + packaging) merged to `main` 2026-07-05**, after two rounds
of bot review (Gemini, Copilot, Codex — 21 findings total, all fixed with regression tests;
see the ledger). GitHub auto-deleted the `OS-APK` head branch on merge; it has been recreated
fresh from the new `main` (squash-merge, so no commits were orphaned) for the next unit below.

- [x] `cli-os/docs/os-architecture.md` — L00prite OS v2 design (run engine embodying
      `execute-loop.md`, role team assembly, approval gates, packaging, roadmap).
- [x] Capability/routing v2 — `roleRanks`, profile `rankMap`/`providers` restriction,
      built-in `plan`/`code`/`review`/`summarize` profiles, explainable decisions.
- [x] `internal/engine/` — protocol-mechanical run engine (pre-flight steps 1-5, Start =
      in-session confirmation, one-unit iterations, nine boundaries in code, repo jail +
      protocol-file hard-deny + Denylist/allowlist gates, per-action approvals fail-closed,
      dual persistence, crash recovery), hardened through review: command-allowlist shell-
      chaining closed, `constraints.md` self-modification closed, `search_files` symlink jail
      escape closed, destructive `git branch` flags gated, failed unit commits stop the run,
      cross-run approval decisions rejected, interrupted-run lease recovery fixed.
- [x] `/v1/runs*` API + `/v1/repos/clone` — same auth/scoping as the rest; clone URL rejects
      embedded credentials, git clone is fully non-interactive cross-platform.
- [x] Packaging — `scripts/dist.sh` 5-target static matrix + SHA256SUMS, stamped version,
      `l00prite version`, `install.ps1` (with the null-Path fix), install.sh updates.
- [x] Tests — engine unit + 4 end-to-end run tests against a scripted caller, plus 7 targeted
      regression tests for the review-round fixes; go test, validator (519 PASS), doctor
      HEALTHY, all still green post-fix.
- [ ] **Dashboard Runs view** (create wizard → pre-flight display → Start → live event feed →
      approvals inbox → stop/resume; clone-from-GitHub in repo connect). The API is complete
      and curl-able; the UI writer was cut off by a session usage limit in the 2026-07-05 pass.
      FIRST unit of the next pass — spec in `cli-os/docs/os-architecture.md` §4.
- [ ] Re-run the adversarial multi-agent review of `internal/engine/` + the gateway seam
      (the 2026-07-05 attempt was cut off by the usage limit before it produced findings;
      bot review substituted this pass — still worth a dedicated internal pass for coverage
      bot review doesn't reach, e.g. concurrency/race conditions under real parallel runs).
- [ ] Docs sweep for the OS layer: cli-os README/INSTALL + root README/GETTING_STARTED
      quickstart ("prompt → Start" flow), security-model.md note on the engine's write model
      (run-branch + jail + gates supersede "read-only except .l00prite/" for confirmed runs),
      os-architecture §2.8 wording (rank-map example vs the four roles).
- [ ] Playwright end-to-end of the Runs UI against the real binary once the view exists.

## Next
- [ ] Maintainer decisions on l00prite CLI-OS design (branch `claude/looprite-cli-os-jntwqi`,
      `cli-os/`): answer `cli-os/docs/open-questions.md` — esp. Q1 (which providers in v1),
      Q2 ("quality" in routing), Q3 (runtime language), Q7 (authoritative pricing). Bless
      assumption A1 (CLI-OS supersedes the "no backend" constraint for the `cli-os/` subtree
      only). Implementation of the Gateway/Memory tracks waits on these.
- [ ] Maintainer review of branch `claude/powerful-helper-agent-pfsyj1` (v1.1: universal
      agent layer + Execution Mode), including the two review-gated files changed at the
      maintainer's direction: `.claude/commands/build-loop.md` and
      `scripts/validate-l00prite.js`. Merge to `main` when satisfied.

## Later
- [ ] Runtime harness that mechanically enforces run boundaries and iteration budgets
      (today they are validator-enforced prompt invariants; a harness would make them
      guarantees a non-compliant model can't ignore).
- [ ] GitHub event ingestion (turn real PR comments into `.l00prite/events/` entries
      automatically).
- [ ] CI failure capture as events.
- [ ] CI workflow for this repo that runs `node scripts/validate-l00prite.js` on every PR —
      the human-review-only rule has no automated backstop yet.
- [ ] Cross-agent compatibility tests, including a mid-execution boundary stop resumed by a
      different vendor's agent.
- [ ] Richer, filled-in examples (a real resolved PR review, a real Execution Mode run
      ledger with a boundary stop and resume).
- [ ] Ledger growth management (archival/rotation conventions).
- [ ] Stack-specific skeleton packs.
- [ ] Release packaging so setup isn't fully manual.

## v1.2 gated batch (maintainer review required — review together, do not start piecemeal)

The 2026-07-04 gap pass (doctor, failure/anti-pattern catalogs, Autonomous-Edit Denylist,
no-progress telemetry) deliberately touched **zero** review-gated files. The following work
is genuinely valuable but *requires* editing `.claude/commands/build-loop.md` and/or
`scripts/validate-l00prite.js`, so it is quarantined here as one coherent batch for the
maintainer to review as a unit — so nobody is tempted to "just quickly" touch a gated file.

- [ ] **Formal resource-guard boundaries** — promote no-progress + budget from telemetry to
      real run boundaries: add `no_progress_detected` and `budget_exceeded` (nine → eleven).
      Budget must be **wall-clock-first** (`time_budget_seconds` + `started_at` — the only
      cost axis a file can honestly check); any token figure is labelled an estimate, never
      an enforcement input. Touches: `execute-loop.md` boundary list (all 7 mirrors),
      `heartbeat.json` `run_boundaries` (3 copies) + budget/time fields, **both** hardcoded
      arrays in the gated validator (`RUN_BOUNDARIES` and `RUN_BOUNDARY_IDS`), the gated
      `build-loop.md` "nine run boundaries" line, and a "nine → eleven" prose sweep
      (README, HANDOFF, CLAUDE, `.claude/README.md`, blueprint).
- [ ] **Machine-parseable run-log** — `templates/l00prite/run-log.jsonl` (one JSON object per
      run: `run_id`, `started_at`, `duration_s`, `iterations`, `items`, `actions`,
      `escalations`, `outcome`, `tokens_estimate` marked estimated) + a dependency-free
      `scripts/l00prite-run-log.js` appender with size-capped rotation + a persist-step
      sentence in `execute-loop.md` + gated scaffold emission from `build-loop.md`. The
      wall-clock substrate the budget boundary reads.
- [ ] **Phased autonomy levels** — `execution.autonomy_level`
      (`report_only` | `assisted` | `unattended`) designed strictly as a **restriction
      ladder** (a level may only *remove* permissions vs today's confirmed Execution Mode;
      `unattended` is exactly today's behavior). Captured at pre-flight, shown in the
      display, ships `report_only`/null default. Touches `execute-loop.md`, the gated
      `build-loop --execute` handoff, and possibly a gated validator field check. Needs its
      own design review; must never weaken the single confirmed-pre-flight entry.
- [ ] **Independent verifier prompt** — a seventh canonical loop prompt, but only *after* the
      runtime harness exists, so the checker is a genuinely separate invocation, not the
      implementer narrating self-review. Touches `PROMPT_NAMES` in the gated validator, the
      gated `build-loop` copy list, all 7 mirror locations, adapters, and every "six loop
      prompts" mention.
- [ ] **Pattern library + `--pattern` scaffold flag** — a JSON (not YAML) pattern registry +
      per-pattern docs with disarmed defaults and mandatory human gates, plus a gated
      `build-loop --pattern` flag. Decide first whether a pattern marketplace belongs in a
      memory/execution protocol or as a layer on top.
- [ ] **Additive validator assertions** (gated) for the 2026-07-04 pass so its invariants are
      enforced, not just present: each `constraints.md` copy carries the Autonomous-Edit
      Denylist block; `failures.md` carries the seeded inherited catalog; `heartbeat.execution`
      has `iterations_since_progress` / `last_progress_iteration` / `no_progress_threshold`.

## Done
- [x] `/build-loop` slash command and Loop Engineering scaffolding — 2026-06-30.
- [x] Dogfood `/build-loop`, fix bugs found — 2026-06-30.
- [x] Codex prompt equivalents, Claude/Codex parity — 2026-07-01.
- [x] README repositioned as vendor-neutral loop memory protocol — 2026-07-01.
- [x] Protocol hardening: lock/lease convention, untrusted-content warnings, event ID
      format, ledger verification-evidence fields — 2026-07-01.
- [x] Branding: ASCII banner and SVG logo — 2026-07-01.
- [x] First public release (v1: scaffold, memory, event, and handoff layers; execution mode
      explicitly out of scope for that release) — 2026-07-01.
- [x] Universal agent layer: canonical prompts in `templates/l00prite/prompts/` with
      byte-identical mirrors (validator-enforced), `AGENTS.md.template`, fixed protocol
      section in `CLAUDE.md.template`, vendor adapters (Gemini CLI, Qwen Code, Copilot,
      Cursor, Windsurf, Aider) + `templates/vendors.json`, dogfooded at this repo's root —
      2026-07-02 (in review).
- [x] Opt-in Execution Mode: `execute-loop` prompts everywhere + `/execute-loop` command,
      `--execute` handoff on `build-loop`, pre-flight display + explicit in-session
      confirmation gate, nine run boundaries, resumable exits, schema v2
      `heartbeat.json`/`state.json` execution fields, self-modification guard — 2026-07-02
      (in review).
- [x] Loop-maturity gap pass (from analyzing loop-engineering), zero gated-file edits —
      2026-07-04:
  - [x] `scripts/l00prite-doctor.js` — read-only, dependency-free health check for a
        scaffolded project's `.l00prite/` (arming consistency, state↔heartbeat drift, stale
        arming, prompt self-parity, ledger evidence, pending-count, denylist/seeded-catalog
        presence, no-progress stall). Passes clean on this repo and the example.
  - [x] `docs/failure-modes.md`, `docs/anti-patterns.md`, `docs/concepts.md`, `docs/README.md`
        — l00prite-specific loop-wisdom catalogs (S1/S2/S3), each mapped to the boundary/lock/
        doctor/denylist that guards it; adapted from loop-engineering.
  - [x] Seeded `failures.md` (template + example + dogfood) with an inherited generic
        failure-mode catalog, clearly marked as not project history.
  - [x] Autonomous-Edit Denylist in `constraints.md` (template + example + dogfood):
        machine-readable protected-path globs enforced via the **existing**
        `destructive_operation_required` boundary — no new boundary, loop-immutable.
  - [x] No-progress telemetry: additive `execution.iterations_since_progress` /
        `last_progress_iteration` / `no_progress_threshold` (disarmed-neutral, 3 copies) +
        `execute-loop.md` persist-step maintenance mapped to the existing `human_review_gate`
        + doctor stall check. Formal `no_progress_detected` boundary deferred to the v1.2
        gated batch above.
