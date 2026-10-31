// Package agentupgrade is the control-plane side of agent self-upgrade.
//
// When the control plane runs a newer version than an agent, the agent can
// replace its own binary with the one this control plane serves (internal/agents
// download endpoints). The control plane
//
//   - decides who is eligible (Evaluate): online, new enough to know the
//     command, provably behind, and this control plane ships a binary for it;
//   - sends one agent_upgrade command per server with the SHA-256 of the binary
//     (the agent checks the download against it), either automatically when a
//     behind agent connects (instance setting, default on) or when an
//     administrator asks, one server at a time for the fleet action;
//   - follows the outcome: the agent's result says "installed", its next hello
//     with the target version says "done", a hello with another version (the
//     agent rolled back) says "failed".
//
// Every start and every outcome is audited.
package agentupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/version"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Upgrade states of a server (server_agent_upgrades.state).
const (
	StateQueued     = "queued"
	StateRestarting = "restarting"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
)

// Triggers.
const (
	TriggerAuto   = "auto"
	TriggerManual = "manual"
)

// Actions recorded in the audit log.
const (
	ActionUpgrade       = "server.agent.upgrade"         // a person started an upgrade
	ActionUpgradeAuto   = "server.agent.upgrade.auto"    // a connecting agent was upgraded automatically
	ActionUpgraded      = "server.agent.upgraded"        // the agent came back with the target version
	ActionUpgradeFailed = "server.agent.upgrade.failed"  // refused, rolled back or never finished
	ActionRollout       = "server.agent.upgrade.rollout" // a person started the staged fleet upgrade
	ActionRolloutHalt   = "server.agent.upgrade.halted"  // the staged fleet upgrade stopped at a failure
	ActionAutoSetting   = "instance.agent_auto_upgrade.update"
)

// Timings.
const (
	// StaleAfter is how long a queued or restarting upgrade blocks a new one: an
	// agent that does not come back within it is treated as lost.
	StaleAfter = 30 * time.Minute
	// RolloutServerTimeout is how long the staged fleet upgrade waits for one
	// server before it stops.
	RolloutServerTimeout = 10 * time.Minute
	// RolloutPoll is how often the fleet upgrade looks at the server it waits for.
	RolloutPoll = 10 * time.Second
)

// Error codes (apierr).
const (
	CodeServerOffline     = "server_offline"
	CodeAlreadyCurrent    = "agent_current"
	CodeAgentNewer        = "agent_newer"
	CodeAgentTooOld       = "agent_predates_upgrade"
	CodeUpgradeInProgress = "upgrade_in_progress"
	CodeNoAgentBinary     = "no_agent_binary"
	CodeNotUpgradable     = "agent_not_upgradable"
)

// BinarySource tells what agent binary this control plane serves.
// *agents.Hub implements it.
type BinarySource interface {
	AgentBinaryInfo(arch string) (agents.BinaryInfo, error)
}

// AdminChecker tells whether a user is the instance admin (the first account).
type AdminChecker interface {
	IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Dispatcher agents.Dispatcher
	Binaries   BinarySource
	Jobs       *jobs.Manager
	Admins     AdminChecker
	// Version returns the control plane's version (default version.Current).
	Version func() string
	Logger  *slog.Logger
}

// Service implements the agent upgrade use cases and the hub's hooks.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	bus    *events.Bus
	disp   agents.Dispatcher
	bins   BinarySource
	jm     *jobs.Manager
	admins AdminChecker
	ver    func() string
	log    *slog.Logger
	now    func() time.Time
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Version == nil {
		d.Version = version.Current
	}
	s := &Service{
		pool: d.Pool, bus: d.Bus, disp: d.Dispatcher, bins: d.Binaries, jm: d.Jobs, admins: d.Admins, ver: d.Version,
		log: d.Logger.With("component", "agent-upgrade"), now: time.Now,
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// ---- eligibility (pure) ------------------------------------------------------

// Eligibility says whether an agent can be upgraded now, and if not, why.
type Eligibility struct {
	Skew       version.Skew
	Supported  bool
	Upgradable bool
	// Code is the apierr code of the refusal ("" when Upgradable).
	Code string
	// Reason is the sentence the UI shows when Upgradable is false.
	Reason string
}

// Evaluate decides whether the agent of a server can be upgraded to the control
// plane's version. prev is the server's latest upgrade row (nil when none).
func Evaluate(status, agentVersion, controlVersion string, facts json.RawMessage, hasBinary func(arch string) bool, prev *db.ServerAgentUpgrade, now time.Time) Eligibility {
	e := Eligibility{Skew: version.Compute(agentVersion, controlVersion)}
	var f agentprotocol.Facts
	if len(facts) > 0 {
		_ = json.Unmarshal(facts, &f)
	}
	e.Supported = slices.Contains(f.Capabilities, agentprotocol.CapAgentUpgrade)
	switch {
	case status == "revoked":
		e.Code, e.Reason = CodeNotUpgradable, "The server is revoked."
	case status == "pending" || agentVersion == "":
		e.Code, e.Reason = CodeNotUpgradable, "The agent has not connected yet."
	case status != "online":
		e.Code, e.Reason = CodeServerOffline, "The server is offline; the agent can only be upgraded while it is connected."
	case e.Skew == version.SkewCurrent:
		e.Code, e.Reason = CodeAlreadyCurrent, "The agent runs the same version as the control plane."
	case e.Skew == version.SkewAhead:
		e.Code, e.Reason = CodeAgentNewer, "The agent is newer than the control plane. Upgrade the control plane first; agents are never downgraded."
	case !e.Supported:
		e.Code, e.Reason = CodeAgentTooOld, "This agent predates self-upgrade (or cannot replace its own binary here). Re-run the agent installer on the host once; later upgrades are automatic."
	case prev != nil && (prev.State == StateQueued || prev.State == StateRestarting) && now.Sub(prev.RequestedAt) < StaleAfter:
		e.Code, e.Reason = CodeUpgradeInProgress, "An upgrade of this agent is already running."
	case f.Arch == "" || (hasBinary != nil && !hasBinary(f.Arch)):
		e.Code, e.Reason = CodeNoAgentBinary, "This control plane does not ship an agent binary for the server's architecture."
	default:
		e.Upgradable = true
	}
	return e
}

// shouldAutoUpgrade is the automatic path's decision for a connecting agent: the
// setting is on, the agent is provably behind and eligible, and the same target
// did not fail on this server before (a retry needs an administrator).
func shouldAutoUpgrade(autoOn bool, e Eligibility, prev *db.ServerAgentUpgrade, target string) bool {
	if !autoOn || !e.Upgradable || !e.Skew.AutoUpgradable() {
		return false
	}
	if prev != nil && prev.State == StateFailed && prev.TargetVersion == target {
		return false
	}
	return true
}

func (s *Service) hasBinary(arch string) bool {
	if s.bins == nil {
		return false
	}
	_, err := s.bins.AgentBinaryInfo(arch)
	return err == nil
}

// publish announces an upgrade change of server serverID of team ws. A change
// that belongs to no team (the instance-wide auto-upgrade switch: nil ws) reaches
// instance administrators only.
func (s *Service) publish(serverID, ws uuid.UUID) {
	if s.bus == nil {
		return
	}
	if ws == uuid.Nil {
		s.bus.Publish(events.Event{Type: EventUpdated, ResourceID: "instance", Instance: true})
		return
	}
	s.bus.Publish(events.Event{Type: "server.updated", ResourceID: serverID.String(), WorkspaceID: ws.String()})
	s.bus.Publish(events.Event{Type: EventUpdated, ResourceID: "instance", WorkspaceID: ws.String()})
}

// EventUpdated is published when the upgrade settings or a server's upgrade changed.
const EventUpdated = "instance.upgrade.updated"

func principalUser(ctx context.Context) *uuid.UUID {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.UserID != uuid.Nil {
		id := p.UserID
		return &id
	}
	return nil
}

// ---- starting an upgrade -----------------------------------------------------

// start queues the agent_upgrade command for srv and records the attempt. The
// caller has authorized the action. A refusal is an apierr.
func (s *Service) start(ctx context.Context, srv db.Server, trigger string, by *uuid.UUID, force bool) (db.ServerAgentUpgrade, error) {
	target := s.ver()
	var prev *db.ServerAgentUpgrade
	if row, err := s.q.GetServerAgentUpgrade(ctx, srv.ID); err == nil {
		prev = &row
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.ServerAgentUpgrade{}, fmt.Errorf("get agent upgrade: %w", err)
	}
	agentVersion := ""
	if srv.AgentVersion != nil {
		agentVersion = *srv.AgentVersion
	}
	e := Evaluate(srv.Status, agentVersion, target, srv.Facts, s.hasBinary, prev, s.now())
	if !e.Upgradable {
		return db.ServerAgentUpgrade{}, refusal(e)
	}
	var f agentprotocol.Facts
	_ = json.Unmarshal(srv.Facts, &f)
	info, err := s.bins.AgentBinaryInfo(f.Arch)
	if err != nil {
		return db.ServerAgentUpgrade{}, apierr.Unprocessable(CodeNoAgentBinary, "this control plane does not ship an agent binary for "+f.Arch)
	}
	raw, err := json.Marshal(agentprotocol.UpgradePayload{Version: target, SHA256: info.SHA256, Size: info.Size, Force: force})
	if err != nil {
		return db.ServerAgentUpgrade{}, err
	}
	var row db.ServerAgentUpgrade
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cid, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: srv.ID, Kind: agentprotocol.KindAgentUpgrade, Payload: raw,
			IdempotencyKey: "agent_upgrade:" + srv.ID.String() + ":" + uuid.NewString(),
		})
		if err != nil {
			return fmt.Errorf("enqueue agent_upgrade: %w", err)
		}
		row, err = db.New(tx).UpsertServerAgentUpgrade(ctx, db.UpsertServerAgentUpgradeParams{
			ServerID: srv.ID, WorkspaceID: srv.WorkspaceID, Trigger: trigger, FromVersion: agentVersion, TargetVersion: target,
			CommandID: &cid, RequestedBy: by,
		})
		if err != nil {
			return fmt.Errorf("record agent upgrade: %w", err)
		}
		meta := map[string]any{"server": srv.Name, "from": agentVersion, "to": target, "trigger": trigger, "force": force}
		if by != nil {
			meta["requestedBy"] = by.String() // a rollout job runs without a principal
		}
		action := ActionUpgrade
		if trigger == TriggerAuto {
			action = ActionUpgradeAuto
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: meta,
		})
	})
	if err != nil {
		return db.ServerAgentUpgrade{}, err
	}
	s.publish(srv.ID, srv.WorkspaceID)
	return row, nil
}

func refusal(e Eligibility) error {
	switch e.Code {
	case CodeServerOffline, CodeAlreadyCurrent, CodeAgentNewer, CodeUpgradeInProgress:
		return apierr.Conflict(e.Code, e.Reason)
	}
	return apierr.Unprocessable(e.Code, e.Reason)
}

// ---- agent hooks ------------------------------------------------------------

// Handles reports whether kind is a command of this domain.
func Handles(kind string) bool { return kind == agentprotocol.KindAgentUpgrade }

// HandleResult follows the result of an agent_upgrade command: installed (the
// agent restarts when idle) or refused.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	row, err := s.q.GetServerAgentUpgradeByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get agent upgrade: %w", err)
	}
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		msg := res.Error
		if msg == "" {
			msg = "The agent did not install the new binary."
		}
		return s.finish(ctx, row, StateFailed, msg, []string{StateQueued, StateRestarting})
	}
	msg := "The new binary is installed; the agent restarts when its running work is done."
	changed, err := s.q.SetServerAgentUpgradeState(ctx, db.SetServerAgentUpgradeStateParams{
		ServerID: row.ServerID, State: StateRestarting, Message: msg, FromStates: []string{StateQueued},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // a duplicate result, or the hello already settled it
	}
	if err != nil {
		return fmt.Errorf("set agent upgrade state: %w", err)
	}
	s.publish(changed.ServerID, row.WorkspaceID)
	return nil
}

// HandleUndelivered fails the upgrade of an agent_upgrade command that never
// reached its agent.
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	row, err := s.q.GetServerAgentUpgradeByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get agent upgrade: %w", err)
	}
	return s.finish(ctx, row, StateFailed, msg, []string{StateQueued})
}

// finish moves an upgrade to a terminal state once, and audits it.
func (s *Service) finish(ctx context.Context, row db.ServerAgentUpgrade, state, message string, from []string) error {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		done, err := q.SetServerAgentUpgradeState(ctx, db.SetServerAgentUpgradeStateParams{
			ServerID: row.ServerID, State: state, Message: message, FromStates: from,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already settled
		}
		if err != nil {
			return err
		}
		action := ActionUpgraded
		if state == StateFailed {
			action = ActionUpgradeFailed
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "server", ResourceID: row.ServerID.String(), WorkspaceID: row.WorkspaceID,
			Metadata: map[string]any{"from": done.FromVersion, "to": done.TargetVersion, "trigger": done.Trigger, "message": message},
		})
	})
	if err != nil {
		return fmt.Errorf("finish agent upgrade: %w", err)
	}
	s.publish(row.ServerID, row.WorkspaceID)
	return nil
}

// OnHello is the hub's HelloHook: it settles an upgrade in flight from the
// version the agent reports, then upgrades a connecting agent that is behind
// when the automatic upgrade is on.
func (s *Service) OnHello(ctx context.Context, serverID uuid.UUID, hello agentprotocol.Hello) {
	srv, err := s.q.GetServerByID(ctx, serverID)
	if err != nil {
		return
	}
	target := s.ver()
	var prev *db.ServerAgentUpgrade
	if row, err := s.q.GetServerAgentUpgrade(ctx, serverID); err == nil {
		prev = &row
	}
	if prev != nil && (prev.State == StateQueued || prev.State == StateRestarting) {
		arrived := version.Compute(hello.AgentVersion, prev.TargetVersion) == version.SkewCurrent
		switch {
		case arrived:
			msg := fmt.Sprintf("Upgraded from %s to %s.", orDash(prev.FromVersion), hello.AgentVersion)
			if err := s.finish(ctx, *prev, StateSucceeded, msg, []string{StateQueued, StateRestarting}); err != nil {
				s.log.ErrorContext(ctx, "settle agent upgrade", "server_id", serverID, "err", err)
			}
			prev.State = StateSucceeded
		case prev.State == StateRestarting:
			msg := fmt.Sprintf("The agent restarted but still runs %s.", orDash(hello.AgentVersion))
			if r := hello.Facts.AgentUpgrade; r != nil && r.Message != "" && (r.State == agentprotocol.UpgradeRolledBack || r.State == agentprotocol.UpgradeAbandoned) {
				msg = "The agent rolled the upgrade back: " + r.Message
			}
			if err := s.finish(ctx, *prev, StateFailed, msg, []string{StateRestarting}); err != nil {
				s.log.ErrorContext(ctx, "settle agent upgrade", "server_id", serverID, "err", err)
			}
			prev.State = StateFailed
		}
	}
	if prev != nil && (prev.State == StateQueued || prev.State == StateRestarting) {
		return // still in flight
	}
	set, err := s.q.GetInstanceUpgrade(ctx)
	if err != nil {
		return
	}
	e := Evaluate(srv.Status, hello.AgentVersion, target, srv.Facts, s.hasBinary, prev, s.now())
	if !shouldAutoUpgrade(set.AgentAutoUpgrade, e, prev, target) {
		return
	}
	if _, err := s.start(ctx, srv, TriggerAuto, nil, false); err != nil {
		if _, expected := apierr.As(err); !expected {
			s.log.ErrorContext(ctx, "automatic agent upgrade", "server_id", serverID, "err", err)
		}
		return
	}
	s.log.InfoContext(ctx, "agent behind the control plane; upgrade started", "server_id", serverID, "agent", hello.AgentVersion, "control_plane", target)
}

func orDash(s string) string {
	if s == "" {
		return "an unknown version"
	}
	return s
}
