// Package policy is the Policy Enforcement Point. Enforcement lives HERE, over the transactional
// store, NOT in the request handler that would benefit from ignoring it. A handler RESERVES budget
// before a provider call (the reservation is the ceiling) and COMMITS the real cost after. Budgets
// are enforced in DOLLARS, per project, per UTC day. Ported from pep.js.
package policy

import (
	"database/sql"
	"math"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// ReserveResult mirrors the reserve() return contract.
type ReserveResult struct {
	OK            bool
	ReservationID string
	Reason        string // "invalid_amount" | "cost_cap"
	Cap           float64
	Spent         float64
	Requested     float64
}

// Spend is the current daily spend snapshot.
type Spend struct {
	Day       string
	Cap       float64
	Reserved  float64
	Committed float64
}

func capFor(q state.Querier, project string, defaultCap float64) float64 {
	var limit float64
	err := q.QueryRowContext(state.Ctx(),
		`SELECT limit_usd FROM caps WHERE project = ? AND window = 'daily'`, project).Scan(&limit)
	if err != nil {
		return defaultCap
	}
	return limit
}

func spendRow(q state.Querier, project, day string) (reserved, committed float64, err error) {
	if _, err = q.ExecContext(state.Ctx(),
		`INSERT OR IGNORE INTO spend(project,day,reserved_usd,committed_usd) VALUES(?,?,0,0)`, project, day); err != nil {
		return 0, 0, err
	}
	err = q.QueryRowContext(state.Ctx(),
		`SELECT reserved_usd, committed_usd FROM spend WHERE project = ? AND day = ?`, project, day).
		Scan(&reserved, &committed)
	return reserved, committed, err
}

// Reserve atomically reserves amountUsd, denying if it would breach the daily cap. Fails closed on
// non-finite / negative amounts.
func Reserve(db *sql.DB, project string, amountUsd, defaultCap float64) ReserveResult {
	if math.IsNaN(amountUsd) || math.IsInf(amountUsd, 0) || amountUsd < 0 {
		return ReserveResult{OK: false, Reason: "invalid_amount", Requested: amountUsd}
	}
	day := util.UTCDay()
	res, err := state.Tx(db, func(q state.Querier) (ReserveResult, error) {
		cap := capFor(q, project, defaultCap)
		reserved, committed, serr := spendRow(q, project, day)
		if serr != nil {
			return ReserveResult{}, serr // fail CLOSED: a DB read failure must not proceed on unknown spend
		}
		inUse := reserved + committed
		if inUse+amountUsd > cap+1e-9 {
			return ReserveResult{OK: false, Reason: "cost_cap", Cap: cap, Spent: inUse, Requested: amountUsd}, nil
		}
		id := util.RID("rsv")
		if _, err := q.ExecContext(state.Ctx(),
			`INSERT INTO reservations(id,project,day,amount_usd,state,created_at) VALUES(?,?,?,?,'reserved',?)`,
			id, project, day, amountUsd, util.NowISO()); err != nil {
			return ReserveResult{}, err
		}
		if _, err := q.ExecContext(state.Ctx(),
			`UPDATE spend SET reserved_usd = reserved_usd + ? WHERE project = ? AND day = ?`,
			amountUsd, project, day); err != nil {
			return ReserveResult{}, err
		}
		return ReserveResult{OK: true, ReservationID: id, Cap: cap, Spent: inUse}, nil
	})
	if err != nil {
		// A transaction/DB error denies (fail closed) rather than silently proceeding.
		return ReserveResult{OK: false, Reason: "internal_error"}
	}
	return res
}

// Commit reconciles a reservation with the real cost. Returns true if a reserved row was committed.
func Commit(db *sql.DB, reservationID string, actualUsd float64) bool {
	ok, _ := state.Tx(db, func(q state.Querier) (bool, error) {
		var (
			rProject, rDay, rState string
			rAmount                float64
		)
		err := q.QueryRowContext(state.Ctx(),
			`SELECT project, day, amount_usd, state FROM reservations WHERE id = ?`, reservationID).
			Scan(&rProject, &rDay, &rAmount, &rState)
		if err != nil || rState != "reserved" {
			return false, nil
		}
		if _, err := q.ExecContext(state.Ctx(),
			`UPDATE spend SET reserved_usd = reserved_usd - ?, committed_usd = committed_usd + ? WHERE project = ? AND day = ?`,
			rAmount, actualUsd, rProject, rDay); err != nil {
			return false, err
		}
		if _, err := q.ExecContext(state.Ctx(),
			`UPDATE reservations SET state = 'committed', amount_usd = ? WHERE id = ?`,
			actualUsd, reservationID); err != nil {
			return false, err
		}
		return true, nil
	})
	return ok
}

// Refund releases a still-reserved reservation. Returns true on success.
func Refund(db *sql.DB, reservationID string) bool {
	ok, _ := state.Tx(db, func(q state.Querier) (bool, error) {
		var (
			rProject, rDay, rState string
			rAmount                float64
		)
		err := q.QueryRowContext(state.Ctx(),
			`SELECT project, day, amount_usd, state FROM reservations WHERE id = ?`, reservationID).
			Scan(&rProject, &rDay, &rAmount, &rState)
		if err != nil || rState != "reserved" {
			return false, nil
		}
		if _, err := q.ExecContext(state.Ctx(),
			`UPDATE spend SET reserved_usd = reserved_usd - ? WHERE project = ? AND day = ?`,
			rAmount, rProject, rDay); err != nil {
			return false, err
		}
		if _, err := q.ExecContext(state.Ctx(),
			`UPDATE reservations SET state = 'refunded' WHERE id = ?`, reservationID); err != nil {
			return false, err
		}
		return true, nil
	})
	return ok
}

// ReapStaleReservations refunds any reservation still 'reserved' after maxAgeMs. Returns the count.
func ReapStaleReservations(db *sql.DB, maxAgeMs int64) int {
	cutoff := util.ISOFromTime(time.Now().Add(-time.Duration(maxAgeMs) * time.Millisecond))
	n, _ := state.Tx(db, func(q state.Querier) (int, error) {
		rows, err := q.QueryContext(state.Ctx(),
			`SELECT id, project, day, amount_usd FROM reservations WHERE state = 'reserved' AND created_at < ?`, cutoff)
		if err != nil {
			return 0, err
		}
		type stale struct {
			id, project, day string
			amount           float64
		}
		var list []stale
		for rows.Next() {
			var s stale
			if err := rows.Scan(&s.id, &s.project, &s.day, &s.amount); err != nil {
				continue
			}
			list = append(list, s)
		}
		rows.Close()
		for _, s := range list {
			if _, err := q.ExecContext(state.Ctx(),
				`UPDATE spend SET reserved_usd = MAX(0, reserved_usd - ?) WHERE project = ? AND day = ?`,
				s.amount, s.project, s.day); err != nil {
				return 0, err
			}
			if _, err := q.ExecContext(state.Ctx(),
				`UPDATE reservations SET state = 'refunded' WHERE id = ?`, s.id); err != nil {
				return 0, err
			}
		}
		return len(list), nil
	})
	return n
}

// GetSpend returns the current daily spend snapshot for a project.
func GetSpend(db *sql.DB, project string, defaultCap float64) Spend {
	day := util.UTCDay()
	s, _ := state.Tx(db, func(q state.Querier) (Spend, error) {
		reserved, committed, _ := spendRow(q, project, day) // best-effort read for display
		return Spend{Day: day, Cap: capFor(q, project, defaultCap), Reserved: reserved, Committed: committed}, nil
	})
	return s
}

// AcquireLease grabs a concurrency lease (session pool / per-repo write). Returns ("", false) if full.
func AcquireLease(db *sql.DB, project, kind string, ttlSec, max int) (string, bool) {
	if ttlSec <= 0 {
		ttlSec = 300
	}
	if max <= 0 {
		max = 1
	}
	id, _ := state.Tx(db, func(q state.Querier) (string, error) {
		now := time.Now()
		if _, err := q.ExecContext(state.Ctx(),
			`DELETE FROM leases WHERE expires_at < ?`, util.ISOFromTime(now)); err != nil {
			return "", err
		}
		var active int
		_ = q.QueryRowContext(state.Ctx(),
			`SELECT COUNT(*) FROM leases WHERE project = ? AND kind = ?`, project, kind).Scan(&active)
		if active >= max {
			return "", nil
		}
		leaseID := util.RID("lease")
		if _, err := q.ExecContext(state.Ctx(),
			`INSERT INTO leases(id,project,kind,created_at,expires_at) VALUES(?,?,?,?,?)`,
			leaseID, project, kind, util.ISOFromTime(now), util.ISOFromTime(now.Add(time.Duration(ttlSec)*time.Second))); err != nil {
			return "", err
		}
		return leaseID, nil
	})
	return id, id != ""
}

// ReleaseLease drops a lease by id.
func ReleaseLease(db *sql.DB, id string) {
	_, _ = db.ExecContext(state.Ctx(), `DELETE FROM leases WHERE id = ?`, id)
}

// Grant is a per-action permission grant for the destructive-action gate.
type Grant struct {
	Action    string
	ExpiresAt string
}

// PermissionResult is the outcome of a destructive-action gate check.
type PermissionResult struct {
	OK     bool
	Reason string
	Action string
}

// RequirePermission enforces the destructive-action gate: denies unless a matching, unexpired grant
// is presented. v1 has no destructive surface in the gateway, so this defaults to deny.
func RequirePermission(action string, grant *Grant) PermissionResult {
	if grant == nil || grant.Action != action {
		return PermissionResult{OK: false, Reason: "permission_required", Action: action}
	}
	if t, err := parseISO(grant.ExpiresAt); err != nil || t.Before(time.Now()) {
		return PermissionResult{OK: false, Reason: "permission_required", Action: action}
	}
	return PermissionResult{OK: true}
}

func parseISO(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05.000Z07:00", s)
}
