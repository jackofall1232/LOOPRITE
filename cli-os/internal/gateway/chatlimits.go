// Per-project override of the Playground chat tool-call budget (the read-only repo-browsing loop in
// chatloop.go). This is the human-set answer to "I hit the tool-call limit when asking it to do
// PR-sized work and it made me start over": a maintainer can RAISE the per-turn round/call budget
// for their project, up to a hard compile-time ceiling, so a single reply can browse enough of the
// repo to finish instead of forcing a "send a follow-up to continue" restart.
//
// The self-modification guard is preserved by construction:
//   - The ceiling (chatCeilingToolRounds / chatCeilingToolCalls) is a compile-time constant no
//     runtime actor can exceed; it is enforced both on write (the POST handler rejects a larger
//     value) and on read (effectiveChatLimits clamps a hand-edited row down to it).
//   - The stored override is written ONLY by POST /v1/chat-limits behind requireToken -- a human's
//     audited dashboard action. The model has no tool that can reach it: chat tools are read-only
//     file ops + propose_run (drafts only), and propose_run's schema deliberately carries no budget
//     field, so the model can never grant itself a bigger budget.
//   - A per-request header may only LOWER the effective value, never raise it above the human-set
//     base -- mirroring BridgeMaxHops, but comparing in float space before the int conversion to
//     avoid the finite-but-huge (1e300) overflow that bug has (see effectiveChatLimits).
//
// Mirrors autopr.go's structure line-for-line: requireToken principal, scope = principal.Project,
// a shared GET/POST view, auditAs on write, a fail-safe read that treats a missing row / DB error
// as "unset" (-> the compiled default), and a column-specific upsert that never clobbers auto_pr.
package gateway

import (
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/jackofall1232/l00prite/cli-os/internal/state"
)

// ChatToolLimits is the resolved per-turn budget RunChatTools loops against.
type ChatToolLimits struct {
	Rounds int // max full model turns (rounds) per chat completion
	Calls  int // max read_file/list_dir/search_files/propose_run calls across those rounds
}

// storedChatLimits reads the project's raw override values (0 = unset for either). A missing row or
// any query error both read as (0, 0) -- fail-safe to the compiled defaults, the same posture
// autoPREnabled uses (a transient DB hiccup must never silently widen a budget).
func (app *App) storedChatLimits(project string) (rounds, calls int) {
	err := app.DB.QueryRowContext(state.Ctx(),
		`SELECT chat_max_tool_rounds, chat_max_tool_calls FROM project_settings WHERE project = ?`,
		project).Scan(&rounds, &calls)
	if err != nil {
		return 0, 0
	}
	return rounds, calls
}

// effectiveChatLimits computes the budget for one request: compiled default -> stored override
// (clamped to the ceiling) -> per-request header (lower-only) -> floor of 1. Pass a nil header for
// the "at rest" value (GET view). The clamp/lower/floor ORDER is load-bearing (see chatEffectiveOne).
func (app *App) effectiveChatLimits(project string, h http.Header) ChatToolLimits {
	rounds, calls := app.storedChatLimits(project)
	return ChatToolLimits{
		Rounds: chatEffectiveOne(chatDefaultMaxToolRounds, chatCeilingToolRounds, rounds, h, "x-l00prite-chat-max-rounds"),
		Calls:  chatEffectiveOne(chatDefaultMaxToolCallsRun, chatCeilingToolCalls, calls, h, "x-l00prite-chat-max-tool-calls"),
	}
}

// chatEffectiveOne resolves one knob. Order matters:
//  1. base = default; a stored value > 0 replaces it, clamped DOWN to the ceiling first.
//  2. a header value may only LOWER base (never raise it above the human-set base).
//  3. floor at 1 LAST -- a 0 would make RunChatTools' round loop never run and return a null body.
func chatEffectiveOne(def, ceiling, stored int, h http.Header, header string) int {
	base := def
	if stored > 0 {
		base = stored
		if base > ceiling {
			base = ceiling
		}
	}
	if h != nil {
		if raw := h.Get(header); raw != "" {
			n, err := strconv.ParseFloat(raw, 64)
			// Reject non-finite/negative exactly as BridgeMaxHops does. Then compare in FLOAT space
			// before converting: a finite-but-huge value like "1e300" passes the Inf/NaN guard, yet
			// int(1e300) is implementation-dependent (min-int on amd64) -- so `int(n) < base` (the
			// latent BridgeMaxHops bug) would treat it as a request to LOWER to a negative. `n <
			// float64(base)` cannot: 1e300 is never < a small base, so the header is simply ignored.
			if err == nil && n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n) && n < float64(base) {
				base = int(n)
			}
		}
	}
	if base < 1 {
		base = 1
	}
	return base
}

// chatLimitsSetReq is the POST /v1/chat-limits body. The dashboard always sends BOTH fields
// (prefilled from the GET view), so a write replaces the whole override; 0 for either means "reset
// that knob to the default". JSON decoding into int fields rejects fractional/overflowing numbers
// before any handler validation runs, so no arithmetic ever touches an unvalidated value.
type chatLimitsSetReq struct {
	MaxToolRounds int `json:"max_tool_rounds"`
	MaxToolCalls  int `json:"max_tool_calls"`
}

// chatLimitsView is the shared GET/POST response. Stored values are shown raw (0 = unset so the UI
// can render the default as a placeholder); "effective" is what a request with no header would use.
func (app *App) chatLimitsView(project string) map[string]any {
	rounds, calls := app.storedChatLimits(project)
	eff := app.effectiveChatLimits(project, nil)
	return map[string]any{
		"object": "l00prite.chat_limits", "project": project,
		"max_tool_rounds": rounds, "max_tool_calls": calls,
		"effective": map[string]any{"rounds": eff.Rounds, "calls": eff.Calls},
		"defaults":  map[string]any{"rounds": chatDefaultMaxToolRounds, "calls": chatDefaultMaxToolCallsRun},
		"ceilings":  map[string]any{"rounds": chatCeilingToolRounds, "calls": chatCeilingToolCalls},
	}
}

// HandleChatLimitsGet is GET /v1/chat-limits.
func (app *App) HandleChatLimitsGet(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	sendJSON(w, 200, app.chatLimitsView(principal.Project))
}

// HandleChatLimitsSet is POST /v1/chat-limits. Body: {"max_tool_rounds": int, "max_tool_calls": int}.
// Each must be 0..ceiling (0 resets to the default); anything else is a 400 that names the ceiling.
func (app *App) HandleChatLimitsSet(w http.ResponseWriter, r *http.Request) {
	principal := app.requireToken(w, r)
	if principal == nil {
		return
	}
	var body chatLimitsSetReq
	if err := decodeSetupBody(r, &body); err != nil {
		oaiError(w, 400, "Invalid JSON body (max_tool_rounds and max_tool_calls must be whole numbers)", "invalid_request_error", "")
		return
	}
	if body.MaxToolRounds < 0 || body.MaxToolRounds > chatCeilingToolRounds {
		oaiError(w, 400, fmt.Sprintf("max_tool_rounds must be between 0 (use the default) and %d", chatCeilingToolRounds), "invalid_request_error", "")
		return
	}
	if body.MaxToolCalls < 0 || body.MaxToolCalls > chatCeilingToolCalls {
		oaiError(w, 400, fmt.Sprintf("max_tool_calls must be between 0 (use the default) and %d", chatCeilingToolCalls), "invalid_request_error", "")
		return
	}
	// Column-specific upsert: names ONLY the two chat columns, so it can never clobber auto_pr, and
	// autopr.go's own upsert (which names only auto_pr) can never clobber these -- the two settings
	// coexist on one row. A fresh row leaves auto_pr at its DEFAULT 0 (OFF), which is its own default.
	if _, err := app.DB.ExecContext(state.Ctx(),
		`INSERT INTO project_settings(project, chat_max_tool_rounds, chat_max_tool_calls) VALUES(?,?,?)
		 ON CONFLICT(project) DO UPDATE SET chat_max_tool_rounds=excluded.chat_max_tool_rounds,
		                                    chat_max_tool_calls=excluded.chat_max_tool_calls`,
		principal.Project, body.MaxToolRounds, body.MaxToolCalls); err != nil {
		oaiError(w, 500, "Could not save the chat tool budget: "+err.Error(), "api_error", "")
		return
	}
	app.auditAs(principal, "chat_limits.set", fmt.Sprintf("%s rounds=%d calls=%d", principal.Project, body.MaxToolRounds, body.MaxToolCalls))
	sendJSON(w, 200, app.chatLimitsView(principal.Project))
}
