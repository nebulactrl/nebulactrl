package agents_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/releasesign"
	"github.com/nebulactrl/nebulactrl/internal/version"
)

func installScript(t *testing.T, h *harness) string {
	t.Helper()
	resp, err := http.Get(h.srv.URL + "/install-agent.sh")
	require.NoError(t, err)
	defer resp.Body.Close()
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return body.String()
}

// install-agent.sh verifies the agent binary against the release key: it is rendered with the
// control plane's release version (to find the signature) and the compiled-in public key.
func TestInstallScriptCarriesTheReleaseVersionAndKey(t *testing.T) {
	prev := version.Current()
	t.Cleanup(func() { version.SetCurrent(prev) })
	h := newHarness(t)

	version.SetCurrent("0.1.2")
	s := installScript(t, h)
	require.Contains(t, s, "RELEASE_VERSION='0.1.2'")
	require.Contains(t, s, "RELEASE_PUBKEY='"+releasesign.PublicKey+"'")

	// A development build has no release, so no signature to look for.
	version.SetCurrent("dev")
	require.Contains(t, installScript(t, h), "RELEASE_VERSION=''")

	// Whatever else the version string holds never reaches the shell.
	version.SetCurrent("0.1.2'; reboot; '")
	require.Contains(t, installScript(t, h), "RELEASE_VERSION=''")
}
