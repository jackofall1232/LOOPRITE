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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackofall1232/l00prite/cli-os/internal/gitx"
	"github.com/jackofall1232/l00prite/cli-os/internal/util"
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
	// Git selects the exec-git/go-git seam (internal/gitx) for the engine-driven git helpers and
	// the git_command tool's Kind() check. Nil defaults to gitx.Detect() (a fresh, cheap decision)
	// so a Toolbox built without wiring one — every existing test literal — keeps working unchanged.
	Git gitx.Client
	// PushCred resolves the GitHub credential (a *gitx.PushAuth) for the push_branch tool, decrypting
	// it at call time (never cached, so a dashboard disconnect takes effect immediately). Bound to
	// the run's project by whoever builds the Toolbox. Nil, or a nil return, means "no stored
	// credential" — push_branch then falls back to ambient credentials on the exec backend and
	// reports an honest capability gap on gogit. The engine package never imports the vault: this
	// closure is injected by the gateway (see Engine.PushCred / githubAuthFor).
	PushCred func() (*gitx.PushAuth, error)
	// PushRequested is set true when the coder calls (and gets approval for) push_branch. The actual
	// push is DEFERRED to after the engine commits the unit (see PerformPendingPush and
	// engine.iterate) — the coder loop runs before CommitUnit, so pushing inline would ship the
	// pre-unit HEAD and miss the verified change.
	PushRequested bool
}

// gitClient returns tb.Git, defaulting to gitx.Detect() when unset.
func (tb *Toolbox) gitClient() gitx.Client { return gitOrDetect(tb.Git) }

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
				"human approval. Paths inside args are repo-relative. To push this run's branch to origin, prefer "+
				"the dedicated push_branch tool — it works even on a host with no git binary and uses the "+
				"dashboard's GitHub connection.",
			objSchema(map[string]any{
				"args": mergeSchema(strArr, map[string]any{"description": "git argument vector; args[0] is the subcommand"}),
			}, "args")),

		fnTool("push_branch",
			"Push THIS run's branch to origin so a human can review it and open a pull request. Takes no "+
				"arguments — it always pushes exactly this run's own branch, never a force push, never another "+
				"branch or refspec. Requires per-action human approval unless the project's Auto-PR setting "+
				"pre-approved pushes. Uses the dashboard's GitHub connection when one exists, so it works even on "+
				"a device with no git binary; without a connection on such a device it reports that pushing is "+
				"unavailable.",
			objSchema(map[string]any{})),

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
	case "push_branch":
		return tb.pushBranch(ctx, approved)
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

var (
	shellPathOnce sync.Once
	shellPathVal  string
)

// shellPath resolves the POSIX shell run_command execs, once per process (Android G3 — see
// docs/android-architecture.md §4). Desktop always has /bin/sh, so LookPath("sh") finds it first
// and behavior there is unchanged; stock Android ships no /bin/sh at all — its shell lives at
// /system/bin/sh — so a hardcoded "/bin/sh" would make run_command unconditionally fail on-device.
// LookPath covers any host with sh anywhere on PATH; the two absolute-path checks cover a host
// with sh installed but not on PATH (Android's case, PATH notwithstanding).
func shellPath() string {
	shellPathOnce.Do(func() {
		if p, err := exec.LookPath("sh"); err == nil {
			shellPathVal = p
			return
		}
		if _, err := os.Stat("/bin/sh"); err == nil {
			shellPathVal = "/bin/sh"
			return
		}
		if _, err := os.Stat("/system/bin/sh"); err == nil {
			shellPathVal = "/system/bin/sh"
			return
		}
		shellPathVal = "/bin/sh" // last resort: matches pre-G3 behavior when nothing resolves
	})
	return shellPathVal
}

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
				Class:  classifyCommand(command, tb.Branch),
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
		cmd = exec.CommandContext(cctx, shellPath(), "-c", command)
	}
	cmd.Dir = tb.Root
	// Scrub the two secret env vars that must never reach a model-directed shell command (Android
	// G8 — see docs/android-architecture.md §4): LOOPRITE_MASTER_KEY (vault) and
	// LOOPRITE_SETUP_SECRET (first-run gate). Everything else in os.Environ() passes through
	// unfiltered — this is the operator's own machine, and provider keys are NOT in the gateway
	// process env by design, so there is nothing else here that needs protecting.
	cmd.Env = util.ScrubSecretEnv(os.Environ())

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

// classifyCommand assigns a gate class to a non-allowlisted command. branch is the run's own
// branch (tb.Branch; may be "" for a Toolbox built without one, e.g. most existing unit tests --
// see pushTargetsRunBranch's doc comment for why an empty branch skips its check rather than
// failing every push closed).
func classifyCommand(command, branch string) string {
	c := strings.TrimSpace(command)
	switch {
	case hasCmdPrefix(c, "git push --force"), hasCmdPrefix(c, "git push -f"):
		return GateDestructive
	case hasCmdPrefix(c, "git push"):
		// A force/delete/mirror/prune flag ANYWHERE in a git push -- not just as the immediate
		// prefix caught above -- must never reach GatePush: GatePush is the one class the
		// project Auto-PR toggle may set to auto_approve, and "git push origin main --force"
		// used to classify as plain GatePush (only an immediately-following --force/-f was
		// caught), which would have made a force-push auto-approvable. containsForcePushToken
		// closes that regardless of flag position.
		if containsForcePushToken(c) {
			return GateDestructive
		}
		// PR review (chatgpt-codex-connector): this case runs via `sh -c` (runCommand), same as
		// the gh-pr-create case below -- without this check, "git push origin HEAD; rm -rf
		// .l00prite" would classify as GatePush (auto-approvable) even though the shell would
		// execute the chained rm too. Must mirror the gh-pr-create case's guard exactly.
		if shellChainChars.MatchString(c) {
			return GateDestructive
		}
		// PR review (chatgpt-codex-connector): GatePush previously covered ANY non-force push --
		// "git push origin main" or "git push origin HEAD:main" would auto-approve just as
		// readily as a push of the run's own branch, letting an auto-approved run write directly
		// to an unrelated (possibly default/protected) branch instead of only ever pushing its
		// own branch for a human to open/review a PR against. Only a push that resolves to
		// exactly the run's own branch stays GatePush; anything else (including an unparseable or
		// multi-ref push) fails closed to GateDestructive.
		fields := strings.Fields(c)
		if !pushTargetsRunBranch(fields[2:], branch) { // fields[0]="git" fields[1]="push", guaranteed by hasCmdPrefix above
			return GateDestructive
		}
		return GatePush
	case hasCmdPrefix(c, "git merge"):
		return GateMerge
	case hasCmdPrefix(c, "git rebase"), hasCmdPrefix(c, "git reset"), hasCmdPrefix(c, "git clean"):
		return GateDestructive
	case hasCmdPrefix(c, "gh pr create"):
		// The only loosening classifyCommand grants: "gh pr create" (and only that exact prefix
		// -- hasCmdPrefix requires the next byte after the prefix to be a space or end-of-string,
		// so "gh pr view/merge/close", "gh repo create", "gh pr createx", and "gh  pr create"
		// (double space) all miss and fall through to the GateDestructive default below, never
		// the other way around). run_command executes via `sh -c` (see runCommand), so a shell
		// metacharacter anywhere in the command -- chaining, piping, redirection, or command
		// substitution inside a --body -- forces it back to GateDestructive: PolicyAutoApprove
		// can therefore only ever reach a single, plain "gh pr create ..." invocation with flags
		// in any order (--title/--body/--head/--base/--draft); a multiline --body needs
		// --body-file or human approval, the safe direction (enforced explicitly just below, not
		// just implied by this comment -- PR review, chatgpt-codex-connector: -F/--body-file and
		// -T/--template read an ARBITRARY LOCAL FILE on the gateway host, not necessarily inside
		// the repo, and paste its contents into a PUBLIC pull request; that must never be
		// reachable without a human looking at the command first).
		if shellChainChars.MatchString(c) {
			return GateDestructive
		}
		if containsFileReadingPRFlag(c) {
			return GateDestructive
		}
		// PR review (chatgpt-codex-connector): -R/--repo lets `gh pr create` target ANY
		// repository the gateway's gh token can reach, not necessarily this run's own --
		// an auto-approved run could otherwise spam or manipulate pull requests on an
		// entirely unrelated repo. Never auto-approvable.
		if containsCrossRepoPRFlag(c) {
			return GateDestructive
		}
		// PR review (chatgpt-codex-connector): without an explicit --head, "gh pr create"
		// falls back to inferring the head branch/repo from ambient git state, which this
		// classifier cannot confirm from the command text alone. Requiring the flag be
		// spelled out keeps auto-approval scoped to exactly what the command says, the
		// same "confirm from text or fail closed" discipline pushTargetsRunBranch uses.
		if !containsHeadFlag(c) {
			return GateDestructive
		}
		return GatePRCreate
	default:
		return GateDestructive
	}
}

func hasCmdPrefix(c, prefix string) bool {
	return c == prefix || strings.HasPrefix(c, prefix+" ")
}

// forcePushTokens: any of these appearing anywhere in a `git push` command's arguments makes it
// destructive, never auto-approvable. Matched whole-token (strings.Fields), so a branch literally
// named "my--force" can never false-positive off a substring match.
var forcePushTokens = map[string]bool{
	"--force": true, "-f": true, "--force-with-lease": true, "--force-if-includes": true,
	"--delete": true, "-d": true, "--mirror": true, "--prune": true,
	// --receive-pack/--exec (aliases of each other, per `git push -h`) tell git to invoke an
	// ARBITRARY PROGRAM on the remote side of the connection instead of git-receive-pack -- the
	// classic git-push-over-SSH command-execution vector (`git push --receive-pack='<cmd>' ...`
	// runs <cmd> via the SSH transport, not git-receive-pack). Far more severe than a force-push;
	// never auto-approvable (PR review, gemini-code-assist).
	"--receive-pack": true, "--exec": true,
}

// forcePushValueFlagPrefixes: --force-with-lease and --force-if-includes both accept an optional
// "=<value>" suffix (e.g. "--force-with-lease=main"), which is a single whitespace-free token and
// so never exact-matches forcePushTokens above -- a real safety bypass, since that let a
// value-qualified force-with-lease push slip through as auto-approvable GatePush instead of
// GateDestructive (caught in PR review; see isForcePushToken's regression tests).
var forcePushValueFlagPrefixes = []string{"--force-with-lease=", "--force-if-includes=", "--receive-pack=", "--exec="}

// isForcePushToken reports whether tok (one whitespace-separated field) is a force/delete/mirror/
// prune push flag, in either its bare or "=<value>"-qualified form.
func isForcePushToken(tok string) bool {
	if forcePushTokens[tok] {
		return true
	}
	for _, p := range forcePushValueFlagPrefixes {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	return false
}

// containsForcePushToken reports whether any whitespace-separated token of c is a force/delete/
// mirror/prune push flag, wherever it appears in the command string.
func containsForcePushToken(c string) bool {
	for _, tok := range strings.Fields(c) {
		if isForcePushToken(tok) {
			return true
		}
	}
	return false
}

// pushTargetsRunBranch reports whether a "git push" (args is everything after the "push"
// subcommand itself -- flags and positional args both) can be confidently determined, from the
// command text alone, to write ONLY to the run's own branch on "origin" -- the sole remote/
// destination every push this codebase itself generates ever uses (EnsureRunBranch,
// scaffold_pr.go's openScaffoldPR). GatePush is the one class the project Auto-PR toggle may
// auto-approve with zero human review, so this must fail closed (false, meaning "gate it, don't
// auto-approve") on anything it cannot confidently parse -- a bare "git push" with no explicit
// destination, an unrecognized remote, a multi-ref push, or a refspec targeting any branch other
// than this run's own. branch=="" (a Toolbox built without one, e.g. most pre-existing unit
// tests, or a code path that genuinely has no run branch to compare against) skips this check
// entirely and returns true, preserving the exact pre-existing behavior for those callers rather
// than failing every push closed for them (PR review, chatgpt-codex-connector).
func pushTargetsRunBranch(args []string, branch string) bool {
	if branch == "" {
		return true
	}
	var positional []string
	for _, tok := range args {
		if strings.HasPrefix(tok, "-") {
			continue // flags never name a destination; force/delete/mirror/prune are already
			// rejected by the caller before this runs, so anything left here is inert for our
			// purposes (e.g. -u/--set-upstream/-q/--verbose).
		}
		positional = append(positional, tok)
	}
	if len(positional) != 2 {
		// 0 or 1: no explicit destination visible in the text to confirm against. 3+: more than
		// one ref/refspec in a single push, so at least one of them could target a different
		// branch. Both fail closed.
		return false
	}
	remote, ref := positional[0], positional[1]
	if remote != "origin" {
		return false
	}
	dst := ref
	if i := strings.IndexByte(ref, ':'); i >= 0 {
		// An explicit refspec "src:dst" -- what matters is the DESTINATION actually written on
		// the remote; src just says what local content feeds it, which is no more of an
		// escalation than the run's own already-ungated commits to its own branch. An empty dst
		// (e.g. "somebranch:", not a real git refspec form but harmless either way) is rejected
		// by the branch-name comparison below, since "" can never equal a real branch name.
		//
		// Two SRC forms are NOT harmless, though, and containsForcePushToken never sees them
		// because they're refspec syntax embedded in one token, not a separate --force/--delete
		// flag (PR review, chatgpt-codex-connector): an empty src ("origin :branch") is git's
		// refspec form for deleting the remote ref -- exactly as destructive as --delete -- and a
		// "+"-prefixed src ("origin +HEAD:branch") is refspec syntax for a force push -- exactly
		// as destructive as --force. Both must fail closed here.
		src := ref[:i]
		if src == "" || strings.HasPrefix(src, "+") {
			return false
		}
		dst = ref[i+1:]
	} else if strings.HasPrefix(ref, "+") {
		// A bare "+HEAD" (no colon) is the same force-push refspec syntax with an implicit
		// same-named destination -- still not visible to containsForcePushToken.
		return false
	} else if ref == "HEAD" {
		// "git push origin HEAD" pushes the CURRENTLY CHECKED-OUT commit to a remote branch of
		// the SAME NAME -- git's own special-cased convention for this bare source. Safe here
		// because switching the checked-out branch mid-run is itself not on the ungated
		// status/diff/log/add/commit/show/branch(list) allowlist (see gitCommand's switch above)
		// -- it already requires its own separate human approval, so by the time an auto-approved
		// push runs, the checked-out branch is still whatever EnsureRunBranch set it to: branch.
		return true
	}
	return strings.TrimPrefix(dst, "refs/heads/") == branch
}

// fileReadingPRFlagTokens/-Prefixes: gh pr create flags that read an arbitrary LOCAL file (not
// necessarily inside the repo -- gh reads it directly off the gateway host's filesystem) and use
// its content as PR text. -F/--body-file and -T/--template both do this per the gh CLI manual.
// gh's shorthand flags (-F, -T) are pflag-based and accept their value three ways: "-F file" (a
// separate token, still caught below since containsFileReadingPRFlag only needs to see the flag
// token itself), "-F=file", or attached with NO separator at all, "-Ffile" -- the last form is
// caught only by matching the bare "-F"/"-T" prefix (PR review, chatgpt-codex-connector: matching
// only the "=<value>" form left the attached-no-separator form unguarded). Long flags don't
// support attachment, so --body-file/--template still need their own "=" prefix and exact-token
// entries. Never auto-approvable: this is how a run could exfiltrate an SSH key or any other host
// file the gateway process can read into a PUBLIC pull request (PR review finding,
// chatgpt-codex-connector).
var fileReadingPRFlagTokens = map[string]bool{"--body-file": true, "--template": true}
var fileReadingPRFlagPrefixes = []string{"-F", "-T", "--body-file=", "--template="}

// containsFileReadingPRFlag reports whether any whitespace-separated token of c is a gh pr create
// flag that reads a local file, in any of its bare/attached/"=<value>" forms.
func containsFileReadingPRFlag(c string) bool {
	for _, tok := range strings.Fields(c) {
		if fileReadingPRFlagTokens[tok] {
			return true
		}
		for _, p := range fileReadingPRFlagPrefixes {
			if strings.HasPrefix(tok, p) {
				return true
			}
		}
	}
	return false
}

// crossRepoPRFlagPrefixes: gh pr create's -R/--repo targets an ARBITRARY repository, not
// necessarily this run's own -- letting an auto-approved run create pull requests against any
// repo the gateway's gh token can reach. Matched by prefix, the same attached-shorthand
// discipline as fileReadingPRFlagPrefixes above, so "-Rowner/repo" is caught alongside
// "-R owner/repo"/"-R=owner/repo"/"--repo=owner/repo" (PR review, chatgpt-codex-connector).
var crossRepoPRFlagPrefixes = []string{"-R", "--repo"}

func containsCrossRepoPRFlag(c string) bool {
	for _, tok := range strings.Fields(c) {
		for _, p := range crossRepoPRFlagPrefixes {
			if strings.HasPrefix(tok, p) {
				return true
			}
		}
	}
	return false
}

// containsHeadFlag reports whether c spells out gh pr create's --head explicitly. Required for
// GatePRCreate (PR review, chatgpt-codex-connector): without it, gh infers the head branch/repo
// from ambient git state this classifier cannot see from the command text alone.
func containsHeadFlag(c string) bool {
	for _, tok := range strings.Fields(c) {
		if tok == "--head" || strings.HasPrefix(tok, "--head=") {
			return true
		}
	}
	return false
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
	// git_command is arbitrary passthrough, which only the exec-git implementation can offer — the
	// pure-Go go-git fallback (Android, no git binary; see internal/gitx) implements the specific
	// primitives EnsureRunBranch/CommitUnit/CurrentDiff need, not an arbitrary subcommand. Under
	// this Kind(), gitCommandGogit serves a conservative, exact-match read-only subset of
	// status/diff/log/show instead of refusing everything outright; anything outside that subset
	// still falls to the plain ERROR below (not a Gate: no human approval can supply a git binary
	// that isn't there). The subset itself never gates — reached here, before the approval switch
	// below is ever consulted, exactly like status/diff/log/show already run gate-free on the exec
	// path (see that switch's case a few lines down).
	if tb.gitClient().Kind() != "exec" {
		if out, ok := tb.gitCommandGogit(sub, list); ok {
			return out
		}
		return ToolOutcome{Result: "ERROR: git passthrough requires the git binary on this host; " +
			"the read-only subset (status, diff, log, show) still works in its basic argument forms " +
			"via the built-in pure-Go git"}
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
					Class:  classifyGitSub(sub, list[1:], tb.Branch),
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
	cmd.Env = util.ScrubSecretEnv(os.Environ()) // Android G8: never leak the vault/setup secrets
	out, runErr := cmd.CombinedOutput()
	return ToolOutcome{Result: formatCmdResult(out, runErr, cctx, gitOutputCap, gitTimeoutSec)}
}

// pushBranch pushes THIS run's branch to origin. It is structurally pinned — origin, the run's own
// branch, never a force push, no arguments to parse — so unlike a model-composed `git push` there
// is nothing for a classifier to get wrong; it is strictly stronger than string-analyzing a push
// command, and it works identically on the gogit backend where `git_command` passthrough can't
// exist. Order: resolve the credential and check capability FIRST — a host with no git binary AND
// no GitHub connection genuinely cannot push, so return a plain capability gap (not a gate no human
// approval could satisfy) the model can adapt to or escalate via unit_blocked(missing_credentials).
// Otherwise gate on GatePush exactly like every consequential action (human approval, or the
// project's Auto-PR pre-approval via the unmodified awaitApproval path), and push only once
// approved. The stored token is spent ONLY here and in the scaffold-PR path — never injected into a
// model-composed git command.
func (tb *Toolbox) pushBranch(ctx context.Context, approved bool) ToolOutcome {
	if strings.TrimSpace(tb.Branch) == "" {
		return ToolOutcome{Result: "ERROR: this run has no branch set, so there is nothing to push"}
	}
	// The credential carries its own host (PushAuth.Host); gitx.Push attaches it ONLY to an https
	// remote on that exact host, so passing it (later, in PerformPendingPush) can never leak the
	// token to a run whose origin is a non-GitHub (or ssh) remote — it silently falls back to ambient
	// credentials there. No origin check is needed here; the seam enforces host-scoping.
	auth, err := tb.resolvePushAuth()
	if err != nil {
		return ToolOutcome{Result: "ERROR: could not load the GitHub credential from the vault: " + err.Error()}
	}
	if tb.gitClient().Kind() != "exec" && auth == nil {
		return ToolOutcome{Result: "This device has no git binary and no GitHub connection, so the branch cannot be pushed. " +
			"Connect GitHub in the dashboard to enable pushing, then retry — or call unit_blocked with kind=missing_credentials."}
	}
	if !approved {
		return ToolOutcome{
			Result: "GATE: pushing the run branch to origin requires human approval",
			Gate: &GateRequest{
				Class:  GatePush,
				Action: fmt.Sprintf("push_branch origin %s", tb.Branch),
				Args:   map[string]any{"remote": "origin", "branch": tb.Branch},
			},
		}
	}
	// Approved. DEFER the actual push until AFTER the engine commits this unit (engine.iterate): the
	// coder loop runs before CommitUnit, so pushing now would ship the pre-unit HEAD and miss the
	// verified change (and committing here would empty the reviewer's diff). Record the intent.
	tb.PushRequested = true
	return ToolOutcome{Result: fmt.Sprintf(`{"status":"push_scheduled","remote":"origin","branch":%q,"note":"the branch will be pushed to origin after this unit's changes are committed and verified"}`, tb.Branch)}
}

// resolvePushAuth loads the push credential (nil when none is wired or stored) — shared by
// pushBranch's capability check and PerformPendingPush.
func (tb *Toolbox) resolvePushAuth() (*gitx.PushAuth, error) {
	if tb.PushCred == nil {
		return nil, nil
	}
	return tb.PushCred()
}

// PerformPendingPush executes a push that push_branch scheduled (PushRequested), called by the
// engine AFTER it commits the unit so origin receives the committed work rather than the pre-unit
// HEAD. Returns (false, nil) when no push was requested. The credential is resolved at THIS call
// time (honoring a mid-run disconnect), and host-scoped by gitx.Push.
func (tb *Toolbox) PerformPendingPush(ctx context.Context) (bool, error) {
	if !tb.PushRequested {
		return false, nil
	}
	auth, err := tb.resolvePushAuth()
	if err != nil {
		return false, err
	}
	if err := tb.gitClient().Push(ctx, tb.Root, "origin", tb.Branch, auth); err != nil {
		return false, err
	}
	return true, nil
}

// logNArg matches git log's bare "-<N>" shorthand for "-n <N>" (e.g. "-5"): a leading dash
// followed by digits with no leading zero, so a malformed count ("-0", "-05") never silently
// matches and instead falls through to the hard refusal below.
var logNArg = regexp.MustCompile(`^-[1-9][0-9]*$`)

// gitCommandGogit serves the conservative, exact-match read-only subset of git_command that the
// pure-Go go-git fallback (Kind()=="gogit") can offer without a git binary: status (no extra
// args), diff / diff HEAD, log in its bare/-n N/--max-count=N/-N shorthand forms, and show <ref>
// (exactly one ref, no flags). ok is false for anything outside this exact contract — an
// unrecognized flag or extra argument is never interpreted as "probably fine"; the caller falls
// through to the existing hard refusal instead. None of these gate: they are strictly read-only,
// the same reason status/diff/log/show already run without approval on the exec path (the switch
// a few lines above this function in gitCommand) — this function is called, and returns directly,
// before that switch is ever reached, so the no-approval property holds here too.
func (tb *Toolbox) gitCommandGogit(sub string, list []string) (ToolOutcome, bool) {
	rest := list[1:]
	switch {
	case sub == "status" && len(rest) == 0:
		out, err := tb.gitClient().StatusPorcelain(tb.Root)
		if err != nil {
			return errResult(err), true
		}
		return ToolOutcome{Result: formatGitResult(out)}, true

	// Bare `diff` is intentionally serviced here as `diff HEAD` (gitClient().DiffHead runs
	// `git diff HEAD` under the exec client, i.e. working-tree-vs-HEAD, which is a superset of
	// plain `git diff`'s working-tree-vs-index and additionally includes staged changes). That is
	// a deliberate divergence from what bare `git diff` means on the exec-git backend a few lines
	// above in gitCommand: gitx.Client has no working-tree-vs-index primitive to offer, and
	// answering with the (truthful, merely broader) HEAD-relative diff beats refusing outright.
	// A model relying on git_command for exact index-vs-worktree staging state will see different
	// output between an exec-git host and this gogit host for the identical `diff` call.
	case sub == "diff" && (len(rest) == 0 || (len(rest) == 1 && rest[0] == "HEAD")):
		out, err := tb.gitClient().DiffHead(tb.Root)
		if err != nil {
			return errResult(err), true
		}
		return ToolOutcome{Result: formatGitResult(out)}, true

	case sub == "log":
		n, ok := gitLogLimitArgs(rest)
		if !ok {
			return ToolOutcome{}, false
		}
		out, err := tb.gitClient().Log(tb.Root, n)
		if err != nil {
			return errResult(err), true
		}
		return ToolOutcome{Result: formatGitResult(out)}, true

	case sub == "show" && len(rest) == 1 && !strings.HasPrefix(rest[0], "-"):
		out, err := tb.gitClient().Show(tb.Root, rest[0])
		if err != nil {
			return errResult(err), true
		}
		return ToolOutcome{Result: formatGitResult(out)}, true

	default:
		return ToolOutcome{}, false
	}
}

// gitLogLimitArgs reports the commit limit for one of the four accepted `git log` argument
// shapes: bare (rest empty -> 0, so gitx.Client.Log applies its own default), "-n <positive int>",
// "--max-count=<positive int>", or the bare "-<positive int>" shorthand. ok is false for anything
// else, including a non-positive or unparseable count — the caller falls through to the hard
// refusal rather than guessing what was meant.
func gitLogLimitArgs(rest []string) (limit int, ok bool) {
	switch {
	case len(rest) == 0:
		return 0, true
	case len(rest) == 2 && rest[0] == "-n":
		n, err := strconv.Atoi(rest[1])
		return n, err == nil && n > 0
	case len(rest) == 1 && strings.HasPrefix(rest[0], "--max-count="):
		n, err := strconv.Atoi(strings.TrimPrefix(rest[0], "--max-count="))
		return n, err == nil && n > 0
	case len(rest) == 1 && logNArg.MatchString(rest[0]):
		n, err := strconv.Atoi(strings.TrimPrefix(rest[0], "-"))
		return n, err == nil && n > 0
	default:
		return 0, false
	}
}

// formatGitResult renders a gitx-served (gogit) git_command result the same way formatCmdResult
// renders an exec-git one — an "exit_code: 0" line first (there is no real child-process exit
// code here, but the model-facing shape stays identical either way so the model sees one
// consistent format regardless of which gitx.Client implementation served the call), then the
// (capped) output, using the same gitOutputCap and the same truncation note text.
func formatGitResult(out string) string {
	truncated := false
	if len(out) > gitOutputCap {
		out = out[:gitOutputCap]
		truncated = true
	}
	var b strings.Builder
	b.WriteString("exit_code: 0\n")
	b.WriteString(out)
	if truncated {
		b.WriteString("\n… [truncated: output exceeds cap]")
	}
	return b.String()
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

// classifyGitSub assigns a gate class to a gated git_command subcommand. rest is the
// subcommand's own argument list (list[1:] at the call site) so a push carrying a force/delete/
// mirror/prune flag -- e.g. git_command ["push","--force"] or ["push","origin","--delete","x"] --
// is caught here exactly as classifyCommand's containsForcePushToken catches it for the
// run_command/sh path: GatePush is the one class the project Auto-PR toggle may set to
// auto_approve, so this must never read GatePush for a push that can rewrite or delete history.
// branch is the run's own branch (tb.Branch at the call site; see pushTargetsRunBranch). No
// shell-metacharacter check is needed here (unlike classifyCommand's git-push case): git_command
// execs git directly with an argument vector, never a shell, so a token like ";" or "$(...)" is
// just an inert literal argument, not something a shell could interpret.
func classifyGitSub(sub string, rest []string, branch string) string {
	switch sub {
	case "push":
		for _, tok := range rest {
			if isForcePushToken(tok) {
				return GateDestructive
			}
		}
		// PR review (chatgpt-codex-connector): same "restrict to the run's own branch" fix as
		// classifyCommand's git-push case -- git_command ["push","origin","main"] previously
		// auto-approved just as readily as pushing the run's own branch.
		if !pushTargetsRunBranch(rest, branch) {
			return GateDestructive
		}
		return GatePush
	case "merge":
		return GateMerge
	default:
		// pull/fetch/rebase/reset/clean/remote/config/checkout/switch/unknown
		return GateDestructive
	}
}

// ---- engine-driven git helpers (NOT model tools; return Go errors) ----
//
// These go through a gitx.Client (internal/gitx) rather than shelling out to "git" directly, so
// they keep working when no git binary is on the host (Android — docs/android-architecture.md
// §4 G4) via the pure-Go go-git fallback. Every call site in this codebase passes the engine's own
// gitx.Client (Engine.Git); a nil client (as every pre-G4 test literal still passes implicitly by
// omission) defaults to gitx.Detect() so nothing that called these before G4 existed had to change
// its behavior, only its signature.

// gitOrDetect returns git, defaulting to gitx.Detect() when nil.
func gitOrDetect(git gitx.Client) gitx.Client {
	if git == nil {
		return gitx.Detect()
	}
	return git
}

// EnsureRunBranch verifies the repo has a commit and a clean worktree, then creates/moves to
// the run branch. Called by the engine before the first iteration.
func EnsureRunBranch(git gitx.Client, root, branch string) error {
	git = gitOrDetect(git)
	if _, err := git.RevParseHead(root); err != nil {
		return fmt.Errorf("repository has no commits")
	}
	out, err := git.StatusPorcelain(root)
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	// .l00prite/ is exempt: the pre-flight arms/scaffolds it, and it belongs on the run branch.
	// The clean-tree guard exists to protect the user's uncommitted work elsewhere.
	if dirty := dirtyPathsOutsideL00prite(out); len(dirty) > 0 {
		return fmt.Errorf("working tree is not clean")
	}
	if err := git.CheckoutNewBranch(root, branch); err != nil {
		return fmt.Errorf("git checkout -B %s failed: %w", branch, err)
	}
	return nil
}

// CommitUnit stages everything and commits with message. "nothing to commit" is not an error:
// it returns ("", nil) (both gitx implementations detect this themselves — see internal/gitx). On
// a real commit it returns the new HEAD hash.
func CommitUnit(git gitx.Client, root, message string) (string, error) {
	git = gitOrDetect(git)
	if err := git.AddAll(root); err != nil {
		return "", err
	}
	hash, err := git.Commit(root, message)
	if err != nil {
		// A commit failure for any reason other than "nothing to commit" or a missing identity
		// (both already handled inside Commit itself) leaves AddAll's staging in place with no
		// commit to show for it — a surprising git state for whoever looks at this repo next (e.g. a
		// hook rejection, a full disk). Best-effort undo it via a plain `git reset` so a failed
		// checkpoint/unit commit doesn't also leave the index dirtied in a way the working tree
		// itself never was. Only meaningful under the exec backend: gogit's Raw is unconditionally
		// unsupported (see gitx.Client's doc comment), so this is a silent no-op there rather than a
		// gap this call can close without a broader Client interface change. The reset's own error is
		// deliberately ignored — a failed best-effort cleanup must never mask the real commit error.
		cctx, cancel := context.WithTimeout(context.Background(), gitTimeoutSec*time.Second)
		_, _ = git.Raw(cctx, root, "reset")
		cancel()
		return "", err
	}
	return hash, nil
}

// AutoCheckpoint commits ALL dirty paths — the user's own uncommitted work and .l00prite/ alike —
// as a WIP checkpoint before the run branch is created. Including .l00prite/ matters: the gogit
// backend's checkout goes through go-git's whole-tree, non-scopable dirty check (see
// EnsureRunBranch's doc comment), so leaving any tracked .l00prite/ file dirty (e.g. right after
// StartRun's own AcquireLock call rewrites lock.json) still trips it. This is deliberately a
// commit, never a stash: a stash is invisible and easy to orphan for a non-technical user, while a
// commit is durable, shows up in the project's normal history, and is exactly what the run's own
// ledger entry can point to. Returns ("", nil) on an already-clean tree (both gitx backends' Commit
// treats "nothing to commit" as success, and CommitUnit passes that through here).
//
// denylist gates this: a dirty path OUTSIDE .l00prite/ that matches the project's own
// Autonomous-Edit Denylist, or looks like it may hold credentials (isSecretLikePath), is refused
// with ErrCheckpointRefused and NOTHING is committed — this is the actual point of mutation, so
// the check has to live here, not just in the pre-flight display (BuildPreflight's checkGitReady
// advises the same thing earlier, but a file dirtied between pre-flight and Start would slip past
// an advisory-only check). .l00prite/ itself is never subject to this gate: it is the engine's own
// bookkeeping, not user content, and go-git's checkout requires it to be committed regardless.
func AutoCheckpoint(git gitx.Client, root, runID string, denylist []string) (string, error) {
	git = gitOrDetect(git)
	out, err := git.StatusPorcelain(root)
	if err != nil {
		return "", fmt.Errorf("could not inspect the project's current state: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	for _, rel := range dirtyPathsOutsideL00prite(out) {
		if hit, pattern := MatchDenylist(denylist, rel); hit {
			return "", fmt.Errorf("%w: %q matches your Autonomous-Edit Denylist (%s)", ErrCheckpointRefused, rel, pattern)
		}
		if isSecretLikePath(rel) {
			return "", fmt.Errorf("%w: %q looks like it may contain credentials or a key", ErrCheckpointRefused, rel)
		}
	}
	return CommitUnit(git, root, "WIP: auto-checkpoint before run-"+runID)
}

// CurrentDiff returns a diff of the worktree against HEAD, capped at maxBytes (used by the
// reviewer role). Under the go-git fallback this may be a file-status summary rather than a
// hunk-level unified diff — see gitx.Client.DiffHead's doc comment.
func CurrentDiff(git gitx.Client, root string, maxBytes int) (string, error) {
	git = gitOrDetect(git)
	out, err := git.DiffHead(root)
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && len(out) > maxBytes {
		out = out[:maxBytes] + "\n… [truncated]"
	}
	return out, nil
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
