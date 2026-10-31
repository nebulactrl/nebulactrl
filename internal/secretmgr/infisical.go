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

// infisicalProvider reads secrets with a machine identity (universal auth).
// A reference's path is `<environment>[/<folder>…]`: the Infisical environment
// slug, then the secret's folder ("/" when absent).
type infisicalProvider struct {
	cfg    SecretManagerConfig
	secret string // the client secret
	hc     *http.Client

	token string
	vals  map[string]string // environment/folder/key → value, per session
}

func (p *infisicalProvider) login(ctx context.Context) error {
	if p.token != "" {
		return nil
	}
	body := map[string]string{"clientId": p.cfg.ClientID, "clientSecret": p.secret}
	if p.cfg.OrganizationSlug != "" {
		body["organizationSlug"] = p.cfg.OrganizationSlug
	}
	status, resp, err := call(ctx, p.hc, http.MethodPost, p.cfg.SiteURL+"/api/v1/auth/universal-auth/login", nil, body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("machine identity login failed: %s", statusMessage(status, resp))
	}
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(resp, &out); err != nil || out.AccessToken == "" {
		return fmt.Errorf("machine identity login returned no access token")
	}
	p.token = out.AccessToken
	return nil
}

func (p *infisicalProvider) check(ctx context.Context) error { return p.login(ctx) }

func (p *infisicalProvider) fetch(ctx context.Context, ref varexpr.ExtRef) (string, error) {
	env, folder, _ := strings.Cut(ref.Path, "/")
	folder = "/" + folder
	id := env + "\x00" + folder + "\x00" + ref.Key
	if v, ok := p.vals[id]; ok {
		return v, nil
	}
	if err := p.login(ctx); err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("projectId", p.cfg.ProjectID)
	q.Set("environment", env)
	q.Set("secretPath", folder)
	q.Set("type", "shared")
	q.Set("expandSecretReferences", "true")
	q.Set("includeImports", "true")
	u := p.cfg.SiteURL + "/api/v4/secrets/" + url.PathEscape(ref.Key) + "?" + q.Encode()
	status, body, err := call(ctx, p.hc, http.MethodGet, u, map[string]string{"Authorization": "Bearer " + p.token}, nil)
	if err != nil {
		return "", err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", fmt.Errorf("no secret %q in environment %q at folder %s (or the identity cannot see it)", ref.Key, env, folder)
	default:
		return "", fmt.Errorf("%s", statusMessage(status, body))
	}
	var out struct {
		Secret struct {
			SecretValue       string `json:"secretValue"`
			SecretValueHidden bool   `json:"secretValueHidden"`
		} `json:"secret"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("the response is not an Infisical secret")
	}
	if out.Secret.SecretValueHidden {
		return "", fmt.Errorf("the value of %q is hidden from this identity (grant it the read value permission)", ref.Key)
	}
	if p.vals == nil {
		p.vals = map[string]string{}
	}
	p.vals[id] = out.Secret.SecretValue
	return out.Secret.SecretValue, nil
}
