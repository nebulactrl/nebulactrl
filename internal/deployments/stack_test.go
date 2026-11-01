package deployments

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const stackTestCompose = `
services:
  web:
    image: traefik/whoami:v1.10
    ports: ["80:80"]
  sidecar:
    image: busybox:1.36
    command: ["sleep", "3600"]
`

const privilegedCompose = `
services:
  web:
    image: traefik/whoami:v1.10
    privileged: true
`

func codeOf(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "expected an API error, got %v", err)
	return ae.Code
}

func (f *routeFixture) setCompose(available, unrestricted bool) {
	f.t.Helper()
	caps := []string{agentprotocol.CapProxyV2}
	if available {
		caps = append(caps, agentprotocol.CapCompose)
	}
	facts, _ := json.Marshal(agentprotocol.Facts{OS: "linux", Capabilities: caps})
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2, unrestricted = $3 WHERE id = $1", f.server.ID, facts, unrestricted)
	require.NoError(f.t, err)
}

func (f *routeFixture) newStack(name, compose string, server bool) (services.ServiceView, error) {
	in := services.CreateInput{
		Name: name, Kind: services.KindStack, SourceType: services.SourceCompose,
		Config: services.Config{Stack: &services.StackConfig{Compose: compose}},
	}
	if server {
		in.ServerID = &f.server.ID
	}
	return f.svcs.Create(f.ctx, f.env.ID, in)
}

func TestStackCreateStagesAndRestrictedByServerFlag(t *testing.T) {
	f := newRouteFixture(t)
	f.setCompose(true, false)

	// Restricted without a server and on a restricted server; an unparsable file always.
	_, err := f.newStack("bad", privilegedCompose, false)
	require.Equal(t, "invalid_compose", codeOf(t, err))
	ae, _ := apierr.As(err)
	require.NotEmpty(t, ae.Details["violations"])
	_, err = f.newStack("bad", privilegedCompose, true)
	require.Equal(t, "invalid_compose", codeOf(t, err))
	_, err = f.newStack("bad", "services: [oops", true)
	require.Equal(t, "invalid_compose", codeOf(t, err))

	// The server flag lifts restricted violations only.
	f.setCompose(true, true)
	v, err := f.newStack("lab", privilegedCompose, true)
	require.NoError(t, err)
	require.Equal(t, services.KindStack, v.Kind)
	require.Equal(t, services.SourceCompose, v.SourceType)
	require.Equal(t, []string{"web"}, v.Config.Stack.ComponentNames())
	_, err = f.newStack("nobuild", "services:\n  web:\n    build: .\n", true)
	require.Equal(t, "invalid_compose", codeOf(t, err), "build is unsupported even when unrestricted")

	// The check endpoint reports without side effects.
	chk, err := f.svcs.CheckStack(f.ctx, f.env.ID, privilegedCompose, &f.server.ID)
	require.NoError(t, err)
	require.True(t, chk.Unrestricted)
	require.Empty(t, chk.Violations)
	require.NotNil(t, chk.ComposeAvailable)
	require.True(t, *chk.ComposeAvailable)
	f.setCompose(true, false)
	chk, err = f.svcs.CheckStack(f.ctx, f.env.ID, privilegedCompose, &f.server.ID)
	require.NoError(t, err)
	require.NotEmpty(t, chk.Violations)
	require.Len(t, chk.Components, 1)

	// Update stages the change with a generation bump and a text diff; a restricted server refuses it.
	f.setCompose(true, false)
	st, err := f.newStack("shop", stackTestCompose, true)
	require.NoError(t, err)
	require.Equal(t, []string{"sidecar", "web"}, st.Config.Stack.ComponentNames())
	next := stackTestCompose + "  extra:\n    image: busybox:1.36\n"
	upd, err := f.svcs.Update(f.ctx, st.ID, st.Generation, services.UpdateInput{Config: &services.Config{Stack: &services.StackConfig{Compose: next}}})
	require.NoError(t, err)
	require.Greater(t, upd.Generation, st.Generation)
	require.Len(t, upd.Config.Stack.Components, 3)
	_, err = f.svcs.Update(f.ctx, st.ID, upd.Generation, services.UpdateInput{Config: &services.Config{Stack: &services.StackConfig{Compose: privilegedCompose}}})
	require.Equal(t, "invalid_compose", codeOf(t, err))

	// Not applicable features answer 422 not_applicable.
	_, err = f.svcs.AddVolume(f.ctx, st.ID, "data", "/data")
	require.Equal(t, "not_applicable", codeOf(t, err))
}

func TestStackPlanBlockersAndDeployPayload(t *testing.T) {
	f := newRouteFixture(t)
	f.setCompose(false, false)
	st, err := f.newStack("shop", stackTestCompose, true)
	require.NoError(t, err)

	plan, err := f.deps.Plan(f.ctx, st.ID)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.StrategyRecreate, plan.Strategy)
	require.Contains(t, plan.Reasons, ReasonStack)
	require.Equal(t, "compose_unavailable", plan.Blocking[0].Code)

	// Alias conflict: a component named like another service's slug.
	f.setCompose(true, false)
	f.web("web")
	plan, err = f.deps.Plan(f.ctx, st.ID)
	require.NoError(t, err)
	var codes []string
	for _, b := range plan.Blocking {
		codes = append(codes, b.Code)
	}
	require.Equal(t, []string{"stack_alias_conflict"}, codes)
	_, err = f.deps.Deploy(f.ctx, st.ID, "", "")
	require.Equal(t, "stack_alias_conflict", codeOf(t, err))
}

func TestStackDomainsRoutesDeployAndReleaseComponents(t *testing.T) {
	f := newRouteFixture(t)
	f.setCompose(true, false)
	st, err := f.newStack("shop", stackTestCompose, true)
	require.NoError(t, err)
	svcID := st.ID.String()

	// Component rules: required, known, port required unless the component declares exactly one.
	_, err = f.doms.Create(f.ctx, st.ID, "shop.example.com", 0, domainsRule())
	require.Equal(t, "component_required", codeOf(t, err))
	_, err = f.doms.CreateForComponent(f.ctx, st.ID, "shop.example.com", 0, "db", domainsRule())
	require.Equal(t, "unknown_component", codeOf(t, err))
	_, err = f.doms.CreateForComponent(f.ctx, st.ID, "shop.example.com", 0, "sidecar", domainsRule())
	require.Equal(t, "target_port_required", codeOf(t, err))
	d1, err := f.doms.CreateForComponent(f.ctx, st.ID, "shop.example.com", 0, "web", domainsRule())
	require.NoError(t, err)
	require.Equal(t, 80, d1.TargetPort, "the single declared port is the default")
	require.Equal(t, "web", d1.Component)
	_, err = f.doms.CreateForComponent(f.ctx, st.ID, "api.example.com", 9000, "sidecar", domainsRule())
	require.NoError(t, err)

	// A component with domains cannot leave the compose file.
	cur, err := f.svcs.Get(f.ctx, st.ID)
	require.NoError(t, err)
	_, err = f.svcs.Update(f.ctx, st.ID, cur.Generation, services.UpdateInput{Config: &services.Config{Stack: &services.StackConfig{Compose: "services:\n  web:\n    image: traefik/whoami:v1.10\n"}}})
	require.Equal(t, "component_in_use", codeOf(t, err))

	// Deploy: payload shape.
	v, err := f.deps.Deploy(f.ctx, st.ID, "", "")
	require.NoError(t, err)
	require.Equal(t, agentprotocol.StrategyRecreate, v.Strategy)
	require.NoError(t, f.deps.runDeploy(context.Background(), v.ID, ""))
	cmd := f.lastCommand(agentprotocol.KindDeploy)
	var pl agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &pl))
	require.NotNil(t, pl.Stack)
	require.Nil(t, pl.Image)
	require.Nil(t, pl.Git)
	require.Equal(t, agentprotocol.StrategyRecreate, pl.Strategy)
	require.Equal(t, 1, pl.Replicas)
	require.Empty(t, pl.Volumes)
	require.Empty(t, pl.Files)
	require.Empty(t, pl.DomainRules)
	require.Empty(t, pl.Hosts)
	require.Equal(t, stackTestCompose, pl.Stack.Compose)
	require.False(t, pl.Stack.Unrestricted)
	require.Empty(t, pl.Stack.Images, "a fresh deploy pulls what the file says")
	require.Equal(t, agentprotocol.DefaultStackWaitSeconds, pl.Stack.WaitSeconds)
	require.Len(t, pl.Stack.Components, 2)
	require.Len(t, pl.Stack.Routes, 2)
	byUp := map[string]agentprotocol.Route{}
	for _, r := range pl.Stack.Routes {
		require.Equal(t, svcID, r.Stack)
		require.Len(t, r.Upstreams, 1)
		byUp[r.Upstreams[0]] = r
	}
	web := byUp[agentprotocol.StackContainerName(svcID, "web")+":80"]
	require.Equal(t, agentprotocol.StackRouteID(svcID, "web", 80), web.ServiceID)
	require.Equal(t, []string{"shop.example.com"}, web.Hosts)
	require.Contains(t, byUp, agentprotocol.StackContainerName(svcID, "sidecar")+":9000")

	// Result: the images land in the release source and observed components are seeded.
	images := map[string]string{"web": "traefik/whoami@sha256:aa", "sidecar": "busybox@sha256:bb"}
	data, _ := json.Marshal(agentprotocol.DeployResult{Outcome: agentprotocol.OutcomeSucceeded, Stack: &agentprotocol.StackResult{
		Images:     images,
		Components: []agentprotocol.ObservedReplica{{Index: 0, Component: "sidecar", State: "running", Health: "healthy"}, {Index: 1, Component: "web", State: "running", Health: "healthy"}},
	}})
	require.NoError(t, f.deps.HandleResult(context.Background(), f.server.ID, uuid.New(), &v.ID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
	dep, err := f.q.ListDeploymentsByIDs(context.Background(), []uuid.UUID{v.ID})
	require.NoError(t, err)
	rel, err := f.q.GetReleaseByID(context.Background(), *dep[0].ReleaseID)
	require.NoError(t, err)
	src := services.DecodeSource(rel.Source)
	require.Equal(t, services.SourceCompose, src.Type)
	require.Equal(t, images, src.Components)
	after, err := f.svcs.Get(f.ctx, st.ID)
	require.NoError(t, err)
	require.Len(t, after.Observed.Replicas, 2)
	require.Equal(t, "web", after.Observed.Replicas[1].Component)

	// A second deploy, then a rollback to the first: the rollback carries the
	// first release's images and reads Unrestricted from the server now.
	v2, err := f.deps.Deploy(f.ctx, st.ID, "", "")
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), v2.ID, ""))
	data2, _ := json.Marshal(agentprotocol.DeployResult{Outcome: agentprotocol.OutcomeSucceeded, Stack: &agentprotocol.StackResult{Images: map[string]string{"web": "x@sha256:cc", "sidecar": "y@sha256:dd"}}})
	require.NoError(t, f.deps.HandleResult(context.Background(), f.server.ID, uuid.New(), &v2.ID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data2}))
	f.setCompose(true, true)
	rb, err := f.deps.Rollback(f.ctx, v.ID)
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(context.Background(), rb.ID, ""))
	var pl2 agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &pl2))
	require.Equal(t, images, pl2.Stack.Images)
	require.True(t, pl2.Stack.Unrestricted)
	require.NotContains(t, string(f.lastCommand(agentprotocol.KindDeploy).Payload), "sekret")

	// The stack's own routes ride in Stack.Routes only: the other-services table
	// of its payload never repeats them.
	for _, r := range pl2.Routes {
		require.NotEqual(t, svcID, r.Stack)
		require.NotEqual(t, agentprotocol.StackRouteID(svcID, "web", 80), r.ServiceID)
	}
	// Other services' deploys (and reconcile) see the stack's routes in the server-wide table.
	other := f.web("other")
	table, err := domains.ServerRoutes(context.Background(), f.q, f.server.ID, other.ID)
	require.NoError(t, err)
	got := map[string]agentprotocol.Route{}
	for _, r := range table {
		got[r.ServiceID] = r
	}
	for _, rt := range []struct {
		component string
		port      int
	}{{"web", 80}, {"sidecar", 9000}} {
		r, ok := got[agentprotocol.StackRouteID(svcID, rt.component, rt.port)]
		require.True(t, ok, rt.component)
		require.Equal(t, svcID, r.Stack)
		require.Equal(t, []string{agentprotocol.StackContainerName(svcID, rt.component) + ":" + strconv.Itoa(rt.port)}, r.Upstreams)
	}
	require.Empty(t, got[agentprotocol.StackRouteID(svcID, "web", 80)].Maintenance, "no maintenance for stacks")
}

func TestStackPrepareBlocksWithoutComposeCapability(t *testing.T) {
	f := newRouteFixture(t)
	f.setCompose(true, false)
	st, err := f.newStack("shop", stackTestCompose, true)
	require.NoError(t, err)
	v, err := f.deps.Deploy(f.ctx, st.ID, "", "")
	require.NoError(t, err)
	// The agent lost the plugin between the request and the preparation.
	f.setCompose(false, false)
	require.NoError(t, f.deps.runDeploy(context.Background(), v.ID, ""))
	dv, err := f.deps.Get(f.ctx, v.ID)
	require.NoError(t, err)
	require.Equal(t, string(Failed), dv.Status)
	require.Contains(t, dv.StatusMessage, "cannot run Compose stacks")
}

func domainsRule() domains.RuleInput { return domains.RuleInput{} }
