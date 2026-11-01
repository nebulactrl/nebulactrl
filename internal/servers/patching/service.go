// Package patching is the control-plane side of server patching: which OS
// updates are pending on each server, whether it needs a reboot, and the two
// actions that change the host, "Apply updates" and "Reboot".
//
// The agent does the work on the host (see internal/agent/patching): directly
// when it is installed there, through a privileged helper container when it
// runs in one. The control plane has a sweep that asks each online server for
// its state at most every CheckEvery, stores what comes back, and tells the team
// once when a host starts needing a reboot (server.reboot_required). The two
// actions are reserved to the instance admin, need the server's name typed as
// confirmation, are audited, and on a server that runs production services (or
// the control plane itself) need an explicit acknowledgement and, for a token
// that asks for approvals, the production approval.
package patching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
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
	"github.com/nebulactrl/nebulactrl/internal/hostpatch"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Schedule and limits.
const (
	// SweepInterval is how often the sweep looks for servers due a check.
	SweepInterval = 30 * time.Minute
	// CheckEvery is the least time between two scheduled checks of a server (and
	// between any two runs the sweep counts).
	CheckEvery = 6 * time.Hour
	// StaleAfter is the age after which a check is shown as stale.
	StaleAfter = 2 * CheckEvery
	// HistoryKept is how many runs of a server are remembered.
	HistoryKept = 30
	// RecentRuns is how many runs the view lists.
	RecentRuns = 10
	// WatchInterval is how often runs waiting for a rebooted server are looked at.
	WatchInterval = 15 * time.Second
	// RebootDeadline is how long a rebooting server may stay away.
	RebootDeadline = 20 * time.Minute
	// verifyEvery is the least time between two checks that try to prove a reboot.
	verifyEvery = 30 * time.Second
	// verifyAfter is how long after scheduling the watcher starts to verify: the
	// agent must have had time to go down.
	verifyAfter = 8 * time.Second
	// noBootIDGrace is how long a reboot of a host that reports no boot id waits.
	noBootIDGrace = 3 * time.Minute
)

// Actions recorded in the audit log.
const (
	ActionCheck  = "server.patches.check"  // a person started a check
	ActionApply  = "server.patches.apply"  // a person started "Apply updates"
	ActionReboot = "server.patches.reboot" // a person started a reboot
	ActionRun    = "server.patches.run"    // an upgrade or reboot finished (system)
)

// Statuses of a run.
const (
	StatusPending   = "pending"
	StatusRebooting = "rebooting"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Deps are the collaborators of Service.
type Deps struct {
	Pool       *pgxpool.Pool
	Bus        *events.Bus
	Dispatcher agents.Dispatcher
	Notifier   notifications.Emitter
	Logger     *slog.Logger
}

// Service implements the patching use cases and the hooks of the agents hub.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	disp agents.Dispatcher
	not  notifications.Emitter
	log  *slog.Logger
	now  func() time.Time
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{
		pool: d.Pool, q: db.New(d.Pool), bus: d.Bus, disp: d.Dispatcher, not: d.Notifier,
		log: d.Logger.With("component", "server-patching"), now: time.Now,
	}
}

// SupportsPatching reports whether the server's agent advertised
// agentprotocol.CapHostPatching. A server that has never connected has no facts
// yet and is assumed to enroll with a current agent.
func SupportsPatching(facts json.RawMessage) bool {
	if len(facts) == 0 {
		return true
	}
	var f agentprotocol.Facts
	if err := json.Unmarshal(facts, &f); err != nil || f.OS == "" {
		return true
	}
	return slices.Contains(f.Capabilities, agentprotocol.CapHostPatching)
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

// requireAdmin limits checks to Owners and Admins of the team.
func requireAdmin(ctx context.Context) error {
	return policy.RequireRole(ctx, nil, auth.RoleOwner, auth.RoleAdmin)
}

// requireInstanceAdmin limits the actions that change the host to the instance
// admin (the earliest-created user, or an admin-scoped token acting for them):
// upgrading packages and rebooting are host-root operations.
func (s *Service) requireInstanceAdmin(ctx context.Context) error {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return err
	}
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return apierr.Forbidden()
	}
	first, err := s.q.FirstUserID(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.Forbidden()
	}
	if err != nil {
		return fmt.Errorf("first user: %w", err)
	}
	if first != p.UserID {
		return apierr.Forbidden()
	}
	return nil
}

func principalUser(ctx context.Context) *uuid.UUID {
	if p, ok := auth.PrincipalFrom(ctx); ok && p.UserID != uuid.Nil {
		id := p.UserID
		return &id
	}
	return nil
}

// ---- starting runs ----

// ApplyInput is the body of an upgrade request.
type ApplyInput struct {
	// Confirm must be the server's name.
	Confirm string
	// SecurityOnly upgrades only the packages with a security update.
	SecurityOnly bool
	// AcknowledgeProduction accepts that production services run on the server.
	AcknowledgeProduction bool
}

// RebootInput is the body of a reboot request.
type RebootInput struct {
	Confirm               string
	AcknowledgeProduction bool
}

// Check starts a check of the server now. It only reads the host (and refreshes
// the package lists). Owners and Admins only.
func (s *Service) Check(ctx context.Context, serverID uuid.UUID) (PatchRun, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return PatchRun{}, err
	}
	if err := requireAdmin(ctx); err != nil {
		return PatchRun{}, err
	}
	run, err := s.start(ctx, srv, startSpec{action: agentprotocol.PatchingActionCheck, trigger: agentprotocol.PatchingTriggerManual, by: principalUser(ctx)})
	if err != nil {
		return PatchRun{}, err
	}
	return runView(run, false), nil
}

// Apply upgrades the server's packages. Instance admin only; the server's name
// is the confirmation; production servers need the acknowledgement.
func (s *Service) Apply(ctx context.Context, serverID uuid.UUID, in ApplyInput) (PatchRun, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return PatchRun{}, err
	}
	if err := s.requireInstanceAdmin(ctx); err != nil {
		return PatchRun{}, err
	}
	if in.Confirm != srv.Name {
		return PatchRun{}, apierr.Unprocessable("confirmation_mismatch", "type the server's name to upgrade its packages")
	}
	state, err := s.q.GetServerPatchState(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && state.CheckedAt == nil) {
		return PatchRun{}, apierr.Conflict("no_check_yet", "check "+srv.Name+" for updates first, then review the plan")
	}
	if err != nil {
		return PatchRun{}, fmt.Errorf("get patch state: %w", err)
	}
	if !state.Supported {
		return PatchRun{}, apierr.Unprocessable("patching_unsupported", srv.Name+" has no supported package manager (apt or dnf)")
	}
	if state.Updates == 0 || (in.SecurityOnly && state.SecurityUpdates == 0) {
		return PatchRun{}, apierr.Unprocessable("no_updates", "there is nothing to upgrade; check for updates again")
	}
	prod, err := s.productionServices(ctx, serverID)
	if err != nil {
		return PatchRun{}, err
	}
	pkgs := packagesOf(state)
	effects := []string{"Upgrades the operating system packages of " + srv.Name + " as root."}
	if hostpatch.RestartsDocker(pkgs) {
		effects = append(effects, "Docker is among the packages: its daemon restarts and containers on the server may stop for a moment.")
	}
	if err := s.guard(ctx, srv, prod, in.AcknowledgeProduction, "patch_server", "Upgrade the packages of "+srv.Name, effects, false, in.SecurityOnly); err != nil {
		return PatchRun{}, err
	}
	run, err := s.start(ctx, srv, startSpec{
		action: agentprotocol.PatchingActionApply, trigger: agentprotocol.PatchingTriggerManual, by: principalUser(ctx),
		securityOnly: in.SecurityOnly, updatesBefore: intPtr(&state.Updates), production: prod,
	})
	if err != nil {
		return PatchRun{}, err
	}
	return runView(run, false), nil
}

// Reboot reboots the server. Instance admin only; the server's name is the
// confirmation; production servers need the acknowledgement. The run stays
// "rebooting" until the agent is back on a new boot.
func (s *Service) Reboot(ctx context.Context, serverID uuid.UUID, in RebootInput) (PatchRun, error) {
	srv, err := s.loadServer(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return PatchRun{}, err
	}
	if err := s.requireInstanceAdmin(ctx); err != nil {
		return PatchRun{}, err
	}
	if in.Confirm != srv.Name {
		return PatchRun{}, apierr.Unprocessable("confirmation_mismatch", "type the server's name to reboot it")
	}
	prod, err := s.productionServices(ctx, serverID)
	if err != nil {
		return PatchRun{}, err
	}
	effects := []string{"Every service on " + srv.Name + " goes down while it reboots and starts again when Docker is back."}
	if err := s.guard(ctx, srv, prod, in.AcknowledgeProduction, "reboot_server", "Reboot "+srv.Name, effects, true, nil); err != nil {
		return PatchRun{}, err
	}
	run, err := s.start(ctx, srv, startSpec{
		action: agentprotocol.PatchingActionReboot, trigger: agentprotocol.PatchingTriggerManual, by: principalUser(ctx), production: prod,
	})
	if err != nil {
		return PatchRun{}, err
	}
	return runView(run, false), nil
}

func (s *Service) productionServices(ctx context.Context, serverID uuid.UUID) ([]ProductionService, error) {
	rows, err := s.q.ListServerProductionServices(ctx, &serverID)
	if err != nil {
		return nil, fmt.Errorf("list production services: %w", err)
	}
	out := make([]ProductionService, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProductionService{
			ServiceID: r.ServiceID, Name: r.ServiceName, EnvironmentID: r.EnvironmentID, Environment: r.EnvironmentName,
			ProjectID: r.ProjectID, Project: r.ProjectName,
		})
	}
	return out, nil
}

// guard is the production guard of the two actions. A server that runs
// production services (or the control plane itself) needs the explicit
// acknowledgement; a principal that must obtain approvals for production
// changes also needs one per production environment on the server.
func (s *Service) guard(ctx context.Context, srv db.Server, prod []ProductionService, ack bool, op, summary string, effects []string, irreversible bool, args ...any) error {
	if len(prod) == 0 && !srv.IsControlPlane {
		return nil
	}
	if !ack {
		var what []string
		if len(prod) > 0 {
			what = append(what, fmt.Sprintf("%d production service(s) (%s)", len(prod), serviceList(prod, 4)))
		}
		if srv.IsControlPlane {
			what = append(what, "the NebulaCtrl control plane itself")
		}
		return apierr.Unprocessable("production_ack_required",
			srv.Name+" runs "+strings.Join(what, " and ")+". Set acknowledgeProduction to confirm you accept the downtime or restart.")
	}
	byEnv := map[uuid.UUID][]ProductionService{}
	var order []uuid.UUID
	for _, p := range prod {
		if _, ok := byEnv[p.EnvironmentID]; !ok {
			order = append(order, p.EnvironmentID)
		}
		byEnv[p.EnvironmentID] = append(byEnv[p.EnvironmentID], p)
	}
	ws := policy.WorkspaceID(ctx)
	for _, envID := range order {
		list := byEnv[envID]
		if !prodguard.Applies(ctx, true) {
			break
		}
		targets := make([]prodguard.ApprovalTarget, 0, len(list))
		for _, p := range list {
			targets = append(targets, prodguard.ApprovalTarget{Kind: "service", ID: p.ServiceID, Name: p.Name})
		}
		scope := prodguard.ProdScope{
			WorkspaceID: ws, ProjectID: list[0].ProjectID, EnvironmentID: envID, Project: list[0].Project, Environment: list[0].Environment,
			IsProduction: true, Summary: summary, Effects: effects, Irreversible: irreversible, Targets: targets,
		}
		digestArgs := append([]any{srv.ID}, args...)
		if err := prodguard.Check(ctx, scope, op, prodguard.Digest(op, digestArgs...)); err != nil {
			return err
		}
	}
	return nil
}

func serviceList(prod []ProductionService, max int) string {
	names := make([]string, 0, max+1)
	for i, p := range prod {
		if i == max {
			names = append(names, fmt.Sprintf("and %d more", len(prod)-max))
			break
		}
		names = append(names, p.Project+"/"+p.Name)
	}
	return strings.Join(names, ", ")
}

type startSpec struct {
	action, trigger string
	by              *uuid.UUID
	securityOnly    bool
	updatesBefore   *int
	production      []ProductionService
}

func (s *Service) canRun(ctx context.Context, srv db.Server) error {
	switch {
	case srv.Status == "revoked":
		return apierr.Unprocessable("server_revoked", "the server was revoked")
	case srv.Status != "online":
		return apierr.Conflict("server_offline", srv.Name+" is offline. Try again when it is back online.")
	case !SupportsPatching(srv.Facts):
		return apierr.Unprocessable("agent_too_old", "the agent on "+srv.Name+" is too old to manage updates; update the agent first")
	}
	active, err := s.q.HasActiveServerPatchRun(ctx, srv.ID)
	if err != nil {
		return fmt.Errorf("check active run: %w", err)
	}
	if active {
		return apierr.Conflict("patching_running", "a check, upgrade or reboot of "+srv.Name+" is already in progress")
	}
	return nil
}

// start records a run and queues its command in one transaction.
func (s *Service) start(ctx context.Context, srv db.Server, sp startSpec) (db.ServerPatchRun, error) {
	if err := s.canRun(ctx, srv); err != nil {
		return db.ServerPatchRun{}, err
	}
	var run db.ServerPatchRun
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var before *int32
		if sp.updatesBefore != nil {
			v := int32(*sp.updatesBefore)
			before = &v
		}
		var err error
		if run, err = q.InsertServerPatchRun(ctx, db.InsertServerPatchRunParams{
			ServerID: srv.ID, WorkspaceID: srv.WorkspaceID, Action: sp.action, Trigger: sp.trigger,
			RequestedBy: sp.by, SecurityOnly: sp.securityOnly, UpdatesBefore: before,
		}); err != nil {
			return fmt.Errorf("insert patch run: %w", err)
		}
		raw, err := json.Marshal(agentprotocol.PatchingPayload{
			RunID: run.ID.String(), Action: sp.action, Trigger: sp.trigger, SecurityOnly: sp.securityOnly,
		})
		if err != nil {
			return err
		}
		cid, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: srv.ID, Kind: agentprotocol.KindHostPatching, Payload: raw, IdempotencyKey: "host_patching:" + run.ID.String(),
		})
		if err != nil {
			return fmt.Errorf("enqueue host_patching: %w", err)
		}
		if err := q.SetServerPatchRunCommand(ctx, db.SetServerPatchRunCommandParams{ID: run.ID, CommandID: &cid}); err != nil {
			return err
		}
		run.CommandID = &cid
		if sp.by == nil {
			return nil // scheduled checks are not audited
		}
		act := ActionCheck
		switch sp.action {
		case agentprotocol.PatchingActionApply:
			act = ActionApply
		case agentprotocol.PatchingActionReboot:
			act = ActionReboot
		}
		meta := map[string]any{"server": srv.Name, "run": run.ID.String(), "securityOnly": sp.securityOnly, "controlPlane": srv.IsControlPlane}
		if sp.updatesBefore != nil {
			meta["updates"] = *sp.updatesBefore
		}
		if len(sp.production) > 0 {
			names := make([]string, 0, len(sp.production))
			for _, p := range sp.production {
				names = append(names, p.Project+"/"+p.Environment+"/"+p.Name)
			}
			meta["productionServices"] = names
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: act, ResourceType: "server", ResourceID: srv.ID.String(), WorkspaceID: srv.WorkspaceID, Metadata: meta,
		})
	})
	if err != nil {
		return db.ServerPatchRun{}, err
	}
	s.publish(srv.ID)
	return run, nil
}

// ---- sweeps ----

// Sweep starts the checks that are due: online servers without any run for
// CheckEvery.
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.q.ListServersDueForPatchCheck(ctx, CheckEvery.Seconds())
	if err != nil {
		return fmt.Errorf("list servers due for a patch check: %w", err)
	}
	for _, r := range rows {
		if !SupportsPatching(r.Facts) {
			continue
		}
		srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: r.ID})
		if err != nil {
			s.log.WarnContext(ctx, "patch sweep: load server", "server_id", r.ID, "err", err)
			continue
		}
		if _, err := s.start(ctx, srv, startSpec{action: agentprotocol.PatchingActionCheck, trigger: agentprotocol.PatchingTriggerScheduled}); err != nil {
			if _, expected := apierr.As(err); !expected {
				s.log.WarnContext(ctx, "patch sweep: start", "server_id", r.ID, "err", err)
			}
			continue
		}
	}
	return nil
}

// WatchReboots looks at the runs waiting for a rebooted server: it fails the
// ones past the deadline (or whose server was revoked) and, once the server is
// online again, asks it for a check whose boot id proves the reboot happened.
func (s *Service) WatchReboots(ctx context.Context) error {
	rows, err := s.q.ListRebootingServerPatchRuns(ctx)
	if err != nil {
		return fmt.Errorf("list rebooting runs: %w", err)
	}
	now := s.now()
	for _, r := range rows {
		run := r.ServerPatchRun
		since := run.CreatedAt
		if run.RebootingSince != nil {
			since = *run.RebootingSince
		}
		switch {
		case r.ServerStatus == "revoked":
			err = s.finishRun(ctx, run, StatusFailed, nil, "", "The server was revoked while it was rebooting.")
		case now.Sub(since) > RebootDeadline:
			err = s.finishRun(ctx, run, StatusFailed, nil, "",
				"The server did not come back within "+RebootDeadline.String()+". Check its console; running services stay stopped until it boots.")
		case r.ServerStatus == "online" && now.Sub(since) > verifyAfter &&
			(run.VerifySentAt == nil || now.Sub(*run.VerifySentAt) > verifyEvery) &&
			(run.BootIDBefore != "" || now.Sub(since) > noBootIDGrace):
			err = s.sendVerify(ctx, run)
		default:
			continue
		}
		if err != nil {
			s.log.WarnContext(ctx, "patch watcher", "run_id", run.ID, "err", err)
		}
	}
	return nil
}

// sendVerify queues a check for a rebooting run. The check's result carries the
// boot id; HandleResult finishes the run when it differs from the old one.
func (s *Service) sendVerify(ctx context.Context, run db.ServerPatchRun) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		raw, err := json.Marshal(agentprotocol.PatchingPayload{
			RunID: run.ID.String(), Action: agentprotocol.PatchingActionCheck, Trigger: agentprotocol.PatchingTriggerScheduled,
		})
		if err != nil {
			return err
		}
		cid, err := s.disp.Enqueue(ctx, tx, agents.NewCommand{
			ServerID: run.ServerID, Kind: agentprotocol.KindHostPatching, Payload: raw,
			IdempotencyKey: fmt.Sprintf("host_patching:verify:%s:%d", run.ID, s.now().UnixNano()),
		})
		if err != nil {
			return fmt.Errorf("enqueue verify check: %w", err)
		}
		return db.New(tx).SetServerPatchRunVerify(ctx, db.SetServerPatchRunVerifyParams{ID: run.ID, VerifyCommandID: &cid})
	})
}

// ---- agent hooks ----

// Handles reports whether kind is a command of this domain.
func Handles(kind string) bool { return kind == agentprotocol.KindHostPatching }

// HandleResult applies the result of a host_patching command. Results of other
// commands, and duplicates, are ignored.
func (s *Service) HandleResult(ctx context.Context, serverID, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	run, err := s.q.GetServerPatchRunByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get patch run: %w", err)
	}
	verify := run.VerifyCommandID != nil && *run.VerifyCommandID == commandID && (run.CommandID == nil || *run.CommandID != commandID)
	if verify && run.Status != StatusRebooting {
		return nil
	}
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		if verify {
			return nil // the watcher tries again
		}
		msg := res.Error
		if msg == "" {
			msg = "The host did not complete the run."
		}
		return s.finishRun(ctx, run, StatusFailed, nil, "", msg)
	}
	var out agentprotocol.PatchingResult
	if err := json.Unmarshal(res.Data, &out); err != nil {
		if verify {
			return nil
		}
		return s.finishRun(ctx, run, StatusFailed, nil, "", "The agent's answer could not be read: "+err.Error())
	}
	switch {
	case verify:
		before := run.BootIDBefore
		if out.State.BootID == "" || (before != "" && out.State.BootID == before) {
			return nil // still the old boot; the watcher asks again
		}
		return s.finishRun(ctx, run, StatusSucceeded, &out, "The server rebooted and its agent reconnected.", "")
	case run.Action == agentprotocol.PatchingActionReboot:
		return s.markRebooting(ctx, run, out)
	case run.Action == agentprotocol.PatchingActionApply:
		if out.ExitCode != 0 {
			msg := out.Message
			if msg == "" {
				msg = fmt.Sprintf("The package manager exited with status %d.", out.ExitCode)
			}
			return s.finishRun(ctx, run, StatusFailed, &out, "", msg)
		}
		return s.finishRun(ctx, run, StatusSucceeded, &out, out.Message, "")
	default: // check
		return s.finishRun(ctx, run, StatusSucceeded, &out, out.Message, "")
	}
}

// HandleUndelivered closes the run of a host_patching command that never
// reached its agent (expired or canceled before being acknowledged).
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	run, err := s.q.GetServerPatchRunByCommand(ctx, &commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get patch run: %w", err)
	}
	if run.VerifyCommandID != nil && *run.VerifyCommandID == commandID && (run.CommandID == nil || *run.CommandID != commandID) {
		return nil // a lost verification is retried by the watcher
	}
	return s.finishRun(ctx, run, StatusFailed, nil, "", msg)
}

func (s *Service) markRebooting(ctx context.Context, run db.ServerPatchRun, out agentprotocol.PatchingResult) error {
	msg := out.Message
	if msg == "" {
		msg = "Reboot scheduled."
	}
	_, err := s.q.MarkServerPatchRunRebooting(ctx, db.MarkServerPatchRunRebootingParams{ID: run.ID, BootIDBefore: out.State.BootID, Message: msg})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // a duplicate result
	}
	if err != nil {
		return fmt.Errorf("mark run rebooting: %w", err)
	}
	s.publish(run.ServerID)
	return nil
}

// finishRun closes a run, stores the state of the check it carried (state may be
// nil), audits upgrades and reboots, and announces a host that started needing a
// reboot.
func (s *Service) finishRun(ctx context.Context, run db.ServerPatchRun, status string, out *agentprotocol.PatchingResult, message, errMsg string) error {
	var updatesAfter, exit *int32
	var log string
	if out != nil {
		if run.Action != agentprotocol.PatchingActionReboot && out.State.Supported {
			v := int32(out.State.Updates)
			updatesAfter = &v
		}
		if run.Action == agentprotocol.PatchingActionApply {
			e := int32(out.ExitCode)
			exit = &e
			log = out.Log
		}
	}
	var finished, newlyRequired bool

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		_, err := q.FinishServerPatchRun(ctx, db.FinishServerPatchRunParams{
			ID: run.ID, Status: status, UpdatesAfter: updatesAfter, ExitCode: exit, Log: log, Message: message, Error: errMsg,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a duplicate result
		}
		if err != nil {
			return err
		}
		finished = true
		if out != nil {
			if newlyRequired, err = s.storeState(ctx, q, run, out.State); err != nil {
				return err
			}
		}
		if err := q.PruneServerPatchRuns(ctx, db.PruneServerPatchRunsParams{ServerID: run.ServerID, Keep: HistoryKept}); err != nil {
			return err
		}
		if run.Action == agentprotocol.PatchingActionCheck {
			return nil
		}
		meta := map[string]any{"run": run.ID.String(), "action": run.Action, "trigger": run.Trigger, "status": status, "error": errMsg, "securityOnly": run.SecurityOnly}
		if run.UpdatesBefore != nil {
			meta["updatesBefore"] = *run.UpdatesBefore
		}
		if updatesAfter != nil {
			meta["updatesAfter"] = *updatesAfter
		}
		if exit != nil {
			meta["exitCode"] = *exit
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRun, ResourceType: "server", ResourceID: run.ServerID.String(), WorkspaceID: run.WorkspaceID, Metadata: meta,
		})
	})
	if err != nil {
		return fmt.Errorf("finish patch run: %w", err)
	}

	if finished {
		s.publish(run.ServerID)
		if newlyRequired && out != nil {
			s.notifyRebootRequired(ctx, run, out.State)
		}
	}
	return nil
}

// storeState saves a check's state and reports whether the server newly needs a
// reboot (it did not in the previous state).
func (s *Service) storeState(ctx context.Context, q *db.Queries, run db.ServerPatchRun, st agentprotocol.PatchState) (bool, error) {
	var wasRequired bool
	if prev, err := q.GetServerPatchState(ctx, run.ServerID); err == nil {
		wasRequired = prev.RebootRequired
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	var pkgs json.RawMessage
	if len(st.Packages) > 0 {
		pkgs, _ = json.Marshal(st.Packages)
	}
	reboot := st.RebootPackages
	if reboot == nil {
		reboot = []string{}
	}
	if _, err := q.UpsertServerPatchState(ctx, db.UpsertServerPatchStateParams{
		ServerID: run.ServerID, WorkspaceID: run.WorkspaceID, Manager: st.Manager, Supported: st.Supported,
		Updates: int32(st.Updates), SecurityUpdates: int32(st.Security), Packages: pkgs, RebootRequired: st.RebootRequired,
		RebootPackages: reboot, BootID: st.BootID, Message: st.Message,
	}); err != nil {
		return false, fmt.Errorf("store patch state: %w", err)
	}
	return st.RebootRequired && !wasRequired, nil
}

func (s *Service) notifyRebootRequired(ctx context.Context, run db.ServerPatchRun, st agentprotocol.PatchState) {
	if s.not == nil {
		return
	}
	name := run.ServerID.String()
	if srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: run.ServerID}); err == nil {
		name = srv.Name
	}
	body := "Installed updates only take effect after a reboot. Services keep running until you reboot the server; do it when a short outage is acceptable."
	ev := notifications.Event{
		Type: notifications.TypeServerRebootRequired, ServerID: &run.ServerID, Severity: notifications.SeverityWarning,
		Title: name + " needs a reboot", Body: body,
	}
	if len(st.RebootPackages) > 0 {
		shown := st.RebootPackages
		if len(shown) > 5 {
			shown = shown[:5]
		}
		ev.Fields = map[string]string{"Packages": strings.Join(shown, ", ")}
	}
	notifications.Emit(ctx, s.not, ev)
}
