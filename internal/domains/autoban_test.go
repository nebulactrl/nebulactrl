package domains_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestDecodeAutoBan(t *testing.T) {
	require.Nil(t, domains.DecodeAutoBan(nil))
	require.Nil(t, domains.DecodeAutoBan(json.RawMessage("null")))
	require.Nil(t, domains.DecodeAutoBan(json.RawMessage("{broken")), "a document that does not decode is no policy")
	got := domains.DecodeAutoBan(json.RawMessage(`{"enabled":true,"threshold":5,"windowSeconds":120,"durationSeconds":900,"allow":["203.0.113.0/24"]}`))
	require.Equal(t, &agentprotocol.AutoBan{Enabled: true, Threshold: 5, WindowSeconds: 120, DurationSeconds: 900, Allow: []string{"203.0.113.0/24"}}, got)
}

// A route carries the policy only while it is enabled: a route without one is
// not evaluated by the guard for auto-ban.
func TestRouteAutoBanCarriesEnabledPoliciesOnly(t *testing.T) {
	require.Nil(t, domains.RouteAutoBan(nil))
	require.Nil(t, domains.RouteAutoBan(json.RawMessage(`{"enabled":false,"threshold":5,"windowSeconds":120,"durationSeconds":900}`)))
	on := json.RawMessage(`{"enabled":true,"threshold":5,"windowSeconds":120,"durationSeconds":900}`)
	require.Equal(t, &agentprotocol.AutoBan{Enabled: true, Threshold: 5, WindowSeconds: 120, DurationSeconds: 900}, domains.RouteAutoBan(on))
	require.Equal(t, domains.RouteAutoBan(on), domains.AutoBanOf(db.Service{Autoban: on}))
	require.Nil(t, domains.AutoBanOf(db.Service{}))
}

func TestStoredAutoBanDefaultsAndDefaultOff(t *testing.T) {
	def := domains.StoredAutoBan(nil)
	require.Equal(t, agentprotocol.AutoBan{Threshold: 10, WindowSeconds: 600, DurationSeconds: 3600, Allow: []string{}}, def)
	require.NoError(t, def.Validate(), "the defaults are a valid policy, so enabling needs one toggle")
	require.True(t, domains.AutoBanIsDefaultOff(def))

	on := def
	on.Enabled = true
	require.False(t, domains.AutoBanIsDefaultOff(on))
	tuned := def
	tuned.Threshold = 3
	require.False(t, domains.AutoBanIsDefaultOff(tuned), "a disabled policy with its own limits is kept")
	allowed := def
	allowed.Allow = []string{"203.0.113.7/32"}
	require.False(t, domains.AutoBanIsDefaultOff(allowed))

	stored := domains.StoredAutoBan(json.RawMessage(`{"enabled":false,"threshold":10,"windowSeconds":600,"durationSeconds":3600}`))
	require.NotNil(t, stored.Allow, "an absent allow list reads as empty")
	require.True(t, domains.AutoBanIsDefaultOff(stored))
}
