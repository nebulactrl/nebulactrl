package backups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Restore from a dump file the user uploads. Unlike a backup restore the data
// does not come from a destination: it arrives in the upload request and is
// relayed to the agent as it comes (dump_upload.go), so the control plane
// holds neither the file nor a copy of it. The flow is:
//
//	create     choose the target (a new sibling database, or this database in
//	           place after a safety backup) → provisioning / pre_backup
//	await      the target runs → awaiting_upload
//	upload     the file is streamed to the agent → uploading → restoring
//	finish     the agent's engine client exits → succeeded / failed

// Dump restore statuses added to the restore statuses of types.go.
const (
	RestoreAwaitingUpload = "awaiting_upload"
	RestoreUploading      = "uploading"
)

// Audit actions of dump restores.
const (
	ActionDumpRestoreCreate = "backup.dump_restore.create"
	ActionDumpRestoreUpload = "backup.dump_restore.upload"
	ActionDumpRestoreDone   = "backup.dump_restore.succeeded"
	ActionDumpRestoreFail   = "backup.dump_restore.failed"
	ActionDumpRestoreCancel = "backup.dump_restore.cancel"
)

const (
	// DefaultDumpMaxBytes is the default limit of one uploaded dump (5 GiB).
	DefaultDumpMaxBytes int64 = 5 << 30
	// dumpAwaitTimeout is how long a prepared target waits for its file.
	dumpAwaitTimeout = 2 * time.Hour
	// dumpOrphanAfter is how long an uploading or restoring restore may go
	// without news when no goroutine of this process owns it (the control
	// plane restarted, which also ended the agent's stream).
	dumpOrphanAfter = 3 * time.Minute

	guardRestoreDump = "restore_dump"
)

// DumpRestoreInput describes a restore from a dump file.
type DumpRestoreInput struct {
	// Mode is "new" (default: restore into a new sibling database) or "in_place".
	Mode string
	// Name names the new database (mode new); default <source>-restored-<time>.
	Name string
	// ServerID places the new database (mode new); default the source's server.
	ServerID *uuid.UUID
	// ConfirmSlug must equal the database's slug for an in-place restore.
	ConfirmSlug string
	// DestinationID receives the safety backup an in-place restore takes first;
	// default: the destination of the database's backup schedule.
	DestinationID *uuid.UUID
	// FileName and SizeBytes describe the file the client is about to upload
	// (both optional). A size above the limit is refused before anything is
	// created.
	FileName  string
	SizeBytes int64
}

// DumpRestoreView is the API view of a restore from a dump file.
type DumpRestoreView struct {
	ID              uuid.UUID  `json:"id"`
	Mode            string     `json:"mode" enum:"new,in_place"`
	SourceServiceID uuid.UUID  `json:"sourceServiceId"`
	TargetServiceID *uuid.UUID `json:"targetServiceId,omitempty"`
	Status          string     `json:"status" enum:"provisioning,pre_backup,awaiting_upload,uploading,restoring,succeeded,failed"`
	FileName        string     `json:"fileName,omitempty"`
	Format          string     `json:"format,omitempty" doc:"Detected from the file's first bytes: pg_custom, pg_tar, sql or mongo_archive."`
	Gzip            bool       `json:"gzip,omitempty"`
	SizeBytes       int64      `json:"sizeBytes" doc:"Bytes announced by the upload."`
	BytesReceived   int64      `json:"bytesReceived" doc:"Bytes the agent has accepted so far."`
	SHA256          string     `json:"sha256,omitempty" doc:"Checksum of the uploaded bytes, known once the upload completed."`
	PreBackupID     *uuid.UUID `json:"preBackupId,omitempty"`
	MaxUploadBytes  int64      `json:"maxUploadBytes" doc:"The largest file the control plane accepts."`
	Error           string     `json:"error,omitempty"`
	Log             string     `json:"log,omitempty" doc:"Progress of the restore (the newest 64 KiB), one line per entry. Omitted from lists."`
	Actor           string     `json:"actor,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
}

func (s *Service) dumpView(r db.DumpRestore, withLog bool) DumpRestoreView {
	v := DumpRestoreView{
		ID: r.ID, Mode: r.Mode, SourceServiceID: r.SourceServiceID, TargetServiceID: r.TargetServiceID, Status: r.Status,
		FileName: r.FileName, Format: r.Format, Gzip: r.Gzip, SizeBytes: r.SizeBytes, BytesReceived: r.BytesReceived, SHA256: r.Sha256,
		PreBackupID: r.PreBackupID, MaxUploadBytes: s.dumpLimit(), Error: r.Error, Actor: r.ActorLabel,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	if withLog {
		v.Log = r.Log
	}
	return v
}

// dumpLimit is the largest upload accepted.
func (s *Service) dumpLimit() int64 {
	if s.dumpMax > 0 {
		return s.dumpMax
	}
	return DefaultDumpMaxBytes
}

// cleanFileName keeps the file name of an upload short and printable.
func cleanFileName(n string) string {
	n = strings.TrimSpace(n)
	if i := strings.LastIndexAny(n, `/\`); i >= 0 {
		n = n[i+1:]
	}
	var b strings.Builder
	for _, r := range n {
		if r == utf8.RuneError || r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 200 {
			break
		}
	}
	return b.String()
}

func tooLarge(limit int64) error {
	return apierr.New(413, "file_too_large", fmt.Sprintf("the dump is larger than the limit of %s; raise NEBULACTRL_MAX_DUMP_UPLOAD_BYTES to accept larger files", humanBytes(limit)))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// guardDumpRestoreCall asks the production guard before a dump restore touches
// or adds a production database.
func guardDumpRestoreCall(ctx context.Context, sc services.Scope, mode, newName string, in DumpRestoreInput) (context.Context, error) {
	if !prodguard.Applies(ctx, sc.IsProduction) {
		return ctx, nil
	}
	var summary string
	var eff []string
	irreversible := false
	if mode == ModeInPlace {
		summary = fmt.Sprintf("Restore %s in production from an uploaded dump file, overwriting its data (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName)
		eff = []string{
			"Replaces the current data of " + sc.Service.Name + " with the contents of the uploaded dump.",
			"A fresh backup of the current data is taken first.",
		}
		irreversible = true
	} else {
		summary = fmt.Sprintf("Restore an uploaded dump file into a new database next to %s (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName)
		eff = []string{
			"Creates the database " + newName + " next to " + sc.Service.Name + " and deploys it.",
			"The existing database is not touched.",
		}
	}
	extra := ""
	if in.ServerID != nil {
		extra = in.ServerID.String()
	}
	if err := prodguard.Check(ctx, sc.ProdScope(summary, irreversible, eff...), guardRestoreDump,
		prodguard.Digest(guardRestoreDump, sc.Service.ID, mode, in.Name, extra)); err != nil {
		return ctx, err
	}
	return prodguard.Covered(ctx), nil
}

// CreateDumpRestore prepares a restore from a dump file: it provisions the
// target (a new sibling database, or a safety backup of this one) and returns
// the restore, which awaits its file once the target is ready.
func (s *Service) CreateDumpRestore(ctx context.Context, serviceID uuid.UUID, in DumpRestoreInput) (DumpRestoreView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return DumpRestoreView{}, err
	}
	spec, err := databaseSpec(sc.Service.Config)
	if err != nil {
		return DumpRestoreView{}, err
	}
	if !agentprotocol.DumpRestoreSupported(spec.Engine) {
		return DumpRestoreView{}, apierr.Unprocessable("restore_unsupported", fmt.Sprintf("restoring a dump file into %s is not supported", spec.Engine))
	}
	if in.SizeBytes < 0 {
		return DumpRestoreView{}, apierr.Unprocessable("validation_failed", "sizeBytes must not be negative")
	}
	if in.SizeBytes > s.dumpLimit() {
		return DumpRestoreView{}, tooLarge(s.dumpLimit())
	}
	in.FileName = cleanFileName(in.FileName)
	mode := in.Mode
	if mode == "" {
		mode = ModeNew
	}
	switch mode {
	case ModeNew:
		return s.createDumpNew(ctx, sc, in)
	case ModeInPlace:
		return s.createDumpInPlace(ctx, sc, in)
	}
	return DumpRestoreView{}, apierr.Unprocessable("validation_failed", "mode must be new or in_place")
}

func (s *Service) createDumpNew(ctx context.Context, sc services.Scope, in DumpRestoreInput) (DumpRestoreView, error) {
	serverID := sc.Service.ServerID
	if in.ServerID != nil {
		serverID = in.ServerID
	}
	if serverID == nil {
		return DumpRestoreView{}, apierr.Unprocessable("no_server", "choose a server for the new database")
	}
	ws := sc.WorkspaceID
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: *serverID, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return DumpRestoreView{}, apierr.Unprocessable("server_not_found", "the selected server does not exist")
	}
	if err != nil {
		return DumpRestoreView{}, fmt.Errorf("load server: %w", err)
	}
	if srv.Status != "online" {
		return DumpRestoreView{}, apierr.Conflict("server_offline", "the server "+srv.Name+" is offline; the restore was not started")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = fmt.Sprintf("%s-restored-%s", sc.Service.Slug, s.now().UTC().Format("20060102-1504"))
	}
	if len(name) > 80 {
		return DumpRestoreView{}, apierr.Unprocessable("validation_failed", "name must be at most 80 characters")
	}
	if err := policy.Require(ctx, policy.ScopeProjectsWrite, &sc.ProjectID); err != nil {
		return DumpRestoreView{}, err // creating the new database needs write access too
	}
	ctx, err = guardDumpRestoreCall(ctx, sc, ModeNew, name, in)
	if err != nil {
		return DumpRestoreView{}, err
	}
	targetID, err := s.prov.CreateSibling(ctx, sc, name, *serverID)
	if err != nil {
		return DumpRestoreView{}, err
	}
	var r db.DumpRestore
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, err = db.New(tx).InsertDumpRestore(ctx, db.InsertDumpRestoreParams{
			WorkspaceID: ws, Mode: ModeNew, SourceServiceID: sc.Service.ID, TargetServiceID: &targetID,
			Status: RestoreProvisioning, DeclaredSize: in.SizeBytes, FileName: in.FileName, ActorLabel: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		if err := s.jm.InsertTx(ctx, tx, DumpRestoreAdvanceArgs{RestoreID: r.ID}, &river.InsertOpts{ScheduledAt: s.now().Add(3 * time.Second)}); err != nil {
			return fmt.Errorf("enqueue dump restore: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDumpRestoreCreate, ResourceType: "dump_restore", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "mode": ModeNew, "target": name, "file": in.FileName, "size": in.SizeBytes},
		})
	})
	if err != nil {
		return DumpRestoreView{}, fmt.Errorf("start dump restore: %w", err)
	}
	dep, err := s.deployer.Deploy(ctx, targetID, "", "Restore target for an uploaded dump")
	if err != nil {
		s.finishDump(ctx, r.ID, false, "could not deploy the new database: "+err.Error())
		return DumpRestoreView{}, err
	}
	_ = s.q.SetDumpRestoreDeployment(ctx, db.SetDumpRestoreDeploymentParams{ID: r.ID, DeploymentID: &dep.ID})
	s.publish(sc, EventBackupUpdated, r.ID)
	r.DeploymentID = &dep.ID
	return s.dumpView(r, true), nil
}

// safetyDestination picks where an in-place restore's safety backup goes: the
// requested destination, else the one of the database's backup schedule.
func (s *Service) safetyDestination(ctx context.Context, sc services.Scope, id *uuid.UUID) (db.BackupDestination, error) {
	if id != nil {
		return s.destination(ctx, s.q, sc.WorkspaceID, *id)
	}
	scheds, err := s.q.ListBackupSchedulesByService(ctx, sc.Service.ID)
	if err != nil {
		return db.BackupDestination{}, fmt.Errorf("list schedules: %w", err)
	}
	for _, sch := range scheds {
		if sch.Enabled {
			return s.destination(ctx, s.q, sc.WorkspaceID, sch.DestinationID)
		}
	}
	if len(scheds) > 0 {
		return s.destination(ctx, s.q, sc.WorkspaceID, scheds[0].DestinationID)
	}
	return db.BackupDestination{}, apierr.Unprocessable("destination_required",
		"an in-place restore takes a safety backup first: choose a backup destination (destinationId) for it")
}

func (s *Service) createDumpInPlace(ctx context.Context, sc services.Scope, in DumpRestoreInput) (DumpRestoreView, error) {
	if in.ConfirmSlug != sc.Service.Slug {
		return DumpRestoreView{}, apierr.Unprocessable("confirmation_required", "type the database's slug ("+sc.Service.Slug+") to confirm an in-place restore")
	}
	if spec, err := databaseSpec(sc.Service.Config); err == nil && spec.Replica != nil {
		return DumpRestoreView{}, apierr.Conflict("replica_read_only", "a read replica cannot be restored in place: restore the primary (it replicates to its replicas as ordinary writes)")
	}
	tgt, err := s.resolveTarget(ctx, s.q, sc)
	if err != nil {
		return DumpRestoreView{}, err
	}
	name, online, err := s.serverState(ctx, s.q, sc.Service.ServerID)
	if err != nil {
		return DumpRestoreView{}, err
	}
	if !online {
		return DumpRestoreView{}, apierr.Conflict("server_offline", "the server "+name+" is offline; the restore was not started")
	}
	if ok, why := databaseRunning(sc.Service, s.now()); !ok {
		return DumpRestoreView{}, apierr.Conflict("database_not_running", "the restore was not started: "+why)
	}
	dest, err := s.safetyDestination(ctx, sc, in.DestinationID)
	if err != nil {
		return DumpRestoreView{}, err
	}
	ctx, err = guardDumpRestoreCall(ctx, sc, ModeInPlace, "", in)
	if err != nil {
		return DumpRestoreView{}, err
	}
	var r db.DumpRestore
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The safety net: a fresh backup of the current data goes first.
		pre, err := s.startBackup(ctx, tx, startInput{sc: sc, dest: dest, trigger: TriggerPreRestore, tgt: tgt, actor: actorLabel(ctx)})
		if err != nil {
			return err
		}
		r, err = db.New(tx).InsertDumpRestore(ctx, db.InsertDumpRestoreParams{
			WorkspaceID: sc.WorkspaceID, Mode: ModeInPlace, SourceServiceID: sc.Service.ID, TargetServiceID: &sc.Service.ID,
			Status: RestorePreBackup, DeclaredSize: in.SizeBytes, FileName: in.FileName, PreBackupID: &pre.ID, ActorLabel: actorLabel(ctx),
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDumpRestoreCreate, ResourceType: "dump_restore", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{
				"service": sc.Service.Slug, "mode": ModeInPlace, "preRestoreBackup": pre.ID.String(), "file": in.FileName, "size": in.SizeBytes,
			},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return DumpRestoreView{}, err
		}
		return DumpRestoreView{}, fmt.Errorf("start dump restore: %w", err)
	}
	s.publish(sc, EventBackupUpdated, r.ID)
	return s.dumpView(r, true), nil
}

// loadDump loads a dump restore of the caller's workspace and authorizes scope
// on the database it belongs to.
func (s *Service) loadDump(ctx context.Context, scope string, id uuid.UUID) (db.DumpRestore, services.Scope, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.DumpRestore{}, services.Scope{}, err
	}
	r, err := s.q.GetDumpRestore(ctx, db.GetDumpRestoreParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, services.Scope{}, apierr.NotFound("restore")
	}
	if err != nil {
		return r, services.Scope{}, fmt.Errorf("get dump restore: %w", err)
	}
	sc, err := services.LoadScope(ctx, s.q, scope, r.SourceServiceID)
	if err != nil {
		return r, sc, err
	}
	return r, sc, nil
}

// GetDumpRestore returns one restore from a dump file.
func (s *Service) GetDumpRestore(ctx context.Context, id uuid.UUID) (DumpRestoreView, error) {
	r, _, err := s.loadDump(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return DumpRestoreView{}, err
	}
	return s.dumpView(r, true), nil
}

// ListDumpRestores returns the dump restores into or from a database, newest
// first.
func (s *Service) ListDumpRestores(ctx context.Context, serviceID uuid.UUID) ([]DumpRestoreView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceDumpRestores(ctx, db.ListServiceDumpRestoresParams{SourceServiceID: sc.Service.ID, Limit: 50})
	if err != nil {
		return nil, fmt.Errorf("list dump restores: %w", err)
	}
	out := make([]DumpRestoreView, len(rows))
	for i, r := range rows {
		out[i] = s.dumpView(r, false)
	}
	return out, nil
}

// CancelDumpRestore ends a restore that has not received its file yet. The new
// database it provisioned stays (delete it like any service); nothing else was
// changed.
func (s *Service) CancelDumpRestore(ctx context.Context, id uuid.UUID) (DumpRestoreView, error) {
	r, sc, err := s.loadDump(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return DumpRestoreView{}, err
	}
	switch r.Status {
	case RestoreProvisioning, RestorePreBackup, RestoreAwaitingUpload:
	case RestoreUploading, RestoreRestoring:
		return DumpRestoreView{}, apierr.Conflict("dump_restore_running", "the dump is being restored and cannot be canceled")
	default:
		return DumpRestoreView{}, apierr.Conflict("dump_restore_finished", "this restore is already finished")
	}
	var out db.DumpRestore
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = db.New(tx).SetDumpRestoreStatus(ctx, db.SetDumpRestoreStatusParams{ID: r.ID, Status: RestoreFailed, Error: "Canceled by " + actorLabel(ctx) + "."})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDumpRestoreCancel, ResourceType: "dump_restore", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DumpRestoreView{}, apierr.Conflict("dump_restore_running", "the restore moved on and cannot be canceled")
	}
	if err != nil {
		return DumpRestoreView{}, fmt.Errorf("cancel dump restore: %w", err)
	}
	s.afterDumpFinished(ctx, out)
	return s.dumpView(out, true), nil
}

// finishDump marks a dump restore finished, records the outcome and tells
// listeners.
func (s *Service) finishDump(ctx context.Context, id uuid.UUID, ok bool, msg string) {
	ctx = context.WithoutCancel(ctx)
	status := RestoreSucceeded
	if !ok {
		status = RestoreFailed
		if msg == "" {
			msg = "the restore failed"
		}
	} else {
		msg = ""
	}
	var r db.DumpRestore
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, err = db.New(tx).SetDumpRestoreStatus(ctx, db.SetDumpRestoreStatusParams{ID: id, Status: status, Error: clip(msg)})
		if err != nil {
			return err
		}
		action := ActionDumpRestoreDone
		if !ok {
			action = ActionDumpRestoreFail
		}
		var pid *uuid.UUID
		if sc, err := services.LoadScopeInternal(ctx, db.New(tx), r.SourceServiceID); err == nil {
			pid = &sc.ProjectID
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "dump_restore", ResourceID: r.ID.String(), ProjectID: pid, WorkspaceID: r.WorkspaceID,
			Metadata: map[string]any{"mode": r.Mode, "format": r.Format, "size": r.SizeBytes, "sha256": r.Sha256, "error": r.Error},
		})
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.ErrorContext(ctx, "finish dump restore", "restore_id", id, "err", err)
		}
		return
	}
	s.afterDumpFinished(ctx, r)
}

// afterDumpFinished publishes and notifies a finished dump restore.
func (s *Service) afterDumpFinished(ctx context.Context, r db.DumpRestore) {
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
	s.notify(ctx, Event{
		Type: typ, WorkspaceID: sc.WorkspaceID, ProjectID: sc.ProjectID, ProjectName: sc.ProjectName, Environment: sc.EnvSlug,
		ServiceID: sc.Service.ID, ServiceName: sc.Service.Name, RestoreID: r.ID, Message: r.Error, At: s.now(),
	})
}

// afterDumpPreBackup moves a dump restore on once its safety backup finished.
func (s *Service) afterDumpPreBackup(ctx context.Context, r db.DumpRestore, b db.Backup) {
	if b.Status != StatusSucceeded {
		s.finishDump(ctx, r.ID, false, "The safety backup taken before the restore failed, so nothing was changed: "+b.Error)
		return
	}
	if _, err := s.q.SetDumpRestoreStatus(ctx, db.SetDumpRestoreStatusParams{ID: r.ID, Status: RestoreAwaitingUpload}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.ErrorContext(ctx, "dump restore awaiting upload", "restore_id", r.ID, "err", err)
		}
		return
	}
	if sc, err := services.LoadScopeInternal(ctx, s.q, r.SourceServiceID); err == nil {
		s.publish(sc, EventBackupUpdated, r.ID)
	}
}

// AdvanceDumpRestore moves a dump restore of a new database forward: once the
// target's deployment succeeded it awaits the file; if the deployment failed
// or the wait times out the restore fails. It returns done=true when nothing
// is left to wait for.
func (s *Service) AdvanceDumpRestore(ctx context.Context, restoreID uuid.UUID) (bool, error) {
	r, err := s.q.GetDumpRestoreInternal(ctx, restoreID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if r.Status != RestoreProvisioning {
		return true, nil
	}
	if s.now().Sub(r.CreatedAt) > restoreTimeout {
		s.finishDump(ctx, r.ID, false, "The new database did not become ready within 30 minutes.")
		return true, nil
	}
	if r.DeploymentID == nil {
		return false, nil // CreateDumpRestore is still starting the deployment
	}
	deps, err := s.q.ListDeploymentsByIDs(ctx, []uuid.UUID{*r.DeploymentID})
	if err != nil {
		return false, err
	}
	if len(deps) == 0 {
		s.finishDump(ctx, r.ID, false, "The new database's deployment no longer exists.")
		return true, nil
	}
	switch dep := deps[0]; dep.Status {
	case "succeeded":
		if _, err := s.q.SetDumpRestoreStatus(ctx, db.SetDumpRestoreStatusParams{ID: r.ID, Status: RestoreAwaitingUpload}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if sc, err := services.LoadScopeInternal(ctx, s.q, r.SourceServiceID); err == nil {
			s.publish(sc, EventBackupUpdated, r.ID)
		}
		return true, nil
	case "failed", "canceled", "rolled_back", "rollback_failed":
		msg := dep.StatusMessage
		if msg == "" {
			msg = dep.Status
		}
		s.finishDump(ctx, r.ID, false, "The new database could not be deployed: "+clip(msg))
		return true, nil
	}
	return false, nil
}

// SweepDumpRestores fails restores that can no longer make progress: a target
// that waited too long for its file, and uploads or restores whose owner (a
// goroutine of this process) is gone because the control plane restarted.
func (s *Service) SweepDumpRestores(ctx context.Context) error {
	now := s.now()
	rows, err := s.q.ListStaleDumpRestores(ctx, now.Add(-dumpOrphanAfter))
	if err != nil {
		return err
	}
	for _, r := range rows {
		switch r.Status {
		case RestoreAwaitingUpload:
			if now.Sub(r.UpdatedAt) > dumpAwaitTimeout {
				s.finishDump(ctx, r.ID, false, "No file was uploaded within "+dumpAwaitTimeout.String()+"; start the restore again.")
			}
		case RestoreUploading, RestoreRestoring:
			if _, live := s.dumpActive.Load(r.ID); !live {
				s.finishDump(ctx, r.ID, false, "The control plane restarted during the restore, which ended the transfer. Start the restore again; the target was not switched.")
			}
		}
	}
	return nil
}
