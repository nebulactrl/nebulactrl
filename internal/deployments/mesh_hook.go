package deployments

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// MeshHook is the cross-server service link domain (internal/meshlinks) as the
// deployments domain sees it: a consumer on one server reaches providers of its
// environment on other servers through the mesh.
type MeshHook interface {
	// PlanCheck is the mesh part of a plan: the links the service needs, the
	// devices they add and the blockers. It never writes.
	PlanCheck(ctx context.Context, q *db.Queries, sc services.Scope) (services.MeshPlan, error)
	// Prepare opens the links, exposures and grants the deployment needs (the
	// deploy of the consumer is the confirmation of the devices the plan listed).
	// It fails with an API error when a blocker prevents them. Automated
	// redeploys (trigger reconcile) never add anything.
	Prepare(ctx context.Context, serviceID uuid.UUID, trigger string) error
	// WaitReady reports what the deployment still waits for ("" when every link
	// it needs carries traffic). It fails with mesh_link_not_ready once the wait
	// that started at since has lasted too long.
	WaitReady(ctx context.Context, serviceID uuid.UUID, since time.Time) (waiting string, err error)
	// Alias is the extra network alias the service's containers carry while it is
	// exposed on the mesh ("" when it is not): DeployPayload.MeshAlias.
	Alias(ctx context.Context, q *db.Queries, serviceID uuid.UUID) (string, error)
}

// SetMesh installs the service link domain (wire time only).
func (s *Service) SetMesh(h MeshHook) { s.Mesh = h }

// meshGate runs before a deployment is prepared: it creates the links the
// deployment needs and waits for them. A non-empty wait is a buildWaitError
// (the job snoozes; the deployment stays in preparing), an API error fails the
// deployment before anything is created.
func (s *Service) meshGate(ctx context.Context, dc depCtx) error {
	if s.Mesh == nil {
		return nil
	}
	dep := dc.Dep
	if err := s.Mesh.Prepare(ctx, dep.ServiceID, dep.Trigger); err != nil {
		return err
	}
	since := dep.CreatedAt
	if dep.StartedAt != nil {
		since = *dep.StartedAt
	}
	waiting, err := s.Mesh.WaitReady(ctx, dep.ServiceID, since)
	if err != nil {
		return err
	}
	if waiting != "" {
		return &buildWaitError{message: waiting}
	}
	return nil
}

// meshPendingMessage says why the resolved variables still reach a provider on
// another server without a mesh route ("" when they do not): a deployment never
// proceeds with the provider's container port, which is not reachable.
func (in *inputs) meshPendingMessage() string {
	for _, u := range in.resolved.MeshUses {
		if u.Pending {
			return u.ProviderSlug + " runs on another server and has no mesh route yet (mesh_route_pending)"
		}
	}
	return ""
}

// meshWarnings are the plan's non-blocking mesh notes: devices the deploy adds
// and providers whose route does not exist yet.
func (in *inputs) meshWarnings() []string {
	var out []string
	if n := in.mesh.DevicesAdded; n > 0 {
		out = append(out, fmt.Sprintf("Deploying adds %d tailnet %s (one mesh link per pair of servers): tagged devices may be billed by Tailscale.", n, plural(n, "device", "devices")))
	}
	for _, u := range in.resolved.MeshUses {
		if u.Pending {
			out = append(out, fmt.Sprintf("%s runs on another server: %s reaches it over the mesh, the route is created by this deployment (mesh_route_pending).", u.ProviderSlug, in.sc.Service.Slug))
		}
	}
	return out
}

// meshChanges are the links the next deployment adds, as pending changes ("Mesh:
// adds link A -> B (1 device)" in the staged review). A link that exists adds
// nothing and is not a change.
func (in *inputs) meshChanges() []services.FieldChange {
	var out []services.FieldChange
	for _, l := range in.mesh.Links {
		if l.State == "new" {
			out = append(out, services.FieldChange{Field: "mesh", To: fmt.Sprintf("adds link %s -> %s (1 device)", l.FromServerName, l.ToServerName)})
		}
	}
	return out
}
