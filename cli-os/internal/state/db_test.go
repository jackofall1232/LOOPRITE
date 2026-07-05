package state

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrationAddsCostUnconfirmed verifies an older (Node v1) data dir — whose ledger table predates
// the cost_unconfirmed column — is migrated on Open so ledger inserts referencing the column succeed.
func TestMigrationAddsCostUnconfirmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','1')`,
		`CREATE TABLE ledger (id TEXT PRIMARY KEY, ts TEXT, cost_usd REAL, cost_estimated INTEGER)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed v1 db: %v", err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO ledger(id,ts,cost_usd,cost_estimated,cost_unconfirmed) VALUES('x','t',0,0,1)`); err != nil {
		t.Fatalf("cost_unconfirmed column missing after migration: %v", err)
	}
	var ver string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != "2" {
		t.Fatalf("schema_version should be bumped to 2, got %q", ver)
	}
}
