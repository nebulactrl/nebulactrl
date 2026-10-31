package teams

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Steps of a team deletion, in the order the job takes them.
const (
	StepProjects     = "projects"
	StepServers      = "servers"
	StepIntegrations = "integrations"
	StepTeam         = "team"
)

// States of a team deletion (workspaces.deletion_state).
const (
	DeletionDeleting = "deleting"
	DeletionFailed   = "failed"
)

// deletionTimeout bounds the whole job: every agent gets its decommission time.
const deletionTimeout = 12 * time.Hour

// TeamDeletion is the state of a team that is being deleted, or whose deletion
// failed. The team shows it in the switcher and in its danger zone.
type TeamDeletion struct {
	State string `json:"state" enum:"deleting,failed" doc:"deleting while the job runs; failed when it stopped (see error): delete again to retry"`
	Step  string `json:"step,omitempty" enum:"projects,servers,integrations,team" doc:"Where the job is: projects (each project with everything in it), servers (the agents empty their hosts), integrations (status pages, Cloudflare, Tailscale), team (the team itself)"`
	Error string `json:"error,omitempty" doc:"Why the deletion stopped"`
	// ForgottenServers names the offline servers that were removed without
	// touching their hosts: those keep their containers and the agent's files.
	ForgottenServers []string   `json:"forgottenServers" doc:"Servers that were offline (or whose agent could not clean the host) and were removed without touching their hosts: clean those up yourself"`
	KeepDNSRecords   bool       `json:"keepDnsRecords" doc:"The DNS records NebulaCtrl created in Cloudflare are left there"`
	StartedAt        *time.Time `json:"startedAt,omitempty"`
}

// Teardown is what a team deletion calls out to. Each method works on the team
// of the principal in ctx, which the job sets to the control plane's own
// administrator principal of the team. The domains implement it behind the
// wiring (internal/app), the tests fake it.
type Teardown interface {
	// DeleteProject deletes a project with everything in it (services, volumes,
	// domains, DNS records NebulaCtrl manages, previews, status page components).
	DeleteProject(ctx context.Context, projectID uuid.UUID) error
	// RemovePlatformServices removes the team's platform service (Headscale)
	// with its data, unwiring what it fed. It lives in the hidden system project.
	RemovePlatformServices(ctx context.Context) error
	// RemoveServer removes a server: clean when its agent is online and able,
	// otherwise without touching the host. It waits for the agent and returns
	// "cleaned" or "forgotten".
	RemoveServer(ctx context.Context, serverID uuid.UUID) (string, error)
	// DeleteStatusPages deletes the team's status pages and their DNS.
	DeleteStatusPages(ctx context.Context) error
	// DisconnectCloudflare removes the Cloudflare connection; unless keepRecords
	// the DNS records NebulaCtrl created are deleted from Cloudflare first.
	DisconnectCloudflare(ctx context.Context, keepRecords bool) error
	// DisconnectTailscale removes the devices and keys NebulaCtrl created on the
	// tailnet and then the integration.
	DisconnectTailscale(ctx context.Context) error
}

// Outcomes RemoveServer reports.
const (
	ServerCleaned   = "cleaned"
	ServerForgotten = "forgotten"
)

// SetTeardown plugs in the domains a team deletion runs through (wiring only).
func (s *Service) SetTeardown(t Teardown) { s.teardown = t }

// TeamDeletionPlan is what deleting a team removes. It deletes nothing.
type TeamDeletionPlan struct {
	Team    PlanTeam `json:"team"`
	Servers struct {
		Online  int `json:"online" doc:"Servers whose agent is connected: the agent empties the host and removes itself"`
		Offline int `json:"offline" doc:"Servers that are not connected: removed without touching the host, which keeps its containers"`
	} `json:"servers"`
	Projects          int  `json:"projects" doc:"Projects, the hidden platform project excluded"`
	Services          int  `json:"services" doc:"Live services other than databases, platform services included"`
	Databases         int  `json:"databases"`
	Volumes           int  `json:"volumes" doc:"Volumes of those services; their data is deleted"`
	Domains           int  `json:"domains"`
	StatusPages       int  `json:"statusPages"`
	ManagedDNSRecords int  `json:"managedDnsRecords" doc:"DNS records NebulaCtrl manages in Cloudflare. They are deleted from the zone unless you keep them"`
	TailnetDevices    int  `json:"tailnetDevices" doc:"Tailscale devices NebulaCtrl created (servers, services, links). They are deleted from the tailnet"`
	HostsControlPlane bool `json:"hostsControlPlane" doc:"The team owns the server NebulaCtrl itself runs on: the team cannot be deleted (409 team_hosts_control_plane)"`
	// BackupDestinationsKept says that backups already written to a backup
	// destination stay there.
	BackupDestinationsKept bool `json:"backupDestinationsKept"`
}

// PlanTeam identifies the team of a TeamDeletionPlan.
type PlanTeam struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Slug string    `json:"slug"`
}

// DeletionPlan reports what deleting the team would remove. Owners only.
func (s *Service) DeletionPlan(ctx context.Context, teamID uuid.UUID) (TeamDeletionPlan, error) {
	if _, err := s.owner(ctx, teamID); err != nil {
		return TeamDeletionPlan{}, err
	}
	ws, err := s.q.GetWorkspace(ctx, teamID)
	if err != nil {
		return TeamDeletionPlan{}, fmt.Errorf("get team: %w", err)
	}
	c, err := s.q.GetTeamDeletionCounts(ctx, teamID)
	if err != nil {
		return TeamDeletionPlan{}, fmt.Errorf("count team contents: %w", err)
	}
	host, err := s.q.TeamHostsControlPlane(ctx, teamID)
	if err != nil {
		return TeamDeletionPlan{}, fmt.Errorf("check control plane server: %w", err)
	}
	out := TeamDeletionPlan{
		Team:     PlanTeam{ID: ws.ID, Name: ws.Name, Slug: ws.Slug},
		Projects: int(c.Projects), Services: int(c.Services), Databases: int(c.Databases), Volumes: int(c.Volumes),
		Domains: int(c.Domains), StatusPages: int(c.StatusPages), ManagedDNSRecords: int(c.ManagedDnsRecords),
		TailnetDevices: int(c.TailnetDevices), HostsControlPlane: host, BackupDestinationsKept: true,
	}
	out.Servers.Online, out.Servers.Offline = int(c.ServersOnline), int(c.ServersOffline)
	return out, nil
}

// owner authorizes the caller as a signed-in owner of the team. Others get
// 404 when they are not members, 403 otherwise.
func (s *Service) owner(ctx context.Context, teamID uuid.UUID) (*auth.Principal, error) {
	p, err := sessionUser(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.auth.MembershipIn(ctx, p.UserID, teamID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, apierr.NotFound("team")
	}
	if err := s.auth.RequireSecondFactorIn(ctx, p, teamID); err != nil {
		return nil, err
	}
	if m.Role != auth.RoleOwner {
		return nil, apierr.Forbidden()
	}
	return p, nil
}

// DeleteInput is the request of Delete.
type DeleteInput struct {
	// Confirm is the team's slug.
	Confirm string
	// KeepDNSRecords leaves the DNS records NebulaCtrl created in Cloudflare.
	KeepDNSRecords bool
}

// Delete starts the deletion of a team with everything in it. Only an owner
// may, with the slug typed as confirmation. The work takes as long as the
// agents need to empty their hosts, so it runs as a job: Delete returns at
// once with the state, the team shows "deleting" until the job has removed it,
// and a failed deletion stays on the team with its error until it is retried
// (by deleting again). A team that owns the server the control plane runs on
// cannot be deleted (409 team_hosts_control_plane).
//
// Teardown order: every project with everything in it, the platform services,
// every server (an offline one without touching its host), the status pages,
// the Cloudflare connection, the tailnet integration, then the team. Backups
// already written to external backup destinations stay there; the team's slices
// of the built-in services are kept 30 days, then purged (internal/instsvc).
func (s *Service) Delete(ctx context.Context, teamID uuid.UUID, in DeleteInput) (Team, error) {
	p, err := s.owner(ctx, teamID)
	if err != nil {
		return Team{}, err
	}
	row, err := s.q.GetWorkspace(ctx, teamID)
	if err != nil {
		return Team{}, fmt.Errorf("get team: %w", err)
	}
	if in.Confirm != row.Slug {
		return Team{}, apierr.Unprocessable("confirmation_mismatch", "type the team's slug to confirm deleting it")
	}
	var team Team
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		host, err := q.TeamHostsControlPlane(ctx, teamID)
		if err != nil {
			return err
		}
		if host {
			return apierr.Conflict("team_hosts_control_plane", "this team owns the server NebulaCtrl itself runs on, so it cannot be deleted")
		}
		if _, err = q.StartTeamDeletion(ctx, db.StartTeamDeletionParams{ID: teamID, KeepDns: in.KeepDNSRecords}); errors.Is(err, pgx.ErrNoRows) {
			return apierr.Conflict("deletion_in_progress", "this team is already being deleted")
		} else if err != nil {
			return err
		}
		if err := s.enqueueDeletion(ctx, tx, DeleteTeamArgs{TeamID: teamID}); err != nil {
			return err
		}
		// Read inside the transaction: the job cannot start before it commits, so
		// the team is still there to answer with.
		r, err := q.GetTeamForUser(ctx, db.GetTeamForUserParams{UserID: p.UserID, ID: teamID})
		if err != nil {
			return err
		}
		team = teamFromRow(r.ID, r.Name, r.Slug, r.Role, r.ProjectIds, int(r.MemberCount), r.CreatedAt, p.WorkspaceID,
			deletionOf(r.DeletionState, r.DeletionError, r.DeletionStep, r.DeletionStartedAt, r.DeletionForgotten, r.DeletionKeepDns))
		return audit.Record(ctx, tx, audit.Entry{
			Action: "team.delete.start", ResourceType: "team", ResourceID: teamID.String(), WorkspaceID: teamID,
			Metadata: map[string]any{"name": row.Name, "slug": row.Slug, "keepDnsRecords": in.KeepDNSRecords},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return Team{}, err
		}
		return Team{}, fmt.Errorf("start team deletion: %w", err)
	}
	s.publish("team.updated", teamID)
	return team, nil
}

func (s *Service) enqueueDeletion(ctx context.Context, tx pgx.Tx, args DeleteTeamArgs) error {
	if s.jobs == nil {
		return errors.New("no job manager to run the team deletion")
	}
	return s.jobs.InsertTx(ctx, tx, args, nil)
}

// deletionOf is the API view of the deletion columns; nil when none is going on.
func deletionOf(state, errMsg, step string, started *time.Time, forgotten []string, keepDNS bool) *TeamDeletion {
	if state == "" {
		return nil
	}
	if forgotten == nil {
		forgotten = []string{}
	}
	return &TeamDeletion{State: state, Step: step, Error: errMsg, ForgottenServers: forgotten, KeepDNSRecords: keepDNS, StartedAt: started}
}

// DeleteTeamArgs is the job that tears a team down.
type DeleteTeamArgs struct {
	TeamID uuid.UUID `json:"teamId"`
}

// Kind implements river.JobArgs.
func (DeleteTeamArgs) Kind() string { return "delete_team" }

// InsertOpts implements river.JobArgsWithInsertOpts: only an infrastructure
// error (the database) is retried; a step that fails is stored on the team.
func (DeleteTeamArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 3} }

type deleteTeamWorker struct {
	river.WorkerDefaults[DeleteTeamArgs]
	svc *Service
}

func (w *deleteTeamWorker) Timeout(*river.Job[DeleteTeamArgs]) time.Duration { return deletionTimeout }

func (w *deleteTeamWorker) Work(ctx context.Context, job *river.Job[DeleteTeamArgs]) error {
	return w.svc.RunDeletion(ctx, job.Args.TeamID)
}

// RegisterJobs adds the worker of the team deletion job.
func RegisterJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &deleteTeamWorker{svc: svc}) })
}

// sysCtx is ctx carrying the control plane's own administrator principal for
// the team: the domains the teardown calls authorize it like an owner, and
// audit rows say "system".
func sysCtx(ctx context.Context, teamID uuid.UUID) context.Context {
	return auth.WithPrincipal(ctx, &auth.Principal{
		Type: auth.TypeSystem, WorkspaceID: teamID, Label: "Team deletion", Scopes: []string{policy.ScopeAdmin},
	})
}

// RunDeletion is the team deletion job. A step that fails stops the job and is
// stored on the team (deletion_state failed with the reason); it is not
// returned, so the job does not retry by itself: the owner retries.
func (s *Service) RunDeletion(ctx context.Context, teamID uuid.UUID) error {
	ws, err := s.q.GetWorkspace(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // gone already
	}
	if err != nil {
		return fmt.Errorf("get team: %w", err)
	}
	if ws.DeletionState != DeletionDeleting {
		return nil // superseded
	}
	if s.teardown == nil {
		return errors.New("team deletion is not configured")
	}
	ctx = sysCtx(ctx, teamID)
	td := s.teardown

	fail := func(what string, err error) error {
		msg := err.Error()
		if ae, ok := apierr.As(err); ok {
			msg = ae.Error()
		}
		if what != "" {
			msg = what + ": " + msg
		}
		s.log.WarnContext(ctx, "team deletion failed", "team_id", teamID, "err", msg)
		if err := s.q.FailTeamDeletion(ctx, db.FailTeamDeletionParams{ID: teamID, Error: msg}); err != nil {
			return fmt.Errorf("record team deletion failure: %w", err)
		}
		s.publish("team.updated", teamID)
		return nil
	}
	step := func(name string) error {
		if err := s.q.SetTeamDeletionStep(ctx, db.SetTeamDeletionStepParams{ID: teamID, Step: name}); err != nil {
			return fmt.Errorf("record team deletion step: %w", err)
		}
		s.publish("team.updated", teamID)
		return nil
	}

	// 1. Every project, then the platform services of the hidden system project.
	if err := step(StepProjects); err != nil {
		return err
	}
	projects, err := s.q.ListTeamProjectIDs(ctx, teamID)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		if p.System {
			continue
		}
		if err := td.DeleteProject(ctx, p.ID); err != nil {
			return fail("project "+p.Name, err)
		}
	}
	if err := td.RemovePlatformServices(ctx); err != nil {
		return fail("platform services", err)
	}

	// 2. Every server; the agents empty their hosts (this is what takes time).
	if err := step(StepServers); err != nil {
		return err
	}
	servers, err := s.q.ListTeamServers(ctx, teamID)
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	for _, sv := range servers {
		outcome, err := td.RemoveServer(ctx, sv.ID)
		if err != nil {
			return fail("server "+sv.Name, err)
		}
		if outcome == ServerForgotten {
			if err := s.q.AddTeamDeletionForgotten(ctx, db.AddTeamDeletionForgottenParams{ID: teamID, Server: sv.Name}); err != nil {
				return fmt.Errorf("record forgotten server: %w", err)
			}
			s.publish("team.updated", teamID)
		}
	}

	// 3. What the team connected to the outside world.
	if err := step(StepIntegrations); err != nil {
		return err
	}
	if err := td.DeleteStatusPages(ctx); err != nil {
		return fail("status pages", err)
	}
	if err := td.DisconnectCloudflare(ctx, ws.DeletionKeepDns); err != nil {
		return fail("Cloudflare", err)
	}
	if err := td.DisconnectTailscale(ctx); err != nil {
		return fail("Tailscale", err)
	}

	// 4. The team. Notification channels, registries, secrets, tokens and the
	// rest are database rows that go with it.
	if err := step(StepTeam); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetWorkspace(ctx, teamID)
		if err != nil {
			return err
		}
		// The team's slices of the built-in services (bucket, key, images, logs)
		// are kept 30 days, then purged; the instance administrator can purge
		// them sooner.
		if s.slices != nil {
			if err := s.slices.MarkDeletedTx(ctx, tx, teamID); err != nil {
				return err
			}
		}
		if _, err := q.DeleteTeam(ctx, teamID); err != nil {
			return err
		}
		// The team's own audit log goes with it; the instance's log keeps nothing
		// of a team that no longer exists, so the facts are in the job's log.
		s.log.InfoContext(ctx, "team deleted", "team_id", teamID, "name", cur.Name, "slug", cur.Slug, "forgotten_servers", cur.DeletionForgotten)
		return nil
	})
	if err != nil {
		return fail("", fmt.Errorf("delete the team: %w", err))
	}
	s.publish("team.deleted", teamID)
	return nil
}
