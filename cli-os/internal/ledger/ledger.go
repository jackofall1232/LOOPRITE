// Package ledger is the run ledger. Every request appends a row (to SQLite for query + JSONL for
// portability) capturing the routing decision, real token/dollar cost, memory-degradation status,
// and outcome. Ported from ledger.js, plus a cost_unconfirmed column so an unconfirmed-price row is
// distinguishable from a merely estimated one.
package ledger

import (
	"database/sql"
	"encoding/json"
	"os"

	"github.com/jackofall1232/l00prite/cli-os/internal/oai"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

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
		if f, ferr := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); ferr == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	return id
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
