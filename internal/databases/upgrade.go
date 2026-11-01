package databases

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// UpgradeResult is the response of an upgrade request.
type UpgradeResult struct {
	Plan engines.UpgradePlan `json:"plan"`
	// Status is "staged" (config changed, deploy to apply) or "pending" (the
	// agent is converting the data; the change is staged when it finishes).
	Status    string     `json:"status" enum:"staged,pending"`
	UpgradeID *uuid.UUID `json:"upgradeId,omitempty"`
	CommandID *uuid.UUID `json:"commandId,omitempty"`
}

// PlanUpgrade plans moving the database to targetVersion and lists what blocks it.
func (s *Service) PlanUpgrade(ctx context.Context, serviceID uuid.UUID, targetVersion string) (engines.UpgradePlan, error) {
	sc, spec, _, err := s.managed(ctx, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return engines.UpgradePlan{}, err
	}
	return s.planUpgrade(ctx, s.q, sc, spec, targetVersion)
}

func (s *Service) planUpgrade(ctx context.Context, q *db.Queries, sc services.Scope, spec engines.Spec, target string) (engines.UpgradePlan, error) {
	p := engines.PlanUpgrade(spec, target)
	if p.Method == engines.MethodPGUpgrade && sc.Service.CurrentDeploymentID == nil {
		// Nothing to convert yet: a database that never ran just changes image.
		p.Method = engines.MethodRedeploy
		p.HelperImage = ""
		p.Downtime = "none: the database has not been deployed yet"
		p.Steps = []string{"Change the image to " + p.Image + " as a staged change.", "Deploy it."}
		p.Warnings = nil
	}
	if p.Kind == "invalid" || p.Kind == "downgrade" {
		return p, nil
	}
	if _, err := q.GetPendingDatabaseUpgrade(ctx, sc.Service.ID); err == nil {
		p.Blockers = append(p.Blockers, "A version upgrade is already in progress.")
	} else if !isNoRows(err) {
		return p, err
	}
	if _, err := q.GetPendingDatabaseRotation(ctx, sc.Service.ID); err == nil {
		p.Blockers = append(p.Blockers, "A credential rotation is in progress.")
	} else if !isNoRows(err) {
		return p, err
	}
	if n, err := q.CountActiveDeployments(ctx, sc.Service.ID); err != nil {
		return p, err
	} else if n > 0 {
		p.Blockers = append(p.Blockers, "A deployment of this database is in progress.")
	}
	if msg, err := s.upgradeReplicaBlocker(ctx, q, sc, spec); err != nil {
		return p, err
	} else if msg != "" {
		p.Blockers = append(p.Blockers, msg)
	}
	if sc.Service.Generation > sc.Service.AppliedGeneration && sc.Service.CurrentDeploymentID != nil && p.Method == engines.MethodPGUpgrade {
		p.Warnings = append(p.Warnings, "The database has staged changes; they are deployed together with the new version.")
	}
	return p, nil
}

// Upgrade changes the database's version. In-place methods are staged: the
// image changes and the next deploy applies it. pg_upgrade stops the database,
// converts its data into a new volume through the agent and then stages the new
// version on that volume.
func (s *Service) Upgrade(ctx context.Context, serviceID uuid.UUID, targetVersion, confirm string) (UpgradeResult, error) {
	sc, spec, _, err := s.managed(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return UpgradeResult{}, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &sc.ProjectID); err != nil {
		return UpgradeResult{}, err
	}
	plan, err := s.planUpgrade(ctx, s.q, sc, spec, targetVersion)
	if err != nil {
		return UpgradeResult{}, err
	}
	if msg, err := s.upgradeReplicaBlocker(ctx, s.q, sc, spec); err != nil {
		return UpgradeResult{}, err
	} else if msg != "" && plan.Kind == "upgrade" {
		return UpgradeResult{}, apierr.Conflict(CodeReplicasExist, msg).WithDetails(map[string]any{"blockers": plan.Blockers})
	}
	if len(plan.Blockers) > 0 {
		return UpgradeResult{}, apierr.Conflict("upgrade_blocked", plan.Blockers[0]).WithDetails(map[string]any{"blockers": plan.Blockers})
	}
	if !plan.Executable {
		return UpgradeResult{}, apierr.Unprocessable("upgrade_not_automatic",
			"this upgrade is done by restoring a backup into a new database; follow the plan's steps").
			WithDetails(map[string]any{"method": plan.Method, "steps": plan.Steps})
	}
	if confirm != sc.Service.Slug {
		return UpgradeResult{}, apierr.Unprocessable("confirm_mismatch", "confirm must equal the service slug").
			WithDetails(map[string]any{"slug": sc.Service.Slug})
	}
	effects := []string{fmt.Sprintf("Upgrades %s from %s to %s (method: %s).", sc.Service.Name, spec.Version, targetVersion, plan.Method)}
	if plan.Method == engines.MethodPGUpgrade {
		effects = append(effects, "Stops the database while its data is converted into a new volume.")
	} else {
		effects = append(effects, "Stages the new version; it runs with the next deployment.")
	}
	if err := sc.Guard(ctx, "upgrade_database",
		fmt.Sprintf("Upgrade the production database %s to %s (%s / %s)", sc.Service.Name, targetVersion, sc.ProjectName, sc.EnvName),
		false, effects, targetVersion); err != nil {
		return UpgradeResult{}, err
	}

	res := UpgradeResult{Plan: plan}
	var serverID uuid.UUID
	if plan.Method == engines.MethodPGUpgrade {
		if serverID, err = s.currentServer(ctx, s.q, sc); err != nil {
			return UpgradeResult{}, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		meta := map[string]any{"service": sc.Service.Slug, "from": spec.Version, "to": targetVersion, "method": plan.Method}
		switch plan.Method {
		case engines.MethodRedeploy:
			if err := s.stageVersion(ctx, q, sc, targetVersion, "", ""); err != nil {
				return err
			}
			res.Status = "staged"
		case engines.MethodPGUpgrade:
			oldVol, err := s.dataVolumeName(ctx, q, sc.Service.ID, spec)
			if err != nil {
				return err
			}
			newVol, err := s.freeVolumeName(ctx, q, sc.Service.ID, fmt.Sprintf("data-pg%d", engines.Major(targetVersion)))
			if err != nil {
				return err
			}
			up, err := q.InsertDatabaseUpgrade(ctx, db.InsertDatabaseUpgradeParams{
				ServiceID: sc.Service.ID, Method: plan.Method, FromVersion: spec.Version, ToVersion: targetVersion,
				OldVolume: oldVol, NewVolume: newVol, ActorLabel: actorLabel(ctx),
			})
			if err != nil {
				return err
			}
			raw := mustJSON(agentprotocol.DBUpgradePayload{
				ServiceID: sc.Service.ID.String(), UpgradeID: up.ID.String(), FromVersion: spec.Version, ToVersion: targetVersion,
				HelperImage: plan.HelperImage, OldVolume: oldVol, NewVolume: newVol, StopGraceSeconds: sc.Config().StopGraceSeconds,
			})
			cmdID, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindDBUpgrade, Payload: raw})
			if err != nil {
				return fmt.Errorf("enqueue db_upgrade: %w", err)
			}
			if _, err := tx.Exec(ctx, "UPDATE database_upgrades SET command_id = $2 WHERE id = $1", up.ID, cmdID); err != nil {
				return err
			}
			// The agent stops the database; keep the reconciler from restarting it.
			if err := q.SetServiceDesiredState(ctx, db.SetServiceDesiredStateParams{ID: sc.Service.ID, DesiredState: services.DesiredStopped}); err != nil {
				return err
			}
			res.Status, res.UpgradeID, res.CommandID = "pending", &up.ID, &cmdID
			meta["upgradeId"] = up.ID.String()
			meta["newVolume"] = newVol
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "database.upgrade", ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID, Metadata: meta,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return UpgradeResult{}, err
		}
		return UpgradeResult{}, fmt.Errorf("upgrade database: %w", err)
	}
	s.publish(sc)
	return res, nil
}

// stageVersion changes the database's version (and, when volume is set, moves
// its data volume) as a staged change.
func (s *Service) stageVersion(ctx context.Context, q *db.Queries, sc services.Scope, version, oldVolume, newVolume string) error {
	cfg := sc.Config()
	spec := *cfg.Database
	spec.Version = version
	cfg.Database = &spec
	cfg = cfg.WithDefaults()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if newVolume != "" {
		_, mount := spec.DataMount()
		if err := q.SetServiceVolumeMount(ctx, db.SetServiceVolumeMountParams{ServiceID: sc.Service.ID, Name: oldVolume, Name_2: newVolume, MountPath: mount}); err != nil {
			return err
		}
	}
	_, err = q.SetServiceConfigBumped(ctx, db.SetServiceConfigBumpedParams{ID: sc.Service.ID, Config: raw})
	return err
}

// dataVolumeName finds the volume that holds the database's data directory.
func (s *Service) dataVolumeName(ctx context.Context, q *db.Queries, serviceID uuid.UUID, spec engines.Spec) (string, error) {
	vols, err := q.ListServiceVolumes(ctx, serviceID)
	if err != nil {
		return "", err
	}
	_, mount := spec.DataMount()
	for _, v := range vols {
		if v.MountPath == mount {
			return v.Name, nil
		}
	}
	return "", apierr.Conflict("data_volume_missing", "the database has no data volume at "+mount)
}

func (s *Service) freeVolumeName(ctx context.Context, q *db.Queries, serviceID uuid.UUID, base string) (string, error) {
	name := base
	for i := 2; i < 50; i++ {
		if _, err := q.GetServiceVolumeByName(ctx, db.GetServiceVolumeByNameParams{ServiceID: serviceID, Name: name}); isNoRows(err) {
			return name, nil
		} else if err != nil {
			return "", err
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return "", apierr.Conflict("volume_name_taken", "no free volume name for the upgraded data")
}

// applyUpgradeResult finishes a pg_upgrade after the agent answered.
func (s *Service) applyUpgradeResult(ctx context.Context, upgradeID uuid.UUID, res agentprotocol.ResultFrame) error {
	up, err := s.q.GetDatabaseUpgrade(ctx, upgradeID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, up.ServiceID)
	if apierr.IsNotFound(err) {
		return nil // database deleted meanwhile
	}
	if err != nil {
		return err
	}
	changed := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if res.Outcome != agentprotocol.OutcomeSucceeded {
			msg := res.Error
			if msg == "" {
				msg = "the upgrade failed"
			}
			if _, err := q.FinishDatabaseUpgrade(ctx, db.FinishDatabaseUpgradeParams{ID: upgradeID, Status: "failed", Error: msg}); err != nil {
				if isNoRows(err) {
					return nil
				}
				return err
			}
			// The agent restarted the old database.
			if err := q.SetServiceDesiredState(ctx, db.SetServiceDesiredStateParams{ID: sc.Service.ID, DesiredState: services.DesiredRunning}); err != nil {
				return err
			}
			changed = true
			return audit.Record(ctx, tx, audit.Entry{
				Action: "database.upgrade.failed", ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID,
				Metadata: map[string]any{"service": sc.Service.Slug, "upgradeId": upgradeID.String(), "error": firstLine(msg)},
			})
		}
		if _, err := q.FinishDatabaseUpgrade(ctx, db.FinishDatabaseUpgradeParams{ID: upgradeID, Status: "ready"}); err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		if err := s.stageVersion(ctx, q, sc, up.ToVersion, up.OldVolume, up.NewVolume); err != nil {
			return err
		}
		changed = true
		return audit.Record(ctx, tx, audit.Entry{
			Action: "database.upgrade.ready", ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "upgradeId": upgradeID.String(), "to": up.ToVersion, "newVolume": up.NewVolume, "oldVolume": up.OldVolume, "requestedBy": up.ActorLabel},
		})
	})
	if err != nil {
		return fmt.Errorf("apply upgrade result: %w", err)
	}
	if changed {
		s.publish(sc)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
