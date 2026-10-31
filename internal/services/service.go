// Package services implements service CRUD, volumes, links, restart/stop,
// service logs and metrics, and the variable reference resolver.
package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Service implements the service use cases.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	box    *secretbox.Box
	disp   agents.Dispatcher
	logger *slog.Logger

	redactors   redactorCache
	routeHook   RouteHook
	tailnetHook TailnetHook
	dnsHook     DNSHook
	sourceHook  SourceHook
	gitLinkHook GitLinkHook
	mesh        MeshLinker
	dbGuard     DatabaseGuard
	ext         *ExtSecrets
}

// NewService returns a Service (pool, box and disp may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, box *secretbox.Box, disp agents.Dispatcher, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, q: db.New(pool), bus: bus, box: box, disp: disp, logger: logger}
}

// SetExtSecrets installs the app's external secret manager handle (used to
// mask fetched values in logs). Nil: none.
func (s *Service) SetExtSecrets(e *ExtSecrets) { s.ext = e }

// BuildViews renders service rows as API views. All rows must belong to
// projectID.
func BuildViews(ctx context.Context, q *db.Queries, box *secretbox.Box, rows []db.Service, projectID uuid.UUID) ([]ServiceView, error) {
	if len(rows) == 0 {
		return []ServiceView{}, nil
	}
	ids := make([]uuid.UUID, len(rows))
	var serverIDs []uuid.UUID
	for i, r := range rows {
		ids[i] = r.ID
		if r.ServerID != nil {
			serverIDs = append(serverIDs, *r.ServerID)
		}
	}
	servers := map[uuid.UUID]db.Server{}
	if len(serverIDs) > 0 {
		srv, err := q.LoadServerRows(ctx, serverIDs)
		if err != nil {
			return nil, fmt.Errorf("load servers: %w", err)
		}
		for _, s := range srv {
			servers[s.ID] = s
		}
	}
	doms, err := q.ListDomainsForServices(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	domViews, err := DomainViews(ctx, q, doms)
	if err != nil {
		return nil, err
	}
	domBySvc := map[uuid.UUID][]DomainView{}
	for i, d := range doms {
		domBySvc[d.ServiceID] = append(domBySvc[d.ServiceID], domViews[i])
	}
	latest, err := q.ListLatestDeploymentsForServices(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("latest deployments: %w", err)
	}
	latestBySvc := map[uuid.UUID]db.Deployment{}
	for _, d := range latest {
		latestBySvc[d.ServiceID] = d
	}
	var curDeps []uuid.UUID
	for _, r := range rows {
		if r.CurrentDeploymentID != nil {
			curDeps = append(curDeps, *r.CurrentDeploymentID)
		}
	}
	releaseByDep := map[uuid.UUID]*ReleaseRef{}
	releaseRowByDep := map[uuid.UUID]db.Release{}
	if len(curDeps) > 0 {
		deps, err := q.ListDeploymentsByIDs(ctx, curDeps)
		if err != nil {
			return nil, fmt.Errorf("current deployments: %w", err)
		}
		var relIDs []uuid.UUID
		for _, d := range deps {
			if d.ReleaseID != nil {
				relIDs = append(relIDs, *d.ReleaseID)
			}
		}
		rels := map[uuid.UUID]db.Release{}
		if len(relIDs) > 0 {
			rr, err := q.ListReleasesByIDs(ctx, relIDs)
			if err != nil {
				return nil, fmt.Errorf("current releases: %w", err)
			}
			for _, r := range rr {
				rels[r.ID] = r
			}
		}
		for _, d := range deps {
			if d.ReleaseID == nil {
				continue
			}
			if r, ok := rels[*d.ReleaseID]; ok {
				releaseByDep[d.ID] = &ReleaseRef{ID: r.ID, ImageRef: r.ImageRef, Source: DecodeSource(r.Source)}
				releaseRowByDep[d.ID] = r
			}
		}
	}

	gitRefs, err := q.ListServiceGitRefs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list git links: %w", err)
	}
	gitBySvc := make(map[uuid.UUID]*GitRef, len(gitRefs))
	for _, g := range gitRefs {
		wp := g.WatchPaths
		if wp == nil {
			wp = []string{}
		}
		gitBySvc[g.ServiceID] = &GitRef{
			ConnectionID: g.ConnectionID, Provider: g.Provider, ConnectionName: g.ConnectionName, Repo: g.RepoFullName,
			AutoDeploy: g.AutoDeploy, WatchPaths: wp,
		}
	}

	out := make([]ServiceView, 0, len(rows))
	now := time.Now()
	for _, r := range rows {
		v := ServiceView{
			ID: r.ID, EnvironmentID: r.EnvironmentID, ProjectID: projectID, Name: r.Name, Slug: r.Slug, Kind: r.Kind,
			SourceType: r.SourceType, Config: DecodeConfig(r.Config), ServerID: r.ServerID,
			Generation: r.Generation, AppliedGeneration: r.AppliedGeneration, HasStagedChanges: r.Generation > r.AppliedGeneration,
			CurrentDeploymentID: r.CurrentDeploymentID, Observed: DecodeObserved(r.Observed), ObservedAt: r.ObservedAt,
			Domains: domBySvc[r.ID], PrivateHost: r.Slug, GitTokenSet: len(r.GitTokenEnc) > 0, RegistryAuthSet: len(r.RegistryAuthEnc) > 0,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Git: gitBySvc[r.ID],
		}
		v.Maintenance = MaintenanceView{
			Enabled: r.MaintenanceEnabled, Title: r.MaintenanceTitle, Message: r.MaintenanceMessage,
			ETA: r.MaintenanceEta, Since: r.MaintenanceSince,
		}
		if v.Domains == nil {
			v.Domains = []DomainView{}
		}
		if r.ServerID != nil {
			if s, ok := servers[*r.ServerID]; ok {
				v.Server = &ServerRef{ID: s.ID, Name: s.Name, Status: s.Status, LastSeenAt: s.LastSeenAt}
			}
		}
		if d, ok := latestBySvc[r.ID]; ok {
			v.LatestDeployment = &DeploymentRef{
				ID: d.ID, Status: d.Status, Trigger: d.Trigger, Strategy: d.Strategy, StatusMessage: d.StatusMessage,
				CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt, ReleaseID: d.ReleaseID, Message: optString(d.Message),
			}
		}
		if r.CurrentDeploymentID != nil {
			v.CurrentRelease = releaseByDep[*r.CurrentDeploymentID]
		}
		if v.Observed != nil {
			offline := v.Server != nil && v.Server.Status != "online"
			v.Stale = offline || r.ObservedAt == nil || now.Sub(*r.ObservedAt) > agentprotocol.OfflineAfter
		}
		if r.Kind == KindRoute {
			applyRouteStatus(&v, r)
		}
		out = append(out, v)
	}
	if err := decorateViews(ctx, q, box, rows, out); err != nil {
		return nil, err
	}
	if err := decorateScaling(ctx, q, rows, out); err != nil {
		return nil, err
	}
	if err := decorateProcesses(ctx, q, rows, out, releaseRowByDep); err != nil {
		return nil, err
	}
	if err := decorateTailnet(ctx, q, rows, out); err != nil {
		return nil, err
	}
	if err := decorateNetworks(ctx, q, rows, out); err != nil {
		return nil, err
	}
	return out, nil
}

// decorateProcesses fills ServiceView.Processes (out is parallel to rows). Only
// services whose staged config has processes read scale rows and their
// release's config; the others get an empty list. releases are the current
// releases keyed by deployment id.
func decorateProcesses(ctx context.Context, q *db.Queries, rows []db.Service, out []ServiceView, releases map[uuid.UUID]db.Release) error {
	var ids []uuid.UUID
	for i, r := range rows {
		out[i].Processes = []ProcessView{}
		if out[i].Config.HasProcesses() {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	scales, err := q.ListProcessScalesForServices(ctx, ids)
	if err != nil {
		return fmt.Errorf("list process scales: %w", err)
	}
	scaleBySvc := map[uuid.UUID][]db.ServiceProcessScale{}
	for _, s := range scales {
		scaleBySvc[s.ServiceID] = append(scaleBySvc[s.ServiceID], s)
	}
	vols, err := ServiceVolumeCounts(ctx, q, ids)
	if err != nil {
		return err
	}
	for i, r := range rows {
		if !out[i].Config.HasProcesses() {
			continue
		}
		var applied *Config
		if r.CurrentDeploymentID != nil {
			if rel, ok := releases[*r.CurrentDeploymentID]; ok {
				cfg := DecodeSnapshot(rel.Config).Config
				applied = &cfg
			}
		}
		out[i].Processes = BuildProcessViews(r, out[i].Config, applied, scaleBySvc[r.ID], vols[r.ID], out[i].Observed)
	}
	return nil
}

// ViewFor renders a single scoped service.
func (s *Service) ViewFor(ctx context.Context, sc Scope) (ServiceView, error) {
	vs, err := BuildViews(ctx, s.q, s.box, []db.Service{sc.Service}, sc.ProjectID)
	if err != nil {
		return ServiceView{}, err
	}
	return vs[0], nil
}

// Get returns one service.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (ServiceView, error) {
	sc, err := LoadScope(ctx, s.q, policy.ScopeProjectsRead, id)
	if err != nil {
		return ServiceView{}, err
	}
	return s.ViewFor(ctx, sc)
}

// List returns an environment's services, oldest first.
func (s *Service) List(ctx context.Context, envID uuid.UUID, cursor string, limit int) ([]ServiceView, string, error) {
	env, err := LoadEnvironment(ctx, s.q, policy.ScopeProjectsRead, envID)
	if err != nil {
		return nil, "", err
	}
	params := db.ListEnvironmentServicesParams{EnvironmentID: envID, RowLimit: int32(limit + 1)}
	ts, id, ok, err := api.DecodeTimeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if ok {
		params.CursorCreatedAt, params.CursorID = &ts, &id
	}
	rows, err := s.q.ListEnvironmentServices(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("list services: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = api.EncodeTimeCursor(last.CreatedAt, last.ID)
	}
	views, err := BuildViews(ctx, s.q, s.box, rows, env.Env.ProjectID)
	if err != nil {
		return nil, "", err
	}
	return views, next, nil
}

func serverNotUsable(name, status string) *apierr.Error {
	return apierr.Unprocessable("server_unavailable", fmt.Sprintf("server %q is %s", name, status))
}

// Server drain states (servers.drain_state). Draining and drained servers are
// not schedulable: no service may be created on or moved to them.
const (
	DrainStateNone     = "none"
	DrainStateDraining = "draining"
	DrainStateDrained  = "drained"
	DrainStateFailed   = "failed"
)

// ServerDrainingError is the 422 returned when placing a service on a server
// that is being (or has been) drained.
func ServerDrainingError(name string) *apierr.Error {
	return apierr.Unprocessable("server_draining", fmt.Sprintf("%s is being drained", name))
}

// ServerNotAcceptingError is the 422 returned when placing a service on a
// server whose "Accept workloads" setting is off.
func ServerNotAcceptingError(name string) *apierr.Error {
	return apierr.Unprocessable("server_not_accepting_workloads", fmt.Sprintf("%s does not accept workloads; turn on \"Accept workloads\" in its settings to schedule services there", name))
}
