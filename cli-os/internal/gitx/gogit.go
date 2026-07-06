package gitx

// gogitClient is the pure-Go git fallback used automatically when no "git" binary is on PATH
// (stock Android ships neither git nor ssh — docs/android-architecture.md §4 G4). It covers
// exactly what the run engine needs to keep functioning without a git binary: HTTPS/local-path
// clone, status, branch, commit (with a synthetic fallback identity — a first-boot device has no
// gitconfig at all), a diff good enough for the reviewer role, and read-only history (log, show)
// good enough for the model-facing git_command tool's narrow gogit subset (see
// engine/tools.go's gitCommand / gitCommandGogit).
//
// It deliberately does NOT support: ssh transport (no ssh binary or library is wired in here —
// ssh-style URLs are rejected up front with a clear error instead of hanging), or arbitrary git
// subcommands (Raw always returns ErrRawUnsupported; the model-facing git_command tool serves only
// its conservative status/diff/log/show subset under this Kind() and refuses everything else with
// "core operations still work, passthrough does not" — see engine/tools.go's gitCommand).
import (
	"context"
	"fmt"
	"io"
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

// firstLine returns the first line of a (possibly multi-line) commit message, for Log's
// one-line-per-commit rendering.
func firstLine(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return msg[:i]
	}
	return msg
}

// Log walks history from HEAD via (*git.Repository).Log (depth-first, the default order — close
// enough to git's own most-recent-first traversal for this seam's purposes) and renders at most
// limit commits as "<7-char-abbrev-hash> <first line of message>", one per line, most-recent-
// first. limit <= 0 is clamped to defaultLogLimit (see the Client.Log doc comment). An empty
// repository (HEAD unborn, the same case RevParseHead documents) is not an error: it returns
// ("", nil), the same contract execClient.Log honors for its own empty-repo case.
func (c gogitClient) Log(repo string, limit int) (string, error) {
	if limit <= 0 {
		limit = defaultLogLimit
	}
	r, err := git.PlainOpen(repo)
	if err != nil {
		return "", err
	}
	head, err := r.Head()
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return "", nil
		}
		return "", err
	}
	iter, err := r.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return "", fmt.Errorf("gitx/gogit: log: %w", err)
	}
	defer iter.Close()

	var b strings.Builder
	for n := 0; n < limit; n++ {
		commit, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("gitx/gogit: log: %w", err)
		}
		fmt.Fprintf(&b, "%s %s\n", commit.Hash.String()[:7], firstLine(commit.Message))
	}
	return b.String(), nil
}

// Show resolves ref (via (*git.Repository).ResolveRevision, which understands full/abbreviated
// hashes, HEAD, branch/tag names, and the "~"/"^" suffixes — the standard revision syntax go-git
// implements) to a commit and renders a short header followed by a real unified diff of that
// commit against its first parent.
//
// The diff direction matters: it is computed as parent.Patch(commit), NOT commit.Patch(parent).
// (*object.Commit).Patch(to) renders the diff FROM the receiver's tree TO to's tree (confirmed
// against go-git's own commit.go: Patch calls PatchContext, which does `fromTree, _ :=
// c.Tree(); ...; return fromTree.PatchContext(ctx, toTree)` — receiver is "from", argument is
// "to"). Concretely: a file newly ADDED in the shown commit does not exist in the parent's tree,
// so for that addition to render as an ADDITION (plus-lines, "new file mode", diff against
// /dev/null) rather than a DELETION, the parent (the "before", without the file) must be the
// receiver and the shown commit (the "after", with the file) must be the argument — i.e.
// parent.Patch(commit). Getting this backwards would silently invert every add/delete in the
// rendered patch while still "looking like" a plausible diff, which is exactly the kind of
// mistake this seam's honesty discipline (see DiffHead's doc comment) exists to avoid.
//
// For a root commit (no parent), go-git exposes no empty-tree object to honestly diff against
// here, so rather than fabricate a diff-looking summary, the diff section is replaced with a plain
// "(root commit — no parent to diff against)" note — the same never-fabricate discipline
// DiffHead's doc comment applies to its own limitation.
func (c gogitClient) Show(repo string, ref string) (string, error) {
	r, err := git.PlainOpen(repo)
	if err != nil {
		return "", err
	}
	hash, err := r.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return "", fmt.Errorf("gitx/gogit: show %q: resolve ref: %w", ref, err)
	}
	commit, err := r.CommitObject(*hash)
	if err != nil {
		return "", fmt.Errorf("gitx/gogit: show %q: %w", ref, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "commit %s\n", commit.Hash.String())
	fmt.Fprintf(&b, "Author: %s <%s>\n", commit.Author.Name, commit.Author.Email)
	fmt.Fprintf(&b, "Date:   %s\n\n", commit.Author.When.Format(time.RFC1123Z))
	for _, line := range strings.Split(strings.TrimRight(commit.Message, "\n"), "\n") {
		fmt.Fprintf(&b, "    %s\n", line)
	}
	b.WriteString("\n")

	parent, err := commit.Parent(0)
	if err != nil {
		if err == object.ErrParentNotFound {
			b.WriteString("(root commit — no parent to diff against)\n")
			return b.String(), nil
		}
		return "", fmt.Errorf("gitx/gogit: show %q: resolve parent: %w", ref, err)
	}
	patch, err := parent.Patch(commit)
	if err != nil {
		return "", fmt.Errorf("gitx/gogit: show %q: diff against parent: %w", ref, err)
	}
	b.WriteString(patch.String())
	return b.String(), nil
}

func (c gogitClient) Raw(ctx context.Context, repo string, args ...string) (string, error) {
	return "", ErrRawUnsupported
}
