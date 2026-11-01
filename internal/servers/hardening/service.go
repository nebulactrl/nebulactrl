// Package hardening is the control-plane side of host hardening: a Lynis audit
// of each server, the curated safe fixes of internal/lynis, and the server's
// Security tab.
//
// The agent does the work on the host (directly, or through a privileged helper
// container when it runs in one). The control plane schedules a weekly audit,
// starts applies and rollbacks on request of an Owner or Admin, stores the
// parsed report and the state of every fix, and explains what is left. Nothing
// here ever touches SSH, the firewall or accounts: the catalog has no fix for
// them (see internal/lynis).
package hardening

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
	"github.com/nebulactrl/nebulactrl/internal/lynis"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Schedule and limits.
const (
	// SweepInterval is how often the sweep looks for servers due a weekly audit.
	SweepInterval = time.Hour
	// AuditEvery is the least time between two runs of a server's weekly audit.
	AuditEvery = 7 * 24 * time.Hour
	// HistoryKept is how many runs of a server are remembered.
	HistoryKept = 30
	// RecentRuns is how many runs the Security tab lists.
	RecentRuns = 10
)

// Actions recorded in the audit log.
const (
	ActionAudit    = "server.hardening.audit"    // a person started an audit
	ActionApply    = "server.hardening.apply"    // a person started "Apply safe fixes"
	ActionRollback = "server.hardening.rollback" // a person started an undo
	ActionSettings = "server.hardening.update"   // the weekly audit was switched
	ActionRun      = "server.hardening.run"      // a run finished (system), with the per-fix outcomes
)

// Fix states in a HardeningView beyond the agent's own.
const stateUnknown = "unknown"

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Dispatcher agents.Dispatcher
	Logger     *slog.Logger
}

// Service implements the hardening use cases and the hooks of the agents hub.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	disp agents.Dispatcher
	log  *slog.Logger
	now  func() time.Time
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{
		pool: d.Pool, q: db.New(d.Pool), bus: d.Bus, disp: d.Dispatcher,
		log: d.Logger.With("component", "host-hardening"), now: time.Now,
	}
}

// SupportsHardening reports whether the server's agent advertised
// agentprotocol.CapHostHardening. A server that has never connected has no
// facts yet and is assumed to enroll with a current agent.
func SupportsHardening(facts json.RawMessage) bool {
	if len(facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapHostHardening)
}

func (s *Service) publish(serverID uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: "server.updated", ResourceID: serverID.String()})
	}
}

func (s *Service) loadServer(ctx context.Context, scope string, id uuid.UUID) (db.Server, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
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

// requireAdmin limits changes to the host to Owners and Admins of the team.
func requireAdmin(ctx context.Context) error {
	return policy.RequireRole(ctx, nil, auth.RoleOwner, auth.RoleAdmin)
}

func principalUser(ctx context.Context) *uuid.UUID {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.UserID != uuid.Nil {
		id := p.UserID
		return &id
	}
	return nil
}

// ---- runs ----

// Audit starts a Lynis audit of the server now. installLynis lets the agent
// install Lynis when it is missing (the person clicking has asked for it).
func (s *Service) Audit(ctx context.Context, serverID uuid.UUID, installLynis bool) (HardeningRun, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return HardeningRun{}, err
	}
	if err := requireAdmin(ctx); err != nil {
		return HardeningRun{}, err
	}
	run, err := s.start(ctx, srv, agentprotocol.HardeningActionAudit, agentprotocol.HardeningTriggerManual, principalUser(ctx), nil, installLynis)
	if err != nil {
		return HardeningRun{}, err
	}
	return runView(run), nil
}

// Apply applies fixes: the default set when fixIDs is empty. Owners and Admins only.
func (s *Service) Apply(ctx context.Context, serverID uuid.UUID, fixIDs []string) (HardeningRun, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return HardeningRun{}, err
	}
	if err := requireAdmin(ctx); err != nil {
		return HardeningRun{}, err
	}
	if len(fixIDs) == 0 {
		fixIDs = lynis.Defaults()
	}
	if err := checkFixIDs(fixIDs); err != nil {
		return HardeningRun{}, err
	}
	run, err := s.start(ctx, srv, agentprotocol.HardeningActionApply, agentprotocol.HardeningTriggerManual, principalUser(ctx), fixIDs, false)
	if err != nil {
		return HardeningRun{}, err
	}
	return runView(run), nil
}

// Rollback undoes fixes NebulaCtrl applied. Owners and Admins only.
func (s *Service) Rollback(ctx context.Context, serverID uuid.UUID, fixIDs []string) (HardeningRun, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return HardeningRun{}, err
	}
	if err := requireAdmin(ctx); err != nil {
		return HardeningRun{}, err
	}
	if len(fixIDs) == 0 {
		return HardeningRun{}, apierr.BadRequest("no_fix_selected", "choose at least one fix to undo")
	}
	if err := checkFixIDs(fixIDs); err != nil {
		return HardeningRun{}, err
	}
	run, err := s.start(ctx, srv, agentprotocol.HardeningActionRollback, agentprotocol.HardeningTriggerManual, principalUser(ctx), fixIDs, false)
	if err != nil {
		return HardeningRun{}, err
	}
	return runView(run), nil
}

func checkFixIDs(ids []string) error {
	_, unknown := lynis.Select(ids)
	if len(unknown) > 0 {
		return apierr.BadRequest("unknown_fix", "unknown fix: "+unknown[0])
	}
	return nil
}

func (s *Service) canRun(ctx context.Context, srv db.Server) error {
	switch {
	case srv.Status == "revoked":
		return apierr.Unprocessable("server_revoked", "the server was revoked")
	case srv.Status != "online":
		return apierr.Conflict("server_offline", srv.Name+" is offline. Try again when it is back online.")
	case !SupportsHardening(srv.Facts):
		return apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old to audit the host; update the agent first")
	}
	pending, err := s.q.HasPendingServerHardeningRun(ctx, srv.ID)
	if err != nil {
		return fmt.Errorf("check pending run: %w", err)
	}
	if pending {
		return apierr.Conflict("hardening_running", "an audit or fix run on "+srv.Name+" is already in progress")
	}
	return nil
}

// start records a run and queues its command in one transaction.
func (s *Service) start(ctx context.Context, srv db.Server, action, trigger string, by *uuid.UUID, fixIDs []string, install bool) (db.ServerHardeningRun, error) {
	if err := s.canRun(ctx, srv); err != nil {
		return db.ServerHardeningRun{}, err
	}
	var run db.ServerHardeningRun
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var before *int32
		if st, err := q.GetServerHardeningState(ctx, srv.ID); err == nil {
			before = st.HardeningIndex
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if fixIDs == nil {
			fixIDs = []string{}
		}
		var err error
		if run, err = q.InsertServerHardeningRun(ctx, db.InsertServerHardeningRunParams{
			ServerID: srv.ID, WorkspaceID: srv.WorkspaceID, Action: action, Trigger: trigger, RequestedBy: by, FixIds: fixIDs, IndexBefore: before,
		}); err != nil {
			return fmt.Errorf("insert hardening run: %w", err)
		}
		raw, err := json.Marshal(agentprotocol.HardeningPayload{
			RunID: run.ID.String(), Action: action, Trigger: trigger, FixIDs: fixIDs, InstallLynis: install,
		})
		if err != nil {
			return err
		}
		cid, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: srv.ID, Kind: agentprotocol.KindHostHardening, Payload: raw, IdempotencyKey: "host_hardening:" + run.ID.String(),
		})
		if err != nil {
			return fmt.Errorf("enqueue host_hardening: %w", err)
		}
		if err := q.SetServerHardeningRunCommand(ctx, db.SetServerHardeningRunCommandParams{ID: run.ID, CommandID: &cid}); err != nil {
			return err
		}
		run.CommandID = &cid
		if by == nil {
			return nil // scheduled runs are audited when they finish
		}
		act := ActionAudit
		switch action {
		case agentprotocol.HardeningActionApply:
			act = ActionApply
		case agentprotocol.HardeningActionRollback:
			act = ActionRollback
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: act, ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name, "run": run.ID.String(), "fixes": fixIDs, "installLynis": install},
		})
	})
	if err != nil {
		return db.ServerHardeningRun{}, err
	}
	s.publish(srv.ID)
	return run, nil
}

// ---- settings ----

// SetWeekly switches the weekly audit of a server.
func (s *Service) SetWeekly(ctx context.Context, serverID uuid.UUID, weekly bool) (HardeningView, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return HardeningView{}, err
	}
	if err := requireAdmin(ctx); err != nil {
		return HardeningView{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).UpsertServerHardeningSettings(ctx, db.UpsertServerHardeningSettingsParams{
			ServerID: serverID, WorkspaceID: srv.WorkspaceID, Weekly: weekly,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionSettings, ResourceType: "server", ResourceID: serverID.String(), WorkspaceID: srv.WorkspaceID,
			Metadata: map[string]any{"server": srv.Name, "weekly": weekly},
		})
	})
	if err != nil {
		return HardeningView{}, fmt.Errorf("update hardening settings: %w", err)
	}
	s.publish(serverID)
	return s.view(ctx, srv)
}

// ---- sweep ----

// Sweep starts the weekly audits that are due. A server without Lynis answers
// with a skipped run; nothing is installed without a person asking.
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.q.ListServersDueForHardening(ctx)
	if err != nil {
		return fmt.Errorf("list servers due for a hardening audit: %w", err)
	}
	for _, r := range rows {
		if !SupportsHardening(r.Facts) {
			continue
		}
		srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: r.ID})
		if err != nil {
			s.log.WarnContext(ctx, "hardening sweep: load server", "server_id", r.ID, "err", err)
			continue
		}
		if _, err := s.start(ctx, srv, agentprotocol.HardeningActionAudit, agentprotocol.HardeningTriggerScheduled, nil, nil, false); err != nil {
			if _, expected := apierr.As(err); !expected { // a busy or offline server is not worth a log line
				s.log.WarnContext(ctx, "hardening sweep: start", "server_id", r.ID, "err", err)
			}
			continue
		}
		s.log.InfoContext(ctx, "weekly hardening audit started", "server_id", r.ID)
	}
	return nil
}

// ---- agent hooks ----

// Handles reports whether kind is a command of this domain.
func Handles(kind string) bool { return kind == agentprotocol.KindHostHardening }

// HandleResult applies the result of a host_hardening command. Results of
// other commands, and duplicates, are ignored.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	run, err := s.q.GetServerHardeningRunByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get hardening run: %w", err)
	}
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		msg := res.Error
		if msg == "" {
			msg = "The host did not complete the run."
		}
		return s.finish(ctx, run, "failed", nil, "", nil, "", msg)
	}
	var out agentprotocol.HardeningResult
	if err := json.Unmarshal(res.Data, &out); err != nil {
		return s.finish(ctx, run, "failed", nil, "", nil, "", "The agent's answer could not be read: "+err.Error())
	}
	status := "succeeded"
	if run.Action == agentprotocol.HardeningActionAudit && !out.LynisInstalled {
		status = "skipped"
		if out.Message == "" {
			out.Message = "Lynis is not installed on this server."
		}
	}
	return s.finish(ctx, run, status, &out, out.LynisVersion, out.Outcomes, out.Message, "")
}

// HandleUndelivered closes the run of a host_hardening command that never
// reached its agent (expired or canceled before being acknowledged).
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	run, err := s.q.GetServerHardeningRunByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get hardening run: %w", err)
	}
	return s.finish(ctx, run, "failed", nil, "", nil, "", msg)
}

func (s *Service) finish(ctx context.Context, run db.ServerHardeningRun, status string, out *agentprotocol.HardeningResult, version string, outcomes []agentprotocol.HardeningFixOutcome, message, errMsg string) error {
	var index *int32
	if out != nil && out.Report != nil {
		v := int32(out.Report.HardeningIndex)
		index = &v
	}
	var rawOutcomes json.RawMessage
	if len(outcomes) > 0 {
		rawOutcomes, _ = json.Marshal(outcomes)
	}
	var finished bool
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		_, err := q.FinishServerHardeningRun(ctx, db.FinishServerHardeningRunParams{
			ID: run.ID, Status: status, HardeningIndex: index, LynisVersion: version, Outcomes: rawOutcomes, Message: message, Error: errMsg,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a duplicate result
		}
		if err != nil {
			return err
		}
		finished = true
		if out != nil {
			if err := s.storeState(ctx, q, run, out, index); err != nil {
				return err
			}
		}
		if err := q.PruneServerHardeningRuns(ctx, db.PruneServerHardeningRunsParams{ServerID: run.ServerID, Keep: HistoryKept}); err != nil {
			return err
		}
		meta := map[string]any{
			"run": run.ID.String(), "action": run.Action, "trigger": run.Trigger, "status": status, "error": errMsg,
		}
		if index != nil {
			meta["hardeningIndex"] = *index
		}
		if run.IndexBefore != nil {
			meta["indexBefore"] = *run.IndexBefore
		}
		if len(outcomes) > 0 {
			per := map[string]string{}
			for _, o := range outcomes {
				per[o.ID] = o.Outcome
			}
			meta["outcomes"] = per
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRun, ResourceType: "server", ResourceID: run.ServerID.String(), WorkspaceID: run.WorkspaceID, Metadata: meta,
		})
	})
	if err != nil {
		return fmt.Errorf("finish hardening run: %w", err)
	}
	if finished {
		s.publish(run.ServerID)
	}
	return nil
}

func (s *Service) storeState(ctx context.Context, q *db.Queries, run db.ServerHardeningRun, out *agentprotocol.HardeningResult, index *int32) error {
	var report, fixes json.RawMessage
	if out.Report != nil {
		report, _ = json.Marshal(out.Report)
	}
	if len(out.Fixes) > 0 {
		fixes, _ = json.Marshal(out.Fixes)
	}
	reboot := out.RebootRequired
	if run.Action == agentprotocol.HardeningActionAudit {
		reboot = false // an audit after a reboot clears the flag
	} else if !reboot {
		if st, err := q.GetServerHardeningState(ctx, run.ServerID); err == nil {
			reboot = st.RebootRequired
		}
	}
	_, err := q.UpsertServerHardeningState(ctx, db.UpsertServerHardeningStateParams{
		ServerID: run.ServerID, WorkspaceID: run.WorkspaceID, LynisInstalled: out.LynisInstalled, LynisVersion: out.LynisVersion,
		Mode: out.Mode, HardeningIndex: index, Report: report, Fixes: fixes, RebootRequired: reboot,
	})
	if err != nil {
		return fmt.Errorf("store hardening state: %w", err)
	}
	return nil
}
