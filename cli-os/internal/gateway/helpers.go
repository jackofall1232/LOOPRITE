package gateway

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// jsTruthy mirrors JavaScript truthiness for a JSON-decoded value.
func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	default:
		return true
	}
}

// jsonLenApprox returns the length of v's JSON encoding the way JS's JSON.stringify(v).length would:
// HTML escaping OFF (JS doesn't escape < > &) and counted in UTF-16-ish units (rune count, exact for
// the BMP). Used for the rough pre-flight token estimate so it matches the Node original.
func jsonLenApprox(v any) int {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return 0
	}
	s := b.String()
	if n := len(s); n > 0 && s[n-1] == '\n' { // Encode appends a trailing newline
		s = s[:n-1]
	}
	return utf8.RuneCountInString(s)
}

func numToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asArr(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
