package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/registries"
)

// registryProvider adapts the registries service to the deploy flow
// (deployments.RegistryProvider), which cannot import it.
type registryProvider struct{ s *registries.Service }

var _ deployments.RegistryProvider = registryProvider{}

func (p registryProvider) RegistryInfo(ctx context.Context, id uuid.UUID) (deployments.RegistryInfo, error) {
	i, err := p.s.Resolve(ctx, id)
	if err != nil {
		return deployments.RegistryInfo{}, err
	}
	return deployments.RegistryInfo{
		ID: i.ID, Host: i.Host, Namespace: i.Namespace, Insecure: i.Insecure,
		Healthy: i.Healthy, HealthMessage: i.HealthMessage, ReadOnly: i.ReadOnly,
	}, nil
}

func (p registryProvider) RegistryCredentials(ctx context.Context, id uuid.UUID, repository string, actions []string, ttl time.Duration) (string, error) {
	return p.s.Credentials(ctx, id, registries.Scope{Repository: repository, Actions: actions}, ttl)
}
