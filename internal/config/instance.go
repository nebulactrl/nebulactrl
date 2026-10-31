package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Instance configures the instance services (internal/instsvc): S3 (backups),
// the image registry and Loki, which run beside the control plane on an internal
// network and are reached by the control plane alone. The installer sets them;
// all empty (a development run without the compose services) leaves the
// built-in services off.
type Instance struct {
	// S3URL, RegistryURL and LokiURL are NEBULACTRL_INSTANCE_{S3,REGISTRY,LOKI}_URL
	// (http://seaweedfs:8333, http://registry:5000, http://loki:3100).
	S3URL, RegistryURL, LokiURL string
	// S3AdminKey and S3AdminSecret are NEBULACTRL_S3_ADMIN_KEY and
	// NEBULACTRL_S3_ADMIN_SECRET (each also as _FILE): the identity that creates
	// the per-team users and buckets.
	S3AdminKey, S3AdminSecret string
	// RegistryJWKSPath is NEBULACTRL_REGISTRY_JWKS_PATH: where the control plane
	// writes the key set the registry verifies its tokens with.
	RegistryJWKSPath string
}

func loadInstance(getenv func(string) string) (Instance, error) {
	i := Instance{
		S3URL:            strings.TrimRight(strings.TrimSpace(getenv("NEBULACTRL_INSTANCE_S3_URL")), "/"),
		RegistryURL:      strings.TrimRight(strings.TrimSpace(getenv("NEBULACTRL_INSTANCE_REGISTRY_URL")), "/"),
		LokiURL:          strings.TrimRight(strings.TrimSpace(getenv("NEBULACTRL_INSTANCE_LOKI_URL")), "/"),
		RegistryJWKSPath: strings.TrimSpace(getenv("NEBULACTRL_REGISTRY_JWKS_PATH")),
	}
	var errs []string
	for _, u := range []struct{ name, v string }{
		{"NEBULACTRL_INSTANCE_S3_URL", i.S3URL}, {"NEBULACTRL_INSTANCE_REGISTRY_URL", i.RegistryURL}, {"NEBULACTRL_INSTANCE_LOKI_URL", i.LokiURL},
	} {
		if u.v == "" {
			continue
		}
		if p, err := url.Parse(u.v); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			errs = append(errs, u.name+" must be an http(s) URL")
		}
	}
	var err error
	if i.S3AdminKey, _, err = secretValue(getenv, "NEBULACTRL_S3_ADMIN_KEY"); err != nil {
		errs = append(errs, err.Error())
	}
	if i.S3AdminSecret, _, err = secretValue(getenv, "NEBULACTRL_S3_ADMIN_SECRET"); err != nil {
		errs = append(errs, err.Error())
	}
	if i.S3URL != "" && (i.S3AdminKey == "" || i.S3AdminSecret == "") {
		errs = append(errs, "NEBULACTRL_INSTANCE_S3_URL needs NEBULACTRL_S3_ADMIN_KEY and NEBULACTRL_S3_ADMIN_SECRET")
	}
	if len(errs) > 0 {
		return i, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return i, nil
}
