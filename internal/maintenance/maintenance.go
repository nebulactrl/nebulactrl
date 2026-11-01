// Package maintenance implements maintenance mode: a per-service switch that
// makes the proxy answer every domain of the service with a 503 maintenance
// page (and Retry-After), instantly and without a deployment.
//
// Maintenance is an operational setting like replica counts: it is stored on
// the service row, never bumps the generation, is not part of a release and is
// never rolled back. The flag reaches the agent on the service's route
// (agentprotocol.Route.Maintenance) through the ordinary route sync.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agent/pages"
	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Audit actions.
const (
	ActionOn  = "service.maintenance.on"
	ActionOff = "service.maintenance.off"
)

// Service implements the maintenance use cases.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	disp   agents.Dispatcher
	svc    *services.Service
	logger *slog.Logger
	now    func() time.Time

	notifier notifications.Emitter
}

// NewService returns a Service (pool, disp and svc may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, bus *events.Bus, disp agents.Dispatcher, svc *services.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{pool: pool, bus: bus, disp: disp, svc: svc, logger: logger, now: time.Now}
	if pool != nil {
		s.q = db.New(pool)
	}
	return s
}

// SetInput switches maintenance on (with the page text) or off.
type SetInput struct {
	Enabled bool
	// Title, Message and ETA only count when enabling; they replace the stored
	// text (empty = the default copy). Switching off keeps the stored text.
	Title, Message string
	ETA            *time.Time
}

func invalid(msg string) *apierr.Error { return apierr.Unprocessable("validation_failed", msg) }

// normalize trims the free text and checks its bounds.
func normalize(in SetInput, now time.Time) (SetInput, error) {
	in.Title, in.Message = strings.TrimSpace(in.Title), strings.TrimSpace(in.Message)
	if err := agentprotocol.ValidateMaintenance(agentprotocol.Maintenance{Title: in.Title, Message: in.Message}); err != nil {
		return in, invalid(err.Error())
	}
	if in.ETA != nil {
		eta := in.ETA.UTC().Truncate(time.Second)
		if !eta.After(now) {
			return in, invalid("eta must be in the future")
		}
		in.ETA = &eta
	}
	return in, nil
}

func sameETA(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// Set switches maintenance on or off. It is idempotent: a request that leaves
// the state unchanged records nothing and sends nothing. A deployed service's
// server receives the new route table immediately (sync_routes), in the same
// transaction as the change.
func (s *Service) Set(ctx context.Context, id uuid.UUID, in SetInput) (services.ServiceView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return services.ServiceView{}, err
	}
	if err := services.RequireNotStack(sc); err != nil {
		return services.ServiceView{}, err
	}
	if rc := sc.Config().Route; sc.Service.Kind == services.KindRoute && rc != nil && rc.Protocol != "" && rc.Protocol != agentprotocol.ProtoHTTP {
		return services.ServiceView{}, apierr.Unprocessable("maintenance_unsupported", "maintenance mode only applies to HTTP services, not tcp/udp routes")
	}
	cur := sc.Service
	if !in.Enabled && !cur.MaintenanceEnabled {
		return s.svc.ViewFor(ctx, sc)
	}
	if in.Enabled {
		if in, err = normalize(in, s.now()); err != nil {
			// An identical repeat of the live request is a no-op even when its
			// ETA has passed since.
			if cur.MaintenanceEnabled && in.Title == cur.MaintenanceTitle && in.Message == cur.MaintenanceMessage && sameETA(in.ETA, cur.MaintenanceEta) {
				return s.svc.ViewFor(ctx, sc)
			}
			return services.ServiceView{}, err
		}
		if cur.MaintenanceEnabled && in.Title == cur.MaintenanceTitle && in.Message == cur.MaintenanceMessage && sameETA(in.ETA, cur.MaintenanceEta) {
			return s.svc.ViewFor(ctx, sc)
		}
	}
	if in.Enabled && prodguard.Applies(ctx, sc.IsProduction) {
		eta := ""
		if in.ETA != nil {
			eta = in.ETA.Format(time.RFC3339)
		}
		if err := sc.Guard(ctx, "enable_maintenance",
			fmt.Sprintf("Put %s in production into maintenance mode (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName), false,
			[]string{"The proxy answers every domain of the service with HTTP 503 and the maintenance page, immediately.", "Visitors cannot reach the service until maintenance is switched off."},
			in.Title, in.Message, eta); err != nil {
			return services.ServiceView{}, err
		}
	}

	params := db.SetServiceMaintenanceParams{ID: id, Enabled: in.Enabled, Title: cur.MaintenanceTitle, Message: cur.MaintenanceMessage}
	action := ActionOff
	if in.Enabled {
		action = ActionOn
		params.Title, params.Message, params.Eta = in.Title, in.Message, in.ETA
		since := cur.MaintenanceSince
		if !cur.MaintenanceEnabled || since == nil {
			now := s.now().UTC()
			since = &now
		}
		params.Since = since
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		row, err := q.SetServiceMaintenance(ctx, params)
		if err != nil {
			return fmt.Errorf("store maintenance: %w", err)
		}
		sc.Service = row
		meta := map[string]any{"slug": row.Slug}
		if in.Enabled {
			meta["title"], meta["hasMessage"] = in.Title, in.Message != ""
			if in.ETA != nil {
				meta["eta"] = in.ETA.Format(time.RFC3339)
			}
		}
		if err := audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "service", ResourceID: id.String(), ProjectID: &sc.ProjectID, WorkspaceID: sc.WorkspaceID,
			Metadata: meta,
		}); err != nil {
			return err
		}
		return s.syncRoutes(ctx, tx, sc)
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return services.ServiceView{}, err
		}
		return services.ServiceView{}, fmt.Errorf("set maintenance: %w", err)
	}
	services.PublishService(s.bus, services.EventServiceUpdated, sc)
	s.notifyChanged(ctx, sc, in)
	return s.svc.ViewFor(ctx, sc)
}

// syncRoutes sends the server of a deployed service its full route table,
// which now carries the new maintenance flag. An undeployed service has no
// route yet: the flag travels with its first deployment's table.
func (s *Service) syncRoutes(ctx context.Context, tx pgx.Tx, sc services.Scope) error {
	if sc.Service.CurrentDeploymentID == nil {
		return nil
	}
	q := db.New(tx)
	deps, err := q.ListDeploymentsByIDs(ctx, []uuid.UUID{*sc.Service.CurrentDeploymentID})
	if err != nil {
		return fmt.Errorf("current deployment: %w", err)
	}
	if len(deps) != 1 || deps[0].ServerID == nil {
		return nil
	}
	serverID := *deps[0].ServerID
	srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return fmt.Errorf("load server: %w", err)
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
	traffic := srv.TrafficAnalyticsEnabled
	pl.TrafficAnalytics = &traffic
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	if _, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindSyncRoutes, Payload: raw}); err != nil {
		return fmt.Errorf("enqueue sync_routes: %w", err)
	}
	return nil
}

// PreviewInput previews unsaved text: with Draft set, Title, Message and ETA
// replace the stored ones (empty values show the defaults).
type PreviewInput struct {
	Draft          bool
	Title, Message string
	ETA            *time.Time
}

// Preview renders the maintenance page of a service as the proxy would serve
// it: from the stored settings, with any override of in applied.
func (s *Service) Preview(ctx context.Context, id uuid.UUID, in PreviewInput) ([]byte, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, id)
	if err != nil {
		return nil, err
	}
	p := pages.Page{Kind: "maintenance", Title: sc.Service.MaintenanceTitle, Message: sc.Service.MaintenanceMessage, ETA: sc.Service.MaintenanceEta, Preview: true}
	if in.Draft {
		p.Title, p.Message, p.ETA = in.Title, in.Message, in.ETA
	}
	for _, v := range []string{p.Title, p.Message} {
		if !utf8.ValidString(v) {
			return nil, invalid("text is not valid UTF-8")
		}
	}
	p.Host = "app.example.com"
	if doms, err := s.q.ListServiceDomains(ctx, id); err == nil && len(doms) > 0 {
		p.Host = doms[0].Hostname
	}
	return pages.Render(p)
}
