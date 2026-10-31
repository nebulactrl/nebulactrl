// Package registryadmin is the control plane's side of the built-in image
// registry (CNCF Distribution, one instance for every team).
//
// Isolation is by repository prefix, "<team-id>/": the forwarder
// (internal/instsvc/forward) checks the prefix and signs, per request, a token
// for exactly the repository it checked. This package owns the signing key (a
// slice of instance_services.secrets_enc), the JWKS file the registry trusts,
// the weekly garbage-collection window and a team's purge.
package registryadmin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/registries"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

const (
	// instanceKind is the instance_services row of the registry.
	instanceKind = "registry"
	// tokenTTL is the life of a token signed for one forwarded request.
	tokenTTL = 5 * time.Minute
	// keyCacheTTL and stateCacheTTL bound how long the signing key and the
	// read-only flag are kept in memory.
	keyCacheTTL   = time.Minute
	stateCacheTTL = 2 * time.Second
)

// Config is what the Backend needs.
type Config struct {
	// URL is the registry on the internal network (http://registry:5000).
	URL string
	// JWKSPath is the file the control plane writes the public key to; the
	// registry container mounts the same directory read-only.
	JWKSPath string
	Pool     *pgxpool.Pool
	Box      *secretbox.Box
	// Dispatcher sends the garbage-collection command to the control-plane host's agent.
	Dispatcher agents.Dispatcher
	HTTP       *http.Client
	Logger     *slog.Logger
	Now        func() time.Time
}

// Backend implements instsvc.Backend for the registry kind, plus the forwarder's
// forward.RegistryAuth.
type Backend struct {
	cfg Config
	q   *db.Queries
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	keys    registries.ManagedSecrets
	keysAt  time.Time
	state   bool
	stateAt time.Time
	writes  func() int
	jobs    *jobs.Manager
}

var _ instsvc.Backend = (*Backend)(nil)

// New returns a Backend. Pool may be nil for OpenAPI generation.
func New(cfg Config) *Backend {
	b := &Backend{cfg: cfg, log: cfg.Logger, now: cfg.Now}
	if b.log == nil {
		b.log = slog.Default()
	}
	if b.now == nil {
		b.now = time.Now
	}
	if cfg.HTTP == nil {
		b.cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Pool != nil {
		b.q = db.New(cfg.Pool)
	}
	return b
}

// SetWriteGauge sets the function the garbage-collection window polls for the
// number of registry writes in flight (the forwarder's RegistryWrites).
func (b *Backend) SetWriteGauge(f func() int) {
	b.mu.Lock()
	b.writes = f
	b.mu.Unlock()
}

// Prepare makes the signing key (once) and writes the JWKS file the registry
// trusts. It runs before every health check, so a registry that started before
// the file existed finds it after the next minute.
func (b *Backend) Prepare(ctx context.Context) error {
	ms, ok, err := registries.LoadManagedSecrets(ctx, b.q, b.cfg.Box)
	if err != nil {
		return err
	}
	if !ok {
		if ms, err = registries.GenerateManagedSecrets(); err != nil {
			return err
		}
		enc, err := registries.SealManagedSecrets(b.cfg.Box, ms)
		if err != nil {
			return fmt.Errorf("seal registry key: %w", err)
		}
		if err := b.q.SetInstanceServiceSecrets(ctx, db.SetInstanceServiceSecretsParams{Kind: instanceKind, SecretsEnc: enc}); err != nil {
			return fmt.Errorf("store registry key: %w", err)
		}
	}
	jwks, err := ms.JWKS()
	if err != nil {
		return err
	}
	return writeIfChanged(b.cfg.JWKSPath, []byte(jwks))
}

// writeIfChanged writes content to path atomically unless it is already there.
func writeIfChanged(path string, content []byte) error {
	if path == "" {
		return errors.New("no JWKS path is configured (NEBULACTRL_REGISTRY_JWKS_PATH)")
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".jwks-*")
	if err != nil {
		return fmt.Errorf("write JWKS: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write JWKS: %w", err)
	}
	// The registry runs as another user and only reads the public key.
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Health checks that the registry answers, asks for a bearer token without one
// and accepts a token signed here: the key file and the issuer match.
func (b *Backend) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	get := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.URL+"/v2/", nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return b.cfg.HTTP.Do(req)
	}
	resp, err := get("")
	if err != nil {
		return fmt.Errorf("built-in registry: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("built-in registry: /v2/ answered %d without a token; token authentication is not enabled", resp.StatusCode)
	}
	tok, err := b.SignRepoToken(ctx)
	if err != nil {
		return fmt.Errorf("built-in registry: %w", err)
	}
	resp, err = get(tok)
	if err != nil {
		return fmt.Errorf("built-in registry: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("built-in registry refused a token signed by NebulaCtrl (status %d): its key file or issuer does not match", resp.StatusCode)
	}
	return nil
}

// secrets returns the signing key, cached for a minute.
func (b *Backend) secrets(ctx context.Context) (registries.ManagedSecrets, error) {
	b.mu.Lock()
	if b.keys.RegistryKeyPEM != "" && b.now().Sub(b.keysAt) < keyCacheTTL {
		ms := b.keys
		b.mu.Unlock()
		return ms, nil
	}
	b.mu.Unlock()
	ms, ok, err := registries.LoadManagedSecrets(ctx, b.q, b.cfg.Box)
	if err != nil {
		return ms, err
	}
	if !ok {
		return ms, errors.New("the built-in registry has no signing key yet")
	}
	b.mu.Lock()
	b.keys, b.keysAt = ms, b.now()
	b.mu.Unlock()
	return ms, nil
}

// SignRepoToken signs a registry token for exactly the given access, valid for
// five minutes. With no access it only authenticates (the /v2/ ping).
func (b *Backend) SignRepoToken(ctx context.Context, access ...registries.RepoAccess) (string, error) {
	ms, err := b.secrets(ctx)
	if err != nil {
		return "", err
	}
	return registries.SignRepoToken(ms, b.now(), tokenTTL, access...)
}

// ReadOnly reports whether the registry is in its garbage-collection window
// (cached for two seconds). When the state cannot be read it says true: a
// refused push is retried, a push during garbage collection can lose a blob.
func (b *Backend) ReadOnly(ctx context.Context) bool {
	b.mu.Lock()
	if !b.stateAt.IsZero() && b.now().Sub(b.stateAt) < stateCacheTTL {
		v := b.state
		b.mu.Unlock()
		return v
	}
	b.mu.Unlock()
	row, err := b.q.GetInstanceService(ctx, instanceKind)
	v := true
	if err == nil {
		v = registries.DecodeManagedState(row.Options).ReadOnly
	} else {
		b.log.Warn("registry read-only flag unreadable; refusing writes", "err", err)
	}
	b.mu.Lock()
	b.state, b.stateAt = v, b.now()
	b.mu.Unlock()
	return v
}

// invalidateState drops the cached read-only flag.
func (b *Backend) invalidateState() {
	b.mu.Lock()
	b.stateAt = time.Time{}
	b.mu.Unlock()
}

// EnsureSlice has nothing to create: a team's slice is a repository prefix.
func (b *Backend) EnsureSlice(context.Context, instsvc.Slice, int64) error { return nil }

// Usage is 0: images have no quota (they follow retention).
func (b *Backend) Usage(context.Context, instsvc.Slice) (int64, error) { return 0, nil }
