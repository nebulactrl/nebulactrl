package uptime

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type serviceUptimeInput struct {
	ServiceID uuid.UUID `path:"serviceId"`
	Range     string    `query:"range" default:"24h" enum:"1h,24h,7d,30d,90d" doc:"Window of the series: 60 minute cells for 1h, hourly points for 24h and 7d, UTC days for 30d and 90d."`
}

type serviceUptimeOutput struct{ Body ServiceReport }

type environmentUptimeInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
}

type environmentUptimeOutput struct {
	Body struct {
		Items []EnvironmentService `json:"items"`
	}
}

type monitorInput struct {
	ServiceID uuid.UUID `path:"serviceId"`
}

type updateMonitorInput struct {
	ServiceID uuid.UUID `path:"serviceId"`
	Body      MonitorView
}

type monitorOutput struct{ Body MonitorView }

// Register attaches the uptime endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"uptime"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceUptime", Method: http.MethodGet, Path: "/services/{serviceId}/uptime", Tags: tags,
		Summary: "Uptime of a service",
		Description: "Current state (up, degraded, down, maintenance, paused, unknown) with when it began, the last check, last error and latency; " +
			"consecutiveFailures and failureThreshold show a failure streak that is not an outage yet; " +
			"availability over 24 hours, 7, 30 and 90 days (up + degraded) / (checks - maintenance), null without data; a series of the range " +
			"(60 minute cells for 1h, hourly points for 24h and 7d, UTC days for 30d and 90d; every bucket is present, with checks 0 where there was no data) and the 20 most recent outages. " +
			"Checks come from the service's server every 30 seconds. Needs `projects:read`.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceUptimeInput) (*serviceUptimeOutput, error) {
		rep, err := svc.GetServiceUptime(ctx, in.ServiceID, in.Range)
		if err != nil {
			return nil, err
		}
		return &serviceUptimeOutput{Body: rep}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getEnvironmentUptime", Method: http.MethodGet, Path: "/environments/{environmentId}/uptime", Tags: tags,
		Summary:     "Uptime of every service of an environment",
		Description: "Each service with its state, its availability over the last 30 UTC days and 30 daily points. Needs `projects:read`.",
		Errors:      errs[:3],
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *environmentUptimeInput) (*environmentUptimeOutput, error) {
		items, err := svc.GetEnvironmentUptime(ctx, in.EnvironmentID)
		if err != nil {
			return nil, err
		}
		out := &environmentUptimeOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getUptimeMonitor", Method: http.MethodGet, Path: "/services/{serviceId}/uptime-monitor", Tags: tags,
		Summary:     "Get a service's uptime monitor settings",
		Description: "Whether the service is probed, whether outages alert, and how many failed or passing checks open and end an outage. A service without settings is monitored with the defaults (3 and 2). Needs `projects:read`.",
		Errors:      errs[:3],
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *monitorInput) (*monitorOutput, error) {
		v, err := svc.GetMonitor(ctx, in.ServiceID)
		if err != nil {
			return nil, err
		}
		return &monitorOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateUptimeMonitor", Method: http.MethodPut, Path: "/services/{serviceId}/uptime-monitor", Tags: tags,
		Summary: "Set a service's uptime monitor settings",
		Description: "Replaces the settings. Takes effect without a deployment: the server's agent stops or resumes probing the service within seconds. " +
			"Switching monitoring off pauses the service and closes its open outage without a recovery notice. Needs the `admin` scope (workspace owner or admin). Audited as `uptime_monitor.update`.",
		Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateMonitorInput) (*monitorOutput, error) {
		v, err := svc.UpdateMonitor(ctx, in.ServiceID, in.Body)
		if err != nil {
			return nil, err
		}
		return &monitorOutput{Body: v}, nil
	})
}
