package gateway

// Tests for the per-project Playground tool-call budget override (chatlimits.go): the pure
// clamp/lower/floor computation, the hand-edited-row read clamp, the GET/POST handlers (auth,
// validation, coexistence with auto_pr), and the companion BridgeMaxHops overflow fix (R1) that
// this file's chatEffectiveOne was written to avoid from the start.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackofall1232/l00prite/cli-os/internal/config"
)

func hdr(name, val string) http.Header {
	h := http.Header{}
	h.Set(name, val)
	return h
}

// TestChatEffectiveOne pins the resolution order: default -> stored (clamped to ceiling) -> header
// (lower-only, NaN/Inf/negative/huge-finite rejected) -> floor 1.
func TestChatEffectiveOne(t *testing.T) {
	const h = "x-test"
	cases := []struct {
		name   string
		stored int
		header http.Header
		want   int
	}{
		{"unset -> default", 0, nil, 6},
		{"stored raises", 10, nil, 10},
		{"stored above ceiling clamps on read", 999, nil, 24},
		{"header lowers below stored", 10, hdr(h, "3"), 3},
		{"header cannot raise above base", 10, hdr(h, "20"), 10},
		{"huge finite header (1e300) ignored, never underflows", 10, hdr(h, "1e300"), 10},
		{"Infinity header ignored", 10, hdr(h, "Infinity"), 10},
		{"NaN header ignored", 10, hdr(h, "NaN"), 10},
		{"negative header ignored", 10, hdr(h, "-5"), 10},
		{"header 0 floors to 1", 0, hdr(h, "0"), 1},
		{"non-integral header truncates down", 10, hdr(h, "2.9"), 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := chatEffectiveOne(6, 24, c.stored, c.header, h)
			if got != c.want {
				t.Fatalf("chatEffectiveOne(6,24,%d,%v) = %d, want %d", c.stored, c.header, got, c.want)
			}
		})
	}
}

// TestEffectiveChatLimitsReadsStoredAndClampsHandEditedRow drives the DB-backed resolver: unset ->
// defaults; a value set through the handler raises the effective budget; a row hand-edited ABOVE
// the ceiling (which the handler would refuse) is clamped down on read (defense in depth).
func TestEffectiveChatLimitsReadsStoredAndClampsHandEditedRow(t *testing.T) {
	app := newAutoPRTestApp(t)
	tok := mintToken(t, app, "p")

	// Unset -> compiled defaults.
	if eff := app.effectiveChatLimits("p", nil); eff.Rounds != chatDefaultMaxToolRounds || eff.Calls != chatDefaultMaxToolCallsRun {
		t.Fatalf("unset should be defaults (%d/%d), got %d/%d", chatDefaultMaxToolRounds, chatDefaultMaxToolCallsRun, eff.Rounds, eff.Calls)
	}

	// Raised via the handler.
	doAuthed(app, "POST", "/v1/chat-limits", tok, map[string]any{"max_tool_rounds": 12, "max_tool_calls": 40}, app.HandleChatLimitsSet)
	if eff := app.effectiveChatLimits("p", nil); eff.Rounds != 12 || eff.Calls != 40 {
		t.Fatalf("stored override not honored, got %d/%d", eff.Rounds, eff.Calls)
	}

	// Hand-edited above the ceiling -> clamped on read.
	if _, err := app.DB.Exec(`UPDATE project_settings SET chat_max_tool_rounds=999, chat_max_tool_calls=999 WHERE project='p'`); err != nil {
		t.Fatal(err)
	}
	if eff := app.effectiveChatLimits("p", nil); eff.Rounds != chatCeilingToolRounds || eff.Calls != chatCeilingToolCalls {
		t.Fatalf("hand-edited over-ceiling row should clamp to %d/%d, got %d/%d", chatCeilingToolRounds, chatCeilingToolCalls, eff.Rounds, eff.Calls)
	}
}

// TestBridgeMaxHopsHugeFiniteHeaderDoesNotUnderflow is the R1 companion-fix regression: a
// finite-but-huge header ("1e300") passes the Inf/NaN guard, but the pre-fix `int(n) < base`
// converted it to a min-int (implementation-defined out-of-range float->int) that looked like a
// request to LOWER the cap to a negative — making the bridge's maxTurns negative. The fix compares
// in float space first, so the header is simply ignored and the config base stands. Confirmed to
// fail against the pre-fix bridge.go (returns a large negative).
func TestBridgeMaxHopsHugeFiniteHeaderDoesNotUnderflow(t *testing.T) {
	cfg := config.Config{Routing: config.Routing{Bridge: config.Bridge{MaxHops: 3}}}
	for _, raw := range []string{"1e300", "Infinity", "NaN", "-1", "abc"} {
		if got := BridgeMaxHops(hdr("x-l00prite-bridge-max-hops", raw), cfg); got != 3 {
			t.Fatalf("BridgeMaxHops with header %q = %d, want the config base 3 (never a negative/underflow)", raw, got)
		}
	}
	// A genuine lower value still works.
	if got := BridgeMaxHops(hdr("x-l00prite-bridge-max-hops", "1"), cfg); got != 1 {
		t.Fatalf("BridgeMaxHops should still allow lowering to 1, got %d", got)
	}
}

// TestHandleChatLimitsRoundTripAndCoexistsWithAutoPR drives the real handlers: 401 unauth, default
// view, POST/GET round-trip, ceiling + negative validation (400), per-project scoping, and — the
// column-specific-upsert invariant — that chat limits and the Auto-PR toggle never clobber each
// other regardless of write order.
func TestHandleChatLimitsRoundTripAndCoexistsWithAutoPR(t *testing.T) {
	app := newAutoPRTestApp(t)
	tok := mintToken(t, app, "proj-a")
	other := mintToken(t, app, "proj-b")

	// Unauthenticated -> 401.
	if w := doAuthed(app, "GET", "/v1/chat-limits", "", nil, app.HandleChatLimitsGet); w.Code != 401 {
		t.Fatalf("unauth GET = %d, want 401", w.Code)
	}

	// Default view: stored 0/0, effective = defaults, and the defaults/ceilings advertised.
	w := doAuthed(app, "GET", "/v1/chat-limits", tok, nil, app.HandleChatLimitsGet)
	var got map[string]any
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["max_tool_rounds"] != float64(0) || got["max_tool_calls"] != float64(0) {
		t.Fatalf("default stored view should be 0/0, got %v", got)
	}
	eff := got["effective"].(map[string]any)
	if eff["rounds"] != float64(6) || eff["calls"] != float64(24) {
		t.Fatalf("default effective should be 6/24, got %v", eff)
	}

	// POST a raise -> reflected on GET.
	w = doAuthed(app, "POST", "/v1/chat-limits", tok, map[string]any{"max_tool_rounds": 10, "max_tool_calls": 50}, app.HandleChatLimitsSet)
	if w.Code != 200 {
		t.Fatalf("POST = %d: %s", w.Code, w.Body.String())
	}
	w = doAuthed(app, "GET", "/v1/chat-limits", tok, nil, app.HandleChatLimitsGet)
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["max_tool_rounds"] != float64(10) || got["max_tool_calls"] != float64(50) {
		t.Fatalf("GET after POST = %v, want 10/50", got)
	}

	// Validation: above ceiling and negative both 400.
	if w := doAuthed(app, "POST", "/v1/chat-limits", tok, map[string]any{"max_tool_rounds": 25, "max_tool_calls": 10}, app.HandleChatLimitsSet); w.Code != 400 {
		t.Fatalf("rounds above ceiling should 400, got %d", w.Code)
	}
	if w := doAuthed(app, "POST", "/v1/chat-limits", tok, map[string]any{"max_tool_rounds": 5, "max_tool_calls": -1}, app.HandleChatLimitsSet); w.Code != 400 {
		t.Fatalf("negative calls should 400, got %d", w.Code)
	}

	// Scoping: proj-b sees its own (default) values, not proj-a's raise.
	w = doAuthed(app, "GET", "/v1/chat-limits", other, nil, app.HandleChatLimitsGet)
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["max_tool_rounds"] != float64(0) || got["project"] != "proj-b" {
		t.Fatalf("proj-b must see its own defaults, got %v", got)
	}

	// Coexistence: turning Auto-PR on must not wipe proj-a's chat limits, and vice versa.
	doAuthed(app, "POST", "/v1/auto-pr", tok, map[string]any{"enabled": true}, app.HandleAutoPRSet)
	if !app.autoPREnabled("proj-a") {
		t.Fatal("auto_pr should be ON")
	}
	if eff := app.effectiveChatLimits("proj-a", nil); eff.Rounds != 10 || eff.Calls != 50 {
		t.Fatalf("setting auto_pr clobbered chat limits, got %d/%d", eff.Rounds, eff.Calls)
	}
	doAuthed(app, "POST", "/v1/chat-limits", tok, map[string]any{"max_tool_rounds": 8, "max_tool_calls": 30}, app.HandleChatLimitsSet)
	if !app.autoPREnabled("proj-a") {
		t.Fatal("setting chat limits clobbered auto_pr (should still be ON)")
	}
}
