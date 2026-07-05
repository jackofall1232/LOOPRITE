// Low-level upstream call helpers shared by the ingress streaming path and the internal turn
// primitive. Extracted so a bridge hop reuses the exact same idempotency-aware, abort-aware call
// path as a top-level request — never by re-entering HTTP. Ported from upstream.js. Go's
// context.WithTimeout(clientCtx, ...) gives both the request timeout and client-disconnect abort
// (the clientCtx is cancelled when the client closes the connection), covering the full body read.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
	"github.com/jackofall1232/l00prite/cli-os/internal/gateway/adapters"
)

// httpClient uses the default transport, which honors HTTPS_PROXY and the system CA bundle. No
// client-level timeout — the per-request context deadline governs (and covers streaming body reads).
var httpClient = &http.Client{}

func isRetryable(status int) bool { return status == 429 || status == 408 || status >= 500 }

// providerFetch issues the POST under a context that both times out and cancels on client
// disconnect. On success the caller MUST call the returned cancel() after the body is fully read; on
// error cancel is a safe no-op (already cancelled).
func providerFetch(ctx context.Context, url string, headers map[string]string, body any, timeoutMs int) (*http.Response, context.CancelFunc, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	b, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	return resp, cancel, nil
}

func readLimited(r io.Reader, n int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return string(b)
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) // cap at n runes, never splitting a multibyte character
}

func backoff(baseMs, attempt, maxMs int) time.Duration {
	ms := baseMs << attempt
	if ms > maxMs {
		ms = maxMs
	}
	return time.Duration(ms) * time.Millisecond
}

// callNonStream is the non-streaming provider call with idempotency-aware retry (no client-visible
// bytes have flushed, so every attempt within the cap is safe).
func callNonStream(a adapters.Adapter, provider ProviderRow, model string, req map[string]any, apiKey string, cfg config.Config, clientCtx context.Context) (adapters.FullResult, error) {
	if a.Direct() {
		return a.(adapters.DirectAdapter).DirectFull(req, model), nil
	}
	na := a.(adapters.NetworkAdapter)
	body := na.BuildRequest(model, req, false)
	url := na.URL(provider.BaseURL)
	headers := na.Headers(apiKey)

	var lastErr error
	for attempt := 0; attempt < cfg.Retry.MaxAttempts; attempt++ {
		resp, cancel, err := providerFetch(clientCtx, url, headers, body, cfg.RequestTimeoutMs)
		if err != nil {
			if clientCtx.Err() != nil {
				return adapters.FullResult{}, HTTPError(499, "client closed request", "client_closed")
			}
			lastErr = err
		} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var j map[string]any
			derr := json.NewDecoder(resp.Body).Decode(&j)
			resp.Body.Close()
			cancel()
			if derr == nil {
				return na.ParseFull(j, model), nil
			}
			lastErr = HTTPError(502, "upstream "+provider.Name+" returned invalid JSON", "upstream_error")
		} else {
			errText := readLimited(resp.Body, 4096)
			resp.Body.Close()
			cancel()
			if !isRetryable(resp.StatusCode) {
				return adapters.FullResult{}, HTTPError(resp.StatusCode,
					fmt.Sprintf("upstream %s %d: %s", provider.Name, resp.StatusCode, truncStr(errText, 300)), "upstream_error")
			}
			lastErr = HTTPError(502, fmt.Sprintf("upstream %s %d", provider.Name, resp.StatusCode), "upstream_error")
		}
		if clientCtx.Err() != nil {
			return adapters.FullResult{}, HTTPError(499, "client closed request", "client_closed")
		}
		time.Sleep(backoff(cfg.Retry.BaseMs, attempt, cfg.Retry.MaxMs))
	}
	if lastErr != nil {
		return adapters.FullResult{}, lastErr
	}
	return adapters.FullResult{}, HTTPError(502, "upstream failed", "upstream_error")
}

// parseSSE parses complete SSE frames from a buffer already CRLF-normalized to \n. It yields every
// frame except the trailing (possibly incomplete) one, matching the JS generator's semantics.
func parseSSE(buffer string) []adapters.SSEEvent {
	parts := strings.Split(buffer, "\n\n")
	var out []adapters.SSEEvent
	for i := 0; i < len(parts)-1; i++ {
		var event string
		var dataLines []string
		for _, line := range strings.Split(parts[i], "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(line[len("data:"):]))
			}
		}
		if len(dataLines) > 0 {
			out = append(out, adapters.SSEEvent{Event: event, Data: strings.Join(dataLines, "\n")})
		}
	}
	return out
}
