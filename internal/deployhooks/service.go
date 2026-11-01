// Package deployhooks implements the per-service deploy hook: an unauthenticated
// POST /hooks/deploy/<serviceId>/<secret> that deploys the service exactly like
// a manual deploy (trigger deploy_hook), for CI pipelines and container
// registries. The secret is shown once and only its SHA-256 is stored.
package deployhooks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

const (
	// SecretPrefix starts every hook secret (secret scanners can match it).
	SecretPrefix = "ndh_"
	// secretBytes is the entropy of a secret (256 bits).
	secretBytes = 32
	// Rate limit of one hook: 6 calls a minute, a burst of 3.
	rateBurst = 3
	rateEvery = 10 * time.Second
)

// Deployer starts deployments on behalf of the system (the deployments service).
type Deployer interface {
	DeployWith(ctx context.Context, serviceID uuid.UUID, trigger string, opts deployments.InternalOptions) (deployments.DeploymentView, error)
}

// Deps are the collaborators of a Service. Jobs may be nil (no periodic sweep).
type Deps struct {
	Pool     *pgxpool.Pool
	Jobs     *jobs.Manager
	Config   *config.Config
	Deployer Deployer
	Logger   *slog.Logger
}

// Service implements deploy hooks.
type Service struct {
	Deps
	q *db.Queries

	mu       sync.Mutex
	limiters map[uuid.UUID]*limiterEntry
	lastGC   time.Time
	// every and burst are the token bucket of one hook (tests change them).
	every time.Duration
	burst int
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{Deps: d, q: db.New(d.Pool), limiters: map[uuid.UUID]*limiterEntry{}, every: rateEvery, burst: rateBurst}
}

// allow takes one call from the hook's token bucket.
func (s *Service) allow(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.lastGC) > 5*time.Minute {
		for k, e := range s.limiters {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(s.limiters, k)
			}
		}
		s.lastGC = now
	}
	e, ok := s.limiters[id]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(s.every), s.burst)}
		s.limiters[id] = e
	}
	e.seen = now
	return e.lim.Allow()
}

func hashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

func newSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate hook secret: %w", err)
	}
	return SecretPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// secretMatches compares a presented secret with a stored hash in constant
// time. A missing hash costs the same comparison and never matches.
func secretMatches(secret string, stored []byte) bool {
	got := hashSecret(secret)
	if len(stored) != sha256.Size {
		_ = subtle.ConstantTimeCompare(got, make([]byte, sha256.Size))
		return false
	}
	return subtle.ConstantTimeCompare(got, stored) == 1
}

func (s *Service) publicURL() string { return strings.TrimRight(s.Config.PublicURL, "/") }

// urlPrefix is the hook URL up to the secret.
func (s *Service) urlPrefix(serviceID uuid.UUID) string {
	return s.publicURL() + "/hooks/deploy/" + serviceID.String() + "/"
}

// DeployHookView is the API representation of a deploy hook. The secret is never part of
// it except in URL, which only the response that created the secret carries.
type DeployHookView struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Enabled   bool      `json:"enabled"`
	// URL is the complete hook URL. It is returned once, by the request that
	// enables the hook or rotates its secret, and never again.
	URL string `json:"url,omitempty" doc:"The complete hook URL, shown once when the hook is enabled or rotated; never returned again"`
	// URLPattern is the hook URL with a placeholder for the secret.
	URLPattern string `json:"urlPattern" doc:"The hook URL with <secret> in place of the secret"`
	// SecretSetAt is when the current secret was created.
	SecretSetAt *time.Time `json:"secretSetAt"`
	// LastCalledAt is the last authenticated call (null before the first).
	LastCalledAt *time.Time `json:"lastCalledAt"`
	// LastOutcome is what that call did: deploying, queued or ignored.
	LastOutcome string `json:"lastOutcome" enum:",deploying,queued,ignored"`
	// Queued reports a call waiting for the running deployment to finish.
	Queued bool `json:"queued"`
	// Image is the service's image (empty for git services); the registry
	// snippets in the UI are built from it.
	Image string `json:"image"`
	// IsProduction tells the UI to warn: the service is in a production environment.
	IsProduction bool `json:"isProduction"`
	// RateLimit describes the per-hook limit.
	RateLimit string `json:"rateLimit"`
}

func (s *Service) view(sc services.Scope, h *db.DeployHook, url string) DeployHookView {
	v := DeployHookView{
		ServiceID: sc.Service.ID, URLPattern: s.urlPrefix(sc.Service.ID) + "<secret>", URL: url,
		Image: sc.Config().Image, IsProduction: sc.IsProduction, RateLimit: "6 calls per minute per hook, burst of 3",
	}
	if sc.Service.SourceType != services.SourceImage {
		v.Image = ""
	}
	if h != nil {
		v.Enabled, v.SecretSetAt, v.LastCalledAt, v.LastOutcome, v.Queued = h.Enabled, h.SecretSetAt, h.LastCalledAt, h.LastOutcome, len(h.Pending) > 0
	}
	return v
}

func (s *Service) requirePublicURL() error {
	if s.publicURL() == "" {
		return apierr.Unprocessable("public_url_required", "Set NEBULACTRL_PUBLIC_URL to the address CI systems and registries can reach NebulaCtrl at; it is the host of the hook URL.")
	}
	return nil
}

// Get returns the hook of a service (disabled when it was never enabled).
func (s *Service) Get(ctx context.Context, serviceID uuid.UUID) (DeployHookView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeProjectsRead, serviceID)
	if err != nil {
		return DeployHookView{}, err
	}
	h, err := s.q.GetDeployHook(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.view(sc, nil, ""), nil
	}
	if err != nil {
		return DeployHookView{}, fmt.Errorf("get deploy hook: %w", err)
	}
	return s.view(sc, &h, ""), nil
}

// SetEnabled turns the hook on or off. Turning it on creates a new secret and
// returns the URL once; enabling an enabled hook changes nothing. Turning it
// off deletes the secret, so a later enable gives a new URL.
func (s *Service) SetEnabled(ctx context.Context, serviceID uuid.UUID, enabled bool) (DeployHookView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return DeployHookView{}, err
	}
	cur, err := s.q.GetDeployHook(ctx, serviceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DeployHookView{}, fmt.Errorf("get deploy hook: %w", err)
	}
	exists := err == nil
	if enabled == (exists && cur.Enabled) {
		if !exists {
			return s.view(sc, nil, ""), nil
		}
		return s.view(sc, &cur, ""), nil
	}
	if !enabled {
		var h db.DeployHook
		err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			if h, err = db.New(tx).DisableDeployHook(ctx, serviceID); err != nil {
				return err
			}
			return s.auditManage(ctx, tx, sc, "deploy_hook.disable")
		})
		if err != nil {
			return DeployHookView{}, fmt.Errorf("disable deploy hook: %w", err)
		}
		return s.view(sc, &h, ""), nil
	}
	if err := s.requirePublicURL(); err != nil {
		return DeployHookView{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return DeployHookView{}, err
	}
	var h db.DeployHook
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		if h, err = db.New(tx).EnableDeployHook(ctx, db.EnableDeployHookParams{ServiceID: serviceID, SecretHash: hashSecret(secret)}); err != nil {
			return err
		}
		return s.auditManage(ctx, tx, sc, "deploy_hook.enable")
	})
	if err != nil {
		return DeployHookView{}, fmt.Errorf("enable deploy hook: %w", err)
	}
	return s.view(sc, &h, s.urlPrefix(serviceID)+secret), nil
}

// Rotate replaces the secret of an enabled hook; the old URL stops working at
// once. The new URL is returned once.
func (s *Service) Rotate(ctx context.Context, serviceID uuid.UUID) (DeployHookView, error) {
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeDeploymentsWrite, serviceID)
	if err != nil {
		return DeployHookView{}, err
	}
	if err := s.requirePublicURL(); err != nil {
		return DeployHookView{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return DeployHookView{}, err
	}
	var h db.DeployHook
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		if h, err = db.New(tx).RotateDeployHook(ctx, db.RotateDeployHookParams{ServiceID: serviceID, SecretHash: hashSecret(secret)}); err != nil {
			return err
		}
		return s.auditManage(ctx, tx, sc, "deploy_hook.rotate")
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DeployHookView{}, apierr.Conflict("deploy_hook_disabled", "the deploy hook is off; enable it to get a URL")
	}
	if err != nil {
		return DeployHookView{}, fmt.Errorf("rotate deploy hook: %w", err)
	}
	return s.view(sc, &h, s.urlPrefix(serviceID)+secret), nil
}

func (s *Service) auditManage(ctx context.Context, tx pgx.Tx, sc services.Scope, action string) error {
	return audit.Record(ctx, tx, audit.Entry{
		Action: action, ResourceType: "service", ResourceID: sc.Service.ID.String(), ProjectID: &sc.ProjectID,
		Metadata: map[string]any{"service": sc.Service.Slug}, WorkspaceID: sc.WorkspaceID,
	})
}

// Mount attaches the unauthenticated hook endpoint at the root router.
func (s *Service) Mount(r chi.Router) {
	r.Post("/hooks/deploy/{serviceId}/{secret}", s.handleCall)
}
