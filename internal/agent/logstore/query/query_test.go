package query

import (
	"math"
	"slices"
	"testing"
)

func kinds(ts []token) []tokenKind {
	out := make([]tokenKind, len(ts))
	for i, t := range ts {
		out[i] = t.kind
	}
	return out
}

func TestTokenize(t *testing.T) {
	ts := tokenize(`level:error -foo (a OR "b c") @x.y:>=5`)
	want := []tokenKind{tkKey, tkValue, tkMinus, tkText, tkLParen, tkText, tkOr, tkPhrase, tkRParen, tkKey, tkValue}
	if !slices.Equal(kinds(ts), want) {
		t.Fatalf("kinds %v, want %v", kinds(ts), want)
	}
	if ts[9].text != "x.y" || !ts[9].attr || ts[10].op != ">=" || ts[10].text != "5" {
		t.Fatalf("attr key/op: %+v %+v", ts[9], ts[10])
	}
	if ts[7].text != "b c" || ts[7].start != 23 || ts[7].end != 28 {
		t.Fatalf("phrase: %+v", ts[7])
	}
}

func TestTokenizeKeyRules(t *testing.T) {
	cases := []struct {
		in   string
		want []tokenKind
	}{
		{"http://x", []tokenKind{tkText}},
		{"level:", []tokenKind{tkKey}},
		{"level: x", []tokenKind{tkKey, tkText}},
		{"k:(a b)", []tokenKind{tkKey, tkLParen, tkValue, tkValue, tkRParen}},
		{"k:(a OR b)", []tokenKind{tkKey, tkLParen, tkValue, tkOr, tkValue, tkRParen}},
		{"- a", []tokenKind{tkText, tkText}},
		{"--a", []tokenKind{tkText}},
		{"a-b", []tokenKind{tkText}},
		{"toString", []tokenKind{tkBogus}},
		{"k:[1 TO 2]", []tokenKind{tkKey, tkRange}},
	}
	for _, c := range cases {
		if got := kinds(tokenize(c.in)); !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.in, got, c.want)
		}
	}
}

func TestWordEscapes(t *testing.T) {
	ts := tokenize(`a\*b*c\ d`)
	if len(ts) != 1 || !slices.Equal(ts[0].parts, []string{"a*b", "c d"}) {
		t.Fatalf("%+v", ts)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		q    string
		want []string
	}{
		{"", nil},
		{"a b", nil},
		{")", []string{"Unmatched closing parenthesis"}},
		{"(a", []string{"Missing closing parenthesis"}},
		{"()", []string{"Empty parentheses"}},
		{"OR a", []string{"OR needs a term before it"}},
		{"a OR", []string{"OR needs a term after it"}},
		{"AND a", []string{"AND needs a term on both sides"}},
		{"a AND", []string{"AND needs a term on both sides"}},
		{"-", nil},
		{"NOT", []string{"Nothing to negate"}},
		{"NOT OR", []string{"Nothing to negate", "OR needs a term after it"}},
		{`"abc`, []string{"Missing closing quote"}},
		{"level:", []string{"Add a value after level:"}},
		{"status:>x", []string{"Expected a number after >"}},
		{"status:>", []string{"Expected a number after >"}},
		{"status:[1 2]", []string{"Write a range as [400 TO 499]"}},
		{"status:[1 TO 2", []string{"Missing closing ]"}},
		{"status:[a TO 2]", []string{"Range bounds are numbers or *"}},
		{"localhost:8080", []string{`Unknown facet "localhost". Write @localhost for a log attribute; searching it as text`}},
		{"foo:(a)", []string{`Unknown facet "foo". Write @foo for a log attribute`}},
		{"foo:", nil},
		{"constructor", []string{"Unexpected constructor"}},
	}
	for _, c := range cases {
		_, errs := Compile(c.q)
		var got []string
		for _, e := range errs {
			got = append(got, e.Message)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, want %q", c.q, got, c.want)
		}
	}
}

func TestErrorSpans(t *testing.T) {
	q := Parse("ok level:  )")
	e := q.Errors()
	if len(e) != 2 || e[0] != (ParseError{3, 9, "Add a value after level:"}) || e[1].Start != 11 {
		t.Fatalf("%+v", e)
	}
}

func TestMatcherNeverNil(t *testing.T) {
	for _, q := range []string{"", "   ", ")", "()", "AND", "level:"} {
		m, _ := Compile(q)
		if m == nil || !m(NewLine(Input{Line: "anything"})) {
			t.Errorf("%q should match everything", q)
		}
	}
}

func hit(q, line string, in ...Input) bool {
	i := Input{}
	if len(in) > 0 {
		i = in[0]
	}
	i.Line = line
	m, _ := Compile(q)
	return m(NewLine(i))
}

func TestMatchText(t *testing.T) {
	for _, c := range []struct {
		q, line string
		want    bool
	}{
		{"timeout", "Request TIMEOUT after 5s", true},
		{"db timeout", "timeout talking to db", true},
		{"db timeout", "timeout talking to cache", false},
		{`"connection refused"`, "dial: Connection Refused", true},
		{`"connection refused"`, "connection was refused", false},
		{"err*", "an error", true}, // free text is a substring, `*` only orders pieces
		{"a*c", "xxcxxaxx", false},
		{"a*c", "xaxxcx", true},
		{`a\*c`, "abc", false},
		{`a\*c`, "a*c", true},
		{"-error", "all fine", true},
		{"-error", "an error", false},
		{"a OR b", "xbx", true},
		{"(a OR b) c", "b c", true},
		{"(a OR b) c", "b d", false},
		{"é", "ÉCOLE", true},
		{"οδυσσευς", "ΟΔΥΣΣΕΥΣ", true},
		{"i̇stanbul", "İstanbul", true},
		{"stanbul", "\x1b[31mİstanbul\x1b[0m", true},
		{"31m", "\x1b[31mred\x1b[0m", false},
		{"Error:", "got Error: boom", true},
	} {
		if got := hit(c.q, c.line); got != c.want {
			t.Errorf("%q on %q = %v", c.q, c.line, got)
		}
	}
}

func TestMatchFacets(t *testing.T) {
	in := Input{Stream: "runtime", ServiceName: "Web", Replica: "2", DeploymentID: "d1"}
	for _, c := range []struct {
		q, line string
		want    bool
	}{
		{"service:web", "x", true},
		{"service:w*", "x", true},
		{"service:api", "x", false},
		{"stream:runtime", "x", true},
		{"replica:2", "x", true},
		{"replica:>=2", "x", true},
		{"replica:>2", "x", false},
		{"deployment:D1", "x", true},
		{"service:(api OR web)", "x", true},
		{"-service:web", "x", false},
		{"status:200", "GET /x 200 1ms", true},
		{"status:[400 TO 499]", "GET /x 404 1ms", true},
		{"status:{400 TO 404}", "GET /x 404 1ms", false},
		{"status:[* TO 299]", "GET /x 200 1ms", true},
		{"status:(404 OR 500)", "GET /x 500 1ms", true},
		{"status:200", "no status here", false},
		{"level:error", "ERROR boom", true},
		{"level:warn", "WARNING x", true},
		{"level:warning", "[warn] x", true},
		{"level:WARNING", `{"level":"warn"}`, true},
		{"level:fatal", "PANIC: x", true},
		{"level:info", "Error loading configuration", false},
		{"level:error", `{"level":50,"msg":"x"}`, true},
		{"level:warn", `{"level":40}`, true},
		{"level:debug", `{"level":10}`, true},
		{"level:info", `{"level":25}`, true},
		{"level:error", `{"level":60}`, true},
		{"level:error", `{"severity":"ERROR"}`, true},
		{"level:(error OR warn)", "level=warn x=1", true},
	} {
		if got := hit(c.q, c.line, in); got != c.want {
			t.Errorf("%q on %q = %v", c.q, c.line, got)
		}
	}
	// Replica, deployment and stream fall back to the line's own attributes.
	for _, c := range []struct{ q, line string }{
		{"replica:3", `{"replicaIndex":3}`},
		{"deployment:abc", `{"deploymentId":"abc"}`},
		{"stream:stderr", `{"stream":"stderr"}`},
		{"replica:3", `replica=3 a=1`},
	} {
		if !hit(c.q, c.line) {
			t.Errorf("%q should match %q", c.q, c.line)
		}
	}
	if hit("replica:3", `{"replica":3}`, Input{Replica: "1"}) {
		t.Error("input replica wins over the attribute")
	}
}

func TestMatchAttrs(t *testing.T) {
	json := `{"a":{"b":{"c":"Deep"}},"a.b":"flat","tags":["db","Slow"],"n":1500.5,"ok":true,"nul":null,"o":{"p":[5,50,500]}}`
	for _, c := range []struct {
		q    string
		want bool
	}{
		{"@a.b.c:deep", true},
		{"@a.b.c:de*", true},
		{"@a.b:flat", true}, // the exact dotted key wins
		{"@a.b.c:x", false},
		{"@tags:slow", true},
		{"@tags:d*", true},
		{"@tags:cache", false},
		{"@tags:(cache OR db)", true},
		{"@n:>1500", true},
		{"@n:>=1500.5", true},
		{"@n:<1500", false},
		{"@n:[1000 TO 2000]", true},
		{"@n:{1500.5 TO 2000]", false},
		{"@n:1500.5", true},
		{"@ok:true", true},
		{"@ok:TRUE", true}, // text of a boolean is compared lower-cased on the query side only
		{"@nul:*", false},
		{"@missing:*", false},
		{"@o.p:>=500", true},
		{"@o.p:[6 TO 49]", false},
		{"@o.p:50", true},
		{"@o:*", false},
		{"@a.b.c.d:*", false},
		{"-@tags:db", false},
	} {
		if got := hit(c.q, json); got != c.want {
			t.Errorf("%q = %v", c.q, got)
		}
	}
	for _, c := range []struct {
		q, line string
		want    bool
	}{
		{"@user:alice", `level=info user=alice code=1`, true},
		{"@user:ALICE", `level=info user=alice code=1`, true},
		{"@msg:*logged*", `msg="user logged in" a=1`, true},
		{"@http.status:503", `a=1 http.status=503`, true},
		{"@code:>=500", `a=1 code=503`, true},
		{"@a:b", `a=\"b\" c=1`, false},
		{"@a:x", `a="x" c=1`, true},
		{"@a:x", `a=x`, false}, // a single pair is not logfmt
		{"@method:GET", `GET /x 200`, true},
		{"@path:/x", `1.1.1.1 - - "GET /x HTTP/1.1" 200 5`, true},
		{"@status:200", `| 200 | 1ms | ip | GET "/x"`, true},
		{"@status:200", `{"status":null} GET /x 200`, true}, // not JSON: the trailing text is an access line
	} {
		if got := hit(c.q, c.line); got != c.want {
			t.Errorf("%q on %q = %v", c.q, c.line, got)
		}
	}
}

func TestLineLevel(t *testing.T) {
	for line, want := range map[string]string{
		"[ERROR] x":                         "error",
		"WARN x":                            "warn",
		"WARNING x":                         "warn",
		"ERR: x":                            "error",
		"Error loading":                     "",
		"2025-01-02T03:04:05Z INFO x":       "info",
		"2025-01-02 03:04:05,123 ERROR [m]": "error",
		"2025/01/02 03:04:05 [error] 29#29": "error",
		"[Warn] mixed":                      "warn",
		"INFO|x":                            "info",
		"INFOX":                             "",
		"WARNINGS x":                        "",
		"level=debug msg=x":                 "debug",
		"x lvl=trace":                       "debug",
		"severity: CRITICAL":                "error",
		`"level":"notice"`:                  "info",
		"level=verbose":                     "",
		`{"level":30}`:                      "info",
		`{"level":"FATAL"}`:                 "error",
		`{"level":true,"msg":"[error] x"}`:  "",
		"\x1b[31mERROR\x1b[0m x":            "error",
		"nothing":                           "",
		"":                                  "",
		"level=info level=error":            "error",
	} {
		if got := NewLine(Input{Line: line}).Level(); got != want {
			t.Errorf("Level(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestStatusOf(t *testing.T) {
	for line, want := range map[string]int{
		`10.0.0.1 - - [x] "GET /index.html HTTP/1.1" 200 1234`:  200,
		`"DELETE /api/x" 500 12`:                                500,
		`[GIN] 2025/01/01 | 404 | 12µs | 127.0.0.1 | POST "/n"`: 404,
		"GET /health 200 3ms":                                   200,
		"2025-01-01T00:00:00Z GET /m 301 1ms":                   301,
		"POST /u 099 x":                                         0,
		"GET /x 2000":                                           0,
		`{"status":503}`:                                        503,
		`{"statusCode":"404"}`:                                  404,
		`{"status":99,"msg":"GET /x 200 1ms"}`:                  0,
		`{"http":{"status_code":502}}`:                          502,
		`{"status":" 0x1F4 "}`:                                  500,
		`{"status":200.5}`:                                      0,
		"status=204 a=1":                                        204,
		"nothing":                                               0,
	} {
		if got := NewLine(Input{Line: line}).Status(); got != want {
			t.Errorf("Status(%q) = %d, want %d", line, got, want)
		}
	}
}

func TestLineText(t *testing.T) {
	l := NewLine(Input{Line: "\x1b[1;31mred\x1b[0m \x1b[bad plain"})
	if got := l.Text(); got != "red ad plain" {
		t.Fatalf("%q", got)
	}
}

func TestJSNumber(t *testing.T) {
	inf := math.Inf(1)
	for s, want := range map[string]float64{
		"":                     0,
		"  ":                   0,
		"5":                    5,
		" 5 ":                  5,
		"\ufeff7\u00a0":        7,
		"-5":                   -5,
		"+5":                   5,
		"5.":                   5,
		".5":                   0.5,
		"5.5e1":                55,
		"1E3":                  1000,
		"1e+3":                 1000,
		"0x1F":                 31,
		"0X1f":                 31,
		"0o17":                 15,
		"0b101":                5,
		"010":                  10,
		"Infinity":             inf,
		"+Infinity":            inf,
		"-Infinity":            -inf,
		"1e999":                inf,
		"0xFFFFFFFFFFFFFFFFFF": 4722366482869645213696,
	} {
		got, ok := jsToNumber(s)
		if !ok || got != want {
			t.Errorf("Number(%q) = %v, %v; want %v", s, got, ok, want)
		}
	}
	for _, s := range []string{
		".", "e5", "1e", "1e+", "0x", "0xg", "0b2", "0o8", "-0x10", "+0x10",
		"inf", "Inf", "infinity", "NaN", "nan", "1_000", "0x1p3", "1 2", "1,5",
		"--1", "+-1", "5..", "١٢", "0x1_0",
	} {
		if got, ok := jsToNumber(s); ok {
			t.Errorf("Number(%q) = %v, want NaN", s, got)
		}
	}
}

func TestJSNumString(t *testing.T) {
	for f, want := range map[float64]string{
		0:                      "0",
		math.Copysign(0, -1):   "0",
		1:                      "1",
		-1:                     "-1",
		200:                    "200",
		1.5:                    "1.5",
		0.1:                    "0.1",
		123456789012345680000:  "123456789012345680000",
		1e21:                   "1e+21",
		1.5e21:                 "1.5e+21",
		1e-6:                   "0.000001",
		1e-7:                   "1e-7",
		1.25e-7:                "1.25e-7",
		1.5e300:                "1.5e+300",
		0.000123:               "0.000123",
		100:                    "100",
		12345.678:              "12345.678",
		math.Inf(1):            "Infinity",
		math.Inf(-1):           "-Infinity",
		5e-324:                 "5e-324",
		1.7976931348623157e308: "1.7976931348623157e+308",
	} {
		if got := jsNumString(f); got != want {
			t.Errorf("String(%v) = %q, want %q", f, got, want)
		}
	}
}

func TestJSLower(t *testing.T) {
	for in, want := range map[string]string{
		"ABC":      "abc",
		"abc":      "abc",
		"İ":        "i\u0307",
		"ΟΔΥΣΣΕΥΣ": "οδυσσευς",
		"Σ":        "σ",
		"ΑΣ":       "ας",
		"ΑΣ Β":     "ας β",
		"ÉCOLE":    "école",
	} {
		if got := jsLower(in); got != want {
			t.Errorf("lower(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLazyLine(t *testing.T) {
	l := NewLine(Input{Line: `{"level":"warn"}`})
	if l.f != nil || l.haveText {
		t.Fatal("NewLine must not parse")
	}
	_ = l.Text()
	if l.f != nil {
		t.Fatal("Text must not parse fields")
	}
	if l.Level() != "warn" || l.f == nil {
		t.Fatal("Level parses fields")
	}
}

func TestRangeComparisonsOnText(t *testing.T) {
	// numeric comparison also reads numeric strings, never empty ones.
	if !hit(`@n:>=1`, `{"n":"2"}`) || hit(`@n:>=0`, `{"n":""}`) || hit(`@n:>=0`, `{"n":" "}`) {
		t.Fatal("numeric strings")
	}
	if !hit(`@n:>0`, `{"n":"0x10"}`) || hit(`@n:>0`, `{"n":"1_0"}`) {
		t.Fatal("JS number grammar")
	}
}

func BenchmarkTextOnly(b *testing.B) {
	m, _ := Compile("timeout")
	line := `{"level":"info","msg":"request completed","path":"/api/users","status":200}`
	b.ReportAllocs()
	for b.Loop() {
		m(NewLine(Input{Line: line}))
	}
}
