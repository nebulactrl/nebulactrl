package agents_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/events"
)

func TestAgentBinaryInfoAndChecksumRoute(t *testing.T) {
	dir := t.TempDir()
	hub := agents.NewHub(nil, events.NewBus(), nil, &config.Config{AgentBinariesDir: dir}, nil)
	t.Cleanup(hub.Close)
	r := chi.NewRouter()
	hub.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	_, err := hub.AgentBinaryInfo("amd64")
	require.ErrorIs(t, err, agents.ErrNoAgentBinary, "no file yet")
	_, err = hub.AgentBinaryInfo("riscv")
	require.ErrorIs(t, err, agents.ErrNoAgentBinary, "not a shipped architecture")
	resp, err := http.Get(srv.URL + "/agent/v1/download/amd64/sha256")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	body := []byte("agent binary v1")
	path := filepath.Join(dir, "nebulactrl-agent-linux-amd64")
	require.NoError(t, os.WriteFile(path, body, 0o755))
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])

	info, err := hub.AgentBinaryInfo("amd64")
	require.NoError(t, err)
	require.Equal(t, want, info.SHA256)
	require.EqualValues(t, len(body), info.Size)

	// The checksum route is in sha256sum format; the download carries the same digest.
	resp, err = http.Get(srv.URL + "/agent/v1/download/amd64/sha256")
	require.NoError(t, err)
	text, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, want+"  nebulactrl-agent\n", string(text))
	resp, err = http.Get(srv.URL + "/agent/v1/download/amd64")
	require.NoError(t, err)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, body, got)
	require.Equal(t, want, resp.Header.Get("X-Checksum-Sha256"))

	// Replacing the file invalidates the cached digest.
	body2 := []byte("agent binary v2, longer")
	require.NoError(t, os.WriteFile(path, body2, 0o755))
	require.NoError(t, os.Chtimes(path, time.Now().Add(time.Minute), time.Now().Add(time.Minute)))
	sum2 := sha256.Sum256(body2)
	info, err = hub.AgentBinaryInfo("amd64")
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(sum2[:]), info.SHA256)
}
