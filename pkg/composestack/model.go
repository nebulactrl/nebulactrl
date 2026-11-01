// Package composestack is the pure core of NebulaCtrl's native Compose
// stacks: it reads a compose model, derives the stack's components, checks the
// model against the safe allow-list and generates the override file the agent
// runs `docker compose` with.
//
// The same code runs in two places. The control plane parses the compose file
// as the user wrote it (short and long syntaxes, no interpolation) to list
// components and reject obvious problems early. The agent normalises the file
// with `docker compose config --format json` first (every syntax resolved to
// its long form, variables interpolated, anchors and extends expanded) and
// then calls the same functions; the agent's verdict is the authoritative
// one.
package composestack

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// MaxComposeBytes bounds a stack's compose file.
const MaxComposeBytes = 256 * 1024

// Project is a parsed compose model. Its maps are the generic YAML/JSON
// values; the helpers below read both the short and the long syntax of every
// key the allow-list looks at.
type Project struct {
	raw      map[string]any
	services map[string]map[string]any
	names    []string // sorted service names
}

// Parse reads a compose file (YAML) or a normalised model (JSON, which is YAML).
func Parse(data []byte) (*Project, error) {
	if len(data) == 0 {
		return nil, errors.New("the compose file is empty")
	}
	if len(data) > MaxComposeBytes {
		return nil, fmt.Errorf("the compose file is larger than %d KiB", MaxComposeBytes/1024)
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("the compose file is not valid YAML: %w", err)
	}
	root, ok := stringKeys(doc).(map[string]any)
	if !ok {
		return nil, errors.New("the compose file must be a YAML mapping with a services section")
	}
	svcs, _ := root["services"].(map[string]any)
	if len(svcs) == 0 {
		return nil, errors.New("the compose file defines no services")
	}
	p := &Project{raw: root, services: make(map[string]map[string]any, len(svcs))}
	for name, v := range svcs {
		sv, ok := v.(map[string]any)
		if !ok {
			if v == nil {
				sv = map[string]any{} // `web:` with no body is legal (image comes from extends)
			} else {
				return nil, fmt.Errorf("service %q must be a mapping", name)
			}
		}
		p.services[name] = sv
		p.names = append(p.names, name)
	}
	sort.Strings(p.names)
	return p, nil
}

// stringKeys converts every map[any]any of a decoded document into
// map[string]any (YAML allows non-string keys).
func stringKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = stringKeys(x)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = stringKeys(x)
		}
		return out
	case []any:
		for i, x := range t {
			t[i] = stringKeys(x)
		}
		return t
	}
	return v
}

// Names returns the service names, sorted.
func (p *Project) Names() []string { return append([]string(nil), p.names...) }

// Components derives the stack's components: every service, sorted by name,
// with its image and the container ports it declares. Index is the position in
// the sorted list.
func (p *Project) Components() []agentprotocol.StackComponent {
	out := make([]agentprotocol.StackComponent, 0, len(p.names))
	for i, name := range p.names {
		sv := p.services[name]
		c := agentprotocol.StackComponent{Name: name, Index: i, Image: asString(sv["image"]), Ports: servicePorts(sv)}
		c.Profiles = stringList(sv["profiles"])
		out = append(out, c)
	}
	return out
}

// servicePorts lists the container ports of ports and expose, sorted and unique.
func servicePorts(sv map[string]any) []int {
	seen := map[int]bool{}
	add := func(n int) {
		if n >= 1 && n <= 65535 {
			seen[n] = true
		}
	}
	for _, e := range asList(sv["ports"]) {
		switch t := e.(type) {
		case map[string]any:
			if n, ok := asInt(t["target"]); ok {
				add(n)
			}
		default:
			for _, n := range containerPortsOfShort(fmt.Sprint(e)) {
				add(n)
			}
		}
	}
	for _, e := range asList(sv["expose"]) {
		for _, n := range portRange(strings.TrimSuffix(strings.TrimSuffix(fmt.Sprint(e), "/tcp"), "/udp")) {
			add(n)
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// containerPortsOfShort extracts the container port(s) of a short ports entry:
// "80", "8080:80", "127.0.0.1:8080:80/tcp", "8000-8010:80-90".
func containerPortsOfShort(s string) []int {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	// An IPv6 host address is bracketed and contains colons.
	if strings.HasPrefix(s, "[") {
		if i := strings.IndexByte(s, ']'); i >= 0 {
			s = strings.TrimPrefix(s[i+1:], ":")
		}
	}
	parts := strings.Split(s, ":")
	return portRange(parts[len(parts)-1])
}

func portRange(s string) []int {
	lo, hi, found := strings.Cut(strings.TrimSpace(s), "-")
	a, err := strconv.Atoi(lo)
	if err != nil {
		return nil
	}
	if !found {
		return []int{a}
	}
	b, err := strconv.Atoi(hi)
	if err != nil || b < a || b-a > 100 {
		return nil
	}
	out := make([]int, 0, b-a+1)
	for n := a; n <= b; n++ {
		out = append(out, n)
	}
	return out
}

// ---- generic accessors ----

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	}
	return fmt.Sprint(v)
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, _ := strconv.ParseBool(t)
		return b
	}
	return false
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	}
	return 0, false
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// stringList reads a list of scalars (or the keys of a mapping) as strings.
func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, asString(e))
		}
		return out
	case map[string]any:
		out := make([]string, 0, len(t))
		for k := range t {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	return nil
}

// labelMap reads labels in list ("k=v") or map form.
func labelMap(v any) map[string]string {
	out := map[string]string{}
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			out[k] = asString(x)
		}
	case []any:
		for _, e := range t {
			k, val, _ := strings.Cut(asString(e), "=")
			out[k] = val
		}
	}
	return out
}

// hasVar reports whether s still contains an unresolved ${…} or $NAME
// reference (a model that was not interpolated).
func hasVar(s string) bool { return strings.Contains(s, "$") }
