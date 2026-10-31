package tailscale

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type env struct {
	pool  *pgxpool.Pool
	q     *db.Queries
	svc   *Service
	fake  *fakeTailscale
	ws    uuid.UUID
	admin context.Context
	user  context.Context // no admin scope
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(context.Background(), "ws")
	require.NoError(t, err)
	u, err := q.CreateUser(context.Background(), db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	key, err := secretbox.GenerateKey()
	require.NoError(t, err)
	raw, err := secretbox.ParseKey(key)
	require.NoError(t, err)
	box, err := secretbox.New(raw)
	require.NoError(t, err)
	f := newFake(t)
	svc := NewService(pool, box, nil, nil)
	svc.SetBaseURL(f.srv.URL, f.srv.Client())
	return &env{
		pool: pool, q: q, svc: svc, fake: f, ws: ws.ID,
		admin: auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "A", Scopes: policy.AllScopes}),
		user:  auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "A", Scopes: []string{policy.ScopeProjectsRead, policy.ScopeProxyWrite, policy.ScopeServersWrite}}),
	}
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func TestIntegrationLifecycleNeverReturnsTheSecret(t *testing.T) {
	e := newEnv(t)

	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.False(t, v.Configured)
	require.Equal(t, []string{DefaultTag}, v.DefaultTags)

	// Admin only.
	_, err = e.svc.Put(e.user, PutInput{ClientID: "client-1", ClientSecret: testSecret})
	require.Equal(t, "forbidden", apiCode(t, err))
	_, err = e.svc.Get(e.user)
	require.Equal(t, "forbidden", apiCode(t, err))

	// The first PUT needs the secret.
	_, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1"})
	require.Equal(t, "invalid_client_secret", apiCode(t, err))
	// Tags are validated.
	_, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: testSecret, DefaultTags: []string{"nebula"}})
	require.Equal(t, "invalid_tag", apiCode(t, err))
	_, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: testSecret, DefaultTags: []string{"tag:a"}, AllowedTags: []string{"tag:b"}})
	require.Equal(t, "invalid_tag", apiCode(t, err), "default tags must be allowed")

	v, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: testSecret, DefaultTags: []string{"tag:nebula-svc", "tag:edge"}, AllowedTags: []string{"tag:nebula-svc", "tag:edge", "tag:db"}})
	require.NoError(t, err)
	require.True(t, v.Configured)
	require.True(t, v.SecretSet)
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	require.NotContains(t, string(raw), testSecret)
	require.NotContains(t, string(raw), "secret_enc")

	// The secret is sealed at rest.
	row, err := e.q.GetTailnetIntegration(context.Background(), e.ws)
	require.NoError(t, err)
	require.NotContains(t, string(row.SecretEnc), testSecret)

	// Updating without the secret keeps it; changing the client id requires a new one.
	v, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1", Tailnet: "example.com", DefaultTags: []string{"tag:nebula-svc"}, AllowedTags: []string{"tag:nebula-svc"}})
	require.NoError(t, err)
	require.Equal(t, "example.com", v.Tailnet)
	row2, err := e.q.GetTailnetIntegration(context.Background(), e.ws)
	require.NoError(t, err)
	require.Equal(t, row.SecretEnc, row2.SecretEnc)
	_, err = e.svc.Put(e.admin, PutInput{ClientID: "client-2"})
	require.Equal(t, "invalid_client_secret", apiCode(t, err))

	// Audit rows name the changed keys, never values.
	var meta []string
	rows, err := e.pool.Query(context.Background(), `SELECT metadata::text FROM audit_events WHERE action = 'integration.tailscale.update'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var m string
		require.NoError(t, rows.Scan(&m))
		meta = append(meta, m)
	}
	require.Len(t, meta, 2)
	for _, m := range meta {
		require.NotContains(t, m, testSecret)
	}
	require.Contains(t, meta[0], "clientSecret")
	require.NotContains(t, meta[1], "clientSecret")
}

func TestTestRecordsResultAndDevicesList(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Test(e.admin)
	require.Equal(t, "not_found", apiCode(t, err))
	_, err = e.svc.Devices(e.admin)
	require.Equal(t, "tailnet_not_configured", apiCode(t, err))

	_, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: testSecret})
	require.NoError(t, err)
	out, err := e.svc.Test(e.admin)
	require.NoError(t, err)
	require.True(t, out.OK)
	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.NotNil(t, v.LastTest.OK)
	require.True(t, *v.LastTest.OK)
	require.NotNil(t, v.HTTPSEnabled)

	devs, err := e.svc.Devices(e.admin)
	require.NoError(t, err)
	require.Len(t, devs, 2)

	// A wrong secret fails the test and the failure is stored without the secret.
	_, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: "wrong-secret"})
	require.NoError(t, err)
	out, err = e.svc.Test(e.admin)
	require.NoError(t, err)
	require.False(t, out.OK)
	v, err = e.svc.Get(e.admin)
	require.NoError(t, err)
	require.NotNil(t, v.LastTest.OK)
	require.False(t, *v.LastTest.OK)
	require.NotContains(t, v.LastTest.Error, "wrong-secret")
	_, err = e.svc.Devices(e.admin)
	require.Equal(t, "tailscale_auth_failed", apiCode(t, err))
}

func TestMintAuthKeyUsesDefaultTagsAndDeleteDevice(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.MintAuthKey(context.Background(), e.ws, 3600, "d")
	require.ErrorIs(t, err, ErrNotConfigured)
	// No integration: deleting a device is a no-op (no API call).
	require.NoError(t, e.svc.DeleteDevice(context.Background(), e.ws, "nNODE1CNTRL"))
	require.Empty(t, e.fake.reqs())

	_, err = e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: testSecret, DefaultTags: []string{"tag:edge"}, AllowedTags: []string{"tag:edge", "tag:x"}})
	require.NoError(t, err)
	key, err := e.svc.MintAuthKey(context.Background(), e.ws, 3600, "nebulactrl server edge-1")
	require.NoError(t, err)
	require.Equal(t, "tskey-auth-FRESH-1234", key)
	var req createKeyRequest
	require.NoError(t, json.Unmarshal([]byte(e.fake.reqs()[0].Body), &req))
	require.Equal(t, []string{"tag:edge"}, req.Capabilities.Devices.Create.Tags)
	require.Equal(t, "nebulactrl server edge 1", req.Description, "only letters, digits and spaces reach the API")

	require.NoError(t, e.svc.DeleteDevice(context.Background(), e.ws, "nNODE1CNTRL"))
	// A device that is already gone is fine; other failures are returned for the job to retry.
	e.fake.deviceCode = http.StatusNotFound
	require.NoError(t, e.svc.DeleteDevice(context.Background(), e.ws, "nNODE1CNTRL"))
	e.fake.deviceCode = http.StatusInternalServerError
	require.Error(t, e.svc.DeleteDevice(context.Background(), e.ws, "nNODE1CNTRL"))
}

func TestDeleteIntegrationRefusedWhileAServerUsesManagedTailnet(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, "not_found", apiCode(t, e.svc.Delete(e.admin)))
	_, err := e.svc.Put(e.admin, PutInput{ClientID: "client-1", ClientSecret: testSecret})
	require.NoError(t, err)
	srv, err := e.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: e.ws, Name: "edge"})
	require.NoError(t, err)
	_, err = e.pool.Exec(context.Background(), `UPDATE servers SET tailnet_mode='managed' WHERE id=$1`, srv.ID)
	require.NoError(t, err)

	require.Equal(t, "integration_in_use", apiCode(t, e.svc.Delete(e.admin)))
	require.Equal(t, "forbidden", apiCode(t, e.svc.Delete(e.user)))

	_, err = e.pool.Exec(context.Background(), `UPDATE servers SET tailnet_mode='off'`)
	require.NoError(t, err)
	require.NoError(t, e.svc.Delete(e.admin))
	v, err := e.svc.Get(e.admin)
	require.NoError(t, err)
	require.False(t, v.Configured)
}
