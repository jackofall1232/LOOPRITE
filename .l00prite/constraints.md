# Constraints

Hard rules, user preferences, security boundaries, and architecture constraints for
l00prite's own repo.

## Hard Rules
- Scaffolding (`build-loop`) generates files only; it does not execute implementation.
- Existing files must not be silently overwritten.
- Every implementation loop must update `.l00prite/` memory before stopping.
- No change to `.claude/commands/build-loop.md` or `scripts/validate-l00prite.js` without
  stopping for human review first (see `heartbeat.json` `human_review_gates`).
- No push, merge, or deploy to `main` without explicit maintainer sign-off.

## User Preferences
- Maintainer: jackofall1232.
- Feature branches only; no direct commits to `main`.

## Security Boundaries
- No secrets, credentials, or tokens are stored in `.l00prite/` or any tracked file.
- l00prite does not auto-push, merge, or act as an autonomous GitHub bot.

## Architecture Constraints
- Vendor-neutral by design: no protocol file may assume only one agent CLI can read it.
- No backend, hosted service, or external runtime dependency — plain Markdown, JSON, and a
  dependency-free Node validator only.

## Autonomous-Edit Denylist

Machine-readable glob list of paths an Execution Mode run must **never** auto-edit. A file
about to be edited that matches any glob below is treated as the
`destructive_operation_required` run boundary: the loop stops and asks for explicit per-action
human permission. This block is **protocol-adjacent and loop-immutable** — a run may never
remove or loosen an entry (doing so is itself the `human_review_gate` boundary). It encodes,
as globs, the human-review gates this repo already declares.

```gitignore
# Review-gated files — human review required before any change
.claude/commands/build-loop.md
scripts/validate-l00prite.js
# Protocol files — never agent-edited during a loop
.l00prite/prompts/**
.l00prite/LOCKING.md
templates/l00prite/prompts/**
templates/l00prite/LOCKING.md
AGENTS.md
templates/AGENTS.md.template
templates/adapters/**
# Secrets & credentials
.env
.env.*
**/*_key*
**/*_secret*
# CI / release — human review before changing how this ships
.github/workflows/**
```

### Auto-merge allowlist (default: none)

Nothing is auto-merged by default — feature branches only, no direct commits to `main`, and
no push/merge without explicit maintainer sign-off.
