package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/config"
)

func httpGet(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(url)
	require.NoError(t, err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res, string(b)
}

// The CLI downloads are public, outside /api/v1: no session, no token.
func TestControlPlaneServesTheCLIWithoutAuthentication(t *testing.T) {
	dir := t.TempDir()
	body := []byte("#!/bin/sh\necho cli\n")
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "linux-amd64"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "linux-amd64", "nebulactrl"), body, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(hexSum+"  linux-amd64/nebulactrl\n"), 0o644))

	h := newHarnessCfg(t, func(c *config.Config) { c.CLIBinariesDir = dir })

	res, got := httpGet(t, h.srv.URL+"/download/cli/linux/amd64")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, string(body), got)
	require.Equal(t, hexSum, res.Header.Get("X-Checksum-Sha256"))

	res, got = httpGet(t, h.srv.URL+"/download/cli/SHA256SUMS")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, got, hexSum+"  linux-amd64/nebulactrl")

	res, got = httpGet(t, h.srv.URL+"/cli.sh")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "text/x-shellscript")
	require.Contains(t, got, "NC_URL=${NEBULACTRL_URL:-'"+h.srv.URL+"'}")

	// Not served by the SPA fallback or the API router either.
	res, _ = httpGet(t, h.srv.URL+"/download/cli/plan9/amd64")
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	require.Equal(t, "application/problem+json", res.Header.Get("Content-Type"))
}

// A development server has no CLI binaries and says so.
func TestControlPlaneWithoutCLIBinariesAnswers404WithAReason(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/download/cli/linux/amd64", "/download/cli/SHA256SUMS", "/cli.sh"} {
		res, got := httpGet(t, h.srv.URL+p)
		require.Equal(t, http.StatusNotFound, res.StatusCode, p)
		require.Contains(t, got, "development build", p)
	}
}
