package main

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// envEntry is one compose environment item in source order.
type envEntry struct {
	Key    string
	Val    string
	HasVal bool // false for the bare "KEY" pass-through form
}

// mount is one compose volume entry in a normalised form.
type mount struct {
	Type     string // volume | bind
	Source   string
	Target   string
	ReadOnly bool
	Content  string
	HasBody  bool // long syntax with content:
}

// portMap is one compose ports entry.
type portMap struct {
	Container string // may still contain ${VAR:-n}
	Host      string
	Proto     string
}

// depend is one depends_on edge.
type depend struct {
	Name      string
	Condition string
}

type health struct {
	Test        []string
	Disabled    bool
	Interval    string
	Timeout     string
	StartPeriod string
	Retries     int
}

// csvc is one compose service, decoded loosely: compose is far wider than
// what templates support, and unsupported keys are detected from Raw.
type csvc struct {
	Name        string
	Raw         map[string]any
	Image       string
	Command     []string
	HasCommand  bool
	Env         []envEntry
	Volumes     []mount
	Ports       []portMap
	DependsOn   []depend
	Health      *health
	Restart     string
	MemLimit    string
	Cpus        float64
	StopGrace   string
	ContainerNm string
}

type composeDoc struct {
	Services []*csvc
	// TopLevel lists top-level compose sections other than services/volumes.
	TopLevel map[string]any
	Volumes  map[string]any
}

func parseCompose(data []byte) (*composeDoc, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("compose yaml: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("compose yaml: not a mapping")
	}
	top := root.Content[0]
	doc := &composeDoc{TopLevel: map[string]any{}, Volumes: map[string]any{}}
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, val := top.Content[i].Value, top.Content[i+1]
		switch key {
		case "services":
			if val.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("compose yaml: services is not a mapping")
			}
			for j := 0; j+1 < len(val.Content); j += 2 {
				var raw map[string]any
				if err := val.Content[j+1].Decode(&raw); err != nil {
					return nil, fmt.Errorf("service %s: %w", val.Content[j].Value, err)
				}
				if raw == nil {
					raw = map[string]any{}
				}
				s, err := newCsvc(val.Content[j].Value, raw)
				if err != nil {
					return nil, err
				}
				doc.Services = append(doc.Services, s)
			}
		case "volumes":
			var m map[string]any
			if err := val.Decode(&m); err == nil {
				doc.Volumes = m
			}
		case "version", "name":
		default:
			var v any
			_ = val.Decode(&v)
			doc.TopLevel[key] = v
		}
	}
	return doc, nil
}

func newCsvc(name string, raw map[string]any) (*csvc, error) {
	s := &csvc{Name: name, Raw: raw}
	s.Image = scalar(raw["image"])
	s.Restart = scalar(raw["restart"])
	s.MemLimit = scalar(raw["mem_limit"])
	s.StopGrace = scalar(raw["stop_grace_period"])
	s.ContainerNm = scalar(raw["container_name"])
	if v := scalar(raw["cpus"]); v != "" {
		s.Cpus, _ = strconv.ParseFloat(v, 64)
	}
	if d, ok := raw["deploy"].(map[string]any); ok {
		if r, ok := d["resources"].(map[string]any); ok {
			if l, ok := r["limits"].(map[string]any); ok {
				if s.MemLimit == "" {
					s.MemLimit = scalar(l["memory"])
				}
				if s.Cpus == 0 {
					s.Cpus, _ = strconv.ParseFloat(scalar(l["cpus"]), 64)
				}
			}
		}
	}
	if c, ok := raw["command"]; ok && c != nil {
		s.HasCommand = true
		switch v := c.(type) {
		case []any:
			for _, a := range v {
				s.Command = append(s.Command, scalar(a))
			}
		default:
			args, err := shellSplit(scalar(v))
			if err != nil {
				return nil, fmt.Errorf("service %s: command: %w", name, err)
			}
			s.Command = args
		}
	}
	switch e := raw["environment"].(type) {
	case []any:
		for _, it := range e {
			str := scalar(it)
			k, v, ok := strings.Cut(str, "=")
			s.Env = append(s.Env, envEntry{Key: strings.TrimSpace(k), Val: v, HasVal: ok})
		}
	case map[string]any:
		for _, k := range sortedKeys(e) {
			if e[k] == nil {
				s.Env = append(s.Env, envEntry{Key: k})
				continue
			}
			s.Env = append(s.Env, envEntry{Key: k, Val: scalar(e[k]), HasVal: true})
		}
	}
	if vols, ok := raw["volumes"].([]any); ok {
		for _, v := range vols {
			switch m := v.(type) {
			case string:
				s.Volumes = append(s.Volumes, parseShortMount(m))
			case map[string]any:
				mt := mount{Type: scalar(m["type"]), Source: scalar(m["source"]), Target: scalar(m["target"])}
				if ro, ok := m["read_only"].(bool); ok {
					mt.ReadOnly = ro
				}
				if c, ok := m["content"]; ok {
					mt.Content, mt.HasBody = scalar(c), true
				}
				if mt.Type == "" {
					mt.Type = "volume"
				}
				s.Volumes = append(s.Volumes, mt)
			}
		}
	}
	if ps, ok := raw["ports"].([]any); ok {
		for _, p := range ps {
			switch m := p.(type) {
			case map[string]any:
				s.Ports = append(s.Ports, portMap{Container: scalar(m["target"]), Host: scalar(m["published"]), Proto: scalar(m["protocol"])})
			default:
				s.Ports = append(s.Ports, parseShortPort(scalar(p)))
			}
		}
	}
	switch d := raw["depends_on"].(type) {
	case []any:
		for _, n := range d {
			s.DependsOn = append(s.DependsOn, depend{Name: scalar(n), Condition: "service_started"})
		}
	case map[string]any:
		for _, n := range sortedKeys(d) {
			cond := "service_started"
			if m, ok := d[n].(map[string]any); ok && scalar(m["condition"]) != "" {
				cond = scalar(m["condition"])
			}
			s.DependsOn = append(s.DependsOn, depend{Name: n, Condition: cond})
		}
	}
	if h, ok := raw["healthcheck"].(map[string]any); ok {
		hc := &health{Interval: scalar(h["interval"]), Timeout: scalar(h["timeout"]), StartPeriod: scalar(h["start_period"])}
		hc.Retries, _ = strconv.Atoi(scalar(h["retries"]))
		if d, ok := h["disable"].(bool); ok && d {
			hc.Disabled = true
		}
		switch t := h["test"].(type) {
		case []any:
			for _, a := range t {
				hc.Test = append(hc.Test, scalar(a))
			}
		case string:
			hc.Test = []string{"CMD-SHELL", t}
		}
		if len(hc.Test) > 0 && hc.Test[0] == "NONE" {
			hc.Disabled = true
		}
		s.Health = hc
	}
	return s, nil
}

// scalar renders a YAML scalar as compose would see it.
func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func sortedKeys(m map[string]any) []string {
	return slices.Sorted(maps.Keys(m))
}

func parseShortMount(s string) mount {
	parts := splitUnbraced(s, ':')
	m := mount{Type: "volume"}
	switch len(parts) {
	case 1:
		m.Target = parts[0] // anonymous volume
		m.Source = ""
	default:
		m.Source, m.Target = parts[0], parts[1]
		if len(parts) > 2 && strings.Contains(parts[2], "ro") {
			m.ReadOnly = true
		}
	}
	if strings.HasPrefix(m.Source, "/") || strings.HasPrefix(m.Source, ".") || strings.HasPrefix(m.Source, "~") {
		m.Type = "bind"
	}
	return m
}

// splitUnbraced splits on sep outside ${…}.
func splitUnbraced(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}' && depth > 0:
			depth--
		case s[i] == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func parseShortPort(s string) portMap {
	s = strings.TrimSpace(s)
	p := portMap{Proto: "tcp"}
	if i := strings.LastIndex(s, "/"); i >= 0 && !strings.Contains(s[i:], "}") {
		p.Proto = strings.ToLower(s[i+1:])
		s = s[:i]
	}
	parts := splitUnbraced(s, ':')
	p.Container = parts[len(parts)-1]
	if len(parts) >= 2 {
		p.Host = parts[len(parts)-2]
	}
	return p
}

// shellSplit splits a command string like a POSIX shell would for plain
// words and quotes (no expansion).
func shellSplit(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\' || s[i+1] == '$') {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord = true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}
