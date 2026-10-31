package deployments

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

func TestOrderByReferences(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	edge := func(from, to uuid.UUID) services.RefEdge { return services.RefEdge{From: from, To: to} }

	// No references: input order.
	require.Equal(t, []uuid.UUID{a, b, c}, OrderByReferences([]uuid.UUID{a, b, c}, nil))

	// a → b → c: providers first.
	require.Equal(t, []uuid.UUID{c, b, a}, OrderByReferences([]uuid.UUID{a, b, c}, []services.RefEdge{edge(a, b), edge(b, c)}))

	// Edges to services outside the batch are ignored.
	require.Equal(t, []uuid.UUID{a, b}, OrderByReferences([]uuid.UUID{a, b}, []services.RefEdge{edge(a, d)}))

	// Independent services keep their relative order around a dependency.
	require.Equal(t, []uuid.UUID{b, a, c}, OrderByReferences([]uuid.UUID{a, b, c}, []services.RefEdge{edge(a, b)}))

	// A cycle keeps the input order for its members; the rest is still ordered.
	require.Equal(t, []uuid.UUID{c, a, b}, OrderByReferences([]uuid.UUID{a, b, c}, []services.RefEdge{edge(a, b), edge(b, a), edge(b, c)}),
		"c is placed first (b needs it), then the unresolvable cycle in input order")
	require.Equal(t, []uuid.UUID{a, b}, OrderByReferences([]uuid.UUID{a, b}, []services.RefEdge{edge(a, b), edge(b, a)}))
}

func TestRevertConfigPath(t *testing.T) {
	snap := services.Config{Image: "img:1", Port: 80, MemoryMB: 256}.WithDefaults()
	cur := services.Config{Image: "img:2", Port: 8080, MemoryMB: 512}.WithDefaults()
	cur.Healthcheck.Path = "/up"

	got, err := RevertConfigPath(cur, snap, "port")
	require.NoError(t, err)
	require.Equal(t, 80, got.Port)
	require.Equal(t, "img:2", got.Image, "other fields stay")
	require.Equal(t, 512, got.MemoryMB)

	got, err = RevertConfigPath(cur, snap, "healthcheck.path")
	require.NoError(t, err)
	require.Equal(t, snap.Healthcheck.Path, got.Healthcheck.Path)
	require.Equal(t, 8080, got.Port)

	// A field the snapshot does not carry is cleared.
	snap.MemoryMB = 0
	got, err = RevertConfigPath(cur, snap, "memoryMb")
	require.NoError(t, err)
	require.Equal(t, 0, got.MemoryMB)
}

func TestCleanMessage(t *testing.T) {
	m, err := cleanMessage("  hello ")
	require.NoError(t, err)
	require.Equal(t, "hello", m)
	_, err = cleanMessage(string(make([]rune, 0)))
	require.NoError(t, err)
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'x'
	}
	_, err = cleanMessage(string(long))
	require.Error(t, err)
}
