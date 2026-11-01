package deployments

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// webWith inserts a deployed service whose release snapshot (and config) has
// the given outbound setting.
func (f *routeFixture) webWith(slug string, blockEgress bool) db.Service {
	f.t.Helper()
	bg := context.Background()
	cfg := services.Config{Image: "traefik/whoami:latest", Port: 80, BlockEgress: blockEgress}.WithDefaults()
	raw, _ := json.Marshal(cfg)
	sv, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: slug, Slug: slug, Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: cfg, ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceImage, Image: cfg.Image})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: cfg.Image})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: sv.Generation}))
	return sv
}

// stage updates a service's config (generation bump), like PATCH /services/{id}.
func (f *routeFixture) stage(id uuid.UUID, blockEgress bool) {
	f.t.Helper()
	v, err := f.svcs.Get(f.ctx, id)
	require.NoError(f.t, err)
	cfg := v.Config
	_, err = f.svcs.Update(f.ctx, id, v.Generation, services.UpdateInput{Config: &cfg, BlockEgress: &blockEgress})
	require.NoError(f.t, err)
}

func TestRouteNetworksFollowEgressBlocking(t *testing.T) {
	f := newRouteFixture(t)
	env := f.env.ID.String()
	open := f.webWith("site", false)
	blocked := f.webWith("secret", true)
	_, err := f.doms.Create(f.ctx, open.ID, "site.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)
	_, err = f.doms.Create(f.ctx, blocked.ID, "secret.example.com", 0, domains.RuleInput{})
	require.NoError(t, err)

	routes, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	byService := map[string]agentprotocol.Route{}
	for _, r := range routes {
		byService[r.ServiceID] = r
	}
	require.Equal(t, []string{agentprotocol.EnvNetwork(env)}, byService[open.ID.String()].Networks,
		"a normal service is reached over the environment network")
	require.Equal(t, []string{agentprotocol.EnvInternalNetwork(env)}, byService[blocked.ID.String()].Networks,
		"a service that blocks outbound internet is only on the internal network")

	// A route service in front of the blocked service needs the internal network too.
	up := []proxyroutes.UpstreamInput{{ServiceID: &blocked.ID, Port: 80}}
	rv, err := f.routes.Create(f.ctx, f.env.ID, proxyroutes.CreateInput{
		Name: "front", ServerID: f.server.ID, Protocol: "http", Domains: []proxyroutes.DomainInput{{Hostname: "front.example.com"}},
		RouteFields: proxyroutes.RouteFields{Upstreams: &up},
	})
	require.NoError(t, err)
	f.deployRoute(rv.ID)
	routes, err = domains.ServerRoutes(context.Background(), f.q, f.server.ID, uuid.Nil)
	require.NoError(t, err)
	var front *agentprotocol.Route
	for i := range routes {
		if routes[i].ServiceID == rv.ID.String() {
			front = &routes[i]
		}
	}
	require.NotNil(t, front)
	require.Equal(t, []string{agentprotocol.EnvInternalNetwork(env)}, front.Networks)
}

// The deploy payload carries the network membership of the release; it never
// persists anything but ids and aliases.
func TestDeployPayloadCarriesNetworkFields(t *testing.T) {
	f := newRouteFixture(t)
	nets := networks.NewService(f.pool, nil)
	api := f.web("api")

	// A shared network attachment rides the payload with its alias.
	n, err := nets.Create(f.ctx, "Backend", f.server.ID)
	require.NoError(t, err)
	_, err = nets.Attach(f.ctx, api.ID, n.ID)
	require.NoError(t, err)
	firstDep, p := f.deployWeb(api.ID)
	require.False(t, p.BlockEgress)
	require.False(t, p.EnvHasEgressBlocked)
	require.Equal(t, []agentprotocol.SharedNetworkAttachment{
		{Name: agentprotocol.SharedNetworkName(n.ID.String()), Alias: "api.production.shop"},
	}, p.SharedNetworks)

	// Blocking outbound needs the attachment gone first, then the payload says so.
	v, err := f.svcs.Get(f.ctx, api.ID)
	require.NoError(t, err)
	cfg := v.Config
	yes := true
	_, err = f.svcs.Update(f.ctx, api.ID, v.Generation, services.UpdateInput{Config: &cfg, BlockEgress: &yes})
	require.Error(t, err)
	require.NoError(t, nets.Detach(f.ctx, api.ID, n.ID))
	f.stage(api.ID, true)
	_, p = f.deployWeb(api.ID)
	require.True(t, p.BlockEgress)
	require.True(t, p.EnvHasEgressBlocked)
	require.Empty(t, p.SharedNetworks)

	// The release snapshot froze the setting; a sibling service now deploys
	// with EnvHasEgressBlocked so it also joins the internal network.
	worker := f.web("worker")
	_, wp := f.deployWeb(worker.ID)
	require.False(t, wp.BlockEgress)
	require.True(t, wp.EnvHasEgressBlocked)

	// Rolling back to a release that joined a network that no longer exists
	// drops the attachment instead of failing.
	require.NoError(t, nets.Delete(f.ctx, n.ID))
	_, err = f.deps.Rollback(f.ctx, firstDep)
	require.NoError(t, err)
	var rb uuid.UUID
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id FROM deployments WHERE service_id = $1 AND trigger = 'rollback'`, api.ID).Scan(&rb))
	require.NoError(t, f.deps.runDeploy(context.Background(), rb))
	var rp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &rp))
	require.Empty(t, rp.SharedNetworks)
}

// Blocking outbound internet on an app with variables that look like internet
// calls produces a plan warning; the first deployment of a blocked service
// tells the owner the other services of the environment must be redeployed.
func TestPlanWarnsAboutOutboundVariables(t *testing.T) {
	f := newRouteFixture(t)
	f.web("other")
	api := f.web("api")
	sc, err := services.LoadScopeInternal(context.Background(), f.q, api.ID)
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(context.Background(), f.q, f.deps.Box, sc, services.VarWrite{Key: "STRIPE_SECRET_KEY", Value: "sk_test_1", Secret: true})
	require.NoError(t, err)
	f.stage(api.ID, true)

	plan, err := f.deps.Plan(f.ctx, api.ID)
	require.NoError(t, err)
	var hint, redeploy bool
	for _, w := range plan.Warnings {
		hint = hint || (strings.Contains(w, "Outbound internet is blocked") && strings.Contains(w, "STRIPE_SECRET_KEY"))
		redeploy = redeploy || strings.Contains(w, "attached to the internal network automatically")
	}
	require.True(t, hint, "warnings: %v", plan.Warnings)
	require.True(t, redeploy, "warnings: %v", plan.Warnings)

	// Without blocking there is nothing to warn about.
	f.stage(api.ID, false)
	plan, err = f.deps.Plan(f.ctx, api.ID)
	require.NoError(t, err)
	for _, w := range plan.Warnings {
		require.NotContains(t, w, "Outbound internet is blocked")
	}
}

// recResync records the servers whose desired state the control plane pushed.
type recResync struct{ servers []uuid.UUID }

func (r *recResync) Resync(_ context.Context, serverID uuid.UUID) error {
	r.servers = append(r.servers, serverID)
	return nil
}

// A batch deploys web, api and postgres together. Postgres blocks egress, so
// every sibling must join the internal network whichever order the deploys run
// in: the staged config of a service that is not deployed yet already counts.
func TestEnvHasEgressBlockedCountsStagedConfig(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("web")
	pg := f.web("postgres")
	bg := context.Background()
	blocked := func(self uuid.UUID) bool {
		got, err := services.EnvHasEgressBlocked(bg, f.q, f.env.ID, f.server.ID, self, false)
		require.NoError(t, err)
		return got
	}
	require.False(t, blocked(web.ID), "nobody blocks egress yet")

	// Postgres only has the setting staged; its deployed release is open.
	f.stage(pg.ID, true)
	require.True(t, blocked(web.ID), "a staged blocking sibling counts before it deploys")
	require.False(t, blocked(pg.ID), "a service never counts itself")
	_, p := f.deployWeb(web.ID)
	require.True(t, p.EnvHasEgressBlocked, "web deployed first in the batch still joins the internal network")
	require.False(t, p.BlockEgress)

	// A blocking service that was never deployed counts as well.
	raw, _ := json.Marshal(services.Config{Image: "postgres:17", Port: 5432, BlockEgress: true}.WithDefaults())
	_, err := f.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env.ID, Name: "cache", Slug: "cache", Kind: services.KindApp, SourceType: services.SourceImage, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	f.stage(pg.ID, false)
	require.True(t, blocked(web.ID), "an undeployed service with a blocking config counts")

	// A deployed release that blocks keeps counting once the config is unstaged.
	other := f.webWith("vault", true)
	_, err = f.pool.Exec(bg, "UPDATE services SET config = $2::jsonb WHERE id = $1", other.ID, `{"image":"traefik/whoami:latest","port":80}`)
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE services SET deleted_at = now() WHERE slug = 'cache'")
	require.NoError(t, err)
	require.True(t, blocked(web.ID), "the deployed release of a sibling still blocks")
	_, err = f.pool.Exec(bg, "UPDATE services SET deleted_at = now() WHERE id = $1", other.ID)
	require.NoError(t, err)
	require.False(t, blocked(web.ID), "deleted services never count")
}

// Deploying a blocking service (first deploy or a toggle) changes which
// services must sit on the internal network, so the control plane pushes the
// server's desired state; ordinary deployments do not.
func TestDeploymentThatChangesEgressStatePushesDesiredState(t *testing.T) {
	f := newRouteFixture(t)
	rs := &recResync{}
	f.deps.SetResync(rs)
	web := f.web("web")
	pg := f.web("postgres")
	bump := func(id uuid.UUID) {
		_, err := f.pool.Exec(context.Background(), "UPDATE services SET generation = generation + 1 WHERE id = $1", id)
		require.NoError(t, err)
	}

	// A sibling deploy that blocks nothing pushes nothing.
	bump(web.ID)
	f.deployWeb(web.ID)
	require.Empty(t, rs.servers)

	// The blocking service's first deploy: running siblings must be attached.
	f.stage(pg.ID, true)
	f.deployWeb(pg.ID)
	require.Equal(t, []uuid.UUID{f.server.ID}, rs.servers)

	// Redeploying it unchanged, or a sibling, pushes nothing more.
	bump(pg.ID)
	f.deployWeb(pg.ID)
	bump(web.ID)
	f.deployWeb(web.ID)
	require.Len(t, rs.servers, 1)

	// Toggling the setting off is a change too.
	f.stage(pg.ID, false)
	f.deployWeb(pg.ID)
	require.Equal(t, []uuid.UUID{f.server.ID, f.server.ID}, rs.servers)
}
