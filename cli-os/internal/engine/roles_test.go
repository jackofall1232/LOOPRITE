package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---- PlanForObjective ----

func TestPlanForObjectiveTable(t *testing.T) {
	cases := []struct {
		objective string
		profiles  map[string]string
		review    bool
		bridge    bool
	}{
		{ObjectiveBalanced, map[string]string{"plan": "plan", "code": "code", "review": "review", "summarize": "summarize"}, true, true},
		{ObjectiveQuality, map[string]string{"plan": "plan", "code": "quality", "review": "review", "summarize": "summarize"}, true, true},
		{ObjectiveCost, map[string]string{"plan": "cheap", "code": "cheap", "review": "review", "summarize": "cheap"}, false, false},
		{ObjectiveSpeed, map[string]string{"plan": "balanced", "code": "balanced", "review": "review", "summarize": "cheap"}, false, false},
		{ObjectivePrivacy, map[string]string{"plan": "privacy", "code": "privacy", "review": "privacy", "summarize": "privacy"}, false, false},
	}
	for _, c := range cases {
		got, err := PlanForObjective(c.objective)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.objective, err)
		}
		if len(got.Profiles) != len(c.profiles) {
			t.Fatalf("%s: profile count = %d, want %d (%v)", c.objective, len(got.Profiles), len(c.profiles), got.Profiles)
		}
		for role, want := range c.profiles {
			if got.Profiles[role] != want {
				t.Errorf("%s: role %q profile = %q, want %q", c.objective, role, got.Profiles[role], want)
			}
		}
		if got.Review != c.review {
			t.Errorf("%s: Review = %v, want %v", c.objective, got.Review, c.review)
		}
		if got.Bridge != c.bridge {
			t.Errorf("%s: Bridge = %v, want %v", c.objective, got.Bridge, c.bridge)
		}
	}
}

func TestPlanForObjectiveEmptyDefaultsToBalanced(t *testing.T) {
	got, err := PlanForObjective("  ")
	if err != nil {
		t.Fatalf("empty objective should default to balanced, got error: %v", err)
	}
	if got.Profiles[RoleCode] != "code" || !got.Review || !got.Bridge {
		t.Fatalf("empty objective did not resolve to balanced: %+v", got)
	}
}

func TestPlanForObjectiveUnknownErrors(t *testing.T) {
	_, err := PlanForObjective("turbo")
	if err == nil {
		t.Fatalf("unknown objective must error")
	}
	// The error must name the valid objectives so the caller can correct it.
	for _, o := range Objectives {
		if !strings.Contains(err.Error(), o) {
			t.Errorf("error %q should list objective %q", err.Error(), o)
		}
	}
}

// ---- ModelForRole ----

func TestModelForRole(t *testing.T) {
	rp, _ := PlanForObjective(ObjectiveQuality)
	if got := ModelForRole(rp, RoleCode); got != "auto:quality" {
		t.Errorf("code role model = %q, want auto:quality", got)
	}
	if got := ModelForRole(rp, RolePlan); got != "auto:plan" {
		t.Errorf("plan role model = %q, want auto:plan", got)
	}
	// A role absent from the plan defensively resolves to bare "auto".
	if got := ModelForRole(rp, "security"); got != "auto" {
		t.Errorf("missing role model = %q, want auto", got)
	}
	// An empty profile also resolves to bare "auto", never "auto:".
	if got := ModelForRole(TeamPlan{Profiles: map[string]string{RoleCode: ""}}, RoleCode); got != "auto" {
		t.Errorf("empty profile model = %q, want auto", got)
	}
}

// ---- ParseUnitSelection ----

func TestParseUnitSelectionToolCallStringArgs(t *testing.T) {
	args := `{"action":"unit","description":"  add roles.go  ","target_paths":["a.go"," "],"verification_command":"go build ./..."}`
	msg := map[string]any{
		"tool_calls": []any{
			map[string]any{"function": map[string]any{"name": "select_unit", "arguments": args}},
		},
	}
	u, err := ParseUnitSelection(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Action != "unit" {
		t.Errorf("action = %q, want unit", u.Action)
	}
	if u.Description != "add roles.go" {
		t.Errorf("description not trimmed: %q", u.Description)
	}
	if len(u.TargetPaths) != 1 || u.TargetPaths[0] != "a.go" {
		t.Errorf("target_paths not normalized: %v", u.TargetPaths)
	}
	if u.VerificationCommand != "go build ./..." {
		t.Errorf("verification_command = %q", u.VerificationCommand)
	}
}

func TestParseUnitSelectionToolCallObjectArgs(t *testing.T) {
	// Some providers hand back arguments already decoded into an object.
	msg := map[string]any{
		"tool_calls": []any{
			map[string]any{"function": map[string]any{
				"name": "select_unit",
				"arguments": map[string]any{
					"action":       "done",
					"done_reason":  "all DoD checks pass",
					"target_paths": []any{"x.go"},
				},
			}},
		},
	}
	u, err := ParseUnitSelection(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Action != "done" || u.DoneReason != "all DoD checks pass" {
		t.Errorf("object args not parsed: %+v", u)
	}
	if len(u.TargetPaths) != 1 || u.TargetPaths[0] != "x.go" {
		t.Errorf("target_paths from object args wrong: %v", u.TargetPaths)
	}
}

func TestParseUnitSelectionContentFallback(t *testing.T) {
	// No tool_calls: the JSON is embedded (with prose around it) in the message content.
	content := "Here is my choice:\n{\"action\":\"ambiguous\",\"question\":\"which API?\"}\nHope that helps."
	msg := map[string]any{"content": content}
	u, err := ParseUnitSelection(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Action != "ambiguous" || u.Question != "which API?" {
		t.Errorf("content fallback not parsed: %+v", u)
	}
}

func TestParseUnitSelectionGarbageErrors(t *testing.T) {
	msg := map[string]any{"content": "I could not decide. No JSON here at all."}
	if _, err := ParseUnitSelection(msg); err == nil {
		t.Fatalf("garbage content must error")
	}
}

func TestParseUnitSelectionUnknownActionErrors(t *testing.T) {
	msg := map[string]any{
		"tool_calls": []any{
			map[string]any{"function": map[string]any{"name": "select_unit", "arguments": `{"action":"proceed"}`}},
		},
	}
	if _, err := ParseUnitSelection(msg); err == nil {
		t.Fatalf("unknown action must error")
	}
}

// ---- ParseReviewVerdict ----

func TestParseReviewVerdictToolCallStringArgs(t *testing.T) {
	msg := map[string]any{
		"tool_calls": []any{
			map[string]any{"function": map[string]any{"name": "review_verdict", "arguments": `{"approve":false,"must_fix":true,"issues":["off by one"," "]}`}},
		},
	}
	v, err := ParseReviewVerdict(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Approve || !v.MustFix {
		t.Errorf("verdict flags wrong: %+v", v)
	}
	if len(v.Issues) != 1 || v.Issues[0] != "off by one" {
		t.Errorf("issues not normalized: %v", v.Issues)
	}
}

func TestParseReviewVerdictToolCallObjectArgs(t *testing.T) {
	msg := map[string]any{
		"tool_calls": []any{
			map[string]any{"function": map[string]any{
				"name":      "review_verdict",
				"arguments": map[string]any{"approve": true, "must_fix": false, "issues": []any{}},
			}},
		},
	}
	v, err := ParseReviewVerdict(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Approve || v.MustFix {
		t.Errorf("object args verdict wrong: %+v", v)
	}
}

func TestParseReviewVerdictContentFallback(t *testing.T) {
	msg := map[string]any{"content": "Verdict: {\"approve\": true, \"issues\": []}"}
	v, err := ParseReviewVerdict(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Approve {
		t.Errorf("content fallback verdict wrong: %+v", v)
	}
}

// ---- BuildPlannerReq ----

func TestBuildPlannerReq(t *testing.T) {
	allow := []string{"go build ./...", "go test ./internal/engine"}
	oversized := strings.Repeat("x", tailCapChars+500)
	req := BuildPlannerReq("auto:plan", "ship the feature", "tests pass", oversized, "ledger tail", allow, "iteration 3 verified")

	if req["model"] != "auto:plan" {
		t.Errorf("model = %v, want auto:plan", req["model"])
	}

	// tool_choice forces select_unit.
	tc, _ := req["tool_choice"].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "select_unit" {
		t.Errorf("tool_choice does not force select_unit: %v", req["tool_choice"])
	}

	// select_unit is the offered tool.
	tools, _ := req["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("want exactly one tool, got %d", len(tools))
	}
	toolFn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	if toolFn["name"] != "select_unit" {
		t.Errorf("offered tool = %v, want select_unit", toolFn["name"])
	}

	// max_tokens is the planner budget.
	if req["max_tokens"] != 2048 {
		t.Errorf("max_tokens = %v, want 2048", req["max_tokens"])
	}

	msgs, _ := req["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("want system+user messages, got %d", len(msgs))
	}
	sys, _ := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != plannerSystem {
		t.Errorf("first message is not the planner system prompt")
	}
	user, _ := msgs[1].(map[string]any)
	content, _ := user["content"].(string)

	// Every labeled section is present.
	for _, label := range []string{
		"GOAL", "DEFINITION OF DONE", "CURRENT TODOS", "RECENT LEDGER",
		"ALLOWED VERIFICATION COMMANDS", "PREVIOUS ITERATION OUTCOME",
	} {
		if !strings.Contains(content, label) {
			t.Errorf("user content missing section label %q", label)
		}
	}
	// The allowlist entries are listed verbatim.
	for _, cmd := range allow {
		if !strings.Contains(content, cmd) {
			t.Errorf("user content missing allowlist entry %q", cmd)
		}
	}
	// The oversized todos tail was truncated.
	if !strings.Contains(content, "[truncated]") {
		t.Errorf("oversized todosTail was not truncated")
	}
	if strings.Count(content, "x") > tailCapChars {
		t.Errorf("todosTail not capped: %d x's present", strings.Count(content, "x"))
	}
	if !strings.Contains(content, "ship the feature") || !strings.Contains(content, "iteration 3 verified") {
		t.Errorf("goal/last-outcome not embedded in user content")
	}
}

func TestBuildReviewerReq(t *testing.T) {
	// 'q' does not appear in the fixed header text, so counting it isolates the diff body.
	big := strings.Repeat("q", diffCapChars+1000)
	req := BuildReviewerReq("auto:review", "the unit", big)
	if req["model"] != "auto:review" || req["max_tokens"] != 1024 {
		t.Errorf("reviewer req scaffolding wrong: model=%v max_tokens=%v", req["model"], req["max_tokens"])
	}
	tc, _ := req["tool_choice"].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "review_verdict" {
		t.Errorf("tool_choice does not force review_verdict")
	}
	msgs, _ := req["messages"].([]any)
	user, _ := msgs[1].(map[string]any)
	content, _ := user["content"].(string)
	if !strings.Contains(content, "[truncated]") {
		t.Errorf("oversized diff was not truncated")
	}
	if n := strings.Count(content, "q"); n > diffCapChars {
		t.Errorf("diff not capped at %d chars: %d body chars kept", diffCapChars, n)
	}
}

func TestBuildSummarizerReqHasNoTools(t *testing.T) {
	req := BuildSummarizerReq("auto:summarize", "3 iterations, tests passed")
	if _, hasTools := req["tools"]; hasTools {
		t.Errorf("summarizer request must not offer tools")
	}
	if req["max_tokens"] != 512 {
		t.Errorf("max_tokens = %v, want 512", req["max_tokens"])
	}
	msgs, _ := req["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)
	if sys["content"] != summarizerSystem {
		t.Errorf("summarizer system prompt not set")
	}
}

// ---- tool schemas round-trip as JSON (providers receive them serialized) ----

func TestToolSchemasMarshal(t *testing.T) {
	for _, tool := range []map[string]any{selectUnitTool(), reviewVerdictTool()} {
		if _, err := json.Marshal(tool); err != nil {
			t.Fatalf("tool schema does not marshal: %v", err)
		}
		fn, _ := tool["function"].(map[string]any)
		if fn["name"] == "" {
			t.Errorf("tool missing function name")
		}
		params, _ := fn["parameters"].(map[string]any)
		if params["type"] != "object" {
			t.Errorf("tool parameters not an object schema")
		}
	}
}
