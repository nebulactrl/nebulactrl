package secretmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// DefaultDopplerURL is the Doppler API.
const DefaultDopplerURL = "https://api.doppler.com"

// dopplerProvider reads secrets with a Doppler token over the REST API v3
// (https://docs.doppler.com/reference/api): `Authorization: Bearer <token>`.
//
// A reference is `doppler:<integration>/<project>/<config>#<NAME>`, or
// `doppler:<integration>#<NAME>` to use the integration's default project and
// config. A service token is bound to one config and needs neither; personal,
// service-account and service-account-identity tokens need both.
//
// One call to `GET /v3/configs/config/secrets/download?format=json` per
// (project, config) per session returns the config's computed secrets
// (https://docs.doppler.com/reference/secrets-download); dynamic secrets are
// never requested, so no lease is issued by accident.
type dopplerProvider struct {
	cfg    SecretManagerConfig
	secret string // the Doppler token
	hc     *http.Client

	// vals holds each downloaded config's secrets, keyed by "project/config"
	// ("" for the config a service token is bound to), for this session only.
	vals map[string]map[string]string
}

func (p *dopplerProvider) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.secret}
}

// check validates the token without reading a secret: `GET /v3/me` describes the
// token (https://docs.doppler.com/reference/auth-me).
func (p *dopplerProvider) check(ctx context.Context) error {
	status, body, err := call(ctx, p.hc, http.MethodGet, p.cfg.SiteURL+"/v3/me", p.headers(), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("token check failed: %s", statusMessage(status, body))
	}
	return nil
}

// scopeOf returns the project and config a reference reads: the reference's own,
// else the integration's defaults, else none (a service token is bound to one).
func (p *dopplerProvider) scopeOf(ref varexpr.ExtRef) (project, config string) {
	if ref.Path != "" {
		project, config, _ = strings.Cut(ref.Path, "/")
		return project, config
	}
	return p.cfg.DefaultProject, p.cfg.DefaultConfig
}

func (p *dopplerProvider) fetch(ctx context.Context, ref varexpr.ExtRef) (string, error) {
	project, config := p.scopeOf(ref)
	id := project + "/" + config
	if project == "" && config == "" {
		id = ""
	}
	secrets, ok := p.vals[id]
	if !ok {
		var err error
		if secrets, err = p.download(ctx, project, config); err != nil {
			return "", err
		}
		if p.vals == nil {
			p.vals = map[string]map[string]string{}
		}
		p.vals[id] = secrets
	}
	v, ok := secrets[ref.Key]
	if !ok {
		return "", fmt.Errorf("no secret %q in %s (or the token cannot see it)", ref.Key, describeScope(project, config))
	}
	return v, nil
}

func describeScope(project, config string) string {
	if project == "" && config == "" {
		return "the config the service token is bound to"
	}
	return fmt.Sprintf("Doppler project %q, config %q", project, config)
}

// download reads every computed secret of one config.
func (p *dopplerProvider) download(ctx context.Context, project, config string) (map[string]string, error) {
	q := url.Values{}
	q.Set("format", "json")
	q.Set("include_dynamic_secrets", "false")
	if project != "" {
		q.Set("project", project)
		q.Set("config", config)
	}
	u := p.cfg.SiteURL + "/v3/configs/config/secrets/download?" + q.Encode()
	status, body, err := call(ctx, p.hc, http.MethodGet, u, p.headers(), nil)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s was not found (or the token cannot see it)", describeScope(project, config))
	default:
		msg := statusMessage(status, body)
		if project == "" && status >= 400 && status < 500 && status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusTooManyRequests {
			msg += "; only a service token is bound to a config: otherwise name the project and config in the reference (doppler:<integration>/<project>/<config>#<NAME>) or set a default on the integration"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("the response is not a Doppler secrets download")
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		var s string
		if string(v) == "null" || json.Unmarshal(v, &s) != nil {
			// A secret that is not text (a null, a number) is skipped: asking
			// for it by name reports "no secret".
			continue
		}
		out[k] = s
	}
	return out, nil
}
