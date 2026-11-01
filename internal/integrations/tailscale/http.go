package tailscale

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type integrationOutput struct{ Body TailscaleIntegration }

type putInput struct {
	Body struct {
		Provider            string   `json:"provider,omitempty" enum:"tailscale_oauth,headscale" doc:"tailscale_oauth (default) or headscale"`
		Tailnet             string   `json:"tailnet,omitempty" maxLength:"128" doc:"Tailnet name; empty or - uses the tailnet of the OAuth client"`
		ClientID            string   `json:"clientId,omitempty" maxLength:"200" doc:"OAuth client id (tailscale_oauth only)"`
		ClientSecret        string   `json:"clientSecret,omitempty" maxLength:"512" doc:"Write-only: the OAuth client secret, the Headscale API key or the Headscale pre-auth key. Omit to keep the stored secret."`
		DefaultTags         []string `json:"defaultTags,omitempty" maxItems:"20" doc:"Tags minted keys carry (default tag:nebula-svc)"`
		AllowedTags         []string `json:"allowedTags,omitempty" maxItems:"20" doc:"Tags NebulaCtrl may use; must include every default tag"`
		LoginServer         string   `json:"loginServer,omitempty" maxLength:"300" doc:"Headscale control URL (headscale only)"`
		HeadscaleUser       string   `json:"headscaleUser,omitempty" maxLength:"128" doc:"Headscale user that owns minted pre-auth keys; empty creates tags-only keys (headscale only)"`
		HeadscaleCredential string   `json:"headscaleCredential,omitempty" enum:"apikey,preauth" doc:"apikey: NebulaCtrl mints single-use pre-auth keys and deletes nodes; preauth: one reusable tagged pre-auth key (headscale only)"`
	}
}

type testOutput struct{ Body TailscaleTestResult }

type devicesOutput struct {
	Body struct {
		Items []TailscaleDevice `json:"items"`
	}
}

// Register attaches the Tailscale integration endpoints (admin scope).
func Register(h huma.API, svc *Service) {
	tags := []string{"integrations"}
	errs := []int{http.StatusUnauthorized, http.StatusForbidden}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getTailscaleIntegration", Method: http.MethodGet, Path: "/integrations/tailscale", Tags: tags,
		Summary: "Get the Tailscale integration", Description: "The OAuth client secret is never returned (`secretSet` only).", Errors: errs,
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*integrationOutput, error) {
		v, err := svc.Get(ctx)
		if err != nil {
			return nil, err
		}
		return &integrationOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "putTailscaleIntegration", Method: http.MethodPut, Path: "/integrations/tailscale", Tags: tags,
		Summary:     "Create or update the Tailscale integration",
		Description: "Stores an OAuth client (scopes `auth_keys` and `devices:core`) or, with `provider: headscale`, a Headscale server (login URL plus an API key, or a reusable tagged pre-auth key). The secret is sealed at rest; omit `clientSecret` to keep the stored one. Nothing contacts the control server until the connection test or first use.",
		Errors:      append(errs, http.StatusUnprocessableEntity),
	}, policy.ScopeAdmin), func(ctx context.Context, in *putInput) (*integrationOutput, error) {
		v, err := svc.Put(ctx, PutInput{
			Provider: in.Body.Provider, Tailnet: in.Body.Tailnet, ClientID: in.Body.ClientID, ClientSecret: in.Body.ClientSecret,
			DefaultTags: in.Body.DefaultTags, AllowedTags: in.Body.AllowedTags,
			LoginServer: in.Body.LoginServer, HeadscaleUser: in.Body.HeadscaleUser, HeadscaleCredential: in.Body.HeadscaleCredential,
		})
		if err != nil {
			return nil, err
		}
		return &integrationOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteTailscaleIntegration", Method: http.MethodDelete, Path: "/integrations/tailscale", Tags: tags,
		Summary: "Remove the Tailscale integration", DefaultStatus: http.StatusNoContent,
		Description: "Refused (409 `integration_in_use`) while a server uses a managed tailnet.",
		Errors:      append(errs, http.StatusNotFound, http.StatusConflict),
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		return nil, svc.Delete(ctx)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "testTailscaleIntegration", Method: http.MethodPost, Path: "/integrations/tailscale/test", Tags: tags,
		Summary:     "Test the Tailscale connection",
		Description: "Exchanges a token, lists devices, mints and revokes a 60 second key with the default tags. Leaves no residue.",
		Errors:      append(errs, http.StatusNotFound),
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*testOutput, error) {
		r, err := svc.Test(ctx)
		if err != nil {
			return nil, err
		}
		return &testOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listTailscaleDevices", Method: http.MethodGet, Path: "/integrations/tailscale/devices", Tags: tags,
		Summary: "List the tailnet's devices (device picker)",
		Errors:  append(errs, http.StatusUnprocessableEntity, http.StatusBadGateway),
	}, policy.ScopeAdmin), func(ctx context.Context, _ *struct{}) (*devicesOutput, error) {
		devs, err := svc.Devices(ctx)
		if err != nil {
			return nil, err
		}
		out := &devicesOutput{}
		out.Body.Items = devs
		return out, nil
	})
}
