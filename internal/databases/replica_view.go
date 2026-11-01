package databases

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Roles of a service in a replica group (ReplicationView.Role).
const (
	RoleStandalone = "standalone"
	RolePrimary    = "primary"
	RoleReplica    = "replica"
	RoleFenced     = "fenced"
)

// ReplicaStateView is the state of a replica as shown: its lifecycle state, or
// "unknown" while it should be streaming but its observation is stale.
type ReplicaStateView struct {
	State       string     `json:"state" doc:"pending, preparing, cloning, deploying, streaming, lagging, broken, needs_rebuild, promoting, removing, failed, or unknown (stale observation)"`
	LagBytes    *int64     `json:"lagBytes,omitempty"`
	ReplayLagMs *int64     `json:"replayLagMs,omitempty"`
	WalStatus   string     `json:"walStatus,omitempty"`
	Message     string     `json:"message"`
	ObservedAt  *time.Time `json:"observedAt,omitempty"`
}

// ReplicaMemberView is one replica of a primary.
type ReplicaMemberView struct {
	ID         uuid.UUID  `json:"id"`
	Slug       string     `json:"slug"`
	ServerID   *uuid.UUID `json:"serverId,omitempty"`
	ServerName string     `json:"serverName,omitempty"`
	ReplicaStateView
}

// PromotionView is the latest promotion that involved the service.
type PromotionView struct {
	ID         uuid.UUID  `json:"id"`
	Status     string     `json:"status" enum:"running,succeeded,failed"`
	Step       string     `json:"step"`
	Mode       string     `json:"mode" enum:"switchover,forced"`
	Message    string     `json:"message"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// ReplicationView is the replica-group panel of a PostgreSQL service. It
// describes manual replication: NebulaCtrl detects nothing and switches nothing
// by itself.
type ReplicationView struct {
	Role        string              `json:"role" enum:"standalone,primary,replica,fenced"`
	Primary     *ServiceRef         `json:"primary,omitempty" doc:"The primary a replica follows"`
	Self        *ReplicaStateView   `json:"self,omitempty" doc:"The viewed service's own replica state (role replica)"`
	Replicas    []ReplicaMemberView `json:"replicas"`
	Rebuildable bool                `json:"rebuildable" doc:"A replica that can be rebuilt, or a fenced old primary that can be rebuilt as a replica"`
	CanPromote  bool                `json:"canPromote"`
	Promotion   *PromotionView      `json:"promotion,omitempty"`
}

func (s *Service) stateView(r db.DatabaseReplica) ReplicaStateView {
	v := ReplicaStateView{
		State: r.State, LagBytes: r.LagBytes, ReplayLagMs: r.ReplayLagMs, Message: r.Message, ObservedAt: r.ObservedAt,
	}
	if r.WalStatus != nil {
		v.WalStatus = *r.WalStatus
	}
	if (r.State == ReplicaStreaming || r.State == ReplicaLagging) && (r.ObservedAt == nil || s.now().Sub(*r.ObservedAt) > StaleAfter) {
		v.State = "unknown"
	}
	return v
}

// Replication returns the replica-group panel of a PostgreSQL database.
func (s *Service) Replication(ctx context.Context, serviceID uuid.UUID) (ReplicationView, error) {
	sc, _, _, err := s.managed(ctx, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return ReplicationView{}, err
	}
	return s.replicationView(ctx, s.q, sc)
}

func (s *Service) replicationView(ctx context.Context, q *db.Queries, sc services.Scope) (ReplicationView, error) {
	v := ReplicationView{Role: RoleStandalone, Replicas: []ReplicaMemberView{}}
	id := sc.Service.ID
	if s.hasFence(ctx, q, id) {
		v.Role, v.Rebuildable = RoleFenced, true
	}
	r, isReplica, err := s.replicaRow(ctx, q, id)
	if err != nil {
		return v, err
	}
	if isReplica {
		v.Role = RoleReplica
		sv := s.stateView(r)
		v.Self = &sv
		if psc, err := services.LoadScopeInternal(ctx, q, r.PrimaryServiceID); err == nil {
			v.Primary = &ServiceRef{ID: psc.Service.ID, Slug: psc.Service.Slug}
		}
		switch r.State {
		case ReplicaStreaming, ReplicaLagging, ReplicaBroken:
			v.CanPromote = true
			v.Rebuildable = true
		case ReplicaNeedsRebuild, ReplicaFailed:
			v.Rebuildable = true
		}
	}
	rows, err := s.groupMembers(ctx, q, id)
	if err != nil {
		return v, err
	}
	if len(rows) > 0 && v.Role == RoleStandalone {
		v.Role = RolePrimary
	}
	servers := map[uuid.UUID]string{}
	for _, row := range rows {
		m := ReplicaMemberView{ID: row.DatabaseReplica.ServiceID, Slug: row.Slug, ServerID: row.ServerID, ReplicaStateView: s.stateView(row.DatabaseReplica)}
		if row.ServerID != nil {
			name, ok := servers[*row.ServerID]
			if !ok {
				if srv, err := s.loadServer(ctx, q, sc.WorkspaceID, *row.ServerID); err == nil {
					name = srv.Name
				}
				servers[*row.ServerID] = name
			}
			m.ServerName = name
		}
		v.Replicas = append(v.Replicas, m)
	}
	if busy, err := s.promotionRunningIn(ctx, q, id); err == nil && busy {
		v.CanPromote = false
	}
	if p, err := q.GetLatestDatabasePromotionOfService(ctx, id); err == nil {
		if p.Status == "running" || (p.FinishedAt != nil && s.now().Sub(*p.FinishedAt) < 7*24*time.Hour) {
			v.Promotion = &PromotionView{
				ID: p.ID, Status: p.Status, Step: p.Step, Mode: p.Mode, Message: p.Message, StartedAt: p.StartedAt, FinishedAt: p.FinishedAt,
			}
		}
	} else if !isNoRows(err) {
		return v, err
	}
	return v, nil
}
