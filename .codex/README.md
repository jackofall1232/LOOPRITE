# Codex Prompts for l00prite

Copy/paste prompts for Codex and any CLI agent — no slash-command tooling required. The
six loop prompts are **byte-identical mirrors** of the canonical set at
`templates/l00prite/prompts/` (enforced by `scripts/validate-l00prite.js`; edit the
canonical file, not these).

- `prompts/build-loop.md` — Planning Mode: scaffolds a target project's blueprint,
  `AGENTS.md`, `.l00prite/` memory folder, prompt mirrors, vendor adapters, and skeleton,
  then stops. With `--execute`, offers the Execution Mode handoff — through execute-loop's
  pre-flight gate only.
- `prompts/execute-loop.md` — Execution Mode: pre-flight display, explicit in-session
  confirmation, then an autonomous run until a run boundary.
- `prompts/resume-loop.md` — one supervised implementation step from `.l00prite/` memory.
- `prompts/heartbeat.md` — should the loop continue, pause, or stop, including whether events preempt roadmap work.
- `prompts/event-loop.md` — one pending event through classify, plan, execute, verify, persist, respond.
- `prompts/respond-to-review.md` — one PR review event with a verified reviewer response.
- `prompts/handoff-summary.md` — the cross-agent handoff summary.

All agents treat `.l00prite/` as the shared source of truth and check
`.l00prite/lock.json` before mutating protected memory files.
