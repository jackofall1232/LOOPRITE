# Installing l00prite CLI-OS

A complete, end-to-end setup guide for a self-hosted l00prite CLI-OS gateway — the real,
current path, verified against what's actually built. Follow it top to bottom with no prior
knowledge of the codebase.

> The product is a single statically-compiled Go binary: an OpenAI-compatible gateway that keeps your
> provider API keys server-side in an encrypted vault, routes across providers, tracks cost, and serves a
> browser dashboard + first-run setup wizard. There is no hosted service and no runtime dependency to
> install beyond the binary itself.

---

## 1. Prerequisites

- **Go 1.24 or newer** — the module targets `go 1.24` (see [`go.mod`](go.mod)). Check with `go version`.
- **git** — to clone the repository.
- **No cgo / no C toolchain** — the only non-stdlib dependency is a *pure-Go* SQLite driver
  (`modernc.org/sqlite`), so `CGO_ENABLED=0` produces a fully static binary with no OS-level libraries to
  install.
- **A web browser** — only for the first-run setup wizard. Everything the wizard does is also available
  from the CLI, so a browser is optional if you prefer the terminal.
- **`openssl`** (optional) — only if you want to generate your own vault master key instead of letting the
  server generate one.

Runs on Linux and macOS. The systemd service in §5 is Linux-specific.

---

## 2. Clone and build

```bash
git clone https://github.com/jackofall1232/l00prite
cd l00prite/cli-os
CGO_ENABLED=0 go build -o cli-os ./cmd/l00prite
```

This produces a single static executable named `cli-os` in the current directory. You can verify it is
truly static (no dynamic linking) with:

```bash
ldd ./cli-os
# prints: "not a dynamic executable"
```

> **Naming note:** the built-in `--help` and its examples refer to the binary as `l00prite` (that's the
> command name once it's on your `PATH`). This guide uses `./cli-os` to match the build command above; if
> you copy the binary to `/usr/local/bin/l00prite` you can drop the `./` and use `l00prite ...` exactly as
> the help text shows.

There is also a convenience installer, `./install/install.sh`, which builds the binary and runs `init` for
you — use it if you'd rather not run the two steps by hand.

Confirm the build works:

```bash
./cli-os --help
```

---

## 3. First run — initialize the data directory

```bash
./cli-os init
```

This creates the data directory (mode `0700`) and, inside it:

- `master.key` — the vault master key, `chmod 600`, used to encrypt every provider API key at rest
  (AES-256-GCM). Never commit or share this file.
- `cli-os.db` (plus `cli-os.db-wal` / `cli-os.db-shm`) — the SQLite database (WAL mode) holding providers,
  tokens, repos, caps, spend, the run ledger, and the audit log.

**Where.** By default the data directory is `~/.l00prite-cli-os`. Override it by setting `LOOPRITE_HOME`
(recommended for a service — e.g. `/var/lib/l00prite-cli-os`). Every command reads the same `LOOPRITE_HOME`,
so keep it consistent between `init`, `serve`, and any CLI calls.

`init` prints your next steps:

```
Initialized l00prite CLI-OS at ~/.l00prite-cli-os
Next (easiest): start the server and finish setup in your browser —
  l00prite serve        # then open http://127.0.0.1:8787/
Or do the same from the CLI:
  l00prite provider add anthropic --key sk-ant-... --default
  l00prite token mint --project default
  l00prite serve
```

> `init` is optional: if you run `./cli-os serve` with no master key present, the server boots straight
> into browser **setup mode** (§6) and the wizard creates the vault for you. Running `init` first just
> makes the vault/DB explicit and lets you use the CLI before opening a browser.

---

## 4. Network binding (loopback, TLS, or private-network)

By default the server binds **`127.0.0.1:8787`** — loopback only, reachable just from the same machine.
That's the safe default and needs no extra configuration.

l00prite **refuses to bind a non-loopback address without TLS**. If you set `LOOPRITE_HOST` to anything
other than a loopback address and don't provide TLS, startup aborts:

```
Refusing to start — fix these first:
  • Refusing to bind non-loopback host "0.0.0.0" without TLS. Either set LOOPRITE_TLS_CERT +
    LOOPRITE_TLS_KEY, bind to 127.0.0.1, or (only behind a trusted reverse proxy / private network)
    set LOOPRITE_ALLOW_INSECURE_BIND=1.
```

You have two real ways to expose it beyond loopback:

**Option A — real TLS (public-facing exposure).** Point the server at a certificate + key pair; it then
serves HTTPS on the bound host:

```bash
export LOOPRITE_HOST=0.0.0.0
export LOOPRITE_PORT=8787
export LOOPRITE_TLS_CERT=/etc/l00prite/tls/cert.pem
export LOOPRITE_TLS_KEY=/etc/l00prite/tls/key.pem
./cli-os serve
```

**Option B — plaintext on a trusted private network.** `LOOPRITE_ALLOW_INSECURE_BIND=1` lets it bind a
non-loopback host *without* TLS:

```bash
export LOOPRITE_HOST=0.0.0.0
export LOOPRITE_ALLOW_INSECURE_BIND=1
./cli-os serve
```

### When the insecure-bind flag is actually appropriate

`LOOPRITE_ALLOW_INSECURE_BIND=1` is a deliberate override, not a default to reach for. It is appropriate
**only when the network path is already private and encrypted by something else**, for example:

- The host is only reachable over a **Tailscale tailnet / WireGuard VPN** (traffic is already encrypted and
  access-controlled by the tunnel).
- A **TLS-terminating reverse proxy** (nginx, Caddy, a load balancer) sits in front and l00prite only
  listens on the private interface behind it.

It is **not** appropriate for **public-facing exposure**. Gateway tokens are sent as `Bearer` credentials
and requests carry your prompts; without TLS both travel in cleartext, so anyone on the network path can
capture a token and use it to spend against your provider keys. For anything reachable from the open
internet, use real TLS (Option A) or keep the loopback bind and reach it over an SSH tunnel
(`ssh -L 8787:127.0.0.1:8787 user@server`).

The full set of environment variables is documented in [`.env.example`](.env.example).

---

## 5. Running as a persistent service (Linux / systemd)

To survive reboots and crashes, run the gateway under an init system. Below is a **complete** systemd unit.
This is **Linux/systemd-specific**; on macOS use a `launchd` plist, and on other systems use the equivalent
supervisor — the requirement is the same: run `cli-os serve` with `LOOPRITE_HOME` set, as a dedicated
user, restarting on failure.

Install the binary and create a dedicated user first:

```bash
sudo cp ./cli-os /usr/local/bin/cli-os
sudo useradd --system --home /var/lib/l00prite-cli-os --shell /usr/sbin/nologin l00prite || true
```

`/etc/systemd/system/l00prite.service`:

```ini
[Unit]
Description=l00prite CLI-OS gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=l00prite
Group=l00prite
Environment=LOOPRITE_HOME=/var/lib/l00prite-cli-os
Environment=LOOPRITE_HOST=127.0.0.1
Environment=LOOPRITE_PORT=8787
# To expose on a private network instead, replace the HOST line and add the relevant
# vars per §4 (LOOPRITE_TLS_CERT/LOOPRITE_TLS_KEY, or LOOPRITE_ALLOW_INSECURE_BIND=1).
ExecStart=/usr/local/bin/cli-os serve
Restart=always
RestartSec=3

# Hardening: the service only needs its own state directory.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
StateDirectory=l00prite-cli-os
ReadWritePaths=/var/lib/l00prite-cli-os

[Install]
WantedBy=multi-user.target
```

`StateDirectory=l00prite-cli-os` creates and owns `/var/lib/l00prite-cli-os` for the `l00prite` user, which
matches `LOOPRITE_HOME`. Enable and start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now l00prite.service
systemctl status l00prite.service
journalctl -u l00prite.service -f      # follow logs
```

On first start with an empty state directory, the server boots into setup mode; open the dashboard (§6) to
initialize the vault, add a provider, and mint your first token. (You can instead pre-initialize as the
service user: `sudo -u l00prite LOOPRITE_HOME=/var/lib/l00prite-cli-os /usr/local/bin/cli-os init`.)

---

## 6. First-run setup wizard (in the browser)

With the server running, open the dashboard URL — by default **`http://127.0.0.1:8787/`**. While the system
is unconfigured, `/` serves the **setup wizard**; once setup completes it permanently becomes the real-data
dashboard. The server prints the URLs on start:

```
l00prite CLI-OS listening on http://127.0.0.1:8787
  • OpenAI endpoint : http://127.0.0.1:8787/v1/chat/completions
  • Dashboard       : http://127.0.0.1:8787/
  • First-run setup : open http://127.0.0.1:8787/ in a browser to configure (no terminal needed)
```

The wizard walks through these steps (this is the **actual current flow**):

1. **Welcome** — overview.
2. **Vault** — generate a master key (recommended) or paste your own base64-encoded 32-byte key.
3. **Provider** — pick an adapter (Anthropic native, or OpenAI-compatible for OpenAI/GLM/DeepSeek/Groq
   and friends), give it a name and API key. The key is **validated with a real upstream call before it
   is saved** — a bad key fails here, not on your first request.
4. **Network** — shows your real bind host / port / TLS / exposure so you know exactly how it's reachable.
5. **Token** — mints your first gateway token. It is **shown once** — copy it on this screen.
6. **Done** — your working base URL, the token, and copy-paste `curl` / tool-config snippets.

The wizard writes the same state the CLI does, so you can mix the browser and the terminal freely
afterward.

> **Accuracy note — what the wizard does NOT include.** The current wizard has no *model-selection* step and
> no *repository-registration* step (an earlier plan mentioned these; the shipped wizard is Vault → Provider
> → Network → Token). Instead, both live in the dashboard **after** setup:
>
> - **Model selection** and **ongoing provider management**: the dashboard's Providers section lets you
>   **add, rotate, remove, enable/disable, set as default, and choose which models are enabled** for a
>   provider — no CLI needed post-setup.
> - **Repository registration**: the dashboard's Repositories section has a **Register repo** modal (and the
>   CLI equivalent below). Registration takes a **filesystem path on the machine running the gateway** —
>   there is no git-URL support today. The dashboard endpoint verifies the directory exists (and registers
>   the repo under your token's project) before storing anything; the CLI stores the path as given:
>   ```bash
>   ./cli-os repo register myrepo --root /path/to/repo
>   ```
>   This registers a repo so the gateway can inject its `.l00prite` memory into requests scoped to it.
> - **Prompting**: the dashboard's **Playground** panel sends real requests through the gateway — pick a
>   model (or `auto`), optionally a registered repo for memory injection, and chat from the browser.

Everything the wizard does is available from the CLI as well:

```bash
./cli-os provider add anthropic --key sk-ant-... --default
./cli-os token mint --project default
./cli-os serve
```

---

## 7. Connecting a coding tool

The gateway speaks the **OpenAI-compatible** API, so any tool that accepts an OpenAI base URL + key works
unchanged — Codex CLI, Aider, OpenCode, IDE extensions, or any OpenAI SDK. (Claude Code is the
exception: it speaks Anthropic's own API, which this gateway does not serve yet.) Point it at the
gateway's `/v1` base and use your **minted gateway token** as the API key (a token looks like
`l00p_<id>_<secret>`, e.g. `l00p_df10555433457fd425_mKbIK_vjcLiJpcTzDOPmwhIPlWN_OpHM`):

```bash
export OPENAI_BASE_URL=http://127.0.0.1:8787/v1
export OPENAI_API_KEY=l00p_df10555433457fd425_mKbIK_vjcLiJpcTzDOPmwhIPlWN_OpHM
```

Verify with a real request to `/v1/chat/completions` (use `"model":"auto"` to let the gateway route, or a
concrete provider model id):

```bash
curl "$OPENAI_BASE_URL/chat/completions" \
  -H "authorization: Bearer $OPENAI_API_KEY" \
  -H "content-type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hello"}]}'
```

A quick unauthenticated connectivity check (no token needed) is `GET /healthz`:

```bash
curl http://127.0.0.1:8787/healthz
```

---

## 8. Known limitations

Before relying on this in a multi-user setting, read
[`docs/known-limitations.md`](docs/known-limitations.md) — most importantly, the current **single-tier
authentication** model: any valid token can manage providers, so treat every token as an admin credential
until scoped tokens land.

---

## 9. Troubleshooting

**`listen tcp ... : bind: address already in use`.** A stale `cli-os` process (or something else) is already
holding the port. Find and stop it, or pick another port:

```bash
ss -ltnp | grep ':8787'        # or: lsof -i :8787
kill <pid>                     # stop the stale process
# systemd deployments: sudo systemctl stop l00prite.service
./cli-os serve --port 8788     # or just start on a different port
```

**Connection times out from another device.** If you exposed the gateway on a private network (Tailscale /
WireGuard), the other device must be **joined to the same tailnet / VPN** — a device outside it cannot reach
the server at all. Check the path and the bind:

```bash
# from the OTHER device — use the server's private (e.g. Tailscale) address, not 127.0.0.1:
curl http://<server-private-ip>:8787/healthz
```

If that hangs: confirm the two devices are on the same tailnet (`tailscale status`), confirm the server is
bound to a reachable address (not `127.0.0.1`) **and** that you set TLS or `LOOPRITE_ALLOW_INSECURE_BIND=1`
(otherwise it refused to start — see §4), and check for a host firewall blocking the port. Loopback-only
deployments are reachable from another machine only via an SSH tunnel
(`ssh -L 8787:127.0.0.1:8787 user@server`, then use `http://127.0.0.1:8787` locally).
