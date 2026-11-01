package services

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func uid() *uuid.UUID { id := uuid.New(); return &id }

func TestEffectivePort(t *testing.T) {
	a, b := uid(), uid()
	cases := []struct {
		name     string
		consumer *uuid.UUID
		p        ServiceData
		want     int
	}{
		{"local provider keeps its container port", a, ServiceData{Port: 8080, ServerID: a, MeshPort: 15001}, 8080},
		{"remote provider with an exposure uses the mesh port", a, ServiceData{Port: 8080, ServerID: b, MeshPort: 15001}, 15001},
		{"remote provider without an exposure keeps the container port", a, ServiceData{Port: 8080, ServerID: b}, 8080},
		{"unplaced consumer keeps the container port", nil, ServiceData{Port: 8080, ServerID: b, MeshPort: 15001}, 8080},
		{"unplaced provider keeps the container port", a, ServiceData{Port: 8080, MeshPort: 15001}, 8080},
	}
	for _, c := range cases {
		require.Equal(t, c.want, EffectivePort(c.consumer, c.p), c.name)
	}
}

// meshEnv is an environment with the api on server A, a postgres and a cache on
// server B (postgres exposed on 15001, the cache without a route) and a worker
// on A.
func meshEnv() (*Resolver, map[string]*uuid.UUID) {
	a, b := uid(), uid()
	svcs := []ServiceData{
		{ID: uuid.New(), Slug: "postgres", Name: "Postgres", Port: 5432, ServerID: b, MeshPort: 15001, Vars: map[string]VarDef{
			"POSTGRES_USER": {Value: "postgres"},
		}},
		{ID: uuid.New(), Slug: "cache", Name: "Cache", Port: 6379, ServerID: b, Vars: map[string]VarDef{}},
		{ID: uuid.New(), Slug: "local", Name: "Local", Port: 9000, ServerID: a, Vars: map[string]VarDef{}},
		{ID: uuid.New(), Slug: "api", Name: "API", Port: 8080, ServerID: a, Vars: map[string]VarDef{
			"DB_HOST":    {Value: "${{postgres.NEBULA_PRIVATE_HOST}}"},
			"DB_PORT":    {Value: "${{postgres.PORT}}"},
			"DB_USER":    {Value: "${{postgres.POSTGRES_USER}}"},
			"CACHE_PORT": {Value: "${{cache.PORT}}"},
			"LOCAL_PORT": {Value: "${{local.PORT}}"},
		}},
	}
	return NewResolver("production", svcs), map[string]*uuid.UUID{"a": a, "b": b}
}

func TestReferencesResolveThePortPerConsumerServer(t *testing.T) {
	r, _ := meshEnv()
	res, err := r.ResolveService("api")
	require.NoError(t, err)
	require.Equal(t, "postgres", res.Vars["DB_HOST"], "the host stays the provider's slug: it is the alias of the link node")
	require.Equal(t, "15001", res.Vars["DB_PORT"], "the remote provider is dialled at its mesh port")
	require.Equal(t, "6379", res.Vars["CACHE_PORT"], "no exposure yet: the container port")
	require.Equal(t, "9000", res.Vars["LOCAL_PORT"], "a provider on the consumer's server keeps its port")
	require.Equal(t, "8080", res.Vars[VarPort], "the service's own PORT is its container port")
	require.Equal(t, "postgres", res.Vars["DB_USER"])
}

func TestMeshUsesListRemoteProvidersAndPendingOnes(t *testing.T) {
	r, servers := meshEnv()
	res, probs, err := r.ResolveServiceLenient("api")
	require.NoError(t, err)
	require.Len(t, res.MeshUses, 2, "postgres and cache are remote, local is not")
	require.Equal(t, "cache", res.MeshUses[0].ProviderSlug)
	require.True(t, res.MeshUses[0].Pending)
	require.Equal(t, "postgres", res.MeshUses[1].ProviderSlug)
	require.False(t, res.MeshUses[1].Pending)
	require.Equal(t, 15001, res.MeshUses[1].MeshPort)
	require.Equal(t, *servers["b"], res.MeshUses[1].ProviderServer)
	require.Equal(t, []string{"DB_HOST", "DB_PORT"}, res.MeshUses[1].Keys)

	require.Len(t, probs, 1, "a remote provider without a route is a lenient problem")
	require.Equal(t, CodeMeshRoutePending, probs[0].Code)
	require.Equal(t, true, probs[0].Details["warning"], "it is a warning in the staged review")
	require.Equal(t, "CACHE_PORT", probs[0].Key)
}

func TestSingleServerEnvironmentIgnoresMeshPorts(t *testing.T) {
	a := uid()
	pgID, apiID := uuid.New(), uuid.New()
	mk := func(server *uuid.UUID, meshPort int) *Resolver {
		return NewResolver("production", []ServiceData{
			{ID: pgID, Slug: "postgres", Port: 5432, ServerID: server, MeshPort: meshPort, Vars: map[string]VarDef{"POSTGRES_USER": {Value: "pg"}}},
			{ID: apiID, Slug: "api", Port: 8080, ServerID: server, Vars: map[string]VarDef{
				"DB": {Value: "${{postgres.NEBULA_PRIVATE_HOST}}:${{postgres.PORT}}/${{postgres.POSTGRES_USER}}"},
			}},
		})
	}
	// A stale mesh port on a provider of the consumer's own server changes nothing.
	got, err := mk(a, 15001).ResolveService("api")
	require.NoError(t, err)
	want, err := mk(nil, 0).ResolveService("api")
	require.NoError(t, err)
	require.Equal(t, "postgres:5432/pg", got.Vars["DB"])
	require.Equal(t, want.Vars, got.Vars)
	require.Empty(t, got.MeshUses)
	_, probs, err := mk(a, 0).ResolveServiceLenient("api")
	require.NoError(t, err)
	require.Empty(t, probs)
}

func TestExportsSeeTheConsumerAndTheEffectivePort(t *testing.T) {
	prev := currentExportProvider()
	t.Cleanup(func() { RegisterExportProvider(prev) })
	RegisterExportProvider(func(in ExportInput) []Export {
		if in.Slug != "postgres" {
			return nil
		}
		consumer := "unplaced"
		if in.Consumer != nil {
			consumer = in.Consumer.String()
		}
		return []Export{{Key: "DATABASE_URL", Value: fmt.Sprintf("postgres://%s:%d/%s", in.Host, in.Port, consumer)}}
	})
	_, servers := meshEnv()
	a, b := servers["a"], servers["b"]
	build := func(consumerServer *uuid.UUID) Resolved {
		res, err := NewResolver("production", []ServiceData{
			{ID: uuid.New(), Slug: "postgres", Port: 5432, ServerID: b, MeshPort: 15001, Vars: map[string]VarDef{}},
			{ID: uuid.New(), Slug: "api", Port: 8080, ServerID: consumerServer, Vars: map[string]VarDef{"U": {Value: "${{postgres.DATABASE_URL}}"}}},
		}).ResolveService("api")
		require.NoError(t, err)
		return res
	}
	require.Equal(t, fmt.Sprintf("postgres://postgres:15001/%s", a), build(a).Vars["U"], "a consumer on another server dials the mesh port")
	require.Equal(t, fmt.Sprintf("postgres://postgres:5432/%s", b), build(b).Vars["U"], "a consumer on the provider's server dials the container port")
	require.Equal(t, "postgres://postgres:5432/unplaced", build(nil).Vars["U"])
	require.Len(t, build(a).MeshUses, 1)
	require.Empty(t, build(b).MeshUses)
}

func TestExportTargetsPickReadersPerConsumerServer(t *testing.T) {
	a, b, c := uid(), uid(), uid()
	primary := GroupMember{ID: uuid.New(), Slug: "pg", IsPrimary: true, Port: 5432, ServerID: a}
	local := GroupMember{ID: uuid.New(), Slug: "pg-r1", State: "streaming", Port: 5432, ServerID: b}
	remote := GroupMember{ID: uuid.New(), Slug: "pg-r2", State: "lagging", Port: 5432, ServerID: c, MeshPort: 15002}
	broken := GroupMember{ID: uuid.New(), Slug: "pg-r3", State: "broken", Port: 5432, ServerID: b}
	group := []GroupMember{primary, local, remote, broken}

	require.Equal(t, []uuid.UUID{local.ID}, ExportTargets("DATABASE_READ_URL", b, group, primary.ID), "replicas on the consumer's server win")
	require.ElementsMatch(t, []uuid.UUID{local.ID, remote.ID}, ExportTargets("DATABASE_READ_HOSTS", a, group, primary.ID), "no local replica: all remote readers")
	require.Equal(t, []uuid.UUID{primary.ID}, ExportTargets("DATABASE_READ_URL", a, []GroupMember{primary, broken}, primary.ID), "no reader: the writer")
	require.Equal(t, []uuid.UUID{primary.ID}, ExportTargets("DATABASE_WRITE_URL", b, group, remote.ID), "the writer is the primary wherever the consumer runs")
	require.Equal(t, []uuid.UUID{remote.ID}, ExportTargets("DATABASE_URL", a, group, remote.ID), "other exports describe the service itself")

	require.Equal(t, 15002, MemberPort(a, remote), "a remote member with an exposure")
	require.Equal(t, 5432, MemberPort(c, remote), "the member's own server")
	require.Equal(t, 5432, MemberPort(a, local), "no exposure yet")
	require.Equal(t, 5432, MemberPort(nil, remote), "unplaced consumer")
}
