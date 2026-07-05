# Provider pricing confirmation pass — 2026-07-04

This resolves Open Question **Q7** (pricing/context numbers first-party-confirmed only for
Anthropic). It records, per provider manifest under
[`../internal/gateway/adapters/manifests/`](../internal/gateway/adapters/manifests/), what was confirmed
against the provider's **own** pricing/docs page (not aggregators, not third-party trackers), the
source URL, the date checked, and what remains `null`.

> **Method.** Each provider's official pricing/docs domain was fetched live. Where the official
> page was reachable, per-model input/output/cache pricing and context windows were read verbatim
> from it. Where every official domain returned an error, the number was left `null` — **not**
> guessed from memory and **not** carried over from the prior third-party research pass. A
> first-party-domain-restricted web search was used only to *locate* official pages, never as a
> pricing source.

## Summary

| Provider | Official page reachable? | Pricing status | Source |
|---|---|---|---|
| **Anthropic** | ✅ yes | **Confirmed first-party** (all models) | `platform.claude.com/docs/en/about-claude/pricing` |
| **OpenAI** | ❌ 403 (all domains) | **Unconfirmed — left `null`** | none reachable |
| **Zhipu / GLM** | ❌ 403 (all domains) | **Unconfirmed — left `null`** (prior third-party figures removed) | none reachable |

## Egress reality in this environment

The task premise ("this environment has real network egress") holds for **Go module fetches**
(`proxy.golang.org` is allow-listed → the port proceeds) and for **Anthropic docs**. It does **not**
hold for the OpenAI or Zhipu documentation domains:

- The outbound HTTPS proxy denies `CONNECT` to `openai.com:443`, `api-docs.deepseek.com:443`, etc.
  with a policy `403` (confirmed via the proxy status endpoint's `recentRelayFailures`).
- The server-side fetch tool independently returns `403 Forbidden` for every official OpenAI and
  Zhipu URL tried (see the per-provider lists below), consistent with provider bot protection on
  top of the egress denial.

This is exactly the case the brief anticipated: *"If a provider's official pricing page cannot be
reached … leave it `null` … do not let the cost meter silently treat `null` as zero."*

## Anthropic — CONFIRMED (first-party)

Source: **https://platform.claude.com/docs/en/about-claude/pricing** — fetched **2026-07-04**.
Prices are USD per **million tokens (MTok)**. Context windows for the 1M-context models are
confirmed from the same page's "Long context pricing" section; Haiku 4.5 is the standard 200K.

| Model (id) | Input | Output | 5m cache write | 1h cache write | Cache read | Context |
|---|---|---|---|---|---|---|
| `claude-fable-5` | $10 | $50 | $12.50 | $20 | $1.00 | 1,000,000 |
| `claude-opus-4-8` | $5 | $25 | $6.25 | $10 | $0.50 | 1,000,000 |
| `claude-sonnet-5` | $3 | $15 | $3.75 | $6 | $0.30 | 1,000,000 |
| `claude-haiku-4-5` | $1 | $5 | $1.25 | $2 | $0.10 | 200,000 |

Notes:
- These match the values already in `anthropic.json`; the manifest was updated to add the
  first-party `price_source`/`price_checked` provenance and the `cache_write_1h` rate.
- **Sonnet 5 introductory pricing:** the page confirms an intro rate of **$2/$10 per MTok**
  (5m cache write $2.50, 1h $4, cache read $0.20) in effect **through 2026-08-31**, reverting to
  the steady-state $3/$15 on 2026-09-01. The manifest carries the **steady-state** $3/$15 (matching
  the pre-existing shipped behavior). Date-gating the intro rate in the meter is a deliberate,
  documented follow-up — not implemented here, because it is a behavioral change (needs a billing
  clock) beyond a language port, and it would only ever *under*-bill relative to the manifest, never
  over-bill silently.
- `max_output` per model is not published on the pricing page; the existing values are model-card
  facts carried unchanged, not fabricated.

## OpenAI — UNCONFIRMED (official pages unreachable)

Official URLs attempted on 2026-07-04, **all HTTP 403**:

- `https://platform.openai.com/docs/pricing`
- `https://openai.com/api/pricing/`
- `https://developers.openai.com/api/docs/pricing`
- `https://help.openai.com/en/articles/7127956-how-much-does-gpt-4-cost`

Result: **pricing and concrete flagship model ids left `null`.** The API *shape* (endpoints, auth,
streaming, tool schema) stays HIGH-confidence — it comes from OpenAI's own OpenAPI spec on GitHub,
which is reachable — but no price or context number was confirmed. The manifest keeps a single
`PENDING-first-party-confirmation` placeholder model (filtered out of the routable catalog), so
OpenAI contributes **no routable models and bills nothing** until a reachable pricing page fills it
in. A first-party-domain-restricted web search *did* surface commonly-cited figures, but a search
summary is not a first-party page read and could be contaminated by training data, so per the brief
it was **not** used to populate the manifest.

## Zhipu / GLM — UNCONFIRMED (official pages unreachable)

Official URLs attempted on 2026-07-04, **all HTTP 403**:

- `https://docs.z.ai/guides/overview/pricing`
- `https://open.bigmodel.cn/pricing`
- `https://z.ai/model-api`

A first-party-domain-restricted search (`z.ai`, `docs.z.ai`, `bigmodel.cn`, `open.bigmodel.cn`)
returned only GLM overview/blog pages — **no pricing table**. Result: **all GLM prices left
`null`.** The previously-carried third-party figures for `glm-5.2` ($1.40 input / $4.40 output /
$0.26 cache-read) were **removed** — the brief is explicit that unconfirmed numbers must not be
carried over from memory. Model *ids* (`glm-5.2`, `glm-5.1`, `glm-5v-turbo`) remain
SDK-verified (`zai-org/z-ai-sdk-python`); only their prices/context are null.

## How the cost meter handles `null` pricing (no silent $0)

The Go cost meter (`internal/gateway/meter.go`, ported from `meter.js`) never lets an unconfirmed
price masquerade as a confident dollar figure:

- **Unpriced model** (`input`/`output` null): the meter returns
  `{ usd: 0, priced: false, estimated: true, unconfirmed: true }`. The row is flagged
  `cost_unconfirmed` in the ledger and the response carries `x-l00prite-cost-unconfirmed: true`.
  It commits `$0` to the budget *because there is no price to bill*, and the `unconfirmed` flag makes
  that explicit rather than silent.
- **Priced but not first-party-confident** (`price_confidence != "high"`, numbers present): the
  meter computes the dollar figure but marks it `estimated: true, unconfirmed: true` so it is never
  presented as authoritative. (After this pass no shipped manifest model is in this state — GLM was
  nulled — but the code path is retained and unit-tested via a synthetic fixture.)
- **Router safety:** the auto-router's `cost` preference excludes unpriced (tier-2) models entirely
  (`no_priced_model` 400) so an unpriceable model can never win the "cheapest" slot with a fabricated
  `$0`. This was already enforced in the Node version and is preserved.

## What still needs a networked environment

- OpenAI: a fetch of a reachable OpenAI pricing page to fill real flagship model ids + prices.
- Zhipu/GLM: a fetch of `z.ai` / `open.bigmodel.cn` pricing to fill GLM prices + context windows.
- DeepSeek, Gemini, Grok, Mistral, OpenRouter: these have **no manifest** in the v1 tree (only
  Anthropic, OpenAI, Zhipu ship as example manifests), so they are out of scope for this pass; they
  are added as data (`l00prite provider add` + a manifest) if/when the maintainer includes them.
