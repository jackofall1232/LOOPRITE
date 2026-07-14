// Package ledger is the run ledger. Every request appends a row (to SQLite for query + JSONL for
// portability) capturing the routing decision, real token/dollar cost, memory-degradation status,
// and outcome. Ported from ledger.js, plus a cost_unconfirmed column so an unconfirmed-price row is
// distinguishable from a merely estimated one.
package ledger

import (
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"sync"

	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// defaultLedgerMaxBytes is the JSONL mirror's rotation threshold. Ledger rows are small (a couple
// hundred bytes of JSON each), so 5 MiB holds tens of thousands of recent requests -- plenty for
// interactively tailing/grepping the raw file for debugging on a live device -- while being a size a
// phone's constrained storage never notices even with the one ".1" backup generation kept alongside it
// (worst case ~10 MiB total for this file). SQLite remains the durable, unbounded historical record;
// this file is documented as "portability" only (see package doc above), so bounding it costs nothing
// but easy tail-reading of old raw lines once they roll into (and eventually out of) the ".1" backup.
const defaultLedgerMaxBytes int64 = 5 * 1024 * 1024

// ledgerMaxBytesOnce/-Val cache the effective rotation threshold: read from LOOPRITE_LEDGER_MAX_BYTES
// once (Append is a hot path hit on every gateway request, so it must not re-parse an env var per
// call), the same "read once" shape config.Load uses for its own numeric env overrides. A test may
// reset ledgerMaxBytesOnce to force a re-read after changing the env var.
var (
	ledgerMaxBytesOnce sync.Once
	ledgerMaxBytesVal  int64
)

func ledgerMaxBytes() int64 {
	ledgerMaxBytesOnce.Do(func() {
		ledgerMaxBytesVal = parseLedgerMaxBytes(os.Getenv("LOOPRITE_LEDGER_MAX_BYTES"), defaultLedgerMaxBytes)
	})
	return ledgerMaxBytesVal
}

// parseLedgerMaxBytes mirrors config.finiteOr's contract ("invalid/zero/negative falls back to the
// default, never to zero/unlimited/a crash"): an empty, non-numeric, zero, or negative override is
// rejected in favor of fallback, so a bad env var can never disable rotation or panic.
func parseLedgerMaxBytes(raw string, fallback int64) int64 {
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// jsonlMu serializes the JSONL mirror's check-size / maybe-rotate / append sequence across every
// concurrent caller of Append (the gateway calls Append per HTTP request, so multiple goroutines can
// race here under real traffic). It guards ONLY the file-mirror section below -- the SQLite insert
// above it already goes through the DB's own connection pool/driver locking and needs no new lock.
// Without this, two goroutines could both observe "size >= threshold" and both rename ledgerPath ->
// ledgerPath+".1" concurrently, or one could rename the file out from under the other mid-write; this
// mutex makes the whole rotate-then-append sequence atomic instead.
var jsonlMu sync.Mutex

// Entry is a ledger row to append. Optional fields are pointers/typed nils.
type Entry struct {
	RequestID       string
	Project         string
	Repo            string
	Provider        string
	Model           string
	RuleID          string
	Decision        any // JSON-marshaled; nil -> NULL
	Usage           *oai.Usage
	CostUSD         *float64
	CostEstimated   bool
	CostUnconfirmed bool
	MemoryStatus    string
	Outcome         string
}

// Row is a ledger row read back for `route explain` / `ledger`.
type Row struct {
	ID               string
	TS               string
	RequestID        string
	Project          string
	Repo             string
	Provider         string
	Model            string
	RuleID           string
	Decision         string
	PromptTokens     sql.NullInt64
	CompletionTokens sql.NullInt64
	CacheReadTokens  sql.NullInt64
	CacheWriteTokens sql.NullInt64
	CostUSD          sql.NullFloat64
	CostEstimated    bool
	CostUnconfirmed  bool
	MemoryStatus     string
	Outcome          string
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Append writes the row to the ledger table and mirrors it to the JSONL file (best-effort).
func Append(db *sql.DB, ledgerPath string, e Entry) string {
	id := util.RID("run")
	ts := util.NowISO()

	var decisionStr any
	if e.Decision != nil {
		if b, err := json.Marshal(e.Decision); err == nil {
			decisionStr = string(b)
		}
	}
	var pt, ct, crt, cwt any
	if e.Usage != nil {
		pt, ct, crt, cwt = e.Usage.PromptTokens, e.Usage.CompletionTokens, e.Usage.CacheReadTokens, e.Usage.CacheWriteTokens
	}
	var cost any
	if e.CostUSD != nil {
		cost = *e.CostUSD
	}
	estimated := 0
	if e.CostEstimated {
		estimated = 1
	}
	unconfirmed := 0
	if e.CostUnconfirmed {
		unconfirmed = 1
	}

	_, _ = db.ExecContext(state.Ctx(),
		`INSERT INTO ledger(id,ts,request_id,project,repo,provider,model,rule_id,decision,prompt_tokens,completion_tokens,cache_read_tokens,cache_write_tokens,cost_usd,cost_estimated,cost_unconfirmed,memory_status,outcome)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, ts, nullStr(e.RequestID), nullStr(e.Project), nullStr(e.Repo), nullStr(e.Provider), nullStr(e.Model),
		nullStr(e.RuleID), decisionStr, pt, ct, crt, cwt, cost, estimated, unconfirmed, nullStr(e.MemoryStatus), nullStr(e.Outcome))

	// JSONL mirror (best-effort).
	mirror := map[string]any{
		"id": id, "ts": ts, "request_id": nullStr(e.RequestID), "project": nullStr(e.Project),
		"repo": nullStr(e.Repo), "provider": nullStr(e.Provider), "model": nullStr(e.Model),
		"rule_id": nullStr(e.RuleID), "decision": decisionStr,
		"prompt_tokens": pt, "completion_tokens": ct, "cache_read_tokens": crt, "cache_write_tokens": cwt,
		"cost_usd": cost, "cost_estimated": estimated, "cost_unconfirmed": unconfirmed,
		"memory_status": nullStr(e.MemoryStatus), "outcome": nullStr(e.Outcome),
	}
	if b, err := json.Marshal(mirror); err == nil {
		appendMirrorLine(ledgerPath, b)
	}
	return id
}

// appendMirrorLine rotates ledgerPath if it has already reached the size threshold, then appends line
// (without a trailing newline; one is added here) to it. The whole check-size / maybe-rotate / append
// sequence runs under jsonlMu so concurrent callers can never race each other's rename or writes.
//
// Size is checked BEFORE appending (not after) so the row being written is guaranteed to land as the
// first line of a fresh, empty ledgerPath whenever rotation fires -- simpler than writing first and
// then deciding to rotate around an already-appended row (which would require re-reading the file back
// to split it). Either choice keeps the row; this one never risks writing into a file that gets renamed
// out from under it a moment later, since rotation (if any) always completes before the write starts.
//
// Best-effort, like the rest of the JSONL mirror: if Stat/Rename/OpenFile/Write fails for any reason,
// this silently does nothing further for that step. The row is never lost overall -- it is already
// durable in the SQLite ledger table inserted above -- and a failed rotation just means the file keeps
// growing past the threshold until a later call succeeds in rotating it.
func appendMirrorLine(ledgerPath string, line []byte) {
	jsonlMu.Lock()
	defer jsonlMu.Unlock()

	rotateLedgerLocked(ledgerPath)

	if f, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
}

// rotateLedgerLocked renames ledgerPath to ledgerPath+".1" (clobbering any prior ".1" -- exactly one
// backup generation is kept, by design, to bound worst-case size rather than archive history) when
// ledgerPath already exists and is at or above the configured threshold. Must be called with jsonlMu
// held. A missing/unreadable ledgerPath (nothing to rotate yet) or a failed rename (best-effort, see
// appendMirrorLine) both simply leave ledgerPath as-is.
func rotateLedgerLocked(ledgerPath string) {
	info, err := os.Stat(ledgerPath)
	if err != nil {
		return
	}
	if info.Size() < ledgerMaxBytes() {
		return
	}
	_ = os.Rename(ledgerPath, ledgerPath+".1")
}

func scanRows(rows *sql.Rows) []Row {
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var (
			r                                     Row
			reqID, project, repo, provider, model sql.NullString
			ruleID, decision, memStatus, outcome  sql.NullString
			estimated, unconfirmed                sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &r.TS, &reqID, &project, &repo, &provider, &model, &ruleID, &decision,
			&r.PromptTokens, &r.CompletionTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.CostUSD, &estimated, &unconfirmed, &memStatus, &outcome); err != nil {
			continue
		}
		r.RequestID, r.Project, r.Repo = reqID.String, project.String, repo.String
		r.Provider, r.Model, r.RuleID = provider.String, model.String, ruleID.String
		r.Decision, r.MemoryStatus, r.Outcome = decision.String, memStatus.String, outcome.String
		r.CostEstimated = estimated.Int64 != 0
		r.CostUnconfirmed = unconfirmed.Int64 != 0
		out = append(out, r)
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

const selectCols = `id,ts,request_id,project,repo,provider,model,rule_id,decision,prompt_tokens,completion_tokens,cache_read_tokens,cache_write_tokens,cost_usd,cost_estimated,cost_unconfirmed,memory_status,outcome`

// Explain returns ledger rows matching a request id (or ledger row id).
func Explain(db *sql.DB, requestID string) []Row {
	rows, err := db.QueryContext(state.Ctx(),
		`SELECT `+selectCols+` FROM ledger WHERE request_id = ? OR id = ? ORDER BY ts DESC`, requestID, requestID)
	if err != nil {
		return nil
	}
	return scanRows(rows)
}

// Recent returns the most recent ledger rows.
func Recent(db *sql.DB, limit int) []Row {
	rows, err := db.QueryContext(state.Ctx(),
		`SELECT `+selectCols+` FROM ledger ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	return scanRows(rows)
}

// RecentForProject returns the most recent ledger rows for one project.
func RecentForProject(db *sql.DB, project string, limit int) []Row {
	rows, err := db.QueryContext(state.Ctx(),
		`SELECT `+selectCols+` FROM ledger WHERE project = ? ORDER BY ts DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil
	}
	return scanRows(rows)
}
