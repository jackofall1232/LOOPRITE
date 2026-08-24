# Changelog

All notable changes to the L00prite OS Android app and marketing site are documented here.
Dates are UTC. The protocol itself (`.l00prite/`, Planning Mode, Execution Mode) has no
separate version — see `README.md` for what it currently supports.

## v0.10.1-beta — 2026-08-24

**First production-signed APK.** Same app as 0.10.0-beta, now signed with a dedicated
LOOPRITE release certificate (`CN=LOOPRITE, O=LOOPRITE, C=US`, RSA-4096) instead of the
local debug keystore (`CN=L00prite Debug`). Download: [looprite.click](https://looprite.click/).

This is a signing-identity change: Android will refuse an in-place update from any
0.10.0-beta or earlier install. Uninstall the old app first (that wipes on-device keys,
tokens, and imported repos — nothing is in the cloud), then sideload 0.10.1-beta. Later
0.10.x builds signed with this same certificate will update in place.

No gateway, dashboard, or protocol behavior change.

## v0.10.0-beta — 2026-07-29

**Never locked out again — named workspaces and a real sign-in.** Signing out (or the 8-hour
session cookie expiring) used to strand Android users with no way back in short of reinstalling,
because the setup wizard minted the app token invisibly. This release makes the per-install
secret the *device-owner* credential and builds sign-in around named workspaces:

- **Workspaces, not lockouts.** The wizard now names your workspace (default `default`), and the
  sign-in card gains a "This device" section: tap a saved workspace to sign back in, or create a
  **new workspace** — a second team on the same box, with its own repos, budget, GitHub
  connection, and runs (its token is shown once for CLI/other devices).
- **Always signed in at launch.** The Android shell now also exchanges the install secret for a
  dashboard session natively at every cold start, so cookie expiry self-heals on relaunch.
- **Security model preserved.** New `/v1/owner/*` endpoints are gated by the install secret (or
  the setup cookie only it can mint) — never by a project name, which would be an
  unauthenticated session oracle to any app on the phone. They fail closed on desktop gateways
  (no install secret), where `l00prite token mint` remains the recovery path. The setup cookie
  lifetime extends to 8h to match the UI session; the setup-mutation endpoints stay latched
  closed after first run. See `cli-os/docs/security-model.md` § Device-owner sessions.
- **Wizard loop fixed.** "Skip providers" used to land you back in the wizard forever (the
  dashboard requires one configured provider); the providers step now says plainly that one
  provider is enough to start.

## v0.9.1-beta — 2026-07-29

**Private GitHub repos now clone in the app.** Clone-from-URL uses the project's connected
GitHub account (Advanced → GitHub) automatically: the vault-sealed token is attached only to
`https://github.com` clone URLs — in memory, never in argv, config, or error text — so private
repos clone on-device with no SSH key or git credential helper (neither exists on Android).
The auth-failure hint now points at the GitHub connect flow, the Repos sheet and clone dialogs
say so up front, and git's `error: 401/403` phrasings are recognized as auth failures.

## v0.9.0-beta — 2026-07-28

**A chat-first app, and Kimi joins the roster.** This release rebuilds the app's interface
around the way people actually use it — pick providers, add a repo, start prompting — and adds
Kimi (Moonshot AI) as a first-party provider:

- **Kimi (Moonshot AI) provider.** First-party adapter manifest (OpenAI-compatible,
  `https://api.moonshot.ai/v1`) with the documented model lineup — `kimi-k3` (1M context
  flagship), `kimi-k2.7-code`, `kimi-k2.6`, `kimi-k2.5`, `moonshot-v1` — verified against
  Moonshot's own API docs. Available in the setup wizard and the Providers sheet like every
  other preset; `moonshot` resolves as an alias. Prices are carried as unconfirmed (fail-closed)
  until first-party pricing can be re-fetched.
- **Redesigned, chat-first UI.** The app now opens straight into a clean conversation view:
  model picker (defaults to `auto`), repo picker, and a composer. The dense nine-section
  control plane (Runs, Costs, Tokens, Activity, GitHub, Budgets, Help) moves behind a single
  Advanced drawer — same features, same endpoints, no longer in your face.
- **Bridge toggle.** Cross-provider bridging ("Claude asks Kimi for help mid-answer") is now a
  one-tap switch in the chat header instead of a header-only power feature. It defaults on once
  you have two or more providers connected, and the UI tells you it's ignored while a repo is
  selected (repo file tools take priority on that path).
- **Seamless first run.** The setup wizard is now three calm steps: welcome (the encrypted
  vault initializes itself), connect providers (one card per provider with live key
  validation, add as many as you like), and done — the app token is minted and exchanged for
  the session invisibly, so a new user never has to think about tokens.
- **Branded native splash.** The Android shell shows a proper l00prite splash (matching the
  app's near-black + electric-lime palette) instead of a bare "Starting…" label, and the
  Import-repo button is quieter.

## v0.8.0-beta — 2026-07-15

**Phase 0 security and orchestration contracts.** This release strengthens the application shell
without changing provider routing or beginning the planned Codex/bridge redesign:

- Gateway tokens now carry explicit capability scopes. Existing tokens are marked legacy-admin for
  compatibility and can be rotated to least-privilege scopes with `token mint --scopes ...`.
- The dashboard exchanges a bearer once for an HttpOnly, SameSite=Strict loopback session instead
  of storing the long-lived token in JavaScript localStorage. Revoking the source token invalidates
  its UI session.
- Android exchanges its per-install setup secret natively for a short-lived setup session. The
  secret no longer enters a URL, WebView history, or JavaScript, and active-session replay is denied.
- The WebView is restricted to the exact loopback origin and disables file/content/mixed-content
  access. Dashboard/API responses add CSP, frame, MIME, referrer, permissions, and no-store headers.
- Privileged audit events are schema-versioned and hash-chained; new provider-neutral orchestration
  contracts define future events, approvals, capabilities, grants, tasks, artifacts, and external
  sessions without activating new runtime behavior.
- Unpriced models remain usable, but the dashboard now gives a prominent warning that `$0`/
  unconfirmed usage is not free and accurate budget enforcement cannot be guaranteed.

## v0.7.0-beta — 2026-07-13

**Override the per-reply and per-run tool-call budgets when a task needs more room.** Asking
the app to do a PR-sized job could hit a fixed tool-call limit partway through, cutting the
reply off with a "send a follow-up to continue" message — and the follow-up re-read everything
from scratch, wasting tokens. Two new, optional budgets fix this, and both are yours to set:

- **Playground tool budget** (Playground → Tool budget). Raises how much repo-browsing the
  assistant may do in a single reply — tool rounds and file reads — up to a safe maximum.
  Defaults are unchanged (6 rounds / 24 reads), and a raised budget still spends against your
  daily cost budget, which stops a runaway reply.
- **Max tool calls per unit** (New run → Advanced). Raises how many tool calls the coder may
  make while finishing one unit of a Run before it pauses for you (default 40, up to 200). A
  run that stops because a unit ran out of tool calls now tells you exactly that, and which
  knob to raise; the pre-flight shows the budget you're arming.

When a reply does spend its whole budget, it now ends with a short "Progress Notes" summary and
the app shows a one-click "Raise tool budget" button, so you can continue from where it left
off instead of starting over.

These budgets are always set by you: the AI can never raise its own limit, a per-request
override can only *lower* it, and a run drafted from a chat prompt always gets the default. The
`EXECUTE` confirmation gate is unchanged. (Also fixes a latent overflow in the bridge hop-cap
header that could make a huge value wrap negative.)

## v0.6.0-beta — 2026-07-12

**Connect GitHub — the app can now push real code and open pull requests from the phone.**
A new "Connect GitHub" card in the dashboard takes a fine-grained personal access token,
verifies it live, and seals it in the same encrypted vault as your provider keys (it is never
shown again and never leaves the gateway). Once connected, autonomous Runs can push their
branch with a new `push_branch` tool, and the "Add l00prite" flow can push + open a pull
request — both over pure-Go HTTPS, so they work on a bare Android device with no `git` binary
and no GitHub CLI installed. Every push still goes through the same human approval (or the
project's Auto-PR setting); l00prite never merges. The token is host-scoped to `github.com`:
it is only ever attached to an HTTPS github.com remote — never sent to an ssh remote, a
different host, or a non-GitHub origin (it silently falls back to your ambient credentials
there), never placed in a command's arguments, never written to `.git/config`, and scrubbed
from any error message.

**Autonomous Runs can act again.** The default run objectives ("balanced"/"quality") were
silently running the coder in a delegation mode that advertised its file/git tools but never
executed them — so a Run would stop at a review gate reporting it had "no tool to run git."
The coder now always runs in the mode where its tools actually execute, so it can write files
and push. (The "only tool is l00prite_bridge / cannot git push" failure.)

**Dark-mode app screens are readable again.** On the Android app, the Add-provider and
Register-repo dialogs rendered as unreadable dark-on-dark (and, in light mode, dark-on-dark
the other way). The app now tells its own screen which theme the device is in and turns off
the WebView's conflicting auto-darkening, so every dialog follows the device's light/dark
setting correctly. Desktop-browser dashboards are unchanged, except that light-mode dialogs —
which had the same low-contrast bug — are now readable there too.

## v0.5.0-beta — 2026-07-12

**Per-project Auto-PR toggle for autonomous Runs, off by default.** A run's `git push` and
`gh pr create` actions can now be pre-approved at the project level instead of waiting on a
human for every single push/PR — but never merge, deploy, credential changes, or anything
destructive, which stay gated no matter what. A human still reviews the exact resolved gate
table in the pre-flight display before ever typing `EXECUTE`, and an explicit per-run choice
always wins over the project-level default.

**Retry support for a capability-gapped "Add l00prite" push/PR.** If pushing the scaffold
branch or opening its pull request failed the first time (no `git`/`gh`, `gh` not signed in,
a bad remote), clicking "Add l00prite" again used to just no-op — the committed protocol
files made the repo look already complete. A new gateway-side tracking table now remembers
the pending branch so a later click retries the exact same push/PR instead of losing track
of it; an out-of-band success (pushed or merged by hand) is detected and reported honestly,
and a hand-opened pull request is found via a read-only lookup so a retry can never open a
duplicate.

**Several push/PR-create gate-classification hardenings, found across two independent
automated review passes on this feature (gemini-code-assist, Copilot, Codex) and fixed one
at a time, each confirmed against the real command text before and after the fix:** a
force-push flag with an attached `=value` (`--force-with-lease=main`) no longer slips past
the exact-match check; `git push`/`gh pr create` commands carrying shell metacharacters
(`; rm -rf ...`) are never auto-approvable; an auto-approved push is now restricted to
exactly the run's own branch (not any branch, and not a refspec-embedded delete/force
form like `origin :branch` or `origin +HEAD:branch`); `gh pr create` flags that read an
arbitrary local file into a public PR body (`-F`/`--body-file`, `-T`/`--template`, including
pflag's attached-shorthand form) are rejected, as is `-R`/`--repo` (targeting a repository
other than the run's own) and a missing `--head`; and — the most serious of the batch —
`--receive-pack`/`--exec`, which let `git push` invoke an arbitrary program on the remote
side of an SSH-transport push (the classic git-push-over-SSH command-execution vector), are
now always treated as destructive and never auto-approvable.

**"Add l00prite" can now push and open a pull request for you.** Previously, adding the
full l00prite protocol to a repo from the dashboard always stopped after a local commit —
you had to copy-paste a `git push` command yourself. The Register-repo modal's consent
checkbox now reads "Create a branch, push it, and open a pull request" (checked by
default; the standalone "Add l00prite" action for an already-registered repo has its own
matching, also-default-checked option) — when you leave that checked, l00prite pushes the
branch and opens a pull request using this gateway host's own git/GitHub CLI credentials,
the same ones a `git push` typed at this host's own terminal would use. l00prite never
merges anything it opens — a human always reviews and merges (or closes) the pull
request. If the host can't push or open a PR (no `git`/`gh` installed, `gh` not signed
in, or the remote rejects the push), you get a clear plain-language explanation and the
same copy-paste fallback instructions as before — the branch and commit always still
happen locally either way, and nothing is ever left half-pushed with no explanation.

Also fixed a related bug caught by this pass's own tests before it shipped: if the push
itself succeeded but opening the pull request specifically failed, the response and
on-screen note used to describe it as "nothing was pushed" — factually wrong, since the
branch really had reached the remote. That case now says so correctly and gives you a
copy-paste `gh pr create` command instead of a push command.

*The push/PR feature itself was not exercised against a real GitHub repository or a real
`gh` CLI in the session that wrote it (no `gh` binary or GitHub network access in that
sandbox) — verified instead against a real local git remote with a stand-in `gh` script
that mimics `gh`'s exit codes and output shape. This build (v0.5.0-beta) is the first
Android APK to ship it, along with the v0.4.1-beta fixes below (which also never got
their own APK) and everything else described above.*

## v0.4.1-beta — 2026-07-12 (source only — folded into v0.5.0-beta's APK, no separate build)

**Dark-theme dashboard fixes: invisible checkboxes, and an audit of the Models modal.**
The "Add provider" panel's two checkboxes ("Make this the default provider", "Add without
validating") relied entirely on native `appearance:auto` rendering plus `accent-color` —
`accent-color` only paints the *checked* state, so the unchecked box had no explicit
background or border and could render with a near-invisible native dark-theme fill against
the dashboard's dark cards. `.field.check input` now sets an explicit background and border
so the unchecked state is always visible regardless of the browser's native checkbox theme.
Investigated the per-provider "Models" modal for a separately reported missing-label bug:
traced the model-name data end to end (manifest → `ModelsFor` → `/v1/dashboard/summary` →
`editModels()`) and live-rendered the modal under multiple providers/adapters, viewport
widths, and disabled-model states — model names were populated and correctly colored in
every case tested, so the reported blank-label symptom did not reproduce against this
commit; `.modelrow span` was still given an explicit `color` (previously relied on
inheritance) as a low-risk hardening measure. Separately confirmed a real, different bug in
the same area: a provider card's "N models off" count is read directly from the stored
`disabled_models` list without intersecting it against the provider's *current* model
catalog, so a stale disabled-model row left over from a manifest update (a model id no
longer offered) inflates the count even though the Models modal itself only ever lists
live models — a state bug, not a rendering bug, left unfixed pending a scoping decision.

**OpenAI Chat Completions reasoning models: `max_completion_tokens`.** OpenAI's o-series
(o1/o3/o4) and, per current reports, the gpt-5.x family reject the legacy `max_tokens`
field under `/chat/completions` ("Unsupported parameter: 'max_tokens' is not supported
with this model. Use 'max_completion_tokens' instead."), which broke provider-add
validation for those models. The openai-compat adapter's `BuildRequest` — the single
call point shared by both the pre-save validation ping and every real inference
request — now renames `max_tokens` to `max_completion_tokens` for model ids matching
those families, leaving every other openai-compat upstream (Zhipu, xAI, Venice, custom
endpoints) untouched. This repo's OpenAI manifest deliberately carries no confirmed
model ids (see `internal/gateway/adapters/manifests/openai.json`) and this environment
could not reach OpenAI's own docs to verify exact current model/version naming, so no
specific dated model snapshot is asserted here or in the updated `gpt-4o` → `gpt-5`
placeholder text; both were chosen conservatively rather than guessed.

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
