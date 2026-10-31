package domains

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type fakeDNSHook struct{ created []uuid.UUID }

func (f *fakeDNSHook) DomainCreated(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	f.created = append(f.created, id)
	return db.New(tx).SetDomainCloudflareZone(ctx, db.SetDomainCloudflareZoneParams{ID: id, ZoneID: "z1", ZoneName: "example.com"})
}

func (f *fakeDNSHook) DomainRenamed(context.Context, pgx.Tx, uuid.UUID) error { return nil }

func (f *fakeDNSHook) DomainsRemoved(context.Context, pgx.Tx, uuid.UUID) error { return nil }

func TestDNSCheckMatchesCloudflareAndTrustsItsStatus(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "team")
	require.NoError(t, err)
	proj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	ip := "203.0.113.10"
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "alpha", PublicIp: &ip})
	require.NoError(t, err)
	svcRow, err := q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1","port":80}`), ServerID: &srv.ID,
	})
	require.NoError(t, err)
	// A domain inserted the way templates and previews do: no DNS hook runs.
	dom, err := q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svcRow.ID, Hostname: "app.example.com", TargetPort: 80})
	require.NoError(t, err)
	require.Empty(t, dom.CfZoneID)

	hook := &fakeDNSHook{}
	svc := &Service{pool: pool, q: q, dns: hook}
	c := &DNSChecker{q: q, resolver: fakeResolver{err: errors.New("no such host")}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.match = func(ctx context.Context, id uuid.UUID) error {
		return db.InTx(ctx, svc.pool, func(tx pgx.Tx) error { return svc.dns.DomainCreated(ctx, tx, id) })
	}

	// The first check hands the domain to the hook, once.
	require.NoError(t, c.Check(bg, dom.ID))
	require.Equal(t, []uuid.UUID{dom.ID}, hook.created)
	got, err := q.GetDomainByID(bg, dom.ID)
	require.NoError(t, err)
	require.Equal(t, "z1", got.CfZoneID)
	require.Nil(t, got.DnsOk, "in a connected zone a lookup is not asked; Cloudflare's status decides")
	require.NotContains(t, got.StatusMessage, "Point an A record")
	require.NoError(t, c.Check(bg, dom.ID))
	require.Len(t, hook.created, 1, "a domain that was matched is not matched again")

	// Cloudflare says a record points here: that settles it, a lookup is not asked.
	_, err = pool.Exec(bg, `UPDATE domains SET cf_status = 'managed' WHERE id = $1`, dom.ID)
	require.NoError(t, err)
	require.NoError(t, c.Check(bg, dom.ID))
	got, err = q.GetDomainByID(bg, dom.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DnsOk)
	require.True(t, *got.DnsOk)
	require.Contains(t, got.StatusMessage, "through Cloudflare")

	// Proxied: the name resolves to Cloudflare, the message says so.
	_, err = pool.Exec(bg, `UPDATE domains SET cf_proxied = true WHERE id = $1`, dom.ID)
	require.NoError(t, err)
	require.NoError(t, c.Check(bg, dom.ID))
	got, err = q.GetDomainByID(bg, dom.ID)
	require.NoError(t, err)
	require.Contains(t, got.StatusMessage, "proxied through Cloudflare")

	// A wrong target is not vouched for.
	_, err = pool.Exec(bg, `UPDATE domains SET cf_status = 'wrong_target' WHERE id = $1`, dom.ID)
	require.NoError(t, err)
	require.NoError(t, c.Check(bg, dom.ID))
	got, err = q.GetDomainByID(bg, dom.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DnsOk)
	require.False(t, *got.DnsOk)
	require.NotContains(t, got.StatusMessage, "Point an A record", "NebulaCtrl creates the record in a connected zone")

	// A record NebulaCtrl did not create is a conflict: what exists is shown, not guidance to create a record.
	_, err = pool.Exec(bg, `UPDATE domains SET cf_status = 'conflict', cf_message = 'app.example.com already has CNAME elsewhere.example.net' WHERE id = $1`, dom.ID)
	require.NoError(t, err)
	require.NoError(t, c.Check(bg, dom.ID))
	got, err = q.GetDomainByID(bg, dom.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DnsOk)
	require.False(t, *got.DnsOk)
	require.Contains(t, got.StatusMessage, "elsewhere.example.net")

	// Not in a connected zone: the lookup and the manual guidance apply as before.
	_, err = pool.Exec(bg, `UPDATE domains SET cf_zone_id = '', cf_status = 'not_in_zone', cf_message = '' WHERE id = $1`, dom.ID)
	require.NoError(t, err)
	require.NoError(t, c.Check(bg, dom.ID))
	got, err = q.GetDomainByID(bg, dom.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DnsOk)
	require.False(t, *got.DnsOk)
	require.Contains(t, got.StatusMessage, "Point an A record")
}
