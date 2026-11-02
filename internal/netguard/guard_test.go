package netguard_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/netguard"
)

func TestCheckHostResolvesNamesAndRefusesWhatTheyPointAt(t *testing.T) {
	ctx := context.Background()
	t.Setenv(netguard.AllowEnv, "")

	require.ErrorIs(t, netguard.CheckHost(ctx, ""), netguard.ErrBlocked)
	require.ErrorIs(t, netguard.CheckHost(ctx, "[]"), netguard.ErrBlocked)
	// localhost resolves to a loopback address, which is refused.
	require.ErrorIs(t, netguard.CheckHost(ctx, "localhost"), netguard.ErrBlocked)
	// A name that does not resolve is an error of its own, not "blocked".
	err := netguard.CheckHost(ctx, "no-such-host.invalid")
	require.ErrorContains(t, err, "does not resolve")
	require.NotErrorIs(t, err, netguard.ErrBlocked)
}

func TestCheckHostTrustsAnAllowedHostNameWithoutResolvingIt(t *testing.T) {
	// The name does not resolve anywhere; being on the list is enough. The entry
	// is matched case-insensitively and without a trailing dot.
	t.Setenv(netguard.AllowEnv, " Git.Corp.Example. ")
	require.NoError(t, netguard.CheckHost(context.Background(), "git.corp.example"))
	require.NoError(t, netguard.CheckHost(context.Background(), "GIT.CORP.EXAMPLE."))
	require.ErrorContains(t, netguard.CheckHost(context.Background(), "other.corp.example"), "does not resolve")
}

func TestCheckHostAllowsAPrivateAddressOnlyWhenListed(t *testing.T) {
	ctx := context.Background()
	t.Setenv(netguard.AllowEnv, "10.0.0.0/24")
	require.NoError(t, netguard.CheckHost(ctx, "10.0.0.7"))
	require.ErrorIs(t, netguard.CheckHost(ctx, "10.0.1.7"), netguard.ErrBlocked)
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestDialContextRefusesAddressesWithoutAPort(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "")
	_, err := netguard.DialContext(time.Second)(context.Background(), "tcp", "8.8.8.8")
	require.ErrorIs(t, err, netguard.ErrBlocked)
}

func TestDialContextRefusesALoopbackTargetUnlessItsNameIsAllowed(t *testing.T) {
	l := listenLoopback(t)
	_, port, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err)
	dial := netguard.DialContext(2 * time.Second)

	t.Setenv(netguard.AllowEnv, "")
	_, err = dial(context.Background(), "tcp", "127.0.0.1:"+port)
	require.ErrorIs(t, err, netguard.ErrBlocked, "a literal loopback address")
	_, err = dial(context.Background(), "tcp", "localhost:"+port)
	require.ErrorIs(t, err, netguard.ErrBlocked, "a name that resolves to loopback")

	// The admin vouches for the name: it is dialed as is.
	t.Setenv(netguard.AllowEnv, "localhost")
	accepted := make(chan struct{})
	go func() {
		c, aerr := l.Accept()
		if aerr == nil {
			_ = c.Close()
		}
		close(accepted)
	}()
	conn, err := dial(context.Background(), "tcp", "localhost:"+port)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	<-accepted
}

func TestDialContextAllowsAListedLiteralAddress(t *testing.T) {
	l := listenLoopback(t)
	_, port, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err)
	go func() {
		if c, aerr := l.Accept(); aerr == nil {
			_ = c.Close()
		}
	}()

	t.Setenv(netguard.AllowEnv, "127.0.0.0/8")
	conn, err := netguard.DialContext(2*time.Second)(context.Background(), "tcp", "127.0.0.1:"+port)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestClientStopsAfterFiveRedirects(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		http.Redirect(w, r, srv.URL+fmt.Sprintf("/hop%d", n), http.StatusFound)
	}))
	defer srv.Close()

	resp, err := netguard.NewClient(5 * time.Second).Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.ErrorContains(t, err, "too many redirects")
	require.EqualValues(t, 5, hits.Load(), "the first request plus four followed redirects")
}

func TestClientFollowsAFewRedirects(t *testing.T) {
	t.Setenv(netguard.AllowEnv, "127.0.0.1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			_, _ = w.Write([]byte("done"))
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()

	resp, err := netguard.NewClient(5 * time.Second).Get(srv.URL + "/start")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "/final", resp.Request.URL.Path)
}

func TestClientRefusesAHTTPSToHTTPDowngrade(t *testing.T) {
	c := netguard.NewClient(time.Second)
	secure := &http.Request{URL: &url.URL{Scheme: "https", Host: "a.example"}}
	plain := &http.Request{URL: &url.URL{Scheme: "http", Host: "b.example"}}

	require.ErrorContains(t, c.CheckRedirect(plain, []*http.Request{secure}), "https to http")
	require.NoError(t, c.CheckRedirect(secure, []*http.Request{plain}), "an upgrade is fine")
	require.NoError(t, c.CheckRedirect(secure, []*http.Request{secure}))
	require.NoError(t, c.CheckRedirect(plain, []*http.Request{plain}))
}

func TestTransportIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	tr := netguard.NewTransport()
	require.Nil(t, tr.Proxy, "a proxy would be handed the destination as a name")
	require.NotNil(t, tr.DialContext)
}
