package databases

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretgen"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Rebuild steps (database_replicas.step names; the cycle number follows '#').
const (
	stepRebuildStop = "rebuild_stop"
	stepRebuildDrop = "rebuild_drop"
)

// RebuildInput is a rebuild request.
type RebuildInput struct {
	// FollowServiceID is the primary a fenced old primary is rebuilt to follow;
	// empty means the replica that was promoted over it.
	FollowServiceID *uuid.UUID
}

// RebuildResult is the response of a rebuild.
type RebuildResult struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Status    string    `json:"status"`
}

// RebuildReplica re-clones a replica (broken, needs a rebuild, failed, or just
// wanted fresh) from its primary, or turns a fenced old primary into a replica
// of the new primary. Either way the data goes to a new volume; the old volume
// is kept (listed as a retired volume) until it is deleted. The saga runs in
// ReplicaAdvanceJob: stop, drop the old slot, prepare, clone, deploy.
func (s *Service) RebuildReplica(ctx context.Context, serviceID uuid.UUID, in RebuildInput) (RebuildResult, error) {
	sc, spec, eng, err := s.managed(ctx, policy.ScopeProjectsWrite, serviceID)
	if err != nil {
		return RebuildResult{}, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &sc.ProjectID); err != nil {
		return RebuildResult{}, err
	}
	if spec.Engine != engines.Postgres {
		return RebuildResult{}, apierr.Unprocessable(CodeReplicaWrongEngine, "read replicas are only available for PostgreSQL")
	}
	r, isReplica, err := s.replicaRow(ctx, s.q, serviceID)
	if err != nil {
		return RebuildResult{}, err
	}
	fenced := s.hasFence(ctx, s.q, serviceID)
	var follow services.Scope
	switch {
	case isReplica:
		switch r.State {
		case ReplicaPending, ReplicaPreparing, ReplicaCloning, ReplicaDeploying, ReplicaRemoving, ReplicaPromoting:
			return RebuildResult{}, apierr.Conflict(CodeReplicaNotRebuildable, "the replica is "+r.State+"; wait for it to settle first")
		}
		if follow, err = services.LoadScopeInternal(ctx, s.q, r.PrimaryServiceID); err != nil {
			return RebuildResult{}, err
		}
	case fenced:
		id := valueOr(in.FollowServiceID)
		if id == uuid.Nil {
			p, err := s.q.GetLatestDatabasePromotionOfService(ctx, serviceID)
			if err != nil || p.OldPrimaryServiceID != serviceID || p.Status != "succeeded" {
				return RebuildResult{}, apierr.Unprocessable("follow_required", "name the new primary this database should follow (followServiceId)")
			}
			id = p.ReplicaServiceID
		}
		if follow, err = services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, id); err != nil {
			return RebuildResult{}, err
		}
		if follow.EnvironmentID != sc.EnvironmentID {
			return RebuildResult{}, apierr.Unprocessable("follow_other_environment", "a replica follows a primary of its own environment")
		}
	default:
		return RebuildResult{}, apierr.Conflict(CodeReplicaNotRebuildable, "only a replica or a fenced old primary can be rebuilt")
	}
	pspec := follow.Config().Database
	if pspec == nil || pspec.Engine != engines.Postgres {
		return RebuildResult{}, apierr.Unprocessable(CodeReplicaWrongEngine, "the primary is not a PostgreSQL database")
	}
	if pspec.Replica != nil {
		return RebuildResult{}, apierr.Conflict(CodeReplicaNotPrimary, follow.Service.Slug+" is itself a replica")
	}
	if s.hasFence(ctx, s.q, follow.Service.ID) {
		return RebuildResult{}, apierr.Conflict(CodeDatabaseFenced, follow.Service.Slug+" is a fenced old primary")
	}
	if busy, err := s.promotionRunningIn(ctx, s.q, follow.Service.ID); err != nil {
		return RebuildResult{}, err
	} else if busy {
		return RebuildResult{}, apierr.Conflict(CodePromoteInProgress, "a promotion is running in this replica group")
	}
	if ok, why := s.serviceRunning(follow.Service); !ok {
		return RebuildResult{}, apierr.Conflict(CodePrimaryNotRunning, "the primary must be running: "+why)
	}
	psrv, err := s.currentServer(ctx, s.q, follow)
	if err != nil {
		return RebuildResult{}, err
	}
	if sc.Service.ServerID == nil {
		return RebuildResult{}, apierr.Conflict("no_server", "the database has no server")
	}
	target, err := s.loadServer(ctx, s.q, sc.WorkspaceID, *sc.Service.ServerID)
	if err != nil {
		return RebuildResult{}, fmt.Errorf("load server: %w", err)
	}
	if target.Status != "online" {
		return RebuildResult{}, apierr.Conflict("server_offline", "server "+target.Name+" is "+target.Status+"; the rebuild was not started")
	}
	if !isReplica {
		if n, err := s.q.CountDatabaseReplicasOfPrimary(ctx, follow.Service.ID); err != nil {
			return RebuildResult{}, err
		} else if n >= MaxReplicas {
			return RebuildResult{}, apierr.Conflict(CodeReplicasLimit, fmt.Sprintf("a primary can have at most %d read replicas", MaxReplicas))
		}
	}
	if *sc.Service.ServerID != psrv {
		if s.rep.Mesh == nil {
			return RebuildResult{}, apierr.Conflict("mesh_not_configured", "the server mesh is not available on this installation")
		}
		if lp, err := s.rep.Mesh.PlanLink(ctx, s.q, sc.EnvironmentID, *sc.Service.ServerID, psrv, agentprotocol.MeshPurposeReplication); err != nil {
			return RebuildResult{}, err
		} else if len(lp.Blockers) > 0 {
			return RebuildResult{}, problemError(Problem{Code: lp.Blockers[0].Code, Message: lp.Blockers[0].Message})
		}
	}

	cycle := 1
	if isReplica {
		_, c := parseStep(r.Step)
		cycle = c + 1
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.rebuildTx(ctx, tx, sc, *pspec, eng, follow, psrv, isReplica, cycle)
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return RebuildResult{}, err
		}
		return RebuildResult{}, fmt.Errorf("rebuild replica: %w", err)
	}
	s.publish(sc)
	services.Publish(s.bus, EventDatabaseUpdated, follow.ProjectID, follow.EnvironmentID, follow.Service.ID)
	return RebuildResult{ServiceID: serviceID, Status: ReplicaPending}, nil
}

// hasFence reports whether a service is a fenced old primary.
func (s *Service) hasFence(ctx context.Context, q *db.Queries, id uuid.UUID) bool {
	_, err := q.GetDatabaseFence(ctx, id)
	return err == nil
}

// rebuildTx prepares the rebuild: a new empty data volume, the replica config
// and credentials, the mesh route, the lifecycle row, and the saga job.
func (s *Service) rebuildTx(ctx context.Context, tx pgx.Tx, sc services.Scope, pspec engines.Spec, eng *engines.Engine, follow services.Scope, psrv uuid.UUID, isReplica bool, cycle int) error {
	q := db.New(tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "replicas:"+follow.Service.ID.String()); err != nil {
		return err
	}
	cur := sc.Config()
	spec := *cur.Database
	oldVol, err := s.dataVolumeName(ctx, q, sc.Service.ID, spec)
	if err != nil {
		return err
	}
	vrow, err := q.GetServiceVolumeByName(ctx, db.GetServiceVolumeByNameParams{ServiceID: sc.Service.ID, Name: oldVol})
	if err != nil {
		return err
	}
	newVol, err := s.freeVolumeName(ctx, q, sc.Service.ID, "data-rebuild")
	if err != nil {
		return err
	}
	_, mount := spec.DataMount()
	if err := q.InsertRetiredVolume(ctx, db.InsertRetiredVolumeParams{
		ServiceID: sc.Service.ID, VolumeName: oldVol, DockerName: services.DockerVolumeName(vrow), Reason: "replica rebuild",
	}); err != nil {
		return err
	}
	if err := q.SetServiceVolumeMount(ctx, db.SetServiceVolumeMountParams{ServiceID: sc.Service.ID, Name: oldVol, Name_2: newVol, MountPath: mount}); err != nil {
		return err
	}
	if err := q.SetServiceDesiredState(ctx, db.SetServiceDesiredStateParams{ID: sc.Service.ID, DesiredState: services.DesiredStopped}); err != nil {
		return err
	}

	if s.rep.Mesh != nil {
		if err := s.rep.Mesh.Release(ctx, tx, sc.Service.ID); err != nil {
			return fmt.Errorf("release old replication route: %w", err)
		}
	}
	rt, err := s.replicationRoute(ctx, tx, follow, pspec, psrv, valueOr(sc.Service.ServerID), sc.Service.ID)
	if err != nil {
		return err
	}
	slot, role := replicaSlot(sc.Service.ID), replicaRole(sc.Service.ID)
	spec.Replica = &engines.ReplicaSpec{PrimaryServiceID: follow.Service.ID.String(), Host: rt.host, Port: rt.port, Slot: slot, User: role}
	spec.Version, spec.Flavor = pspec.Version, pspec.Flavor
	cur.Database = &spec
	cur.BlockEgress = true
	if _, err := q.SetServiceConfigBumped(ctx, db.SetServiceConfigBumpedParams{ID: sc.Service.ID, Config: mustJSON(cur.WithDefaults())}); err != nil {
		return err
	}
	// Credentials follow the new primary by reference (a fenced old primary had
	// literal copies); the replication password is generated once.
	ref := func(key string) string { return "${{" + follow.Service.Slug + "." + key + "}}" }
	if err := s.writeReplicaVars(ctx, q, sc, eng, ref); err != nil {
		return err
	}
	if _, err := q.GetServiceVariable(ctx, db.GetServiceVariableParams{ServiceID: sc.Service.ID, Key: engines.ReplicationPasswordKey}); isNoRows(err) {
		pw, err := secretgen.Secret(passwordLength, secretgen.AlphaAlnum)
		if err != nil {
			return err
		}
		if _, err := services.WriteServiceVariable(ctx, q, s.box, sc, services.VarWrite{
			Key: engines.ReplicationPasswordKey, Value: pw, Secret: true, Source: services.SourceGenerated,
		}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	step := mkStep(stepRebuildStop, cycle)
	msg := "Rebuilding the replica."
	if isReplica {
		err = q.SetDatabaseReplicaPrimary(ctx, db.SetDatabaseReplicaPrimaryParams{
			ServiceID: sc.Service.ID, PrimaryServiceID: follow.Service.ID, State: ReplicaPending, Message: msg, Step: step,
		})
		if err != nil {
			return err
		}
		if err := q.SetDatabaseReplicaLinks(ctx, db.SetDatabaseReplicaLinksParams{ServiceID: sc.Service.ID, LinkID: rt.link, ExposureID: rt.exposure}); err != nil {
			return err
		}
	} else {
		var createdBy *uuid.UUID
		if u, ok := principalUser(ctx); ok {
			createdBy = &u
		}
		row, err := q.InsertDatabaseReplica(ctx, db.InsertDatabaseReplicaParams{
			ServiceID: sc.Service.ID, WorkspaceID: sc.WorkspaceID, EnvironmentID: sc.EnvironmentID, PrimaryServiceID: follow.Service.ID,
			SlotName: slot, ReplUser: role, LinkID: rt.link, ExposureID: rt.exposure, CreatedBy: createdBy,
		})
		if err != nil {
			return err
		}
		if err := q.SetDatabaseReplicaState(ctx, db.SetDatabaseReplicaStateParams{ServiceID: row.ServiceID, State: ReplicaPending, Message: msg, Step: step}); err != nil {
			return err
		}
	}
	if err := s.enqueueAdvance(ctx, tx, sc.Service.ID, 0); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{
		Action: ActionReplicaRebuild, ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{"service": sc.Service.Slug, "follows": follow.Service.Slug, "newVolume": newVol, "oldVolume": oldVol, "fromFenced": !isReplica},
	})
}

// advanceRebuild runs the first two steps of a rebuild: stop the old container,
// drop the slot and role a previous cycle left on the primary. It then hands
// over to the create saga at "prepare".
func (s *Service) advanceRebuild(ctx context.Context, r db.DatabaseReplica, name string, cycle int, rsc, psc services.Scope) (moved, done bool, err error) {
	tag := fmt.Sprintf("%s:%d", r.ServiceID, cycle)
	switch name {
	case stepRebuildStop:
		if rsc.Service.CurrentDeploymentID != nil && rsc.Service.ServerID != nil {
			if srv, err := s.loadServer(ctx, s.q, rsc.WorkspaceID, *rsc.Service.ServerID); err == nil && srv.Status == "online" {
				cmd, err := s.command(ctx, "repl_rb_stop:"+tag, srv.ID, agentprotocol.KindStopService, agentprotocol.ServiceActionPayload{
					ServiceID: rsc.Service.ID.String(), StopGraceSeconds: rsc.Config().StopGraceSeconds,
				})
				if err != nil {
					return false, false, err
				}
				if st, _, _ := outcome(cmd); st == cmdRunning {
					return false, false, s.keepState(ctx, r, ReplicaPending, "Stopping the old container.")
				}
			}
		}
		return true, false, s.transition(ctx, r, ReplicaPending, "Rebuilding the replica.", mkStep(stepRebuildDrop, cycle))
	case stepRebuildDrop:
		if pspec := psc.Config().Database; pspec != nil && psc.Service.CurrentDeploymentID != nil {
			if psrv, err := s.currentServer(ctx, s.q, psc); err == nil {
				cmd, err := s.replCommand(ctx, "repl_rb_drop:"+tag, psrv, psc, *pspec, agentprotocol.DBActionReplDrop,
					&agentprotocol.ReplAdmin{Role: r.ReplUser, Slot: r.SlotName})
				if err != nil {
					return false, false, err
				}
				if st, _, _ := outcome(cmd); st == cmdRunning {
					return false, false, nil
				}
			}
		}
		return true, false, s.transition(ctx, r, ReplicaPending, "Rebuilding the replica.", mkStep(stepPrepare, cycle))
	}
	return false, true, nil
}

// keepState rewrites the message of a replica without changing state or step.
func (s *Service) keepState(ctx context.Context, r db.DatabaseReplica, state, msg string) error {
	if r.State == state && r.Message == msg {
		return nil
	}
	return s.q.SetDatabaseReplicaState(ctx, db.SetDatabaseReplicaStateParams{ServiceID: r.ServiceID, State: state, Message: msg, Step: r.Step})
}
