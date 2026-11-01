package configsync

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// guardFake is a planner/applier that records what the guard asked of it.
type guardFake struct {
	plan      Plan
	planErr   error
	plans     int
	applies   int
	lastApply ApplyRequest
}

func (f *guardFake) Plan(_ context.Context, _ uuid.UUID, _ PlanRequest) (Plan, error) {
	f.plans++
	return f.plan, f.planErr
}

func (f *guardFake) Apply(_ context.Context, _ uuid.UUID, r ApplyRequest) (ApplyResult, error) {
	f.applies++
	f.lastApply = r
	return ApplyResult{PlanID: r.PlanID, Status: StatusStaged}, nil
}

// guardPlan is a valid plan for one service with one staged and one immediate change.
func guardPlan(production bool, planID string, svc uuid.UUID, generation int64) Plan {
	return Plan{
		PlanID: planID, Valid: true,
		Project:        PlanProject{ID: uuid.New(), Slug: "shop"},
		Environment:    PlanEnvironment{ID: uuid.New(), Slug: "production", IsProduction: production},
		RequiredScopes: []string{policy.ScopeProjectsWrite},
		Services: []PlanService{{
			Slug: "web", Kind: "git", Action: ActionUpdate, ServiceID: &svc, Generation: generation,
			Changes: []PlanChange{
				{Field: "config.port", Op: OpSet, Applies: AppliesStaged, From: "3000", To: "8080"},
				{Field: "scale.replicas", Op: OpSet, Applies: AppliesImmediate, From: "1", To: "2"},
			},
		}},
	}
}

func guardStagedOnly(p Plan) Plan {
	p.Services[0].Changes = p.Services[0].Changes[:1]
	return p
}

func guardCtx(p *auth.Principal) context.Context { return auth.WithPrincipal(context.Background(), p) }

func guardAgent(scopes ...string) *auth.Principal {
	tok := uuid.New()
	return &auth.Principal{Type: auth.TypeMCP, UserID: uuid.New(), TokenID: &tok, WorkspaceID: uuid.New(), Role: auth.RoleDeveloper, Label: "agent", Scopes: scopes}
}

func guardErrCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func TestApplyGuardedPassesPrincipalsThatAreNotGuarded(t *testing.T) {
	prodguardtest.Uninstall()
	f := &guardFake{plan: guardPlan(true, "pl_aaaaaaaaaaaa", uuid.New(), 3)}
	person := &auth.Principal{Type: auth.TypeUser, UserID: uuid.New(), WorkspaceID: uuid.New(), Role: auth.RoleOwner, Scopes: policy.ScopesForRole(auth.RoleOwner)}
	res, err := guardedApply(guardCtx(person), f, uuid.New(), ApplyRequest{Deploy: true, PlanRequest: PlanRequest{Prune: true}})
	require.NoError(t, err)
	require.Equal(t, StatusStaged, res.Status)
	require.Equal(t, 0, f.plans, "a person is never planned twice")
	require.Equal(t, 1, f.applies)
	require.True(t, f.lastApply.Deploy && f.lastApply.Prune, "a person keeps deploy and prune")

	// A token that did not opt in to approvals behaves like a person.
	tok := uuid.New()
	token := &auth.Principal{Type: auth.TypeToken, UserID: uuid.New(), TokenID: &tok, WorkspaceID: uuid.New(), Scopes: []string{policy.ScopeProjectsWrite}}
	_, err = guardedApply(guardCtx(token), f, uuid.New(), ApplyRequest{})
	require.NoError(t, err)
	require.Equal(t, 2, f.applies)
	require.Equal(t, 0, f.plans)
}

func TestApplyGuardedRefusesDeployAndPruneForAgents(t *testing.T) {
	prodguardtest.Uninstall()
	f := &guardFake{plan: guardPlan(false, "pl_aaaaaaaaaaaa", uuid.New(), 3)}

	// An MCP session is refused even when its token may skip production approvals.
	exempt := guardAgent(policy.ScopeProjectsWrite)
	exempt.AllowProductionWithoutApproval = true
	require.False(t, prodguard.Guarded(exempt))
	_, err := guardedApply(guardCtx(exempt), f, uuid.New(), ApplyRequest{Deploy: true})
	require.Equal(t, CodeDeployNotAllowed, guardErrCode(t, err))
	_, err = guardedApply(guardCtx(exempt), f, uuid.New(), ApplyRequest{PlanRequest: PlanRequest{Prune: true}})
	require.Equal(t, CodePruneNotAllowed, guardErrCode(t, err))

	// A token that needs approvals cannot deploy from an apply either.
	tok := uuid.New()
	careful := &auth.Principal{Type: auth.TypeToken, UserID: uuid.New(), TokenID: &tok, WorkspaceID: uuid.New(), RequireProductionApproval: true, Scopes: []string{policy.ScopeProjectsWrite}}
	_, err = guardedApply(guardCtx(careful), f, uuid.New(), ApplyRequest{Deploy: true})
	require.Equal(t, CodeDeployNotAllowed, guardErrCode(t, err))

	require.Zero(t, f.plans+f.applies, "refusals happen before anything is planned or applied")
}

func TestApplyGuardedNeedsAPrincipal(t *testing.T) {
	f := &guardFake{}
	_, err := guardedApply(context.Background(), f, uuid.New(), ApplyRequest{})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, http.StatusUnauthorized, ae.Status)
	require.Zero(t, f.applies)
}

func TestApplyGuardedOnlyAsksWhenSomethingTakesEffectAtOnceInProduction(t *testing.T) {
	// No Guard is installed: any call that reaches prodguard.Check as a guarded
	// principal in production fails closed with approval_unavailable. So a call
	// that succeeds here never asked.
	prodguardtest.Uninstall()
	agent := guardAgent(policy.ScopeProjectsWrite)
	require.True(t, prodguard.Guarded(agent))
	svc := uuid.New()

	t.Run("staged only in production", func(t *testing.T) {
		f := &guardFake{plan: guardStagedOnly(guardPlan(true, "pl_aaaaaaaaaaaa", svc, 3))}
		_, err := guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{PlanID: "pl_aaaaaaaaaaaa"})
		require.NoError(t, err)
		require.Equal(t, 1, f.applies)
	})
	t.Run("immediate change outside production", func(t *testing.T) {
		f := &guardFake{plan: guardPlan(false, "pl_aaaaaaaaaaaa", svc, 3)}
		_, err := guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{PlanID: "pl_aaaaaaaaaaaa"})
		require.NoError(t, err)
		require.Equal(t, 1, f.applies)
	})
	t.Run("invalid plan goes to apply, which refuses it", func(t *testing.T) {
		p := guardPlan(true, "pl_aaaaaaaaaaaa", svc, 3)
		p.Valid = false
		f := &guardFake{plan: p}
		_, err := guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{})
		require.NoError(t, err)
		require.Equal(t, 1, f.applies, "no approval is requested for a call that cannot run")
	})
	t.Run("keep and release are not writes", func(t *testing.T) {
		p := guardPlan(true, "pl_aaaaaaaaaaaa", svc, 3)
		p.Services[0].Changes = []PlanChange{
			{Field: "scale.replicas", Op: OpKeep, Applies: AppliesImmediate},
			{Field: "uptime.failure_threshold", Op: OpRelease, Applies: AppliesImmediate},
		}
		f := &guardFake{plan: p}
		_, err := guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{})
		require.NoError(t, err)
	})
	t.Run("immediate change in production fails closed without a guard", func(t *testing.T) {
		f := &guardFake{plan: guardPlan(true, "pl_aaaaaaaaaaaa", svc, 3)}
		_, err := guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{PlanID: "pl_aaaaaaaaaaaa"})
		require.Equal(t, prodguard.CodeApprovalUnavailable, guardErrCode(t, err))
		require.Zero(t, f.applies, "nothing is applied when approvals cannot be checked")
	})
	t.Run("a deferred change counts as immediate", func(t *testing.T) {
		p := guardPlan(true, "pl_aaaaaaaaaaaa", svc, 3)
		p.Services[0].Changes[1].Applies = AppliesDeferred
		f := &guardFake{plan: p}
		_, err := guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{})
		require.Equal(t, prodguard.CodeApprovalUnavailable, guardErrCode(t, err))
	})
	t.Run("shared variable layers are staged", func(t *testing.T) {
		p := guardStagedOnly(guardPlan(true, "pl_aaaaaaaaaaaa", svc, 3))
		p.Shared.Project = []PlanChange{{Field: "variables.APP_NAME", Op: OpSet, Applies: AppliesStaged}}
		f := &guardFake{plan: p}
		_, err := guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{})
		require.NoError(t, err)
	})
}

func TestApplyGuardedStalePlanIsRefusedBeforeApprovalAndScopesBeforeBoth(t *testing.T) {
	prodguardtest.Uninstall() // a call that reached prodguard.Check would answer approval_unavailable
	svc := uuid.New()

	agent := guardAgent(policy.ScopeProjectsWrite)
	f := &guardFake{plan: guardPlan(true, "pl_current0000000", svc, 3)}
	_, err := guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{PlanID: "pl_reviewed000000"})
	require.Equal(t, CodePlanStale, guardErrCode(t, err), "a plan that moved on must not consume or request an approval")
	ae, _ := apierr.As(err)
	require.Equal(t, http.StatusConflict, ae.Status)
	require.Equal(t, "pl_current0000000", ae.Details["plan"].(Plan).PlanID, "the fresh plan comes back")
	require.Zero(t, f.applies)

	// A token missing a scope the plan needs is refused before an approval exists.
	f = &guardFake{plan: guardPlan(true, "pl_current0000000", svc, 3)}
	f.plan.RequiredScopes = []string{policy.ScopeProjectsWrite, policy.ScopeAdmin}
	_, err = guardedApply(guardCtx(agent), f, uuid.New(), ApplyRequest{PlanID: "pl_current0000000"})
	require.Equal(t, CodeInsufficientScope, guardErrCode(t, err))
	ae, _ = apierr.As(err)
	require.Equal(t, []string{policy.ScopeAdmin}, ae.Details["scopes"])
	require.Zero(t, f.applies)

	// Without projects:write there is nothing to approve either.
	reader := guardAgent(policy.ScopeProjectsRead)
	f = &guardFake{plan: guardPlan(true, "pl_current0000000", svc, 3)}
	_, err = guardedApply(guardCtx(reader), f, uuid.New(), ApplyRequest{})
	require.Error(t, err)
	require.NotEqual(t, prodguard.CodeApprovalUnavailable, guardErrCode(t, err))
	require.Zero(t, f.applies)
}

func TestGuardScopeNamesFieldsNeverValues(t *testing.T) {
	svc := uuid.New()
	p := guardPlan(true, "pl_0123456789abcdef", svc, 7)
	p.Services[0].Changes[0].To = "super-secret-value"
	p.Services[0].Changes[1].To = "2"
	ctx := guardCtx(guardAgent(policy.ScopeProjectsWrite))
	sc := guardScope(ctx, p)

	require.True(t, sc.IsProduction)
	require.Equal(t, p.Project.ID, sc.ProjectID)
	require.Equal(t, p.Environment.ID, sc.EnvironmentID)
	require.Contains(t, sc.Summary, "shop / production")
	require.Contains(t, sc.Summary, "pl_01234567", "the approver sees which plan this is")
	require.Contains(t, sc.Effects, "web: changes at once: scale.replicas.")
	require.Contains(t, sc.Effects, "web: stages 1 change(s); nothing deploys.")
	for _, e := range sc.Effects {
		require.NotContains(t, e, "super-secret-value")
	}
	require.Equal(t, []prodguard.ApprovalTarget{
		{Kind: "environment", ID: p.Environment.ID, Name: "production"},
		{Kind: "service", ID: svc, Name: "web", Generation: 7},
	}, sc.Targets, "the approval is bound to the generation of every service it touches")
}

func TestGuardScopeBoundsTheListOfEffects(t *testing.T) {
	p := guardPlan(true, "pl_0123456789abcdef", uuid.New(), 1)
	p.Services = nil
	for i := range 40 {
		id := uuid.New()
		p.Services = append(p.Services, PlanService{
			Slug: "svc" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Action: ActionUpdate, ServiceID: &id, Generation: 1,
			Changes: []PlanChange{{Field: "scale.replicas", Op: OpSet, Applies: AppliesImmediate}},
		})
	}
	sc := guardScope(guardCtx(guardAgent()), p)
	require.LessOrEqual(t, len(sc.Effects), maxGuardEffects+1)
	require.Contains(t, sc.Effects[len(sc.Effects)-1], "more service(s)")
}

// guardDB is the database fixture of the approval flow.
type guardDB struct {
	g      *prodguard.Guard
	ws     db.Workspace
	user   db.CreateUserRow
	prj    db.Project
	prod   db.Environment
	agent  *auth.Principal
	svc    uuid.UUID
	person func() context.Context
}

func newGuardDB(t *testing.T) *guardDB {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	g := prodguard.New(prodguard.Deps{Pool: pool, Box: box, PublicURL: "https://nebula.test"})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)

	tok := uuid.New()
	f := &guardDB{
		g: g, ws: ws, user: u, prj: prj, prod: prod, svc: uuid.New(),
		agent: &auth.Principal{
			Type: auth.TypeMCP, UserID: u.ID, TokenID: &tok, WorkspaceID: ws.ID, Role: auth.RoleDeveloper, Label: "agent-token",
			Scopes: policy.ScopesForRole(auth.RoleDeveloper),
		},
	}
	f.person = func() context.Context {
		return auth.WithPrincipal(context.Background(), &auth.Principal{
			Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Role: auth.RoleDeveloper, Label: "Ada", Scopes: policy.ScopesForRole(auth.RoleDeveloper),
		})
	}
	return f
}

// plan is the plan of the fixture's production environment.
func (f *guardDB) plan(planID string, generation int64) Plan {
	p := guardPlan(true, planID, f.svc, generation)
	p.Project = PlanProject{ID: f.prj.ID, Slug: "shop"}
	p.Environment = PlanEnvironment{ID: f.prod.ID, Slug: "production", IsProduction: true}
	return p
}

func (f *guardDB) apply(fake *guardFake, planID string) error {
	_, err := guardedApply(guardCtx(f.agent), fake, f.prj.ID, ApplyRequest{PlanID: planID})
	return err
}

func (f *guardDB) approve(t *testing.T, err error) {
	t.Helper()
	ae, ok := prodguard.AsRequired(err)
	require.True(t, ok, "want an approval request, got %v", err)
	_, aerr := f.g.Approve(f.person(), ae.ApprovalID)
	require.NoError(t, aerr)
}

func TestApplyGuardedApprovalIsBoundToThePlan(t *testing.T) {
	f := newGuardDB(t)
	fake := &guardFake{plan: f.plan("pl_planAAAAAAAAAAA", 3)}

	// 1. Immediate change in production: the agent gets an approval request and nothing runs.
	err := f.apply(fake, "pl_planAAAAAAAAAAA")
	ae, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, GuardApplyConfig, ae.Plan.Op)
	require.Contains(t, ae.Plan.Summary, "shop / production")
	require.Zero(t, fake.applies)

	// 2. The same call is still pending: no second request is created.
	err2 := f.apply(fake, "pl_planAAAAAAAAAAA")
	ae2, ok := prodguard.AsRequired(err2)
	require.True(t, ok)
	require.Equal(t, ae.ApprovalID, ae2.ApprovalID)

	// 3. A person approves; the retry applies, pinned to the approved plan, and consumes the approval.
	f.approve(t, err)
	res, err := guardedApply(guardCtx(f.agent), fake, f.prj.ID, ApplyRequest{})
	require.NoError(t, err, "a call without planId is pinned to the plan the approver saw")
	require.Equal(t, "pl_planAAAAAAAAAAA", res.PlanID)
	require.Equal(t, 1, fake.applies)
	require.Equal(t, "pl_planAAAAAAAAAAA", fake.lastApply.PlanID)
	require.False(t, fake.lastApply.Deploy)

	// 4. An approval is used once.
	_, ok = prodguard.AsRequired(f.apply(fake, "pl_planAAAAAAAAAAA"))
	require.True(t, ok, "the same plan needs a new approval after it ran")
	require.Equal(t, 1, fake.applies)
}

func TestApplyGuardedApprovalDoesNotCarryOverToAnotherPlan(t *testing.T) {
	f := newGuardDB(t)
	fake := &guardFake{plan: f.plan("pl_planAAAAAAAAAAA", 3)}
	f.approve(t, f.apply(fake, "pl_planAAAAAAAAAAA"))

	// The environment changed: another plan id. The approval of plan A does not cover it.
	fake.plan = f.plan("pl_planBBBBBBBBBBB", 3)
	err := f.apply(fake, "pl_planBBBBBBBBBBB")
	_, ok := prodguard.AsRequired(err)
	require.True(t, ok, "a changed plan needs its own approval, got %v", err)
	require.Zero(t, fake.applies)

	// Plan A is approved and still unchanged: it passes.
	fake.plan = f.plan("pl_planAAAAAAAAAAA", 3)
	require.NoError(t, f.apply(fake, "pl_planAAAAAAAAAAA"))
	require.Equal(t, 1, fake.applies)
}

func TestApplyGuardedApprovalVoidWhenAServiceMovedOn(t *testing.T) {
	f := newGuardDB(t)
	fake := &guardFake{plan: f.plan("pl_planAAAAAAAAAAA", 3)}
	f.approve(t, f.apply(fake, "pl_planAAAAAAAAAAA"))

	// Same plan id, but the service the approver saw at generation 3 is now at 4.
	fake.plan = f.plan("pl_planAAAAAAAAAAA", 4)
	_, ok := prodguard.AsRequired(f.apply(fake, "pl_planAAAAAAAAAAA"))
	require.True(t, ok, "the approval is bound to the generations the approver saw")
	require.Zero(t, fake.applies)
}

func TestApplyGuardedStagedOnlyProductionPlanNeedsNoApproval(t *testing.T) {
	f := newGuardDB(t)
	fake := &guardFake{plan: guardStagedOnly(f.plan("pl_planAAAAAAAAAAA", 3))}
	require.NoError(t, f.apply(fake, "pl_planAAAAAAAAAAA"))
	require.Equal(t, 1, fake.applies)
}
