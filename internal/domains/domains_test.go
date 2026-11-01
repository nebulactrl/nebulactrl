package domains

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

func TestValidateHostname(t *testing.T) {
	good := map[string]string{
		"app.example.com":      "app.example.com",
		"App.Example.COM.":     "app.example.com",
		"whoami.localhost":     "whoami.localhost",
		"a-b.c1.example.co.uk": "a-b.c1.example.co.uk",
		"1.2.3.4.sslip.io":     "1.2.3.4.sslip.io",
	}
	for in, want := range good {
		got, err := ValidateHostname(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got)
	}
	bad := []string{
		"", "localhost", "-a.example.com", "a-.example.com", "a..example.com", "a_b.example.com", "under score.example.com",
		"1.2.3.4", strings.Repeat("a", 64) + ".example.com", strings.Repeat("a.", 130) + "com", "http://example.com",
	}
	for _, in := range bad {
		_, err := ValidateHostname(in)
		ae, ok := apierr.As(err)
		require.True(t, ok, in)
		require.Equal(t, "invalid_hostname", ae.Code, in)
	}
}

func TestGenerateHostnameFormatAndTruncation(t *testing.T) {
	require.Equal(t, "web-production-shop.1.2.3.4.sslip.io", GenerateHostname("web", "production", "shop", "1.2.3.4.sslip.io"))

	long := strings.Repeat("s", 48)
	host := GenerateHostname(long, long, long, "example.com")
	first, rest, _ := strings.Cut(host, ".")
	require.LessOrEqual(t, len(first), 63)
	require.Equal(t, "example.com", rest)
	require.False(t, strings.HasSuffix(first, "-"))
	_, err := ValidateHostname(host)
	require.NoError(t, err)

	// Truncation must not leave a trailing hyphen.
	name := strings.Repeat("a", 62) + "-b"
	h2 := GenerateHostname(name, "e", "p", "example.com")
	first2, _, _ := strings.Cut(h2, ".")
	require.False(t, strings.HasSuffix(first2, "-"))
	require.LessOrEqual(t, len(first2), 63)
}

func TestBaseDomain(t *testing.T) {
	ip, dom := "203.0.113.9", "Apps.Example.com"
	got, ok := BaseDomain(db.Server{BaseDomain: &dom, PublicIp: &ip})
	require.True(t, ok)
	require.Equal(t, "apps.example.com", got)

	got, ok = BaseDomain(db.Server{PublicIp: &ip})
	require.True(t, ok)
	require.Equal(t, "203.0.113.9.sslip.io", got)

	_, ok = BaseDomain(db.Server{})
	require.False(t, ok)
}

type fakeResolver struct {
	addrs []string
	err   error
}

func (f fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	var out []net.IPAddr
	for _, a := range f.addrs {
		out = append(out, net.IPAddr{IP: net.ParseIP(a)})
	}
	return out, f.err
}

func TestDNSEvaluate(t *testing.T) {
	ip := "203.0.113.9"
	c := &DNSChecker{resolver: fakeResolver{addrs: []string{"203.0.113.9"}}}
	ok, msg := c.evaluate(context.Background(), "app.example.com", &ip)
	require.NotNil(t, ok)
	require.True(t, *ok)
	require.Contains(t, msg, ip)

	c.resolver = fakeResolver{addrs: []string{"198.51.100.1"}}
	ok, msg = c.evaluate(context.Background(), "app.example.com", &ip)
	require.NotNil(t, ok)
	require.False(t, *ok)
	require.Contains(t, msg, "Point an A record for app.example.com to 203.0.113.9")
	require.Contains(t, msg, "198.51.100.1")

	c.resolver = fakeResolver{err: errors.New("no such host")}
	ok, _ = c.evaluate(context.Background(), "app.example.com", &ip)
	require.NotNil(t, ok)
	require.False(t, *ok)

	// No public IP: unknown (null), never a false negative.
	ok, msg = c.evaluate(context.Background(), "app.example.com", nil)
	require.Nil(t, ok)
	require.Contains(t, msg, "Point an A record for app.example.com")

	ok, _ = c.evaluate(context.Background(), "whoami.localhost", &ip)
	require.Nil(t, ok)
}
