# Project Blueprint

## Mission
l00prite is a vendor-neutral protocol that gives AI coding agents durable, file-based
project memory and a deterministic execution protocol, so Claude, Codex, GPT, Gemini,
Copilot, Cursor, Windsurf, Aider, or a future CLI agent can hand off work across sessions —
and run autonomous Execution Mode loops behind an explicit pre-flight gate — using files in
the repo instead of vendor-specific session state. This `.l00prite/` folder is the protocol
applied to l00prite's own repo.

## Architecture
See `CLAUDE.md` Section 2 for the authoritative description: two operating modes (Planning,
Execution) across six layers — scaffold layer (`.claude/commands/build-loop.md`,
`.codex/prompts/build-loop.md`), memory layer (`templates/l00prite/`,
`examples/vendor-neutral-output/`), event layer (`events/pending|processing|completed`,
event-loop prompts), handoff layer (resume-loop, heartbeat, respond-to-review,
handoff-summary), universal prompt + vendor layer (canonical prompts in
`templates/l00prite/prompts/` with byte-identical mirrors; `AGENTS.md.template`;
`templates/adapters/` + `templates/vendors.json`), and execution layer (`execute-loop`,
pre-flight gate, nine run boundaries). Cutting across all of them: the lock/lease
convention in `LOCKING.md`/`lock.json`.

## Requirements
See `CLAUDE.md` Section 3 for the full, current requirements list.

## Definition of Done
See `CLAUDE.md` Section 4 for the v1 (shipped) and v1.1 (this branch) checklists.

## Android control plane (in progress)
The L00prite OS Android APK track (maintainer brief 2026-07-05) packages the `cli-os/`
gateway as a self-contained Android app: the device is the local control plane (no hosted
server). Authoritative design: `cli-os/docs/android-architecture.md` — packaging decision
(bundled android/arm64 PIE gateway exec'd from the APK's native-library dir + thin Java
wrapper + WebView dashboard), platform adaptations (DNS/CA/shell/git/secrets), Venice AI +
Gemini provider manifests, architect/writer/reviewer/advisor role profiles, and the
Phase 0–3 roadmap.

## Mode Boundary
Planning Mode generates files only; it never executes the projects it scaffolds and always
ships Execution Mode disarmed. Execution Mode is entered only through
`prompts/execute-loop.md`'s pre-flight display and explicit in-session human confirmation —
persisted flags never substitute for it, and a running loop can never raise its own limits.
