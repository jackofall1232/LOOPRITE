package server_test

// End-to-end tests for Bug 1 of the 2026-07-11 control-plane diagnosis: a chat completion naming a
// registered repo can now actually browse it (read_file/list_dir/search_files), instead of only
// ever seeing the static 5-file .l00prite/ memory digest. Drives the real HTTP path through the
// mock adapter's "/chattool <name> <json-args>" test directive (adapters/mock.go), which only fires
// when the request actually offers that tool -- so a real file-content echo in the final answer is
// itself proof the tool was attached, dispatched, executed, and its result flowed back.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/server"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"

	"net/http/httptest"
)

// chatToolsServer builds a server with the mock provider and one registered repo containing both
// a real source file and a secret-shaped file, returning (base URL, token, repo root).
func chatToolsServer(t *testing.T) (string, string, string) {
	t.Helper()
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
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n\nfunc main() { println(\"chat-tools-e2e-marker\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".env"), []byte("API_KEY=do-not-leak-me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO repos(id,root,project,created_at) VALUES('demo',?,'demo',?)`, repoDir, util.NowISO()); err != nil {
		t.Fatal(err)
	}
	// Project-scoped only (repo=nil), NOT repo-scoped: TestChatToolsNotAttachedWithoutRepo relies on
	// the token itself carrying no repo, so omitting the x-l00prite-repo header really means "no
	// repo selected" rather than falling back to a repo baked into the token.
	_, token, err := security.MintToken(db, "demo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases}
	srv := httptest.NewServer(server.Handler(app))
	t.Cleanup(srv.Close)
	return srv.URL, token, repoDir
}

func chatContent(t *testing.T, body map[string]any) string {
	t.Helper()
	choices, _ := body["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in response: %v", body)
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	c, _ := msg["content"].(string)
	return c
}

// A read_file call for a real file, with a repo selected, round-trips real content back into the
// final answer.
func TestChatToolsReadFileRoundTrip(t *testing.T) {
	base, token, _ := chatToolsServer(t)
	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool read_file {"path":"main.go"}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	content := chatContent(t, body)
	if !strings.Contains(content, "chat-tools-e2e-marker") {
		t.Fatalf("expected the final answer to include real file content, got: %q", content)
	}
}

// Without a repo selected, the chat tools are never attached -- the directive text is inert and
// the reply is the plain mock response, exactly as it was before this change.
func TestChatToolsNotAttachedWithoutRepo(t *testing.T) {
	base, token, _ := chatToolsServer(t)
	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool read_file {"path":"main.go"}`}},
	}, nil)
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	content := chatContent(t, body)
	if strings.Contains(content, "chat-tools-e2e-marker") {
		t.Fatalf("no repo was selected; file content must not have been read, got: %q", content)
	}
	if !strings.Contains(content, "Mock provider response") {
		t.Fatalf("expected the unchanged plain mock reply, got: %q", content)
	}
}

// A path-traversal attempt through the real tool-call round trip is rejected, and the secret .env
// file is never readable even by an explicit read_file call.
func TestChatToolsRejectsTraversalAndSecrets(t *testing.T) {
	base, token, _ := chatToolsServer(t)

	traversal := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool read_file {"path":"../outside.txt"}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	tBody := bodyJSON(t, traversal)
	if traversal.StatusCode != 200 {
		t.Fatalf("expected 200 (the tool call fails gracefully, not the HTTP request), got %d: %v", traversal.StatusCode, tBody)
	}
	if c := chatContent(t, tBody); !strings.Contains(c, "escapes the repository") {
		t.Fatalf("expected an \"escapes the repository\" tool error to be echoed, got: %q", c)
	}

	secret := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool read_file {"path":".env"}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	sBody := bodyJSON(t, secret)
	if secret.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", secret.StatusCode, sBody)
	}
	c := chatContent(t, sBody)
	if strings.Contains(c, "do-not-leak-me") {
		t.Fatalf(".env content leaked into the final answer: %q", c)
	}
	if !strings.Contains(c, "DENIED") && !strings.Contains(c, "credential") {
		t.Fatalf("expected the DENIED-secret-path tool result to be echoed, got: %q", c)
	}
}

// A client's OWN tool literally named "read_file" must never be silently hijacked by the gateway's
// jailed file browser (adversarial-review finding). The mock adapter's /chattool directive still
// proposes a "read_file" tool_call (it only checks that SOME tool named read_file is present, which
// is true here -- it's the client's), but RunChatTools must recognize the name collides with a
// client-supplied tool and defer to the client instead of executing its own local read: the real
// file's marker content must never appear, and the raw tool_call must pass through unexecuted for
// the client to handle.
func TestChatToolsNeverHijacksClientOwnedToolName(t *testing.T) {
	base, token, _ := chatToolsServer(t)
	clientReadFileTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "read_file", "description": "the CLIENT's own tool, unrelated to the repo",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool read_file {"path":"main.go"}`}},
		"tools":    []any{clientReadFileTool},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	choices, _ := body["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in response: %v", body)
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if strings.Contains(content, "chat-tools-e2e-marker") {
		t.Fatalf("a client-owned \"read_file\" tool must never be executed by the gateway's file browser; real file content leaked: %q", content)
	}
	toolCalls, _ := msg["tool_calls"].([]any)
	if len(toolCalls) == 0 {
		t.Fatalf("expected the unexecuted \"read_file\" tool_call to pass through to the client, got message: %v", msg)
	}
	fn, _ := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "read_file" {
		t.Fatalf("expected the passed-through tool_call to be named read_file, got: %v", fn)
	}
}

// A tool-call round trip is two separate, independently billed/ledgered runTurn calls (the
// tool-proposing round and the finalization round). The client-visible response's usage object
// must report the SUM across both, not just the last round's numbers (adversarial-review finding:
// empirically, the last-round-only figure silently dropped the first round's real, already-billed
// usage). Ground truth comes straight from the ledger table, keyed by the request id the server
// itself reports in the x-l00prite-request-id header -- not a hardcoded expectation, so this test
// can't pass by coincidentally matching a guessed number.
func TestChatToolsUsageAccumulatesAcrossRounds(t *testing.T) {
	base, token, _ := chatToolsServer(t)
	cfg := config.Load() // same LOOPRITE_HOME the test harness just set for chatToolsServer
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	resp := post(t, base, token, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": `/chattool read_file {"path":"main.go"}`}},
	}, map[string]string{"x-l00prite-repo": "demo"})
	body := bodyJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
	}
	reqID := resp.Header.Get("x-l00prite-request-id")
	if reqID == "" {
		t.Fatal("expected an x-l00prite-request-id response header")
	}

	var rows int
	var truePrompt, trueCompletion int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0) FROM ledger WHERE request_id = ?`, reqID).
		Scan(&rows, &truePrompt, &trueCompletion); err != nil {
		t.Fatal(err)
	}
	if rows < 2 {
		t.Fatalf("expected at least 2 ledger rows for one tool-call round trip (proposing round + finalization round), got %d", rows)
	}

	usage, _ := body["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("expected a usage object in the response, got: %v", body)
	}
	gotPrompt, _ := usage["prompt_tokens"].(float64)
	gotCompletion, _ := usage["completion_tokens"].(float64)
	if int(gotPrompt) != truePrompt || int(gotCompletion) != trueCompletion {
		t.Fatalf("response usage (prompt=%v, completion=%v) does not match the true summed ledger usage across all %d rounds (prompt=%d, completion=%d) -- earlier rounds' usage was dropped",
			gotPrompt, gotCompletion, rows, truePrompt, trueCompletion)
	}

	gotHeaderCost := resp.Header.Get("x-l00prite-cost-usd")
	if gotHeaderCost == "" {
		t.Fatal("expected an x-l00prite-cost-usd response header")
	}
}
