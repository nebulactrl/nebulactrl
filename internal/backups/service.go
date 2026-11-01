// Package backups implements scheduled and on-demand database backups,
// destinations (S3-compatible buckets or a directory on the database's host),
// retention and restores.
//
// The control plane holds metadata only. A backup is an agent command
// (db_backup) whose payload names the destination and object key but never any
// credential: the destination's keys and the database password are attached at
// send time (SecretsForCommand) and exist only in agent memory for the run.
package backups

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Provisioner creates the database a backup is restored into.
type Provisioner interface {
	// CreateSibling creates a staged database service in source's environment
	// with the same engine, version and settings, fresh credentials and its own
	// volume, placed on serverID, and returns its id. The source is untouched.
	CreateSibling(ctx context.Context, source services.Scope, name string, serverID uuid.UUID) (uuid.UUID, error)
}

// Deployer starts a deployment of a service.
type Deployer interface {
	Deploy(ctx context.Context, serviceID uuid.UUID, commitSHA, message string) (deployments.DeploymentView, error)
}

// Service implements the backup use cases.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	jm   *jobs.Manager
	box  *secretbox.Box
	disp agents.Dispatcher
	log  *slog.Logger
	ext  *services.ExtSecrets

	prov     Provisioner
	deployer Deployer
	notifier atomic.Pointer[Notifier]
	builtin  atomic.Pointer[BuiltinStorage] // resolves builtin destinations; nil: they fail
	streams  Streams                        // the volume backup file browser; nil answers server_offline
	fsWait   time.Duration                  // wait for the agent's next file frame (0 = volumeFSTimeout); tests shrink it

	dumpMax    int64         // largest uploaded dump (0 = DefaultDumpMaxBytes)
	dumpActive sync.Map      // restore id → struct{}: dump restores a goroutine of this process drives
	dumpWait   time.Duration // wait for the agent's answer in a dump restore (0 = dumpAgentWait); tests shrink it

	now func() time.Time
}

// Deps are the collaborators of a Service. Pool, Bus, Jobs, Box, Dispatcher and
// Deployer may be nil for OpenAPI generation only.
type Deps struct {
	Pool        *pgxpool.Pool
	Bus         *events.Bus
	Jobs        *jobs.Manager
	Box         *secretbox.Box
	Dispatcher  agents.Dispatcher
	Deployer    Deployer
	Provisioner Provisioner // creates the restore target; see NewDatabaseProvisioner
	Streams     Streams     // opens the agent streams of the volume backup file browser; see HubStreams
	Logger      *slog.Logger
	// ExtSecrets fetches `${{vault:…}}` values when a backup runs (nil: none).
	ExtSecrets *services.ExtSecrets
	// DumpMaxBytes limits an uploaded dump (0: DefaultDumpMaxBytes).
	DumpMaxBytes int64
	// Builtin resolves the team's slice of the built-in storage for builtin
	// destinations (nil: they fail with a plain error; SetBuiltinStorage adds it later).
	Builtin BuiltinStorage
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	s := &Service{
		pool: d.Pool, bus: d.Bus, jm: d.Jobs, box: d.Box, disp: d.Dispatcher, deployer: d.Deployer, prov: d.Provisioner,
		streams: d.Streams, log: d.Logger, now: time.Now, ext: d.ExtSecrets, dumpMax: d.DumpMaxBytes,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	if d.Builtin != nil {
		s.SetBuiltinStorage(d.Builtin)
	}
	return s
}

// SetNotifier installs the hook told about finished backups and restores.
func (s *Service) SetNotifier(n Notifier) { s.notifier.Store(&n) }

func (s *Service) notify(ctx context.Context, ev Event) {
	if p := s.notifier.Load(); p != nil && *p != nil {
		(*p).Notify(ctx, ev)
	}
}

func (s *Service) publish(sc services.Scope, resource uuid.UUID) {
	services.PublishIn(s.bus, sc.WorkspaceID, EventBackupUpdated, sc.ProjectID, sc.EnvironmentID, resource)
}

// publishWorkspace announces a change to a team-level resource (destinations).
func (s *Service) publishWorkspace(ws, resource uuid.UUID) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{Type: EventDestinationUpdated, ResourceID: resource.String(), WorkspaceID: ws.String()})
}

func (s *Service) workspace(ctx context.Context, scope string) (uuid.UUID, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return uuid.Nil, err
	}
	return policy.WorkspaceID(ctx), nil
}

// destination loads a destination of the caller's workspace.
func (s *Service) destination(ctx context.Context, q *db.Queries, ws, id uuid.UUID) (db.BackupDestination, error) {
	d, err := q.GetBackupDestination(ctx, db.GetBackupDestinationParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return d, apierr.NotFound("backup destination")
	}
	if err != nil {
		return d, fmt.Errorf("get destination: %w", err)
	}
	return d, nil
}

// backupScope authorizes scope on the backup's service and returns it.
func (s *Service) backupScope(ctx context.Context, scope string, id uuid.UUID) (db.Backup, services.Scope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Backup{}, services.Scope{}, err
	}
	b, err := s.q.GetBackup(ctx, db.GetBackupParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return b, services.Scope{}, apierr.NotFound("backup")
	}
	if err != nil {
		return b, services.Scope{}, fmt.Errorf("get backup: %w", err)
	}
	sc, err := services.LoadScope(ctx, s.q, scope, b.ServiceID)
	if err != nil {
		return b, sc, err
	}
	return b, sc, nil
}

// serverState reports the name of the server and whether it can take commands now.
func (s *Service) serverState(ctx context.Context, q *db.Queries, serverID *uuid.UUID) (name string, online bool, err error) {
	if serverID == nil {
		return "", false, nil
	}
	srv, err := q.GetServerByID(ctx, *serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load server: %w", err)
	}
	return srv.Name, srv.Status == "online", nil
}
