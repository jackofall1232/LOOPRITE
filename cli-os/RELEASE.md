# l00prite CLI-OS release notes

## v1.1.0 — Go rewrite (this branch)

The runtime is now a **single statically-compiled Go binary** (was Node.js). This is a language port
of the exact v1.0.0 design — same two-track Gateway/Memory split, PEP, atomic reserve/commit,
adapter interface, and untrusted-content envelope. Highlights:

- **Single static binary.** `CGO_ENABLED=0 go build ./cmd/l00prite` → one executable `ldd` reports
  as "not a dynamic executable". SQLite is `modernc.org/sqlite` (pure Go, no cgo) — the only
  non-stdlib dependency — chosen specifically so static linking holds. See
  [`docs/node-to-go-port-notes.md`](docs/node-to-go-port-notes.md) (SQLite-driver + static-linking
  sign-off items).
- **Full test parity, shown.** `go test ./...` = 51 checks (the 50 Node behaviors + one synthetic
  price-tier test); `go vet` and `gofmt` clean. Vault ciphertext stays **byte-compatible** with the
  Node format (existing vaults decrypt unchanged); token compares stay constant-time
  (`crypto/subtle`); repo-read containment stays symlink-resolving fail-closed.
- **Pricing confirmation pass done.** Anthropic pricing confirmed first-party
  (`platform.claude.com`, 2026-07-04). OpenAI and Zhipu/GLM pages were egress-blocked (403), so their
  prices are `null` and the meter refuses to bill them a silent `$0` — it flags rows
  `cost_unconfirmed` and sets `x-l00prite-cost-unconfirmed`. See
  [`docs/pricing-confirmation.md`](docs/pricing-confirmation.md).

Everything below describes the shipped v1 feature set (unchanged by the port).

---

# l00prite CLI-OS v1.0.0 — release notes

A runnable, self-hostable v1 of the CLI-OS control plane. This document states plainly what is
proven and what still needs a networked validation pass, so "ready to ship" is an honest claim.

## What ships in v1.0.0

- **OpenAI-compatible gateway** — `POST /v1/chat/completions` (streaming + non-streaming),
  `GET /v1/models`, `GET /healthz`, and a served dashboard at `/`.
- **Provider adapters** — a real native Anthropic `/v1/messages` translator (system-as-field,
  `input_schema` tools, tool_use/tool_result, typed SSE events rebuilt into OpenAI chunk
  deltas) and an OpenAI-compatible passthrough covering OpenAI, GLM/Zhipu (`glm-5.2`), DeepSeek,
  Gemini's compat layer, Groq, Mistral, OpenRouter, and local Ollama/vLLM. A zero-key `mock`
  upstream lets a fresh install demo the whole path with no keys or network.
- **Explainable routing** — first-match rules (explicit pin → alias → model-owner → default →
  fallback) with a circuit breaker; every decision is logged and inspectable via
  `l00prite route explain`.
- **Auto-routing (multi-provider)** — opt-in `auto` / `auto:cheap|quality|balanced`: a capability
  filter (tools/vision/context) plus a deterministic cost/quality/balanced scorer picks the best or
  most-efficient provider per task; unpriced/unconfirmed models never win the "cheapest" slot. Fully
  logged; dry-runnable via `l00prite route plan` / `x-l00prite-dry-run`. See
  [`docs/routing-auto-mode.md`](docs/routing-auto-mode.md).
- **Provider bridging** — off by default; with `x-l00prite-bridge: on` the primary model gets an
  `l00prite_bridge` tool to delegate a sub-task to another provider ("Codex asks Claude"). Bounded
  by a hop cap, metered per hop through the PEP, delegate output wrapped untrusted, streaming clients
  see only the final answer. See [`docs/provider-bridging.md`](docs/provider-bridging.md).
- **Real cost tracking** — usage is taken from provider responses (cache tokens included),
  priced from per-model manifests with separate input/output/cache rates; unknown/unconfirmed
  prices are recorded as `estimated` rather than fabricated.
- **Policy Enforcement Point** — daily $ caps enforced by atomic reserve→commit/refund over a
  SQLite (WAL) store, separate from the request handler; concurrency leases; retry cap.
- **Repo memory** — `.l00prite/` files ranked and injected within a token budget, with
  mtime-based staleness, graceful degradation, and an untrusted-content envelope
  (prompt-injection guard).
- **Security** — provider keys AES-256-GCM encrypted at rest under a server-only master key;
  opaque, hashed, revocable gateway tokens; least-privilege repo reads (containment check);
  safe-by-default startup (refuses non-loopback bind without TLS unless explicitly opted in;
  refuses to start without a master key).
- **Ops** — admin CLI, run ledger (SQLite + JSONL), audit log, one-command install, Dockerfile
  + compose.
- **Tests** — `npm test`: 40 checks covering the vault, tokens, PEP cap enforcement + stale-
  reservation reaping, the cost meter, Anthropic request + SSE translation, memory, auto-routing
  (capability filter, cost/quality ordering, unknown-price-last, typed errors), provider bridging
  (delegation loop, hop cap, mid-bridge cap denial, untrusted-envelope breakout, stream no-leak,
  PEP invariants), and a full end-to-end server run against the mock upstream. All pass.

## Decisions made (recorded from the open questions)

- **Runtime: Node.js, zero external dependencies** (was recommended as Go). Node runs and is
  fully testable in the build environment and matches the existing Node validator; `node:sqlite`
  gives real ACID with no dependency. `bin/cli.js` runs offline; `l00prite serve` is one command.
- **Providers in v1:** the adapter framework + Anthropic (native) + OpenAI-compatible (covers
  GLM 5.2 and the rest) + mock. Any OpenAI-compatible provider is a config add.
- **Routing "quality":** operator-assigned static rank in config; no ML.
- **`/v1/responses`:** deferred to v2 (chat/completions first).
- **Memory retrieval:** naive rank-and-select v1 (no embeddings); the interface makes it
  swappable without touching the gateway.

## Honest ship caveats (validate before trusting with real money at scale)

- **Live-provider round-trips were not executed here.** The build environment blocks egress to
  provider domains (openai.com, z.ai, etc. return 403). Adapter *translation* is unit-tested and
  the full pipeline is e2e-tested against the mock upstream, but a smoke test against real
  Anthropic/OpenAI/GLM keys must be run in a networked environment before production traffic.
- **Pricing:** Anthropic prices are first-party-confirmed; other providers' price maps ship with
  `null`/unconfirmed values (their first-party pricing pages were egress-blocked), so their cost
  is recorded as `estimated` until a confirmation pass fills the manifests.
- **Single-node.** SQLite WAL gives atomicity and cross-process safety on one host; multi-node/HA
  is v2. The PEP is a separate module (not a separate process) in v1.
- **Dashboard** (`public/dashboard.html`) is a static control-plane view with representative
  sample state; wiring it to live `/healthz`/ledger data is a small follow-up.
- **Deferred to v2:** `/v1/responses`, embedding retrieval, cross-repo memory scoping,
  multi-tenant RBAC, and prompt-cache-aware routing (all designed in `docs/`).

## Upgrade / run

`./install/install.sh` then `l00prite serve`, or `docker compose up --build`. See
[`README.md`](README.md).
