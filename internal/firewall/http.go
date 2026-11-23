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
	registerAutoBan(h, s)
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

type autoBanPutInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body agentprotocol.AutoBan
}

type autoBanViewOutput struct{ Body AutoBanView }

type listBansInput struct {
	ID    uuid.UUID `path:"serviceId"`
	State string    `query:"state" default:"active" enum:"active,ended" doc:"active: the bans in force now, with the banned address. ended: past bans, prefix only"`
	api.PageQuery
}

type listBansOutput struct{ Body BansPage }

type unbanInput struct {
	ID    uuid.UUID `path:"serviceId"`
	BanID uuid.UUID `path:"banId"`
}

type unbanOutput struct{ Body UnbanView }

// registerAutoBan attaches the auto-ban endpoints (Register calls it).
func registerAutoBan(h huma.API, s *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceAutoBan", Method: http.MethodGet, Path: "/services/{serviceId}/autoban", Tags: []string{"services"},
		Summary: "Get a service's auto-ban policy",
		Description: "Auto-ban bans an address that trips the service's firewall again and again: `threshold` blocked requests within " +
			"`windowSeconds` ban it for `durationSeconds`. Only requests blocked by the standard protection or by a block rule with a condition " +
			"other than `ip` or `country` count; allow rules, private and Cloudflare addresses and the `allow` list never do. " +
			"`supported` is false when the server's agent is too old, `analytics` false while traffic analytics is off on the server " +
			"(auto-ban needs it), `warnings` lists conditions that keep some visitors from being banned.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *firewallPath) (*autoBanViewOutput, error) {
		v, err := s.GetAutoBan(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &autoBanViewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "setServiceAutoBan", Method: http.MethodPut, Path: "/services/{serviceId}/autoban", Tags: []string{"services"},
		Summary: "Replace a service's auto-ban policy",
		Description: "Replaces the whole policy. `threshold` is 3-1000 blocked requests, `windowSeconds` 60-86400, `durationSeconds` 300-604800, `allow` up to 200 " +
			"CIDRs or addresses that are never banned. Visitors are banned by address (IPv4 /32, IPv6 /64). Switching the policy off, or allow-listing a banned " +
			"address, lifts its bans at once. The service's domains are then evaluated by the edge guard next to the proxy (one extra local hop per request). " +
			"The change is operational: it applies at once, without a deployment, is never staged and never rolled back. " +
			"422 `invalid_autoban` for a value out of its limits, `agent_too_old` when the server's agent predates auto-ban, `analytics_required` when traffic " +
			"analytics is off on the server, `autoban_unsupported` for a tcp/udp route.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}), policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *autoBanPutInput) (*autoBanViewOutput, error) {
		v, err := s.PutAutoBan(ctx, in.ID, in.Body)
		if err != nil {
			return nil, err
		}
		return &autoBanViewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServiceBans", Method: http.MethodGet, Path: "/services/{serviceId}/bans", Tags: []string{"services"},
		Summary: "List a service's bans",
		Description: "Bans auto-ban decided for the service, newest first. An active ban shows the banned `address`; an ended one keeps only the `prefix` " +
			"(the address is forgotten when the ban ends) and is listed for 90 days.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listBansInput) (*listBansOutput, error) {
		p, err := s.ListBans(ctx, in.ID, in.State, in.PageQuery)
		if err != nil {
			return nil, err
		}
		return &listBansOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "unbanServiceAddress", Method: http.MethodDelete, Path: "/services/{serviceId}/bans/{banId}", Tags: []string{"services"},
		Summary: "Lift a ban",
		Description: "Tells the server's agent to lift an active ban; it ends when the agent confirms. The address can be banned again if it keeps tripping the " +
			"firewall: add it to the policy's `allow` list to keep it out of auto-ban. 409 `ban_not_active` when the ban already ended.",
		Errors:        append(errs, http.StatusConflict),
		DefaultStatus: http.StatusAccepted,
	}), policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *unbanInput) (*unbanOutput, error) {
		v, err := s.Unban(ctx, in.ID, in.BanID)
		if err != nil {
			return nil, err
		}
		return &unbanOutput{Body: v}, nil
	})
}
