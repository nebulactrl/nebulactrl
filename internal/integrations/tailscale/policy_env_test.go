package tailscale_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/meshtest"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

var bg = context.Background()

// policyEnv is a workspace with the integration Service, wired to the meshtest
// fakes (which stand in for Headscale and Tailscale).
type policyEnv struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *db.Queries
	box   *secretbox.Box
	svc   *tailscale.Service
	ws    uuid.UUID
	admin context.Context
}

// newPolicyEnv builds the service; hc is the HTTP client used for the fakes and
// baseURL the Tailscale API base ("" when only Headscale is used).
func newPolicyEnv(t *testing.T, baseURL string, hc *http.Client) *policyEnv {
	t.Helper()
	pool := testutil.NewPool(t)
	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	e := &policyEnv{t: t, pool: pool, q: db.New(pool), box: box}
	e.svc = tailscale.NewService(pool, box, nil, nil)
	e.svc.SetBaseURL(baseURL, hc)
	e.ws, e.admin = e.workspace("a@example.com")
	return e
}

// workspace creates a team with an admin and returns its admin context.
func (e *policyEnv) workspace(email string) (uuid.UUID, context.Context) {
	e.t.Helper()
	ws, err := e.q.CreateWorkspace(bg, "ws-"+email)
	require.NoError(e.t, err)
	u, err := e.q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: email, Name: "A", PasswordHash: "x"})
	require.NoError(e.t, err)
	return ws.ID, auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "A", Scopes: policy.AllScopes})
}

func (e *policyEnv) putHeadscale(ctx context.Context, h *meshtest.Headscale, cred string) {
	e.t.Helper()
	secret := h.APIKey()
	if cred == tailscale.HeadscalePreauth {
		secret = "hskey-auth-reusable-0000"
	}
	_, err := e.svc.Put(ctx, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: h.URL, ClientSecret: secret, HeadscaleCredential: cred,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", "tag:nebula-mesh"},
	})
	require.NoError(e.t, err)
}

func (e *policyEnv) putTailscale(ctx context.Context) {
	e.t.Helper()
	_, err := e.svc.Put(ctx, tailscale.PutInput{
		ClientID: meshtest.TailscaleClientID, ClientSecret: meshtest.TailscaleClientSecret,
		DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc", "tag:nebula-mesh"},
	})
	require.NoError(e.t, err)
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func hsClient(h *meshtest.Headscale) *tailscale.HeadscaleClient {
	return tailscale.NewHeadscale(tailscale.HeadscaleConfig{LoginServer: h.URL, APIKey: h.APIKey(), HTTPClient: h.Client()})
}

func tsClient(f *meshtest.Tailscale) *tailscale.Client {
	return tailscale.New(tailscale.Config{
		BaseURL: f.URL, ClientID: meshtest.TailscaleClientID, ClientSecret: meshtest.TailscaleClientSecret, HTTPClient: f.Client(),
	})
}
