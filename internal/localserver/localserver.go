// Package localserver enrolls the control-plane host as the first server.
//
// The installer generates an enrollment token, hands it to the agent it
// installs on the host and to the control plane (NEBULACTRL_LOCAL_ENROLL_TOKEN,
// which only ever holds the token). A server belongs to a team, and the team is
// normally created by the person who opens the setup page, so an Ensurer first
// creates the team itself (auth.BootstrapTeam; Setup adopts it, the first user
// becomes its owner), then the server row (flagged is_control_plane) and the
// token's hash, exactly as POST /servers/enrollment-tokens would have, and keeps
// the token redeemable until the agent enrolls. The agent, restarted by systemd
// while its token is rejected, enrolls by itself; its proxy then serves the
// dashboard (package systemroute) before the admin account exists.
package localserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
)

// DefaultName is the server's name when the installer did not pass one.
const DefaultName = "control-plane"

// pollInterval is how often Run looks for the first team.
const pollInterval = 10 * time.Second

// Ensurer creates and maintains the control-plane host's server row.
type Ensurer struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	cfg  config.Installer
	log  *slog.Logger
	now  func() time.Time
}

// New returns an Ensurer. It does nothing without cfg.Installer.LocalEnrollToken.
func New(pool *pgxpool.Pool, bus *events.Bus, cfg *config.Config, logger *slog.Logger) *Ensurer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Ensurer{pool: pool, q: db.New(pool), bus: bus, cfg: cfg.Installer, log: logger.With("component", "localserver"), now: time.Now}
}

// Enabled reports whether the installer configured a local agent token.
func (e *Ensurer) Enabled() bool { return e.cfg.LocalEnrollToken != "" }

// Run polls until the server is enrolled (or ctx ends).
func (e *Ensurer) Run(ctx context.Context) {
	if !e.Enabled() {
		return
	}
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		done, err := e.Ensure(ctx)
		if err != nil && ctx.Err() == nil {
			e.log.WarnContext(ctx, "ensure the control-plane server", "err", err)
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Ensure creates the server row (and its enrollment token) once a team exists
// and keeps an unredeemed token alive. done is true when nothing is left to do:
// the agent enrolled, or the server was revoked (a revoked control-plane server
// is never recreated).
func (e *Ensurer) Ensure(ctx context.Context) (done bool, err error) {
	if !e.Enabled() {
		return true, nil
	}
	hash := agents.HashEnrollmentToken(e.cfg.LocalEnrollToken)
	// The first team exists before the first user does (Setup adopts it), so the
	// agent can enroll, and the dashboard's route and certificate come up, while
	// the admin account is still to be created.
	ws, err := auth.BootstrapTeam(ctx, e.pool, auth.DefaultTeamName)
	if err != nil {
		return false, fmt.Errorf("bootstrap team: %w", err)
	}
	existed, err := e.q.ControlPlaneServerExists(ctx)
	if err != nil {
		return false, fmt.Errorf("check control-plane server: %w", err)
	}
	expires := e.now().Add(agents.EnrollmentTokenTTL)

	if existed {
		srv, err := e.q.GetControlPlaneServer(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil // revoked
		}
		if err != nil {
			return false, fmt.Errorf("get control-plane server: %w", err)
		}
		if srv.Status != "pending" {
			return true, nil
		}
		n, err := e.q.RefreshUnusedEnrollmentToken(ctx, db.RefreshUnusedEnrollmentTokenParams{TokenHash: hash, ExpiresAt: expires})
		if err != nil {
			return false, fmt.Errorf("refresh enrollment token: %w", err)
		}
		if n > 0 {
			return false, nil
		}
		used, err := e.q.EnrollmentTokenExists(ctx, hash)
		if err != nil {
			return false, fmt.Errorf("look up enrollment token: %w", err)
		}
		if used {
			return false, nil // redeemed or revoked; the agent's enrollment is on its way
		}
		// The row exists but its token does not (the token table was cleaned): add it again.
		return false, db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
			return e.insertToken(ctx, db.New(tx), ws.ID, srv.ID, hash, expires)
		})
	}

	name := strings.TrimSpace(e.cfg.LocalServerName)
	if name == "" {
		name = DefaultName
	}
	var ip, acme *string
	if a, perr := netip.ParseAddr(strings.TrimSpace(e.cfg.LocalServerIP)); perr == nil {
		s := a.String()
		ip = &s
	}
	if v := strings.TrimSpace(e.cfg.ACMEEmail); v != "" {
		acme = &v
	}
	var created db.Server
	err = db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		created, err = q.CreateControlPlaneServer(ctx, db.CreateControlPlaneServerParams{
			WorkspaceID: ws.ID, Name: name, PublicIp: ip, AcmeEmail: acme,
		})
		if err != nil {
			return err
		}
		if err := e.insertToken(ctx, q, ws.ID, created.ID, hash, expires); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "server.enroll_token.create", ResourceType: "server", ResourceID: created.ID.String(), WorkspaceID: ws.ID,
			Metadata: map[string]any{"name": name, "controlPlane": true, "by": "installer"},
		})
	})
	if err != nil {
		return false, fmt.Errorf("create control-plane server: %w", err)
	}
	e.log.InfoContext(ctx, "control-plane server created; waiting for its agent to enroll", "server_id", created.ID.String(), "name", name)
	if e.bus != nil {
		e.bus.Publish(events.Event{Type: "server.updated", ResourceID: created.ID.String()})
	}
	return false, nil
}

func (e *Ensurer) insertToken(ctx context.Context, q *db.Queries, workspaceID, serverID uuid.UUID, hash []byte, expires time.Time) error {
	var createdBy *uuid.UUID
	if id, err := q.FirstUserID(ctx); err == nil {
		createdBy = &id
	}
	_, err := q.CreateEnrollmentToken(ctx, db.CreateEnrollmentTokenParams{
		WorkspaceID: workspaceID, ServerID: serverID, TokenHash: hash, ExpiresAt: expires, CreatedBy: createdBy,
	})
	return err
}
