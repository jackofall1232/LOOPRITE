# Claude Prompts for l00prite

Claude Code entry points for l00prite and for any l00prite-managed project.

Commands (`commands/`):

- `build-loop.md` — `/build-loop`: Planning Mode. Scaffolds a target project's `CLAUDE.md`,
  `AGENTS.md`, `.l00prite/` memory (including the canonical prompts), prompt mirrors,
  vendor adapters, and a tiered skeleton, then stops. With `--execute`, offers the
  Execution Mode handoff — through execute-loop's pre-flight gate only.
- `execute-loop.md` — `/execute-loop`: Execution Mode for the current l00prite-managed
  project. Pre-flight display, explicit in-session confirmation, then an autonomous run
  until a run boundary.

Prompts (`prompts/`) — **byte-identical mirrors** of the canonical set at
`templates/l00prite/prompts/` (enforced by `scripts/validate-l00prite.js`; edit the
canonical file, not these):

- `prompts/resume-loop.md` — one supervised implementation step from `.l00prite/` memory.
- `prompts/heartbeat.md` — should the loop continue, pause, or stop, including whether events preempt roadmap work.
- `prompts/event-loop.md` — one pending event through classify, plan, execute, verify, persist, respond.
- `prompts/respond-to-review.md` — one PR review event with a verified reviewer response.
- `prompts/handoff-summary.md` — the cross-agent handoff summary.
- `prompts/execute-loop.md` — the Execution Mode autonomous run (pre-flight gate, nine run boundaries).

All agents treat `.l00prite/` as the shared source of truth and check
`.l00prite/lock.json` before mutating protected memory files.
