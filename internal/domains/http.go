package domains

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

type serviceIDInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type listOutput struct {
	Body struct {
		Items []services.DomainView `json:"items"`
	}
}

type createInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Hostname   string `json:"hostname" minLength:"3" maxLength:"253"`
		TargetPort int    `json:"targetPort,omitempty" minimum:"0" maximum:"65535" doc:"Defaults to, and must equal, the service port. Compose stacks: the container port of the component; optional when the component declares exactly one port"`
		Component  string `json:"component,omitempty" maxLength:"63" doc:"Compose stacks only (required for them): the component (compose service) the domain routes to"`
		Process    string `json:"process,omitempty" maxLength:"31" doc:"Services with several processes: the process the domain routes to. Omit for the default process (or, when it has no port, the only process with one). The target port is always the process' port"`
		RuleInput
	}
}

type generateInput struct {
	ID        uuid.UUID `path:"serviceId"`
	Component string    `query:"component" doc:"Compose stacks only (required for them): the component the generated domain routes to"`
	Process   string    `query:"process" maxLength:"31" doc:"Services with several processes: the process the generated domain routes to; defaults like createDomain's process"`
}

type domainOutput struct{ Body services.DomainView }

type domainETagOutput struct {
	ETag string `header:"ETag" doc:"The owning service's generation after the change"`
	Body services.DomainView
}

type domainIDInput struct {
	ID uuid.UUID `path:"domainId"`
}

type updateInput struct {
	ID      uuid.UUID `path:"domainId"`
	IfMatch string    `header:"If-Match" doc:"The owning service's ETag (generation) from its last GET. Required."`
	Body    struct {
		Hostname *string `json:"hostname,omitempty" minLength:"3" maxLength:"253"`
		RuleInput
	}
}

type authUserBody struct {
	Username string `json:"username" minLength:"1" maxLength:"64"`
	Password string `json:"password,omitempty" maxLength:"72" doc:"Omit to keep the password of an existing user"`
}

type authUsersInput struct {
	ID   uuid.UUID `path:"domainId"`
	Body []authUserBody
}

type authUsersOutput struct {
	Body struct {
		Usernames []string `json:"usernames"`
	}
}

type authUserPath struct {
	ID       uuid.UUID `path:"domainId"`
	Username string    `path:"username"`
}

// Register attaches the domain endpoints.
func Register(h huma.API, svc *Service) {
	errs := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listDomains", Method: http.MethodGet, Path: "/services/{serviceId}/domains", Tags: []string{"domains"},
		Summary: "List a service's domains", Errors: errs,
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*listOutput, error) {
		items, err := svc.List(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &listOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createDomain", Method: http.MethodPost, Path: "/services/{serviceId}/domains", Tags: []string{"domains"},
		Summary: "Add a domain (staged until deployed)", DefaultStatus: http.StatusCreated,
		Description: "Hostnames are RFC 1123, lowercase and globally unique. A hostname that is taken, or lies in a Cloudflare zone another team connected, answers 409 `domain_conflict` (\"the hostname is not available\"): the answer never says which, or whose. " +
			"Fails with 422 `proxy_unavailable` while the server's proxy is not running. " +
			"`statusMessage` carries DNS guidance and `dnsOk` the result of the periodic DNS check. " +
			"Compose stacks need `component` (422 `component_required`, `unknown_component`) and, unless the component declares exactly one port, `targetPort` (422 `target_port_required`). " +
			"A service with several processes routes a domain to one process, named in `process`: the default process when it has a port, else the only process with one (422 `process_required` when several have a port, `unknown_process`, `process_has_no_port`, `invalid_target_port`); `process` on a service without processes is 422 `invalid_process`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *createInput) (*domainOutput, error) {
		component, err := componentOrProcess(in.Body.Component, in.Body.Process)
		if err != nil {
			return nil, err
		}
		d, err := svc.CreateForComponent(ctx, in.ID, in.Body.Hostname, in.Body.TargetPort, component, in.Body.RuleInput)
		if err != nil {
			return nil, err
		}
		return &domainOutput{Body: d}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "generateDomain", Method: http.MethodPost, Path: "/services/{serviceId}/domains/generate", Tags: []string{"domains"},
		Summary: "Generate a domain", DefaultStatus: http.StatusCreated,
		Description: "Creates `<service>-<env>-<project>.<base>` where base is the server's base domain, " +
			"else `<public-ip>.sslip.io`. 422 `no_base_domain` when neither exists. Compose stacks need `component`; the hostname is `<service>-<component>-<env>-<project>.<base>`. " +
			"A service with several processes names the process the domain routes to in `process` (hostname `<service>-<process>-<env>-<project>.<base>`; the default process is the service's own).",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *generateInput) (*domainOutput, error) {
		component, err := componentOrProcess(in.Component, in.Process)
		if err != nil {
			return nil, err
		}
		d, err := svc.Generate(ctx, in.ID, component)
		if err != nil {
			return nil, err
		}
		return &domainOutput{Body: d}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteDomain", Method: http.MethodDelete, Path: "/domains/{domainId}", Tags: []string{"domains"},
		Summary: "Remove a domain (staged until deployed)", DefaultStatus: http.StatusNoContent, Errors: errs,
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *domainIDInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateDomain", Method: http.MethodPatch, Path: "/domains/{domainId}", Tags: []string{"domains"},
		Summary: "Update a domain's routing rule (staged until deployed)",
		Description: "Requires `If-Match: \"<generation>\"` of the owning service; a stale value returns 412 `generation_conflict`. " +
			"Hostname changes need `projects:write`; every other field (path prefix, strip/add prefix, redirect, TLS mode, pinned certificate, options) also needs `proxy:write` " +
			"and an agent with proxy v2 (422 `agent_too_old`). 422 codes: `invalid_path_prefix`, `invalid_redirect`, `redirect_loop`, `invalid_tls_mode`, " +
			"`certificate_mismatch`, `invalid_options`, `basic_auth_needs_users`. 409 `domain_conflict` when host+path is taken.",
		Errors: append(errs, http.StatusConflict, http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *updateInput) (*domainETagOutput, error) {
		gen, ok, err := services.ParseETag(in.IfMatch)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierr.New(http.StatusPreconditionRequired, "if_match_required", "send If-Match with the service generation from the last GET")
		}
		d, newGen, err := svc.Update(ctx, in.ID, gen, UpdateInput{Hostname: in.Body.Hostname, Rule: in.Body.RuleInput})
		if err != nil {
			return nil, err
		}
		return &domainETagOutput{ETag: services.ETag(newGen), Body: d}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setDomainAuthUsers", Method: http.MethodPut, Path: "/domains/{domainId}/auth-users", Tags: []string{"domains"},
		Summary: "Replace a domain's basic-auth users",
		Description: "Replaces the whole user set (needs `proxy:write`). A user that already exists may omit the password to keep it. " +
			"Passwords are bcrypt-hashed (cost 10) and stored sealed; only user names are ever returned. " +
			"Enable the option with `PATCH /domains/{id}` `options.basicAuth`. Users apply live with the next route push.",
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *authUsersInput) (*authUsersOutput, error) {
		users := make([]AuthUserInput, len(in.Body))
		for i, u := range in.Body {
			users[i] = AuthUserInput{Username: u.Username, Password: u.Password}
		}
		names, err := svc.SetAuthUsers(ctx, in.ID, users)
		if err != nil {
			return nil, err
		}
		out := &authUsersOutput{}
		out.Body.Usernames = names
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteDomainAuthUser", Method: http.MethodDelete, Path: "/domains/{domainId}/auth-users/{username}", Tags: []string{"domains"},
		Summary: "Remove one basic-auth user", DefaultStatus: http.StatusNoContent,
		Errors: append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *authUserPath) (*struct{}, error) {
		return nil, svc.DeleteAuthUser(ctx, in.ID, in.Username)
	})
}
