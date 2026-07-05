# Vendor Adapters

These files onboard specific AI coding agents into a l00prite project when the
vendor-neutral `AGENTS.md` standard alone doesn't reach them. Planning Mode (`build-loop`)
copies each adapter to its target location, with the usual no-silent-overwrite rule.
`templates/vendors.json` is the machine-readable manifest of this mapping, consumed by
`scripts/validate-l00prite.js`.

## Mapping

| Template file | Target location | Tool | Why an adapter exists |
|---------------|-----------------|------|-----------------------|
| `GEMINI.md` | `GEMINI.md` (repo root) | Google Gemini CLI | Default context file is `GEMINI.md`, not `AGENTS.md`; the adapter also `@AGENTS.md`-imports the full rules. |
| `QWEN.md` | `QWEN.md` (repo root) | Qwen Code | Same situation as Gemini CLI (it reads `QWEN.md` by default). |
| `copilot-instructions.md` | `.github/copilot-instructions.md` | GitHub Copilot (all surfaces) | Some Copilot surfaces read only this file and cannot open others on instruction. |
| `l00prite.mdc` | `.cursor/rules/l00prite.mdc` | Cursor | `alwaysApply: true` guarantees the rules are injected even where AGENTS.md support is disabled. |
| `windsurf-l00prite.md` | `.windsurf/rules/l00prite.md` | Windsurf / Devin Desktop | `trigger: always_on` rule; guaranteed inclusion alongside its native AGENTS.md support. |
| `CONVENTIONS.md` | `CONVENTIONS.md` (repo root) | Aider | Aider loads convention files only via `--read` / `read:` config; this is the conventional name. |

Not adapter-shaped but part of the same universal layer: `templates/AGENTS.md.template`
(the vendor-neutral standard file, read natively by OpenAI Codex, Cursor, GitHub Copilot
coding agent/CLI/VS Code, Windsurf, Zed, Jules, Factory, Amp, opencode, Devin, Warp, Roo
Code, JetBrains Junie, and more) and `templates/CLAUDE.md.template` (Claude Code reads
`CLAUDE.md`, not `AGENTS.md`; its fixed protocol section is Claude's adapter).

## Inclusion rule

An adapter exists only where (a) the tool does not read `AGENTS.md` natively (Gemini CLI,
Qwen Code, Aider, legacy Copilot surfaces), or (b) it adds a concrete named guarantee
(Cursor `alwaysApply`, Windsurf `always_on`). Don't add adapters beyond that — every
always-on file costs context in tools that load several of them.

## Design rules for adapter content

- **Self-sufficient, never a bare pointer.** Every adapter carries the six load-bearing
  protocol rules inline. Some tools inject the file's text but cannot follow "go read X"
  (Copilot review surfaces), and Zed loads only its *first* match in a priority list where
  `.github/copilot-instructions.md` ranks **above** `AGENTS.md` — a bare pointer there
  would disconnect Zed from the protocol entirely.
- **Short.** Keep every adapter under ~5,000 characters. Windsurf documents hard limits
  (~6,000 per rules file, ~12,000 combined) and silently truncates beyond them; other tools
  simply pay context for every always-on byte.
- **Uniform.** The six numbered rules are identical across all adapters; only the title,
  frontmatter, and vendor-specific footnotes differ. If you change a rule, change it in
  every adapter (the validator's keyword checks will catch a miss).
- **Never ship loaded vendor config.** Files a tool *executes or auto-loads as
  configuration* (`.aider.conf.yml`, `.gemini/settings.json`) are the user's, not the
  scaffold's — a repo-root config can silently override their personal settings. Document
  the snippet instead (see `CONVENTIONS.md` and `GEMINI.md` footnotes).

## Notes for specific tools

- **Zed** reads the first match of: `.rules`, `.cursorrules`, `.windsurfrules`,
  `.clinerules`, `.github/copilot-instructions.md`, `AGENT.md`, `AGENTS.md`, `CLAUDE.md`,
  `GEMINI.md` — root only. l00prite ships nothing above `copilot-instructions.md`, and that
  file is self-sufficient, so Zed is covered.
- **Gemini CLI / Qwen Code** users can skip the adapter entirely with
  `{"context": {"fileName": ["AGENTS.md", "GEMINI.md"]}}` in their own
  `.gemini/settings.json`, or `{"context": {"fileName": ["AGENTS.md", "QWEN.md"]}}` in
  `.qwen/settings.json`.
- **Nested `AGENTS.md` files** (monorepos): several tools apply only the closest file to
  the code being edited. If you add one, start it with a one-line pointer back to the root
  `AGENTS.md` and `.l00prite/`, or that subtree silently loses the protocol.

## Adding a new vendor

1. Confirm the tool's instruction-file path and format from its current official docs.
2. If it reads `AGENTS.md` natively, stop — it's already covered.
3. Otherwise copy an existing adapter, keep the six rules byte-identical, adjust only
   title/frontmatter/footnote, and add an entry to `templates/vendors.json` (with
   `adapter_template`, `target_path`, and `required_strings`).
4. Add the dogfood copy at this repo's own target location, extend
   `scripts/validate-l00prite.js` expectations if needed, and run the validator.
