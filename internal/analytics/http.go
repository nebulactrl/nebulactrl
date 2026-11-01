package analytics

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

const rangeDoc = "Window to report. Minute data covers 24 hours; longer ranges read hourly (default 7 days kept) or daily (90 days) rows."

type serviceAnalyticsInput struct {
	ID      uuid.UUID `path:"serviceId"`
	Range   string    `query:"range" default:"24h" enum:"5m,10m,15m,30m,1h,6h,12h,24h,7d,30d,90d" doc:"Window to report"`
	Compare string    `query:"compare" enum:",prev" doc:"prev adds the previous equal window as comparison"`
	Host    string    `query:"host" maxLength:"253" doc:"Restrict to one hostname of the service"`
	Pages   bool      `query:"pages" default:"true" doc:"true (default) ranks top paths by page views (documents); false ranks every request"`
}

type serverAnalyticsInput struct {
	ID      uuid.UUID `path:"serverId"`
	Range   string    `query:"range" default:"24h" enum:"5m,10m,15m,30m,1h,6h,12h,24h,7d,30d,90d" doc:"Window to report"`
	Compare string    `query:"compare" enum:",prev" doc:"prev adds the previous equal window as comparison"`
	Host    string    `query:"host" maxLength:"253" doc:"Restrict to one hostname"`
	Pages   bool      `query:"pages" default:"true" doc:"true (default) ranks top paths by page views (documents); false ranks every request"`
}

type trafficAnalyticsInput struct {
	Range         string `query:"range" default:"24h" enum:"5m,10m,15m,30m,1h,6h,12h,24h,7d,30d,90d" doc:"Window to report"`
	Compare       string `query:"compare" enum:",prev" doc:"prev adds the previous equal window as comparison"`
	Host          string `query:"host" maxLength:"253" doc:"Restrict to one hostname"`
	Pages         bool   `query:"pages" default:"true" doc:"true (default) ranks top paths by page views (documents); false ranks every request"`
	ServerID      string `query:"serverId" format:"uuid" doc:"Restrict to one server"`
	ServiceID     string `query:"serviceId" format:"uuid" doc:"Restrict to one service (needs projects:read)"`
	EnvironmentID string `query:"environmentId" format:"uuid" doc:"Restrict to the services of one environment (needs projects:read)"`
}

type reportOutput struct{ Body Report }

func query(rng, compare, host string, pages bool) Query {
	return Query{Range: rng, Compare: compare == "prev", Host: host, AllPaths: !pages}
}

// optionalID parses an optional UUID query parameter.
func optionalID(name, v string) (*uuid.UUID, error) {
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, apierr.Unprocessable("invalid_"+name, name+" must be a UUID")
	}
	return &id, nil
}

// Register attaches the analytics endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"analytics"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceAnalytics", Method: http.MethodGet, Path: "/services/{serviceId}/analytics", Tags: tags,
		Summary: "Traffic analytics of a service",
		Description: "Aggregated from Traefik access logs on the service's server: requests, bandwidth, unique visitors (per UTC day, bots excluded), " +
			"latency percentiles from merged histograms, status codes and top lists. Raw access logs, IPs and user agents are never stored. " + rangeDoc,
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceAnalyticsInput) (*reportOutput, error) {
		rep, err := svc.ServiceTraffic(ctx, in.ID, query(in.Range, in.Compare, in.Host, in.Pages))
		if err != nil {
			return nil, err
		}
		return &reportOutput{Body: rep}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerAnalytics", Method: http.MethodGet, Path: "/servers/{serverId}/analytics", Tags: tags,
		Summary:     "Traffic analytics of every host on a server",
		Description: "Same payload as the service report plus `unrouted` (requests that matched no service). " + rangeDoc,
		Errors:      errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverAnalyticsInput) (*reportOutput, error) {
		rep, err := svc.ServerTraffic(ctx, in.ID, query(in.Range, in.Compare, in.Host, in.Pages))
		if err != nil {
			return nil, err
		}
		return &reportOutput{Body: rep}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTrafficAnalytics", Method: http.MethodGet, Path: "/analytics/traffic", Tags: tags,
		Summary: "Traffic analytics across servers",
		Description: "Workspace-wide report, optionally filtered by server, service or environment. Without a service or environment filter it needs `servers:read`; " +
			"with one it needs `projects:read`. Project-restricted tokens only see their own services. " + rangeDoc,
		Errors: errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *trafficAnalyticsInput) (*reportOutput, error) {
		var f Filter
		var err error
		if f.ServerID, err = optionalID("serverId", in.ServerID); err != nil {
			return nil, err
		}
		if f.ServiceID, err = optionalID("serviceId", in.ServiceID); err != nil {
			return nil, err
		}
		if f.EnvironmentID, err = optionalID("environmentId", in.EnvironmentID); err != nil {
			return nil, err
		}
		rep, err := svc.WorkspaceTraffic(ctx, f, query(in.Range, in.Compare, in.Host, in.Pages))
		if err != nil {
			return nil, err
		}
		return &reportOutput{Body: rep}, nil
	})
}
