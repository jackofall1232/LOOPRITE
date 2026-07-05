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
