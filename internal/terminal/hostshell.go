package terminal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The server host shell: an interactive shell on a server itself, relayed over
// the same browser ⇄ control plane ⇄ agent transport as the container
// terminal (GET /api/v1/servers/{serverId}/shell/ws speaks the protocol of
// session.go). It is root on the host, so it is stricter than a container
// terminal:
//
//   - opt-in per server (off by default; an Owner or Admin switches it on);
//   - Owners and Admins only, with a signed-in session (never an API token or
//     MCP), and only when their account has two-factor authentication
//     (403 two_factor_required otherwise);
//   - on the server the control plane itself runs on, the instance admin only
//     (403 instance_admin_required for everyone else): that host holds the
//     database and the secret key of every team;
//   - a server running any service of a production environment needs
//     confirmProduction in the open message;
//   - at most MaxHostSessionsPerUser sessions per person, 15 minutes idle;
//   - server.shell.open / server.shell.close are audited (who, server,
//     duration), never the session's input or output.
//
// What the agent runs (the host's login shell, or a helper container when the
// agent itself runs in a container) is the agent's business: see
// internal/agent/engine/hostshell.go.

// Host shell limits, audit actions and error codes.
const (
	// MaxHostSessionsPerUser bounds the concurrent host shells of one person.
	MaxHostSessionsPerUser = 2

	ActionHostShellOpen    = "server.shell.open"
	ActionHostShellClose   = "server.shell.close"
	ActionHostShellEnable  = "server.shell.enable"
	ActionHostShellDisable = "server.shell.disable"

	// CodeHostShellDisabled: the server's host shell is off.
	CodeHostShellDisabled = "host_shell_disabled"
	// CodeTwoFactorRequired: the caller's account has no two-factor authentication.
	CodeTwoFactorRequired = "two_factor_required"
	// CodeSessionRequired: a token or MCP principal asked for the host shell.
	CodeSessionRequired = "session_required"
	// CodeInstanceAdminRequired: the control-plane host's shell is for the instance admin only.
	CodeInstanceAdminRequired = "instance_admin_required"

	// ReasonHostShellDisabled is recorded when an admin switched the host shell
	// off while the session was open.
	ReasonHostShellDisabled = "host_shell_disabled"
)

// hostServer is a server as the host shell sees it.
type hostServer struct {
	ID     uuid.UUID
	Name   string
	Status string
	// Enabled: an admin switched the host shell on.
	Enabled bool
	// Production: a service of a production environment runs here.
	Production bool
	// ControlPlane: the control plane itself runs on this server (is_control_plane).
	ControlPlane bool
}

// hostStore is the part of the host shell that touches the database.
type hostStore interface {
	// hostServer returns the server inside the caller's team (404 otherwise).
	hostServer(ctx context.Context, serverID uuid.UUID) (hostServer, error)
	// setHostShell stores the setting and the audit entry in one transaction.
	setHostShell(ctx context.Context, serverID, userID uuid.UUID, enabled bool, e audit.Entry) error
	userHasTwoFactor(ctx context.Context, userID uuid.UUID) (bool, error)
	// isInstanceAdmin reports whether the user is the instance admin (the
	// earliest-created user; the same rule as servers.isInstanceAdmin).
	isInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
	// record writes one audit entry in its own transaction.
	record(ctx context.Context, e audit.Entry) error
}

// hostLive tracks the open host shells so that switching the setting off can
// end them.
type hostLive struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]map[string]*hostLiveSession // by server, then session ID
}

type hostLiveSession struct {
	halt chan struct{} // closed to end the session (the setting was switched off)
	once sync.Once
}

func (s *hostLiveSession) end() { s.once.Do(func() { close(s.halt) }) }

func (l *hostLive) add(server uuid.UUID, sid string) *hostLiveSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sessions == nil {
		l.sessions = map[uuid.UUID]map[string]*hostLiveSession{}
	}
	if l.sessions[server] == nil {
		l.sessions[server] = map[string]*hostLiveSession{}
	}
	s := &hostLiveSession{halt: make(chan struct{})}
	l.sessions[server][sid] = s
	return s
}

func (l *hostLive) remove(server uuid.UUID, sid string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions[server], sid)
	if len(l.sessions[server]) == 0 {
		delete(l.sessions, server)
	}
}

// endAll ends every open session of a server and returns how many.
func (l *hostLive) endAll(server uuid.UUID) int {
	l.mu.Lock()
	live := make([]*hostLiveSession, 0, len(l.sessions[server]))
	for _, s := range l.sessions[server] {
		live = append(live, s)
	}
	l.mu.Unlock()
	for _, s := range live {
		s.end()
	}
	return len(live)
}

// HostShellInfoView is what the UI needs before opening a host shell.
type HostShellInfoView struct {
	ServerID   uuid.UUID `json:"serverId"`
	ServerName string    `json:"serverName"`
	Enabled    bool      `json:"enabled" doc:"An admin switched the host shell on for this server (default off)."`
	Available  bool      `json:"available" doc:"A session can be opened now: enabled, the server online and the caller's account has two-factor authentication."`
	Reason     string    `json:"reason,omitempty" doc:"Why Available is false, in a sentence."`
	Production bool      `json:"production" doc:"A service of a production environment runs on this server: opening a shell needs confirmProduction."`
	// TwoFactorEnabled is about the caller's own account.
	TwoFactorEnabled   bool `json:"twoFactorEnabled" doc:"The caller's account has two-factor authentication."`
	MaxSessions        int  `json:"maxSessions"`
	ActiveSessions     int  `json:"activeSessions" doc:"Host shells the caller has open."`
	IdleTimeoutSeconds int  `json:"idleTimeoutSeconds"`
	// ControlPlane and Restricted describe the control-plane host.
	ControlPlane bool `json:"controlPlane" doc:"The control plane itself runs on this server: only the instance admin may enable or open its host shell."`
	Restricted   bool `json:"restricted" doc:"The caller may not enable or open the host shell of this server because it is the control-plane host and the caller is not the instance admin."`
}

// HostShellSettingsView is the host shell setting of a server.
type HostShellSettingsView struct {
	Enabled bool `json:"enabled" doc:"Allow Owners and Admins with two-factor authentication to open a root shell on this server."`
}

// authorizeHost checks who may use or configure the host shell: a signed-in
// user (never a token) who is an Owner or Admin of the team and holds the
// servers scope. It returns the principal.
func (s *Service) authorizeHost(ctx context.Context, scope string) (*auth.Principal, error) {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return nil, err
	}
	if p.Type != auth.TypeUser {
		return nil, apierr.New(http.StatusForbidden, CodeSessionRequired, "the host shell needs a signed-in session, not an API token")
	}
	if err := policy.Require(ctx, scope, nil); err != nil {
		return nil, err
	}
	if err := policy.RequireRole(ctx, nil, auth.RoleOwner, auth.RoleAdmin); err != nil {
		return nil, err
	}
	return p, nil
}

// hostRestricted reports whether srv is the control-plane host and p is not
// the instance admin: a shell there is root on the machine that holds the
// database and the secret key of every team.
func (s *Service) hostRestricted(ctx context.Context, p *auth.Principal, srv hostServer) (bool, error) {
	if !srv.ControlPlane {
		return false, nil
	}
	admin, err := s.hs.isInstanceAdmin(ctx, p.UserID)
	if err != nil {
		return false, err
	}
	return !admin, nil
}

// requireHostServer refuses the control-plane host to everyone but the
// instance admin.
func (s *Service) requireHostServer(ctx context.Context, p *auth.Principal, srv hostServer) error {
	restricted, err := s.hostRestricted(ctx, p, srv)
	if err != nil {
		return err
	}
	if restricted {
		return apierr.New(http.StatusForbidden, CodeInstanceAdminRequired, "the host shell of the control-plane server is only available to the instance admin")
	}
	return nil
}

// HostShellInfo describes what the caller can do with the server's host shell.
func (s *Service) HostShellInfo(ctx context.Context, serverID uuid.UUID) (HostShellInfoView, error) {
	p, err := s.authorizeHost(ctx, policy.ScopeServersRead)
	if err != nil {
		return HostShellInfoView{}, err
	}
	srv, err := s.hs.hostServer(ctx, serverID)
	if err != nil {
		return HostShellInfoView{}, err
	}
	has, err := s.hs.userHasTwoFactor(ctx, p.UserID)
	if err != nil {
		return HostShellInfoView{}, err
	}
	restricted, err := s.hostRestricted(ctx, p, srv)
	if err != nil {
		return HostShellInfoView{}, err
	}
	v := HostShellInfoView{
		ServerID: srv.ID, ServerName: srv.Name, Enabled: srv.Enabled, Production: srv.Production, TwoFactorEnabled: has,
		MaxSessions: s.maxHostSessions, ActiveSessions: s.activeHostSessions(p.ID()), IdleTimeoutSeconds: int(s.idle / time.Second),
		ControlPlane: srv.ControlPlane, Restricted: restricted,
	}
	switch {
	case restricted:
		v.Reason = "This server runs the control plane. Only the instance admin can use its host shell."
	case !srv.Enabled:
		v.Reason = "The host shell is off for this server. An Owner or Admin can allow it with the switch above."
	case !has:
		v.Reason = "Set up two-factor authentication in Account settings → Security to open a host shell."
	case srv.Status != "online":
		v.Reason = "The server is offline."
	default:
		v.Available = true
	}
	return v, nil
}

// SetHostShell switches the host shell of a server on or off. Owners and
// Admins only; audited. Switching it off ends the sessions that are open.
func (s *Service) SetHostShell(ctx context.Context, serverID uuid.UUID, in HostShellSettingsView) (HostShellSettingsView, error) {
	p, err := s.authorizeHost(ctx, policy.ScopeServersWrite)
	if err != nil {
		return HostShellSettingsView{}, err
	}
	srv, err := s.hs.hostServer(ctx, serverID)
	if err != nil {
		return HostShellSettingsView{}, err
	}
	if err := s.requireHostServer(ctx, p, srv); err != nil {
		return HostShellSettingsView{}, err
	}
	action := ActionHostShellDisable
	if in.Enabled {
		action = ActionHostShellEnable
	}
	if err := s.hs.setHostShell(ctx, serverID, p.UserID, in.Enabled, audit.Entry{
		Action: action, ResourceType: "server", ResourceID: serverID.String(), Metadata: map[string]any{"enabled": in.Enabled},
	}); err != nil {
		return HostShellSettingsView{}, err
	}
	if !in.Enabled {
		if n := s.live.endAll(serverID); n > 0 {
			s.log.InfoContext(ctx, "host shell switched off; ending open sessions", "server_id", serverID, "sessions", n)
		}
	}
	return in, nil
}

func (s *Service) activeHostSessions(id uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostActive[id]
}

// acquireHost reserves one of the principal's host shell slots; nil when none is left.
func (s *Service) acquireHost(id uuid.UUID) (release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hostActive[id] >= s.maxHostSessions {
		return nil
	}
	s.hostActive[id]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.hostActive[id]--; s.hostActive[id] <= 0 {
				delete(s.hostActive, id)
			}
		})
	}
}

// serveHostWS upgrades GET /servers/{serverId}/shell/ws and runs one host
// shell. Everything that can be refused before the upgrade is refused with an
// HTTP status.
func (s *Service) serveHostWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := uuid.Parse(chi.URLParam(r, "serverId"))
	if err != nil {
		apierr.Write(w, apierr.NotFound("server"))
		return
	}
	p, err := s.authorizeHost(ctx, policy.ScopeServersWrite)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	srv, err := s.hs.hostServer(ctx, id)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if err := s.requireHostServer(ctx, p, srv); err != nil {
		writeHTTPError(w, err)
		return
	}
	if !srv.Enabled {
		apierr.Write(w, apierr.New(http.StatusForbidden, CodeHostShellDisabled, "the host shell is not enabled for this server"))
		return
	}
	has, err := s.hs.userHasTwoFactor(ctx, p.UserID)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	if !has {
		apierr.Write(w, apierr.New(http.StatusForbidden, CodeTwoFactorRequired, "a host shell needs two-factor authentication on your account; set it up in Account settings → Security first"))
		return
	}
	// Same origin rules as the container terminal: a page of another site
	// cannot open a shell with the caller's session cookie.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPatterns()})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxClientMessage)
	s.hostSession(ctx, ws, srv, p)
}

// hostSession runs one host shell session on an accepted socket.
func (s *Service) hostSession(ctx context.Context, ws *websocket.Conn, srv hostServer, p *auth.Principal) {
	open, err := s.readOpen(ctx, ws)
	if err != nil {
		s.reject(ctx, ws, CodeInvalidRequest, err.Error())
		return
	}
	if len(open.Command) > 0 {
		s.reject(ctx, ws, CodeInvalidRequest, "a host shell always runs the host's login shell; it takes no command")
		return
	}
	if srv.Production && !open.ConfirmProduction {
		s.reject(ctx, ws, CodeConfirmationRequired, "A production service runs on this server: opening a host shell needs an explicit confirmation.")
		return
	}
	if srv.Status != "online" {
		s.reject(ctx, ws, CodeServerOffline, "The server is offline, so a host shell cannot be opened.")
		return
	}
	release := s.acquireHost(p.ID())
	if release == nil {
		s.reject(ctx, ws, CodeTooManySessions, fmt.Sprintf("You already have %d host shells open. Close one first.", s.maxHostSessions))
		return
	}
	defer release()

	sid := uuid.NewString()
	st, err := s.agents.OpenStream(srv.ID, sid)
	if err != nil {
		s.reject(ctx, ws, CodeServerOffline, "The server is offline, so a host shell cannot be opened.")
		return
	}
	defer st.Close()

	cols, rows := clampSize(open.Cols, 80), clampSize(open.Rows, 24)
	if err := st.Send(agentprotocol.TypeExecOpen, agentprotocol.ExecOpen{SessionID: sid, Host: true, Cols: cols, Rows: rows}); err != nil {
		s.reject(ctx, ws, CodeServerOffline, "The server is offline, so a host shell cannot be opened.")
		return
	}
	opened, ok := s.awaitOpened(ctx, st)
	if !ok {
		_ = st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: sid})
		s.reject(ctx, ws, CodeServerOffline, "The server did not answer, so a host shell could not be opened.")
		return
	}
	if opened.Error != "" {
		s.reject(ctx, ws, CodeOpenFailed, opened.Error)
		return
	}

	started := s.now()
	meta := map[string]any{
		"sessionId": sid, "serverId": srv.ID.String(), "server": srv.Name, "production": srv.Production, "mode": opened.Container,
	}
	if err := s.hs.record(ctx, audit.Entry{
		Action: ActionHostShellOpen, ResourceType: "server", ResourceID: srv.ID.String(), Metadata: meta,
	}); err != nil {
		// Fail closed: a root shell that cannot be audited does not run.
		s.log.ErrorContext(ctx, "audit server.shell.open", "server_id", srv.ID, "err", err)
		_ = st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: sid})
		s.reject(ctx, ws, CodeOpenFailed, "The session could not be recorded in the audit log, so it was not opened.")
		return
	}

	// A switch-off by an admin ends the session through live.halt. Check the
	// setting once more after registering: a switch-off between the first check
	// and here would otherwise have missed this session.
	live := s.live.add(srv.ID, sid)
	defer s.live.remove(srv.ID, sid)
	if cur, err := s.hs.hostServer(ctx, srv.ID); err != nil || !cur.Enabled {
		live.end()
	}

	_ = sendJSON(ctx, ws, serverMsg{Type: "opened", Container: opened.Container, Command: opened.Command})
	out := s.pumpUntil(ctx, ws, st, sid, live.halt, ReasonHostShellDisabled)

	// The close entry must be written even when the request context is gone.
	bg := context.WithoutCancel(ctx)
	closeMeta := map[string]any{
		"sessionId": sid, "serverId": srv.ID.String(), "server": srv.Name, "mode": opened.Container,
		"durationSeconds": int(s.now().Sub(started).Seconds()), "reason": out.reason, "production": srv.Production,
	}
	if out.exit != nil {
		closeMeta["exitCode"] = *out.exit
	}
	if err := s.hs.record(bg, audit.Entry{
		Action: ActionHostShellClose, ResourceType: "server", ResourceID: srv.ID.String(), Metadata: closeMeta,
	}); err != nil {
		s.log.ErrorContext(bg, "audit server.shell.close", "server_id", srv.ID, "err", err)
	}
}

// ---- database store ----

type dbHostStore struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func (d *dbHostStore) hostServer(ctx context.Context, id uuid.UUID) (hostServer, error) {
	row, err := d.q.GetServerHostShell(ctx, db.GetServerHostShellParams{ID: id, WorkspaceID: policy.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return hostServer{}, apierr.NotFound("server")
	}
	if err != nil {
		return hostServer{}, fmt.Errorf("get server host shell: %w", err)
	}
	// The scoped query above already proved the server belongs to the caller's team.
	srv, err := d.q.GetServerByID(ctx, row.ServerID)
	if err != nil {
		return hostServer{}, fmt.Errorf("get server: %w", err)
	}
	return hostServer{ID: row.ServerID, Name: row.Name, Status: row.Status, Enabled: row.Enabled, Production: row.Production, ControlPlane: srv.IsControlPlane}, nil
}

func (d *dbHostStore) setHostShell(ctx context.Context, serverID, userID uuid.UUID, enabled bool, e audit.Entry) error {
	return db.InTx(ctx, d.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).UpsertServerHostShell(ctx, db.UpsertServerHostShellParams{ServerID: serverID, Enabled: enabled, UpdatedBy: &userID}); err != nil {
			return fmt.Errorf("save host shell setting: %w", err)
		}
		return audit.Record(ctx, tx, e)
	})
}

func (d *dbHostStore) userHasTwoFactor(ctx context.Context, userID uuid.UUID) (bool, error) {
	has, err := d.q.UserHasSecondFactor(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("check two-factor: %w", err)
	}
	return has, nil
}

func (d *dbHostStore) isInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	first, err := d.q.FirstUserID(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("first user: %w", err)
	}
	return first == userID, nil
}

func (d *dbHostStore) record(ctx context.Context, e audit.Entry) error {
	return db.InTx(ctx, d.pool, func(tx pgx.Tx) error { return audit.Record(ctx, tx, e) })
}
