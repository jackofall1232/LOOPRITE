# Changelog

All notable changes to the L00prite OS Android app and marketing site are documented here.
Dates are UTC. The protocol itself (`.l00prite/`, Planning Mode, Execution Mode) has no
separate version — see `README.md` for what it currently supports.

## v0.4.0-beta — 2026-07-12

**xAI Grok and Google Gemini added as first-class providers; Venice made selectable.**
The Add-provider dropdown now offers seven presets (Anthropic, OpenAI, Gemini, xAI, Venice,
Zhipu, custom), each with its own default base URL and sample model, driven by a new
unauthenticated `GET /v1/providers/catalog` endpoint so the UI never conflates "which
preset was picked" with "which wire adapter kind gets used." Includes Grok 4.5 and the
current `grok-build-0.1` coding model, and Gemini 3.5/3.1 with first-party-verified
pricing.

**Consent-gated full l00prite protocol scaffolding for registered repos.** Registering or
cloning a repo previously only wrote a minimal `.l00prite/` memory subset silently. A new
opt-in action (a checkbox at registration, or a standalone "Add l00prite" button on any
registered repo) creates a local branch, writes the complete protocol — `AGENTS.md`, the
six loop prompts, and all vendor adapters — and commits it, then hands back copy-paste
push instructions. Nothing is ever pushed or opened as a PR automatically.

## v0.3.0-beta — 2026-07-11

**Set a daily spending budget from the dashboard.** A new "Set budget" button and modal
cap what a project can spend per day, with three stop modes: stop hard at the limit,
allow a one-time 100% overage, or meter spend without ever stopping a run. Backed by a
new `GET`/`POST /v1/budget` endpoint and an effective-ceiling calculation in the policy
enforcement point.

**Ask in plain words, get a reviewable run.** The Playground can now turn a request like
"...and execute it" into a drafted Run — a new `propose_run` chat tool follows the same
create-and-pre-flight path as the dashboard's "New run" button. Drafting is always
side-effect-free: starting the run still requires typing `EXECUTE` in its pre-flight,
which this tool can never reach on its own.

**Lock a model in the Playground.** A model-lock toggle pins your choice so it survives
reloads instead of resetting to auto. Client-side and entirely opt-in.

**A Help section, in the app.** Since this dashboard is the whole Android app UI, a new
Help section covers getting started, the Playground's run-drafting flow, the Runs
pre-flight/`EXECUTE` gate, and the new budget modes — illustrated with real screenshots
captured from a live dashboard.

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
