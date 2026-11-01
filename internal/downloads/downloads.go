// Package downloads serves the NebulaCtrl command line client from the control
// plane: the binaries that ship in the control-plane image, their SHA256SUMS and
// the one-line installer.
//
//	GET /download/cli/{os}/{arch}   the binary (os: linux, darwin, windows; arch: amd64, arm64)
//	GET /download/cli/SHA256SUMS    `<sha256>  <os>-<arch>/nebulactrl[.exe]` per binary
//	GET /cli.sh                     POSIX installer: curl -fsSL <control plane>/cli.sh | sh
//
// The routes are public (no session, no token): the binaries are not secret,
// the same as the agent download. They sit outside /api/v1, like /install.sh,
// so they are raw routes and not API operations: they are not in the OpenAPI
// document and so not in the MCP catalog.
//
// The image layout, written by the Dockerfile, is
//
//	<dir>/<os>-<arch>/nebulactrl[.exe]
//	<dir>/SHA256SUMS
//
// A development build has no <dir>: every route answers 404 with a message that
// says so.
package downloads

import (
	"bytes"
	_ "embed"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

//go:embed cli.sh.tmpl
var cliScriptTemplate string

var cliScript = template.Must(template.New("cli.sh").Parse(cliScriptTemplate))

// The platforms the image carries. A request names one of these or gets a 404,
// so nothing a client sends is ever joined into a file path.
var (
	oses   = []string{"linux", "darwin", "windows"}
	arches = []string{"amd64", "arm64"}
)

// unavailable is what a control plane without CLI binaries answers.
const unavailable = "this control plane has no CLI binaries (a development build); build the CLI from source: go build -o nebulactrl ./cmd/cli"

// safeURL is what may be embedded in the single-quoted shell text of cli.sh.
var safeURL = regexp.MustCompile(`^https?://[A-Za-z0-9._:/@%+=~-]+$`)

// Handler serves the downloads.
type Handler struct {
	dir       string
	publicURL string
	log       *slog.Logger

	sumsRaw []byte
	sums    map[string]string // "linux-amd64" -> lower-case hex sha256
}

// New reads <dir>/SHA256SUMS. An empty dir, or a dir without a usable
// SHA256SUMS, leaves the handler in its "no binaries" state.
func New(dir, publicURL string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	h := &Handler{dir: dir, publicURL: strings.TrimRight(publicURL, "/"), log: log}
	if dir == "" {
		return h
	}
	raw, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS")) //nolint:gosec // operator-configured downloads directory
	if err != nil {
		log.Warn("downloads: CLI binaries are not served", "dir", dir, "err", err)
		return h
	}
	sums := parseSums(raw)
	if len(sums) == 0 {
		log.Warn("downloads: SHA256SUMS lists no binary; CLI binaries are not served", "dir", dir)
		return h
	}
	h.sumsRaw, h.sums = raw, sums
	return h
}

// parseSums reads sha256sum output. Entries whose path is not
// <os>-<arch>/<binary name> for a known platform are ignored.
func parseSums(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*") // binary-mode marker
		platform, file := path.Split(name)
		platform = strings.TrimSuffix(platform, "/")
		goos, goarch, ok := strings.Cut(platform, "-")
		if !ok {
			continue
		}
		goos, goarch, ok = knownPlatform(goos, goarch)
		if !ok || file != binaryName(goos) {
			continue
		}
		out[goos+"-"+goarch] = strings.ToLower(fields[0])
	}
	return out
}

// knownPlatform returns the entries of oses and arches that equal goos and
// goarch. Callers build file paths from the returned values, which are the
// package's own constants and never the caller's strings.
func knownPlatform(goos, goarch string) (osName, archName string, ok bool) {
	osName, ok = listed(oses, goos)
	if !ok {
		return "", "", false
	}
	archName, ok = listed(arches, goarch)
	if !ok {
		return "", "", false
	}
	return osName, archName, true
}

// listed returns the element of list equal to s.
func listed(list []string, s string) (string, bool) {
	for _, v := range list {
		if v == s {
			return v, true
		}
	}
	return "", false
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "nebulactrl.exe"
	}
	return "nebulactrl"
}

// Available reports whether the control plane has CLI binaries to serve.
func (h *Handler) Available() bool { return len(h.sums) > 0 }

// Mount attaches the routes to the root router (no session, CSRF or auth).
func (h *Handler) Mount(r chi.Router) {
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		r.Method(m, "/download/cli/SHA256SUMS", http.HandlerFunc(h.serveSums))
		r.Method(m, "/download/cli/{os}/{arch}", http.HandlerFunc(h.serveBinary))
		r.Method(m, "/cli.sh", http.HandlerFunc(h.serveInstaller))
	}
}

func (h *Handler) notFound(w http.ResponseWriter) {
	apierr.Write(w, apierr.New(http.StatusNotFound, "cli_unavailable", unavailable))
}

func (h *Handler) serveSums(w http.ResponseWriter, r *http.Request) {
	if !h.Available() {
		h.notFound(w)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "SHA256SUMS", modTime(filepath.Join(h.dir, "SHA256SUMS")), bytes.NewReader(h.sumsRaw))
}

func (h *Handler) serveBinary(w http.ResponseWriter, r *http.Request) {
	goos, goarch, ok := knownPlatform(chi.URLParam(r, "os"), chi.URLParam(r, "arch"))
	if !ok {
		apierr.Write(w, apierr.New(http.StatusNotFound, "cli_platform_unknown",
			"no CLI for that platform; os is one of "+strings.Join(oses, ", ")+" and arch one of "+strings.Join(arches, ", ")))
		return
	}
	if !h.Available() {
		h.notFound(w)
		return
	}
	platform := goos + "-" + goarch
	sum, ok := h.sums[platform]
	if !ok {
		apierr.Write(w, apierr.New(http.StatusNotFound, "cli_platform_missing", "this control plane has no CLI for "+goos+"/"+goarch))
		return
	}
	name := binaryName(goos)
	f, err := os.Open(filepath.Join(h.dir, platform, name)) //nolint:gosec // platform and name come from the SHA256SUMS allow list
	if err != nil {
		h.log.Warn("downloads: binary listed in SHA256SUMS cannot be opened", "platform", platform, "err", err)
		apierr.Write(w, apierr.New(http.StatusNotFound, "cli_platform_missing", "this control plane has no CLI for "+goos+"/"+goarch))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		apierr.Write(w, apierr.New(http.StatusNotFound, "cli_platform_missing", "this control plane has no CLI for "+goos+"/"+goarch))
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	hd.Set("X-Checksum-Sha256", sum)
	hd.Set("ETag", `"`+sum+`"`)
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (h *Handler) serveInstaller(w http.ResponseWriter, r *http.Request) {
	if !h.Available() {
		// A 404 whose body is a script: `curl | sh` without -f prints the reason
		// instead of trying to run JSON.
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("#!/bin/sh\necho 'nebulactrl: " + unavailable + "' >&2\nexit 1\n"))
		return
	}
	var buf bytes.Buffer
	if err := cliScript.Execute(&buf, struct{ URL string }{URL: h.baseURL(r)}); err != nil {
		h.log.ErrorContext(r.Context(), "render cli.sh", "err", err)
		apierr.Write(w, apierr.Internal())
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(buf.Bytes())
}

// baseURL is the externally reachable origin of this control plane: the
// configured public URL, else one derived from the request. "" when neither is
// safe to embed in the script.
func (h *Handler) baseURL(r *http.Request) string {
	u := h.publicURL
	if u == "" && r.Host != "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		u = (&url.URL{Scheme: scheme, Host: r.Host}).String()
	}
	if !safeURL.MatchString(u) {
		return ""
	}
	return u
}

func modTime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}
