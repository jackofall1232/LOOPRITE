# L00prite OS — architecture (v2 design)

> Status: **v2.0 core implemented on branch `OS-APK`** (run engine, role routing, approval
> gates, Runs dashboard, packaging). This document is the design and the contract; it is
> written to stay accurate across the multi-version evolution it plans.
> Companion docs: [`architecture.md`](architecture.md) (the v1 gateway this builds on),
> [`routing-auto-mode.md`](routing-auto-mode.md), [`security-model.md`](security-model.md),
> [`../../templates/l00prite/prompts/execute-loop.md`](../../templates/l00prite/prompts/execute-loop.md)
> (the protocol the engine mechanically embodies).

## 0. What "L00prite OS" is

l00prite began as a prompt-file protocol (durable `.l00prite/` memory + a deterministic
execution protocol any vendor's agent can follow). CLI-OS added the runtime control plane:
an OpenAI-compatible gateway with provider adapters, explainable routing, real cost
metering, a policy enforcement point (PEP), and a dashboard.

**L00prite OS is the third layer: the machine that runs the protocol itself.** The user
experience it exists to serve:

1. Install L00prite OS (one binary per platform).
2. Launch it; the browser wizard configures the vault and providers.
3. Add API keys for one or more AI providers.
4. Connect a local git repository, or clone one from GitHub.
5. Enter a prompt describing what to build.
6. Press **Start**.

From Start onward, the system is an autonomous AI software-engineering runtime: it selects
providers per task, lets providers collaborate inside one execution, persists every
decision through the l00prite protocol so work survives crashes / interruptions / vendor
changes / context resets, and pauses for a human only at configurable approval gates.

Three commitments frame everything below:

- **Vendor neutrality.** No provider is primary. Providers are interchangeable modules
  described by manifests (facts) and config ranks (opinions). The engine addresses them
  only through the router; nothing in the run path names a vendor.
- **The protocol is the product.** The engine does not invent a new persistence or safety
  model — it *mechanically enforces* the one the repo already ships
  (`execute-loop.md`, `LOCKING.md`, the Autonomous-Edit Denylist in `constraints.md`).
  What was previously validator-checked prompt text becomes code a non-compliant model
  cannot ignore. This closes the roadmap item "runtime harness that mechanically enforces
  run boundaries and iteration budgets."
- **Autonomy is granted, never assumed.** A run starts only through the pre-flight gate
  with an explicit, in-session human confirmation (the Start action). Persisted flags,
  config values, and environment variables can never start a run. Gated actions (push,
  merge, deploy, credential changes, destructive operations) each require their own
  per-action human approval.

## 1. What exists underneath (the seams v2 builds on)

The v1 gateway already provides exactly the primitives an autonomous engine needs; v2 adds
no parallel infrastructure:

| Existing seam | Where | What the engine gets from it |
|---|---|---|
| `runTurn` | `internal/gateway/turn.go` | The single "one completion" primitive: route → memory-inject → PEP reserve → provider call → meter → commit → ledger. Every engine model call goes through it in-process, so budget caps, cost accounting, and the request ledger apply to autonomous work automatically. |
| Auto-router | `internal/gateway/routerauto.go` | Deterministic, explainable `(provider, model)` selection from capability filters + preference ranking. Roles resolve through profiles; no ML, every decision logged. |
| Bridge loop | `internal/gateway/bridge.go` | Bounded cross-provider delegation (`l00prite_bridge` tool) with untrusted-wrapping — in-flight collaboration between providers inside one unit of work. |
| PEP | `internal/policy/pep.go` | Dollar-denominated daily caps enforced outside the deciding process. An autonomous run cannot spend past the cap; a crashed iteration cannot leak budget. |
| Vault + tokens | `internal/security/` | Provider keys never enter the engine's model context; run APIs use the same single-tier bearer auth as every management endpoint. |
| Repos registry | `internal/gateway/repos.go` | The connect-a-repository primitive (id → absolute root, project-scoped) the engine executes runs against. |
| Memory track | `internal/memory/` + `inject.go` | Repo `.l00prite/` context selected under a latency budget and injected as delimited untrusted data into planning turns. |
| Dashboard | `public/dashboard.html` | The authenticated browser surface where pre-flight display, Start, live progress, and approvals live. |

## 2. The Run Engine (`internal/engine/`)

A **run** is one confirmed autonomous execution against one registered repo: a goal, an
objective, a team of roles, a gate configuration, an iteration budget, and a full
protocol-persisted history. Runs are the unit of resumability.

### 2.1 Run state machine

```
 draft ──preflight──▶ ready ──Start (human)──▶ running ──▶ done (definition_of_done_met)
                        │                        │  ▲
                        │                        ▼  │ approve/deny
                        └─ blocked (lock)     waiting_approval
                                                 │
                                                 ▼
                                     stopped (any other boundary; resumable)
                                     interrupted (crash; recovered at next pre-flight)
```

All transitions are persisted twice, by design:

- **Engine store (SQLite):** `runs`, `run_events` (append-only, monotonically sequenced —
  the dashboard's live feed and the machine-parseable run log), `run_approvals`. This is
  the operational, queryable record.
- **Target repo `.l00prite/`:** the protocol record other agents read — `ledger.md`
  entries in the established format, `state.json` execution fields, `heartbeat.json`
  arming/counters/telemetry, `lock.json` lease, `failures.md` on failed verification,
  `todos.md` via the model's own file tools. A different vendor's agent (or a human) can
  pick up a stopped run from files alone, exactly as the protocol promises.

If the two ever disagree (crash between writes), the repo files are authoritative for
protocol state and the next pre-flight reconciles (stale-run recovery, §2.3).

### 2.2 Protocol conformance map

The engine is `execute-loop.md` translated into Go. Every numbered requirement of the
prompt has one concrete mechanism; this table is the contract tests are written against.

| `execute-loop.md` requirement | Engine mechanism |
|---|---|
| Pre-flight 1: read all memory | Pre-flight builder reads `blueprint/ledger/memory/constraints/failures/todos/state/heartbeat/lock` + `CLAUDE.md`/`AGENTS.md` from the target repo; scaffolds `.l00prite/` memory files first if absent (§2.6). |
| Pre-flight 2: lock check first | `lock.json` read before any write; a foreign active unexpired lease → run `blocked`, nothing written. |
| Pre-flight 3: stale-run recovery | `execution_active`/`execution.enabled` with no live lease → disarm both sides under the engine's own lease and append the reclamation to `ledger.md`. |
| Pre-flight 4: schema migration | Missing `execution` block → default disarmed v2 block; missing telemetry fields → backfilled defaults; recorded in `ledger.md`. |
| Pre-flight 5: display | The `preflight` API returns the complete display: goal + Definition of Done, planned units, counters, all nine boundaries, likely-changed paths, per-action permission list, Autonomous-Edit Denylist in effect, `no_progress_threshold`, verification command allowlist, and the resolved team (role → provider/model + reason). The dashboard renders it verbatim. |
| Pre-flight 6: explicit in-session confirmation | The authenticated **Start** action on that displayed pre-flight. It is a human action inside the session, recorded as `preflight_confirmed_by` (token id) + timestamp. No persisted flag, config key, env var, or API default can substitute; the server refuses `start` without a fresh pre-flight. Headless rule holds: there is deliberately no non-interactive path to Start. |
| Pre-flight 7: arm | Engine acquires the file lease (`owner_agent: "l00prite-os"`, `owner_session: <run id>`), resets `current_iteration`/no-progress telemetry, sets the arming fields exactly as specified. |
| Iteration 1: refresh lock | Lease extended every iteration; TTL sized to the iteration timeout. |
| Iteration 2: select one unit | Planner-role turn returns one structured unit (description, target paths, verification command from the allowlist, done-check). Post-confirmation events never expand scope. |
| Iteration 3: execute only that unit | Coder-role tool-loop (§2.4), turn-capped. Every file edit is checked against the repo jail, the protocol-file hard-deny, and the Denylist *before* it happens. |
| Iteration 4: verify | The engine itself (not the model) executes the unit's verification command from the allowlist and records command, exit code, output summary, timestamp. Failures land in `failures.md`; two distinct failed fix attempts → `unfixable_failing_tests`. |
| Iteration 5: persist before anything else | Ledger entry appended, `state.json` updated, `todos.md` maintained, `current_iteration` incremented, no-progress telemetry maintained; threshold breach → stop + escalate at `human_review_gate`. |
| Iteration 6: re-check boundaries | All nine evaluated in code after every iteration (and gate decisions mid-iteration). |
| Nine run boundaries | Typed constants with concrete triggers (§2.5). |
| Mode exit | Run-summary ledger entry, disarm both sides, release lease — except `lock_lease_conflict`, which writes nothing to memory another agent holds. |
| Per-action permission | Approval gates (§2.7): the engine suspends the action, surfaces it, and proceeds only on an explicit human `allow`. A deny is recorded as a skip or boundary stop, never worked around. |
| Self-modification guard | The model's tools cannot write `heartbeat.json`, `state.json`, `lock.json`, or `.l00prite/prompts/` at all (engine-owned/protocol class — not even approval unlocks them mid-run); iteration budget and threshold are immutable for the life of a run; Denylist entries cannot be loosened by the run. |

### 2.3 Resumability and crash recovery

A run survives anything because both records are written *before* the next unit starts
(protocol iteration rule 5). Concretely:

- **Process crash / power loss:** on next boot the engine marks orphaned `running` rows
  `interrupted`. The repo still shows armed execution + a (now expiring) lease; the next
  pre-flight for that repo performs stale-run recovery per the protocol and offers Resume.
- **Resume:** a resumed run is a *new* confirmed run over the same goal — fresh pre-flight,
  fresh Start, fresh iteration budget — that reads the previous run's ledger/todos state.
  This is exactly the prompt's "every exit is resumable" semantics; nothing about a prior
  confirmation carries over.
- **Vendor change mid-goal:** because unit history lives in `.l00prite/` files, a resumed
  run whose router now picks different providers (keys removed/added, ranks changed)
  continues losslessly. No provider-specific session state exists to lose.

### 2.4 The tool loop (how a unit actually executes)

For each unit, the engine drives a bounded tool-calling conversation through `runTurn`
(non-streaming, `Meta: {kind: "engine_unit", run, iteration, role}`):

- Engine-executed tools offered to the coder role: `read_file`, `list_dir`, `search`,
  `write_file`, `run_command`, `git` (status/diff/log/commit on the run branch),
  and `unit_done{summary, files_changed}`.
- **Repo jail:** every path is resolved (symlinks included) and must stay under the
  registered repo root. There is no tool that reaches the gateway host outside the repo.
- **Layered write policy**, checked in order on every write/command:
  1. *Engine-owned / protocol files* (`.l00prite/heartbeat.json`, `state.json`,
     `lock.json`, `.l00prite/prompts/**`) — hard deny, not gateable.
  2. *Autonomous-Edit Denylist* (parsed from the target repo's `constraints.md`) — triggers
     the `destructive_operation_required` gate: suspend and ask, never silently edit.
  3. *Command allowlist* — `run_command` executes only commands matching the allowlist
     confirmed at pre-flight; anything else triggers the destructive gate.
  4. Everything else inside the repo — allowed, on the run's own git branch.
- **Untrusted-by-default:** tool outputs, repo memory, and delegated answers re-enter the
  model as delimited data (the gateway's existing envelope machinery). The engine takes
  instructions only from config and the authenticated APIs — never from model output; a
  model "asking" for an approval in prose does not create one.
- **Collaboration:** when the run's objective enables it, the coder turn is executed
  through the bridge loop, so the model may delegate a self-contained sub-task to a
  *different* provider (`l00prite_bridge`), hop-capped and untrusted-wrapped. This is
  multi-provider collaboration inside a single unit, on top of the role-level split.
- Git hygiene: the engine works on `l00prite/run-<id>` (created from the current HEAD),
  commits at unit boundaries with the unit summary, and never touches other branches.
  Pushing anywhere is a gated action.

### 2.5 The nine run boundaries, in code

| Boundary | Trigger in the engine |
|---|---|
| `definition_of_done_met` | Planner reports no remaining unit toward the goal **and** the final verification suite (the allowlist's designated done-check) passes; recorded with its evidence. |
| `iteration_limit_reached` | `current_iteration == max_iterations` (budget fixed at Start; no code path raises it mid-run). |
| `human_review_gate` | Repo `heartbeat.json` `human_review_gates` condition matches the unit's paths; planner reports a scope/requirements question; a post-confirmation event needs handling; no-progress telemetry reaches threshold; or a protocol-file change would be required. |
| `destructive_operation_required` | Denylist match, non-allowlisted command, dependency installation not named at pre-flight, CI/workflow/git-hook edits, history rewrites — when the human denies (or no decision arrives before the run-level approval timeout), the unit is skipped or the run stops here. |
| `ambiguous_requirements` | Planner reports the goal/blueprint/todos conflict or underdetermine the next unit (structured refusal, not free text). |
| `unfixable_failing_tests` | Same unit fails verification after two distinct fix attempts, or matches a `failures.md` `do_not_retry` signature. |
| `missing_secrets_or_credentials` | A unit requires a credential the engine doesn't hold; the engine never searches for or fabricates one. |
| `lock_lease_conflict` | A foreign active lease appears at refresh time → report and stop **without writing** to the protected files. |
| `stop_signal` | Stop API called, `should_continue` flipped false, `blocked: true`, or disarm detected in the repo files. |

### 2.6 Scaffolding (Planning Mode as a primitive)

Connecting a repo that has no `.l00prite/` yet must not block the OS flow, so the engine
can scaffold the *memory files* programmatically at pre-flight (blueprint seeded from the
run goal, empty ledger/todos/failures, disarmed v2 heartbeat/state, lock + `LOCKING.md`,
`constraints.md` with the standard Autonomous-Edit Denylist). Deliberately **not** copied:
the six canonical loop prompts. They are protocol files for human/agent use, mirrored
byte-identically across seven validator-enforced locations — an eighth generated copy
would either drift or expand the validator's gated surface. The engine *is* the loop; a
project that also wants the prompt files runs `/build-loop` scaffolding as before. The
scaffold notes this in the generated `.l00prite/README.md`.

### 2.7 Approval gates

Gates are the OS-level realization of the protocol's per-action permission rule.

- Gate classes: `push`, `merge`, `deploy`, `credential_change`, `destructive`
  (denylist/command/dependency/history), `outside_repo`. Each is `require_approval`
  (default) or `deny` — **never** `auto_allow`; configured per run at creation, shown at
  pre-flight.
- When a gated action arises, the engine records an approval row (class, exact action,
  arguments, requesting unit), emits a run event, transitions to `waiting_approval`, and
  suspends only that action. The dashboard approvals inbox renders it with full context.
- An `allow` is scoped to that single action instance (per-action permission, not a
  blanket). A `deny` is recorded and the engine either skips the unit or stops at the
  matching boundary. No decision before the approval timeout → treated as deny-and-stop,
  because an unattended run must fail safe, not wait forever holding a lease.
- Approvals are audit-logged with the acting token id, like every management mutation.

### 2.8 Objectives and team assembly (role routing)

The user thinks in outcomes; the engine assembles the team. Deterministic and explainable
today, by the same rule as all v1 routing (no ML, every decision logged — Open Question Q2
stands); *learned* assembly is a roadmap item gated on keeping decisions inspectable.

- **Roles:** `plan` (select the next unit, detect ambiguity), `code` (the tool-loop),
  `review` (pre-persist diff review on quality-sensitive objectives), `summarize`
  (ledger/handoff prose). Each maps to an auto-routing profile.
- **Facts vs opinions, unchanged:** boolean capabilities (tools, vision, streaming usage)
  and context/price stay in manifests; *aptitude* is an operator opinion — config
  `routing.roleRanks` (`plan`/`code`/`review`/`security` maps, falling back to
  `qualityRanks`) with shipped defaults, and role profiles resolve through the existing
  capability-filter → preference pipeline (`code` requires `tools`, ranked by its map;
  `summarize` prefers cost).
- **Objectives** are named presets resolving role → profile (+ options):
  `balanced` (default), `quality` (quality ranks everywhere + review step on),
  `cost` (cost preference, review off, bridge off), `speed` (fewest model turns:
  review off, cheap-fast ranks — no fabricated latency numbers; a measured latency
  preference stays v2+), `privacy` (candidates restricted to
  `routing.privacyProviders` — e.g. local/self-hosted; empty list → a pre-flight error
  saying exactly why, never a silent fallback to a cloud provider).
- The resolved team (role → provider/model + the reason line from the router) is part of
  the pre-flight display, so the human confirms *who* will do the work, not just what.

## 3. API surface (same auth, same flat-action conventions)

All endpoints require the gateway bearer token; run/repo project scoping matches the
token, exactly like `/v1/repos`.

| Route | Purpose |
|---|---|
| `POST /v1/runs` | Create a draft run `{repo, goal, objective?, gates?, command_allowlist?, max_iterations?}` and return its first pre-flight. |
| `GET /v1/runs/list` | Runs for the token's project (paged). |
| `GET /v1/runs/get?id=` | Run detail + latest pre-flight/summary. |
| `POST /v1/runs/preflight` | Rebuild the pre-flight (also performs recovery/migration/lock check). Required again before any Start. |
| `POST /v1/runs/start` | The confirmation gate: `{id, confirm: "EXECUTE"}` against a *fresh* pre-flight arms and launches the run. |
| `GET /v1/runs/events?id=&after=` | Append-only event feed (the dashboard polls this). |
| `POST /v1/runs/approve` | `{id, approval_id, decision: allow\|deny, note?}` — per-action permission. |
| `POST /v1/runs/stop` | `stop_signal` boundary. |
| `POST /v1/repos/clone` | `{id, url}` — clone a Git URL into the managed workspace dir and register it (the "clone from GitHub" path; explicit human action from the dashboard). |

## 4. Dashboard

A new **Runs** area follows the existing single-file conventions (semantic CSS variables
for both color schemes, `fetchJSON` + poll refresh, modal patterns):

1. **Create** — repo picker (register/clone inline), goal prompt, objective picker with
   live team preview, gates + command allowlist editor, iteration budget.
2. **Pre-flight** — the §2.2 display rendered in full, with a single primary **Start**
   action (and nothing armed until it is used).
3. **Live run** — status header (state, iteration n/max, current unit, cost so far from
   the metered ledger), the event stream, and the approvals inbox with allow/deny.
4. **Exit** — boundary banner (which boundary, why, next recommended action), run summary,
   and Resume (which routes back through pre-flight, never around it).

## 5. Packaging (the installable OS)

The Go port made this cheap: pure-Go SQLite (`modernc.org/sqlite`), embedded dashboard —
one static binary per platform, no runtime dependencies.

- `scripts/dist.sh` cross-compiles the release matrix with `CGO_ENABLED=0`:
  `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, with
  checksums, version stamped via `-ldflags -X`.
- Installers: `install/install.sh` (Linux/macOS: platform detection → binary → PATH) and
  `install/install.ps1` (Windows: `%LOCALAPPDATA%\l00prite\bin` + user PATH), both landing
  the user on "open the dashboard in a browser" — the wizard does the rest. Docker remains
  for server installs.
- Security posture is unchanged by packaging: loopback bind by default, no non-loopback
  bind without TLS, vault initialized by the wizard.

## 6. What v2.0 deliberately does not do (and where it goes next)

| Deferred | Why / where it lands |
|---|---|
| Learned routing & team assembly ("l00prite learns the best team") | Needs an evidence base first. The run ledger + `run_events` accumulate per-role outcome data now; a learned ranker lands only behind the explainability rule (every pick still explained, operator-overridable). |
| Measured latency preference | No fake static numbers; EWMA measurement is v2.1 (the meter already sees every call). |
| GitHub event ingestion (PR comments / CI failures → run events) | Protocol layer already models events; the OS ingests them into pending events a *newly confirmed* run handles. |
| Independent verifier role as a separate invocation | The engine's verify step is mechanical (it runs commands itself); a genuinely independent verifier *model* joins once the v1.2 gated batch lands its prompt. |
| Multi-run scheduling / parallel runs per repo | One active run per repo enforced via the lease today; a scheduler queues behind it later. |
| Runtime-loaded provider manifests (drop a JSON into the data dir to add a provider) | The plugin path for providers-as-modules; today manifests are compiled in, adapters cover openai-compat + native-messages + mock, which spans every current major vendor surface. |
| `/v1/responses`, native passthrough, richer multimodal | Unchanged from v1 scope decisions. |

## 7. Invariants (the list reviewers should hold this branch to)

1. No run starts without a fresh pre-flight and an explicit authenticated Start in the
   same session; nothing persisted can arm a run.
2. The engine never raises its own limits mid-run; the model can never write
   engine-owned/protocol files; approvals are per-action and audit-logged.
3. Every model call goes through `runTurn` — budget, metering, and ledgering apply to
   autonomous work with no bypass path.
4. Repo jail holds: no tool reaches outside the registered root; `lock_lease_conflict`
   writes nothing.
5. Vendor neutrality holds: the engine addresses providers only through the router;
   removing any one provider leaves the OS fully functional.
6. The two review-gated files (`.claude/commands/build-loop.md`,
   `scripts/validate-l00prite.js`) are untouched by this branch, and
   `node scripts/validate-l00prite.js` stays at zero FAIL.
