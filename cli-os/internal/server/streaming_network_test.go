package server_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway"
	"github.com/jackofall1232/l00prite/cli-os/internal/security"
	"github.com/jackofall1232/l00prite/cli-os/internal/server"
	"github.com/jackofall1232/l00prite/cli-os/internal/state"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

// setupGateway builds a fresh data dir + db + gateway HTTP server, failing fast on any setup error.
func setupGateway(t *testing.T) (*sql.DB, config.Config) {
	t.Helper()
	t.Setenv("LOOPRITE_HOME", t.TempDir())
	t.Setenv("LOOPRITE_MASTER_KEY", "")
	cfg := config.Load()
	if err := config.EnsureHome(cfg); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := security.EnsureMasterKey(cfg.MasterKeyPath); err != nil {
		t.Fatalf("master key: %v", err)
	}
	db, err := state.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db, cfg
}

// sse writes a set of raw SSE frames with a flush between each, simulating a real provider stream.
func sseServer(frames []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, f := range frames {
			w.Write([]byte(f))
			if fl != nil {
				fl.Flush()
			}
		}
	}))
}

// TestNetworkStreamingTranslators exercises the NETWORK streaming path (not the mock/direct one the
// rest of the suite covers): the gateway connects to a real HTTP provider, reads SSE, and folds it
// into OpenAI chunks — once through the Anthropic native translator, once through openai-compat.
func TestNetworkStreamingTranslators(t *testing.T) {
	db, cfg := setupGateway(t)

	fakeAnthropic := sseServer([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi \"}}\n\n",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"there\"}}\n\n",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n",
		"data: {\"type\":\"message_stop\"}\n\n",
	})
	defer fakeAnthropic.Close()

	fakeOpenAI := sseServer([]string{
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" OAI\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2},\"choices\":[]}\n\n",
		"data: [DONE]\n\n",
	})
	defer fakeOpenAI.Close()

	encA, _ := security.EncryptSecret(cfg.MasterKeyPath, "sk-ant-test")
	encO, _ := security.EncryptSecret(cfg.MasterKeyPath, "sk-oai-test")
	db.Exec(`INSERT INTO providers(name,adapter,base_url,enc_key,enabled,is_default,created_at) VALUES('anthropic','native-messages',?,?,1,1,?)`, fakeAnthropic.URL, encA, util.NowISO())
	db.Exec(`INSERT INTO providers(name,adapter,base_url,enc_key,enabled,is_default,created_at) VALUES('openai','openai-compat',?,?,1,0,?)`, fakeOpenAI.URL, encO, util.NowISO())

	_, tok, _ := security.MintToken(db, "demo", nil, nil)
	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases}
	srv := httptest.NewServer(server.Handler(app))
	defer srv.Close()

	t.Run("anthropic native SSE folds to OpenAI chunks", func(t *testing.T) {
		resp := post(t, srv.URL, tok, map[string]any{"model": "anthropic/claude-opus-4-8", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		text := bodyText(t, resp)
		if !strings.Contains(text, "data: [DONE]") {
			t.Fatalf("missing [DONE]")
		}
		if got := sseContent(text); got != "Hi there" {
			t.Fatalf("folded content = %q, want %q", got, "Hi there")
		}
	})

	t.Run("openai-compat SSE passes through", func(t *testing.T) {
		resp := post(t, srv.URL, tok, map[string]any{"model": "openai/gpt-x", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp.StatusCode)
		}
		text := bodyText(t, resp)
		if !strings.Contains(text, "data: [DONE]") {
			t.Fatalf("missing [DONE]")
		}
		if got := sseContent(text); got != "Hello OAI" {
			t.Fatalf("folded content = %q, want %q", got, "Hello OAI")
		}
	})
}

// TestStreamMidDropFailsClosed verifies the fail-closed path: a provider that drops the connection
// mid-stream (no terminal event) must commit the reservation ceiling and record error_midstream,
// NOT commit ~$0 and log ok. Without this, a dropped stream leaks budget past the daily cap.
func TestStreamMidDropFailsClosed(t *testing.T) {
	db, cfg := setupGateway(t)

	// Fake Anthropic: write partial SSE, then hijack + abruptly close the TCP conn (no terminating
	// chunk) so the gateway's reader sees an unexpected EOF mid-stream.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server does not support hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		writeChunk := func(s string) { fmt.Fprintf(conn, "%x\r\n%s\r\n", len(s), s) }
		writeChunk("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":9,\"output_tokens\":0}}}\n\n")
		writeChunk("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
		// no terminating "0\r\n\r\n" chunk: abrupt close
	}))
	defer fake.Close()

	encA, _ := security.EncryptSecret(cfg.MasterKeyPath, "sk-ant-test")
	db.Exec(`INSERT INTO providers(name,adapter,base_url,enc_key,enabled,is_default,created_at) VALUES('anthropic','native-messages',?,?,1,1,?)`, fake.URL, encA, util.NowISO())
	_, tok, _ := security.MintToken(db, "demo", nil, nil)

	app := &gateway.App{DB: db, Cfg: cfg, Aliases: cfg.Aliases}
	srv := httptest.NewServer(server.Handler(app))
	defer srv.Close()

	resp := post(t, srv.URL, tok, map[string]any{"model": "anthropic/claude-opus-4-8", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, nil)
	reqID := resp.Header.Get("x-l00prite-request-id")
	_ = bodyText(t, resp) // drain (also ensures the handler has finished + logged)

	var outcome string
	var estimated int
	if err := db.QueryRow(`SELECT outcome, cost_estimated FROM ledger WHERE request_id = ?`, reqID).Scan(&outcome, &estimated); err != nil {
		t.Fatalf("ledger query: %v", err)
	}
	if outcome != "error_midstream" {
		t.Fatalf("mid-stream drop must fail closed (outcome error_midstream), got %q", outcome)
	}
	if estimated != 1 {
		t.Fatalf("mid-stream commit must be flagged estimated")
	}
	var committed float64
	db.QueryRow(`SELECT COALESCE(SUM(committed_usd),0) FROM spend`).Scan(&committed)
	if committed <= 0 {
		t.Fatalf("mid-stream failure must commit the reservation ceiling, committed=%v", committed)
	}
}
