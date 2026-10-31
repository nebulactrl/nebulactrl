//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Host hardening through the real path: the API starts an audit, the control
// plane queues host_hardening, the agent runs it on the host it lives on, and
// the result (the state of every safe fix, and the Lynis report when Lynis is
// there) lands in the Security view. The test only reads the host: it never
// applies a fix, because the harness agent runs on the developer's or the CI
// machine. Applying and undoing are covered against a disposable Debian machine
// (see docs/security/host-hardening.md) and by the unit tests that run every fix
// script against a scratch root.
func TestHostHardeningAuditReportsFixStates(t *testing.T) {
	requireTier(t, tierFull)
	s := SharedStack(t)
	base := "/servers/" + s.ServerID + "/hardening"

	// The view exists before any audit and lists every fix as not yet audited.
	r := s.Do("GET", base, nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	v := r.JSON()
	fixes, _ := v["fixes"].([]any)
	require.GreaterOrEqual(t, len(fixes), 10)
	require.Equal(t, true, v["agentSupported"], "the harness agent advertises host.hardening")

	// Requests that would change the host are validated before anything is queued.
	r = s.Do("POST", base+"/apply", map[string]any{"fixIds": []string{"disable-ssh-password-auth"}})
	require.Equal(t, http.StatusBadRequest, r.Status, string(r.Raw))
	r = s.Do("POST", base+"/rollback", map[string]any{})
	require.Equal(t, http.StatusBadRequest, r.Status, string(r.Raw))

	// An audit, without installing anything.
	r = s.Do("POST", base+"/audit", map[string]any{"installLynis": false})
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	runID := r.JSON().Str("id")
	var run J
	require.True(t, eventually(3*time.Minute, time.Second, func() bool {
		v := s.Do("GET", base, nil)
		if v.Status != http.StatusOK {
			return false
		}
		for _, it := range v.JSON()["runs"].([]any) {
			m := it.(map[string]any)
			if m["id"] == runID && m["status"] != "pending" {
				run = J(m)
				return true
			}
		}
		return false
	}), "the audit run never finished")
	require.Contains(t, []string{"succeeded", "skipped"}, run.Str("status"), "%v", run)

	v = s.Do("GET", base, nil).JSON()
	states := map[string]bool{"applied": true, "compliant": true, "pending": true, "unavailable": true}
	for _, it := range v["fixes"].([]any) {
		f := it.(map[string]any)
		require.True(t, states[f["state"].(string)], "fix %v has state %v after an audit", f["id"], f["state"])
	}
	if run.Str("status") == "succeeded" {
		require.NotNil(t, v["hardeningIndex"])
		require.Equal(t, true, v["lynisInstalled"])
	} else {
		require.Nil(t, v["hardeningIndex"], "no Lynis, no index")
		require.Equal(t, false, v["lynisInstalled"])
	}

	// While the weekly audit is on, the next one is dated.
	require.NotNil(t, v["nextAuditAt"])
	r = s.Do("PUT", base, map[string]any{"weekly": false})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Nil(t, r.JSON()["nextAuditAt"])
	r = s.Do("PUT", base, map[string]any{"weekly": true})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))

	// Both the start and the finish are in the audit log.
	r = s.Do("GET", "/audit-events?limit=50", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Contains(t, string(r.Raw), "server.hardening.audit")
	require.Contains(t, string(r.Raw), "server.hardening.run")
}
