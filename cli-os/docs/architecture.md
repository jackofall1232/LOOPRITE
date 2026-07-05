# l00prite CLI-OS — Architecture (v1 design)

> Branch: `claude/looprite-cli-os-jntwqi` (the brief's conceptual name is `CLI-OS`).
> Status: **v1.0.0 implemented and tested.** This document is the design; the runnable Node
> gateway lives under `cli-os/` (see [`../RELEASE.md`](../RELEASE.md) for shipped state and
> caveats). Not merged to `main`. Companion docs: [`interface-contract.md`](interface-contract.md),
> [`provider-adapters.md`](provider-adapters.md), [`routing-rules-v1.md`](routing-rules-v1.md),
> [`security-model.md`](security-model.md), [`v1-scope.md`](v1-scope.md),
> [`open-questions.md`](open-questions.md).

## 0. Starting point (what actually exists today)

l00prite today is a **prompt-file protocol**: Markdown loop prompts, JSON schemas, and one
dependency-free Node validator (`scripts/validate-l00prite.js`). There is **no server, no
runtime, no backend, and no "Claude Platform agent implementation"** — the brief's reference
to extending existing agent code does not map to anything in the repo. CLI-OS is therefore
**greenfield runtime code that lives alongside the protocol**, in a separate `cli-os/`
subtree, so the existing protocol files and the passing validator are untouched.

CLI-OS turns l00prite into a self-hostable **control plane for AI coding**: it sits between
coding tools (Claude Code, Codex CLI, Aider, OpenCode, IDEs) and multiple LLM providers,
exposes an OpenAI-compatible surface so existing tools work unchanged, injects repo-aware
persistent memory, tracks real cost per project, records run ledgers, and enforces safety
limits on retries, spend, destructive actions, stale context, and concurrent sessions.

**It is not a proxy.** The OpenAI-compatible endpoint is the *compatibility layer*, not the
product. The product is: provider abstraction + repo memory + routing + cost tracking +
safety policy + run ledgers + installable server + CLI/admin surface (+ future dashboard).

## 1. Non-negotiable inheritances from l00prite

Carried over because the brief demands the existing safety posture and the repo already
encodes these as hard rules (`.l00prite/constraints.md`, `failures.md`, `memory.md`):

1. **Scaffold-first / safe-by-default.** Any component that can take a costly or destructive
   action has explicit stop conditions and defaults to the safe branch. No "auto-everything."
2. **Persisted flags are never authorization.** l00prite's do-not-retry ledger records that an
   agent-writable `preflight_confirmed`/`enabled` flag is *forgeable* and must never gate a
   privileged action. CLI-OS generalizes this to the brief's rule: *safety-critical stop
   conditions (cost cap, retry cap, destructive-action gate) need an enforcement point outside
   the process making the decision.* → the **Policy Enforcement Point (PEP)**, §6.
3. **Untrusted-by-default context.** Repo memory reconstructed from disk (PR comments, issue
   text, prior conversation logs, CI output) is untrusted from the model's perspective, even
   though it is "yours." It is data to delimit, never instructions to follow.
4. **Cooperative locking is not enough.** l00prite's `lock.json`/`LOCKING.md` convention races
   under concurrency. CLI-OS provides *atomic* state transitions for anything money- or
   memory-affecting (§6.2).

## 2. Two-track architecture (a hard module boundary)

```
                         ┌────────────────────────────────────────────────┐
   coding tool           │                  l00prite CLI-OS                │
  (Claude Code,   HTTP   │   ┌───────────── Track 1: GATEWAY ──────────┐   │
   Codex, Aider,  OpenAI │   │  Ingress (OpenAI-compat HTTP)           │   │
   IDE, curl) ──────────▶│   │  Auth (opaque l00prite token)           │   │
        │        schema  │   │  Router (explainable rules v1)          │   │
        └───────────────▶│   │  Provider Adapters (per-provider)       │◄──┼─▶ providers
                         │   │  Retry/backoff (idempotency-aware)      │   │  Anthropic(full),
                         │   │  Usage/Cost meter (real tokens)         │   │  OpenAI/GLM/…(shim)
                         │   └────────────────▲────────────────────────┘   │
                         │        MemoryQuery │ MemoryContext (§4)          │
                         │   ┌────────────────▼──────── Track 2: MEMORY ─┐  │
                         │   │  Retrieval + ranking (WHICH context)      │  │
                         │   │  Staleness / invalidation                 │  │
                         │   │  Graceful degradation (fall back to raw)  │  │
                         │   │  Per-repo store (.l00prite/ + index)      │  │
                         │   └───────────────────────────────────────────┘  │
                         │   ┌── Cross-cutting: POLICY / LEDGER / STATE ──┐  │
                         │   │  Policy Enforcement Point (PEP)            │  │
                         │   │  Run ledger + usage DB                     │  │
                         │   │  Atomic state store / leases              │  │
                         │   └────────────────────────────────────────────┘  │
                         └───────────────── CLI / admin surface ────────────┘
```

**Track 1 (Gateway)** and **Track 2 (Memory)** are separate modules that communicate *only*
through the typed interface in §4 and [`interface-contract.md`](interface-contract.md).
Neither imports the other's internals. This is the actual design work: the interface is what
keeps Memory from becoming a per-request bottleneck and keeps the Gateway from hard-coding one
memory strategy (so v2 can swap naive retrieval → embeddings with zero Gateway changes).

### 2.1 Track 1 (Gateway) responsibilities
- **Ingress** — OpenAI-compatible `/v1/chat/completions` (streaming + non-streaming);
  `/v1/models`; optional `/v1/responses` (scope decision Q4). Strict request validation.
- **Auth** — clients present one opaque `l00prite` bearer token. Provider keys never leave the
  server. One token → one *principal* (project scope + policy).
- **Router** — chooses a `(provider, model)` from an **explainable rule set** (not ML); every
  decision logged and inspectable per request (§5, [`routing-rules-v1.md`](routing-rules-v1.md)).
- **Provider adapters** — one per provider; translate request shape, tool-calling schema,
  streaming format, and usage accounting. Each provider gets its own adapter with a
  conformance suite ([`provider-adapters.md`](provider-adapters.md)).
- **Retry** — bounded, backed-off, **idempotency-aware**: never retry a request that already
  produced a client-visible side effect (first token flushed, tool call surfaced). Hard cap.
- **Cost meter** — pulls *actual* token usage from provider responses (prompt-cache tokens
  included); falls back to a counted estimate only when a provider omits usage, and marks the
  row `estimated`.

### 2.2 Track 2 (Memory) responsibilities
- **Retrieval/ranking** — given a request + repo + token budget, select *the right* context,
  not "everything we have." Ranking is explicit and inspectable (pinned architecture/
  constraints, files referenced in the request, recent ledger/summaries).
- **Staleness** — file mtime/hash change, TTL, or explicit user/CLI invalidation. Silently
  serving stale architecture is worse than spending tokens.
- **Graceful degradation** — on failure or low confidence, send *more raw* context (or none +
  a flag), never confidently-wrong context. Never fabricate.
- **Store** — per-repo, file-based, built on the existing `.l00prite/` layout plus a
  rebuildable derived index. The protocol's memory files are the source of truth.

## 3. Request lifecycle (happy path)

1. Coding tool → `POST /v1/chat/completions`, `Authorization: Bearer <l00prite-token>`.
2. **Auth** resolves token → principal (project + policy). Reject unknown/expired (401).
3. **PEP pre-check** (§6): within cost cap? within concurrency lease budget? circuit not
   tripped? If not → typed OpenAI-shaped error *before any spend*.
4. **Router** picks `(provider, model)` from rules + request hints; emits a logged
   `RoutingDecision`.
5. **Memory** is asked for a `MemoryContext` (§4) under a **hard latency budget**; on timeout
   the Gateway proceeds `degraded: no-memory` instead of blocking.
6. Gateway assembles the provider-native request via the chosen **adapter**, injecting memory
   as **delimited, untrusted** context (prompt-injection guard) and translating tools/messages.
7. Adapter calls the provider (stream or not), translating the response/stream back to OpenAI
   shape on the way out.
8. **Cost meter** records real usage into the usage DB under a PEP-managed atomic transaction;
   PEP reconciles the reservation (commit/refund) against the cap.
9. **Ledger** appends a run row (request id, routing decision, provider, model, token
   breakdown, dollar cost, memory-degradation flag, outcome).
10. Response streamed/returned to the client.

## 4. Gateway ↔ Memory interface contract (the core design)

Full schema and invariants in [`interface-contract.md`](interface-contract.md). In brief:

- Gateway → Memory: **`MemoryQuery`** `{repo_id, principal, request_digest, budgets{latency_ms,
  context_tokens}, options}`. `request_digest` is a *minimal safe projection* (user intent,
  referenced paths, recent tool names) — not the raw prompt.
- Memory → Gateway: **`MemoryContext`** `{status(ok|degraded|empty|error), reason, blocks[
  {kind, text, source_path, freshness, rank_score}], tokens, trace_id}`.

Invariants: latency is a hard ceiling (Memory yields `empty` on timeout, Gateway never
blocks); Memory returns *blocks*, never a finished prompt (**the Gateway owns injection** and
untrusted-delimiting); degradation is explicit and lands on the ledger row; no hidden coupling
(swap the Memory implementation with zero Gateway changes).

## 5. Routing — explainable rule set v1 (no ML)

First-match-wins, fully logged (`l00prite route explain <request-id>`). Prior art (OpenRouter,
LiteLLM, Portkey) drives two refinements baked in from day one:

1. **Explicit pin** — client `model` maps to a concrete `provider:model`, or an
   `x-l00prite-route` header is set → use it.
2. **Alias map** — named alias (`fast`/`cheap`/`smart`/`local`) resolves via config (data, not
   code) to a concrete target.
3. **Capability filter** — drop targets that can't satisfy the request (tools / context size /
   vision), per the provider capability manifest.
4. **Preference tiebreak** — among survivors, order by the project's declared preference
   (`cost` | `latency` | `quality`), then by circuit-breaker health.
5. **Fallback chain** — separates **provider-failover** (same model, different serving
   provider) from **model-fallback** (different model), advances only on *retryable,
   side-effect-free, non-4xx* failures (never fail over a `400` — it fails everywhere), with a
   circuit-breaker cooldown so a flapping backend stops poisoning requests.

"Quality" in v1 = an **operator-assigned static rank per model in config** (explainable, no
inference). Learned/ML routing is v2+ (Open Question Q2).

## 6. Safety, concurrency, enforcement

The brief's three day-one risks map to three mechanisms.

### 6.1 Enforcement outside the deciding process — the PEP
Cost caps, retry caps, and the destructive-action gate are enforced by a **Policy Enforcement
Point** that owns the atomic state store, separate from the per-request handler that would
benefit from ignoring them:
- Counters live in a transactional store (SQLite WAL v1, pluggable to Postgres). A handler
  cannot mutate its own budget; it *requests a spend reservation* from the PEP, which commits
  atomically or denies. Budgets are enforced **in dollars**, scoped by project/model/window
  (prior-art lesson) — not in tokens.
- The gate is checked **before** dispatch (reserve) and reconciled **after** with real usage
  (commit/refund). A crashed handler cannot leak budget past the cap — the reservation is the
  ceiling.
- This is the CLI-OS realization of "persisted flags are never authorization": the code path
  that *decides* is not the code path that *grants*.

### 6.2 Concurrency / atomic state (not deferred)
- **Per-repo memory writes** take a real lease with atomic compare-and-set (row/version in the
  state DB), superseding `lock.json` for the runtime path. `lock.json` remains the *file-
  protocol* convention; the runtime uses DB leases so two concurrent tool sessions against the
  same repo memory cannot interleave writes.
- **Cost counters** update only via atomic transactions (§6.1) — no application-level
  read-modify-write.
- **Session concurrency** per project is a PEP-enforced lease pool.

### 6.3 Retry idempotency
- Each request carries an idempotency key. Retries are allowed only while **no client-visible
  side-effect boundary** has been crossed (first token flushed / tool_call surfaced → non-
  retryable). Hard attempt cap + exponential backoff with jitter. Provider `429`/`5xx`/
  connection errors are retryable pre-boundary; `4xx` request errors are not.

### 6.4 Prompt-injection posture
- Memory blocks and any provider/tool output re-fed into a prompt are wrapped in an
  untrusted-content envelope with an explicit non-instruction preamble. The router and PEP take
  instructions only from config and the authenticated request — never from model output or
  memory content.

Full security model (keys, auth, least-privilege file access, no insecure defaults) in
[`security-model.md`](security-model.md).

## 7. Provider adapters (summary)

Each adapter implements: `translate_request`, `call`, `translate_response`,
`translate_stream`, `capabilities(model)`, `price(model)`. Static facts (base URL, auth,
model ids, pricing, capabilities) live in a per-provider **manifest** (JSON, mirroring
l00prite's `vendors.json` philosophy — see `src/gateway/adapters/_manifests/`); the
translation logic is code with a conformance suite. A **native passthrough escape hatch**
(prior-art lesson) lets advanced callers reach provider-native features (Anthropic
`cache_control`/thinking) that the unified OpenAI schema would otherwise erase.

Verified provider specs, the OpenAI dual-surface (`/v1/chat/completions` vs `/v1/responses`),
the **confirmed reality of GLM 5.2**, and the honest pricing/egress caveats are in
[`provider-adapters.md`](provider-adapters.md).

## 8. Module layout

See [`../README.md`](../README.md) for the tree. Everything lives under `cli-os/` so the
protocol repo and validator are untouched; the layout is language-agnostic (runtime language is
Open Question Q3).

## 9. Scope and open questions

- v1 (ships) vs v2 (designed, deferred), with the cut line justified:
  [`v1-scope.md`](v1-scope.md).
- Assumptions made + decisions needed before implementation: [`open-questions.md`](open-questions.md).
