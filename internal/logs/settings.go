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
	Capture               bool            `json:"capture" doc:"Capture the runtime output of this server's services. Off: nothing new is stored; what is stored stays until it expires."`
	RetentionDays         int             `json:"retentionDays"`
	MaxServiceBytes       int64           `json:"maxServiceBytes" doc:"Cap per service; the oldest lines go first."`
	MaxServerBytes        int64           `json:"maxServerBytes" doc:"Cap for the whole server; the oldest lines of any service go first."`
	RequestLogs           bool            `json:"requestLogs" doc:"Store one line per routed HTTP request on the host, searchable on the Logs page. Needs traffic analytics and capture on; requests are never shipped to drains. Retention days follow the runtime logs."`
	RequestLogsKeepQuery  bool            `json:"requestLogsKeepQuery" doc:"Keep the query string of a request line. Off: it is dropped before anything is written."`
	RequestLogsKeepAssets bool            `json:"requestLogsKeepAssets" doc:"Store the successful requests (2xx, 304) for scripts, stylesheets, images and fonts too. Off: only failed asset requests are stored; pages and API requests always are."`
	RequestsTotalBytes    int64           `json:"requestsTotalBytes" doc:"Disk the request logs use on the host, as the agent last reported; 0 until it has."`
	Usage                 *LogServerUsage `json:"usage,omitempty" doc:"What the agent last reported; absent until it has."`
	Online                bool            `json:"online"`
}

// SettingsInput replaces the setting. A nil RequestLogs, RequestLogsKeepQuery
// or RequestLogsKeepAssets keeps the stored value.
type SettingsInput struct {
	Capture               bool
	RetentionDays         int
	MaxServiceBytes       int64
	MaxServerBytes        int64
	RequestLogs           *bool
	RequestLogsKeepQuery  *bool
	RequestLogsKeepAssets *bool
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
	row, err := s.settingsRow(ctx, s.q, serverID)
	if err != nil {
		return LogSettingsView{}, fmt.Errorf("log settings: %w", err)
	}
	v := LogSettingsView{
		Capture: row.Capture, RetentionDays: int(row.RetentionDays), MaxServiceBytes: row.MaxServiceBytes, MaxServerBytes: row.MaxServerBytes,
		RequestLogs: row.RequestLogs, RequestLogsKeepQuery: row.RequestLogsKeepQuery, RequestLogsKeepAssets: row.RequestLogsKeepAssets,
		Online: s.hub != nil && s.hub.Online(serverID),
	}
	if row.UsageAt != nil && len(row.Usage) > 0 {
		var u storedLogUsage
		if err := json.Unmarshal(row.Usage, &u); err == nil {
			if u.Services == nil {
				u.Services = []agentprotocol.LogServiceUsage{}
			}
			v.Usage = &LogServerUsage{TotalBytes: u.TotalBytes, Services: u.Services, ReportedAt: *row.UsageAt}
			v.RequestsTotalBytes = u.RequestsTotalBytes
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
		q := db.New(tx)
		cur, err := s.settingsRow(ctx, q, serverID)
		if err != nil {
			return err
		}
		requestLogs, keepQuery, keepAssets := cur.RequestLogs, cur.RequestLogsKeepQuery, cur.RequestLogsKeepAssets
		if in.RequestLogs != nil {
			requestLogs = *in.RequestLogs
		}
		if in.RequestLogsKeepQuery != nil {
			keepQuery = *in.RequestLogsKeepQuery
		}
		if in.RequestLogsKeepAssets != nil {
			keepAssets = *in.RequestLogsKeepAssets
		}
		if _, err := q.UpsertServerLogSettings(ctx, db.UpsertServerLogSettingsParams{
			ServerID: serverID, Capture: in.Capture, RetentionDays: int32(in.RetentionDays),
			MaxServiceBytes: in.MaxServiceBytes, MaxServerBytes: in.MaxServerBytes,
			RequestLogs: requestLogs, RequestLogsKeepQuery: keepQuery, RequestLogsKeepAssets: keepAssets,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionSettingsUpdate, ResourceType: "server", ResourceID: serverID.String(),
			Metadata: map[string]any{
				"server": srv.Name, "capture": in.Capture, "retentionDays": in.RetentionDays,
				"maxServiceBytes": in.MaxServiceBytes, "maxServerBytes": in.MaxServerBytes,
				"requestLogs": requestLogs, "requestLogsKeepQuery": keepQuery, "requestLogsKeepAssets": keepAssets,
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
