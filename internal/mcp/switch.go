package mcp

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Switch decides whether the MCP endpoint answers. NEBULACTRL_MCP_ENABLED,
// when set (or a config built with MCPEnabled, as in tests), decides and locks
// it; otherwise the instance admin turns it on and off from the web UI, and
// the choice is stored in instance_settings. The value is kept in memory and
// changed only through Set, so the endpoint costs no query per request.
type Switch struct {
	locked bool
	on     atomic.Bool
	pool   *pgxpool.Pool
	auth   *auth.Service
}

// NewSwitch builds the switch from the configuration. Load reads the stored
// setting when the environment does not decide.
func NewSwitch(cfg *config.Config, pool *pgxpool.Pool, a *auth.Service) *Switch {
	s := &Switch{pool: pool, auth: a}
	if cfg != nil && (cfg.MCPFromEnv || cfg.MCPEnabled) {
		s.locked = true
		s.on.Store(cfg.MCPEnabled)
	}
	return s
}

// Load reads the stored setting. A locked switch keeps the environment's value.
func (s *Switch) Load(ctx context.Context) error {
	if s == nil || s.locked || s.pool == nil {
		return nil
	}
	set, err := db.New(s.pool).GetInstanceSettings(ctx)
	if err != nil {
		return fmt.Errorf("mcp setting: %w", err)
	}
	s.on.Store(set.McpEnabled)
	return nil
}

// Enabled reports whether the MCP endpoint answers.
func (s *Switch) Enabled() bool { return s != nil && s.on.Load() }

// Locked reports whether NEBULACTRL_MCP_ENABLED decides, so the switch cannot change.
func (s *Switch) Locked() bool { return s == nil || s.locked }

// Set turns the endpoint on or off (the instance admin only; audited). It is
// refused with 409 mcp_locked while the environment decides.
func (s *Switch) Set(ctx context.Context, enabled bool) error {
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return err
	}
	if s.Locked() {
		return apierr.Conflict("mcp_locked", "NEBULACTRL_MCP_ENABLED is set on the control plane, so it decides; remove it to switch MCP here")
	}
	if s.auth == nil || s.pool == nil {
		return apierr.Forbidden()
	}
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return apierr.Forbidden()
	}
	admin, err := s.auth.IsInstanceAdmin(ctx, p.UserID)
	if err != nil {
		return err
	}
	if !admin {
		return apierr.Forbidden()
	}
	action := "instance.mcp.disable"
	if enabled {
		action = "instance.mcp.enable"
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).SetInstanceMCPEnabled(ctx, enabled); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "instance", ResourceID: "instance", WorkspaceID: p.WorkspaceID,
		})
	})
	if err != nil {
		return fmt.Errorf("set mcp: %w", err)
	}
	s.on.Store(enabled)
	return nil
}
