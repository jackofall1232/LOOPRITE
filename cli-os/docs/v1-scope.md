# v1 (ships) vs v2 (designed, deferred)

## v1 — the minimum that is a *control plane*, not a proxy

- **Ingress:** OpenAI-compatible `POST /v1/chat/completions` (streaming + non-streaming) and
  `GET /v1/models`.
- **Adapters:** the **verified v1 provider set** — Anthropic (a *real* native adapter, since its
  shape differs fundamentally from OpenAI) + OpenAI at minimum; additional OpenAI-compatible
  providers (GLM/Zhipu, Gemini, DeepSeek, Mistral, Grok, OpenRouter, local) are cheap thin
  shims and included **iff verified and the maintainer wants them** (Open Question Q1).
- **Router:** the explainable rule set (rules 1–5), decision logging, `l00prite route explain`.
- **Cost tracking:** real provider-reported usage (cache tokens included), versioned
  model→price map, per-project caps enforced by the PEP **in dollars**.
- **Retry:** idempotency-aware, HTTP-status-driven, hard cap + backoff, circuit breaker.
- **Memory:** behind the interface contract, with **naive-but-correct** retrieval v1 — rank by
  (pinned architecture/constraints) + (files referenced in the request) + (recent
  ledger/summary), mtime/hash staleness, graceful degradation to raw/none. **No embeddings.**
- **Concurrency/state:** atomic transactional store, per-repo memory leases, session
  concurrency cap.
- **Security:** server-side key storage, opaque token auth, least-privilege repo reads,
  safe-by-default startup (§ [`security-model.md`](security-model.md)).
- **Admin CLI:** add key, mint/revoke token, register repo, set caps, explain route, tail
  ledger.
- **Install:** one-command install; localhost bind default.

## v2+ — designed here, deliberately deferred

- Full **`/v1/responses`** support (stateful, second streaming + tool translator) — Open
  Question Q4.
- **Embedding / vector retrieval** and cross-repo / monorepo memory scoping.
- **Dashboard UI** — the CLI is the v1 control surface; API shapes are built dashboard-ready.
- **ML / adaptive routing** and learned "quality" scores.
- **Multi-tenant org / RBAC** beyond project-scoped tokens.
- **Prompt-cache-aware routing** optimization (pick targets to maximize cache hits).
- A **runtime harness** that mechanically enforces l00prite Execution-Mode run boundaries
  (already on the l00prite roadmap) by reusing the PEP.

## Why this cut line

v1 must prove the **hard** parts, because everything deferred is either additive (more
providers, dashboard) or an optimization (embeddings, ML routing) that a correct v1 adopts
*without re-architecting* — precisely because the interface contract and the PEP boundaries are
drawn first. The hard parts v1 proves:

1. **The Gateway ↔ Memory interface** actually decouples the two tracks (swap Memory internals,
   zero Gateway change).
2. **Real cost enforcement outside the deciding process** (the PEP) — the property the brief
   demands because self-reported stops aren't trustworthy.
3. **Adapter correctness across ≥2 genuinely different providers.** Anthropic's non-OpenAI shape
   forces a *real* adapter (system-as-field, `input_schema` tools, typed SSE events), not a
   passthrough — so the adapter abstraction is validated against a hard case, not a trivial one.
4. **Safe concurrency** — atomic leases and transactional counters, so concurrent tool sessions
   against the same repo can't corrupt memory or double-spend a budget.

Building `/v1/responses`, embeddings, or a dashboard **before** the interface + PEP are proven
would be polishing surface while the load-bearing walls are unbuilt.
