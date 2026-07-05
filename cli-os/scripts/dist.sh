#!/usr/bin/env bash
# Build the l00prite CLI-OS cross-platform release matrix.
#
# Produces one fully-static binary per platform (CGO disabled -> pure-Go SQLite, embedded
# dashboard, no runtime deps), archives each with INSTALL.md + README.md, writes SHA256SUMS,
# and stamps the version into the binary via -ldflags.
#
#   scripts/dist.sh [VERSION]
#
# VERSION defaults to `git describe --tags --always --dirty`, or "dev" outside a git tree.
# Output lands in cli-os/dist/ (git-ignored). Releases are produced by running this script;
# there is no hosted download service.
set -euo pipefail

# Resolve to the cli-os/ root (this script lives in cli-os/scripts/), like install.sh does.
cd "$(dirname "$0")/.."
ROOT="$(pwd)"
DIST="$ROOT/dist"

# --- version -------------------------------------------------------------------------------
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

# --- ldflags: strip + stamp Version (single source of truth for healthz + dashboard) -------
# The whole -X value (importpath.name=value) is wrapped in one pair of single quotes so go's
# ldflags splitter keeps it as a single token.
LDFLAGS="-s -w -X 'github.com/jackofall1232/l00prite/cli-os/internal/gateway.Version=$VERSION'"

# --- release matrix ------------------------------------------------------------------------
TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
)

# --- checksum tool -------------------------------------------------------------------------
if command -v sha256sum >/dev/null 2>&1; then
  SHA256() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
  SHA256() { shasum -a 256 "$@"; }
else
  echo "error: neither sha256sum nor shasum found; cannot write SHA256SUMS" >&2
  exit 1
fi

HAVE_ZIP=0
if command -v zip >/dev/null 2>&1; then
  HAVE_ZIP=1
fi

# Never let a stale GOOS/GOARCH from the caller's environment leak into the build.
unset GOOS GOARCH || true

# --- fresh output dir ----------------------------------------------------------------------
rm -rf "$DIST"
mkdir -p "$DIST"
STAGE="$DIST/.stage"
mkdir -p "$STAGE"

echo "Building l00prite CLI-OS $VERSION  (CGO_ENABLED=0, static)"
echo

ARTIFACTS=()   # basenames of everything shippable (archives + any raw fallback binary)

for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"

  bin="l00prite"
  [ "$os" = "windows" ] && bin="l00prite.exe"

  name="l00prite_${VERSION}_${os}_${arch}"
  stagedir="$STAGE/$name"
  mkdir -p "$stagedir"

  echo "  -> $os/$arch"
  # Inline env keeps GOOS/GOARCH scoped to this one build; they never persist between iterations.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$stagedir/$bin" ./cmd/l00prite

  # Bundle the install + usage docs alongside the binary.
  cp "$ROOT/INSTALL.md" "$ROOT/README.md" "$stagedir/"

  if [ "$os" = "windows" ]; then
    if [ "$HAVE_ZIP" -eq 1 ]; then
      ( cd "$STAGE" && zip -q -r "$DIST/${name}.zip" "$name" )
      ARTIFACTS+=("${name}.zip")
    else
      echo "     note: 'zip' not found — shipping the raw .exe plus a .tar.gz fallback instead of a .zip."
      cp "$stagedir/$bin" "$DIST/${name}.exe"
      ARTIFACTS+=("${name}.exe")
      tar -czf "$DIST/${name}.tar.gz" -C "$STAGE" "$name"
      ARTIFACTS+=("${name}.tar.gz")
    fi
  else
    tar -czf "$DIST/${name}.tar.gz" -C "$STAGE" "$name"
    ARTIFACTS+=("${name}.tar.gz")
  fi
done

# Staging tree was only scaffolding for the archives.
rm -rf "$STAGE"

# --- checksums -----------------------------------------------------------------------------
( cd "$DIST" && SHA256 "${ARTIFACTS[@]}" > SHA256SUMS )

# --- summary ------------------------------------------------------------------------------
echo
echo "Built artifacts in $DIST:"
printf '  %-48s %10s\n' "ARTIFACT" "SIZE"
printf '  %-48s %10s\n' "------------------------------------------------" "----------"
for a in "${ARTIFACTS[@]}"; do
  size="$(du -h "$DIST/$a" | cut -f1)"
  printf '  %-48s %10s\n' "$a" "$size"
done
printf '  %-48s %10s\n' "SHA256SUMS" "$(du -h "$DIST/SHA256SUMS" | cut -f1)"
echo
echo "Wrote $DIST/SHA256SUMS ( ${#ARTIFACTS[@]} archive(s) )."
