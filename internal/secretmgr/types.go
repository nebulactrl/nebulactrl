// Package secretmgr connects NebulaCtrl to external secret managers
// (HashiCorp Vault / OpenBao KV v2, Infisical, Doppler). A variable value may
// reference a secret as `${{vault:<integration>/<path>#<key>}}` or, for Doppler,
// `${{doppler:<integration>/<project>/<config>#<key>}}`. The reference is stored;
// the value is fetched when a deploy sends its command, held in memory for that
// operation and never persisted. Errors name the reference, never a value.
//
// An integration is available to the whole team or only to selected projects and
// environments (see ScopeSelected); the fetch path refuses an out-of-scope
// reference before any request leaves, and every operation that reads external
// secrets leaves one `secret.fetch` audit event (references and service, never a
// value).
package secretmgr

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// SecretManager kinds.
const (
	KindVault     = "vault"
	KindInfisical = "infisical"
	KindDoppler   = "doppler"
)

// Integration scopes: who may reference an integration.
const (
	// ScopeTeam makes the integration available to every project of the team.
	ScopeTeam = "team"
	// ScopeSelected limits it to the projects and environments in its grants.
	ScopeSelected = "selected"
)

// Vault authentication methods.
const (
	AuthToken   = "token"
	AuthAppRole = "approle"
)

// DefaultInfisicalURL is Infisical Cloud (US); EU and self-hosted instances set siteUrl.
const DefaultInfisicalURL = "https://us.infisical.com"

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	mountRe    = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	nsRe       = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	identRe    = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,256}$`)
	projectRe  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	orgSlugRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	secretSize = 4096
	// dopplerSegRe is a Doppler project or config name.
	dopplerSegRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// SecretManagerConfig is the non-secret connection setting of an integration. Fields that do
// not belong to the integration's kind stay empty.
type SecretManagerConfig struct {
	// Vault / OpenBao.
	Address      string `json:"address,omitempty" doc:"Vault or OpenBao address, e.g. https://vault.example.com:8200"`
	Namespace    string `json:"namespace,omitempty" doc:"Vault Enterprise / OpenBao namespace (X-Vault-Namespace)"`
	Mount        string `json:"mount,omitempty" doc:"KV v2 mount path (default secret)"`
	AuthMethod   string `json:"authMethod,omitempty" enum:"token,approle" doc:"token (default) or approle"`
	RoleID       string `json:"roleId,omitempty" doc:"AppRole role id"`
	AppRoleMount string `json:"approleMount,omitempty" doc:"AppRole auth mount (default approle)"`
	CACert       string `json:"caCert,omitempty" doc:"PEM certificate(s) to trust for a private CA (public material)"`
	// Infisical.
	SiteURL          string `json:"siteUrl,omitempty" doc:"Infisical URL (default https://us.infisical.com) or Doppler API URL (default https://api.doppler.com)"`
	ClientID         string `json:"clientId,omitempty" doc:"Machine identity client id (universal auth)"`
	ProjectID        string `json:"projectId,omitempty" doc:"Infisical project id"`
	OrganizationSlug string `json:"organizationSlug,omitempty" doc:"Optional organization slug to scope the login to"`
	// Doppler.
	DefaultProject string `json:"defaultProject,omitempty" doc:"Doppler project used by references that name none, like doppler:<name>#KEY. Set with defaultConfig, or leave both empty (a service token is bound to one config)"`
	DefaultConfig  string `json:"defaultConfig,omitempty" doc:"Doppler config used by references that name none (with defaultProject)"`
}

// SecretManagerLastTest is the stored result of the last connection test.
type SecretManagerLastTest struct {
	At    *time.Time `json:"at"`
	OK    *bool      `json:"ok"`
	Error string     `json:"error,omitempty"`
}

// ScopeGrant allows a project, or one environment of it, to use an integration.
type ScopeGrant struct {
	ProjectID       uuid.UUID  `json:"projectId"`
	EnvironmentID   *uuid.UUID `json:"environmentId,omitempty" doc:"Omit to allow every environment of the project, including ones created later"`
	ProjectName     string     `json:"projectName,omitempty" readOnly:"true"`
	EnvironmentName string     `json:"environmentName,omitempty" readOnly:"true"`
}

// SecretManager is the API view. The credential is never part of it.
type SecretManager struct {
	ID        uuid.UUID           `json:"id"`
	Name      string              `json:"name" doc:"Used in references: ${{vault:<name>/<path>#<key>}} or ${{doppler:<name>/<project>/<config>#<key>}}"`
	Kind      string              `json:"kind" enum:"vault,infisical,doppler"`
	Config    SecretManagerConfig `json:"config"`
	SecretSet bool                `json:"secretSet"`
	// Scope is team (every project may reference it) or selected (only Grants may).
	Scope     string                `json:"scope" enum:"team,selected"`
	Grants    []ScopeGrant          `json:"grants" doc:"With scope selected: the projects and environments that may reference the integration"`
	LastTest  SecretManagerLastTest `json:"lastTest"`
	Example   string                `json:"example" doc:"A reference in this integration's syntax"`
	CreatedAt time.Time             `json:"createdAt"`
	UpdatedAt time.Time             `json:"updatedAt"`
}

func view(r db.SecretIntegration, grants []ScopeGrant) SecretManager {
	var cfg SecretManagerConfig
	_ = json.Unmarshal(r.Config, &cfg)
	if grants == nil || r.Scope == ScopeTeam {
		grants = []ScopeGrant{}
	}
	return SecretManager{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Config: cfg, SecretSet: len(r.CredentialsEnc) > 0,
		Scope: r.Scope, Grants: grants,
		LastTest: SecretManagerLastTest{At: r.LastTestAt, OK: r.LastTestOk, Error: r.LastTestError},
		Example:  example(r.Name, r.Kind, cfg), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func example(name, kind string, cfg SecretManagerConfig) string {
	switch kind {
	case KindInfisical:
		return "${{vault:" + name + "/prod/backend#DATABASE_URL}}"
	case KindDoppler:
		if cfg.DefaultProject != "" {
			return "${{doppler:" + name + "#DATABASE_URL}}"
		}
		return "${{doppler:" + name + "/backend/prd#DATABASE_URL}}"
	}
	return "${{vault:" + name + "/apps/shop/db#password}}"
}

func unprocessable(code, msg string) error { return apierr.Unprocessable(code, msg) }

// validURL checks an http(s) base URL and returns it without a trailing slash.
func validURL(field, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", unprocessable("invalid_address", field+" must be a full http(s) URL, like https://vault.example.com:8200")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", unprocessable("invalid_address", field+" must not contain credentials, a query or a fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// normalize validates cfg for kind and fills defaults.
func normalize(kind string, cfg SecretManagerConfig) (SecretManagerConfig, error) {
	var out SecretManagerConfig
	switch kind {
	case KindVault:
		addr, err := validURL("address", cfg.Address)
		if err != nil {
			return out, err
		}
		out.Address = addr
		out.Mount = strings.Trim(strings.TrimSpace(cfg.Mount), "/")
		if out.Mount == "" {
			out.Mount = "secret"
		}
		if !mountRe.MatchString(out.Mount) || strings.Contains(out.Mount, "..") {
			return out, unprocessable("invalid_mount", "mount is the path of a KV v2 secrets engine, like secret or kv/team")
		}
		out.Namespace = strings.Trim(strings.TrimSpace(cfg.Namespace), "/")
		if out.Namespace != "" && (!nsRe.MatchString(out.Namespace) || strings.Contains(out.Namespace, "..")) {
			return out, unprocessable("invalid_namespace", "namespace has letters, digits and . _ - / only")
		}
		out.AuthMethod = cfg.AuthMethod
		if out.AuthMethod == "" {
			out.AuthMethod = AuthToken
		}
		switch out.AuthMethod {
		case AuthToken:
		case AuthAppRole:
			out.RoleID = strings.TrimSpace(cfg.RoleID)
			if !identRe.MatchString(out.RoleID) {
				return out, unprocessable("invalid_role_id", "roleId is required for AppRole")
			}
			out.AppRoleMount = strings.Trim(strings.TrimSpace(cfg.AppRoleMount), "/")
			if out.AppRoleMount == "" {
				out.AppRoleMount = "approle"
			}
			if !mountRe.MatchString(out.AppRoleMount) || strings.Contains(out.AppRoleMount, "..") {
				return out, unprocessable("invalid_mount", "approleMount is the path of the AppRole auth method, like approle")
			}
		default:
			return out, unprocessable("invalid_auth_method", "authMethod must be token or approle")
		}
		if pem := strings.TrimSpace(cfg.CACert); pem != "" {
			if len(pem) > 64*1024 {
				return out, unprocessable("invalid_ca_cert", "caCert is too large")
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(pem)) {
				return out, unprocessable("invalid_ca_cert", "caCert must contain at least one PEM certificate")
			}
			out.CACert = pem
		}
	case KindInfisical:
		site := strings.TrimSpace(cfg.SiteURL)
		if site == "" {
			site = DefaultInfisicalURL
		}
		var err error
		if out.SiteURL, err = validURL("siteUrl", site); err != nil {
			return out, err
		}
		out.ClientID = strings.TrimSpace(cfg.ClientID)
		if !identRe.MatchString(out.ClientID) {
			return out, unprocessable("invalid_client_id", "clientId (the machine identity's client id) is required")
		}
		out.ProjectID = strings.TrimSpace(cfg.ProjectID)
		if !projectRe.MatchString(out.ProjectID) {
			return out, unprocessable("invalid_project_id", "projectId (the Infisical project id) is required")
		}
		if o := strings.TrimSpace(cfg.OrganizationSlug); o != "" {
			if !orgSlugRe.MatchString(o) {
				return out, unprocessable("invalid_organization", "organizationSlug has letters, digits and . _ - only")
			}
			out.OrganizationSlug = o
		}
	case KindDoppler:
		site := strings.TrimSpace(cfg.SiteURL)
		if site == "" {
			site = DefaultDopplerURL
		}
		var err error
		if out.SiteURL, err = validURL("siteUrl", site); err != nil {
			return out, err
		}
		project, config := strings.TrimSpace(cfg.DefaultProject), strings.TrimSpace(cfg.DefaultConfig)
		if (project == "") != (config == "") {
			return out, unprocessable("invalid_default", "set both defaultProject and defaultConfig, or neither (a service token is bound to one config)")
		}
		if project != "" {
			for _, v := range []string{project, config} {
				if !dopplerSegRe.MatchString(v) || v == "." || v == ".." {
					return out, unprocessable("invalid_default", "defaultProject and defaultConfig are 1-64 letters, digits and . _ -")
				}
			}
			out.DefaultProject, out.DefaultConfig = project, config
		}
	default:
		return out, unprocessable("invalid_kind", "kind must be vault, infisical or doppler")
	}
	return out, nil
}

// identityChanged reports whether cfg points the stored credential at another
// server or identity: the secret must then be provided again.
func identityChanged(kind string, old, cur SecretManagerConfig) bool {
	switch kind {
	case KindVault:
		return old.Address != cur.Address || old.AuthMethod != cur.AuthMethod || old.RoleID != cur.RoleID ||
			old.AppRoleMount != cur.AppRoleMount || old.Namespace != cur.Namespace
	case KindInfisical:
		return old.SiteURL != cur.SiteURL || old.ClientID != cur.ClientID
	case KindDoppler:
		return old.SiteURL != cur.SiteURL
	}
	return false
}

func checkSecret(secret string) error {
	if len(secret) > secretSize {
		return unprocessable("invalid_secret", "the credential is too long")
	}
	if strings.ContainsAny(secret, "\r\n") {
		return unprocessable("invalid_secret", "the credential must be a single line")
	}
	return nil
}

func refError(ref fmt.Stringer, msg string) *apierr.Error {
	return apierr.Unprocessable("external_secret_failed", fmt.Sprintf("reference ${{%s}}: %s", ref, msg))
}
