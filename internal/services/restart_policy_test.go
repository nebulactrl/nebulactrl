package services

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

func TestRestartPolicyValidation(t *testing.T) {
	base := Config{Image: "x:1", Port: 80}
	for name, tc := range map[string]struct {
		mut    func(*Config)
		source string
	}{
		"empty is always":         {func(*Config) {}, SourceImage},
		"always":                  {func(c *Config) { c.RestartPolicy = RestartAlways }, SourceImage},
		"never":                   {func(c *Config) { c.RestartPolicy = RestartNever }, SourceImage},
		"on-failure":              {func(c *Config) { c.RestartPolicy = RestartOnFailure }, SourceImage},
		"on-failure with retries": {func(c *Config) { c.RestartPolicy, c.RestartMaxRetries = RestartOnFailure, 100 }, SourceImage},
		"a git service": {func(c *Config) {
			*c = Config{GitURL: "https://example.com/a.git", GitBranch: "main", RestartPolicy: RestartNever}
		}, SourceGit},
	} {
		c := tc.mut
		cfg := base
		c(&cfg)
		require.NoError(t, cfg.WithDefaults().Validate(tc.source), name)
	}

	for name, tc := range map[string]struct {
		mut    func(*Config)
		source string
		code   string
	}{
		"unknown policy":           {func(c *Config) { c.RestartPolicy = "sometimes" }, SourceImage, "invalid_restart_policy"},
		"retries below range":      {func(c *Config) { c.RestartPolicy, c.RestartMaxRetries = RestartOnFailure, -1 }, SourceImage, "invalid_restart_max_retries"},
		"retries above range":      {func(c *Config) { c.RestartPolicy, c.RestartMaxRetries = RestartOnFailure, 101 }, SourceImage, "invalid_restart_max_retries"},
		"retries with always":      {func(c *Config) { c.RestartPolicy, c.RestartMaxRetries = RestartAlways, 3 }, SourceImage, "invalid_restart_max_retries"},
		"retries with never":       {func(c *Config) { c.RestartPolicy, c.RestartMaxRetries = RestartNever, 3 }, SourceImage, "invalid_restart_max_retries"},
		"retries without a policy": {func(c *Config) { c.RestartMaxRetries = 3 }, SourceImage, "invalid_restart_max_retries"},
		"one-shot":                 {func(c *Config) { c.RestartPolicy, c.OneShot, c.Port = RestartNever, true, 0 }, SourceImage, "restart_policy_oneshot"},
		"one-shot with always":     {func(c *Config) { c.RestartPolicy, c.OneShot, c.Port = RestartAlways, true, 0 }, SourceImage, "restart_policy_oneshot"},
		"database":                 {func(c *Config) { c.RestartPolicy, c.Database = RestartNever, &engines.Spec{Engine: "postgres"} }, SourceImage, "restart_policy_not_applicable"},
		"route":                    {func(c *Config) { c.RestartPolicy, c.Route = RestartNever, &RouteConfig{} }, SourceExternal, "restart_policy_not_applicable"},
		"stack":                    {func(c *Config) { c.RestartPolicy = RestartNever }, SourceCompose, "restart_policy_not_applicable"},
	} {
		cfg := base
		tc.mut(&cfg)
		err := cfg.WithDefaults().Validate(tc.source)
		require.ErrorIs(t, err, ErrInvalidConfig, name)

		// The HTTP layer reports the specific code.
		var ae *apierr.Error
		require.True(t, errors.As(ConfigError(err), &ae), name)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status, name)
		require.Equal(t, tc.code, ae.Code, name)
	}
}

func TestRestartPolicyNeverTouchesTheDefaults(t *testing.T) {
	c := Config{Image: "x:1", Port: 80}.WithDefaults()
	require.Empty(t, c.RestartPolicy, "WithDefaults leaves the policy unset: no spurious diffs")
	require.Zero(t, c.RestartMaxRetries)

	changes := DiffConfig("config", c, Config{Image: "x:1", Port: 80, RestartPolicy: RestartNever}.WithDefaults())
	require.Len(t, changes, 1)
	require.Equal(t, "config.restartPolicy", changes[0].Field)
	require.Equal(t, "never", changes[0].To)
}
