package adapters

import "encoding/json"

// numToInt coerces a JSON-decoded value (float64) or other numeric to int; 0 for anything else.
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

// copyMap makes a shallow copy of a map[string]any.
func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// asStr returns v as a string, or "" if it is not a string.
func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asArr returns v as []any, or nil.
func asArr(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

// asMap returns v as map[string]any, or nil.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// jsonStringify marshals v to a compact JSON string; nil becomes "{}".
func jsonStringify(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// jsTruthy mirrors JavaScript truthiness for a JSON-decoded value (so a model that stringifies a
// boolean, or a client that sends 1 instead of true, behaves like the Node original).
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
		return true // arrays, maps, etc. are truthy
	}
}
