package tailscale_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
)

const tsDoc = "{\n\t// ours\n\t\"acls\": [],\n}\n"

// The policy comes back as HuJSON (comments kept) with the ETag the next write
// has to present.
func TestTailscaleGetPolicyReturnsHuJSONAndETag(t *testing.T) {
	f := meshtest.NewTailscale(t, meshtest.WithScopes(meshtest.ScopePolicyRead))
	got, err := tsClient(f).GetPolicy(bg)
	require.NoError(t, err)
	doc, etag := f.Policy()
	require.Equal(t, doc, got.Raw)
	require.Contains(t, got.Raw, "// the tailnet's own policy", "comments survive")
	require.NotEmpty(t, etag)
	require.Equal(t, etag, got.Version, "the ETag is the version")

	reqs := f.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, http.MethodGet, reqs[0].Method)
	require.Equal(t, "/api/v2/tailnet/-/acl", reqs[0].Path)
	require.Empty(t, f.PolicyWrites())
}

// A write presents the ETag as If-Match and returns the new one.
func TestTailscaleSetPolicyIsConditionalOnTheETag(t *testing.T) {
	f := meshtest.NewTailscale(t, meshtest.WithScopes(meshtest.ScopePolicy))
	c := tsClient(f)
	cur, err := c.GetPolicy(bg)
	require.NoError(t, err)

	set, err := c.SetPolicy(bg, []byte(tsDoc), cur.Version)
	require.NoError(t, err)
	require.Equal(t, tsDoc, set.Raw)
	require.NotEmpty(t, set.Version)
	require.NotEqual(t, cur.Version, set.Version)

	reqs := f.Requests()
	post := reqs[len(reqs)-1]
	require.Equal(t, http.MethodPost, post.Method)
	require.Equal(t, cur.Version, post.IfMatch, "If-Match carries the ETag that was read")
	doc, etag := f.Policy()
	require.Equal(t, tsDoc, doc)
	require.Equal(t, etag, set.Version)
}

// A human saved the policy in between: the write answers 412, which is reported
// as a conflict (drift), and the human's policy is left alone.
func TestTailscaleETagMismatchIsAConflictNeverAnOverwrite(t *testing.T) {
	f := meshtest.NewTailscale(t, meshtest.WithScopes(meshtest.ScopePolicy))
	c := tsClient(f)
	cur, err := c.GetPolicy(bg)
	require.NoError(t, err)

	const human = "{\n\t// edited in the console\n\t\"acls\": [{\"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"*:22\"]}],\n}\n"
	f.SetPolicyExternally(human)
	_, err = c.SetPolicy(bg, []byte(tsDoc), cur.Version)
	require.ErrorIs(t, err, tailscale.ErrPolicyConflict)
	doc, _ := f.Policy()
	require.Equal(t, human, doc, "the console edit is intact")
	require.Empty(t, f.PolicyWrites(), "nothing was accepted")

	// After a fresh read the write goes through.
	cur, err = c.GetPolicy(bg)
	require.NoError(t, err)
	require.Equal(t, human, cur.Raw)
	_, err = c.SetPolicy(bg, []byte(tsDoc), cur.Version)
	require.NoError(t, err)
	require.Equal(t, []string{tsDoc}, f.PolicyWrites())
}

// Without an ETag the write would be blind: the client refuses and sends nothing.
func TestTailscaleSetPolicyRefusesAnUnconditionalWrite(t *testing.T) {
	f := meshtest.NewTailscale(t, meshtest.WithScopes(meshtest.ScopePolicy))
	before, _ := f.Policy()
	_, err := tsClient(f).SetPolicy(bg, []byte(tsDoc), "")
	require.ErrorIs(t, err, tailscale.ErrPolicyNotConditional)
	require.Empty(t, f.PolicyWrites())
	require.Empty(t, f.Requests(), "no request left the process")
	doc, _ := f.Policy()
	require.Equal(t, before, doc)
}

// A client without the policy scopes gets a 403 that is an ErrAuth, with the
// server's wording; policy_file:read reads and validates but cannot write.
func TestTailscaleMissingPolicyScopeIsReportedClearly(t *testing.T) {
	f := meshtest.NewTailscale(t) // auth_keys and devices:core only
	c := tsClient(f)

	_, err := c.GetPolicy(bg)
	require.ErrorIs(t, err, tailscale.ErrAuth)
	var ae *tailscale.APIError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusForbidden, ae.Status)
	require.Contains(t, ae.Message, "enough permissions")
	require.ErrorIs(t, c.ValidatePolicy(bg, []byte(tsDoc)), tailscale.ErrAuth)
	_, err = c.SetPolicy(bg, []byte(tsDoc), `"etag"`)
	require.ErrorIs(t, err, tailscale.ErrAuth)

	f.GrantScope(meshtest.ScopePolicyRead)
	cur, err := c.GetPolicy(bg)
	require.NoError(t, err)
	require.NoError(t, c.ValidatePolicy(bg, []byte(tsDoc)), "validation needs policy_file:read only")
	_, err = c.SetPolicy(bg, []byte(tsDoc), cur.Version)
	require.ErrorIs(t, err, tailscale.ErrAuth, "reading is not enough to write")
	require.Empty(t, f.PolicyWrites())

	f.GrantScope(meshtest.ScopePolicy)
	_, err = c.SetPolicy(bg, []byte(tsDoc), cur.Version)
	require.NoError(t, err)
}

func TestTailscaleValidatePolicyReportsARejection(t *testing.T) {
	f := meshtest.NewTailscale(t, meshtest.WithScopes(meshtest.ScopePolicyRead))
	c := tsClient(f)
	require.NoError(t, c.ValidatePolicy(bg, []byte(tsDoc)), "comments and trailing commas are fine")
	err := c.ValidatePolicy(bg, []byte("{nope"))
	require.ErrorContains(t, err, "invalid policy")
	require.Empty(t, f.PolicyWrites(), "validation never writes")
	doc, _ := f.Policy()
	require.Contains(t, doc, "the tailnet's own policy")
}

func TestTailscaleBadCredentialsAreAnAuthError(t *testing.T) {
	f := meshtest.NewTailscale(t, meshtest.WithScopes(meshtest.ScopePolicy))
	bad := tailscale.New(tailscale.Config{BaseURL: f.URL, ClientID: meshtest.TailscaleClientID, ClientSecret: "tskey-client-wrong", HTTPClient: f.Client()})
	_, err := bad.GetPolicy(bg)
	require.ErrorIs(t, err, tailscale.ErrAuth)
	require.NotContains(t, err.Error(), "tskey-client-wrong")
}

// A recording server for the wire details a fake does not model: the headers,
// a 409 as a conflict and a 200 that lists failed ACL tests.
func TestTailscalePolicyWireDetails(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	status, body := http.StatusOK, `{}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		st, b := status, body
		mu.Unlock()
		w.Header().Set("ETag", `"new"`)
		w.WriteHeader(st)
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	c := tailscale.New(tailscale.Config{BaseURL: srv.URL, ClientID: "client-1", ClientSecret: "s", HTTPClient: srv.Client()})
	last := func() http.Header { mu.Lock(); defer mu.Unlock(); return seen[len(seen)-1] }

	body = tsDoc
	_, err := c.GetPolicy(bg)
	require.NoError(t, err)
	require.Equal(t, "Bearer tok", last().Get("Authorization"))
	require.Equal(t, "application/hujson", last().Get("Accept"))

	set, err := c.SetPolicy(bg, []byte(tsDoc), `"old"`)
	require.NoError(t, err)
	require.Equal(t, `"new"`, set.Version)
	require.Equal(t, `"old"`, last().Get("If-Match"))
	require.True(t, strings.HasPrefix(last().Get("Content-Type"), "application/hujson"))

	mu.Lock()
	status, body = http.StatusConflict, `{"message":"conflict"}`
	mu.Unlock()
	_, err = c.SetPolicy(bg, []byte(tsDoc), `"old"`)
	require.ErrorIs(t, err, tailscale.ErrPolicyConflict)

	mu.Lock()
	status, body = http.StatusOK, `{"message":"test failed","data":[{"user":"a","errors":["x"]}]}`
	mu.Unlock()
	require.ErrorContains(t, c.ValidatePolicy(bg, []byte(tsDoc)), "rejected the policy")
}
