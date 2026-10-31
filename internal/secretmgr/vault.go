package secretmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// vaultProvider reads KV v2 secrets from HashiCorp Vault or OpenBao (the API is
// the same) with a token or an AppRole login.
type vaultProvider struct {
	cfg    SecretManagerConfig
	secret string // the token, or the AppRole secret id
	hc     *http.Client

	token string
	data  map[string]map[string]any // KV path → fields, per session
}

func (v *vaultProvider) headers() map[string]string {
	return map[string]string{
		"X-Vault-Token":     v.token,
		"X-Vault-Namespace": v.cfg.Namespace,
		"X-Vault-Request":   "true",
	}
}

// login obtains the client token once per session.
func (v *vaultProvider) login(ctx context.Context) error {
	if v.token != "" {
		return nil
	}
	if v.cfg.AuthMethod != AuthAppRole {
		v.token = v.secret
		return nil
	}
	status, body, err := call(ctx, v.hc, http.MethodPost, v.cfg.Address+"/v1/auth/"+escapePath(v.cfg.AppRoleMount)+"/login",
		map[string]string{"X-Vault-Namespace": v.cfg.Namespace, "X-Vault-Request": "true"},
		map[string]string{"role_id": v.cfg.RoleID, "secret_id": v.secret})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("AppRole login failed: %s", statusMessage(status, body))
	}
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Auth.ClientToken == "" {
		return fmt.Errorf("AppRole login returned no token")
	}
	v.token = out.Auth.ClientToken
	return nil
}

func (v *vaultProvider) check(ctx context.Context) error {
	if err := v.login(ctx); err != nil {
		return err
	}
	status, body, err := call(ctx, v.hc, http.MethodGet, v.cfg.Address+"/v1/auth/token/lookup-self", v.headers(), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("the token was not accepted: %s", statusMessage(status, body))
	}
	return nil
}

func (v *vaultProvider) fetch(ctx context.Context, ref varexpr.ExtRef) (string, error) {
	fields, ok := v.data[ref.Path]
	if !ok {
		if err := v.login(ctx); err != nil {
			return "", err
		}
		u := v.cfg.Address + "/v1/" + escapePath(v.cfg.Mount) + "/data/" + escapePath(ref.Path)
		status, body, err := call(ctx, v.hc, http.MethodGet, u, v.headers(), nil)
		if err != nil {
			return "", err
		}
		switch status {
		case http.StatusOK:
		case http.StatusNotFound:
			return "", fmt.Errorf("no secret at %s in mount %q (or the token cannot see it)", ref.Path, v.cfg.Mount)
		default:
			return "", fmt.Errorf("%s", statusMessage(status, body))
		}
		var out struct {
			Data struct {
				Data map[string]any `json:"data"`
			} `json:"data"`
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil {
			return "", fmt.Errorf("the response is not a KV v2 secret (is %q a KV v2 mount?)", v.cfg.Mount)
		}
		if out.Data.Data == nil {
			return "", fmt.Errorf("the secret at %s was deleted or destroyed", ref.Path)
		}
		fields = out.Data.Data
		if v.data == nil {
			v.data = map[string]map[string]any{}
		}
		v.data[ref.Path] = fields
	}
	raw, ok := fields[ref.Key]
	if !ok {
		return "", fmt.Errorf("the secret at %s has no field %q", ref.Path, ref.Key)
	}
	switch x := raw.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	return "", fmt.Errorf("field %q of %s is not a string, number or boolean", ref.Key, ref.Path)
}
