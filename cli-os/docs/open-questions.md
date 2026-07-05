# Assumptions & open questions (need maintainer decisions before implementation)

Per the brief: do not guess on ambiguous product decisions — surface them. Implementation of
Track 1/Track 2 beyond adapter-approach validation waits on Q1–Q3 at minimum.

## Resolved for v1.0.0 (maintainer said "ship it" → proceeded on the recommendations)

- **Q3 runtime → Node.js (zero external deps).** Changed from the earlier Go recommendation for
  a concrete reason: the build environment blocks module fetch + live-provider egress, so Go
  couldn't be built or tested here, while Node runs natively, matches the existing Node
  validator, and gives real ACID via built-in `node:sqlite`. Flagged transparently; say the word
  and a Go port is straightforward against the same design.
- **Q1 providers → framework + Anthropic (native) + OpenAI-compatible (covers GLM 5.2, DeepSeek,
  Gemini, Groq, Mistral, OpenRouter, local) + mock.** Adding any OpenAI-compatible provider is a
  `l00prite provider add`, not code.
- **Q2 "quality" → operator-assigned static rank in config** (no ML).
- **Q4 `/v1/responses` → deferred to v2** (chat/completions ships first).
- **Q5 memory → naive rank-and-select v1** (no embeddings; swappable behind the interface).
- **Q6 cost cap → hard-block by default** (402 before spend).
- **A1 constraint supersession → taken** for the `cli-os/` subtree only; the prompt protocol
  stays dependency-free.

Still genuinely open / needs your input: **Q7 pricing** — Anthropic is first-party-confirmed;
other providers' price maps ship `null`/unconfirmed (egress-blocked). Want me to run a
first-party pricing pass from a networked environment, or will you supply the numbers?

## Assumptions made (flagged, not silently taken)

- **A1 — Constraint supersession.** CLI-OS supersedes the `.l00prite/constraints.md` hard rule
  "*No backend, hosted service, or external runtime dependency — plain Markdown, JSON, and a
  dependency-free Node validator only*", **for the new `cli-os/` component only**. The existing
  prompt protocol stays dependency-free and untouched. This is a documented hard rule, so it
  needs explicit maintainer blessing before runtime code lands. (The brief clearly directs this
  expansion; recording it so the override is deliberate, not silent.)
- **A2 — Single-tenant v1.** Target is self-hosted, single operator, a handful of repos.
  Multi-tenant org/RBAC is v2.
- **A3 — Separate artifact.** The runtime takes real dependencies (HTTP server, transactional
  store) and is a **separate artifact** from `scripts/validate-l00prite.js`, which stays
  zero-dependency.
- **A4 — Branch.** Work proceeds on the harness-designated `claude/looprite-cli-os-jntwqi`
  (the brief's "CLI-OS" is the conceptual name). Nothing merges to `main`. *(If you want the
  branch literally named `CLI-OS`, say so — a remote `origin/CLI-OS` already exists at the same
  commit and can be used instead.)*

## Open questions (change what gets built)

- **Q1 — Which providers are actually in v1?** Anthropic + OpenAI are assumed. GLM 5.2/Zhipu is
  **confirmed real** and is a cheap thin-shim — include in v1? Any of Gemini / DeepSeek /
  Mistral / Grok / OpenRouter / local Ollama? (Each extra OpenAI-compatible provider is a
  small, additive adapter, but pricing for several is currently unconfirmed — see Q7.)
- **Q2 — What does "quality" mean in routing?** v1 proposes an **operator-assigned static rank
  per model in config** (explainable, no inference). Acceptable? Or a different default
  preference among cost/latency/quality? (ML/learned quality is explicitly v2.)
- **Q3 — Runtime language for `cli-os/`?** Options: **TypeScript/Node** (closest to the existing
  Node validator; rich SDKs), **Go** (single static self-hostable binary; best "clone → one
  install command" story), or **Python** (fastest LLM ecosystem; heavier install). This drives
  the install story and dependency posture. *(Leaning Go for the self-hostable-binary goal, but
  this is your call.)*
- **Q4 — `/v1/responses` in v1?** Include a basic version, or defer entirely to v2? It is a
  *second* full streaming + tool-schema translator (named SSE events, flattened tools, no
  `[DONE]`), so it roughly doubles the OpenAI adapter's surface. Recommendation: **defer to v2**;
  ship `/v1/chat/completions` first.
- **Q5 — Memory retrieval depth for v1.** Is naive rank-and-select (no embeddings) acceptable
  for the first ship, or is embedding-based retrieval required in v1? The interface contract
  makes either swappable, so this is a "how much in v1" question, not an architecture one.
- **Q6 — Cost-cap behavior at the ceiling.** Hard-block new requests (safe default), or allow a
  grace/notify mode above the cap? Recommendation: **hard-block by default**, grace mode opt-in.
- **Q7 — Pricing confirmation.** API *shapes* are verified, but **pricing/context numbers for
  OpenAI, GLM, Gemini, DeepSeek, Grok, and OpenRouter are third-party/unconfirmed** because this
  session's egress policy blocked their first-party doc domains. Before the cost meter uses real
  numbers, a confirmation pass is needed from an environment with egress to those domains (or
  you supply the pricing map). Anthropic pricing is first-party-confirmed. Which do you want me
  to treat as authoritative for v1?
