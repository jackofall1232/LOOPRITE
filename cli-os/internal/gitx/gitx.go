// Package gitx is the engine's git seam (docs/android-architecture.md §4 G4): a Client interface
// with the eight primitives the run engine actually needs (clone, rev-parse HEAD, status
// --porcelain, checkout -B, add -A, commit, diff HEAD, and a raw passthrough), plus two
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

	// Raw is an arbitrary git-subcommand passthrough (what the model-facing git_command tool uses
	// when Kind()=="exec"). The gogit implementation always returns ErrRawUnsupported: go-git has
	// no notion of "run any git subcommand", only the specific operations above.
	Raw(ctx context.Context, repo string, args ...string) (string, error)
}

// ErrRawUnsupported is returned by the gogit implementation's Raw method: there is no git binary
// to pass arbitrary subcommands through to, and go-git itself has no generic subcommand runner.
var ErrRawUnsupported = errors.New("gitx: raw git passthrough is not supported without a git binary on this host; " +
	"core operations (status, branch, commit, diff) still work via the built-in pure-Go git")

// Detect chooses the exec implementation when a "git" binary is on PATH (every desktop host, and
// this is exactly what every caller did before this package existed — zero behavior change there),
// else the pure-Go go-git fallback (Android's usual case: no git, no ssh binary on the device).
func Detect() Client {
	if _, err := exec.LookPath("git"); err == nil {
		return execClient{}
	}
	return gogitClient{}
}
