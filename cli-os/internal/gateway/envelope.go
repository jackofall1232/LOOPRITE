// Shared untrusted-content envelope. Anything re-fed into a model prompt that did not come from
// config or the authenticated request — repository memory (inject.go) and delegated provider output
// during bridging (bridge.go) — is UNTRUSTED and wrapped here. Two jobs: a non-instruction preamble
// so the wrapped block is read as reference data, and tag-breakout neutralization so a malicious
// body containing the literal closing delimiter can't "escape" the envelope. Ported from envelope.js.
package gateway

import (
	"fmt"
	"regexp"
	"strings"
)

// Attr is an ordered attribute so wrapper output is deterministic (Go maps are unordered).
type Attr struct {
	Key string
	Val any
}

var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// wsClass matches the same whitespace set JS regex `\s` does (Go's RE2 `\s` is ASCII-only, which
// would let an attacker slip a vertical tab / NBSP / other Unicode space into a closing tag and
// escape the untrusted-content envelope). Covers VT, the Zs separators (incl. NBSP), ZWNBSP, LS, PS.
const wsClass = `[\s\x{000B}\p{Zs}\x{FEFF}\x{2028}\x{2029}]`

// neutralizeClosers entity-escapes the COMPLETE closing token `</tag ...>` for each protected tag,
// case-insensitively and tolerant of internal (Unicode) whitespace. Only complete closers are a
// breakout vector; a bare `</tag` and other angle brackets are left intact for content fidelity.
func neutralizeClosers(text string, tagNames []string) string {
	out := text
	for _, t := range tagNames {
		re := regexp.MustCompile(`(?i)</` + wsClass + `*` + regexp.QuoteMeta(t) + wsClass + `*>`)
		out = re.ReplaceAllString(out, "&lt;/"+t+"&gt;")
	}
	return out
}

func escapeAttr(v any) string { return attrEscaper.Replace(fmt.Sprint(v)) }

func attrString(attrs []Attr) string {
	var b strings.Builder
	for _, a := range attrs {
		if a.Val == nil {
			continue
		}
		b.WriteString(" " + a.Key + `="` + escapeAttr(a.Val) + `"`)
	}
	return b.String()
}

// WrapUntrusted wraps a single untrusted body in one tag with a preamble. protect lists tag names
// whose closers must be neutralized inside the body (defaults to [tag]).
func WrapUntrusted(preamble, tag string, attrs []Attr, body string, protect []string) string {
	if protect == nil {
		protect = []string{tag}
	}
	safe := neutralizeClosers(body, protect)
	return fmt.Sprintf("%s\n\n<%s%s>\n%s\n</%s>", preamble, tag, attrString(attrs), safe, tag)
}

// UntrustedBlock builds one inner block string `<tag ...>...</tag>` with its closer neutralized.
func UntrustedBlock(tag string, attrs []Attr, body string, protect []string) string {
	if protect == nil {
		protect = []string{tag}
	}
	safe := neutralizeClosers(body, protect)
	return fmt.Sprintf("<%s%s>\n%s\n</%s>", tag, attrString(attrs), safe, tag)
}
