package cloudflare

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/statusroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// controlPlane adds the control-plane server with the given public address(es).
func (e *env) controlPlane(t *testing.T, ips string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`INSERT INTO servers (workspace_id, name, status, is_control_plane, public_ip) VALUES ($1, 'cp', 'online', true, $2) RETURNING id`,
		e.ws, ips).Scan(&id))
	return id
}

// page inserts a status page of the team and matches its hostname the way the
// status page service does after creating it.
func (e *env) page(t *testing.T, host string, published bool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`INSERT INTO status_pages (workspace_id, title, slug, hostname, published) VALUES ($1, 'Status', $2, $3, $4) RETURNING id`,
		e.ws, "s-"+uuid.NewString()[:8], host, published).Scan(&id))
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.StatusPageHostChanged(context.Background(), tx, id, true) })
	return id
}

func (e *env) pageRow(t *testing.T, id uuid.UUID) db.StatusPage {
	t.Helper()
	p, err := e.q.GetStatusPageByID(context.Background(), id)
	require.NoError(t, err)
	return p
}

func (e *env) recordsByType(host string) map[string]Record {
	out := map[string]Record{}
	for _, r := range e.fake.records(host) {
		out[r.Type] = r
	}
	return out
}

func TestStatusPageGetsMarkedRecordsOfTheControlPlane(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	cp := e.controlPlane(t, "203.0.113.20, 2001:db8::20")
	id := e.page(t, "status.example.com", false)

	// The hostname is matched against the team's zone: managed from now on and proxied.
	p := e.pageRow(t, id)
	require.Equal(t, "z1", p.CfZoneID)
	require.Equal(t, "example.com", p.CfZoneName)
	require.True(t, p.CfProxied)
	require.True(t, p.CfOriginLock, "every page starts opted in to the lock")
	require.Equal(t, 1, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_sync_status_page'`))

	// An A record per IPv4 and an AAAA record per IPv6 address, marked with the page's id.
	require.NoError(t, e.svc.SyncStatusPage(bg, id))
	recs := e.recordsByType("status.example.com")
	require.Len(t, recs, 2)
	require.Equal(t, "203.0.113.20", recs["A"].Content)
	require.Equal(t, "2001:db8::20", recs["AAAA"].Content)
	for _, r := range recs {
		require.Equal(t, Marker(id), r.Comment)
		require.True(t, r.Proxied)
	}
	p = e.pageRow(t, id)
	require.Equal(t, StatusManaged, p.CfStatus)
	require.Empty(t, p.CfError)
	require.Equal(t, 2, e.count(t, `SELECT count(*) FROM cloudflare_records WHERE owner_kind = 'status_page' AND owner_id = $1`, id))
	require.Equal(t, 2, e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'dns.record.create' AND resource_type = 'status_page' AND resource_id = $1`, id.String()))

	// Syncing again changes nothing.
	e.fake.resetWrites()
	require.NoError(t, e.svc.SyncStatusPage(bg, id))
	require.Empty(t, e.fake.writeLog())

	// The control plane's IPv4 address changes: that record is retargeted in place.
	before := recs["A"].ID
	e.exec(t, `UPDATE servers SET public_ip = '192.0.2.44, 2001:db8::20' WHERE id = $1`, cp)
	require.NoError(t, e.svc.SyncStatusPage(bg, id))
	recs = e.recordsByType("status.example.com")
	require.Len(t, recs, 2)
	require.Equal(t, "192.0.2.44", recs["A"].Content)
	require.Equal(t, before, recs["A"].ID)
	require.Len(t, e.fake.writeLog(), 1, "only the changed record is written")

	// The collector never takes a page's records for orphans while the page has the hostname.
	require.NoError(t, e.svc.Collect(bg, e.ws))
	require.Len(t, e.fake.records("status.example.com"), 2)

	// Removing the hostname lets the collector delete the records NebulaCtrl created.
	e.exec(t, `UPDATE status_pages SET hostname = NULL WHERE id = $1`, id)
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.StatusPageHostChanged(bg, tx, id, false) })
	require.Empty(t, e.pageRow(t, id).CfZoneID)
	require.NoError(t, e.svc.Collect(bg, e.ws))
	require.Empty(t, e.fake.records("status.example.com"))
}

func TestStatusPageWithoutAControlPlaneIPCreatesNothing(t *testing.T) {
	e := newEnv(t)
	e.connect(t)
	id := e.page(t, "status.example.com", false)
	require.NoError(t, e.svc.SyncStatusPage(context.Background(), id))
	require.Empty(t, e.fake.records("status.example.com"))
	st, err := e.svc.StatusPageStatus(e.admin, id)
	require.NoError(t, err)
	require.Equal(t, StatusMissing, st.Status)
	require.Empty(t, st.DesiredRecords)
	require.Nil(t, st.Desired)
}

func TestStatusPageStatusSettingsAndFixNeedTheRightScopes(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	on := true
	e.controlPlane(t, ipA)

	// Without a connection there is nothing to fix, but the status is readable.
	id := e.page(t, "status.example.com", false)
	st, err := e.svc.StatusPageStatus(e.dev, id)
	require.NoError(t, err)
	require.False(t, st.Configured)
	_, err = e.svc.FixStatusPage(e.admin, id, false)
	require.Equal(t, "not_configured", code(t, err))

	e.connect(t)
	st, err = e.svc.StatusPageStatus(e.dev, id)
	require.NoError(t, err)
	require.True(t, st.Configured)
	require.Equal(t, "z1", st.ZoneID)
	require.True(t, st.Manage)

	// Changing needs the admin scope; other teams do not see the page.
	_, err = e.svc.SetStatusPageSettings(e.dev, id, SettingsInput{Proxied: &on})
	require.Equal(t, "forbidden", code(t, err))
	_, err = e.svc.FixStatusPage(e.dev, id, false)
	require.Equal(t, "forbidden", code(t, err))
	_, err = e.svc.StatusPageStatus(e.other, id)
	require.Equal(t, "not_found", code(t, err))
	_, err = e.svc.SetStatusPageSettings(e.other, id, SettingsInput{Proxied: &on})
	require.Equal(t, "not_found", code(t, err))
	_, err = e.svc.StatusPageStatus(e.admin, uuid.New())
	require.Equal(t, "not_found", code(t, err))
	require.True(t, e.pageRow(t, id).CfProxied, "the refused change left the setting alone")

	// A hostname in none of the zones is reported and refuses to be fixed.
	out := e.page(t, "status.elsewhere.org", false)
	st, err = e.svc.StatusPageStatus(e.admin, out)
	require.NoError(t, err)
	require.Equal(t, StatusNotInZone, st.Status)
	_, err = e.svc.FixStatusPage(e.admin, out, false)
	require.Equal(t, "not_in_zone", code(t, err))

	// A page without a hostname has no DNS.
	var none uuid.UUID
	require.NoError(t, e.pool.QueryRow(bg, `INSERT INTO status_pages (workspace_id, title, slug) VALUES ($1, 'None', 'none') RETURNING id`, e.ws).Scan(&none))
	_, err = e.svc.StatusPageStatus(e.admin, none)
	require.Equal(t, "no_hostname", code(t, err))
}

func TestStatusPageSettingsAreAuditedAndFixAdoptsOnRequest(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	e.controlPlane(t, ipA)
	e.fake.put(Record{Type: "A", Name: "status.example.com", Content: "198.51.100.99", Comment: "my own note"})
	id := e.page(t, "status.example.com", false)

	// Sync reports but never writes over a record NebulaCtrl did not create.
	require.NoError(t, e.svc.SyncStatusPage(bg, id))
	require.Empty(t, e.fake.writeLog())
	st, err := e.svc.StatusPageStatus(e.admin, id)
	require.NoError(t, err)
	require.Equal(t, StatusConflict, st.Status)
	require.True(t, st.NeedsAdopt)

	_, err = e.svc.FixStatusPage(e.admin, id, false)
	require.Equal(t, "adoption_required", code(t, err))
	require.Empty(t, e.fake.writeLog())

	st, err = e.svc.FixStatusPage(e.admin, id, true)
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	recs := e.fake.records("status.example.com")
	require.Len(t, recs, 1)
	require.Equal(t, ipA, recs[0].Content)
	require.Equal(t, Marker(id), recs[0].Comment, "adopting marks the record with the page's id")
	require.Equal(t, 1, e.count(t, `SELECT count(*) FROM cloudflare_records WHERE owner_kind = 'status_page' AND owner_id = $1`, id))
	upd := e.audits(t, "dns.record.update")
	require.Len(t, upd, 1)
	require.Contains(t, upd[0].Meta, `"adopted": true`)

	// Settings are audited as status_page.dns.update on the page.
	off := false
	_, err = e.svc.SetStatusPageSettings(e.admin, id, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	require.False(t, e.pageRow(t, id).CfProxied)
	require.Equal(t, 1, e.count(t, `SELECT count(*) FROM audit_events WHERE action = 'status_page.dns.update' AND resource_type = 'status_page' AND resource_id = $1`, id.String()))
}

func TestProxiedStatusPageIsLockedToCloudflareAndTheLockReachesTheRouteTable(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	rec := &pushRecorder{}
	e.svc.SetRoutePusher(rec)
	e.connect(t)
	cp := e.controlPlane(t, ipA)
	id := e.page(t, "status.example.com", true)
	host := "status.example.com"

	table := func() agentprotocol.Route {
		t.Helper()
		routes := []agentprotocol.Route{statusroute.Route("nebulactrl:8081", host, id, "")}
		require.NoError(t, ApplyOriginLock(bg, e.q, cp, routes))
		return routes[0]
	}

	// A new page starts proxied and opted in: its record is proxied and the route is locked.
	require.NoError(t, e.svc.SyncStatusPage(bg, id))
	require.True(t, e.fake.records(host)[0].Proxied)
	require.True(t, table().Domains[0].OriginLocked)
	st, err := e.svc.StatusPageStatus(e.admin, id)
	require.NoError(t, err)
	require.True(t, st.OriginLock.Enabled)

	// DNS only: open.
	on, off := true, false
	st, err = e.svc.SetStatusPageSettings(e.admin, id, SettingsInput{Proxied: &off})
	require.NoError(t, err)
	require.False(t, table().Domains[0].OriginLocked)
	require.False(t, st.OriginLock.Enabled)
	require.Equal(t, LockReasonNotProxied, st.OriginLock.Reason)

	// Proxied: the record is proxied, Authenticated Origin Pulls is on in the zone,
	// the control-plane server is told, and its route table locks the status host.
	rec.reset()
	st, err = e.svc.SetStatusPageSettings(e.admin, id, SettingsInput{Proxied: &on})
	require.NoError(t, err)
	require.Equal(t, StatusManaged, st.Status)
	require.True(t, e.fake.records(host)[0].Proxied)
	require.Equal(t, "on", e.fake.aop["z1"])
	require.Positive(t, rec.count())
	for _, sid := range rec.servers {
		require.Equal(t, cp, sid, "the status routes live on the control-plane server")
	}
	ol := st.OriginLock
	require.True(t, ol.Enabled)
	require.True(t, ol.IPLock)
	require.True(t, ol.RealClientIP)
	require.Equal(t, AOPOn, ol.AOP)
	require.Empty(t, ol.Reason)

	route := table()
	require.True(t, route.Domains[0].OriginLocked, "the status route is locked")
	require.True(t, route.Domains[0].RequireClientCert, "zone AOP is on")
	require.NotNil(t, route.OriginLock)
	require.Equal(t, Builtin().CIDRs(), route.OriginLock.CloudflareCIDRs)
	require.Equal(t, OriginPullCA(), route.OriginLock.ClientCAPEM)

	// A zone-wide change (AOP state, Cloudflare's ranges) re-sends the control plane's table.
	rec.reset()
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.pushZoneInTx(bg, tx, "z1") })
	require.Equal(t, []uuid.UUID{cp}, rec.servers)
	rec.reset()
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.pushZoneInTx(bg, tx, "another-zone") })
	require.Zero(t, rec.count())

	// Not published: no route, so nothing to lock yet.
	e.exec(t, `UPDATE status_pages SET published = false WHERE id = $1`, id)
	require.False(t, table().Domains[0].OriginLocked)
	st, err = e.svc.StatusPageStatus(e.admin, id)
	require.NoError(t, err)
	require.False(t, st.OriginLock.Enabled)
	require.Equal(t, LockReasonNotPublished, st.OriginLock.Reason)
	e.exec(t, `UPDATE status_pages SET published = true WHERE id = $1`, id)
	require.True(t, table().Domains[0].OriginLocked)

	// The operator's opt-out opens it again, audited.
	st, err = e.svc.SetStatusPageSettings(e.admin, id, SettingsInput{OriginLock: &off})
	require.NoError(t, err)
	require.False(t, st.OriginLock.Enabled)
	require.Equal(t, LockReasonOptedOut, st.OriginLock.Reason)
	route = table()
	require.False(t, route.Domains[0].OriginLocked)
	require.Nil(t, route.OriginLock)
	opt := e.audits(t, "status_page.dns.update")
	require.Len(t, opt, 3)
	require.Contains(t, opt[2].Meta, `"originLock": false`)

	// Back to DNS only: unlocked first (the push precedes the record change).
	_, err = e.svc.SetStatusPageSettings(e.admin, id, SettingsInput{OriginLock: &on, Proxied: &off})
	require.NoError(t, err)
	require.False(t, e.fake.records(host)[0].Proxied)
	require.False(t, table().Domains[0].OriginLocked)

	// Proxied again: locked again.
	_, err = e.svc.SetStatusPageSettings(e.admin, id, SettingsInput{Proxied: &on})
	require.NoError(t, err)
	require.True(t, table().Domains[0].OriginLocked)
}

func TestDomainAndStatusPageLocksShareOneTable(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.svc.SetRoutePusher(&pushRecorder{})
	e.connect(t)
	e.deploy(t)
	cp := e.controlPlane(t, ipB)
	id := e.page(t, "status.example.com", true)
	d := e.domain(t, "app.example.com", "")
	on := true
	_, err := e.svc.SetStatusPageSettings(e.admin, id, SettingsInput{Proxied: &on})
	require.NoError(t, err)
	_, err = e.svc.SetSettings(e.dev, d.ID, SettingsInput{Proxied: &on})
	require.NoError(t, err)

	routes := []agentprotocol.Route{
		statusroute.Route("nebulactrl:8081", "status.example.com", id, ""),
		{ServiceID: e.service.ID.String(), Hosts: []string{"app.example.com"}, RouteExt: agentprotocol.RouteExt{Domains: []agentprotocol.DomainRule{{ID: d.ID.String(), Host: "app.example.com"}}}},
	}
	require.NoError(t, ApplyOriginLock(bg, e.q, cp, routes))
	require.True(t, routes[0].Domains[0].OriginLocked)
	require.True(t, routes[1].Domains[0].OriginLocked)
	require.NotNil(t, routes[0].OriginLock)
	require.NotNil(t, routes[1].OriginLock)

	// A status route for another page's id never inherits a domain's lock.
	other := uuid.New()
	routes = []agentprotocol.Route{statusroute.Route("nebulactrl:8081", "status.example.com", other, "")}
	require.NoError(t, ApplyOriginLock(bg, e.q, cp, routes))
	require.False(t, routes[0].Domains[0].OriginLocked, "the rule id must be the page's own")
}

func TestControlPlaneAddressChangeResyncsStatusPages(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.connect(t)
	cp := e.controlPlane(t, ipA)
	id := e.page(t, "status.example.com", false)
	require.NoError(t, e.svc.SyncStatusPage(bg, id))
	e.exec(t, `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'cloudflare_sync_status_page'`)

	// Another server's address changing leaves the pages alone.
	e.inTx(t, func(tx pgx.Tx) error { return e.svc.EnqueueSyncServer(bg, tx, e.ws, e.srvA.ID) })
	require.Zero(t, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_sync_status_page' AND state = 'available'`))

	e.inTx(t, func(tx pgx.Tx) error { return e.svc.EnqueueSyncServer(bg, tx, e.ws, cp) })
	require.Equal(t, 1, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_sync_status_page' AND state = 'available'`))
}

func TestStatusPagesFollowTheConnection(t *testing.T) {
	e := newEnv(t)
	bg := context.Background()
	e.controlPlane(t, ipA)

	// A page that exists before Cloudflare is connected is matched when it is.
	id := e.page(t, "status.example.com", false)
	require.Empty(t, e.pageRow(t, id).CfZoneID)
	e.connect(t)
	p := e.pageRow(t, id)
	require.Equal(t, "z1", p.CfZoneID)
	require.Equal(t, 1, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_sync_status_page'`))
	require.NoError(t, e.svc.SyncStatusPage(bg, id))
	require.Len(t, e.fake.records("status.example.com"), 1)

	// The periodic sweep syncs pages in zones.
	e.exec(t, `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'cloudflare_sync_status_page'`)
	require.NoError(t, e.svc.Sweep(bg))
	require.Equal(t, 1, e.count(t, `SELECT count(*) FROM river_job WHERE kind = 'cloudflare_sync_status_page' AND state = 'available'`))

	// Disconnecting forgets the zone match; the records stay unless removal was asked for.
	require.NoError(t, e.svc.Delete(e.admin, false))
	require.Empty(t, e.pageRow(t, id).CfZoneID)
	require.Len(t, e.fake.records("status.example.com"), 1)
}
