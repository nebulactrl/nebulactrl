package agentprotocol

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckUpstream(t *testing.T) {
	def := DefaultUpstreamPolicy()
	allowAll := UpstreamPolicy{AllowLoopback: true, AllowLinkLocal: true, AllowHostGateway: true}
	noGateway := UpstreamPolicy{}

	denied := []struct {
		name, addr string
		policy     UpstreamPolicy
	}{
		{"loopback v4", "127.0.0.1:8080", def},
		{"loopback v4 range", "127.1.2.3:80", def},
		{"loopback v6", "[::1]:80", def},
		{"v4-mapped loopback", "[::ffff:127.0.0.1]:80", def},
		{"localhost", "localhost:80", def},
		{"sub localhost", "api.localhost:80", def},
		{"metadata ip", "169.254.169.254:80", def},
		{"link-local range", "169.254.10.10:80", def},
		{"link-local v6", "[fe80::1]:80", def},
		{"gcp metadata", "metadata.google.internal:80", def},
		{"gcp metadata upper", "Metadata.Google.Internal:80", def},
		{"short metadata", "metadata:80", def},
		{"instance-data", "instance-data:80", def},
		{"proxy container", "nebulactrl-traefik:8080", allowAll},
		{"agent container", "nebulactrl-agent:80", allowAll},
		{"tailscale container", "nebulactrl-tailscale:80", allowAll},
		{"proxy container trailing dot", "nebulactrl-traefik.:80", allowAll},
		{"unspecified v4", "0.0.0.0:80", allowAll},
		{"unspecified v6", "[::]:80", allowAll},
		{"multicast", "239.1.1.1:80", allowAll},
		{"link-local multicast", "224.0.0.1:80", def},
		{"host gateway disabled", "host.docker.internal:80", noGateway},
		{"admin port via localhost", "localhost:9000", allowAll},
		{"admin port via gateway", "host.docker.internal:9000", allowAll},
		{"no port", "example.com", def},
		{"empty host", ":80", def},
		{"empty port", "example.com:", def},
		{"port zero", "example.com:0", def},
		{"port too large", "example.com:65536", def},
		{"port with sign", "example.com:+80", def},
		{"port with leading zero", "example.com:080", def},
		{"port not a number", "example.com:http", def},
		{"bad host chars", "bad host:80", def},
		{"host with backtick", "a`b:80", def},
		{"host with newline", "a\nb:80", def},
		{"host with slash", "a/b:80", def},
		{"leading dash", "-a.com:80", def},
	}
	for _, c := range denied {
		require.Error(t, CheckUpstream(c.addr, c.policy), c.name)
	}

	allowed := []struct {
		name, addr string
		policy     UpstreamPolicy
	}{
		{"LAN", "192.168.1.20:8123", def},
		{"10/8", "10.0.0.5:80", def},
		{"tailnet CGNAT", "100.64.1.2:443", def},
		{"tailnet high", "100.127.255.254:443", def},
		{"public ip", "93.184.216.34:443", def},
		{"public dns", "example.com:443", def},
		{"managed container", "nc-app-abc12345-0:8080", def},
		{"host gateway", "host.docker.internal:8123", def},
		{"v6 lan", "[fd00::1]:80", def},
		{"loopback with admin toggle", "127.0.0.1:8080", UpstreamPolicy{AllowLoopback: true}},
		{"localhost with admin toggle", "localhost:3000", UpstreamPolicy{AllowLoopback: true}},
		{"link-local with admin toggle", "169.254.169.254:80", UpstreamPolicy{AllowLinkLocal: true}},
		{"metadata name with admin toggle", "metadata.google.internal:80", UpstreamPolicy{AllowLinkLocal: true}},
		{"a similar but different name", "nebulactrl.example.com:80", def},
		{"admin port on another host", "192.168.1.20:9000", def},
	}
	for _, c := range allowed {
		require.NoError(t, CheckUpstream(c.addr, c.policy), c.name)
	}
}

// CheckUpstream must never panic and must never accept the always-denied
// classes, whatever the policy or the (mangled) input.
func TestCheckUpstreamRandomized(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	alphabet := []string{"a", "1", ".", ":", "[", "]", "-", "_", "%", "/", "`", " ", "\n", "nebulactrl-", "localhost", "127.0.0.1", "0.0.0.0", "::", "169.254", "9000", "host.docker.internal"}
	for range 5000 {
		var b strings.Builder
		for j := rng.IntN(8) + 1; j > 0; j-- {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		addr := b.String()
		p := UpstreamPolicy{AllowLoopback: rng.IntN(2) == 0, AllowLinkLocal: rng.IntN(2) == 0, AllowHostGateway: rng.IntN(2) == 0}
		err := CheckUpstream(addr, p)
		if err != nil {
			continue
		}
		low := strings.ToLower(addr)
		require.False(t, strings.Contains(low, "nebulactrl-"), "accepted %q", addr)
		require.False(t, strings.HasPrefix(low, "0.0.0.0:") || strings.HasPrefix(low, "[::]:"), "accepted %q", addr)
		require.False(t, strings.ContainsAny(addr, "` \n/"), "accepted %q", addr)
	}
}

func FuzzCheckUpstream(f *testing.F) {
	for _, s := range []string{"127.0.0.1:80", "[::1]:80", "a.b:1", "nebulactrl-x:1", "host.docker.internal:9000", "::", ":", "a:b:c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = CheckUpstream(s, UpstreamPolicy{})
		_ = CheckUpstream(s, UpstreamPolicy{AllowLoopback: true, AllowLinkLocal: true, AllowHostGateway: true})
	})
}

func TestValidateListenPort(t *testing.T) {
	for _, p := range []int{0, 1, 80, 443, 1023, ProxyProbePort, ProxyAdminPort, 65536, -1} {
		require.Error(t, ValidateListenPort(p), p)
	}
	for _, p := range []int{1024, 5432, 8080, 51820, 65535} {
		require.NoError(t, ValidateListenPort(p), p)
	}
}

func TestValidPathPrefix(t *testing.T) {
	for _, p := range []string{"", "/", "/api", "/api/v1", "/a.b-c_d~e", "/API/Users"} {
		if p == "/" {
			require.Error(t, ValidPathPrefix(p), "a bare slash needs no prefix")
			continue
		}
		require.NoError(t, ValidPathPrefix(p), p)
	}
	bad := []string{"api", "/api/", "//api", "/a//b", "/a/../b", "/..", "/a b", "/a`b", "/a)||Host(`x", "/a?x=1", "/a#x", "/a\n", "/é", "/" + strings.Repeat("a", 201)}
	for _, p := range bad {
		require.Error(t, ValidPathPrefix(p), p)
	}
}

func TestValidHostnameAndUsername(t *testing.T) {
	for _, h := range []string{"a.example.com", "a", "10.0.0.5", "xn--bcher-kva.example", "a_b.example.com", "A-B.c"} {
		require.True(t, ValidHostname(h), h)
	}
	for _, h := range []string{"", "-a", "a-", "a b", "a`b", "a)||Host(`b", "a/b", "a\"b", "a:80", strings.Repeat("a", 254)} {
		require.False(t, ValidHostname(h), h)
	}
	for _, u := range []string{"admin", "a.b-c_d", strings.Repeat("a", 64)} {
		require.True(t, ValidBasicUsername(u), u)
	}
	for _, u := range []string{"", "a:b", "a b", "a\n", strings.Repeat("a", 65), "é"} {
		require.False(t, ValidBasicUsername(u), u)
	}
}

func TestValidBasicAuthLines(t *testing.T) {
	hash := "$2a$10$" + strings.Repeat("a", 53)
	require.NoError(t, ValidBasicAuthLines(nil))
	require.NoError(t, ValidBasicAuthLines([]string{"alice:" + hash, "bob:" + strings.Replace(hash, "$2a", "$2y", 1)}))
	for _, l := range []string{"alice", "alice:", "alice:plaintext", "alice:{SHA}abc", "alice:$apr1$x$y", ":" + hash, "a b:" + hash} {
		require.Error(t, ValidBasicAuthLines([]string{l}), l)
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf("u%d:%s", i, hash)
	}
	require.Error(t, ValidBasicAuthLines(many))
	// Errors name the user, never the hash.
	err := ValidBasicAuthLines([]string{"alice:notbcrypt-SECRET"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET")
}

func TestRouteOptionsValidate(t *testing.T) {
	require.NoError(t, RouteOptions{}.Validate())
	require.True(t, RouteOptions{}.IsZero())
	require.False(t, RouteOptions{Compress: true}.IsZero())
	require.True(t, RouteOptions{}.ForceHTTPSOrDefault())
	require.False(t, RouteOptions{ForceHTTPS: new(false)}.ForceHTTPSOrDefault())

	ok := RouteOptions{
		SecurityHeaders: "strict", Compress: true, Noindex: true,
		RequestHeaders:  []Header{{Name: "X-Custom", Value: "1"}},
		ResponseHeaders: []Header{{Name: "Cache-Control", Value: "no-store"}},
		CORS:            &CORS{Origins: []string{"https://app.example.com", "http://localhost:3000"}, Methods: []string{"GET", "POST"}, Headers: []string{"Authorization"}, MaxAgeSeconds: 600},
		Redirects:       []Redirect{{Regex: `^https://a/(.*)`, Replacement: "https://b/${1}", Permanent: true}},
		RateLimit:       &RateLimit{Average: 100, PeriodSeconds: 1, Burst: 50, TrustedProxyDepth: 1},
		Access:          &Access{AllowCIDRs: []string{"10.0.0.0/8", "192.168.1.5", "2001:db8::/32"}, RejectStatus: 403},
		BasicAuth:       true,
	}
	require.NoError(t, ok.Validate())

	cases := map[string]func(o *RouteOptions){
		"security preset":       func(o *RouteOptions) { o.SecurityHeaders = "paranoid" },
		"header name space":     func(o *RouteOptions) { o.RequestHeaders = []Header{{Name: "X Bad", Value: "1"}} },
		"header name colon":     func(o *RouteOptions) { o.ResponseHeaders = []Header{{Name: "X:Bad", Value: "1"}} },
		"header name empty":     func(o *RouteOptions) { o.RequestHeaders = []Header{{Name: "", Value: "1"}} },
		"forbidden Host":        func(o *RouteOptions) { o.RequestHeaders = []Header{{Name: "Host", Value: "evil"}} },
		"forbidden XFF":         func(o *RouteOptions) { o.RequestHeaders = []Header{{Name: "x-forwarded-for", Value: "1.1.1.1"}} },
		"forbidden Upgrade":     func(o *RouteOptions) { o.RequestHeaders = []Header{{Name: "Upgrade", Value: "websocket"}} },
		"response Set-Cookie":   func(o *RouteOptions) { o.ResponseHeaders = []Header{{Name: "set-cookie", Value: "a=b"}} },
		"header value newline":  func(o *RouteOptions) { o.RequestHeaders = []Header{{Name: "X-A", Value: "a\r\nX-B: c"}} },
		"header value long":     func(o *RouteOptions) { o.ResponseHeaders = []Header{{Name: "X-A", Value: strings.Repeat("a", 2049)}} },
		"too many headers":      func(o *RouteOptions) { o.RequestHeaders = make([]Header, 31) },
		"cors no origins":       func(o *RouteOptions) { o.CORS = &CORS{} },
		"cors bad origin":       func(o *RouteOptions) { o.CORS = &CORS{Origins: []string{"example.com"}} },
		"cors origin path":      func(o *RouteOptions) { o.CORS = &CORS{Origins: []string{"https://a.com/x"}} },
		"cors star credentials": func(o *RouteOptions) { o.CORS = &CORS{Origins: []string{"*"}, Credentials: true} },
		"cors lower method":     func(o *RouteOptions) { o.CORS = &CORS{Origins: []string{"*"}, Methods: []string{"get"}} },
		"cors bad header":       func(o *RouteOptions) { o.CORS = &CORS{Origins: []string{"*"}, Headers: []string{"A B"}} },
		"cors max age":          func(o *RouteOptions) { o.CORS = &CORS{Origins: []string{"*"}, MaxAgeSeconds: 86401} },
		"redirect empty regex":  func(o *RouteOptions) { o.Redirects = []Redirect{{Regex: "", Replacement: "x"}} },
		"redirect bad regex":    func(o *RouteOptions) { o.Redirects = []Redirect{{Regex: "(", Replacement: "x"}} },
		"redirect long regex":   func(o *RouteOptions) { o.Redirects = []Redirect{{Regex: strings.Repeat("a", 513), Replacement: "x"}} },
		"redirect repl newline": func(o *RouteOptions) { o.Redirects = []Redirect{{Regex: "a", Replacement: "x\ny"}} },
		"too many redirects":    func(o *RouteOptions) { o.Redirects = make([]Redirect, 11) },
		"rate average":          func(o *RouteOptions) { o.RateLimit = &RateLimit{Average: 0, PeriodSeconds: 1, Burst: 1} },
		"rate period":           func(o *RouteOptions) { o.RateLimit = &RateLimit{Average: 1, PeriodSeconds: 3601, Burst: 1} },
		"rate depth": func(o *RouteOptions) {
			o.RateLimit = &RateLimit{Average: 1, PeriodSeconds: 1, Burst: 1, TrustedProxyDepth: 6}
		},
		"access empty":          func(o *RouteOptions) { o.Access = &Access{} },
		"access bad cidr":       func(o *RouteOptions) { o.Access = &Access{AllowCIDRs: []string{"10.0.0.0/33"}} },
		"access hostname":       func(o *RouteOptions) { o.Access = &Access{AllowCIDRs: []string{"example.com"}} },
		"access injection":      func(o *RouteOptions) { o.Access = &Access{AllowCIDRs: []string{"10.0.0.0/8`)"}} },
		"access depth":          func(o *RouteOptions) { o.Access = &Access{AllowCIDRs: []string{"10.0.0.0/8"}, TrustedProxyDepth: 9} },
		"access reject status":  func(o *RouteOptions) { o.Access = &Access{AllowCIDRs: []string{"10.0.0.0/8"}, RejectStatus: 200} },
		"access too many CIDRs": func(o *RouteOptions) { o.Access = &Access{AllowCIDRs: make([]string, 201)} },
	}
	for name, mut := range cases {
		o := ok
		mut(&o)
		require.Error(t, o.Validate(), name)
	}
}

func TestDomainRuleValidate(t *testing.T) {
	good := DomainRule{ID: "11111111-2222-3333-4444-555555555555", Host: "app.example.com", PathPrefix: "/api", StripPrefix: true, AddPrefix: "/v2"}
	require.NoError(t, good.Validate())

	redirect := DomainRule{ID: good.ID, Host: "www.example.com", Kind: DomainRedirect, RedirectTo: "example.com", RedirectCode: 301, PreservePath: true}
	require.NoError(t, redirect.Validate())

	withAuth := good
	withAuth.Options.BasicAuth, withAuth.AuthRef = true, SecretAuthPrefix+good.ID
	require.NoError(t, withAuth.Validate())

	mutate := map[string]func(d *DomainRule){
		"host":              func(d *DomainRule) { d.Host = "bad host" },
		"host injection":    func(d *DomainRule) { d.Host = "a`) || Host(`b" },
		"path prefix":       func(d *DomainRule) { d.PathPrefix = "api" },
		"path traversal":    func(d *DomainRule) { d.PathPrefix = "/a/../b" },
		"add prefix":        func(d *DomainRule) { d.AddPrefix = "v2/" },
		"kind":              func(d *DomainRule) { d.Kind = "proxy" },
		"redirect self":     func(d *DomainRule) { *d = redirect; d.RedirectTo = d.Host },
		"redirect target":   func(d *DomainRule) { *d = redirect; d.RedirectTo = "http://x" },
		"redirect code":     func(d *DomainRule) { *d = redirect; d.RedirectCode = 303 },
		"tls mode":          func(d *DomainRule) { d.TLSMode = "strict" },
		"basic auth no ref": func(d *DomainRule) { d.Options.BasicAuth = true },
		"bad options":       func(d *DomainRule) { d.Options.SecurityHeaders = "x" },
	}
	for name, mut := range mutate {
		d := good
		mut(&d)
		require.Error(t, d.Validate(), name)
	}
	for _, code := range []int{0, 301, 302, 307, 308} {
		r := redirect
		r.RedirectCode = code
		require.NoError(t, r.Validate(), code)
	}
	for _, mode := range []string{"", TLSAuto, TLSCustom, TLSNone} {
		d := good
		d.TLSMode = mode
		require.NoError(t, d.Validate(), mode)
	}
}

func TestProxyConfigureRegistered(t *testing.T) {
	require.True(t, AllowedKinds[KindProxyConfigure])
	require.Equal(t, "proxy.v2", CapProxyV2)
	require.True(t, (Route{RouteExt: RouteExt{Protocol: ProtoTCP}}).IsL4())
	require.True(t, (Route{RouteExt: RouteExt{Protocol: ProtoUDP}}).IsL4())
	require.False(t, (Route{}).IsL4())
	require.False(t, (Route{RouteExt: RouteExt{Protocol: ProtoHTTP}}).IsL4())
}
