package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Stream is one open exchange with an agent. *agents.Stream implements it.
type Stream interface {
	Frames() <-chan agentprotocol.Frame
	Done() <-chan struct{}
	Lost() bool
	Send(typ string, body any) error
	Close()
}

// Agents opens streams to connected agents.
type Agents interface {
	// OpenStream registers stream id on the server's connection; it fails
	// with agents.ErrNotConnected while the server is offline.
	OpenStream(serverID uuid.UUID, id string) (Stream, error)
}

// HubAgents adapts the agent hub to Agents.
type HubAgents struct{ Hub *agents.Hub }

// OpenStream implements Agents.
func (h HubAgents) OpenStream(serverID uuid.UUID, id string) (Stream, error) {
	s, err := h.Hub.OpenStream(serverID, id)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// target is a service the caller may reach, with what a terminal needs.
type target struct {
	ServiceID     uuid.UUID
	ProjectID     uuid.UUID
	EnvironmentID uuid.UUID
	ServerID      *uuid.UUID
	Name          string
	Slug          string
	Kind          string
	Production    bool
	// Deployed: the service has a current deployment on a server.
	Deployed bool
	Replicas []ReplicaView
	// DefaultProcess is the name of the default process of a service with
	// several processes; empty for a plain service.
	DefaultProcess string
	// Processes are the process names of the service's config (default first).
	Processes []string
}

// store is the part of the service that touches the database.
type store interface {
	// load authorizes projects:read for the caller and returns the service
	// (404 outside the caller's team or project restriction).
	load(ctx context.Context, serviceID uuid.UUID) (target, error)
	serverName(ctx context.Context, serverID uuid.UUID) string
	// record writes one audit entry in its own transaction.
	record(ctx context.Context, e audit.Entry) error
	allowProductionWrites(ctx context.Context, projectID uuid.UUID) (bool, error)
	// projectInScope reports (404 otherwise) that the caller may see the project.
	projectInScope(ctx context.Context, projectID uuid.UUID) error
	setAllowProductionWrites(ctx context.Context, projectID uuid.UUID, allow bool, e audit.Entry) error
}

// Deps are the collaborators of a Service. Pool may be nil for OpenAPI
// generation only.
type Deps struct {
	Pool   *pgxpool.Pool
	Agents Agents
	Config *config.Config
	Logger *slog.Logger
}

// Service implements the terminal and file-browser use cases.
type Service struct {
	st     store
	hs     hostStore // the server host shell (hostshell.go)
	agents Agents
	cfg    *config.Config
	log    *slog.Logger

	// Tunables (tests shrink them).
	idle        time.Duration
	idleWarning time.Duration
	tick        time.Duration
	maxSessions int
	// maxHostSessions bounds the host shells of one principal.
	maxHostSessions int
	openTimeout     time.Duration
	fsTimeout       time.Duration

	mu         sync.Mutex
	active     map[uuid.UUID]int
	hostActive map[uuid.UUID]int // open host shells by principal
	live       hostLive
	now        func() time.Time
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	s := &Service{
		agents: d.Agents, cfg: d.Config, log: d.Logger,
		idle: IdleTimeout, idleWarning: IdleWarning, tick: 5 * time.Second, maxSessions: MaxSessionsPerUser,
		openTimeout: 20 * time.Second, fsTimeout: 30 * time.Second,
		maxHostSessions: MaxHostSessionsPerUser, active: map[uuid.UUID]int{}, hostActive: map[uuid.UUID]int{}, now: time.Now,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if d.Pool != nil {
		s.st = &dbStore{pool: d.Pool, q: db.New(d.Pool)}
		s.hs = &dbHostStore{pool: d.Pool, q: db.New(d.Pool)}
	}
	return s
}

// authorize checks that the caller may use the terminal and the file browser
// of the service: Owner, Admin or Developer, with deployments:write. A Viewer
// never gets in, and neither does a token without the scope.
func (s *Service) authorize(ctx context.Context, serviceID uuid.UUID) (target, *auth.Principal, error) {
	t, err := s.st.load(ctx, serviceID)
	if err != nil {
		return target{}, nil, err
	}
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return target{}, nil, err
	}
	if err := policy.Require(ctx, policy.ScopeDeploymentsWrite, &t.ProjectID); err != nil {
		return target{}, nil, err
	}
	if err := policy.RequireRole(ctx, &t.ProjectID, auth.RoleOwner, auth.RoleAdmin, auth.RoleDeveloper); err != nil {
		return target{}, nil, err
	}
	return t, p, nil
}

// isDefaultProcess reports whether process names the default process: the empty
// name, or the default process' own name.
func (t target) isDefaultProcess(process string) bool {
	return process == "" || process == t.DefaultProcess
}

// knowsProcess reports whether the service has a process of that name: one of
// its config's, or one that still runs (a process removed from the staged
// config keeps running until the next deploy).
func (t target) knowsProcess(process string) bool {
	if t.isDefaultProcess(process) || slices.Contains(t.Processes, process) {
		return true
	}
	for _, r := range t.Replicas {
		if r.Process == process {
			return true
		}
	}
	return false
}

// agentProcess is the process name to send to the agent: "" for the default
// process (the agent matches it with or without a process label).
func (t target) agentProcess(process string) string {
	if t.isDefaultProcess(process) {
		return ""
	}
	return process
}

// requireRunning checks a process, a replica index within it and that its
// container runs. process "" is the default process.
func (t target) requireRunning(process string, replica int) error {
	if replica < 0 || replica > MaxReplica {
		return apierr.Unprocessable("invalid_replica", "replica must be between 0 and 999")
	}
	if t.Kind == "route" {
		return apierr.Conflict("no_container", "this service has no container (it only routes traffic)")
	}
	if !t.knowsProcess(process) {
		return apierr.NotFound(fmt.Sprintf("process %q", process))
	}
	if !t.Deployed || t.ServerID == nil {
		return apierr.Conflict(CodeNotDeployed, "the service is not deployed yet")
	}
	for _, r := range t.Replicas {
		if r.Index == replica && r.Running && t.sameProcess(r.Process, process) {
			return nil
		}
	}
	if t.isDefaultProcess(process) {
		return apierr.Conflict(CodeReplicaNotRunning, fmt.Sprintf("replica %d is not running", replica))
	}
	return apierr.Conflict(CodeReplicaNotRunning, fmt.Sprintf("replica %d of process %q is not running", replica, process))
}

// sameProcess reports whether a replica of process have belongs to the
// requested process: the default process matches replicas without a process
// and those carrying its own name.
func (t target) sameProcess(have, want string) bool {
	if t.isDefaultProcess(want) {
		return have == "" || have == t.DefaultProcess
	}
	return have == want
}

// acquire reserves one of the principal's session slots; nil when none is left.
func (s *Service) acquire(id uuid.UUID) (release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[id] >= s.maxSessions {
		return nil
	}
	s.active[id]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.active[id]--; s.active[id] <= 0 {
				delete(s.active, id)
			}
		})
	}
}

func (s *Service) activeSessions(id uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[id]
}

// originPatterns lets the WebSocket handshake accept the configured public
// host in addition to the request's own host (a reverse proxy may rewrite it).
func (s *Service) originPatterns() []string {
	if s.cfg == nil {
		return nil
	}
	if u, err := url.Parse(s.cfg.PublicURL); err == nil && u.Host != "" {
		return []string{u.Host}
	}
	return nil
}

// fileWritePolicy says whether uploads are allowed for the service.
func (s *Service) fileWritePolicy(ctx context.Context, t target) (FileWritePolicy, error) {
	if !t.Production {
		return FileWritePolicy{Allowed: true}, nil
	}
	ok, err := s.st.allowProductionWrites(ctx, t.ProjectID)
	if err != nil {
		return FileWritePolicy{}, err
	}
	if ok {
		return FileWritePolicy{Allowed: true}, nil
	}
	return FileWritePolicy{Reason: "The file browser is read-only in production. An admin can allow writes for this project in its settings."}, nil
}

// Info describes what the caller can do with the service's terminal and files.
func (s *Service) Info(ctx context.Context, serviceID uuid.UUID) (InfoView, error) {
	t, p, err := s.authorize(ctx, serviceID)
	if err != nil {
		return InfoView{}, err
	}
	v := InfoView{
		ServiceID: t.ServiceID, Production: t.Production, Replicas: t.Replicas,
		MaxSessions: s.maxSessions, ActiveSessions: s.activeSessions(p.ID()),
		IdleTimeoutSeconds: int(s.idle / time.Second), MaxUploadBytes: agentprotocol.MaxUploadBytes, PreviewBytes: PreviewLimit,
	}
	if v.Replicas == nil {
		v.Replicas = []ReplicaView{}
	}
	if t.ServerID != nil {
		v.ServerName = s.st.serverName(ctx, *t.ServerID)
	}
	switch {
	case t.Kind == "route":
		v.Reason = "This service has no container: it only routes traffic."
	case !t.Deployed || t.ServerID == nil:
		v.Reason = "The service is not deployed yet."
	case !anyRunning(t.Replicas):
		v.Reason = "No replica is running."
	default:
		v.Available = true
	}
	if v.FileWrites, err = s.fileWritePolicy(ctx, t); err != nil {
		return InfoView{}, err
	}
	return v, nil
}

func anyRunning(rs []ReplicaView) bool {
	for _, r := range rs {
		if r.Running {
			return true
		}
	}
	return false
}

// Settings returns the project's terminal settings.
func (s *Service) Settings(ctx context.Context, projectID uuid.UUID) (SettingsView, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, &projectID); err != nil {
		return SettingsView{}, err
	}
	if err := s.st.projectInScope(ctx, projectID); err != nil {
		return SettingsView{}, err
	}
	ok, err := s.st.allowProductionWrites(ctx, projectID)
	if err != nil {
		return SettingsView{}, err
	}
	return SettingsView{AllowProductionFileWrites: ok}, nil
}

// SetSettings changes the project's terminal settings. Admins and Owners only.
func (s *Service) SetSettings(ctx context.Context, projectID uuid.UUID, in SettingsView) (SettingsView, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, &projectID); err != nil {
		return SettingsView{}, err
	}
	if err := policy.RequireRole(ctx, &projectID, auth.RoleOwner, auth.RoleAdmin); err != nil {
		return SettingsView{}, err
	}
	if err := s.st.projectInScope(ctx, projectID); err != nil {
		return SettingsView{}, err
	}
	err := s.st.setAllowProductionWrites(ctx, projectID, in.AllowProductionFileWrites, audit.Entry{
		Action: ActionSettingsUpdate, ResourceType: "project", ResourceID: projectID.String(), ProjectID: &projectID,
		Metadata: map[string]any{"allowProductionFileWrites": in.AllowProductionFileWrites},
	})
	if err != nil {
		return SettingsView{}, err
	}
	return in, nil
}

// ---- database store ----

type dbStore struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func (d *dbStore) load(ctx context.Context, id uuid.UUID) (target, error) {
	sc, err := services.LoadScope(ctx, d.q, policy.ScopeProjectsRead, id)
	if err != nil {
		return target{}, err
	}
	t := target{
		ServiceID: sc.Service.ID, ProjectID: sc.ProjectID, EnvironmentID: sc.EnvironmentID, ServerID: sc.Service.ServerID,
		Name: sc.Service.Name, Slug: sc.Service.Slug, Kind: sc.Service.Kind, Production: sc.IsProduction,
		Deployed: sc.Service.CurrentDeploymentID != nil && sc.Service.ServerID != nil,
		Replicas: replicasOf(sc.Service.Observed),
	}
	if cfg := sc.Config(); cfg.HasProcesses() {
		t.DefaultProcess, t.Processes = cfg.DefaultProcessName(), cfg.ProcessNames()
	}
	return t, nil
}

// replicasOf decodes the observed state of a service into its replicas. A
// service observed as running without per-replica detail has replica 0.
func replicasOf(raw json.RawMessage) []ReplicaView {
	if len(raw) == 0 {
		return nil
	}
	var o agentprotocol.ObservedService
	if json.Unmarshal(raw, &o) != nil {
		return nil
	}
	out := make([]ReplicaView, 0, len(o.Replicas))
	for _, r := range o.Replicas {
		out = append(out, ReplicaView{Index: r.Index, Process: r.Process, State: r.State, Health: r.Health, Running: r.State == agentprotocol.StateRunning})
	}
	if len(out) == 0 && o.State != "" {
		out = append(out, ReplicaView{Index: 0, State: o.State, Health: o.Health, Running: o.State == agentprotocol.StateRunning})
	}
	return out
}

func (d *dbStore) serverName(ctx context.Context, id uuid.UUID) string {
	srv, err := d.q.GetServerByID(ctx, id)
	if err != nil {
		return ""
	}
	return srv.Name
}

func (d *dbStore) record(ctx context.Context, e audit.Entry) error {
	return db.InTx(ctx, d.pool, func(tx pgx.Tx) error { return audit.Record(ctx, tx, e) })
}

func (d *dbStore) allowProductionWrites(ctx context.Context, projectID uuid.UUID) (bool, error) {
	ok, err := d.q.GetProjectTerminalSettings(ctx, projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load terminal settings: %w", err)
	}
	return ok, nil
}

func (d *dbStore) projectInScope(ctx context.Context, projectID uuid.UUID) error {
	ws := policy.WorkspaceID(ctx)
	if _, err := d.q.GetProject(ctx, db.GetProjectParams{ID: projectID, WorkspaceID: ws}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("project")
		}
		return fmt.Errorf("get project: %w", err)
	}
	return nil
}

func (d *dbStore) setAllowProductionWrites(ctx context.Context, projectID uuid.UUID, allow bool, e audit.Entry) error {
	return db.InTx(ctx, d.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).UpsertProjectTerminalSettings(ctx, db.UpsertProjectTerminalSettingsParams{
			ProjectID: projectID, AllowProductionFileWrites: allow,
		}); err != nil {
			return fmt.Errorf("save terminal settings: %w", err)
		}
		return audit.Record(ctx, tx, e)
	})
}
