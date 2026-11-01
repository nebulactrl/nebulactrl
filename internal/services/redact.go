package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Mask replaces redacted secret values in logs.
const Mask = "••••••"

// MinRedactLen is the shortest secret value that is redacted; shorter values
// would mask ordinary text.
const MinRedactLen = 6

const redactorTTL = 5 * time.Second

type cachedRedactor struct {
	fn      func(string) string
	expires time.Time
	// extGen is the generation of the external value registry the redactor was
	// built with: newly fetched values invalidate it at once.
	extGen uint64
}

type redactorCache struct {
	mu sync.Mutex
	m  map[uuid.UUID]cachedRedactor
}

// encodedForms returns the encodings under which a secret commonly shows up in
// logs: URL-escaped (query and path) and base64 (std, URL-safe, padded or not).
func encodedForms(v string) []string {
	b := []byte(v)
	return []string{
		url.QueryEscape(v), url.PathEscape(v),
		base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
	}
}

// MakeRedactor returns a function masking every value of at least
// MinRedactLen characters, together with its URL-escaped and base64 forms.
func MakeRedactor(values []string) func(string) string {
	seen := map[string]struct{}{}
	var vs []string
	add := func(v string) {
		if len(v) < MinRedactLen {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		vs = append(vs, v)
	}
	for _, v := range values {
		add(v)
		if len(v) >= MinRedactLen {
			for _, e := range encodedForms(v) {
				add(e)
			}
		}
	}
	// Longest first so a secret that contains another is masked whole.
	sort.Slice(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
	return func(line string) string {
		for _, v := range vs {
			if strings.Contains(line, v) {
				line = strings.ReplaceAll(line, v, Mask)
			}
		}
		return line
	}
}

// RedactorFor implements agents.Redactor: it masks the service's secret
// variables (own and resolved from references, current and from recent
// releases), git token and registry password.
func (s *Service) RedactorFor(ctx context.Context, serviceID uuid.UUID) (func(string) string, error) {
	s.redactors.mu.Lock()
	if c, ok := s.redactors.m[serviceID]; ok && time.Now().Before(c.expires) && c.extGen == s.ext.generation() {
		s.redactors.mu.Unlock()
		return c.fn, nil
	}
	s.redactors.mu.Unlock()

	gen := s.ext.generation()
	fn, err := s.buildRedactor(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	s.redactors.mu.Lock()
	if s.redactors.m == nil {
		s.redactors.m = map[uuid.UUID]cachedRedactor{}
	}
	if len(s.redactors.m) > 1024 {
		clear(s.redactors.m)
	}
	s.redactors.m[serviceID] = cachedRedactor{fn: fn, expires: time.Now().Add(redactorTTL), extGen: gen}
	s.redactors.mu.Unlock()
	return fn, nil
}

func (s *Service) buildRedactor(ctx context.Context, serviceID uuid.UUID) (func(string) string, error) {
	sc, err := LoadScopeInternal(ctx, s.q, serviceID)
	if err != nil {
		return func(l string) string { return l }, nil //nolint:nilerr // deleted service: nothing left to protect
	}
	values, err := s.SecretValues(ctx, sc)
	if err != nil {
		return nil, err
	}
	return MakeRedactor(values), nil
}

// SecretValues lists every secret value tied to a service.
func (s *Service) SecretValues(ctx context.Context, sc Scope) ([]string, error) {
	var out []string
	vars, err := s.q.ListServiceVariables(ctx, sc.Service.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range vars {
		if !v.IsSecret {
			continue
		}
		if val, err := s.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(sc.Service.ID.String(), v.Key), v.ValueEnc); err == nil {
			out = append(out, val)
		}
	}
	if len(sc.Service.GitTokenEnc) > 0 {
		if val, err := s.box.OpenString(secretbox.ServicesGitToken, sc.Service.ID.String(), sc.Service.GitTokenEnc); err == nil {
			out = append(out, val)
		}
	}
	if k, err := s.q.GetServiceSSHKey(ctx, sc.Service.ID); err == nil {
		if pem, err := s.box.Open(secretbox.ServiceSSHKeysPrivateKey, sc.Service.ID.String(), k.PrivateKeyEnc); err == nil {
			out = append(out, string(pem))
			for _, line := range strings.Split(string(pem), "\n") {
				if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "-----") {
					out = append(out, line)
				}
			}
		}
	}
	if len(sc.Service.RegistryAuthEnc) > 0 {
		if raw, err := s.box.Open(secretbox.ServicesRegistryAuth, sc.Service.ID.String(), sc.Service.RegistryAuthEnc); err == nil {
			var ra RegistryAuth
			if json.Unmarshal(raw, &ra) == nil {
				out = append(out, ra.Password)
			}
		}
	}
	if res, err := LoadResolver(ctx, s.q, s.box, sc.EnvInfo()); err == nil {
		// Lenient: one broken reference must not disable masking of the rest.
		if r, _, err := res.ResolveServiceLenient(sc.Service.Slug); err == nil {
			for k := range r.Secret {
				out = append(out, r.Vars[k])
			}
			out = append(out, r.Leaves...)
		}
	}
	ext := s.ext.values(sc.Service.ID)
	out = append(out, ext...)
	needExt := len(ext) == 0
	rels, err := s.q.ListRecentServiceReleases(ctx, db.ListRecentServiceReleasesParams{ServiceID: sc.Service.ID, Limit: 3})
	if err == nil {
		for _, r := range rels {
			if len(r.EnvEnc) == 0 {
				continue
			}
			raw, err := s.box.Open(secretbox.ReleasesEnv, r.ID.String(), r.EnvEnc)
			if err != nil {
				continue
			}
			var env EnvSnapshot
			if json.Unmarshal(raw, &env) != nil {
				continue
			}
			for _, k := range env.Secret {
				out = append(out, env.Vars[k])
			}
			out = append(out, env.Leaves...)
			if needExt && len(env.Ext) > 0 {
				out = append(out, s.refetchExternal(ctx, sc, env)...)
				needExt = false
			}
		}
	}
	return out, nil
}

// refetchExternal fetches the external values of a release for masking when
// the in-memory registry has none (after a control-plane restart). It runs at
// most once a minute per service, with a short deadline; a failure only means
// those values are not masked until the next deploy fetches them.
func (s *Service) refetchExternal(ctx context.Context, sc Scope, env EnvSnapshot) []string {
	f := s.ext.Fetcher(sc.ExtScope(ExtPurposeMask))
	if f == nil || !s.ext.shouldRetry(sc.Service.ID, time.Now()) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, leaves, err := env.Materialize(ctx, f)
	if err != nil {
		return nil
	}
	s.ext.Remember(sc.Service.ID, leaves)
	return leaves
}
