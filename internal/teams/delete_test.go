package teams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// fakeTeardown records every call in order. Servers answer with outcomes[name].
type fakeTeardown struct {
	mu       sync.Mutex
	calls    []string
	failOn   string
	outcomes map[uuid.UUID]string
	names    map[uuid.UUID]string
}

func (f *fakeTeardown) rec(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.failOn == call {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeTeardown) DeleteProject(_ context.Context, id uuid.UUID) error {
	return f.rec("project:" + id.String()[:8])
}
func (f *fakeTeardown) RemovePlatformServices(context.Context) error { return f.rec("platform") }
func (f *fakeTeardown) RemoveServer(_ context.Context, id uuid.UUID) (string, error) {
	if err := f.rec("server:" + f.names[id]); err != nil {
		return "", err
	}
	return f.outcomes[id], nil
}
func (f *fakeTeardown) DeleteStatusPages(context.Context) error { return f.rec("statuspages") }
func (f *fakeTeardown) DisconnectCloudflare(_ context.Context, keep bool) error {
	return f.rec(fmt.Sprintf("cloudflare:keep=%v", keep))
}
func (f *fakeTeardown) DisconnectTailscale(context.Context) error { return f.rec("tailscale") }

type deleteFixture struct {
	svc  *Service
	q    *db.Queries
	td   *fakeTeardown
	team db.Workspace
	srvs map[string]uuid.UUID
}

func newDeleteFixture(t *testing.T) *deleteFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ctx := context.Background()
	team, err := q.CreateWorkspace(ctx, "Doomed")
	require.NoError(t, err)
	for _, n := range []string{"alpha", "beta"} {
		_, err = q.CreateProject(ctx, db.CreateProjectParams{WorkspaceID: team.ID, Name: n, Slug: n})
		require.NoError(t, err)
	}
	_, err = q.CreateSystemProject(ctx, db.CreateSystemProjectParams{WorkspaceID: team.ID, Name: "Platform", Slug: "platform"})
	require.NoError(t, err)
	td := &fakeTeardown{outcomes: map[uuid.UUID]string{}, names: map[uuid.UUID]string{}}
	srvs := map[string]uuid.UUID{}
	for name, outcome := range map[string]string{"online-1": ServerCleaned, "offline-1": ServerForgotten} {
		s, err := q.CreateServer(ctx, db.CreateServerParams{WorkspaceID: team.ID, Name: name})
		require.NoError(t, err)
		td.outcomes[s.ID], td.names[s.ID] = outcome, name
		srvs[name] = s.ID
	}
	svc := NewService(Deps{Pool: pool, Logger: slog.Default()})
	svc.SetTeardown(td)
	_, err = q.StartTeamDeletion(ctx, db.StartTeamDeletionParams{ID: team.ID, KeepDns: true})
	require.NoError(t, err)
	return &deleteFixture{svc: svc, q: q, td: td, team: team, srvs: srvs}
}

func (f *deleteFixture) exists(t *testing.T) bool {
	t.Helper()
	_, err := f.q.GetWorkspace(context.Background(), f.team.ID)
	return err == nil
}

func TestTeamDeletionRunsTheCascadeInOrder(t *testing.T) {
	f := newDeleteFixture(t)
	require.NoError(t, f.svc.RunDeletion(context.Background(), f.team.ID))

	// Projects (the system project is the platform step's), then the platform
	// services, then the servers in creation order, then the integrations.
	calls := f.td.calls
	require.Len(t, calls, 8, strings.Join(calls, ","))
	require.True(t, strings.HasPrefix(calls[0], "project:"))
	require.True(t, strings.HasPrefix(calls[1], "project:"))
	require.NotEqual(t, calls[0], calls[1])
	require.Equal(t, []string{"platform"}, calls[2:3])
	servers := calls[3:5]
	require.ElementsMatch(t, []string{"server:online-1", "server:offline-1"}, servers)
	require.Equal(t, []string{"statuspages", "cloudflare:keep=true", "tailscale"}, calls[5:8])
	require.False(t, f.exists(t), "the team goes last")
}

func TestTeamDeletionReportsForgottenServers(t *testing.T) {
	f := newDeleteFixture(t)
	f.td.failOn = "statuspages"
	require.NoError(t, f.svc.RunDeletion(context.Background(), f.team.ID))
	ws, err := f.q.GetWorkspace(context.Background(), f.team.ID)
	require.NoError(t, err)
	require.Equal(t, DeletionFailed, ws.DeletionState)
	require.Equal(t, []string{"offline-1"}, ws.DeletionForgotten, "the offline server was removed without touching its host")
	require.Equal(t, StepIntegrations, ws.DeletionStep)
}

func TestTeamDeletionFailureStopsAndKeepsTheTeamUntilRetried(t *testing.T) {
	f := newDeleteFixture(t)
	f.td.failOn = "platform"
	require.NoError(t, f.svc.RunDeletion(context.Background(), f.team.ID), "a failed step is stored, not returned")

	ws, err := f.q.GetWorkspace(context.Background(), f.team.ID)
	require.NoError(t, err)
	require.Equal(t, DeletionFailed, ws.DeletionState)
	require.Contains(t, ws.DeletionError, "platform services: boom")
	for _, c := range f.td.calls {
		require.False(t, strings.HasPrefix(c, "server:"), "no server is touched after a failed step: %s", c)
	}
	// A failed deletion is not run again by itself.
	n := len(f.td.calls)
	require.NoError(t, f.svc.RunDeletion(context.Background(), f.team.ID))
	require.Len(t, f.td.calls, n)

	// The owner retries.
	f.td.failOn = ""
	_, err = f.q.StartTeamDeletion(context.Background(), db.StartTeamDeletionParams{ID: f.team.ID})
	require.NoError(t, err)
	require.NoError(t, f.svc.RunDeletion(context.Background(), f.team.ID))
	require.False(t, f.exists(t))
}

func TestTeamDeletionIsNotStartedTwice(t *testing.T) {
	f := newDeleteFixture(t)
	_, err := f.q.StartTeamDeletion(context.Background(), db.StartTeamDeletionParams{ID: f.team.ID})
	require.Error(t, err, "a team that is being deleted cannot be started again")
}

func TestTeamHostsControlPlane(t *testing.T) {
	f := newDeleteFixture(t)
	host, err := f.q.TeamHostsControlPlane(context.Background(), f.team.ID)
	require.NoError(t, err)
	require.False(t, host)
}
