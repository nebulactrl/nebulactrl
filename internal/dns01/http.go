package dns01

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type providersOutput struct {
	Body struct {
		Items []agentprotocol.DNSProviderSpec `json:"items"`
	}
}

type listCredentialsInput struct {
	ServerID string `query:"serverId" format:"uuid" doc:"Only the team-wide credentials and this server's"`
}

type credentialsOutput struct {
	Body struct {
		Items []DNSCredential `json:"items"`
	}
}

type credentialOutput struct{ Body DNSCredential }

type createCredentialInput struct {
	Body struct {
		Name     string            `json:"name" minLength:"1" maxLength:"80" doc:"Unique per team"`
		Provider string            `json:"provider" pattern:"^[a-z0-9][a-z0-9-]{0,39}$" doc:"The lego provider code: one of GET /dns/providers or GET /dns/lego-providers"`
		ServerID *uuid.UUID        `json:"serverId,omitempty" doc:"Scope the credential to one server; omit for the whole team"`
		Values   map[string]string `json:"values" doc:"Provider variables by name (see GET /dns/providers and GET /dns/lego-providers). Write-only: sealed at rest, never returned."`
	}
}

type updateCredentialInput struct {
	ID   uuid.UUID `path:"credentialId"`
	Body struct {
		Name   *string           `json:"name,omitempty" minLength:"1" maxLength:"80"`
		Values map[string]string `json:"values,omitempty" doc:"Replaces the named variables; an empty value keeps the stored one"`
		Clear  []string          `json:"clear,omitempty" doc:"Optional variables to remove"`
	}
}

type credentialIDInput struct {
	ID uuid.UUID `path:"credentialId"`
}

type serverIDInput struct {
	ID uuid.UUID `path:"serverId"`
}

type stateOutput struct{ Body ServerDNS01 }

type resolverPathInput struct {
	ID       uuid.UUID `path:"serverId"`
	Provider string    `path:"provider" pattern:"^[a-z0-9][a-z0-9-]{0,39}$"`
}

type setResolverInput struct {
	ID       uuid.UUID `path:"serverId"`
	Provider string    `path:"provider" pattern:"^[a-z0-9][a-z0-9-]{0,39}$"`
	Body     struct {
		CredentialID uuid.UUID `json:"credentialId"`
		DelaySeconds int       `json:"delaySeconds,omitempty" minimum:"0" maximum:"600" doc:"Wait before checking that the TXT record propagated (slow DNS providers)"`
		Staging      bool      `json:"staging,omitempty" doc:"Use the Let's Encrypt staging CA (untrusted certificates, generous rate limits)"`
	}
}

type createWildcardInput struct {
	ID   uuid.UUID `path:"serverId"`
	Body struct {
		Domain         string `json:"domain" minLength:"3" maxLength:"253" doc:"Base domain; the certificate covers *.<domain>"`
		Provider       string `json:"provider" pattern:"^[a-z0-9][a-z0-9-]{0,39}$"`
		IncludeApex    bool   `json:"includeApex" doc:"Also cover the base domain itself"`
		CoverGenerated bool   `json:"coverGenerated,omitempty" doc:"Generated domains under the base domain use this certificate"`
		Default        bool   `json:"default,omitempty" doc:"Make it the proxy's default certificate"`
	}
}

type wildcardIDInput struct {
	ID uuid.UUID `path:"wildcardId"`
}

type updateWildcardInput struct {
	ID   uuid.UUID `path:"wildcardId"`
	Body struct {
		IncludeApex    *bool `json:"includeApex,omitempty"`
		CoverGenerated *bool `json:"coverGenerated,omitempty"`
		Default        *bool `json:"default,omitempty"`
	}
}

// Register attaches the DNS-01 endpoints.
func Register(h huma.API, svc *Service) {
	tags := []string{"dns01"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDnsProviders", Method: http.MethodGet, Path: "/dns/providers", Tags: tags,
		Summary:     "Curated DNS-01 providers and their credential variables",
		Description: "Cloudflare, Amazon Route 53, Hetzner, DigitalOcean, Gandi, OVHcloud, Porkbun, Namecheap, Google Cloud DNS, Azure DNS, deSEC, Linode, Vultr, Scaleway, Bunny, DNSimple, IONOS, GoDaddy and Netcup. The variable names are the ones Traefik's ACME client (lego v5) reads. Any other lego provider is in GET /dns/lego-providers.",
		Errors:      errs[:2],
	}, policy.ScopeServersRead), func(ctx context.Context, _ *struct{}) (*providersOutput, error) {
		items, err := svc.Providers(ctx)
		if err != nil {
			return nil, err
		}
		out := &providersOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listLegoDnsProviders", Method: http.MethodGet, Path: "/dns/lego-providers", Tags: tags,
		Summary:     "Every other lego DNS provider, for the generic form",
		Description: "The providers of the lego release Traefik vendors that have no curated form: code, name, the lego docs link and the variables lego reads for it. A credential for one of them is a provider code plus variables from this list. `exec` and `manual` are not offered. Variables that read a file are left out.",
		Errors:      errs[:2],
	}, policy.ScopeServersRead), func(ctx context.Context, _ *struct{}) (*providersOutput, error) {
		items, err := svc.LegoProviders(ctx)
		if err != nil {
			return nil, err
		}
		out := &providersOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDnsCredentials", Method: http.MethodGet, Path: "/dns/credentials", Tags: tags,
		Summary:     "List DNS provider credentials",
		Description: "Needs `proxy:write`. Secret variables are never returned; non-secret ones (an access key ID, a region) only to administrators.",
		Errors:      errs,
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *listCredentialsInput) (*credentialsOutput, error) {
		var serverID *uuid.UUID
		if in.ServerID != "" {
			id, err := uuid.Parse(in.ServerID)
			if err != nil {
				return nil, apierr.Unprocessable("validation_failed", "serverId must be a UUID")
			}
			serverID = &id
		}
		items, err := svc.ListCredentials(ctx, serverID)
		if err != nil {
			return nil, err
		}
		out := &credentialsOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createDnsCredential", Method: http.MethodPost, Path: "/dns/credentials", Tags: tags,
		Summary: "Add a DNS provider credential", DefaultStatus: http.StatusCreated,
		Description: "Needs `admin`. The values are sealed with the instance secret key and delivered to the proxy container as environment variables " +
			"at deploy time; they are never part of a stored payload and never returned. 422 `invalid_credential` / `invalid_provider`, 409 `credential_name_taken`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *createCredentialInput) (*credentialOutput, error) {
		c, err := svc.CreateCredential(ctx, CredentialInput{Name: in.Body.Name, Provider: in.Body.Provider, ServerID: in.Body.ServerID, Values: in.Body.Values})
		if err != nil {
			return nil, err
		}
		return &credentialOutput{Body: c}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateDnsCredential", Method: http.MethodPatch, Path: "/dns/credentials/{credentialId}", Tags: tags,
		Summary: "Rename a credential or rotate its values",
		Description: "Needs `admin`. Changed values stage a proxy deploy on every server whose resolver uses the credential " +
			"(the proxy container is recreated with the new environment).",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *updateCredentialInput) (*credentialOutput, error) {
		c, err := svc.UpdateCredential(ctx, in.ID, CredentialPatch{Name: in.Body.Name, Values: in.Body.Values, Clear: in.Body.Clear})
		if err != nil {
			return nil, err
		}
		return &credentialOutput{Body: c}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteDnsCredential", Method: http.MethodDelete, Path: "/dns/credentials/{credentialId}", Tags: tags,
		Summary: "Delete a credential", DefaultStatus: http.StatusNoContent,
		Description: "Needs `admin`. 409 `credential_in_use` while a resolver (desired or applied) uses it.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *credentialIDInput) (*struct{}, error) {
		return nil, svc.DeleteCredential(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServerDns01", Method: http.MethodGet, Path: "/servers/{serverId}/dns01", Tags: tags,
		Summary:     "A server's DNS-01 resolvers and wildcard certificates",
		Description: "Desired state with staged additions, updates and removals marked; certificate status and expiry are observed by the agent.",
		Errors:      errs,
	}, policy.ScopeServersRead), func(ctx context.Context, in *serverIDInput) (*stateOutput, error) {
		st, err := svc.GetServerState(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &stateOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setServerDns01Resolver", Method: http.MethodPut, Path: "/servers/{serverId}/dns01/resolvers/{provider}", Tags: tags,
		Summary: "Stage the DNS-01 resolver of a provider on a server",
		Description: "Needs `proxy:write`. One resolver per provider and server. Staged until `POST /servers/{id}/proxy/deploy`, which recreates the proxy container. " +
			"422 `acme_email_required`, `invalid_credential`, `agent_too_old`.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *setResolverInput) (*stateOutput, error) {
		st, err := svc.SetResolver(ctx, in.ID, in.Provider, ResolverInput{CredentialID: in.Body.CredentialID, DelaySeconds: in.Body.DelaySeconds, Staging: in.Body.Staging})
		if err != nil {
			return nil, err
		}
		return &stateOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteServerDns01Resolver", Method: http.MethodDelete, Path: "/servers/{serverId}/dns01/resolvers/{provider}", Tags: tags,
		Summary:     "Stage the removal of a resolver",
		Description: "409 `resolver_in_use` while a wildcard certificate or a domain of a service on the server uses it.",
		Errors:      append(errs, http.StatusConflict),
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *resolverPathInput) (*stateOutput, error) {
		st, err := svc.DeleteResolver(ctx, in.ID, in.Provider)
		if err != nil {
			return nil, err
		}
		return &stateOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createServerWildcardCert", Method: http.MethodPost, Path: "/servers/{serverId}/dns01/wildcards", Tags: tags,
		Summary: "Stage a wildcard certificate", DefaultStatus: http.StatusCreated,
		Description: "Needs `proxy:write` and the provider's resolver on the server (422 `resolver_not_configured`). The certificate covers `*.<domain>` " +
			"(and the apex with `includeApex`); `coverGenerated` lets generated domains use it; `default` makes it the proxy's default certificate. 409 `wildcard_exists`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *createWildcardInput) (*stateOutput, error) {
		st, err := svc.CreateWildcard(ctx, in.ID, WildcardInput{
			Domain: in.Body.Domain, Provider: in.Body.Provider, IncludeApex: in.Body.IncludeApex,
			CoverGenerated: in.Body.CoverGenerated, Default: in.Body.Default,
		})
		if err != nil {
			return nil, err
		}
		return &stateOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateWildcardCert", Method: http.MethodPatch, Path: "/dns01/wildcards/{wildcardId}", Tags: tags,
		Summary: "Stage a change of a wildcard certificate's options", Errors: errs,
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *updateWildcardInput) (*stateOutput, error) {
		st, err := svc.UpdateWildcard(ctx, in.ID, WildcardPatch{IncludeApex: in.Body.IncludeApex, CoverGenerated: in.Body.CoverGenerated, Default: in.Body.Default})
		if err != nil {
			return nil, err
		}
		return &stateOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteWildcardCert", Method: http.MethodDelete, Path: "/dns01/wildcards/{wildcardId}", Tags: tags,
		Summary: "Stage the removal of a wildcard certificate", Errors: errs,
	}, policy.ScopeProxyWrite), func(ctx context.Context, in *wildcardIDInput) (*stateOutput, error) {
		st, err := svc.DeleteWildcard(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &stateOutput{Body: st}, nil
	})
}
