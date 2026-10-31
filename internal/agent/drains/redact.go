package drains

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	redactMark   = "\u2022\u2022\u2022\u2022\u2022\u2022"
	redactMinLen = 6
)

// makeRedactor masks every value of at least redactMinLen characters, and its
// URL-escaped and base64 forms, in a line (longest first, so a secret that
// contains another is masked whole). It mirrors the control plane's rule.
func makeRedactor(values []string) func(string) string {
	seen := map[string]bool{}
	var vs []string
	add := func(v string) {
		if len(v) >= redactMinLen && !seen[v] {
			seen[v] = true
			vs = append(vs, v)
		}
	}
	for _, v := range values {
		if len(v) < redactMinLen {
			continue
		}
		add(v)
		b := []byte(v)
		for _, e := range []string{
			url.QueryEscape(v), url.PathEscape(v),
			base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
			base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
		} {
			add(e)
		}
	}
	if len(vs) == 0 {
		return nil
	}
	sort.Slice(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
	return func(line string) string {
		for _, v := range vs {
			if strings.Contains(line, v) {
				line = strings.ReplaceAll(line, v, redactMark)
			}
		}
		return line
	}
}

// redactors builds the per-service redactors from Reconcile.Secrets.
func redactors(secrets map[string]string, services []agentprotocol.LogServiceMeta) map[string]func(string) string {
	out := map[string]func(string) string{}
	for _, svc := range services {
		raw, ok := secrets[agentprotocol.LogRedactSecretKey(svc.ServiceID)]
		if !ok {
			continue
		}
		var values []string
		if json.Unmarshal([]byte(raw), &values) != nil {
			continue
		}
		if fn := makeRedactor(values); fn != nil {
			out[svc.ServiceID] = fn
		}
	}
	return out
}
