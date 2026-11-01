//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pageResp is one response of the proxy.
type pageResp struct {
	Status int
	Header http.Header
	Body   string
}

// proxyGet requests path with Host/SNI = host from the harness proxy, over
// https (or plain http on the web port), without following redirects.
func proxyGet(host, path string, plain bool) (pageResp, error) {
	port, scheme := proxyPorts.HTTPS, "https"
	if plain {
		port, scheme = proxyPorts.HTTP, "http"
	}
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	c := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
			},
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: host}, //nolint:gosec // test-only self-signed default cert
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var res *http.Response
	var err error
	for range 5 { // Traefik's ports blink while Docker attaches networks (see GetRetry)
		if res, err = c.Get(scheme + "://" + host + path); err == nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if err != nil {
		return pageResp{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return pageResp{Status: res.StatusCode, Header: res.Header, Body: string(b)}, err
}

// waitPage waits until GET host/path answers with a status accepted by ok.
func waitPage(t *testing.T, host, path string, timeout time.Duration, ok func(pageResp) bool) pageResp {
	t.Helper()
	var last pageResp
	var err error
	good := eventually(timeout, 300*time.Millisecond, func() bool {
		last, err = proxyGet(host, path, false)
		return err == nil && ok(last)
	})
	require.True(t, good, "GET https://%s%s: last %d err=%v\n%.400s", host, path, last.Status, err, last.Body)
	return last
}

func isOurPage(r pageResp, kind string) bool {
	return strings.Contains(r.Body, `data-kind="`+kind+`"`) && strings.Contains(r.Header.Get("Content-Type"), "text/html")
}

// 14. maintenance mode and automatic error pages: our self-contained page, the
// right status and headers, instantly, surviving a deployment, and the
// 404 / 502-503 pages for unknown hosts and a stopped service.
func TestMaintenanceAndErrorPages(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	svc, px := liveV1(t, s, "web", nil)
	svcPath := "/services/" + svc.ID

	// -- maintenance on: 503 + Retry-After + our page, escaped text -------------
	eta := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	r := s.Do("PUT", svcPath+"/maintenance", map[string]any{
		"enabled": true, "title": "Upgrading <db>", "message": "Back at <b>5</b> & soon", "eta": eta.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.True(t, r.JSON().Obj("maintenance").Bool("enabled"))
	require.Equal(t, s.Service(svc.ID).Int("generation"), s.Service(svc.ID).Int("appliedGeneration"), "never staged")

	page := waitPage(t, s.Host(), "/", 30*time.Second, func(p pageResp) bool { return p.Status == http.StatusServiceUnavailable })
	require.True(t, isOurPage(page, "maintenance"), "%.300s", page.Body)
	require.Contains(t, page.Body, "Upgrading &lt;db&gt;")
	require.Contains(t, page.Body, "Back at &lt;b&gt;5&lt;/b&gt; &amp; soon")
	require.NotContains(t, page.Body, "<b>5</b>")
	require.Contains(t, page.Body, `datetime="`+eta.Format(time.RFC3339)+`"`)
	require.Equal(t, "no-store", page.Header.Get("Cache-Control"))
	retry, err := http.ParseTime(page.Header.Get("Retry-After"))
	require.NoError(t, err, "Retry-After is the ETA as an HTTP date: %q", page.Header.Get("Retry-After"))
	require.WithinDuration(t, eta, retry, time.Second)
	for _, bad := range []string{"<link", "src=\"http", "href=\"http", "@import"} {
		require.NotContains(t, page.Body, bad, "the page makes no external request")
	}
	// Every path of every domain, also over plain http after the https redirect.
	other, err := proxyGet(s.Host(), "/some/deep/path?x=1", false)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, other.Status)
	redir, err := proxyGet(s.Host(), "/", true)
	require.NoError(t, err)
	require.Equal(t, http.StatusMovedPermanently, redir.Status, "http still redirects to https first")

	// The preview endpoint renders the same page (200, its own CSP).
	pv := s.Do("GET", svcPath+"/maintenance/preview?draft=true&title=Draft", nil)
	require.Equal(t, http.StatusOK, pv.Status)
	require.Contains(t, string(pv.Raw), `data-kind="maintenance"`)
	require.Contains(t, string(pv.Raw), "Draft")

	// Repeating the request is a no-op.
	again := s.Do("PUT", svcPath+"/maintenance", map[string]any{
		"enabled": true, "title": "Upgrading <db>", "message": "Back at <b>5</b> & soon", "eta": eta.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusOK, again.Status)

	// -- a deployment during maintenance verifies against the real service and
	// does not end the window ---------------------------------------------------
	s.SetVars(svc.ID, map[string]string{"VERSION": "v2"}, nil)
	d := s.WaitDeployment(s.Deploy(svc.ID), 3*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, s.DeploymentLogs(d.Str("id")))
	still, err := proxyGet(s.Host(), "/", false)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, still.Status, "maintenance survives the deployment")
	require.True(t, isOurPage(still, "maintenance"))

	// -- off: the service answers again (v2) ----------------------------------------
	r = s.Do("PUT", svcPath+"/maintenance", map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.False(t, r.JSON().Obj("maintenance").Bool("enabled"))
	px.WaitBody(t, "/", "v2", 30*time.Second)

	// -- unknown host: our 404 page (https and plain http) ---------------------------
	nf := waitPage(t, "nobody.localhost", "/", 20*time.Second, func(p pageResp) bool { return isOurPage(p, "404") })
	require.Equal(t, http.StatusNotFound, nf.Status)
	require.Contains(t, nf.Body, "No service is configured for this address.")
	require.Equal(t, "no-store", nf.Header.Get("Cache-Control"))
	nfPlain, err := proxyGet("nobody.localhost", "/x", true)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, nfPlain.Status)
	require.True(t, isOurPage(nfPlain, "404"))

	// -- stopped service: our 502/503 page, with the status of the failure -----------
	require.Equal(t, http.StatusAccepted, s.Do("POST", svcPath+"/stop", nil).Status)
	down := waitPage(t, s.Host(), "/", 60*time.Second, func(p pageResp) bool {
		return (p.Status == http.StatusBadGateway || p.Status == http.StatusServiceUnavailable) && isOurPage(p, fmt.Sprint(p.Status))
	})
	require.Contains(t, down.Body, "Service unavailable")
	require.Equal(t, "30", down.Header.Get("Retry-After"))
	require.Equal(t, "no-store", down.Header.Get("Cache-Control"))
}

// Every change of any service's maintenance text replaces the agent's pages
// server, which serves every maintenance and error page of the server. While a
// neighbour's text churns, a service in maintenance must answer 503 every
// time: the replacement may never leave a moment without a pages server (that
// moment is a 502, and it hit the request right after a deployment in CI).
func TestMaintenanceSurvivesPagesReplacement(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	svc, _ := liveV1(t, s, "web", nil)
	app := appFixture(t)
	other := s.NewService(SvcSpec{Name: "churn", Image: app.AppImage, Vars: map[string]string{"VERSION": "n"}, Domain: s.Host("churn")})
	s.DeployOK(other.ID)

	r := s.Do("PUT", "/services/"+svc.ID+"/maintenance", map[string]any{"enabled": true, "title": "Upgrading"})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	waitPage(t, s.Host(), "/", 30*time.Second, func(p pageResp) bool { return p.Status == http.StatusServiceUnavailable })

	stop := make(chan struct{})
	var churn sync.WaitGroup
	churn.Add(1)
	go func() {
		defer churn.Done()
		for i := 0; ; i++ {
			r := s.Do("PUT", "/services/"+other.ID+"/maintenance", map[string]any{"enabled": true, "title": fmt.Sprintf("churn %d", i)})
			if r.Status != http.StatusOK {
				t.Errorf("maintenance change: %d %s", r.Status, r.Raw)
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
	}()
	counts := map[int]int{}
	// What answered when it was not the maintenance page: the app (its
	// VERSION body) or the pages server without this service's page (nginx).
	var stray []string
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		p, err := proxyGet(s.Host(), "/", false)
		if err != nil {
			counts[-1]++
			continue
		}
		counts[p.Status]++
		if p.Status != http.StatusServiceUnavailable && len(stray) < 5 {
			body := p.Body
			if len(body) > 80 {
				body = body[:80]
			}
			stray = append(stray, fmt.Sprintf("%s %d server=%q body=%q",
				time.Now().UTC().Format("15:04:05.000"), p.Status, p.Header.Get("Server"), body))
		}
	}
	close(stop)
	churn.Wait()
	require.Equal(t, []int{http.StatusServiceUnavailable}, keysOf(counts),
		"only the maintenance page answered: %v; first others: %v", counts, stray)
}

func keysOf(m map[int]int) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
