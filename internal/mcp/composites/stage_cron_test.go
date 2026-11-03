package composites

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

func TestStageConfigPatchCron(t *testing.T) {
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	yes := true

	// A schedule on a fresh config.
	cfg := services.Config{Image: "busybox:1"}
	(&StageConfigPatch{OneShot: &yes, Cron: &CronPatch{Schedule: str("0 3 * * *"), Timezone: str("Europe/Berlin")}}).apply(&cfg)
	require.True(t, cfg.OneShot)
	require.Equal(t, &services.CronSpec{Schedule: "0 3 * * *", Timezone: "Europe/Berlin"}, cfg.Cron)

	// Absent fields stay, and the stored value is not mutated in place.
	stored := cfg.Cron
	(&StageConfigPatch{Cron: &CronPatch{TimeoutSeconds: num(600), Concurrency: str("allow")}}).apply(&cfg)
	require.Equal(t, &services.CronSpec{Schedule: "0 3 * * *", Timezone: "Europe/Berlin", TimeoutSeconds: 600, Concurrency: "allow"}, cfg.Cron)
	require.Equal(t, "", stored.Concurrency)

	// An empty schedule removes the schedule.
	(&StageConfigPatch{Cron: &CronPatch{Schedule: str("")}}).apply(&cfg)
	require.Nil(t, cfg.Cron)

	require.True(t, (&StageConfigPatch{Cron: &CronPatch{}}).hasConfigFields())
}
