package servers_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const licenseKey = "sekret-license-key-0123456789"

func newGeoService(t *testing.T) (*servers.Service, context.Context, servers.Server) {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	row, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "edge"})
	require.NoError(t, err)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Scopes: policy.AllScopes})
	box, err := secretbox.New([]byte(strings.Repeat("k", secretbox.KeySize)))
	require.NoError(t, err)
	svc := servers.NewService(pool, events.NewBus(), nil, nil, nil)
	svc.SetBox(box)
	srv, err := svc.Get(ctx, row.ID)
	require.NoError(t, err)
	return svc, ctx, srv
}

func requireStatus(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apierr.Error
	require.True(t, errors.As(err, &ae), "%v", err)
	require.Equal(t, status, ae.Status)
	require.Equal(t, code, ae.Code)
}

func TestGeoSettingsDefaultsRoundTripAndValidation(t *testing.T) {
	svc, ctx, srv := newGeoService(t)
	require.True(t, srv.GeoEnabled)
	require.Equal(t, "dbip", srv.GeoSource)
	require.Equal(t, "country", srv.GeoLevel)
	require.Equal(t, 7, srv.GeoRefreshDays)

	s, err := svc.Update(ctx, srv.ID, servers.UpdateInput{GeoLevel: new("city"), GeoRefreshDays: new(30)})
	require.NoError(t, err)
	require.Equal(t, "city", s.GeoLevel)
	require.Equal(t, 30, s.GeoRefreshDays)
	require.Equal(t, "dbip", s.GeoSource, "unset fields keep their value")
	require.True(t, s.GeoEnabled)

	s, err = svc.Update(ctx, srv.ID, servers.UpdateInput{GeoEnabled: new(false)})
	require.NoError(t, err)
	require.False(t, s.GeoEnabled)
	require.Equal(t, "city", s.GeoLevel)

	_, err = svc.Update(ctx, srv.ID, servers.UpdateInput{GeoSource: new("ipinfo")})
	requireStatus(t, err, 422, "invalid_geo_source")
	_, err = svc.Update(ctx, srv.ID, servers.UpdateInput{GeoLevel: new("street")})
	requireStatus(t, err, 422, "invalid_geo_level")
	for _, days := range []int{0, 91, -3} {
		_, err = svc.Update(ctx, srv.ID, servers.UpdateInput{GeoRefreshDays: new(days)})
		requireStatus(t, err, 422, "validation_failed")
	}
}

func TestMaxMindNeedsTheTeamsKey(t *testing.T) {
	svc, ctx, srv := newGeoService(t)
	_, err := svc.Update(ctx, srv.ID, servers.UpdateInput{GeoSource: new("maxmind")})
	requireStatus(t, err, 422, "maxmind_key_required")

	st, err := svc.SetGeoCredentials(ctx, "123456", licenseKey)
	require.NoError(t, err)
	require.True(t, st.Configured)
	require.Equal(t, "123456", st.AccountID)
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	require.NotContains(t, string(raw), licenseKey, "the licence key is write-only")

	s, err := svc.Update(ctx, srv.ID, servers.UpdateInput{GeoSource: new("maxmind")})
	require.NoError(t, err)
	require.Equal(t, "maxmind", s.GeoSource)

	// A server on MaxMind keeps the account from being deleted.
	st, err = svc.GeoCredentials(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, st.ServersUsing)
	_, err = svc.DeleteGeoCredentials(ctx)
	requireStatus(t, err, 409, "maxmind_in_use")

	_, err = svc.Update(ctx, srv.ID, servers.UpdateInput{GeoSource: new("dbip")})
	require.NoError(t, err)
	st, err = svc.DeleteGeoCredentials(ctx)
	require.NoError(t, err)
	require.False(t, st.Configured)
}

func TestMaxMindCredentialsAreValidatedSealedAndNeverAudited(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Scopes: policy.AllScopes})
	box, err := secretbox.New([]byte(strings.Repeat("k", secretbox.KeySize)))
	require.NoError(t, err)
	svc := servers.NewService(pool, events.NewBus(), nil, nil, nil)
	svc.SetBox(box)

	_, err = svc.SetGeoCredentials(ctx, "not-a-number", licenseKey)
	requireStatus(t, err, 422, "invalid_maxmind_account")
	_, err = svc.SetGeoCredentials(ctx, "123456", "short")
	requireStatus(t, err, 422, "invalid_maxmind_key")
	_, err = svc.SetGeoCredentials(ctx, "123456", "has spaces in it!")
	requireStatus(t, err, 422, "invalid_maxmind_key")

	_, err = svc.SetGeoCredentials(ctx, "123456", licenseKey)
	require.NoError(t, err)
	_, err = svc.SetGeoCredentials(ctx, "654321", licenseKey+"x") // replace
	require.NoError(t, err)

	var enc []byte
	var account string
	require.NoError(t, pool.QueryRow(bg, "SELECT maxmind_key_enc, maxmind_account_id FROM geo_credentials WHERE workspace_id = $1", ws.ID).Scan(&enc, &account))
	require.Equal(t, "654321", account)
	require.NotContains(t, string(enc), licenseKey, "stored sealed")
	plain, err := box.OpenString(secretbox.GeoCredentialsMaxmindKey, ws.ID.String(), enc)
	require.NoError(t, err)
	require.Equal(t, licenseKey+"x", plain)

	var audit string
	require.NoError(t, pool.QueryRow(bg, "SELECT coalesce(string_agg(metadata::text, ' '), '') FROM audit_events WHERE action = 'geo.maxmind.set'").Scan(&audit))
	require.Contains(t, audit, "654321")
	require.NotContains(t, audit, licenseKey)
}

func TestGeoCredentialsNeedServerScopes(t *testing.T) {
	svc, ctx, _ := newGeoService(t)
	p, _ := auth.PrincipalFrom(ctx)
	readOnly := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: p.WorkspaceID, Scopes: []string{policy.ScopeProjectsRead},
	})
	_, err := svc.SetGeoCredentials(readOnly, "123456", licenseKey)
	require.Error(t, err)
	_, err = svc.GeoCredentials(readOnly)
	require.Error(t, err)
	_, err = svc.DeleteGeoCredentials(readOnly)
	require.Error(t, err)
}

func TestGeoConfigForTheAgent(t *testing.T) {
	svc, ctx, srv := newGeoService(t)

	cfg, secrets, err := svc.GeoConfigFor(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, &agentprotocol.GeoConfig{Enabled: true, Source: "dbip", Level: "country", RefreshDays: 7}, cfg)
	require.Empty(t, secrets, "DB-IP needs no key")

	_, err = svc.SetGeoCredentials(ctx, "123456", licenseKey)
	require.NoError(t, err)
	_, err = svc.Update(ctx, srv.ID, servers.UpdateInput{GeoSource: new("maxmind"), GeoLevel: new("city"), GeoRefreshDays: new(3)})
	require.NoError(t, err)
	cfg, secrets, err = svc.GeoConfigFor(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, &agentprotocol.GeoConfig{Enabled: true, Source: "maxmind", Level: "city", RefreshDays: 3}, cfg)
	require.Equal(t, map[string]string{
		agentprotocol.SecretGeoMaxMindAccount: "123456",
		agentprotocol.SecretGeoMaxMindKey:     licenseKey,
	}, secrets)

	// Geolocation rides on traffic analytics: with it off the agent is told to delete the database.
	_, err = svc.Update(ctx, srv.ID, servers.UpdateInput{TrafficAnalyticsEnabled: new(false)})
	require.NoError(t, err)
	cfg, secrets, err = svc.GeoConfigFor(ctx, srv.ID)
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.Empty(t, secrets, "no key is sent while geolocation is off")
}
