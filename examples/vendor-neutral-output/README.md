# Vendor-Neutral Example Output

This directory shows example files that l00prite generates into a target project during
Planning Mode. It is documentation only, not active repo state.

A generated project includes:

- `CLAUDE.md` — the project blueprint for Claude Code, carrying the fixed l00prite
  protocol section.
- `AGENTS.md` — the vendor-neutral operating guide, read natively by OpenAI Codex, Cursor,
  GitHub Copilot, Windsurf, Zed, Jules, Factory, Amp, opencode, Devin, and others.
- `.l00prite/` — shared memory, including `.l00prite/prompts/` with the canonical loop
  prompts (resume, heartbeat, event, review, handoff, execute) any agent can use.
- Vendor adapters for tools that need their own file: `GEMINI.md` (Gemini CLI), `QWEN.md`
  (Qwen Code), `CONVENTIONS.md` (Aider), `.github/copilot-instructions.md` (Copilot),
  `.cursor/rules/l00prite.mdc` (Cursor), `.windsurf/rules/l00prite.md` (Windsurf).

A real scaffold also writes two things this example intentionally omits: the
`.claude/prompts/` and `.codex/prompts/` directories (they would duplicate
`.l00prite/prompts/` byte-for-byte — parity between them is enforced by l00prite's
validator at scaffold-template level), and the tier skeleton (`src/`, `tests/`, …), which
is tier- and stack-dependent, so no single copy would be representative. The `.l00prite/`
memory files here are filled in for a small fictional project (`example-project`, an RSS
daily-digest CLI) so you can see what a freshly planned project looks like.

Any of these agents — plus GPT, Gemini, and future ones — can hand off to each other
through the same project intelligence layer.
