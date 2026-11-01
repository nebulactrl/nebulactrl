package services

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreDeployConfigValidation(t *testing.T) {
	base := Config{Image: "x:1", Port: 80}

	ok := base
	ok.PreDeployCommand = []string{"rake", "db:migrate"}
	ok = ok.WithDefaults()
	require.Equal(t, 600, ok.PreDeployTimeoutSeconds, "default pre-deploy timeout")
	require.NoError(t, ok.Validate(SourceImage))

	empty := base.WithDefaults()
	require.Zero(t, empty.PreDeployTimeoutSeconds)
	require.NoError(t, empty.Validate(SourceImage))

	for name, mut := range map[string]func(*Config){
		"blank program":           func(c *Config) { c.PreDeployCommand = []string{" "} },
		"nul byte":                func(c *Config) { c.PreDeployCommand = []string{"a\x00b"} },
		"timeout without command": func(c *Config) { c.PreDeployTimeoutSeconds = 60 },
		"timeout too large":       func(c *Config) { c.PreDeployCommand, c.PreDeployTimeoutSeconds = []string{"a"}, 4000 },
		"one-shot service":        func(c *Config) { c.PreDeployCommand, c.OneShot, c.Port = []string{"a"}, true, 0 },
	} {
		c := base
		mut(&c)
		c = c.WithDefaults()
		require.ErrorIs(t, c.Validate(SourceImage), ErrInvalidConfig, name)
	}
}
