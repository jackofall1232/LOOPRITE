package audit

import (
	"errors"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

func TestAuditChainDetectsTampering(t *testing.T) {
	db, err := state.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Append(db, "tok_1", "budget.set", "daily=10", "req_1"); err != nil {
		t.Fatal(err)
	}
	if err := Append(db, "tok_1", "provider.add", "anthropic", "req_2"); err != nil {
		t.Fatal(err)
	}
	if err := Verify(db); err != nil {
		t.Fatalf("valid chain: %v", err)
	}
	if _, err := db.Exec(`UPDATE audit SET detail='daily=1000' WHERE action='budget.set'`); err != nil {
		t.Fatal(err)
	}
	if err := Verify(db); !errors.Is(err, ErrChainIntegrity) {
		t.Fatalf("tampered chain error = %v, want ErrChainIntegrity", err)
	}
}
