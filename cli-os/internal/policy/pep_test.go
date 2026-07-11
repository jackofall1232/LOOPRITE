package policy

import (
	"math"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

func testDB(t *testing.T) (*config.Config, func()) {
	t.Helper()
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	return &cfg, func() {}
}

func TestReserveRespectsCapAndCommitRefund(t *testing.T) {
	cfg, _ := testDB(t)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT OR REPLACE INTO caps(project,window,limit_usd) VALUES('p','daily',1.0)`); err != nil {
		t.Fatalf("cap: %v", err)
	}

	a := Reserve(db, "p", 0.6, 10)
	if !a.OK {
		t.Fatalf("first reserve should succeed")
	}
	b := Reserve(db, "p", 0.6, 10)
	if b.OK || b.Reason != "cost_cap" {
		t.Fatalf("second reserve should breach the $1 cap: %+v", b)
	}
	Refund(db, a.ReservationID)
	c := Reserve(db, "p", 0.6, 10)
	if !c.OK {
		t.Fatalf("after refund, budget frees up")
	}
	Commit(db, c.ReservationID, 0.42)
	s := GetSpend(db, "p", 10)
	if math.Abs(s.Committed-0.42) > 1e-9 {
		t.Fatalf("committed want 0.42 got %v", s.Committed)
	}
	if math.Abs(s.Reserved) > 1e-9 {
		t.Fatalf("reserved want 0 got %v", s.Reserved)
	}
}

func TestReserveRejectsBadAmounts(t *testing.T) {
	cfg, _ := testDB(t)
	db, _ := state.Open(cfg.DBPath)
	defer db.Close()
	if Reserve(db, "p", math.NaN(), 10).OK {
		t.Fatalf("NaN must be rejected")
	}
	if Reserve(db, "p", -5, 10).OK {
		t.Fatalf("negative must be rejected")
	}
	if Reserve(db, "p", math.Inf(1), 10).OK {
		t.Fatalf("Inf must be rejected")
	}
}

// TestCapForDistinguishesMissingRowFromRealError pins the exact bug flagged in PR #8's Gemini
// review: a missing caps row (no explicit cap configured — the normal, common case) must return
// defaultCap with a NIL error, but a genuine read failure (a corrupt/unreadable table) must return
// a non-nil error instead of silently substituting defaultCap. Before the fix, capFor conflated
// both cases into the same "return defaultCap" branch — this test fails against that code because
// the corrupted-table case returned (CapInfo{LimitUSD: defaultCap}, no error) instead of an error.
func TestCapForDistinguishesMissingRowFromRealError(t *testing.T) {
	cfg, _ := testDB(t)
	db, _ := state.Open(cfg.DBPath)
	defer db.Close()

	ci, err := capFor(db, "no-such-project", 7)
	if err != nil {
		t.Fatalf("missing row (sql.ErrNoRows) must not be treated as a real error, got: %v", err)
	}
	if ci.LimitUSD != 7 || ci.Unlimited || ci.Explicit {
		t.Fatalf("missing row should yield the plain default cap, got: %+v", ci)
	}

	if _, err := db.Exec(`DROP TABLE caps`); err != nil {
		t.Fatalf("drop caps: %v", err)
	}
	if _, err := capFor(db, "p", 7); err == nil {
		t.Fatalf("a real read failure (dropped table) must be returned as an error, not silently swallowed into the default cap")
	}
}

// TestReserveFailsClosedOnCapReadError pins Reserve's half of the same fix: a real cap-read
// failure must deny the reservation (fail closed), never silently fall back to defaultCap — which
// could be a HIGHER, more permissive ceiling than whatever the project actually had configured.
// This test fails against the pre-fix code because Reserve would ignore capFor's (previously
// error-less) return value entirely and happily reserve against defaultCap.
func TestReserveFailsClosedOnCapReadError(t *testing.T) {
	cfg, _ := testDB(t)
	db, _ := state.Open(cfg.DBPath)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE caps`); err != nil {
		t.Fatalf("drop caps: %v", err)
	}
	r := Reserve(db, "p", 0.01, 1000) // a huge defaultCap: if this silently succeeds, the fail-closed guarantee is broken
	if r.OK {
		t.Fatalf("Reserve must fail closed when the cap table is unreadable, got OK=true: %+v", r)
	}
}

func TestReserveOveragePctExtendsButDoesNotEliminateCap(t *testing.T) {
	cfg, _ := testDB(t)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// limit $1, overage 100% -> effective ceiling $2.
	if _, err := db.Exec(`INSERT INTO caps(project,window,limit_usd,overage_pct,unlimited) VALUES('p','daily',1.0,100,0)`); err != nil {
		t.Fatalf("cap: %v", err)
	}

	a := Reserve(db, "p", 1.0, 10)
	if !a.OK {
		t.Fatalf("first reserve within base limit should succeed: %+v", a)
	}
	b := Reserve(db, "p", 1.0, 10)
	if !b.OK {
		t.Fatalf("second reserve should succeed within the overage-extended ceiling: %+v", b)
	}
	if math.Abs(b.Cap-2.0) > 1e-9 {
		t.Fatalf("effective cap want 2.0 got %v", b.Cap)
	}
	c := Reserve(db, "p", 0.01, 10)
	if c.OK || c.Reason != "cost_cap" {
		t.Fatalf("reserve past the overage-extended ceiling should deny: %+v", c)
	}
	if math.Abs(c.Cap-2.0) > 1e-9 {
		t.Fatalf("denial cap want the effective ceiling 2.0 got %v", c.Cap)
	}
}

func TestReserveUnlimitedNeverDeniesButStillRecordsSpend(t *testing.T) {
	cfg, _ := testDB(t)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// limit $1 but unlimited=1 -> the limit is meaningless; nothing should ever deny on cost.
	if _, err := db.Exec(`INSERT INTO caps(project,window,limit_usd,overage_pct,unlimited) VALUES('p','daily',1.0,0,1)`); err != nil {
		t.Fatalf("cap: %v", err)
	}

	r := Reserve(db, "p", 100.0, 10) // 100x the stored limit
	if !r.OK {
		t.Fatalf("unlimited project must never be denied on cost: %+v", r)
	}
	Commit(db, r.ReservationID, 100.0)
	s := GetSpend(db, "p", 10)
	if !s.Unlimited {
		t.Fatalf("GetSpend must report Unlimited=true")
	}
	if math.Abs(s.Committed-100.0) > 1e-9 {
		t.Fatalf("spend must still be metered even when unlimited: committed want 100 got %v", s.Committed)
	}
}
