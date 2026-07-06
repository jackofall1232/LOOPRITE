package ledger

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// fixture is the Entry used by every test below. Its fields are deliberately identical across all
// calls (only the "id"/"ts" mirror fields vary between rows, and both are fixed-width -- util.RID's
// hex suffix and util.NowISO's UTC timestamp are constant length) so every JSONL row this package
// writes in these tests has exactly the same byte length. That determinism is what lets the rotation
// tests size a threshold relative to a measured row size and predict, exactly, how many rows land on
// each side of a rotation.
var fixture = Entry{RequestID: "row", Outcome: "ok"}

// testDB opens a throwaway SQLite-backed ledger DB (real schema via state.Open, same as production)
// so Append's SQLite insert has somewhere real to write.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// setLedgerMaxBytesForTest points LOOPRITE_LEDGER_MAX_BYTES at raw and resets the package's
// read-once cache so the next call to ledgerMaxBytes() re-reads it (production code reads the env
// var exactly once per process; tests need to override it per-subtest, so this resets that cache
// instead of inventing a second configuration path). Restores both the env var and the cache after
// the test so later tests in the same run see a clean read-once state.
func setLedgerMaxBytesForTest(t *testing.T, raw string) {
	t.Helper()
	prev, had := os.LookupEnv("LOOPRITE_LEDGER_MAX_BYTES")
	if raw == "" {
		os.Unsetenv("LOOPRITE_LEDGER_MAX_BYTES")
	} else {
		os.Setenv("LOOPRITE_LEDGER_MAX_BYTES", raw)
	}
	ledgerMaxBytesOnce = sync.Once{}
	t.Cleanup(func() {
		if had {
			os.Setenv("LOOPRITE_LEDGER_MAX_BYTES", prev)
		} else {
			os.Unsetenv("LOOPRITE_LEDGER_MAX_BYTES")
		}
		ledgerMaxBytesOnce = sync.Once{}
	})
}

// countJSONLines asserts every non-empty line in path parses as valid JSON (failing the test
// otherwise) and returns how many lines it found. A missing file counts as zero lines.
func countJSONLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(line, &v); err != nil {
			t.Fatalf("corrupted/truncated JSON line in %s: %v (line=%q)", path, err, line)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return n
}

// probeRowSize writes one fixture row (with rotation effectively disabled) to measure the exact
// on-disk byte length of a JSONL row, then removes the probe file so the caller starts clean.
func probeRowSize(t *testing.T, db *sql.DB, ledgerPath string) int64 {
	t.Helper()
	setLedgerMaxBytesForTest(t, "1000000000") // effectively unlimited while probing
	Append(db, ledgerPath, fixture)
	info, err := os.Stat(ledgerPath)
	if err != nil {
		t.Fatalf("stat probe ledger: %v", err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatalf("remove probe ledger: %v", err)
	}
	return info.Size()
}

// TestAppendRotatesJSONLWhenThresholdCrossed proves rotation actually happens once the configured
// size threshold is crossed: it writes n rows sequentially with a threshold sized (from a measured
// row size) to trip exactly once partway through, then asserts both the live file and its ".1"
// backup exist and that every row across the two is accounted for (none dropped, none duplicated).
func TestAppendRotatesJSONLWhenThresholdCrossed(t *testing.T) {
	db := testDB(t)
	ledgerPath := filepath.Join(t.TempDir(), "ledger.jsonl")

	rowSize := probeRowSize(t, db, ledgerPath)

	const n = 10
	// Threshold ~5.5 rows: after 6 rows the file exceeds it, forcing exactly one rotation: the first
	// 6 rows land in the ".1" backup and the remaining 4 land in the fresh current file.
	threshold := rowSize*(n/2) + rowSize/2
	setLedgerMaxBytesForTest(t, strconv.FormatInt(threshold, 10))

	for i := 0; i < n; i++ {
		Append(db, ledgerPath, fixture)
	}

	backupPath := ledgerPath + ".1"
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("current ledger file missing after rotation: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected a %s backup to exist after crossing the threshold, but it does not: %v", backupPath, err)
	}

	curLines := countJSONLines(t, ledgerPath)
	backupLines := countJSONLines(t, backupPath)
	if curLines != 4 {
		t.Errorf("current file: want 4 rows post-rotation, got %d", curLines)
	}
	if backupLines != 6 {
		t.Errorf("backup file: want 6 rows (the pre-rotation generation), got %d", backupLines)
	}
	if total := curLines + backupLines; total != n {
		t.Fatalf("expected all %d appended rows accounted for across current+backup, got %d (current=%d backup=%d)",
			n, total, curLines, backupLines)
	}
}

// TestAppendConcurrentRotationSafe drives N goroutines calling Append concurrently against the same
// ledgerPath with a threshold sized to force exactly one rotation somewhere in the middle of the
// run. It proves: no panic; the mutex correctly serializes concurrent rotate-vs-append attempts (a
// naive "check size, then maybe rename, then append" without a lock could have two goroutines rename
// the file out from under each other, or both decide to rotate and clobber the same backup); every
// line in both resulting files parses as valid JSON (no torn/interleaved writes); and the row count
// across current+backup equals N (nothing silently lost). Run with -race to additionally confirm no
// data race around the mutex/cached threshold.
//
// Only one rotation is forced (not several): this package deliberately keeps a single backup
// generation (ledgerPath+".1"), so a second rotation during the same run would legitimately overwrite
// the first backup and age out those earlier rows -- that is the intended size-bounding trade-off,
// not data loss to guard against. Conservation of "every row landed somewhere" is only a meaningful,
// checkable invariant across a single rotation event, which is what this test exercises.
func TestAppendConcurrentRotationSafe(t *testing.T) {
	db := testDB(t)
	ledgerPath := filepath.Join(t.TempDir(), "ledger.jsonl")

	rowSize := probeRowSize(t, db, ledgerPath)

	const n = 20
	// Threshold ~10.5 rows: forces exactly one rotation partway through the 20 concurrent appends,
	// regardless of goroutine interleaving order (the property depends only on how many rows have
	// been serialized into the current generation so far, not on which goroutine wrote which row).
	threshold := rowSize*(n/2) + rowSize/2
	setLedgerMaxBytesForTest(t, strconv.FormatInt(threshold, 10))

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			Append(db, ledgerPath, fixture)
		}()
	}
	wg.Wait()

	backupPath := ledgerPath + ".1"
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected concurrent appends to force a rotation, but %s does not exist: %v", backupPath, err)
	}

	curLines := countJSONLines(t, ledgerPath)    // every line already validated as parseable JSON
	backupLines := countJSONLines(t, backupPath) // every line already validated as parseable JSON
	if total := curLines + backupLines; total != n {
		t.Fatalf("expected all %d concurrent appends accounted for across current+backup, got %d (current=%d backup=%d)",
			n, total, curLines, backupLines)
	}
}

// TestLedgerMaxBytesEnvFallback proves an invalid LOOPRITE_LEDGER_MAX_BYTES (empty/unset, negative,
// zero, or non-numeric) falls back to defaultLedgerMaxBytes rather than disabling rotation (0 or a
// negative number would be nonsensical as a byte threshold) or crashing.
func TestLedgerMaxBytesEnvFallback(t *testing.T) {
	for _, raw := range []string{"", "-5", "0", "not-a-number", "  ", "5.5"} {
		t.Run(fmt.Sprintf("raw=%q", raw), func(t *testing.T) {
			setLedgerMaxBytesForTest(t, raw)
			if got := ledgerMaxBytes(); got != defaultLedgerMaxBytes {
				t.Fatalf("env=%q: want fallback to default %d, got %d", raw, defaultLedgerMaxBytes, got)
			}
		})
	}
}

// TestLedgerMaxBytesEnvOverrideValid proves a valid positive override is actually honored (the
// fallback test above only proves invalid inputs are rejected; this proves the override path works
// at all).
func TestLedgerMaxBytesEnvOverrideValid(t *testing.T) {
	setLedgerMaxBytesForTest(t, "12345")
	if got := ledgerMaxBytes(); got != 12345 {
		t.Fatalf("want overridden threshold 12345, got %d", got)
	}
}
