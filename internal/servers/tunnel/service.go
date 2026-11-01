// Package tunnel is the control-plane side of the Cloudflare Tunnel of a server.
//
// Enabling a tunnel creates a remotely-managed tunnel at Cloudflare (with the
// team's Cloudflare connection), pushes its ingress rules (every hostname of the
// server's services to the proxy's web entrypoint, then a 404 catch-all) and
// tells the agent to run the cloudflared connector. The connector token is
// fetched from the Cloudflare API when the command is sent, attached as a
// send-time secret and never stored. Once the connector reports its first
// connection the tunnel is active: the server's domains in connected zones get
// the tunnel CNAME (the Cloudflare integration asks TunnelTarget) and are
// served as tunnelled (no HTTP-01, no HTTPS redirect). Disabling flips the DNS
// back, removes the connector and deletes the tunnel at Cloudflare.
package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// guardDisableTunnel is the operation name production approvals of a tunnel
// removal are bound to (the catalog's operation id).
const guardDisableTunnel = "disableServerTunnel"

// API is the part of the Cloudflare tunnel API the service uses
// (*cloudflare.Client implements it).
type API interface {
	CreateTunnel(ctx context.Context, accountID, name string) (cloudflare.Tunnel, error)
	PutTunnelConfig(ctx context.Context, accountID, tunnelID string, ingress []cloudflare.IngressRule) error
	TunnelToken(ctx context.Context, accountID, tunnelID string) (string, error)
	DeleteTunnel(ctx context.Context, accountID, tunnelID string) error
}

// Cloudflare is what the service needs from the team's Cloudflare connection.
type Cloudflare interface {
	// Client returns an API client and the connection's account ID, or
	// cloudflare.ErrNotConfigured when the team has no connection.
	Client(ctx context.Context, ws uuid.UUID) (api API, accountID string, err error)
	// EnqueueSyncServer re-syncs the DNS records of every domain on the server
	// inside tx (its tunnel appeared or went away).
	EnqueueSyncServer(ctx context.Context, tx pgx.Tx, ws, serverID uuid.UUID) error
}

// Adapter makes a *cloudflare.Service a Cloudflare.
type Adapter struct{ S *cloudflare.Service }

// Client implements Cloudflare.
func (a Adapter) Client(ctx context.Context, ws uuid.UUID) (API, string, error) {
	conn, err := a.S.Connection(ctx, ws)
	if err != nil {
		return nil, "", err
	}
	c, err := a.S.ClientFor(ctx, ws)
	if err != nil {
		return nil, "", err
	}
	return c, conn.AccountID, nil
}

// EnqueueSyncServer implements Cloudflare.
func (a Adapter) EnqueueSyncServer(ctx context.Context, tx pgx.Tx, ws, serverID uuid.UUID) error {
	return a.S.EnqueueSyncServer(ctx, tx, ws, serverID)
}

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Dispatcher agents.Dispatcher
	Jobs       *jobs.Manager
	Cloudflare Cloudflare
	Logger     *slog.Logger
}

// Service implements the server tunnel use cases and the hooks of the agents
// hub and the Cloudflare integration.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	disp agents.Dispatcher
	jm   *jobs.Manager
	cf   Cloudflare
	log  *slog.Logger
	now  func() time.Time
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{
		pool: d.Pool, q: db.New(d.Pool), bus: d.Bus, disp: d.Dispatcher, jm: d.Jobs, cf: d.Cloudflare,
		log: d.Logger.With("component", "tunnel"), now: time.Now,
	}
}

// deleteDelay is how long after a disable the tunnel is deleted at Cloudflare:
// the domains' DNS records flip back first and the connector goes away.
const deleteDelay = 45 * time.Second

// StateOff is View.State of a server without a tunnel.
const StateOff = "off"

// SupportsTunnel reports whether the server's agent advertised
// agentprotocol.CapTunnel. A server that has never connected has no facts yet
// and is assumed to enroll with a current agent.
func SupportsTunnel(srv db.Server) bool {
	if len(srv.Facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(srv.Facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapTunnel)
}

// TunnelView is the tunnel state of a server.
type TunnelView struct {
	Enabled bool   `json:"enabled" doc:"The server has a tunnel (or is removing one)"`
	State   string `json:"state" enum:"off,provisioning,online,offline,needs_attention,removing" doc:"Observed connector state; off when the server has no tunnel"`
	Message string `json:"message" doc:"Why the connector needs attention, or the last teardown problem"`

	// Active is true once the connector connected for the first time: from then
	// on the server's domains in connected zones point at the tunnel.
	Active      bool       `json:"active"`
	Connections int        `json:"connections" doc:"Ready connections to Cloudflare's edge"`
	TunnelID    string     `json:"tunnelId,omitempty"`
	TunnelName  string     `json:"tunnelName,omitempty"`
	CNAME       string     `json:"cname,omitempty" doc:"The DNS target of the tunnel: <tunnelId>.cfargotunnel.com"`
	AccountID   string     `json:"accountId,omitempty"`
	Image       string     `json:"image,omitempty"`
	ObservedAt  *time.Time `json:"observedAt"`

	// IngressHosts is the number of hostnames in the ingress rules last pushed to
	// Cloudflare; IngressError is the last push failure.
	IngressHosts    int        `json:"ingressHosts"`
	IngressSyncedAt *time.Time `json:"ingressSyncedAt"`
	IngressError    string     `json:"ingressError,omitempty"`

	// CloudflareConnected, AccountReady and AgentSupported tell whether a tunnel can be enabled.
	CloudflareConnected bool   `json:"cloudflareConnected" doc:"The team has a Cloudflare connection"`
	AccountReady        bool   `json:"accountReady" doc:"The connection has an account ID (needed for tunnels)"`
	AgentSupported      bool   `json:"agentSupported" doc:"The server's agent can run the cloudflared connector"`
	Upstream            string `json:"upstream" doc:"Where the tunnel forwards every request"`
}

func (s *Service) publish(serverID uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: "server.updated", ResourceID: serverID.String()})
	}
}

func (s *Service) loadServer(ctx context.Context, scope string, id uuid.UUID, extra ...string) (db.Server, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Server{}, err
	}
	for _, sc := range extra {
		if err := policy.Require(ctx, sc, nil); err != nil {
			return db.Server{}, err
		}
	}
	ws := policy.WorkspaceID(ctx)
	row, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.Server{}, fmt.Errorf("load server: %w", err)
	}
	return row, nil
}

func (s *Service) getRow(ctx context.Context, q *db.Queries, serverID uuid.UUID) (*db.ServerTunnel, error) {
	r, err := q.GetServerTunnel(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get server tunnel: %w", err)
	}
	return &r, nil
}

// view renders a server's tunnel state.
func (s *Service) view(ctx context.Context, srv db.Server, row *db.ServerTunnel) TunnelView {
	v := TunnelView{State: StateOff, AgentSupported: SupportsTunnel(srv), Upstream: agentprotocol.TunnelUpstream}
	if _, account, err := s.cf.Client(ctx, srv.WorkspaceID); err == nil {
		v.CloudflareConnected, v.AccountReady = true, account != ""
	} else if !errors.Is(err, cloudflare.ErrNotConfigured) {
		v.CloudflareConnected = true
	}
	if row == nil {
		return v
	}
	v.Enabled, v.State, v.Message, v.Connections = true, row.State, row.Message, int(row.Connections)
	v.Active = row.ActivatedAt != nil && row.State != "removing"
	v.TunnelID, v.TunnelName, v.AccountID, v.Image = row.TunnelID, row.TunnelName, row.AccountID, row.Image
	v.CNAME = cloudflare.TunnelCNAME(row.TunnelID)
	v.ObservedAt = row.ObservedAt
	v.IngressHosts, v.IngressSyncedAt, v.IngressError = int(row.IngressHosts), row.IngressSyncedAt, row.IngressError
	return v
}

// Get returns the tunnel state of a server.
func (s *Service) Get(ctx context.Context, serverID uuid.UUID) (TunnelView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersRead, serverID)
	if err != nil {
		return TunnelView{}, err
	}
	row, err := s.getRow(ctx, s.q, serverID)
	if err != nil {
		return TunnelView{}, err
	}
	return s.view(ctx, srv, row), nil
}

var nameSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// tunnelName is the display name of a server's tunnel at Cloudflare.
func tunnelName(srv db.Server) string {
	slug := strings.Trim(nameSlugRe.ReplaceAllString(strings.ToLower(srv.Name), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = "server"
	}
	return "nebulactrl-" + slug + "-" + agentprotocol.Short(srv.ID.String())
}

// apiError maps a Cloudflare failure onto an API error.
func apiError(err error, what string) error {
	var ae *cloudflare.APIError
	switch {
	case errors.Is(err, cloudflare.ErrNotConfigured):
		return apierr.Unprocessable("cloudflare_not_connected", "connect Cloudflare in Team settings → Integrations before enabling a tunnel")
	case errors.Is(err, cloudflare.ErrNoAccount):
		return apierr.Unprocessable("cloudflare_account_unknown", "the Cloudflare connection has no account selected; choose the account in Team settings → Integrations")
	case errors.Is(err, cloudflare.ErrAuth):
		return apierr.Unprocessable("cloudflare_tunnel_forbidden", "the Cloudflare token cannot manage tunnels: add the permission Account, Cloudflare Tunnel, Edit to it")
	case errors.As(err, &ae):
		return apierr.New(http.StatusBadGateway, "cloudflare_error", what+": "+ae.Message)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// permanent reports whether retrying a failed Cloudflare call cannot help.
func permanent(err error) bool {
	if errors.Is(err, cloudflare.ErrNotConfigured) || errors.Is(err, cloudflare.ErrNoAccount) || errors.Is(err, cloudflare.ErrAuth) {
		return true
	}
	var ae *cloudflare.APIError
	return errors.As(err, &ae) && !ae.Retryable()
}

// ingress renders the ingress rules of a server's tunnel: every hostname of its
// live services to the proxy, then the 404 catch-all.
func (s *Service) ingress(ctx context.Context, q *db.Queries, serverID uuid.UUID) (rules []cloudflare.IngressRule, hash string, hosts int, err error) {
	names, err := q.ListServerIngressHostnames(ctx, &serverID)
	if err != nil {
		return nil, "", 0, fmt.Errorf("list server hostnames: %w", err)
	}
	rules = cloudflare.IngressForHosts(names, agentprotocol.TunnelUpstream)
	return rules, cloudflare.IngressHash(rules), len(rules) - 1, nil
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// enqueueSync queues a sync_tunnel command. The payload names the tunnel; the
// token is attached when the command is sent.
func (s *Service) enqueueSync(ctx context.Context, tx pgx.Tx, serverID uuid.UUID, tunnelID, name string) error {
	raw, err := json.Marshal(agentprotocol.SyncTunnelPayload{Tunnel: agentprotocol.TunnelSpec{TunnelID: tunnelID, Name: name}})
	if err != nil {
		return err
	}
	if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindSyncTunnel, Payload: raw}); err != nil {
		return fmt.Errorf("enqueue sync_tunnel: %w", err)
	}
	return db.New(tx).SetServerTunnelSynced(ctx, serverID)
}

// enqueueSyncRoutes re-sends the server's route table so the agent renders the
// domains with (or without) their tunnel flag.
func (s *Service) enqueueSyncRoutes(ctx context.Context, tx pgx.Tx, serverID uuid.UUID) error {
	q := db.New(tx)
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	if srv.Status == "revoked" {
		return nil
	}
	routes, seq, err := domains.ServerTable(ctx, q, serverID, uuid.Nil)
	if err != nil {
		return err
	}
	pc, err := proxyserver.BuildConfig(ctx, q, serverID)
	if err != nil {
		return err
	}
	pl := agentprotocol.SyncRoutesPayload{Routes: routes, Proxy: pc, TableSeq: seq}
	if srv.AcmeEmail != nil {
		pl.ACMEEmail = *srv.AcmeEmail
	}
	analytics := srv.TrafficAnalyticsEnabled
	pl.TrafficAnalytics = &analytics
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindSyncRoutes, Payload: raw}); err != nil {
		return fmt.Errorf("enqueue sync_routes: %w", err)
	}
	return nil
}

// Enable creates the server's tunnel, pushes its ingress rules and tells the
// agent to run the connector. The result is the provisioning state: the
// connector connects asynchronously.
func (s *Service) Enable(ctx context.Context, serverID uuid.UUID) (TunnelView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID, policy.ScopeProxyWrite)
	if err != nil {
		return TunnelView{}, err
	}
	if srv.Status == "revoked" {
		return TunnelView{}, apierr.Unprocessable("server_revoked", "the server was revoked")
	}
	if !SupportsTunnel(srv) {
		return TunnelView{}, apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old to run the Cloudflare connector; update the agent first")
	}
	cur, err := s.getRow(ctx, s.q, serverID)
	if err != nil {
		return TunnelView{}, err
	}
	if cur != nil {
		if cur.State == "removing" {
			return TunnelView{}, apierr.Conflict("tunnel_removing", "the server's tunnel is still being removed")
		}
		return TunnelView{}, apierr.Conflict("tunnel_exists", "this server already has a tunnel")
	}
	api, account, err := s.cf.Client(ctx, srv.WorkspaceID)
	if err != nil {
		return TunnelView{}, apiError(err, "load the Cloudflare connection")
	}
	if account == "" {
		return TunnelView{}, apiError(cloudflare.ErrNoAccount, "")
	}

	tun, err := api.CreateTunnel(ctx, account, tunnelName(srv))
	if err != nil {
		return TunnelView{}, apiError(err, "create the tunnel")
	}
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if derr := api.DeleteTunnel(cctx, account, tun.ID); derr != nil {
			s.log.WarnContext(ctx, "could not delete a tunnel after a failed enable", "tunnel_id", tun.ID, "err", derr)
		}
	}
	rules, hash, hosts, err := s.ingress(ctx, s.q, serverID)
	if err != nil {
		cleanup()
		return TunnelView{}, err
	}
	if err := api.PutTunnelConfig(ctx, account, tun.ID, rules); err != nil {
		cleanup()
		return TunnelView{}, apiError(err, "push the tunnel's ingress rules")
	}

	var row db.ServerTunnel
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		ins, err := q.InsertServerTunnel(ctx, db.InsertServerTunnelParams{
			ServerID: serverID, WorkspaceID: srv.WorkspaceID, AccountID: account, TunnelID: tun.ID, TunnelName: tun.Name,
		})
		if err != nil {
			return err
		}
		row = ins
		if err := q.SetServerTunnelIngress(ctx, db.SetServerTunnelIngressParams{ServerID: serverID, IngressHash: hash, IngressHosts: int32(hosts)}); err != nil {
			return err
		}
		if err := s.enqueueSync(ctx, tx, serverID, tun.ID, tun.Name); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "tunnel.enable", ResourceType: "server", ResourceID: serverID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name, "tunnelId": tun.ID, "tunnelName": tun.Name, "hostnames": hosts},
		})
	})
	if err != nil {
		cleanup()
		if isUnique(err) {
			return TunnelView{}, apierr.Conflict("tunnel_exists", "this server already has a tunnel")
		}
		return TunnelView{}, fmt.Errorf("enable tunnel: %w", err)
	}
	s.publish(serverID)
	row.IngressHash, row.IngressHosts = hash, int32(hosts)
	return s.view(ctx, srv, &row), nil
}

// Disable removes the server's tunnel: the domains' DNS flips back, the agent
// removes the connector and the tunnel is deleted at Cloudflare shortly after.
// With force the local record is dropped at once and Cloudflare is not asked:
// use it when the connection is gone or Cloudflare keeps refusing.
func (s *Service) Disable(ctx context.Context, serverID uuid.UUID, force bool) (TunnelView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID, policy.ScopeProxyWrite)
	if err != nil {
		return TunnelView{}, err
	}
	if srv.IsControlPlane && srv.Status != "revoked" {
		return TunnelView{}, apierr.Conflict("server_is_control_plane", "this server runs NebulaCtrl itself: turning off its tunnel would cut off the dashboard and the agent")
	}
	if srv.Status != "revoked" {
		if err := prodguard.CheckServer(ctx, s.pool, srv.WorkspaceID, srv.ID, guardDisableTunnel,
			"Turn off the Cloudflare tunnel of "+srv.Name+", which hosts production services", false,
			[]string{"DNS for the hostnames behind the tunnel is pointed back at the server's address at once."}, force); err != nil {
			return TunnelView{}, err
		}
	}
	cur, err := s.getRow(ctx, s.q, serverID)
	if err != nil {
		return TunnelView{}, err
	}
	if cur == nil {
		return TunnelView{}, apierr.NotFound("tunnel")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.teardown(ctx, tx, srv, *cur, force, srv.Status != "revoked")
	})
	if err != nil {
		return TunnelView{}, fmt.Errorf("disable tunnel: %w", err)
	}
	s.publish(serverID)
	row, err := s.getRow(ctx, s.q, serverID)
	if err != nil {
		return TunnelView{}, err
	}
	return s.view(ctx, srv, row), nil
}

// teardown marks the tunnel removing and queues everything that follows. The
// DNS records flip back first (TunnelTarget stops answering), then the delete
// job removes the tunnel at Cloudflare.
func (s *Service) teardown(ctx context.Context, tx pgx.Tx, srv db.Server, cur db.ServerTunnel, force, agentUp bool) error {
	q := db.New(tx)
	if force {
		if err := q.DeleteServerTunnel(ctx, srv.ID); err != nil {
			return err
		}
	} else if _, err := q.MarkServerTunnelRemoving(ctx, srv.ID); err != nil {
		return err
	}
	if agentUp {
		raw, _ := json.Marshal(agentprotocol.RemoveTunnelPayload{TunnelID: cur.TunnelID})
		if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: srv.ID, Kind: agentprotocol.KindRemoveTunnel, Payload: raw}); err != nil {
			return fmt.Errorf("enqueue remove_tunnel: %w", err)
		}
	}
	if err := s.cf.EnqueueSyncServer(ctx, tx, srv.WorkspaceID, srv.ID); err != nil {
		return err
	}
	if !force && s.jm != nil {
		if err := s.jm.InsertTx(ctx, tx, DeleteArgs{ServerID: srv.ID}, &river.InsertOpts{ScheduledAt: s.now().Add(deleteDelay)}); err != nil {
			return fmt.Errorf("enqueue tunnel delete: %w", err)
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: "tunnel.disable", ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID,
		Metadata: map[string]any{"server": srv.Name, "tunnelId": cur.TunnelID, "force": force},
	}); err != nil {
		return err
	}
	// The numbered route table last: the counter's lock must be the last this
	// transaction takes (db.Queries.NumberRouteTable).
	if agentUp {
		return s.enqueueSyncRoutes(ctx, tx, srv.ID)
	}
	return nil
}

// ServerRevoked tears the server's tunnel down when its agent is revoked: the
// connector cannot be removed any more, but the tunnel is deleted at Cloudflare
// and the DNS flips back. It implements the servers revoke hook.
func (s *Service) ServerRevoked(ctx context.Context, workspaceID, serverID uuid.UUID) {
	cur, err := s.getRow(ctx, s.q, serverID)
	if err != nil || cur == nil || cur.State == "removing" {
		if err != nil {
			s.log.ErrorContext(ctx, "tunnel: load on revoke", "server_id", serverID, "err", err)
		}
		return
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		s.log.ErrorContext(ctx, "tunnel: load server on revoke", "server_id", serverID, "err", err)
		return
	}
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error { return s.teardown(ctx, tx, srv, *cur, false, false) }); err != nil {
		s.log.ErrorContext(ctx, "tunnel: teardown on revoke", "server_id", serverID, "err", err)
		return
	}
	s.publish(serverID)
}

// Resync re-pushes the ingress rules and re-sends the connector command: use it
// after the connector was deleted by hand or its configuration drifted.
func (s *Service) Resync(ctx context.Context, serverID uuid.UUID) (TunnelView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID, policy.ScopeProxyWrite)
	if err != nil {
		return TunnelView{}, err
	}
	cur, err := s.getRow(ctx, s.q, serverID)
	if err != nil {
		return TunnelView{}, err
	}
	if cur == nil {
		return TunnelView{}, apierr.NotFound("tunnel")
	}
	if cur.State == "removing" {
		return TunnelView{}, apierr.Conflict("tunnel_removing", "the server's tunnel is being removed")
	}
	if srv.Status == "revoked" {
		return TunnelView{}, apierr.Unprocessable("server_revoked", "the server was revoked")
	}
	if err := s.SyncIngress(ctx, serverID, true); err != nil {
		return TunnelView{}, apiError(err, "push the tunnel's ingress rules")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.enqueueSync(ctx, tx, serverID, cur.TunnelID, cur.TunnelName); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "tunnel.resync", ResourceType: "server", ResourceID: serverID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name, "tunnelId": cur.TunnelID},
		})
	})
	if err != nil {
		return TunnelView{}, fmt.Errorf("resync tunnel: %w", err)
	}
	s.publish(serverID)
	row, err := s.getRow(ctx, s.q, serverID)
	if err != nil {
		return TunnelView{}, err
	}
	return s.view(ctx, srv, row), nil
}

// SyncIngress pushes the server's ingress rules to Cloudflare when the set of
// hostnames changed (or always with force). It is idempotent and records the
// outcome on the tunnel row.
func (s *Service) SyncIngress(ctx context.Context, serverID uuid.UUID, force bool) error {
	row, err := s.getRow(ctx, s.q, serverID)
	if err != nil || row == nil || row.State == "removing" {
		return err
	}
	rules, hash, hosts, err := s.ingress(ctx, s.q, serverID)
	if err != nil {
		return err
	}
	if !force && hash == row.IngressHash && row.IngressError == "" {
		return nil
	}
	api, _, err := s.cf.Client(ctx, row.WorkspaceID)
	if err == nil {
		err = api.PutTunnelConfig(ctx, row.AccountID, row.TunnelID, rules)
	}
	if err != nil {
		msg := "Could not push the ingress rules: " + errText(err)
		if qerr := s.q.SetServerTunnelIngressError(ctx, db.SetServerTunnelIngressErrorParams{ServerID: serverID, IngressError: msg}); qerr != nil {
			s.log.ErrorContext(ctx, "tunnel: record ingress error", "server_id", serverID, "err", qerr)
		}
		s.publish(serverID)
		return err
	}
	if err := s.q.SetServerTunnelIngress(ctx, db.SetServerTunnelIngressParams{ServerID: serverID, IngressHash: hash, IngressHosts: int32(hosts)}); err != nil {
		return fmt.Errorf("record ingress sync: %w", err)
	}
	s.publish(serverID)
	return nil
}

func errText(err error) string {
	var ae *cloudflare.APIError
	if errors.As(err, &ae) {
		return ae.Message
	}
	switch {
	case errors.Is(err, cloudflare.ErrNotConfigured):
		return "Cloudflare is not connected."
	case errors.Is(err, cloudflare.ErrAuth):
		return "the Cloudflare token was rejected or lacks the Cloudflare Tunnel permission."
	}
	return err.Error()
}

// DeleteRemote deletes a removing tunnel at Cloudflare and drops its row (the
// delete job's body). Failures are kept on the row; a retry is possible by
// disabling again.
func (s *Service) DeleteRemote(ctx context.Context, serverID uuid.UUID) error {
	row, err := s.getRow(ctx, s.q, serverID)
	if err != nil || row == nil || row.State != "removing" {
		return err
	}
	api, _, err := s.cf.Client(ctx, row.WorkspaceID)
	if err == nil {
		err = api.DeleteTunnel(ctx, row.AccountID, row.TunnelID)
	}
	if err != nil {
		msg := "Could not delete the tunnel at Cloudflare: " + errText(err) + " Disable the tunnel again to retry, or force-remove it."
		if qerr := s.q.SetServerTunnelRemoveError(ctx, db.SetServerTunnelRemoveErrorParams{ServerID: serverID, Message: msg}); qerr != nil {
			s.log.ErrorContext(ctx, "tunnel: record remove error", "server_id", serverID, "err", qerr)
		}
		s.publish(serverID)
		if permanent(err) {
			return nil
		}
		return err
	}
	if err := s.q.DeleteServerTunnel(ctx, serverID); err != nil {
		return fmt.Errorf("delete tunnel row: %w", err)
	}
	s.publish(serverID)
	return nil
}

// TunnelTarget implements cloudflare.TunnelTargets: the CNAME target of a
// server's tunnel once the connector has connected, and not while it is being
// removed.
func (s *Service) TunnelTarget(ctx context.Context, serverID uuid.UUID) (string, bool, error) {
	row, err := s.getRow(ctx, s.q, serverID)
	if err != nil || row == nil {
		return "", false, err
	}
	if row.State == "removing" || row.ActivatedAt == nil {
		return "", false, nil
	}
	return cloudflare.TunnelCNAME(row.TunnelID), true, nil
}

var _ cloudflare.TunnelTargets = (*Service)(nil)

// DeleteGate refuses removing the team's Cloudflare connection while a server
// still has (or is removing) a tunnel: the connection is what deletes it.
func (s *Service) DeleteGate(ctx context.Context, ws uuid.UUID) error {
	n, err := s.q.CountWorkspaceTunnels(ctx, ws)
	if err != nil {
		return fmt.Errorf("count tunnels: %w", err)
	}
	if n > 0 {
		return apierr.Conflict("cloudflare_tunnels_exist", "disable the Cloudflare Tunnel of every server before removing the connection")
	}
	return nil
}

// TunnelActivated implements agents.TunnelObserver: the connector of the server
// reported its first connection.
func (s *Service) TunnelActivated(ctx context.Context, serverID uuid.UUID) {
	if err := s.activate(ctx, serverID); err != nil {
		s.log.ErrorContext(ctx, "tunnel: activation", "server_id", serverID, "err", err)
	}
}

var _ agents.TunnelObserver = (*Service)(nil)

// activate moves the server's domains onto the tunnel: their DNS records become
// the tunnel CNAME, the agent re-renders the routes as tunnelled and the
// ingress rules are verified.
func (s *Service) activate(ctx context.Context, serverID uuid.UUID) error {
	row, err := s.getRow(ctx, s.q, serverID)
	if err != nil || row == nil || row.State == "removing" {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.cf.EnqueueSyncServer(ctx, tx, row.WorkspaceID, serverID); err != nil {
			return err
		}
		if s.jm != nil {
			if err := s.jm.InsertTx(ctx, tx, IngressArgs{ServerID: serverID}, nil); err != nil {
				return fmt.Errorf("enqueue ingress sync: %w", err)
			}
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: "tunnel.activate", ResourceType: "server", ResourceID: serverID.String(), WorkspaceID: row.WorkspaceID,
			Metadata: map[string]any{"tunnelId": row.TunnelID},
		}); err != nil {
			return err
		}
		// The numbered route table last (db.Queries.NumberRouteTable).
		return s.enqueueSyncRoutes(ctx, tx, serverID)
	})
	if err != nil {
		return err
	}
	s.publish(serverID)
	return nil
}

// Handles reports whether kind is a command of this domain.
func Handles(kind string) bool {
	return kind == agentprotocol.KindSyncTunnel || kind == agentprotocol.KindRemoveTunnel
}

// SecretsForCommand attaches the send-time connector token of a sync_tunnel
// command. The token is fetched from the Cloudflare API now and lives only in
// the outgoing frame.
func (s *Service) SecretsForCommand(ctx context.Context, kind string, payload json.RawMessage) (map[string]string, error) {
	if kind != agentprotocol.KindSyncTunnel {
		return nil, nil
	}
	var p agentprotocol.SyncTunnelPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode sync_tunnel payload: %w", err)
	}
	row, err := s.q.GetServerTunnelByTunnelID(ctx, p.Tunnel.TunnelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("the tunnel no longer exists")
	}
	if err != nil {
		return nil, fmt.Errorf("get tunnel: %w", err)
	}
	if row.State == "removing" {
		return nil, errors.New("the tunnel is being removed")
	}
	api, _, err := s.cf.Client(ctx, row.WorkspaceID)
	if err != nil {
		return nil, errors.New(errText(err))
	}
	token, err := api.TunnelToken(ctx, row.AccountID, row.TunnelID)
	if err != nil {
		return nil, fmt.Errorf("fetch the tunnel token: %s", errText(err))
	}
	return map[string]string{agentprotocol.SecretTunnelToken: token}, nil
}

var knownStates = []string{
	agentprotocol.TunnelStateProvisioning, agentprotocol.TunnelStateOnline,
	agentprotocol.TunnelStateOffline, agentprotocol.TunnelStateNeedsAttn,
}

// HandleResult applies the result of a sync_tunnel command. A remove_tunnel
// result needs nothing: the delete job drives the teardown.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cmd.Kind != agentprotocol.KindSyncTunnel) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get command: %w", err)
	}
	var p agentprotocol.SyncTunnelPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		s.log.WarnContext(ctx, "tunnel result ignored: the stored sync_tunnel payload cannot be parsed", "command_id", commandID, "err", err)
		return nil
	}
	var st agentprotocol.TunnelStatus
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &st)
	}
	if res.Outcome != agentprotocol.OutcomeSucceeded || !slices.Contains(knownStates, st.State) {
		msg := res.Error
		if msg == "" {
			msg = "The Cloudflare connector could not be started."
		}
		st = agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateNeedsAttn, Message: msg}
	}
	return s.observe(ctx, serverID, p.Tunnel.TunnelID, st)
}

// HandleUndelivered marks the tunnel of a sync_tunnel command that never
// reached its agent as needing attention.
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cmd.Kind != agentprotocol.KindSyncTunnel) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get command: %w", err)
	}
	var p agentprotocol.SyncTunnelPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		s.log.WarnContext(ctx, "tunnel undelivered ignored: the stored sync_tunnel payload cannot be parsed", "command_id", commandID, "err", err)
		return nil
	}
	return s.observe(ctx, cmd.ServerID, p.Tunnel.TunnelID, agentprotocol.TunnelStatus{State: agentprotocol.TunnelStateNeedsAttn, Message: msg})
}

// observe stores a connector state reported for tunnelID; a report for another
// tunnel (a stale command) is ignored. The first online report activates the
// tunnel.
func (s *Service) observe(ctx context.Context, serverID uuid.UUID, tunnelID string, st agentprotocol.TunnelStatus) error {
	prev, err := s.getRow(ctx, s.q, serverID)
	if err != nil || prev == nil || prev.State == "removing" || (tunnelID != "" && prev.TunnelID != tunnelID) {
		return err
	}
	row, err := s.q.SetServerTunnelObserved(ctx, db.SetServerTunnelObservedParams{
		ServerID: serverID, State: st.State, Message: st.Message, Connections: int32(st.Connections), Image: st.Image,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("record tunnel state: %w", err)
	}
	if prev.ActivatedAt == nil && row.ActivatedAt != nil {
		if err := s.activate(ctx, serverID); err != nil {
			return err
		}
	}
	s.publish(serverID)
	return nil
}
