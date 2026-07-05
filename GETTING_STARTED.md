# Getting started

The fastest path from `git clone` to prompting a model — no prior knowledge of this codebase
needed.

This repository contains two things:

1. **The l00prite CLI-OS** (`cli-os/`) — a self-hosted gateway you run once on your machine or a
   server: one OpenAI-compatible endpoint for all your providers, keys kept encrypted server-side,
   a browser dashboard, and a Playground to prompt your models.
2. **The l00prite protocol** (everything else) — file-based project memory and loop prompts that
   let AI coding agents work across sessions and vendors. See [README.md](README.md) for that side.

If you just want a working place to prompt models and point your coding tools at, you only need
the CLI-OS. Start here.

## 1. Start the OS (about 5 minutes)

You need **Go 1.24+** (`go version` to check; install from <https://go.dev/dl>) — or Docker, see
below. Nothing else: the build produces one static binary with no other dependencies.

```bash
git clone https://github.com/jackofall1232/l00prite
cd l00prite/cli-os
./install/install.sh      # builds the static ./l00prite binary and initializes the data dir
./l00prite serve
```

Now open **<http://127.0.0.1:8787/>** in your browser. The first-run wizard walks you through
everything — no terminal, no config files:

1. **Vault** — one click generates the master key that encrypts your provider API keys at rest.
2. **Provider** — pick **Anthropic** or **OpenAI-compatible** (OpenAI, GLM, DeepSeek, Groq, …),
   name it, paste your API key. The key is validated with a real call *before* it's saved, so a
   typo fails here instead of on your first request.
3. **Network** — shows exactly how the gateway is reachable (loopback-only by default — safe).
4. **Token** — mints the gateway token your tools authenticate with. **Copy it — it's shown once.**

Done. The same URL is now your dashboard.

### Prefer Docker?

```bash
cd l00prite/cli-os
docker compose up --build     # then open http://127.0.0.1:8787/ and finish in the wizard
```

## 2. Prompt your models

In the dashboard, go to **Playground**. Pick a model — or leave **auto** to let the gateway route
each prompt to the best configured provider — type your prompt, and hit **Send** (Ctrl/⌘+Enter).
The reply comes through the exact same authenticated endpoint your coding tools will use, so a
working Playground means the whole path works; the request and its cost appear under Activity.

## 3. Add more providers

Dashboard → **Providers** → **Add provider**. Same deal as the wizard: pick the adapter, paste the
key, it's validated with a real call and stored encrypted. From the same cards you can rotate keys,
enable/disable providers, choose which models are advertised, and set the default.

## 4. Connect a repository

Dashboard → **Repositories** → **Register repo**. Give it a short id and the repo's path **on the
machine running the gateway**. Once registered, requests that name this repo get its `.l00prite/`
project memory injected automatically — including from the Playground's repo picker, so you can
prompt a model *with your project's memory* right from the browser.

## 5. Point your coding tools at it

The gateway speaks the OpenAI-compatible API, so Codex CLI, Aider, OpenCode, IDE extensions,
and any OpenAI SDK work unchanged (Claude Code is the exception — it speaks Anthropic's own
API, which this gateway doesn't serve yet):

```bash
export OPENAI_BASE_URL=http://127.0.0.1:8787/v1
export OPENAI_API_KEY=<the token you copied in the wizard>
```

Quick sanity check:

```bash
curl "$OPENAI_BASE_URL/chat/completions" \
  -H "authorization: Bearer $OPENAI_API_KEY" -H 'content-type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"hello"}]}'
```

## Going further

- **Run it as a service, TLS, private networks, troubleshooting** — [`cli-os/INSTALL.md`](cli-os/INSTALL.md)
- **Everything the gateway does** (endpoints, auto-routing, provider bridging, cost caps) — [`cli-os/README.md`](cli-os/README.md)
- **The agent-memory protocol** (blueprints, loops, Execution Mode) — [`README.md`](README.md)

Stuck? The most common first-run issues (port already in use, reaching the gateway from another
machine) are covered in [`cli-os/INSTALL.md` §9 Troubleshooting](cli-os/INSTALL.md#9-troubleshooting).
