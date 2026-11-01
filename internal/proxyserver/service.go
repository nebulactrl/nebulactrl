package proxyserver

import (
	"context"
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
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// TailnetKeyMinter mints the single-use auth key a managed tailnet node is
// provisioned with. The deployments domain calls it at command send time
// (SecretsForCommand) when a proxy config asks for provisioning. Implemented
// by Service.
type TailnetKeyMinter interface {
	MintTailnetKey(ctx context.Context, serverID uuid.UUID) (string, error)
}

// RouteSource builds a server's full route table (domains.ServerRoutes). It is
// injected so this package does not import the domains package, whose tests
// import this one.
type RouteSource func(ctx context.Context, q *db.Queries, serverID uuid.UUID) ([]agentprotocol.Route, error)

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Dispatcher agents.Dispatcher
	Box        *secretbox.Box
	Tailscale  *tailscale.Service // nil = no integration (managed tailnet unavailable)
	Routes     RouteSource        // nil = proxy_configure carries no route table and the routes view is empty
	// Admins decides who is the instance administrator (the proxy ports are theirs to change); nil refuses.
	Admins AdminChecker
	Logger *slog.Logger
}

// Service implements the server-level proxy use cases: custom certificates,
// the upstream policy, the per-server tailnet and "Deploy proxy".
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	disp   agents.Dispatcher
	box    *secretbox.Box
	ts     *tailscale.Service
	routes RouteSource
	admins AdminChecker
	log    *slog.Logger
	now    func() time.Time
}

var _ TailnetKeyMinter = (*Service)(nil)

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{
		pool: d.Pool, q: db.New(d.Pool), bus: d.Bus, disp: d.Dispatcher, box: d.Box, ts: d.Tailscale, routes: d.Routes, admins: d.Admins,
		log: d.Logger.With("component", "proxyserver"), now: time.Now,
	}
}

func (s *Service) publish(serverID uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: "server.updated", ResourceID: serverID.String()})
	}
}

// loadServer authorizes scope and returns a server of the caller's workspace (404 otherwise).
func (s *Service) loadServer(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (db.Server, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Server{}, err
	}
	ws := policy.WorkspaceID(ctx)
	row, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.Server{}, fmt.Errorf("load server: %w", err)
	}
	return row, nil
}

func factsOf(row db.Server) agentprotocol.Facts {
	var f agentprotocol.Facts
	if len(row.Facts) > 0 {
		_ = json.Unmarshal(row.Facts, &f)
	}
	return f
}

// SupportsProxyV2 reports whether the server's agent advertised proxy.v2.
func SupportsProxyV2(row db.Server) bool {
	return slices.Contains(factsOf(row).Capabilities, agentprotocol.CapProxyV2)
}

// ErrAgentTooOld is the 422 returned when a proxy v2 feature is staged for a
// server whose agent does not advertise proxy.v2.
func ErrAgentTooOld() error {
	return apierr.Unprocessable("agent_too_old", "this server's agent is too old for the requested proxy feature; update the agent first")
}

func requireV2(row db.Server) error {
	if !SupportsProxyV2(row) {
		return ErrAgentTooOld()
	}
	return nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// DefaultTailnetHostname is "nebula-<server slug>", a valid tailnet machine name.
func DefaultTailnetHostname(serverName string) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(serverName), "-"), "-")
	if slug == "" {
		slug = "server"
	}
	h := "nebula-" + slug
	if len(h) > 63 {
		h = strings.TrimRight(h[:63], "-")
	}
	return h
}

var tailnetHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidTailnetHostname reports whether h is a valid tailnet machine name (one DNS label).
func ValidTailnetHostname(h string) bool { return tailnetHostRe.MatchString(h) }

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// MintTailnetKey mints a single-use, pre-authorized, tagged auth key for the
// server's workspace integration (tags = the integration's default tags,
// description "nebulactrl server <name>", expiry 1 hour). The key is only ever
// attached to the outgoing command frame.
func (s *Service) MintTailnetKey(ctx context.Context, serverID uuid.UUID) (string, error) {
	if s.ts == nil {
		return "", apierr.Unprocessable("tailnet_not_configured", "the Tailscale integration is not available")
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return "", fmt.Errorf("load server: %w", err)
	}
	key, err := s.ts.MintAuthKey(ctx, srv.WorkspaceID, 3600, "nebulactrl server "+srv.Name)
	if errors.Is(err, tailscale.ErrNotConfigured) {
		return "", apierr.Unprocessable("tailnet_not_configured", "connect Tailscale in Team settings → Integrations before enabling a managed tailnet")
	}
	if err != nil {
		return "", fmt.Errorf("mint tailnet auth key: %w", err)
	}
	return key, nil
}

func (s *Service) integrationConfigured(ctx context.Context, ws uuid.UUID) bool {
	if s.ts == nil {
		return false
	}
	ok, err := s.ts.Configured(ctx, ws)
	return err == nil && ok
}

func agentsCommand(serverID uuid.UUID, payload json.RawMessage, key string) agents.NewCommand {
	return agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindProxyConfigure, Payload: payload, IdempotencyKey: key}
}
