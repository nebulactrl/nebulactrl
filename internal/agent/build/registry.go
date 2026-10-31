package build

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A minimal OCI distribution client: enough to check that a registry is
// reachable and accepts a credential (GET /v2/, with the Docker token flow for
// bearer registries) and to read the size of a pushed manifest. Pushing itself
// is BuildKit's job.

// registryTimeout bounds one registry HTTP exchange.
const registryTimeout = 20 * time.Second

// RegistryCheck describes one registry check.
type RegistryCheck struct {
	// Host is registry.example.com[:port], no scheme.
	Host     string
	Insecure bool
	// Repository (ns/repo or host/ns/repo) scopes the token request of a
	// bearer registry so push permission can be verified. Optional.
	Repository string
	// Auth is the credential; nil checks anonymously.
	Auth *agentprotocol.RegistryAuth
	// WantPush says the credential is meant for pushing.
	WantPush bool
}

// registryEndpoint maps a registry host to the host its API answers on and the
// key `docker login` stores its credential under (Docker Hub is special).
func registryEndpoint(host string) (apiHost, authKey string) {
	switch strings.ToLower(host) {
	case "docker.io", "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "registry-1.docker.io", "https://index.docker.io/v1/"
	}
	return host, host
}

// IsLoopbackHost reports whether a registry host (host[:port]) is loopback.
func IsLoopbackHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// repoPath strips a leading registry host from a repository reference.
func repoPath(host, repo string) string {
	if first, rest, ok := strings.Cut(repo, "/"); ok && strings.EqualFold(first, host) {
		return rest
	}
	return repo
}

// repositoryNameRe is the OCI distribution grammar of a repository name:
// lower-case path components separated by "/", each made of alphanumeric runs
// joined by ".", "_", "__" or dashes.
var repositoryNameRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)*$`)

// validRepositoryName reports whether repo may go into a registry URL path and
// a token scope.
func validRepositoryName(repo string) bool {
	return len(repo) <= 255 && repositoryNameRe.MatchString(repo)
}

type regClient struct {
	hc   *http.Client
	base string
	// insecure is set when the registry itself is reached over plain http.
	insecure bool
	auth     *agentprotocol.RegistryAuth
}

func newRegClient(hc *http.Client, host string, insecure bool, auth *agentprotocol.RegistryAuth) *regClient {
	if hc == nil {
		hc = &http.Client{Timeout: registryTimeout}
	}
	api, _ := registryEndpoint(host)
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	return &regClient{hc: hc, base: scheme + "://" + api, insecure: insecure, auth: auth}
}

// errCredRejected is a bearer registry's token service refusing the credential.
var errCredRejected = errors.New("the registry's token service rejected the credentials")

// tokenGrant is what a bearer token says it allows (when it is a readable JWT).
type tokenGrant struct {
	parsed  bool
	actions map[string]map[string]bool // repository -> action -> true
}

func (g *tokenGrant) allows(repo, action string) bool {
	if g == nil || !g.parsed {
		return true // opaque token: the push itself will tell
	}
	return g.actions[repo][action]
}

// get performs one request, following a bearer challenge once. The caller
// closes the body.
func (c *regClient) get(ctx context.Context, method, path, scope, accept string) (*http.Response, *tokenGrant, error) {
	do := func(authz string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		return c.hc.Do(req)
	}
	first := ""
	if c.auth != nil && c.auth.Username != "" {
		first = "Basic " + base64.StdEncoding.EncodeToString([]byte(c.auth.Username+":"+c.auth.Password))
	}
	resp, err := do(first)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil, nil
	}
	params, ok := parseBearerChallenge(resp.Header.Get("Www-Authenticate"))
	if !ok {
		return resp, nil, nil // Basic (or unknown): the 401 is the answer
	}
	_ = resp.Body.Close()
	token, grant, err := c.token(ctx, params, scope)
	if err != nil {
		return nil, nil, err
	}
	resp, err = do("Bearer " + token)
	return resp, grant, err
}

// token fetches a bearer token from the registry's token service.
func (c *regClient) token(ctx context.Context, params map[string]string, scope string) (string, *tokenGrant, error) {
	realm := params["realm"]
	u, err := url.Parse(realm)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", nil, fmt.Errorf("the registry sent an invalid token realm %q", realm)
	}
	// The credential goes to the token service: a registry reached over https
	// must not send it to a plain http endpoint.
	if u.Scheme == "http" && !c.insecure {
		return "", nil, fmt.Errorf("the registry sent a plain http token realm %q", realm)
	}
	q := u.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, err
	}
	if c.auth != nil && c.auth.Username != "" {
		req.SetBasicAuth(c.auth.Username, c.auth.Password)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("token service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", nil, errCredRejected
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("the registry's token service answered HTTP %d", resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", nil, fmt.Errorf("token service: %w", err)
	}
	tok := body.Token
	if tok == "" {
		tok = body.AccessToken
	}
	if tok == "" {
		return "", nil, errors.New("the registry's token service returned no token")
	}
	return tok, parseGrant(tok), nil
}

// parseBearerChallenge reads `Bearer realm="…",service="…",scope="…"`.
func parseBearerChallenge(h string) (map[string]string, bool) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	if !strings.EqualFold(scheme, "bearer") {
		return nil, false
	}
	out := map[string]string{}
	for rest = strings.TrimSpace(rest); rest != ""; {
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, ","))
		v = strings.TrimSpace(v)
		var val string
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				break
			}
			val, rest = v[1:1+end], strings.TrimSpace(strings.TrimPrefix(v[end+2:], ","))
		} else {
			val, rest, _ = strings.Cut(v, ",")
			rest = strings.TrimSpace(rest)
		}
		out[strings.ToLower(k)] = val
	}
	return out, out["realm"] != ""
}

// parseGrant reads the access claim of a JWT token; an opaque token yields an
// unparsed grant.
func parseGrant(token string) *tokenGrant {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return &tokenGrant{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return &tokenGrant{}
	}
	var claims struct {
		Access []struct {
			Type    string   `json:"type"`
			Name    string   `json:"name"`
			Actions []string `json:"actions"`
		} `json:"access"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Access == nil {
		return &tokenGrant{}
	}
	g := &tokenGrant{parsed: true, actions: map[string]map[string]bool{}}
	for _, a := range claims.Access {
		if a.Type != "repository" {
			continue
		}
		if g.actions[a.Name] == nil {
			g.actions[a.Name] = map[string]bool{}
		}
		for _, act := range a.Actions {
			g.actions[a.Name][act] = true
		}
	}
	return g
}

// CheckRegistry verifies that the registry answers /v2/ and accepts the
// credential. With a bearer registry and a Repository it also checks that the
// granted token may push. Pull and Push report what the credential is good
// for; Error is set when the registry is unusable for the check's purpose.
func CheckRegistry(ctx context.Context, hc *http.Client, chk RegistryCheck) agentprotocol.RegistryCheckResult {
	if chk.Host == "" {
		return agentprotocol.RegistryCheckResult{Error: "registry host is required"}
	}
	c := newRegClient(hc, chk.Host, chk.Insecure, chk.Auth)
	repo := repoPath(chk.Host, chk.Repository)
	if repo != "" && !validRepositoryName(repo) {
		return agentprotocol.RegistryCheckResult{Error: fmt.Sprintf("invalid repository name %q", repo)}
	}
	scope := ""
	if repo != "" {
		scope = "repository:" + repo + ":pull,push"
	}
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	resp, grant, err := c.get(ctx, http.MethodGet, "/v2/", scope, "")
	if err != nil {
		if errors.Is(err, errCredRejected) {
			return agentprotocol.RegistryCheckResult{Error: chk.Host + ": " + err.Error()}
		}
		return agentprotocol.RegistryCheckResult{Error: "cannot reach " + chk.Host + ": " + err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusOK:
		res := agentprotocol.RegistryCheckResult{Pull: true, Push: chk.WantPush && chk.Auth != nil}
		if res.Push && repo != "" && !grant.allows(repo, "push") {
			res.Push = false
		}
		if chk.WantPush && !res.Push && chk.Auth != nil && repo != "" {
			res.Error = "the credential may not push to " + repo
		} else if chk.WantPush && chk.Auth == nil {
			res.Error = "no push credential was provided"
		}
		return res
	case http.StatusUnauthorized, http.StatusForbidden:
		if chk.Auth == nil {
			return agentprotocol.RegistryCheckResult{Error: chk.Host + " requires authentication and no credential was provided"}
		}
		return agentprotocol.RegistryCheckResult{Error: chk.Host + " rejected the credential (HTTP " + fmt.Sprint(resp.StatusCode) + ")"}
	case http.StatusNotFound:
		return agentprotocol.RegistryCheckResult{Error: chk.Host + " does not look like an OCI registry (GET /v2/ returned 404)"}
	}
	return agentprotocol.RegistryCheckResult{Error: fmt.Sprintf("%s answered HTTP %d on /v2/", chk.Host, resp.StatusCode)}
}

// manifestSize returns the compressed size of an image (config plus layers) as
// the registry reports it. It fails for an index (multi-platform) manifest.
func manifestSize(ctx context.Context, hc *http.Client, host string, insecure bool, auth *agentprotocol.RegistryAuth, repository, digest string) (int64, error) {
	c := newRegClient(hc, host, insecure, auth)
	repo := repoPath(host, repository)
	if !validRepositoryName(repo) {
		return 0, fmt.Errorf("invalid repository name %q", repo)
	}
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	resp, _, err := c.get(ctx, http.MethodGet, "/v2/"+repo+"/manifests/"+digest, "repository:"+repo+":pull",
		"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("manifest: HTTP %d", resp.StatusCode)
	}
	var m struct {
		Config struct {
			Size int64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size int64 `json:"size"`
		} `json:"layers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&m); err != nil {
		return 0, err
	}
	total := m.Config.Size
	for _, l := range m.Layers {
		total += l.Size
	}
	return total, nil
}
