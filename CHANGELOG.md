# Changelog

All notable changes to the L00prite OS Android app and marketing site are documented here.
Dates are UTC. The protocol itself (`.l00prite/`, Planning Mode, Execution Mode) has no
separate version — see `README.md` for what it currently supports.

## v0.2.0-beta — 2026-07-11

**Chat can now see your repo.** Selecting a repo in chat used to inject only a static
5-file memory digest — the model had no way to actually look at your code. Ordinary chat
now attaches three read-only tools (`read_file`, `list_dir`, `search_files`), and a newly
registered or cloned repo gets its `.l00prite/` memory folder scaffolded automatically
instead of staying empty until the first Run.

**Starting a run no longer fails on a raw git error.** If `.l00prite/` was already
committed to your repo (e.g. via Planning Mode), pressing Start used to fail with a raw
`worktree contains unstaged changes` error. Start now auto-checkpoints any uncommitted
work — including `.l00prite/` itself — before creating the run branch, so nothing is lost
and nothing blocks you.

**Hardening from two rounds of automated code review**, each verified against the
unfixed code before landing:
- A ledger-write race that could reproduce the same "unstaged changes" failure with a
  different trigger file.
- A real-git quirk that could hide a secret file inside a brand-new untracked directory
  from the auto-checkpoint's credential/Denylist gate.
- Repo registration no longer scaffolds memory files while another agent holds the lock.
- Chat's file-read tool is now memory-bounded instead of loading a whole file before
  truncating it; repo search is now bounded to a fixed number of files walked.
- The tool-call loop backing chat's repo browsing can no longer end a turn with
  unresolved tool calls the client was never meant to execute.
- A failed auto-checkpoint commit no longer leaves your git index silently staged.

**Android app fixes:**
- The on-device master encryption key is now persisted synchronously before it's used —
  previously an app kill or disk-write failure at exactly the wrong moment could silently
  make that session's encrypted provider secrets unrecoverable on next boot.
- The gateway supervisor now correctly restarts after a launch failure or the crash-retry
  limit is hit, instead of requiring the whole Android service process to be killed first.

**Access-control fixes:**
- A token scoped to one repository could previously see run IDs, goals, statuses, and
  costs for every other repository in the same project via the runs list. Runs are now
  filtered to the token's own repo scope.
- The active run's lease no longer gets committed onto your source branch by the
  pre-run auto-checkpoint — it now lands on the run branch, where it belongs.

**Also included** (previously shipped under the same beta APK filename before this
release formalized versioning): the repo-scope footgun fix (a mis-scoped setup token no
longer dead-ends with an unhelpful "not registered" error) and the adaptive launcher icon.

## v0.1.0-beta — 2026-07-06

Initial public beta: marketing site with an animated Execution Mode pre-flight demo, and
a signed, sideloadable Android APK running the full L00prite OS gateway on-device.
