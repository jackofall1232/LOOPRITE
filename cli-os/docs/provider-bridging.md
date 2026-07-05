# Provider bridging — "Codex asks Claude to use a tool"

Bridging lets the model handling a request **delegate a self-contained sub-task to a different
provider/model** mid-conversation, through the gateway. The canonical example: a Codex-style agent
running on one provider hits a sub-problem another model is better at (a hard proof, an image, a
cheap bulk step), calls a gateway tool to ask that model, and folds the answer back into its work.

It is **off by default** (no silent cross-provider spend) and **bounded** at every axis.

## Mechanics

When bridging is armed, the gateway injects one extra tool into the primary model's request:

```jsonc
{ "type": "function", "function": { "name": "l00prite_bridge",
  "parameters": { "target": "anthropic | anthropic/claude-sonnet-5 | auto | auto:quality",
                  "task": "the COMPLETE, self-contained sub-task",
                  "context": "optional extra background",
                  "forward_tools": "optional: share your tools so the delegate can propose calls" } } }
```

If the primary model calls it, the gateway executes the call **server-side**:

1. Route the `target` (a provider, a `provider/model`, or an `auto:*` profile) through the **same
   router**, and run it through the **same `runTurn` primitive** (same adapters, same PEP budget).
2. Wrap the delegate's answer in the cross-provider **untrusted-content envelope** and hand it back
   to the primary as a `role:"tool"` result.
3. Let the primary continue. Repeat until it produces a final answer (or the hop cap forces one).

`forward_tools` shares the **client's own** tool definitions (never the bridge tool) with the
delegate so it can *propose* tool calls; those come back as **inert data** inside the envelope — the
gateway does not execute client-side tools. That is the honest realization of "ask Claude to use a
tool": the delegate decides which tool to call with which arguments, and that proposal is returned
for you or your client to execute.

## Safety invariants (this is the load-bearing part)

- **Recursion is impossible by construction.** The bridge tool is injected **only** into the primary
  conversation. Delegated sub-calls are fresh requests built without it, at `depth 1`. A delegate has
  no bridge tool to call, so it cannot delegate — no A→B→A loop, regardless of aliases or `auto`
  targets resolving back to the primary provider.
- **Bounded hops.** A hard hop cap (`routing.bridge.maxHops`, default 3) limits delegated sub-calls.
  A request header (`x-l00prite-bridge-max-hops`) may only **lower** it, never raise it — the same
  self-modification guard as Execution Mode. On the turn the cap is reached the bridge tool is
  stripped so the primary **must** finalize. A backstop turn cap guarantees termination.
- **Every hop is metered.** Each primary turn and each delegate reserves and commits its own budget
  through the PEP (recomputed per hop). Committed hops are never refunded by a later failure — the
  ledger shows real spend even for a request that ultimately errors. A stale-reservation reaper
  reclaims anything a crash strands (bridging multiplies that exposure).
- **Mid-bridge cap denial is graceful (two-stage).** If a delegate can't be reserved, the model gets
  a structured `budget_exhausted` tool result and composes a final answer from what it has. Only if
  the *continuation itself* can't be reserved does the whole request hard-fail `402`.
- **Delegated output is untrusted.** A model's output is untrusted input to another model, so it is
  wrapped with a non-instruction preamble and closing-tag-breakout neutralization
  (`envelope.js`) — a delegate can't hijack the primary via prompt injection.
- **Streaming never leaks intermediate turns.** A bridged request buffers (every turn runs
  non-streaming); if the client asked for `stream:true`, only the **final** answer is synthesized
  into SSE. No tool-call deltas from the internal loop reach the client.
- **Answer every tool call.** Parallel tool calls are all serviced or answered with a structured
  result, so the next continuation is well-formed; the hop cap counts **sub-calls**, not turns.
- **Delegate errors stay untrusted too.** A failed delegation returns only a status code — the raw
  upstream error body (which can echo attacker-influenced input) is dropped, never laundered into
  the primary's context as a gateway message.

### Known limitation: mixed bridge + client tool calls in one turn

If a primary turn emits **both** a bridge call and a *client-side* tool call, the gateway services
the bridge call but cannot run the client tool (it doesn't have the client). It answers that call
with a `not_executed` result instructing the model to **re-emit** the client tool call in its final
message so the client can run it. A turn containing **only** client tool calls is returned to the
client intact (standard OpenAI flow). Prefer not mixing a delegation with client tools in the same
turn.

## Arming it

```bash
# per request (recommended): header on a single call
curl ... -H 'x-l00prite-bridge: on' -d '{"model":"...","messages":[...]}'

# lower the hop cap for one request
curl ... -H 'x-l00prite-bridge: on' -H 'x-l00prite-bridge-max-hops: 1' ...

# default-on for the install (config.json) or env
"routing": { "bridge": { "enabled": true, "maxHops": 3 } }
LOOPRITE_BRIDGE_ENABLED=1  LOOPRITE_BRIDGE_MAX_HOPS=2
```

Response headers on a bridged reply: `x-l00prite-bridge-hops` (delegated sub-calls made),
`x-l00prite-cost-usd` (summed across all hops), `x-l00prite-provider` (the primary). Every hop is a
ledger row sharing the `request_id`; `l00prite route explain <id>` shows the whole tree.

> **Latency trade-off:** an armed request always **buffers** (it must see the full turn before it
> can detect a bridge call), so time-to-first-token is the whole primary turn even when the model
> ends up not delegating. Keep bridging **per-request** (the header) rather than globally
> `enabled:true` unless you accept buffered streaming for every request on the install.

`l00prite bridge status` prints the current arm state and hop cap.

## A natural use-case: the cross-provider verifier

The "maker and checker must be different models" principle (a coding-loop best practice) maps
directly onto bridging: run the generator on provider A, then have it bridge the verification step
to provider B with reject-biased instructions. Bridging turns a single-agent self-grade into a
genuine second opinion from a different vendor — the killer application for this feature.
