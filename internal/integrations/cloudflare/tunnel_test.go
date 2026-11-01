package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	tunAccount = "0123456789abcdef0123456789abcdef"
	tunID      = "5a1b2c3d-0000-4000-8000-123456789abc"
	tunSecret  = "eyJhIjoidGVzdCIsInQiOiJ0dW4iLCJzIjoic2VjcmV0In0="
)

// fakeTunnelAPI is the part of the Cloudflare API tunnels use.
type fakeTunnelAPI struct {
	mu      sync.Mutex
	calls   []string
	tunnels map[string]Tunnel
	configs map[string]json.RawMessage
	conns   map[string][]TunnelClient
	deny    bool // answer 403 to everything
}

func newFakeTunnelAPI() *fakeTunnelAPI {
	return &fakeTunnelAPI{tunnels: map[string]Tunnel{}, configs: map[string]json.RawMessage{}, conns: map[string][]TunnelClient{}}
}

func writeEnv(w http.ResponseWriter, status int, result any, errs ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	env := map[string]any{"success": len(errs) == 0, "errors": []map[string]any{}, "messages": []any{}, "result": result}
	for _, e := range errs {
		env["errors"] = append(env["errors"].([]map[string]any), map[string]any{"code": 10000, "message": e})
	}
	_ = json.NewEncoder(w).Encode(env)
}

func (f *fakeTunnelAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer test-token" {
		writeEnv(w, http.StatusUnauthorized, nil, "Invalid API Token")
		return
	}
	if f.deny {
		writeEnv(w, http.StatusForbidden, nil, "Authentication error")
		return
	}
	prefix := "/accounts/" + tunAccount + "/cfd_tunnel"
	rest, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		writeEnv(w, http.StatusNotFound, nil, "no route")
		return
	}
	body, _ := io.ReadAll(r.Body)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	switch {
	case rest == "" && r.Method == http.MethodPost:
		var in struct {
			Name      string `json:"name"`
			ConfigSrc string `json:"config_src"`
		}
		_ = json.Unmarshal(body, &in)
		if in.ConfigSrc != "cloudflare" {
			writeEnv(w, http.StatusBadRequest, nil, "config_src must be cloudflare")
			return
		}
		t := Tunnel{ID: tunID, Name: in.Name, Status: "inactive", ConfigSrc: in.ConfigSrc}
		f.tunnels[t.ID] = t
		writeEnv(w, http.StatusOK, t)
	case len(parts) == 1 && r.Method == http.MethodGet:
		t, ok := f.tunnels[parts[0]]
		if !ok {
			writeEnv(w, http.StatusNotFound, nil, "Tunnel not found")
			return
		}
		writeEnv(w, http.StatusOK, t)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		t, ok := f.tunnels[parts[0]]
		if !ok {
			writeEnv(w, http.StatusNotFound, nil, "Tunnel not found")
			return
		}
		if len(f.conns[parts[0]]) > 0 {
			writeEnv(w, http.StatusBadRequest, nil, "Cannot delete tunnel with active connections")
			return
		}
		delete(f.tunnels, parts[0])
		writeEnv(w, http.StatusOK, t)
	case len(parts) == 2 && parts[1] == "configurations" && r.Method == http.MethodPut:
		if _, ok := f.tunnels[parts[0]]; !ok {
			writeEnv(w, http.StatusNotFound, nil, "Tunnel not found")
			return
		}
		f.configs[parts[0]] = body
		writeEnv(w, http.StatusOK, map[string]any{"tunnel_id": parts[0], "version": 1})
	case len(parts) == 2 && parts[1] == "token" && r.Method == http.MethodGet:
		if _, ok := f.tunnels[parts[0]]; !ok {
			writeEnv(w, http.StatusNotFound, nil, "Tunnel not found")
			return
		}
		writeEnv(w, http.StatusOK, tunSecret)
	case len(parts) == 2 && parts[1] == "connections" && r.Method == http.MethodGet:
		writeEnv(w, http.StatusOK, f.conns[parts[0]])
	case len(parts) == 2 && parts[1] == "connections" && r.Method == http.MethodDelete:
		if _, ok := f.tunnels[parts[0]]; !ok {
			writeEnv(w, http.StatusNotFound, nil, "Tunnel not found")
			return
		}
		delete(f.conns, parts[0])
		writeEnv(w, http.StatusOK, nil)
	default:
		writeEnv(w, http.StatusNotFound, nil, "no route")
	}
}

func tunnelClient(t *testing.T, f *fakeTunnelAPI) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, Token: "test-token", HTTPClient: srv.Client()})
}

func TestTunnelLifecycleAgainstFakeAPI(t *testing.T) {
	f := newFakeTunnelAPI()
	c := tunnelClient(t, f)
	ctx := context.Background()

	// Create: a remotely-managed tunnel.
	tun, err := c.CreateTunnel(ctx, tunAccount, "nebulactrl-edge-1")
	require.NoError(t, err)
	require.Equal(t, tunID, tun.ID)
	require.Equal(t, "nebulactrl-edge-1", f.tunnels[tunID].Name)
	require.Equal(t, "5a1b2c3d-0000-4000-8000-123456789abc.cfargotunnel.com", TunnelCNAME(tun.ID))

	// Config: every hostname to the proxy with the Host header, then the 404 catch-all.
	rules := IngressForHosts([]string{"b.example.com", "a.example.com", "B.example.com", "*.wild.example.com"}, "http://nebulactrl-traefik:80")
	require.NoError(t, c.PutTunnelConfig(ctx, tunAccount, tunID, rules))
	var sent struct {
		Config struct {
			Ingress []map[string]any `json:"ingress"`
		} `json:"config"`
	}
	require.NoError(t, json.Unmarshal(f.configs[tunID], &sent))
	require.Equal(t, []map[string]any{
		{"hostname": "*.wild.example.com", "service": "http://nebulactrl-traefik:80"},
		{"hostname": "a.example.com", "service": "http://nebulactrl-traefik:80", "originRequest": map[string]any{"httpHostHeader": "a.example.com"}},
		{"hostname": "b.example.com", "service": "http://nebulactrl-traefik:80", "originRequest": map[string]any{"httpHostHeader": "b.example.com"}},
		{"service": "http_status:404"},
	}, sent.Config.Ingress)

	// Token.
	tok, err := c.TunnelToken(ctx, tunAccount, tunID)
	require.NoError(t, err)
	require.Equal(t, tunSecret, tok)

	// Get and connections.
	got, err := c.GetTunnel(ctx, tunAccount, tunID)
	require.NoError(t, err)
	require.Equal(t, tunID, got.ID)
	cons, err := c.TunnelConnections(ctx, tunAccount, tunID)
	require.NoError(t, err)
	require.Empty(t, cons)

	// Delete cleans lingering connections first, otherwise Cloudflare refuses.
	f.conns[tunID] = []TunnelClient{{ID: "c1", Conns: []TunnelConnection{{ID: "x", ColoName: "AMS"}}}}
	f.calls = nil
	require.NoError(t, c.DeleteTunnel(ctx, tunAccount, tunID))
	require.Equal(t, []string{
		"DELETE /accounts/" + tunAccount + "/cfd_tunnel/" + tunID + "/connections",
		"DELETE /accounts/" + tunAccount + "/cfd_tunnel/" + tunID,
	}, f.calls)
	require.Empty(t, f.tunnels)

	// Deleting a tunnel that is already gone succeeds.
	require.NoError(t, c.DeleteTunnel(ctx, tunAccount, tunID))
	_, err = c.GetTunnel(ctx, tunAccount, tunID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestIngressForHosts(t *testing.T) {
	rules := IngressForHosts(nil, "http://nebulactrl-traefik:80")
	require.Equal(t, []IngressRule{{Service: "http_status:404"}}, rules, "no domains: only the catch-all")

	a := IngressForHosts([]string{"a.example.com", "b.example.com"}, "http://u:80")
	b := IngressForHosts([]string{"b.example.com", "a.example.com", "a.example.com"}, "http://u:80")
	require.Equal(t, a, b, "order and duplicates do not matter")
	require.Equal(t, IngressHash(a), IngressHash(b))
	require.NotEqual(t, IngressHash(a), IngressHash(IngressForHosts([]string{"a.example.com"}, "http://u:80")))
	require.Empty(t, a[len(a)-1].Hostname)
}

func TestTunnelErrors(t *testing.T) {
	f := newFakeTunnelAPI()
	c := tunnelClient(t, f)
	ctx := context.Background()

	// Invalid identifiers never reach the network.
	_, err := c.CreateTunnel(ctx, "", "x")
	require.ErrorIs(t, err, ErrNoAccount)
	_, err = c.CreateTunnel(ctx, "../accounts", "x")
	require.ErrorIs(t, err, ErrNoAccount)
	_, err = c.GetTunnel(ctx, tunAccount, "../x")
	require.Error(t, err)
	require.Empty(t, f.calls)

	// A config without the catch-all is refused locally.
	require.Error(t, c.PutTunnelConfig(ctx, tunAccount, tunID, []IngressRule{{Hostname: "a.example.com", Service: "http://u:80"}}))
	require.Error(t, c.PutTunnelConfig(ctx, tunAccount, tunID, nil))
	require.Empty(t, f.calls)

	// A token without the Tunnel permission is an auth error, and the token is not echoed.
	f.deny = true
	_, err = c.CreateTunnel(ctx, tunAccount, "x")
	require.ErrorIs(t, err, ErrAuth)
	require.NotContains(t, err.Error(), "test-token")

	bad := New(Config{BaseURL: strings.TrimSuffix(httptest.NewServer(f).URL, "/"), Token: "wrong"})
	_, err = bad.TunnelToken(context.Background(), tunAccount, tunID)
	require.ErrorIs(t, err, ErrAuth)
}
