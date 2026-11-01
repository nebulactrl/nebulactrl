package servers_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/servers"
)

// platformHook records the workspaces whose platform routes were re-pushed.
type platformHook struct {
	mu   sync.Mutex
	ws   []uuid.UUID
	fail error
}

func (h *platformHook) ServersChanged(_ context.Context, tx pgx.Tx, ws uuid.UUID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if tx == nil {
		return errors.New("the hook must run inside the transaction of the change")
	}
	if h.fail != nil {
		return h.fail
	}
	h.ws = append(h.ws, ws)
	return nil
}

func (h *platformHook) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.ws)
}

// TestPlatformRoutesAreRepushedWhenServersChange: platform services allow the
// public address of every server, so adding a server with an address, changing
// an address and revoking a server re-send the platform route tables.
func TestPlatformRoutesAreRepushedWhenServersChange(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	hook := &platformHook{}
	f.svc.SetPlatformRoutes(hook)
	ws := uuid.Nil
	err := f.pool.QueryRow(context.Background(), "SELECT workspace_id FROM servers WHERE id = $1", f.server.ID).Scan(&ws)
	require.NoError(t, err)

	// A change that does not touch an address sends nothing.
	_, err = f.svc.Update(f.admin, f.server.ID, servers.UpdateInput{Name: new("renamed")})
	require.NoError(t, err)
	require.Zero(t, hook.calls())

	// A new address does.
	_, err = f.svc.Update(f.admin, f.server.ID, servers.UpdateInput{PublicIP: new("203.0.113.10")})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{ws}, hook.ws)

	// The same address again does not.
	_, err = f.svc.Update(f.admin, f.server.ID, servers.UpdateInput{PublicIP: new("203.0.113.10")})
	require.NoError(t, err)
	require.Equal(t, 1, hook.calls())

	// A changed address does, and so does clearing it.
	_, err = f.svc.Update(f.admin, f.server.ID, servers.UpdateInput{PublicIP: new("203.0.113.11")})
	require.NoError(t, err)
	require.Equal(t, 2, hook.calls())
	_, err = f.svc.Update(f.admin, f.server.ID, servers.UpdateInput{PublicIP: new("")})
	require.NoError(t, err)
	require.Equal(t, 3, hook.calls())

	// Adding a server: with an address it joins the list, without one it does not.
	_, err = f.svc.CreateEnrollmentToken(f.admin, servers.CreateTokenInput{Name: "no address"})
	require.NoError(t, err)
	require.Equal(t, 3, hook.calls())
	tok, err := f.svc.CreateEnrollmentToken(f.admin, servers.CreateTokenInput{Name: "worker", PublicIP: "198.51.100.7"})
	require.NoError(t, err)
	require.Equal(t, 4, hook.calls())

	// Revoking leaves the list, once.
	_, err = f.svc.Revoke(f.admin, tok.Server.ID)
	require.NoError(t, err)
	require.Equal(t, 5, hook.calls())
	_, err = f.svc.Revoke(f.admin, tok.Server.ID)
	require.NoError(t, err)
	require.Equal(t, 5, hook.calls(), "revoking twice is a no-op")
	require.Equal(t, []uuid.UUID{ws, ws, ws, ws, ws}, hook.ws)
}

// A failed push undoes an address change (the change and its route push are one
// transaction), but never a revoke.
func TestPlatformRoutePushFailure(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	hook := &platformHook{fail: errors.New("push failed")}
	f.svc.SetPlatformRoutes(hook)

	_, err := f.svc.Update(f.admin, f.server.ID, servers.UpdateInput{PublicIP: new("203.0.113.10")})
	require.Error(t, err)
	var ip *string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT public_ip FROM servers WHERE id = $1", f.server.ID).Scan(&ip))
	require.Nil(t, ip, "the address did not change without its route push")

	sv, err := f.svc.Revoke(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "revoked", sv.Status)
}
