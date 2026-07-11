package server_test

// End-to-end tests for the propose_run chat tool (spec section B): a chat completion naming a
// registered repo can turn a plain-text "build X and execute it" request into a reviewable Run
// DRAFT via the mock adapter's "/chattool propose_run <json-args>" directive (adapters/mock.go),
// exactly like the existing read_file/list_dir/search_files coverage in chattools_api_test.go.
// The one invariant every test here ultimately protects: propose_run can NEVER leave a run in any
// state past "ready"/"blocked" -- in particular NEVER "running" -- because it only ever reaches
// createDraftRun (CreateRun + BuildPreflight); engine.StartRun requires a separate, explicit,
// human-typed "EXECUTE" confirmation through /v1/runs/start, which nothing here ever calls.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/engine"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/server"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"

	"net/http/httptest"
)

// previewOnlyEngineCaller is a minimal engine.ModelCaller good enough for BuildPreflight's
// team-assembly dry-run (PreviewRoute). Turn must never be invoked by these tests since none of
// them ever reach engine.StartRun.
type previewOnlyEngineCaller struct{}

func (previewOnlyEngineCaller) PreviewRoute(project, repoID, model string, sample map[string]any) (engine.RoutePreview, error) {
	return engine.RoutePreview{Provider: "mock", Model: "m", Reason: "test"}, nil
}
func (previewOnlyEngineCaller) Turn(ctx context.Context, in engine.TurnInput) (engine.TurnResult, error) {
	panic("previewOnlyEngineCaller.Turn must never be called: these tests never reach engine.StartRun")
}

// chatRunServer builds a server with the mock chat provider AND the run engine wired (so
// propose_run's createDraftRun has somewhere real to write), plus one committed git repo
// registered under the token's project. Mirrors chatToolsServer (chattools_api_test.go) +
// runEngineServer (runs_api_test.go) combined.
func chatRunServer(t *testing.T) (string, string, string, *gateway.App) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	if err := config.EnsureHome(cfg); err != nil {
		t.Fatal(err)
	}
	if err := security.EnsureMasterKey(cfg.MasterKeyPath); err != nil {
		t.Fatal(err)
	}
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO providers(name,adapter,base_url,enc_key,enabled,is_default,created_at) VALUES('mock','mock',NULL,NULL,1,1,?)`, util.NowISO()); err != nil {
		t.Fatal(err)
	}

	repoDir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repoDir, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repoDir, "commit", "-q", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if _, err := db.Exec(`INSERT INTO repos(id,root,project,created_at) VALUES('demo',?,'demo',?)`, repoDir, util.NowISO()); err != nil {
		t.Fatal(err)
	}
	_, token, err := security.MintToken(db, "demo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases}
	app.Engine = engine.New(&engine.Store{DB: db}, previewOnlyEngineCaller{})
	srv := httptest.NewServer(server.Handler(app))
	t.Cleanup(srv.Close)
	return srv.URL, token, repoDir, app
}

func countRuns(t *testing.T, app *gateway.App, project string) int {
	t.Helper()
	runs, err := app.Engine.Store.ListRuns(project, 50)
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

// TestChatProposeRunDraftsRunWithoutStarting drives the real HTTP path end to end: the model
// proposes a run via propose_run, the gateway drafts it for real (a genuine engine store row,
// genuine pre-flight), the drafted id surfaces in the response's l00prite_proposed_runs field, and
// the run is never anything but a reviewable draft -- specifically it can never be "running",
// since nothing in this path calls /v1/runs/start.
func TestChatProposeRunDraftsRunWithoutStarting(t *testing.T) {
	base, token, _, app := chatRunServer(t)

	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool propose_run {"goal":"add a hello endpoint","command_allowlist":["true"]}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}

	proposed, _ := body["l00prite_proposed_runs"].([]any)
	if len(proposed) != 1 {
		t.Fatalf("expected exactly one proposed run in the response, got: %v", body["l00prite_proposed_runs"])
	}
	pr, _ := proposed[0].(map[string]any)
	runID, _ := pr["id"].(string)
	if runID == "" {
		t.Fatalf("expected a non-empty drafted run id, got: %v", pr)
	}
	if pr["repo"] != "demo" || pr["goal"] != "add a hello endpoint" {
		t.Fatalf("proposed run view mismatch: %v", pr)
	}

	// The mock's finalization turn echoes (a 400-char-truncated excerpt of) the wrapped tool
	// result -- the untrusted-envelope preamble is long enough that the truncation window lands
	// inside it rather than the JSON payload, so assert on the envelope/preamble making the round
	// trip, not the raw JSON tail. Real proof that the drafted-run JSON itself flowed all the way
	// back into model context lives at the gateway-unit level (chatrun_test.go); this end-to-end
	// test's job is to prove the real HTTP path actually drafted the run, which the l00prite_proposed_runs
	// field and the engine-store lookups below already do.
	content := chatContent(t, body)
	if !strings.Contains(content, "drafting a run") {
		t.Fatalf("expected the propose_run tool result envelope to be echoed in the final answer, got: %q", content)
	}

	// Ground truth from the real engine store: the run exists, and its status is NEVER "running"
	// (StartRun was never called -- there is no code path from propose_run to it).
	stored, err := app.Engine.Store.GetRun(runID)
	if err != nil || stored == nil {
		t.Fatalf("expected the drafted run to be persisted in the engine store: err=%v stored=%v", err, stored)
	}
	if stored.Status == engine.StatusRunning {
		t.Fatal("a run drafted purely via chat must never reach \"running\"")
	}
	if stored.ConfirmedBy != "" || stored.StartedAt != "" {
		t.Fatalf("a chat-drafted run must carry no Start-confirmation fields, got: %+v", stored)
	}

	// Confirm via the real /v1/runs/get endpoint too -- the same one the dashboard's "Review &
	// start" button drives -- so this is provably the exact same run surface, not a shadow copy.
	_, getBody := doJSON(t, "GET", base+"/v1/runs/get?id="+runID, token, nil)
	run, _ := getBody["run"].(map[string]any)
	if run["id"] != runID {
		t.Fatalf("expected /v1/runs/get to find the chat-drafted run, got: %v", getBody)
	}
	if run["status"] == "running" {
		t.Fatal("run must not be running")
	}
}

// TestChatProposeRunNotOfferedWithoutRepo: exactly like the read-only chat tools, propose_run must
// only be attached when a repo is selected -- without one, the directive is inert, the reply is
// the plain mock response, and critically NO run is ever created.
func TestChatProposeRunNotOfferedWithoutRepo(t *testing.T) {
	base, token, _, app := chatRunServer(t)

	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool propose_run {"goal":"add a hello endpoint","command_allowlist":["true"]}`}},
	}, nil)
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	if _, ok := body["l00prite_proposed_runs"]; ok {
		t.Fatalf("expected no l00prite_proposed_runs field without a selected repo, got: %v", body["l00prite_proposed_runs"])
	}
	content := chatContent(t, body)
	if !strings.Contains(content, "Mock provider response") {
		t.Fatalf("expected the unchanged plain mock reply, got: %q", content)
	}
	if got := countRuns(t, app, "demo"); got != 0 {
		t.Fatalf("expected zero runs created with no repo selected, got %d", got)
	}
}

// TestChatProposeRunSurfacesBlockersWithoutStarting: a foreign, unexpired lock on the repo's
// .l00prite/ must surface as a blocker in the tool result (mirroring the dashboard's pre-flight
// view exactly), and the run must still land only as far as "blocked" -- a draft the human must
// act on, never anything that ran on its own.
func TestChatProposeRunSurfacesBlockersWithoutStarting(t *testing.T) {
	base, token, repoDir, _ := chatRunServer(t)

	lockDir := filepath.Join(repoDir, ".l00prite")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockJSON := `{"status":"active","owner_agent":"other-agent","owner_session":"run_other","purpose":"unit test","expires_at":"2999-01-01T00:00:00.000Z"}`
	if err := os.WriteFile(filepath.Join(lockDir, "lock.json"), []byte(lockJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool propose_run {"goal":"add a hello endpoint","command_allowlist":["true"]}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	proposed, _ := body["l00prite_proposed_runs"].([]any)
	if len(proposed) != 1 {
		t.Fatalf("expected the run to still be drafted (just blocked), got: %v", body["l00prite_proposed_runs"])
	}
	pr, _ := proposed[0].(map[string]any)
	if pr["status"] != string(engine.StatusBlocked) {
		t.Fatalf("expected the proposed run's status to be %q, got %v", engine.StatusBlocked, pr["status"])
	}
	content := chatContent(t, body)
	if !strings.Contains(strings.ToLower(content), "lock") {
		t.Fatalf("expected the lock blocker to be echoed in the final answer, got: %q", content)
	}
}

// TestChatProposeRunNeverHijacksClientOwnedToolName: a client that defines its OWN tool literally
// named "propose_run" must never have it silently intercepted by the gateway -- the raw tool_call
// passes through unexecuted for the client to handle, and critically no run is created.
func TestChatProposeRunNeverHijacksClientOwnedToolName(t *testing.T) {
	base, token, _, app := chatRunServer(t)
	clientTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "propose_run", "description": "the CLIENT's own tool, unrelated to l00prite runs",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool propose_run {"goal":"add a hello endpoint","command_allowlist":["true"]}`}},
		"tools":    []any{clientTool},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	if _, ok := body["l00prite_proposed_runs"]; ok {
		t.Fatalf("a client-owned \"propose_run\" tool must never be executed by the gateway, got: %v", body["l00prite_proposed_runs"])
	}
	choices, _ := body["choices"].([]any)
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	toolCalls, _ := msg["tool_calls"].([]any)
	if len(toolCalls) == 0 {
		t.Fatalf("expected the unexecuted \"propose_run\" tool_call to pass through to the client, got message: %v", msg)
	}
	fn, _ := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "propose_run" {
		t.Fatalf("expected the passed-through tool_call to be named propose_run, got: %v", fn)
	}
	if got := countRuns(t, app, "demo"); got != 0 {
		t.Fatalf("expected zero runs created when propose_run collides with a client-owned tool, got %d", got)
	}
}

// TestChatProposeRunBoundedByRoundCap: propose_run shares the SAME per-turn round budget
// (chatMaxToolRounds) as the read-only tools -- no separate allowance. The mock adapter keeps
// re-emitting the same propose_run directive every round (the last user message never changes),
// so a model that never stops asking must still be cut off: strictly fewer runs get drafted than
// rounds would allow if the budget were unbounded, and the final round forces a real answer
// instead of executing yet another tool call.
func TestChatProposeRunBoundedByRoundCap(t *testing.T) {
	base, token, _, app := chatRunServer(t)

	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool propose_run {"goal":"add a hello endpoint","command_allowlist":["true"]}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}

	got := countRuns(t, app, "demo")
	if got == 0 {
		t.Fatal("expected at least one run to have been drafted before the round cap forced a final answer")
	}
	// chatMaxToolRounds is 6 (chattools.go); the forced-final last round never executes a tool
	// call, so at most 5 rounds can ever draft a run for a directive that never stops repeating.
	if got > 5 {
		t.Fatalf("expected the round cap to bound repeated propose_run drafts to at most 5, got %d", got)
	}

	// The final answer must be real content, not an unresolved tool_call left dangling for the
	// client (the forced-final round strips propose_run from the request precisely to guarantee
	// this).
	choices, _ := body["choices"].([]any)
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	if tc, _ := msg["tool_calls"].([]any); len(tc) != 0 {
		t.Fatalf("expected the forced-final round to leave no dangling tool_calls, got: %v", tc)
	}
	if _, ok := msg["content"].(string); !ok {
		t.Fatalf("expected a real string answer after the round cap, got message: %v", msg)
	}
}

// chatContent is defined in chattools_api_test.go (same package) and reused here.
