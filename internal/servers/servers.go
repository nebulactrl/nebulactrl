// Package servers implements the servers use cases: enrollment tokens, listing
// and editing hosts, revocation and host metrics. The WebSocket side lives in
// internal/agents.
package servers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// guardRevokeServer is the operation name production approvals of a revoke are
// bound to (the catalog's operation id).
const guardRevokeServer = "revokeServer"

// Proxy is the managed Traefik state reported by the agent.
type Proxy struct {
	Status string `json:"status" doc:"ok, starting, error, or empty before the first heartbeat"`
	Error  string `json:"error,omitempty"`

	V2                bool                 `json:"v2" doc:"The agent advertises proxy.v2 (domain options, external routes, TCP/UDP, certificates, tailnet)"`
	Generation        int64                `json:"generation" doc:"Bumped by every staged server-level proxy change"`
	AppliedGeneration int64                `json:"appliedGeneration" doc:"Generation the last successful proxy deploy applied; behind Generation means staged changes"`
	Tailnet           ServerTailnetSummary `json:"tailnet"`
}

// ServerTailnetSummary is the per-server tailnet state for badges; the full view is
// GET /servers/{id}/proxy.
type ServerTailnetSummary struct {
	Mode        string   `json:"mode" enum:"off,managed,host" doc:"Desired mode"`
	AppliedMode string   `json:"appliedMode" enum:"off,managed,host"`
	Hostname    string   `json:"hostname"`
	State       string   `json:"state" doc:"Observed state: off, provisioning, online, offline, needs_attention, or empty when never reported"`
	FQDN        string   `json:"fqdn,omitempty"`
	IPs         []string `json:"ips"`
}

// Latest is the newest host-level resource sample.
type Latest struct {
	CPUPercent float64   `json:"cpuPercent"`
	MemUsed    int64     `json:"memUsed"`
	MemTotal   int64     `json:"memTotal"`
	DiskUsed   int64     `json:"diskUsed"`
	DiskTotal  int64     `json:"diskTotal"`
	SampledAt  time.Time `json:"sampledAt"`
}

// Server is the API view of a host.
type Server struct {
	ID              uuid.UUID            `json:"id"`
	Name            string               `json:"name"`
	Status          string               `json:"status" enum:"pending,online,offline,revoked"`
	PublicIP        *string              `json:"publicIp"`
	BaseDomain      *string              `json:"baseDomain"`
	ACMEEmail       *string              `json:"acmeEmail"`
	AgentVersion    *string              `json:"agentVersion"`
	ProtocolVersion *int32               `json:"protocolVersion"`
	Facts           *agentprotocol.Facts `json:"facts"`
	LastSeenAt      *time.Time           `json:"lastSeenAt"`
	CreatedAt       time.Time            `json:"createdAt"`
	Proxy           Proxy                `json:"proxy"`
	Latest          *Latest              `json:"latest"`
	ServiceCount    int                  `json:"serviceCount"`

	MetricsRetentionDays       int  `json:"metricsRetentionDays" doc:"How long resource metric history is kept (1-90)"`
	DiskAlertThresholdPercent  int  `json:"diskAlertThresholdPercent" doc:"Root filesystem usage that raises the disk alert (50-99)"`
	TrafficAnalyticsEnabled    bool `json:"trafficAnalyticsEnabled" doc:"Aggregate Traefik access logs into traffic analytics"`
	TrafficHourlyRetentionDays int  `json:"trafficHourlyRetentionDays" doc:"Hourly traffic history (2-90 days)"`
	TrafficDailyRetentionDays  int  `json:"trafficDailyRetentionDays" doc:"Daily traffic history (7-365 days)"`
	DiskAlert                  bool `json:"diskAlert" doc:"True while the root filesystem is at or above the threshold"`

	GeoEnabled     bool   `json:"geoEnabled" doc:"Count where requests come from (country, and city at the city level) from the client IP; IPs are never stored"`
	GeoSource      string `json:"geoSource" enum:"dbip,maxmind" doc:"dbip = DB-IP Lite (CC BY 4.0, no key); maxmind = MaxMind GeoLite2 with the team's account"`
	GeoLevel       string `json:"geoLevel" enum:"country,city" doc:"country needs a ~8 MB database on the server, city ~130 MB"`
	GeoRefreshDays int    `json:"geoRefreshDays" doc:"Check for a newer database every N days (1-90)"`
	// GeoStatus is the GeoIP database the agent last reported; absent while the agent is not connected or geolocation is off.
	GeoStatus *agentprotocol.GeoStatus `json:"geoStatus,omitempty"`

	DrainState     string     `json:"drainState" enum:"none,draining,drained,failed" doc:"Evacuation state; draining and drained servers accept no new services"`
	DrainMessage   string     `json:"drainMessage" doc:"Why the last drain failed or was canceled, empty otherwise"`
	DrainStartedAt *time.Time `json:"drainStartedAt"`

	AcceptWorkloads     bool   `json:"acceptWorkloads" doc:"Whether services may be scheduled on this server. Turning it off keeps what already runs here and refuses new placements and moves."`
	BuildEnabled        bool   `json:"buildEnabled" doc:"The server builds images for other servers and pushes them to a registry (the Builds or Both role). Independent of acceptWorkloads."`
	BuildMaxConcurrent  int    `json:"buildMaxConcurrent" doc:"Builds that run at the same time; more queue on the control plane"`
	BuildAllowEmulation bool   `json:"buildAllowEmulation" doc:"Allow building for another CPU architecture through emulation (slow); needs the agent to report emulation for the platform"`
	BuildAllowPreviews  bool   `json:"buildAllowPreviews" doc:"Allow pull request previews (untrusted code) to build here; off by default"`
	ControlPlane        bool   `json:"controlPlane" doc:"The host the installer put the control plane on; its proxy serves the dashboard"`
	Unrestricted        bool   `json:"unrestricted" doc:"An instance admin made this server unrestricted: stacks and services on it may use privileged mode, host networking, devices, GPUs, a runtime, the host PID namespace, unconfined seccomp or AppArmor, NET_ADMIN, SYS_ADMIN, the Docker socket and host bind mounts"`
	ComposeVersion      string `json:"composeVersion" doc:"Docker Compose plugin version the agent reported, empty when it has none"`

	// Tunnel is the Cloudflare Tunnel state of the server.
	Tunnel ServerTunnelSummary `json:"tunnel"`

	// Patches is the OS update state of the server: pending updates, reboot-required.
	Patches ServerPatchSummary `json:"patches"`

	// CanDecommission is true when the agent can empty its host and remove itself
	// (the clean removal). Older agents need an upgrade, or the forget removal.
	CanDecommission bool `json:"canDecommission" doc:"The agent can remove everything NebulaCtrl put on the host and then itself, so \"Remove server\" can clean the host. Older agents need an upgrade first"`

	// Removal is set while the server is being removed or its removal failed.
	Removal *ServerRemovalState `json:"removal,omitempty"`
}

// Service implements the server use cases.
type Service struct {
	pool  *pgxpool.Pool
	q     *db.Queries
	bus   *events.Bus
	hub   *agents.Hub
	cfg   *config.Config
	log   *slog.Logger
	now   func() time.Time
	geo   geoDeps
	dns   DNSHook
	hooks []revokeHook
	// platform keeps the route tables of platform services in step with the
	// servers' addresses (SetPlatformRoutes).
	platform PlatformRoutesHook
	// removal holds what Remove server needs besides the database.
	removal removalDeps
}

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, hub *agents.Hub, cfg *config.Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	s := &Service{pool: pool, q: db.New(pool), bus: bus, hub: hub, cfg: cfg, log: logger.With("component", "servers"), now: time.Now}
	if hub != nil {
		s.removal.agent = hub
	}
	return s
}

type serverRow struct {
	db.Server
	serviceCount int32
}

func (s *Service) toServer(r serverRow, latest *db.LatestServerMetricSamplesRow) Server {
	out := Server{
		ID: r.ID, Name: r.Name, Status: r.Status, PublicIP: r.PublicIp, BaseDomain: r.BaseDomain, ACMEEmail: r.AcmeEmail,
		AgentVersion: r.AgentVersion, ProtocolVersion: r.ProtocolVersion, LastSeenAt: r.LastSeenAt, CreatedAt: r.CreatedAt,
		ServiceCount: int(r.serviceCount),

		MetricsRetentionDays: int(r.MetricsRetentionDays), DiskAlertThresholdPercent: int(r.DiskAlertThresholdPercent),
		TrafficAnalyticsEnabled: r.TrafficAnalyticsEnabled, TrafficHourlyRetentionDays: int(r.TrafficHourlyRetentionDays),
		TrafficDailyRetentionDays: int(r.TrafficDailyRetentionDays), DiskAlert: r.DiskAlertActive, Unrestricted: r.Unrestricted,
		GeoEnabled: r.GeoEnabled, GeoSource: r.GeoSource, GeoLevel: r.GeoLevel, GeoRefreshDays: int(r.GeoRefreshDays),
		DrainState: r.DrainState, DrainMessage: r.DrainMessage, DrainStartedAt: r.DrainStartedAt,
		AcceptWorkloads: r.AcceptWorkloads, ControlPlane: r.IsControlPlane,
		BuildEnabled: r.BuildEnabled, BuildMaxConcurrent: int(r.BuildMaxConcurrent),
		BuildAllowEmulation: r.BuildAllowEmulation, BuildAllowPreviews: r.BuildAllowPreviews,
	}
	// The sweep job runs every 10s; do not report a silent server as online in between.
	if out.Status == "online" && (r.LastSeenAt == nil || s.now().Sub(*r.LastSeenAt) > agentprotocol.OfflineAfter) {
		out.Status = "offline"
	}
	if len(r.Facts) > 0 {
		var f agentprotocol.Facts
		if json.Unmarshal(r.Facts, &f) == nil {
			out.Facts = &f
			out.ComposeVersion = f.ComposeVersion
		}
	}
	out.CanDecommission = out.Facts != nil && slices.Contains(out.Facts.Capabilities, agentprotocol.CapDecommission)
	if r.RemovalState != "" {
		out.Removal = &ServerRemovalState{State: r.RemovalState, Error: r.RemovalError, StartedAt: r.RemovalStartedAt}
	}
	if s.hub != nil {
		out.GeoStatus = s.hub.GeoStatus(r.ID)
	}
	if r.ProxyStatus != nil {
		out.Proxy.Status = *r.ProxyStatus
	}
	if r.ProxyError != nil {
		out.Proxy.Error = *r.ProxyError
	}
	out.Proxy.Generation, out.Proxy.AppliedGeneration = r.ProxyGeneration, r.ProxyAppliedGeneration
	out.Proxy.V2 = out.Facts != nil && slices.Contains(out.Facts.Capabilities, agentprotocol.CapProxyV2)
	out.Proxy.Tailnet = ServerTailnetSummary{Mode: r.TailnetMode, AppliedMode: r.TailnetAppliedMode, IPs: []string{}}
	if r.TailnetHostname != nil {
		out.Proxy.Tailnet.Hostname = *r.TailnetHostname
	}
	if len(r.TailnetStatus) > 0 {
		var ts agentprotocol.TailnetStatus
		if json.Unmarshal(r.TailnetStatus, &ts) == nil {
			out.Proxy.Tailnet.State, out.Proxy.Tailnet.FQDN = ts.State, ts.FQDN
			if ts.IPs != nil {
				out.Proxy.Tailnet.IPs = ts.IPs
			}
		}
	}
	if latest != nil {
		out.Latest = &Latest{
			CPUPercent: latest.CpuPercent, MemUsed: latest.MemBytes, MemTotal: latest.MemLimitBytes,
			DiskUsed: latest.DiskUsedBytes, DiskTotal: latest.DiskTotalBytes, SampledAt: latest.Ts,
		}
	}
	return out
}

func (s *Service) publish(id uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: "server.updated", ResourceID: id.String()})
	}
}

// List returns the workspace's servers, newest first.
func (s *Service) List(ctx context.Context, cursor string, limit int) ([]Server, string, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return nil, "", err
	}
	params := db.ListServersPageParams{WorkspaceID: policy.WorkspaceID(ctx), Limit: int32(limit + 1)}
	if t, id, ok, err := api.DecodeTimeCursor(cursor); err != nil {
		return nil, "", err
	} else if ok {
		params.CursorCreatedAt, params.CursorID = &t, &id
	}
	rows, err := s.q.ListServersPage(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("list servers: %w", err)
	}
	var next string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = api.EncodeTimeCursor(last.CreatedAt, last.ID)
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	latest, err := s.latestByServer(ctx, ids)
	if err != nil {
		return nil, "", err
	}
	tunnels, err := s.tunnelsByServer(ctx, ids)
	if err != nil {
		return nil, "", err
	}
	patches, err := s.patchesByServer(ctx, ids)
	if err != nil {
		return nil, "", err
	}
	out := make([]Server, 0, len(rows))
	for _, r := range rows {
		sv := s.toServer(serverRow{Server: db.Server{
			ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, PublicIp: r.PublicIp, BaseDomain: r.BaseDomain,
			AcmeEmail: r.AcmeEmail, Status: r.Status, AgentVersion: r.AgentVersion, ProtocolVersion: r.ProtocolVersion,
			Facts: r.Facts, ProxyStatus: r.ProxyStatus, ProxyError: r.ProxyError, LastSeenAt: r.LastSeenAt, CreatedAt: r.CreatedAt,
			MetricsRetentionDays: r.MetricsRetentionDays, DiskAlertThresholdPercent: r.DiskAlertThresholdPercent,
			DiskAlertActive: r.DiskAlertActive, TrafficAnalyticsEnabled: r.TrafficAnalyticsEnabled, Unrestricted: r.Unrestricted,
			TrafficHourlyRetentionDays: r.TrafficHourlyRetentionDays, TrafficDailyRetentionDays: r.TrafficDailyRetentionDays,
			GeoEnabled: r.GeoEnabled, GeoSource: r.GeoSource, GeoLevel: r.GeoLevel, GeoRefreshDays: r.GeoRefreshDays,
			DrainState: r.DrainState, DrainMessage: r.DrainMessage, DrainStartedAt: r.DrainStartedAt,
			AcceptWorkloads: r.AcceptWorkloads, IsControlPlane: r.IsControlPlane,
			BuildEnabled: r.BuildEnabled, BuildMaxConcurrent: r.BuildMaxConcurrent,
			BuildAllowEmulation: r.BuildAllowEmulation, BuildAllowPreviews: r.BuildAllowPreviews,
			ProxyGeneration: r.ProxyGeneration, ProxyAppliedGeneration: r.ProxyAppliedGeneration, TailnetMode: r.TailnetMode,
			TailnetAppliedMode: r.TailnetAppliedMode, TailnetHostname: r.TailnetHostname, TailnetStatus: r.TailnetStatus,
			RemovalState: r.RemovalState, RemovalError: r.RemovalError, RemovalStartedAt: r.RemovalStartedAt,
		}, serviceCount: r.ServiceCount}, latest[r.ID])
		sv.Tunnel = tunnelSummary(tunnels[r.ID])
		sv.Patches = patches[r.ID]
		out = append(out, sv)
	}
	return out, next, nil
}

func (s *Service) latestByServer(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*db.LatestServerMetricSamplesRow, error) {
	out := map[uuid.UUID]*db.LatestServerMetricSamplesRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.LatestServerMetricSamples(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("latest server samples: %w", err)
	}
	for i := range rows {
		out[rows[i].ServerID] = &rows[i]
	}
	return out, nil
}

// load authorizes scope and fetches a server of the caller's workspace (404
// otherwise).
func (s *Service) load(ctx context.Context, q *db.Queries, scope string, id uuid.UUID) (db.GetServerScopedRow, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.GetServerScopedRow{}, err
	}
	row, err := q.GetServerScoped(ctx, db.GetServerScopedParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GetServerScopedRow{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.GetServerScopedRow{}, fmt.Errorf("get server: %w", err)
	}
	return row, nil
}

func fromScoped(r db.GetServerScopedRow) serverRow {
	return serverRow{Server: db.Server{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, PublicIp: r.PublicIp, BaseDomain: r.BaseDomain,
		AcmeEmail: r.AcmeEmail, Status: r.Status, AgentVersion: r.AgentVersion, ProtocolVersion: r.ProtocolVersion,
		Facts: r.Facts, ProxyStatus: r.ProxyStatus, ProxyError: r.ProxyError, LastSeenAt: r.LastSeenAt, CreatedAt: r.CreatedAt,
		MetricsRetentionDays: r.MetricsRetentionDays, DiskAlertThresholdPercent: r.DiskAlertThresholdPercent,
		DiskAlertActive: r.DiskAlertActive, TrafficAnalyticsEnabled: r.TrafficAnalyticsEnabled, Unrestricted: r.Unrestricted,
		TrafficHourlyRetentionDays: r.TrafficHourlyRetentionDays, TrafficDailyRetentionDays: r.TrafficDailyRetentionDays,
		GeoEnabled: r.GeoEnabled, GeoSource: r.GeoSource, GeoLevel: r.GeoLevel, GeoRefreshDays: r.GeoRefreshDays,
		DrainState: r.DrainState, DrainMessage: r.DrainMessage, DrainStartedAt: r.DrainStartedAt,
		AcceptWorkloads: r.AcceptWorkloads, IsControlPlane: r.IsControlPlane,
		BuildEnabled: r.BuildEnabled, BuildMaxConcurrent: r.BuildMaxConcurrent,
		BuildAllowEmulation: r.BuildAllowEmulation, BuildAllowPreviews: r.BuildAllowPreviews,
		ProxyGeneration: r.ProxyGeneration, ProxyAppliedGeneration: r.ProxyAppliedGeneration, TailnetMode: r.TailnetMode,
		TailnetAppliedMode: r.TailnetAppliedMode, TailnetHostname: r.TailnetHostname, TailnetStatus: r.TailnetStatus,
		RemovalState: r.RemovalState, RemovalError: r.RemovalError, RemovalStartedAt: r.RemovalStartedAt,
	}, serviceCount: r.ServiceCount}
}

func (s *Service) view(ctx context.Context, row db.GetServerScopedRow) (Server, error) {
	latest, err := s.latestByServer(ctx, []uuid.UUID{row.ID})
	if err != nil {
		return Server{}, err
	}
	tunnels, err := s.tunnelsByServer(ctx, []uuid.UUID{row.ID})
	if err != nil {
		return Server{}, err
	}
	patches, err := s.patchesByServer(ctx, []uuid.UUID{row.ID})
	if err != nil {
		return Server{}, err
	}
	sv := s.toServer(fromScoped(row), latest[row.ID])
	sv.Tunnel = tunnelSummary(tunnels[row.ID])
	sv.Patches = patches[row.ID]
	return sv, nil
}

// Get returns one server.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Server, error) {
	row, err := s.load(ctx, s.q, policy.ScopeServersRead, id)
	if err != nil {
		return Server{}, err
	}
	return s.view(ctx, row)
}

// UpdateInput holds the optional fields of a server PATCH. A non-nil empty
// string clears publicIp, baseDomain and acmeEmail.
type UpdateInput struct {
	Name       *string
	PublicIP   *string
	BaseDomain *string
	ACMEEmail  *string

	MetricsRetentionDays       *int
	DiskAlertThresholdPercent  *int
	TrafficAnalyticsEnabled    *bool
	TrafficHourlyRetentionDays *int
	TrafficDailyRetentionDays  *int

	GeoEnabled     *bool
	GeoSource      *string
	GeoLevel       *string
	GeoRefreshDays *int

	// AcceptWorkloads turns scheduling of new services on or off.
	AcceptWorkloads *bool

	// The build role: BuildEnabled makes the server a build server,
	// BuildMaxConcurrent (1-16) caps its parallel builds, BuildAllowEmulation
	// allows cross-architecture builds, BuildAllowPreviews lets PR previews build here.
	BuildEnabled        *bool
	BuildMaxConcurrent  *int
	BuildAllowEmulation *bool
	BuildAllowPreviews  *bool
}

// Bounds of build_max_concurrent.
const (
	MinBuildMaxConcurrent = 1
	MaxBuildMaxConcurrent = 16
)

// Bounds of the per-server observability settings.
const (
	MinMetricsRetentionDays       = 1
	MaxMetricsRetentionDays       = 90
	MinDiskAlertThresholdPercent  = 50
	MaxDiskAlertThresholdPercent  = 99
	MinTrafficHourlyRetentionDays = 2
	MaxTrafficHourlyRetentionDays = 90
	MinTrafficDailyRetentionDays  = 7
	MaxTrafficDailyRetentionDays  = 365
)

func checkRange(name string, v *int, lo, hi int) (*int32, error) {
	if v == nil {
		return nil, nil
	}
	if *v < lo || *v > hi {
		return nil, apierr.Unprocessable("validation_failed", fmt.Sprintf("%s must be between %d and %d", name, lo, hi))
	}
	n := int32(*v)
	return &n, nil
}

var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validHostname(h string) bool {
	if h == "" || len(h) > 253 || h != strings.ToLower(h) {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if !labelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// normalize validates optional host fields and returns them trimmed.
func normalize(ip, baseDomain, acme string) (string, string, string, error) {
	ip, baseDomain, acme = strings.TrimSpace(ip), strings.ToLower(strings.TrimSpace(baseDomain)), strings.TrimSpace(acme)
	if ip != "" {
		a, err := netip.ParseAddr(ip)
		if err != nil || a.IsUnspecified() {
			return "", "", "", apierr.Unprocessable("invalid_public_ip", "publicIp must be an IPv4 or IPv6 address")
		}
		ip = a.String()
	}
	if baseDomain != "" {
		baseDomain = strings.TrimPrefix(baseDomain, "*.")
		if !validHostname(baseDomain) {
			return "", "", "", apierr.Unprocessable("invalid_base_domain", "baseDomain must be a lowercase RFC 1123 hostname")
		}
	}
	if acme != "" {
		a, err := mail.ParseAddress(acme)
		if err != nil || a.Address != acme {
			return "", "", "", apierr.Unprocessable("invalid_acme_email", "acmeEmail must be an email address")
		}
	}
	return ip, baseDomain, acme, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Update edits the host settings of a server.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (Server, error) {
	cur, err := s.load(ctx, s.q, policy.ScopeServersWrite, id)
	if err != nil {
		return Server{}, err
	}
	params := db.UpdateServerSettingsParams{ID: id, WorkspaceID: cur.WorkspaceID}
	changed := map[string]any{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 100 {
			return Server{}, apierr.Unprocessable("validation_failed", "name must be 1-100 characters")
		}
		params.Name = &n
		changed["name"] = n
	}
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	ip, bd, acme, err := normalize(val(in.PublicIP), val(in.BaseDomain), val(in.ACMEEmail))
	if err != nil {
		return Server{}, err
	}
	if in.PublicIP != nil {
		params.SetPublicIp, params.PublicIp = true, nilIfEmpty(ip)
		changed["publicIp"] = ip
	}
	if in.BaseDomain != nil {
		params.SetBaseDomain, params.BaseDomain = true, nilIfEmpty(bd)
		changed["baseDomain"] = bd
	}
	if in.ACMEEmail != nil {
		params.SetAcmeEmail, params.AcmeEmail = true, nilIfEmpty(acme)
		changed["acmeEmail"] = acme
	}
	var err2 error
	if params.MetricsRetentionDays, err2 = checkRange("metricsRetentionDays", in.MetricsRetentionDays, MinMetricsRetentionDays, MaxMetricsRetentionDays); err2 != nil {
		return Server{}, err2
	}
	if params.DiskAlertThresholdPercent, err2 = checkRange("diskAlertThresholdPercent", in.DiskAlertThresholdPercent, MinDiskAlertThresholdPercent, MaxDiskAlertThresholdPercent); err2 != nil {
		return Server{}, err2
	}
	if params.TrafficHourlyRetentionDays, err2 = checkRange("trafficHourlyRetentionDays", in.TrafficHourlyRetentionDays, MinTrafficHourlyRetentionDays, MaxTrafficHourlyRetentionDays); err2 != nil {
		return Server{}, err2
	}
	if params.TrafficDailyRetentionDays, err2 = checkRange("trafficDailyRetentionDays", in.TrafficDailyRetentionDays, MinTrafficDailyRetentionDays, MaxTrafficDailyRetentionDays); err2 != nil {
		return Server{}, err2
	}
	for k, v := range map[string]*int32{
		"metricsRetentionDays": params.MetricsRetentionDays, "diskAlertThresholdPercent": params.DiskAlertThresholdPercent,
		"trafficHourlyRetentionDays": params.TrafficHourlyRetentionDays, "trafficDailyRetentionDays": params.TrafficDailyRetentionDays,
	} {
		if v != nil {
			changed[k] = *v
		}
	}
	trafficToggled := false
	if in.TrafficAnalyticsEnabled != nil {
		params.TrafficAnalyticsEnabled = in.TrafficAnalyticsEnabled
		changed["trafficAnalyticsEnabled"] = *in.TrafficAnalyticsEnabled
		trafficToggled = *in.TrafficAnalyticsEnabled != cur.TrafficAnalyticsEnabled
	}
	geoChanged, err := s.applyGeoUpdate(ctx, cur, in, &params, changed)
	if err != nil {
		return Server{}, err
	}
	if in.AcceptWorkloads != nil {
		changed["acceptWorkloads"] = *in.AcceptWorkloads
	}
	role := db.SetServerBuildRoleParams{ID: id, WorkspaceID: cur.WorkspaceID, BuildEnabled: in.BuildEnabled, BuildAllowEmulation: in.BuildAllowEmulation, BuildAllowPreviews: in.BuildAllowPreviews}
	roleChanged := map[string]any{}
	if in.BuildEnabled != nil {
		roleChanged["buildEnabled"] = *in.BuildEnabled
	}
	if in.BuildAllowEmulation != nil {
		roleChanged["buildAllowEmulation"] = *in.BuildAllowEmulation
	}
	if in.BuildAllowPreviews != nil {
		roleChanged["buildAllowPreviews"] = *in.BuildAllowPreviews
	}
	if in.BuildMaxConcurrent != nil {
		if *in.BuildMaxConcurrent < MinBuildMaxConcurrent || *in.BuildMaxConcurrent > MaxBuildMaxConcurrent {
			return Server{}, apierr.Unprocessable("validation_failed", fmt.Sprintf("buildMaxConcurrent must be between %d and %d", MinBuildMaxConcurrent, MaxBuildMaxConcurrent))
		}
		n := int32(*in.BuildMaxConcurrent)
		role.BuildMaxConcurrent = &n
		roleChanged["buildMaxConcurrent"] = n
	}
	if in.AcceptWorkloads != nil && len(roleChanged) > 0 {
		roleChanged["acceptWorkloads"] = *in.AcceptWorkloads
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).UpdateServerSettings(ctx, params); err != nil {
			return err
		}
		if len(roleChanged) > 0 {
			if _, err := db.New(tx).SetServerBuildRole(ctx, role); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Entry{
				Action: "server.build_role", ResourceType: "server", ResourceID: id.String(), Metadata: roleChanged,
			}); err != nil {
				return err
			}
		}
		if in.AcceptWorkloads != nil {
			if _, err := db.New(tx).SetServerAcceptWorkloads(ctx, db.SetServerAcceptWorkloadsParams{
				ID: id, WorkspaceID: cur.WorkspaceID, AcceptWorkloads: *in.AcceptWorkloads,
			}); err != nil {
				return err
			}
		}
		if in.PublicIP != nil && s.dns != nil && !sameIP(cur.PublicIp, params.PublicIp) {
			// Domains of the services on this server follow its address.
			if err := s.dns.EnqueueSyncServer(ctx, tx, cur.WorkspaceID, id); err != nil {
				return err
			}
		}
		if !(len(changed) == 0 && len(roleChanged) > 0) { // else only the build role changed: server.build_role was recorded
			if err := audit.Record(ctx, tx, audit.Entry{
				Action: "server.update", ResourceType: "server", ResourceID: id.String(), Metadata: changed,
			}); err != nil {
				return err
			}
		}
		if in.PublicIP != nil && !sameIP(cur.PublicIp, params.PublicIp) {
			// Platform services accept the public address of every server. The
			// route tables last: the counters' locks must be the last this
			// transaction takes (db.Queries.NumberRouteTable).
			return s.platformRoutesChanged(ctx, tx, cur.WorkspaceID)
		}
		return nil
	})
	if err != nil {
		return Server{}, fmt.Errorf("update server: %w", err)
	}
	s.publish(id)
	if (trafficToggled || geoChanged) && s.hub != nil {
		// The agent applies the new logging config with its next route push.
		if err := s.hub.Resync(context.WithoutCancel(ctx), id); err != nil {
			s.log.WarnContext(ctx, "resync after traffic analytics toggle", "server_id", id, "err", err)
		}
	}
	return s.Get(ctx, id)
}

// CreateTokenInput describes the server to enroll.
type CreateTokenInput struct {
	Name       string
	PublicIP   string
	BaseDomain string
	ACMEEmail  string
}

// EnrollmentToken is returned once, when a token is created.
type EnrollmentToken struct {
	Server         Server    `json:"server"`
	Token          string    `json:"token" doc:"Shown once; only its hash is stored"`
	ExpiresAt      time.Time `json:"expiresAt"`
	InstallCommand string    `json:"installCommand"`
}

// PublicBase returns the control-plane URL agents should use.
func PublicBase(cfg *config.Config) string {
	if u := strings.TrimRight(cfg.PublicURL, "/"); u != "" {
		return u
	}
	port := "8080"
	if _, p, err := net.SplitHostPort(cfg.Listen); err == nil && p != "" {
		port = p
	}
	return "http://localhost:" + port
}

// InstallCommand renders the one-liner that installs and enrolls an agent.
func InstallCommand(cfg *config.Config, token string) string {
	base := PublicBase(cfg)
	cmd := fmt.Sprintf("curl -fsSL %s/install-agent.sh | sudo sh -s -- --url %s --token %s", base, base, token)
	if strings.HasPrefix(base, "http://") {
		cmd += " --insecure-dev"
	}
	return cmd
}

// CreateEnrollmentToken creates a pending server and a one-time enrollment
// token that expires in an hour.
func (s *Service) CreateEnrollmentToken(ctx context.Context, in CreateTokenInput) (EnrollmentToken, error) {
	if err := policy.Require(ctx, policy.ScopeServersWrite, nil); err != nil {
		return EnrollmentToken{}, err
	}
	p, _ := auth.PrincipalFrom(ctx)
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return EnrollmentToken{}, apierr.Unprocessable("validation_failed", "name must be 1-100 characters")
	}
	ip, bd, acme, err := normalize(in.PublicIP, in.BaseDomain, in.ACMEEmail)
	if err != nil {
		return EnrollmentToken{}, err
	}
	token, hash, err := agents.NewEnrollmentToken()
	if err != nil {
		return EnrollmentToken{}, fmt.Errorf("generate enrollment token: %w", err)
	}
	expires := s.now().Add(agents.EnrollmentTokenTTL)
	var srv db.Server
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		srv, err = q.CreateServer(ctx, db.CreateServerParams{
			WorkspaceID: p.WorkspaceID, Name: name, PublicIp: nilIfEmpty(ip), BaseDomain: nilIfEmpty(bd), AcmeEmail: nilIfEmpty(acme),
		})
		if err != nil {
			return err
		}
		createdBy := p.UserID
		if _, err := q.CreateEnrollmentToken(ctx, db.CreateEnrollmentTokenParams{
			WorkspaceID: p.WorkspaceID, ServerID: srv.ID, TokenHash: hash, ExpiresAt: expires, CreatedBy: &createdBy,
		}); err != nil {
			return err
		}
		// The token itself is never audited.
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: "server.enroll_token.create", ResourceType: "server", ResourceID: srv.ID.String(),
			Metadata: map[string]any{"name": name},
		}); err != nil {
			return err
		}
		if ip != "" {
			// Platform services accept the public address of every server. The
			// route tables last (db.Queries.NumberRouteTable).
			return s.platformRoutesChanged(ctx, tx, p.WorkspaceID)
		}
		return nil
	})
	if err != nil {
		return EnrollmentToken{}, fmt.Errorf("create enrollment token: %w", err)
	}
	s.publish(srv.ID)
	return EnrollmentToken{
		Server:         s.toServer(serverRow{Server: srv}, nil),
		Token:          token,
		ExpiresAt:      expires,
		InstallCommand: InstallCommand(s.cfg, token),
	}, nil
}

// ReissueEnrollmentToken gives a server that has not enrolled yet a fresh
// install command. The server's outstanding tokens are revoked first, so a
// command that leaked or expired stops working and exactly one is live. A
// server whose agent has enrolled is refused (409 server_not_pending): it
// needs no token, and a token for it would never be redeemed.
func (s *Service) ReissueEnrollmentToken(ctx context.Context, id uuid.UUID) (EnrollmentToken, error) {
	cur, err := s.load(ctx, s.q, policy.ScopeServersWrite, id)
	if err != nil {
		return EnrollmentToken{}, err
	}
	if cur.Status != "pending" {
		return EnrollmentToken{}, apierr.Conflict("server_not_pending", "this server already has an agent; only a server that has not connected yet gets a new install command")
	}
	p, _ := auth.PrincipalFrom(ctx)
	token, hash, err := agents.NewEnrollmentToken()
	if err != nil {
		return EnrollmentToken{}, fmt.Errorf("generate enrollment token: %w", err)
	}
	expires := s.now().Add(agents.EnrollmentTokenTTL)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.RevokeServerEnrollmentTokens(ctx, id); err != nil {
			return err
		}
		createdBy := p.UserID
		if _, err := q.CreateEnrollmentToken(ctx, db.CreateEnrollmentTokenParams{
			WorkspaceID: cur.WorkspaceID, ServerID: id, TokenHash: hash, ExpiresAt: expires, CreatedBy: &createdBy,
		}); err != nil {
			return err
		}
		// The token itself is never audited.
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.enroll_token.reissue", ResourceType: "server", ResourceID: id.String(),
			Metadata: map[string]any{"name": cur.Name},
		})
	})
	if err != nil {
		return EnrollmentToken{}, fmt.Errorf("reissue enrollment token: %w", err)
	}
	s.publish(id)
	srv, err := s.Get(ctx, id)
	if err != nil {
		return EnrollmentToken{}, err
	}
	return EnrollmentToken{
		Server:         srv,
		Token:          token,
		ExpiresAt:      expires,
		InstallCommand: InstallCommand(s.cfg, token),
	}, nil
}

// Revoke permanently disables a server's agent: its identity and outstanding
// enrollment tokens are revoked, commands that never reached it are canceled
// (failing their deployments) and a live connection is closed with 4401.
// Revoking twice is a no-op.
func (s *Service) Revoke(ctx context.Context, id uuid.UUID) (Server, error) {
	cur, err := s.load(ctx, s.q, policy.ScopeServersWrite, id)
	if err != nil {
		return Server{}, err
	}
	if cur.Status != "revoked" {
		if cur.IsControlPlane {
			return Server{}, apierr.Conflict("server_is_control_plane", "this server runs NebulaCtrl itself and cannot be revoked: revoking it would cut off the control plane's own agent")
		}
		if err := prodguard.CheckServer(ctx, s.pool, cur.WorkspaceID, cur.ID, guardRevokeServer,
			"Permanently disable the agent of "+cur.Name+", which hosts production services", true,
			[]string{
				"The agent is cut off for good: no deploy, rollback or certificate work reaches the server.",
				"Production keeps running but cannot be managed until someone with SSH access enrolls the server again.",
			}); err != nil {
			return Server{}, err
		}
	}
	if err := s.revokeRow(ctx, cur); err != nil {
		return Server{}, err
	}
	return s.Get(ctx, id)
}

// revokeRow is Revoke for a server the caller has authorized and loaded.
func (s *Service) revokeRow(ctx context.Context, cur db.GetServerScopedRow) error {
	id := cur.ID
	var canceled []agents.UndeliveredCommand
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.MarkServerRevoked(ctx, db.MarkServerRevokedParams{ID: id, WorkspaceID: cur.WorkspaceID}); err != nil {
			return err
		}
		if err := q.RevokeServerAgentIdentity(ctx, id); err != nil {
			return err
		}
		if err := q.RevokeServerEnrollmentTokens(ctx, id); err != nil {
			return err
		}
		rows, err := q.CancelServerAgentCommands(ctx, id)
		if err != nil {
			return err
		}
		for _, r := range rows {
			canceled = append(canceled, agents.UndeliveredCommand{ID: r.ID, ServerID: r.ServerID, DeploymentID: r.DeploymentID})
		}
		if cur.Status == "revoked" {
			return nil
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.revoke", ResourceType: "server", ResourceID: id.String(), WorkspaceID: cur.WorkspaceID,
			Metadata: map[string]any{"name": cur.Name, "canceledCommands": len(canceled)},
		})
	})
	if err != nil {
		return fmt.Errorf("revoke server: %w", err)
	}
	if s.hub != nil {
		s.hub.AfterRevoke(context.WithoutCancel(ctx), id, canceled)
	}
	for _, h := range s.hooks {
		h(context.WithoutCancel(ctx), cur.WorkspaceID, id)
	}
	if cur.Status != "revoked" {
		// The server's address leaves the allow list of the platform services. A
		// failure never undoes the revoke; the next route push carries the list.
		bg := context.WithoutCancel(ctx)
		if err := db.InTx(bg, s.pool, func(tx pgx.Tx) error { return s.platformRoutesChanged(bg, tx, cur.WorkspaceID) }); err != nil {
			s.log.WarnContext(ctx, "push platform routes after revoke", "server_id", id, "err", err)
		}
	}
	s.publish(id)
	return nil
}

// MetricPoint is one bucket of host metrics. cpuMax and memMax are the peak
// inside the bucket (equal to the averages for raw buckets of one sample).
type MetricPoint struct {
	TS             time.Time `json:"ts"`
	CPUPercent     float64   `json:"cpuPercent"`
	CPUMax         float64   `json:"cpuMax"`
	MemBytes       int64     `json:"memBytes"`
	MemMax         int64     `json:"memMax"`
	MemTotalBytes  int64     `json:"memTotalBytes"`
	MemPercent     float64   `json:"memPercent"`
	DiskUsedBytes  int64     `json:"diskUsedBytes"`
	DiskTotalBytes int64     `json:"diskTotalBytes"`
	DiskPercent    float64   `json:"diskPercent"`
}

// MetricsResult is a bucketed host metrics series.
type MetricsResult struct {
	Points        []MetricPoint
	StepSeconds   int
	RetentionDays int
}

func percent(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// Metrics returns bucketed host metrics for the range (see PlanRange). Ranges
// up to one hour read the raw tier, up to 24 hours the 1-minute tier and longer
// ones the 15-minute tier; RetentionDays tells the UI how much history exists.
func (s *Service) Metrics(ctx context.Context, id uuid.UUID, rng string) (MetricsResult, error) {
	srv, err := s.load(ctx, s.q, policy.ScopeServersRead, id)
	if err != nil {
		return MetricsResult{}, err
	}
	plan, err := PlanRange(rng)
	if err != nil {
		return MetricsResult{}, apierr.Unprocessable("invalid_range", err.Error())
	}
	since := s.now().Add(-plan.Window)
	step := plan.Step.Seconds()

	type row struct {
		bucket                                        time.Time
		cpuAvg, cpuMax                                float64
		memAvg, memMax, memLimit, diskUsed, diskTotal int64
	}
	var rows []row
	switch plan.Tier {
	case TierRaw:
		rs, err := s.q.ServerMetricsRaw(ctx, db.ServerMetricsRawParams{ServerID: id, Since: since, BucketSecs: step})
		if err != nil {
			return MetricsResult{}, fmt.Errorf("server metrics: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, row{r.Bucket, r.CpuAvg, r.CpuMax, r.MemAvg, r.MemMax, r.MemLimit, r.DiskUsed, r.DiskTotal})
		}
	case Tier1m:
		rs, err := s.q.ServerMetrics1m(ctx, db.ServerMetrics1mParams{ServerID: id, Since: since, BucketSecs: step})
		if err != nil {
			return MetricsResult{}, fmt.Errorf("server metrics: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, row{r.Bucket, r.CpuAvg, r.CpuMax, r.MemAvg, r.MemMax, r.MemLimit, r.DiskUsed, r.DiskTotal})
		}
	default:
		rs, err := s.q.ServerMetrics15m(ctx, db.ServerMetrics15mParams{ServerID: id, Since: since, BucketSecs: step})
		if err != nil {
			return MetricsResult{}, fmt.Errorf("server metrics: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, row{r.Bucket, r.CpuAvg, r.CpuMax, r.MemAvg, r.MemMax, r.MemLimit, r.DiskUsed, r.DiskTotal})
		}
	}
	out := MetricsResult{Points: make([]MetricPoint, 0, len(rows)), StepSeconds: int(plan.Step.Seconds()), RetentionDays: int(srv.MetricsRetentionDays)}
	for _, r := range rows {
		out.Points = append(out.Points, MetricPoint{
			TS: r.bucket, CPUPercent: r.cpuAvg, CPUMax: r.cpuMax, MemBytes: r.memAvg, MemMax: r.memMax,
			MemTotalBytes: r.memLimit, MemPercent: percent(r.memAvg, r.memLimit), DiskUsedBytes: r.diskUsed,
			DiskTotalBytes: r.diskTotal, DiskPercent: percent(r.diskUsed, r.diskTotal),
		})
	}
	return out, nil
}
