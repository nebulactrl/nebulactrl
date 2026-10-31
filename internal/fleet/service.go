// Package fleet implements the cross-server views: the fleet overview (capacity
// and placement of every server) and server drains (evacuating a host by moving
// its services elsewhere).
package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// sampleWindow is how recent a per-service resource sample must be to count.
const sampleWindow = 2 * time.Minute

// Service implements fleet reads and server drains.
type Service struct {
	pool    *pgxpool.Pool
	q       *db.Queries
	bus     *events.Bus
	jobs    *jobs.Manager
	servers *servers.Service
	svcs    *services.Service
	deps    *deployments.Service
	log     *slog.Logger
	now     func() time.Time
}

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, jm *jobs.Manager, srv *servers.Service, svcs *services.Service, deps *deployments.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		pool: pool, q: db.New(pool), bus: bus, jobs: jm, servers: srv, svcs: svcs, deps: deps,
		log: logger.With("component", "fleet"), now: time.Now,
	}
}

func (s *Service) publish(serverID uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: "server.updated", ResourceID: serverID.String()})
	}
}

// requireFleetWide authorizes a fleet-wide operation: a drain touches every
// project placed on the server, so project-restricted tokens are refused.
func requireFleetWide(ctx context.Context, scopes ...string) (*auth.Principal, error) {
	for _, sc := range scopes {
		if err := policy.Require(ctx, sc, nil); err != nil {
			return nil, err
		}
	}
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return nil, err
	}
	if p.Restricted() {
		return nil, apierr.Forbidden()
	}
	return p, nil
}

// effectiveStatus reports a silent server as offline even before the sweep job
// has caught up (same rule as the servers API).
func (s *Service) effectiveStatus(srv db.Server) string {
	if srv.Status == "online" && (srv.LastSeenAt == nil || s.now().Sub(*srv.LastSeenAt) > agentprotocol.OfflineAfter) {
		return "offline"
	}
	return srv.Status
}

// hostInfo is a server with its latest resource numbers.
type hostInfo struct {
	srv    db.Server
	status string
	cpus   int
	// latest is nil until the first heartbeat.
	latest *db.LatestServerMetricSamplesRow
}

func (h *hostInfo) memTotal() int64 {
	if h.latest != nil && h.latest.MemLimitBytes > 0 {
		return h.latest.MemLimitBytes
	}
	var f agentprotocol.Facts
	if len(h.srv.Facts) > 0 && json.Unmarshal(h.srv.Facts, &f) == nil {
		return int64(f.MemTotal)
	}
	return 0
}

func (h *hostInfo) diskTotal() int64 {
	if h.latest != nil && h.latest.DiskTotalBytes > 0 {
		return h.latest.DiskTotalBytes
	}
	var f agentprotocol.Facts
	if len(h.srv.Facts) > 0 && json.Unmarshal(h.srv.Facts, &f) == nil {
		return int64(f.DiskTotal)
	}
	return 0
}

// snapshot is a consistent-enough read of the whole fleet.
type snapshot struct {
	hosts    map[uuid.UUID]*hostInfo
	order    []uuid.UUID // hosts by creation time
	services []db.ListFleetServicesRow
	samples  map[uuid.UUID]db.LatestServiceMetricSamplesRow
}

func (s *Service) load(ctx context.Context, ws uuid.UUID) (*snapshot, error) {
	srvRows, err := s.q.ListServersForFleet(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	snap := &snapshot{hosts: map[uuid.UUID]*hostInfo{}, samples: map[uuid.UUID]db.LatestServiceMetricSamplesRow{}}
	ids := make([]uuid.UUID, 0, len(srvRows))
	for _, r := range srvRows {
		h := &hostInfo{srv: r, status: s.effectiveStatus(r)}
		var f agentprotocol.Facts
		if len(r.Facts) > 0 && json.Unmarshal(r.Facts, &f) == nil {
			h.cpus = f.CPUs
		}
		snap.hosts[r.ID] = h
		snap.order = append(snap.order, r.ID)
		ids = append(ids, r.ID)
	}
	if len(ids) > 0 {
		latest, err := s.q.LatestServerMetricSamples(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("latest server samples: %w", err)
		}
		for i := range latest {
			snap.hosts[latest[i].ServerID].latest = &latest[i]
		}
	}
	if snap.services, err = s.q.ListFleetServices(ctx, db.ListFleetServicesParams{WorkspaceID: ws}); err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	svcIDs := make([]uuid.UUID, len(snap.services))
	for i, r := range snap.services {
		svcIDs[i] = r.ID
	}
	if len(svcIDs) > 0 {
		rows, err := s.q.LatestServiceMetricSamples(ctx, db.LatestServiceMetricSamplesParams{ServiceIds: svcIDs, Since: s.now().Add(-sampleWindow)})
		if err != nil {
			return nil, fmt.Errorf("latest service samples: %w", err)
		}
		for _, r := range rows {
			if r.ServiceID != nil {
				snap.samples[*r.ServiceID] = r
			}
		}
	}
	return snap, nil
}

// memLimit is the configured memory limit of a service in bytes (0 = none).
func memLimit(raw json.RawMessage) int64 {
	return int64(services.DecodeConfig(raw).MemoryMB) * mib
}

func stateful(r db.ListFleetServicesRow) bool { return r.VolumeCount > 0 || r.Kind == "database" }

// committed sums the memory limits of the services placed on each server and
// counts the ones without a limit.
func (sn *snapshot) committed() (limits map[uuid.UUID]int64, unlimited map[uuid.UUID]int) {
	limits, unlimited = map[uuid.UUID]int64{}, map[uuid.UUID]int{}
	for _, r := range sn.services {
		if r.ServerID == nil {
			continue
		}
		if l := memLimit(r.Config); l > 0 {
			limits[*r.ServerID] += l
		} else {
			unlimited[*r.ServerID]++
		}
	}
	return limits, unlimited
}

// capacityHosts builds the Host list used for target selection.
func (s *Service) capacityHosts(sn *snapshot, source uuid.UUID) []*Host {
	limits, _ := sn.committed()
	out := make([]*Host, 0, len(sn.order))
	for _, id := range sn.order {
		h := sn.hosts[id]
		ch := &Host{
			ID: id, Name: h.srv.Name, Committed: limits[id],
			Eligible: id != source && schedulable(h),
			HasData:  h.latest != nil && h.memTotal() > 0,
			MemTotal: h.memTotal(),
		}
		if h.latest != nil {
			ch.MemUsed = h.latest.MemBytes
		}
		out = append(out, ch)
	}
	return out
}

// schedulable reports whether new services may be placed on a server.
func schedulable(h *hostInfo) bool {
	return h.status == "online" && h.srv.AcceptWorkloads && h.srv.DrainState != services.DrainStateDraining && h.srv.DrainState != services.DrainStateDrained
}

func (s *Service) loadServer(ctx context.Context, scope string, id uuid.UUID) (db.GetServerScopedRow, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.GetServerScopedRow{}, err
	}
	row, err := s.q.GetServerScoped(ctx, db.GetServerScopedParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GetServerScopedRow{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.GetServerScopedRow{}, fmt.Errorf("get server: %w", err)
	}
	return row, nil
}
