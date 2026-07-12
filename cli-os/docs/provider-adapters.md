# Provider Adapters — verified specs (as of research pass, July 2026)

> These specs were verified in a fan-out research pass (Opus researchers; Fable 5 was assigned
> the adversarial-verify role but did not return verdicts before the pass was stopped — see
> **Verification caveats** below). Where a fact is first-party-sourced it is marked HIGH; where
> it comes from vendor SDKs/OpenAPI specs on GitHub (because first-party doc domains were
> egress-blocked in the session) it is marked as such. **Do not treat any pricing number here as
> production-ready without a first-party confirmation pass.**

## Verification caveats (read first)

- **Egress policy blocked many first-party doc domains** in the research session
  (`openai.com`, `z.ai`, `open.bigmodel.cn`, `platform.deepseek.com`, `docs.x.ai`,
  `openrouter.ai`, Google AI docs). The researchers correctly did **not** route around the
  proxy denials. As a result:
  - **API *shapes*** (endpoints, auth, streaming, tool schema) are high-confidence — they came
    from vendors' own **OpenAPI specs and official SDKs hosted on GitHub** (reachable).
  - **Pricing and context-window numbers** for OpenAI, GLM/Zhipu, Gemini, DeepSeek, Grok, and
    OpenRouter are **third-party / unconfirmed** — a first-party pricing-page fetch (needs
    egress those domains) is required before wiring real numbers into the cost meter. This is
    exactly the "don't hardcode pricing from training data without verifying current specs"
    risk the brief called out; the honest status is *shapes verified, prices pending*.
  - **Anthropic** is the exception: first-party docs were reachable (`platform.claude.com`),
    so both shape and pricing are HIGH confidence.
- The Fable 5 adversarial-verify verdicts did not complete (the verifiers hit the same
  egress blocks and ran long). The researchers' own `flags`/confidence self-assessments stand
  in for that pass and are reflected in the confidence column.
- **2026-07-11 follow-up pass** upgraded **Gemini pricing to first-party** (cloud.google.com
  fetched directly; `ai.google.dev` still egress-blocked — a Vertex-vs-Developer-API channel
  caveat is recorded in `gemini.json`'s `verification.pricing_note`), confirmed **xAI's shape
  first-party** via xai-org's official GitHub repos (SDK + cookbook; `docs.x.ai` still
  egress-blocked, so xAI **pricing remains null/unconfirmed**), and re-verified **Venice
  pricing with zero drift** against its first-party docs mirror.

## The flagged question: does "GLM 5.2" exist?

**Yes — GLM 5.2 is a real, currently-documented model. Not hallucinated.**
- Model id **`glm-5.2`** appears **verbatim** in Zhipu AI's official Python SDK
  (`zai-org/z-ai-sdk-python`: README, `examples/`, and `src/`) — a primary source. It is the
  flagship reasoning / agentic-coding model of the GLM-5 generation and supports a
  `reasoning_effort` knob (`none`…`max`).
- Provider is **Zhipu AI / Z.ai (BigModel)**. It exposes an **OpenAI-compatible** endpoint and
  even ships an **Anthropic-Messages-compatible** endpoint (drop-in for Claude Code).
- Adjacent real ids in the same SDK: `glm-5.1` (prior flagship; the one exposed via the
  Anthropic-compat endpoint), `glm-5v-turbo` (vision), and the `glm-4.6`/`glm-4.5*` families.
- **Caveat:** its **pricing and context window are third-party/unconfirmed** (docs
  egress-blocked). Whether GLM 5.2 is actually in the **v1 provider set** is a product decision
  (Open Question Q1) — its reality is confirmed; its inclusion is your call.

## Adapter interface (all providers implement this)

```
translate_request(openai_req, model)  -> provider_req
call(provider_req, stream?)           -> provider_resp | provider_stream
translate_response(provider_resp)     -> openai_resp        (+ real usage)
translate_stream(provider_stream)     -> openai_sse_chunks  (+ usage on final chunk)
capabilities(model)                   -> {tools, parallel_tools, vision, max_context, ...}
price(model)                          -> {input, output, cache_write, cache_read, ...}
```
Static facts live in a per-provider **manifest** (`src/gateway/adapters/_manifests/*.json`);
translation is code + a conformance suite. Two design rules from prior art:
1. **Streaming gets its own stateful translator**, separate from the non-streaming path — it is
   the hardest surface and fails silently.
2. **Native passthrough escape hatch** — the unified OpenAI schema silently erases native
   capabilities (Anthropic `cache_control`/thinking, provider-specific params); always offer a
   native path so advanced callers aren't quality-capped.

## Verified provider matrix

| Provider | OpenAI-compat | Adapter effort | Base URL | Shape confidence | Pricing confidence |
|---|---|---|---|---|---|
| **Anthropic** | compat endpoint is *test/eval-only* | **full native adapter** | `https://api.anthropic.com` (`/v1/messages`) | HIGH (first-party) | HIGH (first-party) |
| **OpenAI** | native (it *is* the schema) | thin shim (+ `/v1/responses` work) | `https://api.openai.com/v1` | HIGH (own OpenAPI spec) | LOW (docs blocked) |
| **Zhipu / GLM (incl. glm-5.2)** | OpenAI-compat endpoint | thin shim | `https://api.z.ai/api/paas/v4` | HIGH (own SDK) | LOW (third-party) |
| **Venice AI** (reseller) | OpenAI-compat | thin shim | `https://api.venice.ai/api/v1` | HIGH (first-party) | HIGH (first-party docs mirror; re-verified 2026-07-11, no drift) |
| **Google Gemini** | OpenAI-compat layer | thin shim | `https://generativelanguage.googleapis.com/v1beta/openai/` | HIGH (own cookbook, 2026-07-11) | HIGH (first-party cloud.google.com, 2026-07-11 — Vertex-channel caveat in manifest) |
| **DeepSeek** | OpenAI-compat | thin shim | `https://api.deepseek.com` | MED (docs blocked) | LOW |
| **xAI Grok** | OpenAI-compat | thin shim | `https://api.x.ai/v1` | HIGH (own SDK + cookbook on GitHub, 2026-07-11) | LOW (docs.x.ai egress-blocked; all prices null/unconfirmed) |
| **Mistral** | OpenAI-compat | thin shim | `https://api.mistral.ai/v1` | HIGH (own OpenAPI+SDK) | MED |
| **OpenRouter** (aggregator) | OpenAI-compat | thin shim | `https://openrouter.ai/api/v1` | MED (docs blocked) | passthrough |

### Anthropic (full native adapter — HIGH confidence)
- `POST https://api.anthropic.com/v1/messages`; headers `x-api-key`,
  `anthropic-version: 2023-06-01`, `anthropic-beta` (features). An OpenAI-compatible
  `/v1/` base **exists but is labeled test/eval-only, non-production** → build the native
  adapter.
- Request deltas vs OpenAI: `system` is a **top-level field** (not a `system` message);
  `max_tokens` is **required**; tools are `{name, description, input_schema}` (not
  `{type:"function", function:{…}}`); `tool_choice` is
  `{type:"auto"|"any"|"tool"|"none", name?}`.
- Streaming SSE events: `message_start` → `content_block_start` →
  `content_block_delta` (`text_delta` / `thinking_delta` / `input_json_delta`) →
  `content_block_stop` → `message_delta` (carries `stop_reason` + `usage`) → `message_stop`.
  Must be rebuilt into OpenAI `chat.completion.chunk` `choices[].delta` frames + `[DONE]`.
- `stop_reason` → OpenAI `finish_reason`: `end_turn`→`stop`, `max_tokens`→`length`,
  `tool_use`→`tool_calls`, `stop_sequence`→`stop`, `refusal`→`content_filter`.
- Usage in every response: `input_tokens`, `output_tokens`, `cache_creation_input_tokens`,
  `cache_read_input_tokens` → exact cost. Prompt caching via `cache_control:{type:"ephemeral",
  ttl?}` (prefix match, ≤4 breakpoints); cache-read ≈0.1×, cache-write ≈1.25×/2× — the meter
  must price these separately.
- Pricing (per MTok in/out, verified July 2026): Fable 5 `$10/$50` · Opus 4.8 `$5/$25` ·
  Opus 4.7 `$5/$25` · Sonnet 5 `$3/$15` (`$2/$10` intro thru 2026‑08‑31) · Sonnet 4.6 `$3/$15`
  · Haiku 4.5 `$1/$5`. Model ids: `claude-fable-5`, `claude-opus-4-8`, `claude-sonnet-5`,
  `claude-haiku-4-5`.

### OpenAI (native schema, but two surfaces — HIGH shape confidence)
- Two endpoints, **each with its own streaming wire format and tool schema** — the adapter must
  handle both:
  - `POST /v1/chat/completions` — classic. SSE where every line is `data: {chat.completion.chunk}`;
    `choices[].delta` carries `{content, role, tool_calls[]}`; streamed tool-call arguments
    arrive as partial JSON fragments in `delta.tool_calls[].function.arguments` (accumulate by
    `index`); terminates with a literal `data: [DONE]`; set `stream_options.include_usage:true`
    to get a final usage chunk. Tools: `{type:"function", function:{name, description,
    parameters, strict}}`; `tool_choice: {type:"function", function:{name}}`.
  - `POST /v1/responses` — newer, recommended for new work, **stateful**. Named SSE events
    (`event: <type>\ndata: {json}`): `response.created` → `…output_text.delta` (repeated) →
    `response.completed`; function args via `response.function_call_arguments.delta/.done`;
    reasoning via `response.reasoning_text.delta`. **No `[DONE]` sentinel** — terminal event is
    `response.completed`/`.failed`/`.incomplete`. Tools are **flattened**: `{type:"function",
    name, description, parameters, strict}` (no nested `function` wrapper). Some newer models
    are Responses-only. → This is exactly why `/v1/responses` support is scoped as a v1/v2
    decision (Q4): it is a *second* full streaming + tool translator.
- Auth: `Authorization: Bearer <key>`; optional `OpenAI-Organization`/`OpenAI-Project`.
- **Pricing/current-flagship-model ids: LOW confidence — first-party docs egress-blocked.**
  Do a first-party confirmation pass before hardcoding.

### Zhipu / GLM (thin shim — HIGH shape confidence, LOW pricing)
- Base `https://api.z.ai/api/paas/v4` (OpenAI-shaped); explicit OpenAI-compat base
  `https://api.z.ai/api/openai/v1`; Anthropic-compat base `https://api.z.ai/api/anthropic`;
  mainland `https://open.bigmodel.cn/api/paas/v4`.
- Auth: send the raw API key as `Authorization: Bearer <key>` against the OpenAI-compat base.
  (The official SDK's *native* path mints a short-lived HS256 JWT from `{id}.{secret}` — the
  legacy Zhipu scheme — but the OpenAI-compat path takes the raw key.)
- Streaming: OpenAI-style SSE chunks + `[DONE]`; note a GLM-specific
  `choices[].delta.reasoning_content` field for thinking models (separate from `content`).
- Tools: OpenAI-compatible `tools[]` / `tool_calls[]`; also supports inline server-side tools
  (`web_search`, retrieval, MCP toolservers).
- Models (ids SDK-verified; **prices third-party/unconfirmed**): `glm-5.2` (flagship,
  ~`$1.40/$4.40` reported), `glm-5.1`, `glm-5v-turbo` (vision), `glm-4.6`, `glm-4.5`/`-air`/
  `-flash`(reported free)/`-4.5v`.

### xAI Grok (thin shim — HIGH shape confidence, pricing unconfirmed)
- Base `https://api.x.ai/v1` (OpenAI-shaped `/chat/completions`); auth is the raw xAI API key
  (`XAI_API_KEY`) as `Authorization: Bearer <key>`. Both the base URL and the Bearer scheme are
  **first-party**: xai-org's official cookbook notebooks point the stock `openai` Python client
  at this base, and `xai-org/xai-sdk-python`'s client defaults to `api.x.ai` — read directly on
  GitHub 2026-07-11 (`docs.x.ai` itself was egress-blocked).
- **2026-05-15 retirement:** xAI retired eight model slugs — including **`grok-code-fast-1`** —
  effective 2026-05-15. Those slugs still resolve but **silently redirect to `grok-4.3` and bill
  at `grok-4.3`'s rate**. `grok-code-fast-1` is therefore **deliberately absent** from the
  manifest; **never re-add it** (it would masquerade as a distinct, cheaper coding model while
  actually billing as the flagship). The current coding-specialized model is **`grok-build-0.1`**.
- **`grok-4.5`** is xAI's current flagship (announced 2026-07-08, corroborated by independent
  press). It is real but **not yet present in the official SDK's model list**, so it is included
  in the manifest but is deliberately **not** `models[0]` — key validation probes the
  SDK-verbatim `grok-4.3` instead, never depending on the unconfirmed id.
- **All xAI pricing is `null` / `price_confidence: "unconfirmed"`**: every first-party xAI domain
  (`docs.x.ai`, `x.ai`, `console.x.ai`) returned HTTP 403 at the proxy CONNECT layer on
  2026-07-11. Per this repo's pricing discipline, snippet-only figures are not written as numbers,
  so the cost meter reports Unconfirmed/not-Priced and the cost-router sorts every xAI model last.
  Re-fetch `docs.x.ai/developers/models` from an unblocked network to upgrade.

### DeepSeek / Mistral / OpenRouter (thin shims, not yet shipped)
- All expose an OpenAI-compatible `/chat/completions` surface → thin-shim adapters, low
  incremental cost per provider (this is why "more providers" is cheap v2 work, not a
  re-architecture).
- **Mistral** is the best-verified of these (own OpenAPI + SDK): note its **9-char tool-call-id
  constraint** — a documented adapter footgun when relaying tool ids.
- **OpenRouter** is itself an aggregator: it could serve as a *single upstream adapter* fronting
  many models behind one key (a fast way to widen coverage) — but it hides per-provider control
  and adds a hop; treat it as one adapter among several, not the whole gateway.
- DeepSeek/OpenRouter: **shapes MED, pricing LOW** (docs egress-blocked) — confirm first-party
  before v1 inclusion.

## Prior-art adapter lessons applied (LiteLLM, OpenRouter, Portkey, Cloudflare AI Gateway)

- **Per-provider transformer/config classes**, not a switch statement (LiteLLM `BaseConfig`
  with pure `transform_request`/`transform_response`).
- **OpenAI Chat Completions as canonical interchange** + a **native passthrough** escape hatch.
- **Explicit, per-field, bidirectional param mapping** (`max_tokens` vs `max_tokens_to_sample`,
  `stop` vs `stop_sequences`, tool_choice enums, system-message placement).
- **Machine-readable capability manifest** per provider; route/translate against it, reject
  unsupported combos with a clear error.
- **Trust provider-reported usage over local estimates**; streaming usage arrives in a
  **separate final chunk** and is **opt-in** (OpenAI `stream_options.include_usage`, Anthropic
  `message_delta.usage`) — a top source of undercounting bugs.
- **Versioned model→price map** with separate input/output/cache-write/cache-read rates.
- **Virtual keys**: callers never see real provider keys; store real keys with envelope
  encryption, resolve at call time.
