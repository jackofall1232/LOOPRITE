# Anti-Patterns

Design mistakes to avoid **before** you arm an Execution Mode run. For runtime incidents, see
[failure-modes.md](./failure-modes.md). Each anti-pattern lists why it fails and the l00prite
guard or convention that prevents it.

This list adapts the anti-pattern catalogue from the
[Loop Engineering](https://github.com/cobusgreyling/loop-engineering) project to l00prite's
file-based protocol, and adds several l00prite-specific ones drawn from its own rejected
designs (recorded in `.l00prite/failures.md`).

## 1. Same agent implements and verifies, with no recorded evidence

**Anti-pattern**: one session marks its own work "done" after glancing at output.

**Why it fails**: confirmation bias; weak checks get rubber-stamped (Verifier Theater).

**Do instead**: record real evidence in the ledger — `command`, `exit_code`, `timestamp`,
`evidence_path`. Never claim success for a check that failed or did not run. Treat a
self-approval without evidence as unverified. (A genuinely independent verifier is a roadmap
item that needs the runtime harness first — narrating "I reviewed it" in the same session is
not independence.)

## 2. No attempt cap

**Anti-pattern**: "keep retrying the unit until it passes."

**Why it fails**: infinite fix loops, wasted budget, wrong fixes committed.

**Do instead**: rely on the `unfixable_failing_tests` boundary (two distinct fix attempts →
stop), record each attempt in `failures.md` with `do_not_retry` when warranted, and watch
`execution.iterations_since_progress` for the broader stall.

## 3. Arming a not-yet-trusted loop with wide scope

**Anti-pattern**: confirm Execution Mode on day one and let it touch anything.

**Why it fails**: the loop acts on unproven judgment; the blast radius is maximal from the
first run (the "L3 before L1" mistake).

**Do instead**: keep the first runs small — few `max_iterations`, a tight `todos.md`, a
populated Autonomous-Edit Denylist. Read the ledger between runs before widening scope. (A
formal report-only autonomy level is a roadmap item; until then, scope discipline lives in the
pre-flight you confirm.)

## 4. Writing shared memory without the lock

**Anti-pattern**: two agents edit `.l00prite/` close together without checking `lock.json`.

**Why it fails**: silent memory corruption — a clobbered ledger, a lost event.

**Do instead**: follow `LOCKING.md` — check before writing, acquire if free, respect an active
foreign lock, reclaim-and-log a stale one, release before stopping. Remember it is cooperative,
not filesystem-enforced.

## 5. Treating a persisted flag as authorization

**Anti-pattern**: reading `preflight_confirmed: true` or `execution.enabled: true` from
`heartbeat.json` and starting a run on that basis.

**Why it fails**: those fields live in agent-writable memory — they are forgeable and
transferable across sessions. They are an **audit record of a past run**, never an
authorization for this one.

**Do instead**: re-run the pre-flight and get a fresh in-session confirmation **every run**.
Headless sessions cannot satisfy the gate and must not enter Execution Mode.

## 6. Pre-arming the repo at scaffold time

**Anti-pattern**: `--execute` (or any scaffold step) writing `execution.enabled: true` so the
repo ships armed.

**Why it fails**: the confirmation would predate the code it covers, and the repo would sit
armed on disk for any later agent to discover. (Rejected design — see `.l00prite/failures.md`.)

**Do instead**: `--execute` only *offers* the handoff after scaffolding; it never pre-arms and
never skips the gate. Planning Mode always ships `execution.enabled: false`.

## 7. Letting the loop raise its own limits or loosen the denylist

**Anti-pattern**: the run edits `max_iterations`, `run_boundaries`, `human_review_gates`, or
removes an Autonomous-Edit Denylist entry to get past a stop.

**Why it fails**: a loop that can widen its own authority has no real boundary at all.

**Do instead**: the self-modification guard forbids it. Needing such a change **is** the
`human_review_gate` boundary — stop and ask.

## 8. No kill switch

**Anti-pattern**: a run with no way to say stop.

**Why it fails**: it keeps going through noise, budget overrun, or a bad patch.

**Do instead**: `heartbeat.json.should_continue: false`, `state.json.blocked: true`, or setting
`execution.enabled: false` each trip the `stop_signal` boundary; a human saying stop always
works. Every stop is resumable and records why.

## 9. Merging or pushing without per-action permission

**Anti-pattern**: "verification passed, so merge it."

**Why it fails**: weak verifiers pass security and business-logic bugs; the pre-flight
confirmation was never a blanket grant.

**Do instead**: push/merge/deploy/publish/credential changes each need separate, explicit
human permission — even mid-run. Use the Autonomous-Edit Denylist and an auto-merge allowlist
(default: none) in `constraints.md` to make the safe set explicit.

## 10. Fixing flakes with code

**Anti-pattern**: changing application code when a test is actually flaky or the failure is
environmental.

**Why it fails**: masks infra problems and injects random diffs.

**Do instead**: classify honestly, record the flake in `failures.md` with `do_not_retry`, and
escalate env/infra failures through `human_review_gate` rather than editing product code.

## 11. Budgeting by self-reported tokens

**Anti-pattern**: a `tokens_used` field the loop increments to enforce a spend cap.

**Why it fails**: an agent cannot observe its own true token usage — the number is fiction, and
a stop built on fiction is Verifier Theater in budget form.

**Do instead**: bound the run by `max_iterations` today; set a hard spend cap with your model
provider. When a budget boundary lands (roadmap), it will be **wall-clock-first** — timestamps
are the only cost axis a file can honestly check — and any token figure will be labelled an
estimate.

## 12. Relying only on the transcript

**Anti-pattern**: keeping "what we tried" in the chat session instead of `.l00prite/`.

**Why it fails**: sessions end and context resets; the next agent (or vendor) starts blind and
repeats dead approaches.

**Do instead**: persist decisions to `memory.md`, failures to `failures.md`, and run history to
`ledger.md` **before** stopping. The whole point of l00prite is that the repo, not the session,
remembers.

## See also

- [failure-modes.md](./failure-modes.md) — the runtime failures these anti-patterns cause.
- [concepts.md](./concepts.md) — the debts and distinctions behind them.
- `.l00prite/failures.md` — l00prite's own rejected designs, kept as do-not-retry history.
