package clone_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/clone"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

func requireClone(t *testing.T, err error, code string) {
	t.Helper()
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status, ae.Message)
	require.Equal(t, code, ae.Code)
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func TestCloneServiceRefusesWhatItCannotCopy(t *testing.T) {
	f := newFixture(t)
	stack := f.insert(f.prod.ID, "shop-stack", services.KindStack, "compose", services.Config{})
	replica := f.insert(f.prod.ID, "pg-replica", services.KindApp, services.SourceImage, services.Config{
		Image: "postgres:17", Database: &engines.Spec{Engine: "postgres", Version: "17", Replica: &engines.ReplicaSpec{PrimaryServiceID: f.postgres.ID.String()}},
	})

	_, err := f.cl.CloneService(f.ctx, stack.ID, clone.ServiceInput{})
	requireClone(t, err, "not_applicable")
	require.ErrorContains(t, err, "Compose stacks are not cloned")

	_, err = f.cl.CloneService(f.ctx, replica.ID, clone.ServiceInput{})
	requireClone(t, err, "not_applicable")
	require.ErrorContains(t, err, "read replica")
}

func TestCloneServiceValidatesItsInput(t *testing.T) {
	f := newFixture(t)
	before := f.count("SELECT count(*) FROM services")

	for _, in := range map[string]clone.ServiceInput{
		"name over 100 characters": {Name: strings.Repeat("n", 101)},
		"unknown domain mode":      {Domains: "keep"},
		"suffix with a dot":        {Domains: clone.DomainsSuffix, DomainSuffix: "-a.b"},
		"suffix with a space":      {Domains: clone.DomainsSuffix, DomainSuffix: "x y"},
	} {
		_, err := f.cl.CloneService(f.ctx, f.api.ID, in)
		requireClone(t, err, "validation_failed")
	}
	require.Equal(t, before, f.count("SELECT count(*) FROM services"))

	// A suffix without its leading hyphen gets one, and is lowercased.
	f.domain(f.api.ID, "api.shop.example.com", "")
	res, err := f.cl.CloneService(f.ctx, f.api.ID, clone.ServiceInput{Domains: clone.DomainsSuffix, DomainSuffix: " Blue "})
	require.NoError(t, err)
	require.Len(t, res.Domains, 1)
	require.Equal(t, "api-blue.shop.example.com", res.Domains[0].To)
}

func TestCloneServiceInTheSameEnvironmentIsNamedCopy(t *testing.T) {
	f := newFixture(t)
	res, err := f.cl.CloneService(f.ctx, f.web.ID, clone.ServiceInput{})
	require.NoError(t, err)
	require.Equal(t, "web copy", res.Service.Name)
	require.Equal(t, "web-copy", res.Service.Slug)

	named, err := f.cl.CloneService(f.ctx, f.web.ID, clone.ServiceInput{Name: "  Blue web  "})
	require.NoError(t, err)
	require.Equal(t, "Blue web", named.Service.Name, "surrounding spaces are trimmed")
}

func TestCloningFromOrIntoThePlatformProjectIsRefused(t *testing.T) {
	f := newFixture(t)
	other, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.ws.ID, Name: "Platform", Slug: "platform"})
	require.NoError(t, err)
	otherEnv, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: other.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	sys := f.insert(otherEnv.ID, "headscale", services.KindApp, services.SourceImage, services.Config{Image: "headscale:1", Port: 8080})
	f.exec(`UPDATE projects SET system = true WHERE id = $1`, other.ID)

	_, err = f.cl.CloneService(f.ctx, sys.ID, clone.ServiceInput{})
	requireClone(t, err, "not_applicable")
	_, err = f.cl.CloneService(f.ctx, f.web.ID, clone.ServiceInput{EnvironmentID: &otherEnv.ID})
	requireClone(t, err, "not_applicable")
	_, err = f.cl.CloneEnvironment(f.ctx, otherEnv.ID, clone.EnvironmentInput{Name: "copy"})
	requireClone(t, err, "not_applicable")
	_, err = f.cl.MoveService(f.ctx, sys.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	requireClone(t, err, "not_applicable")
	_, err = f.cl.MoveService(f.ctx, f.web.ID, clone.MoveInput{EnvironmentID: otherEnv.ID})
	requireClone(t, err, "not_applicable")
}

func TestCloneEnvironmentValidatesItsInput(t *testing.T) {
	f := newFixture(t)
	envs := f.count("SELECT count(*) FROM environments")

	for _, tc := range map[string]struct {
		in   clone.EnvironmentInput
		code string
	}{
		"no name":             {clone.EnvironmentInput{Name: "  "}, "validation_failed"},
		"long name":           {clone.EnvironmentInput{Name: strings.Repeat("n", 101)}, "validation_failed"},
		"reserved slug":       {clone.EnvironmentInput{Name: "Settings"}, "slug_reserved"},
		"unknown domain mode": {clone.EnvironmentInput{Name: "QA", Domains: "keep"}, "validation_failed"},
		"bad suffix":          {clone.EnvironmentInput{Name: "QA", Domains: clone.DomainsSuffix, DomainSuffix: "a b"}, "validation_failed"},
	} {
		_, err := f.cl.CloneEnvironment(f.ctx, f.prod.ID, tc.in)
		requireClone(t, err, tc.code)
	}
	require.Equal(t, envs, f.count("SELECT count(*) FROM environments"), "a refused clone leaves no half-made environment")

	f.exec(`UPDATE environments SET kind = 'preview' WHERE id = $1`, f.staging.ID)
	_, err := f.cl.CloneEnvironment(f.ctx, f.staging.ID, clone.EnvironmentInput{Name: "QA"})
	requireClone(t, err, "preview_environment")
}

func TestCloneEnvironmentListsEveryServiceItLeavesOut(t *testing.T) {
	f := newFixture(t)
	f.insert(f.prod.ID, "edge", services.KindRoute, services.SourceExternal, services.Config{})
	f.insert(f.prod.ID, "shop-stack", services.KindStack, "compose", services.Config{})
	f.insert(f.prod.ID, "pg-replica", services.KindApp, services.SourceImage, services.Config{
		Image: "postgres:17", Database: &engines.Spec{Engine: "postgres", Version: "17", Replica: &engines.ReplicaSpec{PrimaryServiceID: f.postgres.ID.String()}},
	})

	res, err := f.cl.CloneEnvironment(f.ctx, f.prod.ID, clone.EnvironmentInput{Name: "QA"})
	require.NoError(t, err)
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Slug] = s.Reason
	}
	require.Len(t, reasons, 3)
	require.Contains(t, reasons["edge"], "route services")
	require.Contains(t, reasons["shop-stack"], "Compose stacks")
	require.Contains(t, reasons["pg-replica"], "read replica")
	require.Len(t, f.envServices(res.Environment.ID), 3, "postgres, api and web")
}

func TestCloneEnvironmentRegeneratesGeneratedSharedVariables(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	tmpl := "${{secret(24)}}"
	m, err := varexpr.Materialize(tmpl)
	require.NoError(t, err)
	calls, err := json.Marshal(m.Calls)
	require.NoError(t, err)
	id := uuid.New()
	value, err := f.box.SealString(secretbox.SharedVariablesValue, id.String(), m.Stored)
	require.NoError(t, err)
	templateEnc, err := f.box.SealString(secretbox.SharedVariablesTemplate, id.String(), tmpl)
	require.NoError(t, err)
	require.NoError(t, f.q.CopySharedVariable(bg, db.CopySharedVariableParams{
		ID: id, WorkspaceID: f.ws.ID, ProjectID: &f.project.ID, EnvironmentID: &f.prod.ID, Key: "SESSION_KEY", ValueEnc: value, IsSecret: true,
		Source: services.SourceGenerated, TemplateEnc: templateEnc, GenCalls: calls, Comment: "signs sessions",
	}))

	res, err := f.cl.CloneEnvironment(f.ctx, f.prod.ID, clone.EnvironmentInput{Name: "QA"})
	require.NoError(t, err)
	require.Equal(t, 1, res.SharedVariables)

	var newID uuid.UUID
	var newValue, newTemplate []byte
	var comment string
	var secret bool
	require.NoError(t, f.pool.QueryRow(bg,
		`SELECT id, value_enc, template_enc, comment, is_secret FROM shared_variables WHERE environment_id = $1 AND key = 'SESSION_KEY'`, res.Environment.ID).
		Scan(&newID, &newValue, &newTemplate, &comment, &secret))
	require.NotEqual(t, id, newID)
	require.Equal(t, "signs sessions", comment)
	require.True(t, secret)
	gotValue, err := f.box.OpenString(secretbox.SharedVariablesValue, newID.String(), newValue)
	require.NoError(t, err)
	require.NotEqual(t, m.Stored, gotValue, "a generated value is generated again, not copied")
	require.Len(t, gotValue, len(m.Stored))
	gotTemplate, err := f.box.OpenString(secretbox.SharedVariablesTemplate, newID.String(), newTemplate)
	require.NoError(t, err)
	require.Equal(t, tmpl, gotTemplate)
}

// ---- move ----

func (f *fixture) sharedVar(env *uuid.UUID, key, value string) {
	f.t.Helper()
	id := uuid.New()
	enc, err := f.box.SealString(secretbox.SharedVariablesValue, id.String(), value)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.CopySharedVariable(context.Background(), db.CopySharedVariableParams{
		ID: id, WorkspaceID: f.ws.ID, ProjectID: &f.project.ID, EnvironmentID: env, Key: key, ValueEnc: enc, Source: services.SourceManual,
	}))
}

func TestMovePlanReportsOnlyReferencesThatBreak(t *testing.T) {
	f := newFixture(t)
	// The api reads a shared variable that only production has, one that both have, an
	// unparseable value, a reference that was already broken and one written twice.
	f.sharedVar(&f.prod.ID, "PROD_ONLY", "1")
	f.sharedVar(nil, "EVERYWHERE", "1")
	f.setVar(f.api.ID, "NEEDS_PROD", "${{shared.PROD_ONLY}}", false)
	f.setVar(f.api.ID, "NEEDS_ALL", "${{shared.EVERYWHERE}}", false)
	f.setVar(f.api.ID, "TEAM", "${{team.SOMETHING}}", false)
	f.setVar(f.api.ID, "ALREADY_BROKEN", "${{ghost.PORT}}", false)
	f.setVar(f.api.ID, "TWICE", "${{postgres.PORT}}:${{postgres.PORT}}", false)

	plan, err := f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID, DryRun: true})
	require.NoError(t, err)
	found := map[string][]clone.ReferenceFinding{}
	for _, o := range plan.Outgoing {
		found[o.Key] = append(found[o.Key], o)
	}
	require.Len(t, found["NEEDS_PROD"], 1)
	require.Equal(t, clone.RefUnresolved, found["NEEDS_PROD"][0].Status)
	require.Equal(t, "shared.PROD_ONLY", found["NEEDS_PROD"][0].Ref)
	require.Contains(t, found["NEEDS_PROD"][0].Message, "does not exist in staging")
	require.Len(t, found["TWICE"], 1, "the same reference twice in one value is reported once")
	for _, quiet := range []string{"NEEDS_ALL", "TEAM", "ALREADY_BROKEN", "SELF_PORT"} {
		require.NotContains(t, found, quiet, quiet)
	}
	require.True(t, plan.Blocked)
}

func TestMovingAServiceWithAGeneratedVariableRewritesItsTemplate(t *testing.T) {
	f := newFixture(t)
	bg := context.Background()
	// Staging has its own api, so the moved one is renamed and its own references follow.
	f.insert(f.staging.ID, "api", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})
	tmpl := "${{api.PORT}}-${{secret(8)}}"
	m, err := varexpr.Materialize(tmpl)
	require.NoError(t, err)
	sc, err := services.LoadScopeInternal(bg, f.q, f.api.ID)
	require.NoError(t, err)
	_, err = services.WriteServiceVariable(bg, f.q, f.box, sc, services.VarWrite{
		Key: "CALLBACK_TOKEN", Value: m.Stored, Secret: true, Source: services.SourceGenerated, Template: tmpl, GenCalls: m.Calls,
	})
	require.NoError(t, err)

	res, err := f.cl.MoveService(f.ctx, f.api.ID, clone.MoveInput{EnvironmentID: f.staging.ID, Force: true})
	require.NoError(t, err)
	require.Equal(t, "api-2", res.Slug)
	require.True(t, res.SlugChanged)

	var enc, tmplEnc []byte
	require.NoError(t, f.pool.QueryRow(bg, `SELECT value_enc, template_enc FROM service_variables WHERE service_id = $1 AND key = 'CALLBACK_TOKEN'`, f.api.ID).Scan(&enc, &tmplEnc))
	value, err := f.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(f.api.ID.String(), "CALLBACK_TOKEN"), enc)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(value, "${{api-2.PORT}}-"), value)
	template, err := f.box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(f.api.ID.String(), "CALLBACK_TOKEN"), tmplEnc)
	require.NoError(t, err)
	require.Equal(t, "${{api-2.PORT}}-${{secret(8)}}", template, "the template follows the rename, so a regenerated value does too")
}

func TestMoveIntoOrOutOfProductionNeedsApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t)
	g := f.installGuard()
	rest := f.restToken()
	lone := f.insert(f.staging.ID, "lone", services.KindApp, services.SourceImage, services.Config{Image: "traefik/whoami:latest", Port: 80})

	// Into production.
	_, askErr := f.cl.MoveService(rest, lone.ID, clone.MoveInput{EnvironmentID: f.prod.ID})
	ar, ok := prodguard.AsRequired(askErr)
	require.True(t, ok, "got %v", askErr)
	require.Contains(t, ar.Plan.Summary, "Move lone into production")
	require.Equal(t, f.staging.ID, f.row(lone.ID).EnvironmentID, "nothing moved before the approval")

	// A dry run is a plan, not a change: nobody is asked.
	plan, err := f.cl.MoveService(rest, lone.ID, clone.MoveInput{EnvironmentID: f.prod.ID, DryRun: true})
	require.NoError(t, err)
	require.True(t, plan.DryRun)

	f.approve(g, askErr)
	res, err := f.cl.MoveService(rest, lone.ID, clone.MoveInput{EnvironmentID: f.prod.ID})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, f.prod.ID, f.row(lone.ID).EnvironmentID)

	// Out of production.
	_, err = f.cl.MoveService(rest, lone.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	ar, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Move lone out of production")
	require.Equal(t, f.prod.ID, f.row(lone.ID).EnvironmentID)

	// A person in the browser is never asked.
	_, err = f.cl.MoveService(f.browser(), lone.ID, clone.MoveInput{EnvironmentID: f.staging.ID})
	require.NoError(t, err)
}

// ---- HTTP ----

func TestHTTPCloneAndMove(t *testing.T) {
	f := newFixture(t)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p, _ := auth.PrincipalFrom(f.ctx)
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
		})
	})
	cfg := huma.DefaultConfig("test", "1")
	cfg.CreateHooks = nil
	clone.Register(humachi.New(r, cfg), f.cl)
	do := func(path string, body any) (int, map[string]any) {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var m map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
		return rec.Code, m
	}

	// A dry run plans and changes nothing.
	before := f.count("SELECT count(*) FROM services")
	status, body := do("/services/"+f.web.ID.String()+"/clone", map[string]any{"dryRun": true})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, true, body["dryRun"])
	require.Len(t, body["services"], 1)
	require.Equal(t, before, f.count("SELECT count(*) FROM services"))

	status, body = do("/services/"+f.web.ID.String()+"/clone", map[string]any{"name": "Web two"})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, "Web two", body["service"].(map[string]any)["name"])

	status, body = do("/services/"+f.web.ID.String()+"/clone", map[string]any{"domains": "suffix", "domainSuffix": "a.b"})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "validation_failed", body["code"])

	status, body = do("/environments/"+f.prod.ID.String()+"/clone", map[string]any{"name": "QA"})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, "qa", body["environment"].(map[string]any)["slug"])
	status, body = do("/environments/"+f.prod.ID.String()+"/clone", map[string]any{"name": "Settings"})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "slug_reserved", body["code"])

	status, body = do("/services/"+f.api.ID.String()+"/move", map[string]any{"environmentId": f.staging.ID.String()})
	require.Equal(t, http.StatusConflict, status, body)
	require.Equal(t, "move_blocked", body["code"])
	status, body = do("/services/"+f.api.ID.String()+"/move", map[string]any{"environmentId": f.staging.ID.String(), "dryRun": true})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, true, body["blocked"])
	status, body = do("/services/"+f.api.ID.String()+"/move", map[string]any{"environmentId": f.staging.ID.String(), "force": true})
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, true, body["applied"])
	status, body = do("/services/"+f.api.ID.String()+"/move", map[string]any{"environmentId": f.staging.ID.String()})
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "same_environment", body["code"])
}
