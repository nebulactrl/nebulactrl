package databases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Postgres read replicas.
//
// A replica is an ordinary kind=database service of its primary's environment;
// database_replicas holds its lifecycle. Creating one is a River saga
// (ReplicaAdvanceJob): prepare the primary, bring up the replication link when
// the replica is on another server, clone, deploy, wait for streaming.
// Promote is a second durable saga (promote.go). Everything here is manual:
// NebulaCtrl never detects a failure or switches anything by itself.

// Replica lifecycle states (database_replicas.state).
const (
	ReplicaPending      = "pending"
	ReplicaPreparing    = "preparing"
	ReplicaCloning      = "cloning"
	ReplicaDeploying    = "deploying"
	ReplicaStreaming    = "streaming"
	ReplicaLagging      = "lagging"
	ReplicaBroken       = "broken"
	ReplicaNeedsRebuild = "needs_rebuild"
	ReplicaPromoting    = "promoting"
	ReplicaRemoving     = "removing"
	ReplicaFailed       = "failed"
)

// Saga steps (database_replicas.step).
const (
	stepPrepare = "prepare"
	stepLink    = "link"
	stepClone   = "clone"
	stepDeploy  = "deploy"
	stepAwait   = "await_deploy"
	stepStream  = "stream"
	stepDone    = "done"
	// Removal and rebuild.
	stepStop   = "stop"
	stepDrop   = "drop"
	stepDelete = "delete"
)

// Limits and thresholds.
const (
	// MaxReplicas is the most replicas one primary may have.
	MaxReplicas = 5
	// LagBytesThreshold and LagTimeThreshold mark a replica lagging after
	// LaggingAfter above either.
	LagBytesThreshold = 64 << 20
	LagTimeThreshold  = 60 * time.Second
	LaggingAfter      = 5 * time.Minute
	// BrokenAfter is how long a replica may be out of streaming before it is broken.
	BrokenAfter = 2 * time.Minute
	// StaleAfter is the age after which an observation is shown as unknown.
	StaleAfter = 90 * time.Second
	// streamTimeout bounds the wait for the first streaming observation.
	streamTimeout = 10 * time.Minute
	// linkTimeout bounds the wait for the replication link.
	linkTimeout = 5 * time.Minute
	// maxWALKeepCapMB caps max_slot_wal_keep_size (4 GiB).
	maxWALKeepCapMB = 4096
)

// Audit actions.
const (
	ActionReplicaAdd     = "database.replica.add"
	ActionReplicaRemove  = "database.replica.remove"
	ActionReplicaRebuild = "database.replica.rebuild"
	ActionPromote        = "database.promote"
	ActionFence          = "database.fence"
)

// Error codes of the replica domain.
const (
	CodeReplicaWrongEngine    = "replica_wrong_engine"
	CodeReplicasLimit         = "replicas_limit"
	CodeReplicaNotPrimary     = "replica_not_primary"
	CodeReplicasExist         = "replicas_exist"
	CodeReplicaPinned         = "replica_pinned"
	CodeReplicaReadOnly       = "replica_read_only"
	CodeBackupReplica         = "backup_replica_unsupported"
	CodePrimaryNotRunning     = "primary_not_running"
	CodePromoteInProgress     = "promote_in_progress"
	CodePromoteConfirmation   = "promote_confirmation_required"
	CodeDataLossNotAccepted   = "data_loss_not_accepted"
	CodeAgentTooOld           = "agent_too_old"
	CodeMeshServerRequired    = services.CodeMeshServerRequired
	CodeMeshConfirmation      = services.CodeMeshConfirmationRequired
	CodeMeshLinkNotReady      = services.CodeMeshLinkNotReady
	CodeReplicaRemoveFirst    = "replica_remove_required"
	CodeDatabaseFenced        = "database_fenced"
	CodeReplicaNotRebuildable = "replica_not_rebuildable"
)

// Deployer starts a deployment of a service. *deployments.Service implements it.
type Deployer interface {
	Deploy(ctx context.Context, serviceID uuid.UUID, commitSHA, message string) (deployments.DeploymentView, error)
}

// LinkReconciler derives and applies the mesh grants of an environment
// (internal/meshlinks). ReconcileAdd opens the grants the given consumers need
// (an explicit action: Promote), which may add tailnet devices.
type LinkReconciler interface {
	ReconcileAdd(ctx context.Context, envID uuid.UUID, consumers []uuid.UUID) error
}

// Resyncer pushes a server's desired state to its agent (the hub implements it).
type Resyncer interface {
	Resync(ctx context.Context, serverID uuid.UUID) error
}

// ReplicaEvent is something the notification channels hear about a replica.
type ReplicaEvent struct {
	// Type is NotifyReplicaLagging or NotifyReplicaBroken.
	Type        string
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	ServiceID   uuid.UUID
	ServiceName string
	Primary     string
	Message     string
}

// Notification event types of replicas (database.replica.lagging / .broken).
const (
	NotifyReplicaLagging = "database.replica.lagging"
	NotifyReplicaBroken  = "database.replica.broken"
)

// ReplicaNotifier receives replica alerts; the app adapts it to
// internal/notifications.
type ReplicaNotifier func(ctx context.Context, ev ReplicaEvent)

// ReplicaDeps are the collaborators of the replica sagas. Every field is
// installed at wire time; a nil Mesh makes cross-server replicas unavailable.
type ReplicaDeps struct {
	Mesh     mesh.Provider
	Links    LinkReconciler
	Deployer Deployer
	Jobs     *jobs.Manager
	Resync   Resyncer
	Notify   ReplicaNotifier
}

// SetReplicas installs the replica collaborators (wire time only).
func (s *Service) SetReplicas(d ReplicaDeps) { s.rep = d }

// Problem is a blocker or warning of a plan.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func problemError(p Problem) *apierr.Error {
	switch p.Code {
	case CodeReplicaWrongEngine, CodeDataLossNotAccepted, CodePromoteConfirmation, CodeBackupReplica, "server_not_found":
		return apierr.Unprocessable(p.Code, p.Message)
	}
	return apierr.Conflict(p.Code, p.Message)
}

func firstProblem(ps []Problem) error {
	if len(ps) == 0 {
		return nil
	}
	return problemError(ps[0]).WithDetails(map[string]any{"blockers": ps})
}

// replicaSlot and replicaRole name the physical slot (also the application_name)
// and the replication role of a replica.
func replicaSlot(id uuid.UUID) string { return "nc_r_" + agentprotocol.Short(id.String()) }
func replicaRole(id uuid.UUID) string { return "nebula_r_" + agentprotocol.Short(id.String()) }

// systemCtx carries the control plane's principal for a service's project, so
// the sagas can start deployments and call the mesh without a user request.
func systemCtx(ctx context.Context, sc services.Scope, label string) context.Context {
	return auth.WithPrincipal(ctx, auth.System(sc.WorkspaceID, sc.ProjectID, label,
		policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite))
}

// cmdState is where a command stands.
type cmdState int

const (
	cmdRunning cmdState = iota
	cmdOK
	cmdFailed
)

// outcome reads a stored command. Data is the result's payload.
func outcome(c db.AgentCommand) (st cmdState, msg string, data json.RawMessage) {
	switch c.Status {
	case "succeeded", "failed":
		var res agentprotocol.ResultFrame
		_ = json.Unmarshal(c.Result, &res)
		if c.Status == "succeeded" {
			return cmdOK, res.Error, res.Data
		}
		msg = res.Error
		if msg == "" {
			msg = c.Error
		}
		if msg == "" {
			msg = "the agent reported a failure"
		}
		return cmdFailed, msg, res.Data
	case "expired", "canceled":
		msg = c.Error
		if msg == "" {
			msg = "Not delivered: server offline. No changes were made."
		}
		return cmdFailed, msg, nil
	}
	return cmdRunning, "", nil
}

// command returns the command enqueued under key, enqueueing it first when none
// exists (so saga steps are re-entrant: a repeated step finds its command).
func (s *Service) command(ctx context.Context, key string, serverID uuid.UUID, kind string, payload any) (db.AgentCommand, error) {
	if c, err := s.q.GetAgentCommandByKey(ctx, key); err == nil {
		return c, nil
	} else if !isNoRows(err) {
		return db.AgentCommand{}, err
	}
	raw := mustJSON(payload)
	var id uuid.UUID
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = s.disp.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: kind, Payload: raw, IdempotencyKey: key})
		return err
	})
	if err != nil {
		return db.AgentCommand{}, fmt.Errorf("enqueue %s: %w", kind, err)
	}
	return s.q.GetAgentCommand(ctx, id)
}

// replCommand enqueues a typed db_admin action of the replication group.
func (s *Service) replCommand(ctx context.Context, key string, serverID uuid.UUID, svc services.Scope, spec engines.Spec, action string, repl *agentprotocol.ReplAdmin) (db.AgentCommand, error) {
	return s.command(ctx, key, serverID, agentprotocol.KindDBAdmin, agentprotocol.DBAdminPayload{
		ServiceID: svc.Service.ID.String(), Action: action,
		Engine: spec.Engine, Version: spec.Version, Flavor: spec.Flavor, Database: spec.DatabaseName, User: spec.Username, Repl: repl,
	})
}

// replicaRow loads a replica's lifecycle row.
func (s *Service) replicaRow(ctx context.Context, q *db.Queries, id uuid.UUID) (db.DatabaseReplica, bool, error) {
	r, err := q.GetDatabaseReplica(ctx, id)
	if isNoRows(err) {
		return db.DatabaseReplica{}, false, nil
	}
	return r, err == nil, err
}

// healthClass is "ok" while a replica serves reads, else "down". A change of
// class (not of lag numbers) restages the consumers of the group.
func healthClass(state string) string {
	if state == ReplicaStreaming || state == ReplicaLagging {
		return "ok"
	}
	return "down"
}

// groupMembers lists the replicas of a primary's group with their servers.
func (s *Service) groupMembers(ctx context.Context, q *db.Queries, primaryID uuid.UUID) ([]db.ListDatabaseReplicasWithServersRow, error) {
	return q.ListDatabaseReplicasWithServers(ctx, primaryID)
}

// referrersOfAny lists the live services of an environment, other than exclude,
// whose variables reference any of the slugs (${{slug.KEY}}).
func (s *Service) referrersOfAny(ctx context.Context, q *db.Queries, envID uuid.UUID, slugs map[string]bool, exclude map[uuid.UUID]bool) ([]db.Service, map[uuid.UUID][]string, error) {
	rows, err := q.ListEnvironmentVariableTemplates(ctx, envID)
	if err != nil {
		return nil, nil, fmt.Errorf("list variables: %w", err)
	}
	keys := map[uuid.UUID][]string{}
	for _, r := range rows {
		if exclude[r.ServiceID] {
			continue
		}
		val, err := s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(r.ServiceID.String(), r.Key), r.ValueEnc)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt variable %s: %w", r.Key, err)
		}
		for _, ref := range services.References(val) {
			if slugs[ref[0]] {
				keys[r.ServiceID] = append(keys[r.ServiceID], r.Key)
				break
			}
		}
	}
	if len(keys) == 0 {
		return nil, keys, nil
	}
	all, err := q.ListAllEnvironmentServices(ctx, envID)
	if err != nil {
		return nil, nil, fmt.Errorf("list services: %w", err)
	}
	var out []db.Service
	for _, svc := range all {
		if _, ok := keys[svc.ID]; ok {
			out = append(out, svc)
		}
	}
	return out, keys, nil
}

// stageGroupReferrers restages (bumps the generation of) the services outside
// the group whose variables reference any member, so their changed read
// exports show up as staged changes. Nothing is deployed.
func (s *Service) stageGroupReferrers(ctx context.Context, q *db.Queries, envID uuid.UUID, members []db.Service) ([]db.Service, error) {
	slugs := map[string]bool{}
	exclude := map[uuid.UUID]bool{}
	for _, m := range members {
		slugs[m.Slug] = true
		exclude[m.ID] = true
	}
	refs, _, err := s.referrersOfAny(ctx, q, envID, slugs, exclude)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		if _, err := q.BumpServiceGeneration(ctx, r.ID); err != nil {
			return nil, fmt.Errorf("stage %s: %w", r.Slug, err)
		}
	}
	return refs, nil
}

// groupServices returns the service rows of the primary's group (primary first).
func (s *Service) groupServices(ctx context.Context, q *db.Queries, primaryID uuid.UUID) ([]db.Service, error) {
	all := []uuid.UUID{primaryID}
	rows, err := q.ListDatabaseReplicasOfPrimary(ctx, primaryID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		all = append(all, r.ServiceID)
	}
	var out []db.Service
	for _, id := range all {
		sc, err := services.LoadScopeInternal(ctx, q, id)
		if err != nil {
			continue
		}
		out = append(out, sc.Service)
	}
	return out, nil
}

// serverFacts decodes a server row's facts.
func serverFacts(srv db.Server) agentprotocol.Facts {
	var f agentprotocol.Facts
	_ = json.Unmarshal(srv.Facts, &f)
	return f
}

func hasCap(srv db.Server, capability string) bool {
	return slices.Contains(serverFacts(srv).Capabilities, capability)
}

// maxSlotWALKeepMB is max_slot_wal_keep_size: min(4 GiB, 10 % of the primary
// server's disk), at least 512 MiB. It bounds the disk a dead replica can pin.
func maxSlotWALKeepMB(srv db.Server) int {
	disk := serverFacts(srv).DiskTotal
	if disk == 0 {
		return maxWALKeepCapMB
	}
	mb := int(disk / (1 << 20) / 10)
	return min(maxWALKeepCapMB, max(512, mb))
}

// loadServer loads a server of the caller's workspace.
func (s *Service) loadServer(ctx context.Context, q *db.Queries, ws, id uuid.UUID) (db.Server, error) {
	return q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
}

// serviceRunning reports whether a database service is expected to be running
// (deployed, not stopped, and not observed as anything else lately).
func (s *Service) serviceRunning(sv db.Service) (bool, string) {
	if sv.DesiredState == services.DesiredStopped {
		return false, "the database is stopped"
	}
	if sv.CurrentDeploymentID == nil {
		return false, "the database has not been deployed yet"
	}
	if len(sv.Observed) > 0 && sv.ObservedAt != nil && s.now().Sub(*sv.ObservedAt) < 3*time.Minute {
		var o agentprotocol.ObservedService
		if json.Unmarshal(sv.Observed, &o) == nil && o.State != "" && o.State != agentprotocol.StateRunning {
			return false, "the database is " + o.State
		}
	}
	return true, ""
}

// promotionRunningIn reports a running promotion that involves the group of
// primaryID (as old primary or as a replica of it).
func (s *Service) promotionRunningIn(ctx context.Context, q *db.Queries, primaryID uuid.UUID) (bool, error) {
	running, err := q.ListRunningDatabasePromotions(ctx)
	if err != nil {
		return false, err
	}
	if len(running) == 0 {
		return false, nil
	}
	replicas, err := q.ListDatabaseReplicasOfPrimary(ctx, primaryID)
	if err != nil {
		return false, err
	}
	in := map[uuid.UUID]bool{primaryID: true}
	for _, r := range replicas {
		in[r.ServiceID] = true
	}
	for _, p := range running {
		if in[p.OldPrimaryServiceID] || in[p.ReplicaServiceID] {
			return true, nil
		}
	}
	return false, nil
}

// ReplicaGuard is the delete and move guard of services (services.DatabaseHook).

// BeforeDelete vetoes deleting a primary that has replicas and a replica that
// was not removed through the replica API (its slot would keep pinning WAL on
// the primary).
func (s *Service) BeforeDelete(ctx context.Context, tx pgx.Tx, sc services.Scope) error {
	if sc.Service.Kind != services.KindDatabase {
		return nil
	}
	if skip, _ := ctx.Value(skipGuardKey{}).(bool); skip {
		return nil // the replica removal saga deletes the service itself
	}
	q := db.New(tx)
	if _, ok, err := s.replicaRow(ctx, q, sc.Service.ID); err != nil {
		return err
	} else if ok {
		return apierr.Conflict(CodeReplicaRemoveFirst, "this database is a read replica: remove it with Remove replica so its slot on the primary is dropped")
	}
	n, err := q.CountDatabaseReplicasOfPrimary(ctx, sc.Service.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return apierr.Conflict(CodeReplicasExist, "this database has read replicas: remove them first")
	}
	return nil
}

// BeforeMove vetoes moving a member of a replica group to another server: a
// move does not move volumes, and a replica's clone and links are tied to its
// server. Use Rebuild to put a replica elsewhere.
func (s *Service) BeforeMove(ctx context.Context, q *db.Queries, sc services.Scope, newServer *uuid.UUID) error {
	if sc.Service.Kind != services.KindDatabase {
		return nil
	}
	same := (newServer == nil) == (sc.Service.ServerID == nil) && (newServer == nil || *newServer == *sc.Service.ServerID)
	if same {
		return nil
	}
	if _, ok, err := s.replicaRow(ctx, q, sc.Service.ID); err != nil {
		return err
	} else if ok {
		return apierr.Conflict(CodeReplicaPinned, "a read replica cannot move to another server: remove it and add a replica on the other server")
	}
	n, err := q.CountDatabaseReplicasOfPrimary(ctx, sc.Service.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return apierr.Conflict(CodeReplicaPinned, "a primary with read replicas cannot move to another server: promote a replica on that server instead")
	}
	return nil
}

func trimName(s string) string { return strings.TrimSpace(s) }

func mustUnmarshal(raw json.RawMessage, v any) bool { return json.Unmarshal(raw, v) == nil }

// principalUser returns the user behind the caller, when it is a user.
func principalUser(ctx context.Context) (uuid.UUID, bool) {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.UserID != uuid.Nil {
		return p.UserID, true
	}
	return uuid.Nil, false
}

func valueOr(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
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

// upgradeReplicaBlocker explains why a major version change is blocked by the
// replica group: pg_upgrade carries neither pg_hba.conf nor physical slots, so a
// primary with replicas and a replica must be rebuilt, not converted.
func (s *Service) upgradeReplicaBlocker(ctx context.Context, q *db.Queries, sc services.Scope, spec engines.Spec) (string, error) {
	if spec.Engine != engines.Postgres {
		return "", nil
	}
	if spec.Replica != nil {
		return "This database is a read replica: remove it, upgrade the primary, then add replicas again.", nil
	}
	n, err := q.CountDatabaseReplicasOfPrimary(ctx, sc.Service.ID)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return fmt.Sprintf("This database has %d read replica(s): remove them, upgrade, then add replicas again (a major upgrade carries neither pg_hba.conf nor replication slots).", n), nil
	}
	return "", nil
}
