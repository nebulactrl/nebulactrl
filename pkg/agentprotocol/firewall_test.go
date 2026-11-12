package agentprotocol

import (
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fwCond(field, op string, values ...string) FirewallCondition {
	return FirewallCondition{Field: field, Op: op, Values: values}
}

func fwNamed(field, op, name string, values ...string) FirewallCondition {
	return FirewallCondition{Field: field, Op: op, Name: name, Values: values}
}

func fwNot(c FirewallCondition) FirewallCondition {
	c.Not = true
	return c
}

func fwRule(id, action string, conds ...FirewallCondition) FirewallRule {
	return FirewallRule{ID: id, Name: "rule " + id, Action: action, Conditions: conds}
}

func TestFirewallValidateAcceptsARealisticFirewall(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{
		fwRule("office", FirewallAllow, fwCond(FirewallFieldIP, FirewallOpIn, "203.0.113.0/24", "2001:db8::/32", "198.51.100.7")),
		fwRule("bots", FirewallBlock, fwCond(FirewallFieldUserAgent, FirewallOpContains, "BadBot", "scrapy"), fwNot(fwCond(FirewallFieldPath, FirewallOpPrefix, "/robots.txt"))),
		fwRule("admin", FirewallBlock,
			fwCond(FirewallFieldPath, FirewallOpPrefix, "/admin", "/wp-admin"),
			fwNot(fwCond(FirewallFieldCountry, FirewallOpIn, "DE", "AT", "CH")),
			fwCond(FirewallFieldMethod, FirewallOpIn, "POST", "PUT", "DELETE")),
		fwRule("token", FirewallBlock,
			fwNamed(FirewallFieldHeader, FirewallOpPresent, "X-Debug"),
			fwNamed(FirewallFieldHeader, FirewallOpRegex, "Authorization", `^Bearer [a-f0-9]{8}$`),
			fwNamed(FirewallFieldQuery, FirewallOpEquals, "token", "secret"),
			fwNamed(FirewallFieldQuery, FirewallOpContains, "q", "select "),
			fwNamed(FirewallFieldQuery, FirewallOpPresent, "debug"),
			fwCond(FirewallFieldPath, FirewallOpExact, "/.env"),
			fwCond(FirewallFieldPath, FirewallOpContains, "../"),
			fwCond(FirewallFieldPath, FirewallOpRegex, `\.(php|asp)$`)),
		{ID: "off", Name: "disabled", Action: FirewallBlock, Disabled: true, Conditions: []FirewallCondition{fwCond(FirewallFieldMethod, FirewallOpIn, "GET")}},
	}}
	require.NoError(t, fw.Validate())
	require.NoError(t, Firewall{}.Validate(), "an empty firewall is valid")
	_, err := CompileFirewall(&fw)
	require.NoError(t, err)
}

func TestFirewallValidateRejections(t *testing.T) {
	ok := fwRule("a", FirewallBlock, fwCond(FirewallFieldMethod, FirewallOpIn, "GET"))
	with := func(mut func(*FirewallRule)) Firewall {
		r := ok
		r.Conditions = append([]FirewallCondition(nil), ok.Conditions...)
		mut(&r)
		return Firewall{Rules: []FirewallRule{r}}
	}
	cond := func(c FirewallCondition) Firewall {
		return with(func(r *FirewallRule) { r.Conditions = []FirewallCondition{c} })
	}
	many := func(n int, v string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = v + strconv.Itoa(i)
		}
		return out
	}
	tooManyRules := Firewall{}
	for i := range MaxRouteFirewallRules + 1 {
		tooManyRules.Rules = append(tooManyRules.Rules, fwRule("r"+strconv.Itoa(i), FirewallBlock, fwCond(FirewallFieldMethod, FirewallOpIn, "GET")))
	}
	tooManyConds := with(func(r *FirewallRule) {
		r.Conditions = nil
		for range MaxFirewallConditions + 1 {
			r.Conditions = append(r.Conditions, fwCond(FirewallFieldMethod, FirewallOpIn, "GET"))
		}
	})
	manyIPs := make([]string, MaxFirewallIPValues+1)
	for i := range manyIPs {
		manyIPs[i] = "10." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256) + ".0/24"
	}

	cases := map[string]struct {
		fw   Firewall
		want string
	}{
		"too many rules":            {tooManyRules, "at most 64 firewall rules"},
		"empty id":                  {with(func(r *FirewallRule) { r.ID = "" }), "id must be"},
		"upper-case id":             {with(func(r *FirewallRule) { r.ID = "Rule" }), "id must be"},
		"id too long":               {with(func(r *FirewallRule) { r.ID = strings.Repeat("a", 17) }), "id must be"},
		"id with dash":              {with(func(r *FirewallRule) { r.ID = "a-b" }), "id must be"},
		"duplicate ids":             {Firewall{Rules: []FirewallRule{ok, ok}}, "used twice"},
		"blank name":                {with(func(r *FirewallRule) { r.Name = "  " }), "name must be"},
		"name too long":             {with(func(r *FirewallRule) { r.Name = strings.Repeat("n", 81) }), "name must be"},
		"unknown action":            {with(func(r *FirewallRule) { r.Action = "drop" }), "block or allow"},
		"no conditions":             {with(func(r *FirewallRule) { r.Conditions = nil }), "at least one condition"},
		"too many conditions":       {tooManyConds, "at most 10 conditions"},
		"unknown field":             {cond(fwCond("host", FirewallOpIn, "a")), `unknown field "host"`},
		"ip with prefix op":         {cond(fwCond(FirewallFieldIP, FirewallOpPrefix, "10.0.0.0/8")), "ip cannot use"},
		"country with regex op":     {cond(fwCond(FirewallFieldCountry, FirewallOpRegex, "DE")), "country cannot use"},
		"method with equals op":     {cond(fwCond(FirewallFieldMethod, FirewallOpEquals, "GET")), "method cannot use"},
		"path with equals op":       {cond(fwCond(FirewallFieldPath, FirewallOpEquals, "/a")), "path cannot use"},
		"path with present op":      {cond(fwCond(FirewallFieldPath, FirewallOpPresent)), "path cannot use"},
		"header with prefix op":     {cond(fwNamed(FirewallFieldHeader, FirewallOpPrefix, "X-A", "a")), "header cannot use"},
		"userAgent with equals op":  {cond(fwCond(FirewallFieldUserAgent, FirewallOpEquals, "a")), "userAgent cannot use"},
		"query with exact op":       {cond(fwNamed(FirewallFieldQuery, FirewallOpExact, "a", "b")), "query cannot use"},
		"header without name":       {cond(fwCond(FirewallFieldHeader, FirewallOpEquals, "x")), "name the header"},
		"query without name":        {cond(fwCond(FirewallFieldQuery, FirewallOpPresent)), "name the query"},
		"header name too long":      {cond(fwNamed(FirewallFieldHeader, FirewallOpPresent, strings.Repeat("a", 129))), "longer than"},
		"header name with space":    {cond(fwNamed(FirewallFieldHeader, FirewallOpPresent, "X A")), "not a header name"},
		"header name with colon":    {cond(fwNamed(FirewallFieldHeader, FirewallOpPresent, "X:A")), "not a header name"},
		"header name with backtick": {cond(fwNamed(FirewallFieldHeader, FirewallOpPresent, "X`A")), "not a header name"},
		"query name with backtick":  {cond(fwNamed(FirewallFieldQuery, FirewallOpPresent, "a`b")), "backticks"},
		"query name with newline":   {cond(fwNamed(FirewallFieldQuery, FirewallOpPresent, "a\nb")), "backticks or line breaks"},
		"name on a path":            {cond(fwNamed(FirewallFieldPath, FirewallOpPrefix, "x", "/a")), "takes no name"},
		"name on an ip":             {cond(fwNamed(FirewallFieldIP, FirewallOpIn, "x", "10.0.0.1")), "takes no name"},
		"present with values":       {cond(fwNamed(FirewallFieldHeader, FirewallOpPresent, "X-A", "a")), "present takes no values"},
		"no values":                 {cond(fwCond(FirewallFieldMethod, FirewallOpIn)), "at least one value"},
		"empty value":               {cond(fwCond(FirewallFieldPath, FirewallOpContains, "")), "values must be"},
		"value too long":            {cond(fwCond(FirewallFieldPath, FirewallOpContains, strings.Repeat("a", 513))), "values must be"},
		"too many values":           {cond(fwCond(FirewallFieldPath, FirewallOpContains, many(MaxFirewallValues+1, "v")...)), "at most 50 values"},
		"too many ip values":        {cond(fwCond(FirewallFieldIP, FirewallOpIn, manyIPs...)), "values"},
		"backtick in value":         {cond(fwCond(FirewallFieldPath, FirewallOpContains, "a`b")), "backticks"},
		"newline in value":          {cond(fwCond(FirewallFieldUserAgent, FirewallOpContains, "a\nb")), "line breaks"},
		"carriage return in value":  {cond(fwCond(FirewallFieldUserAgent, FirewallOpRegex, "a\rb")), "line breaks"},
		"bad regex":                 {cond(fwCond(FirewallFieldPath, FirewallOpRegex, "(unclosed")), "not a valid regular expression"},
		"bad header regex":          {cond(fwNamed(FirewallFieldHeader, FirewallOpRegex, "X-A", "[z-a]")), "not a valid regular expression"},
		"bad CIDR":                  {cond(fwCond(FirewallFieldIP, FirewallOpIn, "10.0.0.0/33")), "not a CIDR or IP"},
		"hostname as ip":            {cond(fwCond(FirewallFieldIP, FirewallOpIn, "example.com")), "not a CIDR or IP"},
		"lower-case country":        {cond(fwCond(FirewallFieldCountry, FirewallOpIn, "de")), "two-letter country"},
		"three-letter country":      {cond(fwCond(FirewallFieldCountry, FirewallOpIn, "DEU")), "two-letter country"},
		"numeric country":           {cond(fwCond(FirewallFieldCountry, FirewallOpIn, "D1")), "two-letter country"},
		"unknown method":            {cond(fwCond(FirewallFieldMethod, FirewallOpIn, "FETCH")), "not an HTTP method"},
		"lower-case method":         {cond(fwCond(FirewallFieldMethod, FirewallOpIn, "get")), "not an HTTP method"},
		"path prefix without slash": {cond(fwCond(FirewallFieldPath, FirewallOpPrefix, "admin")), "starts with /"},
		"exact path without slash":  {cond(fwCond(FirewallFieldPath, FirewallOpExact, "admin")), "starts with /"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.fw.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), c.want)
			_, cerr := CompileFirewall(&c.fw)
			require.Error(t, cerr, "an invalid firewall never compiles")
		})
	}

	t.Run("errors name the rule and condition", func(t *testing.T) {
		fw := Firewall{Rules: []FirewallRule{ok, fwRule("b", FirewallBlock, fwCond(FirewallFieldMethod, FirewallOpIn, "GET"), fwCond(FirewallFieldCountry, FirewallOpIn, "xx"))}}
		require.ErrorContains(t, fw.Validate(), "rule 2, condition 2")
	})
	t.Run("path contains and regex need no slash", func(t *testing.T) {
		require.NoError(t, cond(fwCond(FirewallFieldPath, FirewallOpContains, "admin")).Validate())
		require.NoError(t, cond(fwCond(FirewallFieldPath, FirewallOpRegex, "admin")).Validate())
	})
	t.Run("limits are inclusive", func(t *testing.T) {
		fw := Firewall{}
		for i := range MaxFirewallRules {
			fw.Rules = append(fw.Rules, fwRule("r"+strconv.Itoa(i), FirewallBlock, fwCond(FirewallFieldPath, FirewallOpContains, many(MaxFirewallValues, "v")...)))
		}
		require.NoError(t, fw.Validate())
	})
}

func TestFirewallOps(t *testing.T) {
	require.Equal(t, []string{FirewallOpIn}, FirewallOps(FirewallFieldIP))
	require.Nil(t, FirewallOps("nope"))
	ops := FirewallOps(FirewallFieldHeader)
	ops[0] = "mutated"
	require.NotEqual(t, "mutated", FirewallOps(FirewallFieldHeader)[0], "the caller gets a copy")
}

func TestFirewallActiveUsesAndNeedsGuard(t *testing.T) {
	var nilFw *Firewall
	require.Empty(t, nilFw.Active())
	require.False(t, nilFw.UsesCountry())
	require.False(t, nilFw.NeedsGuard(true))

	disabledCountry := fwRule("c", FirewallBlock, fwCond(FirewallFieldCountry, FirewallOpIn, "CN"))
	disabledCountry.Disabled = true
	ipRule := fwRule("i", FirewallBlock, fwCond(FirewallFieldIP, FirewallOpIn, "10.0.0.0/8"))
	pathRule := fwRule("p", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpPrefix, "/x"))

	fw := &Firewall{Rules: []FirewallRule{disabledCountry, pathRule}}
	require.Equal(t, []FirewallRule{pathRule}, fw.Active())
	require.False(t, fw.UsesCountry(), "a disabled country rule does not count")
	require.False(t, fw.NeedsGuard(true))

	fw = &Firewall{Rules: []FirewallRule{ipRule, pathRule}}
	require.True(t, fw.UsesIP())
	require.False(t, fw.NeedsGuard(false), "addresses are routers' business when the peer is the visitor")
	require.True(t, fw.NeedsGuard(true), "addresses need the guard when the visitor arrives in a header")

	fw = &Firewall{Rules: []FirewallRule{pathRule, fwRule("c", FirewallBlock, fwCond(FirewallFieldCountry, FirewallOpIn, "CN"))}}
	require.True(t, fw.UsesCountry())
	require.True(t, fw.NeedsGuard(false))

	fw = &Firewall{Rules: []FirewallRule{pathRule}}
	require.False(t, fw.NeedsGuard(true), "no address or country: routers always suffice")
}

func TestGuardCheckURL(t *testing.T) {
	require.Equal(t, "http://127.0.0.1:83/v1/check/abc?client=peer", GuardCheckURL("abc", GuardClientPeer))
	require.Equal(t, "http://127.0.0.1:83/v1/check/abc?client=cf", GuardCheckURL("abc", GuardClientCloudflare))
}

// decide compiles fw and returns the deciding rule and action for req.
func decide(t *testing.T, fw Firewall, req FirewallRequest) (string, string) {
	t.Helper()
	m, err := CompileFirewall(&fw)
	require.NoError(t, err)
	return m.Decide(req)
}

func fwReq(method, path string) FirewallRequest {
	return FirewallRequest{Method: method, Path: path, Query: map[string][]string{}, Header: http.Header{}}
}

func TestFirewallDecideNilAndEmpty(t *testing.T) {
	m, err := CompileFirewall(nil)
	require.NoError(t, err)
	id, action := m.Decide(fwReq("GET", "/"))
	require.Empty(t, id)
	require.Empty(t, action)
	id, action = decide(t, Firewall{}, fwReq("GET", "/"))
	require.Empty(t, id+action)
}

func TestFirewallDecideFirstMatchWinsAndAllowShortCircuits(t *testing.T) {
	admin := fwCond(FirewallFieldPath, FirewallOpPrefix, "/admin")
	office := fwCond(FirewallFieldIP, FirewallOpIn, "203.0.113.0/24")
	fw := Firewall{Rules: []FirewallRule{
		fwRule("office", FirewallAllow, office),
		fwRule("admin", FirewallBlock, admin),
		fwRule("all", FirewallBlock, fwCond(FirewallFieldMethod, FirewallOpIn, "DELETE")),
	}}
	req := fwReq("GET", "/admin/users")
	req.ClientIP = netip.MustParseAddr("203.0.113.9")
	id, action := decide(t, fw, req)
	require.Equal(t, "office", id)
	require.Equal(t, FirewallAllow, action, "allow wins over the block rule below it")

	req.Method = "DELETE"
	id, action = decide(t, fw, req)
	require.Equal(t, []string{"office", FirewallAllow}, []string{id, action}, "and over every later rule")

	req.ClientIP = netip.MustParseAddr("198.51.100.1")
	id, action = decide(t, fw, req)
	require.Equal(t, []string{"admin", FirewallBlock}, []string{id, action}, "the first matching block rule decides, not the later one")

	req.Path = "/public"
	id, action = decide(t, fw, req)
	require.Equal(t, []string{"all", FirewallBlock}, []string{id, action})

	req.Method = "GET"
	id, action = decide(t, fw, req)
	require.Empty(t, id+action, "no rule matches: the request goes through")

	// Order matters: with the block first, the allow below it never fires.
	fw.Rules[0], fw.Rules[1] = fw.Rules[1], fw.Rules[0]
	req.Path = "/admin"
	req.ClientIP = netip.MustParseAddr("203.0.113.9")
	id, action = decide(t, fw, req)
	require.Equal(t, []string{"admin", FirewallBlock}, []string{id, action})
}

func TestFirewallDecideConditionsAreAnded(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock,
		fwCond(FirewallFieldMethod, FirewallOpIn, "POST"),
		fwCond(FirewallFieldPath, FirewallOpPrefix, "/api"),
		fwNamed(FirewallFieldHeader, FirewallOpPresent, "X-Key"))}}
	req := fwReq("POST", "/api/x")
	req.Header.Set("X-Key", "1")
	id, _ := decide(t, fw, req)
	require.Equal(t, "r", id)

	for name, mut := range map[string]func(*FirewallRequest){
		"method":  func(r *FirewallRequest) { r.Method = "GET" },
		"path":    func(r *FirewallRequest) { r.Path = "/web" },
		"header":  func(r *FirewallRequest) { r.Header.Del("X-Key") },
		"all bad": func(r *FirewallRequest) { *r = fwReq("GET", "/") },
	} {
		r := fwReq("POST", "/api/x")
		r.Header.Set("X-Key", "1")
		mut(&r)
		id, action := decide(t, fw, r)
		require.Empty(t, id+action, name)
	}
}

func TestFirewallDecideMultipleValuesAreOred(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpPrefix, "/a", "/b", "/c"))}}
	for path, want := range map[string]string{"/a1": "r", "/b": "r", "/c/d": "r", "/d": "", "/": ""} {
		id, _ := decide(t, fw, fwReq("GET", path))
		require.Equal(t, want, id, path)
	}
}

func TestFirewallDecideNotInverts(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock,
		fwNot(fwCond(FirewallFieldPath, FirewallOpPrefix, "/public", "/static")))}}
	for path, want := range map[string]string{"/public/x": "", "/static": "", "/private": "r", "/": "r"} {
		id, _ := decide(t, fw, fwReq("GET", path))
		require.Equal(t, want, id, path)
	}

	// Not applies to the whole condition (all its values), not to each value.
	fw = Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwNot(fwCond(FirewallFieldMethod, FirewallOpIn, "GET", "HEAD")))}}
	for method, want := range map[string]string{"GET": "", "HEAD": "", "POST": "r"} {
		id, _ := decide(t, fw, fwReq(method, "/"))
		require.Equal(t, want, id, method)
	}
}

func TestFirewallDecideNotOnAbsentThingsMatches(t *testing.T) {
	// "header X-A is not 1" is true when the header is missing: Not inverts a
	// failed match.
	fw := Firewall{Rules: []FirewallRule{
		fwRule("hdr", FirewallBlock, fwNot(fwNamed(FirewallFieldHeader, FirewallOpEquals, "X-A", "1"))),
	}}
	id, _ := decide(t, fw, fwReq("GET", "/"))
	require.Equal(t, "hdr", id)

	fw = Firewall{Rules: []FirewallRule{fwRule("q", FirewallBlock, fwNot(fwNamed(FirewallFieldQuery, FirewallOpPresent, "k")))}}
	id, _ = decide(t, fw, fwReq("GET", "/"))
	require.Equal(t, "q", id)
	r := fwReq("GET", "/")
	r.Query["k"] = []string{"v"}
	id, _ = decide(t, fw, r)
	require.Empty(t, id)
}

func TestFirewallDecideContainsIgnoresCaseAndLiterals(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{
		fwRule("ua", FirewallBlock, fwCond(FirewallFieldUserAgent, FirewallOpContains, "BadBot")),
		fwRule("dot", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpContains, "a.b", "(x)")),
	}}
	for _, ua := range []string{"badbot/1.0", "Mozilla BADBOT", "xBadBotx"} {
		r := fwReq("GET", "/")
		r.Header.Set("User-Agent", ua)
		id, _ := decide(t, fw, r)
		require.Equal(t, "ua", id, ua)
	}
	r := fwReq("GET", "/")
	r.Header.Set("User-Agent", "Bad Bot")
	id, _ := decide(t, fw, r)
	require.Empty(t, id)

	// The value is a literal: a dot is not a wildcard, parentheses are not a group.
	id, _ = decide(t, fw, fwReq("GET", "/xA.Bx"))
	require.Equal(t, "dot", id)
	id, _ = decide(t, fw, fwReq("GET", "/aXb"))
	require.Empty(t, id)
	id, _ = decide(t, fw, fwReq("GET", "/(X)"))
	require.Equal(t, "dot", id)
	id, _ = decide(t, fw, fwReq("GET", "/x"))
	require.Empty(t, id)
}

func TestFirewallDecideRegexIsUnanchoredAndCaseSensitive(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpRegex, `\.php$`, `^/v[0-9]+/admin`))}}
	for path, want := range map[string]string{"/a/b.php": "r", "/a.php/x": "", "/v2/admin/x": "r", "/x/v2/admin": "", "/a.PHP": ""} {
		id, _ := decide(t, fw, fwReq("GET", path))
		require.Equal(t, want, id, path)
	}
	fw = Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpRegex, `admin`))}}
	id, _ := decide(t, fw, fwReq("GET", "/x/admin/y"))
	require.Equal(t, "r", id, "unanchored: matches anywhere")
}

func TestFirewallDecidePathPrefixAndExact(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{
		fwRule("exact", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpExact, "/login", "/.env")),
		fwRule("prefix", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpPrefix, "/admin")),
	}}
	for path, want := range map[string]string{
		"/login": "exact", "/.env": "exact", "/login/": "", "/Login": "",
		"/admin": "prefix", "/admin/x": "prefix", "/administrator": "prefix", "/x/admin": "",
	} {
		id, _ := decide(t, fw, fwReq("GET", path))
		require.Equal(t, want, id, path)
	}
}

func TestFirewallDecideMethodIsCaseInsensitiveOnTheRequest(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwCond(FirewallFieldMethod, FirewallOpIn, "DELETE"))}}
	id, _ := decide(t, fw, fwReq("delete", "/"))
	require.Equal(t, "r", id)
	id, _ = decide(t, fw, fwReq("GET", "/"))
	require.Empty(t, id)
}

func TestFirewallDecideHeaders(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{
		fwRule("eq", FirewallBlock, fwNamed(FirewallFieldHeader, FirewallOpEquals, "x-api-key", "k1", "k2")),
		fwRule("contains", FirewallBlock, fwNamed(FirewallFieldHeader, FirewallOpContains, "Referer", "Evil.example")),
		fwRule("regex", FirewallBlock, fwNamed(FirewallFieldHeader, FirewallOpRegex, "X-Id", `^[0-9]{3}$`)),
		fwRule("present", FirewallBlock, fwNamed(FirewallFieldHeader, FirewallOpPresent, "X-Debug")),
	}}
	cases := []struct {
		name string
		set  func(http.Header)
		want string
	}{
		{"equals, name case-insensitive", func(h http.Header) { h.Set("X-Api-Key", "k2") }, "eq"},
		{"equals is exact and case-sensitive", func(h http.Header) { h.Set("X-Api-Key", "K1") }, ""},
		{"equals is not a substring match", func(h http.Header) { h.Set("X-Api-Key", "k1x") }, ""},
		{"any of several header values", func(h http.Header) { h.Add("X-Api-Key", "nope"); h.Add("X-Api-Key", "k1") }, "eq"},
		{"contains ignores case", func(h http.Header) { h.Set("Referer", "https://sub.EVIL.example/x") }, "contains"},
		{"contains miss", func(h http.Header) { h.Set("Referer", "https://good.example/") }, ""},
		{"regex hit", func(h http.Header) { h.Set("X-Id", "123") }, "regex"},
		{"regex miss", func(h http.Header) { h.Set("X-Id", "1234") }, ""},
		{"present with a value", func(h http.Header) { h.Set("X-Debug", "1") }, "present"},
		{"present with an empty value", func(h http.Header) { h.Set("X-Debug", "") }, "present"},
		{"absent", func(h http.Header) {}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := fwReq("GET", "/")
			c.set(r.Header)
			id, _ := decide(t, fw, r)
			require.Equal(t, c.want, id)
		})
	}
}

func TestFirewallDecideQuery(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{
		fwRule("eq", FirewallBlock, fwNamed(FirewallFieldQuery, FirewallOpEquals, "token", "a", "b")),
		fwRule("contains", FirewallBlock, fwNamed(FirewallFieldQuery, FirewallOpContains, "q", "UNION")),
		fwRule("regex", FirewallBlock, fwNamed(FirewallFieldQuery, FirewallOpRegex, "id", `^\d+$`)),
		fwRule("present", FirewallBlock, fwNamed(FirewallFieldQuery, FirewallOpPresent, "debug")),
	}}
	cases := []struct {
		name  string
		query map[string][]string
		want  string
	}{
		{"equals", map[string][]string{"token": {"b"}}, "eq"},
		{"equals is exact", map[string][]string{"token": {"ab"}}, ""},
		{"name is case-sensitive", map[string][]string{"Token": {"a"}}, ""},
		{"any of several values", map[string][]string{"token": {"x", "a"}}, "eq"},
		{"contains ignores case", map[string][]string{"q": {"1 union select"}}, "contains"},
		{"regex", map[string][]string{"id": {"42"}}, "regex"},
		{"regex miss", map[string][]string{"id": {"4x"}}, ""},
		{"present with a value", map[string][]string{"debug": {"1"}}, "present"},
		{"present without a value", map[string][]string{"debug": {""}}, "present"},
		{"absent", map[string][]string{"other": {"x"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := fwReq("GET", "/")
			r.Query = c.query
			id, _ := decide(t, fw, r)
			require.Equal(t, c.want, id)
		})
	}
}

func TestFirewallDecideIP(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwCond(FirewallFieldIP, FirewallOpIn, "203.0.113.0/24", "198.51.100.7", "2001:db8::/32"))}}
	cases := map[string]string{
		"203.0.113.1":         "r",
		"203.0.113.255":       "r",
		"203.0.114.1":         "",
		"198.51.100.7":        "r",
		"198.51.100.8":        "",
		"2001:db8:1::1":       "r",
		"2001:db9::1":         "",
		"::ffff:203.0.113.77": "r", // IPv4-mapped IPv6 matches the IPv4 CIDR
		"::ffff:198.51.100.7": "r",
		"::ffff:198.51.100.9": "",
	}
	for ip, want := range cases {
		r := fwReq("GET", "/")
		r.ClientIP = netip.MustParseAddr(ip)
		id, _ := decide(t, fw, r)
		require.Equal(t, want, id, ip)
	}

	t.Run("an unknown client never matches an in condition", func(t *testing.T) {
		id, _ := decide(t, fw, fwReq("GET", "/"))
		require.Empty(t, id)
	})
	t.Run("an unknown client matches a negated one", func(t *testing.T) {
		neg := Firewall{Rules: []FirewallRule{fwRule("n", FirewallBlock, fwNot(fwCond(FirewallFieldIP, FirewallOpIn, "10.0.0.0/8")))}}
		id, _ := decide(t, neg, fwReq("GET", "/"))
		require.Equal(t, "n", id)
	})
	t.Run("a mapped address inside a v6 CIDR value", func(t *testing.T) {
		v4 := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwCond(FirewallFieldIP, FirewallOpIn, "10.0.0.0/8"))}}
		r := fwReq("GET", "/")
		r.ClientIP = netip.MustParseAddr("::ffff:10.1.2.3")
		id, _ := decide(t, v4, r)
		require.Equal(t, "r", id)
	})
}

func TestFirewallDecideCountry(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwCond(FirewallFieldCountry, FirewallOpIn, "CN", "RU"))}}
	for country, want := range map[string]string{"CN": "r", "RU": "r", "DE": "", "": ""} {
		r := fwReq("GET", "/")
		r.Country = country
		id, _ := decide(t, fw, r)
		require.Equal(t, want, id, country)
	}

	// Negation is plain inversion: an unknown country (no GeoIP hit) is "not
	// in DE", so the matcher blocks it. The guard avoids this by dropping
	// country rules altogether while it has no GeoIP database.
	neg := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwNot(fwCond(FirewallFieldCountry, FirewallOpIn, "DE")))}}
	r := fwReq("GET", "/")
	r.Country = "DE"
	id, _ := decide(t, neg, r)
	require.Empty(t, id)
	r.Country = "FR"
	id, _ = decide(t, neg, r)
	require.Equal(t, "r", id)
	r.Country = ""
	id, _ = decide(t, neg, r)
	require.Equal(t, "r", id, "an unknown country is not in DE")
}

func TestFirewallDecideSkipsDisabledRules(t *testing.T) {
	off := fwRule("off", FirewallBlock, fwCond(FirewallFieldMethod, FirewallOpIn, "GET"))
	off.Disabled = true
	on := fwRule("on", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpPrefix, "/x"))
	fw := Firewall{Rules: []FirewallRule{off, on}}
	id, _ := decide(t, fw, fwReq("GET", "/y"))
	require.Empty(t, id, "the disabled rule above never fires")
	id, _ = decide(t, fw, fwReq("GET", "/x"))
	require.Equal(t, "on", id)

	// A disabled allow rule does not shield what the block rule below catches.
	allow := fwRule("allow", FirewallAllow, fwCond(FirewallFieldPath, FirewallOpPrefix, "/x"))
	allow.Disabled = true
	block := fwRule("block", FirewallBlock, fwCond(FirewallFieldPath, FirewallOpPrefix, "/x"))
	id, action := decide(t, Firewall{Rules: []FirewallRule{allow, block}}, fwReq("GET", "/x"))
	require.Equal(t, []string{"block", FirewallBlock}, []string{id, action})
}

func TestFirewallDecideUserAgentMissingHeader(t *testing.T) {
	fw := Firewall{Rules: []FirewallRule{fwRule("r", FirewallBlock, fwNot(fwCond(FirewallFieldUserAgent, FirewallOpRegex, `.+`)))}}
	id, _ := decide(t, fw, fwReq("GET", "/"))
	require.Equal(t, "r", id, "no User-Agent: blocked by 'not any user agent'")
	r := fwReq("GET", "/")
	r.Header.Set("User-Agent", "curl/8")
	id, _ = decide(t, fw, r)
	require.Empty(t, id)
}
