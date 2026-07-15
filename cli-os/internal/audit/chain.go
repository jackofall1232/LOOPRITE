// Package audit owns append-only, hash-chained privileged-action records.
package audit

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

const SchemaVersion = 1

func digest(prev, id, ts, actor, action, detail, correlation string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{prev, id, ts, actor, action, detail, correlation}, "\x00")))
	return hex.EncodeToString(h[:])
}

func Append(db *sql.DB, actor, action, detail, correlation string) error {
	_, err := state.Tx(db, func(q state.Querier) (bool, error) {
		var prev sql.NullString
		_ = q.QueryRowContext(state.Ctx(), `SELECT entry_hash FROM audit WHERE entry_hash IS NOT NULL ORDER BY rowid DESC LIMIT 1`).Scan(&prev)
		id, ts := util.RID("aud"), util.NowISO()
		hash := digest(prev.String, id, ts, actor, action, detail, correlation)
		_, err := q.ExecContext(state.Ctx(),
			`INSERT INTO audit(id,ts,actor,action,detail,schema_version,correlation_id,prev_hash,entry_hash) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, ts, actor, action, nullable(detail), SchemaVersion, nullable(correlation), nullable(prev.String), hash)
		return true, err
	})
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func Verify(db *sql.DB) error {
	rows, err := db.QueryContext(state.Ctx(), `SELECT id,ts,actor,action,COALESCE(detail,''),COALESCE(correlation_id,''),COALESCE(prev_hash,''),entry_hash FROM audit WHERE entry_hash IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	prev := ""
	for rows.Next() {
		var id, ts, actor, action, detail, correlation, storedPrev, hash string
		if err := rows.Scan(&id, &ts, &actor, &action, &detail, &correlation, &storedPrev, &hash); err != nil {
			return err
		}
		if storedPrev != prev || digest(prev, id, ts, actor, action, detail, correlation) != hash {
			return sql.ErrNoRows
		}
		prev = hash
	}
	return rows.Err()
}
