// Package mesh is the control-plane side of the managed tailnet server mesh.
//
// The agent joins each mesh-enabled server to the team's tailnet (Tailscale or
// Headscale) with two kinds of containers: a userspace server node per server
// (ingress through `tailscale serve`) and kernel link nodes per environment and
// server pair (egress). This package owns the desired state (server_mesh,
// mesh_links, mesh_exposures, mesh_grants), derives the complete node set of a
// server from it (sync_mesh), mints single-use node keys at send time, generates
// and applies the ACL (internal/mesh/acl) and implements Provider for the
// replica and cross-server link domains.
//
// Secrets: node keys are minted when a command is sent, ride only in
// Command.Secrets and never reach agent_commands.payload, audit rows, the API or
// logs.
package mesh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// keyExpiry is how long a minted node key stays valid. It is consumed on the
// first login; an unused key is revoked by the sweeper.
const keyExpiry = time.Hour

// observedStale is how old an observation may get before the views stop calling
// the node online.
const observedStale = 60 * time.Second

// openCommandGrace is how long an acked, unfinished mesh command keeps the
// reconcile from sending another one (a sync is a complete, idempotent set, so
// sending again later is always safe).
const openCommandGrace = 10 * time.Minute

// ACL states (mesh_settings.acl_state) and modes (acl_mode).
const (
	StateUnknown  = "unknown"
	StateApplied  = "applied"
	StatePending  = "pending"
	StateDrift    = "drift"
	StateAdvisory = "advisory"
	StateError    = "error"

	ModeManaged  = "managed"
	ModeAdvisory = "advisory"
	ModeOff      = "off"
)

// DefaultMeshTag is the tag every mesh node of a team carries.
const DefaultMeshTag = "tag:nebula-mesh"

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Jobs       *jobs.Manager
	Dispatcher agents.Dispatcher
	Tailscale  *tailscale.Service
	Logger     *slog.Logger
}

// Service implements the mesh use cases, the Provider interface and the hooks
// of the agents and servers domains.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	jobs *jobs.Manager
	disp agents.Dispatcher
	ts   *tailscale.Service
	log  *slog.Logger
	now  func() time.Time
	// awaitPoll is the polling interval of awaitCommand (tests shorten it).
	awaitPoll time.Duration
}

var _ Provider = (*Service)(nil)

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{
		pool: d.Pool, q: db.New(d.Pool), bus: d.Bus, jobs: d.Jobs, disp: d.Dispatcher, ts: d.Tailscale,
		log: d.Logger.With("component", "mesh"), now: time.Now, awaitPoll: 400 * time.Millisecond,
	}
}

// publish tells the web app that the mesh changed.
func (s *Service) publish(ws uuid.UUID, servers ...uuid.UUID) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{Type: "mesh.updated", ResourceID: ws.String()})
	for _, id := range servers {
		s.bus.Publish(events.Event{Type: "server.updated", ResourceID: id.String()})
	}
}

// settings returns the workspace's mesh settings. It never writes: it runs inside
// read-only transactions too (the deployment plan and the link checks of a
// deploy), where even an INSERT ... ON CONFLICT DO NOTHING is refused. A team that
// never configured the mesh has no row; the defaults of the table apply, and the
// writers that need the row (settings update, ACL apply, enabling a server) create it.
func (s *Service) settings(ctx context.Context, q *db.Queries, ws uuid.UUID) (db.MeshSetting, error) {
	r, err := q.GetMeshSettings(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.MeshSetting{WorkspaceID: ws, MeshTag: "tag:nebula-mesh", AclMode: "advisory", AclState: "unknown"}, nil
	}
	if err != nil {
		return db.MeshSetting{}, fmt.Errorf("get mesh settings: %w", err)
	}
	return r, nil
}

// integration returns the tailnet integration's settings (mesh_not_configured
// when the team has none).
func (s *Service) integration(ctx context.Context, ws uuid.UUID) (tailscale.Settings, error) {
	set, err := s.ts.Settings(ctx, ws)
	if errors.Is(err, tailscale.ErrNotConfigured) {
		return tailscale.Settings{}, apierr.MeshNotConfigured()
	}
	return set, err
}

// factsOf decodes a server's facts (zero value when it never connected).
func factsOf(srv db.Server) agentprotocol.Facts {
	var f agentprotocol.Facts
	if len(srv.Facts) > 0 {
		_ = json.Unmarshal(srv.Facts, &f)
	}
	return f
}

// factsRaw is factsOf for the raw column of a lighter row.
func factsRaw(raw json.RawMessage) agentprotocol.Facts {
	var f agentprotocol.Facts
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

// capable reports whether the agent advertised CapMesh. A server that never
// connected has no facts and is assumed to enroll with a current agent.
func capable(f agentprotocol.Facts) bool {
	if f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapMesh)
}

// SupportsMesh reports whether the server's agent manages mesh nodes.
func SupportsMesh(srv db.Server) bool { return capable(factsOf(srv)) }

// dnsLabel lowercases s into a DNS label of letters, digits and hyphens.
func dnsLabel(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if max > 0 && len(out) > max {
		out = strings.Trim(out[:max], "-")
	}
	if out == "" {
		out = "x"
	}
	return out
}

// serverHostname is the tailnet machine name of a server node.
func serverHostname(serverName string) string { return "nc-" + dnsLabel(serverName, 56) }

// linkHostname is the tailnet machine name of a link node (a DNS label, at most
// 63 characters; the real FQDN is read back from the node).
func linkHostname(env, local, remote string) string {
	e8 := agentprotocol.Short(env)
	l, r := dnsLabel(local, 20), dnsLabel(remote, 20)
	return "nc-" + e8 + "-" + l + "-to-" + r
}

// identityHash fingerprints what decides re-provisioning of a node: the
// coordinator, the machine name and the tag its key carries.
func identityHash(provider, loginServer, hostname, tag string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + loginServer + "\x00" + hostname + "\x00" + tag))
	return hex.EncodeToString(sum[:8])
}

// firstIPv4 returns the first IPv4 of ips ("" when none).
func firstIPv4(ips []string) string {
	for _, ip := range ips {
		if strings.Count(ip, ".") == 3 && !strings.Contains(ip, ":") {
			return ip
		}
	}
	return ""
}

// loadServer authorizes scope and returns a server of the caller's workspace.
func (s *Service) loadServer(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (db.Server, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Server{}, err
	}
	row, err := q.GetServerScoped(ctx, db.GetServerScopedParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.Server{}, fmt.Errorf("get server: %w", err)
	}
	return serverFromScoped(row), nil
}

// serverFromScoped drops the service count of a scoped row.
func serverFromScoped(r db.GetServerScopedRow) db.Server {
	return db.Server{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, PublicIp: r.PublicIp, Status: r.Status, AgentVersion: r.AgentVersion,
		Facts: r.Facts, LastSeenAt: r.LastSeenAt, CreatedAt: r.CreatedAt,
	}
}

// deployGrace is how long a node that was just written may be missing from the
// agent's report before it is called offline.
const deployGrace = 2 * time.Minute

var tagRe = regexp.MustCompile(`^tag:[a-zA-Z][a-zA-Z0-9-]{0,62}$`)

// tailscaleTag reports whether t is a valid tag name.
func tailscaleTag(t string) bool { return tagRe.MatchString(t) }
