package variables_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/variables"
)

var errRollback = errors.New("rollback on purpose")

type fx struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	ctx  context.Context
	box  *secretbox.Box
	bus  *events.Bus
	svc  *variables.Service
	ws   db.Workspace
	prj  db.Project
	env  db.Environment
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production"})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	bus := events.NewBus()
	return &fx{
		t: t, pool: pool, q: q, box: box, bus: bus, ws: ws, prj: prj, env: env,
		svc: variables.NewService(pool, bus, box),
		ctx: auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "a@example.com", Scopes: policy.AllScopes}),
	}
}

func (f *fx) newEnv(slug string) db.Environment {
	f.t.Helper()
	env, err := f.q.CreateEnvironment(context.Background(), db.CreateEnvironmentParams{ProjectID: f.prj.ID, Name: slug, Slug: slug})
	require.NoError(f.t, err)
	return env
}

func (f *fx) addSvc(env db.Environment, slug string) db.Service {
	f.t.Helper()
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: "app", SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1","port":80}`),
	})
	require.NoError(f.t, err)
	return sv
}

func noop(context.Context) {}

// putTx runs PutTx on tx with a scope loaded through tx.
func (f *fx) putTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, in ...variables.VariableInput) (variables.PutResult, func(context.Context), error) {
	sc, err := services.LoadScope(ctx, db.New(tx), policy.ScopeProjectsWrite, id)
	if err != nil {
		return variables.PutResult{}, noop, err
	}
	return f.svc.PutTx(ctx, tx, sc, in)
}

func (f *fx) deleteTx(tx pgx.Tx, id uuid.UUID, key string) (bool, []variables.RestagedServiceRef, func(context.Context), error) {
	return f.deleteTxCtx(f.ctx, tx, id, key)
}

func (f *fx) deleteTxCtx(ctx context.Context, tx pgx.Tx, id uuid.UUID, key string) (bool, []variables.RestagedServiceRef, func(context.Context), error) {
	sc, err := services.LoadScope(ctx, db.New(tx), policy.ScopeProjectsWrite, id)
	if err != nil {
		return false, nil, noop, err
	}
	return f.svc.DeleteTx(ctx, tx, sc, key)
}

// commitPut commits one PutTx and returns its result and after-commit closure.
func (f *fx) commitPut(id uuid.UUID, in ...variables.VariableInput) (variables.PutResult, func(context.Context)) {
	f.t.Helper()
	var (
		res   variables.PutResult
		after func(context.Context)
	)
	require.NoError(f.t, db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		res, after, err = f.putTx(f.ctx, tx, id, in...)
		return err
	}))
	require.NotNil(f.t, after)
	return res, after
}

func in(key, value string) variables.VariableInput {
	return variables.VariableInput{Key: key, Value: new(value)}
}

type varRow struct {
	Key     string
	Version int32
	Secret  bool
	Source  string
	Scope   string
}

func (f *fx) vars(id uuid.UUID) []varRow {
	f.t.Helper()
	rows, err := f.q.ListServiceVariables(context.Background(), id)
	require.NoError(f.t, err)
	out := make([]varRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, varRow{Key: r.Key, Version: r.Version, Secret: r.IsSecret, Source: r.Source, Scope: r.Scope})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (f *fx) value(id uuid.UUID, key string) string {
	f.t.Helper()
	row, err := f.q.GetServiceVariable(context.Background(), db.GetServiceVariableParams{ServiceID: id, Key: key})
	require.NoError(f.t, err)
	v, err := f.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(id.String(), key), row.ValueEnc)
	require.NoError(f.t, err)
	return v
}

func (f *fx) gen(id uuid.UUID) int {
	f.t.Helper()
	var g int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), "SELECT generation FROM services WHERE id = $1", id).Scan(&g))
	return g
}

func (f *fx) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func (f *fx) auditCount(action string, resourceID uuid.UUID) int {
	f.t.Helper()
	return f.count("SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2", action, resourceID.String())
}

func (f *fx) auditMeta(action string, resourceID uuid.UUID) map[string]any {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		"SELECT metadata FROM audit_events WHERE action = $1 AND resource_id = $2 ORDER BY id DESC LIMIT 1", action, resourceID.String()).Scan(&raw))
	out := map[string]any{}
	require.NoError(f.t, json.Unmarshal(raw, &out))
	return out
}

func slugs(refs []variables.RestagedServiceRef) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.Slug)
	}
	sort.Strings(out)
	return out
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func drain(ch <-chan events.Event) []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

func TestPutTxRolledBackLeavesNothing(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	genBefore := f.gen(app.ID)
	evCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := f.bus.Subscribe(evCtx)

	err := db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		res, after, err := f.putTx(f.ctx, tx, app.ID, in("URL", "http://a"), variables.VariableInput{Key: "TOKEN", Value: new("s3cret"), IsSecret: new(true)})
		require.NoError(t, err)
		require.NotNil(t, after)
		require.Len(t, res.Changes, 2)
		// Inside the transaction the writes are visible.
		rows, err := db.New(tx).ListServiceVariables(f.ctx, app.ID)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)

	require.Empty(t, f.vars(app.ID))
	require.Equal(t, genBefore, f.gen(app.ID))
	require.Zero(t, f.count("SELECT count(*) FROM audit_events WHERE action = 'variable.set'"))
	require.Zero(t, f.count("SELECT count(*) FROM variable_versions WHERE owner_id = $1", app.ID))
	require.Empty(t, drain(ch), "nothing is published before the closure runs")
}

func TestPutTxAfterCommitClosurePublishes(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	evCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := f.bus.Subscribe(evCtx)

	res, after := f.commitPut(app.ID, in("URL", "http://a"))
	require.Len(t, res.Changes, 1)
	require.Empty(t, drain(ch), "PutTx itself never publishes")
	after(f.ctx)
	var types []string
	for _, e := range drain(ch) {
		types = append(types, e.Type)
	}
	require.Contains(t, types, services.EventVariableUpdated)
	require.Contains(t, types, services.EventServiceUpdated)

	// A no-op write returns a closure that publishes nothing.
	_, after = f.commitPut(app.ID, in("URL", "http://a"))
	require.NotNil(t, after)
	after(f.ctx)
	require.Empty(t, drain(ch))
}

// world is one environment: app exports URL, dep references it.
type world struct {
	app, dep db.Service
}

func (f *fx) newWorld(slug string) world {
	f.t.Helper()
	env := f.newEnv(slug)
	w := world{app: f.addSvc(env, "app"), dep: f.addSvc(env, "dep")}
	_, err := f.svc.Put(f.ctx, w.dep.ID, []variables.VariableInput{in("U", "${{app.URL}}")})
	require.NoError(f.t, err)
	return w
}

func TestPutTxMatchesPut(t *testing.T) {
	f := newFx(t)
	a, b := f.newWorld("env-a"), f.newWorld("env-b")
	inputs := []variables.VariableInput{
		in("URL", "http://a"),
		{Key: "TOKEN", Value: new("s3cret"), IsSecret: new(true), Comment: new("api token")},
		{Key: "BUILD_ONLY", Value: new("1"), Scope: new("runtime")},
	}
	genA, genB := f.gen(a.app.ID), f.gen(b.app.ID)
	depA, depB := f.gen(a.dep.ID), f.gen(b.dep.ID)
	require.Equal(t, genA, genB)
	require.Equal(t, depA, depB)

	list, err := f.svc.Put(f.ctx, a.app.ID, inputs)
	require.NoError(t, err)
	res, after := f.commitPut(b.app.ID, inputs...)
	require.NotNil(t, after)

	require.Equal(t, f.vars(a.app.ID), f.vars(b.app.ID))
	require.Equal(t, genA+1, f.gen(a.app.ID), "one bump for the whole request")
	require.Equal(t, genB+1, f.gen(b.app.ID))
	require.Equal(t, depA+1, f.gen(a.dep.ID), "the referencing service is restaged")
	require.Equal(t, depB+1, f.gen(b.dep.ID))
	require.Equal(t, slugs(list.Restaged), slugs(res.Restaged))
	require.Equal(t, []string{"dep"}, slugs(res.Restaged))
	require.Equal(t, 1, f.auditCount("variable.set", a.app.ID))
	require.Equal(t, 1, f.auditCount("variable.set", b.app.ID))
	ma, mb := f.auditMeta("variable.set", a.app.ID), f.auditMeta("variable.set", b.app.ID)
	require.Equal(t, ma["keys"], mb["keys"])
	require.Equal(t, ma["affected"], mb["affected"])
	require.Equal(t, []any{"BUILD_ONLY", "TOKEN", "URL"}, mb["keys"])
	require.Equal(t, "s3cret", f.value(b.app.ID, "TOKEN"))
	require.NotContains(t, mustJSON(t, mb), "s3cret", "audit never records a secret value")

	var keys []string
	for _, c := range res.Changes {
		require.Equal(t, services.OpSet, c.Op)
		require.EqualValues(t, 1, c.Version)
		keys = append(keys, c.Key)
	}
	require.Equal(t, []string{"BUILD_ONLY", "TOKEN", "URL"}, keys)

	// The same input again changes nothing: no bump, no audit, no restage.
	res, after = f.commitPut(b.app.ID, inputs...)
	require.Empty(t, res.Changes)
	require.Empty(t, res.Restaged)
	require.NotNil(t, after)
	require.Equal(t, genB+1, f.gen(b.app.ID))
	require.Equal(t, depB+1, f.gen(b.dep.ID))
	require.Equal(t, 1, f.auditCount("variable.set", b.app.ID))

	// Changing a referenced value bumps the service once more and restages dep.
	res, _ = f.commitPut(b.app.ID, in("URL", "http://b"))
	require.Len(t, res.Changes, 1)
	require.EqualValues(t, 2, res.Changes[0].Version)
	require.Equal(t, []string{"dep"}, slugs(res.Restaged))
	require.Equal(t, genB+2, f.gen(b.app.ID))
	require.Equal(t, depB+2, f.gen(b.dep.ID))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestPutTxManyCallsInOneTransactionRestageEachTime(t *testing.T) {
	f := newFx(t)
	w := f.newWorld("env-a")
	depGen := f.gen(w.dep.ID)
	require.NoError(t, db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		r1, _, err := f.putTx(f.ctx, tx, w.app.ID, in("URL", "http://one"))
		require.NoError(t, err)
		require.Equal(t, []string{"dep"}, slugs(r1.Restaged))
		r2, _, err := f.putTx(f.ctx, tx, w.dep.ID, in("OTHER", "x"))
		require.NoError(t, err)
		require.Empty(t, r2.Restaged)
		r3, _, err := f.putTx(f.ctx, tx, w.app.ID, in("URL", "http://two"))
		require.NoError(t, err)
		require.Equal(t, []string{"dep"}, slugs(r3.Restaged))
		return nil
	}))
	require.Equal(t, depGen+3, f.gen(w.dep.ID), "two restages plus its own write")
	require.Equal(t, "http://two", f.value(w.app.ID, "URL"))
}

func TestPutTxValidationMatchesPut(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	cases := map[string][]variables.VariableInput{
		"invalid_variable_key": {in("1BAD", "x")},
		"reserved_variable":    {in("PORT", "8080")},
		"duplicate_variable":   {in("A", "1"), in("A", "2")},
		"value_required":       {{Key: "NOVALUE"}},
		"invalid_template":     {in("T", "${{ secret( }}")},
		"invalid_scope":        {{Key: "S", Value: new("x"), Scope: new("everywhere")}},
		"too_many_variables":   manyInputs(501),
	}
	for code, inputs := range cases {
		t.Run(code, func(t *testing.T) {
			_, putErr := f.svc.Put(f.ctx, app.ID, inputs)
			require.Equal(t, code, apiCode(t, putErr))
			err := db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
				_, after, err := f.putTx(f.ctx, tx, app.ID, inputs...)
				require.NotNil(t, after, "the closure is never nil, even on error")
				return err
			})
			require.Equal(t, code, apiCode(t, err))
		})
	}
	require.Empty(t, f.vars(app.ID))
	require.Zero(t, f.auditCount("variable.set", app.ID))
}

func manyInputs(n int) []variables.VariableInput {
	out := make([]variables.VariableInput, n)
	for i := range out {
		out[i] = in("K"+string(rune('A'+i%26))+string(rune('A'+(i/26)%26))+string(rune('A'+(i/676)%26)), "v")
	}
	return out
}

func TestPutTxEmptySuggestedSecretIsCreatedOnceAndLeftAlone(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	input := variables.VariableInput{Key: "API_TOKEN", Value: new(""), IsSecret: new(true), Source: new(services.SourceSuggested)}

	res, _ := f.commitPut(app.ID, input)
	require.Len(t, res.Changes, 1)
	require.Empty(t, res.Warnings, "an empty value triggers no hygiene warning")
	require.Equal(t, []varRow{{Key: "API_TOKEN", Version: 1, Secret: true, Source: services.SourceSuggested, Scope: services.ScopeRuntime}}, f.vars(app.ID))
	require.Equal(t, "", f.value(app.ID, "API_TOKEN"))
	gen := f.gen(app.ID)
	require.Equal(t, 1, f.auditCount("variable.set", app.ID))

	// Same input again: unchanged row, no bump, no audit.
	res, _ = f.commitPut(app.ID, input)
	require.Empty(t, res.Changes)
	require.Equal(t, gen, f.gen(app.ID))
	require.Equal(t, 1, f.auditCount("variable.set", app.ID))
	require.Equal(t, []varRow{{Key: "API_TOKEN", Version: 1, Secret: true, Source: services.SourceSuggested, Scope: services.ScopeRuntime}}, f.vars(app.ID))

	// Through Put the variable list reports it as set, secret and without a value.
	list, err := f.svc.List(f.ctx, app.ID)
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.True(t, list.Items[0].IsSecret)
	require.True(t, list.Items[0].Set)
	require.Nil(t, list.Items[0].Value)
	require.Equal(t, services.SourceSuggested, list.Items[0].Source)
}

func TestPutTxGeneratorTemplateKeepsStoredValue(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")
	const tpl = "${{secret(32)}}"

	res, _ := f.commitPut(app.ID, in("SESSION_KEY", tpl))
	require.Len(t, res.Changes, 1)
	first := f.value(app.ID, "SESSION_KEY")
	require.Len(t, first, 32)
	rows := f.vars(app.ID)
	require.Equal(t, []varRow{{Key: "SESSION_KEY", Version: 1, Secret: true, Source: services.SourceGenerated, Scope: services.ScopeRuntime}}, rows)
	gen := f.gen(app.ID)

	// The same template text again keeps the generated value: no new version.
	res, _ = f.commitPut(app.ID, in("SESSION_KEY", tpl))
	require.Empty(t, res.Changes)
	require.Equal(t, first, f.value(app.ID, "SESSION_KEY"))
	require.Equal(t, rows, f.vars(app.ID))
	require.Equal(t, gen, f.gen(app.ID))
	require.Equal(t, 1, f.auditCount("variable.set", app.ID))

	// A different template regenerates.
	res, _ = f.commitPut(app.ID, in("SESSION_KEY", "${{secret(16)}}"))
	require.Len(t, res.Changes, 1)
	require.EqualValues(t, 2, res.Changes[0].Version)
	require.Len(t, f.value(app.ID, "SESSION_KEY"), 16)
	require.Equal(t, gen+1, f.gen(app.ID))
}

func TestPutTxAuditCarriesVia(t *testing.T) {
	f := newFx(t)
	app := f.addSvc(f.env, "app")

	f.commitPut(app.ID, in("PLAIN", "1"))
	require.NotContains(t, f.auditMeta("variable.set", app.ID), "via")

	via := audit.WithVia(f.ctx, "nebula.toml")
	require.NoError(t, db.InTx(via, f.pool, func(tx pgx.Tx) error {
		_, _, err := f.putTx(via, tx, app.ID, in("FROM_FILE", "1"))
		return err
	}))
	meta := f.auditMeta("variable.set", app.ID)
	require.Equal(t, "nebula.toml", meta["via"])
	require.Equal(t, []any{"FROM_FILE"}, meta["keys"])

	require.NoError(t, db.InTx(via, f.pool, func(tx pgx.Tx) error {
		_, _, _, err := f.deleteTxCtx(via, tx, app.ID, "PLAIN")
		return err
	}))
	require.Equal(t, "nebula.toml", f.auditMeta("variable.delete", app.ID)["via"])
}

func TestDeleteTx(t *testing.T) {
	f := newFx(t)
	w := f.newWorld("env-a")
	f.commitPut(w.app.ID, in("URL", "http://a"), in("OTHER", "1"))
	appGen, depGen := f.gen(w.app.ID), f.gen(w.dep.ID)

	// A missing variable: found=false, nothing written.
	require.NoError(t, db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		found, restaged, after, err := f.deleteTx(tx, w.app.ID, "NOPE")
		require.NoError(t, err)
		require.False(t, found)
		require.Empty(t, restaged)
		require.NotNil(t, after)
		return nil
	}))
	require.Equal(t, appGen, f.gen(w.app.ID))
	require.Zero(t, f.auditCount("variable.delete", w.app.ID))

	// Rolled back: the variable stays.
	err := db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		found, _, _, err := f.deleteTx(tx, w.app.ID, "URL")
		require.NoError(t, err)
		require.True(t, found)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	require.Len(t, f.vars(w.app.ID), 2)
	require.Equal(t, appGen, f.gen(w.app.ID))
	require.Zero(t, f.auditCount("variable.delete", w.app.ID))

	// Committed: gone, one bump, dependents restaged, one audit row.
	var (
		restaged []variables.RestagedServiceRef
		after    func(context.Context)
		found    bool
	)
	require.NoError(t, db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		found, restaged, after, err = f.deleteTx(tx, w.app.ID, "URL")
		return err
	}))
	require.True(t, found)
	require.NotNil(t, after)
	require.Equal(t, []string{"dep"}, slugs(restaged))
	require.Equal(t, []varRow{{Key: "OTHER", Version: 1, Source: services.SourceManual, Scope: services.ScopeRuntime}}, f.vars(w.app.ID))
	require.Equal(t, appGen+1, f.gen(w.app.ID))
	require.Equal(t, depGen+1, f.gen(w.dep.ID))
	require.Equal(t, 1, f.auditCount("variable.delete", w.app.ID))
	require.Equal(t, []any{"URL"}, f.auditMeta("variable.delete", w.app.ID)["keys"])

	// The non-Tx Delete still reports a missing variable as not found.
	require.Equal(t, "not_found", apiCode(t, f.svc.Delete(f.ctx, w.app.ID, "URL")))
	require.NoError(t, f.svc.Delete(f.ctx, w.app.ID, "OTHER"))
	require.Empty(t, f.vars(w.app.ID))
}

func TestSharedTxProjectAndEnvironmentLayers(t *testing.T) {
	for _, layer := range []string{variables.LayerProject, variables.LayerEnvironment} {
		t.Run(layer, func(t *testing.T) {
			f := newFx(t)
			otherEnv := f.newEnv("staging")
			dep := f.addSvc(f.env, "dep")
			stagingDep := f.addSvc(otherEnv, "dep")
			// Both services reference the shared key before it exists (a warning, not an error).
			for _, id := range []uuid.UUID{dep.ID, stagingDep.ID} {
				_, err := f.svc.Put(f.ctx, id, []variables.VariableInput{in("DB", "${{shared.DB_URL}}")})
				require.NoError(t, err)
			}
			id, resource := f.prj.ID, "project"
			if layer == variables.LayerEnvironment {
				id, resource = f.env.ID, "environment"
			}
			depGen, stagingGen := f.gen(dep.ID), f.gen(stagingDep.ID)
			sharedCount := func() int { return f.count("SELECT count(*) FROM shared_variables") }
			putShared := func(commit bool, v ...variables.SharedVariableInput) ([]variables.RestagedServiceRef, func(context.Context)) {
				var (
					restaged []variables.RestagedServiceRef
					after    func(context.Context)
				)
				err := db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
					var err error
					restaged, after, err = f.svc.PutSharedTx(f.ctx, tx, layer, id, v)
					if err == nil && !commit {
						return errRollback
					}
					return err
				})
				if commit {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, errRollback)
				}
				require.NotNil(t, after)
				return restaged, after
			}
			value := func(s string) variables.SharedVariableInput {
				return variables.SharedVariableInput{Key: "DB_URL", Value: new(s), IsSecret: new(true)}
			}

			// Rolled back: no row, no restage, no audit.
			restaged, _ := putShared(false, value("postgres://one"))
			require.Equal(t, []string{"dep"}, slugs(restaged)[:1], "the transaction sees the restage")
			require.Zero(t, sharedCount())
			require.Equal(t, depGen, f.gen(dep.ID))
			require.Zero(t, f.count("SELECT count(*) FROM audit_events WHERE action = 'shared_variable.set'"))

			// Committed: the referencing service(s) are restaged inside the transaction.
			restaged, after := putShared(true, value("postgres://one"))
			require.Equal(t, 1, sharedCount())
			require.Equal(t, depGen+1, f.gen(dep.ID))
			require.Contains(t, slugs(restaged), "dep")
			if layer == variables.LayerProject {
				require.Equal(t, stagingGen+1, f.gen(stagingDep.ID), "a project value reaches every environment")
				require.Len(t, restaged, 2)
			} else {
				require.Equal(t, stagingGen, f.gen(stagingDep.ID), "an environment value reaches only its environment")
				require.Len(t, restaged, 1)
			}
			require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'shared_variable.set' AND resource_type = $1", resource))
			var meta []byte
			require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT metadata FROM audit_events WHERE action = 'shared_variable.set'").Scan(&meta))
			var md map[string]any
			require.NoError(t, json.Unmarshal(meta, &md))
			require.Equal(t, layer, md["layer"])
			require.Contains(t, string(meta), "DB_URL")
			require.NotContains(t, string(meta), "postgres://one")

			evCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := f.bus.Subscribe(evCtx)
			require.Empty(t, drain(ch))
			after(f.ctx)
			var types []string
			for _, e := range drain(ch) {
				types = append(types, e.Type)
			}
			require.Contains(t, types, services.EventVariableUpdated)
			require.Contains(t, types, services.EventServiceUpdated)

			// The same value again: nothing changes.
			depNow := f.gen(dep.ID)
			restaged, after = putShared(true, value("postgres://one"))
			require.Empty(t, restaged)
			require.Equal(t, depNow, f.gen(dep.ID))
			require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'shared_variable.set'"))
			after(f.ctx)
			require.Empty(t, drain(ch), "a no-op write publishes nothing")

			// A new value restages again.
			restaged, _ = putShared(true, value("postgres://two"))
			require.Contains(t, slugs(restaged), "dep")
			require.Equal(t, depNow+1, f.gen(dep.ID))

			// Validation errors match PutShared.
			err := db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
				_, _, err := f.svc.PutSharedTx(f.ctx, tx, layer, id, []variables.SharedVariableInput{{Key: "bad-key", Value: new("x")}})
				return err
			})
			require.Equal(t, "invalid_variable_key", apiCode(t, err))
			_, err = f.svc.PutShared(f.ctx, layer, id, []variables.SharedVariableInput{{Key: "bad-key", Value: new("x")}})
			require.Equal(t, "invalid_variable_key", apiCode(t, err))

			// DeleteSharedTx: missing key reports found=false and writes nothing.
			depNow = f.gen(dep.ID)
			require.NoError(t, db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
				found, r, after, err := f.svc.DeleteSharedTx(f.ctx, tx, layer, id, "MISSING")
				require.NoError(t, err)
				require.False(t, found)
				require.Empty(t, r)
				require.NotNil(t, after)
				return nil
			}))
			require.Equal(t, depNow, f.gen(dep.ID))
			require.Zero(t, f.count("SELECT count(*) FROM audit_events WHERE action = 'shared_variable.delete'"))

			// Rolled back delete keeps the row.
			err = db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
				found, _, _, err := f.svc.DeleteSharedTx(f.ctx, tx, layer, id, "DB_URL")
				require.NoError(t, err)
				require.True(t, found)
				return errRollback
			})
			require.ErrorIs(t, err, errRollback)
			require.Equal(t, 1, sharedCount())
			require.Equal(t, depNow, f.gen(dep.ID))

			// Committed delete: row gone, referencing services restaged, audited.
			var (
				found     bool
				delAfter  func(context.Context)
				restaged2 []variables.RestagedServiceRef
			)
			require.NoError(t, db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
				var err error
				found, restaged2, delAfter, err = f.svc.DeleteSharedTx(f.ctx, tx, layer, id, "DB_URL")
				return err
			}))
			require.True(t, found)
			require.NotNil(t, delAfter)
			require.Zero(t, sharedCount())
			require.Contains(t, slugs(restaged2), "dep")
			require.Equal(t, depNow+1, f.gen(dep.ID))
			require.Equal(t, 1, f.count("SELECT count(*) FROM audit_events WHERE action = 'shared_variable.delete' AND resource_type = $1", resource))

			// DeleteShared still answers not found for a missing key.
			_, err = f.svc.DeleteShared(f.ctx, layer, id, "DB_URL")
			require.Equal(t, "not_found", apiCode(t, err))
		})
	}
}
