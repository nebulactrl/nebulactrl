package clone_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/clone"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// restToken is a REST token as authentication builds it: guarded unless exempt.
func (f *fixture) restToken() context.Context {
	id := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: f.userID, TokenID: &id, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

// browser is a person's session: never asked.
func (f *fixture) browser() context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: f.userID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *fixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	f.t.Cleanup(prodguard.Uninstall)
	return g
}

func (f *fixture) approve(g *prodguard.Guard, err error) *prodguard.ErrApprovalRequired {
	f.t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(f.t, ok, "want approval_required, got %v", err)
	_, aerr := g.Approve(f.browser(), ar.ApprovalID)
	require.NoError(f.t, aerr)
	return ar
}

// A REST token cannot clone a production service, with its secrets, without a
// person's approval; a dry run is not asked, a person in the browser is not
// asked either.
func TestCloneServiceFromProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t) // the fixture's "production" environment is production
	g := f.installGuard()
	rest := f.restToken()
	services0 := f.count("SELECT count(*) FROM services")

	// A dry run changes nothing and is not guarded.
	plan, err := f.cl.CloneService(rest, f.api.ID, clone.ServiceInput{DryRun: true})
	require.NoError(t, err)
	require.True(t, plan.DryRun)

	in := clone.ServiceInput{EnvironmentID: &f.staging.ID}
	_, err = f.cl.CloneService(rest, f.api.ID, in)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Clone api from production")
	require.Equal(t, services0, f.count("SELECT count(*) FROM services"), "nothing is cloned before the approval")
	require.Equal(t, 0, f.count("SELECT count(*) FROM services WHERE environment_id = $1", f.staging.ID))

	f.approve(g, err)
	res, err := f.cl.CloneService(rest, f.api.ID, in)
	require.NoError(t, err)
	require.NotNil(t, res.Service)
	require.Equal(t, f.staging.ID, f.row(res.Service.ID).EnvironmentID, "the clone exists")
	require.Equal(t, services0+1, f.count("SELECT count(*) FROM services"))

	// The approval was used up.
	_, err = f.cl.CloneService(rest, f.api.ID, in)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, services0+1, f.count("SELECT count(*) FROM services"))

	// A different call (another name) is not covered by an approval for this one.
	_, err = f.cl.CloneService(rest, f.web.ID, clone.ServiceInput{EnvironmentID: &f.staging.ID, Name: "web2"})
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)

	// A person in the browser is never asked.
	got, err := f.cl.CloneService(f.browser(), f.api.ID, clone.ServiceInput{})
	require.NoError(t, err)
	require.NotNil(t, got.Service)
}

// Only a production source is guarded: copying out of staging is open to a REST token.
func TestCloneServiceFromNonProductionIsNotGuarded(t *testing.T) {
	f := newFixture(t)
	f.installGuard()
	rest := f.restToken()
	stg := f.insert(f.staging.ID, "stg-app", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})

	res, err := f.cl.CloneService(rest, stg.ID, clone.ServiceInput{})
	require.NoError(t, err)
	require.NotNil(t, res.Service)
	require.Equal(t, f.staging.ID, f.row(res.Service.ID).EnvironmentID)
}

// A REST token cannot clone a production environment, with its secrets,
// without a person's approval.
func TestCloneEnvironmentFromProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t)
	g := f.installGuard()
	rest := f.restToken()
	envs0 := f.count("SELECT count(*) FROM environments")
	services0 := f.count("SELECT count(*) FROM services")

	// A dry run is not guarded.
	plan, err := f.cl.CloneEnvironment(rest, f.prod.ID, clone.EnvironmentInput{Name: "QA", DryRun: true})
	require.NoError(t, err)
	require.True(t, plan.DryRun)

	in := clone.EnvironmentInput{Name: "QA"}
	_, err = f.cl.CloneEnvironment(rest, f.prod.ID, in)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Clone the production environment production")
	require.Equal(t, envs0, f.count("SELECT count(*) FROM environments"), "nothing is cloned before the approval")
	require.Equal(t, services0, f.count("SELECT count(*) FROM services"))

	f.approve(g, err)
	res, err := f.cl.CloneEnvironment(rest, f.prod.ID, in)
	require.NoError(t, err)
	require.NotNil(t, res.Environment)
	require.Equal(t, 1, f.count("SELECT count(*) FROM environments WHERE id = $1", res.Environment.ID), "the clone exists")
	require.Equal(t, envs0+1, f.count("SELECT count(*) FROM environments"))
	require.NotEmpty(t, f.envServices(res.Environment.ID))

	// The approval was used up.
	_, err = f.cl.CloneEnvironment(rest, f.prod.ID, in)
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)

	// A person in the browser is never asked.
	got, err := f.cl.CloneEnvironment(f.browser(), f.prod.ID, clone.EnvironmentInput{Name: "Person copy"})
	require.NoError(t, err)
	require.NotNil(t, got.Environment)
}

// Only a production source is guarded: copying staging is open to a REST token.
func TestCloneEnvironmentFromNonProductionIsNotGuarded(t *testing.T) {
	f := newFixture(t)
	f.installGuard()
	rest := f.restToken()
	f.insert(f.staging.ID, "stg-app", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})

	res, err := f.cl.CloneEnvironment(rest, f.staging.ID, clone.EnvironmentInput{Name: "Staging copy"})
	require.NoError(t, err)
	require.NotNil(t, res.Environment)
}
