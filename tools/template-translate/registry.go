package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// registry is a minimal anonymous OCI registry client for the enrich step. It
// only ever reads manifests with HEAD (no pull, and Docker Hub does not count
// HEAD requests against its pull limit) and lists tags. It never downloads a
// layer.
type registry struct {
	hc *http.Client
	// hubAPI is the Docker Hub web API used for the platform list of a tag
	// (a manifest GET would count as a pull). Empty uses https://hub.docker.com.
	hubAPI string
	// scheme is https except in tests.
	scheme string

	mu     sync.Mutex
	tokens map[string]string // realm|service|scope → bearer token
}

func newRegistry() *registry {
	return &registry{hc: &http.Client{Timeout: 30 * time.Second}, scheme: "https", tokens: map[string]string{}}
}

// imageRef is a parsed repo:tag.
type imageRef struct {
	Host string // registry host, docker.io for Docker Hub
	Repo string // path, library/ added for Docker Hub official images
	Tag  string
}

func (r imageRef) String() string {
	h := r.Host
	if h == "docker.io" {
		return strings.TrimPrefix(r.Repo, "library/") + ":" + r.Tag
	}
	return h + "/" + r.Repo + ":" + r.Tag
}

// parseImage splits "[host/]repo[:tag]".
func parseImage(ref string) imageRef {
	var r imageRef
	rest := ref
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		r.Tag, rest = rest[i+1:], rest[:i]
	}
	r.Host = "docker.io"
	if first, tail, ok := strings.Cut(rest, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Host, rest = first, tail
	}
	if r.Host == "index.docker.io" || r.Host == "registry-1.docker.io" {
		r.Host = "docker.io"
	}
	if r.Host == "docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	r.Repo = rest
	return r
}

func (g *registry) endpoint(host string) string {
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	return g.scheme + "://" + host
}

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// do sends a request, answering one Bearer challenge anonymously.
func (g *registry) do(ctx context.Context, method, u string, accept string) (*http.Response, error) {
	req := func(token string) (*http.Response, error) {
		rq, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			rq.Header.Set("Accept", accept)
		}
		if token != "" {
			rq.Header.Set("Authorization", "Bearer "+token)
		}
		return g.hc.Do(rq)
	}
	resp, err := req("")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := resp.Header.Get("Www-Authenticate")
	_ = resp.Body.Close()
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return nil, fmt.Errorf("401 without a bearer challenge")
	}
	params := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if params["realm"] == "" {
		return nil, errors.New("bearer challenge without realm")
	}
	key := params["realm"] + "|" + params["service"] + "|" + params["scope"]
	g.mu.Lock()
	tok := g.tokens[key]
	g.mu.Unlock()
	if tok == "" {
		q := url.Values{}
		if params["service"] != "" {
			q.Set("service", params["service"])
		}
		if params["scope"] != "" {
			q.Set("scope", params["scope"])
		}
		tu := params["realm"] + "?" + q.Encode()
		tr, err := http.NewRequestWithContext(ctx, http.MethodGet, tu, nil)
		if err != nil {
			return nil, err
		}
		tresp, err := g.hc.Do(tr)
		if err != nil {
			return nil, err
		}
		defer tresp.Body.Close()
		var body struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		if tresp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("token endpoint answered %d", tresp.StatusCode)
		}
		if err := json.NewDecoder(tresp.Body).Decode(&body); err != nil {
			return nil, err
		}
		tok = body.Token
		if tok == "" {
			tok = body.AccessToken
		}
		g.mu.Lock()
		g.tokens[key] = tok
		g.mu.Unlock()
	}
	return req(tok)
}

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// Digest is the manifest (index) digest of host/repo:tag, from a HEAD request.
func (g *registry) Digest(ctx context.Context, r imageRef) (string, error) {
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", g.endpoint(r.Host), r.Repo, url.PathEscape(r.Tag))
	resp, err := g.do(ctx, http.MethodHead, u, manifestAccept)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: manifest HEAD answered %d", r, resp.StatusCode)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(d) {
		return "", fmt.Errorf("%s: registry sent no usable digest", r)
	}
	return d, nil
}

// Tags lists every tag of the repository (following Link pagination).
func (g *registry) Tags(ctx context.Context, r imageRef) ([]string, error) {
	var out []string
	next := fmt.Sprintf("%s/v2/%s/tags/list?n=1000", g.endpoint(r.Host), r.Repo)
	for page := 0; next != "" && page < 60; page++ {
		resp, err := g.do(ctx, http.MethodGet, next, "")
		if err != nil {
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = func() error {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s: tag list answered %d", r.Repo, resp.StatusCode)
			}
			return json.NewDecoder(resp.Body).Decode(&body)
		}()
		if err != nil {
			return nil, err
		}
		out = append(out, body.Tags...)
		next = ""
		if link := resp.Header.Get("Link"); link != "" {
			if m := regexp.MustCompile(`<([^>]+)>;\s*rel="next"`).FindStringSubmatch(link); m != nil {
				nu, err := url.Parse(m[1])
				if err == nil {
					next = g.endpoint(r.Host) + nu.RequestURI()
					if nu.IsAbs() {
						next = m[1]
					}
				}
			}
		}
	}
	return out, nil
}

// Platforms lists the os/arch pairs of host/repo:tag without pulling anything.
// Docker Hub answers from its web API (a manifest GET would count as a pull);
// other registries answer from the index manifest.
func (g *registry) Platforms(ctx context.Context, r imageRef) (map[string]bool, error) {
	if r.Host == "docker.io" {
		base := g.hubAPI
		if base == "" {
			base = "https://hub.docker.com"
		}
		ns, name, _ := strings.Cut(r.Repo, "/")
		u := fmt.Sprintf("%s/v2/repositories/%s/%s/tags/%s", base, ns, name, url.PathEscape(r.Tag))
		rq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := g.hc.Do(rq)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: hub tag API answered %d", r, resp.StatusCode)
		}
		var body struct {
			Images []struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"images"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for _, im := range body.Images {
			if im.OS != "" && im.OS != "unknown" {
				out[im.OS+"/"+im.Architecture] = true
			}
		}
		return out, nil
	}
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", g.endpoint(r.Host), r.Repo, url.PathEscape(r.Tag))
	resp, err := g.do(ctx, http.MethodGet, u, manifestAccept)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: manifest answered %d", r, resp.StatusCode)
	}
	var m struct {
		Manifests []struct {
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range m.Manifests {
		if p := e.Platform; p.OS != "" && p.OS != "unknown" {
			out[p.OS+"/"+p.Architecture] = true
		}
	}
	if len(out) == 0 && len(m.Manifests) == 0 {
		// A single-platform manifest: assume nothing; the final template-pin
		// check reads its config.
		return nil, nil
	}
	return out, nil
}
