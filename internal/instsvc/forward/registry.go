package forward

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/registries"
)

// Distribution's name grammar, per path component.
var (
	repoComponent = `[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*`
	repoRestRe    = regexp.MustCompile(`^` + repoComponent + `(?:/` + repoComponent + `)*$`)

	// The route tails, matched from the end so a repository whose own name
	// holds a word like "blobs" keeps the longest name (as Distribution's does).
	manifestsRe = regexp.MustCompile(`^(.+)/manifests/([^/]+)$`)
	uploadsRe   = regexp.MustCompile(`^(.+)/blobs/uploads/([A-Za-z0-9._~=-]*)$`)
	blobsRe     = regexp.MustCompile(`^(.+)/blobs/([a-z0-9]+(?:[.+_-][a-z0-9]+)*:[a-fA-F0-9]+)$`)
	tagsRe      = regexp.MustCompile(`^(.+)/tags/list$`)
	referrersRe = regexp.MustCompile(`^(.+)/referrers/([a-z0-9]+(?:[.+_-][a-z0-9]+)*:[a-fA-F0-9]+)$`)
)

type registryHandler struct {
	f     *Forwarder
	proxy *httputil.ReverseProxy
}

func newRegistry(f *Forwarder, raw string) (http.Handler, error) {
	target, err := parseBase(raw)
	if err != nil {
		return nil, err
	}
	h := &registryHandler{f: f}
	h.proxy = newProxy(target, f.log, nil,
		func(resp *http.Response) error {
			// A 401 here means the token was not accepted; the challenge would send
			// the Docker daemon to a token endpoint that does not exist.
			resp.Header.Del("Www-Authenticate")
			return nil
		},
		func(w http.ResponseWriter, _ *http.Request, _ error) {
			registryError(w, http.StatusBadGateway, "UNAVAILABLE", "the built-in registry is not reachable")
		})
	return h, nil
}

func registryError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": msg}}})
}

// registryRoute is a parsed /v2/ request.
type registryRoute struct {
	// name is the repository ("" for the /v2/ ping).
	name string
}

// parseRegistryPath parses the part of the path after "/v2/".
func parseRegistryPath(rest string) (registryRoute, bool) {
	if rest == "" {
		return registryRoute{}, true // GET /v2/
	}
	for _, re := range []*regexp.Regexp{manifestsRe, uploadsRe, blobsRe, tagsRe, referrersRe} {
		if m := re.FindStringSubmatch(rest); m != nil {
			return registryRoute{name: m[1]}, true
		}
	}
	return registryRoute{}, false
}

// inTeam reports whether repository name lies under prefix ("<team-id>/").
func inTeam(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix)
	return ok && repoRestRe.MatchString(rest)
}

// hasEncodedSeparator reports a raw path with an encoded slash, dot or backslash.
func hasEncodedSeparator(raw string) bool {
	raw = strings.ToLower(raw)
	return strings.Contains(raw, "%2f") || strings.Contains(raw, "%2e") || strings.Contains(raw, "%5c")
}

func (h *registryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pe := peerOf(ctx)
	deny := func(why string) {
		h.f.log.Warn("registry request refused", "team_id", pe.team.String(), "server_id", pe.server.String(), "method", r.Method, "path", r.URL.Path, "why", why)
		registryError(w, http.StatusForbidden, "DENIED", "requested access to the resource is denied")
	}
	// Encoded slashes or dots would let the registry and this check read the
	// same bytes as different repositories.
	if hasEncodedSeparator(r.URL.RawPath) || strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.Path, "//") {
		deny("unclean path")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/")
	if !ok {
		if r.URL.Path == "/v2" {
			rest = ""
		} else {
			registryError(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}
	}
	route, ok := parseRegistryPath(rest)
	if !ok {
		deny("not a repository route") // _catalog included
		return
	}
	ts, err := h.f.teamSlices(ctx, pe.team)
	if err != nil || ts.Registry == nil || (ts.Registry.State != "ready" && ts.Registry.State != "full") {
		registryError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the built-in registry is not available")
		return
	}
	prefix := strings.TrimSuffix(ts.Registry.Ref, "/") + "/"
	if prefix != pe.team.String()+"/" {
		registryError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the built-in registry is not available")
		return
	}

	write := false
	var access []registries.RepoAccess
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if route.name != "" {
			access = []registries.RepoAccess{{Name: route.name, Actions: []string{registries.ActionPull}}}
		}
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		if route.name == "" {
			deny("write without a repository")
			return
		}
		write = true
		access = []registries.RepoAccess{{Name: route.name, Actions: []string{registries.ActionPull, registries.ActionPush}}}
	case http.MethodDelete:
		if route.name == "" {
			deny("delete without a repository")
			return
		}
		write = true
		access = []registries.RepoAccess{{Name: route.name, Actions: []string{registries.ActionDelete}}}
	default:
		deny("method")
		return
	}
	if route.name != "" && !inTeam(route.name, prefix) {
		deny("repository is outside the team prefix")
		return
	}
	// Cross-repository mount: the source needs pull rights, and only inside the team.
	q := r.URL.Query()
	if from := q.Get("from"); from != "" || q.Has("mount") {
		if r.Method != http.MethodPost || from == "" || !inTeam(from, prefix) {
			deny("mount source is outside the team prefix")
			return
		}
		access = append(access, registries.RepoAccess{Name: from, Actions: []string{registries.ActionPull}})
	}

	if write {
		if h.f.d.Registry.ReadOnly(ctx) {
			w.Header().Set("Retry-After", "60")
			registryError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the registry is in its weekly maintenance window; try again in a few minutes")
			return
		}
		// Deletes always pass: they are how a team makes room.
		if r.Method != http.MethodDelete && h.f.d.Ledger != nil {
			switch err := h.f.d.Ledger.CheckRegistryWrite(ctx, pe.team); {
			case errors.Is(err, instsvc.ErrQuotaExceeded):
				registryError(w, http.StatusForbidden, "DENIED", "the storage quota of this team's built-in registry is used up; delete images or wait for the retention sweep")
				return
			case errors.Is(err, instsvc.ErrDiskLow):
				w.Header().Set("Retry-After", "300")
				registryError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the host running the built-in registry is low on disk space")
				return
			case err != nil:
				h.f.log.Error("registry write check", "team_id", pe.team.String(), "err", err)
				registryError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the built-in registry is not available")
				return
			}
		}
		h.f.registryWrites.Add(1)
		defer h.f.registryWrites.Add(-1)
	}
	tok, err := h.f.d.Registry.SignRepoToken(ctx, access...)
	if err != nil {
		h.f.log.Error("registry token", "team_id", pe.team.String(), "err", err)
		registryError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the built-in registry is not available")
		return
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	// Every manifest a team pushes is recorded, so images NebulaCtrl did not
	// build are counted against the team's quota and age out (registries.Sweep).
	if r.Method == http.MethodPut && h.f.d.Ledger != nil && manifestsRe.MatchString(rest) {
		body := &captureBody{rc: r.Body, max: maxManifestCapture}
		r.Body = body
		rec := &statusRecorder{ResponseWriter: w}
		h.proxy.ServeHTTP(rec, r)
		if rec.status == http.StatusCreated {
			h.f.recordPush(ctx, pe.team, route.name, rec.Header().Get("Docker-Content-Digest"), body.buf.Bytes())
		}
		return
	}
	h.proxy.ServeHTTP(w, r)
}
