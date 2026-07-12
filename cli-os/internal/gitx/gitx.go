// Package gitx is the engine's git seam (docs/android-architecture.md §4 G4): a Client interface
// with the ten primitives the run engine actually needs (clone, rev-parse HEAD, status
// --porcelain, checkout -B, add -A, commit, diff HEAD, log, show, and a raw passthrough), plus two
// implementations. Detect picks the exec-git implementation when a "git" binary is on PATH — this
// is byte-for-byte the behavior every desktop host had before this package existed — and falls
// back to a pure-Go go-git implementation only when no git binary exists at all (stock Android
// ships neither git nor ssh). Callers hold a Client (Engine.Git / Toolbox.Git) rather than calling
// exec.Command("git", ...) directly, so "no git binary" degrades the engine instead of breaking it.
package gitx

import (
	"context"
	"errors"
	"os/exec"
)

// Client is the git seam the run engine drives every repository operation through. Paths ("repo")
// are always the target repository's root; this package never assumes a global cwd.
type Client interface {
	// Kind reports which implementation this is: "exec" (shells out to the git binary) or "gogit"
	// (pure-Go go-git fallback). Callers use this to decide whether an operation this seam does
	// not cover (arbitrary git subcommands, ssh transport) is available at all.
	Kind() string

	// Clone performs a shallow clone of url into dest (depth commits of history). Only https and
	// local-filesystem-path URLs are guaranteed to work under every Kind(); the exec implementation
	// also supports ssh.
	Clone(ctx context.Context, url, dest string, depth int) error

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
}

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
