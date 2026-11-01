package databases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/secretgen"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Rotation modes.
const (
	// ModeExec changes the password inside the running database through the
	// agent, then updates the variable.
	ModeExec = "exec"
	// ModeRedeploy updates the variable only; the engine reads it on every
	// start, so the database is restaged and redeployed.
	ModeRedeploy = "redeploy"
	// ModeVariable is used for a database that was never deployed: there is no
	// data directory yet, so the new password simply becomes the initial one.
	ModeVariable = "variable"
)

// ConsumerRef is a service that references the database.
type ConsumerRef struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
}

// RotationPlan is what rotating the credentials will do.
type RotationPlan struct {
	Mode        string        `json:"mode" enum:"exec,redeploy,variable"`
	PasswordKey string        `json:"passwordKey" doc:"The variable that holds the rotated password"`
	Consumers   []ConsumerRef `json:"consumers" doc:"Services that reference this database; they are staged and need a deploy"`
	// DatabaseRestart is true when the database itself is restaged and restarts on deploy.
	DatabaseRestart bool     `json:"databaseRestart"`
	Warnings        []string `json:"warnings"`
	Blockers        []string `json:"blockers"`
}

// RotationResult is the response of a rotation request.
type RotationResult struct {
	Plan       RotationPlan `json:"plan"`
	DryRun     bool         `json:"dryRun"`
	Status     string       `json:"status" enum:"planned,pending,applied"`
	RotationID *uuid.UUID   `json:"rotationId,omitempty"`
	CommandID  *uuid.UUID   `json:"commandId,omitempty"`
}

func (s *Service) planRotation(ctx context.Context, q *db.Queries, sc services.Scope, eng *engines.Engine) (RotationPlan, error) {
	p := RotationPlan{PasswordKey: eng.PasswordKey, Consumers: []ConsumerRef{}, Warnings: []string{}, Blockers: []string{}}
	refs, err := s.referrers(ctx, q, sc)
	if err != nil {
		return p, err
	}
	for _, r := range refs {
		p.Consumers = append(p.Consumers, ConsumerRef{ServiceID: r.ID, Slug: r.Slug, Name: r.Name})
	}
	switch {
	case sc.Service.CurrentDeploymentID == nil:
		p.Mode = ModeVariable
		p.Warnings = append(p.Warnings, "The database has not been deployed yet, so the new password simply becomes its initial password.")
	case eng.Rotation == engines.RotateRedeploy:
		p.Mode = ModeRedeploy
		p.DatabaseRestart = true
		p.Warnings = append(p.Warnings, eng.Label+" reads its password when it starts: the database is restaged and restarts briefly on the next deploy, together with its consumers.")
	default:
		p.Mode = ModeExec
		p.Warnings = append(p.Warnings,
			"The password is changed inside the running database right away. The old password stops working immediately, so services using it fail to connect until they are redeployed: deploy the staged changes straight after.")
		srvID, err := s.currentServer(ctx, q, sc)
		if err != nil {
			return p, err
		}
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: srvID})
		if err != nil {
			return p, fmt.Errorf("load server: %w", err)
		}
		if srv.Status != "online" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s is %s: the rotation waits until the agent is back.", srv.Name, srv.Status))
		}
	}
	if len(refs) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d service(s) reference this database and will be staged for a redeploy.", len(refs)))
	}
	p.Warnings = append(p.Warnings, "Services that copied the password as plain text (not as ${{"+sc.Service.Slug+"."+engines.PrimaryExport(eng.ID)+"}}) must be updated by hand.")

	if _, err := q.GetPendingDatabaseRotation(ctx, sc.Service.ID); err == nil {
		p.Blockers = append(p.Blockers, "A credential rotation is already in progress.")
	} else if !isNoRows(err) {
		return p, err
	}
	if _, err := q.GetPendingDatabaseUpgrade(ctx, sc.Service.ID); err == nil {
		p.Blockers = append(p.Blockers, "A version upgrade is in progress.")
	} else if !isNoRows(err) {
		return p, err
	}
	if n, err := q.CountActiveDeployments(ctx, sc.Service.ID); err != nil {
		return p, err
	} else if n > 0 {
		p.Blockers = append(p.Blockers, "A deployment of this database is in progress.")
	}
	return p, nil
}

// RotateCredentials changes the application password of a managed database.
// With dryRun it only returns the plan. Otherwise: engines that keep their
// password in the data directory (PostgreSQL, MySQL, MariaDB, MongoDB,
// ClickHouse) get an `ALTER ROLE`-style exec through the agent, and the variable
// changes when the agent reports success; engines that read it at start get the
// variable changed and are restaged. Every service that references the database
// is staged either way. Nothing is deployed.
func (s *Service) RotateCredentials(ctx context.Context, serviceID uuid.UUID, dryRun bool) (RotationResult, error) {
	sc, spec, eng, err := s.managed(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return RotationResult{}, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &sc.ProjectID); err != nil {
		return RotationResult{}, err
	}
	if spec.Replica != nil {
		return RotationResult{}, apierr.Conflict(CodeReplicaReadOnly, "this database is a read replica: its credentials are the primary's; rotate them on the primary")
	}
	plan, err := s.planRotation(ctx, s.q, sc, eng)
	if err != nil {
		return RotationResult{}, err
	}
	if dryRun {
		return RotationResult{Plan: plan, DryRun: true, Status: "planned"}, nil
	}
	if len(plan.Blockers) > 0 {
		return RotationResult{}, apierr.Conflict("rotation_blocked", plan.Blockers[0]).WithDetails(map[string]any{"blockers": plan.Blockers})
	}
	effects := []string{"Generates a new application password for " + sc.Service.Name + "; the old one stops working."}
	if plan.Mode == ModeExec {
		effects = append(effects, "Changes the password inside the running database immediately.")
	}
	effects = append(effects, fmt.Sprintf("Stages the new password on %d service(s) that reference it; they use it after their next deployment.", len(plan.Consumers)))
	if err := sc.Guard(ctx, "rotate_database_credentials",
		fmt.Sprintf("Rotate the credentials of the production database %s (%s / %s)", sc.Service.Name, sc.ProjectName, sc.EnvName),
		false, effects); err != nil {
		return RotationResult{}, err
	}
	pw, err := secretgen.Secret(passwordLength, secretgen.AlphaAlnum)
	if err != nil {
		return RotationResult{}, err
	}
	res := RotationResult{Plan: plan}
	var staged []db.Service
	var serverID uuid.UUID
	if plan.Mode == ModeExec {
		if serverID, err = s.currentServer(ctx, s.q, sc); err != nil {
			return RotationResult{}, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		meta := map[string]any{"service": sc.Service.Slug, "mode": plan.Mode, "key": eng.PasswordKey}
		switch plan.Mode {
		case ModeExec:
			rotID := uuid.New()
			enc, err := s.box.SealString(secretbox.DatabaseRotationsNewPassword, rotID.String(), pw)
			if err != nil {
				return err
			}
			rot, err := q.InsertDatabaseRotation(ctx, db.InsertDatabaseRotationParams{
				ID: rotID, ServiceID: serviceID, Mode: plan.Mode, PasswordKey: eng.PasswordKey, NewPasswordEnc: enc, ActorLabel: actorLabel(ctx),
			})
			if err != nil {
				return err
			}
			raw := mustJSON(agentprotocol.DBAdminPayload{
				ServiceID: serviceID.String(), Action: agentprotocol.DBActionRotatePassword, RotationID: rot.ID.String(),
				Engine: spec.Engine, Version: spec.Version, Flavor: spec.Flavor, Database: spec.DatabaseName, User: spec.Username,
			})
			cmdID, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindDBAdmin, Payload: raw})
			if err != nil {
				return fmt.Errorf("enqueue db_admin: %w", err)
			}
			if err := q.SetDatabaseRotationCommand(ctx, db.SetDatabaseRotationCommandParams{ID: rot.ID, CommandID: &cmdID}); err != nil {
				return err
			}
			res.RotationID, res.CommandID, res.Status = &rot.ID, &cmdID, "pending"
			meta["rotationId"] = rot.ID.String()
		default:
			if _, err := services.WriteServiceVariable(ctx, q, s.box, sc, services.VarWrite{
				Key: eng.PasswordKey, Value: pw, Secret: true, Source: services.SourceGenerated, Op: services.OpRotate,
			}); err != nil {
				return err
			}
			if _, err := q.BumpServiceGeneration(ctx, serviceID); err != nil {
				return err
			}
			if staged, err = s.stageReferrers(ctx, q, sc); err != nil {
				return err
			}
			res.Status = "applied"
		}
		meta["consumers"] = len(plan.Consumers)
		return audit.Record(ctx, tx, audit.Entry{
			Action: "database.rotate_credentials", ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID, Metadata: meta,
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return RotationResult{}, err
		}
		return RotationResult{}, fmt.Errorf("rotate credentials: %w", err)
	}
	s.publish(sc)
	s.publishReferrers(sc, staged)
	return res, nil
}

func actorLabel(ctx context.Context) string {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.Label != "" {
		return p.Label
	}
	return "system"
}

// applyRotationResult finishes a rotation after the agent answered.
func (s *Service) applyRotationResult(ctx context.Context, rotationID uuid.UUID, res agentprotocol.ResultFrame) error {
	rot, err := s.q.GetDatabaseRotation(ctx, rotationID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, rot.ServiceID)
	if apierr.IsNotFound(err) {
		return nil // database deleted meanwhile
	}
	if err != nil {
		return err
	}
	var staged []db.Service
	changed := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if res.Outcome != agentprotocol.OutcomeSucceeded {
			msg := res.Error
			if msg == "" {
				msg = "the agent could not change the password"
			}
			if _, err := q.FinishDatabaseRotation(ctx, db.FinishDatabaseRotationParams{ID: rotationID, Status: "failed", Error: msg}); err != nil {
				if isNoRows(err) {
					return nil // already finished
				}
				return err
			}
			changed = true
			return audit.Record(ctx, tx, audit.Entry{
				Action: "database.rotate_credentials.failed", ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID,
				Metadata: map[string]any{"service": sc.Service.Slug, "rotationId": rotationID.String(), "error": msg},
			})
		}
		if _, err := q.FinishDatabaseRotation(ctx, db.FinishDatabaseRotationParams{ID: rotationID, Status: "applied"}); err != nil {
			if isNoRows(err) {
				return nil // duplicate result
			}
			return err
		}
		pw, err := s.box.OpenString(secretbox.DatabaseRotationsNewPassword, rot.ID.String(), rot.NewPasswordEnc)
		if err != nil {
			return fmt.Errorf("decrypt rotation password: %w", err)
		}
		// The database already runs with the new password; record it. The
		// database itself is not restaged: its image only reads the variable
		// when it initialises an empty data directory.
		if _, err := services.WriteServiceVariable(ctx, q, s.box, sc, services.VarWrite{
			Key: rot.PasswordKey, Value: pw, Secret: true, Source: services.SourceGenerated, Op: services.OpRotate,
		}); err != nil {
			return err
		}
		if staged, err = s.stageReferrers(ctx, q, sc); err != nil {
			return err
		}
		changed = true
		return audit.Record(ctx, tx, audit.Entry{
			Action: "database.rotate_credentials.applied", ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": sc.Service.Slug, "rotationId": rotationID.String(), "key": rot.PasswordKey, "consumers": len(staged), "requestedBy": rot.ActorLabel},
		})
	})
	if err != nil {
		return fmt.Errorf("apply rotation result: %w", err)
	}
	if changed {
		s.publish(sc)
		s.publishReferrers(sc, staged)
	}
	return nil
}

// errNoRotationID is returned for a db_admin payload without a rotation.
var errNoRotationID = errors.New("db_admin rotate payload has no rotation id")

func rotationIDOf(payload json.RawMessage) (uuid.UUID, error) {
	var p agentprotocol.DBAdminPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return uuid.Nil, err
	}
	if p.RotationID == "" {
		return uuid.Nil, errNoRotationID
	}
	return uuid.Parse(p.RotationID)
}
