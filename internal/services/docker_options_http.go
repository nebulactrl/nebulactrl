package services

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

type dockerOptionsOutput struct{ Body DockerOptionsCatalog }

// RegisterDockerOptions adds the Docker option catalog endpoint.
func RegisterDockerOptions(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getDockerOptionsPolicy", Method: http.MethodGet, Path: "/instance/docker-options", Tags: []string{"services"},
		Summary: "Get the Docker option catalog",
		Description: "What the Docker options accept: capability lists, the curated sysctls, reserved label prefixes, stop signals " +
			"and which options work only on an unrestricted server.",
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*dockerOptionsOutput, error) {
		c, err := svc.DockerOptionsPolicy(ctx)
		if err != nil {
			return nil, err
		}
		return &dockerOptionsOutput{Body: c}, nil
	})
}
