# Node → Go port notes

The `cli-os/` runtime was rewritten from Node.js to a single statically-compiled **Go** binary. This
is a **language port of an already-decided design** — the two-track Gateway/Memory separation, the
Policy Enforcement Point, atomic reserve/commit counters, the provider-adapter interface, and the
untrusted-content envelope are all preserved. Q1–Q6 in `open-questions.md` were **not** revisited.

Everything worth signing off on is called out below. TL;DR of the two flagged items:

- **SQLite driver → `modernc.org/sqlite` (pure Go, no cgo).** This is what makes the "single static
  binary" goal real. **Needs your ack** (it's the load-bearing dependency choice).
- **Static linking → confirmed true static.** `CGO_ENABLED=0 go build` yields a binary `ldd` reports
  as "not a dynamic executable". No shared-library-at-runtime surprise.

## Environment verification (done before porting, per the brief)

| Check | Result |
|---|---|
| `go version` | go1.24.7 linux/amd64 ✅ |
| `go get` a module (live fetch) | ✅ works (`proxy.golang.org` is allow-listed) |
| Live HTTPS to a provider docs domain | **partial** — Anthropic (`platform.claude.com`) reachable; OpenAI / z.ai egress-blocked (403). Go module fetch is what gates the port, and it works, so the port proceeded. |

The brief's stop-condition ("if this environment is *also* egress-restricted for Go module fetches,
stop and report") did **not** trigger — modules fetch fine.

## SQLite driver choice (needs sign-off)

`node:sqlite` → **`modernc.org/sqlite` v1.38.0** (a CGo-free, pure-Go SQLite).

Why this one, explicitly, given the "single static binary" goal:

- A **cgo** SQLite driver (e.g. `mattn/go-sqlite3`) links libsqlite3/libc and **breaks static
  linking** unless you fight the toolchain (musl, `-extldflags -static`), which reintroduces exactly
  the fragile install story Go was chosen to avoid.
- `modernc.org/sqlite` compiles SQLite to Go, so `CGO_ENABLED=0 go build` produces a **fully static**
  binary — verified:

  ```
  $ CGO_ENABLED=0 go build -o l00prite ./cmd/l00prite
  $ ldd l00prite
      not a dynamic executable
  $ file l00prite
      ELF 64-bit LSB executable, x86-64, statically linked, stripped
  ```

- It supports **real ACID + WAL + `BEGIN IMMEDIATE`**, which the PEP's atomic reservation counters
  require. The pep/e2e tests exercise reserve→commit/refund, stale-reservation reaping, and the
  cross-hop budget invariant, all against this driver.
- v1.38.0 is pinned because it declares `go 1.23`, so it builds on the local Go 1.24 toolchain
  **without** triggering an automatic newer-toolchain download at build time. (Latest `v1.53.0`
  wants go 1.25.)

**If you'd rather use a cgo driver** for raw performance, say so and I'll switch — but then the
binary is no longer a single static file and the Docker/runtime story changes. My recommendation is
to keep the pure-Go driver.

## Concurrency model (flagged — a Go idiom that changes behavior)

Node is single-threaded; `net/http` serves requests **concurrently** on goroutines. Two deliberate
adaptations:

1. **DB is capped at one open connection** (`db.SetMaxOpenConns(1)` in `internal/state/db.go`). This
   preserves `node:sqlite`'s single-connection, non-interleaving semantics that the PEP's
   reserve/commit atomicity relies on, and sidesteps intra-process `SQLITE_BUSY`. Transactions use
   `BEGIN IMMEDIATE` on a **pinned `*sql.Conn`** so a reservation takes the write lock up front. This
   is correct and safe for the single-node, single-operator v1 target; raising the connection cap for
   throughput is a v2 perf task (needs busy-timeout-aware retry) — not a v1 change.
2. **The circuit breaker is a `map` guarded by a `sync.Mutex`** (Node used a bare `Map`, safe only
   because it was single-threaded).

Neither changes observable behavior; both are noted because they are places where "just port it"
would have been subtly wrong under Go's concurrency.

## Security-sensitive translations (1:1, verified)

- **Vault (`security/vault.go`)** — `crypto/aes` + `crypto/cipher` GCM, same algorithm as the Node
  `aes-256-gcm`. The on-disk format is **byte-compatible**: `v1.<iv b64>.<tag b64>.<ct b64>`,
  standard base64, 12-byte IV, 16-byte GCM tag (Go's `Seal` appends the tag; it's split back out to
  match the Node layout). **Existing vault files written by the Node version decrypt unchanged**, so
  no migration is needed.
- **Tokens (`security/tokens.go`)** — opaque `l00p_<id>_<secret>`, only the sha-256 of the secret is
  stored, and the compare is **constant-time** via `crypto/subtle.ConstantTimeCompare`
  (`util.TimingSafeEqual`). No `==` on secrets anywhere.
- **Untrusted-content envelope (`gateway/envelope.go`)** — preserved exactly, including complete
  closing-tag neutralization and full attribute-value entity-escaping. The breakout/attr-escape tests
  are ported and pass. Repo memory and bridged output are still wrapped before injection.
- **Least-privilege repo reads (`memory/memory.go`)** — symlink-resolving containment via
  `filepath.EvalSymlinks` (fail-closed), same discipline as the Node `realpathSync` check; the
  symlink-escape test is ported and passes.
- **PEP stays outside the request handler** — `policy` package; a handler *requests* a reservation
  and cannot grant its own. Persisted flags are never authorization.

## SSE streaming (net/http + a proper SSE writer)

- Non-streaming and streaming paths are separate, as in Node. Streaming connects (with retry) **before**
  writing the `200`, so a pre-first-byte failure is a proper error + refund, not a silent empty stream.
- The stream translators (`anthropic.NewStream(...)` returning a `StreamTranslator`, `openaicompat`)
  fold provider SSE events into OpenAI `chat.completion.chunk` frames; the buffer is CRLF-normalized
  and framed on `\n\n`, matching `upstream.js`. The direct (mock) stream path and the bridge
  `synthesizeStream` path are covered by e2e tests (SSE deltas, `[DONE]`, include_usage chunk,
  no-intermediate-leak).
- `Flush()` is called after each SSE write so clients see incremental output.

## Cost-meter hardening (the pricing brief)

Beyond a straight port, the meter now returns an explicit `Unconfirmed` flag distinct from
`Estimated`:

- unpriced model → `{USD:0, Priced:false, Estimated:true, Unconfirmed:true}` — surfaced as
  `x-l00prite-cost-unconfirmed: true` on the response and a `cost_unconfirmed` column in the ledger.
  A `null` price is treated as "unbilled, not free", never a silent `$0`.
- The auto-router's `cost` preference already excluded unpriced models (`no_priced_model`); preserved.

## What could NOT be ported 1:1 (and why it doesn't matter)

- **`bin/cli.js`'s re-exec to suppress a `node:sqlite` ExperimentalWarning** — no Go equivalent
  needed; the pure-Go driver emits no such warning.
- **Mock-usage token counts differ by a few tokens** — both Node and Go derive mock "usage" from
  `len(JSON.stringify(messages))`, and Go's `encoding/json` produces a slightly different byte length
  (key ordering/spacing) than V8's `JSON.stringify`. This only affects the *demo* mock provider's
  fabricated usage; every cap/routing test that depends on it is robust to the difference (the
  ceilings differ by orders of magnitude from the caps), and it never touches a real provider's
  reported usage. Verified: all ported cap-math tests pass.
- **Attribute ordering in the untrusted envelope** — Node preserved JS object insertion order; Go
  maps are unordered, so envelope attributes are passed as an **ordered slice** (`[]Attr`) to keep
  output deterministic. Same bytes, deterministically.

## Test parity (run and shown, not claimed)

Every `cli-os/test/*.test.js` behavior has a passing Go equivalent. The Node suite was 50 tests; the
Go suite is **67 leaf tests** — the 50 ported behaviors + a synthetic price-tier-ordering test (GLM,
the shipped tier-1 exemplar, is now `null`) + a vault Node-blob cross-compat test + network SSE
translator tests + regression tests for every post-review fix above (mid-stream fail-closed, DB
migration, config deep-merge/overrides, non-finite hops, Unicode-whitespace envelope, base64url key,
never-expires token).

```
$ go test ./...            # 67 leaf tests, 0 failures
ok   .../internal/config
ok   .../internal/gateway
ok   .../internal/gateway/adapters
ok   .../internal/memory
ok   .../internal/policy
ok   .../internal/security
ok   .../internal/server
ok   .../internal/state
$ go test -race ./...      # clean
$ go vet ./...             # clean
$ gofmt -l .               # clean
```

Mapping: `unit.test.js` (11) → security/policy/config/memory/gateway/adapters unit tests;
`e2e.test.js` (7) → `server.TestServerE2E` subtests; `routing-auto.test.js` (17) →
`gateway` routing tests; `bridge.test.js` (4 unit + 11 e2e) → `gateway` envelope/bridge-helper tests
+ `server.TestBridgeE2E` subtests.

## Post-review hardening (from adversarial review + automated PR reviewers)

A 6-way adversarial parity review (each Go subsystem diffed against its Node original) plus the PR's
automated reviewers (Copilot, Gemini, Codex) surfaced divergences; the substantive ones were fixed:

- **Streaming fails closed** — a mid-stream provider drop / timeout / client-disconnect (a non-`io.EOF`
  read error) now returns an error so the caller commits the reservation **ceiling** and logs
  `error_midstream`, instead of committing ~$0 and marking the provider healthy (which leaked budget
  past the cap). Stream usage falls back to the accumulator (`StreamTranslator.Usage()`) if the
  terminal event never arrives. (test: `TestStreamMidDropFailsClosed`)
- **DB migration** — `state.Open` now runs a best-effort idempotent v1→v2 migration
  (`ALTER TABLE ledger ADD COLUMN cost_unconfirmed`), so a data dir created by the Node runtime is not
  orphaned (ledger inserts would otherwise silently fail). (test: `TestMigrationAddsCostUnconfirmed`)
- **Config `bridge` deep-merge** — `{"routing":{"bridge":{"enabled":true}}}` keeps `maxHops:3` instead
  of zeroing it. **`config.json` runtime overrides restored** — `retry`/`memory`/`requestTimeoutMs`/
  `tls` are honored again (they were being dropped). String-typed numeric settings (`"20"`) parse.
  (tests: `TestBridgeConfigDeepMerge`, `TestRuntimeOverridesHonored`, `TestStringTypedNumericConfig`)
- **PEP fails closed on DB errors** — a spend-read failure now denies rather than proceeding on
  unknown spend (was a fail-open cap-bypass vector).
- **Envelope Unicode whitespace** — the closing-tag breakout guard now matches the same whitespace set
  JS `\s` does (VT, NBSP, ideographic space, ZWNBSP, LS/PS); Go's RE2 `\s` is ASCII-only.
  (test: `TestEnvelopeNeutralizesUnicodeWhitespaceCloser`)
- **JS-truthiness parity** — `forward_tools` / `include_usage` / anthropic `stop` & `tool_choice` use a
  `jsTruthy` helper (so `"true"`/`1`/`""` behave like Node), not strict `== true`.
- **`bridgeMaxHops` rejects non-finite headers** (`Infinity`/`NaN`); whitespace bridge `target` routes
  like Node; **bridge responses carry `x-l00prite-cost-unconfirmed`** when any hop is unpriced.
- **Rune-safe truncation** (mock reply, `userIntent`, memory budget cut) and **rune-based token
  estimates** (memory, reservation ceiling, routing) so non-ASCII content isn't over-counted or split
  into invalid UTF-8. HTML escaping is disabled in the estimate JSON so `<>&` match JS `JSON.stringify`.
- **`cap list` no longer deadlocks** — cap rows are drained before `GetSpend` (single-connection pool).
- **Master key** accepts base64url / unpadded forms (Node's `Buffer.from` is lenient).
- **`token mint --expires 0`** means never-expires (Node falsy), not expired-at-creation.

Deliberately **kept as Go-is-better / benign** (documented, not "fixed"): a `null` request body → 400
(Node crashes to 500); `arguments:"null"` → graceful delegate error (Node throws); a garbage token
expiry → deny (Node grants). JSON **object key ordering** is alphabetical (Go `map` + `json.Marshal`)
vs insertion-order (JS) — semantically identical, no correct consumer depends on it; this is inherent
to the `map[string]any` passthrough design. Empty-string env vars (e.g. `LOOPRITE_DEFAULT_DAILY_CAP=`)
are treated as unset rather than coerced to 0/false — Node's `Number("")===0` there is a footgun.

## Open items / your call

- **Ack the `modernc.org/sqlite` dependency** (or ask for cgo + the non-static tradeoff).
- OpenAI / GLM pricing remain `null` (egress-blocked) — see `docs/pricing-confirmation.md`. When you
  have a networked environment that can reach their pricing pages, fill the manifests and the meter
  starts billing them.
- The `SecretStore` remains the env-var / local-file vault by default (cloud KMS is an optional
  future implementation behind the same interface); nothing is hardcoded to a specific cloud.
