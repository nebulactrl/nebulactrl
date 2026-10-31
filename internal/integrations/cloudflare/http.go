package cloudflare

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type connectionOutput struct{ Body CloudflareConnectionView }

type putInput struct {
	Body struct {
		Token             string     `json:"token,omitempty" maxLength:"512" doc:"Write-only: a Cloudflare API token with Zone → Zone: Read and Zone → DNS: Edit (add Zone → Zone Settings: Read for SSL-mode warnings and Account → Cloudflare Tunnel: Edit for tunnels). Omit to keep the stored token."`
		DNS01CredentialID *uuid.UUID `json:"dns01CredentialId,omitempty" doc:"Use the token of an existing Cloudflare DNS-01 credential instead of typing one"`
		AccountID         string     `json:"accountId,omitempty" maxLength:"64" doc:"Account used for Cloudflare Tunnel calls; defaults to the account of the token's zones"`
	}
}

type deleteInput struct {
	RemoveRecords bool `query:"removeRecords" doc:"Delete the DNS records NebulaCtrl created from Cloudflare first (the removal is refused when that fails)"`
}

type zonesOutput struct {
	Body struct {
		Items []Zone `json:"items"`
	}
}

type domainDNSInput struct {
	ID uuid.UUID `path:"domainId"`
}

type dnsOutput struct{ Body DNSStatus }

type dnsSettingsInput struct {
	ID   uuid.UUID `path:"domainId"`
	Body struct {
		Proxied    *bool `json:"proxied,omitempty" doc:"Proxy through Cloudflare (orange cloud). Tunnel records are always proxied."`
		OriginLock *bool `json:"originLock,omitempty" doc:"Lock the origin to Cloudflare while the domain is proxied (default true). False opts out: anyone who finds the server's IP can bypass Cloudflare. Audited."`
	}
}

type ipListOutput struct{ Body IPListView }

type dnsFixInput struct {
	ID   uuid.UUID `path:"domainId"`
	Body struct {
		Adopt bool `json:"adopt,omitempty" doc:"The explicit replace: the existing records at the name that NebulaCtrl did not create are replaced with its own (marked as managed and pointed at the server). Audited."`
	}
}

type pageDNSInput struct {
	ID uuid.UUID `path:"pageId"`
}

type pageDNSSettingsInput struct {
	ID   uuid.UUID `path:"pageId"`
	Body struct {
		Proxied    *bool `json:"proxied,omitempty" doc:"Proxy through Cloudflare (orange cloud). A proxied page is locked to Cloudflare while it is published."`
		OriginLock *bool `json:"originLock,omitempty" doc:"Lock the control-plane server's route of the page to Cloudflare while it is proxied (default true). False opts out: anyone who finds the server's IP can bypass Cloudflare. Audited."`
	}
}

type pageDNSFixInput struct {
	ID   uuid.UUID `path:"pageId"`
	Body struct {
		Adopt bool `json:"adopt,omitempty" doc:"Take over the existing record NebulaCtrl did not create: it is marked as managed and pointed at the control-plane server"`
	}
}

// Register attaches the Cloudflare integration and per-domain DNS endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"integrations"}
	dtags := []string{"domains"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getCloudflareIntegration", Method: http.MethodGet, Path: "/integrations/cloudflare", Tags: tags,
		Summary: "Get the Cloudflare connection", Description: "The API token is never returned.", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*connectionOutput, error) {
		v, err := svc.Get(ctx)
		if err != nil {
			return nil, err
		}
		return &connectionOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "putCloudflareIntegration", Method: http.MethodPut, Path: "/integrations/cloudflare", Tags: tags,
		Summary: "Connect Cloudflare",
		Description: "Stores a scoped API token (sealed at rest, write-only) after verifying it against Cloudflare and caching its zone list; " +
			"every domain of the team is matched against the zones. Omit the token to change only the settings. 422 `invalid_token` when Cloudflare rejects the token.",
		Errors: append(errs, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeAdmin), func(ctx context.Context, in *putInput) (*connectionOutput, error) {
		v, err := svc.Put(ctx, PutInput{
			Token: in.Body.Token, DNS01Credential: in.Body.DNS01CredentialID, AccountID: in.Body.AccountID,
		})
		if err != nil {
			return nil, err
		}
		return &connectionOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteCloudflareIntegration", Method: http.MethodDelete, Path: "/integrations/cloudflare", Tags: tags,
		Summary: "Disconnect Cloudflare", DefaultStatus: http.StatusNoContent,
		Description: "Forgets the token and the zone list. Records NebulaCtrl created stay in Cloudflare (unmanaged) unless `removeRecords=true`.",
		Errors:      append(errs, http.StatusNotFound, http.StatusConflict, http.StatusBadGateway),
	}, policy.ScopeAdmin), func(ctx context.Context, in *deleteInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.RemoveRecords)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testCloudflareIntegration", Method: http.MethodPost, Path: "/integrations/cloudflare/test", Tags: tags,
		Summary:     "Re-verify the token and refresh the zone list",
		Description: "Verifies the stored token, refreshes the cached zones and re-matches every domain of the team.",
		Errors:      append(errs, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*connectionOutput, error) {
		v, err := svc.Test(ctx)
		if err != nil {
			return nil, err
		}
		return &connectionOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listCloudflareZones", Method: http.MethodGet, Path: "/integrations/cloudflare/zones", Tags: tags,
		Summary: "List the connected zones (zone picker)", Description: "The cached zone list; empty without a connection.", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*zonesOutput, error) {
		zs, err := svc.Zones(ctx)
		if err != nil {
			return nil, err
		}
		out := &zonesOutput{}
		out.Body.Items = zs
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDomainDns", Method: http.MethodGet, Path: "/domains/{domainId}/dns", Tags: dtags,
		Summary: "Live Cloudflare DNS status of a domain",
		Description: "Reads the records of the hostname from Cloudflare and classifies them: `managed` (a record NebulaCtrl created points to the server or tunnel), " +
			"`present` (a record NebulaCtrl did not create already points there), `wrong_target`, `missing` or `not_in_zone`. Changes nothing.",
		Errors: append(errs, http.StatusNotFound, http.StatusBadGateway),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *domainDNSInput) (*dnsOutput, error) {
		v, err := svc.Status(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &dnsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateDomainDns", Method: http.MethodPut, Path: "/domains/{domainId}/dns", Tags: dtags,
		Summary: "Change a domain's Cloudflare settings",
		Description: "Sets the per-domain proxy toggle and the origin lock opt-in, then syncs; DNS of a domain in a connected zone is always managed. " +
			"A failed sync is reported as `error` in the status and retried in the background.",
		Errors: append(errs, http.StatusNotFound),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *dnsSettingsInput) (*dnsOutput, error) {
		v, err := svc.SetSettings(ctx, in.ID, SettingsInput{Proxied: in.Body.Proxied, OriginLock: in.Body.OriginLock})
		if err != nil {
			return nil, err
		}
		return &dnsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "fixDomainDns", Method: http.MethodPost, Path: "/domains/{domainId}/dns/fix", Tags: dtags,
		Summary: "Fix a domain's DNS record",
		Description: "Creates or corrects the record so the hostname points at the server (or tunnel). A record NebulaCtrl did not create is never modified " +
			"unless `adopt` is true, the explicit replace: the existing records at the name are replaced with NebulaCtrl's own (409 `adoption_required` otherwise; the replace is audited). `adopt` needs the admin scope and a token not restricted to projects (403 otherwise), like the status page fix. " +
			"422 `not_configured` / `not_in_zone`; 502 when Cloudflare fails.",
		Errors: append(errs, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *dnsFixInput) (*dnsOutput, error) {
		v, err := svc.Fix(ctx, in.ID, in.Body.Adopt)
		if err != nil {
			return nil, err
		}
		return &dnsOutput{Body: v}, nil
	})

	ptags := []string{"status-pages"}
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getStatusPageCloudflare", Method: http.MethodGet, Path: "/status-pages/{pageId}/cloudflare", Tags: ptags,
		Summary: "Live Cloudflare DNS status of a status page's hostname",
		Description: "Reads the records of the hostname from Cloudflare and classifies them like `GET /domains/{domainId}/dns` does: `managed`, `present`, `wrong_target`, `missing` or `not_in_zone`. " +
			"The wanted records are the control-plane server's public addresses (`desiredRecords`). `originLock` says whether the page is locked to Cloudflare. Changes nothing. 422 `no_hostname` while the page has no hostname.",
		Errors: append(errs, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *pageDNSInput) (*dnsOutput, error) {
		v, err := svc.StatusPageStatus(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &dnsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateStatusPageCloudflare", Method: http.MethodPut, Path: "/status-pages/{pageId}/cloudflare", Tags: ptags,
		Summary: "Change the Cloudflare settings of a status page's hostname",
		Description: "Sets the proxy toggle and the origin lock opt-in, then syncs; DNS of a hostname in a connected zone is always managed. A proxied, published page is locked to Cloudflare. " +
			"A failed sync is reported as `error` in the status and retried in the background. Audited as `status_page.dns.update`. Requires the admin scope.",
		Errors: append(errs, http.StatusNotFound, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *pageDNSSettingsInput) (*dnsOutput, error) {
		v, err := svc.SetStatusPageSettings(ctx, in.ID, SettingsInput{Proxied: in.Body.Proxied, OriginLock: in.Body.OriginLock})
		if err != nil {
			return nil, err
		}
		return &dnsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "fixStatusPageCloudflare", Method: http.MethodPost, Path: "/status-pages/{pageId}/cloudflare/fix", Tags: ptags,
		Summary: "Fix a status page's DNS records",
		Description: "Creates or corrects the records so the hostname points at the control-plane server. A record NebulaCtrl did not create is never modified " +
			"unless `adopt` is true, the explicit replace (409 `adoption_required` otherwise; the replace is audited). " +
			"422 `not_configured` / `not_in_zone` / `no_hostname`; 502 when Cloudflare fails. Requires the admin scope.",
		Errors: append(errs, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeAdmin), func(ctx context.Context, in *pageDNSFixInput) (*dnsOutput, error) {
		v, err := svc.FixStatusPage(ctx, in.ID, in.Body.Adopt)
		if err != nil {
			return nil, err
		}
		return &dnsOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getCloudflareIPs", Method: http.MethodGet, Path: "/cloudflare/ips", Tags: tags,
		Summary: "Cloudflare IP ranges in use",
		Description: "The list the origin lock allows: its source (built-in copy, Cloudflare API or the published text files), size, age and the state of the 3-hourly refresh. " +
			"`stale` is set when fetching has failed for more than 24 hours; the last good list stays in use.",
		Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*ipListOutput, error) {
		v, err := svc.IPs(ctx)
		if err != nil {
			return nil, err
		}
		return &ipListOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "refreshCloudflareIPs", Method: http.MethodPost, Path: "/cloudflare/ips/refresh", Tags: tags,
		Summary: "Refresh Cloudflare's IP ranges now (team admin)",
		Description: "Fetches the list like the periodic job does (API first, text files second), validates it and stores it. A changed list is pushed to the servers with locked routes " +
			"(a route update, no redeploy). 502 `ip_refresh_failed` when neither source works or the answer fails validation; the stored list stays.",
		Errors: append(errs, http.StatusBadGateway),
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*ipListOutput, error) {
		v, err := svc.RefreshIPsNow(ctx)
		if err != nil {
			return nil, err
		}
		return &ipListOutput{Body: v}, nil
	})
}
