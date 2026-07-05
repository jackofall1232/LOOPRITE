// Freshness is the read-only staleness view of a repo's .l00prite memory files, used by the
// dashboard's "memory freshness" panel. It reports each canonical memory source's presence, last
// modified time, byte size, and whether it has crossed the same 30-day staleness threshold the
// ranking path applies — so the dashboard shows REAL mtime-based freshness, never a fabricated %.
package memory

import (
	"os"
	"path/filepath"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// FileFreshness is the freshness of one canonical .l00prite memory file.
type FileFreshness struct {
	Name      string `json:"name"`       // e.g. "constraints.md"
	Kind      string `json:"kind"`       // the memory kind this file contributes (constraint, architecture, …)
	Present   bool   `json:"present"`    // the file exists and is inside the repo root
	AsOf      string `json:"as_of"`      // ISO mtime, "" when absent
	Stale     bool   `json:"stale"`      // mtime older than the 30-day staleness threshold
	SizeBytes int64  `json:"size_bytes"` // file size in bytes, 0 when absent
}

// FreshnessReport is the repo-level rollup of memory-file freshness.
type FreshnessReport struct {
	Status       string          `json:"status"` // ok | degraded | no_repo | no_memory_dir | empty
	Dir          string          `json:"dir"`    // absolute .l00prite dir (informational)
	Files        []FileFreshness `json:"files"`
	PresentCount int             `json:"present_count"`
	StaleCount   int             `json:"stale_count"`
	TotalFiles   int             `json:"total_files"` // number of canonical sources checked
}

// staleAfterDays mirrors the ranking path's freshness threshold (see Query: ageDays > 30).
const staleAfterDays = 30

// RepoFreshness stats each canonical memory source under repoRoot/.l00prite and reports real
// mtime-based freshness. It performs NO ranking, NO token budgeting, and reads no file contents —
// only os.Stat — so it is cheap enough to call for every repo on a dashboard refresh. Path access is
// confined to the repo root via the same symlink-resolving containment check as the ranking path.
func RepoFreshness(repoRoot string) FreshnessReport {
	rep := FreshnessReport{Status: "empty", TotalFiles: len(sources)}
	if repoRoot == "" {
		rep.Status = "no_repo"
		return rep
	}
	dir := filepath.Join(repoRoot, ".l00prite")
	rep.Dir = dir
	if !within(repoRoot, dir) {
		rep.Status = "no_memory_dir"
		return rep
	}
	if _, err := os.Stat(dir); err != nil {
		rep.Status = "no_memory_dir"
		return rep
	}
	for _, s := range sources {
		f := FileFreshness{Name: s.file, Kind: s.kind}
		fp := filepath.Join(dir, s.file)
		if within(repoRoot, fp) {
			if st, err := os.Stat(fp); err == nil && !st.IsDir() {
				f.Present = true
				f.SizeBytes = st.Size()
				f.AsOf = util.ISOFromTime(st.ModTime())
				f.Stale = time.Since(st.ModTime()).Hours()/24 > staleAfterDays
				rep.PresentCount++
				if f.Stale {
					rep.StaleCount++
				}
			}
		}
		rep.Files = append(rep.Files, f)
	}
	switch {
	case rep.PresentCount == 0:
		rep.Status = "empty"
	case rep.StaleCount > 0:
		rep.Status = "degraded"
	default:
		rep.Status = "ok"
	}
	return rep
}
