#!/usr/bin/env bash
# One-command install for a local (non-Docker) run. Builds the single static Go binary, initializes
# the data dir, and prints the next steps. On Windows use install/install.ps1 (same flow, installs
# to %LOCALAPPDATA%\l00prite\bin). Prebuilt static binaries for Linux/macOS/Windows are produced
# by scripts/dist.sh — releases come from it; there is no hosted download service.
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  echo "l00prite CLI-OS is built from Go source and needs the Go toolchain (>= 1.24)." >&2
  echo "Install Go from https://go.dev/dl and re-run, or use the Docker image (docker compose up)," >&2
  echo "or use a prebuilt binary produced by scripts/dist.sh (Linux/macOS/Windows)." >&2
  exit 1
fi

echo "Building the static l00prite binary (CGO disabled -> single static executable)…"
CGO_ENABLED=0 go build -ldflags='-s -w' -o ./l00prite ./cmd/l00prite

./l00prite init

cat <<'EOF'

Installed ./l00prite. One step left:

  ./l00prite serve

then open  http://127.0.0.1:8787/  in your browser. The first-run wizard walks you
through adding a provider (your API key is validated with a real call, then stored
encrypted) and minting the gateway token your coding tools will use. After setup the
same URL is the dashboard: add providers, register repos, and prompt your models
from the Playground — no terminal needed.

Prefer to stay in the terminal? The CLI does everything the wizard does:

  ./l00prite provider add anthropic --key sk-ant-... --default
  ./l00prite provider add openai    --key sk-...     --adapter openai-compat
  ./l00prite repo register myrepo --root /path/to/repo   # optional: inject .l00prite memory
  ./l00prite token mint --project default
  ./l00prite serve

Point any OpenAI-compatible tool (Codex, Aider, an OpenAI SDK) at it:
  OPENAI_BASE_URL=http://127.0.0.1:8787/v1
  OPENAI_API_KEY=<the l00prite token>

Optionally put the binary on your PATH:
  sudo install -m 0755 ./l00prite /usr/local/bin/l00prite
EOF
