package servers

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type geoCredentialsOutput struct{ Body GeoCredentialsStatus }

type setGeoCredentialsInput struct {
	Body struct {
		AccountID  string `json:"accountId" minLength:"1" maxLength:"20" doc:"Numeric MaxMind account ID"`
		LicenseKey string `json:"licenseKey" minLength:"6" maxLength:"100" doc:"MaxMind licence key; stored encrypted, never returned"`
	}
}

// registerGeo attaches the team's MaxMind GeoLite2 account endpoints.
func registerGeo(h huma.API, svc *Service) {
	tags := []string{"servers"}
	read := []int{http.StatusUnauthorized, http.StatusForbidden}
	write := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity}

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getMaxMindCredentials", Method: http.MethodGet, Path: "/geo/maxmind", Tags: tags,
		Summary:     "Whether the team has a MaxMind GeoLite2 account on file",
		Description: "The licence key is write-only. `serversUsing` counts the team's servers that currently use the MaxMind source.",
		Errors:      read,
	}, policy.ScopeServersRead), func(ctx context.Context, _ *struct{}) (*geoCredentialsOutput, error) {
		st, err := svc.GeoCredentials(ctx)
		if err != nil {
			return nil, err
		}
		return &geoCredentialsOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "setMaxMindCredentials", Method: http.MethodPut, Path: "/geo/maxmind", Tags: tags,
		Summary: "Store the team's MaxMind account ID and licence key",
		Description: "The key is sealed with the instance secret key. Servers on the MaxMind source receive it with their next reconcile " +
			"(sent immediately to online agents, never persisted in a command).",
		Errors: write,
	}, policy.ScopeServersWrite), func(ctx context.Context, in *setGeoCredentialsInput) (*geoCredentialsOutput, error) {
		st, err := svc.SetGeoCredentials(ctx, in.Body.AccountID, in.Body.LicenseKey)
		if err != nil {
			return nil, err
		}
		return &geoCredentialsOutput{Body: st}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteMaxMindCredentials", Method: http.MethodDelete, Path: "/geo/maxmind", Tags: tags,
		Summary:     "Remove the team's MaxMind account",
		Description: "Refused with 409 `maxmind_in_use` while a server still uses the MaxMind source.",
		Errors:      append(append([]int{}, write...), http.StatusConflict),
	}, policy.ScopeServersWrite), func(ctx context.Context, _ *struct{}) (*geoCredentialsOutput, error) {
		st, err := svc.DeleteGeoCredentials(ctx)
		if err != nil {
			return nil, err
		}
		return &geoCredentialsOutput{Body: st}, nil
	})
}
