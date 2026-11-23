package firewall

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/analytics"
	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type firewallPath struct {
	ID uuid.UUID `path:"serviceId"`
}

type putInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Rules []agentprotocol.FirewallRule `json:"rules" maxItems:"50" doc:"The complete rule list, in evaluation order; an empty list removes the firewall. A rule without an id gets one."`
	}
}

type blocksInput struct {
	ID    uuid.UUID `path:"serviceId"`
	Range string    `query:"range" default:"24h" enum:"24h,7d,30d" doc:"Window to count"`
}

type viewOutput struct{ Body FirewallView }

type blocksOutput struct{ Body analytics.FirewallBlocks }

// Register attaches the firewall endpoints.
func Register(h huma.API, s *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceFirewall", Method: http.MethodGet, Path: "/services/{serviceId}/firewall", Tags: []string{"services"},
		Summary:     "Get a service's firewall",
		Description: "The rules the proxy evaluates against every request to the service's domains, and how it enforces them.",
		Errors:      errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *firewallPath) (*viewOutput, error) {
		v, err := s.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceFirewallBlocks", Method: http.MethodGet, Path: "/services/{serviceId}/firewall/blocks", Tags: []string{"services"},
		Summary: "Requests a service's firewall blocked",
		Description: "How many requests the firewall blocked in the last 24 hours, 7 or 30 days, per rule: the service's own rules and the standard protection " +
			"(`std…` ids), named as they are now (`removed` for a rule deleted since). Counted from the proxy's access log, so only while traffic analytics is on " +
			"for the service's server (`trafficAnalytics`), and from the moment the agent first reported it: nothing is backfilled, and a guard-enforced rule " +
			"(country conditions, or visitor addresses behind Cloudflare) is counted once the proxy was recreated with the agent that sets the rule header. " +
			"The blocked paths, client networks (/24, /48) and countries stay on the host: `logsQuery` is the query that finds the blocked request lines in the Logs " +
			"explorer, and its facets rank them.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *blocksInput) (*blocksOutput, error) {
		b, err := s.Blocks(ctx, in.ID, in.Range)
		if err != nil {
			return nil, err
		}
		return &blocksOutput{Body: b}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "setServiceFirewall", Method: http.MethodPut, Path: "/services/{serviceId}/firewall", Tags: []string{"services"},
		Summary: "Replace a service's firewall",
		Description: "Rules are evaluated top to bottom for every request to any domain of the service; the first rule whose conditions all match " +
			"decides. `block` answers HTTP 403, `allow` lets the request through and skips the rules below it; a request no rule matches goes through. " +
			"A condition matches when any of its values does (`not` inverts it). Fields and operators: `ip` in (CIDRs or addresses), `country` in " +
			"(ISO 3166-1 codes; needs geolocation on the server, 422 `geo_required`), `method` in, `path` prefix/exact/contains/regex, `header` and " +
			"`query` equals/contains/regex/present (with `name`), `userAgent` contains/regex. `contains` ignores case; regular expressions use Go " +
			"syntax and are unanchored. The change is operational: it applies at once, without a deployment, is never staged and never rolled back. " +
			"422 `invalid_firewall` names the rule and condition at fault; 422 `agent_too_old` when the server's agent predates firewalls.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}), policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *putInput) (*viewOutput, error) {
		v, err := s.Put(ctx, in.ID, PutInput{Rules: in.Body.Rules})
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})
}
