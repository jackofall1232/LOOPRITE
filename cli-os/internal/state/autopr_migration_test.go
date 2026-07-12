package state

// TestProjectSettingsTableCreatedOnPreExistingDB verifies a data dir created before the Auto-PR
// feature (internal/gateway/autopr.go) -- whose schema has no project_settings table at all --
// gains it on Open, following the same "CREATE TABLE IF NOT EXISTS runs unconditionally on every
// Open" pattern the other run-engine/budget tables already use (see schema in db.go). Unlike
// caps' overage_pct/unlimited columns (which needed an explicit ALTER because they extended an
// existing table), project_settings is an entirely new table, so plain CREATE TABLE IF NOT
// EXISTS is sufficient -- no separate migrate() step is required, and this test pins that.

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestProjectSettingsTableCreatedOnPreExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre_autopr.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','1')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed pre-auto-pr db: %v", err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	// A missing row (no project has ever touched the setting) reads via a plain SELECT as
	// sql.ErrNoRows -- the fail-closed-to-OFF contract autoPREnabled relies on.
	var v int
	err = db.QueryRow(`SELECT auto_pr FROM project_settings WHERE project = 'p'`).Scan(&v)
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows for a project with no row, got err=%v v=%v", err, v)
	}

	// The upsert autopr.go uses must succeed against the migrated table.
	if _, err := db.Exec(`INSERT INTO project_settings(project,auto_pr) VALUES('p',1)
		ON CONFLICT(project) DO UPDATE SET auto_pr=excluded.auto_pr`); err != nil {
		t.Fatalf("project_settings upsert failed after migration: %v", err)
	}
	if err := db.QueryRow(`SELECT auto_pr FROM project_settings WHERE project = 'p'`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("round-trip failed: v=%d err=%v", v, err)
	}
}
