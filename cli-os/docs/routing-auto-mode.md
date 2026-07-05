# Auto-routing — best provider per task, most-efficient mode

Auto-routing implements **Rule 3 (capability filter)** and **Rule 4 (preference tiebreak)** from
[`routing-rules-v1.md`](routing-rules-v1.md), which were designed but not yet built in v1.0.0. It
answers two operator asks:

- **"Pick the best provider for THIS task"** — e.g. a vision request must go to a multimodal model;
  a hard reasoning task should go to the highest-ranked model.
- **"Pick the most efficient provider"** — the cheapest model that can still satisfy the request.

It is **opt-in, deterministic, and explainable** — no ML, no learned scores (Open Question Q2).
Every decision is logged and inspectable (`l00prite route explain`, `l00prite route plan`).

## How a request opts in

Auto never fires unless the caller asks for it, and an explicit `provider/model` pin always wins
first (operator intent). A request selects auto by either:

- **model field**: `"model": "auto"` or `"model": "auto:cheap"` / `"auto:quality"` / `"auto:balanced"`
- **route header**: `x-l00prite-route: auto:<profile>` (a header auto signal beats a model pin)

A bare `auto` uses the configured default profile (`routing.autoDefaultProfile`, ships `balanced`).

## The pipeline (all four steps logged into the RoutingDecision)

1. **Derive requirements** from the request, deterministically:
   - `needs_tools` — the request carries `tools`.
   - `needs_vision` — any message has an `image_url` content part.
   - `needs_streaming_usage` — `stream:true` + `stream_options.include_usage`.
   - `min_context_tokens` — a coarse estimate of prompt + requested max output.
2. **Capability filter (Rule 3)** — drop any `(provider, model)` whose manifest can't serve the
   requirements: tools, vision, streaming-usage, a `min_context_tokens` that exceeds the model's
   window, or a requested `max_tokens` above the model's `max_output`. Absent boolean capabilities
   read as **false** (fail-closed). A model with an **undeclared** (`null`) context/max-output
   **passes** with a caveat rather than being banned on missing data. `min_context_tokens` is
   estimated from **text only** plus a fixed per-image constant — base64 image bytes are never
   counted as prompt text. If nothing survives → a typed error, never a silent downgrade.
3. **Preference scoring (Rule 4)** — order survivors by the profile's preference:
   - `cost` — cheapest by a blended estimate `prompt_tokens·input + max_out·output` from the
     manifest price map.
   - `quality` — highest operator-assigned static rank (`routing.qualityRanks`), 0–100.
   - `balanced` — a Borda blend of the cost and quality orderings.
4. **Health** — circuit-tripped providers are dropped **after** the capability filter, so the error
   distinguishes "no model can do this" from "the capable models are temporarily down".

### The unknown-price rule (why the cheapest column isn't just the smallest number)

Cost sorting keys on the tuple **`(price_tier, blended_usd, name)`** where the tier is:

| tier | meaning |
|---|---|
| 0 | priced **and** first-party-confident |
| 1 | priced but the number is third-party/unconfirmed |
| 2 | unpriced (`price_per_mtok` null) |

Tier leads the sort, so a tier-1 model (a cheaper-but-unconfirmed number) can **never** masquerade
as cheaper than a confirmed one. Additionally, the **`cost` preference excludes tier-2 (unpriced)
models entirely** — an unpriceable model can't honestly be "cheapest" and would commit `$0` through
the meter (unmetered spend that never binds the daily cap). If no priced model can serve the
request, `auto:cheap` returns `400 no_priced_model` rather than route blind on price. `auto:cheap`
picks the cheapest **confirmed** price first; it reaches an unconfirmed (tier-1) model only when no
confirmed one qualifies, and the decision reason says so.

## Profiles (data, not code)

Profiles live in `config.json`'s `routing` block (facts stay in manifests; opinions stay in config).
Built-in defaults:

```jsonc
"routing": {
  "autoDefaultProfile": "balanced",
  "profiles": {
    "cheap":    { "preference": "cost" },
    "quality":  { "preference": "quality" },
    "balanced": { "preference": "balanced" }
  },
  "qualityRanks": { "anthropic/claude-opus-4-8": 96, "zhipu/glm-5.2": 82, "...": 0 }
}
```

Operators add their own task profiles — a profile may also **require** capabilities regardless of
what the request implies:

```jsonc
"profiles": { "vision": { "require": ["vision"], "preference": "quality" } }
```

Because task-specificity mostly falls out of the auto-derived requirements (a request with an image
already forces vision), a small profile set covers the two headline asks; the requirement derivation
does the per-task work.

## Errors (no silent downgrade)

- **No capable model** → `400` `no_capable_model` (deterministic — retrying elsewhere is futile),
  with a per-candidate rejection list (`"zhipu/glm-5.2: needs vision; model is not multimodal"`).
- **`auto:cheap` but no capable model is priced** → `400` `no_priced_model`.
- **Capable models exist but all are circuit-tripped** → `503` `service_unavailable` (transient).
- **Unknown profile** → `400`.

All three are appended to the ledger as a `denied_route` decision, so "every routing decision is
logged" holds even when auto-mode denies.

## Inspecting it

```bash
l00prite route profiles                                  # profiles + quality ranks
l00prite route plan auto:cheap --task "refactor" --vision  # dry-run: winner + candidates + rejects
curl ... -H 'x-l00prite-dry-run: 1' -d '{"model":"auto:quality",...}'   # HTTP dry-run (no spend)
l00prite route explain <request-id>                      # the decision that actually ran
```

## Not in scope (still v2)

Learned/EWMA routing, a real measured `latency` preference (a static latency hint is fake
precision — deliberately omitted), and prompt-size-adaptive model downshifting beyond the cost
preference. The manifest + PEP seams make all of these additive later with no re-architecture.
