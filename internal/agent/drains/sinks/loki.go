package sinks

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type labelPair struct{ k, v string }

// lokiBuiltinLabels are always set from the entry; static labels never replace them.
var lokiBuiltinLabels = map[string]bool{
	"service": true, "service_id": true, "project": true, "environment": true,
	"server": true, "level": true, "stream": true,
}

type lokiSink struct {
	http   *httpSender
	static []labelPair // sanitised static labels, built-in names removed
}

func newLoki(c *agentprotocol.LokiDrainConfig, secrets map[string]string) (Sink, error) {
	hdr, err := buildHeaders(secrets, true)
	if err != nil {
		return nil, err
	}
	if c.TenantID != "" {
		hdr.Set("X-Scope-OrgID", c.TenantID)
	}
	keys := make([]string, 0, len(c.Labels))
	for k := range c.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var static []labelPair
	seen := map[string]bool{}
	for _, k := range keys {
		name, v := sanitizeLabelName(k), c.Labels[k]
		if name == "" || v == "" || lokiBuiltinLabels[name] || seen[name] {
			continue
		}
		seen[name] = true
		static = append(static, labelPair{name, v})
	}
	return &lokiSink{http: newHTTPSender(http.MethodPost, c.URL, hdr, true, secrets), static: static}, nil
}

// sanitizeLabelName maps a name to [a-zA-Z_][a-zA-Z0-9_]*; "" when empty.
func sanitizeLabelName(s string) string {
	if s == "" {
		return ""
	}
	b := make([]byte, 0, len(s)+1)
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
			if i == 0 {
				b = append(b, '_')
			}
		default:
			c = '_'
		}
		b = append(b, c)
	}
	return string(b)
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

type lokiPush struct {
	Streams []lokiStream `json:"streams"`
}

func (l *lokiSink) Send(ctx context.Context, batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	body, err := l.encode(batch)
	if err != nil {
		return err
	}
	return l.http.send(ctx, "application/json", body)
}

func (l *lokiSink) Close() error { return l.http.close() }

type lokiLine struct {
	ns   int64
	text string
}

type lokiGroup struct {
	key    string
	labels map[string]string
	lines  []lokiLine
}

func (l *lokiSink) encode(batch []Entry) ([]byte, error) {
	groups := map[string]*lokiGroup{}
	for _, e := range batch {
		pairs := l.labels(e)
		var kb strings.Builder
		for _, p := range pairs {
			kb.WriteString(p.k)
			kb.WriteByte('=')
			kb.WriteString(strconv.Quote(p.v))
			kb.WriteByte(',')
		}
		key := kb.String()
		g := groups[key]
		if g == nil {
			g = &lokiGroup{key: key, labels: make(map[string]string, len(pairs))}
			for _, p := range pairs {
				g.labels[p.k] = p.v
			}
			groups[key] = g
		}
		g.lines = append(g.lines, lokiLine{ns: e.ts().UnixNano(), text: e.Text})
	}
	ordered := make([]*lokiGroup, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	slices.SortFunc(ordered, func(a, b *lokiGroup) int { return strings.Compare(a.key, b.key) })

	push := lokiPush{Streams: make([]lokiStream, 0, len(ordered))}
	for _, g := range ordered {
		slices.SortStableFunc(g.lines, func(a, b lokiLine) int {
			switch {
			case a.ns < b.ns:
				return -1
			case a.ns > b.ns:
				return 1
			}
			return 0
		})
		st := lokiStream{Stream: g.labels, Values: make([][2]string, len(g.lines))}
		for i, ln := range g.lines {
			st.Values[i] = [2]string{strconv.FormatInt(ln.ns, 10), ln.text}
		}
		push.Streams = append(push.Streams, st)
	}
	return marshalJSON(push)
}

// labels returns the stream labels of an entry sorted by name; empty values are omitted.
func (l *lokiSink) labels(e Entry) []labelPair {
	p := make([]labelPair, 0, len(l.static)+7)
	add := func(k, v string) {
		if v != "" {
			p = append(p, labelPair{k, v})
		}
	}
	add("service", e.Service)
	add("service_id", e.ServiceID)
	add("project", e.Project)
	add("environment", e.Environment)
	add("server", e.Server)
	add("level", normLevel(e.Level))
	add("stream", e.Stream)
	p = append(p, l.static...)
	slices.SortFunc(p, func(a, b labelPair) int { return strings.Compare(a.k, b.k) })
	return p
}
