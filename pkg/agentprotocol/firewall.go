package agentprotocol

import (
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// Service firewall: ordered rules evaluated against every request to the
// service's domains (Route.Firewall). The first rule whose conditions all
// match decides: "block" answers 403, "allow" lets the request through and
// skips the rules below it. A request no rule matches goes through.
//
// The agent compiles a firewall without country conditions into Traefik
// routers (no extra hop). A firewall that matches countries, or client
// addresses on a domain whose visitor address arrives in a header (Cloudflare
// Tunnel, origin locked to Cloudflare), is evaluated by the edge guard instead
// (forwardAuth to a sidecar in the proxy's network namespace) with
// FirewallMatcher, which implements exactly the semantics of the compiled
// routers.

// CapFirewall is advertised by agents that enforce Route.Firewall. The control
// plane refuses firewall rules (422 agent_too_old) for servers without it.
const CapFirewall = "proxy.firewall"

// Rule actions (FirewallRule.Action).
const (
	FirewallBlock = "block"
	FirewallAllow = "allow"
)

// Condition fields (FirewallCondition.Field).
const (
	FirewallFieldIP        = "ip"
	FirewallFieldCountry   = "country"
	FirewallFieldPath      = "path"
	FirewallFieldMethod    = "method"
	FirewallFieldHeader    = "header"
	FirewallFieldUserAgent = "userAgent"
	FirewallFieldQuery     = "query"
)

// Condition operators (FirewallCondition.Op).
const (
	// FirewallOpIn: the request's value is one of Values (ip: inside one of
	// the CIDRs; country, method: equal).
	FirewallOpIn = "in"
	// FirewallOpPrefix: the path starts with one of Values.
	FirewallOpPrefix = "prefix"
	// FirewallOpExact: the path is one of Values.
	FirewallOpExact = "exact"
	// FirewallOpEquals: a value of the header or query parameter is one of Values.
	FirewallOpEquals = "equals"
	// FirewallOpContains: the value contains one of Values, ignoring case.
	FirewallOpContains = "contains"
	// FirewallOpRegex: the value matches one of Values (Go regular
	// expressions, unanchored, like Traefik's *Regexp matchers).
	FirewallOpRegex = "regex"
	// FirewallOpPresent: the header or query parameter is present.
	FirewallOpPresent = "present"
)

// Limits of a firewall.
const (
	MaxFirewallRules      = 50
	MaxFirewallConditions = 10
	MaxFirewallValues     = 50
	// MaxFirewallIPValues bounds the address list of one ip condition.
	MaxFirewallIPValues  = MaxAccessCIDRs
	maxFirewallValueLen  = 512
	maxFirewallNameLen   = 80
	maxFirewallFieldName = 128
)

// FirewallMethods are the methods a method condition may name.
var FirewallMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"}

// firewallOps lists the operators each field accepts.
var firewallOps = map[string][]string{
	FirewallFieldIP:        {FirewallOpIn},
	FirewallFieldCountry:   {FirewallOpIn},
	FirewallFieldMethod:    {FirewallOpIn},
	FirewallFieldPath:      {FirewallOpPrefix, FirewallOpExact, FirewallOpContains, FirewallOpRegex},
	FirewallFieldHeader:    {FirewallOpEquals, FirewallOpContains, FirewallOpRegex, FirewallOpPresent},
	FirewallFieldUserAgent: {FirewallOpContains, FirewallOpRegex},
	FirewallFieldQuery:     {FirewallOpEquals, FirewallOpContains, FirewallOpRegex, FirewallOpPresent},
}

// FirewallOps returns the operators field accepts (nil for an unknown field).
func FirewallOps(field string) []string { return slices.Clone(firewallOps[field]) }

// The edge guard: a sidecar in the proxy container's network namespace that
// evaluates the firewalls Traefik routers cannot express (countries, visitor
// addresses from Cf-Connecting-IP), answering Traefik's forwardAuth.
const (
	// GuardPort is the guard's loopback port inside the proxy's network
	// namespace. Below 1024 like ProxyTunnelPort, so no tcp/udp route can
	// ever listen on it (ValidateListenPort).
	GuardPort = 83
	// GuardContainer is the guard's container name.
	GuardContainer = "nebulactrl-guard"
	// GuardClientPeer and GuardClientCloudflare say where the guard reads the
	// visitor address: X-Forwarded-For as Traefik sets it (the TCP peer), or
	// Cf-Connecting-IP (tunnelled and Cloudflare-locked domains, whose peer is
	// Cloudflare).
	GuardClientPeer       = "peer"
	GuardClientCloudflare = "cf"
)

// GuardCheckURL is the forwardAuth address of a route's firewall.
func GuardCheckURL(routeID, client string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/v1/check/%s?client=%s", GuardPort, routeID, client)
}

// Firewall is the ordered rule list of a service.
type Firewall struct {
	Rules []FirewallRule `json:"rules"`
}

// FirewallRule is one rule: every condition must match.
type FirewallRule struct {
	// ID is a short stable identifier ([a-z0-9]{1,16}), unique in the list.
	// The control plane gives a rule without one an id when it is saved.
	ID     string `json:"id" required:"false" doc:"1-16 lowercase letters or digits, unique in the list; generated when omitted"`
	Name   string `json:"name"`
	Action string `json:"action" enum:"block,allow"`
	// Disabled rules are kept but never evaluated.
	Disabled   bool                `json:"disabled,omitempty"`
	Conditions []FirewallCondition `json:"conditions"`
}

// FirewallCondition is one test of a request. Several Values match when any
// of them does; Not inverts the result.
type FirewallCondition struct {
	Field string `json:"field" enum:"ip,country,path,method,header,userAgent,query"`
	Op    string `json:"op" enum:"in,prefix,exact,equals,contains,regex,present"`
	// Name is the header or query parameter name (header and query only).
	Name   string   `json:"name,omitempty"`
	Values []string `json:"values,omitempty"`
	Not    bool     `json:"not,omitempty"`
}

// Active returns the rules that are evaluated (not disabled), in order.
func (f *Firewall) Active() []FirewallRule {
	if f == nil {
		return nil
	}
	out := make([]FirewallRule, 0, len(f.Rules))
	for _, r := range f.Rules {
		if !r.Disabled {
			out = append(out, r)
		}
	}
	return out
}

// uses reports whether an active rule has a condition on field.
func (f *Firewall) uses(field string) bool {
	for _, r := range f.Active() {
		for _, c := range r.Conditions {
			if c.Field == field {
				return true
			}
		}
	}
	return false
}

// UsesCountry reports whether an active rule matches countries (it needs the
// server's GeoIP database and the edge guard).
func (f *Firewall) UsesCountry() bool { return f.uses(FirewallFieldCountry) }

// UsesIP reports whether an active rule matches client addresses.
func (f *Firewall) UsesIP() bool { return f.uses(FirewallFieldIP) }

// NeedsGuard reports whether the firewall of a route must be evaluated by the
// edge guard rather than compiled into routers: it matches countries, or it
// matches client addresses while the visitor address of some domain arrives in
// a header (headerClientIP: tunnelled or Cloudflare-locked domains).
func (f *Firewall) NeedsGuard(headerClientIP bool) bool {
	return f.UsesCountry() || (headerClientIP && f.UsesIP())
}

var firewallRuleID = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// headerName is an RFC 9110 field name, minus the backtick (a legal token
// character, but the compiled router rules quote names with it).
var headerName = regexp.MustCompile("^[!#$%&'*+.^_|~0-9A-Za-z-]+$")

// Validate checks the firewall against the limits and the operator matrix.
// Values are checked as the compiled routers need them: no backtick or line
// break (Traefik rule quoting), paths start with "/", regular expressions
// compile.
func (f Firewall) Validate() error {
	if len(f.Rules) > MaxFirewallRules {
		return fmt.Errorf("at most %d firewall rules", MaxFirewallRules)
	}
	seen := map[string]bool{}
	for i, r := range f.Rules {
		at := fmt.Sprintf("rule %d", i+1)
		if !firewallRuleID.MatchString(r.ID) {
			return fmt.Errorf("%s: id must be 1-16 lowercase letters or digits", at)
		}
		if seen[r.ID] {
			return fmt.Errorf("%s: id %q is used twice", at, r.ID)
		}
		seen[r.ID] = true
		if n := strings.TrimSpace(r.Name); n == "" || len(n) > maxFirewallNameLen {
			return fmt.Errorf("%s: name must be 1-%d characters", at, maxFirewallNameLen)
		}
		if r.Action != FirewallBlock && r.Action != FirewallAllow {
			return fmt.Errorf("%s: action must be block or allow", at)
		}
		if len(r.Conditions) == 0 {
			return fmt.Errorf("%s: add at least one condition", at)
		}
		if len(r.Conditions) > MaxFirewallConditions {
			return fmt.Errorf("%s: at most %d conditions", at, MaxFirewallConditions)
		}
		for j, c := range r.Conditions {
			if err := c.validate(); err != nil {
				return fmt.Errorf("%s, condition %d: %w", at, j+1, err)
			}
		}
	}
	return nil
}

func (c FirewallCondition) validate() error {
	ops, ok := firewallOps[c.Field]
	if !ok {
		return fmt.Errorf("unknown field %q", c.Field)
	}
	if !slices.Contains(ops, c.Op) {
		return fmt.Errorf("%s cannot use %q (use %s)", c.Field, c.Op, strings.Join(ops, ", "))
	}
	named := c.Field == FirewallFieldHeader || c.Field == FirewallFieldQuery
	switch {
	case named && c.Name == "":
		return fmt.Errorf("name the %s", c.Field)
	case named && len(c.Name) > maxFirewallFieldName:
		return fmt.Errorf("the %s name is longer than %d characters", c.Field, maxFirewallFieldName)
	case !named && c.Name != "":
		return fmt.Errorf("%s takes no name", c.Field)
	case c.Field == FirewallFieldHeader && !headerName.MatchString(c.Name):
		return fmt.Errorf("%q is not a header name", c.Name)
	case c.Field == FirewallFieldQuery && !plainText(c.Name):
		return fmt.Errorf("the query parameter name cannot contain backticks or line breaks")
	}
	if c.Op == FirewallOpPresent {
		if len(c.Values) > 0 {
			return fmt.Errorf("present takes no values")
		}
		return nil
	}
	limit := MaxFirewallValues
	if c.Field == FirewallFieldIP {
		limit = MaxFirewallIPValues
	}
	if len(c.Values) == 0 {
		return fmt.Errorf("add at least one value")
	}
	if len(c.Values) > limit {
		return fmt.Errorf("at most %d values", limit)
	}
	for _, v := range c.Values {
		if v == "" || len(v) > maxFirewallValueLen {
			return fmt.Errorf("values must be 1-%d characters", maxFirewallValueLen)
		}
		if !plainText(v) {
			return fmt.Errorf("%q: values cannot contain backticks or line breaks", v)
		}
		if err := c.validateValue(v); err != nil {
			return err
		}
	}
	return nil
}

func (c FirewallCondition) validateValue(v string) error {
	switch {
	case c.Field == FirewallFieldIP:
		if _, err := parsePrefix(v); err != nil {
			return err
		}
	case c.Field == FirewallFieldCountry:
		if !isISO2(v) {
			return fmt.Errorf("%q is not a two-letter country code (ISO 3166-1, upper case)", v)
		}
	case c.Field == FirewallFieldMethod:
		if !slices.Contains(FirewallMethods, v) {
			return fmt.Errorf("%q is not an HTTP method (%s)", v, strings.Join(FirewallMethods, ", "))
		}
	case c.Field == FirewallFieldPath && (c.Op == FirewallOpPrefix || c.Op == FirewallOpExact):
		if !strings.HasPrefix(v, "/") {
			return fmt.Errorf("%q: a path starts with /", v)
		}
	case c.Op == FirewallOpRegex:
		if _, err := regexp.Compile(v); err != nil {
			return fmt.Errorf("%q is not a valid regular expression: %w", v, err)
		}
	}
	return nil
}

func plainText(s string) bool { return !strings.ContainsAny(s, "`\r\n") }

func isISO2(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

// ContainsPattern is the regular expression of a contains value: the literal,
// ignoring case. The compiled routers and FirewallMatcher both use it, so
// "contains" means the same thing in both.
func ContainsPattern(v string) string { return "(?i)" + regexp.QuoteMeta(v) }

// FirewallRequest is what FirewallMatcher looks at.
type FirewallRequest struct {
	ClientIP netip.Addr
	// Country is the ISO code of ClientIP ("" when unknown).
	Country string
	Method  string
	// Path is the decoded URL path.
	Path   string
	Query  map[string][]string
	Header http.Header
}

// FirewallMatcher evaluates a compiled firewall.
type FirewallMatcher struct {
	rules []matcherRule
}

type matcherRule struct {
	id, action string
	conds      []matcherCond
}

type matcherCond struct {
	c        FirewallCondition
	prefixes []netip.Prefix
	res      []*regexp.Regexp
}

// CompileFirewall prepares f's active rules for evaluation. f must be valid.
func CompileFirewall(f *Firewall) (*FirewallMatcher, error) {
	if f == nil {
		return &FirewallMatcher{}, nil
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	m := &FirewallMatcher{}
	for _, r := range f.Active() {
		mr := matcherRule{id: r.ID, action: r.Action}
		for _, c := range r.Conditions {
			mc := matcherCond{c: c}
			for _, v := range c.Values {
				switch {
				case c.Field == FirewallFieldIP:
					p, _ := parsePrefix(v)
					mc.prefixes = append(mc.prefixes, p)
				case c.Op == FirewallOpRegex:
					mc.res = append(mc.res, regexp.MustCompile(v))
				case c.Op == FirewallOpContains:
					mc.res = append(mc.res, regexp.MustCompile(ContainsPattern(v)))
				}
			}
			mr.conds = append(mr.conds, mc)
		}
		m.rules = append(m.rules, mr)
	}
	return m, nil
}

// Decide returns the rule that decides req and its action; ("", "") when no
// rule matches (the request goes through).
func (m *FirewallMatcher) Decide(req FirewallRequest) (ruleID, action string) {
	for _, r := range m.rules {
		all := true
		for _, c := range r.conds {
			if c.match(req) == c.c.Not {
				all = false
				break
			}
		}
		if all {
			return r.id, r.action
		}
	}
	return "", ""
}

func (c matcherCond) match(req FirewallRequest) bool {
	switch c.c.Field {
	case FirewallFieldIP:
		if !req.ClientIP.IsValid() {
			return false
		}
		ip := req.ClientIP.Unmap()
		for _, p := range c.prefixes {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	case FirewallFieldCountry:
		return req.Country != "" && slices.Contains(c.c.Values, req.Country)
	case FirewallFieldMethod:
		return slices.Contains(c.c.Values, strings.ToUpper(req.Method))
	case FirewallFieldPath:
		return c.matchValue([]string{req.Path})
	case FirewallFieldHeader:
		return c.matchValue(req.Header.Values(c.c.Name))
	case FirewallFieldUserAgent:
		return c.matchValue(req.Header.Values("User-Agent"))
	case FirewallFieldQuery:
		vals, ok := req.Query[c.c.Name]
		if !ok {
			return false
		}
		return c.matchValue(vals)
	}
	return false
}

// matchValue tests the values of one request part. A header or query
// parameter that is present with no value counts as one empty value.
func (c matcherCond) matchValue(vals []string) bool {
	if c.c.Op == FirewallOpPresent {
		return len(vals) > 0
	}
	for _, got := range vals {
		switch c.c.Op {
		case FirewallOpPrefix:
			for _, v := range c.c.Values {
				if strings.HasPrefix(got, v) {
					return true
				}
			}
		case FirewallOpExact, FirewallOpEquals:
			if slices.Contains(c.c.Values, got) {
				return true
			}
		case FirewallOpContains, FirewallOpRegex:
			for _, re := range c.res {
				if re.MatchString(got) {
					return true
				}
			}
		}
	}
	return false
}
