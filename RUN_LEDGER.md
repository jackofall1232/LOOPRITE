# RUN_LEDGER.md — Diagnosis Run: cli-os Control-Plane Bugs 1 & 2

**Run type:** diagnosis-only (no code written).
**Date:** 2026-07-11.
**Inputs:** two independent direct-code-reading diagnoses per bug, each adversarially verified by a second agent against the actual compiling source (build confirmed clean) and, for Bug 2, against the exact pinned dependency source (go-git v5.18.0 per `cli-os/go.mod`) plus two empirical reproductions (real `git` CLI and a throwaway Go test driving the real `gogitClient`).
**Output:** this document — the authoritative handoff for the implementing agent (Opus). A prior stale draft at `cli-os/RUN_LEDGER.md` (produced on the wrong model, carrying Bug 2's since-refuted root cause) has been deleted.

---

## 1. Bug 1 — "Can you see the repo?" → model says it has no file/repo access

### 1.1 Final root cause (confidence: high — all five verification points CONFIRMED, counter-example search came up empty)

The model is telling the truth. The ordinary chat path never gives it any file access, and for a freshly linked repo it gives it no repository context at all. Three stacked facts:

1. **No tool is ever attached.** The chat completion path (`POST /v1/chat/completions` → `HandleChatCompletion` in `cli-os/internal/gateway/ingress.go:106` → `runTurn` in `cli-os/internal/gateway/turn.go`) never writes a file-access tool into the outgoing request. The engine's `Toolbox` (`cli-os/internal/engine/tools.go:94`, exposing `read_file`/`write_file`/`list_dir`/`search_files`/`run_command`/`git_command`) is referenced only from `internal/engine/exec.go:53` and `internal/engine/engine.go:349` — engine-package code that runs only inside an armed Run. A grep of the entire `internal/gateway` package for `Definitions(`, `Toolbox`, `read_file`, `list_dir`, `search_files` returns **zero** hits. The only site in the gateway that ever mutates `req["tools"]` is `bridge.go:321` (`convo["tools"] = append(append([]any{}, clientTools...), BridgeTool)`) — the delegate-to-another-model BridgeTool, gated by `IsBridgeArmed` (`bridge.go:78-88`), unrelated to file access. `routerauto.go:71` only *reads* the tools array for routing heuristics. `EngineCaller` (`gateway/enginecaller.go:17-20`) is constructed once at `internal/server/server.go:204` and invoked only from `engine/exec.go:19` (`callRole`) inside a Run — never from ingress.

2. **Repo "awareness" in chat is a static digest of 5 protocol files, not browsing.** `runTurn` (turn.go:282-289, finalized at turn.go:302 `finalReq := InjectMemory(cleanReq, mem)`) and the streaming path (ingress.go:262-267) call `memory.Query` (`cli-os/internal/memory/memory.go:99-110`), which reads exactly five hardcoded files under `<repoRoot>/.l00prite/` (memory.go:26-32: `constraints.md`, `blueprint.md`, `memory.md`, `failures.md`, `todos.md`) and prepends them as one untrusted-content system message (`gateway/inject.go:12-14`). It never reads `main.go`, `package.json`, `src/**`, or anything else.

3. **A repo linked via the dashboard has no `.l00prite/` at all until its first Run.** `HandleRepoRegister` (`gateway/repos.go:94-104`) does `filepath.Abs` + `os.Stat` + SQL `INSERT INTO repos` + a **read-only** `memory.RepoFreshness` call (`memory/freshness.go:42-56` — `os.Stat` only, no `MkdirAll`, no writes). `HandleRepoClone` (`repos_clone.go:118, 125-127, 133`) clones the git repo but likewise never scaffolds. The only scaffold call in the entire repo is `f.Scaffold(...)` at `internal/engine/preflight.go:150`, inside `BuildPreflight` Step 1, reachable only via `/v1/runs*`. So for a paste-a-URL-then-chat user, `memory.Query` returns `Status: "empty"`, reason `no_memory_dir` (memory.go:108-109), and `InjectMemory` no-ops — returning the request byte-identical (`inject.go:21-24`: `if mem.Status == "empty" || ... || len(mem.Blocks) == 0 { return req }`). The model receives **zero** repository context.

**Refinement from verification:** the UI does *not* overpromise — it is deliberately honest. The `play-repo` select (`dashboard.html:339`) is titled "Inject a registered repo's .l00prite memory", the repos table shows a "no .l00prite memory" badge (`dashboard.html:632`), and the post-register note explicitly says no memory was found yet (`dashboard.html:928`). Selecting a repo does nothing beyond setting the `x-l00prite-repo` header. This is a **capability gap**, not a delivery bug or a UI-copy bug: the product's DoD promises a conversational repo-aware experience the backend simply does not implement.

### 1.2 Recommended architectural fix (opinionated)

**Do both of the following. Neither alone is sufficient.**

**(a) Attach a read-only chat toolbox — a NEW, gateway-owned, deliberately narrower construct, not the engine `Toolbox`.**

When a chat completion names a registered repo (via `x-l00prite-repo`), the gateway should attach three tools to `req["tools"]` and execute them locally in an agentic tool loop within `runTurn`: `read_file`, `list_dir`, `search_files`. Explicitly excluded: `write_file`, `run_command`, `git_command` — no mutation, no execution, ever, outside a governed Run.

Implementation shape: a new `internal/gateway/chattools.go` (or a shared `internal/repotools` package) that **reuses the engine's path-containment/jail logic** (the symlink-safe `within`-style checks the engine tools already have) but defines its own tool structs. Do not export or reuse `engine.Toolbox` directly: the engine toolbox's identity is "governed Run tool surface" with denylist/allowlist/approval semantics that must not be entangled with an ungoverned chat path — sharing the struct invites future drift where an engine tool addition silently becomes chat-reachable.

Tradeoffs, weighed:
- *Scope creep / security:* this sends arbitrary repo file contents to third-party model providers outside a Run's governance. That is acceptable because it is precisely the user's intent when they select a repo and ask about it — but it must be bounded: (1) jail to the registered repo root with the same symlink-containment discipline as the engine; (2) per-call and per-turn byte caps (reuse the memory budget config); (3) wrap every tool result in the same untrusted-content envelope `InjectMemory` uses (`inject.go:12-14`) so repo file contents cannot smuggle instructions; (4) read-deny obvious secret paths (`.env`, `*.pem`, `id_rsa*`, `.git/config` credentials) — see §5 for the open design question on reusing an engine denylist here; (5) meter/ledger the tool calls like any other gateway activity.
- *Alternative rejected — RAG/pre-indexing:* building an embedding index of the repo would answer "what's in this repo" without tools, but adds a whole indexing subsystem, staleness management, and storage for a product whose architecture is "no backend, plain files." Interactive read-only tools are strictly simpler and match how every competing agent product answers this question.
- *Alternative rejected — expand the static digest to include a file tree:* cheap, but it only answers one question ("what files exist"), not "what does main.go do" — the user's actual follow-up. Do add a repo file-tree block to the injected context as a cheap complement (one `list_dir` equivalent, capped), since it primes the model to know tools are worth calling.

**(b) Auto-scaffold `.l00prite/` at repo link time.**

Yes — `HandleRepoClone` should call the existing `engine.Files{Root: absRoot}.Scaffold(...)` (the same never-overwrites scaffold used at `preflight.go:150`) immediately after a successful clone, and `HandleRepoRegister` should do the same for local-path registration (surfacing a plain-language note in the response: "a `.l00prite` memory folder was added to this project"). Rationale: (1) memory injection has something to inject from the moment a repo is linked, instead of a silent no-op; (2) the dashboard's "no .l00prite memory" badge state disappears for the normal flow; (3) it removes the "virgin vs. already-committed `.l00prite/`" bimodality that made Bug 2's original hypothesis wrong (freshly scaffolded files are untracked and harmless to go-git; see §2). Scaffold is already idempotent and non-destructive, so this is safe on repos that already carry `.l00prite/`.

**Honesty caveat (state this in the UI):** a fresh scaffold is template boilerplate. Until a run or a human fills `blueprint.md`/`memory.md`, injection gives the model protocol context, not project knowledge. The tool attachment in (a) is what actually answers "can you see the repo"; (b) fixes the misleading empty state and the injection no-op. Update the Playground labels ("memory injected") to reflect the new reality once tools ship — today's honest copy becomes stale the moment (a) lands.

---

## 2. Bug 2 — `git checkout -B l00prite/run-…` fails with raw "worktree contains unstaged changes"

### 2.1 Final root cause (confidence: high — empirically reproduced under both backends; original mechanism REFUTED and REPLACED by verification)

**The original hypothesis's trigger was wrong; the verified trigger is more deterministic.** The originally hypothesized mechanism — that `BuildPreflight` Step 1's freshly scaffolded, never-committed `.l00prite/` files trip go-git's dirty check — is **refuted**: go-git v5.18.0's `containsUnstagedChanges()` (`worktree.go:624-644`) diffs **index → worktree** and explicitly `continue`s past `merkletrie.Insert` entries, i.e. **untracked new files are skipped**. Only `Modify`/`Delete` of already-**tracked** files return true. Empirically verified: a new untracked file under `.l00prite/` → `CheckoutNewBranch` error = nil; a modification of an already-tracked `lock.json` → `worktree contains unstaged changes`.

The verified causal chain:

1. `StartRun` (`cli-os/internal/engine/engine.go:97`) **unconditionally** calls `f.AcquireLock(run.ID, "execute-loop run (l00prite OS engine)", e.LeaseTTLSec)` — *before* `EnsureRunBranch` at engine.go:102.
2. `AcquireLock` (`cli-os/internal/engine/l00pfiles.go:201-254`) always rewrites `.l00prite/lock.json` with fresh `lock_id`/`acquired_at`/`expires_at` values (`f.writeLock(m)` at l00pfiles.go:247) — always different bytes.
3. If `lock.json` is already **git-tracked** — true after any prior `CommitUnit` (`tools.go:912-918` → `AddAll`, a blanket `git add -A` with no `.l00prite/` exclusion), or on a first engine run against a repo whose `.l00prite/` was committed via Planning Mode — this rewrite is a tracked-file `Modify`.
4. `EnsureRunBranch` (`tools.go:889-907`) tolerates it: its `dirtyPathsOutsideL00prite` check (tools.go:900) exempts `.l00prite/`, so it proceeds to `git.CheckoutNewBranch(root, branch)` (tools.go:903).
5. Under the **gogit** backend (`gitx.Detect()` falls back to go-git when no git binary is on PATH — the documented Android/on-device case), `gogitClient.CheckoutNewBranch` (`gitx/gogit.go:108-126`) calls `wt.Checkout(&git.CheckoutOptions{Branch: ref})` with **neither `Force` nor `Keep`** (gogit.go:125 — the only `CheckoutOptions` construction in the entire tree). go-git's `Checkout` then runs `Reset(ResetOptions{Mode: MergeReset})` (go-git worktree.go:181-186, 202) → `ResetSparsely` → `containsUnstagedChanges()` → returns `ErrUnstagedChanges` ("worktree contains unstaged changes", worktree.go:31). No go-git public API option scopes this check to exclude paths — `ResetOptions.Files`/sparse dirs apply only *after* the unconditional check.

**When it fires:** every `Start` after the first commit has ever landed in the target repo's `.l00prite/` (practically: every second-and-later run), and the very first run against any repo whose `.l00prite/` was committed before the engine touched it — combined with the gogit backend. **exec-git is immune in this exact scenario** (empirically verified): `git checkout -B <new-branch>` at current HEAD is a tree no-op, and real git refuses only when checkout would overwrite differing files.

**Test coverage: genuine gap.** `internal/gitx/gitx_test.go` `TestClientsBasicFlow` exercises `CheckoutNewBranch` under both backends but only on clean trees. `internal/engine/tools_test.go` `TestBranchAndCommit` skips without a git binary and uses `gitx.Detect()`, so it only ever runs the exec backend; its one dirty-tree case is outside `.l00prite/`. Zero tests combine gogit + dirty tracked `.l00prite/` path + `EnsureRunBranch`/`StartRun`.

**Propagation (confirmed verbatim at every hop, zero translation):**
- `tools.go:903-904`: `fmt.Errorf("git checkout -B %s failed: %w", branch, err)`
- `engine.go:102-105`: `fmt.Errorf("%w: %v", ErrBadState, err)`
- `gateway/runs.go:187`: `oaiError(w, status, "Could not start run: "+err.Error(), "invalid_request_error", code)` — raw string concatenation
- `dashboard.html:740-747` (`mErr`) + `:1189-1195` (`startBtn.onclick`): renders `data.error.message` verbatim (HTML-escaped only).

User sees: `Could not start run: run not ready: git checkout -B l00prite/run-<id> failed: worktree contains unstaged changes`.

### 2.2 The mandated fix — auto-checkpoint before checkout (pseudo-diff)

**Placement is the critical subtlety.** The spec says "pre-flight-time detection … auto-commit … BEFORE the checkout." A checkpoint made *only* at pre-flight time **does not fix the bug**: the actual trigger — `AcquireLock`'s rewrite of `lock.json` — happens inside `StartRun` at engine.go:97, *after* pre-flight, immediately before the checkout. Therefore: **detection and disclosure happen at pre-flight; the auto-commit itself executes inside `StartRun`, after `AcquireLock` (engine.go:97) and before `EnsureRunBranch` (engine.go:102).** That ordering guarantees the lock rewrite is inside the checkpoint.

**Why the checkpoint must include `.l00prite/`:** go-git's `containsUnstagedChanges()` is whole-tree and unconditional, with no path-exemption API (verified against v5.18.0 source). The engine's Go-side `.l00prite/` exemption is invisible to it. An auto-commit that excluded `.l00prite/` would leave the tracked-`lock.json` `Modify` in place and the checkout would still fail. Commit **all** dirty paths — the user's work outside `.l00prite/` *and* everything inside it.

**File-by-file pseudo-diff:**

**`cli-os/internal/engine/engine.go` — `StartRun`, between lines 99 and 101:**
```
  if _, _, err := f.AcquireLock(run.ID, "execute-loop run (l00prite OS engine)", e.LeaseTTLSec); err != nil {
      return fmt.Errorf("could not acquire .l00prite lease to arm the run: %w", err)
  }

+ // Auto-checkpoint: commit EVERYTHING dirty (including .l00prite/ — go-git's checkout
+ // dirty-check is whole-tree with no path exemption; AcquireLock above just rewrote
+ // lock.json) so the run branch is created from a clean tree. This is the run's ONLY
+ // unprompted state mutation. Explicitly no stash: a commit is visible, recoverable,
+ // and survives on the run branch.
+ checkpointHash, err := AutoCheckpoint(e.Git, run.RepoRoot, run.ID)   // new func, tools.go
+ if err != nil {
+     _ = f.ReleaseLock(run.ID)
+     return fmt.Errorf("%w: %v", ErrCheckpointFailed, err)            // new sentinel, see below
+ }
+ if checkpointHash != "" {
+     _ = f.AppendLedger(LedgerEntry{
+         Timestamp: now, RunID: run.ID,
+         Goal:            "auto-checkpoint before run " + run.ID,
+         TriggeringEvent: "none",
+         Decision:        "auto-commit dirty worktree before creating the run branch",
+         CompletedWork:   "WIP checkpoint commit " + checkpointHash,
+         ChangedFiles:    "all uncommitted changes at Start time (including .l00prite/)",
+         EventStatus:     "not applicable",
+         Confidence:      "high — mechanical checkpoint; user work preserved in commit " + checkpointHash,
+         NextAction:      "run branch created on top of this checkpoint",
+         LockNote:        "checkpointed under the run's own lease",
+     })   // ledger write failure: append to run events / log, do not abort — mirror preflight.go:219's tolerance
+ }

  branch := runBranch(run.ID)
  if err := EnsureRunBranch(e.Git, run.RepoRoot, branch); err != nil {
```
Also define near `ErrBadState`:
```
+ // ErrCheckpointFailed: the pre-run auto-checkpoint commit failed; Start must not proceed.
+ var ErrCheckpointFailed = errors.New("checkpoint_failed")
```

**`cli-os/internal/engine/tools.go` — new function beside `CommitUnit` (tools.go:912):**
```
+ // AutoCheckpoint commits ALL dirty paths (tracked and untracked, .l00prite/ included) as a
+ // WIP checkpoint. Returns ("", nil) on an already-clean tree (both gitx backends' Commit
+ // contract). Never stashes; never proceeds past a failure.
+ func AutoCheckpoint(git gitx.Client, root, runID string) (string, error) {
+     git = gitOrDetect(git)
+     out, err := git.StatusPorcelain(root)
+     if err != nil { return "", fmt.Errorf("could not inspect the project's saved state: %w", err) }
+     if strings.TrimSpace(out) == "" { return "", nil }        // clean — nothing to checkpoint
+     return CommitUnit(git, root, "WIP: auto-checkpoint before run-"+runID)
+ }
```
(`CommitUnit` already does `AddAll` → `Commit`; both backends resolve a fallback commit identity — `gogit.go:182-187` synthesizes `l00prite-os@localhost`, and `execClient.Commit` documents an identity-missing retry — so the checkpoint cannot fail on a gitconfig-less on-device host for identity reasons.)

Leave `EnsureRunBranch`'s own clean-tree guard (tools.go:900-901) in place as a fail-closed re-check — after a successful checkpoint it always passes; if it ever fires, something raced the checkpoint and stopping is correct.

**`cli-os/internal/engine/preflight.go` — `checkGitReady` (lines 36-52) and its call site (line 288): yes, the dirty-tree Blocker MUST become a Note.** The dashboard disables Start entirely whenever `pf.Blockers` is non-empty (`dashboard.html:1175, 1185`), which would make the `StartRun` checkpoint code unreachable for exactly the users it exists for. Split `checkGitReady`'s three findings:
```
  func checkGitReady(git gitx.Client, root string) (blockers, notes []string) {
      ...
-     blockers = append(blockers, "working tree has uncommitted changes outside .l00prite/ (commit or stash them before starting an autonomous run): "+strings.Join(dirty, ", "))
+     notes = append(notes, fmt.Sprintf(
+         "This project has %d file(s) with unsaved changes. When you press Start, l00prite will save them first as a checkpoint (commit \"WIP: auto-checkpoint\") so nothing is lost.", len(dirty)))
      ...
  }
```
Keep "repository has no commits / not a git repository" and "git status failed" as **Blockers** (they are genuinely unstartable states) — but route their text through the translation layer below rather than concatenating `err.Error()` (preflight.go:40, 45 currently embed raw error text).
Call site (preflight.go:288):
```
- pf.Blockers = append(pf.Blockers, checkGitReady(e.Git, run.RepoRoot)...)
+ gb, gn := checkGitReady(e.Git, run.RepoRoot)
+ pf.Blockers = append(pf.Blockers, gb...)
+ pf.Notes = append(pf.Notes, gn...)
```

**`cli-os/internal/gateway/runs.go` — `HandleRunStart` (lines 182-189): error-translation layer, defense-in-depth.** Raw `err.Error()` must never reach `oaiError`'s message:
```
  if err := app.Engine.StartRun(...); err != nil {
      status, code := 400, "start_rejected"
      if errors.Is(err, engine.ErrBadState) { status, code = 409, "run_not_ready" }
+     if errors.Is(err, engine.ErrCheckpointFailed) { status, code = 409, "checkpoint_failed" }
-     oaiError(w, status, "Could not start run: "+err.Error(), "invalid_request_error", code)
+     app.audit("run.start.error", run.ID, err.Error())      // full technical detail → server log/audit only
+     oaiError(w, status, humanizeStartError(err), "invalid_request_error", code)
      return
  }
```
`humanizeStartError` maps typed causes to fixed plain-English strings and **falls through to a generic message for anything unrecognized** — this is what makes it defense-in-depth for corrupted repos, mid-rebase state, permission errors, disk-full, and every future unanticipated git/go-git failure:
```
+ func humanizeStartError(err error) string {
+     switch {
+     case errors.Is(err, engine.ErrCheckpointFailed):
+         return "l00prite tried to save your unsaved changes before starting, but the save failed. Nothing was changed and the run was not started. Details were logged for support."
+     case errors.Is(err, engine.ErrBadState):
+         return "The run can't start right now — the project isn't in a startable state. Try Rebuild pre-flight; if that doesn't help, details were logged for support."
+     default:
+         return "Something went wrong preparing the project for this run. Nothing was started. Details were logged for support."
+     }
+ }
```
No change needed in `dashboard.html`'s `mErr` (740-747): once the server only emits translated strings, verbatim display is correct. Audit the other `oaiError(..., "..."+err.Error(), ...)` sites in `runs.go` (preflight/create/approve handlers) for the same pattern while there.

**Explicit behavioral consequences to preserve and disclose:**
- The WIP checkpoint commit lands on the user's **current branch head** (checkout -B branches from HEAD), then the run branch is created on top of it. The user's work is never lost, but their branch advances by one commit. The run's plain-language report and the pre-flight Note must both say this ("we saved your unsaved changes as a checkpoint").
- **No auto-stash**, per spec: a stash is invisible to non-git users and easy to orphan; a commit is durable and shows up in history and the ledger.
- The auto-commit is the **only** unprompted state mutation added. If it fails: plain-English message (above), lock released, no checkout, no Start.

### 2.3 Complementary hardening (recommend, but do not substitute for the checkpoint)

`gogitClient.CheckoutNewBranch` (gogit.go:108-126) diverges from `git checkout -B` semantics: real git succeeds on a same-tree `-B` regardless of dirty tracked files; go-git's `MergeReset` path refuses. Even with the checkpoint in place, any future pre-checkout write reintroduces the bug on gogit only. Opus should add a regression test pinning the parity (gogit backend, tracked-dirty `.l00prite/` file, `CheckoutNewBranch` to a new branch at HEAD must succeed after checkpoint) and may optionally make `gogitClient.CheckoutNewBranch` behave like real git for the same-commit case (e.g. `Keep: true` when the target equals HEAD — evaluate carefully; `Force: true` is wrong, it would discard dirty worktree content).

---

## 3. What should and should not be automatic

**Automatic (no user prompt, no git vocabulary):**
- Cloning when a URL is pasted; registering the repo.
- Scaffolding `.l00prite/` at link time (Bug 1 fix b).
- Attaching read-only repo tools to chat when a repo is selected (Bug 1 fix a).
- The pre-run WIP auto-checkpoint commit of **all** dirty paths, with its hash logged to `ledger.md` (Bug 2 fix) — the single sanctioned unprompted mutation.
- Run-branch creation (`l00prite/run-<id>`), the generator/evaluator loop, allowlisted verification commands, per-unit commits — all already inside the confirmed Run.
- Translating every git/go-git/engine error into plain language before it reaches an API response or the dashboard.
- Crash recovery / stale-run disarm at pre-flight (already implemented, preflight.go:171-244).

**Never automatic (existing gates that must not be weakened by these fixes):**
- Starting a Run: the pre-flight display + typed in-session "EXECUTE" confirmation stays mandatory.
- Push, merge, deploy, credential use: per-action approval, fail-closed on timeout (existing engine behavior).
- Anything on the Autonomous-Edit Denylist or protocol files.
- Any write/execute capability in ordinary chat — the chat toolbox is read-only by construction.
- Deleting, stashing, resetting, or force-overwriting any user content: the checkpoint is a commit precisely because commits are additive; **no auto-stash, no `Force: true` checkout.**

---

## 4. Definition-of-Done gap analysis

**Bug 1 fix (read-only chat tools + link-time scaffold): PARTIAL against the Product DoD.**
- Meets: the model can genuinely answer "what's in this repo / what does X do" in casual chat (DoD 3, part of 6); the misleading empty-memory state disappears (DoD 1).
- Still missing, justified from evidence: (1) chat remains read-only by design — "type a plain-English goal and the app handles branching/loop/tests/commits" (DoD 3-4) still requires the user to cross into the Runs view and the EXECUTE confirmation flow; nothing in this fix bridges chat → Run creation, and no such bridge exists in the code today (`ingress.go` never touches `app.Engine`). (2) Injected memory for a just-linked repo is template boilerplate until a run populates it — the fix makes injection *function*, not *informative*. (3) Repo file contents now flow to the user's chosen model provider outside a governed Run; the DoD doesn't forbid this, but the UI copy (currently honest per verification point 5) must be updated or it becomes inaccurate in the other direction.

**Bug 2 fix (auto-checkpoint + Blocker→Note + error translation): PARTIAL against the Product DoD.**
- Meets: dirty worktrees are handled without the user knowing what a worktree is (DoD 4); the raw `worktree contains unstaged changes` string — and, via the fallback translator, *any* raw git text on this path — can no longer reach the user (DoD 5); the checkpoint is visible in the ledger (DoD 6, partially).
- Still missing, justified from evidence: (1) the run ends on branch `l00prite/run-<id>` and nothing anywhere in the codebase merges it back or explains the branch state to the user — a non-git user who runs a successful run still cannot get their changes "onto their project" without git knowledge. That gap predates this bug but the fix does not close it, so per the mandate: **this fix alone is NOT done** against DoD 4/6. (2) `ledger.md` is a Markdown file inside `.l00prite/`; the DoD's "see in plain language what changed" requires the dashboard run view to surface the checkpoint event too, not just the ledger file. (3) The translation layer as specified covers `HandleRunStart`; other `runs.go` handlers still concatenate `err.Error()` and need the same audit (flagged in §2.2) before DoD 5 is fully met for the Runs surface.

---

## 5. What remains unknown / next diagnostic steps

1. **Resolved disagreement (do not re-litigate):** Bug 2's original root cause (untracked scaffold files tripping go-git) was **refuted empirically** by the verification agent — go-git skips `merkletrie.Insert` (untracked) entries — and replaced with the tracked-`lock.json`-rewrite mechanism. The two diagnoses agree on symptom, propagation chain, exec-git immunity, and test gap; they disagree only on trigger, and the verifier's version is backed by two reproductions. Implement against the verifier's mechanism.
2. **Gitignored `.l00prite/` (live trace needed):** if a *user's own* `.gitignore` ignores `.l00prite/`, `lock.json` stays untracked (no bug 2 trigger), but `AddAll` (`git add -A` / go-git `AddOptions{All: true}`) will not add ignored files — the checkpoint would silently exclude them, and go-git's status handling of ignored files at checkout is unverified here. Before implementing, run a quick live test: gogit backend, `.gitignore` containing `.l00prite/`, dirty ignored `lock.json`, `CheckoutNewBranch` — confirm it passes (expected: ignored files are invisible to the dirty check) and that `AutoCheckpoint` returning `""` on an "only-ignored-files dirty" tree is handled (StatusPorcelain likely already omits ignored paths — verify for both backends).
3. **`AddWithOptions{All: true}` vs untracked files under gogit (live trace):** confirm go-git's `AddOptions{All: true}` stages untracked files (it should — but the checkpoint's correctness for a user's brand-new files depends on it; the existing `CommitUnit` tests may already cover this — check before writing new ones).
4. **Chat-tool secret hygiene (design decision for the maintainer):** the engine `Toolbox`'s read path protections (protocol-file hard-deny is about *writes*; does `read_file` deny anything?) need a read-side answer for chat: should `.env`/key material be readable by a chat model? Recommend deny-by-default patterns, but this is policy, not diagnosis — get a maintainer call.
5. **Review-gate reminder:** neither fix touches the two review-gated files (`.claude/commands/build-loop.md`, `scripts/validate-l00prite.js`). Keep it that way; `node scripts/validate-l00prite.js` must stay at 0 FAIL after all changes (the fixes are cli-os-only, so no validator impact is expected — verify anyway).
6. **On-device verification remains open:** as with prior APK work, no emulator ABI exists in this container; the gogit-backend fixes should be regression-tested in Go (forcing the gogit client explicitly, not via `Detect()`) since real-device verification isn't currently possible here.

---

## 6. Structured handoff for Opus — implementation checklist (in order)

**Bug 2 first (smaller blast radius, deterministic repro, unblocks on-device runs):**

1. `cli-os/internal/engine/tools.go` — add `AutoCheckpoint(git gitx.Client, root, runID string) (string, error)` beside `CommitUnit` (~line 912): StatusPorcelain → if clean return `("", nil)` → else `CommitUnit(git, root, "WIP: auto-checkpoint before run-"+runID)`. No stash. Leave `EnsureRunBranch` (889-907) unchanged.
2. `cli-os/internal/engine/engine.go` — define `var ErrCheckpointFailed = errors.New("checkpoint_failed")` near `ErrBadState`; in `StartRun`, insert between line 99 (`AcquireLock` success) and line 101 (`branch := ...`): call `AutoCheckpoint`; on error → `ReleaseLock` + return `fmt.Errorf("%w: %v", ErrCheckpointFailed, err)`; on non-empty hash → `f.AppendLedger(LedgerEntry{...})` with the checkpoint hash in `CompletedWork` (ledger failure: log, don't abort). **The commit must be after AcquireLock (engine.go:97) — a pre-flight-only checkpoint does not fix the bug because AcquireLock re-dirties lock.json after pre-flight.**
3. `cli-os/internal/engine/preflight.go` — change `checkGitReady` (36-52) to return `(blockers, notes []string)`: dirty-tree finding becomes a plain-language **Note** announcing the upcoming checkpoint; "no commits" and "status failed" stay Blockers but without raw `err.Error()` in the string. Update call site at line 288.
4. `cli-os/internal/gateway/runs.go` — `HandleRunStart` (182-189): stop concatenating `err.Error()`; add `humanizeStartError(err)` with cases for `ErrCheckpointFailed` / `ErrBadState` / default-generic; log the raw error via audit. Sweep the file's other `+err.Error()` `oaiError` calls the same way.
5. Tests: (a) `internal/engine` — gogit-forced (not `Detect()`) test: commit `.l00prite/lock.json`, dirty it, `StartRun` path (or `AutoCheckpoint`+`EnsureRunBranch`) succeeds and the WIP commit exists with the exact message; (b) same scenario must fail *without* the checkpoint (regression pin); (c) checkpoint-failure path returns `ErrCheckpointFailed` and does not create the branch; (d) preflight dirty-tree now yields a Note, not a Blocker; (e) `HandleRunStart` never emits a message containing "unstaged" / "checkout -B". Run `go test -race ./...`.

**Bug 1:**

6. New `cli-os/internal/gateway/chattools.go` (or `internal/repotools` shared package): `read_file`/`list_dir`/`search_files` tool schemas + local executors, repo-root-jailed with symlink containment (reuse engine's containment logic; do NOT reuse `engine.Toolbox`), byte caps, untrusted-content envelope on results, secret-path read-deny (pending §5.4 policy call).
7. `cli-os/internal/gateway/turn.go` — in `runTurn`, when the request resolved a registered repo: append the three tools to `req["tools"]` (compose with the existing `bridge.go:321` pattern) and add a bounded tool-execution loop (cap tool rounds; meter/ledger calls). Mirror on the streaming path in `ingress.go` (262-267 region) or document why streaming defers tool support.
8. `cli-os/internal/gateway/repos.go` (`HandleRepoRegister`, after the INSERT ~94-104) and `repos_clone.go` (`HandleRepoClone`, after clone+INSERT ~118-133): call `engine.Files{Root: absRoot}.Scaffold(projectName, "")`; include a plain-language "memory folder added" note in the response; `RepoFreshness` will then report a real status.
9. `cli-os/public/dashboard.html` — update `play-repo` title (line 339), register-modal copy (~940), Playground empty-state (~1366), and the badge logic (~632) to reflect: repo selection now grants read-only file browsing + memory injection, and linking scaffolds memory immediately.
10. Tests: gateway test proving tools are attached only when a repo is selected and are read-only (a `write_file` name in a model tool-call must be rejected); jail test (path traversal + symlink escape rejected); register/clone tests asserting `.l00prite/` exists afterward and existing files are never overwritten.

**Both bugs:** finish with `go test -race ./...`, `node scripts/validate-l00prite.js` (0 FAIL), `node scripts/l00prite-doctor.js .` (HEALTHY); zero edits to `.claude/commands/build-loop.md` and `scripts/validate-l00prite.js`; feature branch + PR per Section 6 branch policy; append the implementation run to CLAUDE.md's Run Ledger.
