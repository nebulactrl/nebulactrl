package app

import (
	"context"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/instsvc/forward"
	"github.com/nebulactrl/nebulactrl/internal/instsvc/lokiadmin"
	"github.com/nebulactrl/nebulactrl/internal/instsvc/registryadmin"
	"github.com/nebulactrl/nebulactrl/internal/instsvc/s3admin"
	"github.com/nebulactrl/nebulactrl/internal/logs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/teams"
)

var (
	// A team's slices are made and kept through the teams service's own transactions.
	_ teams.SliceLifecycle = (*instsvc.Service)(nil)
	_ instsvc.Slices       = (*instsvc.Service)(nil)
	_ instsvc.QuotaGate    = (*instsvc.Service)(nil)
)

// ensureAtStart bounds the first run of instsvc.Ensure at boot; a service that
// does not answer yet is shown as failed and the minute-by-minute run retries.
const ensureAtStart = 2 * time.Minute

// wireInstance builds the instance services (S3, registry, Loki on the internal
// network) and hands the agent hub the forwarder that serves the servers' local
// endpoints. A kind whose URL is not configured (a development run without the
// compose services) is left out: no slice is made for it and the servers get no
// local endpoint. Returns the service and its start hook.
func wireInstance(c *Core, hub *agents.Hub, notifier notifications.Emitter) (*instsvc.Service, func(ctx context.Context) error, logs.CentralLogs) {
	cfg := c.Config.Instance
	// DiskPath "/": in the container that is the host's Docker data disk, where the
	// services' volumes live; writes are refused while it is nearly full.
	svc := instsvc.New(instsvc.Deps{Pool: c.Pool, Box: c.Box, Jobs: c.Jobs, Admins: c.Auth, Notifier: notifier, Logger: c.Logger, DiskPath: "/"})

	if cfg.S3URL != "" {
		be, err := s3admin.New(s3admin.Config{Endpoint: cfg.S3URL, AdminKey: cfg.S3AdminKey, AdminSecret: cfg.S3AdminSecret, Slices: svc})
		if err != nil {
			c.Logger.Error("built-in S3 is not set up", "err", err)
		} else {
			svc.SetBackend(instsvc.KindS3, be, cfg.S3URL)
		}
	}
	var registry *registryadmin.Backend
	if cfg.RegistryURL != "" {
		registry = registryadmin.New(registryadmin.Config{
			URL: cfg.RegistryURL, JWKSPath: cfg.RegistryJWKSPath, Pool: c.Pool, Box: c.Box, Dispatcher: hub, Logger: c.Logger,
		})
		svc.SetBackend(instsvc.KindRegistry, registry, cfg.RegistryURL)
		registryadmin.RegisterJobs(c.Jobs, registry)
	}
	// central is nil (not a typed nil) while Loki is not set up: the central logs
	// view then says forwarding is off.
	var central logs.CentralLogs
	if cfg.LokiURL != "" {
		be, err := lokiadmin.New(lokiadmin.Config{URL: cfg.LokiURL})
		if err != nil {
			c.Logger.Error("built-in Loki is not set up", "err", err)
		} else {
			svc.SetBackend(instsvc.KindLoki, be, cfg.LokiURL)
			central = centralLogs{be}
		}
	}

	if svc.Enabled() {
		deps := forward.Deps{
			Slices: svc, Gate: svc, S3URL: cfg.S3URL, RegistryURL: cfg.RegistryURL, LokiURL: cfg.LokiURL, Logger: c.Logger,
		}
		if registry != nil {
			deps.Registry = registry // not a typed nil: the interface stays nil without a registry
		}
		fw, err := forward.New(deps)
		if err != nil {
			c.Logger.Error("built-in services forwarder is not set up", "err", err)
		} else {
			hub.SetTunnelAcceptor(fw)
			if registry != nil {
				registry.SetWriteGauge(fw.RegistryWrites)
			}
		}
	}

	start := func(ctx context.Context) error {
		if !svc.Enabled() {
			return nil
		}
		go func() {
			ectx, cancel := context.WithTimeout(ctx, ensureAtStart)
			defer cancel()
			if err := svc.Ensure(ectx); err != nil {
				c.Logger.Warn("built-in services are not all ready yet; retrying every minute", "err", err)
			}
		}()
		return nil
	}
	return svc, start, central
}

// centralLogs adapts the Loki backend to the central logs view of internal/logs.
type centralLogs struct{ b *lokiadmin.Backend }

func (c centralLogs) QueryRange(ctx context.Context, tenant, logql string, from, to time.Time, limit int) ([]logs.CentralStream, error) {
	streams, err := c.b.QueryRange(ctx, tenant, logql, from, to, limit)
	if err != nil {
		return nil, err
	}
	out := make([]logs.CentralStream, len(streams))
	for i, s := range streams {
		out[i] = logs.CentralStream{Labels: s.Labels, Entries: make([]logs.CentralEntry, len(s.Entries))}
		for j, e := range s.Entries {
			out[i].Entries[j] = logs.CentralEntry{Time: e.Time, Line: e.Line}
		}
	}
	return out, nil
}
