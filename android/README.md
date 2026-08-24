# L00prite OS — Android wrapper app

A thin Java wrapper (no AndroidX/Jetpack, framework APIs only) around the unmodified
`l00prite` gateway binary (`cli-os/cmd/l00prite`). The APK ships the Go gateway as
`lib/<abi>/libl00prite.so`, execs it from the app's writable-but-executable
`nativeLibraryDir`, and points a WebView at its loopback HTTP server. Design rationale,
feasibility analysis, and the full gap analysis this app implements live in
[`cli-os/docs/android-architecture.md`](../cli-os/docs/android-architecture.md) — read
that first; this file is the "how to build/run it" companion.

Package: `com.l00prite.os`. `minSdkVersion` 26 (Android 8.0), `targetSdkVersion` 34.

## What it does

1. `MainActivity` starts `GatewayService` (a foreground service) and shows a WebView.
2. `GatewayService` prepares an app-private environment (an Android-Keystore-wrapped
   master key, a per-install setup secret, an extracted CA bundle, the device's real DNS
   servers) and execs `libl00prite.so serve` with that environment, supervising it
   (drain stdout to logcat, restart on crash with backoff).
3. Once the gateway answers `GET /healthz`, `MainActivity` loads
   a short-lived HttpOnly setup session natively, then opens `http://127.0.0.1:8787/` in the
   WebView. The install secret never enters the URL or JavaScript. From there it's the exact
   same setup wizard → dashboard → playground → runs flow that runs on desktop —
   nothing in the gateway/dashboard changes for Android.

There is no separate "Android mode": the same binary that ships for
linux/darwin/windows runs here; only the wrapper around it is platform-specific.

## Environment contract

`GatewayService` launches `libl00prite.so serve` with exactly this environment (see
`android-architecture.md` §3, "boot sequence", and §4 for the gap each variable closes):

| Variable | Value | Why |
|---|---|---|
| `LOOPRITE_HOME` | `<filesDir>/l00prite` | App-private data root (SQLite vault, `.l00prite/` repo clones). |
| `HOME` | `<filesDir>` | Android execs a process with no `HOME`; without it `os.UserHomeDir()` fails and data would land in `/` (gap G5). |
| `LOOPRITE_PORT` | `8787` | Fixed loopback port the WebView and health poll both target. |
| `LOOPRITE_MASTER_KEY` | Base64 of 32 random bytes | Unwrapped in memory each boot from an Android-Keystore-wrapped ciphertext (gap G6); `master.key` never exists as a file on this device. |
| `LOOPRITE_SETUP_SECRET` | 32 random hex chars, per install | Exchanged natively once for a short-lived HttpOnly setup session, gating pre-latch setup against other apps racing first run (gap G7). Never sent in the WebView URL or exposed to JavaScript. |
| `SSL_CERT_FILE` | `<filesDir>/cacert.pem` | Android 14+ moved system CA roots out of reach of Go's `crypto/x509`; a Mozilla CA bundle ships as an APK asset and is extracted here (gap G2). |
| `LOOPRITE_DNS` | Comma-joined IPs from `ConnectivityManager.getLinkProperties`, else `8.8.8.8,1.1.1.1` | Android apps cannot read `/etc/resolv.conf`; Go's resolver needs explicit servers or every outbound provider call fails DNS (gap G1). |
| `GIT_AUTHOR_NAME` / `GIT_AUTHOR_EMAIL` | `l00prite-os` / `l00prite-os@localhost` | Default commit identity for on-device runs (no `git config --global` on Android). |
| `GIT_COMMITTER_NAME` / `GIT_COMMITTER_EMAIL` | same as above | ditto |

`GatewayService` does not currently set `LOOPRITE_LEDGER_MAX_BYTES`, so it isn't part of the
fixed contract above; it's mentioned here because it's the one ledger/storage knob most
relevant to a storage-constrained device. It caps the size (default 5 MiB, plus one `.1`
backup generation, so ~10 MiB worst case) of the human-readable JSONL mirror of the ledger
under `LOOPRITE_HOME` before it rotates; the durable, unbounded record always stays in
SQLite regardless. An empty, non-numeric, zero, or negative value falls back to the
default rather than disabling rotation. To raise or lower it on-device, add it to the
`env` map in `GatewayService.launchGateway()` (or launch `libl00prite.so serve` manually
with it set, e.g. from an `adb shell` on a debug build) — there is no dashboard/UI control
for it today.

## How to build

Two independent pipelines consume the same `android/` sources (`android-architecture.md`
§7):

### 1. `cli-os/scripts/build-apk.sh` — hermetic, no Google-hosted downloads

Uses this environment's non-Gradle toolchain: `aapt` (v1) + Ubuntu's
`android-framework-res` package for resource compilation, `javac --release 8` against
Robolectric's `org.robolectric:android-all` jar as a framework compile classpath,
`dalvik-exchange` (`dx`) for dexing, and `apksigner`/`zipalign` for signing/alignment.

```
bash cli-os/scripts/build-apk.sh [VERSION]
```

Output: `cli-os/dist-android/l00prite-os-<VERSION>.apk` plus a `SHA256SUMS` file next to
it. See the script's own comments for the exact tool-check / fallback-download / cache
behavior. Requires (all preinstalled in this repo's build environment; see the script's
error messages otherwise): `aapt`, `zipalign`, `apksigner`, `dalvik-exchange`, `javac`,
`go`, and the `android-framework-res` package (`/usr/share/android-framework-res/framework-res.apk`).

### 2. `.github/workflows/android-apk.yml` — real Android SDK, CI-reproducible

Runs on GitHub-hosted runners (which ship `ANDROID_HOME` with the real SDK): `go test
./...` + the l00prite protocol validator first, then builds the same `android/` sources
against `android-34` platform + build-tools 34.0.0 using `javac` against the real
`android.jar`, `d8` instead of `dx`, and a job-ephemeral debug keystore. Uploads the
signed APK + `SHA256SUMS` as a workflow artifact (`l00prite-os-apk`). This is the
reproducibility/CI check; it is not where release signing happens (see below).

## Importing a repo via Storage Access Framework

A small "Import repo..." button sits in the bottom-right corner of `MainActivity`, on top
of the WebView (native Android UI, no JavaScript bridge — it works independently of the
dashboard). It closes a specific gap: a repo reachable only through Android's Storage
Access Framework (a synced folder, a file manager, another app's exposed documents) hands
you a `content://` tree URI, not a real filesystem path, and the gateway's git tooling
(both real `git` and the `go-git` fallback, gap G4) needs a real path to operate on.

Tapping it launches the system `ACTION_OPEN_DOCUMENT_TREE` picker (classic
`startActivityForResult`, no `androidx.activity` Result API — this app takes no AndroidX
dependency anywhere), takes a persistable **read-only** grant on the chosen tree, then
walks and copies it — `.git` included, nothing hidden is skipped — on a background thread
into `<filesDir>/imported-repos/<name>`, where `<name>` is a sanitized form of the picked
folder's display name (or a random fallback if that's unavailable/unsafe). The tree is
walked directly via the framework's `android.provider.DocumentsContract`, not
`DocumentFile`: `DocumentFile` has only ever shipped in the (AndroidX) support library,
never as a platform class, so it isn't an option here.

On completion, a dialog shows the resulting absolute on-device path with a "Copy path"
button (`ClipboardManager`). Paste that path into the dashboard's existing "register an
existing path" field to finish registering the repo — this feature only gets a repo onto
the device; the dashboard flow (unchanged) is still how it becomes usable to the gateway.

## How to sideload

```
adb install -r cli-os/dist-android/l00prite-os-<VERSION>.apk
```

First launch: the app requests the `POST_NOTIFICATIONS` permission (Android 13+, so the
foreground-service notification can show; the service runs either way if you deny it),
starts the gateway, and once `/healthz` answers, loads the setup wizard. Add provider
keys, register or clone a repo, and use the playground / runs API exactly as on desktop.

## Known limitations (Phase 0 prototype)

- **No on-device verification toolchains (gap G11).** Stock Android has no `go`, `npm`,
  test runners, etc. Execution-Mode units whose verification step is a shell command the
  device can't run will stop at the `unfixable_failing_tests` run boundary. Everything
  that doesn't require a toolchain — the gateway itself, routing, cross-provider
  bridging, memory injection, clone/commit flows, the playground — is fully functional.
  Termux-bridge / remote-verifier options are a Phase 2 candidate.
- **No native Runs UI yet.** Runs are created/pre-flighted/started/approved/stopped via
  the existing `/v1/runs*` API (curl, or the dashboard Playground) — a dedicated
  dashboard Runs view is Phase 1 (already specced in `cli-os/docs/os-architecture.md`
  §4, queued next).
- **No Play Store packaging, no split ABIs / app bundle, no background-execution
  guarantees beyond the foreground service** (Phase 3 territory — see
  `android-architecture.md` §8-§9).
- **`git_command`/ssh-URL clones need a `git` binary that stock Android doesn't have.**
  Core git operations (clone over HTTPS, status, branch, commit, diff) fall back
  automatically to the pure-Go `go-git` implementation server-side (gap G4, implemented
  in `cli-os/internal/gitx`); the model-facing raw `git_command` passthrough tool and
  ssh-URL clones are unavailable in that fallback and return a clear error.
- **CI and a bare `build-apk.sh` are debug-signed.** Both pipelines default to a
  locally/job-generated debug keystore (`storepass android`, `CN=L00prite Debug`), never a
  value from repo secrets. **Published APKs on the website are release-signed** starting
  at `0.10.1-beta`, using the dedicated LOOPRITE production keystore via env override
  (never committed):

  ```
  APK_KEYSTORE=/path/to/l00prite-release.keystore \
  APK_KS_ALIAS=l00prite \
  APK_KS_PASS=... \
  bash cli-os/scripts/build-apk.sh <version>
  ```

  That keystore is the permanent update-identity of every installed app. Losing it strands
  every install. A debug-signed APK cannot update a release-signed one (or vice versa) —
  Android refuses the install.

## Security notes

- **Keys at rest**: the 32-byte `LOOPRITE_MASTER_KEY` is generated once, wrapped with a
  non-exportable AES-256-GCM key that lives only in the Android Keystore
  (`Keys.java`), and only the wrapped ciphertext is ever written to app-private
  `SharedPreferences`. The raw key exists only in the gateway process's environment —
  never as a file — matching desktop's existing "env overrides key file" precedence, so
  `master.key` never touches flash on this device.
- **Setup secret**: a per-install random token gates the pre-authentication `/v1/setup/*`
  endpoints so another app on the same device can't race first-run setup and mint the
  admin token (gap G7). It is deliberately stored in plain `SharedPreferences` — see the
  comment on `Keys.getOrCreateSetupSecret()` for why that's an acceptable trade-off (it
  is not a long-term credential, and the file is already private to this app's UID).
- **Loopback surface**: `network_security_config.xml` permits cleartext HTTP to
  `127.0.0.1` only; `android:usesCleartextTraffic="false"` denies it everywhere else.
  Every data/run API endpoint still requires the existing Bearer-token auth; only the
  pre-latch setup endpoints are additionally gated by the setup secret.
- **The gateway's own protections are unchanged on-device**: repo jail, protocol-file
  hard-deny, the Autonomous-Edit Denylist, command allowlist, per-action approvals
  (fail-closed on timeout), all nine run boundaries, and the confirmed pre-flight gate
  (`confirm:"EXECUTE"`) all carry over from `execute-loop.md` unmodified.
- **App storage** is additionally sandboxed and file-based-encrypted by the platform on
  top of the above.
