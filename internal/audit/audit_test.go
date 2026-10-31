package audit_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func TestViaMarker(t *testing.T) {
	bg := context.Background()
	require.Equal(t, "", audit.ViaFrom(bg))
	require.Equal(t, "nebula.toml", audit.ViaFrom(audit.WithVia(bg, "nebula.toml")))
}

func TestRecordAddsViaToMetadata(t *testing.T) {
	pool := testutil.NewPool(t)
	bg := context.Background()
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "a@example.com", Scopes: policy.AllScopes})

	record := func(ctx context.Context, action string, md map[string]any) {
		t.Helper()
		require.NoError(t, db.InTx(ctx, pool, func(tx pgx.Tx) error {
			return audit.Record(ctx, tx, audit.Entry{Action: action, ResourceType: "service", ResourceID: "r1", Metadata: md})
		}))
	}
	metadata := func(action string) map[string]any {
		t.Helper()
		var raw []byte
		require.NoError(t, pool.QueryRow(bg, "SELECT metadata FROM audit_events WHERE action = $1", action).Scan(&raw))
		out := map[string]any{}
		require.NoError(t, json.Unmarshal(raw, &out))
		return out
	}

	// Without a marker the metadata is stored as given.
	record(ctx, "t.plain", map[string]any{"k": "v"})
	require.Equal(t, map[string]any{"k": "v"}, metadata("t.plain"))

	// With a marker the row carries via; the caller's map is not modified.
	via := audit.WithVia(ctx, "nebula.toml")
	in := map[string]any{"k": "v"}
	record(via, "t.via", in)
	require.Equal(t, map[string]any{"k": "v", "via": "nebula.toml"}, metadata("t.via"))
	require.Equal(t, map[string]any{"k": "v"}, in, "Record must write on a copy")

	// Nil metadata still gets the marker.
	record(via, "t.nilmeta", nil)
	require.Equal(t, map[string]any{"via": "nebula.toml"}, metadata("t.nilmeta"))

	// A via key the entry already carries wins.
	record(via, "t.explicit", map[string]any{"via": "other"})
	require.Equal(t, map[string]any{"via": "other"}, metadata("t.explicit"))
}
