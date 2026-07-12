// Capability-gap surfacing for chat/bridge tool-call dispatch. Neither RunChatTools (chatloop.go)
// nor RunBridge (bridge.go) can determine ahead of time whether a task needs git/shell access --
// that would mean guessing from prose. What IS a deterministic fact is the moment the model emits a
// tool call naming an execution capability (git, gh, a shell) that nobody in this session owns:
// not a gateway-active tool (chatToolNames/chatRunToolName), not one the CLIENT defined. Today that
// dead-end is answered with deferredClientTool's "RE-EMIT this in your FINAL message" instruction --
// which is actively misleading here, because there IS no client waiting to run it (e.g. the
// dashboard Playground defines no tools at all), so the model spirals into free-text reasoning about
// a capability that will never appear. This file gives that specific dead-end a second, more honest
// answer.
//
// Mirrors humanizeStartError's (runs.go) discipline: the returned string is always one of two fixed,
// human-authored sentences, selected only by a boolean -- never the tool name's raw arguments, never
// model/error text. A tool call from a genuinely unowned NON-execution-shaped name (some client
// tool-plausible name the model hallucinated) is unaffected -- it still gets deferredClientTool
// exactly as before; this file only changes the answer for the execution-shaped subset.
package gateway

import "strings"

// executionShapedToolNames is a conservative, fixed set of tool names that name real execution
// capability -- the engine's own six tool names (read_file/write_file/list_dir/search_files/
// run_command/git_command) plus common aliases a model might plausibly emit for "run a shell
// command" or "use git/gh" (bash, shell, exec_command, terminal, apply_patch, edit_file, gh,
// git, create_pull_request). Deliberately does NOT include read_file/list_dir/search_files --
// those are the read-only tools this chat session already grants when a repo is selected, so a
// call to one of them is never "unowned" in the first place (it's dispatched via activeNames
// before this file is ever consulted).
var executionShapedToolNames = map[string]bool{
	"git_command":         true,
	"run_command":         true,
	"write_file":          true,
	"edit_file":           true,
	"apply_patch":         true,
	"bash":                true,
	"shell":               true,
	"exec_command":        true,
	"terminal":            true,
	"gh":                  true,
	"git":                 true,
	"create_pull_request": true,
}

// executionShapedToolPrefixes catches common naming FAMILIES (a model rarely emits the exact bare
// name "gh" -- it more often emits "gh_pr_create", "git_push", "push_to_remote", etc.) without
// trying to enumerate every possible suffix. Kept short and conservative on purpose: a false
// positive here just means a legitimately client-owned tool falls through to this check, but
// clientOwned (see unownedToolResult) is checked FIRST and always wins, so a real collision can
// never be hijacked by this heuristic regardless of how broad these prefixes are.
var executionShapedToolPrefixes = []string{"git_", "gh_", "push_"}

// executionShapedTool reports whether name plausibly names an execution capability (git/gh/shell/
// filesystem-write) rather than an inert data lookup. Used only to decide which of the two fixed
// "nobody owns this call" sentences to return -- never to allow or deny execution of anything.
func executionShapedTool(name string) bool {
	if name == "" {
		return false
	}
	if executionShapedToolNames[name] {
		return true
	}
	for _, p := range executionShapedToolPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// capabilityGapNote returns the fixed tool-result JSON for an execution-shaped tool call nobody in
// this session owns. Shape deliberately mirrors chatToolCapReached/deferredClientTool/capReached
// (all in this package): {"status", "tool", "note"} with the note a fixed, human-authored sentence
// pair, never the tool's raw name-derived text or any error string. The propose_run clause is
// included only when the caller has that tool active in this session (RunChatTools with an engine
// wired); RunBridge, which has no propose_run concept at all, always passes false.
func capabilityGapNote(tool string, proposeRunAvailable bool) string {
	if tool == "" {
		tool = "unknown"
	}
	note := "This chat session has read-only repository access — it cannot run git, GitHub (gh), or " +
		"shell commands. Tell the user in one or two plain sentences that this step needs execution " +
		"access, and offer next steps: "
	if proposeRunAvailable {
		note += "draft an autonomous Run with propose_run — Runs have governed git and command tools " +
			"behind human approval gates, or "
	}
	note += "split the work so this step is done by hand in a terminal. Do not print your internal reasoning."
	return jsonStr(map[string]any{"status": "capability_gap", "tool": tool, "note": note})
}

// unownedToolResult is the single decision both RunChatTools (chatloop.go) and RunBridge
// (bridge.go) delegate to for a tool call that is not gateway-active (not a chat tool / not the
// bridge tool). clientOwned must be true when and only when the CLIENT's own request already
// defines a tool with this exact name -- that check always wins over executionShapedTool, so a
// client-defined tool literally named "git_command" is still deferred to the client (today's
// behavior, unchanged) rather than hijacked into a capability-gap answer the client never asked
// for. Centralizing the decision here (rather than duplicating the executionShapedTool check
// inline at both call sites) keeps the two call sites' dispatch branches identical in shape and
// gives the decision one unit-testable seam.
func unownedToolResult(tc map[string]any, clientOwned, proposeRunAvailable bool) string {
	name := asStr(asMap(tc["function"])["name"])
	if !clientOwned && executionShapedTool(name) {
		return capabilityGapNote(name, proposeRunAvailable)
	}
	return deferredClientTool(tc)
}
