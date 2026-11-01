// Package instsvc runs the instance services: S3 (NebulaCtrl's own backup
// storage), the image registry and Loki. They run once, beside the control
// plane, on an internal network; only the instance administrator manages them
// (Admin → Built-in services). Every team gets an isolated slice of each: a
// bucket and a key that can only touch it, a repository prefix, a log tenant.
// Servers reach the services through the agent's outbound link (the forwarder in
// internal/instsvc/forward), never through a public address.
//
// This package owns the database side: which slices exist, their quota, usage,
// alerts, provisioning and the 30-day purge of a deleted team's slices. The
// three Backends (internal/instsvc/{s3admin,registryadmin,lokiadmin}) talk to
// the services.
package instsvc

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Kinds of instance service.
const (
	KindS3       = "s3"
	KindRegistry = "registry"
	KindLoki     = "loki"
)

// kinds lists the instance services in provisioning order.
var kinds = []string{KindS3, KindRegistry, KindLoki}

// Slice and service states.
const (
	StateProvisioning = "provisioning"
	StateReady        = "ready"
	StateFull         = "full"
	StateFailed       = "failed"
)

const (
	// DefaultQuotaBytes is the S3 quota of a team that has none of its own and
	// whose instance default was never changed: 50 GiB.
	DefaultQuotaBytes int64 = 50 << 30
	// MinQuotaBytes is the smallest quota (and default quota) the admin may set.
	MinQuotaBytes int64 = 1 << 30
	// PurgeAfter is how long the slices of a deleted team are kept.
	PurgeAfter = 30 * 24 * time.Hour
	// LogRetentionDays is how long Loki keeps a team's central logs.
	LogRetentionDays = 7
	// SystemBucket is the bucket of NebulaCtrl's own data (the control-plane
	// backup may use it). It belongs to no team.
	SystemBucket = "nebulactrl-system"

	// Intervals of the periodic jobs.
	EnsureInterval = time.Minute
	UsageInterval  = 5 * time.Minute
	PurgeInterval  = time.Hour
)

// Backend is one instance service's side of a slice: the control plane's calls
// into SeaweedFS, the registry or Loki. They are built once per kind.
type Backend interface {
	// EnsureSlice creates what the slice needs inside the service (idempotent)
	// and applies the quota (0: none). For the s3 kind the slice's key and its
	// secret (Slices.Secret) become a key that can only touch the slice's bucket.
	EnsureSlice(ctx context.Context, s Slice, quotaBytes int64) error
	// Usage is the bytes the slice holds (0 for kinds without a quota).
	Usage(ctx context.Context, s Slice) (int64, error)
	// Purge removes the slice's data and credentials from the service.
	Purge(ctx context.Context, s Slice) error
}

// Healther is implemented by Backends that can say whether their service
// answers (checked by Ensure).
type Healther interface {
	Health(ctx context.Context) error
}

// Preparer is implemented by Backends that set up instance-wide state before
// their first health check (the registry's signing key).
type Preparer interface {
	Prepare(ctx context.Context) error
}

// AdminChecker says who the instance administrator is (auth.Service).
type AdminChecker interface {
	IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Deps are the collaborators of the Service.
type Deps struct {
	Pool *pgxpool.Pool
	Box  *secretbox.Box
	Jobs *jobs.Manager
	// Admins authorizes the Admin → Built-in services routes.
	Admins AdminChecker
	// Notifier receives the 80% and 100% quota alerts (nil: none).
	Notifier notifications.Emitter
	// Backends is one Backend per configured kind ("s3", "registry", "loki").
	// A kind without one is reported as not configured and no slice is made.
	Backends map[string]Backend
	// Endpoints is the internal URL per kind, recorded on instance_services.
	Endpoints map[string]string
	Logger    *slog.Logger
	// DiskPath is a path on the filesystem that holds the instance services'
	// data (in the control plane's container "/" is the host's Docker data
	// disk). With it set, writes to the built-in S3 and registry are refused
	// while less than DiskReserveBytes or 5% of the disk is free. Empty: no guard.
	DiskPath string
}

// Service owns the instance services' state in the database.
type Service struct {
	pool      *pgxpool.Pool
	q         *db.Queries
	box       *secretbox.Box
	jobs      *jobs.Manager
	admins    AdminChecker
	notifier  notifications.Emitter
	backends  map[string]Backend
	endpoints map[string]string
	log       *slog.Logger
	now       func() time.Time

	// ensureMu serializes Ensure and EnsureTeam runs (boot, periodic, request).
	ensureMu sync.Mutex

	gate gate
	// refreshDelay is how long after an upload the team's usage is re-read.
	refreshDelay time.Duration

	// disk refuses writes when the shared disk is nearly full; regUsage caches
	// the registry ledger's byte count per team.
	disk     diskGuard
	regUsage registryUsages
}

// New returns a Service. Pool may be nil (OpenAPI generation); nothing touches
// the database before the first call.
func New(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{
		pool: d.Pool, box: d.Box, jobs: d.Jobs, admins: d.Admins, notifier: d.Notifier,
		backends: d.Backends, endpoints: d.Endpoints, log: d.Logger.With("component", "instsvc"), now: time.Now,
		refreshDelay: 20 * time.Second,
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	s.gate = newGate()
	if d.DiskPath != "" {
		s.disk.usage = DiskUsageOf(d.DiskPath)
	}
	return s
}

// SetBackend installs the Backend of a kind and the internal URL recorded on its
// row. The Backends need the Service (they open slice secrets through it), so
// the wiring builds the Service first and adds them before the first call.
func (s *Service) SetBackend(kind string, be Backend, endpoint string) {
	if s.backends == nil {
		s.backends = map[string]Backend{}
	}
	if s.endpoints == nil {
		s.endpoints = map[string]string{}
	}
	s.backends[kind], s.endpoints[kind] = be, endpoint
}

// Enabled reports whether any instance service is configured.
func (s *Service) Enabled() bool { return len(s.backends) > 0 }

// wantedKinds lists the configured kinds, in provisioning order.
func (s *Service) wantedKinds() []string {
	var out []string
	for _, k := range kinds {
		if s.backends[k] != nil {
			out = append(out, k)
		}
	}
	return out
}

var (
	// ErrNoSlice means no usable slice exists for the team, server or key.
	ErrNoSlice = errors.New("instsvc: no such slice")
	// ErrQuotaExceeded is what the quota gate answers when a write would go over
	// the team's quota.
	ErrQuotaExceeded = errors.New("built-in backup storage is full")
	// ErrNotReady means the team's slice is still being set up or failed.
	ErrNotReady = errors.New("built-in backup storage is not ready")
)
