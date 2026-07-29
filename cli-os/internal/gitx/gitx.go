// Package gitx is the engine's git seam (docs/android-architecture.md §4 G4): a Client interface
// with the primitives the run engine actually needs (clone, rev-parse HEAD, status --porcelain,
// checkout -B, add -A, force-add specific paths, commit, current branch, diff HEAD, log, show, and
// a raw passthrough), plus two implementations. Detect picks the exec-git implementation when a
// "git" binary is on PATH — this
// is byte-for-byte the behavior every desktop host had before this package existed — and falls
// back to a pure-Go go-git implementation only when no git binary exists at all (stock Android
// ships neither git nor ssh). Callers hold a Client (Engine.Git / Toolbox.Git) rather than calling
// exec.Command("git", ...) directly, so "no git binary" degrades the engine instead of breaking it.
package gitx

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"
)

// redactSecrets replaces each non-empty secret with "[redacted]" in s. Defense in depth: an
// implementation must not put a token where an error could carry it in the first place, but any
// error text surfaced from a push is scrubbed through this before leaving the package regardless.
func redactSecrets(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return s
}

// TokenUsableFor reports whether auth may serve as the push transport for a remote at remoteURL —
// i.e. whether Push will actually attach the credential (an https URL on auth.Host). Callers outside
// this package (scaffold_pr.go) use it to decide token-vs-ambient BEFORE calling Push, so their
// probe/push/PR-lookup paths all agree with what Push itself does for the same remote (rather than
// one path taking the token route while another falls back to ambient).
func TokenUsableFor(remoteURL string, auth *PushAuth) bool {
	_, ok := credentialScope(remoteURL, auth)
	return ok
}

// pushRefspec is the single explicit refspec Push uses on both backends: exactly
// refs/heads/<branch>:refs/heads/<branch> — never a bare branch (ambiguous), never a wildcard.
func pushRefspec(branch string) string {
	return "refs/heads/" + branch + ":refs/heads/" + branch
}

// credentialScope decides whether auth may be attached to a push whose remote is remoteURL, and
// returns the "https://<host>/" prefix an http.<url>.extraheader key scopes to when so. A credential
// is attached ONLY to an https URL whose host matches auth.Host (case-insensitive) — so a
// github.com token is NEVER transmitted to any other origin: not an ssh remote, not a different
// (or attacker-controlled) https host that happens to share a project, and not a cleartext http
// URL. In every non-matching case Push silently falls back to ambient/unauthenticated credentials
// rather than leaking the token or failing outright. A nil/empty auth or an empty auth.Host
// (fail-closed) never attaches.
func credentialScope(remoteURL string, auth *PushAuth) (string, bool) {
	if auth == nil || auth.Token == "" || auth.Host == "" {
		return "", false
	}
	u, err := url.Parse(remoteURL)
	// Match on Hostname() (no port) so an explicit-port github URL (e.g. https://github.com:443/…)
	// still matches auth.Host ("github.com"); the scope keeps u.Host (with any port) so the
	// extraheader is bound to exactly the host:port git will request.
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || !strings.EqualFold(u.Hostname(), auth.Host) {
		return "", false
	}
	return "https://" + u.Host + "/", true
}

// Client is the git seam the run engine drives every repository operation through. Paths ("repo")
// are always the target repository's root; this package never assumes a global cwd.
type Client interface {
	// Kind reports which implementation this is: "exec" (shells out to the git binary) or "gogit"
	// (pure-Go go-git fallback). Callers use this to decide whether an operation this seam does
	// not cover (arbitrary git subcommands, ssh transport) is available at all.
	Kind() string

	// Clone performs a shallow clone of url into dest (depth commits of history). Only https and
	// local-filesystem-path URLs are guaranteed to work under every Kind(); the exec implementation
	// also supports ssh. A non-nil auth attaches an HTTPS credential under exactly the PushAuth
	// host-scoping rules Push uses (see credentialScope): the token goes ONLY to an https URL on the
	// credential's own host, never to an ssh URL, a foreign host, or a local path — those clone
	// unauthenticated. This is what lets an on-device (gogit) install clone a PRIVATE github.com
	// repo with the project's connected GitHub token.
	Clone(ctx context.Context, url, dest string, depth int, auth *PushAuth) error

	// RevParseHead returns the hash of HEAD. It errors if the repository has no commits yet
	// (an "unborn" HEAD) or repo is not a git repository at all.
	RevParseHead(repo string) (string, error)

	// CurrentBranch returns the name of the currently checked-out branch, or "" (with a nil
	// error) on a detached HEAD — never an error just because there's no branch name to report.
	// Read-only; added for callers that need to report (not restore) which branch a new branch
	// was created from.
	CurrentBranch(repo string) (string, error)

	// StatusPorcelain reports the working tree status. An empty string means a clean tree; callers
	// in this codebase never rely on anything beyond that emptiness check and the per-line "XY
	// path" shape (see gogit's doc comment on the caveats of its rendering).
	StatusPorcelain(repo string) (string, error)

	// CheckoutNewBranch creates branch name at the current HEAD and switches to it, or — if it
	// already exists — resets it to HEAD and switches to it (the semantics of `git checkout -B`).
	CheckoutNewBranch(repo, name string) error

	// AddAll stages every change in the working tree (the semantics of `git add -A`).
	AddAll(repo string) error

	// AddPaths force-stages the given repo-relative paths, bypassing .gitignore (the semantics
	// of `git add -f`) — for generated content that must land in the next commit regardless of
	// the target repo's own ignore rules. A no-op for an empty paths slice.
	AddPaths(repo string, paths []string) error

	// Commit commits the currently staged changes with message and returns the new commit hash.
	// "Nothing staged to commit" is not an error: it returns ("", nil).
	Commit(repo, message string) (string, error)

	// DiffHead reports what changed in the working tree (plus anything staged) relative to HEAD,
	// for the reviewer role. Under Kind()=="gogit" this may be a file-status summary rather than a
	// hunk-level unified diff — see gogitClient.DiffHead's doc comment for why and what callers
	// should expect.
	DiffHead(repo string) (string, error)

	// Log returns one line per commit, most-recent-first, formatted as "<7-char-abbrev-hash> <first
	// line of the commit message>" (git's own --oneline shape), for at most limit commits. A limit
	// <= 0 is clamped to a sane built-in default (20) rather than treated as zero or unlimited —
	// a bad or absent limit must never silently produce an empty response or an unbounded one. An
	// empty repository (no commits yet — the same unborn-HEAD case RevParseHead's doc comment
	// describes) is NOT an error: it returns ("", nil).
	Log(repo string, limit int) (string, error)

	// Show resolves ref to a single commit and returns a short header (hash, author name and email,
	// author date, full commit message) followed by a real unified diff of that commit against its
	// first parent. ref is whatever a single git revision spec means to the underlying
	// implementation (a full hash, an abbreviated hash, HEAD, etc.). For a root commit (no parent to
	// diff against), the diff section is omitted in favor of a plain "(root commit — no parent to
	// diff against)" note rather than a fabricated diff — see gogitClient.Show's doc comment for the
	// direction subtlety in how that real diff is computed.
	Show(repo string, ref string) (string, error)

	// Raw is an arbitrary git-subcommand passthrough (what the model-facing git_command tool uses
	// when Kind()=="exec"). The gogit implementation always returns ErrRawUnsupported: go-git has
	// no notion of "run any git subcommand", only the specific operations above.
	Raw(ctx context.Context, repo string, args ...string) (string, error)

	// RemoteURL returns the PUSH URL for remote (e.g. "origin") — where a push actually goes, which
	// is what credential scoping and PR-target owner/repo resolution must key off. Under Kind()=="exec"
	// that is `git remote get-url --push` (remote.<name>.pushurl if set, else the fetch URL); the
	// gogit backend has no separate push URL, so it returns its single configured URL (which go-git
	// uses for both fetch and push). Read-only; errors if the remote does not exist.
	RemoteURL(repo, remote string) (string, error)

	// Push uploads the single ref refs/heads/<branch> to refs/heads/<branch> on remote. It is NEVER
	// a force push, NEVER a wildcard/other refspec, and NEVER mutates remote config. A nil auth uses
	// ambient credentials (the host's git credential helpers / ssh agent under Kind()=="exec"; no
	// authentication under Kind()=="gogit", which suffices only for a local-path remote and is
	// refused by any real host like github.com). A non-nil auth carries an HTTPS token used
	// in-memory only — see PushAuth. Returns nil when the remote is already up to date.
	//
	// Under Kind()=="gogit" a shallow clone cannot push reliably, so Push fails closed with
	// ErrShallowPush (the remote is never touched) rather than corrupting it.
	Push(ctx context.Context, repo, remote, branch string, auth *PushAuth) error
}

// PushAuth carries an HTTPS credential for Push. Token is secret and MUST NEVER appear in a
// command's argv (/proc/*/cmdline is world-readable), be written to .git/config or any file, or be
// echoed in an error/log — implementations inject it only as an in-memory or host-scoped,
// env-only HTTP Authorization header, and redact it from any error text. Username is the HTTP basic
// username paired with the token; for a GitHub personal access token it is conventionally
// "x-access-token" (any non-empty value works — GitHub authenticates on the token).
type PushAuth struct {
	Username string
	Token    string
	// Host is the host the credential is valid for (e.g. "github.com"). Push attaches the token
	// ONLY to an https remote whose host matches this, so a credential can never be sent to a
	// different origin. An empty Host fail-closes: the token is never attached.
	Host string
}

// token is a nil-safe accessor so redaction code can pass auth.token() unconditionally.
func (a *PushAuth) token() string {
	if a == nil {
		return ""
	}
	return a.Token
}

// ErrShallowPush is returned by the gogit backend's Push when the repository is a shallow clone:
// go-git cannot reliably push shallow history, so Push refuses rather than risk a corrupt remote.
// The remedy is to re-clone the repo at full depth (the dashboard's clone path does this for the
// gogit backend); the message says so.
var ErrShallowPush = errors.New("gitx: this copy has shallow history and cannot push; re-clone it (full depth) from the dashboard to enable pushing")

// defaultLogLimit is the commit count Log uses when called with limit <= 0 (see Log's doc
// comment). Shared by both implementations so the clamp is defined in exactly one place.
const defaultLogLimit = 20

// ErrRawUnsupported is returned by the gogit implementation's Raw method: there is no git binary
// to pass arbitrary subcommands through to, and go-git itself has no generic subcommand runner.
var ErrRawUnsupported = errors.New("gitx: raw git passthrough is not supported without a git binary on this host; " +
	"core operations (status, branch, commit, diff) still work via the built-in pure-Go git")

// detectOnce resolves the git binary's PATH presence into a Client. Whether one exists never
// changes for the lifetime of the gateway process, so re-running exec.LookPath on every Detect
// call (every clone request, every engine iteration) would be a pure redundant filesystem scan.
func detectOnce() Client {
	if _, err := exec.LookPath("git"); err == nil {
		return execClient{}
	}
	return gogitClient{}
}

// detected is resolved once at process start; see detectOnce. A package-internal var (rather than
// being inlined into Detect) so gitx_test.go can force re-detection after mutating PATH — real
// callers only ever go through Detect.
var detected = detectOnce()

// Detect returns the Client selected at process start: the exec implementation when a "git"
// binary was on PATH (every desktop host, and this is exactly what every caller did before this
// package existed — zero behavior change there), else the pure-Go go-git fallback (Android's
// usual case: no git, no ssh binary on the device).
func Detect() Client {
	return detected
}

// NewGogitClient returns the pure-Go go-git implementation directly, regardless of whether a git
// binary is on PATH. gogitClient is unexported (Detect is meant to be the only production entry
// point), but Detect()'s choice is cached once at process start (see detectOnce/detected) and does
// not react to a PATH mutated mid-test — so callers outside this package that need to exercise the
// gogit code path deterministically (e.g. internal/engine tests constructing a Toolbox with
// Toolbox.Git forced to gogit) cannot get there through Detect() alone. This is that seam.
func NewGogitClient() Client { return gogitClient{} }
