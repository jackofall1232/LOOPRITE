package server_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
)

func strptr(s string) *string { return &s }

func post(t *testing.T, base, token string, body map[string]any, headers map[string]string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", base+"/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("content-type", "application/json")
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

func bodyJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &m)
	return m
}

func bodyText(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// sseContent joins the content deltas from an SSE stream body.
func sseContent(text string) string {
	var sb strings.Builder
	for _, block := range strings.Split(text, "\n\n") {
		if !strings.HasPrefix(block, "data:") || strings.Contains(block, "[DONE]") {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(block[len("data:"):])), &chunk) != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok {
			sb.WriteString(c)
		}
	}
	return sb.String()
}

func TestServerE2E(t *testing.T) {
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	config.EnsureHome(cfg)
	security.EnsureMasterKey(cfg.MasterKeyPath)
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	db.Exec(`INSERT INTO providers(name,adapter,base_url,enc_key,enabled,is_default,created_at) VALUES('mock','mock',NULL,NULL,1,1,?)`, util.NowISO())

	repoDir := t.TempDir()
	os.MkdirAll(filepath.Join(repoDir, ".l00prite"), 0o755)
	os.WriteFile(filepath.Join(repoDir, ".l00prite", "constraints.md"), []byte("# Constraints\nUse tabs."), 0o600)
	db.Exec(`INSERT INTO repos(id,root,project,created_at) VALUES('demo',?,'demo',?)`, repoDir, util.NowISO())

	_, tokenDemo, _ := security.MintToken(db, "demo", strptr("demo"), nil)
	_, tokenCapped, _ := security.MintToken(db, "capped", nil, nil)
	db.Exec(`INSERT OR REPLACE INTO caps(project,window,limit_usd) VALUES('capped','daily',0.10)`)

	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases}
	srv := httptest.NewServer(server.Handler(app))
	defer srv.Close()
	base := srv.URL

	t.Run("rejects missing token 401", func(t *testing.T) {
		resp := post(t, base, "", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}, nil)
		if resp.StatusCode != 401 {
			t.Fatalf("want 401 got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("non-streaming returns OpenAI shape + records ledger", func(t *testing.T) {
		resp := post(t, base, tokenDemo, map[string]any{"model": "demo-model", "messages": []any{map[string]any{"role": "user", "content": "hello there"}}}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		reqID := resp.Header.Get("x-l00prite-request-id")
		j := bodyJSON(t, resp)
		if j["object"] != "chat.completion" {
			t.Fatalf("object %v", j["object"])
		}
		msg := j["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
		if s, _ := msg["content"].(string); len(s) == 0 {
			t.Fatalf("empty content")
		}
		var outcome, provider, memStatus string
		if err := db.QueryRow(`SELECT outcome, provider, memory_status FROM ledger WHERE request_id = ?`, reqID).Scan(&outcome, &provider, &memStatus); err != nil {
			t.Fatalf("ledger query: %v", err)
		}
		if outcome != "ok" || provider != "mock" || memStatus != "ok" {
			t.Fatalf("ledger row: outcome=%s provider=%s memory=%s", outcome, provider, memStatus)
		}
	})

	t.Run("streaming emits SSE deltas then [DONE]", func(t *testing.T) {
		resp := post(t, base, tokenDemo, map[string]any{"model": "demo-model", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "stream please"}}}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("content-type"), "text/event-stream") {
			t.Fatalf("content-type %s", resp.Header.Get("content-type"))
		}
		text := bodyText(t, resp)
		if !strings.Contains(text, "data: [DONE]") {
			t.Fatalf("missing [DONE]")
		}
		if len(sseContent(text)) == 0 {
			t.Fatalf("stream should carry content deltas")
		}
	})

	t.Run("cost cap denies with 402 before spending", func(t *testing.T) {
		resp := post(t, base, tokenCapped, map[string]any{"model": "demo-model", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, nil)
		if resp.StatusCode != 402 {
			t.Fatalf("want 402 got %d", resp.StatusCode)
		}
		j := bodyJSON(t, resp)
		if j["error"].(map[string]any)["code"] != "cost_cap" {
			t.Fatalf("error code %v", j["error"])
		}
	})

	t.Run("repo-scoped token cannot be widened via header 403", func(t *testing.T) {
		resp := post(t, base, tokenDemo, map[string]any{"model": "demo", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, map[string]string{"x-l00prite-repo": "some-other-repo"})
		if resp.StatusCode != 403 {
			t.Fatalf("want 403 got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("unregistered repo 404", func(t *testing.T) {
		resp := post(t, base, tokenCapped, map[string]any{"model": "demo", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, map[string]string{"x-l00prite-repo": "does-not-exist"})
		if resp.StatusCode != 404 {
			t.Fatalf("want 404 got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("models and healthz", func(t *testing.T) {
		h, _ := http.Get(base + "/healthz")
		if h.StatusCode != 200 {
			t.Fatalf("healthz %d", h.StatusCode)
		}
		hj := bodyJSON(t, h)
		if hj["status"] != "ok" {
			t.Fatalf("status %v", hj["status"])
		}
		found := false
		for _, p := range hj["providers"].([]any) {
			if p.(map[string]any)["name"] == "mock" {
				found = true
			}
		}
		if !found {
			t.Fatalf("mock provider missing from healthz")
		}
		m, _ := http.Get(base + "/v1/models")
		if m.StatusCode != 200 {
			t.Fatalf("models %d", m.StatusCode)
		}
		m.Body.Close()
	})
}

func TestBridgeE2E(t *testing.T) {
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	config.EnsureHome(cfg)
	security.EnsureMasterKey(cfg.MasterKeyPath)
	db, _ := state.Open(cfg.DBPath)
	addProv := func(name string, isDef int) {
		db.Exec(`INSERT INTO providers(name,adapter,base_url,enc_key,enabled,is_default,created_at) VALUES(?,?,NULL,NULL,1,?,?)`, name, "mock", isDef, util.NowISO())
	}
	addProv("mock", 1)
	addProv("mock2", 0)
	addProv("anthropic", 0) // adapter=mock but PRICED via the anthropic manifest, for cap math

	repoDir := t.TempDir()
	os.MkdirAll(filepath.Join(repoDir, ".l00prite"), 0o755)
	os.WriteFile(filepath.Join(repoDir, ".l00prite", "constraints.md"), []byte("# Constraints\nBe careful."), 0o600)
	db.Exec(`INSERT INTO repos(id,root,project,created_at) VALUES('demo',?,'demo',?)`, repoDir, util.NowISO())

	_, tokDemo, _ := security.MintToken(db, "demo", strptr("demo"), nil)
	_, tokCapTiny, _ := security.MintToken(db, "capTiny", nil, nil)
	_, tokCapMid, _ := security.MintToken(db, "capMid", nil, nil)
	db.Exec(`INSERT OR REPLACE INTO caps(project,window,limit_usd) VALUES('capTiny','daily',0.001)`)
	db.Exec(`INSERT OR REPLACE INTO caps(project,window,limit_usd) VALUES('capMid','daily',0.05)`)

	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases}
	srv := httptest.NewServer(server.Handler(app))
	defer srv.Close()
	base := srv.URL

	t.Run("bridge delegates and folds the answer", func(t *testing.T) {
		resp := post(t, base, tokDemo, map[string]any{"model": "demo-model", "messages": []any{map[string]any{"role": "user", "content": "/bridge mock2 :: what is 2+2"}}}, map[string]string{"x-l00prite-bridge": "on"})
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		reqID := resp.Header.Get("x-l00prite-request-id")
		if resp.Header.Get("x-l00prite-bridge-hops") != "1" {
			t.Fatalf("hops %s", resp.Header.Get("x-l00prite-bridge-hops"))
		}
		j := bodyJSON(t, resp)
		content := j["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		if !strings.Contains(content, "Mock final answer") {
			t.Fatalf("content %q", content)
		}
		rows, _ := db.Query(`SELECT provider, decision, outcome FROM ledger WHERE request_id = ? ORDER BY ts`, reqID)
		defer rows.Close()
		var n, delegateMock2 int
		allOK := true
		for rows.Next() {
			var provider, decision, outcome sql.NullString
			rows.Scan(&provider, &decision, &outcome)
			n++
			if strings.Contains(decision.String, "bridge_delegate") && provider.String == "mock2" {
				delegateMock2++
			}
			if outcome.String != "ok" {
				allOK = false
			}
		}
		if n < 3 {
			t.Fatalf("expected >=3 ledger rows, got %d", n)
		}
		if delegateMock2 == 0 {
			t.Fatalf("a delegate hop to mock2 must be recorded")
		}
		if !allOK {
			t.Fatalf("all hops should be ok")
		}
	})

	t.Run("delegate not given the bridge tool (one hop)", func(t *testing.T) {
		resp := post(t, base, tokDemo, map[string]any{"model": "demo-model", "messages": []any{map[string]any{"role": "user", "content": "/bridge mock2 :: summarize"}}}, map[string]string{"x-l00prite-bridge": "on"})
		if resp.Header.Get("x-l00prite-bridge-hops") != "1" {
			t.Fatalf("hops %s", resp.Header.Get("x-l00prite-bridge-hops"))
		}
		resp.Body.Close()
	})

	t.Run("bridging off by default", func(t *testing.T) {
		resp := post(t, base, tokDemo, map[string]any{"model": "demo-model", "messages": []any{map[string]any{"role": "user", "content": "/bridge mock2 :: what is 2+2"}}}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		if resp.Header.Get("x-l00prite-bridge-hops") != "" {
			t.Fatalf("no bridge hops header expected")
		}
		j := bodyJSON(t, resp)
		content := j["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		if !strings.Contains(content, "Mock provider response") {
			t.Fatalf("content %q", content)
		}
	})

	t.Run("hop cap bounds delegated sub-calls", func(t *testing.T) {
		resp := post(t, base, tokDemo, map[string]any{"model": "demo-model", "messages": []any{map[string]any{"role": "user", "content": "/bridgeloop mock2 :: keep delegating"}}}, map[string]string{"x-l00prite-bridge": "on", "x-l00prite-bridge-max-hops": "2"})
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		if resp.Header.Get("x-l00prite-bridge-hops") != "2" {
			t.Fatalf("loop must stop at the lowered hop cap, got %s", resp.Header.Get("x-l00prite-bridge-hops"))
		}
		j := bodyJSON(t, resp)
		content := j["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		if !strings.Contains(content, "Mock final answer") {
			t.Fatalf("content %q", content)
		}
	})

	t.Run("stream + bridge: only the final answer leaks", func(t *testing.T) {
		resp := post(t, base, tokDemo, map[string]any{"model": "demo-model", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "/bridge mock2 :: hi"}}}, map[string]string{"x-l00prite-bridge": "on"})
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		text := bodyText(t, resp)
		if !strings.Contains(text, "data: [DONE]") {
			t.Fatalf("missing [DONE]")
		}
		if strings.Contains(text, "l00prite_bridge") {
			t.Fatalf("no delegate tool_call may leak to the client")
		}
		if !strings.Contains(sseContent(text), "Mock final answer") {
			t.Fatalf("final answer not streamed")
		}
	})

	t.Run("cap below first hop: 402 up front", func(t *testing.T) {
		resp := post(t, base, tokCapTiny, map[string]any{"model": "claude-haiku-4-5", "messages": []any{map[string]any{"role": "user", "content": "/bridge anthropic/claude-opus-4-8 :: compute"}}}, map[string]string{"x-l00prite-bridge": "on"})
		if resp.StatusCode != 402 {
			t.Fatalf("want 402 got %d", resp.StatusCode)
		}
		j := bodyJSON(t, resp)
		if j["error"].(map[string]any)["code"] != "cost_cap" {
			t.Fatalf("code %v", j["error"])
		}
		if resp.Header.Get("x-l00prite-cap-usd") == "" {
			t.Fatalf("cap header missing")
		}
	})

	t.Run("mid-bridge cap denial: delegate refused, primary finalizes", func(t *testing.T) {
		resp := post(t, base, tokCapMid, map[string]any{"model": "claude-haiku-4-5", "messages": []any{map[string]any{"role": "user", "content": "/bridge anthropic/claude-opus-4-8 :: compute the 5th fibonacci number"}}}, map[string]string{"x-l00prite-bridge": "on"})
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		reqID := resp.Header.Get("x-l00prite-request-id")
		j := bodyJSON(t, resp)
		content := j["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		if !strings.Contains(content, "Mock final answer") {
			t.Fatalf("content %q", content)
		}
		rows, _ := db.Query(`SELECT model, outcome FROM ledger WHERE request_id = ?`, reqID)
		defer rows.Close()
		found := false
		for rows.Next() {
			var model, outcome sql.NullString
			rows.Scan(&model, &outcome)
			if outcome.String == "denied_cost_cap" && model.String == "claude-opus-4-8" {
				found = true
			}
		}
		if !found {
			t.Fatalf("opus delegate hop must be recorded as cap-denied")
		}
	})

	t.Run("PEP invariant: no stranded reservations", func(t *testing.T) {
		var stranded int
		db.QueryRow(`SELECT COUNT(*) FROM reservations WHERE state = 'reserved'`).Scan(&stranded)
		if stranded != 0 {
			t.Fatalf("every reservation must be committed or refunded, got %d stranded", stranded)
		}
		var reserved float64
		db.QueryRow(`SELECT COALESCE(SUM(reserved_usd),0) FROM spend`).Scan(&reserved)
		if reserved > 1e-9 || reserved < -1e-9 {
			t.Fatalf("reserved balance must net to zero, got %v", reserved)
		}
	})

	t.Run("plain client tool loop is not a bridge finalization", func(t *testing.T) {
		resp := post(t, base, tokDemo, map[string]any{"model": "demo-model", "messages": []any{
			map[string]any{"role": "user", "content": "weather?"},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_x", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": "call_x", "content": "72F sunny"},
		}}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		j := bodyJSON(t, resp)
		content := j["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		if !strings.Contains(content, "Mock provider response") {
			t.Fatalf("content %q", content)
		}
		if strings.Contains(content, "composed after delegating") {
			t.Fatalf("must not fire the bridge finalization for a plain tool loop")
		}
	})

	t.Run("stream + bridge with include_usage emits a usage chunk", func(t *testing.T) {
		resp := post(t, base, tokDemo, map[string]any{"model": "demo-model", "stream": true, "stream_options": map[string]any{"include_usage": true}, "messages": []any{map[string]any{"role": "user", "content": "/bridge mock2 :: hi"}}}, map[string]string{"x-l00prite-bridge": "on"})
		text := bodyText(t, resp)
		var usageChunk map[string]any
		for _, block := range strings.Split(text, "\n\n") {
			if !strings.HasPrefix(block, "data:") || strings.Contains(block, "[DONE]") {
				continue
			}
			var chunk map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(block[len("data:"):])), &chunk) == nil {
				if chunk["usage"] != nil {
					usageChunk = chunk
				}
			}
		}
		if usageChunk == nil {
			t.Fatalf("a usage chunk must be emitted when include_usage is set")
		}
		if len(usageChunk["choices"].([]any)) != 0 {
			t.Fatalf("usage chunk must have empty choices")
		}
		if usageChunk["usage"].(map[string]any)["total_tokens"].(float64) <= 0 {
			t.Fatalf("aggregate usage must be reported")
		}
	})

	t.Run("dry-run route plan does not spend", func(t *testing.T) {
		var before int
		db.QueryRow(`SELECT COUNT(*) FROM ledger`).Scan(&before)
		resp := post(t, base, tokDemo, map[string]any{"model": "demo-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}, map[string]string{"x-l00prite-dry-run": "1"})
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		j := bodyJSON(t, resp)
		if j["object"] != "l00prite.route_plan" {
			t.Fatalf("object %v", j["object"])
		}
		if j["would_route"].(map[string]any)["provider"] == nil {
			t.Fatalf("would_route missing")
		}
		if j["bridge"].(map[string]any)["max_hops"].(float64) != 3 {
			t.Fatalf("max_hops %v", j["bridge"])
		}
		var after int
		db.QueryRow(`SELECT COUNT(*) FROM ledger`).Scan(&after)
		if after != before {
			t.Fatalf("dry-run must not append a ledger row")
		}
	})
}
