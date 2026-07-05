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

- Clients present a single opaque bearer token, minted by the admin CLI, **scoped to a project
  + policy**, revocable, optionally expiring.
- Tokens are **hashed at rest** and compared in **constant time**. A leaked token is revocable
  without touching provider keys.
- One token → one principal → one budget/policy/repo scope (isolation between projects).

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
