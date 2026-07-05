package engine

// tools.go is the engine-executed tool layer for the coder role's tool-loop
// (design: cli-os/docs/os-architecture.md §2.4). It is the ONLY place the autonomous run
// engine touches the user's filesystem and shell, so every path is jailed to the repo root,
// every mutating tool goes through the layered write policy (protocol-file hard-deny ->
// Autonomous-Edit Denylist gate -> allowed inside the repo), and every command/git action
// that is not on the pre-flight allowlist is turned into a per-action approval gate instead
// of being executed silently.
//
// Model-visible failures NEVER surface as Go errors: they become Result strings the model can
// read and react to (e.g. "ERROR: path escapes the repository"). Go errors are reserved for
// the engine-driven helpers (EnsureRunBranch/CommitUnit/CurrentDiff), which the loop calls
// directly, not the model.
//
// Denylist parsing/matching lives in the sibling l00pfiles.go (ParseDenylist / MatchDenylist);
// this file only calls it.

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// GateRequest describes an action that was NOT executed because it needs per-action human
// approval. The engine surfaces it through the approvals UI and, once approved, re-calls
// Execute with approved=true.
type GateRequest struct {
	Class  string         // GatePush | GateMerge | GateDeploy | GateCredentialChange | GateDestructive | GateOutsideRepo
	Action string         // human-readable, e.g. `write_file ".env"` or `run_command "rm -rf build"`
	Args   map[string]any // exact tool args, for the approvals UI
}

// ToolOutcome is the result of one tool call. When Gate is non-nil the action was NOT
// executed and Result is advisory; the engine must obtain per-action approval, then re-call
// Execute with approved=true.
type ToolOutcome struct {
	Result string       // the string fed back to the model as the tool result
	Gate   *GateRequest // non-nil -> action not executed; needs per-action approval
}

// Toolbox is the per-run, per-repo tool executor. Root is the jail boundary; nothing the
// model does may read or write outside it.
type Toolbox struct {
	Root      string   // absolute repo root (jail boundary)
	Denylist  []string // parsed Autonomous-Edit Denylist globs from the target repo's constraints.md
	Allowlist []string // command allowlist confirmed at pre-flight
	Branch    string   // the run branch (git ops constrained to it)
}

// ---- limits ----

const (
	writeCapBytes   = 2 * 1024 * 1024 // write_file content cap
	readCapBytes    = 256 * 1024      // read_file read cap
	listCapEntries  = 500             // list_dir entry cap
	searchTotalCap  = 48 * 1024       // search_files total output cap
	searchFileCap   = 1 << 20         // search_files per-file size cap (1 MiB)
	searchDefaultN  = 100             // search_files default max_results
	searchMaxN      = 200             // search_files max_results ceiling
	cmdOutputCap    = 64 * 1024       // run_command combined-output cap
	gitOutputCap    = 32 * 1024       // git_command combined-output cap
	cmdDefaultTOSec = 300             // run_command default timeout
	cmdMaxTOSec     = 900             // run_command timeout ceiling
	gitTimeoutSec   = 60              // git_command / helper git timeout
)

// ---- tool definitions ----

// Definitions returns the OpenAI-shaped tool definitions offered to the coder role. Paths in
// every tool are repository-relative; `.l00prite/` protocol files are never writable.
func (tb *Toolbox) Definitions() []map[string]any {
	strArr := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return []map[string]any{
		fnTool("read_file",
			"Read a UTF-8 text file. `path` is repository-relative (never absolute, never outside the repo). "+
				"Optional 1-indexed `offset_line`/`limit_lines` select a line window. Files are read up to 256 KiB; "+
				"any truncation is noted explicitly in the result.",
			objSchema(map[string]any{
				"path":        map[string]any{"type": "string", "description": "repository-relative path"},
				"offset_line": map[string]any{"type": "integer", "description": "1-indexed first line to return (optional)"},
				"limit_lines": map[string]any{"type": "integer", "description": "maximum number of lines to return (optional)"},
			}, "path")),

		fnTool("write_file",
			"Create or overwrite a text file at a repository-relative `path`; parent directories are created. "+
				"Paths are always repo-relative and stay inside the repository. `.l00prite/` protocol files "+
				"(heartbeat.json, state.json, lock.json, constraints.md, prompts/**) are engine-owned and are NEVER writable during a "+
				"run. A path matching the Autonomous-Edit Denylist is suspended for separate human approval.",
			objSchema(map[string]any{
				"path":    map[string]any{"type": "string", "description": "repository-relative path"},
				"content": map[string]any{"type": "string", "description": "full file contents (max 2 MiB)"},
			}, "path", "content")),

		fnTool("list_dir",
			"List a repository-relative directory (defaults to the repo root when `path` is omitted). Directory names "+
				"end with `/`; files show their size. `.git` is skipped. Paths are always repo-relative.",
			objSchema(map[string]any{
				"path": map[string]any{"type": "string", "description": "repository-relative directory (optional; defaults to repo root)"},
			})),

		fnTool("search_files",
			"Search file contents under the repository root. `query` is treated as a regular expression if it compiles, "+
				"otherwise as a literal substring. `.git` and files over 1 MiB are skipped. Each hit is `path:line: text` "+
				"(paths repo-relative); results are capped.",
			objSchema(map[string]any{
				"query":       map[string]any{"type": "string", "description": "regexp (if it compiles) or literal substring"},
				"max_results": map[string]any{"type": "integer", "description": "max matching lines (default 100, cap 200)"},
			}, "query")),

		fnTool("run_command",
			"Run a shell command in the repository root. Only commands on the pre-flight command allowlist run without "+
				"approval; anything not on that allowlist is suspended for explicit per-action human approval before it "+
				"runs — no other command is executed automatically. The result begins with an `exit_code` line.",
			objSchema(map[string]any{
				"command":   map[string]any{"type": "string", "description": "the shell command to run in the repo root"},
				"timeout_s": map[string]any{"type": "integer", "description": "timeout in seconds (default 300, cap 900)"},
			}, "command")),

		fnTool("git_command",
			"Run a git subcommand in the repository. `args` is the argument vector (e.g. [\"status\",\"--porcelain\"]); "+
				"args[0] must be a subcommand, never a global flag. status/diff/log/add/commit/show run without "+
				"approval; so does a bare `branch` (list) or `branch <name>` (create one) — any branch flag "+
				"(-d/-D/-f/-m/-M/--delete/--force/--move, etc.) requires approval since it can delete, rename, or "+
				"force-move a ref. push/merge and history rewrites (rebase/reset/clean/force-push, etc.) require "+
				"human approval. Paths inside args are repo-relative.",
			objSchema(map[string]any{
				"args": mergeSchema(strArr, map[string]any{"description": "git argument vector; args[0] is the subcommand"}),
			}, "args")),

		fnTool("unit_done",
			"Signal that the current unit of work is complete. Provide a short `summary` and the list of "+
				"repository-relative files changed.",
			objSchema(map[string]any{
				"summary":       map[string]any{"type": "string", "description": "what this unit accomplished"},
				"files_changed": mergeSchema(strArr, map[string]any{"description": "repository-relative files changed"}),
			}, "summary", "files_changed")),

		fnTool("unit_blocked",
			"Signal that the current unit cannot proceed. `kind` is one of ambiguous, missing_credentials, "+
				"cannot_proceed; `reason` explains why.",
			objSchema(map[string]any{
				"kind":   map[string]any{"type": "string", "enum": []string{"ambiguous", "missing_credentials", "cannot_proceed"}},
				"reason": map[string]any{"type": "string", "description": "why the unit is blocked"},
			}, "kind", "reason")),
	}
}

func fnTool(name, desc string, params map[string]any) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  params,
		},
	}
}

func objSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// mergeSchema shallow-merges extra keys (e.g. description) onto a base schema fragment.
func mergeSchema(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// ---- dispatch ----

// Execute runs one model tool call. It never returns a Go error; model-visible failures are
// Result strings. unit_done/unit_blocked are normally intercepted by the loop before Execute
// reaches them — this keeps the safe no-op so a stray call cannot do anything.
func (tb *Toolbox) Execute(ctx context.Context, name string, args map[string]any, approved bool) ToolOutcome {
	switch name {
	case "read_file":
		return tb.readFile(args)
	case "write_file":
		return tb.writeFile(args, approved)
	case "list_dir":
		return tb.listDir(args)
	case "search_files":
		return tb.searchFiles(args)
	case "run_command":
		return tb.runCommand(ctx, args, approved)
	case "git_command":
		return tb.gitCommand(ctx, args, approved)
	case "unit_done", "unit_blocked":
		return ToolOutcome{Result: "acknowledged"}
	default:
		return ToolOutcome{Result: fmt.Sprintf("ERROR: unknown tool %q", name)}
	}
}

// ---- path jail ----

// resolvePath validates a repo-relative path and returns the absolute path inside Root.
// It rejects empty/absolute/escaping paths and, mirroring internal/memory.within(), fails
// closed if the deepest existing ancestor resolves (symlinks included) outside Root.
func (tb *Toolbox) resolvePath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths are not allowed; use a repository-relative path")
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the repository")
	}
	abs := filepath.Join(tb.Root, cleaned)

	rootResolved, err := filepath.EvalSymlinks(tb.Root)
	if err != nil {
		return "", fmt.Errorf("path escapes the repository")
	}
	// Deepest EXISTING ancestor of the target (Lstat so a symlink counts as existing and is
	// then resolved to where it truly points).
	ancestor := abs
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	ancestorResolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("path escapes the repository")
	}
	if ancestorResolved != rootResolved && !strings.HasPrefix(ancestorResolved, rootResolved+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the repository")
	}
	return abs, nil
}

// policyRel returns the forward-slash, cleaned, repo-relative form used by the write policy.
func policyRel(raw string) string {
	return filepath.ToSlash(filepath.Clean(raw))
}

// protocolProtected reports whether rel (forward-slash, repo-relative) is an engine-owned
// protocol file that is never writable during a run — not gate-then-approvable like a Denylist
// hit, an unconditional hard-deny. constraints.md carries the Autonomous-Edit Denylist itself
// and its own doc block calls it "protocol-adjacent and loop-immutable... edit it yourself,
// before you arm a run": if it were only Denylist-gated (or ungated, since it wouldn't match its
// own globs), a run could edit constraints.md to remove/loosen entries and then, next iteration,
// freely edit whatever it just unprotected — defeating the self-modification guard entirely.
// Case-sensitive by design.
func protocolProtected(rel string) bool {
	switch rel {
	case ".l00prite/heartbeat.json", ".l00prite/state.json", ".l00prite/lock.json", ".l00prite/constraints.md":
		return true
	}
	return rel == ".l00prite/prompts" || strings.HasPrefix(rel, ".l00prite/prompts/")
}

// ---- read_file ----

func (tb *Toolbox) readFile(args map[string]any) ToolOutcome {
	abs, err := tb.resolvePath(argString(args, "path"))
	if err != nil {
		return errResult(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return errResult(err)
	}
	if info.IsDir() {
		return ToolOutcome{Result: "ERROR: path is a directory; use list_dir"}
	}
	f, err := os.Open(abs)
	if err != nil {
		return errResult(err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, readCapBytes+1))
	if err != nil {
		return errResult(err)
	}
	byteTruncated := false
	if len(data) > readCapBytes {
		data = data[:readCapBytes]
		byteTruncated = true
	}
	text := string(data)

	offset, hasOffset := argInt(args, "offset_line")
	limit, hasLimit := argInt(args, "limit_lines")
	windowNote := ""
	if hasOffset || hasLimit {
		lines := strings.Split(text, "\n")
		total := len(lines)
		start := 0
		if hasOffset && offset > 1 {
			start = offset - 1
		}
		if start > total {
			start = total
		}
		end := total
		if hasLimit && limit >= 0 && start+limit < end {
			end = start + limit
		}
		text = strings.Join(lines[start:end], "\n")
		if start > 0 || end < total {
			windowNote = fmt.Sprintf("\n… [showing lines %d-%d of %d]", start+1, end, total)
		}
	}

	out := text + windowNote
	if byteTruncated {
		out += "\n… [truncated: file exceeds 256 KiB read cap]"
	}
	return ToolOutcome{Result: out}
}

// ---- write_file (layered write policy) ----

func (tb *Toolbox) writeFile(args map[string]any, approved bool) ToolOutcome {
	raw := argString(args, "path")
	abs, err := tb.resolvePath(raw)
	if err != nil {
		return errResult(err)
	}
	rel := policyRel(raw)

	// a. protocol-file hard-deny (not gateable, even with approved=true).
	if protocolProtected(rel) {
		return ToolOutcome{Result: fmt.Sprintf(
			"DENIED: %s is an engine-owned protocol file; it is never writable during a run (self-modification guard)", rel)}
	}

	// b. Autonomous-Edit Denylist -> destructive gate unless already approved.
	if hit, pattern := MatchDenylist(tb.Denylist, rel); hit && !approved {
		return ToolOutcome{
			Result: fmt.Sprintf("GATE: writing %s needs human approval (Autonomous-Edit Denylist match: %s)", rel, pattern),
			Gate: &GateRequest{
				Class:  GateDestructive,
				Action: fmt.Sprintf("write_file %q (Autonomous-Edit Denylist match: %s)", rel, pattern),
				Args:   args,
			},
		}
	}

	// c. write.
	content := argString(args, "content")
	if len(content) > writeCapBytes {
		return ToolOutcome{Result: fmt.Sprintf("ERROR: content is %d bytes; exceeds the 2 MiB write cap", len(content))}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return errResult(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return errResult(err)
	}
	return ToolOutcome{Result: fmt.Sprintf("wrote %s (%d bytes)", rel, len(content))}
}

// ---- list_dir ----

func (tb *Toolbox) listDir(args map[string]any) ToolOutcome {
	raw := argString(args, "path")
	if raw == "" {
		raw = "."
	}
	abs, err := tb.resolvePath(raw)
	if err != nil {
		return errResult(err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return errResult(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var lines []string
	count := 0
	capped := false
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if count >= listCapEntries {
			capped = true
			break
		}
		if e.IsDir() {
			lines = append(lines, e.Name()+"/")
		} else {
			size := int64(0)
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
			lines = append(lines, fmt.Sprintf("%s (%d bytes)", e.Name(), size))
		}
		count++
	}
	out := strings.Join(lines, "\n")
	if out == "" {
		out = "(empty directory)"
	}
	if capped {
		out += fmt.Sprintf("\n… [truncated: more than %d entries]", listCapEntries)
	}
	return ToolOutcome{Result: out}
}

// ---- search_files ----

func (tb *Toolbox) searchFiles(args map[string]any) ToolOutcome {
	query := argString(args, "query")
	if query == "" {
		return ToolOutcome{Result: "ERROR: query is required"}
	}
	maxResults := searchDefaultN
	if v, ok := argInt(args, "max_results"); ok && v > 0 {
		maxResults = v
	}
	if maxResults > searchMaxN {
		maxResults = searchMaxN
	}

	var re *regexp.Regexp
	if compiled, err := regexp.Compile(query); err == nil {
		re = compiled
	}
	matcher := func(line string) bool {
		if re != nil {
			return re.MatchString(line)
		}
		return strings.Contains(line, query)
	}

	var b strings.Builder
	count := 0
	truncated := false

	_ = filepath.WalkDir(tb.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		// A symlink is reported as a non-dir entry with its own type (WalkDir already doesn't
		// descend into a symlinked directory as if it were one). os.ReadFile below follows
		// symlinks like a normal open(), so without this check a symlink pointing outside Root
		// would let search_files read arbitrary host files that resolvePath's containment check
		// (used by read_file/write_file/list_dir) would have rejected.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > searchFileCap {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if !utf8.Valid(data) {
			return nil // binary file (image, archive, compiled artifact, ...) — never search/return raw bytes to the model
		}
		rel, err := filepath.Rel(tb.Root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		lineNo := 0
		for _, raw := range strings.Split(string(data), "\n") {
			lineNo++
			line := strings.TrimRight(raw, "\r")
			if !matcher(line) {
				continue
			}
			trimmed := line
			if utf8.RuneCountInString(trimmed) > 200 {
				trimmed = string([]rune(trimmed)[:200])
			}
			entry := fmt.Sprintf("%s:%d: %s\n", rel, lineNo, trimmed)
			if b.Len()+len(entry) > searchTotalCap {
				truncated = true
				return filepath.SkipAll
			}
			b.WriteString(entry)
			count++
			if count >= maxResults {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})

	if b.Len() == 0 {
		return ToolOutcome{Result: "(no matches)"}
	}
	out := b.String()
	if truncated {
		out += "… [truncated: result cap reached]\n"
	}
	return ToolOutcome{Result: out}
}

// ---- run_command ----

func (tb *Toolbox) runCommand(ctx context.Context, args map[string]any, approved bool) ToolOutcome {
	command := strings.TrimSpace(argString(args, "command"))
	if command == "" {
		return ToolOutcome{Result: "ERROR: run_command requires a non-empty command"}
	}
	// approved=true bypasses classification: the human approved this exact command.
	if !approved && !tb.commandAllowed(command) {
		return ToolOutcome{
			Result: fmt.Sprintf("GATE: %q is not on the command allowlist; human approval required before it runs", command),
			Gate: &GateRequest{
				Class:  classifyCommand(command),
				Action: fmt.Sprintf("run_command %q", command),
				Args:   args,
			},
		}
	}

	timeout := cmdDefaultTOSec
	if v, ok := argInt(args, "timeout_s"); ok && v > 0 {
		timeout = v
	}
	if timeout > cmdMaxTOSec {
		timeout = cmdMaxTOSec
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(cctx, "/bin/sh", "-c", command)
	}
	cmd.Dir = tb.Root
	// os.Environ() unchanged: this is the operator's own machine; provider keys are NOT in the
	// gateway process env by design — do not filter, do not add.
	cmd.Env = os.Environ()

	out, runErr := cmd.CombinedOutput()
	return ToolOutcome{Result: formatCmdResult(out, runErr, cctx, cmdOutputCap, timeout)}
}

// shellChainChars are the shell metacharacters that can chain, pipe, redirect, or substitute an
// extra command onto an allowlisted prefix. A prefix-extended command (one that starts with
// "<allowlisted-entry> ") is only honored when its APPENDED suffix contains none of them —
// otherwise "go test ./..." on the allowlist would let a run smuggle
// "go test ./... ; rm -rf /" straight to the shell, since it too starts with "go test ./... ".
// An EXACT match against the allowlist is always honored regardless of metacharacters: a human
// approved that literal string at pre-flight, compound command or not.
var shellChainChars = regexp.MustCompile("[;&|`$<>\n]")

// commandAllowed matches a command against the allowlist: exactly, or as an allowlisted prefix
// extended with additional plain arguments (no shell-chaining metacharacters in the extension).
func (tb *Toolbox) commandAllowed(command string) bool {
	c := strings.TrimSpace(command)
	for _, p := range tb.Allowlist {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if c == p {
			return true
		}
		if strings.HasPrefix(c, p+" ") && !shellChainChars.MatchString(c[len(p):]) {
			return true
		}
	}
	return false
}

// classifyCommand assigns a gate class to a non-allowlisted command.
func classifyCommand(command string) string {
	c := strings.TrimSpace(command)
	switch {
	case hasCmdPrefix(c, "git push --force"), hasCmdPrefix(c, "git push -f"):
		return GateDestructive
	case hasCmdPrefix(c, "git push"):
		return GatePush
	case hasCmdPrefix(c, "git merge"):
		return GateMerge
	case hasCmdPrefix(c, "git rebase"), hasCmdPrefix(c, "git reset"), hasCmdPrefix(c, "git clean"):
		return GateDestructive
	default:
		return GateDestructive
	}
}

func hasCmdPrefix(c, prefix string) bool {
	return c == prefix || strings.HasPrefix(c, prefix+" ")
}

// ---- git_command ----

func (tb *Toolbox) gitCommand(ctx context.Context, args map[string]any, approved bool) ToolOutcome {
	list, err := stringSlice(args["args"])
	if err != nil || len(list) == 0 {
		return ToolOutcome{Result: "ERROR: git_command requires a non-empty args array of strings"}
	}
	sub := list[0]
	// Never pass through -c/--exec-path style global flags.
	if strings.HasPrefix(sub, "-") {
		return ToolOutcome{Result: fmt.Sprintf(
			"ERROR: refusing git global flag %q as args[0]; pass a subcommand (status/diff/log/add/commit/show/branch)", sub)}
	}
	if !approved {
		gated := false
		switch sub {
		case "status", "diff", "log", "add", "commit", "show":
			// runs without approval
		case "branch":
			// A bare `branch` (list) or `branch <name>` (create one) touches nothing existing.
			// Any flag — -d/-D/-f/-m/-M/--delete/--force/--move, etc. — can delete, rename, or
			// force-move a ref, so it needs the same approval as any other destructive git op.
			if !gitBranchArgsAreSafe(list[1:]) {
				gated = true
			}
		default:
			gated = true
		}
		if gated {
			return ToolOutcome{
				Result: fmt.Sprintf("GATE: git %s requires human approval", sub),
				Gate: &GateRequest{
					Class:  classifyGitSub(sub),
					Action: fmt.Sprintf("git_command %v", list),
					Args:   args,
				},
			}
		}
	}

	cctx, cancel := context.WithTimeout(ctx, gitTimeoutSec*time.Second)
	defer cancel()
	full := append([]string{"-C", tb.Root}, list...)
	cmd := exec.CommandContext(cctx, "git", full...)
	cmd.Env = os.Environ()
	out, runErr := cmd.CombinedOutput()
	return ToolOutcome{Result: formatCmdResult(out, runErr, cctx, gitOutputCap, gitTimeoutSec)}
}

// gitBranchArgsAreSafe reports whether `git branch <rest...>` only lists (no args) or creates one
// new branch (a single bare name), the only forms that touch nothing already in the repo. Any
// flag at all — short or long, combined or not — routes to approval instead of being enumerated,
// so a new destructive branch flag never has to be added here to stay covered.
func gitBranchArgsAreSafe(rest []string) bool {
	switch len(rest) {
	case 0:
		return true
	case 1:
		return !strings.HasPrefix(rest[0], "-")
	default:
		return false
	}
}

func classifyGitSub(sub string) string {
	switch sub {
	case "push":
		return GatePush
	case "merge":
		return GateMerge
	default:
		// pull/fetch/rebase/reset/clean/remote/config/checkout/switch/unknown
		return GateDestructive
	}
}

// ---- engine-driven git helpers (NOT model tools; return Go errors) ----

// EnsureRunBranch verifies the repo has a commit and a clean worktree, then creates/moves to
// the run branch. Called by the engine before the first iteration.
func EnsureRunBranch(root, branch string) error {
	if _, err := runGit(root, "rev-parse", "--verify", "HEAD"); err != nil {
		return fmt.Errorf("repository has no commits")
	}
	out, err := runGit(root, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	// .l00prite/ is exempt: the pre-flight arms/scaffolds it, and it belongs on the run branch.
	// The clean-tree guard exists to protect the user's uncommitted work elsewhere.
	if dirty := dirtyPathsOutsideL00prite(out); len(dirty) > 0 {
		return fmt.Errorf("working tree is not clean")
	}
	if _, err := runGit(root, "checkout", "-B", branch); err != nil {
		return fmt.Errorf("git checkout -B %s failed: %w", branch, err)
	}
	return nil
}

// CommitUnit stages everything and commits with message. "nothing to commit" is not an error:
// it returns ("", nil). On a real commit it returns the new HEAD hash.
func CommitUnit(root, message string) (string, error) {
	if _, err := runGit(root, "add", "-A"); err != nil {
		return "", err
	}
	out, err := runGit(root, "commit", "-m", message)
	if err != nil {
		if strings.Contains(out, "nothing to commit") || strings.Contains(err.Error(), "nothing to commit") {
			return "", nil
		}
		return "", err
	}
	hash, err := runGit(root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(hash), nil
}

// CurrentDiff returns `git diff HEAD`, capped at maxBytes (used by the reviewer role).
func CurrentDiff(root string, maxBytes int) (string, error) {
	out, err := runGit(root, "diff", "HEAD")
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && len(out) > maxBytes {
		out = out[:maxBytes] + "\n… [truncated]"
	}
	return out, nil
}

// runGit runs `git -C root <args...>` with a bounded timeout. On failure it returns the
// combined output alongside an error that includes it (so callers can inspect both).
func runGit(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeoutSec*time.Second)
	defer cancel()
	full := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ---- shared helpers ----

// formatCmdResult renders a command result: an "exit_code: N" line first, an optional timeout
// note, the (capped) combined output, and a truncation note. A context deadline is reported as
// exit_code -1.
func formatCmdResult(out []byte, runErr error, cctx context.Context, maxBytes, timeoutSec int) string {
	truncated := false
	if len(out) > maxBytes {
		out = out[:maxBytes]
		truncated = true
	}
	timedOut := cctx.Err() == context.DeadlineExceeded
	exitCode := 0
	switch {
	case timedOut:
		exitCode = -1
	case runErr != nil:
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "exit_code: %d\n", exitCode)
	if timedOut {
		fmt.Fprintf(&b, "timed out after %ds\n", timeoutSec)
	}
	b.Write(out)
	if truncated {
		b.WriteString("\n… [truncated: output exceeds cap]")
	}
	return b.String()
}

func errResult(err error) ToolOutcome {
	return ToolOutcome{Result: "ERROR: " + err.Error()}
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// argInt reads an integer tool arg. JSON numbers decode to float64; test callers pass int.
func argInt(args map[string]any, key string) (int, bool) {
	switch n := args[key].(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

// stringSlice coerces a tool arg into []string, handling both []string (test) and []any (JSON).
func stringSlice(v any) ([]string, error) {
	switch s := v.(type) {
	case []string:
		return s, nil
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("non-string element in args array")
			}
			out = append(out, str)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("args must be an array of strings")
	}
}
