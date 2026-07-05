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
