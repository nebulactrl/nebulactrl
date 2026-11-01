package services

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPostDeployConfigValidation(t *testing.T) {
	base := Config{Image: "x:1", Port: 80}

	ok := base
	ok.PostDeployCommand = []string{"rake", "cache:warm"}
	ok = ok.WithDefaults()
	require.Equal(t, 600, ok.PostDeployTimeoutSeconds, "default post-deploy timeout")
	require.NoError(t, ok.Validate(SourceImage))

	empty := base.WithDefaults()
	require.Zero(t, empty.PostDeployTimeoutSeconds)
	require.NoError(t, empty.Validate(SourceImage))

	// Pre- and post-deploy coexist.
	both := base
	both.PreDeployCommand, both.PostDeployCommand = []string{"migrate"}, []string{"warm"}
	require.NoError(t, both.WithDefaults().Validate(SourceImage))

	for name, mut := range map[string]func(*Config){
		"blank program":           func(c *Config) { c.PostDeployCommand = []string{" "} },
		"nul byte":                func(c *Config) { c.PostDeployCommand = []string{"a\x00b"} },
		"timeout without command": func(c *Config) { c.PostDeployTimeoutSeconds = 60 },
		"timeout too large":       func(c *Config) { c.PostDeployCommand, c.PostDeployTimeoutSeconds = []string{"a"}, 4000 },
		"one-shot service":        func(c *Config) { c.PostDeployCommand, c.OneShot, c.Port = []string{"a"}, true, 0 },
	} {
		c := base
		mut(&c)
		c = c.WithDefaults()
		require.ErrorIs(t, c.Validate(SourceImage), ErrInvalidConfig, name)
	}
}
