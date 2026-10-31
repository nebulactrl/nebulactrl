package servers

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type listServersInput struct{ api.PageQuery }

type listServersOutput struct{ Body api.Page[Server] }

type serverIDInput struct {
	ID uuid.UUID `path:"serverId"`
}

type serverOutput struct{ Body Server }

type removeServerInput struct {
	ID      uuid.UUID `path:"serverId"`
	Confirm string    `query:"confirm" doc:"The server's name"`
	Mode    string    `query:"mode" enum:"clean,forget" default:"clean" doc:"clean empties the host through the agent first; forget leaves the host as it is"`
}

type removeServerOutput struct {
	Status int
	Body   ServerRemoval
}

type updateServerInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Name       *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
		PublicIP   *string `json:"publicIp,omitempty" maxLength:"45" doc:"Empty string clears"`
		BaseDomain *string `json:"baseDomain,omitempty" maxLength:"253" doc:"Empty string clears"`
		ACMEEmail  *string `json:"acmeEmail,omitempty" maxLength:"254" doc:"Empty string clears"`

		MetricsRetentionDays       *int  `json:"metricsRetentionDays,omitempty" minimum:"1" maximum:"90" doc:"Days of resource metric history (default 7)"`
		DiskAlertThresholdPercent  *int  `json:"diskAlertThresholdPercent,omitempty" minimum:"50" maximum:"99" doc:"Disk usage percent that raises the disk alert (default 80)"`
		TrafficAnalyticsEnabled    *bool `json:"trafficAnalyticsEnabled,omitempty" doc:"Turns Traefik access-log analytics on or off (default on)"`
		TrafficHourlyRetentionDays *int  `json:"trafficHourlyRetentionDays,omitempty" minimum:"2" maximum:"90" doc:"Days of hourly traffic history (default 7)"`
		TrafficDailyRetentionDays  *int  `json:"trafficDailyRetentionDays,omitempty" minimum:"7" maximum:"365" doc:"Days of daily traffic history (default 90)"`

		GeoEnabled     *bool   `json:"geoEnabled,omitempty" doc:"Count where requests come from (default on); needs traffic analytics"`
		GeoSource      *string `json:"geoSource,omitempty" enum:"dbip,maxmind" doc:"dbip = DB-IP Lite (default, no key); maxmind = MaxMind GeoLite2 (needs the team's account, see PUT /geo/maxmind)"`
		GeoLevel       *string `json:"geoLevel,omitempty" enum:"country,city" doc:"country (default, ~8 MB database) or city (~130 MB)"`
		GeoRefreshDays *int    `json:"geoRefreshDays,omitempty" minimum:"1" maximum:"90" doc:"Check for a newer database every N days (default 7)"`

		AcceptWorkloads *bool `json:"acceptWorkloads,omitempty" doc:"false stops new services being scheduled on this server (what already runs here keeps running); default true"`

		BuildEnabled        *bool `json:"buildEnabled,omitempty" doc:"Make the server a build server: it builds git sources for other servers and pushes the images to a registry. Combine with acceptWorkloads: Deployments (true/false), Builds (false/true) or Both (true/true)."`
		BuildMaxConcurrent  *int  `json:"buildMaxConcurrent,omitempty" minimum:"1" maximum:"16" doc:"Parallel builds on this server (default 2); more queue on the control plane"`
		BuildAllowEmulation *bool `json:"buildAllowEmulation,omitempty" doc:"Allow cross-architecture builds through emulation (default off)"`
		BuildAllowPreviews  *bool `json:"buildAllowPreviews,omitempty" doc:"Let pull request previews build on this server (default off: previews build on their own server)"`
	}
}

type createTokenInput struct {
	Body struct {
		Name       string `json:"name" minLength:"1" maxLength:"100"`
		PublicIP   string `json:"publicIp,omitempty" maxLength:"45"`
		BaseDomain string `json:"baseDomain,omitempty" maxLength:"253"`
		ACMEEmail  string `json:"acmeEmail,omitempty" maxLength:"254"`
	}
}

type createTokenOutput struct{ Body EnrollmentToken }

type serverMetricsInput struct {
	ID    uuid.UUID `path:"serverId"`
	Range string    `query:"range" default:"1h" enum:"5m,10m,30m,1h,6h,12h,24h,7d,30d" doc:"Time window; each response has at most 300 points"`
}

type serverMetricsOutput struct {
	Body struct {
		Points        []MetricPoint `json:"points"`
		StepSeconds   int           `json:"stepSeconds" doc:"Bucket width of every point"`
		RetentionDays int           `json:"retentionDays" doc:"How many days of history this server keeps; longer ranges only show what is retained"`
	}
}

type unrestrictedOutput struct{ Body UnrestrictedSettings }

type setUnrestrictedInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Unrestricted bool   `json:"unrestricted"`
		Confirm      string `json:"confirm" required:"false" maxLength:"200" doc:"The server's name; required when enabling"`
	}
}

// Register attaches the server endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"servers"}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServers", Method: http.MethodGet, Path: "/servers", Tags: tags,
		Summary: "List servers", Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeServersRead), func(ctx context.Context, in *listServersInput) (*listServersOutput, error) {
		items, next, err := svc.List(ctx, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &listServersOutput{Body: api.Page[Server]{Items: items, NextCursor: next}}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createEnrollmentToken", Method: http.MethodPost, Path: "/servers/enrollment-tokens", Tags: tags,
		Summary: "Create a pending server and a one-time enrollment token", DefaultStatus: http.StatusCreated,
		Description: "The token is valid for one hour and can enroll exactly one agent. It is returned only in this response (and in an idempotent replay of it).",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}), policy.ScopeServersWrite), func(ctx context.Context, in *createTokenInput) (*createTokenOutput, error) {
		t, err := svc.CreateEnrollmentToken(ctx, CreateTokenInput{
			Name: in.Body.Name, PublicIP: in.Body.PublicIP, BaseDomain: in.Body.BaseDomain, ACMEEmail: in.Body.ACMEEmail,
		})
		if err != nil {
			return nil, err
		}
		return &createTokenOutput{Body: t}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "reissueEnrollmentToken", Method: http.MethodPost, Path: "/servers/{serverId}/enrollment-token", Tags: tags,
		Summary: "Issue a new install command for a server that has not connected yet", DefaultStatus: http.StatusCreated,
		Description: "Revokes the server's outstanding enrollment tokens and returns a new one, valid for one hour. " +
			"The token is returned only in this response (and in an idempotent replay of it). " +
			"Refused with 409 `server_not_pending` once the server's agent has enrolled.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}), policy.ScopeServersWrite), func(ctx context.Context, in *serverIDInput) (*createTokenOutput, error) {
		t, err := svc.ReissueEnrollmentToken(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &createTokenOutput{Body: t}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServer", Method: http.MethodGet, Path: "/servers/{serverId}", Tags: tags,
		Summary: "Get a server", Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*serverOutput, error) {
		s, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &serverOutput{Body: s}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateServer", Method: http.MethodPatch, Path: "/servers/{serverId}", Tags: tags,
		Summary: "Update a server's name, host settings, retention or alert settings",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeServersWrite), func(ctx context.Context, in *updateServerInput) (*serverOutput, error) {
		s, err := svc.Update(ctx, in.ID, UpdateInput{
			Name: in.Body.Name, PublicIP: in.Body.PublicIP, BaseDomain: in.Body.BaseDomain, ACMEEmail: in.Body.ACMEEmail,
			MetricsRetentionDays: in.Body.MetricsRetentionDays, DiskAlertThresholdPercent: in.Body.DiskAlertThresholdPercent,
			TrafficAnalyticsEnabled: in.Body.TrafficAnalyticsEnabled, TrafficHourlyRetentionDays: in.Body.TrafficHourlyRetentionDays,
			TrafficDailyRetentionDays: in.Body.TrafficDailyRetentionDays,
			GeoEnabled:                in.Body.GeoEnabled, GeoSource: in.Body.GeoSource, GeoLevel: in.Body.GeoLevel, GeoRefreshDays: in.Body.GeoRefreshDays,
			AcceptWorkloads: in.Body.AcceptWorkloads,
			BuildEnabled:    in.Body.BuildEnabled, BuildMaxConcurrent: in.Body.BuildMaxConcurrent,
			BuildAllowEmulation: in.Body.BuildAllowEmulation, BuildAllowPreviews: in.Body.BuildAllowPreviews,
		})
		if err != nil {
			return nil, err
		}
		return &serverOutput{Body: s}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "revokeServer", Method: http.MethodPost, Path: "/servers/{serverId}/revoke", Tags: tags,
		Summary: "Revoke a server's agent",
		Description: "Revokes the agent identity and pending enrollment tokens, cancels commands that were never delivered " +
			"(their deployments fail with \"Server revoked. No changes were made.\") and disconnects the agent. " +
			"Containers already running on the host are left untouched.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeServersWrite), func(ctx context.Context, in *serverIDInput) (*serverOutput, error) {
		s, err := svc.Revoke(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &serverOutput{Body: s}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "removeServer", Method: http.MethodDelete, Path: "/servers/{serverId}", Tags: tags,
		Summary: "Remove a server",
		Description: "Deletes the server. `mode=clean` (the default) first has the agent remove every container, network and volume " +
			"NebulaCtrl manages on the host (the proxy and platform containers included) and then itself: its identity, systemd unit and files, " +
			"as `install-agent.sh --uninstall --purge` does; Docker and everything that is not NebulaCtrl's stay. That takes a while, so it answers " +
			"202 and the server shows its `removal` state until it is deleted (or `removal.state` is `failed` with the reason: remove again to retry). " +
			"`mode=forget`, and any offline server, revokes the agent and deletes the server at once (200) without touching the host: its containers " +
			"keep running and the agent's files stay (`hostUntouched`). Both need `confirm=<the server's name>` and are refused while services are " +
			"placed on the server (409 `server_has_services`, `details.projects` lists them) and for the server the control plane runs on " +
			"(409 `server_is_control_plane`). Clean needs an agent that advertises `canDecommission` (409 `agent_cannot_decommission`). " +
			"Audited as `server.removed`. Admin scope; denied to MCP agents.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, policy.ScopeServersWrite, policy.ScopeAdmin), func(ctx context.Context, in *removeServerInput) (*removeServerOutput, error) {
		res, err := svc.Remove(ctx, in.ID, RemoveInput{Confirm: in.Confirm, Mode: in.Mode})
		if err != nil {
			return nil, err
		}
		status := http.StatusOK
		if !res.Removed {
			status = http.StatusAccepted
		}
		return &removeServerOutput{Status: status, Body: res}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerMetrics", Method: http.MethodGet, Path: "/servers/{serverId}/metrics", Tags: []string{"metrics"},
		Summary: "Host CPU, memory and disk over time",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverMetricsInput) (*serverMetricsOutput, error) {
		res, err := svc.Metrics(ctx, in.ID, in.Range)
		if err != nil {
			return nil, err
		}
		out := &serverMetricsOutput{}
		out.Body.Points, out.Body.StepSeconds, out.Body.RetentionDays = res.Points, res.StepSeconds, res.RetentionDays
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerUnrestricted", Method: http.MethodGet, Path: "/servers/{serverId}/unrestricted", Tags: []string{"servers"},
		Summary: "The Unrestricted switch and Compose availability of a server",
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*unrestrictedOutput, error) {
		c, err := svc.Unrestricted(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &unrestrictedOutput{Body: c}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setServerUnrestricted", Method: http.MethodPut, Path: "/servers/{serverId}/unrestricted", Tags: []string{"servers"},
		Summary: "Make a server unrestricted, or restricted again",
		Description: "By default Compose stacks and services run under safe allow-lists: no privileged mode, host network, host PID namespace, " +
			"devices, GPUs, custom runtime, unconfined seccomp or AppArmor, NET_ADMIN or SYS_ADMIN, Docker socket or host bind mounts. " +
			"An unrestricted server lifts them for everything on it, so anyone who can deploy there can take over the server. " +
			"Instance admin only; enabling needs `confirm` to equal the server's name. Audited as " +
			"`server.unrestricted.enable` or `.disable`. Disabling does not touch running containers: the next deploy, rollback or " +
			"reconcile of a stack or service that needs the lifted rules fails until it is edited or moved.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeServersWrite, policy.ScopeAdmin), func(ctx context.Context, in *setUnrestrictedInput) (*unrestrictedOutput, error) {
		c, err := svc.SetUnrestricted(ctx, in.ID, UnrestrictedInput{Unrestricted: in.Body.Unrestricted, Confirm: in.Body.Confirm})
		if err != nil {
			return nil, err
		}
		return &unrestrictedOutput{Body: c}, nil
	})

	registerGeo(h, svc)
}
