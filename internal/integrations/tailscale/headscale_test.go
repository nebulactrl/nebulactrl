package tailscale

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

const hsKey = "hs-apikey-1234567"

// fakeHeadscale is a minimal Headscale REST API (/api/v1).
type fakeHeadscale struct {
	srv *httptest.Server
	mu  sync.Mutex
	// recorded
	calls []string // "METHOD path"
	mints []map[string]any
}

func newFakeHeadscale(t *testing.T) *fakeHeadscale {
	f := &fakeHeadscale{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"status":"pass"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+hsKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return
		}
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
			if r.URL.Query().Get("name") == "ops" {
				_, _ = w.Write([]byte(`{"users":[{"id":"7","name":"ops"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"users":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/preauthkey":
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			f.mints = append(f.mints, m)
			_, _ = w.Write([]byte(`{"preAuthKey":{"id":"3","key":"hskey-MINTED-abcdef","expiration":"2026-10-26T11:00:00Z"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/preauthkey/expire":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			_, _ = w.Write([]byte(`{"nodes":[
				{"id":"12","name":"web-production","givenName":"web-production","ipAddresses":["100.64.0.3","fd7a:115c:a1e0::3"],"online":true,"forcedTags":["tag:nebula-svc"]},
				{"id":"13","name":"laptop","givenName":"laptop","ipAddresses":["100.64.0.4"],"online":false}]}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/node/"):
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHeadscale) client(user string) *HeadscaleClient {
	return NewHeadscale(HeadscaleConfig{LoginServer: f.srv.URL, APIKey: hsKey, User: user, HTTPClient: f.srv.Client()})
}

func (f *fakeHeadscale) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestValidLoginServer(t *testing.T) {
	got, err := ValidLoginServer(" https://hs.example.com/ ")
	require.NoError(t, err)
	require.Equal(t, "https://hs.example.com", got)
	for _, loopback := range []string{"http://localhost:8080/headscale", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		_, err = ValidLoginServer(loopback)
		require.NoError(t, err, loopback)
	}
	for _, bad := range []string{"", "hs.example.com", "ftp://hs.example.com", "http://hs.example.com", "http://10.0.0.5:8080/headscale", "https://user:pw@hs.example.com", "https://hs.example.com?x=1", "https://hs.example.com#f"} {
		_, err := ValidLoginServer(bad)
		require.Error(t, err, bad)
	}
}

func TestMintPreAuthKeyIsSingleUseAndTagged(t *testing.T) {
	f := newFakeHeadscale(t)
	k, err := f.client("ops").MintPreAuthKey(context.Background(), []string{"tag:nebula-svc"}, time.Hour)
	require.NoError(t, err)
	require.Equal(t, "hskey-MINTED-abcdef", k.Key)
	require.Equal(t, "3", k.ID)

	m := f.mints[0]
	require.Equal(t, false, m["reusable"])
	require.Equal(t, false, m["ephemeral"])
	require.Equal(t, []any{"tag:nebula-svc"}, m["aclTags"])
	require.Equal(t, "7", m["user"], "the configured user is resolved to its id")
	exp, err := time.Parse(time.RFC3339, m["expiration"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Hour), exp, time.Minute)

	// Without a user the key is tags-only.
	f.mints = nil
	_, err = f.client("").MintPreAuthKey(context.Background(), []string{"tag:nebula-svc"}, time.Hour)
	require.NoError(t, err)
	_, hasUser := f.mints[0]["user"]
	require.False(t, hasUser)

	_, err = f.client("nobody").MintPreAuthKey(context.Background(), []string{"tag:nebula-svc"}, time.Hour)
	require.ErrorContains(t, err, `"nobody" does not exist`)

	require.NoError(t, f.client("").ExpirePreAuthKey(context.Background(), "3"))
	require.NoError(t, f.client("").ExpirePreAuthKey(context.Background(), ""))
}

func TestHeadscaleRejectedKeyIsErrAuth(t *testing.T) {
	f := newFakeHeadscale(t)
	c := NewHeadscale(HeadscaleConfig{LoginServer: f.srv.URL, APIKey: "wrong", HTTPClient: f.srv.Client()})
	_, err := c.Nodes(context.Background())
	require.ErrorIs(t, err, ErrAuth)
	require.NotContains(t, err.Error(), "wrong")
}

func TestNodesAndFindNode(t *testing.T) {
	f := newFakeHeadscale(t)
	nodes, err := f.client("").Nodes(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	require.Equal(t, "12", nodes[0].ID)
	require.Equal(t, []string{"tag:nebula-svc"}, nodes[0].Tags)
	require.True(t, nodes[0].Online)
	require.NotNil(t, nodes[1].Tags, "tags are an empty list, never null")

	n, ok := FindNode(nodes, []string{"100.64.0.3"}, "")
	require.True(t, ok)
	require.Equal(t, "12", n.ID, "found by tailnet address (Headscale ids differ from Tailscale's stable ids)")
	n, ok = FindNode(nodes, []string{"100.99.9.9"}, "laptop")
	require.True(t, ok)
	require.Equal(t, "13", n.ID, "else by host name")
	_, ok = FindNode(nodes, []string{"100.99.9.9"}, "ghost")
	require.False(t, ok)
	_, ok = FindNode(nodes, nil, "")
	require.False(t, ok)

	require.NoError(t, f.client("").DeleteNode(context.Background(), "12"))
	require.Contains(t, f.callLog(), "DELETE /api/v1/node/12")
	require.Error(t, f.client("").DeleteNode(context.Background(), "12; drop"), "ids are numeric")
}

func TestHeadscaleConnectionTest(t *testing.T) {
	f := newFakeHeadscale(t)
	res := f.client("ops").Test(context.Background(), []string{"tag:nebula-svc"})
	require.True(t, res.OK, "%+v", res.Checks)
	var ids []string
	for _, c := range res.Checks {
		ids = append(ids, c.ID)
	}
	require.Equal(t, []string{"credentials", "user", "auth_keys", "https"}, ids)
	require.Contains(t, f.callLog(), "POST /api/v1/preauthkey/expire", "the test key is expired again")

	res = f.client("nobody").Test(context.Background(), []string{"tag:nebula-svc"})
	require.False(t, res.OK)

	bad := NewHeadscale(HeadscaleConfig{LoginServer: f.srv.URL, APIKey: "nope", HTTPClient: f.srv.Client()})
	res = bad.Test(context.Background(), []string{"tag:nebula-svc"})
	require.False(t, res.OK)
	require.Len(t, res.Checks, 1)

	require.NoError(t, HealthCheck(context.Background(), f.srv.Client(), f.srv.URL))
}

func TestHeadscaleIntegrationLifecycleAndNodeOperations(t *testing.T) {
	e := newEnv(t)
	hs := newFakeHeadscale(t)
	ctx := context.Background()

	// Validation.
	_, err := e.svc.Put(e.admin, PutInput{Provider: ProviderHeadscale, ClientSecret: hsKey})
	require.Equal(t, "invalid_login_server", apiCode(t, err))
	_, err = e.svc.Put(e.admin, PutInput{Provider: ProviderHeadscale, LoginServer: hs.srv.URL})
	require.Equal(t, "invalid_client_secret", apiCode(t, err))
	_, err = e.svc.Put(e.admin, PutInput{Provider: ProviderHeadscale, LoginServer: hs.srv.URL, ClientSecret: hsKey, HeadscaleCredential: "password"})
	require.Equal(t, "invalid_credential", apiCode(t, err))
	_, err = e.svc.Put(e.admin, PutInput{Provider: "wireguard", ClientSecret: hsKey})
	require.Equal(t, "invalid_provider", apiCode(t, err))

	v, err := e.svc.Put(e.admin, PutInput{Provider: ProviderHeadscale, LoginServer: hs.srv.URL + "/", HeadscaleUser: "ops", ClientSecret: hsKey})
	require.NoError(t, err)
	require.Equal(t, ProviderHeadscale, v.Provider)
	require.Equal(t, hs.srv.URL, v.LoginServer)
	require.Equal(t, HeadscaleAPIKey, v.HeadscaleCredential)
	require.True(t, v.SecretSet)
	raw, _ := json.Marshal(v)
	require.NotContains(t, string(raw), hsKey)

	st, err := e.svc.Settings(ctx, e.ws)
	require.NoError(t, err)
	require.Equal(t, "headscale", st.Provider)
	require.Equal(t, hs.srv.URL, st.LoginServer)

	// The connection test goes through the Headscale API.
	out, err := e.svc.Test(e.admin)
	require.NoError(t, err)
	require.True(t, out.OK, "%+v", out.Checks)

	// Keys are single-use pre-auth keys carrying the requested tags.
	key, err := e.svc.MintNodeKey(ctx, e.ws, MintRequest{Tags: []string{"tag:nebula-svc"}, Expiry: time.Hour, Description: "d"})
	require.NoError(t, err)
	require.Equal(t, NodeKey{ID: "3", Key: "hskey-MINTED-abcdef"}, key)
	require.NoError(t, e.svc.RevokeNodeKey(ctx, e.ws, key.ID))
	_, err = e.svc.MintNodeKey(ctx, e.ws, MintRequest{})
	require.Error(t, err, "an untagged key is refused")

	// A node is deleted by address, not by the Tailscale id.
	require.NoError(t, e.svc.DeleteNode(ctx, e.ws, NodeRef{DeviceID: "nSTABLEID", IPs: []string{"100.64.0.3"}, Hostname: "web-production"}))
	require.Contains(t, hs.callLog(), "DELETE /api/v1/node/12")
	n := len(hs.callLog())
	require.NoError(t, e.svc.DeleteNode(ctx, e.ws, NodeRef{IPs: []string{"100.99.9.9"}}), "a node that is gone is done")
	require.Equal(t, n+1, len(hs.callLog()), "only the node listing")

	// The device picker lists Headscale nodes.
	devs, err := e.svc.Devices(e.admin)
	require.NoError(t, err)
	require.Len(t, devs, 2)
	require.Equal(t, "12", devs[0].ID)

	// Per-server egress (a Tailscale OAuth feature) is not available.
	_, err = e.svc.MintAuthKey(ctx, e.ws, 3600, "d")
	require.ErrorIs(t, err, ErrWrongProvider)

	// Switching provider needs the new secret.
	_, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1"})
	require.Equal(t, "invalid_client_secret", apiCode(t, err))
}

func TestHeadscalePreauthCredential(t *testing.T) {
	e := newEnv(t)
	hs := newFakeHeadscale(t)
	ctx := context.Background()
	_, err := e.svc.Put(e.admin, PutInput{
		Provider: ProviderHeadscale, LoginServer: hs.srv.URL, HeadscaleCredential: HeadscalePreauth, HeadscaleUser: "ignored", ClientSecret: "reusable-preauth-key-1",
	})
	require.NoError(t, err)
	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.Empty(t, v.HeadscaleUser, "a pre-auth credential has no user")

	key, err := e.svc.MintNodeKey(ctx, e.ws, MintRequest{Tags: []string{"tag:nebula-svc"}})
	require.NoError(t, err)
	require.Equal(t, NodeKey{Key: "reusable-preauth-key-1"}, key, "the stored reusable key; it is never revoked")
	require.NoError(t, e.svc.RevokeNodeKey(ctx, e.ws, "anything"))
	require.NoError(t, e.svc.DeleteNode(ctx, e.ws, NodeRef{IPs: []string{"100.64.0.3"}}), "no API access: nothing to delete with")
	require.Empty(t, hs.callLog())

	out, err := e.svc.Test(e.admin)
	require.NoError(t, err)
	require.True(t, out.OK)
	_, err = e.svc.Devices(e.admin)
	require.Equal(t, "not_applicable", apiCode(t, err))
}

func TestDeleteIntegrationRefusedWhileServicesAreOnTheTailnet(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: testSecret})
	require.NoError(t, err)
	prj, err := e.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: e.ws, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := e.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	_, err = e.pool.Exec(context.Background(),
		`INSERT INTO services (environment_id, name, slug, kind, source_type, config) VALUES ($1, 'web', 'web', 'app', 'image', '{"image":"x","tailnet":{"enabled":true,"web":true}}')`, env.ID)
	require.NoError(t, err)

	require.Equal(t, "integration_in_use", apiCode(t, e.svc.Delete(e.admin)))
	_, err = e.pool.Exec(context.Background(), `UPDATE services SET config = '{"image":"x"}'`)
	require.NoError(t, err)
	require.NoError(t, e.svc.Delete(e.admin))
}
