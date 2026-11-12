package firewall

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

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

type viewOutput struct{ Body FirewallView }

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
