package proxy

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/pages"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---- A small evaluator of the Traefik rule subset firewalls compile to ----

// fwHTTPReq is a request as the router rules see it.
type fwHTTPReq struct {
	Host   string
	Path   string
	Method string
	Query  map[string][]string
	Header http.Header
	IP     netip.Addr
}

type ruleFn func(fwHTTPReq) bool

// ruleParser parses a Traefik rule: matchers, !, &&, || and parentheses, with
// && binding tighter than ||. An unknown matcher or malformed syntax is an
// error, so a rule the generator renders wrongly fails the test.
type ruleParser struct {
	s   string
	pos int
}

func parseTraefikRule(s string) (ruleFn, error) {
	p := &ruleParser{s: s}
	fn, err := p.or()
	if err != nil {
		return nil, fmt.Errorf("%w in %q", err, s)
	}
	p.space()
	if p.pos != len(p.s) {
		return nil, fmt.Errorf("trailing input at %d in %q", p.pos, s)
	}
	return fn, nil
}

func (p *ruleParser) space() {
	for p.pos < len(p.s) && p.s[p.pos] == ' ' {
		p.pos++
	}
}

func (p *ruleParser) eat(tok string) bool {
	p.space()
	if strings.HasPrefix(p.s[p.pos:], tok) {
		p.pos += len(tok)
		return true
	}
	return false
}

func (p *ruleParser) or() (ruleFn, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.eat("||") {
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(q fwHTTPReq) bool { return l(q) || r(q) }
	}
	return left, nil
}

func (p *ruleParser) and() (ruleFn, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.eat("&&") {
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(q fwHTTPReq) bool { return l(q) && r(q) }
	}
	return left, nil
}

func (p *ruleParser) unary() (ruleFn, error) {
	switch {
	case p.eat("!"):
		inner, err := p.unary()
		if err != nil {
			return nil, err
		}
		return func(q fwHTTPReq) bool { return !inner(q) }, nil
	case p.eat("("):
		inner, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.eat(")") {
			return nil, fmt.Errorf("missing ) at %d", p.pos)
		}
		return inner, nil
	}
	p.space()
	start := p.pos
	for p.pos < len(p.s) && (p.s[p.pos] >= 'A' && p.s[p.pos] <= 'Z' || p.s[p.pos] >= 'a' && p.s[p.pos] <= 'z') {
		p.pos++
	}
	name := p.s[start:p.pos]
	if name == "" || !p.eat("(") {
		return nil, fmt.Errorf("expected a matcher at %d", start)
	}
	var args []string
	for {
		if !p.eat("`") {
			return nil, fmt.Errorf("expected a backtick string at %d", p.pos)
		}
		end := strings.IndexByte(p.s[p.pos:], '`')
		if end < 0 {
			return nil, fmt.Errorf("unterminated string at %d", p.pos)
		}
		args = append(args, p.s[p.pos:p.pos+end])
		p.pos += end + 1
		if p.eat(",") {
			continue
		}
		if !p.eat(")") {
			return nil, fmt.Errorf("missing ) after %s arguments at %d", name, p.pos)
		}
		break
	}
	return traefikMatcher(name, args)
}

func traefikMatcher(name string, args []string) (ruleFn, error) {
	want := map[string]int{"Host": 1, "HostRegexp": 1, "PathPrefix": 1, "Path": 1, "PathRegexp": 1, "Method": 1, "ClientIP": 1,
		"Header": 2, "HeaderRegexp": 2, "Query": 2, "QueryRegexp": 2}[name]
	if want == 0 {
		return nil, fmt.Errorf("unsupported matcher %s", name)
	}
	if len(args) != want {
		return nil, fmt.Errorf("%s takes %d arguments, got %d", name, want, len(args))
	}
	var re *regexp.Regexp
	if strings.HasSuffix(name, "Regexp") {
		var err error
		if re, err = regexp.Compile(args[len(args)-1]); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	anyValue := func(vals []string, ok func(string) bool) bool {
		for _, v := range vals {
			if ok(v) {
				return true
			}
		}
		return false
	}
	switch name {
	case "Host":
		return func(q fwHTTPReq) bool { return strings.EqualFold(q.Host, args[0]) }, nil
	case "HostRegexp":
		return func(q fwHTTPReq) bool { return re.MatchString(strings.ToLower(q.Host)) }, nil
	case "PathPrefix":
		return func(q fwHTTPReq) bool { return strings.HasPrefix(q.Path, args[0]) }, nil
	case "Path":
		return func(q fwHTTPReq) bool { return q.Path == args[0] }, nil
	case "PathRegexp":
		return func(q fwHTTPReq) bool { return re.MatchString(q.Path) }, nil
	case "Method":
		return func(q fwHTTPReq) bool { return q.Method == args[0] }, nil
	case "ClientIP":
		p, err := netip.ParsePrefix(args[0])
		if err != nil {
			a, aerr := netip.ParseAddr(args[0])
			if aerr != nil {
				return nil, fmt.Errorf("ClientIP(%q): %w", args[0], aerr)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		return func(q fwHTTPReq) bool { return q.IP.IsValid() && p.Contains(q.IP.Unmap()) }, nil
	case "Header":
		// The key is matched as written: a rule must name the canonical form,
		// as net/http stores it.
		return func(q fwHTTPReq) bool {
			return anyValue(q.Header[args[0]], func(v string) bool { return v == args[1] })
		}, nil
	case "HeaderRegexp":
		return func(q fwHTTPReq) bool { return anyValue(q.Header[args[0]], re.MatchString) }, nil
	case "Query":
		return func(q fwHTTPReq) bool {
			return anyValue(q.Query[args[0]], func(v string) bool { return v == args[1] })
		}, nil
	default: // QueryRegexp
		return func(q fwHTTPReq) bool { return anyValue(q.Query[args[0]], re.MatchString) }, nil
	}
}

// ---- Reading a rendered config ----

type tRouter struct {
	EntryPoints []string `json:"entryPoints"`
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	Middlewares []string `json:"middlewares"`
	Priority    int      `json:"priority"`
	TLS         any      `json:"tls"`
}

// effectivePriority is Traefik's: the explicit priority, else the rule length.
func (r *tRouter) effectivePriority() int {
	if r.Priority != 0 {
		return r.Priority
	}
	return len(r.Rule)
}

type tConfig struct {
	HTTP struct {
		Routers     map[string]*tRouter       `json:"routers"`
		Middlewares map[string]map[string]any `json:"middlewares"`
	} `json:"http"`
	rules map[string]ruleFn
}

func buildConfig(t *testing.T, routes []agentprotocol.Route, opts Options) *tConfig {
	t.Helper()
	var c tConfig
	require.NoError(t, json.Unmarshal(mustBuild(t, routes, opts), &c))
	c.rules = map[string]ruleFn{}
	return &c
}

// winner is the router Traefik picks for req on entrypoint ep: the highest
// priority of the routers whose rule matches. Two matching routers of the top
// priority are an ambiguity unless neither is a firewall router.
func (c *tConfig) winner(t *testing.T, ep string, req fwHTTPReq) (string, *tRouter) {
	t.Helper()
	best, bestPrio := []string{}, -1
	for name, r := range c.HTTP.Routers {
		if !slices.Contains(r.EntryPoints, ep) {
			continue
		}
		fn, ok := c.rules[name]
		if !ok {
			var err error
			fn, err = parseTraefikRule(r.Rule)
			require.NoError(t, err, "router %s", name)
			c.rules[name] = fn
		}
		if !fn(req) {
			continue
		}
		switch p := r.effectivePriority(); {
		case p > bestPrio:
			best, bestPrio = []string{name}, p
		case p == bestPrio:
			best = append(best, name)
		}
	}
	if len(best) == 0 {
		return "", nil
	}
	sort.Strings(best)
	if len(best) > 1 {
		for _, n := range best {
			require.NotContains(t, n, "-fw", "request %+v matches routers %v at priority %d: ambiguous", req, best, bestPrio)
		}
	}
	return best[0], c.HTTP.Routers[best[0]]
}

// ---- Sample requests ----

type reqPools struct {
	hosts, paths, methods, ips []string
	headers, queries           map[string][]string // name -> values ("-" is absent)
}

var samplePools = reqPools{
	hosts:   []string{"app.example.com", "app.example.com", "app.localhost", "other.example.com"},
	paths:   []string{"/", "/admin", "/admin/users", "/administrator", "/wp-admin/x", "/login", "/login/", "/Login", "/.env", "/robots.txt", "/a/b.php", "/a.php/x", "/a.PHP", "/v2/admin/x", "/x/v2/admin", "/shop", "/shop/cart", "/shop/ADMIN", "/shop/a.b", "/(x)", "/../etc", "/Admin", "/shop/robots.txt"},
	methods: []string{"GET", "GET", "HEAD", "POST", "DELETE", "PUT"},
	ips:     []string{"203.0.113.5", "198.51.100.7", "198.51.100.8", "2001:db8::1", "2001:dba::1", "10.1.2.3", "192.0.2.1", "::ffff:203.0.113.9", "::ffff:192.0.2.1"},
	headers: map[string][]string{
		"User-Agent": {"-", "Mozilla/5.0", "BadBot/1.0", "xbadbotx", "curl/8.1", "Scrapy", "mozilla"},
		"X-Api-Key":  {"-", "k1", "k2", "K1", "k1x"},
		"Referer":    {"-", "https://evil.example/", "https://EVIL.example/x", "https://good.example/"},
		"X-Id":       {"-", "123", "1234", "12a"},
		"X-Debug":    {"-", "-", "", "1"},
		"X-Extra":    {"-", "x"},
	},
	queries: map[string][]string{
		"token": {"-", "a", "b", "ab"},
		"q":     {"-", "1 UNION select", "union", "x"},
		"id":    {"-", "42", "4x", ""},
		"debug": {"-", "-", "", "1"},
	},
}

func (p reqPools) random(rng *rand.Rand) (fwHTTPReq, agentprotocol.FirewallRequest) {
	pick := func(s []string) string { return s[rng.IntN(len(s))] }
	h := http.Header{}
	for _, name := range sortedKeys(p.headers) {
		vals := p.headers[name]
		if v := pick(vals); v != "-" {
			h.Add(name, v)
			if rng.IntN(10) == 0 { // a second value of the same header
				if v2 := pick(vals); v2 != "-" {
					h.Add(name, v2)
				}
			}
		}
	}
	q := map[string][]string{}
	for _, name := range sortedKeys(p.queries) {
		if v := pick(p.queries[name]); v != "-" {
			q[name] = []string{v}
			if rng.IntN(10) == 0 {
				if v2 := pick(p.queries[name]); v2 != "-" {
					q[name] = append(q[name], v2)
				}
			}
		}
	}
	ip := netip.MustParseAddr(pick(p.ips))
	method, path := pick(p.methods), pick(p.paths)
	return fwHTTPReq{Host: pick(p.hosts), Path: path, Method: method, Query: q, Header: h, IP: ip},
		agentprotocol.FirewallRequest{ClientIP: ip, Method: method, Path: path, Query: q, Header: h}
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- Firewalls under test ----

func fwc(field, op string, values ...string) agentprotocol.FirewallCondition {
	return agentprotocol.FirewallCondition{Field: field, Op: op, Values: values}
}

func fwn(field, op, name string, values ...string) agentprotocol.FirewallCondition {
	return agentprotocol.FirewallCondition{Field: field, Op: op, Name: name, Values: values}
}

func notc(c agentprotocol.FirewallCondition) agentprotocol.FirewallCondition {
	c.Not = true
	return c
}

func fwr(id, action string, conds ...agentprotocol.FirewallCondition) agentprotocol.FirewallRule {
	return agentprotocol.FirewallRule{ID: id, Name: "rule " + id, Action: action, Conditions: conds}
}

const (
	blockA = agentprotocol.FirewallBlock
	allowA = agentprotocol.FirewallAllow
)

// everyOpFirewall uses every non-country field and operator, Not, and several
// values, with allow rules above and below block rules.
func everyOpFirewall() *agentprotocol.Firewall {
	off := fwr("off", blockA, fwc("method", "in", "GET"))
	off.Disabled = true
	return &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{
		fwr("office", allowA, fwc("ip", "in", "203.0.113.0/24", "198.51.100.7", "2001:db8::/32")),
		off,
		fwr("bots", blockA, fwc("userAgent", "contains", "BadBot", "scrapy"), notc(fwc("path", "prefix", "/robots.txt", "/shop/robots.txt"))),
		fwr("admin", blockA, fwc("path", "prefix", "/admin", "/wp-admin", "/shop/ADMIN"), notc(fwc("method", "in", "GET", "HEAD"))),
		fwr("env", blockA, fwc("path", "exact", "/.env", "/login")),
		fwr("key", blockA, fwn("header", "equals", "x-api-key", "k1", "k2")),
		fwr("ref", blockA, fwn("header", "contains", "referer", "Evil.Example")),
		fwr("id", blockA, fwn("header", "regex", "X-Id", `^[0-9]{3}$`), fwn("query", "present", "debug")),
		fwr("dbg", blockA, fwn("header", "present", "x-debug")),
		fwr("tok", blockA, fwn("query", "equals", "token", "a", "b")),
		fwr("qc", blockA, fwn("query", "contains", "q", "UNION"), notc(fwn("query", "regex", "id", `^\d+$`))),
		fwr("php", blockA, fwc("path", "regex", `\.php$`, `^/v[0-9]+/admin`)),
		fwr("pc", allowA, fwc("path", "contains", "(x)", "a.b"), notc(fwc("method", "in", "DELETE"))),
		fwr("curl", blockA, fwc("userAgent", "regex", `^curl/`)),
		fwr("last", blockA, fwc("path", "prefix", "/shop"), notc(fwn("header", "present", "X-Extra")), notc(fwc("ip", "in", "10.0.0.0/8"))),
	}}
}

// condTemplates are the conditions random firewalls draw from; values are
// drawn from the pool of each template.
var condTemplates = []struct {
	field, op, name string
	values          []string
}{
	{"ip", "in", "", []string{"203.0.113.0/24", "198.51.100.7", "2001:db8::/32", "10.0.0.0/8", "192.0.2.0/24"}},
	{"method", "in", "", []string{"GET", "POST", "DELETE", "PUT", "HEAD"}},
	{"path", "prefix", "", []string{"/admin", "/login", "/v2", "/shop", "/.env", "/a"}},
	{"path", "exact", "", []string{"/", "/login", "/.env", "/admin", "/shop"}},
	{"path", "contains", "", []string{"ADMIN", "../", "php", ".env", "a.b", "(x)"}},
	{"path", "regex", "", []string{`\.php$`, `^/v[0-9]+/`, `admin`, `^/$`, `[A-Z]`}},
	{"header", "equals", "x-api-key", []string{"k1", "k2", "k1x"}},
	{"header", "contains", "REFERER", []string{"evil.example", "good"}},
	{"header", "regex", "X-Id", []string{`^[0-9]{3}$`, `[a-z]`}},
	{"header", "present", "X-Debug", nil},
	{"header", "present", "x-extra", nil},
	{"userAgent", "contains", "", []string{"badbot", "curl", "Moz"}},
	{"userAgent", "regex", "", []string{`^Mozilla`, `^curl/`, `bot`}},
	{"query", "equals", "token", []string{"a", "b", "ab"}},
	{"query", "contains", "q", []string{"union", "x"}},
	{"query", "regex", "id", []string{`^\d+$`, `x`}},
	{"query", "present", "debug", nil},
}

func randomFirewall(rng *rand.Rand) *agentprotocol.Firewall {
	fw := &agentprotocol.Firewall{}
	for i := range 1 + rng.IntN(6) {
		action := blockA
		if rng.IntN(3) == 0 {
			action = allowA
		}
		r := agentprotocol.FirewallRule{ID: fmt.Sprintf("r%d", i), Name: "random", Action: action, Disabled: rng.IntN(10) == 0}
		for range 1 + rng.IntN(3) {
			tpl := condTemplates[rng.IntN(len(condTemplates))]
			c := agentprotocol.FirewallCondition{Field: tpl.field, Op: tpl.op, Name: tpl.name, Not: rng.IntN(3) == 0}
			if len(tpl.values) > 0 {
				for _, j := range rng.Perm(len(tpl.values))[:1+rng.IntN(min(3, len(tpl.values)))] {
					c.Values = append(c.Values, tpl.values[j])
				}
			}
			r.Conditions = append(r.Conditions, c)
		}
		fw.Rules = append(fw.Rules, r)
	}
	return fw
}

// ---- Route shapes under test ----

type fwShape struct {
	name   string
	routes func(fw *agentprotocol.Firewall) []agentprotocol.Route
	opts   Options
	// hosts are the host names the route serves; prefix its path prefix.
	hosts  []string
	prefix string
	eps    []string
	// noIP drops the firewall's address rules: behind a tunnel they go to the
	// edge guard (see the guard tests), not to routers.
	noIP bool
}

// shapeFirewall is fw as the shape can render it as routers.
func shapeFirewall(sh fwShape, fw *agentprotocol.Firewall) *agentprotocol.Firewall {
	if !sh.noIP {
		return fw
	}
	out := &agentprotocol.Firewall{}
	for _, r := range fw.Rules {
		hasIP := false
		for _, c := range r.Conditions {
			hasIP = hasIP || c.Field == agentprotocol.FirewallFieldIP
		}
		if !hasIP {
			out.Rules = append(out.Rules, r)
		}
	}
	if len(out.Active()) == 0 {
		out.Rules = append(out.Rules, fwr("fallback", blockA, fwc("method", "in", "DELETE")))
	}
	return out
}

func fwShapes() []fwShape {
	withFW := func(r agentprotocol.Route, fw *agentprotocol.Firewall) []agentprotocol.Route {
		r.Firewall = fw
		return []agentprotocol.Route{r}
	}
	acme := Options{ACMEEmail: "ops@example.com"}
	return []fwShape{
		{
			name: "domain",
			routes: func(fw *agentprotocol.Firewall) []agentprotocol.Route {
				return withFW(ruleRoute(rule(domA, "app.example.com", nil)), fw)
			},
			opts: acme, hosts: []string{"app.example.com"}, eps: []string{EntryWebSecure, EntryWeb},
		},
		{
			name: "domain with a path prefix",
			routes: func(fw *agentprotocol.Firewall) []agentprotocol.Route {
				return withFW(ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.PathPrefix = "/shop" })), fw)
			},
			opts: acme, hosts: []string{"app.example.com"}, prefix: "/shop", eps: []string{EntryWebSecure, EntryWeb},
		},
		{
			name: "domain without TLS",
			routes: func(fw *agentprotocol.Firewall) []agentprotocol.Route {
				return withFW(ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.TLSMode = agentprotocol.TLSNone })), fw)
			},
			opts: acme, hosts: []string{"app.example.com"}, eps: []string{EntryWeb},
		},
		{
			name: "domain without forced https",
			routes: func(fw *agentprotocol.Firewall) []agentprotocol.Route {
				return withFW(ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.Options.ForceHTTPS = new(false) })), fw)
			},
			opts: acme, hosts: []string{"app.example.com"}, eps: []string{EntryWebSecure, EntryWeb},
		},
		{
			name: "tunnelled domain",
			routes: func(fw *agentprotocol.Firewall) []agentprotocol.Route {
				return withFW(ruleRoute(rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) { d.Tunnel = true })), fw)
			},
			opts: acme, hosts: []string{"app.example.com"}, eps: []string{EntryTunnel}, noIP: true,
		},
		{
			name: "hosts without a rule",
			routes: func(fw *agentprotocol.Firewall) []agentprotocol.Route {
				return withFW(agentprotocol.Route{ServiceID: svcApp, Hosts: []string{"app.example.com", "app.localhost"}, Upstreams: []string{"nc-app-aaaaaaaa-0:8080"}}, fw)
			},
			opts: acme, hosts: []string{"app.example.com", "app.localhost"}, eps: []string{EntryWebSecure, EntryWeb},
		},
	}
}

// outcome classifies the router that serves a request, and checks that a
// firewall router has the shape its rule's action calls for.
func classifyRouter(t *testing.T, c *tConfig, name string, r *tRouter, pages bool) (kind, ruleID string) {
	t.Helper()
	i := strings.LastIndex(name, "-fw")
	if i < 0 {
		return "pass", ""
	}
	base := c.HTTP.Routers[name[:i]]
	require.NotNil(t, base, "firewall router %s has a base router", name)
	blocked := r.Service == "noop@internal" && slices.Equal(r.Middlewares, []string{firewallDenyMiddleware})
	if pages {
		blocked = r.Service == pagesService && slices.Equal(r.Middlewares, []string{firewallBlockMiddleware})
	}
	if blocked {
		return "block", name[i+len("-fw"):]
	}
	require.Equal(t, base.Service, r.Service, "an allow router serves like %s", name[:i])
	require.Equal(t, base.Middlewares, r.Middlewares, "an allow router serves like %s", name[:i])
	return "allow", name[i+len("-fw"):]
}

// checkEquivalence renders fw for the shape and compares, for every sample
// request on every entrypoint, the router Traefik would pick with the
// decision of agentprotocol.FirewallMatcher.
func checkEquivalence(t *testing.T, sh fwShape, fw *agentprotocol.Firewall, pagesOn bool, reqs int, rng *rand.Rand) map[string]int {
	t.Helper()
	opts := sh.opts
	opts.Pages = pagesOn
	cfg := buildConfig(t, sh.routes(fw), opts)
	m, err := agentprotocol.CompileFirewall(fw)
	require.NoError(t, err)

	seen := map[string]int{}
	for range reqs {
		hreq, freq := samplePools.random(rng)
		inScope := slices.Contains(sh.hosts, hreq.Host) && strings.HasPrefix(hreq.Path, sh.prefix)
		for _, ep := range sh.eps {
			name, r := cfg.winner(t, ep, hreq)
			desc := fmt.Sprintf("%s pages=%v ep=%s req=%+v", sh.name, pagesOn, ep, hreq)
			if !inScope {
				require.NotContains(t, name, "-fw", "a firewall router took a request outside its domains: %s", desc)
				continue
			}
			require.NotNil(t, r, "no router serves an in-scope request: %s", desc)
			kind, id := classifyRouter(t, cfg, name, r, pagesOn)
			wantID, wantAction := m.Decide(freq)
			want := map[string]string{"": "pass", blockA: "block", allowA: "allow"}[wantAction]
			require.Equal(t, want, kind, "%s: matcher says rule %q %q, router %s", desc, wantID, wantAction, name)
			require.Equal(t, wantID, id, desc)
			seen[wantID+"/"+want]++
		}
	}
	return seen
}

func TestFirewallRoutersEqualFirewallMatcher(t *testing.T) {
	fw := everyOpFirewall()
	for _, sh := range fwShapes() {
		for _, pagesOn := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pages=%v", sh.name, pagesOn), func(t *testing.T) {
				rng := rand.New(rand.NewPCG(7, 11))
				combined := shapeFirewall(sh, fw)
				checkEquivalence(t, sh, combined, pagesOn, 3000, rng)

				// Each active rule alone, so none is shadowed by the rules above
				// it: every compiled condition decides some samples and lets
				// others through. (A path-prefixed domain only sees paths under
				// its prefix, which not every rule can match: equivalence still
				// holds there, coverage is not asked.)
				for _, r := range combined.Active() {
					single := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{r}}
					seen := checkEquivalence(t, sh, single, pagesOn, 1500, rng)
					if sh.prefix == "" {
						require.Positive(t, seen[r.ID+"/"+map[string]string{blockA: "block", allowA: "allow"}[r.Action]], "no sample reached rule %s: %v", r.ID, seen)
						require.Positive(t, seen["/pass"], "rule %s matched every sample: %v", r.ID, seen)
					}
				}
			})
		}
	}
}

func TestFirewallRoutersEqualFirewallMatcherRandomFirewalls(t *testing.T) {
	shapes := fwShapes()
	rng := rand.New(rand.NewPCG(2024, 1))
	for i := range 250 {
		fw := randomFirewall(rng)
		sh := shapes[i%len(shapes)]
		fw = shapeFirewall(sh, fw)
		pagesOn := i%2 == 0
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			checkEquivalence(t, sh, fw, pagesOn, 80, rng)
		})
	}
}

func TestFirewallEvaluatorSelfCheck(t *testing.T) {
	// The evaluator is test machinery: pin its handling of the syntax the
	// generator emits, including precedence and negation.
	req := fwHTTPReq{Host: "A.example.com", Path: "/x/y", Method: "GET", Header: http.Header{"X-A": {"1", "2"}}, Query: map[string][]string{"k": {"v"}}, IP: netip.MustParseAddr("::ffff:10.0.0.9")}
	for rule, want := range map[string]bool{
		"Host(`a.example.com`)":                                 true,
		"HostRegexp(`^[^.]+\\.example\\.com$`)":                 true,
		"!(Host(`b`))":                                          true,
		"Host(`b`) || Host(`a.example.com`) && Method(`POST`)":  false,
		"(Host(`b`) || Host(`a.example.com`)) && Method(`GET`)": true,
		"PathPrefix(`/x`) && !PathPrefix(`/x/z`)":               true,
		"Path(`/x`)":                                            false,
		"Header(`X-A`, `2`)":                                    true,
		"Header(`x-a`, `2`)":                                    false,
		"HeaderRegexp(`X-A`, `.*`)":                             true,
		"HeaderRegexp(`X-B`, `.*`)":                             false,
		"Query(`k`, `v`) && QueryRegexp(`k`, `^v$`)":            true,
		"ClientIP(`10.0.0.0/8`)":                                true,
		"ClientIP(`10.0.0.9`) && !ClientIP(`2001:db8::/32`)":    true,
		"((PathRegexp(`^/x/`)))":                                true,
		"!(Method(`GET`) || Method(`POST`)) || Host(`nomatch`)": false,
	} {
		fn, err := parseTraefikRule(rule)
		require.NoError(t, err, rule)
		require.Equal(t, want, fn(req), rule)
	}
	for _, bad := range []string{"", "Host(`a`", "Host(a)", "Bogus(`a`)", "Host(`a`) &&", "Header(`a`)", "PathRegexp(`(`)", "Host(`a`) Host(`b`)"} {
		_, err := parseTraefikRule(bad)
		require.Error(t, err, bad)
	}
}

// ---- Exact rendering ----

const (
	fwSvc = "nc-" + svcApp
	fwDom = fwSvc + "-daaaaaaaa"
	bt    = "`"
)

func domainConfig(t *testing.T, fw *agentprotocol.Firewall, opts Options, mut func(*agentprotocol.DomainRule)) *tConfig {
	t.Helper()
	r := ruleRoute(rule(domA, "app.example.com", mut))
	r.Firewall = fw
	return buildConfig(t, []agentprotocol.Route{r}, opts)
}

func TestFirewallConditionMatchersRendering(t *testing.T) {
	q := func(s ...string) string { return bt + strings.Join(s, bt+", "+bt) + bt }
	cases := []struct {
		cond agentprotocol.FirewallCondition
		want string
	}{
		{fwc("ip", "in", "203.0.113.0/24", "198.51.100.7", "2001:db8::/32"), "(ClientIP(" + q("203.0.113.0/24") + ") || ClientIP(" + q("198.51.100.7") + ") || ClientIP(" + q("2001:db8::/32") + "))"},
		{fwc("method", "in", "GET", "POST"), "(Method(" + q("GET") + ") || Method(" + q("POST") + "))"},
		{fwc("path", "prefix", "/admin", "/wp"), "(PathPrefix(" + q("/admin") + ") || PathPrefix(" + q("/wp") + "))"},
		{fwc("path", "exact", "/login"), "(Path(" + q("/login") + "))"},
		{fwc("path", "contains", "a.b", "(x)"), "(PathRegexp(" + q(`(?i)a\.b`) + ") || PathRegexp(" + q(`(?i)\(x\)`) + "))"},
		{fwc("path", "regex", `\.php$`), "(PathRegexp(" + q(`\.php$`) + "))"},
		{fwn("header", "equals", "x-api-key", "k1", "k2"), "(Header(" + q("X-Api-Key", "k1") + ") || Header(" + q("X-Api-Key", "k2") + "))"},
		{fwn("header", "contains", "referer", "Evil"), "(HeaderRegexp(" + q("Referer", "(?i)Evil") + "))"},
		{fwn("header", "regex", "X-Id", `^\d+$`), "(HeaderRegexp(" + q("X-Id", `^\d+$`) + "))"},
		{fwn("header", "present", "x-debug"), "(HeaderRegexp(" + q("X-Debug", ".*") + "))"},
		{fwc("userAgent", "contains", "badbot"), "(HeaderRegexp(" + q("User-Agent", "(?i)badbot") + "))"},
		{fwc("userAgent", "regex", `^curl/`), "(HeaderRegexp(" + q("User-Agent", `^curl/`) + "))"},
		{fwn("query", "equals", "Token", "a"), "(Query(" + q("Token", "a") + "))"},
		{fwn("query", "contains", "q", "UNION"), "(QueryRegexp(" + q("q", "(?i)UNION") + "))"},
		{fwn("query", "regex", "id", `^\d+$`), "(QueryRegexp(" + q("id", `^\d+$`) + "))"},
		{fwn("query", "present", "debug"), "(QueryRegexp(" + q("debug", ".*") + "))"},
		{notc(fwc("method", "in", "GET")), "!(Method(" + q("GET") + "))"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("a", blockA, c.cond)}}
			cfg := domainConfig(t, fw, Options{ACMEEmail: "ops@example.com"}, nil)
			r := cfg.HTTP.Routers[fwDom+"-https-fwa"]
			require.NotNil(t, r)
			require.Equal(t, "(Host("+bt+"app.example.com"+bt+")) && "+c.want, r.Rule)
		})
	}

	t.Run("conditions are joined with &&", func(t *testing.T) {
		fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("a", blockA,
			fwc("method", "in", "POST"), notc(fwc("path", "prefix", "/ok")), fwn("header", "present", "X-A"))}}
		r := domainConfig(t, fw, Options{ACMEEmail: "ops@example.com"}, nil).HTTP.Routers[fwDom+"-https-fwa"]
		require.Equal(t, "(Host("+bt+"app.example.com"+bt+")) && (Method("+bt+"POST"+bt+")) && !(PathPrefix("+bt+"/ok"+bt+")) && (HeaderRegexp("+bt+"X-A"+bt+", "+bt+".*"+bt+"))", r.Rule)
	})
}

func TestFirewallRouterShapes(t *testing.T) {
	fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{
		fwr("a", allowA, fwc("ip", "in", "203.0.113.0/24")),
		fwr("b", blockA, fwc("path", "prefix", "/admin")),
		fwr("c", blockA, fwc("method", "in", "DELETE")),
	}}
	opts := Options{ACMEEmail: "ops@example.com"}
	cfg := domainConfig(t, fw, opts, func(d *agentprotocol.DomainRule) {
		d.Options.Compress = true
		d.Options.RateLimit = &agentprotocol.RateLimit{Average: 10, PeriodSeconds: 1, Burst: 5}
	})

	for i, id := range []string{"a", "b", "c"} {
		for _, kind := range []string{"https", "http"} {
			name := fwDom + "-" + kind + "-fw" + id
			r := cfg.HTTP.Routers[name]
			base := cfg.HTTP.Routers[fwDom+"-"+kind]
			require.NotNil(t, r, name)
			require.Equal(t, 1<<30-i, r.Priority, "%s: rule %d outranks the rules below it", name, i)
			require.Equal(t, base.EntryPoints, r.EntryPoints)
			require.Equal(t, base.TLS, r.TLS, "the copy terminates TLS like its base")
			require.True(t, strings.HasPrefix(r.Rule, "("+base.Rule+")"), r.Rule)
		}
	}
	require.NotNil(t, cfg.HTTP.Routers[fwDom+"-https"].TLS)

	// An allow copy serves exactly like the base router, rules below skipped.
	allow, base := cfg.HTTP.Routers[fwDom+"-https-fwa"], cfg.HTTP.Routers[fwDom+"-https"]
	require.Equal(t, base.Service, allow.Service)
	require.Equal(t, base.Middlewares, allow.Middlewares)
	require.Contains(t, base.Middlewares, fwDom+"-ratelimit")
	// ... also on the plain-HTTP router, which redirects to https.
	require.Equal(t, []string{redirectMiddleware}, cfg.HTTP.Routers[fwDom+"-http-fwa"].Middlewares)
	require.Equal(t, "noop@internal", cfg.HTTP.Routers[fwDom+"-http-fwa"].Service)

	// A block copy answers the pages server's 403 page, never reaching the
	// service or its rate limit.
	for _, id := range []string{"b", "c"} {
		for _, kind := range []string{"https", "http"} {
			r := cfg.HTTP.Routers[fwDom+"-"+kind+"-fw"+id]
			require.Equal(t, "noop@internal", r.Service, "no pages server: a bare 403")
			require.Equal(t, []string{firewallDenyMiddleware}, r.Middlewares)
		}
	}
	deny := cfg.HTTP.Middlewares[firewallDenyMiddleware]["ipAllowList"].(map[string]any)
	require.Equal(t, []any{neverMatch}, deny["sourceRange"])
	require.EqualValues(t, 403, deny["rejectStatusCode"])

	opts.Pages = true
	cfg = domainConfig(t, fw, opts, nil)
	for _, id := range []string{"b", "c"} {
		for _, kind := range []string{"https", "http"} {
			r := cfg.HTTP.Routers[fwDom+"-"+kind+"-fw"+id]
			require.Equal(t, pagesService, r.Service)
			require.Equal(t, []string{firewallBlockMiddleware}, r.Middlewares)
		}
	}
	require.Equal(t, map[string]any{"path": pages.ErrorPath(403)}, cfg.HTTP.Middlewares[firewallBlockMiddleware]["replacePath"])
	require.NotContains(t, cfg.HTTP.Middlewares, firewallDenyMiddleware)
	allow = cfg.HTTP.Routers[fwDom+"-https-fwa"]
	require.Equal(t, cfg.HTTP.Routers[fwDom+"-https"].Service, allow.Service)
	require.Equal(t, cfg.HTTP.Routers[fwDom+"-https"].Middlewares, allow.Middlewares)
	require.Equal(t, errorsMiddleware, allow.Middlewares[0], "an allowed request still gets the error pages")
}

func TestFirewallNeverTouchesProbeRouters(t *testing.T) {
	fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("a", blockA, notc(fwc("path", "prefix", "/nothing")))}}
	for _, mut := range []func(*agentprotocol.DomainRule){nil, func(d *agentprotocol.DomainRule) { d.Tunnel = true }} {
		cfg := domainConfig(t, fw, Options{ACMEEmail: "ops@example.com", Pages: true}, mut)
		probe := cfg.HTTP.Routers[fwDom+"-probe"]
		require.NotNil(t, probe)
		require.Equal(t, fwSvc, probe.Service, "verification still reaches the service")
		for name, r := range cfg.HTTP.Routers {
			if strings.Contains(name, "-fw") {
				require.NotContains(t, r.EntryPoints, EntryProbe, name)
				require.False(t, strings.Contains(name, "probe"), "no firewall copy of a probe router: %s", name)
			}
		}
	}

	// Same for hosts served without a rule.
	r := agentprotocol.Route{ServiceID: svcApp, Hosts: []string{"app.example.com"}, Upstreams: []string{"nc-app-aaaaaaaa-0:8080"}}
	r.Firewall = fw
	cfg := buildConfig(t, []agentprotocol.Route{r}, Options{ACMEEmail: "ops@example.com"})
	require.NotNil(t, cfg.HTTP.Routers[fwSvc+"-probe"])
	for name := range cfg.HTTP.Routers {
		require.False(t, strings.Contains(name, "probe-fw"), name)
	}
	require.NotNil(t, cfg.HTTP.Routers[fwSvc+"-https-fwa"])
	require.NotNil(t, cfg.HTTP.Routers[fwSvc+"-http-fwa"])
}

func TestFirewallHostsWithoutRuleRouterNames(t *testing.T) {
	fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("a", blockA, fwc("method", "in", "DELETE"))}}
	r := agentprotocol.Route{ServiceID: svcApp, Hosts: []string{"app.example.com", "app.localhost"}, Upstreams: []string{"nc-app-aaaaaaaa-0:8080"}}
	r.Firewall = fw
	cfg := buildConfig(t, []agentprotocol.Route{r}, Options{ACMEEmail: "ops@example.com"})
	require.Equal(t, "(Host("+bt+"app.example.com"+bt+")) && (Method("+bt+"DELETE"+bt+"))", cfg.HTTP.Routers[fwSvc+"-https-fwa"].Rule)
	require.Equal(t, "(Host("+bt+"app.localhost"+bt+")) && (Method("+bt+"DELETE"+bt+"))", cfg.HTTP.Routers[fwSvc+"-https-local-fwa"].Rule)
	require.Equal(t, "(Host("+bt+"app.example.com"+bt+") || Host("+bt+"app.localhost"+bt+")) && (Method("+bt+"DELETE"+bt+"))", cfg.HTTP.Routers[fwSvc+"-http-fwa"].Rule)
}

// ---- Exclusions ----

func withoutFirewalls(routes []agentprotocol.Route) []agentprotocol.Route {
	out := slices.Clone(routes)
	for i := range out {
		out[i].Firewall = nil
	}
	return out
}

// requireRoutingKept checks that the firewalls of a table never change which
// router serves a request, except by a firewall router taking over from its
// own base router (the one that served it without firewalls). It returns the
// name of the router that serves req.
func requireRoutingKept(t *testing.T, with, without *tConfig, ep string, req fwHTTPReq) string {
	t.Helper()
	name, _ := with.winner(t, ep, req)
	base, _ := without.winner(t, ep, req)
	if i := strings.LastIndex(name, "-fw"); i >= 0 {
		require.Equal(t, base, name[:i], "%+v: a firewall router took a request its base router does not serve", req)
	} else {
		require.Equal(t, base, name, "%+v: a firewall changed the router that serves this request", req)
	}
	return name
}

// routerMatches evaluates a router's rule alone, ignoring priorities.
func routerMatches(t *testing.T, r *tRouter, req fwHTTPReq) bool {
	t.Helper()
	require.NotNil(t, r)
	fn, err := parseTraefikRule(r.Rule)
	require.NoError(t, err)
	return fn(req)
}

func TestFirewallRouterStaysOffMoreSpecificPrefixesOfTheHost(t *testing.T) {
	// Route A: the whole host, firewalled (blocks everything). Route B: the
	// same host under /api, no firewall. A's firewall must not take /api.
	everything := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("all", blockA, notc(fwc("path", "exact", "/never")))}}
	a := ruleRoute(rule(domA, "a.example.com", nil))
	a.Firewall = everything
	b := agentprotocol.Route{ServiceID: svcWhoami, Hosts: []string{"a.example.com"}, Upstreams: []string{"nc-whoami-bbbbbbbb-0:80"},
		RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{rule(domB, "a.example.com", func(d *agentprotocol.DomainRule) { d.PathPrefix = "/api" })}}}
	c := agentprotocol.Route{ServiceID: "33333333-3333-3333-3333-333333333333", Hosts: []string{"a.example.com"}, Upstreams: []string{"nc-c-cccccccc-0:80"},
		RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{rule(domC, "a.example.com", func(d *agentprotocol.DomainRule) { d.PathPrefix = "/api/v2" })}}}

	cfg := buildConfig(t, []agentprotocol.Route{a, b, c}, Options{ACMEEmail: "ops@example.com"})
	r := cfg.HTTP.Routers[fwDom+"-https-fwall"]
	require.Equal(t, "(Host("+bt+"a.example.com"+bt+")) && !PathPrefix("+bt+"/api"+bt+") && !PathPrefix("+bt+"/api/v2"+bt+") && !(Path("+bt+"/never"+bt+"))", r.Rule)

	without := buildConfig(t, withoutFirewalls([]agentprotocol.Route{a, b, c}), Options{ACMEEmail: "ops@example.com"})
	for path, wantFW := range map[string]bool{
		"/": true, "/other": true, "/ap": true, "/shop/api/v2": true,
		"/apix": false, "/api": false, "/api/users": false, "/api/v1/x": false, "/api/v2/x": false, "/api/v2": false,
	} {
		name := requireRoutingKept(t, cfg, without, EntryWebSecure, fwHTTPReq{Host: "a.example.com", Path: path, Method: "GET"})
		require.Equal(t, wantFW, strings.Contains(name, "-fw"), "%s served by %s", path, name)
	}

	// B's own firewall is scoped to its prefix and excludes C's.
	b.Firewall = &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("bb", blockA, fwc("method", "in", "DELETE"))}}
	cfg = buildConfig(t, []agentprotocol.Route{a, b, c}, Options{ACMEEmail: "ops@example.com"})
	require.Equal(t, "(Host("+bt+"a.example.com"+bt+") && PathPrefix("+bt+"/api"+bt+")) && !PathPrefix("+bt+"/api/v2"+bt+") && (Method("+bt+"DELETE"+bt+"))",
		cfg.HTTP.Routers["nc-"+svcWhoami+"-dbbbbbbbb-https-fwbb"].Rule)
	name, _ := cfg.winner(t, EntryWebSecure, fwHTTPReq{Host: "a.example.com", Path: "/api/v2/x", Method: "DELETE"})
	require.Equal(t, "nc-33333333-3333-3333-3333-333333333333-dcccccccc-https", name, "C serves /api/v2 unfirewalled")
	name, _ = cfg.winner(t, EntryWebSecure, fwHTTPReq{Host: "a.example.com", Path: "/api/x", Method: "DELETE"})
	require.Equal(t, "nc-"+svcWhoami+"-dbbbbbbbb-https-fwbb", name)
	name, _ = cfg.winner(t, EntryWebSecure, fwHTTPReq{Host: "a.example.com", Path: "/api/x", Method: "GET"})
	require.Equal(t, "nc-"+svcWhoami+"-dbbbbbbbb-https", name, "A's firewall has no claim on /api, B's has no matching rule: B serves")
}

func TestFirewallExclusionsOfHostsWithoutRule(t *testing.T) {
	// Hosts served by default (no domain rule) in a firewalled route, while
	// another route serves a.example.com/api: the exclusion names the host.
	fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("all", blockA, notc(fwc("path", "exact", "/never")))}}
	a := agentprotocol.Route{ServiceID: svcApp, Hosts: []string{"a.example.com", "b.example.com"}, Upstreams: []string{"nc-app-aaaaaaaa-0:8080"}}
	a.Firewall = fw
	b := agentprotocol.Route{ServiceID: svcWhoami, Hosts: []string{"a.example.com"}, Upstreams: []string{"nc-whoami-bbbbbbbb-0:80"},
		RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{rule(domB, "a.example.com", func(d *agentprotocol.DomainRule) { d.PathPrefix = "/api" })}}}
	cfg := buildConfig(t, []agentprotocol.Route{a, b}, Options{ACMEEmail: "ops@example.com"})

	require.Equal(t, "(Host("+bt+"a.example.com"+bt+") || Host("+bt+"b.example.com"+bt+")) && !(Host("+bt+"a.example.com"+bt+") && PathPrefix("+bt+"/api"+bt+")) && !(Path("+bt+"/never"+bt+"))",
		cfg.HTTP.Routers[fwSvc+"-https-fwall"].Rule)
	without := buildConfig(t, withoutFirewalls([]agentprotocol.Route{a, b}), Options{ACMEEmail: "ops@example.com"})
	for _, c := range []struct {
		host, path string
		wantFW     bool
	}{
		{"a.example.com", "/x", true},
		{"b.example.com", "/api/x", true},
		{"a.example.com", "/api/x", false},
		{"a.example.com", "/api", false},
	} {
		name := requireRoutingKept(t, cfg, without, EntryWebSecure, fwHTTPReq{Host: c.host, Path: c.path, Method: "GET"})
		require.Equal(t, c.wantFW, strings.Contains(name, "-fw"), "%s%s served by %s", c.host, c.path, name)
	}
	fwRouter := cfg.HTTP.Routers[fwSvc+"-https-fwall"]
	require.True(t, routerMatches(t, fwRouter, fwHTTPReq{Host: "b.example.com", Path: "/api/x", Method: "GET"}))
	require.False(t, routerMatches(t, fwRouter, fwHTTPReq{Host: "a.example.com", Path: "/api/x", Method: "GET"}), "B's /api is not A's to block")
	require.True(t, routerMatches(t, fwRouter, fwHTTPReq{Host: "a.example.com", Path: "/ap", Method: "GET"}))
}

func TestFirewallWildcardRouterStaysOffExactHostsItCovers(t *testing.T) {
	everything := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("all", blockA, notc(fwc("path", "exact", "/never")))}}
	wild := ruleRoute(rule(domA, "*.example.com", nil))
	wild.Firewall = everything
	www := agentprotocol.Route{ServiceID: svcWhoami, Hosts: []string{"www.example.com"}, Upstreams: []string{"nc-whoami-bbbbbbbb-0:80"}}
	nested := agentprotocol.Route{ServiceID: "33333333-3333-3333-3333-333333333333", Hosts: []string{"a.b.example.com"}, Upstreams: []string{"nc-c-cccccccc-0:80"}}
	other := agentprotocol.Route{ServiceID: "44444444-4444-4444-4444-444444444444", Hosts: []string{"www.other.org"}, Upstreams: []string{"nc-d-dddddddd-0:80"}}

	cfg := buildConfig(t, []agentprotocol.Route{wild, www, nested, other}, Options{})
	r := cfg.HTTP.Routers[fwDom+"-https-fwall"]
	require.Equal(t, "(HostRegexp("+bt+`^[^.]+\.example\.com$`+bt+")) && !Host("+bt+"www.example.com"+bt+") && !(Path("+bt+"/never"+bt+"))", r.Rule,
		"only the exact host one label below the wildcard is excluded, not a nested name nor another domain")

	without := buildConfig(t, withoutFirewalls([]agentprotocol.Route{wild, www, nested, other}), Options{})
	for _, c := range []struct {
		host   string
		wantFW bool
	}{
		{"x.example.com", true},
		{"www.example.com", false},
		{"a.b.example.com", false},
		{"example.com", false},
		{"www.other.org", false},
	} {
		name := requireRoutingKept(t, cfg, without, EntryWebSecure, fwHTTPReq{Host: c.host, Path: "/", Method: "GET"})
		require.Equal(t, c.wantFW, strings.Contains(name, "-fw"), "%s served by %s", c.host, name)
	}
	fwRouter := cfg.HTTP.Routers[fwDom+"-https-fwall"]
	require.True(t, routerMatches(t, fwRouter, fwHTTPReq{Host: "x.example.com", Path: "/", Method: "GET"}))
	require.False(t, routerMatches(t, fwRouter, fwHTTPReq{Host: "www.example.com", Path: "/", Method: "GET"}), "an exact host of another route is not the wildcard's to block")
	require.False(t, routerMatches(t, fwRouter, fwHTTPReq{Host: "a.b.example.com", Path: "/", Method: "GET"}), "the wildcard covers one label")
}

// ---- Edge guard ----

func countryFirewall() *agentprotocol.Firewall {
	return &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{
		fwr("geo", blockA, notc(fwc("country", "in", "DE", "AT")), fwc("path", "prefix", "/admin")),
		fwr("bots", blockA, fwc("userAgent", "contains", "badbot")),
	}}
}

func noFirewallRouters(t *testing.T, cfg *tConfig) {
	t.Helper()
	for name := range cfg.HTTP.Routers {
		require.NotContains(t, name, "-fw", "a guarded firewall renders no routers")
	}
}

func TestFirewallWithCountriesRendersGuardMiddleware(t *testing.T) {
	for _, pagesOn := range []bool{false, true} {
		cfg := domainConfig(t, countryFirewall(), Options{ACMEEmail: "ops@example.com", Pages: pagesOn}, func(d *agentprotocol.DomainRule) {
			d.Options.Compress = true
		})
		noFirewallRouters(t, cfg)
		mw := fwDom + "-guard"
		require.Equal(t, map[string]any{"address": agentprotocol.GuardCheckURL(svcApp, agentprotocol.GuardClientPeer)},
			cfg.HTTP.Middlewares[mw]["forwardAuth"])
		require.Equal(t, "http://127.0.0.1:83/v1/check/"+svcApp+"?client=peer", agentprotocol.GuardCheckURL(svcApp, "peer"))
		chain := cfg.HTTP.Routers[fwDom+"-https"].Middlewares
		require.Contains(t, chain, mw)
		want := []string{mw, fwDom + "-compress"}
		if pagesOn {
			want = append([]string{errorsMiddleware}, want...)
		}
		require.Equal(t, want, chain, "pages=%v: the guard comes before every other rule", pagesOn)
		require.Contains(t, cfg.HTTP.Routers[fwDom+"-http"].Middlewares, redirectMiddleware)
		require.NotContains(t, cfg.HTTP.Routers[fwDom+"-http"].Middlewares, mw, "the redirect needs no guard")
		require.NotContains(t, cfg.HTTP.Routers[fwDom+"-probe"].Middlewares, mw, "verification does not go through the guard")
		require.NotContains(t, cfg.HTTP.Middlewares, firewallBlockMiddleware)
	}
}

func TestFirewallGuardForHostsWithoutRule(t *testing.T) {
	r := agentprotocol.Route{ServiceID: svcApp, Hosts: []string{"app.example.com", "app.localhost"}, Upstreams: []string{"nc-app-aaaaaaaa-0:8080"}}
	r.Firewall = countryFirewall()
	cfg := buildConfig(t, []agentprotocol.Route{r}, Options{ACMEEmail: "ops@example.com"})
	noFirewallRouters(t, cfg)
	mw := fwSvc + "-guard"
	require.Equal(t, map[string]any{"address": agentprotocol.GuardCheckURL(svcApp, "peer")}, cfg.HTTP.Middlewares[mw]["forwardAuth"])
	require.Equal(t, []string{mw}, cfg.HTTP.Routers[fwSvc+"-https"].Middlewares)
	require.Equal(t, []string{mw}, cfg.HTTP.Routers[fwSvc+"-https-local"].Middlewares)
	require.NotContains(t, cfg.HTTP.Routers[fwSvc+"-probe"].Middlewares, mw)
}

func TestFirewallGuardAfterTheOriginLockAndReadsCloudflareClient(t *testing.T) {
	for _, pagesOn := range []bool{false, true} {
		r := lockedRoute(t, false)
		r.Firewall = countryFirewall()
		cfg := buildConfig(t, []agentprotocol.Route{r}, Options{ACMEEmail: "ops@example.com", Pages: pagesOn})
		noFirewallRouters(t, cfg)

		locked, plain := "nc-"+svcApp+"-daaaaaaaa", "nc-"+svcApp+"-dbbbbbbbb"
		chain := cfg.HTTP.Routers[locked+"-https"].Middlewares
		lockAt := slices.IndexFunc(chain, func(m string) bool { return strings.HasPrefix(m, lockMiddlewarePrefix) })
		guardAt := slices.Index(chain, locked+"-guard")
		require.GreaterOrEqual(t, lockAt, 0, "%v", chain)
		require.Equal(t, lockAt+1, guardAt, "the guard directly follows the lock: %v", chain)
		if pagesOn {
			require.Equal(t, errorsMiddleware, chain[0])
		}
		require.Equal(t, map[string]any{"address": agentprotocol.GuardCheckURL(svcApp, agentprotocol.GuardClientCloudflare)},
			cfg.HTTP.Middlewares[locked+"-guard"]["forwardAuth"], "a locked domain's peer is Cloudflare")

		// The unlocked sibling of the same route reads the peer.
		require.Equal(t, map[string]any{"address": agentprotocol.GuardCheckURL(svcApp, agentprotocol.GuardClientPeer)},
			cfg.HTTP.Middlewares[plain+"-guard"]["forwardAuth"])
		require.Contains(t, cfg.HTTP.Routers[plain+"-https"].Middlewares, plain+"-guard")
		require.NotContains(t, cfg.HTTP.Routers[locked+"-probe"].Middlewares, locked+"-guard")
	}
}

func TestFirewallGuardOnTunnelledDomainReadsCloudflareClient(t *testing.T) {
	cfg := domainConfig(t, countryFirewall(), Options{ACMEEmail: "ops@example.com"}, func(d *agentprotocol.DomainRule) { d.Tunnel = true })
	noFirewallRouters(t, cfg)
	require.Equal(t, map[string]any{"address": agentprotocol.GuardCheckURL(svcApp, agentprotocol.GuardClientCloudflare)},
		cfg.HTTP.Middlewares[fwDom+"-guard"]["forwardAuth"])
	require.Equal(t, []string{fwDom + "-guard"}, cfg.HTTP.Routers[fwDom+"-tunnel"].Middlewares)
}

func TestFirewallAddressRulesNeedTheGuardOnlyBehindAHeaderClient(t *testing.T) {
	ipOnly := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("ip", blockA, fwc("ip", "in", "198.51.100.0/24"))}}
	pathOnly := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("p", blockA, fwc("path", "prefix", "/admin"))}}
	opts := Options{ACMEEmail: "ops@example.com"}
	locked := func(d *agentprotocol.DomainRule) { d.Tunnel = true }

	// A direct domain: ClientIP routers see the real peer.
	cfg := domainConfig(t, ipOnly, opts, nil)
	require.Contains(t, cfg.HTTP.Routers, fwDom+"-https-fwip")
	require.NotContains(t, cfg.HTTP.Middlewares, fwDom+"-guard")

	// Behind a tunnel the peer is cloudflared: the guard reads Cf-Connecting-IP.
	cfg = domainConfig(t, ipOnly, opts, locked)
	noFirewallRouters(t, cfg)
	require.Contains(t, cfg.HTTP.Middlewares, fwDom+"-guard")

	// A firewall without addresses or countries never needs the guard.
	cfg = domainConfig(t, pathOnly, opts, locked)
	require.Contains(t, cfg.HTTP.Routers, fwDom+"-tunnel-fwp")
	require.NotContains(t, cfg.HTTP.Middlewares, fwDom+"-guard")
	require.Equal(t, "(Host("+bt+"app.example.com"+bt+")) && (PathPrefix("+bt+"/admin"+bt+"))", cfg.HTTP.Routers[fwDom+"-tunnel-fwp"].Rule)
	require.Equal(t, []string{EntryTunnel}, cfg.HTTP.Routers[fwDom+"-tunnel-fwp"].EntryPoints)

	// One locked domain in the route puts the whole route behind the guard.
	r := ruleRoute(rule(domA, "app.example.com", nil), rule(domB, "t.example.com", locked))
	r.Firewall = ipOnly
	cfg = buildConfig(t, []agentprotocol.Route{r}, opts)
	noFirewallRouters(t, cfg)
	require.Contains(t, cfg.HTTP.Middlewares, "nc-"+svcApp+"-daaaaaaaa-guard")
	require.Contains(t, cfg.HTTP.Middlewares, "nc-"+svcApp+"-dbbbbbbbb-guard")
}

func TestFirewallDisabledOnlyRendersNothing(t *testing.T) {
	off := fwr("off", blockA, fwc("method", "in", "GET"))
	off.Disabled = true
	offGeo := fwr("geo", blockA, fwc("country", "in", "CN"))
	offGeo.Disabled = true
	fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{off, offGeo}}

	for _, pagesOn := range []bool{false, true} {
		opts := Options{ACMEEmail: "ops@example.com", Pages: pagesOn}
		with := domainConfig(t, fw, opts, nil)
		without := domainConfig(t, nil, opts, nil)
		require.Equal(t, without, with, "pages=%v", pagesOn)
		noFirewallRouters(t, with)
		for name := range with.HTTP.Middlewares {
			require.NotContains(t, name, "guard")
		}
		require.NotContains(t, with.HTTP.Middlewares, firewallBlockMiddleware)
		require.NotContains(t, with.HTTP.Middlewares, firewallDenyMiddleware)

		// The rendered bytes are identical, not just the parsed shape.
		r1, r2 := ruleRoute(rule(domA, "app.example.com", nil)), ruleRoute(rule(domA, "app.example.com", nil))
		r1.Firewall = fw
		require.Equal(t, mustBuild(t, []agentprotocol.Route{r2}, opts), mustBuild(t, []agentprotocol.Route{r1}, opts))
	}
}

func TestFirewallDisabledRulesAreSkippedWhenRendering(t *testing.T) {
	off := fwr("off", blockA, fwc("method", "in", "GET"))
	off.Disabled = true
	fw := &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{off, fwr("on", blockA, fwc("method", "in", "POST")), fwr("on2", allowA, fwc("method", "in", "PUT"))}}
	cfg := domainConfig(t, fw, Options{ACMEEmail: "ops@example.com"}, nil)
	require.NotContains(t, cfg.HTTP.Routers, fwDom+"-https-fwoff")
	require.Equal(t, 1<<30, cfg.HTTP.Routers[fwDom+"-https-fwon"].Priority, "the first active rule gets the top priority")
	require.Equal(t, 1<<30-1, cfg.HTTP.Routers[fwDom+"-https-fwon2"].Priority)
}

// ---- Validation ----

func TestValidateRoutesFirewall(t *testing.T) {
	good := ruleRoute(rule(domA, "app.example.com", nil))
	good.Firewall = &agentprotocol.Firewall{Rules: []agentprotocol.FirewallRule{fwr("a", blockA, fwc("method", "in", "POST"))}}
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{good}, agentprotocol.UpstreamPolicy{}))

	bad := map[string]*agentprotocol.Firewall{
		"bad regex":    {Rules: []agentprotocol.FirewallRule{fwr("a", blockA, fwc("path", "regex", "(unclosed"))}},
		"bad op":       {Rules: []agentprotocol.FirewallRule{fwr("a", blockA, fwc("ip", "prefix", "10.0.0.0/8"))}},
		"backtick":     {Rules: []agentprotocol.FirewallRule{fwr("a", blockA, fwc("path", "contains", "a`b"))}},
		"no condition": {Rules: []agentprotocol.FirewallRule{fwr("a", blockA)}},
		"duplicate id": {Rules: []agentprotocol.FirewallRule{fwr("a", blockA, fwc("method", "in", "GET")), fwr("a", blockA, fwc("method", "in", "GET"))}},
	}
	for name, fw := range bad {
		t.Run(name, func(t *testing.T) {
			r := good
			r.Firewall = fw
			err := ValidateRoutes([]agentprotocol.Route{r}, agentprotocol.UpstreamPolicy{})
			require.ErrorContains(t, err, "firewall")
			_, err = BuildConfig([]agentprotocol.Route{r}, Options{})
			require.Error(t, err, "an invalid firewall never renders")
		})
	}

	t.Run("a firewall on a tcp route", func(t *testing.T) {
		tcp := agentprotocol.Route{
			ServiceID: svcApp, Upstreams: []string{"nc-pg-aaaaaaaa-0:5432"},
			RouteExt: agentprotocol.RouteExt{Protocol: agentprotocol.ProtoTCP, ListenPort: 5432},
		}
		require.NoError(t, ValidateRoutes([]agentprotocol.Route{tcp}, agentprotocol.UpstreamPolicy{}))
		tcp.Firewall = good.Firewall
		err := ValidateRoutes([]agentprotocol.Route{tcp}, agentprotocol.UpstreamPolicy{})
		require.ErrorContains(t, err, "only apply to http routes")
		udp := tcp
		udp.Protocol = agentprotocol.ProtoUDP
		require.ErrorContains(t, ValidateRoutes([]agentprotocol.Route{udp}, agentprotocol.UpstreamPolicy{}), "only apply to http routes")
	})
}
