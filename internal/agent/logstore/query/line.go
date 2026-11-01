package query

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Port of apps/web/src/features/logs/search/line.ts. A field is set only when
// the line says it. Everything past the ANSI-stripped text is computed on
// first use.

// Input is what the caller knows about a stored line.
type Input struct {
	Line         string
	Stream       string // "runtime" for stored runtime logs
	ServiceName  string
	Replica      string // "" = unknown; decimal index otherwise
	DeploymentID string
}

// Line is a log line prepared for matching. Not safe for concurrent use.
type Line struct {
	in        Input
	text      string
	lower     string
	haveText  bool
	haveLower bool
	f         *lineFields
}

type lineFields struct {
	level      string
	status     int
	replica    string
	deployment string
	stream     string
	attrs      map[string]any
}

// NewLine wraps an input; parsing is lazy.
func NewLine(in Input) *Line { return &Line{in: in} }

// Text returns the line without ANSI colour escapes.
func (l *Line) Text() string {
	if !l.haveText {
		l.text = stripANSI(l.in.Line)
		l.haveText = true
	}
	return l.text
}

// lowerText is Text in lower case, for matching.
func (l *Line) lowerText() string {
	if !l.haveLower {
		l.lower = jsLower(l.Text())
		l.haveLower = true
	}
	return l.lower
}

// Level returns "debug", "info", "warn" or "error", or "" when the line
// states none.
func (l *Line) Level() string { return l.fields().level }

// Status returns the HTTP status from a JSON/logfmt field or an access-log
// line, or 0.
func (l *Line) Status() int { return l.fields().status }

// stripANSI strips colour escapes: /\x1b\[[0-9;]*[A-Za-z]/g.
func stripANSI(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == ';') {
				j++
			}
			if j < len(s) && ((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// `.` in JavaScript excludes line terminators.
const jsDot = `[^\n\r\x{2028}\x{2029}]`

var logfmtPair = regexp.MustCompile(`(?:^|[` + jsWS + `])([A-Za-z_][A-Za-z0-9_.-]*)=("(?:[^"\\]|\\` + jsDot + `)*"|[^` + jsWS + `"]*)`)

func parseLogfmt(text string) map[string]any {
	// Two pairs need two '='.
	first := strings.IndexByte(text, '=')
	if first < 0 || strings.IndexByte(text[first+1:], '=') < 0 {
		return nil
	}
	matches := logfmtPair.FindAllStringSubmatch(text, -1)
	if len(matches) < 2 {
		return nil
	}
	attrs := make(map[string]any, len(matches))
	for _, m := range matches {
		value := m[2]
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = unescapeLogfmt(value[1 : len(value)-1])
		}
		attrs[m[1]] = value
	}
	return attrs
}

// unescapeLogfmt is .replace(/\\(.)/g, "$1").
func unescapeLogfmt(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+1 < len(s) {
			// The escaped character is one code point; a line terminator is not matched by `.`.
			r, size := utf8.DecodeRuneInString(s[i+1:])
			if r != '\n' && r != '\r' && r != 0x2028 && r != 0x2029 {
				b.WriteString(s[i+1 : i+1+size])
				i += 1 + size
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func parseJSON(text string) map[string]any {
	trimmed := jsTrim(text)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.InputOffset() != int64(len(trimmed)) {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	for k, e := range m {
		m[k] = convertNumbers(e)
	}
	return m
}

// convertNumbers turns json.Number into float64 like JSON.parse does (1e999 is Infinity).
func convertNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := strconv.ParseFloat(string(x), 64)
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = convertNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = convertNumbers(e)
		}
	}
	return v
}

// getAttr finds an attribute by path: an exact key first (`http.status` in
// logfmt), then nested objects. nil when missing.
func getAttr(attrs map[string]any, path string) any {
	if v, ok := attrs[path]; ok {
		return v
	}
	var current any = attrs
	for step := range strings.SplitSeq(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = obj[step]
		if !ok {
			return nil
		}
	}
	return current
}

var (
	levelKeys      = []string{"level", "severity", "lvl", "levelname", "loglevel", "log.level"}
	statusKeys     = []string{"status", "status_code", "statusCode", "http.status_code", "http_status", "response.status"}
	replicaKeys    = []string{"replica", "replica_index", "replicaIndex"}
	deploymentKeys = []string{"deployment", "deployment_id", "deploymentId"}
)

func firstAttr(attrs map[string]any, keys []string) any {
	for _, key := range keys {
		v := getAttr(attrs, key)
		if v != nil && v != "" {
			return v
		}
	}
	return nil
}

// levelOfValue reads a level attribute. pino and bunyan write numbers: 10
// trace, 20 debug, 30 info, 40 warn, 50 error, 60 fatal.
func levelOfValue(v any) string {
	switch x := v.(type) {
	case string:
		return levelFromName(x)
	case float64:
		switch {
		case x <= 20:
			return "debug"
		case x <= 30:
			return "info"
		case x <= 40:
			return "warn"
		}
		return "error"
	}
	return ""
}

// statusOfValue returns an HTTP status (100..599) or 0.
func statusOfValue(v any) int {
	var f float64
	switch x := v.(type) {
	case string:
		n, ok := jsToNumber(x)
		if !ok {
			return 0
		}
		f = n
	case float64:
		f = x
	default:
		return 0
	}
	if f != math.Trunc(f) || f < 100 || f > 599 {
		return 0
	}
	return int(f)
}

const methods = `GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|CONNECT|TRACE`

const (
	ws = `[` + jsWS + `]`
	ns = `[^` + jsWS + `]`
)

var (
	// nginx, traefik, caddy, uvicorn: ... "GET /path HTTP/1.1" 200 1234
	combinedRe = regexp.MustCompile(`"(` + methods + `)` + ws + `+(` + ns + `+)(?:` + ws + `+HTTP/[0-9.]+)?"` + ws + `+([0-9]{3})\b`)
	// gin: | 200 |  1.2ms | 127.0.0.1 | GET "/path"
	ginRe = regexp.MustCompile(`\|` + ws + `*([0-9]{3})` + ws + `*\|[^|]*\|[^|]*\|` + ws + `*(` + methods + `)` + ws + `+"?([^"` + jsWS + `]+)`)
	// GET /path 200 3ms, optionally after a timestamp word
	bareRe = regexp.MustCompile(`^` + ws + `*(?:` + ns + `+` + ws + `+)?(` + methods + `)` + ws + `+(` + ns + `+)` + ws + `+([0-9]{3})\b`)
)

type access struct {
	method, path string
	status       int
}

func accessLine(text string) (access, bool) {
	if m := combinedRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[3])
		return access{m[1], m[2], n}, true
	}
	if m := ginRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		return access{m[2], m[3], n}, true
	}
	if m := bareRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[3])
		return access{m[1], m[2], n}, true
	}
	return access{}, false
}

// asText is a string or a number as text; anything else is absent.
func asText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return jsNumString(x)
	}
	return ""
}

func (l *Line) fields() *lineFields {
	if l.f != nil {
		return l.f
	}
	text := l.Text()
	attrs := parseJSON(text)
	if attrs == nil {
		attrs = parseLogfmt(text)
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	f := &lineFields{attrs: attrs}
	l.f = f

	f.level = levelOfValue(firstAttr(attrs, levelKeys))
	if f.level == "" {
		f.level = textLevel(text)
	}

	f.status = statusOfValue(firstAttr(attrs, statusKeys))
	if f.status == 0 {
		if a, ok := accessLine(text); ok && a.status >= 100 && a.status <= 599 {
			f.status = a.status
			setIfMissing(attrs, "status", float64(a.status))
			setIfMissing(attrs, "method", a.method)
			setIfMissing(attrs, "path", a.path)
		}
	}

	f.replica = l.in.Replica
	if f.replica == "" {
		f.replica = asText(firstAttr(attrs, replicaKeys))
	}
	f.deployment = l.in.DeploymentID
	if f.deployment == "" {
		f.deployment = asText(firstAttr(attrs, deploymentKeys))
	}
	f.stream = l.in.Stream
	if f.stream == "" {
		f.stream = asText(getAttr(attrs, "stream"))
	}
	return f
}

// setIfMissing is `attrs[key] ??= value`.
func setIfMissing(attrs map[string]any, key string, value any) {
	if v, ok := attrs[key]; !ok || v == nil {
		attrs[key] = value
	}
}

// facetValue is the built-in facet of a line as a string.
func (l *Line) facetValue(key string) (string, bool) {
	switch key {
	case "service":
		return l.in.ServiceName, l.in.ServiceName != ""
	case "level":
		f := l.fields()
		return f.level, f.level != ""
	case "stream":
		f := l.fields()
		return f.stream, f.stream != ""
	case "replica":
		f := l.fields()
		return f.replica, f.replica != ""
	case "deployment":
		f := l.fields()
		return f.deployment, f.deployment != ""
	case "status":
		f := l.fields()
		if f.status == 0 {
			return "", false
		}
		return strconv.Itoa(f.status), true
	}
	return "", false
}
