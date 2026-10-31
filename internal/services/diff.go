package services

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// FieldChange is one field-level difference. Values are rendered as strings;
// an absent value is "".
type FieldChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
	// Origin is "direct" (the variable itself changed; omitted for settings) or
	// "inherited": its text is unchanged but it resolves to a different value
	// because a variable it references changed. Via lists those references.
	Origin          string `json:"origin,omitempty"`
	Via             string `json:"via,omitempty"`
	ResolvedChanged bool   `json:"resolvedChanged,omitempty"`
}

// Change origins.
const (
	OriginDirect    = "direct"
	OriginInherited = "inherited"
)

// DiffConfig lists the fields that differ between two (defaulted) configs,
// keyed "<prefix>.<field>" with nested objects flattened. The processes are
// keyed by name: a process that is added or removed is one change,
// "<prefix>.processes.<name>" (the process as compact JSON), and an edited one
// reports "<prefix>.processes.<name>.<field>" per changed field.
func DiffConfig(prefix string, from, to Config) []FieldChange {
	a, b := flatten(from), flatten(to)
	collapseProcesses(a, from.Processes, b, to.Processes)
	// The stack's components are derived from its compose file, which is diffed.
	delete(a, "stack.components")
	delete(b, "stack.components")
	keys := map[string]struct{}{}
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []FieldChange
	for _, k := range sorted {
		if a[k] != b[k] {
			out = append(out, FieldChange{Field: prefix + "." + k, From: a[k], To: b[k]})
		}
	}
	return out
}

// collapseProcesses replaces, in the flattened maps, the per-field entries of
// every process the other side lacks with one entry holding the process as
// compact JSON.
func collapseProcesses(a map[string]string, ap []ProcessConfig, b map[string]string, bp []ProcessConfig) {
	collapse := func(m map[string]string, own, other []ProcessConfig) {
		for _, p := range own {
			if slices.ContainsFunc(other, func(o ProcessConfig) bool { return o.Name == p.Name }) {
				continue
			}
			base := "processes." + p.Name
			for k := range m {
				if strings.HasPrefix(k, base+".") {
					delete(m, k)
				}
			}
			raw, _ := json.Marshal(p)
			m[base] = string(raw)
		}
	}
	collapse(a, ap, bp)
	collapse(b, bp, ap)
}

func flatten(c Config) map[string]string {
	raw, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	// The processes are an array; keyed by name they read as processes.<name>.<field>.
	if list, ok := m["processes"].([]any); ok {
		byName := make(map[string]any, len(list))
		for _, item := range list {
			if p, ok := item.(map[string]any); ok {
				if name, _ := p["name"].(string); name != "" {
					byName[name] = p
				}
			}
		}
		m["processes"] = byName
	}
	out := map[string]string{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if obj, ok := v.(map[string]any); ok {
			for k, vv := range obj {
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				walk(key, vv)
			}
			return
		}
		switch x := v.(type) {
		case string:
			out[prefix] = x
		case nil:
			out[prefix] = ""
		default:
			out[prefix] = fmt.Sprint(x)
			if b, err := json.Marshal(x); err == nil {
				out[prefix] = string(b)
			}
		}
	}
	walk("", m)
	return out
}
