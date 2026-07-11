package gateway

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeChatFixture(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- read-only enforcement ----

func TestChatToolDefinitionsAreReadOnly(t *testing.T) {
	defs := chatToolDefinitions()
	if len(defs) != 3 {
		t.Fatalf("expected exactly 3 chat tools, got %d", len(defs))
	}
	names := map[string]bool{}
	for _, d := range defs {
		fn := asMap(d.(map[string]any)["function"])
		names[asStr(fn["name"])] = true
	}
	for _, want := range []string{"read_file", "list_dir", "search_files"} {
		if !names[want] {
			t.Fatalf("expected %q among chat tool definitions, got %v", want, names)
		}
	}
	for _, forbidden := range []string{"write_file", "run_command", "git_command"} {
		if names[forbidden] {
			t.Fatalf("chat tool definitions must never include %q (mutating/executing tool)", forbidden)
		}
	}
}

func TestChatToolboxExecuteRejectsUnknownName(t *testing.T) {
	tb := chatToolbox{Root: t.TempDir()}
	out := tb.Execute("write_file", map[string]any{"path": "x", "content": "y"})
	if !strings.HasPrefix(out, "ERROR: unknown tool") {
		t.Fatalf("write_file must not be dispatchable through chatToolbox.Execute, got: %q", out)
	}
}

// ---- path jail ----

func TestChatToolboxRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, "README.md", "hello\n")
	outsideDir := t.TempDir()
	writeChatFixture(t, outsideDir, "secret.txt", "outside content\n")

	tb := chatToolbox{Root: root}
	for _, rel := range []string{"../secret.txt", "../../etc/passwd", "/etc/passwd"} {
		out := tb.readFile(map[string]any{"path": rel})
		if !strings.HasPrefix(out, "ERROR:") {
			t.Fatalf("path %q should have been rejected, got: %q", rel, out)
		}
		if strings.Contains(out, "outside content") {
			t.Fatalf("path %q leaked content from outside the repo root: %q", rel, out)
		}
	}
}

func TestChatToolboxRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeChatFixture(t, outside, "secret.txt", "outside content\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}
	tb := chatToolbox{Root: root}
	out := tb.readFile(map[string]any{"path": "link.txt"})
	if strings.Contains(out, "outside content") {
		t.Fatalf("a symlink pointing outside the repo root must not be readable, got: %q", out)
	}
}

// ---- secret-path denial ----

func TestChatToolboxDeniesSecretPaths(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, ".env", "API_KEY=super-secret\n")
	writeChatFixture(t, root, "id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	writeChatFixture(t, root, "config/prod.pem", "cert data\n")
	writeChatFixture(t, root, "README.md", "not a secret\n")

	tb := chatToolbox{Root: root}
	for _, rel := range []string{".env", "id_rsa", "config/prod.pem"} {
		out := tb.readFile(map[string]any{"path": rel})
		if !strings.HasPrefix(out, "DENIED:") {
			t.Fatalf("expected %q to be denied as a secret path, got: %q", rel, out)
		}
	}
	// A non-secret path must still work.
	out := tb.readFile(map[string]any{"path": "README.md"})
	if out != "not a secret\n" {
		t.Fatalf("README.md should read normally, got: %q", out)
	}

	// search_files must not surface content from secret files even via a match-everything query.
	searchOut := tb.searchFiles(map[string]any{"query": "."})
	if strings.Contains(searchOut, "super-secret") || strings.Contains(searchOut, "BEGIN OPENSSH") || strings.Contains(searchOut, "cert data") {
		t.Fatalf("search_files leaked secret file content: %q", searchOut)
	}
}

// Regression test for an adversarial-review finding: a symlink INSIDE the repo root, given an
// innocuous name, must not be able to alias a denied secret file -- the deny check has to key off
// the RESOLVED target, not the caller-supplied name, or the read follows the symlink straight
// through the check.
func TestChatToolboxDeniesSecretPathsViaSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, ".env", "API_KEY=super-secret\n")
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, "totally_fine_notes.md")); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}
	tb := chatToolbox{Root: root}
	out := tb.readFile(map[string]any{"path": "totally_fine_notes.md"})
	if strings.Contains(out, "super-secret") {
		t.Fatalf("a symlink alias to a secret file must not leak its content, got: %q", out)
	}
	if !strings.HasPrefix(out, "DENIED:") {
		t.Fatalf("expected the resolved target to be denied as a secret path, got: %q", out)
	}
}

// Deny-list matching must be case-insensitive (a filesystem that preserves case, e.g. a
// case-sensitive Linux host, would otherwise let ".ENV" or "ID_RSA" slip through the exact-case
// pattern list).
func TestChatToolboxDeniesSecretPathsCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, ".ENV", "API_KEY=super-secret\n")
	tb := chatToolbox{Root: root}
	out := tb.readFile(map[string]any{"path": ".ENV"})
	if strings.Contains(out, "super-secret") {
		t.Fatalf(".ENV (differently-cased) leaked secret content: %q", out)
	}
	if !strings.HasPrefix(out, "DENIED:") {
		t.Fatalf("expected .ENV to be denied case-insensitively, got: %q", out)
	}
}

// A nested/vendored .git directory (e.g. a submodule or vendored checkout) must be denied at any
// depth, not just a literal top-level ".git/config" -- filepath.Match never spans path
// separators, so a plain "*.git/config" glob only ever matches the top-level case.
func TestChatToolboxDeniesNestedDotGit(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, "vendor/lib/.git/config", "[credential]\n\thelper = store --file=/secret/creds\n")
	writeChatFixture(t, root, ".git/config", "top-level config\n")
	tb := chatToolbox{Root: root}

	nested := tb.readFile(map[string]any{"path": "vendor/lib/.git/config"})
	if strings.Contains(nested, "secret/creds") {
		t.Fatalf("nested .git/config leaked content: %q", nested)
	}
	if !strings.HasPrefix(nested, "DENIED:") {
		t.Fatalf("expected a nested .git/config to be denied, got: %q", nested)
	}

	topLevel := tb.readFile(map[string]any{"path": ".git/config"})
	if !strings.HasPrefix(topLevel, "DENIED:") {
		t.Fatalf("expected the literal top-level .git/config to still be denied, got: %q", topLevel)
	}
}

// ---- read_file / list_dir / search_files behavior ----

func TestChatToolboxReadFileWindowing(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, "lines.txt", "one\ntwo\nthree\nfour\nfive\n")
	tb := chatToolbox{Root: root}

	out := tb.readFile(map[string]any{"path": "lines.txt"})
	if out != "one\ntwo\nthree\nfour\nfive\n" {
		t.Fatalf("unexpected full read: %q", out)
	}

	windowed := tb.readFile(map[string]any{"path": "lines.txt", "offset_line": float64(2), "limit_lines": float64(2)})
	if !strings.HasPrefix(windowed, "two\nthree") {
		t.Fatalf("expected a 2-line window starting at line 2, got: %q", windowed)
	}
}

// Code-review finding (Gemini Code Assist, PR #6, HIGH severity): an unbounded limit_lines from
// the model would overflow start+limit (Go int addition wraps rather than panicking), potentially
// wrapping to a large negative number that passed the "start+limit < end" check and got assigned
// to end; lines[start:end] with a negative end then panics ("slice bounds out of range"), crashing
// the gateway process for every tenant, not just this request.
//
// The precise reproduction matters: math.MaxInt64 itself does NOT trigger it, because round-
// tripping through JSON's float64 representation rounds it up to 2^63 (out of int64 range), and
// Go's float64->int conversion saturates that to math.MinInt64 -- which the pre-existing
// "limit >= 0" guard filters out before the addition. The actual danger zone is a float64 value
// that survives the round trip as a large but VALID positive int (float64's ~2048-wide precision
// gap near 2^63 means math.MaxInt64-2047 is the largest such value) combined with a large `total`
// line count so `start` (itself clamped to `total`, regardless of how huge offset_line was) is
// close enough to push start+limit past math.MaxInt64. Both conditions are independently
// attacker/model-controlled: an arbitrarily large limit_lines value, against any file with enough
// lines.
func TestChatToolboxReadFileLimitLinesOverflowDoesNotPanic(t *testing.T) {
	root := t.TempDir()
	// 5000 short lines: within the 32 KiB read cap (so the file is read whole, not truncated
	// first), but enough lines that a clamped `start` near `total` is large enough to matter.
	var lines []byte
	for i := 0; i < 5000; i++ {
		lines = append(lines, []byte("x\n")...)
	}
	writeChatFixture(t, root, "lines.txt", string(lines))
	tb := chatToolbox{Root: root}

	dangerLimit := float64(math.MaxInt64 - 2047) // largest float64-representable value below 2^63
	cases := []struct {
		args        map[string]any
		wantContent bool // false when offset_line clamps start to the end of the file (an empty,
		// but legitimate, window -- not a bug), so there's genuinely nothing to assert content on
	}{
		{args: map[string]any{"path": "lines.txt", "limit_lines": dangerLimit}, wantContent: true},
		{args: map[string]any{"path": "lines.txt", "offset_line": float64(999999999), "limit_lines": dangerLimit}, wantContent: false},
		{args: map[string]any{"path": "lines.txt", "limit_lines": float64(math.MaxInt64)}, wantContent: true}, // saturates to MinInt64; must also not panic
	}
	for _, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("readFile panicked with args=%v: %v", c.args, r)
				}
			}()
			out := tb.readFile(c.args)
			if c.wantContent && !strings.Contains(out, "x") {
				t.Fatalf("expected sane clamped content back for args=%v, got: %q", c.args, out)
			}
		}()
	}
}

// TestChatToolboxReadFileTruncatesLargeFileWithoutLoadingItWhole pins the fix for Codex's PR #6
// review finding: readFile used to call os.ReadFile on the resolved path, loading the ENTIRE file
// into memory before applying the chatReadCapBytes truncation -- a file far larger than the 32 KiB
// cap (a multi-GB log, an accidentally-committed binary blob) would be read in full just to have
// almost all of it discarded. The fix switches to os.Open + io.LimitReader(f, cap+1), which stops
// reading at the cap. This test can't directly observe memory usage, but it does pin the
// user-visible contract the bounded read must still satisfy: the returned text is truncated to
// exactly chatReadCapBytes runes worth of content and carries the truncation note.
func TestChatToolboxReadFileTruncatesLargeFileWithoutLoadingItWhole(t *testing.T) {
	root := t.TempDir()
	// One byte over the cap, all 'a' so the truncation boundary is unambiguous.
	big := strings.Repeat("a", chatReadCapBytes+1)
	writeChatFixture(t, root, "big.txt", big)
	tb := chatToolbox{Root: root}

	out := tb.readFile(map[string]any{"path": "big.txt"})
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected a truncation note, got a %d-byte result", len(out))
	}
	content := strings.SplitN(out, "\n… [truncated", 2)[0]
	if len(content) != chatReadCapBytes {
		t.Fatalf("expected exactly %d bytes of content before the truncation note, got %d", chatReadCapBytes, len(content))
	}
}

func TestChatToolboxReadFileRejectsDirectoryAndBinary(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, "sub/file.txt", "x\n")
	writeChatFixture(t, root, "bin.dat", "\x00\x01\xff\xfe")
	tb := chatToolbox{Root: root}

	if out := tb.readFile(map[string]any{"path": "sub"}); !strings.Contains(out, "directory") {
		t.Fatalf("expected a directory error, got: %q", out)
	}
	if out := tb.readFile(map[string]any{"path": "bin.dat"}); !strings.Contains(out, "binary") {
		t.Fatalf("expected a binary-file error, got: %q", out)
	}
}

func TestChatToolboxListDir(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, "a.txt", "1")
	writeChatFixture(t, root, "sub/b.txt", "2")
	writeChatFixture(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	tb := chatToolbox{Root: root}

	out := tb.listDir(map[string]any{})
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "sub/") {
		t.Fatalf("expected a.txt and sub/ in listing, got: %q", out)
	}
	if strings.Contains(out, ".git") {
		t.Fatalf(".git must never be listed, got: %q", out)
	}
}

func TestChatToolboxSearchFiles(t *testing.T) {
	root := t.TempDir()
	writeChatFixture(t, root, "main.go", "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	writeChatFixture(t, root, "other.go", "package other\n")
	tb := chatToolbox{Root: root}

	out := tb.searchFiles(map[string]any{"query": "func main"})
	if !strings.Contains(out, "main.go:3:") {
		t.Fatalf("expected a main.go:3: match, got: %q", out)
	}
	if strings.Contains(out, "other.go") {
		t.Fatalf("did not expect other.go to match \"func main\", got: %q", out)
	}

	none := tb.searchFiles(map[string]any{"query": "nonexistent_token_xyz"})
	if none != "(no matches)" {
		t.Fatalf("expected \"(no matches)\", got: %q", none)
	}
}

// TestChatToolboxSearchFilesBoundsFilesWalked pins Codex's PR #6 review finding: search_files'
// WalkDir had no bound on the total number of files visited -- a rare or non-matching query
// against a very large repo would walk and open every eligible file with no cap at all. Lowering
// chatSearchMaxFilesWalked (a var specifically so tests don't need tens of thousands of fixture
// files) proves the walk actually stops instead of visiting the whole tree.
func TestChatToolboxSearchFilesBoundsFilesWalked(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		writeChatFixture(t, root, fmt.Sprintf("f%02d.txt", i), "needle\n")
	}
	tb := chatToolbox{Root: root}

	orig := chatSearchMaxFilesWalked
	chatSearchMaxFilesWalked = 5
	t.Cleanup(func() { chatSearchMaxFilesWalked = orig })

	out := tb.searchFiles(map[string]any{"query": "needle", "max_results": float64(100)})
	got := strings.Count(out, "needle\n")
	if got == 0 {
		t.Fatal("expected at least one match from within the walked-files budget")
	}
	if got >= 20 {
		t.Fatalf("expected the walk to stop well before visiting all 20 files, got %d matches", got)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected a truncation note once the walked-files cap was hit, got: %q", out)
	}
}
