package databases

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretgen"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// AddReplicaInput describes a new read replica.
type AddReplicaInput struct {
	// Name is the display name; empty means "<primary>-replica-<n>".
	Name     string
	ServerID uuid.UUID
	// DryRun only returns the plan.
	DryRun bool
	// AcceptMeshChanges confirms that the replica adds mesh devices (a link node).
	AcceptMeshChanges bool
}

// ReplicaPlanLink is a mesh link a plan would create or use.
type ReplicaPlanLink struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Purpose string `json:"purpose"`
}

// ReplicaPlan is what adding a replica on a server would do and what blocks it.
type ReplicaPlan struct {
	ServerID     uuid.UUID         `json:"serverId"`
	ServerName   string            `json:"serverName"`
	SameServer   bool              `json:"sameServer"`
	DevicesAdded int               `json:"devicesAdded"`
	Links        []ReplicaPlanLink `json:"links"`
	Blockers     []Problem         `json:"blockers"`
	Warnings     []Problem         `json:"warnings"`
}

// AddReplicaResult is the response of the add endpoint: the plan, and for a real
// request the new replica service.
type AddReplicaResult struct {
	DryRun    bool        `json:"dryRun"`
	Plan      ReplicaPlan `json:"plan"`
	ServiceID *uuid.UUID  `json:"serviceId,omitempty"`
	Status    string      `json:"status,omitempty"`
}

type addState struct {
	plan      ReplicaPlan
	primarySv uuid.UUID // server running the primary
	primary   db.Server
	target    db.Server
}

// planReplica checks everything adding a replica of psc on serverID depends on.
// It never writes.
func (s *Service) planReplica(ctx context.Context, q *db.Queries, psc services.Scope, spec engines.Spec, serverID uuid.UUID) (addState, error) {
	st := addState{plan: ReplicaPlan{ServerID: serverID, Links: []ReplicaPlanLink{}, Blockers: []Problem{}, Warnings: []Problem{}}}
	block := func(code, msg string) { st.plan.Blockers = append(st.plan.Blockers, Problem{Code: code, Message: msg}) }
	warn := func(code, msg string) { st.plan.Warnings = append(st.plan.Warnings, Problem{Code: code, Message: msg}) }

	switch {
	case spec.Engine != engines.Postgres:
		block(CodeReplicaWrongEngine, "read replicas are only available for PostgreSQL")
		return st, nil
	case spec.Replica != nil:
		block(CodeReplicaNotPrimary, "a read replica cannot have replicas of its own")
		return st, nil
	}
	if _, err := q.GetDatabaseFence(ctx, psc.Service.ID); err == nil {
		block(CodeDatabaseFenced, "this database is a fenced old primary: rebuild it as a replica of the new primary instead")
		return st, nil
	} else if !isNoRows(err) {
		return st, err
	}
	n, err := q.CountDatabaseReplicasOfPrimary(ctx, psc.Service.ID)
	if err != nil {
		return st, err
	}
	if n >= MaxReplicas {
		block(CodeReplicasLimit, fmt.Sprintf("a primary can have at most %d read replicas", MaxReplicas))
	}
	if busy, err := s.promotionRunningIn(ctx, q, psc.Service.ID); err != nil {
		return st, err
	} else if busy {
		block(CodePromoteInProgress, "a promotion is running in this replica group")
	}
	if _, err := q.GetPendingDatabaseUpgrade(ctx, psc.Service.ID); err == nil {
		block("upgrade_in_progress", "a version upgrade of the primary is in progress")
	} else if !isNoRows(err) {
		return st, err
	}
	if _, err := q.GetPendingDatabaseRotation(ctx, psc.Service.ID); err == nil {
		block("rotation_in_progress", "a credential rotation of the primary is in progress")
	} else if !isNoRows(err) {
		return st, err
	}
	if ok, why := s.serviceRunning(psc.Service); !ok {
		block(CodePrimaryNotRunning, "the primary must be running: "+why)
	}
	psrv, err := s.currentServer(ctx, q, psc)
	if err != nil {
		if ae, ok := apierr.As(err); ok {
			block(CodePrimaryNotRunning, ae.Message)
			return st, nil
		}
		return st, err
	}
	st.primarySv = psrv
	if st.primary, err = s.loadServer(ctx, q, psc.WorkspaceID, psrv); err != nil {
		return st, fmt.Errorf("load primary server: %w", err)
	}
	if st.primary.Status != "online" {
		block(CodePrimaryNotRunning, "the primary's server "+st.primary.Name+" is "+st.primary.Status)
	}
	if !hasCap(st.primary, agentprotocol.CapReplication) {
		block(CodeAgentTooOld, "the agent on "+st.primary.Name+" is too old for read replicas; update the agent first")
	}

	tsrv, err := s.loadServer(ctx, q, psc.WorkspaceID, serverID)
	if isNoRows(err) {
		block("server_not_found", "the selected server does not exist")
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("load server: %w", err)
	}
	st.target = tsrv
	st.plan.ServerName = tsrv.Name
	switch {
	case tsrv.Status == "revoked":
		block("server_not_found", "server "+tsrv.Name+" is revoked")
	case tsrv.Status != "online":
		block("server_offline", "server "+tsrv.Name+" is "+tsrv.Status+"; the replica was not started")
	case tsrv.DrainState == services.DrainStateDraining || tsrv.DrainState == services.DrainStateDrained || !tsrv.AcceptWorkloads:
		block("server_not_accepting", "server "+tsrv.Name+" does not accept new workloads")
	}
	if !hasCap(tsrv, agentprotocol.CapReplication) {
		block(CodeAgentTooOld, "the agent on "+tsrv.Name+" is too old for read replicas; update the agent first")
	}

	st.plan.SameServer = tsrv.ID == psrv
	if !st.plan.SameServer && st.primary.ID != uuid.Nil {
		if s.rep.Mesh == nil {
			block("mesh_not_configured", "the server mesh is not available on this installation")
		} else {
			lp, err := s.rep.Mesh.PlanLink(ctx, q, psc.EnvironmentID, tsrv.ID, psrv, agentprotocol.MeshPurposeReplication)
			if err != nil {
				return st, fmt.Errorf("plan mesh link: %w", err)
			}
			for _, b := range lp.Blockers {
				block(b.Code, b.Message)
			}
			for _, w := range lp.Warnings {
				warn("mesh_warning", w)
			}
			st.plan.DevicesAdded = lp.DevicesAdded
			st.plan.Links = append(st.plan.Links, ReplicaPlanLink{From: tsrv.Name, To: st.primary.Name, Purpose: agentprotocol.MeshPurposeReplication})
			if lp.DevicesAdded > 0 {
				warn(CodeMeshConfirmation, fmt.Sprintf("Connecting %s to %s adds %d tailnet device(s).", tsrv.Name, st.primary.Name, lp.DevicesAdded))
			}
		}
	}
	if info, err := q.GetDatabaseInfo(ctx, psc.Service.ID); err == nil && len(info.Info) > 0 {
		var d agentprotocol.DBInfo
		if mustUnmarshal(info.Info, &d) && d.SizeBytes > 0 {
			warn("clone_size", fmt.Sprintf("The clone copies the whole database (%s); the primary stays in service.", humanBytes(d.SizeBytes)))
		}
	}
	warn("async", "Replication is asynchronous: a replica can lag behind the primary, and a forced promote loses what it had not received.")
	return st, nil
}

// PlanReplica returns the plan of adding a replica on serverID.
func (s *Service) PlanReplica(ctx context.Context, primaryID, serverID uuid.UUID) (ReplicaPlan, error) {
	psc, spec, _, err := s.managed(ctx, policy.ScopeProjectsRead, primaryID)
	if err != nil {
		return ReplicaPlan{}, err
	}
	st, err := s.planReplica(ctx, s.q, psc, spec, serverID)
	return st.plan, err
}

// AddReplica creates a read replica of a PostgreSQL primary: the replica
// service (staged, with its credentials as references to the primary), its
// lifecycle row and, across servers, the mesh exposure, link and grant. The
// saga (prepare, link, clone, deploy, streaming) runs in ReplicaAdvanceJob.
func (s *Service) AddReplica(ctx context.Context, primaryID uuid.UUID, in AddReplicaInput) (AddReplicaResult, error) {
	psc, spec, eng, err := s.managed(ctx, policy.ScopeProjectsWrite, primaryID)
	if err != nil {
		return AddReplicaResult{}, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &psc.ProjectID); err != nil {
		return AddReplicaResult{}, err
	}
	st, err := s.planReplica(ctx, s.q, psc, spec, in.ServerID)
	if err != nil {
		return AddReplicaResult{}, err
	}
	res := AddReplicaResult{DryRun: in.DryRun, Plan: st.plan}
	if in.DryRun {
		return res, nil
	}
	if err := firstProblem(st.plan.Blockers); err != nil {
		return AddReplicaResult{}, err
	}
	if st.plan.DevicesAdded > 0 && !in.AcceptMeshChanges {
		return AddReplicaResult{}, apierr.Conflict(CodeMeshConfirmation,
			fmt.Sprintf("this replica adds %d tailnet device(s); send acceptMeshChanges=true to confirm", st.plan.DevicesAdded)).
			WithDetails(map[string]any{"devicesAdded": st.plan.DevicesAdded})
	}

	var row db.Service
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = s.createReplicaTx(ctx, tx, psc, spec, eng, st, in)
		return err
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return AddReplicaResult{}, err
		}
		if services.IsUniqueViolation(err) {
			return AddReplicaResult{}, apierr.Conflict("slug_conflict", "a service with that name was just created; retry")
		}
		return AddReplicaResult{}, fmt.Errorf("add replica: %w", err)
	}
	s.publish(psc)
	services.Publish(s.bus, services.EventServiceUpdated, psc.ProjectID, psc.EnvironmentID, row.ID)
	s.resync(ctx, st.primarySv)
	res.ServiceID, res.Status = &row.ID, ReplicaPending
	return res, nil
}

// createReplicaTx does the transactional part of AddReplica.
func (s *Service) createReplicaTx(ctx context.Context, tx pgx.Tx, psc services.Scope, pspec engines.Spec, eng *engines.Engine, st addState, in AddReplicaInput) (db.Service, error) {
	q := db.New(tx)
	// One add at a time per primary: the limit is checked under the lock.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "replicas:"+psc.Service.ID.String()); err != nil {
		return db.Service{}, err
	}
	n, err := q.CountDatabaseReplicasOfPrimary(ctx, psc.Service.ID)
	if err != nil {
		return db.Service{}, err
	}
	if n >= MaxReplicas {
		return db.Service{}, apierr.Conflict(CodeReplicasLimit, fmt.Sprintf("a primary can have at most %d read replicas", MaxReplicas))
	}
	env, err := services.LoadEnvironment(ctx, q, policy.ScopeProjectsWrite, psc.EnvironmentID)
	if err != nil {
		return db.Service{}, err
	}
	name := trimName(in.Name)
	if name == "" {
		name = fmt.Sprintf("%s-replica-%d", psc.Service.Slug, n+1)
	}
	pcfg := psc.Config()
	spec := pspec
	// A placeholder keeps the config valid until the service id (which names the
	// slot and role) is known.
	spec.Replica = &engines.ReplicaSpec{
		PrimaryServiceID: psc.Service.ID.String(), Host: psc.Service.Slug, Port: pspec.Port(), Slot: "nc_r_pending", User: "nebula_r_pending",
	}
	cfg := services.Config{
		Database: &spec, BlockEgress: true, MemoryMB: pcfg.MemoryMB, CPUs: pcfg.CPUs,
		StopGraceSeconds: pcfg.StopGraceSeconds, Docker: pcfg.Docker,
	}
	row, err := s.svcs.CreateInTx(ctx, tx, env, services.CreateInput{
		Name: name, Kind: services.KindDatabase, SourceType: services.SourceImage, Config: cfg, ServerID: &in.ServerID,
	})
	if err != nil {
		return db.Service{}, err
	}
	rsc := services.ScopeOf(env, row)
	slot, role := replicaSlot(row.ID), replicaRole(row.ID)
	rt, err := s.replicationRoute(ctx, tx, psc, pspec, st.primarySv, st.target.ID, row.ID)
	if err != nil {
		return db.Service{}, err
	}
	host, port, linkID, exposureID := rt.host, rt.port, rt.link, rt.exposure
	spec.Replica = &engines.ReplicaSpec{PrimaryServiceID: psc.Service.ID.String(), Host: host, Port: port, Slot: slot, User: role}
	cfg.Database = &spec
	raw := mustJSON(cfg.WithDefaults())
	if _, err := q.SetServiceConfigBumped(ctx, db.SetServiceConfigBumpedParams{ID: row.ID, Config: raw}); err != nil {
		return db.Service{}, fmt.Errorf("set replica config: %w", err)
	}
	vol, mount := spec.DataMount()
	if _, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: row.ID, Name: vol, MountPath: mount}); err != nil {
		return db.Service{}, fmt.Errorf("insert volume: %w", err)
	}
	ref := func(key string) string { return "${{" + psc.Service.Slug + "." + key + "}}" }
	if err := s.writeReplicaVars(ctx, q, rsc, eng, pspec, ref, true); err != nil {
		return db.Service{}, err
	}
	replPW, err := secretgen.Secret(passwordLength, secretgen.AlphaAlnum)
	if err != nil {
		return db.Service{}, err
	}
	if _, err := services.WriteServiceVariable(ctx, q, s.box, rsc, services.VarWrite{
		Key: engines.ReplicationPasswordKey, Value: replPW, Secret: true, Source: services.SourceGenerated,
	}); err != nil {
		return db.Service{}, fmt.Errorf("write %s: %w", engines.ReplicationPasswordKey, err)
	}
	var createdBy *uuid.UUID
	if p, ok := principalUser(ctx); ok {
		createdBy = &p
	}
	if _, err := q.InsertDatabaseReplica(ctx, db.InsertDatabaseReplicaParams{
		ServiceID: row.ID, WorkspaceID: psc.WorkspaceID, EnvironmentID: psc.EnvironmentID, PrimaryServiceID: psc.Service.ID,
		SlotName: slot, ReplUser: role, LinkID: linkID, ExposureID: exposureID, CreatedBy: createdBy,
	}); err != nil {
		return db.Service{}, fmt.Errorf("insert replica: %w", err)
	}
	if err := s.enqueueAdvance(ctx, tx, row.ID, 0); err != nil {
		return db.Service{}, err
	}
	meta := map[string]any{"primary": psc.Service.Slug, "replica": row.Slug, "server": st.target.Name, "sameServer": st.target.ID == st.primarySv}
	if err := audit.Record(ctx, tx, audit.Entry{
		Action: ActionReplicaAdd, ResourceType: "service", ResourceID: row.ID.String(), ProjectID: &psc.ProjectID, Metadata: meta,
	}); err != nil {
		return db.Service{}, err
	}
	return row, nil
}

// writeReplicaVars writes the replica's credential variables: references to the
// primary's (so a rotation, which replicates through WAL, stays consistent).
func (s *Service) writeReplicaVars(ctx context.Context, q *db.Queries, rsc services.Scope, eng *engines.Engine, spec engines.Spec, ref func(string) string, generated bool) error {
	for _, v := range []struct {
		key    string
		secret bool
	}{{eng.UserKey, false}, {eng.PasswordKey, true}, {eng.DatabaseKey, false}} {
		if v.key == "" {
			continue
		}
		if _, err := services.WriteServiceVariable(ctx, q, s.box, rsc, services.VarWrite{Key: v.key, Value: ref(v.key), Secret: v.secret}); err != nil {
			return fmt.Errorf("write %s: %w", v.key, err)
		}
	}
	return nil
}

func (s *Service) enqueueAdvance(ctx context.Context, tx pgx.Tx, id uuid.UUID, delay time.Duration) error {
	if s.rep.Jobs == nil {
		return nil
	}
	opts := &river.InsertOpts{}
	if delay > 0 {
		opts.ScheduledAt = s.now().Add(delay)
	}
	if err := s.rep.Jobs.InsertTx(ctx, tx, ReplicaAdvanceArgs{ServiceID: id}, opts); err != nil {
		return fmt.Errorf("enqueue replica advance: %w", err)
	}
	return nil
}

// kick runs the saga of a replica again soon, outside a transaction.
func (s *Service) kick(ctx context.Context, id uuid.UUID) {
	if s.rep.Jobs == nil {
		return
	}
	if err := s.rep.Jobs.Insert(ctx, ReplicaAdvanceArgs{ServiceID: id}, nil); err != nil {
		s.logger.ErrorContext(ctx, "enqueue replica advance", "service_id", id, "err", err)
	}
}

func (s *Service) resync(ctx context.Context, serverID uuid.UUID) {
	if s.rep.Resync == nil || serverID == uuid.Nil {
		return
	}
	if err := s.rep.Resync.Resync(context.WithoutCancel(ctx), serverID); err != nil {
		s.logger.WarnContext(ctx, "resync server", "server_id", serverID, "err", err)
	}
}

// mkStep and parseStep encode the saga cursor as "<name>#<cycle>". A rebuild
// starts a new cycle, which keeps the idempotency keys of its commands apart
// from the earlier cycles'.
func mkStep(name string, cycle int) string { return name + "#" + strconv.Itoa(cycle) }

func parseStep(step string) (name string, cycle int) {
	name, c, _ := strings.Cut(step, "#")
	cycle, _ = strconv.Atoi(c)
	if cycle < 1 {
		cycle = 1
	}
	return name, cycle
}

// AdvanceReplica moves a replica's saga forward. It is re-entrant: every step
// finds the command or deployment it started earlier. It reports done once the
// replica settled (streaming, failed, removed, ...).
func (s *Service) AdvanceReplica(ctx context.Context, id uuid.UUID) (bool, error) {
	for range 12 {
		r, ok, err := s.replicaRow(ctx, s.q, id)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
		moved, done, err := s.advanceOnce(ctx, r)
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
		if !moved {
			return false, nil
		}
	}
	return false, nil
}

// advanceOnce runs one step; moved reports a transition (run the next step now).
func (s *Service) advanceOnce(ctx context.Context, r db.DatabaseReplica) (moved, done bool, err error) {
	switch r.State {
	case ReplicaPending, ReplicaPreparing, ReplicaCloning, ReplicaDeploying:
	case ReplicaRemoving:
		return s.advanceRemove(ctx, r)
	default:
		return false, true, nil
	}
	rsc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID)
	if err != nil {
		return false, true, nil // the replica service is gone
	}
	psc, err := services.LoadScopeInternal(ctx, s.q, r.PrimaryServiceID)
	if err != nil {
		return false, true, s.failReplica(ctx, r, "the primary no longer exists")
	}
	rspec := rsc.Config().Database
	pspec := psc.Config().Database
	if rspec == nil || pspec == nil || rspec.Replica == nil {
		return false, true, s.failReplica(ctx, r, "the replica service lost its replica configuration")
	}
	if paused, err := s.meshPaused(ctx, r); err != nil {
		return false, false, err
	} else if paused {
		return false, false, nil
	}
	name, cycle := parseStep(r.Step)
	tag := fmt.Sprintf("%s:%d", r.ServiceID, cycle)
	next := func(state, msg, step string) (bool, bool, error) {
		return true, false, s.transition(ctx, r, state, msg, mkStep(step, cycle))
	}
	fail := func(msg string) (bool, bool, error) {
		return false, true, s.failReplica(ctx, r, msg)
	}

	switch name {
	case stepRebuildStop, stepRebuildDrop:
		return s.advanceRebuild(ctx, r, name, cycle, rsc, psc)

	case stepPrepare:
		psrv, err := s.currentServer(ctx, s.q, psc)
		if err != nil {
			return fail("preparing the primary failed: " + err.Error())
		}
		srvRow, err := s.loadServer(ctx, s.q, psc.WorkspaceID, psrv)
		if err != nil {
			return false, false, err
		}
		cmd, err := s.replCommand(ctx, "repl_prepare:"+tag, psrv, psc, *pspec, agentprotocol.DBActionReplPrepare, &agentprotocol.ReplAdmin{
			Role: r.ReplUser, Slot: r.SlotName, MaxSlotWALKeepMB: maxSlotWALKeepMB(srvRow),
		})
		if err != nil {
			return false, false, err
		}
		switch st, msg, _ := outcome(cmd); st {
		case cmdFailed:
			return fail("preparing the primary failed: " + msg)
		case cmdOK:
			return next(ReplicaPreparing, "", stepLink)
		}
		if r.State != ReplicaPreparing {
			return false, false, s.transition(ctx, r, ReplicaPreparing, "Preparing the primary for replication.", mkStep(stepPrepare, cycle))
		}
		return false, false, nil

	case stepLink:
		if r.LinkID == nil || r.ExposureID == nil {
			return next(ReplicaPreparing, "", stepClone)
		}
		if s.rep.Mesh == nil {
			return fail("the server mesh is not available")
		}
		wait := func(why string) (bool, bool, error) {
			if s.now().Sub(r.UpdatedAt) > linkTimeout {
				return fail(CodeMeshLinkNotReady + ": " + why)
			}
			return false, false, nil
		}
		rd, err := s.rep.Mesh.Ready(ctx, *r.LinkID)
		if err != nil {
			return false, false, err
		}
		if !rd.Online || !rd.ACLOK {
			return wait("the replication link is not ready: " + rd.Reason)
		}
		cr, err := s.rep.Mesh.Check(ctx, *r.LinkID, *r.ExposureID)
		if err != nil {
			return false, false, err
		}
		if cr.Result != agentprotocol.MeshCheckConnected {
			return wait("the replication link does not connect yet (" + cr.Result + ")")
		}
		return next(ReplicaPreparing, "", stepClone)

	case stepClone:
		if rsc.Service.ServerID == nil {
			return fail("the replica has no server")
		}
		vol, err := s.dataVolumeName(ctx, s.q, rsc.Service.ID, *rspec)
		if err != nil {
			return fail(err.Error())
		}
		cmd, err := s.command(ctx, "repl_clone:"+tag, *rsc.Service.ServerID, agentprotocol.KindDBReplicaClone, agentprotocol.ReplicaClonePayload{
			ServiceID: rsc.Service.ID.String(), Engine: rspec.Engine, Version: rspec.Version, Flavor: rspec.Flavor, DataVolume: vol,
			Host: rspec.Replica.Host, Port: rspec.Replica.Port, User: rspec.Replica.User, Slot: rspec.Replica.Slot,
			Networks: services.RouteNetworks(rsc.EnvironmentID, true),
		})
		if err != nil {
			return false, false, err
		}
		if r.CloneCommandID == nil || *r.CloneCommandID != cmd.ID {
			if err := s.q.SetDatabaseReplicaClone(ctx, db.SetDatabaseReplicaCloneParams{ServiceID: r.ServiceID, CloneCommandID: &cmd.ID}); err != nil {
				return false, false, err
			}
		}
		switch st, msg, _ := outcome(cmd); st {
		case cmdFailed:
			return fail("cloning the primary failed: " + msg)
		case cmdOK:
			return next(ReplicaDeploying, "", stepDeploy)
		}
		if r.State != ReplicaCloning {
			return false, false, s.transition(ctx, r, ReplicaCloning, "Copying the primary's data (pg_basebackup).", mkStep(stepClone, cycle))
		}
		return false, false, nil

	case stepDeploy:
		if s.rep.Deployer == nil {
			return fail("deployments are not available")
		}
		if rsc.Service.DesiredState == services.DesiredStopped {
			if err := s.q.SetServiceDesiredState(ctx, db.SetServiceDesiredStateParams{ID: rsc.Service.ID, DesiredState: services.DesiredRunning}); err != nil {
				return false, false, err
			}
		}
		// A fenced old primary rebuilt as a replica now has a new, empty data
		// volume: the fence protected the old data and has done its job.
		if n, err := s.q.DeleteDatabaseFence(ctx, rsc.Service.ID); err != nil {
			return false, false, err
		} else if n > 0 {
			s.resync(ctx, valueOr(rsc.Service.ServerID))
		}
		dctx := systemCtx(ctx, rsc, "Read replica")
		if _, err := s.rep.Deployer.Deploy(dctx, rsc.Service.ID, "", "Read replica of "+psc.Service.Name); err != nil {
			if ae, ok := apierr.As(err); !ok || ae.Code != "deployment_in_progress" {
				return fail("deploying the replica failed: " + err.Error())
			}
		}
		return next(ReplicaDeploying, "Deploying the replica.", stepAwait)

	case stepAwait:
		n, err := s.q.CountActiveDeployments(ctx, rsc.Service.ID)
		if err != nil {
			return false, false, err
		}
		if n > 0 {
			return false, false, nil
		}
		msg := "the deployment of the replica did not succeed"
		if d, err := s.q.GetLatestServiceDeployment(ctx, rsc.Service.ID); err == nil {
			if d.Status == "succeeded" {
				return next(ReplicaDeploying, "Waiting for the replica to start streaming.", stepStream)
			}
			if d.StatusMessage != "" {
				msg = "deploying the replica failed: " + d.StatusMessage
			}
		}
		return fail(msg)

	case stepStream:
		// ObserveReplication moves the replica to streaming; here we only bound the wait.
		if s.now().Sub(r.UpdatedAt) > streamTimeout {
			return false, false, s.transition(ctx, r, ReplicaBroken, "The replica started but never began streaming; check its logs, or rebuild it.", mkStep(stepDone, cycle))
		}
		return false, false, nil
	}
	return false, true, nil
}

// meshPaused reports whether the mesh is paused, which makes the sagas that
// need it wait (they are re-entrant). Only replicas with a link care.
func (s *Service) meshPaused(ctx context.Context, r db.DatabaseReplica) (bool, error) {
	if r.LinkID == nil {
		return false, nil
	}
	st, err := s.q.GetMeshSettings(ctx, r.WorkspaceID)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return st.Paused, nil
}

// transition sets a replica's state, message and step. A change of health class
// (serving reads or not) restages the services that reference the group; it
// never deploys them.
func (s *Service) transition(ctx context.Context, r db.DatabaseReplica, state, msg, step string) error {
	var staged []db.Service
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.SetDatabaseReplicaState(ctx, db.SetDatabaseReplicaStateParams{ServiceID: r.ServiceID, State: state, Message: msg, Step: step}); err != nil {
			return err
		}
		if healthClass(r.State) != healthClass(state) {
			members, err := s.groupServices(ctx, q, r.PrimaryServiceID)
			if err != nil {
				return err
			}
			if staged, err = s.stageGroupReferrers(ctx, q, r.EnvironmentID, members); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replica state: %w", err)
	}
	s.afterTransition(ctx, r, state, msg, staged)
	return nil
}

func (s *Service) afterTransition(ctx context.Context, r db.DatabaseReplica, state, msg string, staged []db.Service) {
	psc, err := services.LoadScopeInternal(ctx, s.q, r.PrimaryServiceID)
	if err != nil {
		return
	}
	services.Publish(s.bus, EventDatabaseUpdated, psc.ProjectID, psc.EnvironmentID, r.PrimaryServiceID)
	services.Publish(s.bus, EventDatabaseUpdated, psc.ProjectID, psc.EnvironmentID, r.ServiceID)
	services.Publish(s.bus, services.EventServiceUpdated, psc.ProjectID, psc.EnvironmentID, r.ServiceID)
	s.publishReferrers(psc, staged)
	if state != r.State && (state == ReplicaBroken || state == ReplicaLagging || state == ReplicaNeedsRebuild) && s.rep.Notify != nil {
		typ := NotifyReplicaBroken
		if state == ReplicaLagging {
			typ = NotifyReplicaLagging
		}
		name := r.ServiceID.String()
		if rsc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID); err == nil {
			name = rsc.Service.Name
		}
		s.rep.Notify(ctx, ReplicaEvent{
			Type: typ, WorkspaceID: psc.WorkspaceID, ProjectID: psc.ProjectID, ServiceID: r.ServiceID, ServiceName: name,
			Primary: psc.Service.Name, Message: msg,
		})
	}
}

// failReplica ends a replica's saga as failed: the mesh grants are released and
// the role and slot on the primary are dropped (best effort). The replica
// service stays so its message can be read; Remove deletes it.
func (s *Service) failReplica(ctx context.Context, r db.DatabaseReplica, msg string) error {
	name, cycle := parseStep(r.Step)
	if err := s.transition(ctx, r, ReplicaFailed, msg, mkStep(name, cycle)); err != nil {
		return err
	}
	s.releaseReplica(ctx, r, cycle)
	return nil
}

// releaseReplica drops what a replica holds on the primary side: its mesh grants
// and its slot, role and pg_hba.conf line.
func (s *Service) releaseReplica(ctx context.Context, r db.DatabaseReplica, cycle int) {
	if s.rep.Mesh != nil && r.LinkID != nil {
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error { return s.rep.Mesh.Release(ctx, tx, r.ServiceID) })
		if err != nil {
			s.logger.WarnContext(ctx, "release replica mesh grants", "service_id", r.ServiceID, "err", err)
		}
	}
	psc, err := services.LoadScopeInternal(ctx, s.q, r.PrimaryServiceID)
	if err != nil {
		return
	}
	pspec := psc.Config().Database
	if pspec == nil || psc.Service.CurrentDeploymentID == nil {
		return
	}
	psrv, err := s.currentServer(ctx, s.q, psc)
	if err != nil {
		return
	}
	if _, err := s.replCommand(ctx, fmt.Sprintf("repl_drop_failed:%s:%d", r.ServiceID, cycle), psrv, psc, *pspec,
		agentprotocol.DBActionReplDrop, &agentprotocol.ReplAdmin{Role: r.ReplUser, Slot: r.SlotName}); err != nil {
		s.logger.WarnContext(ctx, "drop replica slot", "service_id", r.ServiceID, "err", err)
	}
}

// RemoveResult is the response of removing a replica.
type RemoveResult struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Status    string    `json:"status"`
}

// RemoveReplica removes a replica: its container and data volume are deleted,
// its slot, role and pg_hba.conf line on the primary are dropped and its mesh
// grants are released (a link and an exposure nobody else needs go with them).
func (s *Service) RemoveReplica(ctx context.Context, replicaID uuid.UUID) (RemoveResult, error) {
	sc, _, _, err := s.managed(ctx, policy.ScopeProjectsWrite, replicaID)
	if err != nil {
		return RemoveResult{}, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &sc.ProjectID); err != nil {
		return RemoveResult{}, err
	}
	r, ok, err := s.replicaRow(ctx, s.q, replicaID)
	if err != nil {
		return RemoveResult{}, err
	}
	if !ok {
		return RemoveResult{}, apierr.Conflict("not_a_replica", "this database is not a read replica")
	}
	if r.State == ReplicaPromoting {
		return RemoveResult{}, apierr.Conflict(CodePromoteInProgress, "a promotion of this replica is running")
	}
	if r.State == ReplicaRemoving {
		return RemoveResult{ServiceID: replicaID, Status: ReplicaRemoving}, nil
	}
	psc, err := services.LoadScopeInternal(ctx, s.q, r.PrimaryServiceID)
	if err != nil {
		return RemoveResult{}, err
	}
	// A saga still running (clone) is cancelled by the removal.
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.SetDatabaseReplicaState(ctx, db.SetDatabaseReplicaStateParams{
			ServiceID: replicaID, State: ReplicaRemoving, Message: "Removing the replica.", Step: mkStep(stepStop, 1),
		}); err != nil {
			return err
		}
		if err := s.enqueueAdvance(ctx, tx, replicaID, 0); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionReplicaRemove, ResourceType: "service", ResourceID: replicaID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"primary": psc.Service.Slug, "replica": sc.Service.Slug},
		})
	})
	if err != nil {
		return RemoveResult{}, fmt.Errorf("remove replica: %w", err)
	}
	s.afterTransition(ctx, r, ReplicaRemoving, "Removing the replica.", nil)
	return RemoveResult{ServiceID: replicaID, Status: ReplicaRemoving}, nil
}

type skipGuardKey struct{}

// advanceRemove runs the removal saga: stop, drop the primary's side, delete.
func (s *Service) advanceRemove(ctx context.Context, r db.DatabaseReplica) (moved, done bool, err error) {
	name, cycle := parseStep(r.Step)
	tag := fmt.Sprintf("%s:%d", r.ServiceID, cycle)
	rsc, err := services.LoadScopeInternal(ctx, s.q, r.ServiceID)
	if err != nil {
		// The replica service is deleted: the agent's removal and the row are left.
		return s.awaitRemoval(ctx, r, tag)
	}
	switch name {
	case stepStop:
		// A replica streams before its first deploy is over (and recorded as
		// current): wait for that deploy, or the stop is skipped, the replica
		// stays connected and the primary refuses to drop its active slot.
		if _, err := s.q.GetActiveDeployment(ctx, r.ServiceID); err == nil {
			return false, false, nil
		} else if !isNoRows(err) {
			return false, false, err
		}
		if rsc.Service.CurrentDeploymentID != nil && rsc.Service.ServerID != nil {
			if srv, err := s.loadServer(ctx, s.q, rsc.WorkspaceID, *rsc.Service.ServerID); err == nil && srv.Status == "online" {
				cmd, err := s.command(ctx, "repl_rm_stop:"+tag, srv.ID, agentprotocol.KindStopService, agentprotocol.ServiceActionPayload{
					ServiceID: rsc.Service.ID.String(), StopGraceSeconds: rsc.Config().StopGraceSeconds,
				})
				if err != nil {
					return false, false, err
				}
				if st, _, _ := outcome(cmd); st == cmdRunning {
					return false, false, nil
				}
			}
		}
		return true, false, s.setStep(ctx, r, mkStep(stepDrop, cycle))
	case stepDrop:
		if wait, err := s.dropReplicaSide(ctx, r, tag); err != nil || wait {
			return false, false, err
		}
		return true, false, s.setStep(ctx, r, mkStep(stepDelete, cycle))
	case stepDelete:
		return true, false, s.deleteReplicaService(ctx, r, rsc, tag)
	}
	return false, true, nil
}

// dropAttempts is how often a failed drop of the replica's slot and role is
// sent again (a replica that reconnected in between makes its slot active).
const dropAttempts = 3

// dropReplicaSide drops the replica's slot, role and pg_hba line on the
// primary and reports whether the saga must wait for it. A failed drop is sent
// again: a slot left behind pins the primary's WAL for good. A primary that is
// gone or offline, or a drop that keeps failing, does not hold the removal: it
// goes on with a warning.
func (s *Service) dropReplicaSide(ctx context.Context, r db.DatabaseReplica, tag string) (wait bool, err error) {
	psc, err := services.LoadScopeInternal(ctx, s.q, r.PrimaryServiceID)
	if err != nil || psc.Service.CurrentDeploymentID == nil {
		return false, nil
	}
	pspec := psc.Config().Database
	if pspec == nil {
		return false, nil
	}
	psrv, err := s.currentServer(ctx, s.q, psc)
	if err != nil {
		return false, nil
	}
	if srv, err := s.loadServer(ctx, s.q, psc.WorkspaceID, psrv); err != nil || srv.Status != "online" {
		return false, nil
	}
	base := "repl_drop:" + tag
	attempt := 1
	for ; attempt < dropAttempts; attempt++ { // the newest attempt queued so far
		if _, err := s.q.GetAgentCommandByKey(ctx, attemptKey(base, attempt+1)); isNoRows(err) {
			break
		} else if err != nil {
			return false, err
		}
	}
	send := func(n int) (db.AgentCommand, error) {
		return s.replCommand(ctx, attemptKey(base, n), psrv, psc, *pspec, agentprotocol.DBActionReplDrop,
			&agentprotocol.ReplAdmin{Role: r.ReplUser, Slot: r.SlotName})
	}
	cmd, err := send(attempt)
	if err != nil {
		return false, err
	}
	st, msg, _ := outcome(cmd)
	switch {
	case st == cmdRunning:
		return true, nil
	case st == cmdFailed && cmd.Status == "failed" && attempt < dropAttempts:
		if _, err := send(attempt + 1); err != nil {
			return false, err
		}
		return true, nil
	case st == cmdFailed:
		s.logger.WarnContext(ctx, "replication slot or role of a removed replica may remain on the primary",
			"service_id", r.ServiceID, "primary_id", r.PrimaryServiceID, "slot", r.SlotName, "attempts", attempt, "reason", msg)
	}
	return false, nil
}

func (s *Service) setStep(ctx context.Context, r db.DatabaseReplica, step string) error {
	return s.q.SetDatabaseReplicaState(ctx, db.SetDatabaseReplicaStateParams{ServiceID: r.ServiceID, State: r.State, Message: r.Message, Step: step})
}

// Removal of the replica's container and data volume is the agent's work: the
// delete queues a remove_service command, and the replica row (the saga's own
// cursor) stays until the agent reports it done. Deleting the row when the
// command is merely queued would make "the replica is gone" true while its
// volume still exists, and a failed removal (the agent's "remove incomplete")
// would leak the volume with nobody left to retry it.
const (
	// removeAttempts is how often a failed remove_service is sent again.
	removeAttempts = 3
	// removeWaitLimit is how long the saga waits for an online agent's result.
	removeWaitLimit = 10 * time.Minute
)

// attemptKey is the idempotency key of the nth attempt of a saga command: the
// first keeps the base key, later ones get ":r<n>". The "repl_" prefix of the
// base makes the command's result kick the saga (kickForCommand).
func attemptKey(base string, attempt int) string {
	if attempt <= 1 {
		return base
	}
	return fmt.Sprintf("%s:r%d", base, attempt)
}

// removeKey is the idempotency key of the nth remove_service of a removal.
func removeKey(tag string, attempt int) string {
	return attemptKey("repl_rm_remove:"+tag, attempt)
}

// deleteReplicaService releases the replica's mesh grants and deletes its
// service (queueing the removal of its container and data volume on the agent).
func (s *Service) deleteReplicaService(ctx context.Context, r db.DatabaseReplica, rsc services.Scope, tag string) error {
	if s.rep.Mesh != nil {
		if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error { return s.rep.Mesh.Release(ctx, tx, r.ServiceID) }); err != nil {
			return fmt.Errorf("release mesh grants: %w", err)
		}
	}
	dctx := context.WithValue(systemCtx(ctx, rsc, "Read replica"), skipGuardKey{}, true)
	if err := s.svcs.Delete(dctx, r.ServiceID, services.DeleteInput{
		Confirm: rsc.Service.Slug, DeleteVolumes: true, RemoveKey: removeKey(tag, 1),
	}); err != nil {
		if ae, ok := apierr.As(err); !ok || ae.Status != 404 {
			return fmt.Errorf("delete replica service: %w", err)
		}
	}
	return nil
}

// awaitRemoval runs once the replica service is deleted: it waits for the
// agent's removal of the container and volume, sends it again when the agent
// reports a failure, and then deletes the replica row. An agent that is
// offline (the command stays queued and runs when it returns), does not answer
// within removeWaitLimit or keeps failing does not hold the row forever: the
// saga gives up with a warning.
func (s *Service) awaitRemoval(ctx context.Context, r db.DatabaseReplica, tag string) (moved, done bool, err error) {
	var cmd db.AgentCommand
	attempt := 0
	for n := 1; n <= removeAttempts; n++ {
		c, err := s.q.GetAgentCommandByKey(ctx, removeKey(tag, n))
		if isNoRows(err) {
			break
		}
		if err != nil {
			return false, false, err
		}
		cmd, attempt = c, n
	}
	if attempt == 0 {
		// Nothing was queued (the replica never ran): only the row is left.
		return false, true, s.finishRemove(ctx, r)
	}
	st, msg, _ := outcome(cmd)
	switch st {
	case cmdOK:
		return false, true, s.finishRemove(ctx, r)
	case cmdRunning:
		srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: cmd.ServerID})
		if err == nil && srv.Status == "online" && time.Since(cmd.CreatedAt) < removeWaitLimit {
			return false, false, nil
		}
		msg = "the agent did not finish it"
	case cmdFailed:
		if cmd.Status == "failed" && attempt < removeAttempts {
			// The agent answered but could not finish (a volume still in use, say).
			if _, err := s.command(ctx, removeKey(tag, attempt+1), cmd.ServerID, cmd.Kind, cmd.Payload); err != nil {
				return false, false, err
			}
			return false, false, nil
		}
	}
	s.logger.WarnContext(ctx, "replica container or data volume may remain on the server",
		"service_id", r.ServiceID, "server_id", cmd.ServerID, "attempts", attempt, "reason", msg)
	return false, true, s.finishRemove(ctx, r)
}

// finishRemove deletes the replica's row and restages the group's consumers.
func (s *Service) finishRemove(ctx context.Context, r db.DatabaseReplica) error {
	var staged []db.Service
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.DeleteDatabaseReplica(ctx, r.ServiceID); err != nil {
			return err
		}
		members, err := s.groupServices(ctx, q, r.PrimaryServiceID)
		if err != nil {
			return err
		}
		staged, err = s.stageGroupReferrers(ctx, q, r.EnvironmentID, members)
		return err
	})
	if err != nil {
		return fmt.Errorf("delete replica row: %w", err)
	}
	if psc, err := services.LoadScopeInternal(ctx, s.q, r.PrimaryServiceID); err == nil {
		services.Publish(s.bus, EventDatabaseUpdated, psc.ProjectID, psc.EnvironmentID, r.PrimaryServiceID)
		services.PublishIn(s.bus, psc.WorkspaceID, services.EventServiceDeleted, psc.ProjectID, r.EnvironmentID, r.ServiceID)
		s.publishReferrers(psc, staged)
		s.resync(ctx, valueOr(psc.Service.ServerID))
	}
	return nil
}

// repRoute is how a replica reaches its primary: the host and port it dials and,
// across servers, the mesh link and exposure that carry it.
type repRoute struct {
	host           string
	port           int
	link, exposure *uuid.UUID
}

// replicationRoute works out (and across servers creates) the route of a replica
// on targetSrv to the primary on primarySrv: the same server needs no mesh; across
// servers the primary's port is exposed on its server node, the replica's server
// gets a replication link and the replica a grant.
func (s *Service) replicationRoute(ctx context.Context, tx pgx.Tx, psc services.Scope, pspec engines.Spec, primarySrv, targetSrv, replicaID uuid.UUID) (repRoute, error) {
	rt := repRoute{host: psc.Service.Slug, port: pspec.Port()}
	if targetSrv == primarySrv {
		return rt, nil
	}
	m := s.rep.Mesh
	if m == nil {
		return rt, apierr.Conflict("mesh_not_configured", "the server mesh is not available on this installation")
	}
	exp, err := m.EnsureExposure(ctx, tx, psc.Service.ID, agentprotocol.MeshPurposeReplication, pspec.Port())
	if err != nil {
		return rt, fmt.Errorf("ensure exposure: %w", err)
	}
	link, err := m.EnsureLink(ctx, tx, psc.EnvironmentID, targetSrv, primarySrv, agentprotocol.MeshPurposeReplication)
	if err != nil {
		return rt, fmt.Errorf("ensure link: %w", err)
	}
	if err := m.Grant(ctx, tx, link.ID, exp.ID, replicaID, "replica"); err != nil {
		return rt, fmt.Errorf("grant replication link: %w", err)
	}
	rt.host, rt.port = agentprotocol.MeshReplicationAlias(primarySrv.String()), exp.MeshPort
	rt.link, rt.exposure = &link.ID, &exp.ID
	return rt, nil
}
