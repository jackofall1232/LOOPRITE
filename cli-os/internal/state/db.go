// Package state is the transactional store (SQLite WAL) that the Policy Enforcement Point reserves
// budget against — ported from db.js. The driver is modernc.org/sqlite: a PURE-Go SQLite
// (no cgo), so the binary stays a single static executable with real ACID + WAL. See
// docs/node-to-go-port-notes.md for why this driver over a cgo one.
//
// The pool is capped at ONE open connection (SetMaxOpenConns(1)) to preserve node:sqlite's
// single-connection, non-interleaving semantics that the PEP's reserve/commit atomicity assumes;
// WAL still provides durability and cross-process locking. Transactions use BEGIN IMMEDIATE on a
// pinned *sql.Conn so a reservation grabs the write lock up front (no deferred-lock deadlock).
package state

import (
	"context"
	"database/sql"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);

CREATE TABLE IF NOT EXISTS providers (
  name TEXT PRIMARY KEY,
  adapter TEXT NOT NULL,
  base_url TEXT,
  enc_key TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  is_default INTEGER NOT NULL DEFAULT 0,
  verified INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);

-- Per-provider model selection (Part C / Part E). A row records an operator's explicit choice for one
-- (provider, model); ABSENCE means "enabled" (the manifest default), so a provider with no rows here
-- exposes its full manifest catalog exactly as before. Only a stored enabled=0 row disables a model.
CREATE TABLE IF NOT EXISTS provider_models (
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (provider, model)
);

CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY,
  hash TEXT NOT NULL,
  project TEXT NOT NULL,
  repo TEXT,
  revoked INTEGER NOT NULL DEFAULT 0,
  expires_at TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS repos (
  id TEXT PRIMARY KEY,
  root TEXT NOT NULL,
  project TEXT NOT NULL,
  created_at TEXT NOT NULL
);

-- Tracks, per registered repo, the one live "Add l00prite" branch that was created and committed
-- locally but has not yet resulted in an opened pull request, so a later dashboard click can retry
-- the push/PR instead of no-opping on repo_scaffold.go's already_complete check. Gateway-side state
-- only: this is NOT the target repo's cooperative .l00prite/lock.json and never touches it. One row
-- per repo (PRIMARY KEY repo_id) -- only the latest pending attempt matters; a fresh attempt
-- supersedes the row in place, a confirmed-opened (or found-already-existing) PR deletes it, and
-- HandleRepoRemove deletes it with the repo. CREATE TABLE IF NOT EXISTS runs unconditionally on
-- every Open (same guarantee as project_settings), so a pre-existing DB gains it with no migration.
CREATE TABLE IF NOT EXISTS repo_scaffold_attempts (
  repo_id TEXT PRIMARY KEY,
  branch TEXT NOT NULL,
  commit_hash TEXT,
  pushed INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS caps (
  project TEXT NOT NULL,
  window TEXT NOT NULL,
  limit_usd REAL NOT NULL,
  overage_pct REAL NOT NULL DEFAULT 0,
  unlimited INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project, window)
);

-- Per-project settings that are neither budget (caps) nor a run's own config: today just the
-- Auto-PR toggle (gateway/autopr.go). A dedicated table, not an ALTER onto caps, because caps is
-- keyed (project, window) for the budget feature specifically and auto_pr has no "window" -- the
-- wrong home. CREATE TABLE IF NOT EXISTS runs unconditionally on every Open (same guarantee as
-- every other table here), so a pre-existing DB gains this table with no separate migration step.
-- A missing row reads as auto_pr=0 (OFF) -- see autoPREnabled -- so every existing and new project
-- defaults to OFF with no backfill needed.
CREATE TABLE IF NOT EXISTS project_settings (
  project TEXT PRIMARY KEY,
  auto_pr INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS spend (
  project TEXT NOT NULL,
  day TEXT NOT NULL,
  reserved_usd REAL NOT NULL DEFAULT 0,
  committed_usd REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (project, day)
);

CREATE TABLE IF NOT EXISTS reservations (
  id TEXT PRIMARY KEY,
  project TEXT NOT NULL,
  day TEXT NOT NULL,
  amount_usd REAL NOT NULL,
  state TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS leases (
  id TEXT PRIMARY KEY,
  project TEXT NOT NULL,
  kind TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ledger (
  id TEXT PRIMARY KEY,
  ts TEXT NOT NULL,
  request_id TEXT,
  project TEXT,
  repo TEXT,
  provider TEXT,
  model TEXT,
  rule_id TEXT,
  decision TEXT,
  prompt_tokens INTEGER,
  completion_tokens INTEGER,
  cache_read_tokens INTEGER,
  cache_write_tokens INTEGER,
  cost_usd REAL,
  cost_estimated INTEGER,
  cost_unconfirmed INTEGER,
  memory_status TEXT,
  outcome TEXT
);

CREATE TABLE IF NOT EXISTS audit (
  id TEXT PRIMARY KEY,
  ts TEXT NOT NULL,
  actor TEXT,
  action TEXT NOT NULL,
  detail TEXT
);

-- L00prite OS run-engine tables (see cli-os/docs/os-architecture.md §2.1). A run is one confirmed
-- autonomous execution against one registered repo; run_events is the append-only, monotonically
-- sequenced feed (dashboard live feed + machine-parseable run log); run_approvals holds per-action
-- permission requests. Additive/idempotent (IF NOT EXISTS), so an existing DB gains them on Open
-- without a schema_version bump.
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY, project TEXT NOT NULL, repo TEXT NOT NULL, repo_root TEXT NOT NULL,
  goal TEXT NOT NULL, objective TEXT NOT NULL,
  gates TEXT NOT NULL, command_allowlist TEXT NOT NULL,          -- JSON
  max_iterations INTEGER NOT NULL, approval_timeout_s INTEGER NOT NULL,
  no_progress_threshold INTEGER NOT NULL,
  status TEXT NOT NULL, boundary TEXT,
  current_iteration INTEGER NOT NULL DEFAULT 0,
  iterations_since_progress INTEGER NOT NULL DEFAULT 0,
  last_progress_iteration INTEGER,
  branch TEXT, preflight TEXT, preflight_at TEXT,
  confirmed_by TEXT, confirmed_at TEXT,
  cost_usd REAL NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, started_at TEXT, ended_at TEXT, summary TEXT
);
CREATE TABLE IF NOT EXISTS run_events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL, ts TEXT NOT NULL, kind TEXT NOT NULL, payload TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id, seq);
CREATE TABLE IF NOT EXISTS run_approvals (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL, class TEXT NOT NULL, action TEXT NOT NULL,
  args TEXT NOT NULL, status TEXT NOT NULL, decided_by TEXT, note TEXT,
  created_at TEXT NOT NULL, decided_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_run_approvals_run ON run_approvals(run_id, status);
`

// Querier is satisfied by *sql.DB, *sql.Conn, and *sql.Tx — so a function can run either directly
// on the pool or inside a pinned transaction. Uses the Context method set (the common denominator).
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Open opens (creating if needed) the SQLite database at dbPath, applies WAL + schema, and pins a
// single connection.
func Open(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single-writer parity with node:sqlite; avoids intra-process SQLITE_BUSY
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 5000;",
		// This DB stores encrypted provider-key ciphertext. secure_delete zeroes freed page content, so
		// a rotated/removed key's old ciphertext does not linger in freed pages until the next vacuum —
		// defense in depth behind the AES-256-GCM at rest (the plaintext key is never written here).
		"PRAGMA secure_delete = ON;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	migrate(db)
	if _, err := db.Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('schema_version','2');`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate applies best-effort, idempotent schema upgrades for data dirs created by an earlier
// version (e.g. the Node runtime, whose ledger table predates cost_unconfirmed). CREATE TABLE IF NOT
// EXISTS does not add columns to an existing table, so the column is added here explicitly. The
// ALTER errors when the column already exists (a fresh v2 DB) — that is expected and ignored.
func migrate(db *sql.DB) {
	_, _ = db.Exec(`ALTER TABLE ledger ADD COLUMN cost_unconfirmed INTEGER`)
	// A provider added before Part E has no verified column; add it. On a FRESH v-current DB the schema
	// const already created the column, so this ALTER errors (duplicate column) and is ignored — new
	// providers correctly start verified=0. On an OLD DB the ALTER SUCCEEDS (err==nil): those providers
	// predate the flag and were presumably in working use, so backfill them to verified=1 rather than
	// showing every existing provider as "unverified" after an upgrade.
	if _, err := db.Exec(`ALTER TABLE providers ADD COLUMN verified INTEGER NOT NULL DEFAULT 0`); err == nil {
		_, _ = db.Exec(`UPDATE providers SET verified = 1`)
	}
	// caps predates overage/unlimited (v0.2 budget controls); a fresh DB already has the columns
	// via the schema const, so the duplicate-column error here is expected and ignored.
	_, _ = db.Exec(`ALTER TABLE caps ADD COLUMN overage_pct REAL NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE caps ADD COLUMN unlimited INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`UPDATE meta SET value = '2' WHERE key = 'schema_version' AND value = '1'`)
}

// Tx runs fn inside a BEGIN IMMEDIATE transaction on a pinned connection, committing on success and
// rolling back if fn returns an error. The result of fn is returned on commit. Ported from tx().
func Tx[T any](db *sql.DB, fn func(q Querier) (T, error)) (T, error) {
	var zero T
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); err != nil {
		return zero, err
	}
	res, err := fn(conn)
	if err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK;")
		return zero, err
	}
	if _, cerr := conn.ExecContext(ctx, "COMMIT;"); cerr != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK;")
		return zero, cerr
	}
	return res, nil
}

// Ctx is a convenience background context for one-off queries.
func Ctx() context.Context { return context.Background() }
