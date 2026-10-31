package logs

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type HistoryQuery struct {
	From   time.Time `query:"from" doc:"Only lines at or after this time (RFC 3339). Default: the oldest line the server keeps."`
	To     time.Time `query:"to" doc:"Only lines before this time (RFC 3339). Default: now."`
	Q      string    `query:"q" doc:"A log search query (the same language as the log views): free text, \"phrases\", level:error, -service:web, @status:[400 TO 499]. Evaluated on the server that stores the log."`
	Limit  int       `query:"limit" minimum:"1" maximum:"2000" doc:"Page size (default 500, max 2000)."`
	Cursor string    `query:"cursor" doc:"Opaque cursor from a previous page's nextCursor."`
	Order  string    `query:"order" enum:"desc,asc" doc:"desc (default): newest first. asc: oldest first."`
}

func (q HistoryQuery) params() HistoryParams {
	return HistoryParams{From: q.From, To: q.To, Query: q.Q, Limit: q.Limit, Cursor: q.Cursor, Order: q.Order}
}

type serviceHistoryInput struct {
	ID        uuid.UUID `path:"serviceId"`
	Component string    `query:"component" doc:"Compose stacks only: only the lines of this component (stored lines keep the '<component> | ' prefix the agent adds)."`
	Process   string    `query:"process" doc:"Services with processes only: only the lines of this process (stored lines keep the '<process> | ' prefix the agent adds); without it every process. Not together with component."`
	HistoryQuery
}

type envHistoryInput struct {
	ID uuid.UUID `path:"environmentId"`
	HistoryQuery
}

type historyOutput struct{ Body LogHistoryPage }

type serviceCentralInput struct {
	ID      uuid.UUID `path:"serviceId"`
	Process string    `query:"process" doc:"Services with processes only: only the lines of this process (lines carry a '<process> | ' prefix); without it every process."`
	Q       string    `query:"q" maxLength:"500" doc:"Only lines that contain this text (a plain, case-sensitive substring)."`
	Since   string    `query:"since" doc:"Start of the window: a duration back from the end (15m, 2h, 3d) or an RFC 3339 time. Default 1h. Not with from."`
	From    time.Time `query:"from" doc:"Start of the window (RFC 3339). Default: one hour before the end."`
	To      time.Time `query:"to" doc:"End of the window (RFC 3339). Default: now."`
	Limit   int       `query:"limit" minimum:"1" maximum:"1000" doc:"Lines to return (default 200, max 1000)."`
}

type centralOutput struct{ Body CentralLogPage }

type serviceDownloadInput struct {
	ID        uuid.UUID `path:"serviceId"`
	Component string    `query:"component" doc:"Compose stacks only: only the lines of this component."`
	Process   string    `query:"process" doc:"Services with processes only: only the lines of this process; without it every process. Not together with component."`
	From      time.Time `query:"from" doc:"Only lines at or after this time (RFC 3339). Default: the oldest line the server keeps."`
	To        time.Time `query:"to" doc:"Only lines before this time (RFC 3339). Default: now."`
	Q         string    `query:"q" maxLength:"2000" doc:"A log search query (the same language as the log views)."`
	Format    string    `query:"format" enum:"txt,ndjson" default:"txt" doc:"txt (default) or ndjson."`
}

type drainIDInput struct {
	ID uuid.UUID `path:"drainId"`
}

type drainOutput struct{ Body LogDrainView }

type drainListOutput struct {
	Body struct {
		Items []LogDrainView `json:"items"`
	}
}

// drainConfigBody is the settings shared by create and update. Exactly the
// settings of the drain's kind are used.
type drainConfigBody struct {
	Loki   *agentprotocol.LokiDrainConfig   `json:"loki,omitempty" doc:"Loki push API: url (the full /loki/api/v1/push URL), tenantId, static labels."`
	OTLP   *agentprotocol.OTLPDrainConfig   `json:"otlp,omitempty" doc:"OTLP/HTTP logs: url (the full /v1/logs URL), resource attributes."`
	Syslog *agentprotocol.SyslogDrainConfig `json:"syslog,omitempty" doc:"RFC 5424 syslog: address host:port, protocol udp, tcp or tcp+tls."`
	HTTP   *agentprotocol.HTTPDrainConfig   `json:"http,omitempty" doc:"Generic HTTP JSON batches (Datadog, Better Stack, Axiom intakes, your own endpoint)."`
}

func (b drainConfigBody) config() DrainConfig {
	return DrainConfig{Loki: b.Loki, OTLP: b.OTLP, Syslog: b.Syslog, HTTP: b.HTTP}
}

type createDrainInput struct {
	Body struct {
		Name        string               `json:"name" minLength:"1" maxLength:"80"`
		Kind        string               `json:"kind" enum:"loki,otlp,syslog,http"`
		Enabled     *bool                `json:"enabled,omitempty" doc:"Default true."`
		Config      drainConfigBody      `json:"config"`
		Secrets     LogDrainSecretsInput `json:"secrets,omitempty"`
		AllProjects *bool                `json:"allProjects,omitempty" doc:"Default true: every project of the team. false: only projectIds."`
		ProjectIDs  []uuid.UUID          `json:"projectIds,omitempty"`
		ServiceIDs  []uuid.UUID          `json:"serviceIds,omitempty" doc:"Narrow the scope to these services."`
		Query       string               `json:"query,omitempty" doc:"Only ship lines matching this log search query."`
		BufferMB    int                  `json:"bufferMb,omitempty" minimum:"0" maximum:"5120" doc:"Undelivered backlog kept on disk per server (default 256, min 8)."`
	}
}

type patchDrainInput struct {
	ID   uuid.UUID `path:"drainId"`
	Body struct {
		Name        *string               `json:"name,omitempty" minLength:"1" maxLength:"80"`
		Enabled     *bool                 `json:"enabled,omitempty"`
		Config      *drainConfigBody      `json:"config,omitempty" doc:"Replaces the non-secret settings of the drain's kind."`
		Secrets     *LogDrainSecretsInput `json:"secrets,omitempty" doc:"Fields left empty keep the stored credential."`
		AllProjects *bool                 `json:"allProjects,omitempty"`
		ProjectIDs  *[]uuid.UUID          `json:"projectIds,omitempty"`
		ServiceIDs  *[]uuid.UUID          `json:"serviceIds,omitempty"`
		Query       *string               `json:"query,omitempty"`
		BufferMB    *int                  `json:"bufferMb,omitempty" minimum:"1" maximum:"5120"`
	}
}

type testDrainInput struct {
	ID   uuid.UUID `path:"drainId"`
	Body struct {
		ServerID *uuid.UUID `json:"serverId,omitempty" doc:"Send from this server. Default: the first connected one."`
	}
}

type testDrainOutput struct{ Body LogDrainTestResult }

type serverIDInput struct {
	ID uuid.UUID `path:"serverId"`
}

type settingsOutput struct{ Body LogSettingsView }

type putSettingsInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Capture         bool  `json:"capture"`
		RetentionDays   int   `json:"retentionDays" minimum:"1" maximum:"90"`
		MaxServiceBytes int64 `json:"maxServiceBytes" minimum:"33554432"`
		MaxServerBytes  int64 `json:"maxServerBytes" minimum:"33554432"`
	}
}

// Register attaches the log history and drain endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	tag := []string{"logs"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceLogHistory", Method: http.MethodGet, Path: "/services/{serviceId}/logs/history", Tags: tag,
		Summary: "Stored runtime logs of a service",
		Description: "A page of the service's runtime log from the server's own history (7 days by default), within an optional time range, filtered by the log search language. " +
			"The server's agent answers, so history is unavailable (503 `server_offline`) while the server is offline. Secret values are redacted. " +
			"Page with `nextCursor`; `partial: true` means the scan stopped early and the cursor continues it.",
		Errors: append(errs, http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout),
	}, policy.ScopeLogsRead), func(ctx context.Context, in *serviceHistoryInput) (*historyOutput, error) {
		p := in.params()
		p.Component, p.Process = in.Component, in.Process
		page, err := svc.ServiceHistory(ctx, in.ID, p)
		if err != nil {
			return nil, err
		}
		return &historyOutput{Body: page}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceCentralLogs", Method: http.MethodGet, Path: "/services/{serviceId}/logs/central", Tags: tag,
		Summary: "Central logs of a service",
		Description: "The service's lines from the team's central log store, collected from every server it ran on, newest first. " +
			"Lines arrive while the team's built-in log drain is on (Team settings, Log forwarding); while it is off the answer has `enabled: false`, no lines and a `hint`. " +
			"The window is the last hour unless `since`, `from` or `to` say otherwise, at most 7 days (a longer range is cut to its newest 7 days); `limit` defaults to 200, at most 1000. " +
			"`q` keeps the lines that contain the text. Secret values are redacted. The log store is the control plane's own, so this answers while the service's server is offline.",
		Errors: append(errs, http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeLogsRead), func(ctx context.Context, in *serviceCentralInput) (*centralOutput, error) {
		page, err := svc.ServiceCentralLogs(ctx, in.ID, CentralParams{Query: in.Q, Process: in.Process, Since: in.Since, From: in.From, To: in.To, Limit: in.Limit})
		if err != nil {
			return nil, err
		}
		return &centralOutput{Body: page}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getEnvironmentLogHistory", Method: http.MethodGet, Path: "/environments/{environmentId}/logs/history", Tags: tag,
		Summary: "Stored runtime logs of an environment",
		Description: "The same as the service endpoint for every service of the environment, merged by time. Entries carry `serviceId` and `serviceName`. " +
			"Services whose server is offline are listed in `unavailable` instead of failing the request.",
		Errors: append(errs, http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusBadGateway, http.StatusGatewayTimeout),
	}, policy.ScopeLogsRead), func(ctx context.Context, in *envHistoryInput) (*historyOutput, error) {
		page, err := svc.EnvironmentHistory(ctx, in.ID, in.params())
		if err != nil {
			return nil, err
		}
		return &historyOutput{Body: page}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "downloadServiceLogs", Method: http.MethodGet, Path: "/services/{serviceId}/logs/download", Tags: tag,
		Summary: "Download a service's stored runtime logs as a file",
		Description: "The service's runtime log from the server's own history (7 days by default) as `format=txt` (default; one `<time> <line>` per line) " +
			"or `format=ndjson` (`{ts, stream, replica, deploymentId, line}` per line), oldest first, within an optional `from`/`to` range and `q` filter. " +
			"Secret values are redacted exactly as in the log view. At most 200,000 lines: a longer range ends with a truncation note, so narrow it. " +
			"The history comes from the server's agent: 503 `server_offline` while it is offline, 409 `history_disabled` when capture is off. Audited as `service.logs.download`.",
		Responses: services.DownloadResponses(),
		Errors:    append(errs, http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout),
	}, policy.ScopeLogsRead), func(ctx context.Context, in *serviceDownloadInput) (*huma.StreamResponse, error) {
		dl, err := svc.OpenDownload(ctx, in.ID, DownloadParams{From: in.From, To: in.To, Query: in.Q, Component: in.Component, Process: in.Process, Format: in.Format})
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			dl.WriteTo(hctx.Context(), services.NewLogDownload(hctx, dl.Format(), dl.Filename))
		}}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listLogDrains", Method: http.MethodGet, Path: "/log-drains", Tags: tag,
		Summary: "List log drains",
		Description: "Destinations the servers ship runtime logs to. Credentials are never returned; `servers` carries the drain's health as each server's agent reports it. " +
			"The team's built-in drain (`builtin: true`, the instance's log store) is listed with the others; it starts off. Requires the admin scope.",
		Errors: errs[:2],
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*drainListOutput, error) {
		items, err := svc.ListDrains(ctx)
		if err != nil {
			return nil, err
		}
		out := &drainListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createLogDrain", Method: http.MethodPost, Path: "/log-drains", Tags: tag,
		Summary: "Add a log drain", DefaultStatus: http.StatusCreated,
		Description: "Kinds: `loki` (push URL, tenant, labels; basic auth or bearer token), `otlp` (OTLP/HTTP logs URL; bearer token and/or headers), " +
			"`syslog` (RFC 5424 over UDP, TCP or TCP+TLS) and `http` (JSON batches to any intake: Datadog, Better Stack, Axiom; credentials as headers, bearer or basic). " +
			"Every connected server ships its lines from its own host, starting from the moment the drain is saved. Requires the admin scope.",
		Errors: append(errs[:2], http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *createDrainInput) (*drainOutput, error) {
		v, err := svc.CreateDrain(ctx, DrainInput{
			Name: in.Body.Name, Kind: in.Body.Kind, Enabled: in.Body.Enabled, Config: in.Body.Config.config(), Secrets: in.Body.Secrets,
			AllProjects: in.Body.AllProjects, ProjectIDs: in.Body.ProjectIDs, ServiceIDs: in.Body.ServiceIDs, Query: in.Body.Query, BufferMB: in.Body.BufferMB,
		})
		if err != nil {
			return nil, err
		}
		return &drainOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getLogDrain", Method: http.MethodGet, Path: "/log-drains/{drainId}", Tags: tag,
		Summary: "Get a log drain", Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *drainIDInput) (*drainOutput, error) {
		v, err := svc.GetDrain(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &drainOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateLogDrain", Method: http.MethodPatch, Path: "/log-drains/{drainId}", Tags: tag,
		Summary: "Update a log drain",
		Description: "Omitted fields stay as they are; empty credentials keep the stored ones (`secrets.clear` removes some). The kind cannot change. " +
			"The built-in drain (`builtin: true`) can only be switched on or off (`enabled`) and have its scope changed: renaming it or changing its settings or credentials answers 409 `builtin_drain`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *patchDrainInput) (*drainOutput, error) {
		p := DrainPatch{
			Name: in.Body.Name, Enabled: in.Body.Enabled, Secrets: in.Body.Secrets, AllProjects: in.Body.AllProjects,
			ProjectIDs: in.Body.ProjectIDs, ServiceIDs: in.Body.ServiceIDs, Query: in.Body.Query, BufferMB: in.Body.BufferMB,
		}
		if in.Body.Config != nil {
			c := in.Body.Config.config()
			p.Config = &c
		}
		v, err := svc.UpdateDrain(ctx, in.ID, p)
		if err != nil {
			return nil, err
		}
		return &drainOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteLogDrain", Method: http.MethodDelete, Path: "/log-drains/{drainId}", Tags: tag,
		Summary: "Delete a log drain", Description: "Servers stop shipping and forget the drain's position. The built-in drain (`builtin: true`) cannot be deleted (409 `builtin_drain`): turn it off instead.",
		DefaultStatus: http.StatusNoContent, Errors: append(errs, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *drainIDInput) (*struct{}, error) {
		return nil, svc.DeleteDrain(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testLogDrain", Method: http.MethodPost, Path: "/log-drains/{drainId}/test", Tags: tag,
		Summary: "Send a test line through a drain",
		Description: "Sends one test line from a connected server's agent, which is where reachability and credentials count, and returns the outcome (`ok`, a message, the HTTP status). " +
			"The result is also kept on the drain's `test` state. Works on a disabled drain.",
		Errors: append(errs, http.StatusConflict, http.StatusServiceUnavailable, http.StatusGatewayTimeout),
	}, policy.ScopeAdmin), func(ctx context.Context, in *testDrainInput) (*testDrainOutput, error) {
		res, err := svc.TestDrain(ctx, in.ID, in.Body.ServerID)
		if err != nil {
			return nil, err
		}
		return &testDrainOutput{Body: res}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerLogSettings", Method: http.MethodGet, Path: "/servers/{serverId}/log-settings", Tags: tag,
		Summary:     "A server's log history settings",
		Description: "Capture on or off, retention days and size caps, with the usage the agent last reported.",
		Errors:      errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*settingsOutput, error) {
		v, err := svc.GetSettings(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &settingsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateServerLogSettings", Method: http.MethodPut, Path: "/servers/{serverId}/log-settings", Tags: tag,
		Summary:     "Change a server's log history settings",
		Description: "Retention 1-90 days (default 7); size caps of at least 32 MB (defaults 500 MB per service, 5 GB per server; the oldest lines go first). The agent applies them at once.",
		Errors:      append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeServersWrite), func(ctx context.Context, in *putSettingsInput) (*settingsOutput, error) {
		v, err := svc.UpdateSettings(ctx, in.ID, SettingsInput{
			Capture: in.Body.Capture, RetentionDays: in.Body.RetentionDays, MaxServiceBytes: in.Body.MaxServiceBytes, MaxServerBytes: in.Body.MaxServerBytes,
		})
		if err != nil {
			return nil, err
		}
		return &settingsOutput{Body: v}, nil
	})
}
