package services_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func TestAutoscalingValidationBounds(t *testing.T) {
	ok := services.Autoscaling{Enabled: true, Min: 1, Max: 5, TargetCPUPercent: 70}
	require.NoError(t, ok.Validate())

	for name, a := range map[string]services.Autoscaling{
		"min below range": {Min: 0, Max: 5, TargetCPUPercent: 70},
		"max above range": {Min: 1, Max: 11, TargetCPUPercent: 70},
		"min above max":   {Min: 6, Max: 3, TargetCPUPercent: 70},
		"target too low":  {Min: 1, Max: 5, TargetCPUPercent: 19},
		"target too high": {Min: 1, Max: 5, TargetCPUPercent: 96},
	} {
		require.Error(t, a.Validate(), name)
	}
	require.ErrorContains(t, services.Autoscaling{Min: 6, Max: 3, TargetCPUPercent: 70}.Validate(), "min (6) must not exceed max (3)")
}

func TestRouteDescribeReadsLikeTheRoute(t *testing.T) {
	id := uuid.New()
	var nilRoute *services.RouteConfig
	require.Empty(t, nilRoute.Describe())
	require.Equal(t, "tcp :25565 → service:25565", (&services.RouteConfig{Protocol: "tcp", ListenPort: 25565, Upstreams: []services.RouteUpstream{{ServiceID: &id, Port: 25565}}}).Describe())
	require.Equal(t, "http → 10.0.0.5:8080, 10.0.0.6:8080", (&services.RouteConfig{Protocol: "http", Upstreams: []services.RouteUpstream{{Address: "10.0.0.5:8080"}, {Address: "10.0.0.6:8080"}}}).Describe(), "no listen port, external addresses")
}

func TestHealthcheckSpecCarriesEveryField(t *testing.T) {
	h := services.Healthcheck{
		Type: services.HealthHTTP, Path: "/up", Port: 8080, IntervalSeconds: 5, TimeoutSeconds: 2, StartPeriodSeconds: 10, DeadlineSeconds: 60, SuccessThreshold: 3,
		Command: []string{"curl", "-f", "localhost"},
	}
	spec := h.Spec()
	require.Equal(t, services.HealthHTTP, spec.Type)
	require.Equal(t, "/up", spec.Path)
	require.Equal(t, 8080, spec.Port)
	require.Equal(t, 5, spec.IntervalSeconds)
	require.Equal(t, 2, spec.TimeoutSeconds)
	require.Equal(t, 10, spec.StartPeriodSeconds)
	require.Equal(t, 60, spec.DeadlineSeconds)
	require.Equal(t, 3, spec.SuccessThreshold)
	require.Equal(t, []string{"curl", "-f", "localhost"}, spec.Command)
}

func TestDeployEdgesMergeReferencesManualLinksAndRouteUpstreams(t *testing.T) {
	f := newOpsFixture(t)
	bg := context.Background()
	api := f.imageService(f.prod.ID, "api")
	pg := f.imageService(f.prod.ID, "pg")
	cache := f.imageService(f.prod.ID, "cache")
	_, err := services.WriteServiceVariable(f.ctx, f.q, f.box(), f.varScope(api.ID), services.VarWrite{Key: "DB", Value: "${{pg.NEBULA_PRIVATE_HOST}}"})
	require.NoError(t, err)
	// A manual link that duplicates a reference edge, one that is new, and a self link.
	for _, l := range [][2]uuid.UUID{{api.ID, pg.ID}, {api.ID, cache.ID}, {cache.ID, cache.ID}} {
		_, err = f.pool.Exec(bg, `INSERT INTO service_links (environment_id, from_service_id, to_service_id) VALUES ($1, $2, $3)`, f.prod.ID, l[0], l[1])
		require.NoError(t, err)
	}

	edges, err := services.DeployEdges(bg, f.q, f.box(), f.prod.ID)
	require.NoError(t, err)
	pairs := map[[2]uuid.UUID]int{}
	for _, e := range edges {
		pairs[[2]uuid.UUID{e.From, e.To}]++
	}
	require.Equal(t, map[[2]uuid.UUID]int{{api.ID, pg.ID}: 1, {api.ID, cache.ID}: 1}, pairs,
		"the reference and the manual link on the same pair count once; a self link is no dependency")
}

func TestScopeGuardOnlyAsksForGuardedPrincipalsInProduction(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.imageService(f.prod.ID, "web")
	sc := f.varScope(sv.ID)

	// A person's session is never guarded, in production or not.
	require.NoError(t, sc.Guard(f.ctx, "delete_service", "Delete web", true, []string{"gone"}))
	sc.IsProduction = true
	require.NoError(t, sc.Guard(f.ctx, "delete_service", "Delete web", true, []string{"gone"}))

	ps := sc.ProdScope("Delete web", true, "effect one", "effect two")
	require.Equal(t, f.ws.ID, ps.WorkspaceID)
	require.Equal(t, sc.ProjectID, ps.ProjectID)
	require.Equal(t, sc.EnvironmentID, ps.EnvironmentID)
	require.True(t, ps.IsProduction)
	require.True(t, ps.Irreversible)
	require.Equal(t, []string{"effect one", "effect two"}, ps.Effects)
	require.Len(t, ps.Targets, 1)
	require.Equal(t, "service", ps.Targets[0].Kind)
	require.Equal(t, sv.ID, ps.Targets[0].ID)
	require.Equal(t, "web", ps.Targets[0].Name)
	require.Equal(t, sc.Service.Generation, ps.Targets[0].Generation)

	env, err := services.LoadEnvironment(f.ctx, f.q, "projects:read", f.prod.ID)
	require.NoError(t, err)
	target := services.GuardTarget("environment", f.prod.ID, "production")
	require.Equal(t, "environment", target.Kind)
	require.Equal(t, f.prod.ID, target.ID)
	require.Zero(t, target.Generation, "an object without a generation")
	eps := env.ProdScope("Link two services", false, []prodguard.ApprovalTarget{target}, "opens a grant")
	require.Equal(t, f.prod.ID, eps.EnvironmentID)
	require.Equal(t, f.project.ID, eps.ProjectID)
	require.False(t, eps.Irreversible)
	require.Equal(t, []string{"opens a grant"}, eps.Effects)
	require.NoError(t, env.Guard(f.ctx, "createLink", "Link", false, nil))
}

func TestLogSubscriptionsFollowTheServiceAndComponent(t *testing.T) {
	f := newOpsFixture(t)
	sv := f.deployed(f.prod.ID, "web", services.Config{Image: "traefik/whoami:latest", Port: 80})
	f.logLines(sv.ID, "a", "b")

	ch, cancel, err := f.svcs.SubscribeServiceLogs(f.viewer, sv.ID, 10, false)
	require.NoError(t, err)
	defer cancel()
	var got []string
	for e := range ch {
		got = append(got, e.Line)
	}
	require.Equal(t, []string{"a", "b"}, got)

	_, _, err = f.svcs.SubscribeServiceComponentLogs(f.viewer, sv.ID, 10, false, "db")
	require.Error(t, err, "components belong to stacks")
	_, _, err = f.svcs.SubscribeServiceLogs(f.viewer, uuid.New(), 10, false)
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, f.do(f.viewer, http.MethodGet, "/services/"+uuid.NewString()+"/logs", nil).Code)
}

func TestInvalidComposeInAConfigIsAnInvalidConfigError(t *testing.T) {
	err := services.Config{Stack: &services.StackConfig{Compose: "services: [oops"}}.WithDefaults().Validate(services.SourceCompose)
	require.ErrorIs(t, err, services.ErrInvalidConfig)
	require.ErrorContains(t, err, "stack.compose")
}
