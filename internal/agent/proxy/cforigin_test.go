package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// cfCIDRs is a small stand-in for Cloudflare's list, deliberately unsorted and
// with a duplicate and an unmasked entry (normalisation is part of the contract).
func cfCIDRs() []string {
	return []string{"173.245.48.0/20", "103.21.244.0/22", "2400:cb00::/32", "103.21.244.0/22", "104.16.5.7/13"}
}

var cfSorted = []string{"103.21.244.0/22", "104.16.0.0/13", "173.245.48.0/20", "2400:cb00::/32"}

// lockedRoute is app.example.com locked (with the user's rules) next to the
// unlocked www.example.com, one route, one origin lock.
func lockedRoute(t *testing.T, requireCert bool) agentprotocol.Route {
	t.Helper()
	crt, _ := testCert(t) // any certificate PEM serves as the CA here
	r := ruleRoute(
		rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
			d.OriginLocked, d.RequireClientCert = true, requireCert
			d.Options.Access = &agentprotocol.Access{DenyCIDRs: []string{"198.51.100.7"}, TrustedProxyDepth: 3}
			d.Options.RateLimit = &agentprotocol.RateLimit{Average: 10, PeriodSeconds: 1, Burst: 5, TrustedProxyDepth: 2}
			d.Options.Compress = true
		}),
		rule(domB, "www.example.com", nil),
	)
	r.OriginLock = &agentprotocol.OriginLock{CloudflareCIDRs: cfCIDRs(), ClientCAPEM: crt}
	return r
}

type dynTLSParsed struct {
	TLS struct {
		Options map[string]struct {
			ClientAuth struct {
				CAFiles        []string `json:"caFiles"`
				ClientAuthType string   `json:"clientAuthType"`
			} `json:"clientAuth"`
		} `json:"options"`
	} `json:"tls"`
}

func TestOriginLockedRouteGolden(t *testing.T) {
	raw := goldenConfig(t, "cforigin_locked", []agentprotocol.Route{lockedRoute(t, false)}, Options{ACMEEmail: "ops@example.com"})
	p := parse(t, raw)
	n := "nc-" + svcApp + "-daaaaaaaa"
	lock := ""
	for name := range p.HTTP.Middlewares {
		if strings.HasPrefix(name, lockMiddlewarePrefix) {
			lock = name
		}
	}
	require.NotEmpty(t, lock, "one shared lock middleware")

	// (a) The lock checks the direct peer: no ipStrategy at all, sorted masked ranges.
	lockMW := p.HTTP.Middlewares[lock]["ipAllowList"].(map[string]any)
	require.Equal(t, cfSorted, strs(lockMW["sourceRange"]))
	require.NotContains(t, lockMW, "ipStrategy", "an absent ipStrategy is Traefik's RemoteAddr strategy")
	require.NotContains(t, lockMW, "rejectStatusCode", "the default 403 with Traefik's plain body")

	// (d) Order: lock, the user's rules, then the rest.
	require.Equal(t, []string{lock, n + "-ipallow", n + "-ratelimit", n + "-compress"}, strs(p.HTTP.Routers[n+"-https"]["middlewares"]))
	require.Equal(t, []string{lock, "nc-https-redirect"}, strs(p.HTTP.Routers[n+"-http"]["middlewares"]),
		"the :80 router of a locked domain is locked too and only redirects")
	require.NotContains(t, p.HTTP.Routers[n+"-probe"], "middlewares", "verification checks routing, the lock is not part of it")

	// (b) The user's rules read the visitor: depth 1, whatever depth the user asked for.
	require.Equal(t, map[string]any{"depth": float64(1)}, p.HTTP.Middlewares[n+"-ipallow"]["ipAllowList"].(map[string]any)["ipStrategy"])
	rl := p.HTTP.Middlewares[n+"-ratelimit"]["rateLimit"].(map[string]any)
	require.Equal(t, map[string]any{"ipStrategy": map[string]any{"depth": float64(1)}}, rl["sourceCriterion"])

	// Without ACME HTTP-01 being affected: the https router keeps the resolver, no TLS option.
	require.Equal(t, map[string]any{"certResolver": "le"}, p.HTTP.Routers[n+"-https"]["tls"])
	var tlsOpts dynTLSParsed
	require.NoError(t, json.Unmarshal(raw, &tlsOpts))
	require.Empty(t, tlsOpts.TLS.Options)

	// The unlocked domain of the same route is untouched.
	w := "nc-" + svcApp + "-dbbbbbbbb"
	require.NotContains(t, p.HTTP.Routers[w+"-https"], "middlewares")
	require.Equal(t, []string{"nc-https-redirect"}, strs(p.HTTP.Routers[w+"-http"]["middlewares"]))
}

func TestOriginLockedWithClientCertGolden(t *testing.T) {
	raw := goldenConfig(t, "cforigin_client_cert", []agentprotocol.Route{lockedRoute(t, true)}, Options{ACMEEmail: "ops@example.com"})
	p := parse(t, raw)
	n := "nc-" + svcApp + "-daaaaaaaa"

	var tlsOpts dynTLSParsed
	require.NoError(t, json.Unmarshal(raw, &tlsOpts))
	require.Len(t, tlsOpts.TLS.Options, 1)
	var name string
	for k, o := range tlsOpts.TLS.Options {
		name = k
		require.True(t, strings.HasPrefix(k, aopOptionPrefix), k)
		require.Equal(t, "RequireAndVerifyClientCert", o.ClientAuth.ClientAuthType)
		crt, _ := testCert(t)
		require.Equal(t, []string{strings.TrimSpace(crt) + "\n"}, o.ClientAuth.CAFiles, "the CA travels inline")
	}
	https := p.HTTP.Routers[n+"-https"]["tls"].(map[string]any)
	require.Equal(t, name, https["options"], "the https router of the locked domain requires the certificate")
	require.Equal(t, "le", https["certResolver"], "the certificate is still issued by the resolver")
	require.NotContains(t, p.HTTP.Routers[n+"-http"], "tls", "the :80 router is plain http: ACME HTTP-01 is never behind the requirement")
	require.NotContains(t, p.HTTP.Routers["nc-"+svcApp+"-dbbbbbbbb-https"]["tls"], "options", "an unlocked domain needs no client certificate")

	// The option name follows the CA: a refreshed CA is a new option, the same CA is stable.
	again := parse(t, mustBuild(t, []agentprotocol.Route{lockedRoute(t, true)}, Options{ACMEEmail: "ops@example.com"}))
	require.Equal(t, https, again.HTTP.Routers[n+"-https"]["tls"])
}

func TestOriginLockKeepsTheLockInMaintenance(t *testing.T) {
	r := lockedRoute(t, false)
	r.Maintenance = &agentprotocol.Maintenance{Title: "Back soon"}
	p := parse(t, mustBuild(t, []agentprotocol.Route{r}, Options{Pages: true}))
	n := "nc-" + svcApp + "-daaaaaaaa"
	mws := strs(p.HTTP.Routers[n+"-https"]["middlewares"])
	require.Len(t, mws, 2)
	require.True(t, strings.HasPrefix(mws[0], lockMiddlewarePrefix), "the maintenance page is behind the lock")
	require.Equal(t, "nc-"+svcApp+"-maint", mws[1])
	require.Equal(t, "nc-pages", p.HTTP.Routers[n+"-https"]["service"])
}

func TestOriginLockWithPagesPutsErrorsOutermost(t *testing.T) {
	p := parse(t, mustBuild(t, []agentprotocol.Route{lockedRoute(t, false)}, Options{Pages: true}))
	n := "nc-" + svcApp + "-daaaaaaaa"
	mws := strs(p.HTTP.Routers[n+"-https"]["middlewares"])
	require.Equal(t, errorsMiddleware, mws[0], "the errors middleware only rewrites 502-504")
	require.True(t, strings.HasPrefix(mws[1], lockMiddlewarePrefix), "the lock is the first access check")
	require.Equal(t, n+"-ipallow", mws[2])
}

// Unlocked domains render without any lock artifact, even when the route
// carries lock data for a sibling domain.
func TestUnlockedRoutesAreUnchanged(t *testing.T) {
	plain := ruleRoute(
		rule(domA, "app.example.com", func(d *agentprotocol.DomainRule) {
			d.Options.Access = &agentprotocol.Access{AllowCIDRs: []string{"10.0.0.0/8"}, TrustedProxyDepth: 2}
			d.Options.RateLimit = &agentprotocol.RateLimit{Average: 10, PeriodSeconds: 1, Burst: 5, TrustedProxyDepth: 2}
		}),
	)
	base := mustBuild(t, []agentprotocol.Route{plain}, Options{ACMEEmail: "ops@example.com"})
	withData := plain
	withData.OriginLock = &agentprotocol.OriginLock{CloudflareCIDRs: cfCIDRs()}
	require.JSONEq(t, string(base), string(mustBuild(t, []agentprotocol.Route{withData}, Options{ACMEEmail: "ops@example.com"})),
		"lock data without a locked domain changes nothing")

	p := parse(t, base)
	n := "nc-" + svcApp + "-daaaaaaaa"
	require.Equal(t, map[string]any{"depth": float64(2)}, p.HTTP.Middlewares[n+"-ipallow"]["ipAllowList"].(map[string]any)["ipStrategy"],
		"an unlocked domain keeps the user's trusted proxy depth")
	require.Empty(t, TrustedProxyCIDRs([]agentprotocol.Route{withData}))
	for name := range p.HTTP.Middlewares {
		require.False(t, strings.HasPrefix(name, lockMiddlewarePrefix), name)
	}
	require.NotContains(t, string(base), `"options"`, "no TLS options")
}

func TestTrustedProxyCIDRsIsTheUnionOfLockedRoutes(t *testing.T) {
	a := lockedRoute(t, false)
	b := lockedRoute(t, false)
	b.ServiceID = svcWhoami
	b.OriginLock = &agentprotocol.OriginLock{CloudflareCIDRs: []string{"198.41.128.0/17", "103.21.244.0/22"}}
	unlocked := lockedRoute(t, false)
	unlocked.ServiceID = "33333333-3333-3333-3333-333333333333"
	unlocked.Domains[0].OriginLocked = false
	unlocked.OriginLock = &agentprotocol.OriginLock{CloudflareCIDRs: []string{"192.0.2.0/24"}}

	require.Equal(t, []string{"103.21.244.0/22", "104.16.0.0/13", "173.245.48.0/20", "198.41.128.0/17", "2400:cb00::/32"},
		TrustedProxyCIDRs([]agentprotocol.Route{unlocked, b, a}), "sorted, masked, deduplicated; unlocked routes contribute nothing")
	require.Empty(t, TrustedProxyCIDRs(nil))
	require.Empty(t, TrustedProxyCIDRs(twoRoutes()))
}

func TestStaticFlagsTrustForwardedHeadersOnlyWhenNeeded(t *testing.T) {
	plain := strings.Join(StaticFlags(StaticOptions{PublicHTTPSPort: 443}), "\n")
	require.NotContains(t, plain, "forwardedheaders", "no locked domain: Traefik keeps trusting nobody")

	flags := StaticFlags(StaticOptions{PublicHTTPSPort: 443, TrustedProxyCIDRs: cfSorted})
	want := strings.Join(cfSorted, ",")
	require.Contains(t, flags, "--entrypoints.web.forwardedheaders.trustedips="+want)
	require.Contains(t, flags, "--entrypoints.websecure.forwardedheaders.trustedips="+want)
	joined := strings.Join(flags, "\n")
	require.NotContains(t, joined, "entrypoints.probe.forwardedheaders")
	require.NotContains(t, joined, "entrypoints.ncadmin.forwardedheaders")
	require.NotContains(t, joined, "forwardedheaders.insecure", "never trust every peer")
	require.Len(t, flags, len(StaticFlags(StaticOptions{PublicHTTPSPort: 443}))+2)
}

// The trust set is part of the container's spec fingerprint, and only the set:
// a table change that leaves the ranges alone never recreates the proxy.
func TestRecreateOnlyWhenTheCIDRSetChanges(t *testing.T) {
	m, eng, tr := newTestManager(t, &memRouteStore{})
	apply := func(r agentprotocol.Route) {
		t.Helper()
		require.NoError(t, m.Apply([]agentprotocol.Route{r}))
	}
	locked := func(upstream string, cidrs ...string) agentprotocol.Route {
		r := lockedRoute(t, false)
		r.Upstreams = []string{upstream}
		r.OriginLock.CloudflareCIDRs = cidrs
		return r
	}
	unlocked := func(upstream string) agentprotocol.Route {
		r := lockedRoute(t, false)
		r.Upstreams = []string{upstream}
		r.Domains[0].OriginLocked = false
		r.OriginLock = nil
		return r
	}

	apply(unlocked("nc-app-1:8080"))
	require.Len(t, eng.created, 1)
	require.NotContains(t, strings.Join(eng.created[0].Cmd, " "), "forwardedheaders")

	// The first locked domain adds the trust set: one recreate.
	apply(locked("nc-app-1:8080", cfCIDRs()...))
	require.Len(t, eng.created, 2)
	require.Contains(t, eng.created[1].Cmd, "--entrypoints.websecure.forwardedheaders.trustedips="+strings.Join(cfSorted, ","))
	require.NotEqual(t, eng.created[0].Labels[LabelSpec], eng.created[1].Labels[LabelSpec])

	// A route change with the same ranges, or the same ranges in another order
	// and notation: routes are pushed, the container stays.
	apply(locked("nc-app-2:8080", cfCIDRs()...))
	apply(locked("nc-app-3:8080", "2400:cb00::/32", "104.16.0.0/13", "173.245.48.0/20", "103.21.244.0/22"))
	require.Len(t, eng.created, 2, "unchanged range set: no recreate")
	require.Equal(t, 0, tr.unauthPUTs)

	// Cloudflare adds a range: the static trust set changes, one recreate.
	apply(locked("nc-app-3:8080", append(cfCIDRs(), "198.41.128.0/17")...))
	require.Len(t, eng.created, 3)
	require.Contains(t, strings.Join(eng.created[2].Cmd, " "), "198.41.128.0/17")

	// The last locked domain goes: the trust is dropped again.
	apply(unlocked("nc-app-3:8080"))
	require.Len(t, eng.created, 4)
	require.NotContains(t, strings.Join(eng.created[3].Cmd, " "), "forwardedheaders")
	apply(unlocked("nc-app-4:8080"))
	require.Len(t, eng.created, 4)
}

func TestValidateOriginLocks(t *testing.T) {
	def := agentprotocol.DefaultUpstreamPolicy()
	good := lockedRoute(t, true)
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{good}, def))

	cases := map[string]func(*agentprotocol.Route){
		"no originLock on the route":    func(r *agentprotocol.Route) { r.OriginLock = nil },
		"no ranges":                     func(r *agentprotocol.Route) { r.OriginLock.CloudflareCIDRs = nil },
		"invalid range":                 func(r *agentprotocol.Route) { r.OriginLock.CloudflareCIDRs = []string{"nope"} },
		"a bare address is not a CIDR":  func(r *agentprotocol.Route) { r.OriginLock.CloudflareCIDRs = []string{"103.21.244.1"} },
		"the whole internet v4":         func(r *agentprotocol.Route) { r.OriginLock.CloudflareCIDRs = []string{"0.0.0.0/0"} },
		"the whole internet v6":         func(r *agentprotocol.Route) { r.OriginLock.CloudflareCIDRs = []string{"::/0"} },
		"client cert without a CA":      func(r *agentprotocol.Route) { r.OriginLock.ClientCAPEM = "" },
		"client cert with a garbage CA": func(r *agentprotocol.Route) { r.OriginLock.ClientCAPEM = "not a pem" },
		"client cert with TLS off":      func(r *agentprotocol.Route) { r.Domains[0].TLSMode = agentprotocol.TLSNone },
		"tunnel domain":                 func(r *agentprotocol.Route) { r.Domains[0].Tunnel = true },
		"redirect domain": func(r *agentprotocol.Route) {
			r.Domains[0].Kind, r.Domains[0].RedirectTo = agentprotocol.DomainRedirect, "x.example.com"
		},
		"client cert on a wildcard host": func(r *agentprotocol.Route) { r.Hosts[0], r.Domains[0].Host = "*.example.com", "*.example.com" },
		"client cert without being lock": func(r *agentprotocol.Route) { r.Domains[0].OriginLocked = false },
		"one host, two lock states": func(r *agentprotocol.Route) {
			r.Domains = append(r.Domains, agentprotocol.DomainRule{ID: domC, Host: "app.example.com", PathPrefix: "/x"})
		},
	}
	for name, mut := range cases {
		r := lockedRoute(t, true)
		mut(&r)
		require.Error(t, ValidateRoutes([]agentprotocol.Route{r}, def), name)
	}

	// A wildcard host can be IP-locked, only the client certificate is refused.
	w := lockedRoute(t, false)
	w.Hosts[0], w.Domains[0].Host = "*.example.com", "*.example.com"
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{w}, def))

	// L4 routes carry no lock.
	l4 := l4Route(svcApp, "tcp", 5432, "10.0.0.5:5432")
	l4.OriginLock = &agentprotocol.OriginLock{CloudflareCIDRs: cfCIDRs()}
	require.Error(t, ValidateRoutes([]agentprotocol.Route{l4}, agentprotocol.UpstreamPolicy{AllowHostGateway: true, AllowLoopback: true}))

	// The same host with the same lock state in two rules (path based) is fine.
	two := lockedRoute(t, true)
	two.Domains = append(two.Domains, agentprotocol.DomainRule{ID: domC, Host: "app.example.com", PathPrefix: "/x", OriginLocked: true, RequireClientCert: true})
	require.NoError(t, ValidateRoutes([]agentprotocol.Route{two}, def))
}

// Direct callers that skip validation fail closed: a locked domain with no
// usable range admits nobody.
func TestOriginLockedWithoutRangesFailsClosed(t *testing.T) {
	r := lockedRoute(t, false)
	r.OriginLock = nil
	cfg := buildDynamic([]agentprotocol.Route{r}, Options{Generation: 1})
	found := false
	for name, mw := range cfg.HTTP.Middlewares {
		if strings.HasPrefix(name, lockMiddlewarePrefix) {
			found = true
			require.Equal(t, []string{neverMatch}, mw.IPAllowList.SourceRange)
		}
	}
	require.True(t, found)
}

func TestOriginLockRoundTripsThroughJSON(t *testing.T) {
	r := lockedRoute(t, true)
	b, err := json.Marshal(r)
	require.NoError(t, err)
	var back agentprotocol.Route
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, r, back)
	require.Contains(t, string(b), `"originLock":{"cloudflareCidrs":`)
	require.Contains(t, string(b), `"originLocked":true`)
	require.Contains(t, string(b), `"requireClientCert":true`)

	// A route without the lock serialises without the lock fields.
	plain, _ := json.Marshal(twoRoutes()[0])
	require.NotContains(t, string(plain), "originLock")
}
