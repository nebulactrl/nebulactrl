package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// idp serves doc as the discovery document, with the issuer filled in as the
// server's own URL unless doc already names one.
func idp(t *testing.T, status int, doc map[string]any) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != discoveryPath {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if doc == nil {
			_, _ = w.Write([]byte("<html>not json</html>"))
			return
		}
		out := map[string]any{"issuer": srv.URL}
		for k, v := range doc {
			out[k] = v
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func problemCode(t *testing.T, err error) string {
	t.Helper()
	var p *IssuerProblem
	require.ErrorAs(t, err, &p)
	require.NotEmpty(t, p.Message)
	return p.Code
}

func TestDiscoverIssuerAcceptsACompleteDocument(t *testing.T) {
	srv := idp(t, http.StatusOK, map[string]any{
		"authorization_endpoint": "https://idp.example/authorize", "token_endpoint": "https://idp.example/token", "jwks_uri": "https://idp.example/jwks",
	})
	got, err := DiscoverIssuer(context.Background(), srv.Client(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, srv.URL, got.Issuer)
	require.Equal(t, "https://idp.example/authorize", got.AuthorizationEndpoint)
	require.Equal(t, "https://idp.example/token", got.TokenEndpoint)
	require.Equal(t, "https://idp.example/jwks", got.JWKSURI)
}

func TestDiscoverIssuerSpecificFailures(t *testing.T) {
	ctx := context.Background()

	notFound := idp(t, http.StatusNotFound, nil)
	_, err := DiscoverIssuer(ctx, notFound.Client(), notFound.URL)
	require.Equal(t, IssuerNotFound, problemCode(t, err))
	require.Contains(t, err.Error(), "404")

	html := idp(t, http.StatusOK, nil)
	_, err = DiscoverIssuer(ctx, html.Client(), html.URL)
	require.Equal(t, IssuerBadResponse, problemCode(t, err))

	// The provider reports a different issuer (a tenant placeholder was left in,
	// or a trailing slash is missing).
	mismatch := idp(t, http.StatusOK, map[string]any{"issuer": "https://login.example.com/tenant-1/", "authorization_endpoint": "a", "token_endpoint": "t", "jwks_uri": "j"})
	_, err = DiscoverIssuer(ctx, mismatch.Client(), mismatch.URL)
	require.Equal(t, IssuerMismatch, problemCode(t, err))
	require.Contains(t, err.Error(), "tenant-1")

	incomplete := idp(t, http.StatusOK, map[string]any{"authorization_endpoint": "a"})
	_, err = DiscoverIssuer(ctx, incomplete.Client(), incomplete.URL)
	require.Equal(t, IssuerIncomplete, problemCode(t, err))
	require.Contains(t, err.Error(), "token_endpoint")
	require.Contains(t, err.Error(), "jwks_uri")
	require.NotContains(t, err.Error(), "authorization_endpoint")
}

func TestDiscoverIssuerRejectsABadURLWithoutAnyRequest(t *testing.T) {
	for _, raw := range []string{"", "not a url", "ftp://idp.example.com", "http://idp.example.com"} {
		_, err := DiscoverIssuer(context.Background(), &http.Client{Transport: failTransport{t}}, raw)
		require.Equal(t, IssuerInvalid, problemCode(t, err), raw)
	}
}

type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("no request expected")
	return nil, nil
}

func TestDiscoverIssuerTransportFailures(t *testing.T) {
	ctx := context.Background()

	// A closed port is "unreachable".
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	_, err := DiscoverIssuer(ctx, &http.Client{Timeout: 2 * time.Second}, url)
	require.Equal(t, IssuerUnreachable, problemCode(t, err))

	// A certificate this client does not trust.
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()
	_, err = DiscoverIssuer(ctx, &http.Client{Timeout: 2 * time.Second}, tlsSrv.URL)
	require.Equal(t, IssuerTLSInvalid, problemCode(t, err))

	// A host that never answers.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	_, err = DiscoverIssuer(ctx, &http.Client{Timeout: 100 * time.Millisecond}, slow.URL)
	require.Equal(t, IssuerTimeout, problemCode(t, err))

	// A name that cannot resolve (.invalid is reserved, RFC 6761).
	_, err = DiscoverIssuer(ctx, &http.Client{Timeout: 5 * time.Second}, "https://idp.nebulactrl.invalid")
	require.Contains(t, []string{IssuerHostNotFound, IssuerUnreachable, IssuerTimeout}, problemCode(t, err))
}

func TestDiscoverIssuerKeepsATrailingSlashExactly(t *testing.T) {
	// Auth0 style: the issuer ends with "/" and the document says so.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, discoveryPath, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": srv.URL + "/", "authorization_endpoint": srv.URL + "/authorize", "token_endpoint": srv.URL + "/token", "jwks_uri": srv.URL + "/jwks",
		})
	}))
	defer srv.Close()
	got, err := DiscoverIssuer(context.Background(), srv.Client(), srv.URL+"/")
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(got.Issuer, "/"))
	// Leaving the slash off is a mismatch the message explains.
	_, err = DiscoverIssuer(context.Background(), srv.Client(), srv.URL)
	require.Equal(t, IssuerMismatch, problemCode(t, err))
	require.Contains(t, err.Error(), "trailing slash")
}
