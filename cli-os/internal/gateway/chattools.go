// Read-only repo browsing for ordinary chat (Bug 1 of the 2026-07-11 control-plane diagnosis:
// "can you see the repo?" -> the model had no file-access tool at all, only a static 5-file
// .l00prite/ memory digest that no-ops for any repo that hasn't been through a Run yet). This is a
// DELIBERATELY narrower, self-contained construct, not the engine's Toolbox (internal/engine/tools.go):
// it exposes exactly three tools -- read_file, list_dir, search_files -- and nothing that mutates
// the filesystem, runs a command, or touches git. The engine Toolbox's identity is "the governed
// Run tool surface" (denylist/allowlist/per-action approval); sharing that struct here would invite
// a future engine tool addition to silently become reachable from ungoverned chat. Path containment
// mirrors internal/memory.within() and internal/engine/tools.go's resolvePath (symlink-safe, fails
// closed) by design, not by shared code.
package gateway

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	chatReadCapBytes   = 32 * 1024 // read_file cap -- smaller than the engine's 256 KiB: this is
	// chat context budget, not a coding agent's per-unit work budget.
	chatListCapEntries  = 200
	chatSearchFileCap   = 512 * 1024 // skip searching any single file bigger than this
	chatSearchTotalCap  = 8 * 1024  // total bytes returned across all search matches
	chatSearchDefaultN  = 30
	chatSearchMaxN      = 100
	chatMaxToolRounds   = 6 // bounds the tool-call loop in runChatTools (loop.go)
	chatMaxToolCallsRun = 24
)

// chatSecretDeny is a fixed set of filename/path patterns that read_file and search_files refuse
// to return content for, regardless of the repo owner's own .gitignore. This is a conservative
// default deny-list for common credential/key file shapes; it is deliberately not configurable
// from chat. Patterns are matched against both the base name and the full repo-relative
// (forward-slash) path via path.Match-style globs.
var chatSecretDenyPatterns = []string{
	".env", ".env.*",
	"*.pem", "*.key", "*.p12", "*.pfx",
	"id_rsa", "id_rsa.*", "id_ed25519", "id_ed25519.*", "id_dsa", "id_dsa.*", "id_ecdsa", "id_ecdsa.*",
	".netrc", ".npmrc",
	".git/config", ".git/credentials",
	"*.keystore", "*.jks",
}

func isChatSecretPath(rel string) bool {
	base := filepath.Base(rel)
	for _, pat := range chatSecretDenyPatterns {
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, rel); ok {
			return true
		}
	}
	return false
}

// chatToolbox is a per-request, read-only file browser jailed to one registered repo's root.
type chatToolbox struct {
	Root string
}

// chatToolDefinitions returns the OpenAI-compatible function-tool schemas for the three read-only
// tools. Attached to a chat completion request only when it names a registered repo (see loop.go).
func chatToolDefinitions() []any {
	return []any{
		chatFnTool("read_file", "Read a text file from the linked repository (read-only; you cannot write or run anything).", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "Repository-relative file path, e.g. \"src/main.go\"."},
				"offset_line": map[string]any{"type": "integer", "description": "Optional: first line to return (1-based)."},
				"limit_lines": map[string]any{"type": "integer", "description": "Optional: maximum number of lines to return."},
			},
			"required": []any{"path"},
		}),
		chatFnTool("list_dir", "List files and subdirectories at a path in the linked repository (read-only).", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Repository-relative directory path; omit or use \".\" for the repository root."},
			},
		}),
		chatFnTool("search_files", "Search the linked repository's text files for a substring or regular expression, returning matching lines with file:line references (read-only).", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "description": "Substring or RE2 regular expression to search for."},
				"max_results": map[string]any{"type": "integer", "description": "Optional cap on the number of matching lines returned (default 30, max 100)."},
			},
			"required": []any{"query"},
		}),
	}
}

func chatFnTool(name, desc string, params map[string]any) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": name, "description": desc, "parameters": params,
		},
	}
}

var chatToolNames = map[string]bool{"read_file": true, "list_dir": true, "search_files": true}

// resolvePath validates a repo-relative path and returns the absolute path inside Root, failing
// closed on anything that would escape it (absolute paths, "..", or a symlinked ancestor that
// resolves outside Root) -- the same discipline internal/memory.within() and the engine's Toolbox
// apply, reimplemented here so this read-only surface has no code-level dependency on the
// Run-governed tool surface.
func (c chatToolbox) resolvePath(rel string) (string, error) {
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
	abs := filepath.Join(c.Root, cleaned)

	rootResolved, err := filepath.EvalSymlinks(c.Root)
	if err != nil {
		return "", fmt.Errorf("path escapes the repository")
	}
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

func (c chatToolbox) relOf(abs string) string {
	rel, err := filepath.Rel(c.Root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// Execute dispatches one chat tool call by name. Only ever called for names in chatToolNames.
func (c chatToolbox) Execute(name string, args map[string]any) string {
	switch name {
	case "read_file":
		return c.readFile(args)
	case "list_dir":
		return c.listDir(args)
	case "search_files":
		return c.searchFiles(args)
	default:
		return fmt.Sprintf("ERROR: unknown tool %q", name)
	}
}

func (c chatToolbox) readFile(args map[string]any) string {
	raw := chatArgString(args, "path")
	abs, err := c.resolvePath(raw)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	if isChatSecretPath(filepath.ToSlash(filepath.Clean(raw))) {
		return "DENIED: this path looks like a credential or key file; it is never readable from chat"
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	if info.IsDir() {
		return "ERROR: path is a directory; use list_dir"
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	if !utf8.Valid(data) {
		return "ERROR: binary file; only text files can be read from chat"
	}
	byteTruncated := false
	if len(data) > chatReadCapBytes {
		data = data[:chatReadCapBytes]
		byteTruncated = true
	}
	text := string(data)

	offset, hasOffset := chatArgInt(args, "offset_line")
	limit, hasLimit := chatArgInt(args, "limit_lines")
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
		out += fmt.Sprintf("\n… [truncated: file exceeds the %d KiB chat read cap]", chatReadCapBytes/1024)
	}
	return out
}

func (c chatToolbox) listDir(args map[string]any) string {
	raw := chatArgString(args, "path")
	if raw == "" {
		raw = "."
	}
	abs, err := c.resolvePath(raw)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var lines []string
	count, capped := 0, false
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if count >= chatListCapEntries {
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
		out += fmt.Sprintf("\n… [truncated: more than %d entries]", chatListCapEntries)
	}
	return out
}

func (c chatToolbox) searchFiles(args map[string]any) string {
	query := chatArgString(args, "query")
	if query == "" {
		return "ERROR: query is required"
	}
	maxResults := chatSearchDefaultN
	if v, ok := chatArgInt(args, "max_results"); ok && v > 0 {
		maxResults = v
	}
	if maxResults > chatSearchMaxN {
		maxResults = chatSearchMaxN
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
	count, truncated := 0, false

	_ = filepath.WalkDir(c.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		// A symlinked file is reported as a non-dir entry; os.ReadFile follows it like a normal
		// open(), so without this check search_files could read arbitrary host files that
		// resolvePath's containment check (used by read_file/list_dir) would have rejected.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > chatSearchFileCap {
			return nil
		}
		rel := c.relOf(p)
		if isChatSecretPath(rel) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || !utf8.Valid(data) {
			return nil
		}
		lineNo := 0
		for _, rawLine := range strings.Split(string(data), "\n") {
			lineNo++
			line := strings.TrimRight(rawLine, "\r")
			if !matcher(line) {
				continue
			}
			trimmed := line
			if utf8.RuneCountInString(trimmed) > 200 {
				trimmed = string([]rune(trimmed)[:200])
			}
			entry := fmt.Sprintf("%s:%d: %s\n", rel, lineNo, trimmed)
			if b.Len()+len(entry) > chatSearchTotalCap {
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
		return "(no matches)"
	}
	out := b.String()
	if truncated {
		out += "… [truncated: result cap reached]\n"
	}
	return out
}

// ---- tiny arg helpers (mirrors engine/tools.go's argString/argInt; kept local, no cross-package
// dependency for this deliberately self-contained surface) ----

func chatArgString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	s, _ := args[key].(string)
	return s
}

func chatArgInt(args map[string]any, key string) (int, bool) {
	if args == nil {
		return 0, false
	}
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}
