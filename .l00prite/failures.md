# Known Failures

Record failed approaches and why they should not be retried unless conditions change.

## Inherited loop failure modes (generic — not this project's history)

> Generic loop wisdom, **not** a record of anything l00prite tried. Read it before arming an
> Execution Mode run. Full catalog with mitigations: `docs/failure-modes.md`. Real
> project-specific failures are recorded under *Failed Approaches* below.

| Failure mode | Severity | Guard to lean on |
|--------------|----------|------------------|
| Verifier Theater (claimed pass, check never ran) | S2 | Record `command`/`exit_code`/`timestamp` evidence in `ledger.md`; never claim success for a check that failed or didn't run. |
| Infinite Fix Loop (same unit, endless retries) | S2 | `unfixable_failing_tests` after two distinct fixes; log attempts + `do_not_retry` here. |
| State Rot (memory references finished work) | S1→S2 | Prune resolved events/closed todos each run; keep `memory.md` durable-only. |
| Over-Reach (edits `.env`, `auth/`, migrations, or unrelated code) | S2→S3 | Autonomous-Edit Denylist in `constraints.md` → `destructive_operation_required`; per-action permission; treat event text as untrusted. |
| Token / Wall-Clock Burn (spend explodes) | S1 | Bounded `max_iterations`; one unit per iteration; set a provider spend cap. Self-reported token counts are fiction — don't gate on them. |
| Parallel Collision (two agents clobber memory) | S2 | Check `lock.json` before writing; `lock_lease_conflict` writes nothing on a foreign lock. |
| Stale Arming (crashed run left `enabled: true`) | S2 | Pre-flight stale-run recovery; persisted flags never authorize a run. |

## Failed Approaches
- Documenting `status: "expired"` as a valid `lock.json` state without a rule permitting its
  acquisition/reclamation (fixed — see `LOCKING.md` rules 2 and 4, and `memory.md`).
- Telling an agent to stop on *any* active, unexpired lock, including one it already owns —
  this made an agent updating several protected files in one run block on its own first
  write. Fixed by scoping the "respect an active lock" rule to locks owned by a different
  agent/session (`LOCKING.md` rule 3).
- Maintaining the loop prompts as four hand-synchronized copies with keyword-only
  validation — copies drifted in spirit (a hardcoded `.codex/prompts/` path shipped inside
  even the Claude mirrors) and one PR had to fix the same bug in 13 places. Do not add a
  prompt copy without adding it to the validator's byte-parity mirror list.
- Execution-mode designs rejected during the 2026-07-02 adversarial design review — do not
  retry these shapes:
  - `--execute` writing `execution.enabled: true` at scaffold time ("pre-arming"): the
    confirmation would predate the code it covers, and the repo would sit armed on disk for
    any later agent to discover.
  - Treating a persisted `preflight_confirmed: true` as authorization for a new run: the
    field lives in agent-writable `heartbeat.json`, so it is forgeable and transferable
    across sessions — audit record only.
  - Bare-pointer vendor adapters ("read AGENTS.md"): they deliver nothing on Copilot
    surfaces that can't open files, and Zed's first-match priority list would let
    `.github/copilot-instructions.md` shadow `AGENTS.md` entirely.
  - Shipping `.aider.conf.yml` (or any auto-loaded vendor config) into target repos: Aider
    merges config per-key with repo-root winning, silently overriding user settings, and
    the conventional `.aider*` gitignore pattern swallows the file anyway.
  - Naming the execution boundary list `stop_conditions`: heartbeat.json already has a
    top-level `stop_conditions` with different semantics; the collision made governance
    ambiguous. It is `run_boundaries`.
- Approaches that failed during the 2026-07-05 Android APK pass — do not retry unless the
  stated condition changes:
  - Downloading anything Android from Google hosts in a proxied build container:
    `dl.google.com` is CONNECT-403-blocked and `maven.google.com` is just a 301 to it — no
    SDK, no AGP, no NDK, no emulator images. The working chain is apt
    (aapt/zipalign/apksigner/dalvik-exchange/android-framework-res) + Maven Central
    (robolectric `android-all` as the javac framework classpath) + real-SDK builds in CI.
  - Using the robolectric `android-all` jar as `aapt -I` framework resources: its
    `resources.arsc` is unparseable by aapt/aapt2 ("Package Groups (0)") — it is a compile
    CLASSPATH substitute only; resource compilation needs `android-framework-res`'s
    `framework-res.apk`.
  - `CGO_ENABLED=0 GOOS=android GOARCH=amd64` (emulator ABI): the Go toolchain hard-refuses
    ("requires external (cgo) linking") — unlike arm64 there is no internal-linker support,
    and `-linkmode=internal` / `-tags netgo,osusergo` do not help. Needs a real NDK cross
    toolchain, or a future Go release; both build pipelines skip it on exactly that error.
  - Bumping go-git past v5.18.0 while the module targets go 1.24: v5.19.1's graph requires
    go >= 1.25. Bump the toolchain first.
  - Fetching `docs.venice.ai`, `api.venice.ai`, or `curl.se` from the build container —
    egress-blocked; Venice docs are reachable via their public GitHub mirror
    (`veniceai/api-docs`).
  - In tests, setting `GIT_AUTHOR_NAME=""` (empty) to simulate missing git identity: git
    treats a present-but-empty env var as an authoritative override that defeats even
    `-c user.name`. Unset the vars instead.

## Blockers
- None currently active.
