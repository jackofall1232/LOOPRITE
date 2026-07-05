# Concepts

The ideas behind l00prite's design choices. Short by intent — the mechanics live in the
prompts and schemas; this file is the *why*.

## Protocol vs harness

l00prite is a **protocol**, not a runtime harness. Its invariants — the pre-flight gate, the
run boundaries, the self-modification guard — are **validator-enforced prompt text** that a
compliant agent follows, not machinery that mechanically forces compliance. That distinction
is deliberate and honest: a non-compliant model can still ignore a prompt rule. A harness that
turns these invariants into guarantees is the headline roadmap item. Read every claim in
l00prite through this lens — "the loop may never raise its own limits" means *the protocol
forbids it and the validator checks the prompt says so*, not *the kernel will kill the process*.

## Files as memory

The core bet: **put project memory and the execution protocol in the repo, as files.** Files
survive context resets, are diffable, are readable by any agent or human, and depend on no one
vendor's session state. Everything else — resumability, cross-vendor handoff, the audit trail —
falls out of that one decision. When something must be remembered, it goes in `.l00prite/`, not
in the transcript.

## Intent debt

Every autonomous step widens the gap between *what you meant* and *what the loop did*. l00prite
keeps that gap payable by forcing intent to be explicit and recorded: the pre-flight display
states the goal and planned units before arming; the ledger records the decision and evidence
after. You can always reconstruct what the loop thought it was doing.

## Comprehension debt

The faster a loop ships, the faster the pile of changes **no human has read** grows. Left alone
it turns review into rubber-stamping and, eventually, into cognitive surrender ("the loop
handles it"). l00prite's answer is friction in the right places: a human-readable ledger meant
to be read, per-action permission on anything outward-facing, and a fresh confirmation every
run. The friction is not overhead — it is what keeps a long autonomous run trustworthy enough
to let it run.

## Honest telemetry

A file can honestly record a command, an exit code, a timestamp, and a wall-clock duration.
A file **cannot** honestly record how many tokens an agent spent — the agent cannot observe its
own true usage, so any such number is a guess dressed as a measurement. l00prite refuses to
build stop conditions on figures it cannot verify. This is why the iteration budget is a step
**count**, why a future budget boundary will be **wall-clock-first**, and why any token figure
is always labelled an estimate.

## The mode boundary

The single sentence everything else depends on: **Planning Mode never executes; Execution Mode
starts only behind a confirmed pre-flight, every run.** No flag, no persisted bit, no leftover
confirmation, and no headless session can substitute for that in-session human confirmation.
Guard this boundary above all — every other safety property assumes it holds.

## See also

- [failure-modes.md](./failure-modes.md) · [anti-patterns.md](./anti-patterns.md)
- Attribution: the failure/anti-pattern framing is adapted from the
  [Loop Engineering](https://github.com/cobusgreyling/loop-engineering) project.
