package agents

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/template"

	"github.com/go-chi/chi/v5"

	agentdeploy "github.com/nebulactrl/nebulactrl/deploy/agent"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/releasesign"
	"github.com/nebulactrl/nebulactrl/internal/version"
)

var installTmpl = template.Must(template.New("install-agent.sh").Parse(agentdeploy.InstallScriptTemplate))

// safeURL is what may be embedded in the install script (it is single-quoted
// shell text and ends up in a systemd unit).
var safeURL = regexp.MustCompile(`^https?://[A-Za-z0-9._:/@%+=~-]+$`)

// controlPlaneURL returns the externally reachable base URL: the configured
// public URL, else one derived from the request. "" when neither is usable.
func (h *Hub) controlPlaneURL(r *http.Request) string {
	u := strings.TrimRight(h.cfg.PublicURL, "/")
	if u == "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		if r.Host != "" {
			u = (&url.URL{Scheme: scheme, Host: r.Host}).String()
		}
	}
	if !safeURL.MatchString(u) {
		return ""
	}
	return u
}

// installData is what install-agent.sh is rendered with.
type installData struct {
	DefaultURL string
	// Version is this control plane's release version ("0.1.2"), empty for a
	// development build. The agent binary it serves is the one of that release, so
	// the script fetches the release's signature of the binary by it.
	Version string
	// ReleasePubKey is the minisign public key the agent binary must be signed with.
	ReleasePubKey string
}

// releaseVersion is the control plane's version when it is a plain release (a
// development build has none), safe to write into the script.
func releaseVersion() string {
	v := strings.TrimPrefix(strings.TrimSpace(version.Current()), "v")
	if _, ok := version.Parse(v); !ok || strings.Contains(v, "+") {
		return ""
	}
	return v
}

// handleInstallScript serves GET /install-agent.sh.
func (h *Hub) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	data := installData{DefaultURL: h.controlPlaneURL(r), Version: releaseVersion(), ReleasePubKey: releasesign.PublicKey}
	if err := installTmpl.Execute(&buf, data); err != nil {
		h.log.ErrorContext(r.Context(), "render install script", "err", err)
		problem(w, apierr.Internal())
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// handleDownload serves GET /agent/v1/download/{amd64|arm64}.
func (h *Hub) handleDownload(w http.ResponseWriter, r *http.Request) {
	arch := chi.URLParam(r, "arch")
	path := h.agentBinaryPath(arch)
	if path == "" {
		problem(w, apierr.NotFound("agent binary"))
		return
	}
	f, err := os.Open(path) //nolint:gosec // operator-configured agent binary path
	if err != nil {
		problem(w, apierr.NotFound("agent binary"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		problem(w, apierr.NotFound("agent binary"))
		return
	}
	if info, err := h.AgentBinaryInfo(arch); err == nil {
		w.Header().Set("X-Checksum-Sha256", info.SHA256)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="nebulactrl-agent"`)
	http.ServeContent(w, r, "nebulactrl-agent", st.ModTime(), f)
}
