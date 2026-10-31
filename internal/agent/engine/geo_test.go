package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type geoRecorder struct {
	calls   int
	cfg     agentprotocol.GeoConfig
	secrets map[string]string
}

func (g *geoRecorder) Apply(cfg agentprotocol.GeoConfig, secrets map[string]string) {
	g.calls++
	g.cfg, g.secrets = cfg, secrets
}

func TestReconcileHandsGeolocationToTheManager(t *testing.T) {
	r := newRig(t, nil)
	rec := &geoRecorder{}
	r.eng.cfg.Geo = rec

	// No setting in the frame: the manager is left alone.
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{}))
	require.Zero(t, rec.calls)

	want := agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceMaxMind, Level: agentprotocol.GeoLevelCity, RefreshDays: 3}
	secrets := map[string]string{agentprotocol.SecretGeoMaxMindAccount: "1", agentprotocol.SecretGeoMaxMindKey: "k-secret"}
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Geo: &want, Secrets: secrets}))
	require.Equal(t, 1, rec.calls)
	require.Equal(t, want, rec.cfg)
	require.Equal(t, secrets, rec.secrets)
}

func TestReconcileWithoutAGeoManagerIsFine(t *testing.T) {
	r := newRig(t, nil)
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Geo: &agentprotocol.GeoConfig{Enabled: true}}))
}
