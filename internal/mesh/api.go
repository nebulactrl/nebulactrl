package mesh

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// Provider is what the replica and service-link domains need from the mesh. It
// is the only surface they use: they never touch mesh_* tables directly.
//
// All mutating methods run inside the caller's transaction and are idempotent.
// None of them sends a command: the desired mesh set of a server is derived from
// the tables (nodes, exposures, links and the aliases of their grants) by the
// reconcile job, which the mutations nudge after commit (nudgeSync, nudgeACL).
type Provider interface {
	// PlanLink describes what ensuring a link between two servers for a purpose
	// would do: the devices it adds and what blocks it (mesh not enabled on a
	// server, no /dev/net/tun on the consumer side, agent too old, mesh paused,
	// coordinator unreachable). It never writes.
	PlanLink(ctx context.Context, q *db.Queries, env, consumerSrv, providerSrv uuid.UUID, purpose string) (LinkPlan, error)
	// EnsureExposure makes containerPort of a service reachable on its server's
	// node and allocates the mesh port (policy: replication takes the first free
	// port of 15000-15999; service keeps the container port when free on that
	// node, else the first free one). Idempotent: an existing exposure of the
	// (service, purpose) pair is returned unchanged.
	EnsureExposure(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, purpose string, containerPort int) (Exposure, error)
	// EnsureLink makes sure the link node of (env, localSrv -> remoteSrv, purpose)
	// exists. Idempotent.
	EnsureLink(ctx context.Context, tx pgx.Tx, env, localSrv, remoteSrv uuid.UUID, purpose string) (Link, error)
	// Grant lets a link reach an exposure on behalf of ref (the consumer service,
	// or the replica for source "replica"). It rejects an environment mismatch
	// (the link's environment is not the exposure's) and a purpose mismatch, so a
	// link of one environment can never be the source of a rule that opens another
	// environment's port. Idempotent.
	Grant(ctx context.Context, tx pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error
	// Revoke removes one grant. A link without grants and an exposure without
	// grants are released (ref-counted cascade) when no other user needs them.
	Revoke(ctx context.Context, tx pgx.Tx, linkID, exposureID, refID uuid.UUID, source string) error
	// Release drops EVERY grant (any source) of ref, then cascades like Revoke.
	// Used when a consumer or replica is deleted.
	Release(ctx context.Context, tx pgx.Tx, refID uuid.UUID) error
	// Ready reports whether a link carries traffic: its node is online and the ACL
	// is applied (or advisory/off).
	Ready(ctx context.Context, linkID uuid.UUID) (Readiness, error)
	// Check runs mesh_check through the dispatcher: the agent dials the link's
	// address on the environment network at the exposure's mesh port.
	Check(ctx context.Context, linkID, exposureID uuid.UUID) (CheckResult, error)
	// RouteOf resolves what a consumer on consumerSrv dials to reach a provider
	// service on another server: the provider's slug (the alias on the link) and
	// its mesh port. ok is false when the provider has no service exposure or runs
	// on the consumer's server.
	RouteOf(ctx context.Context, q *db.Queries, consumerSrv, providerServiceID uuid.UUID) (Route, bool, error)
}

// Blocker is a reason a link cannot be created or used.
type Blocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LinkPlan is the dry-run of EnsureLink.
type LinkPlan struct {
	// DevicesAdded is the number of tailnet devices the action adds (0 when the
	// link already exists; server nodes of a server without mesh are NOT counted:
	// that is a blocker, enabling mesh is its own action).
	DevicesAdded int       `json:"devicesAdded"`
	Blockers     []Blocker `json:"blockers"`
	Warnings     []string  `json:"warnings"`
	// LinkExists is true when the link node already exists.
	LinkExists bool `json:"linkExists"`
}

// Exposure is a service port reachable on a server node.
type Exposure struct {
	ID            uuid.UUID
	ServerID      uuid.UUID
	ServiceID     uuid.UUID
	EnvironmentID uuid.UUID
	Purpose       string
	ContainerPort int
	MeshPort      int
}

// Link is the link node of (environment, local server -> remote server, purpose).
type Link struct {
	ID             uuid.UUID
	EnvironmentID  uuid.UUID
	ServerID       uuid.UUID
	RemoteServerID uuid.UUID
	Purpose        string
	State          string
}

// Readiness is the answer of Provider.Ready.
type Readiness struct {
	Online bool
	ACLOK  bool
	// Reason says what is missing when the link is not ready.
	Reason string
}

// CheckResult is the answer of Provider.Check.
type CheckResult struct {
	// Result is one of agentprotocol.MeshCheck* (connected, refused, timeout, no_path).
	Result  string
	Message string
}

// Route is how a consumer reaches a provider on another server.
type Route struct {
	// Host is the provider's slug: the alias the link node answers to.
	Host string
	// Port is the provider's mesh port.
	Port   int
	LinkID uuid.UUID
	State  string
}
