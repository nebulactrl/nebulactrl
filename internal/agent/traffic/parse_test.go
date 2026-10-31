package traffic

import (
	"bufio"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const svcA = "11111111-1111-1111-1111-111111111111"

// fixtureLines returns the Traefik v3 JSON access log lines in testdata/access.jsonl
// (captured from traefik:v3, v3.7.16, with the agent's accesslog flags).
func fixtureLines(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open("testdata/access.jsonl")
	require.NoError(t, err)
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	require.NoError(t, sc.Err())
	return out
}

func TestParseLineRoutedHTML(t *testing.T) {
	ev, err := ParseLine(fixtureLines(t)[0])
	require.NoError(t, err)
	require.Equal(t, svcA, ev.ServiceID, "the router name nc-<serviceId>-https attributes the request")
	require.Equal(t, "app.localhost", ev.Host)
	require.Equal(t, "/pricing", ev.Path, "query string is stripped")
	require.Equal(t, "GET", ev.Method)
	require.Equal(t, 200, ev.Status)
	require.InDelta(t, 12.345678, ev.DurationMs, 0.0001, "Duration is nanoseconds in the log")
	require.Equal(t, int64(2048), ev.BytesOut)
	require.Equal(t, "192.168.165.1", ev.ClientIP)
	require.Contains(t, ev.UserAgent, "Chrome/124")
	require.Equal(t, "https://news.example.org/post?id=1", ev.Referer)
	require.Equal(t, "text/html; charset=utf-8", ev.ContentType)
	require.Equal(t, time.Date(2025, 10, 26, 17, 33, 20, 123456789, time.UTC), ev.Time)
}

func TestParseLineIgnoresUncountedLines(t *testing.T) {
	lines := fixtureLines(t)
	cases := map[string]int{
		"status 0 (no response written)": 4,
		"http→https redirect router":     5,
		"ACME HTTP-01 challenge":         6,
		"non-access traefik log line":    8,
	}
	for name, idx := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseLine(lines[idx])
			require.ErrorIs(t, err, ErrIgnored)
		})
	}
}

func TestParseLineProviderSuffixIsIgnored(t *testing.T) {
	ev, err := ParseLine(fixtureLines(t)[1]) // router nc-<id>-https@rest
	require.NoError(t, err)
	require.Equal(t, svcA, ev.ServiceID)
	require.Equal(t, "/assets/app.9f8e7d6c5b4a.js", ev.Path)
}

func TestParseLineUnroutedAndErrors(t *testing.T) {
	lines := fixtureLines(t)

	ev, err := ParseLine(lines[3]) // 500
	require.NoError(t, err)
	require.Equal(t, 500, ev.Status)
	require.Equal(t, "POST", ev.Method)
	require.Equal(t, "/api/orders/12345", ev.Path)

	ev, err = ParseLine(lines[7]) // no RouterName: no route matched
	require.NoError(t, err)
	require.Empty(t, ev.ServiceID, "no service router = unrouted")
	require.Equal(t, "unknown.example.net", ev.Host)
	require.Equal(t, 404, ev.Status)

	_, err = ParseLine([]byte(`{"DownstreamStatus":200, "RequestHost":`))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrIgnored, "truncated JSON is an error, not an ignorable line")
}

func TestServiceFromRouter(t *testing.T) {
	id, redirect, ok := serviceFromRouter("nc-" + svcA + "-https@file")
	require.True(t, ok)
	require.False(t, redirect)
	require.Equal(t, svcA, id)

	id, redirect, ok = serviceFromRouter("nc-" + svcA + "-http@rest")
	require.True(t, ok)
	require.True(t, redirect)
	require.Equal(t, svcA, id)

	id, redirect, ok = serviceFromRouter("nc-" + svcA + "-https-local@rest")
	require.True(t, ok)
	require.False(t, redirect)
	require.Equal(t, svcA, id)

	for _, bad := range []string{"", "api@internal", "dashboard@internal", "nc-gen@rest", "nc-notauuid-https@file", "nc-" + svcA + "-probe@rest"} {
		_, _, ok := serviceFromRouter(bad)
		require.False(t, ok, bad)
	}
}

// Behind Cloudflare Traefik logs the whole X-Forwarded-For value as ClientHost.
// Entries left of the last one are visitor-supplied; the last one is the
// address the trusted proxy saw.
func TestClientIPIsTheLastForwardedEntry(t *testing.T) {
	require.Equal(t, "203.0.113.9", clientIP("203.0.113.9"))
	require.Equal(t, "198.51.100.7", clientIP("1.2.3.4, 198.51.100.7"))
	require.Equal(t, "2001:db8::7", clientIP("1.2.3.4,10.0.0.1, 2001:db8::7"))
	require.Equal(t, "", clientIP(""))
}

func TestCleanHostStripsPortAndCase(t *testing.T) {
	require.Equal(t, "app.example.com", cleanHost("App.Example.com:8443"))
	require.Equal(t, "app.example.com", cleanHost("app.example.com."))
	require.Equal(t, agentprotocol.IPLiteralHost, cleanHost("[::1]:8080"))
	require.Equal(t, agentprotocol.IPLiteralHost, cleanHost("127.0.0.1:18443"))
	require.Equal(t, agentprotocol.IPLiteralHost, cleanHost("10.0.0.5"))
	require.Equal(t, "1.example.com", cleanHost("1.example.com"))
	require.Equal(t, "(none)", cleanHost(""))
}

func TestRequestPath(t *testing.T) {
	require.Equal(t, "/a/b", requestPath("/a/b?x=1#f"))
	require.Equal(t, "/x", requestPath("http://h.example/x?y=1"))
	require.Equal(t, "/", requestPath("http://h.example"))
	require.Equal(t, "/", requestPath(""))
}

func TestNormalizePath(t *testing.T) {
	require.Equal(t, "/users/:id/posts", NormalizePath("/users/42/posts"))
	require.Equal(t, "/o/:id", NormalizePath("/o/3f2504e0-4f89-11d3-9a0c-0305e82c3301"))
	require.Equal(t, "/assets/app.9f8e7d6c5b4a.js", NormalizePath("/assets/app.9f8e7d6c5b4a.js"), "only whole segments are replaced")
	require.Equal(t, "/s/:id", NormalizePath("/s/9f8e7d6c5b4a"))
	require.Equal(t, "/blog/hello-world", NormalizePath("/blog/hello-world"))
	require.Equal(t, "/", NormalizePath("/"))
	require.LessOrEqual(t, len(NormalizePath("/"+string(make([]byte, 500)))), maxPathLen)
}

func TestReferrerHost(t *testing.T) {
	require.Equal(t, "news.example.org", ReferrerHost("https://News.Example.org/post?id=1", "app.example.com"))
	require.Equal(t, DirectReferrer, ReferrerHost("", "app.example.com"))
	require.Equal(t, DirectReferrer, ReferrerHost("not a url ::", "app.example.com"))
	require.Equal(t, agentprotocol.IPLiteralHost, ReferrerHost("http://192.168.1.10:3000/page", "app.example.com"))
	require.Empty(t, ReferrerHost("https://app.example.com/other", "app.example.com"), "internal navigation is not a source")
}

func TestClassifier(t *testing.T) {
	c := NewClassifier()
	chrome := c.Classify("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	require.Equal(t, Client{Browser: "Chrome", OS: "Windows", Device: "desktop"}, chrome)

	phone := c.Classify("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1")
	require.Equal(t, "mobile", phone.Device)
	require.False(t, phone.Bot)

	bot := c.Classify("Googlebot/2.1 (+http://www.google.com/bot.html)")
	require.True(t, bot.Bot)
	require.Equal(t, "bot", bot.Device)

	require.Equal(t, "unknown", c.Classify("").Device)
}
