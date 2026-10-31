package agents

import (
	"net/http"
	"sync"

	installdeploy "github.com/nebulactrl/nebulactrl/deploy/install"
)

var (
	installerOnce   sync.Once
	installerScript []byte
)

// handleInstaller serves GET /install.sh: the control-plane installer with its
// files and libraries inlined, so `curl -fsSL <url>/install.sh | sudo bash`
// needs nothing else. It installs a control plane on the machine that runs it
// (a new one, or an upgrade of that machine's); it contains no secret and no
// state of this instance.
func (h *Hub) handleInstaller(w http.ResponseWriter, r *http.Request) {
	installerOnce.Do(func() { installerScript = []byte(installdeploy.Script()) })
	serveShell(w, installerScript)
}

// handleUninstaller serves GET /uninstall.sh.
func (h *Hub) handleUninstaller(w http.ResponseWriter, r *http.Request) {
	serveShell(w, []byte(installdeploy.Uninstall()))
}

func serveShell(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
