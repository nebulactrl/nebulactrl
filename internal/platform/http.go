package platform

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type idInput struct {
	ID uuid.UUID `path:"id"`
}

type listingOutput struct{ Body PlatformListing }

type provisionInput struct {
	Body struct {
		Kind      string    `json:"kind" enum:"headscale" doc:"Which platform service to set up."`
		ServerID  uuid.UUID `json:"serverId" doc:"The server it runs on."`
		Hostname  string    `json:"hostname,omitempty" maxLength:"253" doc:"Public hostname; default a generated one under the server's base domain, else <ip>.sslip.io."`
		PublicURL string    `json:"publicUrl,omitempty" maxLength:"300" doc:"Overrides https://<hostname>: a proxy on a non-standard port, or plain http for a local test."`
		Replace   bool      `json:"replace,omitempty" doc:"Replace an existing Tailscale integration."`
	}
}

// PlatformProvisionBody is the 202 response of a set-up.
type PlatformProvisionBody struct {
	Service PlatformServiceView `json:"service"`
}

type provisionOutput struct{ Body PlatformProvisionBody }

type viewOutput struct{ Body PlatformServiceView }

type removeInput struct {
	ID         uuid.UUID `path:"id"`
	Confirm    string    `query:"confirm" doc:"Must equal the kind."`
	DeleteData bool      `query:"deleteData" doc:"Also delete the data volumes."`
}

// Register attaches the platform service endpoints (administrators).
func Register(h huma.API, svc *Service) {
	tags := []string{"platform"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listPlatformServices", Method: http.MethodGet, Path: "/platform-services", Tags: tags,
		Summary:     "Platform services: the catalog and what is set up",
		Description: "The tailnet control server (Headscale) NebulaCtrl runs for the team, wired into the Tailscale integration.",
		Errors:      errs,
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*listingOutput, error) {
		l, err := svc.List(ctx)
		if err != nil {
			return nil, err
		}
		return &listingOutput{Body: l}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createPlatformService", Method: http.MethodPost, Path: "/platform-services", Tags: tags,
		Summary: "Set up a platform service for me", DefaultStatus: http.StatusAccepted,
		Description: "Stages Headscale in the team's system project, deploys it, then connects the Tailscale integration with a generated API key and tests the connection. " +
			"Returns 202; poll the service (or listen to events) until `status` is `ready` or `failed`.",
		Errors: append(errs, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *provisionInput) (*provisionOutput, error) {
		r, err := svc.Provision(ctx, ProvisionInput{
			Kind: in.Body.Kind, ServerID: in.Body.ServerID, Hostname: in.Body.Hostname, PublicURL: in.Body.PublicURL,
			Replace: in.Body.Replace,
		})
		if err != nil {
			return nil, err
		}
		return &provisionOutput{Body: PlatformProvisionBody{Service: r.View}}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getPlatformService", Method: http.MethodGet, Path: "/platform-services/{id}", Tags: tags,
		Summary: "Get a platform service", Errors: append(errs, http.StatusNotFound),
	}, policy.ScopeAdmin), func(ctx context.Context, in *idInput) (*viewOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "retryPlatformService", Method: http.MethodPost, Path: "/platform-services/{id}/retry", Tags: tags,
		Summary: "Retry a failed platform service", DefaultStatus: http.StatusAccepted,
		Errors: append(errs, http.StatusNotFound, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *idInput) (*viewOutput, error) {
		v, err := svc.Retry(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "reconnectPlatformService", Method: http.MethodPost, Path: "/platform-services/{id}/reconnect", Tags: tags,
		Summary: "Run the wiring of a ready platform service again", DefaultStatus: http.StatusAccepted,
		Description: "Mints a new Headscale API key, updates the Tailscale integration and tests it.",
		Errors:      append(errs, http.StatusNotFound, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, in *idInput) (*viewOutput, error) {
		v, err := svc.Rewire(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &viewOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deletePlatformService", Method: http.MethodDelete, Path: "/platform-services/{id}", Tags: tags,
		Summary: "Remove a platform service and unwire its integration", DefaultStatus: http.StatusNoContent,
		Description: "`confirm` must equal the kind. The Tailscale integration is removed first; a tailnet still in use answers 409 and nothing changes. `deleteData` also deletes the data volumes.",
		Errors:      append(errs, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *removeInput) (*struct{}, error) {
		return nil, svc.Remove(ctx, in.ID, RemoveInput{Confirm: in.Confirm, DeleteData: in.DeleteData})
	})
}
