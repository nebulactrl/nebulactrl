package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// MigrationPausedReason is why the mesh is paused while the platform Headscale of
// an existing install moves from policy.mode: file to policy.mode: database. The
// mesh page shows it, followed by what failed when the migration did.
const MigrationPausedReason = "Headscale is being migrated"

const (
	headscalePolicyFile = "/etc/headscale/policy.hujson"
	headscaleConfigFile = "/etc/headscale/config.yaml"
	migrateInterval     = 10 * time.Minute
)

// HeadscalePolicyMigrateArgs scans for platform Headscale services that still run
// with a static policy file and migrates them. It is idempotent: a migrated
// Headscale is skipped.
type HeadscalePolicyMigrateArgs struct{}

// Kind implements river.JobArgs.
func (HeadscalePolicyMigrateArgs) Kind() string { return "mesh_headscale_policy_migrate" }

type migrateWorker struct {
	river.WorkerDefaults[HeadscalePolicyMigrateArgs]
	svc *Service
}

func (w *migrateWorker) Timeout(*river.Job[HeadscalePolicyMigrateArgs]) time.Duration {
	return 2 * time.Minute
}

func (w *migrateWorker) Work(ctx context.Context, _ *river.Job[HeadscalePolicyMigrateArgs]) error {
	return w.svc.MigrateHeadscalePolicy(ctx)
}

// RegisterMigrationJobs registers the one-time policy-mode migration of existing
// platform Headscale services. It runs at start and then every ten minutes (a
// failed or interrupted migration is picked up again; a finished one costs one
// cheap query).
func RegisterMigrationJobs(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &migrateWorker{svc: svc}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(migrateInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			return HeadscalePolicyMigrateArgs{}, &river.InsertOpts{MaxAttempts: 1}
		},
		&river.PeriodicJobOpts{ID: "mesh_headscale_policy_migrate", RunOnStart: true},
	))
}

// needsPolicyMigration reports whether a Headscale service still runs with the
// static policy file of earlier versions.
func needsPolicyMigration(cfg services.Config) bool {
	for _, f := range cfg.Files {
		if f.Path == headscalePolicyFile {
			return true
		}
		if f.Path == headscaleConfigFile && !strings.Contains(f.Content, "mode: database") {
			return true
		}
	}
	return false
}

// MigrateHeadscalePolicy moves every ready platform Headscale to policy.mode:
// database. For each one it pauses the mesh first (no key is minted, no node is
// synced, no ACL is written; established tunnels keep working), stages the new
// configuration, and restarts the platform service's own provisioning at the
// deploy step: it redeploys Headscale, waits for health and wires again, which
// writes the base policy plus the mesh rules and then lifts the pause
// (afterHeadscaleWire). A failure leaves the mesh paused with the reason; Retry
// on the platform service resumes it.
func (s *Service) MigrateHeadscalePolicy(ctx context.Context) error {
	rows, err := s.q.ListReadyPlatformServicesOfKind(ctx, KindHeadscale)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.migrateOne(ctx, row); err != nil {
			s.Logger.WarnContext(ctx, "platform: migrate headscale policy mode", "platform_service", row.ID.String(), "err", err)
		}
	}
	return nil
}

func (s *Service) migrateOne(ctx context.Context, row db.PlatformService) error {
	sys := sysCtx(ctx, row.WorkspaceID)
	svc, err := s.primary(ctx, row)
	if err != nil {
		return err
	}
	cfg := services.DecodeConfig(svc.Config)
	if !needsPolicyMigration(cfg) {
		if svc.Generation > svc.AppliedGeneration && s.pausedByMigration(ctx, row.WorkspaceID) {
			// The configuration was staged but the redeploy never started (the
			// restart failed after it): the pause must not be lifted while Headscale
			// still runs the old policy mode.
			if _, err := s.restart(sys, row, StepDeploy, "Migrating Headscale to the database policy mode"); err != nil {
				return s.keepPaused(ctx, row, fmt.Sprintf("the redeploy could not be started: %s", shorten(err)))
			}
			return nil
		}
		// A finished migration whose pause was not lifted (a crash between the wiring
		// and the unpause).
		s.afterHeadscaleWire(ctx, row.WorkspaceID, nil)
		return nil
	}
	if err := s.q.EnsureMeshSettings(ctx, row.WorkspaceID); err != nil {
		return err
	}
	if err := s.q.SetMeshPaused(ctx, db.SetMeshPausedParams{WorkspaceID: row.WorkspaceID, Paused: true, PausedReason: MigrationPausedReason}); err != nil {
		return err
	}
	s.publishMesh(row.WorkspaceID)

	files := make([]services.FileMount, 0, len(cfg.Files))
	for _, f := range cfg.Files {
		switch f.Path {
		case headscalePolicyFile:
			continue
		case headscaleConfigFile:
			f.Content = headscaleConfig(row.PublicUrl)
		}
		files = append(files, f)
	}
	cfg.Files = files
	if _, err := s.Services.Update(sys, svc.ID, svc.Generation, services.UpdateInput{Config: &cfg}); err != nil {
		return s.keepPaused(ctx, row, fmt.Sprintf("the new configuration was refused: %s", shorten(err)))
	}
	if _, err := s.restart(sys, row, StepDeploy, "Migrating Headscale to the database policy mode"); err != nil {
		return s.keepPaused(ctx, row, fmt.Sprintf("the redeploy could not be started: %s", shorten(err)))
	}
	return nil
}

// keepPaused records why the migration stopped and keeps the mesh paused.
func (s *Service) keepPaused(ctx context.Context, row db.PlatformService, why string) error {
	reason := MigrationPausedReason + " (failed: " + why + "). Retry it under Team settings → Private network (Headscale)."
	if err := s.q.SetMeshPaused(ctx, db.SetMeshPausedParams{WorkspaceID: row.WorkspaceID, Paused: true, PausedReason: reason}); err != nil {
		return err
	}
	s.publishMesh(row.WorkspaceID)
	return errors.New(why)
}

// pausedByMigration reports whether the mesh of ws is paused by this migration
// (not by an operator, whose pause is never lifted here).
func (s *Service) pausedByMigration(ctx context.Context, ws uuid.UUID) bool {
	set, err := s.q.GetMeshSettings(ctx, ws)
	return err == nil && set.Paused && strings.HasPrefix(set.PausedReason, MigrationPausedReason)
}

// afterHeadscaleWire lifts the migration pause once the Headscale wiring
// succeeded (the policy, including the mesh rules, is written by then) and keeps
// it, with the failure in the reason, when the wiring failed for good.
func (s *Service) afterHeadscaleWire(ctx context.Context, ws uuid.UUID, werr error) {
	if !s.pausedByMigration(ctx, ws) {
		return
	}
	var fe *fatalError
	switch {
	case werr == nil:
		if err := s.q.SetMeshPaused(ctx, db.SetMeshPausedParams{WorkspaceID: ws, Paused: false, PausedReason: ""}); err != nil {
			s.Logger.WarnContext(ctx, "platform: lift the mesh pause", "err", err)
			return
		}
	case errors.As(werr, &fe):
		reason := MigrationPausedReason + " (failed: " + fe.msg + "). Retry it under Team settings → Private network (Headscale)."
		if err := s.q.SetMeshPaused(ctx, db.SetMeshPausedParams{WorkspaceID: ws, Paused: true, PausedReason: reason}); err != nil {
			return
		}
	default:
		return // retrying: the pause stays as it is
	}
	s.publishMesh(ws)
}

func (s *Service) publishMesh(ws uuid.UUID) {
	if s.Bus != nil {
		s.Bus.Publish(events.Event{Type: "mesh.updated", ResourceID: ws.String()})
	}
}

// defaultMeshTag is the tag every mesh node of a team carries by default
// (mesh.DefaultMeshTag; platform does not import the mesh package).
const defaultMeshTag = "tag:nebula-mesh"

// meshTag is the team's mesh tag.
func (s *Service) meshTag(ctx context.Context, ws uuid.UUID) string {
	if set, err := s.q.GetMeshSettings(ctx, ws); err == nil && set.MeshTag != "" {
		return set.MeshTag
	}
	return defaultMeshTag
}
