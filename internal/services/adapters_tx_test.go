package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

var adapterKey = bytes.Repeat([]byte{9}, 32)

type txFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	ctx     context.Context
	svcs    *services.Service
	ws      db.Workspace
	project db.Project
	prod    db.Environment
	staging db.Environment
}

func newTxFixture(t *testing.T) *txFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	staging, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	box, err := secretbox.New(adapterKey)
	require.NoError(t, err)
	svcs := services.NewService(pool, events.NewBus(), box, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "dev@example.com", Scopes: policy.AllScopes})
	return &txFixture{t: t, pool: pool, q: q, ctx: ctx, svcs: svcs, ws: ws, project: prj, prod: prod, staging: staging}
}

func (f *txFixture) insert(envID uuid.UUID, slug string, cfg services.Config) db.Service {
	f.t.Helper()
	raw, err := json.Marshal(cfg.WithDefaults())
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: envID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *txFixture) imageService(envID uuid.UUID, slug string) db.Service {
	return f.insert(envID, slug, services.Config{Image: "traefik/whoami:latest", Port: 80})
}

func (f *txFixture) row(id uuid.UUID) db.Service {
	f.t.Helper()
	sc, err := services.LoadScopeInternal(context.Background(), f.q, id)
	require.NoError(f.t, err)
	return sc.Service
}

func (f *txFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func (f *txFixture) audits(action, resourceID string) int {
	f.t.Helper()
	return f.count("SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2", action, resourceID)
}

// inTx runs fn in a transaction that commits when commit is true and rolls back
// otherwise; it returns fn's error.
func (f *txFixture) inTx(commit bool, fn func(tx pgx.Tx) error) error {
	f.t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(f.t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(tx); err != nil {
		return err
	}
	if commit {
		require.NoError(f.t, tx.Commit(f.ctx))
	}
	return nil
}

func (f *txFixture) scope(tx pgx.Tx, id uuid.UUID) services.Scope {
	f.t.Helper()
	sc, err := services.LoadScope(f.ctx, db.New(tx), policy.ScopeProjectsWrite, id)
	require.NoError(f.t, err)
	return sc
}

func (f *txFixture) env(tx pgx.Tx, id uuid.UUID) services.EnvScope {
	f.t.Helper()
	env, err := services.LoadEnvironment(f.ctx, db.New(tx), policy.ScopeProjectsWrite, id)
	require.NoError(f.t, err)
	return env
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, code, ae.Code)
}

// ── UpdateTx ────────────────────────────────────────────────────────────────

func newMemoryConfig(row db.Service, mb int) *services.Config {
	cfg := services.DecodeConfig(row.Config)
	cfg.MemoryMB = mb
	return &cfg
}

func TestUpdateTxRollbackLeavesNoChange(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.prod.ID, "web")
	name := "Renamed"

	err := f.inTx(false, func(tx pgx.Tx) error {
		sc := f.scope(tx, svc.ID)
		row, after, err := f.svcs.UpdateTx(f.ctx, tx, sc, svc.Generation, services.UpdateInput{Name: &name, Config: newMemoryConfig(svc, 256)})
		require.NoError(t, err)
		require.Equal(t, "Renamed", row.Name)
		require.Equal(t, svc.Generation+1, row.Generation)
		require.NotNil(t, after)
		// The caller's own transaction sees the change.
		var inTxName string
		require.NoError(t, tx.QueryRow(f.ctx, "SELECT name FROM services WHERE id = $1", svc.ID).Scan(&inTxName))
		require.Equal(t, "Renamed", inTxName)
		return nil
	})
	require.NoError(t, err)

	after := f.row(svc.ID)
	require.Equal(t, "web", after.Name)
	require.Equal(t, svc.Generation, after.Generation)
	require.JSONEq(t, string(svc.Config), string(after.Config))
	require.Equal(t, 0, f.audits("service.update", svc.ID.String()))
}

func TestUpdateTxCommittedMatchesUpdate(t *testing.T) {
	f := newTxFixture(t)
	a := f.imageService(f.prod.ID, "viaupdate")
	b := f.imageService(f.prod.ID, "viatx")
	name := "Renamed"

	view, err := f.svcs.Update(f.ctx, a.ID, a.Generation, services.UpdateInput{Name: &name, Config: newMemoryConfig(a, 256)})
	require.NoError(t, err)
	require.Equal(t, "Renamed", view.Name)

	var txRow db.Service
	var after func(context.Context)
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		var err error
		txRow, after, err = f.svcs.UpdateTx(f.ctx, tx, f.scope(tx, b.ID), b.Generation, services.UpdateInput{Name: &name, Config: newMemoryConfig(b, 256)})
		return err
	}))
	require.NotNil(t, after)
	after(f.ctx) // must be safe to run: no mesh, no source hook

	ra, rb := f.row(a.ID), f.row(b.ID)
	require.Equal(t, txRow.Generation, rb.Generation)
	require.Equal(t, ra.Generation-a.Generation, rb.Generation-b.Generation, "one generation bump each")
	require.Equal(t, int64(1), rb.Generation-b.Generation)
	require.Equal(t, ra.Name, rb.Name)
	require.Equal(t, services.DecodeConfig(ra.Config), services.DecodeConfig(rb.Config))
	require.Equal(t, 256, services.DecodeConfig(rb.Config).MemoryMB)

	require.Equal(t, 1, f.audits("service.update", a.ID.String()))
	require.Equal(t, 1, f.audits("service.update", b.ID.String()))
	var fa, fb string
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT (metadata->'fields')::text FROM audit_events WHERE action = 'service.update' AND resource_id = $1", a.ID.String()).Scan(&fa))
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT (metadata->'fields')::text FROM audit_events WHERE action = 'service.update' AND resource_id = $1", b.ID.String()).Scan(&fb))
	require.JSONEq(t, fa, fb)
}

func TestUpdateTxGenerationConflict(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.prod.ID, "web")
	name := "Renamed"

	// A wrong expected generation.
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		_, after, err := f.svcs.UpdateTx(f.ctx, tx, f.scope(tx, svc.ID), svc.Generation+5, services.UpdateInput{Name: &name})
		requireCode(t, err, "generation_conflict")
		require.NotNil(t, after)
		return nil
	}))

	// Somebody else commits between loading the scope and the update.
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		sc := f.scope(tx, svc.ID)
		_, err := f.pool.Exec(context.Background(), "UPDATE services SET generation = generation + 1 WHERE id = $1", svc.ID)
		require.NoError(t, err)
		_, _, err = f.svcs.UpdateTx(f.ctx, tx, sc, svc.Generation, services.UpdateInput{Name: &name})
		requireCode(t, err, "generation_conflict")
		return nil
	}))
	require.Equal(t, "web", f.row(svc.ID).Name)
	require.Equal(t, 0, f.audits("service.update", svc.ID.String()))
}

func TestUpdateTxNothingChanged(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.prod.ID, "web")
	same := "web"
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		row, after, err := f.svcs.UpdateTx(f.ctx, tx, f.scope(tx, svc.ID), svc.Generation, services.UpdateInput{Name: &same, Config: func() *services.Config { c := services.DecodeConfig(svc.Config); return &c }()})
		require.NoError(t, err)
		require.Equal(t, svc.ID, row.ID)
		require.Equal(t, svc.Generation, row.Generation)
		require.NotNil(t, after)
		after(f.ctx)
		return nil
	}))
	require.Equal(t, svc.Generation, f.row(svc.ID).Generation)
	require.Equal(t, 0, f.audits("service.update", svc.ID.String()))

	// Update agrees: no bump either.
	view, err := f.svcs.Update(f.ctx, svc.ID, svc.Generation, services.UpdateInput{Name: &same})
	require.NoError(t, err)
	require.Equal(t, svc.Generation, f.row(svc.ID).Generation)
	require.Equal(t, "web", view.Name)
}

func TestUpdateTxValidationMatchesUpdate(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.prod.ID, "web")
	empty := ""
	badCfg := services.DecodeConfig(svc.Config)
	badCfg.Image = ""
	gitToken := "ghp_x"

	unknownServer := uuid.New()
	for name, in := range map[string]services.UpdateInput{
		"empty name":         {Name: &empty},
		"invalid config":     {Config: &badCfg},
		"git token on image": {GitToken: &gitToken},
		"unknown server":     {ServerID: &unknownServer},
	} {
		_, wantErr := f.svcs.Update(f.ctx, svc.ID, svc.Generation, in)
		var want *apierr.Error
		require.ErrorAs(t, wantErr, &want, name)

		err := f.inTx(false, func(tx pgx.Tx) error {
			_, _, err := f.svcs.UpdateTx(f.ctx, tx, f.scope(tx, svc.ID), svc.Generation, in)
			return err
		})
		var got *apierr.Error
		require.ErrorAs(t, err, &got, name)
		require.Equal(t, want.Code, got.Code, name)
		require.Equal(t, want.Status, got.Status, name)
	}
}

// A server of another workspace is refused like a missing one, on both the
// direct and the in-transaction update; a server of the caller's workspace is accepted.
func TestUpdateRefusesServerOfAnotherWorkspace(t *testing.T) {
	f := newTxFixture(t)
	bg := context.Background()
	svc := f.imageService(f.prod.ID, "web")

	otherWS, err := f.q.CreateWorkspace(bg, "other")
	require.NoError(t, err)
	foreign, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: otherWS.ID, Name: "foreign"})
	require.NoError(t, err)
	own, err := f.q.CreateServer(bg, db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "own"})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1 OR id = $2", foreign.ID, own.ID)
	require.NoError(t, err)

	in := services.UpdateInput{ServerID: &foreign.ID}
	var ae *apierr.Error
	_, err = f.svcs.Update(f.ctx, svc.ID, svc.Generation, in)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "server_not_found", ae.Code)

	err = f.inTx(false, func(tx pgx.Tx) error {
		_, _, err := f.svcs.UpdateTx(f.ctx, tx, f.scope(tx, svc.ID), svc.Generation, in)
		return err
	})
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "server_not_found", ae.Code)
	require.Nil(t, f.row(svc.ID).ServerID, "the service was not moved")

	_, err = f.svcs.Update(f.ctx, svc.ID, svc.Generation, services.UpdateInput{ServerID: &own.ID})
	require.NoError(t, err)
}

// ── volumes ─────────────────────────────────────────────────────────────────

func TestAddVolumeTxAndDeleteVolumeTx(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.prod.ID, "web")

	// Rolled back: nothing remains.
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		v, err := f.svcs.AddVolumeTx(f.ctx, tx, f.scope(tx, svc.ID), "uploads", "/app/uploads")
		require.NoError(t, err)
		require.Equal(t, "uploads", v.Name)
		return nil
	}))
	require.Equal(t, 0, f.count("SELECT count(*) FROM volumes WHERE service_id = $1", svc.ID))
	require.Equal(t, svc.Generation, f.row(svc.ID).Generation)
	require.Equal(t, 0, f.count("SELECT count(*) FROM audit_events WHERE action = 'volume.create'"))

	// Committed: row, one generation bump, audit.
	var view services.VolumeView
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		var err error
		view, err = f.svcs.AddVolumeTx(f.ctx, tx, f.scope(tx, svc.ID), " uploads ", "/app/uploads/")
		return err
	}))
	require.Equal(t, "uploads", view.Name)
	require.Equal(t, "/app/uploads", view.MountPath, "the path is cleaned like AddVolume does")
	require.Equal(t, 1, f.count("SELECT count(*) FROM volumes WHERE service_id = $1", svc.ID))
	require.Equal(t, svc.Generation+1, f.row(svc.ID).Generation)
	require.Equal(t, 1, f.audits("volume.create", view.ID.String()))

	// Same validation as AddVolume.
	direct, err := f.svcs.AddVolume(f.ctx, svc.ID, "cache", "/app/cache")
	require.NoError(t, err)
	require.Equal(t, "/app/cache", direct.MountPath)
	for _, c := range []struct{ name, mount, code string }{
		{"uploads", "/other", "volume_exists"},
		{"other", "/app/uploads", "mount_path_in_use"},
		{"Bad Name", "/x", "invalid_volume_name"},
		{"fine", "relative", "invalid_mount_path"},
		{"fine", "/", "invalid_mount_path"},
		{"fine", "/a:b", "invalid_mount_path"},
	} {
		_, wantErr := f.svcs.AddVolume(f.ctx, svc.ID, c.name, c.mount)
		requireCode(t, wantErr, c.code)
		err := f.inTx(false, func(tx pgx.Tx) error {
			_, err := f.svcs.AddVolumeTx(f.ctx, tx, f.scope(tx, svc.ID), c.name, c.mount)
			return err
		})
		requireCode(t, err, c.code)
	}

	// A route has no container to mount volumes into (RequireContainer is kept).
	route := f.insert(f.prod.ID, "edge", services.Config{})
	_, err = f.pool.Exec(context.Background(), "UPDATE services SET kind = 'route', source_type = 'external' WHERE id = $1", route.ID)
	require.NoError(t, err)
	err = f.inTx(false, func(tx pgx.Tx) error {
		_, err := f.svcs.AddVolumeTx(f.ctx, tx, f.scope(tx, route.ID), "data", "/data")
		return err
	})
	requireCode(t, err, "not_applicable")

	// DeleteVolumeTx: rolled back keeps the row, committed detaches it.
	gen := f.row(svc.ID).Generation
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		return f.svcs.DeleteVolumeTx(f.ctx, tx, f.scope(tx, svc.ID), "uploads")
	}))
	require.Equal(t, 2, f.count("SELECT count(*) FROM volumes WHERE service_id = $1", svc.ID))
	require.Equal(t, gen, f.row(svc.ID).Generation)

	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		return f.svcs.DeleteVolumeTx(f.ctx, tx, f.scope(tx, svc.ID), "uploads")
	}))
	require.Equal(t, 1, f.count("SELECT count(*) FROM volumes WHERE service_id = $1", svc.ID))
	require.Equal(t, 0, f.count("SELECT count(*) FROM volumes WHERE service_id = $1 AND name = 'uploads'", svc.ID))
	require.Equal(t, gen+1, f.row(svc.ID).Generation)
	require.Equal(t, 1, f.audits("volume.delete", view.ID.String()))

	// An unknown name is a 404 and changes nothing.
	err = f.inTx(false, func(tx pgx.Tx) error {
		return f.svcs.DeleteVolumeTx(f.ctx, tx, f.scope(tx, svc.ID), "uploads")
	})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, 404, ae.Status)

	// DeleteVolume (by id) goes through the same core.
	require.NoError(t, f.svcs.DeleteVolume(f.ctx, direct.ID))
	require.Equal(t, 0, f.count("SELECT count(*) FROM volumes WHERE service_id = $1", svc.ID))
	require.Equal(t, gen+2, f.row(svc.ID).Generation)
	require.Equal(t, 1, f.audits("volume.delete", direct.ID.String()))
}

// ── links ───────────────────────────────────────────────────────────────────

func TestCreateLinkTxAndDeleteLinkTx(t *testing.T) {
	f := newTxFixture(t)
	web := f.imageService(f.prod.ID, "web")
	api := f.imageService(f.prod.ID, "api")
	stray := f.imageService(f.staging.ID, "stray")

	// Rolled back: no row, no audit.
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		res, after, err := f.svcs.CreateLinkTx(f.ctx, tx, f.env(tx, f.prod.ID), web.ID, api.ID)
		require.NoError(t, err)
		require.NotNil(t, after)
		require.Equal(t, web.ID, res.Link.FromServiceID)
		return nil
	}))
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_links"))
	require.Equal(t, 0, f.count("SELECT count(*) FROM audit_events WHERE action = 'link.create'"))

	// Committed.
	var linkID string
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		res, after, err := f.svcs.CreateLinkTx(f.ctx, tx, f.env(tx, f.prod.ID), web.ID, api.ID)
		require.NoError(t, err)
		require.NotNil(t, after)
		require.NotNil(t, res.ProposedVariables)
		linkID = res.Link.ID
		return nil
	}))
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links WHERE from_service_id = $1 AND to_service_id = $2", web.ID, api.ID))
	require.Equal(t, 1, f.audits("link.create", linkID))

	// Refusals: duplicate, self, other environment, same codes as CreateLink.
	for _, c := range []struct {
		from, to uuid.UUID
		code     string
	}{
		{web.ID, api.ID, "link_exists"},
		{web.ID, web.ID, "invalid_link"},
		{web.ID, stray.ID, "invalid_link"},
	} {
		_, wantErr := f.svcs.CreateLink(f.ctx, f.prod.ID, c.from, c.to)
		requireCode(t, wantErr, c.code)
		err := f.inTx(false, func(tx pgx.Tx) error {
			_, _, err := f.svcs.CreateLinkTx(f.ctx, tx, f.env(tx, f.prod.ID), c.from, c.to)
			return err
		})
		requireCode(t, err, c.code)
	}
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links"))

	// The reverse edge is a different link and does not collide.
	res, err := f.svcs.CreateLink(f.ctx, f.prod.ID, api.ID, web.ID)
	require.NoError(t, err)
	require.Equal(t, 2, f.count("SELECT count(*) FROM service_links"))
	require.Equal(t, 1, f.audits("link.create", res.Link.ID))

	// DeleteLinkTx: rolled back keeps it; committed removes it once; a missing link reports false.
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		ok, after, err := f.svcs.DeleteLinkTx(f.ctx, tx, f.env(tx, f.prod.ID), web.ID, api.ID)
		require.NoError(t, err)
		require.True(t, ok)
		require.NotNil(t, after)
		return nil
	}))
	require.Equal(t, 2, f.count("SELECT count(*) FROM service_links"))

	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		ok, after, err := f.svcs.DeleteLinkTx(f.ctx, tx, f.env(tx, f.prod.ID), web.ID, api.ID)
		require.NoError(t, err)
		require.True(t, ok)
		require.NotNil(t, after)
		after(f.ctx)
		ok, after, err = f.svcs.DeleteLinkTx(f.ctx, tx, f.env(tx, f.prod.ID), web.ID, api.ID)
		require.NoError(t, err)
		require.False(t, ok)
		require.NotNil(t, after)
		after(f.ctx)
		return nil
	}))
	require.Equal(t, 1, f.count("SELECT count(*) FROM service_links"))
	require.Equal(t, 1, f.audits("link.delete", linkID))

	// DeleteLink (by id) shares the core.
	require.NoError(t, f.svcs.DeleteLink(f.ctx, res.Link.ID))
	require.Equal(t, 0, f.count("SELECT count(*) FROM service_links"))
	require.Equal(t, 1, f.audits("link.delete", res.Link.ID))
}

// ── CreateInTx with an exact slug ───────────────────────────────────────────

func TestCreateInTxExactSlug(t *testing.T) {
	f := newTxFixture(t)
	in := func(name, slug string) services.CreateInput {
		return services.CreateInput{Name: name, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: services.Config{Image: "traefik/whoami:latest", Port: 80}}
	}
	create := func(commit bool, ci services.CreateInput) (db.Service, error) {
		var row db.Service
		err := f.inTx(commit, func(tx pgx.Tx) error {
			var err error
			row, err = f.svcs.CreateInTx(f.ctx, tx, f.env(tx, f.prod.ID), ci)
			return err
		})
		return row, err
	}

	// Used verbatim, whatever the name derives to.
	row, err := create(true, in("Web App", "frontend"))
	require.NoError(t, err)
	require.Equal(t, "frontend", row.Slug)
	require.Equal(t, "Web App", row.Name)
	require.Equal(t, "frontend", f.row(row.ID).Slug)

	// 63 characters are fine.
	long := strings.Repeat("a", 63)
	row, err = create(true, in("Long", long))
	require.NoError(t, err)
	require.Equal(t, long, row.Slug)

	// Empty still derives it from the name, with the usual numeric suffix on a clash.
	row, err = create(true, in("Frontend", ""))
	require.NoError(t, err)
	require.Equal(t, "frontend-2", row.Slug, "the derived slug avoids the exact one")

	// A slug a live service already holds is a 409 and never gets a suffix.
	_, err = create(false, in("Another", "frontend"))
	requireCode(t, err, "slug_conflict")

	// The same slug is free in another environment.
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		r, err := f.svcs.CreateInTx(f.ctx, tx, f.env(tx, f.staging.ID), in("Web", "frontend"))
		require.NoError(t, err)
		require.Equal(t, "frontend", r.Slug)
		return nil
	}))

	// Invalid and reserved slugs are rejected before anything is written.
	before := f.count("SELECT count(*) FROM services")
	invalid := []string{"Bad_Slug", "UPPER", "-lead", "has space", "dot.ted", strings.Repeat("a", 64), "é"}
	for _, s := range invalid {
		_, err = create(false, in("X", s))
		requireCode(t, err, "validation_failed")
	}
	for _, s := range services.ReservedSlugs {
		_, err = create(false, in("X", s))
		requireCode(t, err, "validation_failed")
		require.True(t, services.IsReservedSlug(s))
	}
	require.Equal(t, before, f.count("SELECT count(*) FROM services"))

	// A rolled back create leaves nothing, so the slug stays free.
	_, err = create(false, in("Temp", "scratch"))
	require.NoError(t, err)
	require.Equal(t, 0, f.count("SELECT count(*) FROM services WHERE slug = 'scratch'"))
	_, err = create(true, in("Temp", "scratch"))
	require.NoError(t, err)

	// Create (not in a transaction) does not take a slug.
	_, err = f.svcs.Create(f.ctx, f.prod.ID, in("Plain", "plain"))
	requireCode(t, err, "validation_failed")
	require.Equal(t, 0, f.count("SELECT count(*) FROM services WHERE slug = 'plain'"))
}
