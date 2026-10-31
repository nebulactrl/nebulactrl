package configsync

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

// An omitted proxied is not part of the plan: a live DNS-only name stays DNS only, whatever the
// server's default for new names is. A written value is compared with the live one.
func TestCloudflareOpsOmittedProxiedNeverFlipsALiveName(t *testing.T) {
	pc := unitPlanCtx()
	pc.live.Refs.Cloudflare = true
	ls := &liveService{Domains: []db.Domain{{Hostname: "shop.acme.com", CfZoneID: "z", CfProxied: false, CfOriginLock: true}}}
	const id = "domains.shop.acme.com.cloudflare"

	plan := func(c projectconfig.CloudflareRule) (drift bool, proj string) {
		t.Helper()
		want, err := cloudflareOps{}.desired(pc, ls, projectconfig.FieldValue{ID: id, Value: c})
		require.NoError(t, err)
		live, ok := cloudflareOps{}.live(pc, ls, id, want.Proj)
		require.True(t, ok)
		return live.FP != want.FP, want.Proj
	}

	drift, proj := plan(projectconfig.CloudflareRule{})
	require.False(t, drift)
	require.Empty(t, proj)

	drift, proj = plan(projectconfig.CloudflareRule{OriginLock: new(true)})
	require.False(t, drift, "proxied is omitted, so the live DNS-only setting is not compared")
	require.Equal(t, "originLock", proj)

	drift, _ = plan(projectconfig.CloudflareRule{Proxied: new(false)})
	require.False(t, drift, "proxied = false matches the live DNS-only name")

	drift, proj = plan(projectconfig.CloudflareRule{Proxied: new(true)})
	require.True(t, drift, "an explicit proxied = true is a change")
	require.Equal(t, "proxied", proj)
}

func TestCloudflareOpsNeedAConnection(t *testing.T) {
	pc := unitPlanCtx()
	_, err := cloudflareOps{}.desired(pc, nil, projectconfig.FieldValue{ID: "domains.a.acme.com.cloudflare", Value: projectconfig.CloudflareRule{}})
	require.Error(t, err)
}
