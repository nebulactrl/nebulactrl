package services

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
)

func TestCronConfigValidation(t *testing.T) {
	base := Config{Image: "busybox:1", OneShot: true, Cron: &CronSpec{Schedule: "*/5 * * * *"}}
	for name, mut := range map[string]func(*Config){
		"minimal":  func(*Config) {},
		"timezone": func(c *Config) { c.Cron.Timezone = "Europe/Berlin" },
		"utc":      func(c *Config) { c.Cron.Timezone = "utc" },
		"full": func(c *Config) {
			c.Cron = &CronSpec{Schedule: "@daily", TimeoutSeconds: 86400, Concurrency: CronConcurrencyAllow}
		},
		"start cmd":    func(c *Config) { c.StartCommand = []string{"sh", "-c", "date"} },
		"cron absent":  func(c *Config) { c.Cron = nil },
		"forbid":       func(c *Config) { c.Cron.Concurrency = CronConcurrencyForbid },
		"timeout is 1": func(c *Config) { c.Cron.TimeoutSeconds = 1 },
	} {
		cfg := base
		cr := *base.Cron
		cfg.Cron = &cr
		mut(&cfg)
		require.NoError(t, cfg.WithDefaults().Validate(SourceImage), name)
	}

	for name, tc := range map[string]struct {
		mut    func(*Config)
		source string
		code   string
	}{
		"bad expression":   {func(c *Config) { c.Cron.Schedule = "every day" }, SourceImage, "invalid_cron"},
		"empty expression": {func(c *Config) { c.Cron.Schedule = "" }, SourceImage, "invalid_cron"},
		"never fires":      {func(c *Config) { c.Cron.Schedule = "0 0 31 2 *" }, SourceImage, "cron_never_fires"},
		"bad timezone":     {func(c *Config) { c.Cron.Timezone = "Mars/Olympus" }, SourceImage, "invalid_timezone"},
		"timeout high":     {func(c *Config) { c.Cron.TimeoutSeconds = 86401 }, SourceImage, "invalid_timeout"},
		"timeout negative": {func(c *Config) { c.Cron.TimeoutSeconds = -1 }, SourceImage, "invalid_timeout"},
		"concurrency":      {func(c *Config) { c.Cron.Concurrency = "queue" }, SourceImage, "invalid_cron_concurrency"},
		"not one-shot":     {func(c *Config) { c.OneShot = false }, SourceImage, "cron_requires_oneshot"},
		"database":         {func(c *Config) { c.Database = &engines.Spec{Engine: "postgres"} }, SourceImage, "cron_not_applicable"},
		"route":            {func(c *Config) { c.Route = &RouteConfig{} }, SourceExternal, "cron_not_applicable"},
		"stack":            {func(c *Config) {}, SourceCompose, "cron_not_applicable"},
	} {
		cfg := base
		cr := *base.Cron
		cfg.Cron = &cr
		tc.mut(&cfg)
		err := cfg.WithDefaults().Validate(tc.source)
		require.ErrorIs(t, err, ErrInvalidConfig, name)
		var ae *apierr.Error
		require.True(t, errors.As(ConfigError(err), &ae), name)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status, name)
		require.Equal(t, tc.code, ae.Code, name)
	}
}

func TestCronSpecInTheSnapshotsAndDiffs(t *testing.T) {
	a := Config{Image: "busybox:1", OneShot: true, Cron: &CronSpec{Schedule: "* * * * *"}}.WithDefaults()
	b := a
	b.Cron = &CronSpec{Schedule: "0 3 * * *"}
	require.NotEmpty(t, DiffConfig("config", a, b), "a schedule change is a staged change")
	require.Empty(t, DiffConfig("config", a, a))

	in := ConfigInput{Image: "busybox:1", OneShot: true, Cron: &CronSpec{Schedule: "* * * * *", Timezone: "UTC"}}
	require.Equal(t, in.Cron, in.ToConfig().Cron)
}
