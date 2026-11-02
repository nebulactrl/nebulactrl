package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// release builds a release row of svc carrying snap, its source type and the
// sealed environment env (nil: no environment frozen).
func (f *txFixture) release(serviceID uuid.UUID, snap services.Snapshot, sourceType string, env *services.EnvSnapshot) db.Release {
	f.t.Helper()
	cfg, err := json.Marshal(snap)
	require.NoError(f.t, err)
	src, err := json.Marshal(services.ReleaseSource{Type: sourceType})
	require.NoError(f.t, err)
	rel := db.Release{ID: uuid.New(), ServiceID: serviceID, Config: cfg, Source: src}
	if env != nil {
		plain, err := json.Marshal(env)
		require.NoError(f.t, err)
		rel.EnvEnc, err = f.box().Seal(secretbox.ReleasesEnv, rel.ID.String(), plain)
		require.NoError(f.t, err)
	}
	return rel
}

func changesByField(cs []services.FieldChange) map[string]services.FieldChange {
	out := map[string]services.FieldChange{}
	for _, c := range cs {
		out[c.Field] = c
	}
	return out
}

func TestDiffReleaseListsVariableChangesAndMasksSecrets(t *testing.T) {
	f := newTxFixture(t)
	sv := f.imageService(f.prod.ID, "web")
	cfg := services.DecodeConfig(sv.Config)
	rel := f.release(sv.ID, services.Snapshot{Config: cfg}, services.SourceImage, &services.EnvSnapshot{
		Vars: map[string]string{},
		Raw: map[string]services.RawVar{
			"LOG":    {Value: "info"},
			"PASS":   {Value: "old-secret", Secret: true},
			"REMOVE": {Value: "bye"},
			"SCOPED": {Value: "x"},
			"SAME":   {Value: "same"},
		},
	})
	cur := services.CurrentState{Config: cfg, SourceType: services.SourceImage, EnvironmentID: f.prod.ID, Raw: map[string]services.RawVar{
		"LOG":    {Value: "debug"},
		"PASS":   {Value: "new-secret", Secret: true},
		"NEW":    {Value: "hello"},
		"TOKEN":  {Value: "abc", Secret: true, Gen: "secret(32)"},
		"SCOPED": {Value: "x", Scope: services.ScopeBuild},
		"SAME":   {Value: "same"},
	}}

	changes, err := services.DiffRelease(f.ctx, f.q, f.box(), rel, cur)
	require.NoError(t, err)
	by := changesByField(changes)
	require.Len(t, changes, 6, "SAME is not a change: %+v", changes)

	require.Equal(t, services.FieldChange{Field: "variables.LOG", From: "info", To: "debug", Origin: services.OriginDirect}, by["variables.LOG"])
	require.Equal(t, services.FieldChange{Field: "variables.PASS", From: services.Mask, To: services.Mask, Origin: services.OriginDirect}, by["variables.PASS"],
		"a changed secret shows masks on both sides, never the values")
	require.Equal(t, services.FieldChange{Field: "variables.NEW", To: "hello", Origin: services.OriginDirect}, by["variables.NEW"])
	require.Equal(t, services.FieldChange{Field: "variables.REMOVE", From: "bye", Origin: services.OriginDirect}, by["variables.REMOVE"])
	require.Equal(t, "generated · secret(32)", by["variables.TOKEN"].To, "a generated secret says how it is generated, not what it is")
	require.Equal(t, services.FieldChange{Field: "variables.SCOPED.scope", From: "runtime", To: "build"}, by["variables.SCOPED.scope"])

	counts := services.CountChanges(changes)
	require.Equal(t, services.PendingChangeCounts{Variables: 6}, counts)
	require.Equal(t, services.PendingChangeCounts{Variables: 1, Settings: 2},
		services.CountChanges([]services.FieldChange{{Field: "variables.A"}, {Field: "server"}, {Field: "config.port"}}))
}

func TestDiffReleaseReportsVariablesThatResolveDifferentlyEvenWhenTheirTextIsUnchanged(t *testing.T) {
	f := newTxFixture(t)
	sv := f.imageService(f.prod.ID, "web")
	cfg := services.DecodeConfig(sv.Config)
	rel := f.release(sv.ID, services.Snapshot{Config: cfg}, services.SourceImage, &services.EnvSnapshot{
		Vars: map[string]string{"DATABASE_URL": "postgres://old", "PLAIN": "p", "HIDDEN": "old"},
		Raw:  map[string]services.RawVar{"DATABASE_URL": {Value: "${{team.DB}}"}, "PLAIN": {Value: "p"}, "HIDDEN": {Value: "${{team.KEY}}", Secret: true}},
	})
	cur := services.CurrentState{
		Config: cfg, SourceType: services.SourceImage, EnvironmentID: f.prod.ID,
		Raw:      map[string]services.RawVar{"DATABASE_URL": {Value: "${{team.DB}}"}, "PLAIN": {Value: "p"}, "HIDDEN": {Value: "${{team.KEY}}", Secret: true}},
		Resolved: &services.Resolved{Vars: map[string]string{"DATABASE_URL": "postgres://new", "PLAIN": "p", "HIDDEN": "changed"}},
	}

	changes, err := services.DiffRelease(f.ctx, f.q, f.box(), rel, cur)
	require.NoError(t, err)
	by := changesByField(changes)
	require.Len(t, changes, 2, "%+v", changes)
	inh := by["variables.DATABASE_URL"]
	require.Equal(t, services.OriginInherited, inh.Origin)
	require.True(t, inh.ResolvedChanged)
	require.Equal(t, "${{team.DB}}", inh.From, "the text shown is the authored one")
	require.Equal(t, inh.From, inh.To)
	require.Equal(t, "team.DB", inh.Via, "it says which reference moved")
	require.Equal(t, services.Mask, by["variables.HIDDEN"].From, "an inherited secret is masked too")
	require.Equal(t, "team.KEY", by["variables.HIDDEN"].Via)

	// Without a resolved environment nothing can be said about inherited values.
	cur.Resolved = nil
	changes, err = services.DiffRelease(f.ctx, f.q, f.box(), rel, cur)
	require.NoError(t, err)
	require.Empty(t, changes)
}

func TestDiffReleaseServerEnvironmentAndSourceChanges(t *testing.T) {
	f := newTxFixture(t)
	sv := f.imageService(f.prod.ID, "web")
	a, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "edge-a"})
	require.NoError(t, err)
	b, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws.ID, Name: "edge-b"})
	require.NoError(t, err)
	cfg := services.DecodeConfig(sv.Config)
	gone := uuid.New()

	rel := f.release(sv.ID, services.Snapshot{Config: cfg, ServerID: &a.ID, EnvironmentID: &f.prod.ID}, services.SourceImage, nil)
	cur := services.CurrentState{Config: cfg, SourceType: services.SourceImage, ServerID: &b.ID, EnvironmentID: f.staging.ID}
	changes, err := services.DiffRelease(f.ctx, f.q, f.box(), rel, cur)
	require.NoError(t, err)
	by := changesByField(changes)
	require.Equal(t, services.FieldChange{Field: "server", From: "edge-a", To: "edge-b"}, by["server"])
	require.Equal(t, services.FieldChange{Field: "environment", From: "Shop / production", To: "Shop / staging"}, by["environment"])

	// A server that no longer exists is shown by id; no server at all by an empty name.
	rel = f.release(sv.ID, services.Snapshot{Config: cfg, ServerID: &gone, EnvironmentID: &gone}, services.SourceImage, nil)
	cur = services.CurrentState{Config: cfg, SourceType: services.SourceImage, EnvironmentID: f.staging.ID}
	changes, err = services.DiffRelease(f.ctx, f.q, f.box(), rel, cur)
	require.NoError(t, err)
	by = changesByField(changes)
	require.Equal(t, services.FieldChange{Field: "server", From: gone.String(), To: ""}, by["server"])
	require.Equal(t, gone.String(), by["environment"].From, "a deleted environment is shown by id")

	// Switching the source collapses the image and repository fields into one change.
	imgCfg := services.Config{Image: "traefik/whoami:latest", Port: 80}
	gitCfg := services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 80}
	rel = f.release(sv.ID, services.Snapshot{Config: imgCfg.WithDefaults()}, services.SourceImage, nil)
	cur = services.CurrentState{Config: gitCfg.WithDefaults(), SourceType: services.SourceGit}
	changes, err = services.DiffRelease(f.ctx, f.q, f.box(), rel, cur)
	require.NoError(t, err)
	require.NotEmpty(t, changes)
	require.Equal(t, services.FieldSource, changes[0].Field, "the source change comes first")
	for _, c := range changes[1:] {
		require.NotEqual(t, "config.image", c.Field)
		require.NotEqual(t, "config.gitUrl", c.Field)
		require.NotEqual(t, "config.gitBranch", c.Field)
	}
}

func TestDiffReleaseVolumesNetworksAndDomains(t *testing.T) {
	f := newTxFixture(t)
	sv := f.imageService(f.prod.ID, "web")
	cfg := services.DecodeConfig(sv.Config)
	netA, netB, netC := uuid.New(), uuid.New(), uuid.New()
	redirectOld, redirectNew := "https://a.example.com", "https://b.example.com"

	snap := services.Snapshot{
		Config: cfg,
		Volumes: []services.SnapshotVolume{
			{Name: "data", MountPath: "/data"},
			{Name: "cache", MountPath: "/cache"},
			{Name: "uploads", MountPath: "/uploads"},
			{Name: "logs", MountPath: "/logs"},
		},
		Networks: []services.SnapshotNetwork{{ID: netA, Name: "backend", Alias: "web.old"}, {ID: netB, Name: "shared", Alias: "web.shared"}},
		Domains: []services.SnapshotDomain{
			{Hostname: "app.example.com", TargetPort: 80},
			{Hostname: "old.example.com", TargetPort: 80},
			{Hostname: "go.example.com", TargetPort: 80, Kind: "redirect", RedirectTo: redirectOld, RedirectCode: 301, PreservePath: true},
			{Hostname: "locked.example.com", TargetPort: 80, AuthUsers: []string{"bob", "alice"}},
		},
	}
	rel := f.release(sv.ID, snap, services.SourceImage, nil)
	cur := services.CurrentState{
		Config: cfg, SourceType: services.SourceImage, EnvironmentID: f.prod.ID,
		Volumes: []db.Volume{
			{ServiceID: sv.ID, Name: "data", MountPath: "/data"},
			{ServiceID: sv.ID, Name: "cache", MountPath: "/cache2"},
			{ServiceID: sv.ID, Name: "uploads", MountPath: "/uploads", DockerName: "nc-restored-1", RestoreLabel: "restored from Jan 12"},
			{ServiceID: sv.ID, Name: "models", MountPath: "/models"},
		},
		Networks: []services.NetworkRef{{ID: netA, Name: "backend", Alias: "web.new"}, {ID: netC, Name: "extra", Alias: "web.extra"}},
		Domains: []db.Domain{
			{ID: uuid.New(), ServiceID: sv.ID, Hostname: "app.example.com", TargetPort: 80, Kind: "serve", TlsMode: "auto", RedirectCode: 308, AddPrefix: "/v2"},
			{ID: uuid.New(), ServiceID: sv.ID, Hostname: "go.example.com", TargetPort: 80, Kind: "redirect", TlsMode: "auto", RedirectCode: 308, RedirectTo: &redirectNew},
			{ID: uuid.New(), ServiceID: sv.ID, Hostname: "new.example.com", TargetPort: 80, Kind: "serve", TlsMode: "auto", RedirectCode: 308},
			{ID: uuid.New(), ServiceID: sv.ID, Hostname: "locked.example.com", TargetPort: 80, Kind: "serve", TlsMode: "none", RedirectCode: 308},
		},
	}

	changes, err := services.DiffRelease(f.ctx, f.q, f.box(), rel, cur)
	require.NoError(t, err)
	by := changesByField(changes)

	// Volumes.
	require.Equal(t, services.FieldChange{Field: "volumes.cache", From: "/cache", To: "/cache2"}, by["volumes.cache"])
	require.Equal(t, services.FieldChange{Field: "volumes.uploads", From: "/uploads", To: "/uploads · restored from Jan 12"}, by["volumes.uploads"], "a restored Docker volume is a change even with the same mount")
	require.Equal(t, services.FieldChange{Field: "volumes.models", To: "/models"}, by["volumes.models"])
	require.Equal(t, services.FieldChange{Field: "volumes.logs", From: "/logs"}, by["volumes.logs"])
	require.NotContains(t, by, "volumes.data")

	// Networks.
	require.Equal(t, services.FieldChange{Field: "networks.backend", From: "web.old", To: "web.new"}, by["networks.backend"])
	require.Equal(t, services.FieldChange{Field: "networks.extra", To: "web.extra"}, by["networks.extra"])
	require.Equal(t, services.FieldChange{Field: "networks.shared", From: "web.shared"}, by["networks.shared"])

	// Domains.
	require.Equal(t, services.FieldChange{Field: "domains.app.example.com.addPrefix", From: "", To: "/v2"}, by["domains.app.example.com.addPrefix"])
	require.Equal(t, services.FieldChange{Field: "domains.go.example.com.redirectTo", From: redirectOld, To: redirectNew}, by["domains.go.example.com.redirectTo"])
	require.Equal(t, services.FieldChange{Field: "domains.go.example.com.redirectCode", From: "301", To: "308"}, by["domains.go.example.com.redirectCode"])
	require.Equal(t, services.FieldChange{Field: "domains.go.example.com.preservePath", From: "true", To: "false"}, by["domains.go.example.com.preservePath"])
	require.Equal(t, services.FieldChange{Field: "domains.new.example.com", To: "added"}, by["domains.new.example.com"])
	require.Equal(t, services.FieldChange{Field: "domains.old.example.com", From: "attached"}, by["domains.old.example.com"])
	require.Equal(t, services.FieldChange{Field: "domains.locked.example.com.tlsMode", From: "auto", To: "none"}, by["domains.locked.example.com.tlsMode"])
	require.Equal(t, services.FieldChange{Field: "domains.locked.example.com.authUsers", From: "alice, bob", To: ""}, by["domains.locked.example.com.authUsers"], "auth users are compared as sorted names")
	require.Len(t, changes, 15, "%+v", changes)
	require.Equal(t, services.PendingChangeCounts{Settings: 15}, services.CountChanges(changes))
}

func TestReleaseEnvAndRawHandleMissingAndCorruptSnapshots(t *testing.T) {
	f := newTxFixture(t)
	box := f.box()
	sv := f.imageService(f.prod.ID, "web")
	cfg := services.DecodeConfig(sv.Config)

	bare := f.release(sv.ID, services.Snapshot{Config: cfg}, services.SourceImage, nil)
	env, err := services.ReleaseEnv(box, bare)
	require.NoError(t, err)
	require.Nil(t, env, "a release without a frozen environment has none")
	raw, err := services.ReleaseRaw(box, bare)
	require.NoError(t, err)
	require.Nil(t, raw)

	full := f.release(sv.ID, services.Snapshot{Config: cfg}, services.SourceImage, &services.EnvSnapshot{
		Vars: map[string]string{"A": "1"}, Raw: map[string]services.RawVar{"A": {Value: "1"}},
	})
	env, err = services.ReleaseEnv(box, full)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"A": "1"}, env.Vars)
	raw, err = services.ReleaseRaw(box, full)
	require.NoError(t, err)
	require.Equal(t, map[string]services.RawVar{"A": {Value: "1"}}, raw)

	// A snapshot sealed for another release does not open: it is an error, not an empty environment.
	moved := full
	moved.ID = uuid.New()
	_, err = services.ReleaseEnv(box, moved)
	require.ErrorContains(t, err, "decrypt release env")
	_, err = services.ReleaseRaw(box, moved)
	require.Error(t, err)
	_, err = services.DiffRelease(f.ctx, f.q, box, moved, services.CurrentState{Config: cfg})
	require.Error(t, err, "a diff against an unreadable snapshot fails instead of reporting every variable as new")

	garbage := db.Release{ID: uuid.New(), Config: bare.Config, Source: bare.Source}
	garbage.EnvEnc, err = box.Seal(secretbox.ReleasesEnv, garbage.ID.String(), []byte("not json"))
	require.NoError(t, err)
	_, err = services.ReleaseEnv(box, garbage)
	require.ErrorContains(t, err, "decode release env")
}

func TestLoadCurrentStateReadsVariablesVolumesAndResolvedValues(t *testing.T) {
	f := newTxFixture(t)
	box := f.box()
	sv := f.imageService(f.prod.ID, "web")
	sc := f.varScope(sv.ID)
	for _, w := range []services.VarWrite{
		{Key: "PLAIN", Value: "x"},
		{Key: "BUILD_ONLY", Value: "b", Scope: services.ScopeBuild},
		{Key: "SECRET", Value: "s3", Secret: true, Source: services.SourceGenerated, GenCalls: nil},
	} {
		_, err := services.WriteServiceVariable(f.ctx, f.q, box, sc, w)
		require.NoError(t, err)
	}
	_, err := f.pool.Exec(context.Background(), `UPDATE service_variables SET gen_calls = '[{"fn":"secret","args":["32"]}]' WHERE service_id = $1 AND key = 'SECRET'`, sv.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `INSERT INTO volumes (service_id, name, mount_path) VALUES ($1, 'data', '/data')`, sv.ID)
	require.NoError(t, err)

	row := f.row(sv.ID)
	cache := services.ResolverCache{}
	st, err := services.LoadCurrentStateCached(f.ctx, f.q, box, row, cache)
	require.NoError(t, err)
	require.Equal(t, services.SourceImage, st.SourceType)
	require.Equal(t, f.prod.ID, st.EnvironmentID)
	require.Len(t, st.Volumes, 1)
	require.Equal(t, "/data", st.Volumes[0].MountPath)
	require.Equal(t, services.RawVar{Value: "x"}, st.Raw["PLAIN"], "runtime scope is the default and is not spelled out")
	require.Equal(t, services.RawVar{Value: "b", Scope: services.ScopeBuild}, st.Raw["BUILD_ONLY"])
	require.Equal(t, services.RawVar{Value: "s3", Secret: true, Gen: "secret(32)"}, st.Raw["SECRET"])
	require.NotNil(t, st.Resolved, "the environment resolves")
	require.Equal(t, "x", st.Resolved.Vars["PLAIN"])
	require.Contains(t, cache, f.prod.ID, "the resolver is kept for the next service of the environment")

	again, err := services.LoadCurrentState(f.ctx, f.q, box, row)
	require.NoError(t, err)
	require.Equal(t, st.Raw, again.Raw)

	// A variable sealed under another key cannot be read: the state does not load.
	other, err := secretbox.New(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	_, err = services.LoadRawVars(f.ctx, f.q, other, sv.ID)
	require.ErrorContains(t, err, "decrypt variable")
}
