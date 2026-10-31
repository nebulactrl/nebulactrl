package projects_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/previews"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/statuspages"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// rowDispatcher stores commands as real agent_commands rows, like the hub.
type rowDispatcher struct{ cmds []agents.NewCommand }

func (d *rowDispatcher) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.cmds = append(d.cmds, c)
	id := uuid.New()
	_, err := db.New(tx).InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: c.ServerID, DeploymentID: c.DeploymentID, Kind: c.Kind, Payload: c.Payload,
		IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	return id, err
}

func (d *rowDispatcher) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

// removals returns the remove_service payloads queued so far, by service id.
func (d *rowDispatcher) removals(t *testing.T) map[string]agentprotocol.ServiceActionPayload {
	t.Helper()
	out := map[string]agentprotocol.ServiceActionPayload{}
	for _, c := range d.cmds {
		if c.Kind != agentprotocol.KindRemoveService {
			continue
		}
		var p agentprotocol.ServiceActionPayload
		require.NoError(t, json.Unmarshal(c.Payload, &p))
		out[p.ServiceID] = p
	}
	return out
}

type fakeGit struct{}

func (fakeGit) ServicesForRepo(context.Context, uuid.UUID, string) ([]uuid.UUID, error) {
	return nil, nil
}
func (fakeGit) CopyServiceGitTx(context.Context, pgx.Tx, uuid.UUID, uuid.UUID) error { return nil }
func (fakeGit) SetCommitStatus(context.Context, uuid.UUID, string, string, previews.CommitStatus) error {
	return nil
}
func (fakeGit) UpsertPRComment(context.Context, uuid.UUID, string, int, string, string) error {
	return nil
}

type fakeDeployer struct{}

func (fakeDeployer) Deploy(context.Context, uuid.UUID, previews.Commit, string) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (fakeDeployer) Cancel(context.Context, uuid.UUID) error { return nil }

// dnsHook counts the calls services make after removing domains.
type dnsHook struct{ removed int }

func (h *dnsHook) DomainsRemoved(context.Context, pgx.Tx, uuid.UUID) error {
	h.removed++
	return nil
}

type rig struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	svc     *projects.Service
	disp    *rowDispatcher
	dns     *dnsHook
	person  context.Context
	mcp     context.Context
	project db.Project
	prod    db.Environment
	staging db.Environment
	preview db.Environment
	server  db.Server
	// deployed lists every service that has a container to remove.
	deployed []db.Service
	web      db.Service
	page     uuid.UUID
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	g := prodguard.New(prodguard.Deps{Pool: pool, Box: box})
	g.Install()
	t.Cleanup(prodguard.Uninstall)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewBus()
	disp := &rowDispatcher{}
	dns := &dnsHook{}
	svcs := services.NewService(pool, bus, box, disp, logger)
	svcs.SetDNSHook(dns)
	pv := previews.NewService(previews.Deps{
		Pool: pool, Bus: bus, Box: box, Config: &config.Config{PublicURL: "https://nebula.example.com"},
		Services: svcs, Git: fakeGit{}, Deployer: fakeDeployer{}, Logger: logger,
	})
	pages := statuspages.NewService(statuspages.Deps{Pool: pool, Bus: bus, Logger: logger})
	psvc := projects.NewService(pool, bus)
	psvc.SetCascade(projects.CascadeDeps{
		DeleteService: func(ctx context.Context, id uuid.UUID, slug string) error {
			return svcs.Delete(ctx, id, services.DeleteInput{Confirm: slug, DeleteVolumes: true})
		},
		TeardownPreview:   pv.Teardown,
		DetachStatusPages: pages.DetachProject,
	})

	r := &rig{t: t, pool: pool, q: q, svc: psvc, disp: disp, dns: dns}
	tok := uuid.New()
	r.mcp = auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeMCP, UserID: u.ID, TokenID: &tok, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "agent", Scopes: policy.AllScopes,
	})
	r.person = auth.WithPrincipal(bg, &auth.Principal{
		Type: auth.TypeUser, UserID: u.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "Ada", Scopes: policy.AllScopes,
	})

	r.server, err = q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "agent1"})
	require.NoError(t, err)
	r.project, err = q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	r.prod, err = q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: r.project.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	r.staging, err = q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: r.project.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)

	// Production: web (volume, domain), worker, a database (volume). Staging: web (volume).
	r.web = r.service(r.prod, "web", "app", true)
	_, err = q.InsertDomain(bg, db.InsertDomainParams{ServiceID: r.web.ID, Hostname: "shop.example.com", TargetPort: 80})
	require.NoError(t, err)
	r.service(r.prod, "worker", "worker", false)
	r.service(r.prod, "postgres", "database", true)
	stagingWeb := r.service(r.staging, "web", "app", true)

	// A preview of the production environment with one service.
	pr := int32(7)
	conn := uuid.New()
	r.preview, err = q.InsertPreviewEnvironment(bg, db.InsertPreviewEnvironmentParams{
		ProjectID: r.project.ID, Name: "PR #7", Slug: "pr-7", ParentEnvironmentID: &r.prod.ID, PrNumber: &pr, PrTitle: "Fix", ConnectionID: &conn,
	})
	require.NoError(t, err)
	_, err = q.InsertPreview(bg, db.InsertPreviewParams{
		WorkspaceID: ws.ID, ProjectID: r.project.ID, EnvironmentID: r.preview.ID, BaseEnvironmentID: r.prod.ID, ConnectionID: conn,
		Provider: "github", Repo: "acme/shop", PrNumber: pr, AffectedServiceIds: []uuid.UUID{},
	})
	require.NoError(t, err)
	r.service(r.preview, "web", "app", true)

	// A status page shows the production web and the staging web; a Cloudflare
	// record belongs to the production domain; shared variables in both layers.
	r.page = uuid.New()
	r.exec(`INSERT INTO status_pages (id, workspace_id, title, slug) VALUES ($1, $2, 'Status', 'status')`, r.page, ws.ID)
	r.exec(`INSERT INTO status_page_components (page_id, service_id, display_name, position) VALUES ($1, $2, 'Shop', 0), ($1, $3, 'Shop staging', 1)`, r.page, r.web.ID, stagingWeb.ID)
	dom, err := q.ListServiceDomains(bg, r.web.ID)
	require.NoError(t, err)
	require.Len(t, dom, 1)
	r.exec(`INSERT INTO cloudflare_records (zone_id, record_id, workspace_id, owner_kind, owner_id, hostname, type, content)
	        VALUES ('z', 'r1', $1, 'domain', $2, 'shop.example.com', 'A', '192.0.2.1')`, ws.ID, dom[0].ID)
	r.exec(`INSERT INTO shared_variables (workspace_id, project_id, key, value_enc) VALUES ($1, $2, 'REGION', '\x00')`, ws.ID, r.project.ID)
	r.exec(`INSERT INTO shared_variables (workspace_id, project_id, environment_id, key, value_enc) VALUES ($1, $2, $3, 'REGION', '\x00')`, ws.ID, r.project.ID, r.staging.ID)
	return r
}

func (r *rig) exec(sql string, args ...any) {
	r.t.Helper()
	_, err := r.pool.Exec(context.Background(), sql, args...)
	require.NoError(r.t, err)
}

func (r *rig) count(sql string, args ...any) int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// service inserts a deployed service (optionally with a volume) into env.
func (r *rig) service(env db.Environment, slug, kind string, withVolume bool) db.Service {
	r.t.Helper()
	bg := context.Background()
	sv, err := r.q.InsertService(bg, db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: slug, Slug: slug, Kind: kind, SourceType: "image",
		Config: json.RawMessage(`{"image":"nginx:1","port":80}`), ServerID: &r.server.ID,
	})
	require.NoError(r.t, err)
	if withVolume {
		_, err = r.q.InsertVolume(bg, db.InsertVolumeParams{ServiceID: sv.ID, Name: "data", MountPath: "/data"})
		require.NoError(r.t, err)
	}
	dep, err := r.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ServerID: &r.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(r.t, err)
	r.exec("UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)
	require.NoError(r.t, r.q.MarkServiceDeployed(bg, db.MarkServiceDeployedParams{ID: sv.ID, CurrentDeploymentID: &dep.ID}))
	r.deployed = append(r.deployed, sv)
	return sv
}

func (r *rig) projectExists() bool {
	return r.count(`SELECT count(*) FROM projects WHERE id = $1`, r.project.ID) == 1
}

func code(err error) string {
	if ae, ok := apierr.As(err); ok {
		return ae.Code
	}
	return ""
}

func TestDeletionPlanCountsMatchTheProject(t *testing.T) {
	r := newRig(t)
	plan, err := r.svc.Plan(r.person, r.project.ID)
	require.NoError(t, err)
	require.Equal(t, projects.PlanProject{ID: r.project.ID, Name: "Shop", Slug: "shop"}, plan.Project)
	require.Equal(t, []projects.PlanEnvironment{
		{ID: r.prod.ID, Name: "production", Production: true},
		{ID: r.staging.ID, Name: "staging", Production: false},
	}, plan.Environments, "the preview is not an environment of its own")
	require.Equal(t, 4, plan.Services, "web, worker, staging web, preview web; databases are counted apart")
	require.Equal(t, 1, plan.Databases)
	require.Equal(t, 4, plan.Volumes)
	require.Equal(t, 1, plan.Domains)
	require.Equal(t, 1, plan.Previews)
	require.Equal(t, 2, plan.StatusPageComponents)
	require.Equal(t, 1, plan.ManagedDNSRecords)
	require.True(t, plan.BackupDestinationsKept)

	// A soft-deleted service no longer counts.
	require.NoError(t, r.q.SoftDeleteService(context.Background(), r.deployed[1].ID))
	plan, err = r.svc.Plan(r.person, r.project.ID)
	require.NoError(t, err)
	require.Equal(t, 3, plan.Services)

	// Nothing was deleted by asking.
	require.True(t, r.projectExists())
}

func TestCascadeDeleteRemovesEverything(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	require.NoError(t, r.svc.Delete(r.person, r.project.ID, projects.DeleteInput{Cascade: true, Confirm: "Shop"}))

	require.False(t, r.projectExists())
	require.Zero(t, r.count(`SELECT count(*) FROM environments WHERE project_id = $1`, r.project.ID))
	require.Zero(t, r.count(`SELECT count(*) FROM services WHERE id = ANY($1)`, ids(r.deployed)), "soft-deleted rows go with their environments")
	require.Zero(t, r.count(`SELECT count(*) FROM volumes WHERE service_id = ANY($1)`, ids(r.deployed)))
	require.Zero(t, r.count(`SELECT count(*) FROM domains WHERE service_id = ANY($1)`, ids(r.deployed)))
	require.Zero(t, r.count(`SELECT count(*) FROM previews WHERE project_id = $1`, r.project.ID))
	require.Zero(t, r.count(`SELECT count(*) FROM shared_variables WHERE project_id = $1`, r.project.ID))

	// The status page stays; only the components that showed the project went.
	require.Equal(t, 1, r.count(`SELECT count(*) FROM status_pages WHERE id = $1`, r.page))
	require.Zero(t, r.count(`SELECT count(*) FROM status_page_components WHERE page_id = $1`, r.page))

	// Every deployed service, the preview's included, got a removal for its
	// server, with its volumes.
	removals := r.disp.removals(t)
	require.Len(t, removals, len(r.deployed))
	for _, sv := range r.deployed {
		p, ok := removals[sv.ID.String()]
		require.True(t, ok, "no removal for %s", sv.Slug)
		require.True(t, p.DeleteVolumes, sv.Slug)
	}
	require.Len(t, removals[r.web.ID.String()].VolumeNames, 1)
	require.Equal(t, len(r.deployed), r.count(`SELECT count(*) FROM agent_commands WHERE kind = $1`, agentprotocol.KindRemoveService))
	require.Positive(t, r.dns.removed, "the DNS hook that removes managed records ran")

	// One audit entry with the counts; the entries of the services stay too.
	var meta []byte
	require.NoError(t, r.pool.QueryRow(bg, `SELECT metadata FROM audit_events WHERE action = 'project.deleted' AND resource_id = $1`, r.project.ID.String()).Scan(&meta))
	var m map[string]any
	require.NoError(t, json.Unmarshal(meta, &m))
	require.Equal(t, map[string]any{
		"name": "Shop", "slug": "shop", "cascade": true, "environments": 2.0, "services": 4.0, "databases": 1.0, "volumes": 4.0,
		"domains": 1.0, "previews": 1.0, "statusPageComponents": 2.0, "managedDnsRecords": 1.0,
	}, m)
	require.Equal(t, len(r.deployed), r.count(`SELECT count(*) FROM audit_events WHERE action = 'service.delete' AND project_id = $1`, r.project.ID))
	require.Equal(t, 1, r.count(`SELECT count(*) FROM audit_events WHERE action = 'preview.delete' AND project_id = $1`, r.project.ID))
}

func ids(svcs []db.Service) []uuid.UUID {
	out := make([]uuid.UUID, len(svcs))
	for i, s := range svcs {
		out[i] = s.ID
	}
	return out
}

func TestCascadeDeleteNeedsTheProjectName(t *testing.T) {
	r := newRig(t)
	for _, confirm := range []string{"", "shop", "Shop "} {
		err := r.svc.Delete(r.person, r.project.ID, projects.DeleteInput{Cascade: true, Confirm: confirm})
		require.Equal(t, "confirm_mismatch", code(err), "confirm %q", confirm)
		ae, _ := apierr.As(err)
		require.Equal(t, 422, ae.Status)
	}
	require.True(t, r.projectExists())
	require.Empty(t, r.disp.cmds, "nothing was removed")
	require.Equal(t, len(r.deployed), r.count(`SELECT count(*) FROM services WHERE deleted_at IS NULL AND id = ANY($1)`, ids(r.deployed)))
}

func TestDeleteWithoutCascadeStillRefusesANonEmptyProject(t *testing.T) {
	r := newRig(t)
	// A confirm without cascade changes nothing.
	err := r.svc.Delete(r.person, r.project.ID, projects.DeleteInput{Confirm: "Shop"})
	require.Equal(t, "project_not_empty", code(err))
	ae, _ := apierr.As(err)
	require.Equal(t, 409, ae.Status)
	require.True(t, r.projectExists())
	require.Empty(t, r.disp.cmds)
}

func TestCascadeDeleteOfAProductionProjectNeedsApprovalForMCP(t *testing.T) {
	r := newRig(t)
	g := prodguard.Installed()
	in := projects.DeleteInput{Cascade: true, Confirm: "Shop"}

	err := r.svc.Delete(r.mcp, r.project.ID, in)
	ar, ok := prodguard.AsRequired(err)
	require.True(t, ok, "got %v", err)
	require.Contains(t, ar.Plan.Summary, "Delete the project Shop with everything in it")
	require.True(t, ar.Plan.Irreversible)
	require.True(t, r.projectExists(), "nothing is deleted before the approval")
	require.Empty(t, r.disp.cmds)

	// A wrong confirm is refused before anyone is asked.
	err = r.svc.Delete(r.mcp, r.project.ID, projects.DeleteInput{Cascade: true, Confirm: "nope"})
	require.Equal(t, "confirm_mismatch", code(err))

	// Once a person approved, one retry deletes everything without a second
	// approval for each service.
	_, err = g.Approve(r.person, ar.ApprovalID)
	require.NoError(t, err)
	require.NoError(t, r.svc.Delete(r.mcp, r.project.ID, in))
	require.False(t, r.projectExists())
	require.Len(t, r.disp.removals(t), len(r.deployed))
}

func TestCascadeDeleteWithoutProductionNeedsNoApproval(t *testing.T) {
	r := newRig(t)
	r.exec(`UPDATE environments SET is_production = false WHERE id = $1`, r.prod.ID)
	require.NoError(t, r.svc.Delete(r.mcp, r.project.ID, projects.DeleteInput{Cascade: true, Confirm: "Shop"}))
	require.False(t, r.projectExists())
}

func TestCascadeDeleteRefusesBeforeDeletingAnything(t *testing.T) {
	r := newRig(t)
	in := projects.DeleteInput{Cascade: true, Confirm: "Shop"}

	// A running deployment.
	dep, err := r.q.InsertDeployment(context.Background(), db.InsertDeploymentParams{ServiceID: r.web.ID, ServerID: &r.server.ID, Trigger: "manual", ActorType: "user", Strategy: "recreate"})
	require.NoError(t, err)
	err = r.svc.Delete(r.person, r.project.ID, in)
	require.Equal(t, "deployment_in_progress", code(err))
	r.exec("UPDATE deployments SET status = 'succeeded' WHERE id = $1", dep.ID)

	require.True(t, r.projectExists())
	require.Empty(t, r.disp.cmds, "no service was touched")
}

func TestCascadeDeleteIsRepeatableAfterAPartialFailure(t *testing.T) {
	r := newRig(t)
	// A service that was already deleted before (a first attempt got that far).
	require.NoError(t, r.q.SoftDeleteService(context.Background(), r.deployed[1].ID))
	require.NoError(t, r.svc.Delete(r.person, r.project.ID, projects.DeleteInput{Cascade: true, Confirm: "Shop"}))
	require.False(t, r.projectExists())
}
