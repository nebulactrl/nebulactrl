package tasks

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
)

// restToken is a REST token as authentication builds it: guarded unless exempt.
func (f *fixture) restToken(scopes []string) context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: scopes, RequireProductionApproval: true,
	})
}

func (f *fixture) person() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
}

func (f *fixture) installGuard() *prodguard.Guard {
	f.t.Helper()
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	f.t.Cleanup(prodguardtest.Uninstall)
	return g
}

func (f *fixture) approve(g *prodguard.Guard, err error) {
	f.t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(f.t, ok, "want approval_required, got %v", err)
	_, aerr := g.Approve(f.person(), ar.ApprovalID)
	require.NoError(f.t, aerr)
}

// A REST token cannot create, change or delete a task of a production service
// without a person's approval; the browser session is not asked.
func TestTaskChangesInProductionNeedApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t) // the fixture environment is production
	g := f.installGuard()
	sv := f.deployed()
	rest := f.restToken(policy.AllScopes)

	in := TaskInput{Name: "x", Schedule: "* * * * *", Command: []string{"sh", "-c", "env | rev"}}
	_, createErr := f.svc.CreateTask(rest, sv.ID, in)
	ar, ok := prodguard.AsRequired(createErr)
	require.True(t, ok, "got %v", createErr)
	require.Contains(t, ar.Plan.Summary, "Schedule the task x")
	require.Contains(t, ar.Plan.Effects, "Command: `sh -c env | rev`.")
	list, err := f.svc.ListTasks(f.ctx, sv.ID)
	require.NoError(t, err)
	require.Empty(t, list, "nothing is created before the approval")

	f.approve(g, createErr)
	created, err := f.svc.CreateTask(rest, sv.ID, in)
	require.NoError(t, err)

	// A different command is a different call: the approval does not carry over.
	cmd := []string{"id"}
	_, updErr := f.svc.UpdateTask(rest, created.ID, TaskPatch{Command: &cmd})
	_, ok = prodguard.AsRequired(updErr)
	require.True(t, ok, "got %v", updErr)
	f.approve(g, updErr)
	_, err = f.svc.UpdateTask(rest, created.ID, TaskPatch{Command: &cmd})
	require.NoError(t, err)

	// The approval was used up.
	_, updErr = f.svc.UpdateTask(rest, created.ID, TaskPatch{Command: &cmd})
	_, ok = prodguard.AsRequired(updErr)
	require.True(t, ok, "got %v", updErr)

	delErr := f.svc.DeleteTask(rest, created.ID)
	ar, ok = prodguard.AsRequired(delErr)
	require.True(t, ok, "got %v", delErr)
	require.True(t, ar.Plan.Irreversible)
	_, err = f.svc.GetTask(f.ctx, created.ID)
	require.NoError(t, err, "the task still exists")
	f.approve(g, delErr)
	require.NoError(t, f.svc.DeleteTask(rest, created.ID))

	// A person in the browser is never asked.
	_, err = f.svc.CreateTask(f.ctx, sv.ID, in)
	require.NoError(t, err)
}

// A task runs its command in the production container, so creating or
// changing one needs deployments:write, like run_command.
func TestProductionTaskCommandNeedsDeploymentsWrite(t *testing.T) {
	f := newFixture(t)
	g := f.installGuard()
	sv := f.deployed()
	writeOnly := f.restToken([]string{policy.ScopeProjectsRead, policy.ScopeProjectsWrite})

	in := TaskInput{Name: "x", Schedule: "* * * * *", Command: []string{"sh", "-c", "env | rev"}}
	_, err := f.svc.CreateTask(writeOnly, sv.ID, in)
	requireAPIError(t, err, "forbidden")
	_, ok := prodguard.AsRequired(err)
	require.False(t, ok, "the scope is refused before an approval is asked")

	task := f.task(sv.ID, "@daily")
	cmd := []string{"id"}
	_, err = f.svc.UpdateTask(writeOnly, task.ID, TaskPatch{Command: &cmd})
	requireAPIError(t, err, "forbidden")

	// Changing the schedule only keeps the command: projects:write is enough
	// for the scope, and the guard still asks.
	sched := "@hourly"
	_, err = f.svc.UpdateTask(writeOnly, task.ID, TaskPatch{Schedule: &sched})
	_, ok = prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	f.approve(g, err)
	_, err = f.svc.UpdateTask(writeOnly, task.ID, TaskPatch{Schedule: &sched})
	require.NoError(t, err)

	// Outside production the command needs no extra scope.
	_, err = f.pool.Exec(context.Background(), "UPDATE environments SET is_production = false WHERE id = $1", f.env.ID)
	require.NoError(t, err)
	_, err = f.svc.UpdateTask(writeOnly, task.ID, TaskPatch{Command: &cmd})
	require.NoError(t, err)
}
