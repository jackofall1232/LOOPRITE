#!/usr/bin/env bash
# Build the L00prite OS Android wrapper APK — hermetic pipeline, no Google-hosted
# downloads (see cli-os/docs/android-architecture.md section 7, pipeline 1).
#
# Toolchain sources (all from apt on this build environment, no dl.google.com):
#   - javac/keytool          : OpenJDK (apt)
#   - aapt (v1), zipalign,
#     apksigner, dalvik-exchange (dx) : apt
#   - android-framework-res  : apt (real "android" package resources.arsc for aapt -I;
#                              org.robolectric:android-all's own bundled resources.arsc
#                              is NOT usable for that — aapt reports "Package Groups (0)")
#   - org.robolectric:android-all jar : Maven Central (javac compile classpath substitute
#     for the Android framework — NOT used for resource compilation, see above)
#
#   cli-os/scripts/build-apk.sh [VERSION]
#
# VERSION defaults to `git describe --tags --always --dirty`, or "dev" outside a git tree.
# Output lands in cli-os/dist-android/ (NOT covered by any existing .gitignore pattern —
# unlike cli-os/dist/, which IS covered by this repo's generic top-level "dist" ignore
# rule; this is a deliberate note for whoever reviews the PR, since this script's own
# permission scope does not include touching .gitignore).
set -euo pipefail

# --- 1. resolve dirs -------------------------------------------------------------------
# This script lives in cli-os/scripts/; walk up to the actual repo root (one level above
# cli-os/), which is where android/ lives, matching cli-os/scripts/dist.sh's own
# self-location convention.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLI_OS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$CLI_OS_DIR/.." && pwd)"
ANDROID_DIR="$REPO_ROOT/android"
OUT="$CLI_OS_DIR/dist-android"
CACHE_DIR="${L00PRITE_APK_CACHE:-$HOME/.cache/l00prite-apk}"
WORK="$OUT/.build"

PACKAGE="com.l00prite.os"
FWRES="/usr/share/android-framework-res/framework-res.apk"
MOZILLA_CA_DIR="/usr/share/ca-certificates/mozilla"
ANDROID_ALL_METADATA_URL="https://repo.maven.apache.org/maven2/org/robolectric/android-all/maven-metadata.xml"

# --- version -----------------------------------------------------------------------------
VERSION="${1:-$(cd "$REPO_ROOT" && git describe --tags --always --dirty 2>/dev/null || echo dev)}"
VERSION_CODE="$(date +%s)"
APK_NAME="l00prite-os-${VERSION}.apk"

echo "Building L00prite OS APK $VERSION (versionCode $VERSION_CODE)"
echo "  android sources : $ANDROID_DIR"
echo "  output          : $OUT/$APK_NAME"
echo

# --- 2. tool checks ------------------------------------------------------------------------
missing=0
require_tool() {
  local bin="$1" apt_pkg="$2"
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "error: required tool '$bin' not found (apt package: $apt_pkg)" >&2
    missing=1
  fi
}
require_tool aapt aapt
require_tool zipalign zipalign
require_tool apksigner apksigner
require_tool dalvik-exchange dalvik-exchange
require_tool javac "openjdk-21-jdk-headless (or: default-jdk)"
require_tool keytool "openjdk-21-jdk-headless (or: default-jdk)"
require_tool go "golang-go, or install from https://go.dev (need >= $(sed -n 's/^go //p' "$CLI_OS_DIR/go.mod" | head -1))"
require_tool zip zip
require_tool unzip unzip
require_tool curl curl
# --- 4. framework-res (aapt's -I resource-compile source; see the header comment on why
#        android-all's own resources.arsc does NOT substitute for this) -------------------
if [ ! -f "$FWRES" ]; then
  echo "error: android-framework-res not found at $FWRES (apt package: android-framework-res)" >&2
  missing=1
fi
if [ "$missing" -ne 0 ]; then
  echo >&2
  echo "Install everything at once with:" >&2
  echo "  sudo apt-get install aapt zipalign apksigner dalvik-exchange android-framework-res openjdk-21-jdk-headless" >&2
  exit 1
fi

# --- fresh work dir, persistent cache dir --------------------------------------------------
rm -rf "$WORK"
mkdir -p "$WORK" "$WORK/assets" "$WORK/lib/arm64-v8a" "$WORK/lib/x86_64" "$WORK/classes"
mkdir -p "$OUT" "$CACHE_DIR"

# --- 3. android-all jar (javac's Android-framework compile classpath) ----------------------
echo "== android-all jar (javac framework compile classpath) =="
CACHED_JAR="$(ls -1 "$CACHE_DIR"/android-all-*.jar 2>/dev/null | sort | tail -1 || true)"
if [ -n "${ANDROID_ALL_JAR:-}" ]; then
  echo "  using ANDROID_ALL_JAR=$ANDROID_ALL_JAR"
  [ -f "$ANDROID_ALL_JAR" ] || { echo "error: ANDROID_ALL_JAR=$ANDROID_ALL_JAR does not exist" >&2; exit 1; }
elif [ -n "$CACHED_JAR" ]; then
  ANDROID_ALL_JAR="$CACHED_JAR"
  echo "  using cached $ANDROID_ALL_JAR (delete it to force a fresh Maven Central resolve)"
else
  echo "  no ANDROID_ALL_JAR / cached PoC jar; resolving newest 15-robolectric-* from Maven Central"
  curl -sS -o "$WORK/maven-metadata.xml" "$ANDROID_ALL_METADATA_URL"
  ANDROID_ALL_VERSION="$(grep -oE '<version>15-robolectric-[0-9]+</version>' "$WORK/maven-metadata.xml" \
    | sed -e 's#</\?version>##g' | tail -1 || true)"
  [ -n "$ANDROID_ALL_VERSION" ] || { echo "error: could not find a 15-robolectric-* version in maven-metadata.xml" >&2; exit 1; }
  ANDROID_ALL_JAR="$CACHE_DIR/android-all-${ANDROID_ALL_VERSION}.jar"
  if [ ! -f "$ANDROID_ALL_JAR" ]; then
    echo "  downloading android-all $ANDROID_ALL_VERSION"
    curl -sSfL -o "$ANDROID_ALL_JAR" \
      "https://repo.maven.apache.org/maven2/org/robolectric/android-all/${ANDROID_ALL_VERSION}/android-all-${ANDROID_ALL_VERSION}.jar"
  fi
fi
file "$ANDROID_ALL_JAR" | grep -qiE 'zip|jar|java archive' \
  || { echo "error: $ANDROID_ALL_JAR is not a zip/jar file" >&2; exit 1; }
ls -la "$ANDROID_ALL_JAR"
echo

# --- 5. CA bundle: Mozilla roots ONLY, from the ca-certificates PACKAGE contents -----------
# Deliberately NOT /etc/ssl/certs/ca-certificates.crt: that system trust store may contain
# locally-injected corporate/proxy CAs (this very build environment's outbound proxy is one
# example) alongside the real Mozilla roots. The gateway ships to end-user devices, so its
# embedded trust root must be exactly upstream Mozilla, nothing environment-specific.
echo "== CA bundle (Mozilla roots from $MOZILLA_CA_DIR only) =="
[ -d "$MOZILLA_CA_DIR" ] || { echo "error: $MOZILLA_CA_DIR not found (apt package: ca-certificates)" >&2; exit 1; }
CACERT="$WORK/assets/cacert.pem"
: > "$CACERT"
cert_count=0
for f in "$MOZILLA_CA_DIR"/*.crt; do
  [ -e "$f" ] || { echo "error: no *.crt files under $MOZILLA_CA_DIR" >&2; exit 1; }
  if [ ! -s "$f" ]; then
    echo "error: zero-byte cert file $f — refusing to build a CA bundle from a corrupt package" >&2
    exit 1
  fi
  cat "$f" >> "$CACERT"
  cert_count=$((cert_count + 1))
done
begin_count="$(grep -c 'BEGIN CERTIFICATE' "$CACERT")"
if [ "$begin_count" -le 100 ]; then
  echo "error: only $begin_count certificates in cacert.pem (expected > 100) — mozilla CA package looks incomplete" >&2
  exit 1
fi
echo "  $cert_count files concatenated, $begin_count certificates, $(wc -c < "$CACERT") bytes -> $CACERT"
echo

# --- 6. Go cross-builds ---------------------------------------------------------------------
LDFLAGS="-s -w -X 'github.com/jackofall1232/l00prite/cli-os/internal/gateway.Version=$VERSION'"
unset GOOS GOARCH || true

echo "== go build android/arm64 -> lib/arm64-v8a/libl00prite.so (real devices; required) =="
( cd "$CLI_OS_DIR" && CGO_ENABLED=0 GOOS=android GOARCH=arm64 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$WORK/lib/arm64-v8a/libl00prite.so" ./cmd/l00prite )
file "$WORK/lib/arm64-v8a/libl00prite.so"
echo

# android/amd64 (emulator ABI) is best-effort: as of this Go toolchain, the linker
# refuses it outright ("android/amd64 requires external (cgo) linking, but cgo is not
# enabled") -- unlike android/arm64, there is no internal-linker support for android/amd64,
# so it needs a real NDK cross C toolchain, which this hermetic no-NDK pipeline does not
# have (see android/README.md "Known limitations" and the deviation note in this script's
# header). Rather than hard-failing the whole build over an emulator-only ABI, skip it and
# ship arm64-v8a alone -- what every physical device needs. If a future Go toolchain adds
# internal-linking support for android/amd64, this step starts succeeding and the APK
# automatically gains the second ABI with no script change required.
HAVE_X86_64=0
echo "== go build android/amd64 -> lib/x86_64/libl00prite.so (emulator ABI; best-effort) =="
AMD64_LOG="$WORK/android-amd64-build.log"
if ( cd "$CLI_OS_DIR" && CGO_ENABLED=0 GOOS=android GOARCH=amd64 \
       go build -trimpath -ldflags "$LDFLAGS" -o "$WORK/lib/x86_64/libl00prite.so" ./cmd/l00prite \
     ) >"$AMD64_LOG" 2>&1; then
  file "$WORK/lib/x86_64/libl00prite.so"
  HAVE_X86_64=1
elif grep -q "requires external (cgo) linking" "$AMD64_LOG"; then
  echo "  SKIPPED: $(cat "$AMD64_LOG")"
  echo "  (known Go/android limitation, not an NDK/toolchain misconfiguration here -- see above)"
  rm -rf "$WORK/lib/x86_64"
else
  echo "error: android/amd64 build failed for an unexpected reason:" >&2
  cat "$AMD64_LOG" >&2
  exit 1
fi
echo

echo "== source-tree health smoke build: plain linux/amd64 (runs on this host) =="
# GOOS=android GOARCH=amd64 binaries link against the Android dynamic linker and cannot
# execute on this glibc host even when the build succeeds, so they can't smoke-test
# anything by running here. A plain linux/amd64 build of the identical source tree can
# actually be executed, proving the tree that just got cross-compiled is genuinely
# healthy (not just "produces bytes").
SMOKE_BIN="$WORK/l00prite-linux-amd64-smoke"
( cd "$CLI_OS_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$SMOKE_BIN" ./cmd/l00prite )
SMOKE_HOME="$(mktemp -d)"
SMOKE_OUT="$(LOOPRITE_HOME="$SMOKE_HOME" "$SMOKE_BIN" version)"
echo "  $SMOKE_OUT"
rm -rf "$SMOKE_HOME"
echo

# --- 7. assemble: aapt package -> javac -> dalvik-exchange -> zip -> zipalign --------------
echo "== aapt package (compile res/ + AndroidManifest.xml + assets/ against framework-res.apk) =="
aapt package -f \
  -M "$ANDROID_DIR/AndroidManifest.xml" \
  -S "$ANDROID_DIR/res" \
  -A "$WORK/assets" \
  -I "$FWRES" \
  -F "$WORK/base.apk" \
  --version-code "$VERSION_CODE" \
  --version-name "$VERSION"

echo "== javac --release 8 against android-all.jar =="
JAVA_FILES=()
while IFS= read -r -d '' f; do JAVA_FILES+=("$f"); done \
  < <(find "$ANDROID_DIR/src" -name '*.java' -print0)
javac --release 8 -classpath "$ANDROID_ALL_JAR" -d "$WORK/classes" "${JAVA_FILES[@]}"

echo "== dalvik-exchange --dex (classes/ -> classes.dex) =="
dalvik-exchange --dex --min-sdk-version=26 --output="$WORK/classes.dex" "$WORK/classes/"

echo "== assemble APK: classes.dex (deflated) + lib/**/*.so (STORED) into base.apk =="
SO_ENTRIES=(lib/arm64-v8a/libl00prite.so)
if [ "$HAVE_X86_64" -eq 1 ]; then
  SO_ENTRIES+=(lib/x86_64/libl00prite.so)
fi
cp "$WORK/base.apk" "$WORK/work.apk"
( cd "$WORK" && zip -q work.apk classes.dex )
( cd "$WORK" && zip -q -0 work.apk "${SO_ENTRIES[@]}" )
unzip -lv "$WORK/work.apk"

echo "== zipalign =="
zipalign -f 4 "$WORK/work.apk" "$WORK/aligned.apk"
zipalign -c -v 4 "$WORK/aligned.apk" >/dev/null
echo

# --- keystore: env override, else a locally generated debug keystore (NEVER in the repo) --
if [ -n "${APK_KEYSTORE:-}" ]; then
  KEYSTORE="$APK_KEYSTORE"
  KS_PASS="${APK_KS_PASS:?APK_KS_PASS must be set when APK_KEYSTORE is set}"
  KS_ALIAS="${APK_KS_ALIAS:-l00prite}"
  echo "== using provided keystore $KEYSTORE =="
else
  KEYSTORE="$CACHE_DIR/debug.keystore"
  KS_ALIAS="l00pritedebug"
  KS_PASS="android"
  if [ ! -f "$KEYSTORE" ]; then
    echo "== generating debug keystore at $KEYSTORE (never inside the repo working tree) =="
    keytool -genkeypair -v \
      -keystore "$KEYSTORE" -alias "$KS_ALIAS" \
      -storepass "$KS_PASS" -keypass "$KS_PASS" \
      -keyalg RSA -keysize 2048 -validity 10000 \
      -dname "CN=L00prite Debug"
  else
    echo "== reusing cached debug keystore at $KEYSTORE =="
  fi
fi
echo

# --- 8. sign + verify ------------------------------------------------------------------------
echo "== apksigner sign =="
SIGNED_APK="$OUT/$APK_NAME"
mkdir -p "$OUT"
apksigner sign \
  --ks "$KEYSTORE" --ks-key-alias "$KS_ALIAS" \
  --ks-pass "pass:$KS_PASS" --key-pass "pass:$KS_PASS" \
  --min-sdk-version 26 \
  --out "$SIGNED_APK" "$WORK/aligned.apk"
echo

echo "== apksigner verify =="
VERIFY_OUT="$(apksigner verify --verbose "$SIGNED_APK")"
echo "$VERIFY_OUT"
echo "$VERIFY_OUT" | grep -q "Verified using v2 scheme (APK Signature Scheme v2): true" \
  || { echo "error: APK did not verify with v2 signature scheme" >&2; exit 1; }
echo

echo "== aapt dump badging =="
BADGING_OUT="$(aapt dump badging "$SIGNED_APK")"
echo "$BADGING_OUT"
echo "$BADGING_OUT" | grep -q "package: name='$PACKAGE'" \
  || { echo "error: badging package name mismatch (expected $PACKAGE)" >&2; exit 1; }
echo "$BADGING_OUT" | grep -q "launchable-activity: name='${PACKAGE}.MainActivity'" \
  || { echo "error: badging missing launchable-activity ${PACKAGE}.MainActivity" >&2; exit 1; }
echo "$BADGING_OUT" | grep -q "uses-permission: name='android.permission.INTERNET'" \
  || { echo "error: badging missing INTERNET permission" >&2; exit 1; }
echo "$BADGING_OUT" | grep -q "native-code: .*'arm64-v8a'" \
  || { echo "error: badging missing arm64-v8a native-code" >&2; exit 1; }
if [ "$HAVE_X86_64" -eq 1 ]; then
  echo "$BADGING_OUT" | grep -q "'x86_64'" \
    || { echo "error: badging missing x86_64 native-code (was built, should be present)" >&2; exit 1; }
else
  echo "  (x86_64 native-code intentionally absent -- see the android/amd64 skip note above)"
fi
echo

echo "== unzip -lv contents (classes.dex, .so entries STORED, assets/cacert.pem) =="
UNZIP_OUT="$(unzip -lv "$SIGNED_APK")"
echo "$UNZIP_OUT"
echo "$UNZIP_OUT" | grep -q "classes.dex" \
  || { echo "error: classes.dex missing from APK" >&2; exit 1; }
echo "$UNZIP_OUT" | grep "lib/arm64-v8a/libl00prite.so" | grep -q "Stored" \
  || { echo "error: lib/arm64-v8a/libl00prite.so missing or not Stored" >&2; exit 1; }
if [ "$HAVE_X86_64" -eq 1 ]; then
  echo "$UNZIP_OUT" | grep "lib/x86_64/libl00prite.so" | grep -q "Stored" \
    || { echo "error: lib/x86_64/libl00prite.so missing or not Stored" >&2; exit 1; }
fi
echo "$UNZIP_OUT" | grep -q "assets/cacert.pem" \
  || { echo "error: assets/cacert.pem missing from APK" >&2; exit 1; }
echo

echo "== native payload identity (pre-zip binaries; not extracted, same bytes that were zip -0'd) =="
file "$WORK/lib/arm64-v8a/libl00prite.so"
if [ "$HAVE_X86_64" -eq 1 ]; then
  file "$WORK/lib/x86_64/libl00prite.so"
fi
echo

echo "== checksums =="
SIZE="$(du -h "$SIGNED_APK" | cut -f1)"
SHA="$(sha256sum "$SIGNED_APK" | awk '{print $1}')"
( cd "$OUT" && sha256sum "$APK_NAME" > SHA256SUMS )
echo "  $APK_NAME  ($SIZE)"
echo "  sha256: $SHA"
echo

echo "== DONE =="
echo "  APK      : $SIGNED_APK"
echo "  SHA256SUMS: $OUT/SHA256SUMS"
echo
echo "Sideload with:"
echo "  adb install -r $SIGNED_APK"
