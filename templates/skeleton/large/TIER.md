# Large Tier

For a multi-service or multi-module system: several independently meaningful components,
integration tests, infra/deployment concerns, and architecture decisions worth recording.

`/build-loop` should pick this tier when the user's answers describe something that spans
multiple services or modules that could reasonably be developed/tested somewhat
independently — e.g. "a system with a backend API, a worker service, and a shared library",
"a platform with multiple deployable components", "a project where infra/deployment is a
first-class concern and architectural tradeoffs need to be documented as we go". If the
project is really just one API plus some business logic, prefer `medium/` instead — don't
over-scaffold.

Layout:

- `src/` — shared/core modules
  - `module-a/` — first core module
  - `module-b/` — second core module
- `services/` — independently deployable services
  - `service-a/` — first service
  - `service-b/` — second service
- `tests/`
  - `unit/` — unit tests, one per module
  - `integration/` — cross-module/cross-service integration tests, one per service
- `docs/` — project documentation
  - `adr/` — architecture decision records (numbered, append-only)
- `infra/` — deployment/infrastructure config
- `.github/workflows/` — CI workflow stubs (build/test + integration)
- `README.md` — stub, filled in by the build loop
- `.gitignore` — stub, filled in by the build loop

The `module-a` / `module-b` / `service-a` / `service-b` names are placeholders — `/build-loop`
should rename them to match the actual modules/services described by the user, and add or
remove module/service folders as needed for the real project shape.
