// Full l00prite protocol scaffold — everything Scaffold() (l00pfiles.go) deliberately does NOT
// write: AGENTS.md, the six canonical loop prompts, the vendor adapter files other AI coding
// tools read, and (only when a target repo has no CLAUDE.md at all) a starter CLAUDE.md carrying
// just the fixed protocol section. This is the maintainer-requested "full benefit of the
// l00prite methodology" scaffold, offered as an explicit, consent-gated action (never automatic
// — see gateway/repo_scaffold.go), unlike Scaffold()'s silent auto-run at repo register/clone.
//
// cli-os is a standalone portable binary (it must run against arbitrary repos, including from
// the Android APK, with no access to this monorepo's own templates/ directory at runtime), so
// this package carries its OWN embedded copy of the source-of-truth content under ./protocol/ —
// copied verbatim from templates/{AGENTS.md.template,l00prite/prompts/*,adapters/*} and the
// fixed protocol section of templates/CLAUDE.md.template. This is the SAME "separate copy kept
// in sync by hand" pattern l00pfiles.go's own heartbeat/state/lock/LOCKING.md constants already
// use (see that file's package doc comment) — just embedded from real files instead of
// hand-transcribed into Go string constants, which is far less error-prone for ~1200 lines of
// content. It is NOT YET covered by scripts/validate-l00prite.js's byte-parity check across the
// six canonical prompts' other mirrors — that validator is a human-review-gated file per this
// repo's CLAUDE.md, so extending it is a queued follow-up for the maintainer, not done here.
package engine

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed protocol
var protocolFS embed.FS

// protocolFile reads one file from the embedded protocol tree (relative to protocol/).
func protocolFile(rel string) (string, error) {
	b, err := protocolFS.ReadFile("protocol/" + rel)
	if err != nil {
		return "", fmt.Errorf("embedded protocol file %q missing: %w", rel, err)
	}
	return string(b), nil
}

// protocolCopy is one embedded source file and the repo-relative path it lands at.
type protocolCopy struct{ src, dest string }

// fullProtocolCopies is every byte-identical file ScaffoldFull adds beyond Scaffold()'s
// baseline: the six canonical loop prompts (mirrored into .l00prite/prompts/, matching every
// other mirror location) and the vendor adapter files (target paths from templates/vendors.json
// — Zed and opencode need no adapter file, they read AGENTS.md/CLAUDE.md natively).
var fullProtocolCopies = []protocolCopy{
	{"prompts/README.md", ".l00prite/prompts/README.md"},
	{"prompts/event-loop.md", ".l00prite/prompts/event-loop.md"},
	{"prompts/execute-loop.md", ".l00prite/prompts/execute-loop.md"},
	{"prompts/handoff-summary.md", ".l00prite/prompts/handoff-summary.md"},
	{"prompts/heartbeat.md", ".l00prite/prompts/heartbeat.md"},
	{"prompts/respond-to-review.md", ".l00prite/prompts/respond-to-review.md"},
	{"prompts/resume-loop.md", ".l00prite/prompts/resume-loop.md"},
	{"adapters/GEMINI.md", "GEMINI.md"},
	{"adapters/QWEN.md", "QWEN.md"},
	{"adapters/copilot-instructions.md", ".github/copilot-instructions.md"},
	{"adapters/l00prite.mdc", ".cursor/rules/l00prite.mdc"},
	{"adapters/windsurf-l00prite.md", ".windsurf/rules/l00prite.md"},
	{"adapters/CONVENTIONS.md", "CONVENTIONS.md"},
}

// fullProtocolTargets is every repo-relative path ScaffoldFull may create, beyond Scaffold()'s
// own baseline — AGENTS.md plus every fullProtocolCopies destination. CLAUDE.md is tracked
// separately (see FullProtocolGaps) since ScaffoldFull only ever writes a STARTER CLAUDE.md when
// one is entirely absent, never alongside an existing one.
func fullProtocolTargets() []string {
	out := make([]string, 0, len(fullProtocolCopies)+1)
	out = append(out, "AGENTS.md")
	for _, c := range fullProtocolCopies {
		out = append(out, c.dest)
	}
	return out
}

// FullProtocolGaps reports, without writing anything, which of ScaffoldFull's files are missing
// and whether CLAUDE.md itself is absent. Callers use this to decide whether a scaffold action
// has anything to do before creating a branch for it — a repo that already has the full
// protocol should never get a pointless empty branch.
func (f Files) FullProtocolGaps() (missing []string, claudeMissing bool) {
	for _, rel := range fullProtocolTargets() {
		if _, err := os.Stat(filepath.Join(f.Root, filepath.FromSlash(rel))); os.IsNotExist(err) {
			missing = append(missing, rel)
		}
	}
	if _, err := os.Stat(filepath.Join(f.Root, "CLAUDE.md")); os.IsNotExist(err) {
		claudeMissing = true
	}
	return missing, claudeMissing
}

// ScaffoldFull writes the full l00prite protocol surface: it first runs the same baseline
// Scaffold() every register/clone already gets (idempotent — createIfMissing skips anything
// already there), then additionally writes AGENTS.md, the six canonical loop prompts, and the
// vendor adapter files, and — only when the target repo has NO CLAUDE.md at all — a starter
// CLAUDE.md carrying just the fixed protocol section verbatim. An existing CLAUDE.md is NEVER
// touched (createIfMissing's "never overwrite" invariant, same as every other scaffolded file);
// claudeSkipped reports that case so the caller can tell the user to add the section by hand.
// created lists every repo-relative path actually written (Scaffold()'s own list plus these).
func (f Files) ScaffoldFull(projectName, goal string) (created []string, claudeSkipped bool, err error) {
	created, err = f.Scaffold(projectName, goal)
	if err != nil {
		return created, false, err
	}

	agentsTpl, ferr := protocolFile("AGENTS.md.template")
	if ferr != nil {
		return created, false, ferr
	}
	missionLine := goal
	if strings.TrimSpace(missionLine) == "" {
		missionLine = "an l00prite-managed project — fill in a real mission in CLAUDE.md"
	}
	agentsMD := strings.NewReplacer(
		"{{project_name}}", projectName,
		"{{mission_line}}", missionLine,
	).Replace(agentsTpl)

	writes := []protocolCopy{{"", "AGENTS.md"}} // src unused for this one; content built above
	writes = append(writes, fullProtocolCopies...)
	for _, cp := range writes {
		content := agentsMD
		if cp.src != "" {
			content, ferr = protocolFile(cp.src)
			if ferr != nil {
				return created, false, ferr
			}
		}
		wrote, werr := f.createIfMissing(cp.dest, content)
		if werr != nil {
			return created, false, werr
		}
		if wrote {
			created = append(created, cp.dest)
		}
	}

	claudePath := filepath.Join(f.Root, "CLAUDE.md")
	if _, statErr := os.Stat(claudePath); os.IsNotExist(statErr) {
		section, ferr := protocolFile("claude_protocol_section.md")
		if ferr != nil {
			return created, false, ferr
		}
		starter := "# CLAUDE.md\n\n" + section +
			"\n<!-- Fill in Mission / Architecture / Requirements / Definition of Done / Agent\n" +
			"     Operating Loop / Heartbeat Rules / Run Ledger / Completion Criteria as this\n" +
			"     project takes shape. The section above must stay verbatim. -->\n"
		wrote, werr := f.createIfMissing("CLAUDE.md", starter)
		if werr != nil {
			return created, false, werr
		}
		if wrote {
			created = append(created, "CLAUDE.md")
		}
	} else if statErr == nil {
		claudeSkipped = true
	} else {
		return created, false, statErr
	}

	return created, claudeSkipped, nil
}
