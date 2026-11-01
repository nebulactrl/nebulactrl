package engine

import (
	"encoding/base64"
	"net/url"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	redactMark   = "••••••"
	redactMinLen = 6
)

// redactor masks the secret values delivered with a command so they never
// reach deploy logs, events or result messages (build output and container
// logs may echo them). Mirrors the control plane's rule: values shorter than
// 6 characters are not masked. Only secret variables (secretEnvKeys) and
// non-variable secrets (git token, registry auth) are masked; plain and
// platform variables such as NEBULA_SERVICE_NAME stay readable.
type redactor struct{ values []string }

// secretEnvKeys are variable keys ("API_KEY") or, for build secrets, command
// keys with the "build:" prefix. Plain build args are not masked. Each masked
// value is also masked in its URL-escaped and base64 forms.
func newRedactor(secrets map[string]string, secretEnvKeys []string) *redactor {
	secretEnv := make(map[string]bool, len(secretEnvKeys))
	for _, k := range secretEnvKeys {
		if strings.HasPrefix(k, agentprotocol.SecretBuildPrefix) {
			secretEnv[k] = true
			continue
		}
		secretEnv[agentprotocol.SecretEnvPrefix+k] = true
	}
	r := &redactor{}
	seen := map[string]bool{}
	add := func(v string) {
		if len(v) >= redactMinLen && !seen[v] {
			seen[v] = true
			r.values = append(r.values, v)
		}
	}
	for k, v := range secrets {
		if (strings.HasPrefix(k, agentprotocol.SecretEnvPrefix) || strings.HasPrefix(k, agentprotocol.SecretBuildPrefix)) && !secretEnv[k] {
			continue
		}
		add(v)
		// auth:<id> holds "user:hash" lines and cert:<id> a PEM key (so is git_ssh_key): mask every
		// line (and each hash) so partial echoes are caught too.
		if strings.HasPrefix(k, agentprotocol.SecretAuthPrefix) || strings.HasPrefix(k, agentprotocol.SecretCertPrefix) || k == agentprotocol.SecretGitSSHKey {
			for _, line := range strings.Split(v, "\n") {
				if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "-----") {
					add(line)
					if _, hash, ok := strings.Cut(line, ":"); ok {
						add(hash)
					}
				}
			}
		}
		if len(v) >= redactMinLen {
			add(url.QueryEscape(v))
			add(url.PathEscape(v))
			b := []byte(v)
			add(base64.StdEncoding.EncodeToString(b))
			add(base64.RawStdEncoding.EncodeToString(b))
			add(base64.URLEncoding.EncodeToString(b))
			add(base64.RawURLEncoding.EncodeToString(b))
		}
	}
	// Longest first so a secret containing another is fully masked.
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

func (r *redactor) apply(s string) string {
	for _, v := range r.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, redactMark)
		}
	}
	return s
}
