# Project Blueprint

## Mission
example-project is a small command-line tool that fetches a list of RSS feeds and prints a
readable daily digest to stdout. It exists to demonstrate the files l00prite scaffolds into
a target repo; success looks like a working `digest` command covered by unit tests, run by
a developer once a morning.

## Architecture
Small tier: a single `src/main.py` entry point (argument parsing, feed fetching, digest
formatting) and `tests/test_main.py`. Feeds are listed in a plain `feeds.txt` checked into
the repo. No database, no external services beyond HTTP GETs to the feeds themselves.

## Requirements
- [ ] `digest` prints the newest item title + link per feed in `feeds.txt`.
- [ ] Unreachable feeds are reported to stderr without aborting the digest.
- [ ] `digest --limit N` caps items per feed (default 1).
- [ ] Unit tests cover formatting and the unreachable-feed path.

## Definition of Done
- [ ] All requirements above checked off, each verified by a test run recorded in
      `ledger.md` (command, exit code, summary, timestamp).
- [ ] `python -m pytest` exits 0.
- [ ] README documents install and usage in under a screen.

## Non-Execution Boundary
This blueprint is guidance for later implementation loops. Scaffolding tools must not execute the project unless a human explicitly starts an implementation session.
