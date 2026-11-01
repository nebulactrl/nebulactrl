package servers_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type unrestrictedFixture struct {
	svc    *servers.Service
	pool   *pgxpool.Pool
	q      *db.Queries
	admin  context.Context
	member context.Context
	server db.Server
	env    uuid.UUID
}

func newUnrestrictedFixture(t *testing.T) (*unrestrictedFixture, *events.Bus) {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	first, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	second, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "b@example.com", Name: "B", PasswordHash: "x"})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "edge"})
	require.NoError(t, err)
	proj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "p"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: "prod", Slug: "prod"})
	require.NoError(t, err)

	bus := events.NewBus()
	return &unrestrictedFixture{
		svc: servers.NewService(pool, bus, nil, nil, nil), pool: pool, q: q, server: srv, env: env.ID,
		admin:  auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: first.ID, WorkspaceID: ws.ID, Scopes: policy.AllScopes}),
		member: auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: second.ID, WorkspaceID: ws.ID, Scopes: policy.AllScopes}),
	}, bus
}

// addService places a live (or deleted) service of kind on the fixture's server.
func (f *unrestrictedFixture) addService(t *testing.T, kind string, deleted bool) {
	t.Helper()
	f.addServiceConfig(t, kind, deleted, `{}`)
}

func (f *unrestrictedFixture) addServiceConfig(t *testing.T, kind string, deleted bool, config string) {
	t.Helper()
	name := uuid.NewString()[:8]
	source := "image"
	if kind == "stack" {
		source = "compose"
	}
	svc, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: f.env, Name: name, Slug: name, Kind: kind, SourceType: source, Config: json.RawMessage(config), ServerID: &f.server.ID,
	})
	require.NoError(t, err)
	if deleted {
		_, err = f.pool.Exec(context.Background(), "UPDATE services SET deleted_at = now() WHERE id = $1", svc.ID)
		require.NoError(t, err)
	}
}

func (f *unrestrictedFixture) audit(t *testing.T, action string) []map[string]any {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), "SELECT metadata FROM audit_events WHERE action = $1 ORDER BY id", action)
	require.NoError(t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		m := map[string]any{}
		require.NoError(t, json.Unmarshal(raw, &m))
		out = append(out, m)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestServerUnrestrictedDefaultsToRestricted(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	c, err := f.svc.Unrestricted(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, servers.UnrestrictedSettings{}, c)

	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.False(t, sv.Unrestricted)
	require.Empty(t, sv.ComposeVersion)
}

func TestServerUnrestrictedReportsCapabilityAndStacks(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	facts, err := json.Marshal(agentprotocol.Facts{ComposeVersion: "2.29.1", Capabilities: []string{agentprotocol.CapProxyV2, agentprotocol.CapCompose}})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(t, err)
	f.addService(t, "stack", false)
	f.addService(t, "stack", false)
	f.addService(t, "stack", true) // deleted: not counted
	f.addService(t, "app", false)  // not a stack

	c, err := f.svc.Unrestricted(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, servers.UnrestrictedSettings{ComposeVersion: "2.29.1", Available: true, Stacks: 2}, c)

	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, "2.29.1", sv.ComposeVersion)

	// An old plugin: version known, capability missing.
	facts, err = json.Marshal(agentprotocol.Facts{ComposeVersion: "2.20.0"})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET facts = $2 WHERE id = $1", f.server.ID, facts)
	require.NoError(t, err)
	c, err = f.svc.Unrestricted(f.admin, f.server.ID)
	require.NoError(t, err)
	require.False(t, c.Available)
	require.Equal(t, "2.20.0", c.ComposeVersion)
}

func TestServerUnrestrictedEnableDisableAuditsAndPublishes(t *testing.T) {
	f, bus := newUnrestrictedFixture(t)
	f.addService(t, "stack", false)
	evCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev := bus.Subscribe(evCtx)

	c, err := f.svc.SetUnrestricted(f.admin, f.server.ID, servers.UnrestrictedInput{Unrestricted: true, Confirm: "edge"})
	require.NoError(t, err)
	require.True(t, c.Unrestricted)
	require.Equal(t, 1, c.Stacks)
	require.Equal(t, "server.updated", (<-ev).Type)

	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.True(t, sv.Unrestricted, "the server view carries the flag")
	list, _, err := f.svc.List(f.admin, "", 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.True(t, list[0].Unrestricted, "and so does the list")

	require.Equal(t, []map[string]any{{"server": "edge", "stacks": float64(1), "services": float64(0)}}, f.audit(t, "server.unrestricted.enable"))

	// Disabling needs no confirmation.
	c, err = f.svc.SetUnrestricted(f.admin, f.server.ID, servers.UnrestrictedInput{Unrestricted: false})
	require.NoError(t, err)
	require.False(t, c.Unrestricted)
	require.Equal(t, 1, c.Stacks, "running stacks are untouched")
	require.Equal(t, []map[string]any{{"server": "edge", "stacks": float64(1), "services": float64(0)}}, f.audit(t, "server.unrestricted.disable"))
	sv, err = f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.False(t, sv.Unrestricted)
}

func TestServerUnrestrictedNeedsExactNameAndInstanceAdmin(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)

	for _, confirm := range []string{"", "Edge", "edge ", "other"} {
		_, err := f.svc.SetUnrestricted(f.admin, f.server.ID, servers.UnrestrictedInput{Unrestricted: true, Confirm: confirm})
		requireStatus(t, err, 422, "confirmation_mismatch")
	}

	// A second team member, even with every scope, is not the instance admin.
	_, err := f.svc.SetUnrestricted(f.member, f.server.ID, servers.UnrestrictedInput{Unrestricted: true, Confirm: "edge"})
	requireStatus(t, err, 403, "forbidden")
	_, err = f.svc.SetUnrestricted(f.member, f.server.ID, servers.UnrestrictedInput{Unrestricted: false})
	requireStatus(t, err, 403, "forbidden")

	// A token without the admin scope is not either, even for the first user.
	admin, err := policy.Authenticated(f.admin)
	require.NoError(t, err)
	tokenID := uuid.New()
	tokenCtx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, TokenID: &tokenID, UserID: admin.UserID, WorkspaceID: admin.WorkspaceID, Scopes: []string{policy.ScopeServersWrite, policy.ScopeServersRead},
	})
	_, err = f.svc.SetUnrestricted(tokenCtx, f.server.ID, servers.UnrestrictedInput{Unrestricted: true, Confirm: "edge"})
	requireStatus(t, err, 403, "forbidden")

	// Anyone who can read servers sees the state.
	c, err := f.svc.Unrestricted(f.member, f.server.ID)
	require.NoError(t, err)
	require.False(t, c.Unrestricted)

	require.Empty(t, f.audit(t, "server.unrestricted.enable"))
	sv, err := f.svc.Get(f.admin, f.server.ID)
	require.NoError(t, err)
	require.False(t, sv.Unrestricted)
}

func TestServerUnrestrictedUnknownServerIs404(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	_, err := f.svc.Unrestricted(f.admin, uuid.New())
	requireStatus(t, err, 404, "not_found")
	_, err = f.svc.SetUnrestricted(f.admin, uuid.New(), servers.UnrestrictedInput{Unrestricted: false})
	requireStatus(t, err, 404, "not_found")
}

func TestServerUnrestrictedCountsServicesThatNeedIt(t *testing.T) {
	f, _ := newUnrestrictedFixture(t)
	for _, cfg := range []string{
		`{"docker":{"privileged":true}}`,
		`{"docker":{"devices":[{"hostPath":"/dev/kvm"}]}}`,
		`{"docker":{"gpus":{"count":-1,"driver":"nvidia"}}}`,
		`{"docker":{"runtime":"runsc"}}`,
		`{"docker":{"pidHost":true}}`,
		`{"docker":{"seccompUnconfined":true}}`,
		`{"docker":{"apparmorUnconfined":true}}`,
		`{"docker":{"capAdd":["NET_ADMIN"]}}`,
		`{"docker":{"capAdd":["CHOWN","SYS_ADMIN"]}}`,
	} {
		f.addServiceConfig(t, "app", false, cfg)
	}
	// None of these needs it: the ungated tier, no options, false flags, an empty list, a deleted service, a stack.
	for _, cfg := range []string{
		`{}`,
		`{"docker":{"capDrop":["ALL"],"noNewPrivileges":true,"readOnlyRootfs":true,"pidsLimit":64,"hostname":"a"}}`,
		`{"docker":{"privileged":false,"devices":[],"capAdd":["NET_BIND_SERVICE"],"runtime":""}}`,
	} {
		f.addServiceConfig(t, "app", false, cfg)
	}
	f.addServiceConfig(t, "app", true, `{"docker":{"privileged":true}}`)
	f.addServiceConfig(t, "stack", false, `{"docker":{"privileged":true}}`)

	c, err := f.svc.Unrestricted(f.admin, f.server.ID)
	require.NoError(t, err)
	require.Equal(t, 9, c.DockerServices)
	require.Equal(t, 1, c.Stacks)

	_, err = f.svc.SetUnrestricted(f.admin, f.server.ID, servers.UnrestrictedInput{Unrestricted: true, Confirm: "edge"})
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{"server": "edge", "stacks": float64(1), "services": float64(9)}}, f.audit(t, "server.unrestricted.enable"),
		"the audit entry records how many services use unrestricted-only options")
}
