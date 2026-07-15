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
	if ver != "3" {
		t.Fatalf("schema_version should be bumped to 3, got %q", ver)
	}
}

// TestMigrationAddsCapsOverageColumns verifies an older data dir -- whose caps table predates the v0.2
// budget controls (overage_pct, unlimited) -- is migrated on Open so a caps row insert referencing those
// columns succeeds, and an existing row backfills to the zero-value default (no overage, not unlimited).
func TestMigrationAddsCapsOverageColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old_caps.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','1')`,
		`CREATE TABLE caps (project TEXT NOT NULL, window TEXT NOT NULL, limit_usd REAL NOT NULL, PRIMARY KEY (project, window))`,
		`INSERT INTO caps(project,window,limit_usd) VALUES('p','daily',1.5)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed pre-overage db: %v", err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var overage float64
	var unlimited int
	if err := db.QueryRow(`SELECT overage_pct, unlimited FROM caps WHERE project = 'p' AND window = 'daily'`).
		Scan(&overage, &unlimited); err != nil {
		t.Fatalf("overage_pct/unlimited columns missing after migration: %v", err)
	}
	if overage != 0 || unlimited != 0 {
		t.Fatalf("a pre-existing row should backfill to the zero default, got overage=%v unlimited=%v", overage, unlimited)
	}
}

// TestCapSetUpsertClearsOverageAndUnlimited pins the exact UPSERT statement `l00prite cap set` uses
// (main.go capCmd): a CLI daily-cap set is an explicit request to enforce a hard limit, so it must
// reset a dashboard-configured overage_pct/unlimited back to zero, not preserve them. Otherwise a
// project a dashboard operator had put into "no budget stop" (unlimited=1) or an overage allowance
// would silently keep running unenforced/over-ceiling after `cap set` reports a plain $X cap is now
// active -- pep.EffectiveCeiling treats Unlimited as capped=false regardless of limit_usd.
func TestCapSetUpsertClearsOverageAndUnlimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caps.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO caps(project,window,limit_usd,overage_pct,unlimited) VALUES('p','daily',1.0,50,1)`); err != nil {
		t.Fatalf("seed cap: %v", err)
	}

	// The exact statement shape capCmd's "cap set" uses -- resets overage_pct/unlimited to zero.
	if _, err := db.Exec(`INSERT INTO caps(project,window,limit_usd,overage_pct,unlimited) VALUES(?,'daily',?,0,0)
		ON CONFLICT(project,window) DO UPDATE SET limit_usd = excluded.limit_usd, overage_pct = excluded.overage_pct, unlimited = excluded.unlimited`, "p", 3.0); err != nil {
		t.Fatalf("cap-set upsert: %v", err)
	}

	var limit, overage float64
	var unlimited int
	if err := db.QueryRow(`SELECT limit_usd, overage_pct, unlimited FROM caps WHERE project = 'p' AND window = 'daily'`).
		Scan(&limit, &overage, &unlimited); err != nil {
		t.Fatal(err)
	}
	if limit != 3.0 {
		t.Fatalf("limit_usd should update to 3.0, got %v", limit)
	}
	if overage != 0 || unlimited != 0 {
		t.Fatalf("cap set must clear a prior overage_pct/unlimited override, got overage=%v unlimited=%v", overage, unlimited)
	}
}
