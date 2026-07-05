# Constraints

Hard rules, user preferences, security boundaries, and architecture constraints.

## Hard Rules
- Scaffolding generates files only; it does not execute implementation.
- Existing files must not be silently overwritten.
- Every implementation loop must update `.l00prite/` memory before stopping.

## User Preferences
- Python 3.11+, standard library plus `feedparser` only.
- Plain-text output; no color codes.

## Security Boundaries
- No credentials of any kind — feeds are public URLs.
- Network access is limited to HTTP(S) GETs against URLs listed in `feeds.txt`.

## Architecture Constraints
- Single-file CLI (`src/main.py`); resist adding modules until a requirement forces it.
- Tests use pytest with default discovery (`tests/test_*.py`).

## Autonomous-Edit Denylist

Machine-readable glob list of paths an Execution Mode run must **never** auto-edit. A file
about to be edited that matches any glob below is treated as the
`destructive_operation_required` run boundary: the loop stops and asks for explicit per-action
human permission. This block is **protocol-adjacent and loop-immutable** — a run may never
remove or loosen an entry to get past a stop. `scripts/l00prite-doctor.js` warns if it is
missing.

```gitignore
# Secrets & credentials (none expected in this project — keep it that way)
.env
.env.*
**/secrets/**
**/credentials/**
**/*_key*
**/*_secret*
# CI / packaging — human review before changing how this ships
.github/workflows/**
pyproject.toml
# Protocol files (never agent-edited during a loop)
.l00prite/prompts/**
.l00prite/LOCKING.md
```

### Auto-merge allowlist (default: none)

Nothing is auto-merged by default — push/merge/deploy always need per-action human permission.
