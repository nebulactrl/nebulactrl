package variables_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

// as returns a context for the fixture's user with another role and project
// restriction (nil: unrestricted).
func (f *fx) as(role string, projects []uuid.UUID, scopes ...string) context.Context {
	f.t.Helper()
	p, ok := auth.PrincipalFrom(f.ctx)
	require.True(f.t, ok)
	cp := *p
	cp.Role = role
	cp.ProjectIDs = projects
	if len(scopes) > 0 {
		cp.Scopes = scopes
	}
	return auth.WithPrincipal(context.Background(), &cp)
}

func sv(key, value string) variables.SharedVariableInput {
	return variables.SharedVariableInput{Key: key, Value: new(value)}
}

func sharedItem(t *testing.T, l variables.SharedVariableList, key string) variables.SharedVariableView {
	t.Helper()
	for _, it := range l.Items {
		if it.Key == key {
			return it
		}
	}
	require.Failf(t, "shared variable missing", "%s not in %v", key, sharedKeys(l))
	return variables.SharedVariableView{}
}

func sharedKeys(l variables.SharedVariableList) []string {
	out := []string{}
	for _, it := range l.Items {
		out = append(out, it.Key)
	}
	return out
}

// projectValue reads and decrypts a project-layer shared value straight from the table.
func (f *fx) projectValue(key string) string {
	f.t.Helper()
	row, err := f.q.GetSharedVariableProject(context.Background(), db.GetSharedVariableProjectParams{ProjectID: f.prj.ID, Key: key})
	require.NoError(f.t, err)
	v, err := f.box.OpenString(secretbox.SharedVariablesValue, row.ID.String(), row.ValueEnc)
	require.NoError(f.t, err)
	return v
}

func TestSharedProjectLifecycle(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")

	l, err := f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{
		sv("API_URL", "https://api.example.com"),
		{Key: "TOKEN", Value: new("hunter2"), IsSecret: new(true), Comment: new("upstream token")},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"API_URL", "TOKEN"}, sharedKeys(l), "items are sorted by key")
	api := sharedItem(t, l, "API_URL")
	require.NotNil(t, api.Value)
	require.Equal(t, "https://api.example.com", *api.Value)
	require.Equal(t, variables.LayerProject, api.Layer)
	require.Equal(t, 1, api.Version)
	require.Equal(t, services.SourceManual, api.Source)
	require.False(t, api.IsSecret)
	tok := sharedItem(t, l, "TOKEN")
	require.Nil(t, tok.Value, "a secret's value is never returned")
	require.True(t, tok.IsSecret)
	require.True(t, tok.ComputedSecret)
	require.Equal(t, "upstream token", tok.Comment)
	require.Empty(t, l.Restaged, "nothing references the variables yet")
	require.Equal(t, 1, f.auditCount("shared_variable.set", f.prj.ID))
	meta := f.auditMeta("shared_variable.set", f.prj.ID)
	require.Equal(t, "project", meta["layer"])
	require.Equal(t, []any{"API_URL", "TOKEN"}, meta["keys"])
	require.NotContains(t, mustJSON(t, meta), "hunter2")
	require.Equal(t, "hunter2", f.projectValue("TOKEN"))

	// The same input again writes nothing.
	l, err = f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("API_URL", "https://api.example.com")})
	require.NoError(t, err)
	require.Equal(t, 1, sharedItem(t, l, "API_URL").Version)
	require.Equal(t, 1, f.auditCount("shared_variable.set", f.prj.ID), "a no-op write is not audited")

	// Sharing stages KEY=${{shared.KEY}} on the service and counts the usage.
	genBefore := f.gen(app.ID)
	l, err = f.svc.ShareShared(f.ctx, variables.LayerProject, f.prj.ID, "API_URL", variables.ShareSharedInput{ServiceIDs: []uuid.UUID{app.ID}})
	require.NoError(t, err)
	require.Empty(t, l.Warnings)
	require.Equal(t, "${{shared.API_URL}}", f.value(app.ID, "API_URL"))
	require.Equal(t, genBefore+1, f.gen(app.ID), "the service is restaged")
	require.Equal(t, 1, sharedItem(t, l, "API_URL").UsedBy)
	require.Equal(t, 0, sharedItem(t, l, "TOKEN").UsedBy)
	require.Equal(t, 1, f.auditCount("variable.share", app.ID))

	// Sharing again leaves the existing variable alone and says so.
	l, err = f.svc.ShareShared(f.ctx, variables.LayerProject, f.prj.ID, "API_URL", variables.ShareSharedInput{ServiceIDs: []uuid.UUID{app.ID}})
	require.NoError(t, err)
	require.Len(t, l.Warnings, 1)
	require.Equal(t, "already_set", l.Warnings[0].Code)
	require.Equal(t, "API_URL", l.Warnings[0].Key)
	require.Contains(t, l.Warnings[0].Message, "app")
	require.Equal(t, genBefore+1, f.gen(app.ID))

	// Changing the shared value restages the consumer and bumps the version.
	l, err = f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("API_URL", "https://api2.example.com")})
	require.NoError(t, err)
	require.Equal(t, []string{"app"}, slugs(l.Restaged))
	require.Equal(t, 2, sharedItem(t, l, "API_URL").Version)
	require.Equal(t, genBefore+2, f.gen(app.ID))

	hist, err := f.svc.SharedHistory(f.ctx, variables.LayerProject, f.prj.ID, "API_URL")
	require.NoError(t, err)
	require.Len(t, hist, 2)
	require.Equal(t, 2, hist[0].Version, "newest first")

	// Deleting restages the consumer and records a delete version.
	l, err = f.svc.DeleteShared(f.ctx, variables.LayerProject, f.prj.ID, "API_URL")
	require.NoError(t, err)
	require.Equal(t, []string{"TOKEN"}, sharedKeys(l))
	require.Equal(t, []string{"app"}, slugs(l.Restaged))
	require.Equal(t, 1, f.auditCount("shared_variable.delete", f.prj.ID))
	hist, err = f.svc.SharedHistory(f.ctx, variables.LayerProject, f.prj.ID, "API_URL")
	require.NoError(t, err)
	require.Equal(t, "delete", hist[0].Op)
	require.Equal(t, 3, hist[0].Version)

	_, err = f.svc.DeleteShared(f.ctx, variables.LayerProject, f.prj.ID, "API_URL")
	require.Equal(t, "not_found", apiCode(t, err), "deleting a missing variable is a 404")
}

func TestSharedEnvironmentLayerOverridesAndInherits(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	_, err := f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("REGION", "eu"), sv("LEVEL", "info")})
	require.NoError(t, err)
	_, err = f.svc.ShareShared(f.ctx, variables.LayerProject, f.prj.ID, "REGION", variables.ShareSharedInput{ServiceIDs: []uuid.UUID{app.ID}})
	require.NoError(t, err)

	l, err := f.svc.PutShared(f.ctx, variables.LayerEnvironment, f.env.ID, []variables.SharedVariableInput{sv("REGION", "us")})
	require.NoError(t, err)
	require.Equal(t, []string{"app"}, slugs(l.Restaged), "the override changes what app resolves")
	region := sharedItem(t, l, "REGION")
	require.Equal(t, variables.LayerEnvironment, region.Layer)
	require.True(t, region.Overrides)
	require.False(t, region.Inherited)
	require.Equal(t, "us", *region.Value)
	level := sharedItem(t, l, "LEVEL")
	require.Equal(t, variables.LayerProject, level.Layer)
	require.True(t, level.Inherited, "project values show up read-only in the environment listing")
	require.Len(t, l.Items, 2, "the overridden project value is not listed twice")
	require.Equal(t, 1, region.UsedBy)
	require.Equal(t, 1, f.auditCount("shared_variable.set", f.env.ID))
	require.Equal(t, "environment", f.auditMeta("shared_variable.set", f.env.ID)["layer"])

	// The project listing is untouched by the environment value.
	pl, err := f.svc.ListShared(f.ctx, variables.LayerProject, f.prj.ID)
	require.NoError(t, err)
	require.Equal(t, "eu", *sharedItem(t, pl, "REGION").Value)

	// Deleting the override falls back to the project value and restages.
	l, err = f.svc.DeleteShared(f.ctx, variables.LayerEnvironment, f.env.ID, "REGION")
	require.NoError(t, err)
	require.Equal(t, []string{"app"}, slugs(l.Restaged))
	region = sharedItem(t, l, "REGION")
	require.True(t, region.Inherited)
	require.Equal(t, "eu", *region.Value)

	// An environment of another project is not reachable by id guessing.
	_, err = f.svc.ListShared(f.ctx, variables.LayerEnvironment, uuid.New())
	require.Equal(t, "not_found", apiCode(t, err))
	_, err = f.svc.ListShared(f.ctx, variables.LayerProject, uuid.New())
	require.Equal(t, "not_found", apiCode(t, err))
}

func TestSharedPutRejectsBadInput(t *testing.T) {
	f := newFx(t)
	put := func(in ...variables.SharedVariableInput) error {
		_, err := f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, in)
		return err
	}
	require.Equal(t, "invalid_variable_key", apiCode(t, put(sv("1BAD", "x"))))
	require.Equal(t, "invalid_variable_key", apiCode(t, put(sv("has space", "x"))))
	require.Equal(t, "invalid_variable_key", apiCode(t, put(sv(strings.Repeat("K", 256), "x"))))
	require.Equal(t, "duplicate_variable", apiCode(t, put(sv("A", "1"), sv("A", "2"))))
	require.Equal(t, "value_too_large", apiCode(t, put(sv("BIG", strings.Repeat("x", 64*1024+1)))))
	many := make([]variables.SharedVariableInput, 501)
	for i := range many {
		many[i] = sv("K", "v")
	}
	require.Equal(t, "too_many_variables", apiCode(t, put(many...)))
	require.Equal(t, "value_required", apiCode(t, put(variables.SharedVariableInput{Key: "NEW"})), "a new variable needs a value")
	require.Equal(t, "invalid_template", apiCode(t, put(sv("BROKEN", "${{unclosed"))))
	require.Equal(t, "invalid_shared_reference", apiCode(t, put(sv("URL", "${{app.URL}}"))), "shared values cannot reference a service")
	require.Equal(t, "invalid_shared_reference", apiCode(t, put(sv("BARE", "${{URL}}"))))

	// Nothing was written by the failed requests.
	l, err := f.svc.ListShared(f.ctx, variables.LayerProject, f.prj.ID)
	require.NoError(t, err)
	require.Empty(t, l.Items)
	require.Zero(t, f.count("SELECT count(*) FROM audit_events WHERE action = 'shared_variable.set'"))

	// Shared and team references are allowed, and a write reports hygiene warnings.
	l, err = f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{
		sv("BASE", " padded "), sv("DERIVED", "${{shared.BASE}}/x"), sv("FROM_TEAM", "${{team.ORG}}"),
	})
	require.NoError(t, err)
	require.Len(t, l.Items, 3)
	require.Len(t, l.Warnings, 1)
	require.Equal(t, "whitespace", l.Warnings[0].Code)
	require.Equal(t, "BASE", l.Warnings[0].Key)
}

func TestSharedSecretnessAndKeepingValues(t *testing.T) {
	f := newFx(t)
	put := func(in ...variables.SharedVariableInput) (variables.SharedVariableList, error) {
		return f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, in)
	}
	_, err := put(variables.SharedVariableInput{Key: "S", Value: new("v1"), IsSecret: new(true)})
	require.NoError(t, err)

	// Omitting the value keeps it, also when only the comment changes.
	l, err := put(variables.SharedVariableInput{Key: "S", Comment: new("why")})
	require.NoError(t, err)
	s := sharedItem(t, l, "S")
	require.Equal(t, "why", s.Comment)
	require.True(t, s.IsSecret)
	require.Equal(t, 2, s.Version)
	require.Equal(t, "v1", f.projectValue("S"))

	// Making a secret plain needs a new value: the old one was never public.
	_, err = put(variables.SharedVariableInput{Key: "S", IsSecret: new(false)})
	require.Equal(t, "value_required", apiCode(t, err))
	l, err = put(variables.SharedVariableInput{Key: "S", Value: new("public"), IsSecret: new(false)})
	require.NoError(t, err)
	s = sharedItem(t, l, "S")
	require.False(t, s.IsSecret)
	require.Equal(t, "public", *s.Value)

	// Marking a plain value secret keeps the value and hides it.
	l, err = put(variables.SharedVariableInput{Key: "S", IsSecret: new(true)})
	require.NoError(t, err)
	s = sharedItem(t, l, "S")
	require.True(t, s.IsSecret)
	require.Nil(t, s.Value)
	require.Equal(t, "public", f.projectValue("S"))

	// A plain variable that embeds a secret is secret by derivation.
	l, err = put(sv("WRAPPED", "x-${{shared.S}}"))
	require.NoError(t, err)
	w := sharedItem(t, l, "WRAPPED")
	require.False(t, w.IsSecret)
	require.True(t, w.ComputedSecret)
}

func TestSharedGeneratedValuesRunOnceAndRotate(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	l, err := f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("SESSION", "${{secret(16)}}"), sv("PLAIN", "v")})
	require.NoError(t, err)
	sess := sharedItem(t, l, "SESSION")
	require.Equal(t, services.SourceGenerated, sess.Source)
	require.True(t, sess.IsSecret, "secret() output is always secret")
	require.Nil(t, sess.Value)
	require.Len(t, sess.Generated, 1)
	first := f.projectValue("SESSION")
	require.Len(t, first, 16)
	require.NotContains(t, first, "$", "the stored value is the generated text, not the template")

	// The same template text again keeps the generated value.
	l, err = f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("SESSION", "${{secret(16)}}")})
	require.NoError(t, err)
	require.Equal(t, 1, sharedItem(t, l, "SESSION").Version)
	require.Equal(t, first, f.projectValue("SESSION"))

	// A generator cannot be forced to be non-secret.
	_, err = f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{
		{Key: "OTHER", Value: new("${{secret(8)}}"), IsSecret: new(false)},
	})
	require.Equal(t, "secret_required", apiCode(t, err))

	// Rotation of values that are not generated, or missing, is refused.
	_, err = f.svc.RotateShared(f.ctx, variables.LayerProject, f.prj.ID, "PLAIN", false)
	require.Equal(t, "not_rotatable", apiCode(t, err))
	_, err = f.svc.RotateShared(f.ctx, variables.LayerProject, f.prj.ID, "NOPE", false)
	require.Equal(t, "not_found", apiCode(t, err))

	_, err = f.svc.ShareShared(f.ctx, variables.LayerProject, f.prj.ID, "SESSION", variables.ShareSharedInput{ServiceIDs: []uuid.UUID{app.ID}})
	require.NoError(t, err)
	genBefore := f.gen(app.ID)

	// A dry run reports the plan and changes nothing.
	plan, err := f.svc.RotateShared(f.ctx, variables.LayerProject, f.prj.ID, "SESSION", true)
	require.NoError(t, err)
	require.False(t, plan.Staged)
	require.Equal(t, "SESSION", plan.Key)
	require.Equal(t, variables.KindAppSecret, plan.Kind)
	require.Len(t, plan.Affected, 1)
	require.Equal(t, "app", plan.Affected[0].Slug)
	require.Equal(t, []string{"SESSION"}, plan.Affected[0].Via)
	require.Equal(t, []string{"app"}, plan.DeployOrder)
	require.NotEmpty(t, plan.Warnings)
	require.Equal(t, first, f.projectValue("SESSION"))
	require.Equal(t, genBefore, f.gen(app.ID))
	require.Zero(t, f.auditCount("shared_variable.rotate", f.prj.ID))

	// Staging stores a new value, restages the consumer and audits it.
	plan, err = f.svc.RotateShared(f.ctx, variables.LayerProject, f.prj.ID, "SESSION", false)
	require.NoError(t, err)
	require.True(t, plan.Staged)
	second := f.projectValue("SESSION")
	require.Len(t, second, 16)
	require.NotEqual(t, first, second)
	require.Equal(t, genBefore+1, f.gen(app.ID))
	require.Equal(t, 1, f.auditCount("shared_variable.rotate", f.prj.ID))
	require.NotContains(t, mustJSON(t, f.auditMeta("shared_variable.rotate", f.prj.ID)), second)
	l, err = f.svc.ListShared(f.ctx, variables.LayerProject, f.prj.ID)
	require.NoError(t, err)
	require.Equal(t, 2, sharedItem(t, l, "SESSION").Version)
}

func TestSharedShareValidatesTargets(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	other := f.newEnv("staging")
	inOther := f.addSvc(other, "worker")
	otherPrj, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Blog", Slug: "blog"})
	require.NoError(t, err)
	otherPrjEnv, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: otherPrj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	foreign := f.addSvc(otherPrjEnv, "foreign")

	_, err = f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("P", "1")})
	require.NoError(t, err)
	_, err = f.svc.PutShared(f.ctx, variables.LayerEnvironment, f.env.ID, []variables.SharedVariableInput{sv("E", "1")})
	require.NoError(t, err)
	share := func(kind string, id uuid.UUID, key string, sids ...uuid.UUID) error {
		_, err := f.svc.ShareShared(f.ctx, kind, id, key, variables.ShareSharedInput{ServiceIDs: sids})
		return err
	}

	require.Equal(t, "not_found", apiCode(t, share(variables.LayerProject, f.prj.ID, "MISSING", app.ID)))
	require.Equal(t, "not_found", apiCode(t, share(variables.LayerProject, f.prj.ID, "P", foreign.ID)), "a service of another project")
	require.Equal(t, "not_found", apiCode(t, share(variables.LayerProject, f.prj.ID, "P", uuid.New())))
	require.Equal(t, "not_found", apiCode(t, share(variables.LayerEnvironment, f.env.ID, "E", inOther.ID)), "a service of another environment")
	require.Equal(t, "not_found", apiCode(t, share(variables.LayerEnvironment, f.env.ID, "MISSING", app.ID)))
	require.Empty(t, f.vars(app.ID), "a refused share leaves no partial write")

	// An environment can share a project value it does not override.
	require.NoError(t, share(variables.LayerEnvironment, f.env.ID, "P", app.ID))
	require.Equal(t, "${{shared.P}}", f.value(app.ID, "P"))
	// A service of any environment of the project takes a project value.
	require.NoError(t, share(variables.LayerProject, f.prj.ID, "P", inOther.ID))
	require.Equal(t, "${{shared.P}}", f.value(inOther.ID, "P"))

	// A viewer cannot write.
	viewer := f.as(auth.RoleViewer, nil, policy.ScopeProjectsRead)
	_, err = f.svc.ShareShared(viewer, variables.LayerProject, f.prj.ID, "P", variables.ShareSharedInput{ServiceIDs: []uuid.UUID{app.ID}})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, ae.Status)
}

func TestSharedTeamLayerAccessAndReferences(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	admin := f.as(auth.RoleAdmin, nil)
	dev := f.as(auth.RoleDeveloper, nil, policy.ScopeProjectsRead, policy.ScopeProjectsWrite)
	restricted := f.as(auth.RoleAdmin, []uuid.UUID{f.prj.ID})

	status := func(err error) int {
		t.Helper()
		ae, ok := apierr.As(err)
		require.True(t, ok, "not an API error: %v", err)
		return ae.Status
	}

	// Only owners and admins change team values.
	_, err := f.svc.PutShared(dev, variables.LayerTeam, uuid.Nil, []variables.SharedVariableInput{sv("ORG", "acme")})
	require.Equal(t, http.StatusForbidden, status(err))
	_, err = f.svc.DeleteShared(dev, variables.LayerTeam, uuid.Nil, "ORG")
	require.Equal(t, http.StatusForbidden, status(err))
	// A principal limited to some projects has no access to the team layer at all.
	_, err = f.svc.ListShared(restricted, variables.LayerTeam, uuid.Nil)
	require.Equal(t, http.StatusForbidden, status(err))
	_, err = f.svc.PutShared(restricted, variables.LayerTeam, uuid.Nil, []variables.SharedVariableInput{sv("ORG", "acme")})
	require.Equal(t, http.StatusForbidden, status(err))

	// Team values may only reference team values.
	_, err = f.svc.PutShared(admin, variables.LayerTeam, uuid.Nil, []variables.SharedVariableInput{sv("X", "${{shared.Y}}")})
	require.Equal(t, "invalid_shared_reference", apiCode(t, err))
	_, err = f.svc.PutShared(admin, variables.LayerTeam, uuid.Nil, []variables.SharedVariableInput{sv("X", "${{app.URL}}")})
	require.Equal(t, "invalid_shared_reference", apiCode(t, err))

	l, err := f.svc.PutShared(admin, variables.LayerTeam, uuid.Nil, []variables.SharedVariableInput{
		sv("ORG", "acme"), sv("ORG_URL", "https://${{team.ORG}}.example.com"),
	})
	require.NoError(t, err)
	org := sharedItem(t, l, "ORG")
	require.Equal(t, variables.LayerTeam, org.Layer)
	require.Equal(t, "acme", *org.Value)
	require.Equal(t, 1, f.auditCount("shared_variable.set", f.ws.ID))
	require.Equal(t, "team", f.auditMeta("shared_variable.set", f.ws.ID)["layer"])

	// The team listing is readable by a developer, and counts team references only.
	_, err = f.svc.ShareShared(admin, variables.LayerTeam, uuid.Nil, "ORG", variables.ShareSharedInput{ServiceIDs: []uuid.UUID{app.ID}})
	require.NoError(t, err)
	require.Equal(t, "${{team.ORG}}", f.value(app.ID, "ORG"))
	l, err = f.svc.ListShared(dev, variables.LayerTeam, uuid.Nil)
	require.NoError(t, err)
	require.Equal(t, []string{"ORG", "ORG_URL"}, sharedKeys(l))
	require.Equal(t, 1, sharedItem(t, l, "ORG").UsedBy)
	require.Equal(t, 0, sharedItem(t, l, "ORG_URL").UsedBy)

	// Restaging reaches the services that resolve to a changed team value.
	l, err = f.svc.PutShared(admin, variables.LayerTeam, uuid.Nil, []variables.SharedVariableInput{sv("ORG", "globex")})
	require.NoError(t, err)
	require.Equal(t, []string{"app"}, slugs(l.Restaged))

	hist, err := f.svc.SharedHistory(admin, variables.LayerTeam, uuid.Nil, "ORG")
	require.NoError(t, err)
	require.Len(t, hist, 2)

	l, err = f.svc.DeleteShared(admin, variables.LayerTeam, uuid.Nil, "ORG_URL")
	require.NoError(t, err)
	require.Equal(t, []string{"ORG"}, sharedKeys(l))
	_, err = f.svc.DeleteShared(admin, variables.LayerTeam, uuid.Nil, "ORG_URL")
	require.Equal(t, "not_found", apiCode(t, err))
}

func TestSharedTeamRotateNeedsAdmin(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	admin := f.as(auth.RoleOwner, nil)
	_, err := f.svc.PutShared(admin, variables.LayerTeam, uuid.Nil, []variables.SharedVariableInput{sv("KEY", "${{secret(12)}}")})
	require.NoError(t, err)
	_, err = f.svc.ShareShared(admin, variables.LayerTeam, uuid.Nil, "KEY", variables.ShareSharedInput{ServiceIDs: []uuid.UUID{app.ID}})
	require.NoError(t, err)

	dev := f.as(auth.RoleDeveloper, nil, policy.ScopeProjectsRead, policy.ScopeProjectsWrite)
	_, err = f.svc.RotateShared(dev, variables.LayerTeam, uuid.Nil, "KEY", false)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, ae.Status)

	genBefore := f.gen(app.ID)
	plan, err := f.svc.RotateShared(admin, variables.LayerTeam, uuid.Nil, "KEY", false)
	require.NoError(t, err)
	require.True(t, plan.Staged)
	require.Len(t, plan.Affected, 1)
	require.Equal(t, "app", plan.Affected[0].Slug)
	require.Equal(t, genBefore+1, f.gen(app.ID))
	require.Equal(t, 1, f.auditCount("shared_variable.rotate", f.ws.ID))
}

func TestSharedTxVariantsHonourTheCallersTransaction(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	_, err := f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("A", "1")})
	require.NoError(t, err)
	_, err = f.svc.ShareShared(f.ctx, variables.LayerProject, f.prj.ID, "A", variables.ShareSharedInput{ServiceIDs: []uuid.UUID{app.ID}})
	require.NoError(t, err)
	genBefore := f.gen(app.ID)

	// A rolled back PutSharedTx leaves the stored value and the generation alone.
	err = db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		restaged, after, err := f.svc.PutSharedTx(f.ctx, tx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("A", "2")})
		require.NoError(t, err)
		require.NotNil(t, after)
		require.Equal(t, []string{"app"}, slugs(restaged))
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	require.Equal(t, "1", f.projectValue("A"))
	require.Equal(t, genBefore, f.gen(app.ID))

	// DeleteSharedTx reports a missing variable as found=false without error.
	err = db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		found, restaged, after, err := f.svc.DeleteSharedTx(f.ctx, tx, variables.LayerProject, f.prj.ID, "MISSING")
		require.NoError(t, err)
		require.False(t, found)
		require.Empty(t, restaged)
		require.NotNil(t, after)
		found, restaged, after, err = f.svc.DeleteSharedTx(f.ctx, tx, variables.LayerProject, f.prj.ID, "A")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, []string{"app"}, slugs(restaged))
		require.NotNil(t, after)
		return nil
	})
	require.NoError(t, err)
	l, err := f.svc.ListShared(f.ctx, variables.LayerProject, f.prj.ID)
	require.NoError(t, err)
	require.Empty(t, l.Items)
}

func TestVariableUsageFindsReferences(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	web := f.addSvc(f.env, "web")
	job := f.addSvc(f.env, "job")
	_, err := f.svc.PutShared(f.ctx, variables.LayerProject, f.prj.ID, []variables.SharedVariableInput{sv("REGION", "eu")})
	require.NoError(t, err)
	_, err = f.svc.Put(f.ctx, app.ID, []variables.VariableInput{in("URL", "http://app"), in("COPY", "${{URL}}"), in("R", "${{shared.REGION}}")})
	require.NoError(t, err)
	_, err = f.svc.Put(f.ctx, web.ID, []variables.VariableInput{in("UPSTREAM", "${{app.URL}}"), in("R2", "r=${{shared.REGION}}")})
	require.NoError(t, err)
	_, err = f.svc.Put(f.ctx, job.ID, []variables.VariableInput{in("PLAIN", "x")})
	require.NoError(t, err)

	got, err := f.svc.VariableUsage(f.ctx, f.env.ID, "shared", "REGION")
	require.NoError(t, err)
	require.Equal(t, []variables.VariableUsage{
		{ServiceID: app.ID, Slug: "app", Key: "R"},
		{ServiceID: web.ID, Slug: "web", Key: "R2"},
	}, got, "sorted by service slug then key")

	got, err = f.svc.VariableUsage(f.ctx, f.env.ID, "", "REGION")
	require.NoError(t, err)
	require.Len(t, got, 2, "an empty namespace means shared")

	got, err = f.svc.VariableUsage(f.ctx, f.env.ID, "app", "URL")
	require.NoError(t, err)
	require.Equal(t, []variables.VariableUsage{
		{ServiceID: app.ID, Slug: "app", Key: "COPY"},
		{ServiceID: web.ID, Slug: "web", Key: "UPSTREAM"},
	}, got, "a service's own bare references count too")

	got, err = f.svc.VariableUsage(f.ctx, f.env.ID, "shared", "UNUSED")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)

	_, err = f.svc.VariableUsage(f.ctx, uuid.New(), "shared", "REGION")
	require.Equal(t, "not_found", apiCode(t, err))
}
