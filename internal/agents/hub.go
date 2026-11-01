package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var _ Dispatcher = (*Hub)(nil)

// Delivery model. Enqueue writes the command row and, in the same transaction,
// runs pg_notify('nebulactrl_commands', serverID); Postgres delivers the
// notification only after the transaction commits, so a listener that reacts to
// it always sees the row. A background loop LISTENs on a dedicated connection
// and kicks the connection of that server. The loop also polls pending rows
// every pollInterval, which covers lost notifications, a restarted listener and
// commands written by another control-plane process. The loop starts with the
// first agent connection (nothing can be delivered before that) and stops with
// Close.
const (
	notifyChannel = "nebulactrl_commands"
	pollInterval  = 2 * time.Second
)

// UndeliveredMessage is the deployment failure text for commands that never
// reached an agent.
const UndeliveredMessage = "Not delivered: server offline. No changes were made."

// RevokedMessage is the deployment failure text for commands canceled by a
// server revocation.
const RevokedMessage = "Server revoked. No changes were made."

// Hub is the control-plane end of every agent connection. It owns the
// WebSocket sessions, durable command delivery and log routing, and implements
// Dispatcher.
type Hub struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	jm   *jobs.Manager
	cfg  *config.Config
	log  *slog.Logger

	mu        sync.RWMutex
	conns     map[uuid.UUID]*agentConn
	secrets   SecretSource
	results   ResultHandler
	redactor  Redactor
	traffic   TrafficSink
	probes    ProbeSink
	scanSinks []SourceScanSink
	taskLogs  TaskLogSink
	notifier  notifications.Emitter
	tunnelObs TunnelObserver
	meshObs   MeshObserver

	subMu sync.Mutex
	subs  map[string]*logSub

	folMu     sync.Mutex
	followers map[uuid.UUID]map[*follower]struct{}

	strMu   sync.Mutex
	streams map[string]*Stream

	tun tunnelState // instance service tunnels (localtunnel.go)

	binCache  binaryCache   // digests of the served agent binaries
	helloHook helloHooks    // callbacks run after an agent's hello
	reqs      requestTable  // ephemeral requests awaiting a result
	logStatus logStatusSlot // log store and drain status reports

	enrollLimit  *ipLimiter
	connectLimit *ipLimiter // handshakes per source address on /agent/v1/connect; each costs a database lookup
	now          func() time.Time

	rootCtx  context.Context
	cancel   context.CancelFunc
	loopOnce sync.Once
	wg       sync.WaitGroup
}

// NewHub returns a Hub. It does not touch the pool (the pool is nil while the
// OpenAPI document is generated).
func NewHub(pool *pgxpool.Pool, bus *events.Bus, jm *jobs.Manager, cfg *config.Config, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		pool: pool, q: db.New(pool), bus: bus, jm: jm, cfg: cfg,
		log:          logger.With("component", "agent-hub"),
		conns:        map[uuid.UUID]*agentConn{},
		subs:         map[string]*logSub{},
		followers:    map[uuid.UUID]map[*follower]struct{}{},
		enrollLimit:  newIPLimiter(10, time.Minute),
		connectLimit: newIPLimiter(connectsPerMinute, time.Minute),
		now:          time.Now,
		rootCtx:      ctx,
		cancel:       cancel,
	}
}

// SetSecretSource installs the send-time secret resolver.
func (h *Hub) SetSecretSource(s SecretSource) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.secrets = s
}

// SetResultHandler installs the domain hook for results, events and observations.
func (h *Hub) SetResultHandler(r ResultHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results = r
}

// SetTrafficSink installs the traffic analytics ingest hook. Without one,
// traffic frames are acknowledged and discarded.
func (h *Hub) SetTrafficSink(t TrafficSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.traffic = t
}

func (h *Hub) trafficSink() TrafficSink {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.traffic
}

// SetProbeSink installs the uptime ingest hook. Without one, probe frames are
// acknowledged and discarded.
func (h *Hub) SetProbeSink(p ProbeSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.probes = p
}

func (h *Hub) probeSink() ProbeSink {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.probes
}

// SetTaskLogSink installs the hook that stores the output of task runs
// (frames carrying a taskRunId). Without one such frames are dropped.
func (h *Hub) SetTaskLogSink(t TaskLogSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.taskLogs = t
}

func (h *Hub) taskLogSink() TaskLogSink {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.taskLogs
}

// Resync sends the server's current desired state (routes and settings such as
// the traffic analytics toggle) to its connected agent. It is a no-op when the
// server is offline: the agent receives the state on its next hello.
func (h *Hub) Resync(ctx context.Context, serverID uuid.UUID) error {
	c := h.conn(serverID)
	rh := h.resultHandler()
	if c == nil || rh == nil {
		return nil
	}
	rec, err := rh.DesiredState(ctx, serverID)
	if err != nil {
		return fmt.Errorf("resync: compute desired state: %w", err)
	}
	if err := c.sendFrame(agentprotocol.TypeReconcile, "", rec); err != nil {
		return fmt.Errorf("resync: send reconcile: %w", err)
	}
	return nil
}

// SetRedactor installs the log redactor.
func (h *Hub) SetRedactor(r Redactor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.redactor = r
}

func (h *Hub) secretSource() SecretSource {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.secrets
}

func (h *Hub) resultHandler() ResultHandler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.results
}

func (h *Hub) logRedactor() Redactor {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.redactor
}

// Mount registers the unauthenticated agent routes on the root router.
func (h *Hub) Mount(r chi.Router) {
	r.Post("/agent/v1/enroll", h.handleEnroll)
	r.Get("/agent/v1/connect", h.handleConnect)
	r.Get("/agent/v1/download/{arch}", h.handleDownload)
	r.Get("/agent/v1/download/{arch}/sha256", h.handleDownloadChecksum)
	r.Get("/install-agent.sh", h.handleInstallScript)
	r.Get("/install.sh", h.handleInstaller)
	r.Get("/uninstall.sh", h.handleUninstaller)
}

// Close stops the background loop and disconnects every agent. It does not
// close the pool.
func (h *Hub) Close() {
	h.cancel()
	h.mu.RLock()
	conns := make([]*agentConn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		c.closeWith(int(websocket.StatusGoingAway), "control plane shutting down")
	}
	h.wg.Wait()
}

// Online reports whether an agent is currently connected for the server.
func (h *Hub) Online(serverID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[serverID]
	return ok
}

// Disconnect closes the server's connection with the given WebSocket close code.
func (h *Hub) Disconnect(serverID uuid.UUID, code int, reason string) {
	h.mu.RLock()
	c := h.conns[serverID]
	h.mu.RUnlock()
	if c != nil {
		c.closeWith(code, reason)
	}
}

// Kick asks the server's connection to deliver any pending commands now. It is
// a no-op when the server is not connected.
func (h *Hub) Kick(serverID uuid.UUID) {
	h.mu.RLock()
	c := h.conns[serverID]
	h.mu.RUnlock()
	if c != nil {
		c.kickDelivery(false)
	}
}

func (h *Hub) conn(serverID uuid.UUID) *agentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[serverID]
}

func (h *Hub) publishServer(serverID uuid.UUID) {
	if h.bus != nil {
		h.bus.Publish(events.Event{Type: "server.updated", ResourceID: serverID.String()})
	}
}

// Enqueue persists a command inside tx and schedules its delivery for after the
// commit (see the delivery model above). A repeated idempotency key returns the
// ID of the command already stored under it.
func (h *Hub) Enqueue(ctx context.Context, tx pgx.Tx, cmd NewCommand) (uuid.UUID, error) {
	if !agentprotocol.AllowedKinds[cmd.Kind] {
		return uuid.Nil, fmt.Errorf("enqueue: unknown command kind %q", cmd.Kind)
	}
	q := db.New(tx)
	srv, err := q.GetServerByID(ctx, cmd.ServerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, apierr.NotFound("server")
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("enqueue: load server: %w", err)
	}
	if srv.Status == "revoked" {
		return uuid.Nil, apierr.Conflict("server_revoked", "server revoked")
	}
	id := uuid.New()
	key := cmd.IdempotencyKey
	if key == "" {
		key = id.String()
	}
	payload := cmd.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	row, err := q.InsertAgentCommand(ctx, db.InsertAgentCommandParams{
		ID: id, ServerID: cmd.ServerID, DeploymentID: cmd.DeploymentID, Kind: cmd.Kind,
		Payload: payload, IdempotencyKey: key, ExpiresAt: h.now().Add(CommandTTL),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("enqueue: insert command: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", notifyChannel, cmd.ServerID.String()); err != nil {
		return uuid.Nil, fmt.Errorf("enqueue: notify: %w", err)
	}
	return row.ID, nil
}

// UndeliveredCommand identifies a command canceled before it was ever acked.
type UndeliveredCommand struct {
	ID           uuid.UUID
	ServerID     uuid.UUID
	DeploymentID *uuid.UUID
}

// AfterRevoke finishes a server revocation once its transaction committed: the
// deployments of the canceled commands are failed and the live connection, if
// any, is closed with 4401.
func (h *Hub) AfterRevoke(ctx context.Context, serverID uuid.UUID, canceled []UndeliveredCommand) {
	h.Disconnect(serverID, agentprotocol.CloseRevoked, "agent revoked")
	h.reportUndelivered(ctx, canceled, RevokedMessage)
	h.publishServer(serverID)
}

func (h *Hub) reportUndelivered(ctx context.Context, cmds []UndeliveredCommand, msg string) {
	rh := h.resultHandler()
	if rh == nil {
		return
	}
	for _, c := range cmds {
		if err := rh.HandleUndelivered(ctx, c.ServerID, c.ID, c.DeploymentID, msg); err != nil {
			h.log.ErrorContext(ctx, "handle undelivered command", "command_id", c.ID, "server_id", c.ServerID, "err", err)
		}
	}
}

// ExpireCommands expires never-acked commands past their deadline and fails
// their deployments. Acked commands are never touched.
func (h *Hub) ExpireCommands(ctx context.Context) (int, error) {
	rows, err := h.q.ExpireAgentCommands(ctx)
	if err != nil {
		return 0, fmt.Errorf("expire commands: %w", err)
	}
	out := make([]UndeliveredCommand, 0, len(rows))
	for _, r := range rows {
		out = append(out, UndeliveredCommand{ID: r.ID, ServerID: r.ServerID, DeploymentID: r.DeploymentID})
	}
	h.reportUndelivered(ctx, out, UndeliveredMessage)
	return len(out), nil
}

// SweepOffline marks servers without a recent heartbeat offline.
func (h *Hub) SweepOffline(ctx context.Context) (int, error) {
	ids, err := h.q.MarkStaleServersOffline(ctx, h.now().Add(-agentprotocol.OfflineAfter))
	if err != nil {
		return 0, fmt.Errorf("sweep offline: %w", err)
	}
	for _, id := range ids {
		h.log.InfoContext(ctx, "server offline", "server_id", id)
		h.publishServer(id)
		h.notifyOffline(ctx, id)
	}
	return len(ids), nil
}

// startLoop launches the delivery loop once.
func (h *Hub) startLoop() {
	h.loopOnce.Do(func() {
		h.wg.Add(2)
		go func() { defer h.wg.Done(); h.listenLoop(h.rootCtx) }()
		go func() { defer h.wg.Done(); h.pollLoop(h.rootCtx) }()
	})
}

func (h *Hub) pollLoop(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ids, err := h.q.ListServersWithPendingCommands(ctx)
		if err != nil {
			if ctx.Err() == nil {
				h.log.Warn("poll pending commands", "err", err)
			}
			continue
		}
		for _, id := range ids {
			h.Kick(id)
		}
	}
}

// listenLoop turns pg_notify wake-ups into immediate deliveries. It uses its
// own connection (not the pool's) so shutting the pool down never waits for it.
func (h *Hub) listenLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if err := h.listenOnce(ctx); err != nil && ctx.Err() == nil {
			h.log.Warn("command listener stopped; polling continues", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context) error {
	if h.pool == nil {
		return errors.New("no database pool")
	}
	cc := h.pool.Config().ConnConfig.Copy()
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if id, err := uuid.Parse(n.Payload); err == nil {
			h.Kick(id)
		}
	}
}

// problem writes an RFC 9457 error on a raw route.
func problem(w http.ResponseWriter, e *apierr.Error) { apierr.Write(w, e) }
