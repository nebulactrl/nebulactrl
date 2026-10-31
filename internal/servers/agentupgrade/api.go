package agentupgrade

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/version"
)

// ServerUpgradeView is one server's agent version against the control plane's.
type ServerUpgradeView struct {
	ServerID     uuid.UUID `json:"serverId"`
	Name         string    `json:"name"`
	Status       string    `json:"status" doc:"pending, online, offline or revoked."`
	AgentVersion string    `json:"agentVersion,omitempty"`
	Skew         string    `json:"skew" enum:"current,behind,ahead,unknown" doc:"The agent against the control plane: behind (older), ahead (newer), current, or unknown when a version is not a release version."`
	// CanSelfUpgrade is true when the agent advertises self-upgrade.
	CanSelfUpgrade bool   `json:"canSelfUpgrade"`
	Upgradable     bool   `json:"upgradable" doc:"The agent can be upgraded now."`
	Reason         string `json:"reason,omitempty" doc:"Why the agent cannot be upgraded now."`
	// The latest upgrade of this server's agent.
	State         string     `json:"state,omitempty" enum:"queued,restarting,succeeded,failed"`
	Trigger       string     `json:"trigger,omitempty" enum:"auto,manual"`
	TargetVersion string     `json:"targetVersion,omitempty"`
	Message       string     `json:"message,omitempty"`
	RequestedAt   *time.Time `json:"requestedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

// PreUpgradeBackupView is the outcome of the last backup the control plane took
// before an upgrade.
type PreUpgradeBackupView struct {
	At          time.Time `json:"at"`
	Status      string    `json:"status" enum:"succeeded,failed,skipped,forced"`
	Message     string    `json:"message,omitempty"`
	FromVersion string    `json:"fromVersion,omitempty"`
	ToVersion   string    `json:"toVersion,omitempty"`
}

// InstanceUpgradeView is the upgrade overview of the control plane.
type InstanceUpgradeView struct {
	ControlPlaneVersion string                `json:"controlPlaneVersion"`
	AgentAutoUpgrade    bool                  `json:"agentAutoUpgrade" doc:"Upgrade an agent that is behind the control plane as soon as it connects. Default on."`
	PreUpgradeBackup    *PreUpgradeBackupView `json:"preUpgradeBackup,omitempty"`
	Behind              int                   `json:"behind" doc:"Servers whose agent is behind the control plane."`
	Upgradable          int                   `json:"upgradable" doc:"Servers whose agent can be upgraded now."`
	Servers             []ServerUpgradeView   `json:"servers"`
}

// RolloutView is the answer of the fleet upgrade.
type RolloutView struct {
	ServerIDs []uuid.UUID `json:"serverIds" doc:"The servers in upgrade order. They are upgraded one at a time; the rollout stops at the first failure."`
}

func requireAdmin(ctx context.Context) error {
	return policy.RequireRole(ctx, nil, auth.RoleOwner, auth.RoleAdmin)
}

func (s *Service) serverView(srv db.Server, up *db.ServerAgentUpgrade, target string) ServerUpgradeView {
	agentVersion := ""
	if srv.AgentVersion != nil {
		agentVersion = *srv.AgentVersion
	}
	e := Evaluate(srv.Status, agentVersion, target, srv.Facts, s.hasBinary, up, s.now())
	v := ServerUpgradeView{
		ServerID: srv.ID, Name: srv.Name, Status: srv.Status, AgentVersion: agentVersion, Skew: string(e.Skew),
		CanSelfUpgrade: e.Supported, Upgradable: e.Upgradable, Reason: e.Reason,
	}
	if agentVersion == "" {
		v.Skew = string(version.SkewUnknown)
	}
	if up != nil {
		v.State, v.Trigger, v.TargetVersion, v.Message = up.State, up.Trigger, up.TargetVersion, up.Message
		ra := up.RequestedAt
		v.RequestedAt, v.FinishedAt = &ra, up.FinishedAt
	}
	return v
}

// Get returns the upgrade overview: the control plane's version, the automatic
// upgrade setting, the last pre-upgrade backup and every server's skew.
func (s *Service) Get(ctx context.Context) (InstanceUpgradeView, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return InstanceUpgradeView{}, err
	}
	ws := policy.WorkspaceID(ctx)
	set, err := s.q.GetInstanceUpgrade(ctx)
	if err != nil {
		return InstanceUpgradeView{}, fmt.Errorf("instance upgrade: %w", err)
	}
	servers, err := s.q.ListServersForFleet(ctx, ws)
	if err != nil {
		return InstanceUpgradeView{}, fmt.Errorf("list servers: %w", err)
	}
	ups, err := s.q.ListServerAgentUpgrades(ctx, ws)
	if err != nil {
		return InstanceUpgradeView{}, fmt.Errorf("list agent upgrades: %w", err)
	}
	byServer := map[uuid.UUID]*db.ServerAgentUpgrade{}
	for i := range ups {
		byServer[ups[i].ServerID] = &ups[i]
	}
	target := s.ver()
	v := InstanceUpgradeView{ControlPlaneVersion: target, AgentAutoUpgrade: set.AgentAutoUpgrade, Servers: []ServerUpgradeView{}}
	if set.PreUpgradeAt != nil && set.PreUpgradeStatus != "" {
		v.PreUpgradeBackup = &PreUpgradeBackupView{
			At: *set.PreUpgradeAt, Status: set.PreUpgradeStatus, Message: set.PreUpgradeMessage,
			FromVersion: set.PreUpgradeFromVersion, ToVersion: set.PreUpgradeToVersion,
		}
	}
	for _, srv := range servers {
		if srv.Status == "revoked" {
			continue
		}
		sv := s.serverView(srv, byServer[srv.ID], target)
		if sv.Skew == string(version.SkewBehind) {
			v.Behind++
		}
		if sv.Upgradable {
			v.Upgradable++
		}
		v.Servers = append(v.Servers, sv)
	}
	return v, nil
}

// SetAutoUpgrade switches the automatic agent upgrade (instance admin only).
func (s *Service) SetAutoUpgrade(ctx context.Context, on bool) (InstanceUpgradeView, error) {
	if err := policy.Require(ctx, policy.ScopeServersWrite, nil); err != nil {
		return InstanceUpgradeView{}, err
	}
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return InstanceUpgradeView{}, err
	}
	if s.admins == nil {
		return InstanceUpgradeView{}, apierr.Forbidden()
	}
	ok, err := s.admins.IsInstanceAdmin(ctx, p.UserID)
	if err != nil {
		return InstanceUpgradeView{}, err
	}
	if !ok {
		return InstanceUpgradeView{}, apierr.Forbidden()
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).SetAgentAutoUpgrade(ctx, on); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionAutoSetting, ResourceType: "instance", ResourceID: "upgrade", WorkspaceID: p.WorkspaceID,
			Metadata: map[string]any{"agentAutoUpgrade": on},
		})
	})
	if err != nil {
		return InstanceUpgradeView{}, err
	}
	s.publish(uuid.Nil, uuid.Nil)
	return s.Get(ctx)
}

func (s *Service) loadServer(ctx context.Context, id uuid.UUID) (db.Server, error) {
	if err := policy.Require(ctx, policy.ScopeServersWrite, nil); err != nil {
		return db.Server{}, err
	}
	if err := requireAdmin(ctx); err != nil {
		return db.Server{}, err
	}
	ws := policy.WorkspaceID(ctx)
	row, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Server{}, apierr.NotFound("server")
	}
	if err != nil {
		return db.Server{}, fmt.Errorf("load server: %w", err)
	}
	return row, nil
}

// UpgradeServer upgrades one server's agent now. With force the agent installs
// a version it rolled back earlier.
func (s *Service) UpgradeServer(ctx context.Context, serverID uuid.UUID, force bool) (ServerUpgradeView, error) {
	srv, err := s.loadServer(ctx, serverID)
	if err != nil {
		return ServerUpgradeView{}, err
	}
	row, err := s.start(ctx, srv, TriggerManual, principalUser(ctx), force)
	if err != nil {
		return ServerUpgradeView{}, err
	}
	return s.serverView(srv, &row, s.ver()), nil
}

// UpgradeAll starts the staged upgrade of the given servers (all upgradable
// servers of the team when none are given): one at a time, each confirmed by its
// agent's reconnect before the next starts; the first failure stops the rollout.
func (s *Service) UpgradeAll(ctx context.Context, ids []uuid.UUID, force bool) (RolloutView, error) {
	if err := policy.Require(ctx, policy.ScopeServersWrite, nil); err != nil {
		return RolloutView{}, err
	}
	if err := requireAdmin(ctx); err != nil {
		return RolloutView{}, err
	}
	if s.jm == nil {
		return RolloutView{}, errors.New("no job manager")
	}
	ws := policy.WorkspaceID(ctx)
	servers, err := s.q.ListServersForFleet(ctx, ws)
	if err != nil {
		return RolloutView{}, fmt.Errorf("list servers: %w", err)
	}
	ups, err := s.q.ListServerAgentUpgrades(ctx, ws)
	if err != nil {
		return RolloutView{}, fmt.Errorf("list agent upgrades: %w", err)
	}
	byServer := map[uuid.UUID]*db.ServerAgentUpgrade{}
	for i := range ups {
		byServer[ups[i].ServerID] = &ups[i]
	}
	target := s.ver()
	sort.SliceStable(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	var order []uuid.UUID
	for _, srv := range servers {
		if len(ids) > 0 && !slices.Contains(ids, srv.ID) {
			continue
		}
		if s.serverView(srv, byServer[srv.ID], target).Upgradable {
			order = append(order, srv.ID)
		}
	}
	for _, id := range ids {
		if !slices.ContainsFunc(servers, func(sv db.Server) bool { return sv.ID == id }) {
			return RolloutView{}, apierr.NotFound("server")
		}
	}
	if len(order) == 0 {
		return RolloutView{}, apierr.Conflict("nothing_to_upgrade", "No selected agent can be upgraded now: each is current, offline, newer than the control plane or too old to upgrade itself.")
	}
	args := RolloutArgs{ServerIDs: order, StartedAt: s.now().UTC(), By: principalUser(ctx), Force: force}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.jm.InsertTx(ctx, tx, args, nil); err != nil {
			return fmt.Errorf("queue rollout: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRollout, ResourceType: "instance", ResourceID: "upgrade", WorkspaceID: ws,
			Metadata: map[string]any{"servers": len(order), "to": target, "force": force},
		})
	})
	if err != nil {
		return RolloutView{}, err
	}
	return RolloutView{ServerIDs: order}, nil
}
