package tailscale

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const testSecret = "tskey-client-SUPERSECRET-value"

// fakeTailscale is an httptest stand-in for api.tailscale.com.
type fakeTailscale struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	tokenReqs int
	requests  []recorded
	// behaviour
	keyStatus   int // non-zero forces a failure status on key creation
	keyExpiry   func(secs int) int
	deviceCode  int
	settingsErr int
	logs        []string
}

type recorded struct {
	Method, Path, Query, Auth string
	Body                      string
}

func newFake(t *testing.T) *fakeTailscale {
	f := &fakeTailscale{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTailscale) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, r.Method+" "+r.URL.String()+" "+string(body))
	if r.URL.Path == "/api/v2/oauth/token" {
		f.tokenReqs++
		form, _ := url.ParseQuery(string(body))
		if form.Get("grant_type") != "client_credentials" || form.Get("client_id") != "client-1" || form.Get("client_secret") != testSecret {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-abc","token_type":"Bearer","expires_in":3600}`))
		return
	}
	f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(body)})
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/devices"):
		_, _ = w.Write([]byte(`{"devices":[
			{"id":"123","nodeId":"nNODE1CNTRL","name":"web.tail1.ts.net.","hostname":"web","addresses":["100.64.0.1","fd7a:115c:a1e0::1"],"tags":["tag:nebula-svc"],"os":"linux","connectedToControl":true,"lastSeen":"2026-10-20T10:00:00Z"},
			{"id":"456","name":"laptop.tail1.ts.net","hostname":"laptop","addresses":["100.64.0.2"],"os":"macOS","connectedToControl":false}]}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/keys"):
		if f.keyStatus != 0 {
			w.WriteHeader(f.keyStatus)
			_, _ = w.Write([]byte(`{"message":"requested tags [tag:nope] are invalid or not permitted"}`))
			return
		}
		var req createKeyRequest
		_ = json.Unmarshal(body, &req)
		if f.keyExpiry != nil {
			if code := f.keyExpiry(req.ExpirySeconds); code != 0 {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"message":"expirySeconds is below the minimum expiry"}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"id":"kKEY1CNTRL","key":"tskey-auth-FRESH-1234","created":"2026-10-26T10:00:00Z","expires":"2026-10-26T11:00:00Z"}`))
	case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/keys/"):
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/device/"):
		code := f.deviceCode
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		if code >= 400 {
			_, _ = w.Write([]byte(`{"message":"device not found"}`))
		}
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/settings"):
		if f.settingsErr != 0 {
			w.WriteHeader(f.settingsErr)
			_, _ = w.Write([]byte(`{"message":"missing scope"}`))
			return
		}
		_, _ = w.Write([]byte(`{"httpsEnabled":true,"devicesApprovalOn":false}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeTailscale) client(secret string) *Client {
	return New(Config{BaseURL: f.srv.URL, ClientID: "client-1", ClientSecret: secret, HTTPClient: f.srv.Client()})
}

func (f *fakeTailscale) reqs() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

func TestTokenExchangeIsCachedAndSendsBearer(t *testing.T) {
	f := newFake(t)
	c := f.client(testSecret)
	for i := 0; i < 3; i++ {
		_, err := c.ListDevices(context.Background())
		require.NoError(t, err)
	}
	require.Equal(t, 1, f.tokenReqs, "the access token is reused until shortly before it expires")
	// A second client for the same credential shares the cached token.
	_, err := f.client(testSecret).ListDevices(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, f.tokenReqs)
	for _, r := range f.reqs() {
		require.Equal(t, "Bearer tok-abc", r.Auth)
	}
}

func TestListDevicesMapsFields(t *testing.T) {
	f := newFake(t)
	devs, err := f.client(testSecret).ListDevices(context.Background())
	require.NoError(t, err)
	require.Len(t, devs, 2)
	require.Equal(t, "nNODE1CNTRL", devs[0].ID, "the stable node id is preferred")
	require.Equal(t, "web.tail1.ts.net", devs[0].Name, "trailing dot trimmed")
	require.Equal(t, []string{"100.64.0.1", "fd7a:115c:a1e0::1"}, devs[0].Addresses)
	require.True(t, devs[0].Online)
	require.Equal(t, []string{"tag:nebula-svc"}, devs[0].Tags)
	require.NotNil(t, devs[0].LastSeen)
	require.Equal(t, "456", devs[1].ID)
	require.False(t, devs[1].Online)
	require.NotNil(t, devs[1].Tags, "tags are an empty list, never null")
	got := f.reqs()[0]
	require.Equal(t, "/api/v2/tailnet/-/devices", got.Path)
	require.Contains(t, got.Query, "fields=all")
}

func TestMintAuthKeyRequestShape(t *testing.T) {
	f := newFake(t)
	key, err := f.client(testSecret).MintAuthKey(context.Background(), []string{"tag:nebula-svc", "tag:edge"}, 3600, "nebulactrl server "+strings.Repeat("x", 80))
	require.NoError(t, err)
	require.Equal(t, "tskey-auth-FRESH-1234", key.Key)
	require.Equal(t, "kKEY1CNTRL", key.ID)

	r := f.reqs()[0]
	require.Equal(t, http.MethodPost, r.Method)
	require.Equal(t, "/api/v2/tailnet/-/keys", r.Path)
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Body), &raw))
	create := raw["capabilities"].(map[string]any)["devices"].(map[string]any)["create"].(map[string]any)
	require.Equal(t, false, create["reusable"], "single use")
	require.Equal(t, false, create["ephemeral"], "persistent node")
	require.Equal(t, true, create["preauthorized"])
	require.Equal(t, []any{"tag:nebula-svc", "tag:edge"}, create["tags"])
	require.EqualValues(t, 3600, raw["expirySeconds"])
	require.LessOrEqual(t, len([]rune(raw["description"].(string))), 50)

	_, err = f.client(testSecret).MintAuthKey(context.Background(), nil, 60, "x")
	require.Error(t, err, "an untagged key is refused client-side")
}

// The Tailscale API refuses a key whose description holds anything but
// letters, digits and spaces (400 "description had invalid characters"):
// a service's "nebulactrl project/env/service" failed every tailnet deploy.
func TestMintAuthKeyDescriptionHasOnlyCharactersTheAPIAccepts(t *testing.T) {
	for in, want := range map[string]string{
		"nebulactrl dashboard/production/dashboard": "nebulactrl dashboard production dashboard",
		"nebulactrl server homelab-1.example.com":   "nebulactrl server homelab 1 example com",
		"  nebulactrl   mesh   (edge)  ":            "nebulactrl mesh edge",
		"nebulactrl connection test":                "nebulactrl connection test",
		"//":                                        "",
	} {
		require.Equal(t, want, keyDescription(in), in)
	}
	long := keyDescription("nebulactrl " + strings.Repeat("ab/", 40))
	require.LessOrEqual(t, len(long), maxKeyDescription)
	require.NotRegexp(t, ` $`, long)

	f := newFake(t)
	_, err := f.client(testSecret).MintAuthKey(context.Background(), []string{"tag:nebula-svc"}, 3600, "nebulactrl dashboard/production/dashboard")
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(f.reqs()[0].Body), &raw))
	require.Equal(t, "nebulactrl dashboard production dashboard", raw["description"])
}

func TestCustomTailnetNameInPath(t *testing.T) {
	f := newFake(t)
	c := New(Config{BaseURL: f.srv.URL, Tailnet: "example.com", ClientID: "client-1", ClientSecret: testSecret, HTTPClient: f.srv.Client()})
	_, err := c.ListDevices(context.Background())
	require.NoError(t, err)
	require.Equal(t, "/api/v2/tailnet/example.com/devices", f.reqs()[0].Path)
}

func TestDeleteDeviceAndKey(t *testing.T) {
	f := newFake(t)
	c := f.client(testSecret)
	require.NoError(t, c.DeleteDevice(context.Background(), "nNODE1CNTRL"))
	require.NoError(t, c.DeleteKey(context.Background(), "kKEY1CNTRL"))
	reqs := f.reqs()
	require.Equal(t, "DELETE /api/v2/device/nNODE1CNTRL", reqs[0].Method+" "+reqs[0].Path)
	require.Equal(t, "DELETE /api/v2/tailnet/-/keys/kKEY1CNTRL", reqs[1].Method+" "+reqs[1].Path)

	f.deviceCode = http.StatusNotFound
	err := c.DeleteDevice(context.Background(), "gone")
	require.ErrorIs(t, err, ErrNotFound)
	require.Error(t, c.DeleteDevice(context.Background(), ""))
}

func TestErrorMappingAndSecretNeverInErrors(t *testing.T) {
	f := newFake(t)

	// Wrong secret: the token exchange is refused.
	_, err := f.client("wrong-secret-xyz").ListDevices(context.Background())
	require.ErrorIs(t, err, ErrAuth)
	require.NotContains(t, err.Error(), "wrong-secret-xyz")
	require.NotContains(t, err.Error(), testSecret)

	// API errors carry the status and Tailscale's message only.
	f.keyStatus = http.StatusForbidden
	_, err = f.client(testSecret).MintAuthKey(context.Background(), []string{"tag:nope"}, 3600, "d")
	require.ErrorIs(t, err, ErrAuth)
	var ae *APIError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	require.Contains(t, ae.Message, "tag:nope")
	require.NotContains(t, err.Error(), testSecret)
	require.NotContains(t, err.Error(), "tok-abc")

	f.keyStatus = http.StatusServiceUnavailable
	_, err = f.client(testSecret).MintAuthKey(context.Background(), []string{"tag:x"}, 3600, "d")
	require.ErrorAs(t, err, &ae)
	require.True(t, ae.Retryable())

	// An unreachable server: the transport reason is kept, the URL (and with it
	// anything in it) is not.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	_, err = New(Config{BaseURL: url, ClientID: "client-1", ClientSecret: testSecret}).ListDevices(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), testSecret)
	require.NotContains(t, err.Error(), url)
}

func TestTestMintsAndRevokesKeyAndReportsChecks(t *testing.T) {
	f := newFake(t)
	res := f.client(testSecret).Test(context.Background(), []string{"tag:nebula-svc"})
	require.True(t, res.OK)
	ids := []string{}
	for _, c := range res.Checks {
		require.True(t, c.OK, c.ID+": "+c.Message)
		ids = append(ids, c.ID)
	}
	require.Equal(t, []string{"credentials", "devices", "auth_keys", "settings"}, ids)
	require.NotNil(t, res.HTTPSEnabled)
	require.True(t, *res.HTTPSEnabled)

	var minted, revoked bool
	for _, r := range f.reqs() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/keys") {
			minted = true
			var req createKeyRequest
			require.NoError(t, json.Unmarshal([]byte(r.Body), &req))
			require.Equal(t, 60, req.ExpirySeconds, "the test key lives 60 seconds")
		}
		if r.Method == http.MethodDelete && strings.HasSuffix(r.Path, "/keys/kKEY1CNTRL") {
			revoked = true
		}
	}
	require.True(t, minted)
	require.True(t, revoked, "the test key is revoked: no residue")
	for _, l := range f.logs {
		require.NotContains(t, l, "tskey-auth-FRESH-1234 ", "the minted key is never sent back anywhere")
	}
}

func TestTestFallsBackToOneDayExpiryAndSkipsOptionalSettings(t *testing.T) {
	f := newFake(t)
	var attempts atomic.Int32
	f.keyExpiry = func(secs int) int {
		attempts.Add(1)
		if secs < 3600 {
			return http.StatusBadRequest
		}
		return 0
	}
	f.settingsErr = http.StatusForbidden
	res := f.client(testSecret).Test(context.Background(), []string{"tag:nebula-svc"})
	require.True(t, res.OK, "%+v", res.Checks)
	require.EqualValues(t, 2, attempts.Load(), "60s refused, 1 day accepted")
	last := res.Checks[len(res.Checks)-1]
	require.Equal(t, "settings", last.ID)
	require.True(t, last.Skipped)
	require.Nil(t, res.HTTPSEnabled)
}

func TestTestFailsOnBadCredentialsAndOnTagNotPermitted(t *testing.T) {
	f := newFake(t)
	res := f.client("wrong").Test(context.Background(), []string{"tag:nebula-svc"})
	require.False(t, res.OK)
	require.Len(t, res.Checks, 1)
	require.Equal(t, "credentials", res.Checks[0].ID)
	require.NotContains(t, res.Checks[0].Message, "wrong")

	f.keyStatus = http.StatusForbidden
	res = f.client(testSecret).Test(context.Background(), []string{"tag:nope"})
	require.False(t, res.OK)
	var failed []string
	for _, c := range res.Checks {
		if !c.OK {
			failed = append(failed, c.ID)
		}
	}
	require.Equal(t, []string{"auth_keys"}, failed)
}
