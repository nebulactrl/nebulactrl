package domains

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// runTx runs fn in one transaction on the service's scope and then commits or
// rolls it back.
func (f *wwwFixture) runTx(commit bool, fn func(tx pgx.Tx, sc services.Scope)) {
	f.t.Helper()
	tx, err := f.svc.pool.Begin(f.ctx)
	require.NoError(f.t, err)
	sc, err := services.LoadScope(f.ctx, db.New(tx), policy.ScopeProjectsWrite, f.svcA.ID)
	require.NoError(f.t, err)
	fn(tx, sc)
	if commit {
		require.NoError(f.t, tx.Commit(f.ctx))
		return
	}
	require.NoError(f.t, tx.Rollback(f.ctx))
}

func (f *wwwFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.svc.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (f *wwwFixture) auditCount(action string, resourceID uuid.UUID) int {
	f.t.Helper()
	return f.count("SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2", action, resourceID.String())
}

func (f *wwwFixture) auditTotal(action string) int {
	f.t.Helper()
	return f.count("SELECT count(*) FROM audit_events WHERE action = $1", action)
}

func (f *wwwFixture) dnsJobs() int {
	f.t.Helper()
	return f.count("SELECT count(*) FROM river_job WHERE kind = 'domain_dns_check'")
}

func TestCreateTxRolledBackLeavesNoTrace(t *testing.T) {
	f := newWWWFixture(t)
	gen, jobsBefore, auditBefore := f.gen(), f.dnsJobs(), f.auditTotal("domain.add")

	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		v, after, err := f.svc.CreateTx(f.ctx, tx, sc, "Gone.Example.com", 0, "", RuleInput{WWWRedirect: new(true)})
		require.NoError(t, err)
		require.NotNil(t, after)
		require.Equal(t, "gone.example.com", v.Hostname)
		require.True(t, v.WWWRedirect)
		// Inside the transaction the rows are visible.
		_, ok, err := f.svc.FindTx(f.ctx, tx, f.svcA.ID, "gone.example.com", "")
		require.NoError(t, err)
		require.True(t, ok)
	})

	require.Empty(t, f.rows())
	require.Equal(t, gen, f.gen())
	require.Equal(t, jobsBefore, f.dnsJobs(), "no DNS check job survives the rollback")
	require.Equal(t, auditBefore, f.auditTotal("domain.add"))
}

func TestCreateTxMatchesCreate(t *testing.T) {
	f := newWWWFixture(t)
	in := func() RuleInput {
		return RuleInput{
			PathPrefix: new("/api"), StripPrefix: new(true), AddPrefix: new("/v1"),
			Options: &agentprotocol.RouteOptions{Compress: true, SecurityHeaders: "basic"},
		}
	}

	gen := f.gen()
	want, err := f.svc.Create(f.ctx, f.svcA.ID, "plain.example.com", 0, in())
	require.NoError(t, err)
	require.Equal(t, gen+1, f.gen())

	jobsBefore := f.dnsJobs()
	var got services.DomainView
	var after func(context.Context)
	f.runTx(true, func(tx pgx.Tx, sc services.Scope) {
		got, after, err = f.svc.CreateTx(f.ctx, tx, sc, "tx.example.com", 0, "", in())
		require.NoError(t, err)
	})
	require.NotNil(t, after)
	after(f.ctx)
	require.Equal(t, gen+2, f.gen(), "one generation bump per created domain")
	require.Equal(t, jobsBefore+1, f.dnsJobs())
	require.Equal(t, 1, f.auditCount("domain.add", want.ID))
	require.Equal(t, 1, f.auditCount("domain.add", got.ID))

	rows := f.rows()
	a, b := rows["plain.example.com"], rows["tx.example.com"]
	require.Equal(t, a.ServiceID, b.ServiceID)
	require.Equal(t, a.TargetPort, b.TargetPort)
	require.Equal(t, a.Generated, b.Generated)
	require.Equal(t, a.Kind, b.Kind)
	require.Equal(t, a.PathPrefix, b.PathPrefix)
	require.Equal(t, a.StripPrefix, b.StripPrefix)
	require.Equal(t, a.AddPrefix, b.AddPrefix)
	require.Equal(t, a.RedirectCode, b.RedirectCode)
	require.Equal(t, a.PreservePath, b.PreservePath)
	require.Equal(t, a.TlsMode, b.TlsMode)
	require.Equal(t, a.Component, b.Component)
	require.Equal(t, services.DomainOptions(a.Options), services.DomainOptions(b.Options))
	require.Equal(t, "/api", b.PathPrefix)
	require.True(t, services.DomainOptions(b.Options).Compress)
	require.Equal(t, want.TargetPort, got.TargetPort)
	require.Equal(t, want.Options, got.Options)
}

func TestCreateTxDuplicateIsDomainConflictAndKeepsTxUsable(t *testing.T) {
	f := newWWWFixture(t)
	_, err := f.svc.Create(f.ctx, f.svcA.ID, "taken.example.com", 0, RuleInput{})
	require.NoError(t, err)
	gen := f.gen()

	f.runTx(true, func(tx pgx.Tx, sc services.Scope) {
		_, _, err := f.svc.CreateTx(f.ctx, tx, sc, "taken.example.com", 0, "", RuleInput{})
		requireCode(t, err, "domain_conflict")

		// The failed insert did not poison the transaction.
		_, after, err := f.svc.CreateTx(f.ctx, tx, sc, "free.example.com", 0, "", RuleInput{})
		require.NoError(t, err)
		require.NotNil(t, after)

		// The same hostname under another path prefix is a different domain.
		_, _, err = f.svc.CreateTx(f.ctx, tx, sc, "taken.example.com", 0, "", RuleInput{PathPrefix: new("/api")})
		require.NoError(t, err)

		// A duplicate of a row created earlier in the same transaction conflicts too.
		_, _, err = f.svc.CreateTx(f.ctx, tx, sc, "free.example.com", 0, "", RuleInput{})
		requireCode(t, err, "domain_conflict")
	})

	require.Equal(t, gen+2, f.gen(), "only the two successful creates bumped the generation")
	require.Len(t, f.rows(), 2, "taken.example.com (one row per host in the map) and free.example.com")
	require.Equal(t, 3, f.count("SELECT count(*) FROM domains WHERE service_id = $1", f.svcA.ID))
}

func TestCreateTxValidationMatchesCreate(t *testing.T) {
	f := newWWWFixture(t)
	bad := []struct {
		name string
		host string
		in   RuleInput
		code string
	}{
		{"bad hostname", "not a host", RuleInput{}, "invalid_hostname"},
		{"strip without prefix", "a.example.com", RuleInput{StripPrefix: new(true)}, "invalid_path_prefix"},
		{"redirect without target", "b.example.com", RuleInput{Kind: new(agentprotocol.DomainRedirect)}, "invalid_redirect"},
		{"www on a path domain", "c.example.com", RuleInput{PathPrefix: new("/x"), WWWRedirect: new(true)}, "invalid_www_redirect"},
	}
	for _, c := range bad {
		_, err := f.svc.Create(f.ctx, f.svcA.ID, c.host, 0, c.in)
		requireCode(t, err, c.code)
		f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
			_, after, err := f.svc.CreateTx(f.ctx, tx, sc, c.host, 0, "", c.in)
			requireCode(t, err, c.code)
			require.NotNil(t, after, c.name+": the after-commit func is never nil")
		})
	}
	// Wrong target port.
	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		_, _, err := f.svc.CreateTx(f.ctx, tx, sc, "p.example.com", 8080, "", RuleInput{})
		requireCode(t, err, "invalid_target_port")
		_, _, err = f.svc.CreateTx(f.ctx, tx, sc, "p.example.com", 0, "web", RuleInput{})
		requireCode(t, err, "invalid_process")
	})
	require.Empty(t, f.rows())
}

func TestUpdateTxPartialInputChangesOnlyDeclaredFields(t *testing.T) {
	f := newWWWFixture(t)
	opts := agentprotocol.RouteOptions{Compress: true, SecurityHeaders: "strict"}
	made, err := f.svc.Create(f.ctx, f.svcA.ID, "app.example.com", 0, RuleInput{Options: &opts, AddPrefix: new("/v1")})
	require.NoError(t, err)
	gen := f.gen()

	var upd services.DomainView
	var after func(context.Context)
	f.runTx(true, func(tx pgx.Tx, sc services.Scope) {
		d, ok, err := f.svc.FindTx(f.ctx, tx, f.svcA.ID, "app.example.com", "")
		require.NoError(t, err)
		require.True(t, ok)
		upd, after, err = f.svc.UpdateTx(f.ctx, tx, d.ID, UpdateInput{Rule: RuleInput{AddPrefix: new("/v2")}})
		require.NoError(t, err)
	})
	require.NotNil(t, after)
	after(f.ctx)

	require.Equal(t, made.ID, upd.ID)
	require.Equal(t, "/v2", upd.AddPrefix)
	require.Equal(t, opts, upd.Options, "options were not declared, so they are untouched")
	require.Equal(t, gen+1, f.gen())
	require.Equal(t, 1, f.auditCount("domain.update", made.ID))
	row := f.rows()["app.example.com"]
	require.Equal(t, "/v2", row.AddPrefix)
	require.Equal(t, opts, services.DomainOptions(row.Options))
	require.Equal(t, agentprotocol.TLSAuto, row.TlsMode)
	require.True(t, row.PreservePath)

	// A declared options set replaces the whole set.
	f.runTx(true, func(tx pgx.Tx, sc services.Scope) {
		_, _, err := f.svc.UpdateTx(f.ctx, tx, made.ID, UpdateInput{Rule: RuleInput{Options: &agentprotocol.RouteOptions{Noindex: true}}})
		require.NoError(t, err)
	})
	row = f.rows()["app.example.com"]
	require.Equal(t, agentprotocol.RouteOptions{Noindex: true}, services.DomainOptions(row.Options))
	require.Equal(t, "/v2", row.AddPrefix, "addPrefix was not declared this time")
}

func TestUpdateTxRenameResetsDNSAndQueuesCheck(t *testing.T) {
	f := newWWWFixture(t)
	made, err := f.svc.Create(f.ctx, f.svcA.ID, "old.example.com", 0, RuleInput{})
	require.NoError(t, err)
	jobsBefore := f.dnsJobs()
	f.runTx(true, func(tx pgx.Tx, sc services.Scope) {
		v, _, err := f.svc.UpdateTx(f.ctx, tx, made.ID, UpdateInput{Hostname: new("New.Example.com")})
		require.NoError(t, err)
		require.Equal(t, "new.example.com", v.Hostname)
	})
	require.Contains(t, f.rows(), "new.example.com")
	require.NotContains(t, f.rows(), "old.example.com")
	require.Equal(t, jobsBefore+1, f.dnsJobs())
}

func TestUpdateTxNoopDoesNotBumpGeneration(t *testing.T) {
	f := newWWWFixture(t)
	made, err := f.svc.Create(f.ctx, f.svcA.ID, "same.example.com", 0, RuleInput{AddPrefix: new("/v1")})
	require.NoError(t, err)
	gen, jobs := f.gen(), f.dnsJobs()

	f.runTx(true, func(tx pgx.Tx, sc services.Scope) {
		for _, in := range []UpdateInput{
			{},
			{Hostname: new("same.example.com"), Rule: RuleInput{AddPrefix: new("/v1"), TLSMode: new(agentprotocol.TLSAuto)}},
		} {
			v, after, err := f.svc.UpdateTx(f.ctx, tx, made.ID, in)
			require.NoError(t, err)
			require.NotNil(t, after)
			require.Equal(t, made.ID, v.ID)
			require.Equal(t, "/v1", v.AddPrefix)
		}
	})
	require.Equal(t, gen, f.gen())
	require.Equal(t, jobs, f.dnsJobs())
	require.Zero(t, f.auditCount("domain.update", made.ID))
}

func TestUpdateTxValidationMatchesUpdate(t *testing.T) {
	f := newWWWFixture(t)
	made, err := f.svc.Create(f.ctx, f.svcA.ID, "v.example.com", 0, RuleInput{})
	require.NoError(t, err)
	gen := f.gen()

	in := UpdateInput{Rule: RuleInput{StripPrefix: new(true)}}
	_, _, err = f.svc.Update(f.ctx, made.ID, gen, in)
	requireCode(t, err, "invalid_path_prefix")
	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		_, _, err := f.svc.UpdateTx(f.ctx, tx, made.ID, in)
		requireCode(t, err, "invalid_path_prefix")

		_, _, err = f.svc.UpdateTx(f.ctx, tx, uuid.New(), UpdateInput{})
		requireCode(t, err, "not_found")
	})
	require.Equal(t, gen, f.gen())
}

func TestUpdateTxToExistingKeyIsDomainConflictAndKeepsTxUsable(t *testing.T) {
	f := newWWWFixture(t)
	_, err := f.svc.Create(f.ctx, f.svcA.ID, "one.example.com", 0, RuleInput{})
	require.NoError(t, err)
	two, err := f.svc.Create(f.ctx, f.svcA.ID, "two.example.com", 0, RuleInput{})
	require.NoError(t, err)

	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		_, _, err := f.svc.UpdateTx(f.ctx, tx, two.ID, UpdateInput{Hostname: new("one.example.com")})
		requireCode(t, err, "domain_conflict")
		_, ok, err := f.svc.FindTx(f.ctx, tx, f.svcA.ID, "two.example.com", "")
		require.NoError(t, err)
		require.True(t, ok, "the transaction is still usable and the row unchanged")
	})
}

func TestDeleteTxRemovesWWWCompanion(t *testing.T) {
	f := newWWWFixture(t)
	on := true
	made, err := f.svc.Create(f.ctx, f.svcA.ID, "shop.example.com", 0, RuleInput{WWWRedirect: &on})
	require.NoError(t, err)
	require.Len(t, f.rows(), 2)
	gen := f.gen()

	// Rolled back: both rows stay.
	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		after, err := f.svc.DeleteTx(f.ctx, tx, sc, made.ID)
		require.NoError(t, err)
		require.NotNil(t, after)
	})
	require.Len(t, f.rows(), 2)
	require.Equal(t, gen, f.gen())
	require.Zero(t, f.auditCount("domain.remove", made.ID))

	var after func(context.Context)
	f.runTx(true, func(tx pgx.Tx, sc services.Scope) {
		var err error
		after, err = f.svc.DeleteTx(f.ctx, tx, sc, made.ID)
		require.NoError(t, err)
	})
	after(f.ctx)
	require.Empty(t, f.rows(), "the www redirect goes with its domain")
	require.Equal(t, gen+1, f.gen())
	require.Equal(t, 1, f.auditCount("domain.remove", made.ID))
	var meta string
	require.NoError(t, f.svc.pool.QueryRow(context.Background(),
		"SELECT metadata->>'wwwRedirectRemoved' FROM audit_events WHERE action = 'domain.remove' AND resource_id = $1", made.ID.String()).Scan(&meta))
	require.Equal(t, "www.shop.example.com", meta)
}

func TestDeleteTxIgnoresDomainsOfOtherServices(t *testing.T) {
	f := newWWWFixture(t)
	other, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.svcA.EnvironmentID, Name: "other", Slug: "other", Kind: services.KindApp, SourceType: services.SourceImage,
		Config: f.svcA.Config, ServerID: f.svcA.ServerID,
	})
	require.NoError(t, err)
	otherDom, err := f.svc.Create(f.ctx, other.ID, "other.example.com", 0, RuleInput{})
	require.NoError(t, err)

	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		_, err := f.svc.DeleteTx(f.ctx, tx, sc, otherDom.ID)
		requireCode(t, err, "not_found")
		_, ok, err := f.svc.FindTx(f.ctx, tx, f.svcA.ID, "other.example.com", "")
		require.NoError(t, err)
		require.False(t, ok)
		d, ok, err := f.svc.FindTx(f.ctx, tx, other.ID, "OTHER.example.com", "")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, otherDom.ID, d.ID)
	})
}

func TestFindTxKeysOnHostAndPathPrefix(t *testing.T) {
	f := newWWWFixture(t)
	root, err := f.svc.Create(f.ctx, f.svcA.ID, "k.example.com", 0, RuleInput{})
	require.NoError(t, err)
	api, err := f.svc.Create(f.ctx, f.svcA.ID, "k.example.com", 0, RuleInput{PathPrefix: new("/api")})
	require.NoError(t, err)

	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		d, ok, err := f.svc.FindTx(f.ctx, tx, f.svcA.ID, "k.example.com", "")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, root.ID, d.ID)
		d, ok, err = f.svc.FindTx(f.ctx, tx, f.svcA.ID, "K.Example.com", "/api")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, api.ID, d.ID)
		_, ok, err = f.svc.FindTx(f.ctx, tx, f.svcA.ID, "k.example.com", "/nope")
		require.NoError(t, err)
		require.False(t, ok)
		_, _, err = f.svc.FindTx(f.ctx, tx, f.svcA.ID, "no host", "")
		requireCode(t, err, "invalid_hostname")
	})
}

func TestGenerateTxPicksFreeCandidate(t *testing.T) {
	f := newWWWFixture(t)
	const base = "site-production-shop.203.0.113.9.sslip.io"
	candidate := func(i int) string { return candidateHost(base, i) }
	require.Equal(t, "site-production-shop-2.203.0.113.9.sslip.io", candidate(2))

	// Nothing taken: the plain generated hostname, like Generate.
	gen := f.gen()
	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		v, after, err := f.svc.GenerateTx(f.ctx, tx, sc, "")
		require.NoError(t, err)
		require.NotNil(t, after)
		require.Equal(t, base, v.Hostname)
		require.True(t, v.Generated)
	})
	require.Empty(t, f.rows())
	require.Equal(t, gen, f.gen())

	// The first two candidates are taken (by another service): the third is used.
	other, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.svcA.EnvironmentID, Name: "other", Slug: "other", Kind: services.KindApp, SourceType: services.SourceImage,
		Config: f.svcA.Config, ServerID: f.svcA.ServerID,
	})
	require.NoError(t, err)
	for i := 1; i <= 2; i++ {
		_, err := f.svc.Create(f.ctx, other.ID, candidate(i), 0, RuleInput{})
		require.NoError(t, err)
	}
	gen = f.gen()
	var got services.DomainView
	f.runTx(true, func(tx pgx.Tx, sc services.Scope) {
		var err error
		got, _, err = f.svc.GenerateTx(f.ctx, tx, sc, "")
		require.NoError(t, err)
	})
	require.Equal(t, candidate(3), got.Hostname)
	require.True(t, got.Generated)
	require.Equal(t, gen+1, f.gen())
	require.Equal(t, 1, f.auditCount("domain.add", got.ID))

	// Every plain candidate taken: the team's own candidate is used, so another
	// team squatting the plain names cannot leave the service without a hostname.
	for i := 4; i <= 5; i++ {
		_, err := f.svc.Create(f.ctx, other.ID, candidate(i), 0, RuleInput{})
		require.NoError(t, err)
	}
	require.Equal(t, 5, f.count("SELECT count(*) FROM domains WHERE hostname LIKE 'site-production-shop%'"))
	f.runTx(false, func(tx pgx.Tx, sc services.Scope) {
		v, _, err := f.svc.GenerateTx(f.ctx, tx, sc, "")
		require.NoError(t, err)
		require.Equal(t, teamCandidate(base, sc.WorkspaceID), v.Hostname)
		require.True(t, v.Generated)
		_, ok, err := f.svc.FindTx(f.ctx, tx, f.svcA.ID, candidate(3), "")
		require.NoError(t, err)
		require.True(t, ok)
	})
}

// A hostname inside a Cloudflare zone another team connected is not available to
// this team, and the refusal reads the same as any other taken name.
func TestHostnamesInAnotherTeamsZoneAreUnavailable(t *testing.T) {
	f := newWWWFixture(t)
	bg := context.Background()
	rival, err := f.q.CreateWorkspace(bg, "rival")
	require.NoError(t, err)
	claim := func(ws uuid.UUID, zones string) {
		_, err := f.q.UpsertCloudflareConnection(bg, db.UpsertCloudflareConnectionParams{
			WorkspaceID: ws, TokenEnc: []byte("sealed"), Zones: json.RawMessage(zones),
		})
		require.NoError(t, err)
	}
	claim(rival.ID, `[{"id":"z1","name":"claimed.example","status":"active"},{"id":"z2","name":"pending.example","status":"pending"}]`)

	_, err = f.svc.Create(f.ctx, f.svcA.ID, "app.claimed.example", 0, RuleInput{})
	requireCode(t, err, "domain_conflict")
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "claimed.example", 0, RuleInput{})
	requireCode(t, err, "domain_conflict")
	ae, _ := apierr.As(err)
	require.Equal(t, "the hostname claimed.example is not available", ae.Message)

	_, err = f.svc.Create(f.ctx, f.svcA.ID, "taken.claimed.example", 0, RuleInput{PathPrefix: new("/x")})
	requireCode(t, err, "domain_conflict")

	// A pending zone proves nothing: Cloudflare accepts any domain until its name servers are delegated.
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "app.pending.example", 0, RuleInput{})
	require.NoError(t, err)
	// Names outside every zone stay first come, first served.
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "app.elsewhere.example", 0, RuleInput{})
	require.NoError(t, err)

	// An existing domain whose name a team later claims stays editable, but cannot be renamed into the claim.
	d, err := f.svc.Create(f.ctx, f.svcA.ID, "legacy.later.example", 0, RuleInput{})
	require.NoError(t, err)
	claim(rival.ID, `[{"id":"z1","name":"claimed.example","status":"active"},{"id":"z3","name":"later.example","status":"active"}]`)
	_, _, err = f.svc.Update(f.ctx, d.ID, f.gen(), UpdateInput{Rule: RuleInput{PathPrefix: new("/api")}})
	require.NoError(t, err)
	_, _, err = f.svc.Update(f.ctx, d.ID, f.gen(), UpdateInput{Hostname: new("other.later.example")})
	requireCode(t, err, "domain_conflict")

	// The owner of the zone can use it.
	var own services.Scope
	f.runTx(false, func(_ pgx.Tx, sc services.Scope) { own = sc })
	claim(own.WorkspaceID, `[{"id":"z9","name":"mine.example","status":"active"}]`)
	_, err = f.svc.Create(f.ctx, f.svcA.ID, "app.mine.example", 0, RuleInput{})
	require.NoError(t, err)
}
