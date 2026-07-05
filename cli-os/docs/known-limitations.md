# Known limitations

Honest notes on where l00prite CLI-OS is deliberately simpler than a production system would be. These
are conscious scope decisions, not oversights — each says what the limitation is, why it's acceptable
today, when it stops being acceptable, and what the fix would look like.

## Single-tier authentication (no scoped tokens)

**What it is.** Every gateway token is equally privileged. Any valid project token — the same kind of
token a client uses for `/v1/chat/completions` — can also perform provider lifecycle actions: add,
rotate, or remove providers and edit per-provider model selection through the `/v1/providers/*`
endpoints. There is no separate lower-privilege token for routine chat traffic versus a higher-privilege
token for administrative actions; the auth layer only asks "is this a valid, non-revoked token?"

**Why it's this way.** This is a deliberate scope decision for the current proof-of-concept stage. The
expected deployment is a **single operator who holds all the tokens themselves** — a self-hosted instance
on a loopback bind or a trusted private network, where the person minting tokens and the person managing
providers are the same person. In that setting a role/scope system adds schema, minting UX, and
migration complexity for no real security gain, so building it now would be premature.

**When this becomes a real problem.** The moment **more than one token is in circulation with different
trust levels**. For example: a token handed to a teammate, a CI job, a coding agent, or a secondary
device — any holder who *should* be able to make chat requests but should *not* be able to rotate or
delete your provider keys. At that point single-tier auth means a leaked or over-shared low-trust token
is also a provider-management (and, via a caller-supplied `base_url`, an egress) credential.

**What the fix looks like (not implemented — noted for a future reader).** A scoped / role-based token
system: e.g. a `can_manage_providers` boolean (or distinct token types) set at mint time, checked at the
same authentication layer that today only validates the token. The natural enforcement point is
`requireToken` in [`internal/gateway/providers.go`](../internal/gateway/providers.go) — where the
`/v1/providers/*` handlers currently accept any valid principal — with the flag stored alongside the
existing token fields in [`internal/security/tokens.go`](../internal/security/tokens.go). Provider
management would then require a management-scoped token while chat requests keep working with an ordinary
one. Until then, treat every token as an admin credential.
