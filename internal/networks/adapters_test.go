package networks_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// inTx runs fn in a transaction that is always rolled back afterwards.
func (f *fixture) inTx(tm team, fn func(tx pgx.Tx, q *db.Queries) error) error {
	f.t.Helper()
	tx, err := f.pool.Begin(tm.ctx)
	require.NoError(f.t, err)
	defer func() { require.NoError(f.t, tx.Rollback(context.Background())) }()
	return fn(tx, db.New(tx))
}

func (f *fixture) generation(id uuid.UUID) int64 {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service.Generation
}

func (f *fixture) attachments(id uuid.UUID) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM service_networks WHERE service_id = $1`, id).Scan(&n))
	return n
}

func TestAttachTxRolledBackLeavesNothing(t *testing.T) {
	f := newFixture(t)
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	gen := f.generation(api.ID)

	tx, err := f.pool.Begin(f.a.ctx)
	require.NoError(t, err)
	sc, err := services.LoadScope(f.a.ctx, db.New(tx), policy.ScopeProjectsWrite, api.ID)
	require.NoError(t, err)
	ref, after, err := f.nets.AttachTx(f.a.ctx, tx, sc, n.ID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.Equal(t, "api.production.shop", ref.Alias)
	require.NoError(t, tx.Rollback(context.Background()))

	require.Equal(t, 0, f.attachments(api.ID))
	require.Equal(t, gen, f.generation(api.ID))
	require.Equal(t, []string{"network.create"}, f.auditActions(f.a))
}

func TestAttachDetachTxCommitMatchesNonTx(t *testing.T) {
	f := newFixture(t)
	n, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	viaTx := f.service(f.a, "tx-api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	viaSvc := f.service(f.a, "svc-api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	genTx, genSvc := f.generation(viaTx.ID), f.generation(viaSvc.ID)

	want, err := f.nets.Attach(f.a.ctx, viaSvc.ID, n.ID)
	require.NoError(t, err)

	tx, err := f.pool.Begin(f.a.ctx)
	require.NoError(t, err)
	sc, err := services.LoadScope(f.a.ctx, db.New(tx), policy.ScopeProjectsWrite, viaTx.ID)
	require.NoError(t, err)
	got, after, err := f.nets.AttachTx(f.a.ctx, tx, sc, n.ID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.NoError(t, tx.Commit(f.a.ctx))
	after(f.a.ctx)

	require.Equal(t, want.Name, got.Name)
	require.Equal(t, want.ServerID, got.ServerID)
	require.Equal(t, "tx-api.production.shop", got.Alias)
	require.Equal(t, genTx+1, f.generation(viaTx.ID))
	require.Equal(t, genSvc+1, f.generation(viaSvc.ID))
	require.Equal(t, 1, f.attachments(viaTx.ID))

	// Re-attaching is a no-op: same ref, no generation bump, no second audit entry.
	tx, err = f.pool.Begin(f.a.ctx)
	require.NoError(t, err)
	sc, err = services.LoadScope(f.a.ctx, db.New(tx), policy.ScopeProjectsWrite, viaTx.ID)
	require.NoError(t, err)
	again, after, err := f.nets.AttachTx(f.a.ctx, tx, sc, n.ID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.NoError(t, tx.Commit(f.a.ctx))
	require.Equal(t, got, again)
	require.Equal(t, genTx+1, f.generation(viaTx.ID))

	// Detach: rolled back keeps the attachment, committed removes it and bumps once.
	tx, err = f.pool.Begin(f.a.ctx)
	require.NoError(t, err)
	sc, err = services.LoadScope(f.a.ctx, db.New(tx), policy.ScopeProjectsWrite, viaTx.ID)
	require.NoError(t, err)
	after, err = f.nets.DetachTx(f.a.ctx, tx, sc, n.ID)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.NoError(t, tx.Rollback(context.Background()))
	require.Equal(t, 1, f.attachments(viaTx.ID))
	require.Equal(t, genTx+1, f.generation(viaTx.ID))

	tx, err = f.pool.Begin(f.a.ctx)
	require.NoError(t, err)
	sc, err = services.LoadScope(f.a.ctx, db.New(tx), policy.ScopeProjectsWrite, viaTx.ID)
	require.NoError(t, err)
	after, err = f.nets.DetachTx(f.a.ctx, tx, sc, n.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(f.a.ctx))
	after(f.a.ctx)
	require.Equal(t, 0, f.attachments(viaTx.ID))
	require.Equal(t, genTx+2, f.generation(viaTx.ID))
	require.NoError(t, f.nets.Detach(f.a.ctx, viaSvc.ID, n.ID))
	require.Equal(t, genSvc+2, f.generation(viaSvc.ID))

	require.Equal(t,
		[]string{"network.create", "network.attach", "network.attach", "network.detach", "network.detach"},
		f.auditActions(f.a))
}

func TestAttachDetachTxErrorParity(t *testing.T) {
	f := newFixture(t)
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.a.ws.ID, Name: "alpha-2"})
	require.NoError(t, err)
	far, err := f.nets.Create(f.a.ctx, "Far", other.ID)
	require.NoError(t, err)
	home, err := f.nets.Create(f.a.ctx, "Home", f.a.server.ID)
	require.NoError(t, err)
	api := f.service(f.a, "api", &f.a.server.ID, services.Config{Image: "x:1", Port: 80})
	blocked := f.service(f.a, "db", &f.a.server.ID, services.Config{Image: "postgres:17", Port: 5432, BlockEgress: true})
	unplaced := f.service(f.a, "worker", nil, services.Config{Image: "x:1"})
	bsvc := f.service(f.b, "api", &f.b.server.ID, services.Config{Image: "x:1", Port: 80})

	attach := func(tm team, svc, net uuid.UUID) error {
		return f.inTx(tm, func(tx pgx.Tx, q *db.Queries) error {
			sc, err := services.LoadScope(tm.ctx, q, policy.ScopeProjectsWrite, svc)
			if err != nil {
				return err
			}
			_, _, err = f.nets.AttachTx(tm.ctx, tx, sc, net)
			return err
		})
	}

	for _, tc := range []struct {
		name     string
		svc, net uuid.UUID
		tm       team
		status   int
		code     string
	}{
		{"wrong server", api.ID, far.ID, f.a, 422, "network_wrong_server"},
		{"no server", unplaced.ID, home.ID, f.a, 422, "service_no_server"},
		{"egress blocked", blocked.ID, home.ID, f.a, 422, "egress_blocked"},
		{"other team's network", bsvc.ID, home.ID, f.b, 404, "not_found"},
		{"other team's service", bsvc.ID, home.ID, f.a, 404, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			txErr := attach(tc.tm, tc.svc, tc.net)
			_, svcErr := f.nets.Attach(tc.tm.ctx, tc.svc, tc.net)
			requireStatus(t, txErr, tc.status, tc.code)
			requireStatus(t, svcErr, tc.status, tc.code)
			require.Equal(t, svcErr.Error(), txErr.Error())
		})
	}

	// Detaching something that is not attached is 404 in both flavours.
	txErr := f.inTx(f.a, func(tx pgx.Tx, q *db.Queries) error {
		sc, err := services.LoadScope(f.a.ctx, q, policy.ScopeProjectsWrite, api.ID)
		if err != nil {
			return err
		}
		_, err = f.nets.DetachTx(f.a.ctx, tx, sc, home.ID)
		return err
	})
	requireStatus(t, txErr, 404, "not_found")
	requireStatus(t, f.nets.Detach(f.a.ctx, api.ID, home.ID), 404, "not_found")
}

func TestResolveByName(t *testing.T) {
	f := newFixture(t)
	second, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.a.ws.ID, Name: "alpha-2"})
	require.NoError(t, err)
	home, err := f.nets.Create(f.a.ctx, "Backend", f.a.server.ID)
	require.NoError(t, err)
	other, err := f.nets.Create(f.a.ctx, "Edge", second.ID)
	require.NoError(t, err)
	_, err = f.nets.Create(f.b.ctx, "Backend", f.b.server.ID)
	require.NoError(t, err)

	got, err := f.nets.ResolveByName(context.Background(), f.q, f.a.ws.ID, "backend", nil)
	require.NoError(t, err)
	require.Equal(t, home.ID, got.ID, "name matches case-insensitively and only inside the workspace")

	got, err = f.nets.ResolveByName(context.Background(), f.q, f.a.ws.ID, "Edge", &second.ID)
	require.NoError(t, err)
	require.Equal(t, other.ID, got.ID)

	_, err = f.nets.ResolveByName(context.Background(), f.q, f.a.ws.ID, "Edge", &f.a.server.ID)
	requireStatus(t, err, 422, "unknown_network")
	_, err = f.nets.ResolveByName(context.Background(), f.q, f.a.ws.ID, "Nope", nil)
	requireStatus(t, err, 422, "unknown_network")

	// The unique index is per workspace, so the same name on several servers
	// cannot exist through the API; force it to prove the ambiguity guard.
	_, err = f.pool.Exec(context.Background(), `DROP INDEX shared_networks_name_key`)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(),
		`INSERT INTO shared_networks (workspace_id, server_id, name) VALUES ($1, $2, 'Backend')`, f.a.ws.ID, second.ID)
	require.NoError(t, err)
	_, err = f.nets.ResolveByName(context.Background(), f.q, f.a.ws.ID, "Backend", nil)
	requireStatus(t, err, 422, "ambiguous_reference")
	got, err = f.nets.ResolveByName(context.Background(), f.q, f.a.ws.ID, "Backend", &f.a.server.ID)
	require.NoError(t, err)
	require.Equal(t, home.ID, got.ID)
}
