# Handoff Summary Prompt

Prepare a cross-agent handoff for a l00prite-managed project.

Read `.l00prite/blueprint.md`, `.l00prite/ledger.md`, `.l00prite/memory.md`, `.l00prite/constraints.md`, `.l00prite/failures.md`, `.l00prite/todos.md`, `.l00prite/state.json`, and `.l00prite/heartbeat.json`.

Write or update `HANDOFF.md` with:

- Current mission and architecture summary
- Current status and active goal
- Recently completed work
- Known constraints and decisions
- Failed approaches / do-not-retry notes
- Verification status
- Blockers or human review gates
- Execution mode status (`heartbeat.json` `execution.enabled`, `execution.current_iteration` / `execution.max_iterations`, and the last run boundary, if the project has an `execution` block)
- Next smallest useful step
- Which `.l00prite/` files were updated

Do not implement project features while producing a handoff.
