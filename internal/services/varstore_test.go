package services_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

type version struct {
	Version     int32
	Op          string
	IsSecret    bool
	Source      string
	Scope       string
	ValueEnc    []byte
	Fingerprint string
	ActorType   string
	ActorID     *string
	GenCalls    []byte
}

func (f *txFixture) box() *secretbox.Box {
	f.t.Helper()
	box, err := secretbox.New(adapterKey)
	require.NoError(f.t, err)
	return box
}

// versions lists a variable's history rows, oldest first.
func (f *txFixture) versions(ownerID uuid.UUID, key string) []version {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT version, op, is_secret, source, scope, value_enc, fingerprint, actor_type, actor_id, gen_calls
		   FROM variable_versions WHERE owner_id = $1 AND key = $2 ORDER BY id`, ownerID, key)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []version
	for rows.Next() {
		var v version
		require.NoError(f.t, rows.Scan(&v.Version, &v.Op, &v.IsSecret, &v.Source, &v.Scope, &v.ValueEnc, &v.Fingerprint, &v.ActorType, &v.ActorID, &v.GenCalls))
		out = append(out, v)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func (f *txFixture) varScope(id uuid.UUID) services.Scope {
	f.t.Helper()
	sc, err := services.LoadScope(f.ctx, f.q, policy.ScopeProjectsWrite, id)
	require.NoError(f.t, err)
	return sc
}

func TestWriteServiceVariableCreatesUpdatesAndSkipsNoOps(t *testing.T) {
	f := newTxFixture(t)
	box := f.box()
	sv := f.imageService(f.prod.ID, "web")
	sc := f.varScope(sv.ID)

	// A new variable defaults to a manual runtime variable and records version 1.
	res, err := services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "LOG_LEVEL", Value: "info"})
	require.NoError(t, err)
	require.True(t, res.Created)
	require.True(t, res.Changed)
	require.False(t, res.HadOld)
	require.Equal(t, services.ScopeRuntime, res.Row.Scope)
	require.Equal(t, services.SourceManual, res.Row.Source)
	require.Equal(t, box.Fingerprint("info"), res.NewFP)
	require.NotContains(t, string(res.Row.ValueEnc), "info", "the value is stored sealed")

	vs := f.versions(sv.ID, "LOG_LEVEL")
	require.Len(t, vs, 1)
	require.EqualValues(t, 1, vs[0].Version)
	require.Equal(t, services.OpSet, vs[0].Op)
	require.False(t, vs[0].IsSecret)
	plain, err := box.OpenString(secretbox.VariableVersionsValue, secretbox.RowID("service", sv.ID.String(), "LOG_LEVEL", "1"), vs[0].ValueEnc)
	require.NoError(t, err)
	require.Equal(t, "info", plain, "plain variables keep their value in the history")
	require.Equal(t, "user", vs[0].ActorType)
	require.NotNil(t, vs[0].ActorID)

	// Writing the same thing again changes nothing and adds no history.
	res, err = services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "LOG_LEVEL", Value: "info"})
	require.NoError(t, err)
	require.False(t, res.Changed)
	require.False(t, res.Created)
	require.True(t, res.HadOld)
	require.Len(t, f.versions(sv.ID, "LOG_LEVEL"), 1)

	// A new value bumps the version, reports the replaced one and keeps scope and source.
	res, err = services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "LOG_LEVEL", Value: "debug"})
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.False(t, res.Created)
	require.True(t, res.HadOld)
	require.Equal(t, "info", res.OldValue)
	require.Equal(t, box.Fingerprint("info"), res.OldFP)
	require.Equal(t, box.Fingerprint("debug"), res.NewFP)
	vs = f.versions(sv.ID, "LOG_LEVEL")
	require.Len(t, vs, 2)
	require.EqualValues(t, 2, vs[1].Version)

	// Metadata alone is a change too: the value is the same but the comment differs.
	note := "verbosity of the app"
	res, err = services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "LOG_LEVEL", Value: "debug", Comment: &note})
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.Equal(t, note, res.Row.Comment)
	// Leaving the comment nil keeps it.
	res, err = services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "LOG_LEVEL", Value: "debug"})
	require.NoError(t, err)
	require.False(t, res.Changed)
	require.Equal(t, note, res.Row.Comment)
}

func TestSecretVariablesNeverEnterTheHistoryInTheClear(t *testing.T) {
	f := newTxFixture(t)
	box := f.box()
	sv := f.imageService(f.prod.ID, "web")
	sc := f.varScope(sv.ID)

	_, err := services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "DB_PASSWORD", Value: "hunter2", Secret: true, Scope: services.ScopeBuild})
	require.NoError(t, err)
	vs := f.versions(sv.ID, "DB_PASSWORD")
	require.Len(t, vs, 1)
	require.True(t, vs[0].IsSecret)
	require.Nil(t, vs[0].ValueEnc, "a secret's value is not kept in the history, only its fingerprint")
	require.Equal(t, box.Fingerprint("hunter2"), vs[0].Fingerprint)
	require.Equal(t, services.ScopeBuild, vs[0].Scope)

	// Flipping only the secret flag is a change.
	res, err := services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "DB_PASSWORD", Value: "hunter2", Secret: false})
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.True(t, res.OldSecret)
	require.Equal(t, services.ScopeBuild, res.Row.Scope, "the scope of the existing row is kept")
}

func TestGeneratedVariablesRecordTheirTemplateCallsAndRotation(t *testing.T) {
	f := newTxFixture(t)
	box := f.box()
	sv := f.imageService(f.prod.ID, "web")
	sc := f.varScope(sv.ID)
	calls := []varexpr.GeneratorCall{{Fn: "secret", Args: []string{"32"}}, {Fn: "randomBytes", Args: []string{"8", "hex"}}}
	days := 30

	res, err := services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{
		Key: "TOKEN", Value: "abc123", Secret: true, Source: services.SourceGenerated, Template: "${secret(32)}${randomBytes(8, hex)}",
		GenCalls: calls, RotateEveryDays: &days, Op: services.OpRotate,
	})
	require.NoError(t, err)
	require.NotNil(t, res.Row.GeneratedAt, "a generated value is stamped")
	require.NotNil(t, res.Row.RotateEveryDays)
	require.EqualValues(t, 30, *res.Row.RotateEveryDays)
	tmpl, err := box.OpenString(secretbox.ServiceVariablesTemplate, secretbox.RowID(sv.ID.String(), "TOKEN"), res.Row.TemplateEnc)
	require.NoError(t, err)
	require.Equal(t, "${secret(32)}${randomBytes(8, hex)}", tmpl, "the template is kept (sealed) so the value can be rotated")

	vs := f.versions(sv.ID, "TOKEN")
	require.Equal(t, services.OpRotate, vs[0].Op)
	require.Equal(t, services.SourceGenerated, vs[0].Source)
	require.Equal(t, "secret(32) + randomBytes(8, hex)", services.GenDescriptor(vs[0].GenCalls))

	// The same value with the same template is a no-op, and the generation time stays.
	again, err := services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{
		Key: "TOKEN", Value: "abc123", Secret: true, Source: services.SourceGenerated, Template: "${secret(32)}${randomBytes(8, hex)}",
		GenCalls: calls, RotateEveryDays: &days,
	})
	require.NoError(t, err)
	require.False(t, again.Changed)

	// A metadata change with the same value keeps the generation time.
	zero := 0
	same, err := services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{
		Key: "TOKEN", Value: "abc123", Secret: true, Source: services.SourceGenerated, Template: "${secret(32)}${randomBytes(8, hex)}",
		GenCalls: calls, RotateEveryDays: &zero,
	})
	require.NoError(t, err)
	require.True(t, same.Changed)
	require.Nil(t, same.Row.RotateEveryDays, "zero days turns rotation off")
	require.WithinDuration(t, *res.Row.GeneratedAt, *same.Row.GeneratedAt, 0, "the value did not change, so it was not regenerated")
}

func TestDeleteServiceVariableVersionedLeavesATombstone(t *testing.T) {
	f := newTxFixture(t)
	box := f.box()
	sv := f.imageService(f.prod.ID, "web")
	sc := f.varScope(sv.ID)

	gone, ok, err := services.DeleteServiceVariableVersioned(f.ctx, f.q, box, sc, "MISSING")
	require.NoError(t, err)
	require.False(t, ok, "deleting what does not exist is not an error")
	require.Empty(t, gone.Key)
	require.Empty(t, f.versions(sv.ID, "MISSING"))

	_, err = services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "API_KEY", Value: "k-1", Secret: true})
	require.NoError(t, err)
	row, ok, err := services.DeleteServiceVariableVersioned(f.ctx, f.q, box, sc, "API_KEY")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "API_KEY", row.Key)
	require.True(t, row.IsSecret)

	_, err = f.q.GetServiceVariable(f.ctx, db.GetServiceVariableParams{ServiceID: sv.ID, Key: "API_KEY"})
	require.Error(t, err, "the variable is gone")
	vs := f.versions(sv.ID, "API_KEY")
	require.Len(t, vs, 2)
	require.Equal(t, services.OpDelete, vs[1].Op)
	require.EqualValues(t, 2, vs[1].Version)
	require.True(t, vs[1].IsSecret, "the tombstone remembers it was a secret")
	require.Equal(t, box.Fingerprint("k-1"), vs[1].Fingerprint)

	// Writing it again continues the numbering.
	res, err := services.WriteServiceVariable(f.ctx, f.q, box, sc, services.VarWrite{Key: "API_KEY", Value: "k-2", Secret: true})
	require.NoError(t, err)
	require.True(t, res.Created)
	require.False(t, res.HadOld)
	require.EqualValues(t, 3, f.versions(sv.ID, "API_KEY")[2].Version)
}

func TestVariableHistoryNamesTheActor(t *testing.T) {
	f := newTxFixture(t)
	box := f.box()
	sv := f.imageService(f.prod.ID, "web")
	sc := f.varScope(sv.ID)
	p, _ := auth.PrincipalFrom(f.ctx)
	tokenID := uuid.New()

	for _, c := range []struct {
		name string
		ctx  context.Context
		typ  string
		id   string
	}{
		{"user", f.ctx, "user", p.UserID.String()},
		{"mcp", auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeMCP, UserID: p.UserID, TokenID: &tokenID, WorkspaceID: p.WorkspaceID, Scopes: policy.AllScopes}), "mcp", tokenID.String()},
		{"token", auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeToken, UserID: p.UserID, TokenID: &tokenID, WorkspaceID: p.WorkspaceID, Scopes: policy.AllScopes}), "token", tokenID.String()},
		{"system", auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeSystem}), "system", ""},
		{"nobody", context.Background(), "system", ""},
	} {
		key := "VAR_" + c.name
		_, err := services.WriteServiceVariable(c.ctx, f.q, box, sc, services.VarWrite{Key: key, Value: "v"})
		require.NoError(t, err, c.name)
		vs := f.versions(sv.ID, key)
		require.Len(t, vs, 1, c.name)
		require.Equal(t, c.typ, vs[0].ActorType, c.name)
		if c.id == "" {
			require.Nil(t, vs[0].ActorID, c.name)
		} else {
			require.NotNil(t, vs[0].ActorID, c.name)
			require.Equal(t, c.id, *vs[0].ActorID, c.name)
		}
	}
}

func TestRecordSharedVersionKeepsNoProjectForTeamVariables(t *testing.T) {
	f := newTxFixture(t)
	box := f.box()
	owner := uuid.New()

	require.NoError(t, services.RecordSharedVersion(f.ctx, f.q, box, f.ws.ID, uuid.Nil, services.OwnerTeam, owner, "REGION", 1, services.OpSet, false, services.SourceManual, "eu", nil))
	require.NoError(t, services.RecordSharedVersion(f.ctx, f.q, box, f.ws.ID, f.project.ID, services.OwnerProject, owner, "REGION", 2, services.OpSet, true, services.SourceManual, "us", json.RawMessage(`[{"fn":"secret","args":["8"]}]`)))

	var projects []*uuid.UUID
	rows, err := f.pool.Query(f.ctx, `SELECT project_id FROM variable_versions WHERE owner_id = $1 ORDER BY version`, owner)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var p *uuid.UUID
		require.NoError(t, rows.Scan(&p))
		projects = append(projects, p)
	}
	require.Len(t, projects, 2)
	require.Nil(t, projects[0], "team variables belong to no project")
	require.NotNil(t, projects[1])
	require.Equal(t, f.project.ID, *projects[1])
	vs := f.versions(owner, "REGION")
	require.NotNil(t, vs[0].ValueEnc)
	require.Nil(t, vs[1].ValueEnc, "a secret's value is not kept")
}

func TestGenDescriptor(t *testing.T) {
	require.Equal(t, "secret(32)", services.GenDescriptor(json.RawMessage(`[{"fn":"secret","args":["32"]}]`)))
	require.Equal(t, "secret(32) + randomBytes(8, hex)", services.GenDescriptor(json.RawMessage(`[{"fn":"secret","args":["32"]},{"fn":"randomBytes","args":["8","hex"]}]`)))
	require.Equal(t, "uuid()", services.GenDescriptor(json.RawMessage(`[{"fn":"uuid","args":[]}]`)))
	for _, empty := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`[]`), json.RawMessage(`{not json`), json.RawMessage(`null`)} {
		require.Empty(t, services.GenDescriptor(empty), string(empty))
	}
}
