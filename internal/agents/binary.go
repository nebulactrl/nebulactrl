package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// BinaryInfo describes the agent binary the control plane serves for one
// architecture.
type BinaryInfo struct {
	// SHA256 is the lower-case hex digest of the file.
	SHA256 string
	Size   int64
}

// ErrNoAgentBinary means this control plane ships no agent binary for the
// architecture (a development server, or an image built without them).
var ErrNoAgentBinary = errors.New("this control plane has no agent binary for that architecture")

type binaryCacheKey struct {
	path    string
	size    int64
	modTime time.Time
}

type binaryCache struct {
	mu sync.Mutex
	m  map[binaryCacheKey]BinaryInfo
}

// agentBinaryPath is where the binary for arch lives, or "" when arch is not a
// shipped architecture or no directory is configured.
func (h *Hub) agentBinaryPath(arch string) string {
	name := agentBinaryFile(arch)
	if name == "" || h.cfg.AgentBinariesDir == "" {
		return ""
	}
	return filepath.Join(h.cfg.AgentBinariesDir, name)
}

// agentBinaryFile maps the architecture of a request to the file name of its
// binary, or "" for any other value. The names are fixed here, never built
// from the request, so the URL cannot steer the lookup out of the directory.
func agentBinaryFile(arch string) string {
	switch arch {
	case "amd64":
		return "nebulactrl-agent-linux-amd64"
	case "arm64":
		return "nebulactrl-agent-linux-arm64"
	}
	return ""
}

// AgentBinaryInfo returns the digest and size of the served agent binary. The
// digest is computed once per file version (path, size, modification time).
func (h *Hub) AgentBinaryInfo(arch string) (BinaryInfo, error) {
	path := h.agentBinaryPath(arch)
	if path == "" {
		return BinaryInfo{}, ErrNoAgentBinary
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return BinaryInfo{}, ErrNoAgentBinary
	}
	key := binaryCacheKey{path, st.Size(), st.ModTime()}
	h.binCache.mu.Lock()
	defer h.binCache.mu.Unlock()
	if info, ok := h.binCache.m[key]; ok {
		return info, nil
	}
	f, err := os.Open(path) //nolint:gosec // operator-configured agent binary path
	if err != nil {
		return BinaryInfo{}, ErrNoAgentBinary
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	if err != nil {
		return BinaryInfo{}, fmt.Errorf("hash agent binary: %w", err)
	}
	info := BinaryInfo{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: n}
	if h.binCache.m == nil {
		h.binCache.m = map[binaryCacheKey]BinaryInfo{}
	}
	for k := range h.binCache.m {
		if k.path == path {
			delete(h.binCache.m, k) // a replaced file's digest is stale
		}
	}
	h.binCache.m[key] = info
	return info, nil
}

// handleDownloadChecksum serves GET /agent/v1/download/{arch}/sha256 in the
// format of sha256sum(1), so `sha256sum -c` verifies a download.
func (h *Hub) handleDownloadChecksum(w http.ResponseWriter, r *http.Request) {
	info, err := h.AgentBinaryInfo(chi.URLParam(r, "arch"))
	if err != nil {
		problem(w, apierr.NotFound("agent binary"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "%s  nebulactrl-agent\n", info.SHA256)
}
