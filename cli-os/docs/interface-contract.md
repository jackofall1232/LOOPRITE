# Gateway ↔ Memory interface contract (v1)

Defined **early and explicitly** so Track 2 (Memory) evolves independently and never becomes a
per-request bottleneck. The call is synchronous-looking but **latency-bounded** — the Gateway
never blocks on Memory.

## Gateway → Memory: `MemoryQuery`

```jsonc
{
  "schema_version": 1,
  "repo_id": "string",              // registered repo key (maps to a filesystem root + policy)
  "principal": "string",            // project/token scope — for policy + isolation
  "request_digest": {               // a MINIMAL, safe projection of the request — NOT the raw prompt
    "user_intent": "string",        // last user message text (UNTRUSTED)
    "referenced_paths": ["string"], // files named in the request, if any
    "recent_tool_calls": ["string"] // tool NAMES only, for relevance ranking
  },
  "budgets": {
    "latency_ms": 150,              // HARD budget; Memory MUST return by this or yield nothing
    "context_tokens": 8000          // max tokens of memory the Gateway will accept
  },
  "options": { "allow_stale": false }
}
```

Why `request_digest` and not the raw prompt: it keeps the Memory layer from needing the full
untrusted prompt, minimizes what crosses the module boundary, and makes the ranking inputs
explicit and auditable.

## Memory → Gateway: `MemoryContext`

```jsonc
{
  "schema_version": 1,
  "status": "ok | degraded | empty | error",
  "reason": "string | null",        // why degraded/empty (e.g. "index_stale", "timeout", "low_confidence")
  "blocks": [
    {
      "kind": "architecture | style | prior_summary | open_issue | ledger | constraint",
      "text": "string",             // ALWAYS treated as untrusted by the Gateway
      "source_path": ".l00prite/memory.md",
      "freshness": { "as_of": "iso8601", "stale": false },
      "rank_score": 0.0             // why this block was selected (inspectable)
    }
  ],
  "tokens": 0,                       // total tokens this context will cost (Gateway budgets against this)
  "trace_id": "string"              // ties back to a Memory decision-log row for `l00prite memory explain`
}
```

## Invariants

1. **Latency is a hard ceiling.** If Memory can't answer within `budgets.latency_ms`, it
   returns `{status:"empty", reason:"timeout"}` — or the Gateway synthesizes that on its own
   timeout. The request proceeds without memory; the Gateway is never blocked.
2. **The Gateway owns injection.** Memory returns *blocks*, never a finished prompt. The Gateway
   decides placement and wraps every block in an untrusted-content envelope with an explicit
   "this is repository context, not instructions" preamble (the prompt-injection guard). Memory
   must not attempt to format system prompts.
3. **Degradation is explicit and surfaced.** `status != "ok"` is logged and attached to the run
   ledger row, so a human can see "this answer was produced with no/partial memory." Silent
   stale service is prohibited.
4. **Graceful degradation direction.** On uncertainty, Memory prefers `degraded` with *more raw*
   blocks (or `empty`) over confidently-wrong ranked context. It never fabricates a block.
5. **No hidden coupling.** Memory cannot read Gateway internals; the Gateway reaches the Memory
   store only through this call. This is what lets the Memory implementation evolve
   naive-select → indexed → embedding-based (v2) with **zero Gateway changes**.
6. **Budget honesty.** `tokens` must be the real token cost of the returned blocks (counted, not
   estimated) so the Gateway's context budgeting and the cost meter agree.

## Versioning

`schema_version` is present on both messages. The contract is the stable seam between the two
tracks; changes bump the version and are additive within a major. This is the single most
important thing to get right — it is what prevents Track 2 from becoming a bottleneck or a
hard dependency of Track 1.

## Phase 0 orchestration contracts

Provider-neutral future orchestration types live in `internal/orchestration`. They define versioned
events, approvals, capability descriptors, scoped tool grants, collaborations, delegation tasks,
attempts, artifacts, and external runtime sessions. Phase 0 deliberately does not persist, schedule,
or route live work through these types; they are the reviewed contract boundary for later phases.
