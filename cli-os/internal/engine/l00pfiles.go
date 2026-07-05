package engine

// l00pfiles.go is the engine's IO layer for a target repo's .l00prite/ memory directory. It
// implements the l00prite protocol mechanically: surgical JSON edits that preserve unknown
// fields, append-only ledger/failures logs, the cooperative lock/lease convention
// (LOCKING.md rules 1-7), the Autonomous-Edit Denylist matcher that guards the
// destructive_operation_required boundary, and never-overwrite scaffolding.
//
// Protocol sources this file must track:
//   - templates/l00prite/prompts/execute-loop.md (pre-flight, iteration, boundaries, exit)
//   - .l00prite/LOCKING.md (lock rules + field semantics + protected paths)
//   - templates/l00prite/{heartbeat.json,state.json,lock.json,ledger.md,constraints.md}
//
// Design invariants:
//   - JSON files are edited surgically (unmarshal -> mutate named keys -> marshal 2-space +
//     trailing newline -> temp file + os.Rename). Unknown fields survive every write.
//   - ledger.md / failures.md are append-only; append errors are RETURNED, never swallowed.
//   - Scaffolding never overwrites an existing file; it creates only what is missing.
//   - The engine's lock identity is owner_agent "l00prite-os", owner_session = the run id.
//
// The big embedded protocol texts (LOCKING.md, the denylist block, the heartbeat/state/lock
// templates) are scaffold-scoped raw strings at the bottom of this file. They were copied
// from templates/l00prite@v1.1 and MUST be updated in lockstep with protocol changes there.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

const (
	// engineAgent is the engine's lock identity (owner_agent). owner_session is the run id.
	engineAgent = "l00prite-os"
	// defaultTTLSec is the LOCKING.md default lease length (30 minutes).
	defaultTTLSec = 1800
)

// Files is the IO handle for one target repo's .l00prite/ directory.
type Files struct {
	Root string // target repo root; the protocol dir is filepath.Join(Root, ".l00prite")
}

func (f Files) dir() string { return filepath.Join(f.Root, ".l00prite") }

// LockInfo is a parsed lock.json. Raw holds the full parsed file so a surgical rewrite
// preserves any unknown fields another agent may have written.
type LockInfo struct {
	LockID       string
	OwnerAgent   string
	OwnerSession string
	AcquiredAt   string
	ExpiresAt    string
	Purpose      string
	Status       string
	TTLSeconds   int
	Raw          map[string]any
}

// MemorySnapshot is everything the engine reads from .l00prite/ at pre-flight and each
// iteration. Missing files are zero values; a malformed JSON file is surfaced in the error
// return of ReadSnapshot (fail-closed: the caller must know memory is corrupt).
type MemorySnapshot struct {
	HasL00prite bool

	Blueprint   string
	Ledger      string
	Memory      string
	Constraints string
	Failures    string
	Todos       string

	Heartbeat map[string]any // nil if absent/unparseable
	State     map[string]any // nil if absent/unparseable
	Lock      *LockInfo      // nil if absent

	PendingEvents []string // filenames in events/pending, excluding README.md and dotfiles
}

// ---- snapshot ----

// ReadSnapshot reads every .l00prite memory file. Missing files are not errors (zero
// values); a malformed JSON file (heartbeat/state/lock) IS surfaced in the error return.
func (f Files) ReadSnapshot() (MemorySnapshot, error) {
	dir := f.dir()
	var snap MemorySnapshot
	var errs []error

	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		snap.HasL00prite = true
	}

	snap.Blueprint = readText(filepath.Join(dir, "blueprint.md"))
	snap.Ledger = readText(filepath.Join(dir, "ledger.md"))
	snap.Memory = readText(filepath.Join(dir, "memory.md"))
	snap.Constraints = readText(filepath.Join(dir, "constraints.md"))
	snap.Failures = readText(filepath.Join(dir, "failures.md"))
	snap.Todos = readText(filepath.Join(dir, "todos.md"))

	if m, err := readJSONMap(filepath.Join(dir, "heartbeat.json")); err != nil {
		errs = append(errs, err)
	} else {
		snap.Heartbeat = m
	}
	if m, err := readJSONMap(filepath.Join(dir, "state.json")); err != nil {
		errs = append(errs, err)
	} else {
		snap.State = m
	}
	if lock, err := f.ReadLock(); err != nil {
		errs = append(errs, err)
	} else {
		snap.Lock = lock
	}

	snap.PendingEvents = f.listPending()

	return snap, errors.Join(errs...)
}

func (f Files) listPending() []string {
	entries, err := os.ReadDir(filepath.Join(f.dir(), "events", "pending"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || n == "README.md" || strings.HasPrefix(n, ".") {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ---- lock lifecycle (LOCKING.md rules 1-7) ----

// ReadLock parses lock.json. Absent -> (nil, nil); malformed -> (nil, error).
func (f Files) ReadLock() (*LockInfo, error) {
	path := filepath.Join(f.dir(), "lock.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("lock.json is malformed: %w", err)
	}
	return parseLock(m), nil
}

// LockAvailability classifies a lock relative to a session, evaluating expiry by comparing
// expires_at with util.NowISO() lexicographically (the millisecond ISO format the engine
// writes makes string comparison a correct time comparison).
//
//	"free"    -> nil lock, or status unlocked/released
//	"stale"   -> status expired, OR status active with expires_at <= now
//	"mine"    -> active, unexpired, owned by (l00prite-os, session)
//	"foreign" -> active, unexpired, owned by someone else (also the fail-closed default)
func LockAvailability(l *LockInfo, session string) string {
	if l == nil {
		return "free"
	}
	switch l.Status {
	case "unlocked", "released":
		return "free"
	case "expired":
		return "stale"
	case "active":
		now := util.NowISO()
		if l.ExpiresAt == "" || l.ExpiresAt <= now {
			return "stale"
		}
		if l.OwnerAgent == engineAgent && l.OwnerSession == session {
			return "mine"
		}
		return "foreign"
	default:
		// Fail-closed on an unrecognized status: treat it as an unownable foreign lock so a
		// corrupt file forces a human rather than being silently reclaimed.
		return "foreign"
	}
}

// AcquireLock takes the lease for (l00prite-os, session). Allowed only when availability is
// "free" or "stale"; a "stale" reclamation returns the PRIOR LockInfo (second return) so the
// caller can record the reclamation in ledger.md per LOCKING.md rule 4. Otherwise it refuses.
func (f Files) AcquireLock(session, purpose string, ttlSec int) (acquired *LockInfo, prior *LockInfo, err error) {
	if ttlSec <= 0 {
		ttlSec = defaultTTLSec
	}
	cur, rerr := f.ReadLock()
	if rerr != nil {
		return nil, nil, rerr
	}
	switch LockAvailability(cur, session) {
	case "free", "stale":
		// proceed
	case "mine":
		return nil, nil, fmt.Errorf("cannot acquire lock: already held by this session %q", session)
	default: // foreign
		owner := "unknown"
		exp := ""
		if cur != nil {
			owner = cur.OwnerAgent + "/" + cur.OwnerSession
			exp = cur.ExpiresAt
		}
		return nil, nil, fmt.Errorf("foreign lock held by %s (expires %s)", owner, exp)
	}

	// Build the new lock in a fresh map that carries over any unknown fields, leaving cur.Raw
	// (and thus the returned prior) untouched.
	m := map[string]any{}
	if cur != nil && cur.Raw != nil {
		for k, v := range cur.Raw {
			m[k] = v
		}
	}
	t := time.Now()
	if _, ok := m["schema_version"]; !ok {
		m["schema_version"] = 1
	}
	m["lock_id"] = "lock-" + t.UTC().Format("20060102-150405") + "-" + engineAgent + "-" + session
	m["owner_agent"] = engineAgent
	m["owner_session"] = session
	m["acquired_at"] = util.ISOFromTime(t)
	m["ttl_seconds"] = ttlSec
	m["expires_at"] = util.ISOFromTime(t.Add(time.Duration(ttlSec) * time.Second))
	m["purpose"] = purpose
	m["status"] = "active"
	if _, ok := m["protected_paths"]; !ok {
		m["protected_paths"] = defaultProtectedPaths()
	}
	if werr := f.writeLock(m); werr != nil {
		return nil, nil, werr
	}
	if LockAvailability(cur, session) == "stale" {
		prior = cur
	}
	return parseLock(m), prior, nil
}

// RefreshLock extends expires_at from now, but only for a lock owned by this session
// (LOCKING.md rule 7; rule 3 permits refreshing your own already-expired lock before the
// next write, so ownership — not unexpired "mine" — is the gate here).
func (f Files) RefreshLock(session string, ttlSec int) error {
	cur, err := f.ReadLock()
	if err != nil {
		return err
	}
	if !ownedByUs(cur, session) {
		return fmt.Errorf("cannot refresh lock: not owned by session %q", session)
	}
	if ttlSec <= 0 {
		ttlSec = cur.TTLSeconds
		if ttlSec <= 0 {
			ttlSec = defaultTTLSec
		}
	}
	t := time.Now()
	m := cur.Raw
	m["ttl_seconds"] = ttlSec
	m["expires_at"] = util.ISOFromTime(t.Add(time.Duration(ttlSec) * time.Second))
	m["status"] = "active"
	return f.writeLock(m)
}

// ReleaseLock sets status "released" and nulls owner_agent/owner_session/purpose, but only
// for a lock owned by this session (LOCKING.md rule 5). lock_id/acquired_at/expires_at are
// left as history, matching the template's released shape.
func (f Files) ReleaseLock(session string) error {
	cur, err := f.ReadLock()
	if err != nil {
		return err
	}
	if !ownedByUs(cur, session) {
		return fmt.Errorf("cannot release lock: not owned by session %q", session)
	}
	m := cur.Raw
	m["status"] = "released"
	m["owner_agent"] = nil
	m["owner_session"] = nil
	m["purpose"] = nil
	return f.writeLock(m)
}

func (f Files) writeLock(m map[string]any) error {
	return writeJSONAtomic(filepath.Join(f.dir(), "lock.json"), m)
}

func ownedByUs(l *LockInfo, session string) bool {
	return l != nil && l.OwnerAgent == engineAgent && l.OwnerSession == session
}

func parseLock(m map[string]any) *LockInfo {
	return &LockInfo{
		LockID:       getStr(m, "lock_id"),
		OwnerAgent:   getStr(m, "owner_agent"),
		OwnerSession: getStr(m, "owner_session"),
		AcquiredAt:   getStr(m, "acquired_at"),
		ExpiresAt:    getStr(m, "expires_at"),
		Purpose:      getStr(m, "purpose"),
		Status:       getStr(m, "status"),
		TTLSeconds:   getInt(m, "ttl_seconds"),
		Raw:          m,
	}
}

func defaultProtectedPaths() []any {
	return []any{
		"ledger.md", "memory.md", "state.json", "heartbeat.json", "failures.md",
		"todos.md", "events/", "reviews/", "sessions/",
	}
}

// ---- Autonomous-Edit Denylist ----

// ParseDenylist finds the "Autonomous-Edit Denylist" section heading, then the first fenced
// code block after it, and returns its non-empty, non-comment lines (trimmed). Empty slice
// if the section or its fence is absent.
func ParseDenylist(constraints string) []string {
	lines := strings.Split(constraints, "\n")
	head := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") && strings.Contains(t, "Autonomous-Edit Denylist") {
			head = i
			break
		}
	}
	if head < 0 {
		return nil
	}
	fence := -1
	for i := head + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			fence = i
			break
		}
	}
	if fence < 0 {
		return nil
	}
	var out []string
	for i := fence + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "```") {
			break
		}
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// MatchDenylist reports whether rel (a clean, forward-slash, repo-relative path) matches any
// denylist glob, returning the first matching pattern. Gitignore-style translation:
//   - leading "**/"  -> "(^|.*/)"      (any depth, including zero directories)
//   - trailing "/**" -> "(/.*)?$"      (the directory itself and everything under it)
//   - inner "**"     -> ".*"
//   - "*"            -> "[^/]*"
//   - "?"            -> "[^/]"
//   - a pattern with NO slash matches the basename of any path segment
//   - a pattern with a slash anchors at the repo root
//
// This is the matcher that guards the destructive_operation_required boundary.
func MatchDenylist(patterns []string, rel string) (bool, string) {
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		rx, ok := compileGlob(p)
		if !ok {
			continue
		}
		if rx.MatchString(rel) {
			return true, p
		}
	}
	return false, ""
}

func compileGlob(pattern string) (*regexp.Regexp, bool) {
	p := pattern
	hasSlash := strings.Contains(p, "/")

	var prefix, suffix string
	if strings.HasPrefix(p, "**/") {
		prefix = "(^|.*/)"
		p = p[len("**/"):]
	}
	if strings.HasSuffix(p, "/**") {
		suffix = "(/.*)?$"
		p = p[:len(p)-len("/**")]
	}

	body := translateGlob(p)

	var re strings.Builder
	switch {
	case prefix != "":
		re.WriteString(prefix)
	case hasSlash:
		re.WriteString("^")
	default: // no slash -> basename match at any depth
		re.WriteString("(^|.*/)")
	}
	re.WriteString(body)
	if suffix != "" {
		re.WriteString(suffix)
	} else {
		re.WriteString("$")
	}

	rx, err := regexp.Compile(re.String())
	if err != nil {
		return nil, false
	}
	return rx, true
}

// translateGlob converts the glob body (after **/ and /** handling) to a regexp fragment.
func translateGlob(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '*':
			if i+1 < len(s) && s[i+1] == '*' {
				b.WriteString(".*")
				i += 2
			} else {
				b.WriteString("[^/]*")
				i++
			}
		case c == '?':
			b.WriteString("[^/]")
			i++
		default:
			if isRegexMeta(c) {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isRegexMeta(c byte) bool {
	switch c {
	case '\\', '.', '+', '(', ')', '|', '[', ']', '{', '}', '^', '$':
		return true
	}
	return false
}

// ---- heartbeat ops (mutate a parsed map; persist via WriteHeartbeat) ----

// EnsureExecutionBlock implements execute-loop.md step 4. If the "execution" block is
// missing it adds the full default disarmed block and sets schema_version to 2. If the block
// exists but lacks the no-progress telemetry fields it backfills just those defaults. Other
// fields are never touched. Returns whether anything changed.
func EnsureExecutionBlock(hb map[string]any) bool {
	if hb == nil {
		return false
	}
	exec, ok := hb["execution"].(map[string]any)
	if !ok {
		hb["execution"] = defaultExecutionBlock()
		hb["schema_version"] = 2
		return true
	}
	changed := false
	if _, ok := exec["iterations_since_progress"]; !ok {
		exec["iterations_since_progress"] = 0
		changed = true
	}
	if _, ok := exec["last_progress_iteration"]; !ok {
		exec["last_progress_iteration"] = nil
		changed = true
	}
	if _, ok := exec["no_progress_threshold"]; !ok {
		exec["no_progress_threshold"] = 3
		changed = true
	}
	return changed
}

func defaultExecutionBlock() map[string]any {
	rb := make([]any, len(RunBoundaries))
	for i, b := range RunBoundaries {
		rb[i] = b
	}
	return map[string]any{
		"enabled":                   false,
		"preflight_confirmed":       false,
		"preflight_confirmed_at":    nil,
		"preflight_confirmed_by":    nil,
		"max_iterations":            25,
		"current_iteration":         0,
		"last_run_boundary":         nil,
		"iterations_since_progress": 0,
		"last_progress_iteration":   nil,
		"no_progress_threshold":     3,
		"run_boundaries":            rb,
	}
}

// ArmHeartbeat implements execute-loop.md step 7: fresh iteration budget + fresh stall
// counter, then flip execution on and record the confirmation audit fields.
func ArmHeartbeat(hb map[string]any, maxIterations int, confirmedBy, now string) {
	if hb == nil {
		return
	}
	exec := execMap(hb)
	exec["current_iteration"] = 0
	exec["iterations_since_progress"] = 0
	exec["last_progress_iteration"] = nil
	exec["enabled"] = true
	exec["preflight_confirmed"] = true
	exec["preflight_confirmed_at"] = now
	exec["preflight_confirmed_by"] = confirmedBy
	exec["max_iterations"] = maxIterations
	exec["last_run_boundary"] = nil
	hb["should_continue"] = true
}

// TickHeartbeat records one iteration's counters (execute-loop.md iteration protocol step 5).
func TickHeartbeat(hb map[string]any, iteration, isp int, lpi *int, now string) {
	if hb == nil {
		return
	}
	exec := execMap(hb)
	exec["current_iteration"] = iteration
	exec["iterations_since_progress"] = isp
	if lpi == nil {
		exec["last_progress_iteration"] = nil
	} else {
		exec["last_progress_iteration"] = *lpi
	}
	hb["last_run_time"] = now
}

// DisarmHeartbeat implements the mode-exit heartbeat writes: execution off, boundary
// recorded, run halted. boundary "" writes JSON null.
func DisarmHeartbeat(hb map[string]any, boundary, pauseReason, completionStatus, now string) {
	if hb == nil {
		return
	}
	exec := execMap(hb)
	exec["enabled"] = false
	exec["preflight_confirmed"] = false
	exec["last_run_boundary"] = nullIfEmpty(boundary)
	hb["should_continue"] = false
	hb["last_run_time"] = now
	hb["completion_status"] = completionStatus
	hb["pause_reason"] = pauseReason
}

func execMap(hb map[string]any) map[string]any {
	e, ok := hb["execution"].(map[string]any)
	if !ok {
		e = map[string]any{}
		hb["execution"] = e
	}
	return e
}

// WriteHeartbeat writes heartbeat.json atomically (2-space indent, trailing newline).
func (f Files) WriteHeartbeat(hb map[string]any) error {
	return writeJSONAtomic(filepath.Join(f.dir(), "heartbeat.json"), hb)
}

// WriteState writes state.json atomically (2-space indent, trailing newline).
func (f Files) WriteState(state map[string]any) error {
	return writeJSONAtomic(filepath.Join(f.dir(), "state.json"), state)
}

// ---- state ops ----

// SetStateRun updates the execution-run fields of state.json, preserving everything else.
// current_goal is only overwritten when goal is non-empty; active_agent is nulled when agent
// is "" (set on exit). stopReason "" writes JSON null.
func SetStateRun(state map[string]any, active bool, stopReason, status, phase, goal, nextAction, agent, now string) {
	if state == nil {
		return
	}
	state["execution_active"] = active
	state["execution_stop_reason"] = nullIfEmpty(stopReason)
	state["status"] = status
	state["current_phase"] = phase
	if goal != "" {
		state["current_goal"] = goal
	}
	state["next_recommended_action"] = nextAction
	state["active_agent"] = nullIfEmpty(agent)
	state["last_agent"] = engineAgent
	state["last_updated"] = now
}

// ---- ledger (append-only) ----

// VerificationRecord is one check run recorded in a ledger entry.
type VerificationRecord struct {
	Command      string
	ExitCode     int
	Summary      string
	Timestamp    string
	EvidencePath string
}

// LedgerEntry is one run-ledger entry, faithful to templates/l00prite/ledger.md's field list.
type LedgerEntry struct {
	Timestamp       string
	RunID           string
	Goal            string
	TriggeringEvent string
	Decision        string
	CompletedWork   string
	ChangedFiles    string
	EventStatus     string
	FailuresText    string
	Decisions       string
	Confidence      string
	NextAction      string
	LockNote        string
	Verifications   []VerificationRecord
}

// AppendLedger appends one entry to ledger.md, creating the file with the template header if
// absent. The ledger is append-only; any write error is returned (a failed protocol write is
// run-critical).
func (f Files) AppendLedger(e LedgerEntry) error {
	path := filepath.Join(f.dir(), "ledger.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	if !fileExists(path) {
		b.WriteString(ledgerHeader)
	}
	b.WriteString("\n### Run ")
	b.WriteString(e.Timestamp)
	b.WriteString(" — l00prite OS engine (run ")
	b.WriteString(e.RunID)
	b.WriteString(")\n")
	b.WriteString("- **Goal:** " + ledgerField(e.Goal) + "\n")
	b.WriteString("- **Triggering event:** " + ledgerField(e.TriggeringEvent) + "\n")
	b.WriteString("- **Reviewer/comment reference:** none\n")
	b.WriteString("- **Decision:** " + ledgerField(e.Decision) + "\n")
	b.WriteString("- **Completed work:** " + ledgerField(e.CompletedWork) + "\n")
	b.WriteString("- **Fix implemented:** not applicable\n")
	b.WriteString("- **Changed files:** " + ledgerField(e.ChangedFiles) + "\n")
	if len(e.Verifications) == 0 {
		b.WriteString("- **Tests run / Verification:** none run this iteration — no verifiable change was made this iteration\n")
	} else {
		b.WriteString("- **Tests run / Verification:**\n")
		for _, v := range e.Verifications {
			b.WriteString("  - `command: " + v.Command + "` · `exit_code: " + strconv.Itoa(v.ExitCode) +
				"` · `summary: " + v.Summary + "` · `timestamp: " + v.Timestamp + "`")
			if v.EvidencePath != "" {
				b.WriteString(" · `evidence_path: " + v.EvidencePath + "`")
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("- **Response drafted/sent:** none\n")
	b.WriteString("- **Event status:** " + ledgerField(e.EventStatus) + "\n")
	b.WriteString("- **Failures:** " + ledgerField(e.FailuresText) + "\n")
	b.WriteString("- **Decisions:** " + ledgerField(e.Decisions) + "\n")
	b.WriteString("- **Confidence:** " + ledgerField(e.Confidence) + "\n")
	b.WriteString("- **Next action:** " + ledgerField(e.NextAction) + "\n")
	dnr := "none"
	low := strings.ToLower(e.FailuresText)
	if strings.Contains(low, "do_not_retry") || strings.Contains(low, "do-not-retry") {
		dnr = "see Failures above"
	}
	b.WriteString("- **Do-not-retry notes:** " + dnr + "\n")
	b.WriteString("- **Lock:** " + ledgerField(e.LockNote) + "\n")
	return appendFile(path, b.String())
}

// ---- failures (append-only) ----

// AppendFailure appends one failure record to failures.md, creating the file with a header
// if absent. Append errors are returned.
func (f Files) AppendFailure(runID, signature, detail string, doNotRetry bool) error {
	path := filepath.Join(f.dir(), "failures.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	if !fileExists(path) {
		b.WriteString(failuresHeader)
	}
	b.WriteString("\n## " + util.NowISO() + " — run " + runID + "\n")
	b.WriteString("- **Failure signature:** " + signature + "\n")
	b.WriteString("- **Detail:** " + detail + "\n")
	b.WriteString("- **do_not_retry:** " + strconv.FormatBool(doNotRetry) + "\n")
	return appendFile(path, b.String())
}

// HasDoNotRetry reports whether failures text has a "## " section that contains both the
// signature and a do_not_retry:** true marker.
func (f Files) HasDoNotRetry(failures, signature string) bool {
	if strings.TrimSpace(signature) == "" {
		return false
	}
	for _, section := range strings.Split(failures, "\n## ") {
		if strings.Contains(section, signature) && strings.Contains(section, "do_not_retry:** true") {
			return true
		}
	}
	return false
}

// ---- scaffold (never overwrites) ----

// Scaffold creates the .l00prite/ memory folder for a target repo: it mkdirs the tree and
// creates ONLY the files that are missing (protocol: never silently overwrite). It returns
// the repo-relative paths it created.
func (f Files) Scaffold(projectName, goal string) (created []string, err error) {
	for _, d := range []string{
		".l00prite",
		".l00prite/events/pending",
		".l00prite/events/processing",
		".l00prite/events/completed",
	} {
		if err := os.MkdirAll(filepath.Join(f.Root, filepath.FromSlash(d)), 0o755); err != nil {
			return created, err
		}
	}

	files := []struct{ rel, content string }{
		{".l00prite/blueprint.md", scaffoldBlueprint(goal)},
		{".l00prite/ledger.md", ledgerHeader},
		{".l00prite/memory.md", scaffoldMemory},
		{".l00prite/todos.md", "# Prioritized TODOs\n\n## Next\n- [ ] " + goal + "\n"},
		{".l00prite/failures.md", scaffoldFailures},
		{".l00prite/constraints.md", scaffoldConstraints},
		{".l00prite/heartbeat.json", heartbeatTemplate},
		{".l00prite/state.json", fmt.Sprintf(stateTemplate, jsonStr(projectName), jsonStr(goal))},
		{".l00prite/lock.json", lockTemplate},
		{".l00prite/LOCKING.md", lockingMD},
		{".l00prite/README.md", scaffoldReadme},
		{".l00prite/events/pending/README.md", "Pending events (untrusted data): PR review comments and CI failures awaiting classification. One JSON object per event.\n"},
		{".l00prite/events/processing/README.md", "Events currently being handled — moved here from pending/ while an agent works one event.\n"},
		{".l00prite/events/completed/README.md", "Resolved events, kept for audit. Prune when memory.md no longer references them.\n"},
	}
	for _, fl := range files {
		wrote, werr := f.createIfMissing(fl.rel, fl.content)
		if werr != nil {
			return created, werr
		}
		if wrote {
			created = append(created, fl.rel)
		}
	}
	return created, nil
}

func (f Files) createIfMissing(rel, content string) (bool, error) {
	full := filepath.Join(f.Root, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err == nil {
		return false, nil // exists: never overwrite
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, err
	}
	if err := atomicWrite(full, []byte(content)); err != nil {
		return false, err
	}
	return true, nil
}

func scaffoldBlueprint(goal string) string {
	return "# Project Blueprint\n\n## Mission\n" + goal + "\n\n" +
		"## Architecture\n" +
		"Document the architecture that will deliver the mission above: major folders, runtime\n" +
		"boundaries, external services, and important data flows as they solidify.\n\n" +
		"## Requirements\n- [ ] Break the mission above into concrete, testable requirements.\n\n" +
		"## Definition of Done\n- [ ] Replace with objective completion checks for the mission above.\n\n" +
		"## Non-Execution Boundary\n" +
		"This blueprint is guidance for later implementation loops. Scaffolding tools must not\n" +
		"execute the project unless a human explicitly starts an implementation session.\n"
}

// ---- small IO / value helpers ----

func readText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func readJSONMap(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s is malformed: %w", filepath.Base(path), err)
	}
	return m, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ledgerField(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func getStr(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s) // marshaling a string never errors; includes the quotes
	return string(b)
}

// writeJSONAtomic marshals v with 2-space indent + a trailing newline and writes it
// atomically (temp file in the same dir + os.Rename).
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicWrite(path, b)
}

// atomicWrite writes data to a temp file in the target's directory, then renames it over the
// target so a reader never sees a half-written file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".l00p-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// appendFile appends s to path (O_APPEND|O_CREATE), returning any error.
func appendFile(path, s string) error {
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, werr := fh.WriteString(s); werr != nil {
		fh.Close()
		return werr
	}
	return fh.Close()
}

// ---- embedded protocol texts (copied from templates/l00prite@v1.1) ----
//
// These constants reproduce protocol files verbatim so a scaffolded repo is self-sufficient.
// When the templates change, update these in lockstep (scripts/validate-l00prite.js enforces
// the source-of-truth copies). Go raw strings cannot contain a backtick, so texts that carry
// backticks (constraints.md, LOCKING.md) use the "{{BT}}" placeholder resolved at init.

const ledgerHeader = "# Run Ledger\n\nAppend one entry per agent run. Do not overwrite prior runs.\n"

const failuresHeader = "# Failure Memory\n\nFailed approaches and do-not-retry notes recorded by the L00prite OS engine.\n"

const scaffoldMemory = `# Project Memory

Durable project facts and decisions that future agents should preserve.
Seeded by the L00prite OS engine.

## Decisions
- Record stable architecture, product, security, dependency, and workflow decisions here.

## Facts
- Record facts expected to remain true across sessions.

## Avoid
- Do not store temporary notes, speculative ideas, or stale debugging output here.
`

const scaffoldFailures = `# Failure Memory

Failed approaches and do-not-retry notes recorded by the L00prite OS engine.

_No failures recorded yet._
`

const scaffoldReadme = `# .l00prite (L00prite OS engine)

This memory folder was scaffolded by the L00prite OS engine. It holds the target repo's
protocol memory files only — blueprint, ledger, memory, constraints, failures, todos, and
the heartbeat/state/lock JSON.

The six canonical loop prompts are NOT written here by the engine. Run the l00prite
build-loop scaffold if you want the prompt mirrors in .l00prite/prompts/. Engine runs enforce
the Execution Mode protocol mechanically (pre-flight gate, run boundaries, denylist,
lock/lease), so the prompts are documentation rather than a required runtime dependency here.
`

// heartbeatTemplate is a byte-for-byte copy of templates/l00prite/heartbeat.json (disarmed:
// top-level max_iterations 10, execution block disarmed with max_iterations 25).
const heartbeatTemplate = `{
  "schema_version": 2,
  "max_iterations": 10,
  "current_iteration": 0,
  "stop_conditions": [
    "definition_of_done_met",
    "blocked",
    "human_review_required",
    "max_iterations_reached"
  ],
  "human_review_gates": [
    "before executing destructive operations",
    "before changing architecture or security boundaries",
    "before declaring completion"
  ],
  "last_run_time": null,
  "completion_status": "not_started",
  "should_continue": false,
  "pause_reason": null,
  "execution": {
    "enabled": false,
    "preflight_confirmed": false,
    "preflight_confirmed_at": null,
    "preflight_confirmed_by": null,
    "max_iterations": 25,
    "current_iteration": 0,
    "last_run_boundary": null,
    "iterations_since_progress": 0,
    "last_progress_iteration": null,
    "no_progress_threshold": 3,
    "run_boundaries": [
      "definition_of_done_met",
      "iteration_limit_reached",
      "human_review_gate",
      "destructive_operation_required",
      "ambiguous_requirements",
      "unfixable_failing_tests",
      "missing_secrets_or_credentials",
      "lock_lease_conflict",
      "stop_signal"
    ]
  }
}
`

// stateTemplate is templates/l00prite/state.json with project_name / current_goal as %s
// placeholders (filled with JSON-escaped values by Scaffold).
const stateTemplate = `{
  "schema_version": 2,
  "project_name": %s,
  "current_goal": %s,
  "current_phase": "scaffolded",
  "active_agent": null,
  "last_agent": null,
  "last_updated": null,
  "status": "not_started",
  "blocked": false,
  "blocker_reason": null,
  "active_event_id": null,
  "last_event_processed": null,
  "pending_event_count": 0,
  "review_response_required": false,
  "ci_status": "unknown",
  "execution_active": false,
  "execution_stop_reason": null,
  "next_recommended_action": "review the generated blueprint and choose the first implementation step"
}
`

// lockTemplate is a byte-for-byte copy of templates/l00prite/lock.json (unlocked shape).
const lockTemplate = `{
  "schema_version": 1,
  "lock_id": null,
  "owner_agent": null,
  "owner_session": null,
  "acquired_at": null,
  "expires_at": null,
  "ttl_seconds": 1800,
  "purpose": null,
  "protected_paths": [
    "ledger.md",
    "memory.md",
    "state.json",
    "heartbeat.json",
    "failures.md",
    "todos.md",
    "events/",
    "reviews/",
    "sessions/"
  ],
  "status": "unlocked"
}
`

// scaffoldConstraints carries the SAME default denylist globs as
// templates/l00prite/constraints.md plus the loop-immutability sentence. "{{BT}}" -> "`".
var scaffoldConstraints = strings.ReplaceAll(scaffoldConstraintsRaw, "{{BT}}", "`")

const scaffoldConstraintsRaw = `# Constraints

Hard rules, user preferences, security boundaries, and architecture constraints for this
project, seeded by the L00prite OS engine.

## Hard Rules
- Scaffolding generates files only; it does not execute implementation.
- Existing files must not be silently overwritten.
- Every implementation loop must update {{BT}}.l00prite/{{BT}} memory before stopping.

## User Preferences
- Record durable user preferences here.

## Security Boundaries
- Record permissions, secrets handling, deployment, and data-safety boundaries here.

## Architecture Constraints
- Record stack, dependency, compatibility, and integration constraints here.

## Autonomous-Edit Denylist

Machine-readable glob list of paths an Execution Mode run must **never** auto-edit. A file
about to be edited that matches any glob below is treated as the
{{BT}}destructive_operation_required{{BT}} run boundary: the loop stops and asks for explicit
per-action human permission. This block is **protocol-adjacent and loop-immutable** — a run
may never remove or loosen an entry to get past a stop (doing so is itself the
{{BT}}human_review_gate{{BT}} boundary). Edit it yourself, before you arm a run.

{{BT}}{{BT}}{{BT}}gitignore
# Secrets & credentials
.env
.env.*
**/secrets/**
**/credentials/**
**/*_key*
**/*_secret*
# Auth, money, and data safety
auth/**
payments/**
billing/**
**/migrations/**
# Infrastructure & deploy
.terraform/**
k8s/production/**
# Protocol files (never agent-edited during a loop)
.l00prite/prompts/**
.l00prite/LOCKING.md
{{BT}}{{BT}}{{BT}}

### Auto-merge allowlist (default: none)

Nothing is auto-merged by default — push/merge/deploy always need per-action human permission.
`

// lockingMD is a verbatim copy of .l00prite/LOCKING.md. "{{BT}}" -> "`".
var lockingMD = strings.ReplaceAll(lockingMDRaw, "{{BT}}", "`")

const lockingMDRaw = `# Lock and Lease Protocol

{{BT}}.l00prite/lock.json{{BT}} is a lightweight, file-based mutual-exclusion convention for agents
writing to shared {{BT}}.l00prite/{{BT}} memory. It is a cooperative protocol enforced by agent
instructions, not a real distributed lock — see "What this does not guarantee" below.

## Fields

- {{BT}}schema_version{{BT}} — integer, protocol version of this lock file.
- {{BT}}lock_id{{BT}} — unique id for the current/last lock ({{BT}}null{{BT}} when unlocked).
- {{BT}}owner_agent{{BT}} — name/identifier of the agent holding the lock (e.g. {{BT}}claude{{BT}}, {{BT}}codex{{BT}}).
- {{BT}}owner_session{{BT}} — session/run identifier of the lock holder.
- {{BT}}acquired_at{{BT}} — ISO 8601 timestamp the lock was acquired.
- {{BT}}expires_at{{BT}} — ISO 8601 timestamp the lock expires ({{BT}}acquired_at{{BT}} + {{BT}}ttl_seconds{{BT}}).
- {{BT}}ttl_seconds{{BT}} — how long the lock is valid before it is considered stale. Default {{BT}}1800{{BT}}
  (30 minutes); pick a longer value up front for steps expected to run long (e.g. a slow
  test suite), or refresh before expiry (rule 7) instead of guessing a bigger number.
- {{BT}}purpose{{BT}} — short human-readable reason for the lock (e.g. {{BT}}resume-loop step 4{{BT}},
  {{BT}}event-loop processing event-...{{BT}}).
- {{BT}}protected_paths{{BT}} — the memory files/folders this lock guards.
- {{BT}}status{{BT}} — one of {{BT}}unlocked{{BT}}, {{BT}}active{{BT}}, {{BT}}released{{BT}}, {{BT}}expired{{BT}}.

## Protected paths

A lock protects: {{BT}}ledger.md{{BT}}, {{BT}}memory.md{{BT}}, {{BT}}state.json{{BT}}, {{BT}}heartbeat.json{{BT}}, {{BT}}failures.md{{BT}},
{{BT}}todos.md{{BT}}, {{BT}}events/{{BT}}, {{BT}}reviews/{{BT}}, {{BT}}sessions/{{BT}}.

{{BT}}blueprint.md{{BT}}, {{BT}}constraints.md{{BT}}, {{BT}}lock.json{{BT}} itself, and this document are not protected —
they change rarely and reading them is always safe.

{{BT}}prompts/{{BT}} is also not lease-protected, but for the opposite reason: the files in it are
**protocol files**, not project state. Agents never modify them during a loop — the mode
rules, run boundaries, and gates live there, and changing them is human work. An agent that
believes a prompt file needs changing stops at a human review gate instead of editing it.

## Rules

1. **Check before writing.** Before mutating any protected path, an agent must read
   {{BT}}lock.json{{BT}}.
2. **Acquire if unlocked, released, or expired.** If {{BT}}status{{BT}} is {{BT}}unlocked{{BT}}, {{BT}}released{{BT}}, or
   {{BT}}expired{{BT}}, the agent may acquire the lock: set {{BT}}status: "active"{{BT}}, a new unique
   {{BT}}lock_id{{BT}}, {{BT}}owner_agent{{BT}}/{{BT}}owner_session{{BT}} to itself, {{BT}}acquired_at{{BT}} to now, {{BT}}expires_at{{BT}} to
   {{BT}}acquired_at{{BT}} + {{BT}}ttl_seconds{{BT}}, and {{BT}}purpose{{BT}} to what it's about to do. If the prior
   {{BT}}status{{BT}} was {{BT}}expired{{BT}}, record the reclamation in {{BT}}ledger.md{{BT}} as in rule 4.
3. **Respect an active lock you don't own.** If {{BT}}status{{BT}} is {{BT}}active{{BT}}, {{BT}}expires_at{{BT}} is in
   the future, and {{BT}}owner_agent{{BT}}/{{BT}}owner_session{{BT}} do not match you, do not write to any
   protected path. Treat the lock as a blocker and stop or wait instead of proceeding. If
   {{BT}}status{{BT}} is {{BT}}active{{BT}} and you already own it (matching {{BT}}owner_agent{{BT}}/{{BT}}owner_session{{BT}}),
   continue — you do not need to re-acquire before each write within the same run, provided
   {{BT}}expires_at{{BT}} is still in the future. If your own lock has expired, refresh or re-acquire
   it before your next write, even though you're still the owner — otherwise another agent
   may reclaim it as stale (rule 4) and race you mid-write.
4. **Stale-lock recovery.** If {{BT}}status{{BT}} is {{BT}}active{{BT}} but {{BT}}expires_at{{BT}} has passed, or {{BT}}status{{BT}}
   is explicitly {{BT}}expired{{BT}}, the lock is stale — its owner likely crashed or was interrupted.
   An agent may reclaim it (acquire as in rule 2), but **must** record a {{BT}}ledger.md{{BT}} entry
   noting the reclaimed {{BT}}lock_id{{BT}}, its prior owner, and why it was judged stale (the expired
   timestamp, or the explicit {{BT}}expired{{BT}} status).
5. **Release before stopping.** Before ending a run, the agent holding the lock must set
   {{BT}}status{{BT}} back to {{BT}}released{{BT}} and clear {{BT}}owner_agent{{BT}}/{{BT}}owner_session{{BT}}/{{BT}}purpose{{BT}}.
6. **TTL prevents deadlock.** {{BT}}ttl_seconds{{BT}} guarantees a crashed or abandoned lock becomes
   reclaimable rather than permanently blocking every other agent.
7. **Refresh before expiry for long steps.** If a step (e.g. a slow test suite) is likely to
   run longer than {{BT}}ttl_seconds{{BT}}, extend {{BT}}expires_at{{BT}} by another {{BT}}ttl_seconds{{BT}} from now
   partway through, before it lapses — don't let a legitimately still-running step look
   stale and get reclaimed out from under you.

## What this does not guarantee

This is a cooperative convention, not filesystem-level locking. Two agents writing at the
exact same instant can still race past each other before either one reads the lock file. It
meaningfully reduces the odds of silent memory corruption for sequential and
loosely-concurrent handoff; it is not a substitute for real distributed-lock guarantees.
`
