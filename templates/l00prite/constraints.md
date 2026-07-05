# Constraints

Hard rules, user preferences, security boundaries, and architecture constraints.

## Hard Rules
- Scaffolding generates files only; it does not execute implementation.
- Existing files must not be silently overwritten.
- Every implementation loop must update `.l00prite/` memory before stopping.

## User Preferences
- Record durable user preferences here.

## Security Boundaries
- Record permissions, secrets handling, deployment, and data-safety boundaries here.

## Architecture Constraints
- Record stack, dependency, compatibility, and integration constraints here.

## Autonomous-Edit Denylist

Machine-readable glob list of paths an Execution Mode run must **never** auto-edit. A file
about to be edited that matches any glob below is treated as the
`destructive_operation_required` run boundary: the loop stops and asks for explicit per-action
human permission. This block is **protocol-adjacent and loop-immutable** — a run may never
remove or loosen an entry to get past a stop (doing so is itself the `human_review_gate`
boundary). Edit it yourself, before you arm a run. `scripts/l00prite-doctor.js` warns if this
block is missing.

```gitignore
# Secrets & credentials
.env
.env.*
**/secrets/**
**/credentials/**
**/*_key*
**/*_secret*
# Auth, money, and data safety
auth/**
payments/**
billing/**
**/migrations/**
# Infrastructure & deploy
.terraform/**
k8s/production/**
# Protocol files (never agent-edited during a loop)
.l00prite/prompts/**
.l00prite/LOCKING.md
```

### Auto-merge allowlist (default: none)

Nothing is auto-merged by default — push/merge/deploy always need per-action human permission.
If you ever allow auto-merge for trivial changes, list the exact safe paths here (e.g. docs or
comment-only edits). Behavior changes, dependency bumps, lockfile edits, and any denylisted
path are never eligible.
