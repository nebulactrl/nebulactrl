package gitproviders_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/prodguard/prodguardtest"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func (f *fixture) restToken() context.Context {
	p, _ := auth.PrincipalFrom(f.ctx)
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: p.UserID, TokenID: &tok, WorkspaceID: f.ws.ID, Role: auth.RoleOwner,
		Label: "ci", Scopes: policy.AllScopes, RequireProductionApproval: true,
	})
}

func (f *fixture) approve(g *prodguard.Guard, err error) {
	f.t.Helper()
	ar, ok := prodguard.AsRequired(err)
	require.True(f.t, ok, "want approval_required, got %v", err)
	p, _ := auth.PrincipalFrom(f.ctx)
	person := auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: p.UserID, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})
	_, aerr := g.Approve(person, ar.ApprovalID)
	require.NoError(f.t, aerr)
}

// deployed gives the service a running release (the applied configuration).
func (f *fixture) deployed(sv services.ServiceView) {
	f.t.Helper()
	bg := context.Background()
	row, err := f.q.GetServiceScopeInternal(bg, sv.ID)
	require.NoError(f.t, err)
	snap, _ := json.Marshal(services.Snapshot{Config: services.DecodeConfig(row.Service.Config), ServerID: &f.server.ID})
	src, _ := json.Marshal(services.ReleaseSource{Type: services.SourceGit})
	rel, err := f.q.InsertRelease(bg, db.InsertReleaseParams{ID: uuid.New(), ServiceID: sv.ID, Source: src, Config: snap, ImageRef: "x"})
	require.NoError(f.t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ReleaseID: &rel.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, "UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID, AppliedGeneration: row.Service.Generation}))
}

// A REST token cannot link or unlink a production service's repository without
// a person's approval (a push deploys it).
func TestLinkAndUnlinkInProductionNeedApprovalForRESTTokens(t *testing.T) {
	f := newFixture(t) // the fixture environment is production
	conn := f.giteaConnection()
	f.giteaRepo("main")
	g := prodguard.New(prodguard.Deps{Pool: f.pool, Box: f.box})
	g.Install()
	t.Cleanup(prodguardtest.Uninstall)
	sv := f.gitService("api")
	rest := f.restToken()
	in := gitproviders.LinkInput{ConnectionID: conn.Connection.ID, Repo: "acme/shop", Branch: "agent-tmp"}

	_, linkErr := f.svc.Link(rest, sv.ID, in)
	ar, ok := prodguard.AsRequired(linkErr)
	require.True(t, ok, "got %v", linkErr)
	require.Contains(t, ar.Plan.Summary, "Link api in production")
	v, err := f.svc.GetLink(f.ctx, sv.ID)
	require.NoError(t, err)
	require.False(t, v.Linked, "nothing is linked before the approval")

	f.approve(g, linkErr)
	_, err = f.svc.Link(rest, sv.ID, in)
	require.NoError(t, err)
	v, err = f.svc.GetLink(f.ctx, sv.ID)
	require.NoError(t, err)
	require.True(t, v.Linked)

	unlinkErr := f.svc.Unlink(rest, sv.ID)
	_, ok = prodguard.AsRequired(unlinkErr)
	require.True(t, ok, "got %v", unlinkErr)
	v, err = f.svc.GetLink(f.ctx, sv.ID)
	require.NoError(t, err)
	require.True(t, v.Linked, "still linked")
	f.approve(g, unlinkErr)
	require.NoError(t, f.svc.Unlink(rest, sv.ID))

	// A person in the browser is never asked.
	_, err = f.svc.Link(f.ctx, sv.ID, in)
	require.NoError(t, err)
}

// A production service with a staged branch switch is still matched by the
// branch it runs, so a push to the staged branch deploys nothing.
func TestPushMatchesTheAppliedBranchOfAProductionServiceWithStagedChanges(t *testing.T) {
	f := newFixture(t)
	conn := f.giteaConnection()
	f.giteaRepo("main")
	sv := f.gitService("api")
	f.link(sv.ID, conn.Connection.ID, gitproviders.LinkInput{Branch: "main"})
	f.deployed(sv)
	_, err := f.pool.Exec(context.Background(),
		`UPDATE services SET config = jsonb_set(config, '{gitBranch}', '"agent-tmp"'), generation = generation + 1 WHERE id = $1`, sv.ID)
	require.NoError(t, err)

	push := func(ref, delivery string) {
		body := bytes.ReplaceAll(readTestdata(t, "gitea_push.json"), []byte("refs/heads/main"), []byte("refs/heads/"+ref))
		code, _ := f.post("gitea", conn.Connection.ID, giteaHeaders(conn.WebhookSecret, body, delivery), body)
		require.Equal(t, 200, code)
		f.svc.Wait()
	}

	push("agent-tmp", "d-staged")
	require.Empty(t, f.dep.snapshot(), "a push to the staged branch deploys nothing")

	push("main", "d-applied")
	calls := f.dep.snapshot()
	require.Len(t, calls, 1, "a push to the branch the service runs deploys it")
	require.Equal(t, sv.ID, calls[0].ServiceID)
}
