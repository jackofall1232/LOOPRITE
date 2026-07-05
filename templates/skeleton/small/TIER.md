# Small Tier

For a single-purpose script, plugin, CLI tool, or narrow library: one main responsibility,
few or no external services, a small number of files.

`/build-loop` should pick this tier when the user's answers describe something with a
single clear job — e.g. "a script that converts X to Y", "a small WordPress plugin that
adds one feature", "a CLI that wraps one API". If the project has multiple independently
meaningful components, multiple integrations, or needs its own CI/infra setup, prefer
`medium/` or `large/` instead.

Layout:

- `src/` — main implementation, single entry point
- `tests/` — tests for the entry point
- `README.md` — stub, filled in by the build loop
- `.gitignore` — stub, filled in by the build loop
