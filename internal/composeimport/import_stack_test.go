package composeimport_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/composeimport"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

const stackCompose = `name: shop
services:
  web:
    image: nginx:1.27
    ports:
      - "8080:80"
    environment:
      DB_PASSWORD: ${DB_PASSWORD}
    depends_on: [worker]
  worker:
    image: busybox:1.36
    command: ["sleep", "3600"]
  cache:
    image: redis:7
`

const stackDotenv = "APP_NAME=demo\nDB_PASSWORD=hunter2hunter2\nCOMPOSE_PROJECT_NAME=ignored\n"

func stackReq(compose, env string) composeimport.ComposeSource {
	return composeimport.ComposeSource{Compose: compose, Env: env}
}

func (f *importFixture) setServer(t *testing.T, sql string) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET "+sql+" WHERE id = $1", f.srv.ID)
	require.NoError(t, err)
}

func violationKeys(vs []composestack.Violation) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Service+"."+v.Key)
	}
	return out
}

func TestStackPlanListsComponentsDomainsAndVariables(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "base_domain = 'example.test'")

	plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: stackReq(stackCompose, stackDotenv), ServerID: &f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	require.Equal(t, "stack", plan.Mode)
	require.True(t, plan.Installable, "%v", plan.Blockers)
	require.Empty(t, plan.Services, "stack mode has no split services")
	require.NotNil(t, plan.Stack)
	require.Equal(t, "shop", plan.Stack.Name)
	require.Equal(t, "shop", plan.Stack.Slug)
	require.Empty(t, plan.Stack.Violations)

	var names []string
	byName := map[string]composeimport.ComposeStackComponent{}
	for _, c := range plan.Stack.Components {
		names = append(names, c.Name)
		byName[c.Name] = c
	}
	require.Equal(t, []string{"cache", "web", "worker"}, names, "sorted by name")
	require.Equal(t, 1, byName["web"].Index)
	require.Equal(t, "nginx:1.27", byName["web"].Image)
	require.Equal(t, 80, byName["web"].Port)
	require.Len(t, byName["web"].Domains, 1)
	require.True(t, byName["web"].Domains[0].Enabled, "a published HTTP port gets a domain")
	require.True(t, byName["web"].Domains[0].Generate)
	require.Empty(t, byName["worker"].Domains)
	require.Empty(t, byName["cache"].Domains, "redis is not HTTP")

	vars := map[string]composeimport.ComposePlanEnv{}
	for _, v := range plan.Stack.Variables {
		vars[v.Key] = v
	}
	require.Equal(t, "demo", vars["APP_NAME"].Value)
	require.False(t, vars["APP_NAME"].Secret)
	require.True(t, vars["DB_PASSWORD"].Secret)
	require.True(t, vars["DB_PASSWORD"].SecretGuess)
	require.Empty(t, vars["DB_PASSWORD"].Value, "secret values are never returned")
	require.True(t, vars["COMPOSE_PROJECT_NAME"].Skipped, "compose never gets COMPOSE_* from a stack")
	body, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NotContains(t, string(body), "hunter2")

	require.False(t, plan.Stack.Unrestricted)
	require.Nil(t, plan.Stack.ComposeAvailable, "the agent has not reported facts yet")
}

func TestStackImportCreatesOneStackWithComposeVerbatim(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "base_domain = 'example.test'")
	src := stackReq(stackCompose, stackDotenv)

	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	require.Len(t, res.Services, 1)
	require.Empty(t, res.GroupID, "a stack is one node, there is no canvas group")
	require.Equal(t, "shop", res.Name)

	got := f.services()
	require.Len(t, got, 1)
	st := got["shop"]
	require.Equal(t, services.KindStack, st.Kind)
	require.Equal(t, services.SourceCompose, st.SourceType)
	require.Equal(t, f.srv.ID, *st.ServerID)
	require.Greater(t, st.Generation, st.AppliedGeneration, "staged, nothing deployed")
	cfg := services.DecodeConfig(st.Config)
	require.NotNil(t, cfg.Stack)
	require.Equal(t, stackCompose, cfg.Stack.Compose, "the file as given, ${VAR} placeholders untouched")
	require.Len(t, cfg.Stack.Components, 3)

	raw := f.vars(st.ID)
	require.Equal(t, "demo", raw["APP_NAME"].Value)
	require.False(t, raw["APP_NAME"].Secret)
	require.Equal(t, "hunter2hunter2", raw["DB_PASSWORD"].Value)
	require.True(t, raw["DB_PASSWORD"].Secret)
	require.NotContains(t, raw, "COMPOSE_PROJECT_NAME")

	var host, component string
	var port int32
	var generated bool
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT hostname, component, target_port, generated FROM domains WHERE service_id = $1", st.ID).Scan(&host, &component, &port, &generated))
	require.Equal(t, "web", component)
	require.EqualValues(t, 80, port)
	require.True(t, generated)
	require.Equal(t, "shop-production-shop.example.test", host)

	var meta []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata FROM audit_events WHERE action = 'compose.import'").Scan(&meta))
	var m map[string]any
	require.NoError(t, json.Unmarshal(meta, &m))
	require.Equal(t, "stack", m["mode"])
	require.EqualValues(t, 1, m["services"])
	require.EqualValues(t, 3, m["components"])
	require.Equal(t, "paste", m["source"])

	var deployments int
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT count(*) FROM deployments WHERE service_id = $1", st.ID).Scan(&deployments))
	require.Zero(t, deployments, "nothing deploys until the user deploys")

	// A second import of the same file is a new stack with its own slug and hostname.
	res, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: src, ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	require.Len(t, res.Services, 1)
	require.Equal(t, "shop-2", res.Services[0].Slug)
	require.Len(t, f.services(), 2)
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT hostname FROM domains WHERE service_id = $1", res.Services[0].ID).Scan(&host))
	require.Equal(t, "shop-2-production-shop.example.test", host)
}

func TestStackImportSplitsDomainsByComponent(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "base_domain = 'example.test'")
	compose := "services:\n  web:\n    image: nginx\n    ports: ['8080:80']\n  api:\n    image: ghcr.io/acme/api:1\n    ports: ['3000:3000']\n"
	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{
		Source: composeimport.ComposeSource{Compose: compose, Name: "Acme"}, ServerID: f.srv.ID, Mode: composeimport.ModeStack,
	})
	require.NoError(t, err)
	rows, err := f.pool.Query(context.Background(), "SELECT component, target_port, hostname FROM domains WHERE service_id = $1 ORDER BY component", res.Services[0].ID)
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var c, h string
		var p int32
		require.NoError(t, rows.Scan(&c, &p, &h))
		got[c] = h
	}
	require.Equal(t, map[string]string{
		"api": "acme-api-production-shop.example.test",
		"web": "acme-web-production-shop.example.test",
	}, got)
}

func TestStackRestrictedViolationsBlockUnlessTheServerIsUnrestricted(t *testing.T) {
	f := newImportFixture(t)
	compose := `services:
  agent:
    image: example/agent:1
    privileged: true
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
  web:
    image: nginx
`
	req := composeimport.ComposePlanRequest{Source: stackReq(compose, ""), ServerID: &f.srv.ID, Mode: composeimport.ModeStack}

	plan, err := f.svc.Plan(f.ctx, f.env.ID, req)
	require.NoError(t, err)
	require.False(t, plan.Installable)
	require.Contains(t, violationKeys(plan.Stack.Violations), "agent.privileged")
	require.Len(t, plan.Stack.Violations, 2, "privileged and the Docker socket bind")
	for _, v := range plan.Stack.Violations {
		require.Equal(t, composestack.KindRestricted, v.Kind)
	}
	require.NotEmpty(t, plan.Blockers)

	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: req.Source, ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.Equal(t, "invalid_compose", apiCode(t, err))
	e, _ := apierr.As(err)
	require.Equal(t, 422, e.Status)
	vs, ok := e.Details["violations"].([]composestack.Violation)
	require.True(t, ok, "details carry the violations")
	require.Len(t, vs, 2)
	require.Empty(t, f.services(), "nothing was written")

	// Without a server the restricted rules apply too.
	req.ServerID = nil
	plan, err = f.svc.Plan(f.ctx, f.env.ID, req)
	require.NoError(t, err)
	require.False(t, plan.Installable)
	require.Len(t, plan.Stack.Violations, 2)

	// An unrestricted server lifts them.
	f.setServer(t, "unrestricted = true")
	req.ServerID = &f.srv.ID
	plan, err = f.svc.Plan(f.ctx, f.env.ID, req)
	require.NoError(t, err)
	require.True(t, plan.Installable, "%v", plan.Blockers)
	require.Empty(t, plan.Stack.Violations)
	require.True(t, plan.Stack.Unrestricted)

	res, err := f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: req.Source, ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.NoError(t, err)
	require.Len(t, res.Services, 1)
}

func TestStackBuildIsUnsupportedEvenOnAnUnrestrictedServer(t *testing.T) {
	f := newImportFixture(t)
	f.setServer(t, "unrestricted = true")
	compose := "services:\n  app:\n    build: .\n"
	req := composeimport.ComposePlanRequest{Source: stackReq(compose, ""), ServerID: &f.srv.ID, Mode: composeimport.ModeStack}

	plan, err := f.svc.Plan(f.ctx, f.env.ID, req)
	require.NoError(t, err)
	require.False(t, plan.Installable)
	require.Len(t, plan.Stack.Violations, 1)
	v := plan.Stack.Violations[0]
	require.Equal(t, composestack.KindUnsupported, v.Kind)
	require.Equal(t, "app", v.Service)
	require.Equal(t, "build", v.Key)

	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: req.Source, ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.Equal(t, "invalid_compose", apiCode(t, err))
	require.Empty(t, f.services())
}

func TestStackRejectsAnUnparsableFile(t *testing.T) {
	f := newImportFixture(t)
	req := composeimport.ComposePlanRequest{Source: stackReq("services: [", ""), ServerID: &f.srv.ID, Mode: composeimport.ModeStack}
	_, err := f.svc.Plan(f.ctx, f.env.ID, req)
	require.Equal(t, "invalid_compose", apiCode(t, err))
	_, err = f.svc.Import(f.ctx, f.env.ID, composeimport.ComposeImportRequest{Source: req.Source, ServerID: f.srv.ID, Mode: composeimport.ModeStack})
	require.Equal(t, "invalid_compose", apiCode(t, err))
}

func TestStackVariableChoicesAndSplitModeStaysTheDefault(t *testing.T) {
	f := newImportFixture(t)
	yes, no := true, false
	_ = no
	plan, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{
		Source: stackReq(stackCompose, stackDotenv), ServerID: &f.srv.ID, Mode: composeimport.ModeStack,
		Choices: composeimport.ComposeChoices{Variables: map[string]composeimport.ComposeEnvChoice{
			"APP_NAME": {Secret: &yes}, "DB_PASSWORD": {Skip: true},
		}},
	})
	require.NoError(t, err)
	vars := map[string]composeimport.ComposePlanEnv{}
	for _, v := range plan.Stack.Variables {
		vars[v.Key] = v
	}
	require.True(t, vars["APP_NAME"].Secret)
	require.False(t, vars["APP_NAME"].SecretGuess)
	require.True(t, vars["DB_PASSWORD"].Skipped)

	// No mode is split mode.
	split, err := f.svc.Plan(f.ctx, f.env.ID, composeimport.ComposePlanRequest{Source: stackReq(stackCompose, ""), ServerID: &f.srv.ID})
	require.NoError(t, err)
	require.Equal(t, "split", split.Mode)
	require.Nil(t, split.Stack)
	require.Len(t, split.Services, 3)
}
