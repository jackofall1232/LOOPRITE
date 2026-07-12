package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

func ptr(i int) *int { return &i }

// writeFile is a test helper that writes content, creating parent dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- denylist parsing ----

func TestDenylistParse(t *testing.T) {
	// A realistic constraints.md with prose before and after the fenced block.
	constraints := `# Constraints

## Hard Rules
- something

## Autonomous-Edit Denylist

Some explanatory prose that should be ignored.

` + "```gitignore" + `
# Secrets & credentials
.env
.env.*
**/secrets/**
**/*_key*

auth/**
.l00prite/LOCKING.md
` + "```" + `

### Auto-merge allowlist (default: none)
Nothing here.
`
	got := ParseDenylist(constraints)
	want := []string{".env", ".env.*", "**/secrets/**", "**/*_key*", "auth/**", ".l00prite/LOCKING.md"}
	if len(got) != len(want) {
		t.Fatalf("ParseDenylist len=%d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ParseDenylist[%d]=%q, want %q", i, got[i], want[i])
		}
	}

	// Comment lines and blanks must be dropped.
	for _, g := range got {
		if g == "" || strings.HasPrefix(g, "#") {
			t.Errorf("ParseDenylist leaked a comment/blank: %q", g)
		}
	}

	// Absent section -> empty.
	if d := ParseDenylist("# Constraints\n\nnothing here\n"); len(d) != 0 {
		t.Errorf("ParseDenylist(no section) = %v, want empty", d)
	}

	// The engine's own scaffolded constraints.md must parse to the default denylist.
	sd := ParseDenylist(scaffoldConstraints)
	if len(sd) == 0 {
		t.Fatal("scaffoldConstraints produced an empty denylist")
	}
	found := map[string]bool{}
	for _, p := range sd {
		found[p] = true
	}
	for _, must := range []string{".env", "**/secrets/**", "**/*_key*", "auth/**", "**/migrations/**", ".l00prite/prompts/**", ".l00prite/LOCKING.md"} {
		if !found[must] {
			t.Errorf("scaffold denylist missing %q (got %v)", must, sd)
		}
	}
}

// ---- exhaustive denylist matching ----

func TestMatchDenylist(t *testing.T) {
	patterns := ParseDenylist(scaffoldConstraints)

	cases := []struct {
		rel       string
		wantMatch bool
		wantPat   string // when wantMatch, the expected first matching pattern
	}{
		// .env at root and nested (no-slash pattern, basename at any depth).
		{".env", true, ".env"},
		{"config/.env", true, ".env"},
		{"a/b/c/.env", true, ".env"},
		// .env.* variants.
		{".env.local", true, ".env.*"},
		{".env.production", true, ".env.*"},
		{"deploy/.env.staging", true, ".env.*"},
		// **/secrets/** at root and nested.
		{"secrets/aws.json", true, "**/secrets/**"},
		{"src/secrets/aws.json", true, "**/secrets/**"},
		{"a/b/secrets/c/d.txt", true, "**/secrets/**"},
		// **/credentials/**.
		{"app/credentials/token", true, "**/credentials/**"},
		// *_key* basename behavior (via **/*_key*), at any depth.
		{"config_key.json", true, "**/*_key*"},
		{"foo_key", true, "**/*_key*"},
		{"deep/nested/api_key_v2.txt", true, "**/*_key*"},
		{"my_secretfile", true, "**/*_secret*"},
		// auth/** is root-anchored and must NOT match xauth/.
		{"auth/login.go", true, "auth/**"},
		{"auth", true, "auth/**"}, // the dir itself
		{"xauth/login.go", false, ""},
		{"src/auth/login.go", false, ""}, // not nested-matchable (root-anchored)
		// payments / billing root-anchored.
		{"payments/charge.go", true, "payments/**"},
		{"billing/invoice.go", true, "billing/**"},
		// migrations at any depth.
		{"migrations/001.sql", true, "**/migrations/**"},
		{"db/migrations/001.sql", true, "**/migrations/**"},
		{"a/b/migrations/c.sql", true, "**/migrations/**"},
		// infra/deploy.
		{".terraform/state", true, ".terraform/**"},
		{"k8s/production/deploy.yaml", true, "k8s/production/**"},
		{"k8s/staging/deploy.yaml", false, ""}, // only production is denied
		// protocol files.
		{".l00prite/prompts/execute-loop.md", true, ".l00prite/prompts/**"},
		{".l00prite/LOCKING.md", true, ".l00prite/LOCKING.md"},
		{".l00prite/ledger.md", false, ""}, // ledger is editable memory, not denylisted
		// clearly-allowed source paths.
		{"src/main.go", false, ""},
		{"README.md", false, ""},
		{"internal/engine/l00pfiles.go", false, ""},
	}

	for _, c := range cases {
		gotMatch, gotPat := MatchDenylist(patterns, c.rel)
		if gotMatch != c.wantMatch {
			t.Errorf("MatchDenylist(%q) match=%v, want %v (pattern=%q)", c.rel, gotMatch, c.wantMatch, gotPat)
			continue
		}
		if c.wantMatch && gotPat != c.wantPat {
			t.Errorf("MatchDenylist(%q) pattern=%q, want %q", c.rel, gotPat, c.wantPat)
		}
	}
}

func TestMatchDenylistNoSlashBasename(t *testing.T) {
	// A no-slash pattern matches the basename of ANY path segment depth.
	patterns := []string{"*_key*"}
	cases := []struct {
		rel  string
		want bool
	}{
		{"my_key_file", true},
		{"a/b/my_key_file", true},
		{"a/b/service_keys.json", true},
		{"keyless", false},        // no "_key" substring
		{"a/b/keyless.go", false}, // basename has no "_key"
		{"a/b/c.go", false},
	}
	for _, c := range cases {
		got, _ := MatchDenylist(patterns, c.rel)
		if got != c.want {
			t.Errorf("MatchDenylist([*_key*], %q) = %v, want %v", c.rel, got, c.want)
		}
	}

	// A slash pattern with a trailing /** matches the dir and everything under it, root-anchored.
	if m, _ := MatchDenylist([]string{"vendor/**"}, "vendor/pkg/file.go"); !m {
		t.Error("vendor/** should match vendor/pkg/file.go")
	}
	if m, _ := MatchDenylist([]string{"vendor/**"}, "src/vendor/pkg.go"); m {
		t.Error("vendor/** must not match src/vendor/pkg.go (root-anchored)")
	}
}

// ---- lock lifecycle ----

func TestLockAvailabilityStates(t *testing.T) {
	if a := LockAvailability(nil, "s"); a != "free" {
		t.Errorf("nil lock availability = %q, want free", a)
	}
	if a := LockAvailability(&LockInfo{Status: "unlocked"}, "s"); a != "free" {
		t.Errorf("unlocked availability = %q, want free", a)
	}
	if a := LockAvailability(&LockInfo{Status: "released"}, "s"); a != "free" {
		t.Errorf("released availability = %q, want free", a)
	}
	if a := LockAvailability(&LockInfo{Status: "expired"}, "s"); a != "stale" {
		t.Errorf("expired availability = %q, want stale", a)
	}
	// active but past expiry -> stale.
	past := &LockInfo{Status: "active", ExpiresAt: "2000-01-01T00:00:00.000Z", OwnerAgent: engineAgent, OwnerSession: "s"}
	if a := LockAvailability(past, "s"); a != "stale" {
		t.Errorf("expired-active availability = %q, want stale", a)
	}
	// active, unexpired, mine.
	mine := &LockInfo{Status: "active", ExpiresAt: "2999-01-01T00:00:00.000Z", OwnerAgent: engineAgent, OwnerSession: "run-1"}
	if a := LockAvailability(mine, "run-1"); a != "mine" {
		t.Errorf("mine availability = %q, want mine", a)
	}
	if a := LockAvailability(mine, "run-2"); a != "foreign" {
		t.Errorf("other-session availability = %q, want foreign", a)
	}
	// active, unexpired, different agent.
	foreign := &LockInfo{Status: "active", ExpiresAt: "2999-01-01T00:00:00.000Z", OwnerAgent: "claude", OwnerSession: "x"}
	if a := LockAvailability(foreign, "run-1"); a != "foreign" {
		t.Errorf("foreign-agent availability = %q, want foreign", a)
	}
}

func TestLockAcquireRefreshRelease(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}

	// Acquire on absent/unlocked lock.
	acq, prior, err := f.AcquireLock("run-1", "execute-loop run", 1800)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if prior != nil {
		t.Errorf("prior should be nil on a free acquire, got %+v", prior)
	}
	if acq.Status != "active" || acq.OwnerAgent != "l00prite-os" || acq.OwnerSession != "run-1" {
		t.Errorf("acquired lock = %+v", acq)
	}
	if !strings.HasPrefix(acq.LockID, "lock-") || !strings.Contains(acq.LockID, "l00prite-os-run-1") {
		t.Errorf("lock_id = %q, want lock-<ts>-l00prite-os-run-1", acq.LockID)
	}
	if acq.TTLSeconds != 1800 {
		t.Errorf("ttl_seconds = %d, want 1800", acq.TTLSeconds)
	}
	if acq.ExpiresAt <= util.NowISO() {
		t.Errorf("expires_at %q should be in the future", acq.ExpiresAt)
	}

	// It's mine; a different session sees foreign and cannot acquire.
	reread, _ := f.ReadLock()
	if LockAvailability(reread, "run-1") != "mine" {
		t.Error("lock should be mine to run-1")
	}
	if LockAvailability(reread, "run-2") != "foreign" {
		t.Error("lock should be foreign to run-2")
	}
	if _, _, err := f.AcquireLock("run-2", "steal", 1800); err == nil || !strings.Contains(err.Error(), "foreign lock") {
		t.Errorf("AcquireLock by run-2 should fail with foreign lock, got %v", err)
	}

	// Refresh extends expiry and only the owner may do it.
	before, _ := f.ReadLock()
	if err := f.RefreshLock("run-1", 3600); err != nil {
		t.Fatalf("RefreshLock: %v", err)
	}
	after, _ := f.ReadLock()
	if after.ExpiresAt <= before.ExpiresAt {
		t.Errorf("refresh should extend expires_at: before=%q after=%q", before.ExpiresAt, after.ExpiresAt)
	}
	if after.TTLSeconds != 3600 {
		t.Errorf("refresh ttl_seconds = %d, want 3600", after.TTLSeconds)
	}
	if err := f.RefreshLock("run-2", 3600); err == nil {
		t.Error("RefreshLock by a non-owner should fail")
	}

	// Release nulls owner/session/purpose and sets released; only the owner may do it.
	if err := f.ReleaseLock("run-2"); err == nil {
		t.Error("ReleaseLock by a non-owner should fail")
	}
	if err := f.ReleaseLock("run-1"); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	rel, _ := f.ReadLock()
	if rel.Status != "released" {
		t.Errorf("status after release = %q, want released", rel.Status)
	}
	if rel.OwnerAgent != "" || rel.OwnerSession != "" || rel.Purpose != "" {
		t.Errorf("release should null owner/session/purpose, got %+v", rel)
	}
	// Assert the raw JSON really wrote nulls (not empty strings).
	raw := readLockRaw(t, dir)
	for _, k := range []string{"owner_agent", "owner_session", "purpose"} {
		if v, ok := raw[k]; !ok || v != nil {
			t.Errorf("released lock.json %s = %v (ok=%v), want JSON null", k, v, ok)
		}
	}
	if raw["status"] != "released" {
		t.Errorf("released lock.json status = %v", raw["status"])
	}

	// A released lock is free again.
	if LockAvailability(rel, "run-9") != "free" {
		t.Error("released lock should read as free")
	}
	if _, prior, err := f.AcquireLock("run-9", "next", 1800); err != nil || prior != nil {
		t.Errorf("re-acquire on released should succeed with nil prior, got prior=%+v err=%v", prior, err)
	}
}

func TestLockStaleReclaimReturnsPrior(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}
	// Write a foreign, expired-active lock (crashed peer).
	writeFile(t, filepath.Join(dir, ".l00prite", "lock.json"), `{
  "schema_version": 1,
  "lock_id": "lock-old-claude-sessX",
  "owner_agent": "claude",
  "owner_session": "sessX",
  "acquired_at": "2000-01-01T00:00:00.000Z",
  "expires_at": "2000-01-01T00:30:00.000Z",
  "ttl_seconds": 1800,
  "purpose": "resume-loop step 4",
  "protected_paths": ["ledger.md"],
  "status": "active",
  "extra_unknown": "preserve-me"
}
`)
	cur, _ := f.ReadLock()
	if LockAvailability(cur, "run-1") != "stale" {
		t.Fatal("expired-active foreign lock should be stale")
	}
	acq, prior, err := f.AcquireLock("run-1", "reclaim", 1800)
	if err != nil {
		t.Fatalf("stale reclaim AcquireLock: %v", err)
	}
	if prior == nil {
		t.Fatal("stale reclaim must return the prior LockInfo for ledger recording")
	}
	if prior.OwnerAgent != "claude" || prior.LockID != "lock-old-claude-sessX" {
		t.Errorf("prior = %+v, want claude/lock-old-claude-sessX", prior)
	}
	if acq.OwnerAgent != "l00prite-os" || acq.OwnerSession != "run-1" || acq.Status != "active" {
		t.Errorf("reclaimed lock = %+v", acq)
	}
	// Unknown fields survive the surgical rewrite.
	raw := readLockRaw(t, dir)
	if raw["extra_unknown"] != "preserve-me" {
		t.Errorf("surgical lock rewrite dropped unknown field: %v", raw["extra_unknown"])
	}

	// Explicitly-expired status is also stale/reclaimable.
	writeFile(t, filepath.Join(dir, ".l00prite", "lock.json"), `{"schema_version":1,"status":"expired","owner_agent":"codex","owner_session":"y","lock_id":"lid","ttl_seconds":1800}`)
	cur2, _ := f.ReadLock()
	if LockAvailability(cur2, "run-1") != "stale" {
		t.Fatal("explicit expired status should be stale")
	}
	_, prior2, err := f.AcquireLock("run-1", "reclaim2", 1800)
	if err != nil || prior2 == nil {
		t.Errorf("reclaim of expired-status lock: prior=%+v err=%v", prior2, err)
	}
}

func readLockRaw(t *testing.T, root string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".l00prite", "lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// ---- heartbeat ops ----

func TestEnsureExecutionBlockMigrationAndBackfill(t *testing.T) {
	// A v1 heartbeat with no execution block.
	hb := map[string]any{
		"schema_version":  float64(1),
		"max_iterations":  float64(10),
		"should_continue": false,
	}
	if !EnsureExecutionBlock(hb) {
		t.Fatal("EnsureExecutionBlock should report changed for a missing block")
	}
	if hb["schema_version"] != 2 {
		t.Errorf("schema_version = %v, want 2", hb["schema_version"])
	}
	exec, ok := hb["execution"].(map[string]any)
	if !ok {
		t.Fatal("execution block not added")
	}
	for _, k := range []string{"enabled", "preflight_confirmed", "preflight_confirmed_at", "preflight_confirmed_by",
		"max_iterations", "current_iteration", "last_run_boundary", "iterations_since_progress",
		"last_progress_iteration", "no_progress_threshold", "run_boundaries"} {
		if _, ok := exec[k]; !ok {
			t.Errorf("default execution block missing %q", k)
		}
	}
	if exec["enabled"] != false || exec["preflight_confirmed"] != false {
		t.Error("default execution block must be disarmed")
	}
	if exec["max_iterations"] != 25 || exec["no_progress_threshold"] != 3 {
		t.Errorf("default max_iterations/threshold wrong: %v / %v", exec["max_iterations"], exec["no_progress_threshold"])
	}
	rb, ok := exec["run_boundaries"].([]any)
	if !ok || len(rb) != len(RunBoundaries) {
		t.Fatalf("run_boundaries = %v, want %d entries", exec["run_boundaries"], len(RunBoundaries))
	}
	// No-op on a full block.
	if EnsureExecutionBlock(hb) {
		t.Error("EnsureExecutionBlock should be a no-op on a complete block")
	}

	// Telemetry backfill: execution block present but pre-telemetry.
	hb2 := map[string]any{
		"schema_version": float64(2),
		"execution": map[string]any{
			"enabled":           false,
			"max_iterations":    float64(25),
			"current_iteration": float64(7),
		},
	}
	if !EnsureExecutionBlock(hb2) {
		t.Fatal("telemetry backfill should report changed")
	}
	e2 := hb2["execution"].(map[string]any)
	if e2["iterations_since_progress"] != 0 || e2["no_progress_threshold"] != 3 {
		t.Errorf("telemetry defaults wrong: %v / %v", e2["iterations_since_progress"], e2["no_progress_threshold"])
	}
	if v, ok := e2["last_progress_iteration"]; !ok || v != nil {
		t.Errorf("last_progress_iteration backfill = %v (ok=%v), want null", v, ok)
	}
	// Existing fields must be untouched by the backfill.
	if e2["current_iteration"] != float64(7) {
		t.Errorf("backfill clobbered current_iteration: %v", e2["current_iteration"])
	}
	if hb2["schema_version"] != float64(2) {
		t.Errorf("backfill must not touch schema_version: %v", hb2["schema_version"])
	}
}

func TestArmTickDisarmRoundtrip(t *testing.T) {
	// Parse the template and inject unknown fields to prove they survive.
	var hb map[string]any
	if err := json.Unmarshal([]byte(heartbeatTemplate), &hb); err != nil {
		t.Fatal(err)
	}
	hb["custom_top"] = "keep-top"
	hb["execution"].(map[string]any)["custom_exec"] = "keep-exec"

	// Arm.
	ArmHeartbeat(hb, 25, "alice@example", "2026-07-05T10:00:00.000Z")
	exec := hb["execution"].(map[string]any)
	if exec["enabled"] != true || exec["preflight_confirmed"] != true {
		t.Error("arm should enable + confirm")
	}
	if exec["preflight_confirmed_by"] != "alice@example" || exec["preflight_confirmed_at"] != "2026-07-05T10:00:00.000Z" {
		t.Errorf("arm audit fields wrong: %v / %v", exec["preflight_confirmed_by"], exec["preflight_confirmed_at"])
	}
	if exec["current_iteration"] != 0 || exec["iterations_since_progress"] != 0 {
		t.Error("arm should reset counters to 0")
	}
	if exec["last_progress_iteration"] != nil || exec["last_run_boundary"] != nil {
		t.Error("arm should null last_progress_iteration + last_run_boundary")
	}
	if exec["max_iterations"] != 25 {
		t.Errorf("arm max_iterations = %v, want 25", exec["max_iterations"])
	}
	if hb["should_continue"] != true {
		t.Error("arm should set should_continue true")
	}

	// Tick with a progress iteration.
	TickHeartbeat(hb, 3, 0, ptr(3), "2026-07-05T10:05:00.000Z")
	if exec["current_iteration"] != 3 || exec["iterations_since_progress"] != 0 || exec["last_progress_iteration"] != 3 {
		t.Errorf("tick(progress) wrong: %v %v %v", exec["current_iteration"], exec["iterations_since_progress"], exec["last_progress_iteration"])
	}
	if hb["last_run_time"] != "2026-07-05T10:05:00.000Z" {
		t.Errorf("tick last_run_time = %v", hb["last_run_time"])
	}
	// Tick with no progress (nil lpi).
	TickHeartbeat(hb, 4, 1, nil, "2026-07-05T10:06:00.000Z")
	if exec["last_progress_iteration"] != nil || exec["iterations_since_progress"] != 1 {
		t.Errorf("tick(no progress) wrong: %v %v", exec["last_progress_iteration"], exec["iterations_since_progress"])
	}

	// Disarm.
	DisarmHeartbeat(hb, "iteration_limit_reached", "hit the budget", "in_progress", "2026-07-05T10:10:00.000Z")
	if exec["enabled"] != false || exec["preflight_confirmed"] != false {
		t.Error("disarm should turn execution off")
	}
	if exec["last_run_boundary"] != "iteration_limit_reached" {
		t.Errorf("disarm boundary = %v", exec["last_run_boundary"])
	}
	if hb["should_continue"] != false || hb["completion_status"] != "in_progress" || hb["pause_reason"] != "hit the budget" {
		t.Errorf("disarm top-level fields wrong: %v %v %v", hb["should_continue"], hb["completion_status"], hb["pause_reason"])
	}
	// Empty boundary -> null.
	DisarmHeartbeat(hb, "", "aborted", "not_started", "2026-07-05T10:11:00.000Z")
	if exec["last_run_boundary"] != nil {
		t.Errorf("disarm empty boundary should be null, got %v", exec["last_run_boundary"])
	}

	// Unknown fields survive the whole round-trip AND a JSON re-serialization.
	if hb["custom_top"] != "keep-top" || exec["custom_exec"] != "keep-exec" {
		t.Error("arm/tick/disarm dropped unknown fields")
	}
	if _, err := json.MarshalIndent(hb, "", "  "); err != nil {
		t.Fatalf("heartbeat no longer serializable after ops: %v", err)
	}
}

func TestSetStateRun(t *testing.T) {
	var st map[string]any
	if err := json.Unmarshal([]byte(fmtState("proj", "the goal")), &st); err != nil {
		t.Fatal(err)
	}
	st["custom"] = "preserve"

	// Enter a run.
	SetStateRun(st, true, "", "running", "executing", "build feature X", "next step", "l00prite-os", "2026-07-05T10:00:00.000Z")
	if st["execution_active"] != true {
		t.Error("execution_active should be true")
	}
	if st["execution_stop_reason"] != nil {
		t.Errorf("empty stopReason should be null, got %v", st["execution_stop_reason"])
	}
	if st["status"] != "running" || st["current_phase"] != "executing" {
		t.Errorf("status/phase wrong: %v %v", st["status"], st["current_phase"])
	}
	if st["current_goal"] != "build feature X" {
		t.Errorf("current_goal = %v", st["current_goal"])
	}
	if st["active_agent"] != "l00prite-os" || st["last_agent"] != "l00prite-os" {
		t.Errorf("agents wrong: %v / %v", st["active_agent"], st["last_agent"])
	}
	if st["custom"] != "preserve" {
		t.Error("SetStateRun dropped an unknown field")
	}

	// Exit: empty goal leaves current_goal unchanged; empty agent -> null active_agent.
	SetStateRun(st, false, "iteration_limit_reached", "stopped", "planning", "", "human review", "", "2026-07-05T10:10:00.000Z")
	if st["execution_active"] != false {
		t.Error("execution_active should be false on exit")
	}
	if st["execution_stop_reason"] != "iteration_limit_reached" {
		t.Errorf("stop reason = %v", st["execution_stop_reason"])
	}
	if st["current_goal"] != "build feature X" {
		t.Errorf("empty goal must not overwrite current_goal, got %v", st["current_goal"])
	}
	if st["active_agent"] != nil {
		t.Errorf("empty agent should null active_agent, got %v", st["active_agent"])
	}
	if st["last_agent"] != "l00prite-os" {
		t.Errorf("last_agent = %v", st["last_agent"])
	}
}

func fmtState(name, goal string) string {
	return `{"schema_version":2,"project_name":"` + name + `","current_goal":"` + goal +
		`","status":"not_started","execution_active":false,"execution_stop_reason":null}`
}

// ---- ledger ----

func TestAppendLedgerCreatesHeaderThenAppends(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}

	err := f.AppendLedger(LedgerEntry{
		Timestamp: "2026-07-05T10:00:00.000Z",
		RunID:     "run-abc",
		Goal:      "do a thing",
		Decision:  "normal work",
		Verifications: []VerificationRecord{
			{Command: "go test ./...", ExitCode: 0, Summary: "ok", Timestamp: "2026-07-05T10:00:05.000Z", EvidencePath: "logs/t.txt"},
		},
	})
	if err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}
	body := readText(filepath.Join(dir, ".l00prite", "ledger.md"))
	if !strings.HasPrefix(body, "# Run Ledger") {
		t.Error("ledger missing template header")
	}
	for _, want := range []string{
		"### Run 2026-07-05T10:00:00.000Z — l00prite OS engine (run run-abc)",
		"- **Goal:** do a thing",
		"`command: go test ./...`",
		"`exit_code: 0`",
		"`summary: ok`",
		"`timestamp: 2026-07-05T10:00:05.000Z`",
		"`evidence_path: logs/t.txt`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ledger missing %q\n---\n%s", want, body)
		}
	}

	// Second append: header appears exactly once; two run headings.
	if err := f.AppendLedger(LedgerEntry{Timestamp: "2026-07-05T11:00:00.000Z", RunID: "run-def", Goal: "another"}); err != nil {
		t.Fatal(err)
	}
	body = readText(filepath.Join(dir, ".l00prite", "ledger.md"))
	if strings.Count(body, "# Run Ledger") != 1 {
		t.Errorf("header should appear once, got %d", strings.Count(body, "# Run Ledger"))
	}
	if n := strings.Count(body, "### Run "); n != 2 {
		t.Errorf("want 2 run headings, got %d", n)
	}
	// Entry with no verifications states so honestly.
	if !strings.Contains(body, "none run this iteration") {
		t.Error("no-verification entry should say 'none run this iteration'")
	}
}

// ---- failures ----

func TestAppendFailureAndHasDoNotRetry(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}

	if err := f.AppendFailure("run-1", "build:pkg X exit 2", "compiler exploded", true); err != nil {
		t.Fatalf("AppendFailure: %v", err)
	}
	body := readText(filepath.Join(dir, ".l00prite", "failures.md"))
	if !strings.HasPrefix(body, "# Failure Memory") {
		t.Error("failures.md missing header")
	}
	for _, want := range []string{"- **Failure signature:** build:pkg X exit 2", "- **Detail:** compiler exploded", "- **do_not_retry:** true"} {
		if !strings.Contains(body, want) {
			t.Errorf("failures.md missing %q", want)
		}
	}
	if !f.HasDoNotRetry(body, "build:pkg X exit 2") {
		t.Error("HasDoNotRetry should find the do_not_retry:true signature")
	}
	if f.HasDoNotRetry(body, "some other signature") {
		t.Error("HasDoNotRetry must not match an unrelated signature")
	}
	if f.HasDoNotRetry(body, "") {
		t.Error("HasDoNotRetry(\"\") must be false")
	}

	// A do_not_retry:false entry must NOT be reported.
	if err := f.AppendFailure("run-2", "flaky:test Y", "timing", false); err != nil {
		t.Fatal(err)
	}
	body = readText(filepath.Join(dir, ".l00prite", "failures.md"))
	if f.HasDoNotRetry(body, "flaky:test Y") {
		t.Error("HasDoNotRetry must not match a do_not_retry:false entry")
	}
}

// ---- scaffold ----

func TestScaffoldCreatesAllThenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}

	created, err := f.Scaffold("my-project", "ship the widget")
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}
	wantFiles := []string{
		".l00prite/blueprint.md", ".l00prite/ledger.md", ".l00prite/memory.md",
		".l00prite/todos.md", ".l00prite/failures.md", ".l00prite/constraints.md",
		".l00prite/heartbeat.json", ".l00prite/state.json", ".l00prite/lock.json",
		".l00prite/LOCKING.md", ".l00prite/README.md",
		".l00prite/events/pending/README.md", ".l00prite/events/processing/README.md",
		".l00prite/events/completed/README.md",
	}
	if len(created) != len(wantFiles) {
		t.Fatalf("Scaffold created %d files, want %d: %v", len(created), len(wantFiles), created)
	}
	gotSet := map[string]bool{}
	for _, c := range created {
		gotSet[c] = true
	}
	for _, w := range wantFiles {
		if !gotSet[w] {
			t.Errorf("Scaffold did not create %q", w)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(w))); err != nil {
			t.Errorf("scaffolded file missing on disk: %q (%v)", w, err)
		}
	}

	// heartbeat.json is the disarmed template.
	hbRaw, _ := os.ReadFile(filepath.Join(dir, ".l00prite", "heartbeat.json"))
	if string(hbRaw) != heartbeatTemplate {
		t.Error("scaffolded heartbeat.json is not byte-identical to the disarmed template")
	}
	var hb map[string]any
	if err := json.Unmarshal(hbRaw, &hb); err != nil {
		t.Fatalf("heartbeat.json invalid: %v", err)
	}
	if hb["execution"].(map[string]any)["enabled"] != false {
		t.Error("scaffolded heartbeat must ship disarmed")
	}

	// state.json has project_name/current_goal filled and status not_started.
	var st map[string]any
	stRaw, _ := os.ReadFile(filepath.Join(dir, ".l00prite", "state.json"))
	if err := json.Unmarshal(stRaw, &st); err != nil {
		t.Fatalf("state.json invalid: %v", err)
	}
	if st["project_name"] != "my-project" || st["current_goal"] != "ship the widget" {
		t.Errorf("state.json name/goal = %v / %v", st["project_name"], st["current_goal"])
	}
	if st["status"] != "not_started" {
		t.Errorf("state.json status = %v, want not_started", st["status"])
	}

	// lock.json is the unlocked shape.
	lock, _ := f.ReadLock()
	if lock == nil || lock.Status != "unlocked" || lock.TTLSeconds != 1800 {
		t.Errorf("scaffolded lock = %+v, want unlocked/1800", lock)
	}

	// constraints.md parses to a valid denylist; todos seeded with the goal.
	if d := ParseDenylist(readText(filepath.Join(dir, ".l00prite", "constraints.md"))); len(d) == 0 {
		t.Error("scaffolded constraints.md has no parseable denylist")
	}
	if td := readText(filepath.Join(dir, ".l00prite", "todos.md")); !strings.Contains(td, "- [ ] ship the widget") {
		t.Errorf("todos.md missing seeded goal: %q", td)
	}
	if bp := readText(filepath.Join(dir, ".l00prite", "blueprint.md")); !strings.Contains(bp, "ship the widget") {
		t.Error("blueprint.md should reference the goal as its mission")
	}
	// LOCKING.md is the full protocol text (backticks resolved).
	if lk := readText(filepath.Join(dir, ".l00prite", "LOCKING.md")); !strings.Contains(lk, "# Lock and Lease Protocol") || !strings.Contains(lk, "`ttl_seconds`") {
		t.Error("scaffolded LOCKING.md is not the full verbatim text")
	}

	// Second scaffold creates nothing and overwrites nothing.
	blueprintPath := filepath.Join(dir, ".l00prite", "blueprint.md")
	writeFile(t, blueprintPath, "USER EDITED — DO NOT CLOBBER")
	created2, err := f.Scaffold("my-project", "ship the widget")
	if err != nil {
		t.Fatalf("second Scaffold: %v", err)
	}
	if len(created2) != 0 {
		t.Errorf("second scaffold created %v, want nothing", created2)
	}
	if got := readText(blueprintPath); got != "USER EDITED — DO NOT CLOBBER" {
		t.Errorf("second scaffold clobbered an existing file: %q", got)
	}
}

// ---- full protocol scaffold (AGENTS.md, loop prompts, vendor adapters, starter CLAUDE.md) ----

func TestFullProtocolGapsOnEmptyDir(t *testing.T) {
	f := Files{Root: t.TempDir()}
	missing, claudeMissing := f.FullProtocolGaps()
	if len(missing) == 0 {
		t.Error("expected gaps on an empty dir")
	}
	if !claudeMissing {
		t.Error("expected claudeMissing=true on an empty dir")
	}
}

// TestFullProtocolGapsCatchesMissingBaselineFiles: a repo carrying ONLY ScaffoldFull's own
// additions (AGENTS.md, prompts, adapters, CLAUDE.md) but missing the baseline Scaffold() memory
// files (e.g. because the auto-scaffold on register/clone was skipped by a foreign lock, or
// failed on a read-only filesystem) must NOT be reported as gap-free — otherwise the "Add
// l00prite" repair action would see already_complete and never call ScaffoldFull to fill in
// blueprint.md/state.json/etc.
func TestFullProtocolGapsCatchesMissingBaselineFiles(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}
	// Write ONLY the full-protocol extras "by hand", simulating a leftover/manual copy —
	// deliberately not via ScaffoldFull, so the baseline is genuinely absent.
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "leftover AGENTS.md")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "leftover CLAUDE.md")
	for _, rel := range fullProtocolTargets() {
		if rel == "AGENTS.md" {
			continue
		}
		writeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), "leftover")
	}
	missing, claudeMissing := f.FullProtocolGaps()
	if claudeMissing {
		t.Error("CLAUDE.md exists, claudeMissing should be false")
	}
	if len(missing) == 0 {
		t.Fatal("expected the missing baseline files to still be reported as gaps")
	}
	for _, want := range []string{".l00prite/blueprint.md", ".l00prite/state.json"} {
		found := false
		for _, m := range missing {
			if m == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in missing, got %v", want, missing)
		}
	}
}

func TestScaffoldFullWritesEverythingThenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}

	created, claudeSkipped, err := f.ScaffoldFull("my-project", "ship the widget")
	if err != nil {
		t.Fatalf("ScaffoldFull: %v", err)
	}
	if claudeSkipped {
		t.Error("expected CLAUDE.md to be created on a repo with none, not skipped")
	}
	wantExtra := []string{
		"AGENTS.md", "CLAUDE.md",
		".l00prite/prompts/README.md", ".l00prite/prompts/event-loop.md",
		".l00prite/prompts/execute-loop.md", ".l00prite/prompts/handoff-summary.md",
		".l00prite/prompts/heartbeat.md", ".l00prite/prompts/respond-to-review.md",
		".l00prite/prompts/resume-loop.md",
		"GEMINI.md", "QWEN.md", ".github/copilot-instructions.md",
		".cursor/rules/l00prite.mdc", ".windsurf/rules/l00prite.md", "CONVENTIONS.md",
	}
	gotSet := map[string]bool{}
	for _, c := range created {
		gotSet[c] = true
	}
	for _, w := range wantExtra {
		if !gotSet[w] {
			t.Errorf("ScaffoldFull did not create %q (created=%v)", w, created)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(w))); err != nil {
			t.Errorf("scaffolded file missing on disk: %q (%v)", w, err)
		}
	}
	// The baseline Scaffold() files must ALSO be present — ScaffoldFull adds to the baseline,
	// it never replaces it.
	if _, err := os.Stat(filepath.Join(dir, ".l00prite", "blueprint.md")); err != nil {
		t.Errorf("ScaffoldFull must still write the baseline Scaffold() files: %v", err)
	}
	// AGENTS.md has the project name filled in, no leftover {{placeholder}} tokens.
	agents := readText(filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(agents, "my-project") {
		t.Error("AGENTS.md missing the filled-in project name")
	}
	if strings.Contains(agents, "{{") {
		t.Errorf("AGENTS.md has an unfilled {{placeholder}}: %q", agents)
	}
	// CLAUDE.md carries the fixed protocol section verbatim.
	claude := readText(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(claude, "## l00prite Protocol (fixed") || !strings.Contains(claude, ".l00prite/prompts/README.md") {
		t.Errorf("CLAUDE.md missing the fixed protocol section: %q", claude)
	}

	if missing, claudeMissing := f.FullProtocolGaps(); len(missing) != 0 || claudeMissing {
		t.Errorf("expected no gaps after ScaffoldFull, got missing=%v claudeMissing=%v", missing, claudeMissing)
	}

	// Idempotent: nothing new, nothing clobbered, and an existing CLAUDE.md is now reported skipped.
	writeFile(t, filepath.Join(dir, "GEMINI.md"), "USER EDITED — DO NOT CLOBBER")
	created2, claudeSkipped2, err := f.ScaffoldFull("my-project", "ship the widget")
	if err != nil {
		t.Fatalf("second ScaffoldFull: %v", err)
	}
	if len(created2) != 0 {
		t.Errorf("second ScaffoldFull created %v, want nothing", created2)
	}
	if !claudeSkipped2 {
		t.Error("expected claudeSkipped=true once CLAUDE.md already exists")
	}
	if got := readText(filepath.Join(dir, "GEMINI.md")); got != "USER EDITED — DO NOT CLOBBER" {
		t.Errorf("second ScaffoldFull clobbered an existing file: %q", got)
	}
}

func TestScaffoldFullNeverTouchesExistingClaudeMD(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "# Pre-existing project doc\nDo not overwrite.")
	f := Files{Root: dir}
	created, claudeSkipped, err := f.ScaffoldFull("my-project", "ship the widget")
	if err != nil {
		t.Fatalf("ScaffoldFull: %v", err)
	}
	if !claudeSkipped {
		t.Error("expected claudeSkipped=true when CLAUDE.md already exists")
	}
	for _, c := range created {
		if c == "CLAUDE.md" {
			t.Error("ScaffoldFull must never report writing an already-existing CLAUDE.md")
		}
	}
	if got := readText(filepath.Join(dir, "CLAUDE.md")); got != "# Pre-existing project doc\nDo not overwrite." {
		t.Errorf("ScaffoldFull modified an existing CLAUDE.md: %q", got)
	}
}

// ---- snapshot ----

func TestReadSnapshotOnScaffold(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}
	if _, err := f.Scaffold("proj", "goal text"); err != nil {
		t.Fatal(err)
	}
	// Add a pending event plus files that must be excluded.
	writeFile(t, filepath.Join(dir, ".l00prite", "events", "pending", "event-abc123.json"), `{"id":"event-abc123"}`)
	writeFile(t, filepath.Join(dir, ".l00prite", "events", "pending", ".hidden"), "x")

	snap, err := f.ReadSnapshot()
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if !snap.HasL00prite {
		t.Error("HasL00prite should be true")
	}
	if snap.Heartbeat == nil || snap.State == nil || snap.Lock == nil {
		t.Error("heartbeat/state/lock should all be parsed")
	}
	if snap.Blueprint == "" || snap.Constraints == "" {
		t.Error("blueprint/constraints text should be present")
	}
	if len(snap.PendingEvents) != 1 || snap.PendingEvents[0] != "event-abc123.json" {
		t.Errorf("PendingEvents = %v, want [event-abc123.json] (README.md + dotfiles excluded)", snap.PendingEvents)
	}
}

func TestReadSnapshotMalformedJSONSurfaced(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}
	writeFile(t, filepath.Join(dir, ".l00prite", "heartbeat.json"), "{ this is not json ]")

	snap, err := f.ReadSnapshot()
	if err == nil {
		t.Fatal("ReadSnapshot must surface a malformed JSON file (fail-closed)")
	}
	if snap.Heartbeat != nil {
		t.Error("malformed heartbeat should leave snap.Heartbeat nil")
	}
	if !strings.Contains(err.Error(), "heartbeat.json") {
		t.Errorf("error should name the offending file, got %v", err)
	}
}

// ---- missing-files snapshot ----

func TestReadSnapshotEmptyRepo(t *testing.T) {
	dir := t.TempDir()
	f := Files{Root: dir}
	snap, err := f.ReadSnapshot()
	if err != nil {
		t.Fatalf("empty repo should not error: %v", err)
	}
	if snap.HasL00prite {
		t.Error("HasL00prite should be false for a bare repo")
	}
	if snap.Blueprint != "" || snap.Heartbeat != nil || snap.Lock != nil || len(snap.PendingEvents) != 0 {
		t.Errorf("empty repo snapshot should be all zero values: %+v", snap)
	}
}
