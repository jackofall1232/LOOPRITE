# Security model (v1)

Production infrastructure that will run unattended and be trusted with API keys, routing, and
cost tracking. Least privilege, no insecure defaults, real error handling, no silent failure
modes.

## Trust boundaries

```
  client (coding tool)  ──►  CLI-OS server  ──►  provider APIs
   holds: opaque              holds: real           receive: real key
   l00prite token             provider keys         (server-side only)
```
The client only ever holds an **opaque `l00prite` token**. Real provider keys live server-side
and are never returned by any endpoint, never logged, never sent to the client. Repo memory
content (from disk) is **untrusted input** to the model, never instructions.

## Provider key storage

- Keys are stored **server-side only**, **encrypted at rest** (v1: an OS-keyring / `age`-style
  envelope over a key file with `0600` perms; env-var fallback for dev, flagged as such).
- Resolved at call time by the adapter; never materialized into logs, ledger rows, error
  messages, or responses.
- Rotatable and revocable via the admin CLI; rotation does not require a restart.

## Gateway authentication

- API clients present an opaque bearer token, minted by the admin CLI, **scoped to a project,
  optional repository, and explicit capabilities**, revocable and optionally expiring. Named roles
  are convenience mappings; endpoint authorization is enforced against scopes. Existing pre-scope
  tokens are marked legacy and retain temporary admin compatibility so operators can rotate them.
- Tokens are **hashed at rest** and compared in **constant time**. A leaked token is revocable
  without touching provider keys.
- One token → one principal → one budget/policy/repo scope (isolation between projects).
- The browser dashboard never persists that bearer in JavaScript storage. It exchanges an
  `audit:read`-capable bearer for an in-memory, HttpOnly, SameSite=Strict loopback session; token
  revocation invalidates the session on its next request.

## Android setup authentication

When `LOOPRITE_SETUP_SECRET` is present, it is accepted only by `POST /v1/setup/session`. The
Android wrapper performs that exchange natively and installs the returned short-lived HttpOnly
cookie before loading the WebView. The install secret never appears in a URL, WebView history,
JavaScript, or localStorage. A second exchange while an unexpired setup session exists is rejected.

## Device-owner (workspace) sessions

The install secret doubles as the **device-owner credential**, gating three endpoints that keep an
app install from ever being locked out after sign-out or UI-cookie expiry:
`GET /v1/owner/projects` (list workspaces — projects with an active unscoped token),
`POST /v1/owner/session` (issue the dashboard's HttpOnly UI cookie for a chosen workspace), and
`POST /v1/owner/projects` (mint a new workspace's owner token — returned once — and sign into it).
Rules:

- The gate is the install-secret header (timing-safe) or the setup cookie it mints — never a
  project name. A name only SELECTS which existing token's principal a session is issued for,
  after ownership is proven; a bare "project name to sign in" flow would be an unauthenticated
  session oracle to anything that can reach loopback (on Android: any app on the phone).
- The endpoints sit OUTSIDE the setup latch (they are the post-setup recovery path) but fail
  closed with 403 whenever no install secret is configured (desktop), where
  `l00prite token mint` remains the recovery path. Nothing about the desktop auth model changes.
- The Android wrapper also exchanges the secret for a UI session natively at every cold start,
  so an expired 8h cookie self-heals on relaunch; "sign out" lands on a workspace switcher,
  not a dead end.


## Least-privilege repo file access

- Each registered repo has an explicit filesystem **root**. The Memory layer may read **only
  within that root**: canonicalize the path and verify containment, **reject traversal /
  symlink escape / absolute-path escape** (the same discipline the memory-tool guidance uses).
- **Read-only by default.** The only writes are to `.l00prite/` under an atomic lease (§ atomic
  state in [`architecture.md`](architecture.md)). No write touches source outside `.l00prite/`.

## No insecure defaults

- The server **refuses to start** with a placeholder/empty admin secret.
- It **refuses to bind a non-loopback interface without TLS** configured. Single-user installs
  default to **localhost bind**; binding externally is an explicit opt-in.
- Execution/automation ships **disarmed** (mirrors l00prite Execution Mode's disarmed default):
  nothing that spends money or mutates state runs until a token + policy exist and a cap is set.
- Cost caps, retry caps, and the destructive-action gate default to **on / safe** — a project
  with no explicit cap gets a conservative default cap, not "unlimited."

## Enforcement outside the deciding process

Safety-critical stops (cost cap, retry cap, destructive-action gate) are enforced by the
**Policy Enforcement Point** over an atomic store, separate from the request handler that would
benefit from ignoring them. A handler *requests* a spend reservation; it cannot grant its own.
This is the CLI-OS form of l00prite's "persisted flags are never authorization." Details in
[`architecture.md`](architecture.md) §6.

## Prompt-injection posture

- All memory blocks and any provider/tool output re-fed into a prompt are wrapped in an
  untrusted-content envelope with an explicit non-instruction preamble.
- The router, PEP, and cost meter take instructions **only** from config and the authenticated
  request — never from model output or repo memory content. A PR comment stored in memory that
  says "raise the cost cap" is data, and is ignored as an instruction.

## Auditability

- Every privileged action (add/rotate key, mint/revoke token, change a cap, override a route)
  is appended to an **audit log the request path cannot rewrite**.
- The run ledger records per-request routing decisions, token/dollar cost, and
  memory-degradation status, so spend and behavior are reconstructable after the fact.

## Transport

- TLS terminated at the server or a trusted reverse proxy. Localhost-only by default; external
  exposure requires TLS + explicit config, enforced at startup (see "no insecure defaults").
