package tailscale_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
)

const hsDoc = "{\n\t// owned by NebulaCtrl\n\t\"tagOwners\": {\"tag:nebula-mesh\": []},\n\t\"acls\": []\n}\n"

// Database mode: nothing is stored until the first PUT (Headscale then answers
// `acl policy not found`, which is not an outage), then a PUT is read back as
// written, comments included.
func TestHeadscaleGetAndSetPolicyInDatabaseMode(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	c := hsClient(h)

	_, err := c.GetPolicy(bg)
	require.ErrorIs(t, err, tailscale.ErrPolicyNotFound)
	_, stored := h.Policy()
	require.False(t, stored)

	set, err := c.SetPolicy(bg, hsDoc)
	require.NoError(t, err)
	require.Equal(t, hsDoc, set.Raw)
	require.NotEmpty(t, set.Version, "updatedAt is the version")

	got, err := c.GetPolicy(bg)
	require.NoError(t, err)
	require.Equal(t, hsDoc, got.Raw, "the document is returned verbatim, comments included")
	require.NotEmpty(t, got.Version)

	next := "{\"acls\": [{\"action\": \"accept\", \"src\": [\"100.64.0.1\"], \"dst\": [\"100.64.0.2:5432\"]}]}"
	_, err = c.SetPolicy(bg, next)
	require.NoError(t, err)
	got, err = c.GetPolicy(bg)
	require.NoError(t, err)
	require.Equal(t, next, got.Raw)
	require.Equal(t, []string{hsDoc, next}, h.PolicyHistory())
}

// An invalid document is refused by the server and never stored; the failure is
// not mistaken for the file mode.
func TestHeadscaleRefusesAnInvalidPolicyWithoutStoringIt(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	c := hsClient(h)
	_, err := c.SetPolicy(bg, hsDoc)
	require.NoError(t, err)

	require.Error(t, c.CheckPolicy(bg, "not json"))
	_, err = c.SetPolicy(bg, "{\"acls\": 3}")
	require.Error(t, err)
	require.NotErrorIs(t, err, tailscale.ErrPolicyFileMode)
	require.Equal(t, []string{hsDoc}, h.PolicyHistory())
	require.Equal(t, 1, h.Count(http.MethodPost, "/api/v1/policy/check"), "only CheckPolicy called the check endpoint")
}

// Servers that lack POST /policy/check are treated as "cannot check": the PUT
// validates anyway. Other failures are real errors.
func TestHeadscaleCheckPolicyToleratesAServerWithoutTheEndpoint(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	c := hsClient(h)
	for _, st := range []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented} {
		h.FailNext(http.MethodPost, "/api/v1/policy/check", st)
		require.NoError(t, c.CheckPolicy(bg, hsDoc), "status %d", st)
	}
	h.FailNext(http.MethodPost, "/api/v1/policy/check", http.StatusInternalServerError)
	require.Error(t, c.CheckPolicy(bg, hsDoc))
	require.NoError(t, c.CheckPolicy(bg, hsDoc))
}

// policy.mode: file. The PUT is refused (the mesh falls back to advisory),
// nothing is stored, and the file's policy can still be read for the drift check.
func TestHeadscaleFileModeRefusesWritesAndStillReads(t *testing.T) {
	h := meshtest.NewHeadscale(t, meshtest.WithPolicyMode(meshtest.PolicyModeFile))
	h.SetFilePolicy(hsDoc)
	c := hsClient(h)

	_, err := c.SetPolicy(bg, hsDoc)
	require.ErrorIs(t, err, tailscale.ErrPolicyFileMode)
	require.Empty(t, h.PolicyHistory())

	got, err := c.GetPolicy(bg)
	require.NoError(t, err)
	require.Equal(t, hsDoc, got.Raw)
}

func TestHeadscalePolicyCallsNeedTheAPIKey(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	bad := tailscale.NewHeadscale(tailscale.HeadscaleConfig{LoginServer: h.URL, APIKey: "wrong", HTTPClient: h.Client()})
	_, err := bad.GetPolicy(bg)
	require.ErrorIs(t, err, tailscale.ErrAuth)
	_, err = bad.SetPolicy(bg, hsDoc)
	require.ErrorIs(t, err, tailscale.ErrAuth)
	require.NotErrorIs(t, err, tailscale.ErrPolicyFileMode)
	require.Empty(t, h.PolicyHistory())
}

// The PolicyAPI of a workspace's Headscale integration: the same calls through
// the credential kind check. A pre-auth key has no API access.
func TestPolicyAPIOfAHeadscaleIntegration(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	e := newPolicyEnv(t, "", h.Client())

	_, err := e.svc.PolicyAPI(bg, e.ws)
	require.ErrorIs(t, err, tailscale.ErrNotConfigured)

	e.putHeadscale(e.admin, h, tailscale.HeadscaleAPIKey)
	api, err := e.svc.PolicyAPI(bg, e.ws)
	require.NoError(t, err)
	require.Equal(t, "headscale", api.Provider())

	_, err = api.Get(bg)
	require.ErrorIs(t, err, tailscale.ErrPolicyNotFound)
	require.NoError(t, api.Check(bg, []byte(hsDoc)))
	set, err := api.Set(bg, []byte(hsDoc), "ignored: Headscale has no ETag")
	require.NoError(t, err)
	require.Equal(t, hsDoc, set.Raw)
	got, err := api.Get(bg)
	require.NoError(t, err)
	require.Equal(t, hsDoc, got.Raw)
	require.Equal(t, []string{hsDoc}, h.PolicyHistory())
}

func TestPolicyAPIOfAHeadscaleInFileModeIsAdvisory(t *testing.T) {
	h := meshtest.NewHeadscale(t, meshtest.WithPolicyMode(meshtest.PolicyModeFile))
	e := newPolicyEnv(t, "", h.Client())
	e.putHeadscale(e.admin, h, tailscale.HeadscaleAPIKey)
	api, err := e.svc.PolicyAPI(bg, e.ws)
	require.NoError(t, err)
	_, err = api.Set(bg, []byte(hsDoc), "")
	require.ErrorIs(t, err, tailscale.ErrPolicyFileMode)
	require.Empty(t, h.PolicyHistory())
}

func TestPolicyAPIRefusesAPreAuthCredential(t *testing.T) {
	h := meshtest.NewHeadscale(t)
	e := newPolicyEnv(t, "", h.Client())
	e.putHeadscale(e.admin, h, tailscale.HeadscalePreauth)
	_, err := e.svc.PolicyAPI(bg, e.ws)
	require.ErrorIs(t, err, tailscale.ErrPolicyUnsupported)
	require.Empty(t, h.Calls(), "a pre-auth key never reaches the policy API")
}
