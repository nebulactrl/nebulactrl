package platform

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

var pinnedRe = regexp.MustCompile(`^[a-z0-9./_-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)

func TestCatalogIsHeadscaleOnlyWithAPinnedImage(t *testing.T) {
	cat := Catalog()
	require.Equal(t, []string{KindHeadscale}, Kinds)
	require.Len(t, cat, 1)
	require.Equal(t, KindHeadscale, cat[0].Kind)
	require.Regexp(t, pinnedRe, cat[0].Image, "the image must be pinned by tag and digest")
	require.NotEmpty(t, cat[0].License)
	require.NotEmpty(t, cat[0].Summary)
	require.False(t, cat[0].AGPL)
	_, ok := InfoFor("s3")
	require.False(t, ok, "object storage, the registry and logs are instance services")
}

func specFor(t *testing.T) serviceSpec {
	t.Helper()
	sp, err := build(KindHeadscale, specInput{Hostname: "svc.example.com", PublicURL: "https://svc.example.com"})
	require.NoError(t, err)
	return sp
}

func TestSpecIsAValidStagedService(t *testing.T) {
	sp := specFor(t)
	cfg := sp.Config.WithDefaults()
	require.NoError(t, cfg.Validate(services.SourceImage))
	info, _ := InfoFor(KindHeadscale)
	require.Equal(t, info.Image, cfg.Image)
	require.Equal(t, sp.DomainPort, cfg.Port, "the domain routes to the port the service listens on")
	require.NotEmpty(t, sp.Volumes)
	require.Equal(t, cfg.Healthcheck.Port, cfg.Port)
	// Headscale fetches the DERP map, so its egress stays open.
	require.Equal(t, !info.Egress, cfg.BlockEgress)
	require.False(t, cfg.BlockEgress)
}

func TestUnknownKindIsRefused(t *testing.T) {
	for _, kind := range []string{"s3", "loki", "grafana", "registry"} {
		_, err := build(kind, specInput{})
		require.Error(t, err, kind)
	}
}
