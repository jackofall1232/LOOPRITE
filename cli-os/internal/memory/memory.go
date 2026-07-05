// Package memory is Repo Memory (Track 2). It answers a MemoryQuery with a MemoryContext of ranked
// blocks selected from a repo's .l00prite/ files, within a token budget, with mtime-based freshness
// and graceful degradation. It returns BLOCKS, never a finished prompt — the gateway owns injection
// and the untrusted-content envelope. File access is confined to the repo root (symlink-resolving
// containment check). Ported from memory.js.
package memory

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackofall1232/l00prite/cli-os/internal/util"
)

type source struct {
	file string
	kind string
	base float64
}

var sources = []source{
	{"constraints.md", "constraint", 0.9},
	{"blueprint.md", "architecture", 0.85},
	{"memory.md", "architecture", 0.8},
	{"failures.md", "open_issue", 0.6},
	{"todos.md", "open_issue", 0.55},
}

// Digest is the minimal safe projection of the request the memory layer ranks against.
type Digest struct {
	UserIntent      string
	ReferencedPaths []string
	RecentToolCalls []string
}

// Freshness records a block's source mtime and staleness.
type Freshness struct {
	AsOf  string `json:"as_of"`
	Stale bool   `json:"stale"`
}

// Block is one ranked memory block.
type Block struct {
	Kind       string    `json:"kind"`
	Text       string    `json:"text"`
	SourcePath string    `json:"source_path"`
	Freshness  Freshness `json:"freshness"`
	RankScore  float64   `json:"rank_score"`
}

// Context is the MemoryContext returned to the gateway.
type Context struct {
	SchemaVersion int
	Status        string // ok | degraded | empty | error | skipped
	Reason        string
	Blocks        []Block
	Tokens        int
	TraceID       string
	GeneratedAt   string
}

// approxTokens estimates tokens from character count (runes, ≈ JS String.length for the BMP) so a
// non-ASCII .l00prite file (em-dashes, smart quotes) isn't over-counted 2-3x by byte length.
func approxTokens(s string) int { return int(math.Ceil(float64(utf8.RuneCountInString(s)) / 4)) }

// within resolves symlinks (fail closed) and verifies p is inside root.
func within(root, p string) bool {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	c, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	return c == r || strings.HasPrefix(c, r+string(os.PathSeparator))
}

var keywordRE = mustKeywordRE()

func keywordsFrom(d Digest) map[string]bool {
	words := keywordRE.FindAllString(strings.ToLower(d.UserIntent), -1)
	kws := map[string]bool{}
	for i, w := range words {
		if i >= 40 {
			break
		}
		kws[w] = true
	}
	return kws
}

// Query answers the MemoryQuery. contextTokens/maxFileBytes come from the config budgets.
func Query(repoRoot string, d Digest, contextTokens, maxFileBytes int, allowStale bool) Context {
	traceID := "mem_" + strconv.FormatInt(time.Now().UnixMilli(), 36)
	if repoRoot == "" {
		return empty("no_repo", traceID)
	}
	dir := filepath.Join(repoRoot, ".l00prite")
	if !within(repoRoot, dir) {
		return empty("no_memory_dir", traceID)
	}
	if _, err := os.Stat(dir); err != nil {
		return empty("no_memory_dir", traceID)
	}

	kws := keywordsFrom(d)
	refPaths := make([]string, 0, len(d.ReferencedPaths))
	for _, p := range d.ReferencedPaths {
		refPaths = append(refPaths, strings.ToLower(p))
	}
	maxBytes := maxFileBytes
	if maxBytes == 0 {
		maxBytes = 262144
	}

	var candidates []Block
	for _, s := range sources {
		fp := filepath.Join(dir, s.file)
		if !within(repoRoot, fp) {
			continue
		}
		st, err := os.Stat(fp)
		if err != nil {
			continue
		}
		var text string
		if st.Size() > int64(maxBytes) {
			f, err := os.Open(fp)
			if err != nil {
				continue
			}
			buf := make([]byte, maxBytes)
			n, _ := f.Read(buf)
			f.Close()
			// A byte-cap read can split a trailing multibyte rune; replace any invalid tail with U+FFFD
			// (as Node's Buffer.toString('utf8') does) so the injected block is valid UTF-8.
			text = strings.TrimSpace(strings.ToValidUTF8(string(buf[:n]), "�")) + "\n… [truncated: file exceeds memory read cap]"
		} else {
			b, err := os.ReadFile(fp)
			if err != nil {
				continue
			}
			text = strings.TrimSpace(string(b))
		}
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)
		score := s.base
		for k := range kws {
			if strings.Contains(lower, k) {
				score += 0.02
			}
		}
		for _, rp := range refPaths {
			if strings.Contains(lower, rp) {
				score += 0.08
			}
		}
		ageDays := time.Since(st.ModTime()).Hours() / 24
		stale := ageDays > 30
		if stale {
			score -= 0.15
		}
		candidates = append(candidates, Block{
			Kind:       s.kind,
			Text:       text,
			SourcePath: ".l00prite/" + s.file,
			Freshness:  Freshness{AsOf: util.ISOFromTime(st.ModTime()), Stale: stale},
			RankScore:  math.Round(score*1000) / 1000,
		})
	}
	if len(candidates) == 0 {
		return empty("no_content", traceID)
	}

	// Stable sort by rank_score desc (mirrors JS Array.sort with a numeric comparator).
	sortByScoreDesc(candidates)

	budget := contextTokens
	if budget == 0 {
		budget = 8000
	}
	var blocks []Block
	tokens := 0
	truncated := false
	for _, c := range candidates {
		text := c.Text
		t := approxTokens(text)
		if tokens+t > budget {
			room := budget - tokens
			if room < 0 {
				room = 0
			}
			if room < 120 {
				truncated = true
				break
			}
			runes := []rune(text)
			end := room * 4
			if end > len(runes) {
				end = len(runes)
			}
			text = string(runes[:end]) + "\n… [truncated by context budget]" // rune-safe slice (never splits a rune)
			truncated = true
		}
		b := c
		b.Text = text
		blocks = append(blocks, b)
		tokens += approxTokens(text)
		if tokens >= budget {
			truncated = true
			break
		}
	}

	anyStale := false
	for _, b := range blocks {
		if b.Freshness.Stale {
			anyStale = true
		}
	}
	status, reason := "ok", ""
	if anyStale && !allowStale {
		status, reason = "degraded", "stale_blocks_present"
	}
	if truncated {
		status = "degraded"
		if reason == "" {
			reason = "context_budget_truncated"
		}
	}
	return Context{
		SchemaVersion: 1, Status: status, Reason: reason, Blocks: blocks,
		Tokens: tokens, TraceID: traceID, GeneratedAt: util.NowISO(),
	}
}

func empty(reason, traceID string) Context {
	return Context{SchemaVersion: 1, Status: "empty", Reason: reason, Blocks: nil, Tokens: 0, TraceID: traceID}
}
