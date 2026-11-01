// Package stacktest holds cross-package tests of Compose stacks: the features
// a stack does not have answer 422 not_applicable.
package stacktest

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/maintenance"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type noDisp struct{}

func (noDisp) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (noDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func notApplicable(t *testing.T, err error, what string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "%s: expected an API error, got %v", what, err)
	require.Equal(t, "not_applicable", ae.Code, what)
	require.Equal(t, 422, ae.Status, what)
}

func TestStackRefusesFeaturesItDoesNotHave(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", srv.ID)
	require.NoError(t, err)

	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	jm := jobs.New(pool, logger)
	svcs := services.NewService(pool, bus, box, noDisp{}, logger)
	scale := scaling.NewService(pool, bus, noDisp{}, svcs, logger)
	maint := maintenance.NewService(pool, bus, noDisp{}, svcs, logger)
	tsk := tasks.NewService(tasks.Deps{Pool: pool, Bus: bus, Jobs: jm, Box: box, Dispatcher: noDisp{}, Logger: logger})
	tn := tailnet.NewService(tailnet.Deps{Pool: pool, Bus: bus, Dispatcher: noDisp{}, Logger: logger})
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "t", Scopes: policy.AllScopes})

	st, err := svcs.Create(ctx, env.ID, services.CreateInput{
		Name: "shop", Kind: services.KindStack, SourceType: services.SourceCompose, ServerID: &srv.ID,
		Config: services.Config{Stack: &services.StackConfig{Compose: "services:\n  web:\n    image: traefik/whoami:v1.10\n"}},
	})
	require.NoError(t, err)

	_, err = scale.Set(ctx, st.ID, scaling.SetInput{Replicas: new(2)})
	notApplicable(t, err, "scaling")
	_, err = svcs.AddVolume(ctx, st.ID, "data", "/data")
	notApplicable(t, err, "volumes")
	_, err = tsk.CreateTask(ctx, st.ID, tasks.TaskInput{Name: "nightly", Command: []string{"true"}, Schedule: "0 3 * * *"})
	notApplicable(t, err, "tasks")
	_, err = tsk.RunCommand(ctx, st.ID, []string{"ls"}, 0)
	notApplicable(t, err, "exec")
	_, err = maint.Set(ctx, st.ID, maintenance.SetInput{Enabled: true})
	notApplicable(t, err, "maintenance")
	_, err = tn.Reprovision(ctx, st.ID)
	notApplicable(t, err, "tailnet")

	// The scaling view says why.
	require.False(t, st.Scaling.Eligible)
}

type logDisp struct {
	noDisp
	got []agentprotocol.LogsSubscribe
}

func (d *logDisp) SubscribeLogs(_ context.Context, _ uuid.UUID, req agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	d.got = append(d.got, req)
	ch := make(chan agentprotocol.LogFrame)
	close(ch)
	return ch, func() {}, nil
}

func TestStackLiveLogsPassTheComponentToTheAgent(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	env, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	srv, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	disp := &logDisp{}
	svcs := services.NewService(pool, events.NewBus(), box, disp, logger)
	ctx := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Label: "t", Scopes: policy.AllScopes})
	st, err := svcs.Create(ctx, env.ID, services.CreateInput{
		Name: "shop", Kind: services.KindStack, SourceType: services.SourceCompose, ServerID: &srv.ID,
		Config: services.Config{Stack: &services.StackConfig{Compose: "services:\n  web:\n    image: traefik/whoami:v1.10\n"}},
	})
	require.NoError(t, err)
	rel, err := q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: st.ID, Source: []byte(`{"type":"compose"}`), Config: []byte(`{}`)})
	require.NoError(t, err)
	dep, err := q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: st.ID, ReleaseID: &rel.ID, ServerID: &srv.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	require.NoError(t, q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: st.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: 1}))

	_, cancel, err := svcs.SubscribeServiceComponentLogs(ctx, st.ID, 50, false, "web")
	require.NoError(t, err)
	cancel()
	_, cancel, err = svcs.SubscribeServiceLogs(ctx, st.ID, 50, false)
	require.NoError(t, err)
	cancel()
	require.Len(t, disp.got, 2)
	require.Equal(t, "web", disp.got[0].Component)
	require.Equal(t, st.ID.String(), disp.got[0].ServiceID)
	require.Empty(t, disp.got[1].Component, "without a component every component streams")

	_, _, err = svcs.SubscribeServiceComponentLogs(ctx, st.ID, 50, false, "db")
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, "unknown_component", ae.Code)
}
