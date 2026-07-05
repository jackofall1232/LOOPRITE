package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSymlinkEscapingRootNotRead(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOP-SECRET-OUTSIDE-ROOT"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".l00prite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".l00prite", "memory.md"), []byte("# Memory\nlegit content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(repo, ".l00prite", "constraints.md")); err != nil {
		t.Fatal(err)
	}
	ctx := Query(repo, Digest{}, 8000, 0, false)
	var joined strings.Builder
	for _, b := range ctx.Blocks {
		joined.WriteString(b.Text)
		joined.WriteString("\n")
		if strings.Contains(b.SourcePath, "constraints") {
			t.Fatalf("escaping symlink must be skipped, got block %s", b.SourcePath)
		}
	}
	if strings.Contains(joined.String(), "TOP-SECRET-OUTSIDE-ROOT") {
		t.Fatalf("symlinked-out file must not be read")
	}
}

func TestReadsBlocksAndEmptyWhenNoDir(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".l00prite"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".l00prite", "constraints.md"), []byte("# Constraints\nNo secrets in memory."), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := Query(repo, Digest{UserIntent: "add constraints check"}, 8000, 0, false)
	if ctx.Status != "ok" {
		t.Fatalf("status want ok got %q (reason %q)", ctx.Status, ctx.Reason)
	}
	if len(ctx.Blocks) == 0 || ctx.Blocks[0].Kind != "constraint" {
		t.Fatalf("first block should be a constraint: %+v", ctx.Blocks)
	}
	if !strings.Contains(ctx.Blocks[0].Text, "No secrets") {
		t.Fatalf("block text missing content")
	}
	empty := Query(t.TempDir(), Digest{}, 0, 0, false)
	if empty.Status != "empty" {
		t.Fatalf("no memory dir must be empty, got %q", empty.Status)
	}
}
