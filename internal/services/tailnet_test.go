package services

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
)

func TestConfigTailnetDefaultsAndValidation(t *testing.T) {
	c := Config{Image: "x", Port: 8080, Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, Hostname: " Web ", Web: true}}.WithDefaults()
	require.Equal(t, "web", c.Tailnet.Hostname, "normalised with the other defaults")
	require.NoError(t, c.Validate(SourceImage))

	off := Config{Image: "x", Port: 8080, Tailnet: &tailnetcfg.TailnetConfig{Enabled: false, Web: true}}.WithDefaults()
	require.Nil(t, off.Tailnet, "a disabled tailnet is not part of the config")
	raw, err := json.Marshal(off)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "tailnet")

	noPort := Config{Image: "x", Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, Web: true}}.WithDefaults()
	require.ErrorIs(t, noPort.Validate(SourceImage), ErrInvalidConfig)
	require.ErrorContains(t, noPort.Validate(SourceImage), "tailnet")

	tcp := Config{Image: "x", Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432}}}}.WithDefaults()
	require.NoError(t, tcp.Validate(SourceImage), "a database needs no HTTP port")

	oneShot := Config{Image: "x", OneShot: true, Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432}}}}.WithDefaults()
	require.ErrorContains(t, oneShot.Validate(SourceImage), "one-shot")

	bad := Config{Image: "x", Port: 80, Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, Web: true, Funnel: false, ExtraTags: []string{"nope"}}}.WithDefaults()
	require.ErrorContains(t, bad.Validate(SourceImage), "not a valid tag")
}

func TestTailnetChangesArePendingConfigFields(t *testing.T) {
	from := Config{Image: "x", Port: 80}.WithDefaults()
	to := Config{Image: "x", Port: 80, Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, Web: true, HidePublic: true}}.WithDefaults()
	var fields []string
	for _, c := range DiffConfig("config", from, to) {
		fields = append(fields, c.Field)
	}
	require.Contains(t, fields, "config.tailnet.enabled")
	require.Contains(t, fields, "config.tailnet.web")
	require.Contains(t, fields, "config.tailnet.hidePublic")

	require.False(t, tailnetChanged(nil, nil))
	require.True(t, tailnetChanged(nil, to.Tailnet))
	require.False(t, tailnetChanged(to.Tailnet, Config{Port: 80, Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, Web: true, HidePublic: true}}.WithDefaults().Tailnet))
	hostname := *to.Tailnet
	hostname.Hostname = "api"
	require.True(t, tailnetChanged(to.Tailnet, &hostname))
}
