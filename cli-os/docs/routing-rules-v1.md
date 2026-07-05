# Routing — explainable rule set v1 (no ML)

The router turns a request into a concrete `(provider, model)` target and a **logged,
inspectable** `RoutingDecision`. v1 is deliberately deterministic and explainable — no ML, no
learned scores. Design informed by OpenRouter, LiteLLM, and Portkey (see prior-art lessons in
[`provider-adapters.md`](provider-adapters.md)).

> **Status update:** Rules 3 (capability filter) and 4 (preference tiebreak) below are now
> **implemented** as the opt-in **auto-router** — see [`routing-auto-mode.md`](routing-auto-mode.md).
> Per-request cost-optimization ("cheapest sufficient model") ships as `auto:cheap`. Cross-provider
> **bridging** (a model delegating a sub-task to another provider) is documented in
> [`provider-bridging.md`](provider-bridging.md).

## Inputs / outputs

- **Inputs:** the resolved `principal` (project + policy), the request (its `model` field +
  optional `x-l00prite-route` header/hints), the provider **capability manifest**, and live
  provider health (circuit-breaker state).
- **Output:** `(provider, model)` + a `RoutingDecision`:
  ```jsonc
  { "request_id": "...", "rule_id": "alias_map", "inputs": {...},
    "chosen": "anthropic:claude-opus-4-8", "alternatives": ["openai:gpt-...", "zhipu:glm-5.2"],
    "reason": "alias 'smart' → project default; capability + health ok" }
  ```
  Retrievable via `l00prite route explain <request-id>`.

## Rules (first match wins)

1. **Explicit pin.** If the client `model` maps to a concrete `provider:model` (config, or
   `provider/model` syntax), or the `x-l00prite-route` header is set → use it. Operator intent
   always wins.
2. **Alias map.** A named alias (`fast`, `cheap`, `smart`, `local`, …) resolves via project
   config to a concrete target. Aliases are **data, not code** — editable without a deploy.
3. **Capability filter.** Drop any candidate whose model can't satisfy the request: needs tools,
   needs ≥N context tokens, needs vision, needs streaming usage. Capabilities are declared per
   model in the provider manifest; an unsatisfiable request gets a clear typed error, not a
   silent downgrade.
4. **Preference tiebreak.** Among survivors, order by the project's declared preference —
   `cost` | `latency` | `quality` — then by circuit-breaker health (skip currently-tripped
   providers). **"quality" in v1 = an operator-assigned static rank per model in config**
   (explainable, no inference). See Open Question Q2.
5. **Fallback chain.** On a *retryable, side-effect-free, non-4xx* provider failure, advance to
   the next healthy target. Two layers, kept explicit because users reason about them
   differently:
   - **Provider failover** — keep the *same model*, swap the serving provider (least disruptive;
     e.g. an OpenAI-compatible model served by two backends).
   - **Model fallback** — swap to a *different model* per the configured chain.

## Hard rules baked in from prior art

- **Never fail over on a `4xx`.** A malformed request (`400`) fails identically everywhere;
  failing it over just burns budget across providers. Fallback triggers are HTTP-status-driven
  and operator-configurable (`on_status_codes`), defaulting to retryable classes only.
- **Circuit breaker / cooldown.** After `allowed_fails` within a window, a backend is excluded
  for `cooldown_time` so a flapping provider stops poisoning requests. Health feeds rule 4.
- **Config-first and declarative.** Routing is data (`{mode, targets[], weights, on_status_codes}`),
  not branching code — so an operator can change routing without a code change, and every
  decision remains explainable.
- **Bounded.** The fallback chain has a hard hop cap; each hop is logged. No infinite failover.

## Explicitly out of scope for v1 (→ v2+)

- ML / learned routing, latency-EWMA or quality-inferred scoring. (A static `latency` preference
  was deliberately **not** shipped — a per-model latency hint with no measurement is fake precision;
  `cost` and `quality` are the real axes.)
- Weighted load-balancing across many identical backends beyond a simple configured weight.
- ~~Per-request cost-optimization that inspects prompt size to pick the cheapest sufficient model~~
  — **now shipped** as `auto:cheap` (blended prompt+output price from the manifest, unpriced/
  unconfirmed models sorted last). See [`routing-auto-mode.md`](routing-auto-mode.md).
