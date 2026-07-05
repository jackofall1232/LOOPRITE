# l00prite docs

Operating knowledge for running l00prite's Execution Mode safely. These are protocol-level
docs about *how loops fail and how to run them well* — distinct from `README.md` (what
l00prite is) and `HANDOFF.md` (what changed).

| Doc | Read it for |
|-----|-------------|
| [concepts.md](./concepts.md) | The *why*: protocol vs harness, files as memory, intent/comprehension debt, honest telemetry, the mode boundary. |
| [failure-modes.md](./failure-modes.md) | Runtime failures (Verifier Theater, State Rot, Over-Reach, Parallel Collision, …) with S1/S2/S3 severity and the l00prite guard for each. |
| [anti-patterns.md](./anti-patterns.md) | Design mistakes to avoid *before* arming a run, each with the l00prite convention that prevents it. |

## How this connects to the rest of the protocol

- **`.l00prite/failures.md`** ships with a compact, severity-tagged version of the failure
  catalog seeded in (marked as inherited generic wisdom, not project history), so a freshly
  scaffolded project warns a fresh agent about known mistakes before it repeats them.
- **`templates/l00prite/constraints.md`** carries the machine-readable **Autonomous-Edit
  Denylist** referenced throughout the failure catalog — the path guard that maps onto the
  `destructive_operation_required` boundary.
- **`scripts/l00prite-doctor.js`** is the read-only health check that mechanically detects many
  of these failure modes (state/heartbeat drift, stale arming, missing ledger evidence, absent
  denylist, prompt-mirror drift) in a scaffolded project. Run it before arming a run:

  ```bash
  node scripts/l00prite-doctor.js /path/to/your/project
  ```

## Attribution

The failure-mode and anti-pattern framing (and the S1/S2/S3 severity taxonomy) is adapted from
the [Loop Engineering](https://github.com/cobusgreyling/loop-engineering) project's
`docs/failure-modes.md`, `docs/anti-patterns.md`, and `docs/safety.md`, remapped onto
l00prite's file-based protocol and mechanisms.
