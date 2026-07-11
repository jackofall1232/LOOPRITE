package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoRegisteredResponseRespectsForeignLock pins a PR #6 review finding: registering a repo
// used to call engine.Files.Scaffold unconditionally, even while another agent held an active,
// unexpired lock on that repo's .l00prite/ folder. Scaffold only creates missing files (it never
// overwrites), but a file appearing mid-run underneath a lock holder's feet is exactly what
// LOCKING.md's convention exists to prevent -- so a foreign lock must make registration skip
// scaffolding and say so, not silently write anyway.
func TestRepoRegisteredResponseRespectsForeignLock(t *testing.T) {
	app := &App{}

	t.Run("foreign active lock blocks scaffolding", func(t *testing.T) {
		root := t.TempDir()
		lockPath := filepath.Join(root, ".l00prite", "lock.json")
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
			t.Fatal(err)
		}
		lockJSON := `{
			"status": "active",
			"owner_agent": "l00prite-os",
			"owner_session": "run_other_agent",
			"expires_at": "2999-01-01T00:00:00.000Z"
		}`
		if err := os.WriteFile(lockPath, []byte(lockJSON), 0o644); err != nil {
			t.Fatal(err)
		}

		resp := app.repoRegisteredResponse("r1", root, "proj")

		if _, err := os.Stat(filepath.Join(root, ".l00prite", "blueprint.md")); !os.IsNotExist(err) {
			t.Fatalf("expected scaffolding to be skipped while a foreign lock is active, but blueprint.md exists (err=%v)", err)
		}
		note, _ := resp["note"].(string)
		if note == "" || !strings.Contains(strings.ToLower(note), "lock") {
			t.Fatalf("expected a note explaining the skipped scaffold, got %q", note)
		}
	})

	t.Run("no lock scaffolds normally", func(t *testing.T) {
		root := t.TempDir()

		resp := app.repoRegisteredResponse("r2", root, "proj")

		if _, err := os.Stat(filepath.Join(root, ".l00prite", "blueprint.md")); err != nil {
			t.Fatalf("expected scaffolding to run with no lock present, got: %v", err)
		}
		note, _ := resp["note"].(string)
		if note == "" {
			t.Fatal("expected a note describing the newly-scaffolded memory folder")
		}
	})

	t.Run("released lock scaffolds normally", func(t *testing.T) {
		root := t.TempDir()
		lockPath := filepath.Join(root, ".l00prite", "lock.json")
		if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lockPath, []byte(`{"status":"released"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		app.repoRegisteredResponse("r3", root, "proj")

		if _, err := os.Stat(filepath.Join(root, ".l00prite", "blueprint.md")); err != nil {
			t.Fatalf("expected scaffolding to run when the lock is released, got: %v", err)
		}
	})
}
