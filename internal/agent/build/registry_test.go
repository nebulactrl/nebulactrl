package build

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func jwtWith(access any) string {
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(map[string]any{"access": access}) + ".sig"
}

// bearerRegistry is a token-auth registry: /v2/ demands a bearer token, the
// token service needs robot/pw and grants the given actions.
func bearerRegistry(t *testing.T, actions []string) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			u, p, ok := r.BasicAuth()
			if !ok || u != "robot" || p != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var access []map[string]any
			if scope := r.URL.Query().Get("scope"); strings.HasPrefix(scope, "repository:") {
				name := strings.Split(strings.TrimPrefix(scope, "repository:"), ":")[0]
				access = append(access, map[string]any{"type": "repository", "name": name, "actions": actions})
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": jwtWith(access)})
		case "/v2/":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test-registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func robot() *agentprotocol.RegistryAuth {
	return &agentprotocol.RegistryAuth{Username: "robot", Password: "pw"}
}

func TestCheckRegistryNoAuthRegistryAcceptsADummyCredential(t *testing.T) {
	host, _ := registry(t) // plain registry: /v2/ answers 200 to anyone
	res := CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true, Auth: robot(), WantPush: true})
	require.Equal(t, agentprotocol.RegistryCheckResult{Push: true, Pull: true}, res)

	res = CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true})
	require.Equal(t, agentprotocol.RegistryCheckResult{Pull: true}, res, "anonymous: reachable, pull only, no error")

	res = CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true, WantPush: true})
	require.Contains(t, res.Error, "no push credential")
}

func TestCheckRegistryBasicAuthRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == "robot" && p == "pw" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Www-Authenticate", `Basic realm="Registry Realm"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	ok := CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true, Auth: robot(), WantPush: true})
	require.Empty(t, ok.Error)
	require.True(t, ok.Push)
	require.True(t, ok.Pull)

	bad := CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true, WantPush: true,
		Auth: &agentprotocol.RegistryAuth{Username: "robot", Password: "wrong"}})
	require.Contains(t, bad.Error, "rejected the credential")
	require.False(t, bad.Pull)
	require.False(t, bad.Push)

	anon := CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true})
	require.Contains(t, anon.Error, "requires authentication")
}

func TestCheckRegistryBearerFlowVerifiesPushScope(t *testing.T) {
	host := bearerRegistry(t, []string{"pull", "push"})
	res := CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true, Auth: robot(), WantPush: true, Repository: host + "/acme/web"})
	require.Equal(t, agentprotocol.RegistryCheckResult{Push: true, Pull: true}, res)

	ro := bearerRegistry(t, []string{"pull"})
	res = CheckRegistry(context.Background(), nil, RegistryCheck{Host: ro, Insecure: true, Auth: robot(), WantPush: true, Repository: ro + "/acme/web"})
	require.True(t, res.Pull)
	require.False(t, res.Push)
	require.Contains(t, res.Error, "may not push to acme/web")

	// The same read-only grant is fine for a pull-only check.
	res = CheckRegistry(context.Background(), nil, RegistryCheck{Host: ro, Insecure: true, Auth: robot(), Repository: ro + "/acme/web"})
	require.Empty(t, res.Error)
	require.True(t, res.Pull)

	res = CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true, WantPush: true,
		Auth: &agentprotocol.RegistryAuth{Username: "robot", Password: "nope"}})
	require.Contains(t, res.Error, "token service rejected the credentials")
}

func TestCheckRegistryOpaqueTokenAssumesPushAndUnreachableRegistryFails(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "djE6b3BhcXVl"}) // not a JWT, like GHCR's
			return
		}
		if r.Header.Get("Authorization") != "Bearer djE6b3BhcXVl" {
			w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="ghcr.io"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	res := CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true, Auth: robot(), WantPush: true, Repository: host + "/acme/web"})
	require.Empty(t, res.Error)
	require.True(t, res.Push, "an opaque token cannot be inspected; the push itself will tell")

	srv.Close()
	res = CheckRegistry(context.Background(), nil, RegistryCheck{Host: host, Insecure: true, Auth: robot()})
	require.Contains(t, res.Error, "cannot reach")

	notRegistry := httptest.NewServer(http.NotFoundHandler())
	defer notRegistry.Close()
	res = CheckRegistry(context.Background(), nil, RegistryCheck{Host: strings.TrimPrefix(notRegistry.URL, "http://"), Insecure: true})
	require.Contains(t, res.Error, "does not look like an OCI registry")
}

func TestCheckRegistryRefusesAPlainHTTPTokenRealmOverHTTPS(t *testing.T) {
	var tokenHits int
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tokenHits++ }))
	defer token.Close()
	reg := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Www-Authenticate", `Bearer realm="`+token.URL+`/token",service="r"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer reg.Close()
	res := CheckRegistry(context.Background(), reg.Client(), RegistryCheck{Host: strings.TrimPrefix(reg.URL, "https://"), Auth: robot()})
	require.Contains(t, res.Error, "plain http token realm")
	require.Zero(t, tokenHits, "the credential never reaches the plain http token service")
}

func TestCheckRegistryRejectsAMalformedRepository(t *testing.T) {
	for _, repo := range []string{"acme/web:pull,push repository:other/x", "ACME/web", "acme//web", "../web", "acme/web\n"} {
		res := CheckRegistry(context.Background(), nil, RegistryCheck{Host: "ghcr.io", Repository: repo})
		require.Contains(t, res.Error, "invalid repository name", repo)
	}
}

func TestParseBearerChallengeAndEndpoints(t *testing.T) {
	p, ok := parseBearerChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:a/b:pull"`)
	require.True(t, ok)
	require.Equal(t, "https://auth.docker.io/token", p["realm"])
	require.Equal(t, "registry.docker.io", p["service"])
	_, ok = parseBearerChallenge(`Basic realm="x"`)
	require.False(t, ok)

	api, key := registryEndpoint("docker.io")
	require.Equal(t, "registry-1.docker.io", api)
	require.Equal(t, "https://index.docker.io/v1/", key)
	api, key = registryEndpoint("ghcr.io")
	require.Equal(t, "ghcr.io", api)
	require.Equal(t, "ghcr.io", key)
	require.Equal(t, "acme/web", repoPath("ghcr.io", "ghcr.io/acme/web"))
	require.Equal(t, "acme/web", repoPath("ghcr.io", "acme/web"))
}
