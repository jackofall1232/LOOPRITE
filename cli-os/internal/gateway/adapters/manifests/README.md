# Provider manifests

Machine-readable, per-provider static facts that drive the adapters — the "data" half of the
"adapters are data + code" design. Translation logic is code (`../*.go`); everything here is
declarative so adding or retuning a provider is a data edit, not a code change. These files are
**embedded** into the binary at build time (`//go:embed manifests/*.json` in `../registry.go`), so
the single static binary ships them with no external files.

## Fields

| Field | Meaning |
|---|---|
| `provider` / `display_name` | machine key / human label |
| `adapter` | `native-messages` (full translator) or `openai-compat` (thin shim) |
| `base_url`, `endpoints`, `auth` | how to reach it |
| `streaming` | wire format the stream translator must handle |
| `tool_schema` | tool-calling shape (`openai-function`, `openai-function-flat`, `anthropic-input-schema`) |
| `verification` | provenance + confidence for shape and pricing (honesty about egress limits) |
| `models[]` | id, context, max_output, capabilities, price map (with per-model `price_confidence`) |

`price_confidence: "unconfirmed"` means the number is not first-party-confirmed and MUST NOT be
treated by the cost meter as an authoritative dollar figure. `price_per_mtok` carries **separate**
input / output / cache-write (5m/1h) / cache-read rates because provider prompt-cache tokens are
billed differently and the meter must not conflate them.

The 2026-07-04 first-party pricing confirmation pass (see
[`../../../../docs/pricing-confirmation.md`](../../../../docs/pricing-confirmation.md)) confirmed
Anthropic first-party and left OpenAI and Zhipu/GLM pricing `null` (their official pages were
egress-blocked). The meter (`../../meter.go`) returns `Unconfirmed=true` / `Priced=false` for a
`null`-priced model rather than a silent `$0`, and the cost-preference auto-router refuses to route
to an unpriced model.

The 2026-07-05 Android pass added `venice.json` (Venice AI — OpenAI-compatible; per-model pricing
first-party-confirmed via Venice's own docs mirror, capability flags marked training-knowledge
pending first-party confirmation) and `gemini.json` (Google's documented OpenAI-compatible
endpoint; pricing left `null`/unconfirmed — Google's pricing pages were egress-blocked from that
build environment, same discipline as OpenAI/Zhipu above). Venice's catalog intentionally resells
other labs' models under Venice-hosted ids (`claude-sonnet-5`, `openai-gpt-52-codex`, …); bare
model ids that exist in more than one enabled provider's catalog resolve by provider order
(router.go rule 3), so pin `provider/model` when the distinction matters.

The 2026-07-11 provider-presets pass added `xai.json` (xAI Grok — OpenAI-compatible at
`api.x.ai/v1`; model ids read verbatim from xAI's official SDK source; **all prices
null/unconfirmed** — every first-party xAI domain was egress-blocked; the retired
`grok-code-fast-1` is deliberately absent), upgraded `gemini.json` from null/unconfirmed to
**first-party pricing** fetched directly from Google's own pricing page (with a recorded
Vertex-vs-Developer-API channel caveat) plus the confirmed `gemini-3.5-flash`/`gemini-3.1-*`
models, and re-verified `venice.json` against the first-party mirror (zero drift). Manifests now
also drive the Add-provider UI: `Presets()` in `../registry.go` projects
`provider`/`display_name`/`adapter`/`base_url`/first-model into `GET /v1/providers/catalog`
(unauthenticated; static embedded facts only). A manifest appears in the UI only if listed in
`presetOrder` — the mock adapter never does.
