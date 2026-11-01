package dns01

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const canary = "cf-canary-token-98765"

func principal(ws uuid.UUID, scopes []string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: ws, Label: "t", Scopes: scopes})
}

func TestCredentialsResolversAndSecrets(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	ws2, err := q.CreateWorkspace(bg, "w2")
	require.NoError(t, err)
	email, name := "ops@example.com", "Edge"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: name, AcmeEmail: &email})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	svc := NewService(pool, nil, box)
	admin := principal(ws.ID, policy.AllScopes)
	dev := principal(ws.ID, []string{policy.ScopeProxyWrite, policy.ScopeServersRead})
	other := principal(ws2.ID, policy.AllScopes)

	// Only administrators create credentials; invalid variables are refused.
	_, err = svc.CreateCredential(dev, CredentialInput{Name: "cf", Provider: "cloudflare", Values: map[string]string{"CF_DNS_API_TOKEN": canary}})
	require.Error(t, err)
	_, err = svc.CreateCredential(admin, CredentialInput{Name: "cf", Provider: "cloudflare", Values: map[string]string{"PATH": "x"}})
	require.Error(t, err)
	cred, err := svc.CreateCredential(admin, CredentialInput{Name: "cf", Provider: "cloudflare", Values: map[string]string{"CF_DNS_API_TOKEN": canary}})
	require.NoError(t, err)

	// A secret value is never returned, by any read.
	list, err := svc.ListCredentials(dev, nil)
	require.NoError(t, err)
	raw, _ := json.Marshal(list)
	require.NotContains(t, string(raw), canary)
	require.True(t, list[0].Fields[0].Set)

	// Another team sees and touches nothing.
	_, err = svc.UpdateCredential(other, cred.ID, CredentialPatch{Name: new("x")})
	require.Error(t, err)
	_, err = svc.GetServerState(other, srv.ID)
	require.Error(t, err)

	// Staging a resolver bumps the proxy generation; the payload has names only.
	st, err := svc.SetResolver(dev, srv.ID, "cloudflare", ResolverInput{CredentialID: cred.ID})
	require.NoError(t, err)
	require.True(t, st.Staged)
	require.Equal(t, ChangeAdd, st.Resolvers[0].Change)
	_, err = svc.CreateWildcard(dev, srv.ID, WildcardInput{Domain: "*.Example.com", Provider: "cloudflare", IncludeApex: true})
	require.NoError(t, err)
	_, err = svc.CreateWildcard(dev, srv.ID, WildcardInput{Domain: "example.org", Provider: "route53"})
	require.Error(t, err, "no route53 resolver on the server")

	desired, err := Desired(bg, q, srv.ID)
	require.NoError(t, err)
	var pc agentprotocol.ProxyConfig
	desired.Apply(&pc)
	payload, err := json.Marshal(agentprotocol.ProxyConfigurePayload{Proxy: pc})
	require.NoError(t, err)
	require.NotContains(t, string(payload), canary, "credentials never enter a persisted payload")
	require.NoError(t, agentprotocol.ValidateDNS01(pc, "ops@example.com"))

	// Secrets are produced only for send time.
	secrets, err := Secrets(bg, q, box, srv.ID, &pc)
	require.NoError(t, err)
	require.Equal(t, canary, secrets[agentprotocol.DNSSecretKey(cred.ID.String(), "CF_DNS_API_TOKEN")])

	// An in-use credential cannot be deleted; rotation advances rev and keeps the name list.
	require.Error(t, svc.DeleteCredential(admin, cred.ID))
	rot, err := svc.UpdateCredential(admin, cred.ID, CredentialPatch{Values: map[string]string{"CF_DNS_API_TOKEN": "rotated-token-123"}})
	require.NoError(t, err)
	require.Equal(t, 2, rot.Rev)

	// A resolver with wildcards cannot be removed.
	_, err = svc.DeleteResolver(dev, srv.ID, "cloudflare")
	require.Error(t, err)

	// Plan: a new resolver recreates the proxy container.
	srvRow, err := q.LoadServerRow(bg, db.LoadServerRowParams{ID: srv.ID})
	require.NoError(t, err)
	plan, err := PlanFor(bg, q, srvRow)
	require.NoError(t, err)
	require.True(t, plan.Restart)
	require.NotEmpty(t, plan.Diff)
}
