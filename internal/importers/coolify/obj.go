package coolify

import (
	"encoding/json"
	"strconv"
	"strings"
)

// obj is one JSON object of the Coolify API read leniently: Coolify's field
// types drift between versions (numbers as strings, booleans as 0/1, keys that
// disappear for a token without read:sensitive), so every accessor converts
// instead of failing.
type obj map[string]any

func (o obj) has(key string) bool { _, ok := o[key]; return ok }

func (o obj) str(key string) string {
	switch v := o[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (o obj) int(key string) int {
	switch v := o[key].(type) {
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	case bool:
		if v {
			return 1
		}
	}
	return 0
}

func (o obj) bool(key string) bool {
	switch v := o[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func (o obj) obj(key string) obj {
	m, _ := o[key].(map[string]any)
	return obj(m)
}

func (o obj) list(key string) []obj {
	arr, _ := o[key].([]any)
	out := make([]obj, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, obj(m))
		}
	}
	return out
}

// strList reads an array of strings or a comma-separated string.
func (o obj) strList(key string) []string {
	switch v := o[key].(type) {
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		return splitList(v)
	}
	return nil
}

// splitList splits a comma-separated list and drops empty entries.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
