package servers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// removalStaleGrace is how much longer than the decommission timeout a removal
// may stay "removing" before it counts as lost (see Remove).
const removalStaleGrace = 15 * time.Minute

// Modes of Remove server.
const (
	// ModeClean empties the host first: the agent removes every container, network
	// and volume NebulaCtrl manages and then itself.
	ModeClean = "clean"
	// ModeForget revokes the agent and deletes the server without touching the
	// host: its containers keep running and its files stay.
	ModeForget = "forget"
)

// Removal states of a server (servers.removal_state).
const (
	RemovalRemoving = "removing"
	RemovalFailed   = "failed"
)

// ActionRemoved is the audit action of a removed server.
const ActionRemoved = "server.removed"

// DecommissionTimeout bounds how long the control plane waits for an agent to
// empty its host.
const DecommissionTimeout = 30 * time.Minute

// Outcomes of a removal, as the team deletion reports them.
const (
	// OutcomeCleaned: the agent emptied the host and removed itself.
	OutcomeCleaned = "cleaned"
	// OutcomeForgotten: the server was removed without touching its host.
	OutcomeForgotten = "forgotten"
)

// ServerRemovalState is the removal of a server that is under way or failed.
type ServerRemovalState struct {
	State     string     `json:"state" enum:"removing,failed" doc:"removing while the agent empties the host, failed when it could not (see error); remove again to retry, or forget the server"`
	Error     string     `json:"error,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// BlockingProject is a project with services placed on a server.
type BlockingProject struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Slug     string    `json:"slug"`
	System   bool      `json:"system" doc:"The team's hidden project of platform services (backup storage, log store, registry, tailnet)"`
	Services int       `json:"services" doc:"Live services of the project on this server"`
}

// Agent is the connection to a host's agent: whether it is online and a
// request that waits for its answer. *agents.Hub implements it; tests fake it.
type Agent interface {
	Online(serverID uuid.UUID) bool
	Request(ctx context.Context, serverID uuid.UUID, kind string, payload any, secrets map[string]string, timeout time.Duration) (agentprotocol.ResultFrame, error)
}

// TailnetDevices queues the removal of a device from the tailnet.
// *tailscale.Service implements it.
type TailnetDevices interface {
	EnqueueDeleteDevice(ctx context.Context, tx pgx.Tx, ws uuid.UUID, deviceID string) error
}

// removalDeps are the collaborators of Remove server.
type removalDeps struct {
	agent Agent
	jm    *jobs.Manager
	ts    TailnetDevices
	// enqueue queues the removal job inside tx (default: the job manager).
	enqueue func(ctx context.Context, tx pgx.Tx, args RemoveServerArgs) error
}

// SetAgent replaces the agent connection (tests).
func (s *Service) SetAgent(a Agent) { s.removal.agent = a }

// SetJobs installs the job manager that runs removals in the background.
func (s *Service) SetJobs(jm *jobs.Manager) { s.removal.jm = jm }

// SetTailnetDevices installs the tailnet integration that deletes the managed
// tailnet device of a removed server.
func (s *Service) SetTailnetDevices(t TailnetDevices) { s.removal.ts = t }

// SetRemovalQueue replaces how removal jobs are queued (tests).
func (s *Service) SetRemovalQueue(fn func(ctx context.Context, tx pgx.Tx, args RemoveServerArgs) error) {
	s.removal.enqueue = fn
}

// RemoveInput is the request of Remove server.
type RemoveInput struct {
	// Confirm is the server's name.
	Confirm string
	// Mode is ModeClean (the default) or ModeForget.
	Mode string
}

// ServerRemoval is what Remove server answers.
type ServerRemoval struct {
	Mode    string `json:"mode" enum:"clean,forget" doc:"What the removal does. An offline server is always forgotten: its host cannot be reached"`
	Removed bool   `json:"removed" doc:"The server is gone. False while the agent empties the host: the server shows its removal state until the row is deleted"`
	// HostUntouched is true when the removal did not touch the host.
	HostUntouched bool    `json:"hostUntouched" doc:"The host keeps its containers, networks, volumes and the agent's files: clean them up yourself"`
	Server        *Server `json:"server,omitempty" doc:"The server while it is being removed"`
}

func (s *Service) blockingProjects(ctx context.Context, q *db.Queries, id uuid.UUID) ([]BlockingProject, error) {
	rows, err := q.ListServerBlockingProjects(ctx, &id)
	if err != nil {
		return nil, fmt.Errorf("list projects on server: %w", err)
	}
	out := make([]BlockingProject, 0, len(rows))
	for _, r := range rows {
		out = append(out, BlockingProject{ID: r.ID, Name: r.Name, Slug: r.Slug, System: r.System, Services: int(r.Services)})
	}
	return out, nil
}

func hasServicesError(ps []BlockingProject) error {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.System {
			names = append(names, "platform services")
		} else {
			names = append(names, p.Name)
		}
	}
	return apierr.Conflict("server_has_services", "services of "+strings.Join(names, ", ")+" are placed on this server: move or delete them first").
		WithDetails(map[string]any{"projects": ps})
}

// Remove deletes a server.
//
// With ModeClean the agent first removes every container, network and volume
// NebulaCtrl manages on the host and then itself; the row is deleted once the
// agent reports success. That takes a while, so the work runs as a job: the
// call returns with Removed false and the server shows its removal state.
// ModeForget, and any server whose agent is offline, revokes the agent and
// deletes the row at once without touching the host.
//
// Both are refused while services are placed on the server (409
// server_has_services, listing the projects), and for the server the control
// plane itself runs on (409 server_is_control_plane).
func (s *Service) Remove(ctx context.Context, id uuid.UUID, in RemoveInput) (ServerRemoval, error) {
	cur, err := s.load(ctx, s.q, policy.ScopeServersWrite, id)
	if err != nil {
		return ServerRemoval{}, err
	}
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return ServerRemoval{}, err
	}
	if cur.IsControlPlane && cur.Status != "revoked" {
		return ServerRemoval{}, apierr.Conflict("server_is_control_plane", "this server runs NebulaCtrl itself and cannot be removed here; uninstall NebulaCtrl from the host instead")
	}
	mode := in.Mode
	if mode == "" {
		mode = ModeClean
	}
	if mode != ModeClean && mode != ModeForget {
		return ServerRemoval{}, apierr.Unprocessable("invalid_mode", "mode is clean or forget")
	}
	if in.Confirm != cur.Name {
		return ServerRemoval{}, apierr.Unprocessable("confirmation_mismatch", "type the server's name to remove it").
			WithDetails(map[string]any{"name": cur.Name})
	}
	// A removal that has been "removing" for longer than any agent can take lost
	// its job (the control plane restarted): it may be started again.
	if cur.RemovalState == RemovalRemoving && mode == ModeClean &&
		(cur.RemovalStartedAt == nil || s.now().Sub(*cur.RemovalStartedAt) < DecommissionTimeout+removalStaleGrace) {
		return ServerRemoval{}, apierr.Conflict("removal_in_progress", "this server is already being removed")
	}
	blocking, err := s.blockingProjects(ctx, s.q, id)
	if err != nil {
		return ServerRemoval{}, err
	}
	if len(blocking) > 0 {
		return ServerRemoval{}, hasServicesError(blocking)
	}

	// An offline server cannot be cleaned: its agent would never answer.
	online := cur.Status == "online" && s.removal.agent != nil && s.removal.agent.Online(id)
	if mode == ModeClean && !online {
		mode = ModeForget
	}
	if mode == ModeForget {
		if err := s.removeNow(ctx, cur, false, nil); err != nil {
			return ServerRemoval{}, err
		}
		return ServerRemoval{Mode: ModeForget, Removed: true, HostUntouched: true}, nil
	}
	if !s.canDecommission(cur.Facts) {
		return ServerRemoval{}, apierr.Conflict("agent_cannot_decommission",
			"this server's agent is too old to clean the host: upgrade the agent first, or remove the server with mode forget")
	}

	args := RemoveServerArgs{ServerID: id, WorkspaceID: cur.WorkspaceID, Guard: true}
	if p, ok := auth.PrincipalFrom(ctx); ok {
		args.ActorType, args.ActorUserID, args.ActorTokenID, args.ActorLabel = p.Type, p.UserID, p.TokenID, p.Label
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).SetServerRemoval(ctx, db.SetServerRemovalParams{ID: id, WorkspaceID: cur.WorkspaceID, State: RemovalRemoving}); err != nil {
			return err
		}
		return s.enqueueRemoval(ctx, tx, args)
	})
	if err != nil {
		return ServerRemoval{}, fmt.Errorf("start server removal: %w", err)
	}
	s.publish(id)
	sv, err := s.Get(ctx, id)
	if err != nil {
		return ServerRemoval{}, err
	}
	return ServerRemoval{Mode: ModeClean, Server: &sv}, nil
}

func (s *Service) enqueueRemoval(ctx context.Context, tx pgx.Tx, args RemoveServerArgs) error {
	if s.removal.enqueue != nil {
		return s.removal.enqueue(ctx, tx, args)
	}
	if s.removal.jm == nil {
		return errors.New("no job manager to run the removal")
	}
	return s.removal.jm.InsertTx(ctx, tx, args, nil)
}

func (s *Service) canDecommission(facts []byte) bool {
	if len(facts) == 0 {
		return false
	}
	var f agentprotocol.Facts
	return json.Unmarshal(facts, &f) == nil && slices.Contains(f.Capabilities, agentprotocol.CapDecommission)
}

// RemoveServerArgs is the job that empties a host and deletes its server.
type RemoveServerArgs struct {
	ServerID    uuid.UUID `json:"serverId"`
	WorkspaceID uuid.UUID `json:"workspaceId"`
	// Guard re-checks that no service was placed on the server meanwhile.
	Guard bool `json:"guard"`
	// The person (or token) that asked, for the audit row.
	ActorType    string     `json:"actorType,omitempty"`
	ActorUserID  uuid.UUID  `json:"actorUserId,omitempty"`
	ActorTokenID *uuid.UUID `json:"actorTokenId,omitempty"`
	ActorLabel   string     `json:"actorLabel,omitempty"`
}

// Kind implements river.JobArgs.
func (RemoveServerArgs) Kind() string { return "remove_server" }

// InsertOpts implements river.JobArgsWithInsertOpts: a removal that failed is
// shown to the person, who retries it; River does not.
func (RemoveServerArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 1} }

type removeServerWorker struct {
	river.WorkerDefaults[RemoveServerArgs]
	svc *Service
}

// Timeout implements river.Worker: the agent gets DecommissionTimeout.
func (w *removeServerWorker) Timeout(*river.Job[RemoveServerArgs]) time.Duration {
	return DecommissionTimeout + 5*time.Minute
}

func (w *removeServerWorker) Work(ctx context.Context, job *river.Job[RemoveServerArgs]) error {
	return w.svc.RunRemoval(ctx, job.Args)
}

// RegisterRemovalJob adds the worker of the removal job.
func RegisterRemovalJob(jm *jobs.Manager, svc *Service) {
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &removeServerWorker{svc: svc}) })
}

// RunRemoval is the removal job: it empties the host through the agent and
// deletes the server. A failure is stored on the server (removal_state failed
// with the reason) for the person to retry or to forget the server; it is not
// returned, so the job is not retried by itself.
func (s *Service) RunRemoval(ctx context.Context, args RemoveServerArgs) error {
	if args.ActorType != "" {
		ctx = auth.WithPrincipal(ctx, &auth.Principal{
			Type: args.ActorType, UserID: args.ActorUserID, TokenID: args.ActorTokenID, Label: args.ActorLabel, WorkspaceID: args.WorkspaceID,
		})
	}
	cur, err := s.q.GetServerScoped(ctx, db.GetServerScopedParams{ID: args.ServerID, WorkspaceID: args.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // forgotten meanwhile
	}
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	if cur.RemovalState != RemovalRemoving {
		return nil // the removal was replaced (forgotten) while the job waited
	}
	fail := func(msg string) error {
		if _, err := s.q.SetServerRemoval(ctx, db.SetServerRemovalParams{ID: args.ServerID, WorkspaceID: args.WorkspaceID, State: RemovalFailed, Error: msg}); err != nil {
			return fmt.Errorf("record removal failure: %w", err)
		}
		s.log.WarnContext(ctx, "server removal failed", "server_id", args.ServerID, "err", msg)
		s.publish(args.ServerID)
		return nil
	}
	if args.Guard {
		blocking, err := s.blockingProjects(ctx, s.q, args.ServerID)
		if err != nil {
			return err
		}
		if len(blocking) > 0 {
			return fail(hasServicesError(blocking).Error())
		}
	}
	res := new(agentprotocol.DecommissionResult)
	if err := s.removeNow(ctx, cur, true, res); err != nil {
		if ae, ok := apierr.As(err); ok {
			return fail(ae.Error())
		}
		return fail(err.Error())
	}
	return nil
}

// RemoveForTeam removes a server while its team is deleted: clean when its
// agent is online and able, otherwise without touching the host. It waits for
// the agent. It checks no scope and no services: the team's projects are gone
// by then. It returns OutcomeCleaned or OutcomeForgotten.
func (s *Service) RemoveForTeam(ctx context.Context, serverID uuid.UUID) (string, error) {
	srv, err := s.q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutcomeForgotten, nil // gone already
	}
	if err != nil {
		return "", fmt.Errorf("get server: %w", err)
	}
	cur, err := s.q.GetServerScoped(ctx, db.GetServerScopedParams{ID: srv.ID, WorkspaceID: srv.WorkspaceID})
	if err != nil {
		return "", fmt.Errorf("get server: %w", err)
	}
	if cur.IsControlPlane && cur.Status != "revoked" {
		return "", apierr.Conflict("server_is_control_plane", "this server runs NebulaCtrl itself and cannot be removed")
	}
	clean := cur.Status == "online" && s.removal.agent != nil && s.removal.agent.Online(serverID) && s.canDecommission(cur.Facts)
	if err := s.removeNow(ctx, cur, clean, nil); err != nil {
		return "", err
	}
	if clean {
		return OutcomeCleaned, nil
	}
	return OutcomeForgotten, nil
}

// decommission asks the agent to empty its host and waits for its answer.
func (s *Service) decommission(ctx context.Context, id uuid.UUID) (agentprotocol.DecommissionResult, error) {
	var out agentprotocol.DecommissionResult
	if s.removal.agent == nil {
		return out, errors.New("no connection to the agents")
	}
	res, err := s.removal.agent.Request(ctx, id, agentprotocol.KindDecommission, agentprotocol.DecommissionPayload{ServerID: id.String()}, nil, DecommissionTimeout)
	switch {
	case errors.Is(err, agents.ErrRequestLost):
		return out, errors.New("the agent disconnected before it answered; if the host is gone, forget the server, otherwise try again")
	case err != nil:
		if ae, ok := apierr.As(err); ok && ae.Status == http.StatusServiceUnavailable {
			return out, errors.New("the agent is offline: bring it back to clean the host, or forget the server")
		}
		return out, err
	}
	if len(res.Data) > 0 {
		_ = json.Unmarshal(res.Data, &out)
	}
	if res.Outcome != agentprotocol.OutcomeSucceeded {
		msg := res.Error
		if msg == "" {
			msg = "the agent could not empty the host"
		}
		return out, errors.New(msg)
	}
	return out, nil
}

// removeNow removes a server and waits. With clean the agent empties the host
// first (res, when set, receives its report); the agent identity is revoked and
// the row deleted afterwards. The caller has checked authorization and guards.
func (s *Service) removeNow(ctx context.Context, cur db.GetServerScopedRow, clean bool, res *agentprotocol.DecommissionResult) error {
	if clean {
		r, err := s.decommission(ctx, cur.ID)
		if err != nil {
			return err
		}
		if res != nil {
			*res = r
		}
	}
	// The revoke hooks forget the mesh nodes, tunnels and databases of the
	// server while its row still exists.
	if err := s.revokeRow(ctx, cur); err != nil {
		return err
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if cur.TailnetDeviceID != nil && *cur.TailnetDeviceID != "" && s.removal.ts != nil {
			if err := s.removal.ts.EnqueueDeleteDevice(ctx, tx, cur.WorkspaceID, *cur.TailnetDeviceID); err != nil {
				return fmt.Errorf("queue tailnet device removal: %w", err)
			}
		}
		if _, err := q.DeleteServer(ctx, db.DeleteServerParams{ID: cur.ID, WorkspaceID: cur.WorkspaceID}); err != nil {
			return err
		}
		meta := map[string]any{"name": cur.Name, "mode": ModeForget, "hostUntouched": !clean}
		if clean {
			meta["mode"] = ModeClean
			if res != nil {
				meta["containers"], meta["networks"], meta["volumes"] = res.Containers, res.Networks, res.Volumes
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionRemoved, ResourceType: "server", ResourceID: cur.ID.String(), WorkspaceID: cur.WorkspaceID, Metadata: meta,
		})
	})
	if err != nil {
		return fmt.Errorf("delete server: %w", err)
	}
	// The server row is gone, so the event names the team (which owns it).
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: "server.removed", ResourceID: cur.WorkspaceID.String(), WorkspaceID: cur.WorkspaceID.String()})
	}
	return nil
}
