package state

// Verifies the v0.7 tool-call-budget columns are added to a data dir created by an EARLIER version:
// project_settings (which previously had only project + auto_pr) gains chat_max_tool_rounds /
// chat_max_tool_calls, and runs (which had no per-unit budget column) gains max_tool_calls. Unlike
// an entirely new table, these extend EXISTING tables, so plain CREATE TABLE IF NOT EXISTS is not
// enough — migrate()'s explicit ALTER statements are required, and this pins them.

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestToolCallBudgetColumnsAddedOnPreExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre_v07.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the OLD shapes: project_settings without the chat columns, runs without max_tool_calls.
	for _, stmt := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO meta(key,value) VALUES('schema_version','2')`,
		`CREATE TABLE project_settings (project TEXT PRIMARY KEY, auto_pr INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO project_settings(project,auto_pr) VALUES('p',1)`,
		`CREATE TABLE runs (id TEXT PRIMARY KEY, max_iterations INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO runs(id,max_iterations) VALUES('run_old',25)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed pre-v0.7 db (%s): %v", stmt, err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	// project_settings gained both chat columns, defaulting to 0 (= unset) for the existing row, so
	// auto_pr is untouched (still 1) and the chat budget reads as "use the default".
	var autoPR, rounds, calls int
	if err := db.QueryRow(`SELECT auto_pr, chat_max_tool_rounds, chat_max_tool_calls FROM project_settings WHERE project='p'`).
		Scan(&autoPR, &rounds, &calls); err != nil {
		t.Fatalf("chat budget columns missing after migration: %v", err)
	}
	if autoPR != 1 || rounds != 0 || calls != 0 {
		t.Fatalf("migration should preserve auto_pr=1 and default the chat budget to 0/0, got %d/%d/%d", autoPR, rounds, calls)
	}
	// The column-specific upsert chatlimits.go uses must work against the migrated table.
	if _, err := db.Exec(`INSERT INTO project_settings(project,chat_max_tool_rounds,chat_max_tool_calls) VALUES('p',10,50)
		ON CONFLICT(project) DO UPDATE SET chat_max_tool_rounds=excluded.chat_max_tool_rounds, chat_max_tool_calls=excluded.chat_max_tool_calls`); err != nil {
		t.Fatalf("chat-limits upsert failed after migration: %v", err)
	}
	if err := db.QueryRow(`SELECT auto_pr, chat_max_tool_rounds, chat_max_tool_calls FROM project_settings WHERE project='p'`).
		Scan(&autoPR, &rounds, &calls); err != nil || autoPR != 1 || rounds != 10 || calls != 50 {
		t.Fatalf("upsert round-trip: auto_pr=%d rounds=%d calls=%d err=%v (want 1/10/50)", autoPR, rounds, calls, err)
	}

	// runs gained max_tool_calls, defaulting the pre-existing row to 0 (the legacy sentinel the
	// engine falls back on).
	var mtc int
	if err := db.QueryRow(`SELECT max_tool_calls FROM runs WHERE id='run_old'`).Scan(&mtc); err != nil {
		t.Fatalf("max_tool_calls column missing on runs after migration: %v", err)
	}
	if mtc != 0 {
		t.Fatalf("legacy run row should default max_tool_calls to 0, got %d", mtc)
	}
}
