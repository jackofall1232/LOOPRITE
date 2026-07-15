# Prioritized TODOs

## Approved roadmap — LOOPRITE AI orchestration platform review (maintainer approval 2026-07-14)

**Status:** Roadmap approved for persistence; implementation has **not** begun. Before any code
change, resolve the human decisions below, create `feature/phase-0-security-contracts`, and keep
Phase 0 limited to security and contracts. Preserve `/v1/chat/completions`, the PEP, provider
vault, deterministic routing, repository containment, run engine, and current bridge compatibility.

**Design record:** Senior-staff review produced 2026-07-14 as
`/tmp/LOOPRITE-engineering-review.md` (1303 lines). The scope and sequencing below are the durable
repository copy; `/tmp` is not a portable source of truth.

### Phase 0 — security and contracts (first approved implementation unit; no implementation yet)

- [ ] Decide existing-token migration: safe-scope downgrade vs temporary legacy-admin scopes with
      forced rotation warning.
- [ ] Decide UI authentication: Android-Keystore custody plus short-lived HttpOnly loopback session
      is recommended; retain bearer auth for Codex/Aider/API clients.
- [ ] Decide roles/scopes and administrative split. Recommended initial scopes:
      `chat:invoke`, `repo:read`, `run:create`, `run:approve`, `provider:manage`,
      `credential:manage`, `budget:manage`, `audit:read`, `admin`.
- [ ] Add token scopes and centralized endpoint authorization; fail closed on unknown scopes;
      expose effective scopes in principal/dashboard metadata; audit privileged denials/actions.
- [ ] Harden the Android WebView: exact loopback navigation allowlist, external-browser handoff,
      file/content/mixed-content restrictions, Safe Browsing, credential/logout cleanup.
- [ ] Remove long-lived dashboard bearer storage from JavaScript `localStorage`.
- [ ] Replace `?ss=<setup-secret>` with a one-time native-to-gateway exchange and short-lived setup
      session; reject replay/expiry and preserve a documented non-Android bootstrap path.
- [ ] Add browser security headers: strict CSP migration, `frame-ancestors`, `nosniff`, referrer
      policy, permissions policy, and `no-store` for authenticated UI responses.
- [ ] Define versioned, provider-neutral contracts (types + JSON schemas only unless separately
      approved): `OrchestrationEvent`, `ApprovalRequest`, `CapabilityDescriptor`, `ToolGrant`,
      `CollaborationRun`, `DelegationTask`, `TaskAttempt`, `Artifact`, `ExternalSession`.
- [ ] Decide whether Phase 0 creates empty orchestration tables or defers tables to Phase 1.
- [ ] Add audit schema/correlation fields; decide whether hash chaining is Phase 0 or deferred.
- [ ] Add authorization, setup-replay, security-header, contract-validation, audit-integrity, and
      Android WebView-policy tests. Keep `go test ./...` green.
- [ ] Update security/Android/interface/OS architecture docs to distinguish shipped behavior from
      target contracts.

**Phase 0 exact existing-file boundary:** `cli-os/internal/state/db.go`,
`security/tokens.go`, `gateway/ingress.go`, `server/server.go`, `gateway/setup.go`,
`gateway/dashboard.go`, optionally `ledger/ledger.go`, `cmd/l00prite/main.go`,
`android/{MainActivity.java,Keys.java,AndroidManifest.xml,res/xml/network_security_config.xml}`,
`public/{dashboard.html,setup.html}`, and directly relevant docs/tests. Proposed new modules:
`security/scopes.go`, optionally `security/websession.go`, `orchestration/{types,validate}.go`,
and optionally `audit/chain.go`. Do not touch provider adapter or bridge algorithms in Phase 0
except mechanical centralized HTTP authorization.

### Phase 1 — durable conversation and event foundation

- [ ] Add durable conversations/messages, collaboration runs, delegation tasks/attempts,
      artifacts/provenance, external sessions, and append-only versioned events.
- [ ] Add SSE or WebSocket event delivery with resume cursor, cancellation, deadlines,
      idempotency, and crash recovery.
- [ ] Persist Playground threads and expose thread list/search/resume in the UI.
- [ ] Unify chat exploration, delegation, approvals, autonomous runs, and artifacts under shared
      correlation identities without replacing the existing run engine.

### Phase 2 — first-class Codex runtime integration

- [ ] Implement a layered Codex runtime adapter, using Codex app-server first, for thread start/
      resume, typed turn/item events, cancellation, session persistence, and approval forwarding.
- [ ] Keep Platform API keys, ChatGPT/Codex login, and enterprise access-token authentication as
      distinct credential/lifecycle modes; never serialize them into prompts or bridge payloads.
- [ ] Add a verified OpenAI Responses adapter and `/v1/responses`; do not alias `openai-native` to
      the generic Chat Completions adapter.
- [ ] Add verified GPT-5.6 catalog/policy support: Sol for complex architecture/coding/final review,
      Terra for read-heavy repository inspection/parallel reconnaissance, Luna for lighter-volume
      tasks where available. Keep policy configurable and fallback explicit.
- [ ] Map Codex sandbox/command/MCP/app/permission requests into LOOPRITE's typed approval inbox;
      support Android notifications for background `needs input` states.

### Phase 3 — bridge v2 and provider-neutral capability broker

- [ ] Preserve the current `l00prite_bridge` API as a compatibility facade but back it with durable
      collaboration/task records rather than an in-memory loop.
- [ ] Add capability descriptors, scoped/expiring tool grants, broker-side argument/schema/policy
      validation, and typed artifacts. Providers never exchange credentials.
- [ ] Support policy-bounded nested delegation, parallel independent reviews, context grants,
      deadlines/retries/cancellation, structured aggregation, and collaboration-level budgets.
- [ ] Enforce independence constraints when requested (different provider/model/vendor family) and
      preserve per-attempt provenance, audit history, and cost.

### Phase 4 — premium application UI

- [ ] Componentize the dashboard with typed API/state boundaries; remove duplicated setup/provider
      form logic.
- [ ] Make Conversations, Runs, Collaborations, Repositories, Approvals, Usage, and Settings the
      primary information architecture.
- [ ] Add streaming response/tool/provider-handoff timeline, collaboration task graph, artifact/
      diff/Markdown viewers, approval inbox, stable run routes, native notifications, and integrated
      repository import.
- [ ] Complete keyboard, screen-reader, focus, contrast, motion, and text-scaling accessibility.

### Phase 5 — scale, reliability, and operations

- [ ] Add durable provider health/latency metrics, verified catalog/pricing refresh, strict handling
      of unpriced models under dollar caps, backpressure/queueing, and load/chaos/live-provider tests.
- [ ] Evolve the state store beyond its single-connection bottleneck while preserving PEP atomicity;
      support an external DB only if multi-user/server deployment is approved.
- [ ] Add tamper-evident audit export/anchoring, structured redacted logging, Android lifecycle/
      background modernization, recovery notifications, and reproducible release controls.

### Explicit exclusions until separately approved

- [ ] Do **not** rewrite the application, replace the PEP/run engine, weaken approvals/budgets/
      denylist/preflight, guess pricing/models, or modify canonical protocol prompts.
- [ ] Do **not** begin Codex execution, `/v1/responses`, bridge v2, nested delegation, capability
      execution, Compose migration, DB replacement, APK release, push, or PR as part of Phase 0.
- [ ] Do **not** create a branch or implementation commit until the Phase 0 human decisions are
      answered in-session.

### Human decisions still required before implementation

- [ ] Existing-token migration and deprecation window.
- [ ] UI credential/session design and non-Android setup bootstrap.
- [ ] Pure scopes vs named roles mapped to scopes; separate admin credential policy.
- [ ] Strict-CSP asset split vs nonce/hash generation.
- [ ] Audit hash chaining now vs later; orchestration tables now vs Phase 1.
- [ ] Authorization to add a Gradle/Android instrumentation test harness.
- [ ] Phase 2 Codex surface/auth modes and Sol/Terra/Luna default policy.
- [ ] Unpriced-provider behavior under strict caps.
- [ ] Single-device-only vs future multi-user/server product boundary.

**Recommended first branch:** `feature/phase-0-security-contracts`<br>
**Recommended first commit:** `feat(security): add scoped auth and orchestration contracts`

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

## Phase 1 — dashboard Runs view (android-architecture.md §8), branch same as above

- [x] `cli-os/public/dashboard.html` (f73ce08, fix follow-up unhashed) — full Runs view:
      create/pre-flight/live/exit lifecycle, exact-match "EXECUTE" Start gate, esc()'d
      2s-polled event feed, approvals inbox, Resume-always-through-fresh-preflight,
      repo clone-from-URL, phone-first hamburger nav. Command allowlist made a required
      field with client-side validation (engine hard-blocks pre-flight without one — an
      e2e-surfaced gap, fixed post-workflow) + a `.btn:disabled` visual-affordance fix.
- [x] `cli-os/public/setup.html` (f73ce08) — wizard copy: footer CLI framing reworded
      platform-neutral; vault-step key copy covers both file-based and env-injected cases;
      network-step TLS/env guidance checked and confirmed already correctly gated on the
      real non-loopback/exposed signal (left untouched).
- [x] Adversarial review (2 Opus lenses) — zero blocking findings; 6 non-blocking findings
      (keyboard access, silent-failure feedback, a poll-race modal reopen, focus-on-open,
      gate-label consistency, one copy nit) all fixed and re-verified.
- [x] E2E verification (Opus + Playwright 1.56.1 against a freshly built real binary) —
      10/10 checks pass, zero console errors, both critical invariants (Start-gate
      exactness, Resume-to-preflight-not-running) asserted via DOM properties not
      screenshots. Scratchpad-only script per this repo's established convention (not
      committed — no reusable Playwright harness exists in the repo yet).
- [x] Ledger/todos/memory updated; lock released; PR #1 description to be updated to
      cover Phase 1.

## Phase 2 — deeper on-device autonomy (android-architecture.md §8), branch same as above

- [x] `cli-os/internal/gitx` + `cli-os/internal/engine/tools.go` (54515f4) — Log(repo,limit)/
      Show(repo,ref) added to the gitx.Client seam (exec: byte-identical passthrough to real
      git log/show; gogit: go-git's Repository.Log + Commit.Patch, direction-verified —
      parent.Patch(commit) so an added file renders as a genuine addition). git_command's
      gogit path now serves an EXACT-MATCH-ONLY subset (bare status; bare diff/diff HEAD;
      log or log -n N/--max-count=N/-N; show <ref> with no flags) instead of unconditionally
      refusing every call — any other shape falls through unchanged to the original refusal.
- [x] `cli-os/internal/ledger` (54515f4) — JSONL mirror rotation (5 MiB default, one backup
      generation, LOOPRITE_LEDGER_MAX_BYTES override) under a mutex serializing check-size/
      rotate/append against concurrent HTTP-request callers. SQLite ledger table untouched
      (out of scope, a separate future decision).
- [x] `android/src/com/l00prite/os/MainActivity.java` (54515f4) — native "Import repo…" SAF
      picker (DocumentsContract, not DocumentFile — confirmed AndroidX-only/absent from the
      platform jar) copying a picked folder into `<filesDir>/imported-repos/<name>`, with
      canonical-path containment checked at the destination root AND every recursive child
      (not just per-segment sanitization).
- [x] Adversarial review (2 Opus lenses) — zero blocking findings; 3 non-blocking (stale
      refusal-message enumeration, an undocumented bare-diff semantic difference between
      backends, an undocumented env var) all fixed.
- [x] Verification (Opus) — full test+race suite; git_command subset live-driven through
      the real engine/Toolbox path with git genuinely stripped from PATH; Show's patch text
      inspected directly for addition-direction correctness; ledger rotation stress-tested
      with 2500 concurrent Append calls (zero panics/corruption, SQLite exactly 2500 rows);
      APK rebuilt + apksigner verify + confirmed SAF code compiled into the dex via strings.
      Explicit, undisguised limitation: no real-device SAF picker/copy verification possible
      (no emulator ABI in this container).
- [x] Architect independently spot-checked the ledger mutex and the SAF path-containment
      check in the committed files (not just trusting sub-agent reports) before committing.
- [x] Explicit scope decisions recorded in android-architecture.md §8 (a726bc2): ssh clones
      stay https-only (no on-device key-provisioning UI yet); Termux bridge/remote-verifier
      deferred (needs its own protocol/auth/trust design pass); SAF export deferred (import
      was the more urgent gap; export's natural path is a git-remote push, same
      key-provisioning gap as ssh).
- [x] Ledger/todos/memory updated; PR #1 description to be updated to cover Phase 2; a
      Gemini Code Assist re-review requested on the PR per maintainer direction.

Deferred to Phase 3+ (see android-architecture.md §8): real-device smoke test (no emulator
ABI possible in this container), clone-from-URL e2e coverage (needs network egress),
~~venice/gemini capability confirmation from an unblocked network~~ — **done, 2026-07-12**:
Gemini pricing/model-lineup (incl. confirming Gemini 3.5 is real) was re-verified first-party
this pass (cloud.google.com was reachable), and Venice was re-checked with zero drift; see the
"xAI Grok + Gemini providers, Venice made selectable" ledger entry. Still deferred: making the
internal `mock` test adapter selectable in the setup wizard (currently only reachable by direct
API/DB injection, and must be named after a real manifest like `anthropic` to be routable —
recorded in failures.md; the new provider-presets dropdown from that same 2026-07-12 pass
deliberately keeps `mock` excluded, enforced by a whitelist + a test), a committed reusable
Playwright harness for future UI regressions (a scratchpad-only script was used again this pass,
per the standing convention below),
ssh clone support once a key-provisioning UI exists, the Termux/remote-verifier bridge
(needs its own design pass), SAF export, split ABIs/app bundle, release signing ceremony,
battery/doze tuning, F-Droid-style reproducible build recipe, on-device model
(Ollama-on-LAN) quickstart.

## PR #1 bot-review fixes + /review skill pass (branch same as above)

- [x] Gemini Code Assist's 3 findings, all fixed and pushed (e820a69): `gitx/exec.go`'s
      `run()` now forces `LC_ALL=C` so the English-substring failure detection (`Commit`'s
      `identityMissing`, `Log`'s empty-repo marker) can't silently break under a localized
      git build; `MainActivity.onPollFinished`'s posted `Runnable` now checks the existing
      `activityDestroyed` flag before touching `webView`/`statusView` (independently
      corroborated by two of the /review skill's own finders); `resolver.go`'s
      `normalizeDNSAddr` now strips an IPv6 zone identifier (e.g. `fe80::1%wlan0`) before
      `net.ParseIP` validation while preserving it in the dial address, with new
      `TestNormalizeDNSAddr` cases. Also added `webView.destroy()` to `onDestroy()` (a
      real native-resource-leak finding from the same finder pass, same root cause as the
      `activityDestroyed` gap).
- [x] `/review` skill run over the full PR diff (8 finder angles: line-by-line,
      removed-behavior, cross-file, reuse, simplification, efficiency, altitude,
      CLAUDE.md conventions — each independently verified against the real code, not taken
      on a single agent's word). Zero CLAUDE.md violations found. 8 non-blocking findings
      posted as inline PR review comments (submitted as one GitHub review, COMMENT event):
      a stale human-review-boundary comment in `engine.go` vs. the new gitx seam's silent
      synthetic-identity commit retry; an undocumented `git_command log` output-shape
      divergence between exec-git and gogit hosts (unlike the neighboring `diff` case,
      which documents its own divergence); a possible duplicate-event race in the
      dashboard's 2s run-event poll under slow round trips (no in-flight guard); a stale
      doc comment on `gitx.Client.Raw` describing a call path `git_command` doesn't
      actually use; two ledger-rotation efficiency notes (mutex scope, per-request
      `os.Stat`); two independently-maintained "safe without approval" git-subcommand lists
      with no shared source of truth; the new SAF-import subsystem bolted onto
      `MainActivity` instead of extracted into its own class.
- [x] Verified after the fixes: `go build/vet/test ./...` clean, APK rebuilt via
      `cli-os/scripts/build-apk.sh` and re-signed/verified (v2+v3 true), validator
      519 PASS / 0 FAIL.
- [ ] None of the 8 review findings are blocking; left for the maintainer to triage
      alongside merge — no further autonomous action queued on them.

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
- [ ] **Persist a provider's originating manifest key separately from its editable display name**
      (deeper fix for a Codex review finding on PR #10, 2026-07-12): renaming a manifest-backed
      "Add provider" preset (e.g. `gemini` → `my-gemini-key`) breaks bare/default-model routing —
      `adapters.ModelsFor(p.Name)` (used by `router.go`'s Rules 3-4 and `/v1/models`) finds no
      catalog under the edited name, so the renamed provider is unreachable except via an explicit
      `name/model` pin and never appears in the model picker. Root cause: an earlier fix in the
      same PR (falling back to the preset's `sample_model` when validating a renamed provider)
      removed the validation failure that used to accidentally guard against saving this exact
      broken state. A real fix needs a schema change — store the preset key the provider was
      added from (e.g. a `manifest_key` column) alongside its user-editable `name`, and have
      `ModelsFor`/the router/the model picker resolve the catalog by that key instead of by name.
      Judged architecturally significant, not a small confident fix, so NOT done inline; a
      client-side warning (fires when a manifest-backed preset's name diverges from its key,
      `setup.html`/`dashboard.html`) shipped instead as the interim mitigation, per maintainer
      decision on the PR. Queued here as real follow-up work, not dropped.
- [ ] **Extend validator byte-parity to the cli-os protocol embed** (small, standalone gated
      follow-up, 2026-07-12; NOT part of the v1.2 batch below — different scope, cli-os-specific):
      `cli-os/internal/engine/protocol/prompts/*.md` is now an 8th verbatim mirror of the six
      canonical loop prompts (added by the consent-gated full-protocol repo-scaffold feature —
      see the ledger), but `scripts/validate-l00prite.js`'s byte-parity check does not cover it
      yet, since extending that check means editing a review-gated file. Until reviewed, this
      mirror's sync is enforced only by convention (like `l00pfiles.go`'s existing hand-copied
      constants), not mechanically.
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
