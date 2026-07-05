---
description: Enter Execution Mode for the current l00prite-managed project — autonomous run behind a pre-flight confirmation gate. Requires explicit in-session confirmation before any iteration.
---

You are running the `/execute-loop` command from the l00prite project. This command enters
**Execution Mode** for the l00prite-managed project in the current working directory.

Read `.l00prite/prompts/execute-loop.md` in the current repo and follow it exactly — the
pre-flight gate, the iteration protocol, the nine run boundaries, and the hard rules are
all defined there. If the current repo has no `.l00prite/prompts/execute-loop.md`, follow
`.claude/prompts/execute-loop.md` from this repo instead (they are byte-identical mirrors).

Optional context from the user for this run (goal, scope limits, or constraints to include
in the pre-flight display):

$ARGUMENTS

Non-negotiable, regardless of arguments:

- Do not begin any iteration before the pre-flight summary has been displayed **and** the
  human has explicitly confirmed in this session. A `preflight_confirmed: true` or
  `execution.enabled: true` already sitting in `heartbeat.json` does not satisfy the gate —
  re-confirm every run.
- If `.l00prite/` does not exist here, this project isn't l00prite-managed yet: stop and
  point the user at `/build-loop` (Planning Mode) instead of improvising memory files.
- Respect every run boundary in the prompt, including the special no-write rule for
  `lock_lease_conflict`.
