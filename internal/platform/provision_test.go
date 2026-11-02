package platform_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/platform"
)

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func TestProvisionRefusesWhatCannotWork(t *testing.T) {
	f := newFixture(t)
	in := func(mod func(*platform.ProvisionInput)) error {
		p := platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID}
		mod(&p)
		_, err := f.svc.Provision(f.ctx, p)
		return err
	}

	require.Equal(t, "validation_failed", apiCode(in(func(p *platform.ProvisionInput) { p.ServerID = uuid.Nil })))
	require.Equal(t, http.StatusUnprocessableEntity, apiStatus(in(func(p *platform.ProvisionInput) { p.ServerID = uuid.Nil })))

	// A server of another team does not exist for this one.
	ws2, err := f.q.CreateWorkspace(context.Background(), "other")
	require.NoError(t, err)
	foreign, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: ws2.ID, Name: "foreign"})
	require.NoError(t, err)
	require.Equal(t, "server_not_found", apiCode(in(func(p *platform.ProvisionInput) { p.ServerID = foreign.ID })))

	// A proxy that is down could not serve the new hostname.
	f.exec(`UPDATE servers SET proxy_status = 'error' WHERE id = $1`, f.server.ID)
	require.Equal(t, "proxy_unavailable", apiCode(in(func(p *platform.ProvisionInput) {})))
	f.exec(`UPDATE servers SET proxy_status = NULL WHERE id = $1`, f.server.ID)

	// Neither a base domain nor an address to build a hostname from.
	f.exec(`UPDATE servers SET base_domain = NULL, public_ip = NULL WHERE id = $1`, f.server.ID)
	require.Equal(t, "no_base_domain", apiCode(in(func(p *platform.ProvisionInput) {})))
	// ... a typed hostname is the way out.
	r, err := f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID, Hostname: "  Hs.Example.Test "})
	require.NoError(t, err)
	require.Equal(t, "hs.example.test", r.View.Hostname)
	require.Equal(t, 0, f.count(`SELECT count(*) FROM domains WHERE generated`), "a typed hostname is not a generated one")

	// One of each kind per team.
	require.Equal(t, "platform_service_exists", apiCode(in(func(p *platform.ProvisionInput) { p.Hostname = "other.example.test" })))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM platform_services`))
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func TestProvisionGeneratesAHostnameUnderTheBaseDomainAndSkipsTakenOnes(t *testing.T) {
	f := newFixture(t)
	r := f.provision()
	require.True(t, strings.HasSuffix(r.View.Hostname, ".example.test"), r.View.Hostname)
	require.Equal(t, "https://"+r.View.Hostname, r.View.PublicURL)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM domains WHERE generated AND hostname = $1`, r.View.Hostname))
	first := r.View.Hostname

	// Remove it, take its name with another service, and the next one gets a numbered name.
	require.NoError(t, f.svc.Remove(f.ctx, r.View.ID, platform.RemoveInput{Confirm: platform.KindHeadscale, DeleteData: true}))
	f.holdHostname(first)
	r2 := f.provision()
	label, rest, _ := strings.Cut(first, ".")
	require.Equal(t, label+"-2."+rest, r2.View.Hostname)
}

// holdHostname makes an ordinary service of the team serve host.
func (f *fixture) holdHostname(host string) {
	f.t.Helper()
	bg := context.Background()
	prj, err := f.q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Site", Slug: "site"})
	require.NoError(f.t, err)
	env, err := f.q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: "web", Slug: "web", Kind: "app", SourceType: "image", Config: []byte(`{"image":"nginx"}`), ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	_, err = f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: sv.ID, Hostname: host, TargetPort: 80, TlsMode: "none"})
	require.NoError(f.t, err)
}

func TestPublicURLOverrideMustNameTheServicesHostname(t *testing.T) {
	f := newFixture(t)
	for name, override := range map[string]string{
		"not http":     "ftp://hs.example.test",
		"credentials":  "https://user:pw@hs.example.test",
		"a path":       "https://hs.example.test/admin",
		"a query":      "https://hs.example.test?x=1",
		"a fragment":   "https://hs.example.test#x",
		"another host": "https://elsewhere.example.test",
		"not a url":    "::::",
		"no scheme":    "hs.example.test",
	} {
		_, err := f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID, Hostname: "hs.example.test", PublicURL: override})
		require.Equal(t, "validation_failed", apiCode(err), name)
	}
	require.Zero(t, f.count(`SELECT count(*) FROM platform_services`))

	// A port or plain http is allowed for the same host; the proxy then serves plain http.
	r, err := f.svc.Provision(f.ctx, platform.ProvisionInput{Kind: platform.KindHeadscale, ServerID: f.server.ID, Hostname: "hs.example.test", PublicURL: "http://HS.example.test:8080/"})
	require.NoError(t, err)
	require.Equal(t, "http://HS.example.test:8080", r.View.PublicURL)
	var forceHTTPS string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT options->>'forceHttps' FROM domains WHERE hostname = 'hs.example.test'`).Scan(&forceHTTPS))
	require.Equal(t, "false", forceHTTPS, "no redirect to https for a plain-http public URL")
}

func TestServiceStateOfAPlatformServiceFollowsWhatTheAgentObserved(t *testing.T) {
	f := newFixture(t)
	r := f.provision()
	svc := f.platformService(r)
	state := func() (string, string) {
		v, err := f.svc.Get(f.ctx, r.View.ID)
		require.NoError(t, err)
		require.Len(t, v.Services, 1)
		return v.Services[0].State, v.Services[0].Health
	}
	observe := func(js string) {
		f.exec(`UPDATE services SET observed = $2::jsonb WHERE id = $1`, svc.ID, js)
	}

	st, _ := state()
	require.Equal(t, "not_deployed", st, "nothing observed and nothing deployed")
	observe(`{"state":"running","health":"healthy"}`)
	f.exec(`UPDATE services SET generation = applied_generation + 1 WHERE id = $1`, svc.ID)
	st, health := state()
	require.Equal(t, "staged", st, "running, but changes wait for a deployment")
	require.Equal(t, "healthy", health)
	f.exec(`UPDATE services SET applied_generation = generation WHERE id = $1`, svc.ID)
	st, _ = state()
	require.Equal(t, "running", st)
	observe(`{"state":"running","health":"unhealthy"}`)
	st, health = state()
	require.Equal(t, "starting", st)
	require.Equal(t, "unhealthy", health)
	for _, s := range []string{"exited", "stopped"} {
		observe(`{"state":"` + s + `"}`)
		st, _ = state()
		require.Equal(t, "stopped", st, s)
	}
	observe(`{"state":"created"}`)
	st, _ = state()
	require.Equal(t, "not_deployed", st, "an unknown container state of a service without a deployment")

	// A service deleted behind the page's back is reported, with a hint once the service is ready.
	f.makeReady(r.View.ID)
	f.exec(`UPDATE services SET deleted_at = now() WHERE id = $1`, svc.ID)
	v, err := f.svc.Get(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.Equal(t, "missing", v.Services[0].State)
	require.Equal(t, "(removed)", v.Services[0].Name)
	require.Len(t, v.Warnings, 1)
}

func TestBackupHintsFollowTheDestination(t *testing.T) {
	f := newFixture(t)
	r := f.provision()
	f.makeReady(r.View.ID)
	v, err := f.svc.Get(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.NotNil(t, v.Backup)
	require.False(t, v.Backup.Configured)
	require.Contains(t, v.Backup.Hint, "Not backed up")

	dest := uuid.New()
	f.exec(`UPDATE platform_services SET wiring = jsonb_build_object('type', 'tailscale_integration', 'id', 'tailscale', 'name', 'Tailscale integration', 'backupDestinationId', $2::text) WHERE id = $1`, r.View.ID, dest.String())
	f.backups.mu.Lock()
	f.backups.existing = []backups.DestinationView{{ID: dest, Name: "offsite"}}
	f.backups.mu.Unlock()
	v, err = f.svc.Get(f.ctx, r.View.ID)
	require.NoError(t, err)
	require.True(t, v.Backup.Configured)
	require.Equal(t, "offsite", v.Backup.DestinationName)
	require.Contains(t, v.Backup.Hint, "daily at 03:17 UTC")
	require.NotNil(t, v.Wiring)
	require.Equal(t, "Tailscale integration", v.Wiring.Name)
}
