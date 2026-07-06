package gitx

// gogitClient is the pure-Go git fallback used automatically when no "git" binary is on PATH
// (stock Android ships neither git nor ssh — docs/android-architecture.md §4 G4). It covers
// exactly what the run engine needs to keep functioning without a git binary: HTTPS/local-path
// clone, status, branch, commit (with a synthetic fallback identity — a first-boot device has no
// gitconfig at all), and a diff good enough for the reviewer role.
//
// It deliberately does NOT support: ssh transport (no ssh binary or library is wired in here —
// ssh-style URLs are rejected up front with a clear error instead of hanging), or arbitrary git
// subcommands (Raw always returns ErrRawUnsupported; the model-facing git_command tool surfaces
// this as "core operations still work, passthrough does not" — see engine/tools.go's gitCommand).
import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type gogitClient struct{}

func (gogitClient) Kind() string { return "gogit" }

// isLocalPath reports whether url looks like a plain filesystem path rather than an https:// URL
// or an scp-like ssh spec (user@host:path) — go-git's own NewEndpoint resolves any non-scheme,
// non-scp-like string as a "file" transport via filepath.Abs, which is exactly what we want to
// allow (used by this codebase's own local-path clone tests) while still rejecting ssh.
func isLocalPath(url string) bool {
	return !strings.Contains(url, "://") && !strings.Contains(url, "@")
}

func (c gogitClient) Clone(ctx context.Context, url, dest string, depth int) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") && !isLocalPath(url) {
		return fmt.Errorf("gitx/gogit: %q needs ssh, which requires the git binary (only https:// and local-path URLs work without one)", url)
	}
	_, err := git.PlainCloneContext(ctx, dest, false, &git.CloneOptions{URL: url, Depth: depth})
	return err
}

func (c gogitClient) RevParseHead(repo string) (string, error) {
	r, err := git.PlainOpen(repo)
	if err != nil {
		return "", err
	}
	head, err := r.Head()
	if err != nil {
		return "", err // includes the "reference not found" case for an unborn/empty repository
	}
	return head.Hash().String(), nil
}

// StatusPorcelain renders worktree.Status() in a porcelain-LIKE format: "XY path" per changed
// file, sorted for determinism. This is NOT guaranteed byte-identical to `git status
// --porcelain`'s exact status-code semantics (go-git's StatusCode set is a close but not perfect
// mirror) — every caller in this codebase (EnsureRunBranch's clean-tree check,
// dirtyPathsOutsideL00prite's path filter) only depends on emptiness and the "path starts at
// column 3" shape, both of which this preserves.
func (c gogitClient) StatusPorcelain(repo string) (string, error) {
	st, err := status(repo)
	if err != nil {
		return "", err
	}
	if st.IsClean() {
		return "", nil
	}
	var b strings.Builder
	for _, p := range sortedStatusPaths(st) {
		s := st[p]
		fmt.Fprintf(&b, "%c%c %s\n", byte(s.Staging), byte(s.Worktree), p)
	}
	return b.String(), nil
}

func status(repo string) (git.Status, error) {
	r, err := git.PlainOpen(repo)
	if err != nil {
		return nil, err
	}
	wt, err := r.Worktree()
	if err != nil {
		return nil, err
	}
	return wt.Status()
}

func sortedStatusPaths(st git.Status) []string {
	paths := make([]string, 0, len(st))
	for p := range st {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// CheckoutNewBranch matches `git checkout -B name`: create the branch at HEAD if absent, or RESET
// it to HEAD if it already exists (checkout -B never fails because the branch already exists —
// that's the whole point of -B over a plain checkout -b), then switch to it.
func (c gogitClient) CheckoutNewBranch(repo, name string) error {
	r, err := git.PlainOpen(repo)
	if err != nil {
		return err
	}
	head, err := r.Head()
	if err != nil {
		return err
	}
	ref := plumbing.NewBranchReferenceName(name)
	if err := r.Storer.SetReference(plumbing.NewHashReference(ref, head.Hash())); err != nil {
		return err
	}
	wt, err := r.Worktree()
	if err != nil {
		return err
	}
	return wt.Checkout(&git.CheckoutOptions{Branch: ref})
}

func (c gogitClient) AddAll(repo string) error {
	r, err := git.PlainOpen(repo)
	if err != nil {
		return err
	}
	wt, err := r.Worktree()
	if err != nil {
		return err
	}
	return wt.AddWithOptions(&git.AddOptions{All: true})
}

// Commit stages nothing itself (AddAll already ran) and commits with the resolved identity.
// "Nothing to commit" (a clean tree after AddAll) is not an error: it returns ("", nil), the same
// contract execClient.Commit honors.
func (c gogitClient) Commit(repo, message string) (string, error) {
	r, err := git.PlainOpen(repo)
	if err != nil {
		return "", err
	}
	wt, err := r.Worktree()
	if err != nil {
		return "", err
	}
	st, err := wt.Status()
	if err != nil {
		return "", err
	}
	if st.IsClean() {
		return "", nil
	}
	sig := commitSignature(r)
	hash, err := wt.Commit(message, &git.CommitOptions{Author: sig})
	if err != nil {
		// st.IsClean() above already filters the common case, but a staged-then-reverted change
		// (tree matches HEAD again after AddAll) can still reach wt.Commit with nothing to record;
		// go-git surfaces that as ErrEmptyCommit rather than an error worth propagating — same
		// "nothing to commit" contract execClient.Commit honors.
		if err == git.ErrEmptyCommit {
			return "", nil
		}
		return "", err
	}
	return hash.String(), nil
}

// commitSignature prefers the repository's configured identity — local (repo-scoped) config
// overriding global, matching git's own precedence — falling back to a clearly-synthetic identity
// for whichever of name/email neither scope sets. A first-boot Android host has no gitconfig at
// all — the same rationale execClient.Commit documents for its exec-git identity-missing retry
// applies here, just resolved up front instead of via a retry-after-failure (go-git does not error
// the way real git does on a missing identity, so there's no failure to retry after; this is the
// equivalent proactive fallback). Name and email are resolved independently: a host with only
// user.email configured (no user.name) must not have the email silently discarded.
func commitSignature(r *git.Repository) *object.Signature {
	name, email := "l00prite-os", "l00prite-os@localhost"
	for _, scope := range []config.Scope{config.GlobalScope, config.LocalScope} {
		cfg, err := r.ConfigScoped(scope)
		if err != nil {
			continue
		}
		if n := strings.TrimSpace(cfg.User.Name); n != "" {
			name = n
		}
		if e := strings.TrimSpace(cfg.User.Email); e != "" {
			email = e
		}
	}
	return &object.Signature{Name: name, Email: email, When: time.Now()}
}

// DiffHead reports what changed relative to HEAD. A faithful unified diff (hunk-level, worktree +
// index vs HEAD) needs a text-diff algorithm go-git does not expose for this comparison; rather
// than fabricate diff-looking output that could mislead the reviewer role about exactly what
// changed, this reports the same file-level status worktree.Status() already computes — enough to
// know WHICH files changed and HOW (added/modified/deleted/renamed), clearly labeled as a summary
// rather than a real diff. Callers cap this at 60000 bytes and feed it to a reviewer model, where a
// good summary is an accepted approximation (see docs/android-architecture.md §4 G4).
func (c gogitClient) DiffHead(repo string) (string, error) {
	st, err := status(repo)
	if err != nil {
		return "", err
	}
	if st.IsClean() {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("gitx/gogit summary (unified diff unavailable):\n")
	for _, p := range sortedStatusPaths(st) {
		s := st[p]
		fmt.Fprintf(&b, "%c%c %s\n", byte(s.Staging), byte(s.Worktree), p)
	}
	return b.String(), nil
}

func (c gogitClient) Raw(ctx context.Context, repo string, args ...string) (string, error) {
	return "", ErrRawUnsupported
}
