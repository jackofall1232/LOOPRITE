# Medium Tier

For a web app or service with a few moving parts: an API layer plus a small amount of
business logic, maybe a database or a couple of integrations, config and basic CI.

`/build-loop` should pick this tier when the user's answers describe something with more
than one responsibility but not yet a multi-service system — e.g. "a small web app with a
REST API and a frontend", "a service that talks to one external API and persists state",
"a tool with a couple of distinct modules that need their own tests and config". If the
project is a single-file script or plugin, prefer `small/`. If it spans multiple
independently deployable services or needs architecture decision records, prefer `large/`.

Layout:

- `src/` — main implementation
  - `api/` — API layer / route handlers
  - `lib/` — shared business logic
- `tests/`
  - `unit/` — unit tests
- `config/` — environment/config files
- `docs/` — project documentation
- `.github/workflows/` — CI workflow stub
- `README.md` — stub, filled in by the build loop
- `.gitignore` — stub, filled in by the build loop
