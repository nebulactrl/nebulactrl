package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MeshBlocker is a reason a cross-server link cannot be created or used (mesh
// not enabled on a server, no /dev/net/tun on the consumer's host, mesh paused,
// the provider cannot be exposed, ...).
type MeshBlocker struct {
	Code       string     `json:"code"`
	Message    string     `json:"message"`
	ServerID   *uuid.UUID `json:"serverId,omitempty"`
	ServerName string     `json:"serverName,omitempty"`
}

// MeshPlanLink is one link node an action needs: the consumer's server dials
// the providers' server through it. State is new (the action creates it),
// pending (exists, not carrying traffic yet) or the link's own state (online,
// degraded, offline, ...).
type MeshPlanLink struct {
	FromServerID   uuid.UUID `json:"fromServerId"`
	FromServerName string    `json:"fromServerName"`
	ToServerID     uuid.UUID `json:"toServerId"`
	ToServerName   string    `json:"toServerName"`
	Purpose        string    `json:"purpose" enum:"service,replication"`
	State          string    `json:"state"`
	// Providers are the slugs of the services the link reaches for this action.
	Providers []string `json:"providers"`
}

// MeshPlan says what a deployment or a canvas link needs from the mesh: the
// links (existing ones included), the tailnet devices it adds and what blocks
// it. CrossServer is false for services of one server: nothing is needed.
type MeshPlan struct {
	CrossServer  bool           `json:"crossServer"`
	Links        []MeshPlanLink `json:"links"`
	DevicesAdded int            `json:"devicesAdded"`
	Blockers     []MeshBlocker  `json:"blockers"`
}

// EmptyMeshPlan is the plan of an action that needs no mesh.
func EmptyMeshPlan() MeshPlan {
	return MeshPlan{Links: []MeshPlanLink{}, Blockers: []MeshBlocker{}}
}

// MeshLinker is the cross-server service link domain (internal/meshlinks) as
// services sees it: canvas edges between services on different servers open and
// close mesh grants, and deleting or moving a service re-checks the grants.
type MeshLinker interface {
	// PlanEdge describes what the canvas edge from -> to needs from the mesh
	// (nothing when both run on one server). It never writes.
	PlanEdge(ctx context.Context, envID, fromID, toID uuid.UUID) (MeshPlan, error)
	// EdgeCreated opens the grants of the new edge in the link's transaction.
	// It is a no-op for services on one server and when the plan has blockers
	// (the deploy of the consumer reports them). The caller has checked the
	// confirmation of added devices.
	EdgeCreated(ctx context.Context, tx pgx.Tx, envID, fromID, toID uuid.UUID) error
	// EdgeDeleted removes the grants the edge opened, at once.
	EdgeDeleted(ctx context.Context, tx pgx.Tx, envID, fromID, toID uuid.UUID) error
	// ServiceDeleted drops every grant the service holds as a consumer and every
	// exposure it has as a provider, in the delete's transaction.
	ServiceDeleted(ctx context.Context, tx pgx.Tx, envID, serviceID uuid.UUID) error
	// Repair runs the removal-only reconciliation of the environment after a
	// placement change or a delete (it never adds a device).
	Repair(ctx context.Context, envID uuid.UUID)
}

// SetMeshLinker installs the cross-server link domain (wire time only).
func (s *Service) SetMeshLinker(m MeshLinker) { s.mesh = m }
