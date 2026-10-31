package databases

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type publicPortOutput struct{ Body PublicPortView }

type setPublicPortInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		ListenPort  int      `json:"listenPort" minimum:"1024" maximum:"65535" doc:"Port on the server (not 80, 443 or a proxy port)"`
		AllowCIDRs  []string `json:"allowCidrs" minItems:"1" doc:"Client networks (CIDRs or addresses) allowed to connect. Mandatory."`
		DenyCIDRs   []string `json:"denyCidrs,omitempty" doc:"Client networks refused first, even when allowCidrs admits them"`
		ConfirmOpen string   `json:"confirmOpen,omitempty" doc:"Required when allowCidrs admits every address (0.0.0.0/0 or ::/0): the database slug"`
	}
}

type publicConnectionInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Key string `json:"key,omitempty" maxLength:"64" doc:"One of the database's exports; defaults to the engine's connection URL (DATABASE_URL, REDIS_URL, MONGO_URL)"`
	}
}

type publicConnectionOutput struct {
	CacheControl string `header:"Cache-Control"`
	Pragma       string `header:"Pragma"`
	Body         PublicConnection
}

func registerPublicPort(h huma.API, svc *Service) {
	errs := func(extra ...int) []int {
		return append([]int{http.StatusUnauthorized, http.StatusForbidden}, extra...)
	}
	nf, uc, cf := http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusConflict

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDatabasePublicPort", Method: http.MethodGet, Path: "/services/{serviceId}/database/public-port", Tags: []string{"databases"},
		Summary: "Get a database's public port", Errors: errs(nf, cf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*publicPortOutput, error) {
		v, err := svc.PublicPort(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &publicPortOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setDatabasePublicPort", Method: http.MethodPut, Path: "/services/{serviceId}/database/public-port", Tags: []string{"databases"},
		Summary: "Publish (or change) a database's public port",
		Description: "Stages a TCP route on `listenPort` of the database's server with a mandatory IP allow-list and an optional deny list " +
			"(deny first). `0.0.0.0/0` or `::/0` is accepted only with `confirmOpen` set to the database slug. The proxy forwards raw TCP and never " +
			"terminates TLS. Nothing changes until the route is deployed; a new port recreates the proxy container (about a second of dropped " +
			"connections). Needs `projects:write` and `proxy:write`. 422 codes: `allowlist_required`, `open_confirmation_required`, `invalid_config`, " +
			"`invalid_listen_port`, `no_server`, `agent_too_old`. 409 `port_in_use`.",
		Errors: errs(nf, cf, uc),
	}, policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *setPublicPortInput) (*publicPortOutput, error) {
		v, err := svc.SetPublicPort(ctx, in.ID, PublicPortInput{
			ListenPort: in.Body.ListenPort, AllowCIDRs: in.Body.AllowCIDRs, DenyCIDRs: in.Body.DenyCIDRs, ConfirmOpen: in.Body.ConfirmOpen,
		})
		if err != nil {
			return nil, err
		}
		return &publicPortOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "disableDatabasePublicPort", Method: http.MethodDelete, Path: "/services/{serviceId}/database/public-port", Tags: []string{"databases"},
		Summary:     "Close a database's public port",
		Description: "Deletes the route service. A deployed route disappears from the server's table at once.",
		Errors:      errs(nf, cf),
	}, policy.ScopeProjectsWrite, policy.ScopeProxyWrite), func(ctx context.Context, in *serviceIDInput) (*publicPortOutput, error) {
		v, err := svc.DisablePublicPort(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &publicPortOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "revealDatabasePublicConnection", Method: http.MethodPost, Path: "/services/{serviceId}/database/public-port/connection", Tags: []string{"databases"},
		Summary: "Reveal the external connection string (audited)",
		Description: "Like `revealDatabaseExport`, but with the server's public IP and the public port instead of the private host. " +
			"Needs `deployments:write`, is uncached and recorded as `secret.reveal`. 409 `public_port_disabled`; 422 `no_public_address` when the server has no public IP.",
		Errors: errs(nf, cf, uc),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *publicConnectionInput) (*publicConnectionOutput, error) {
		r, err := svc.RevealPublicConnection(ctx, in.ID, in.Body.Key)
		if err != nil {
			return nil, err
		}
		return &publicConnectionOutput{CacheControl: "no-store, max-age=0", Pragma: "no-cache", Body: r}, nil
	})
}
