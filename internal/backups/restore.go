package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// RestoreInput describes a restore.
type RestoreInput struct {
	// Mode is "new" (default: restore into a new sibling database) or "in_place".
	Mode string
	// Name names the new database (mode new); default <source>-restored-<time>.
	Name string
	// ServerID places the new database (mode new); default the source's server.
	// A backup stored in a local directory can only be restored on its own server.
	ServerID *uuid.UUID
	// ConfirmSlug must equal the database's slug for an in-place restore.
	ConfirmSlug string
}

// restoreTimeout bounds how long a restore waits for its new database to run.
const restoreTimeout = 30 * time.Minute

// Restore starts a restore of a backup.
func (s *Service) Restore(ctx context.Context, backupID uuid.UUID, in RestoreInput) (RestoreView, error) {
	b, sc, err := s.backupScope(ctx, policy.ScopeDeploymentsWrite, backupID)
	if err != nil {
		return RestoreView{}, err
	}
	if b.Status != StatusSucceeded || b.ObjectDeletedAt != nil {
		return RestoreView{}, apierr.Conflict("backup_not_restorable", "only a successful backup whose stored object still exists can be restored")
	}
	if !agentprotocol.RestoreSupported(b.Engine) {
		return RestoreView{}, apierr.Unprocessable("restore_unsupported", fmt.Sprintf("restoring %s backups is not supported yet; the dump object can be restored manually", b.Engine))
	}
	dest, err := s.q.GetBackupDestinationInternal(ctx, b.DestinationID)
	if err != nil {
		return RestoreView{}, fmt.Errorf("load destination: %w", err)
	}
	mode := in.Mode
	if mode == "" {
		mode = ModeNew
	}
	switch mode {
	case ModeNew:
		return s.restoreNew(ctx, b, sc, dest, in)
	case ModeInPlace:
		return s.restoreInPlace(ctx, b, sc, dest, in)
	}
	return RestoreView{}, apierr.Unprocessable("validation_failed", "mode must be new or in_place")
}

func (s *Service) restoreNew(ctx context.Context, b db.Backup, sc services.Scope, dest db.BackupDestination, in RestoreInput) (RestoreView, error) {
	serverID := sc.Service.ServerID
	if in.ServerID != nil {
		serverID = in.ServerID
	}
	if serverID == nil {
		return RestoreView{}, apierr.Unprocessable("no_server", "choose a server for the restored database")
	}
	if dest.Kind == agentprotocol.DestLocal && (b.ServerID == nil || *b.ServerID != *serverID) {
		return RestoreView{}, apierr.Unprocessable("local_backup_server", "this backup is stored in a directory on its own server; restore it onto a database on that same server")
	}
	ws := sc.WorkspaceID
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return RestoreView{}, apierr.Unprocessable("server_not_found", "the selected server does not exist")
	}
	if err != nil {
		return RestoreView{}, fmt.Errorf("load server: %w", err)
	}
	if srv.Status != "online" {
		return RestoreView{}, apierr.Conflict("server_offline", "the server "+srv.Name+" is offline; the restore was not started")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = fmt.Sprintf("%s-restored-%s", sc.Service.Slug, s.now().UTC().Format("20060102-1504"))
	}
	if len(name) > 80 {
		return RestoreView{}, apierr.Unprocessable("validation_failed", "name must be at most 80 characters")
	}
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, &sc.ProjectID); err != nil {
		return RestoreView{}, err // creating the new database needs write access too
	}
	ctx, err = guardRestoreCall(ctx, sc, b, ModeNew, name, in)
	if err != nil {
		return RestoreView{}, err
	}
	targetID, err := s.prov.CreateSibling(ctx, sc, name, *serverID)
	if err != nil {
		return RestoreView{}, err
	}
	var r db.BackupRestore
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, err = db.New(tx).InsertBackupRestore(ctx, db.InsertBackupRestoreParams{
			WorkspaceID: ws, BackupID: b.ID, Mode: ModeNew, SourceServiceID: sc.Service.ID, TargetServiceID: &targetID,
			Status: RestoreProvisioning, ActorLabel: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		if err := s.jm.InsertTx(ctx, tx, RestoreAdvanceArgs{RestoreID: r.ID}, &river.InsertOpts{ScheduledAt: s.now().Add(3 * time.Second)}); err != nil {
			return fmt.Errorf("enqueue restore: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRestore, ResourceType: "backup_restore", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "backup": b.ID.String(), "mode": ModeNew, "target": name},
		})
	})
	if err != nil {
		return RestoreView{}, fmt.Errorf("start restore: %w", err)
	}
	dep, err := s.deployer.Deploy(ctx, targetID, "", "Restore target for backup "+b.ID.String()[:8])
	if err != nil {
		s.finishRestore(ctx, r.ID, RestoreFailed, "could not deploy the new database: "+err.Error())
		return RestoreView{}, err
	}
	_ = s.q.SetRestoreDeployment(ctx, db.SetRestoreDeploymentParams{ID: r.ID, DeploymentID: &dep.ID})
	s.publish(sc, EventBackupUpdated, r.ID)
	r.DeploymentID = &dep.ID
	return restoreView(r), nil
}

func (s *Service) restoreInPlace(ctx context.Context, b db.Backup, sc services.Scope, dest db.BackupDestination, in RestoreInput) (RestoreView, error) {
	if in.ConfirmSlug != sc.Service.Slug {
		return RestoreView{}, apierr.Unprocessable("confirmation_required", "type the database's slug ("+sc.Service.Slug+") to confirm an in-place restore")
	}
	if spec, err := databaseSpec(sc.Service.Config); err == nil && spec.Replica != nil {
		return RestoreView{}, apierr.Conflict("replica_read_only", "a read replica cannot be restored in place: restore the primary (it replicates to its replicas as ordinary writes)")
	}
	tgt, err := s.resolveTarget(ctx, s.q, sc)
	if err != nil {
		return RestoreView{}, err
	}
	name, online, err := s.serverState(ctx, s.q, sc.Service.ServerID)
	if err != nil {
		return RestoreView{}, err
	}
	if !online {
		return RestoreView{}, apierr.Conflict("server_offline", "the server "+name+" is offline; the restore was not started")
	}
	if ok, why := databaseRunning(sc.Service, s.now()); !ok {
		return RestoreView{}, apierr.Conflict("database_not_running", "the restore was not started: "+why)
	}
	if dest.Kind == agentprotocol.DestLocal && (b.ServerID == nil || *b.ServerID != *sc.Service.ServerID) {
		return RestoreView{}, apierr.Unprocessable("local_backup_server", "this backup is stored on another server's disk")
	}
	ctx, err = guardRestoreCall(ctx, sc, b, ModeInPlace, "", in)
	if err != nil {
		return RestoreView{}, err
	}
	var r db.BackupRestore
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The safety net: a fresh backup of the current data goes first.
		pre, err := s.startBackup(ctx, tx, startInput{sc: sc, dest: dest, trigger: TriggerPreRestore, tgt: tgt, actor: actorLabel(ctx)})
		if err != nil {
			return err
		}
		r, err = db.New(tx).InsertBackupRestore(ctx, db.InsertBackupRestoreParams{
			WorkspaceID: sc.WorkspaceID, BackupID: b.ID, Mode: ModeInPlace, SourceServiceID: sc.Service.ID, TargetServiceID: &sc.Service.ID,
			Status: RestorePreBackup, PreBackupID: &pre.ID, ActorLabel: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRestore, ResourceType: "backup_restore", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "backup": b.ID.String(), "mode": ModeInPlace, "preRestoreBackup": pre.ID.String()},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return RestoreView{}, err
		}
		return RestoreView{}, fmt.Errorf("start restore: %w", err)
	}
	s.publish(sc, EventBackupUpdated, r.ID)
	return restoreView(r), nil
}

// GetRestore returns one restore.
func (s *Service) GetRestore(ctx context.Context, id uuid.UUID) (RestoreView, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return RestoreView{}, err
	}
	r, err := s.q.GetBackupRestore(ctx, db.GetBackupRestoreParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return RestoreView{}, apierr.NotFound("restore")
	}
	if err != nil {
		return RestoreView{}, fmt.Errorf("get restore: %w", err)
	}
	if _, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, r.SourceServiceID); err != nil {
		return RestoreView{}, err
	}
	return restoreView(r), nil
}

// ListRestores returns the restores into or from a database, newest first.
func (s *Service) ListRestores(ctx context.Context, serviceID uuid.UUID) ([]RestoreView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceRestores(ctx, db.ListServiceRestoresParams{SourceServiceID: sc.Service.ID, Limit: 50})
	if err != nil {
		return nil, fmt.Errorf("list restores: %w", err)
	}
	out := make([]RestoreView, len(rows))
	for i, r := range rows {
		out[i] = restoreView(r)
	}
	return out, nil
}

// finishRestore marks a restore finished and tells listeners.
func (s *Service) finishRestore(ctx context.Context, id uuid.UUID, status, msg string) {
	r, err := s.q.SetRestoreProgress(ctx, db.SetRestoreProgressParams{ID: id, Status: status, Error: msg})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.ErrorContext(ctx, "finish restore", "restore_id", id, "err", err)
		}
		return
	}
	s.afterRestoreFinished(ctx, r)
}

func (s *Service) afterRestoreFinished(ctx context.Context, r db.BackupRestore) {
	sc, err := services.LoadScopeInternal(ctx, s.q, r.SourceServiceID)
	if err != nil {
		return
	}
	s.publish(sc, EventBackupUpdated, r.ID)
	if r.TargetServiceID != nil && *r.TargetServiceID != r.SourceServiceID {
		services.PublishService(s.bus, services.EventServiceUpdated, services.Scope{
			Service: db.Service{ID: *r.TargetServiceID}, ProjectID: sc.ProjectID, EnvironmentID: sc.EnvironmentID,
		})
	}
	typ := NotifyRestoreSucceeded
	if r.Status == RestoreFailed {
		typ = NotifyRestoreFailed
	}
	ev := Event{
		Type: typ, WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID, ProjectName: sc.ProjectName, Environment: sc.EnvSlug,
		ServiceID: sc.Service.ID, ServiceName: sc.Service.Name, BackupID: r.BackupID, RestoreID: r.ID, Message: r.Error, At: s.now(),
	}
	s.notify(ctx, ev)
}

// sendRestore enqueues the db_restore command of a restore whose target is
// ready (new database running, or pre-restore backup done).
func (s *Service) sendRestore(ctx context.Context, r db.BackupRestore) error {
	b, err := s.q.GetBackupInternal(ctx, r.BackupID)
	if err != nil {
		return fmt.Errorf("load backup: %w", err)
	}
	dest, err := s.q.GetBackupDestinationInternal(ctx, b.DestinationID)
	if err != nil {
		return fmt.Errorf("load destination: %w", err)
	}
	targetID := r.SourceServiceID
	if r.TargetServiceID != nil {
		targetID = *r.TargetServiceID
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, targetID)
	if err != nil {
		return fmt.Errorf("load target database: %w", err)
	}
	if sc.Service.ServerID == nil {
		return errors.New("the target database has no server")
	}
	tgt, err := s.resolveTarget(ctx, s.q, sc)
	if err != nil {
		return err
	}
	agentDest, err := s.destinationFor(ctx, dest)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(agentprotocol.DBRestorePayload{
		RestoreID: r.ID.String(), Target: tgt.proto(targetID.String()), Destination: agentDest,
		ObjectKey: b.ObjectKey, Format: b.Format, SHA256: b.Sha256, Clean: r.Mode == ModeInPlace,
	})
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cmdID, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: *sc.Service.ServerID, Kind: agentprotocol.KindDBRestore, Payload: payload, IdempotencyKey: "db_restore:" + r.ID.String(),
		})
		if err != nil {
			return err
		}
		_, err = db.New(tx).SetRestoreProgress(ctx, db.SetRestoreProgressParams{ID: r.ID, Status: RestoreRestoring, CommandID: &cmdID})
		return err
	})
}
