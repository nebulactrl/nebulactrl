package logs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// LogSettingsView is a server's log history setting with the agent's last usage report.
type LogSettingsView struct {
	Capture         bool            `json:"capture" doc:"Capture the runtime output of this server's services. Off: nothing new is stored; what is stored stays until it expires."`
	RetentionDays   int             `json:"retentionDays"`
	MaxServiceBytes int64           `json:"maxServiceBytes" doc:"Cap per service; the oldest lines go first."`
	MaxServerBytes  int64           `json:"maxServerBytes" doc:"Cap for the whole server; the oldest lines of any service go first."`
	Usage           *LogServerUsage `json:"usage,omitempty" doc:"What the agent last reported; absent until it has."`
	Online          bool            `json:"online"`
}

// SettingsInput replaces the setting.
type SettingsInput struct {
	Capture         bool
	RetentionDays   int
	MaxServiceBytes int64
	MaxServerBytes  int64
}

func (s *Service) server(ctx context.Context, scope string, id uuid.UUID) (db.Server, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.Server{}, err
	}
	ws := policy.WorkspaceID(ctx)
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: id, WorkspaceID: &ws})
	if isNoRows(err) {
		return srv, apierr.NotFound("server")
	}
	if err != nil {
		return srv, fmt.Errorf("get server: %w", err)
	}
	return srv, nil
}

func (s *Service) settingsView(ctx context.Context, serverID uuid.UUID) (LogSettingsView, error) {
	capture, ret, row, err := s.settingsRow(ctx, serverID)
	if err != nil {
		return LogSettingsView{}, fmt.Errorf("log settings: %w", err)
	}
	v := LogSettingsView{
		Capture: capture, RetentionDays: ret.Days, MaxServiceBytes: ret.MaxServiceBytes, MaxServerBytes: ret.MaxServerBytes,
		Online: s.hub != nil && s.hub.Online(serverID),
	}
	if row != nil && row.UsageAt != nil && len(row.Usage) > 0 {
		var u LogServerUsage
		if err := json.Unmarshal(row.Usage, &u); err == nil {
			u.ReportedAt = *row.UsageAt
			if u.Services == nil {
				u.Services = []agentprotocol.LogServiceUsage{}
			}
			v.Usage = &u
		}
	}
	return v, nil
}

// GetSettings returns a server's log history setting.
func (s *Service) GetSettings(ctx context.Context, serverID uuid.UUID) (LogSettingsView, error) {
	if _, err := s.server(ctx, policy.ScopeServersRead, serverID); err != nil {
		return LogSettingsView{}, err
	}
	return s.settingsView(ctx, serverID)
}

// UpdateSettings replaces a server's log history setting and pushes it to the agent.
func (s *Service) UpdateSettings(ctx context.Context, serverID uuid.UUID, in SettingsInput) (LogSettingsView, error) {
	srv, err := s.server(ctx, policy.ScopeServersWrite, serverID)
	if err != nil {
		return LogSettingsView{}, err
	}
	if in.RetentionDays < 1 || in.RetentionDays > agentprotocol.MaxLogRetentionDays {
		return LogSettingsView{}, bad(fmt.Sprintf("retentionDays must be between 1 and %d", agentprotocol.MaxLogRetentionDays))
	}
	if in.MaxServiceBytes < agentprotocol.MinLogMaxBytes || in.MaxServerBytes < agentprotocol.MinLogMaxBytes {
		return LogSettingsView{}, bad(fmt.Sprintf("a size limit must be at least %d MB", agentprotocol.MinLogMaxBytes>>20))
	}
	if in.MaxServiceBytes > in.MaxServerBytes {
		return LogSettingsView{}, bad("the per-service limit cannot exceed the server limit")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).UpsertServerLogSettings(ctx, db.UpsertServerLogSettingsParams{
			ServerID: serverID, Capture: in.Capture, RetentionDays: int32(in.RetentionDays),
			MaxServiceBytes: in.MaxServiceBytes, MaxServerBytes: in.MaxServerBytes,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionSettingsUpdate, ResourceType: "server", ResourceID: serverID.String(),
			Metadata: map[string]any{
				"server": srv.Name, "capture": in.Capture, "retentionDays": in.RetentionDays,
				"maxServiceBytes": in.MaxServiceBytes, "maxServerBytes": in.MaxServerBytes,
			},
		})
	})
	if err != nil {
		return LogSettingsView{}, fmt.Errorf("update log settings: %w", err)
	}
	s.publish(EventSettingsUpdated, srv.WorkspaceID, serverID)
	if s.hub != nil && s.hub.Online(serverID) {
		if err := s.hub.Resync(context.WithoutCancel(ctx), serverID); err != nil {
			s.log.WarnContext(ctx, "resync log settings", "server_id", serverID, "err", err)
		}
	}
	return s.settingsView(ctx, serverID)
}
