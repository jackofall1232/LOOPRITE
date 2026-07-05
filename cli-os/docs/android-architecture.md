# L00prite OS on Android — architecture, feasibility, and roadmap

Status: **v1 of this design — implemented in prototype form on branch
`claude/looprite-android-apk-4mth8g`** (maintainer brief, 2026-07-05).
Author: Claude (Fable 5) as architect; peripheral units written by Sonnet-class writer
agents to this spec, reviewed by the architect.

This document is deliverables 1–3 of the Android brief: (1) the architecture plan,
(2) the feasibility decision for embedding/running the CLI-OS gateway on Android, and
(3) the phased implementation roadmap. It extends `os-architecture.md` (the L00prite OS
v2 design); nothing here replaces that document.

---

## 1. Vision

A user sideloads one APK. On first launch they add provider API keys (stored encrypted
on-device), connect or clone a Git repo, enter a prompt, and let L00prite run the project
end-to-end **from the phone**. The Android device is the *local control plane*: there is
no hosted L00prite server, no account, no telemetry. Everything that exists today on
desktop — the OpenAI-compatible gateway, deterministic + auto routing, role-aware
profiles, cross-provider bridging, budget PEP, the run engine mechanically embodying
`execute-loop.md`, and `.l00prite/` repo memory — runs unchanged on the device.

## 2. Feasibility decision — how the gateway runs inside an APK

### Options considered

| Option | Verdict | Why |
|---|---|---|
| **A. Bundled native binary**: cross-compile the existing `l00prite` Go binary for `android/arm64`, ship it inside the APK as `lib/arm64-v8a/libl00prite.so`, exec it from the app's `nativeLibraryDir`, wrap with a thin Java app (foreground service + WebView on `http://127.0.0.1:<port>/`) | **CHOSEN** | Reuses ~100% of the existing gateway/dashboard/engine; one code path for desktop and Android; process isolation (a gateway crash cannot take down the app, and `os.Exit(1)` boot failures stay contained); trivially updatable by swapping one artifact. Proven in this repo's environment — see evidence below. |
| B. `gomobile bind` (.aar, in-process JNI) | Rejected | Requires the NDK toolchain; `server.Start` blocks forever and calls `os.Exit(1)` on fatal config errors, which in-process would kill the whole app — it would force an entrypoint refactor for no functional gain; harder to keep byte-parity with the desktop binary; gomobile's type restrictions add an interface layer nobody needs since all communication is already HTTP-on-loopback. |
| C. Depend on Termux (install binary into Termux env) | Rejected | Not self-contained: violates the "sideload one APK" requirement; Termux's Play/packaging situation is unstable; no control over lifecycle. |
| D. Rewrite the control plane natively (Kotlin/Flutter/RN) | Rejected | Discards the working dashboard/wizard/gateway; permanent double-maintenance; contradicts the brief ("build on the existing CLI-OS/gateway/dashboard"). |

### Evidence the chosen path works (verified 2026-07-05, this branch's environment)

1. **Cross-compile works today with zero code changes**:
   `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/l00prite` succeeds from
   current source and produces `ELF 64-bit LSB pie executable, ARM aarch64 …
   interpreter /system/bin/linker64` — a position-independent executable linked against
   the Android dynamic linker. No NDK, no gomobile, no cgo: the only dependency is
   `modernc.org/sqlite` (pure Go).
2. **Exec-from-APK is Android-sanctioned**: since API 29, apps may not exec binaries
   from writable app storage (W^X), but the app's `nativeLibraryDir` is executable by
   design. Shipping the gateway as `lib/arm64-v8a/libl00prite.so` with
   `android:extractNativeLibs="true"` lands it in `nativeLibraryDir` at install time;
   `Runtime.exec` from there is the same mechanism production apps use to ship
   helper binaries.
3. **A complete APK build chain exists without Google-hosted tooling** (relevant because
   this build environment blocks `dl.google.com`, and useful generally for reproducible
   vendor-neutral builds): Ubuntu's `aapt`, `zipalign`, `apksigner`, `dalvik-exchange`
   (dx) + `android-framework-res` packages, `javac --release 8` against Maven Central's
   `org.robolectric:android-all` framework jar. A signed (v2+v3) hello-world APK with a
   WebView activity, a service, and a native-lib payload was produced and verified with
   `apksigner verify` and `aapt dump badging` before this design was committed. CI
   builds additionally use the real Android SDK (present on GitHub-hosted runners).
4. **The dashboard is WebView-clean**: both embedded pages are fully self-contained
   vanilla JS (no CDN, no external fonts, no WebSockets, no window.open/downloads/file
   inputs), all API calls are same-origin relative `fetch()` with a Bearer token in
   `localStorage`, viewport meta + responsive breakpoints + dark scheme already exist.

**Decision: Option A.** The APK = thin Java wrapper (no AndroidX, framework APIs only)
+ the unmodified-in-spirit Go gateway binary + the embedded dashboard it already serves.

## 3. System architecture on the device

```
┌───────────────────────────── Android app (com.l00prite.os) ─────────────────────────────┐
│                                                                                          │
│  MainActivity ──────────────► WebView  http://127.0.0.1:8787/                            │
│   (single activity)            │  setup wizard → dashboard → playground → runs API       │
│                                │  (cleartext-to-127.0.0.1 via networkSecurityConfig)     │
│  GatewayService (foreground, dataSync)                                                   │
│   ├─ first boot: generate 32-byte master key, wrap with Android Keystore AES key,        │
│   │              store ciphertext in app prefs; extract assets/cacert.pem → filesDir     │
│   ├─ exec  nativeLibraryDir/libl00prite.so  serve                                        │
│   │        env: LOOPRITE_HOME=<filesDir>/l00prite     HOME=<filesDir>                    │
│   │             LOOPRITE_MASTER_KEY=<unwrapped key>   LOOPRITE_PORT=8787                 │
│   │             LOOPRITE_SETUP_SECRET=<per-install>   SSL_CERT_FILE=<filesDir>/cacert.pem│
│   │             LOOPRITE_DNS=<from LinkProperties, else 8.8.8.8,1.1.1.1>                 │
│   │             GIT_AUTHOR/COMMITTER identity defaults                                   │
│   ├─ watches child; restarts on crash; stops on service destroy                          │
│   └─ notification = the OS-mandated foreground service notification                      │
│                                                                                          │
│  l00prite gateway process (Go, android/arm64 PIE)                                        │
│   ├─ HTTP 127.0.0.1:8787 — wizard/dashboard/API exactly as on desktop                    │
│   ├─ SQLite (pure Go, WAL) + master-key-encrypted provider vault under LOOPRITE_HOME     │
│   ├─ run engine (execute-loop.md mechanically) + .l00prite/ memory in each repo          │
│   └─ outbound HTTPS to api.anthropic.com / api.openai.com / api.venice.ai / …            │
└──────────────────────────────────────────────────────────────────────────────────────────┘
```

Everything below `GatewayService` is the same binary that ships for linux/darwin/windows.

### Boot sequence

1. `MainActivity.onCreate` starts `GatewayService` (foreground) and shows the WebView.
2. `GatewayService` prepares the environment (keys, CA bundle, DNS list), starts the
   gateway process, waits for `GET /healthz` to answer, then broadcasts readiness.
3. WebView loads `http://127.0.0.1:8787/?ss=<setup-secret>` — the server serves the
   setup wizard until the setup latch flips, then always the dashboard. No client-side
   routing state exists, so a cold app start always lands correctly.
4. Process death at any point is safe: engine boot runs `ReconcileOrphans` (interrupted
   runs are marked, stale repo state is disarmed at next pre-flight) — Android's
   process-killing behavior maps onto crash paths the engine already handles.

## 4. Platform gap analysis and adaptations (all additive; desktop behavior unchanged)

| # | Gap on Android | Adaptation | Where |
|---|---|---|---|
| G1 | No `/etc/resolv.conf` → Go's pure resolver falls back to `localhost:53`; **all outbound provider calls fail DNS** | `LOOPRITE_DNS=ip[,ip…]` env: when set, install a `net.DefaultResolver` with `PreferGo` and a custom dialer over the listed servers (rotating). When unset and `GOOS==android` and `/etc/resolv.conf` is absent, default to `8.8.8.8,1.1.1.1`. Wrapper passes the real per-network DNS from `ConnectivityManager.getLinkProperties`. | `internal/util` + `cmd/l00prite` init |
| G2 | System CA roots unreachable from Go on Android 14+ (moved into the conscrypt APEX; Go only scans `/system/etc/security/cacerts`) | Ship a Mozilla CA bundle as an APK asset, extract to `filesDir`, export `SSL_CERT_FILE` — Go's `crypto/x509` honors it natively. Zero Go changes. | wrapper + build script |
| G3 | `run_command` execs hardcoded `/bin/sh` (absent on Android; the shell is `/system/bin/sh`) | Resolve the shell once: `LookPath("sh")`, else `/bin/sh`, else `/system/bin/sh`. | `internal/engine/tools.go` |
| G4 | No `git`/`ssh` binaries: pre-flight hard-blocks, `/v1/repos/clone` 500s, unit commits impossible | New `internal/gitx` seam: `Client` interface with the engine's 8 primitives (clone, rev-parse HEAD, status --porcelain, checkout -B, add -A, commit, diff HEAD, raw). Default impl = exec `git` (byte-for-byte today's behavior when git exists). Fallback impl = pure-Go **go-git** used automatically when `git` is not in PATH: HTTPS clone (depth 1), status, branch, commit (with explicit fallback identity), diff. The model-facing `git_command` passthrough tool stays exec-only and returns a clear "requires the git binary; core git operations still work via the built-in implementation" error under go-git. ssh-URL clones require exec git + ssh and are rejected under go-git. | `internal/gitx`, rewires `engine/preflight.go`, `engine/tools.go`, `gateway/repos_clone.go` |
| G5 | `HOME` unset for an exec'd process → `os.UserHomeDir` fails, data lands in cwd (`/`) | Wrapper always exports `LOOPRITE_HOME` (and `HOME`) into app-private `filesDir`. Already supported by config; no Go change. | wrapper |
| G6 | Plaintext `master.key` on disk is weaker than platform storage | Wrapper generates the 32-byte master key, wraps it with a non-exportable Android Keystore AES-GCM key, stores only ciphertext in prefs, and injects the plaintext via `LOOPRITE_MASTER_KEY` env at process start (env takes precedence over the key file by existing design; `master.key` never exists on flash). Zero Go changes. | wrapper |
| G7 | Any co-installed app can reach `127.0.0.1:8787`; the **pre-latch `/v1/setup/*` endpoints are unauthenticated** → a malicious app could race first-run setup and mint the admin token | New optional `LOOPRITE_SETUP_SECRET` env: when set, every `/v1/setup/*` POST requires header `x-l00prite-setup-secret` (constant-time compare). The wizard page reads `?ss=…` once from its URL (never persisted) and attaches the header. Wrapper generates a per-install secret. Desktop behavior unchanged when env unset. Post-latch, everything already requires Bearer tokens. | `gateway/setup.go`, `public/setup.html`, wrapper |
| G8 | `run_command`/`git_command` children inherit the full env → `LOOPRITE_MASTER_KEY` and `LOOPRITE_SETUP_SECRET` would leak into model-directed shell commands | Scrub both vars from child env in the engine toolbox and repo-clone exec paths (desktop hardening too). | `engine/tools.go`, `gateway/repos_clone.go` |
| G9 | WebView blocks cleartext HTTP on targetSdk ≥ 28 (loopback is **not** exempt) | `network_security_config.xml` permitting cleartext to `127.0.0.1` only. | wrapper |
| G10 | Long-lived runs vs. Android process killing | Foreground service (`dataSync` type). Runs die with the process but are crash-recoverable by the engine's existing `ReconcileOrphans` + stale-run pre-flight recovery; resumability is a protocol property (`.l00prite/` files), not process state. | wrapper |
| G11 | On-device verification toolchains (go test, npm…) don't exist on stock Android | Out of scope for the prototype (documented limitation): runs whose units verify via shell commands will stop at `unfixable_failing_tests` unless the verification command is something the device can run. Roadmap: Termux-bridge / remote-verifier options in Phase 2+. The gateway, playground, routing, bridging, memory injection, clone-and-commit flows are all fully functional without any toolchain. | docs |

## 5. Provider layer expansion (Venice AI + roles)

### 5.1 Dedicated Venice AI path

Venice is OpenAI-compatible (`https://api.venice.ai/api/v1`), so it plugs into the
existing `openai-compat` adapter — the dedicated path is a first-class **embedded
manifest** (`manifests/venice.json`), giving Venice the same status as Anthropic/OpenAI/
Zhipu: default base URL (`l00prite provider add venice --key …` just works), a routable
model catalog with **first-party-verified pricing** (fetched from Venice's own docs
mirror, 2026-07-05), auto-routing candidacy, and price metering. Venice's catalog also
resells Claude (`claude-sonnet-5`, `claude-fable-5`), GPT/Codex (`openai-gpt-52-codex`),
Gemini and GLM models — meaning a Venice-only user still gets multi-family routing
through one key, and cross-provider bridging can delegate between native providers and
Venice-hosted models.

### 5.2 Gemini

`manifests/gemini.json` targets Google's OpenAI-compatible endpoint
(`https://generativelanguage.googleapis.com/v1beta/openai`) with the `openai-compat`
adapter. Model shapes/pricing marked at the confidence the sources support; unpriced or
unverified entries deliberately sort last in cost-preference routing (existing tier
rules).

### 5.3 OpenAI / Codex

`manifests/openai.json` already exists; its models remain `PENDING-first-party-
confirmation` per this repo's pricing-verification discipline (unverifiable from this
build environment). OpenAI/Codex remain fully usable today via (a) explicit
provider-qualified pins (`openai/<model>` passes through), (b) custom base URL
providers, or (c) Venice's verified `openai-gpt-52-codex` / `openai-gpt-52` entries.

### 5.4 Roles: architect / writer / reviewer / advisor

The brief's role model maps onto the existing profile + `roleRanks` machinery — no new
mechanism. Added built-in profiles (all overridable in `config.json`):

| Profile | Preference | Ranks encode |
|---|---|---|
| `auto:architect` | quality | **Fable 5 first** (then Opus, GLM-5.x, Venice-hosted equivalents) — planning/scaffolding/instructions |
| `auto:writer` | quality, requires tools | **Sonnet 5 first** — bulk implementation. Deliberately not `balanced`: a cost blend would silently hand the writing role to the cheapest tools-capable catalog entry (observed with Venice pricing loaded), making the stated policy decorative. Cost control stays with the PEP caps and `cheap`/`balanced` profiles. |
| `auto:reviewer` | quality | Fable/Opus-class first — adversarial review |
| `auto:advisor` | balanced | strong generalists, cost-aware — consultation |

Default `roleRanks` maps are seeded for these four **and** for the engine's internal
`plan`/`code`/`review`/`summarize` roles, so on-device Execution Mode runs inherit the
same policy: Fable 5 architects, Sonnet 5 writes. Cross-provider bridging
(`x-l00prite-bridge`) is unchanged and lets any primary model delegate a sub-task to
`auto:architect`/`auto:writer`/another provider under the existing hop cap.

## 6. Security model on-device

- **Keys at rest**: provider keys AES-256-GCM in SQLite (existing vault) under a master
  key that exists only (a) wrapped by Android Keystore in app prefs and (b) in the
  gateway process env — never as a file (G6). App storage is additionally sandboxed +
  FBE-encrypted by the platform.
- **Loopback surface**: Bearer-token auth on all data/run endpoints (unchanged);
  first-run setup gated by the per-install setup secret (G7); secrets scrubbed from
  model-directed child processes (G8).
- **The engine's protections carry over unchanged**: repo jail, protocol-file hard-deny,
  Autonomous-Edit Denylist, command allowlist, per-action approvals (fail-closed on
  timeout), nine run boundaries, confirmed pre-flight gate (`confirm:"EXECUTE"` typed in
  the dashboard — persisted flags never satisfy it).
- **What we do NOT do**: no cleartext except loopback; no non-loopback bind (the
  existing no-TLS-non-loopback boot refusal stays authoritative); no secrets in the APK;
  the APK signing keystore is generated at build time / held as a CI secret and is never
  committed (also enforced culturally by the `**/*_key*` denylist glob).

## 7. Build & release pipeline

Two independent ways to produce the APK; both consume the same `android/` sources:

1. **`cli-os/scripts/build-apk.sh` (sibling of `dist.sh`) — hermetic, no Google downloads.** Go cross-compile
   (`android/arm64` + `android/x86_64` for emulators) → binaries placed as
   `lib/<abi>/libl00prite.so` → `aapt package` against `android-framework-res` →
   `javac --release 8` against the Robolectric `android-all` jar → `dalvik-exchange
   --dex` → zip assembly (`.so` entries **stored uncompressed**) → `zipalign` →
   `apksigner sign` (v2+v3) with a locally generated debug keystore → `apksigner
   verify` + `aapt dump badging` as built-in verification. Runs in this repo's CI-less
   container today.
2. **`.github/workflows/android-apk.yml` — real-SDK reproducible build.** GitHub-hosted
   runners ship the Android SDK; the workflow builds the same sources with `aapt2`/
   current build-tools, runs `go test ./...` + the validator first, and uploads the
   signed APK + `SHA256SUMS` as artifacts. (Note: `.github/workflows/**` is on the
   Autonomous-Edit Denylist — this file ships in a human-reviewed PR, exactly as that
   gate intends.)

`scripts/dist.sh` (desktop 5-target matrix) is deliberately untouched.

## 8. Phased roadmap

- **Phase 0 — this branch (prototype, "smallest useful APK")**: everything in §4
  G1–G10, Venice + Gemini manifests, role profiles, `android/` wrapper app, both build
  pipelines, docs. User can: sideload → wizard (keys, encrypted) → clone/register repo →
  playground chat with memory injection → create/pre-flight/start/approve/stop runs via
  the existing `/v1/runs*` API (curl/Playground; native Runs UI is Phase 1) → commits
  land on `l00prite/run-*` branches with `.l00prite/` memory updated.
- **Phase 1 — Runs UI + mobile polish**: the dashboard Runs view (already specced in
  `os-architecture.md` §4 — it was the queued next unit before this brief), phone-first
  nav affordance (< 1000 px sidebar), repo-register path picker, wizard copy that stops
  referencing CLI/TLS env vars, Playwright e2e against the APK's gateway binary.
- **Phase 2 — deeper on-device autonomy**: `git_command` subset mapped onto go-git
  (status/diff/log/show), ssh clones via embedded ssh (or keep https-only), optional
  Termux bridge / remote-verifier so `run_command` verification has real toolchains,
  SAF import/export of repos, ledger/JSONL rotation for constrained storage.
- **Phase 3 — distribution hardening**: split ABIs / app bundle, release signing
  ceremony, battery/doze tuning (WorkManager-scheduled resumption), F-Droid-style
  reproducible build recipe, on-device model (Ollama-on-LAN) quickstart.

## 9. Non-goals of the prototype

No Play Store packaging, no AndroidX/Compose UI (the dashboard is the UI), no gomobile,
no background execution guarantees beyond the foreground service, no on-device
compiler toolchains, no push notifications for approvals (Phase 1+ candidates), no
change to the two review-gated files (`.claude/commands/build-loop.md`,
`scripts/validate-l00prite.js` — zero-line diffs, verified in the PR).

## 10. Verification matrix (what "done" means for Phase 0)

| Check | Command / method |
|---|---|
| Go suite (incl. gitx fallback + setup-secret + resolver tests) | `go test ./...` |
| Protocol validator | `node scripts/validate-l00prite.js` → zero FAIL |
| Repo health | `node scripts/l00prite-doctor.js .` → HEALTHY (post lock-release) |
| Android binary is a valid Android PIE | `file` output: ELF aarch64 PIE, interpreter `/system/bin/linker64` |
| APK builds + signs locally with no Google downloads | `cli-os/scripts/build-apk.sh` → `apksigner verify --verbose` (v2+v3 true) |
| APK manifest sane | `aapt dump badging` — launchable activity, service, INTERNET permission, `native-code: 'arm64-v8a'` |
| Gateway e2e smoke (same code the APK runs) | linux build: wizard-latch + provider add (mock) + playground round-trip + venice/gemini manifest routing visible in `/v1/models` + `route plan auto:writer` |
| CI workflow builds the APK with the real SDK | GitHub Actions run on the PR |
