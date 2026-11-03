package docker

import (
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
)

func TestRestartPolicyOfCarriesTheRetryCapOnlyForOnFailure(t *testing.T) {
	onFailure := restartPolicyOf(ContainerSpec{RestartPolicy: "on-failure", RestartMaxRetries: 7})
	assert.Equal(t, container.RestartPolicyOnFailure, onFailure.Name)
	assert.Equal(t, 7, onFailure.MaximumRetryCount)

	// The daemon refuses a retry cap with any other policy.
	for _, name := range []string{"unless-stopped", "no", "always", ""} {
		rp := restartPolicyOf(ContainerSpec{RestartPolicy: name, RestartMaxRetries: 7})
		assert.Equal(t, container.RestartPolicyMode(name), rp.Name)
		assert.Zero(t, rp.MaximumRetryCount, name)
	}
}
