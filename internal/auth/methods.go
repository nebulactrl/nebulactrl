package auth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// SignupHooks lets the teams module attach people who sign up through an
// identity provider to a team. Until it is set, OIDC sign-up is unavailable.
type SignupHooks interface {
	// CheckInvite validates an invite token (exists, not expired, not used up,
	// bound email matches when the invite is email-bound) without consuming it.
	CheckInvite(ctx context.Context, token, email string) error
	// AcceptInvite consumes the invite and adds userID as a member, inside tx.
	AcceptInvite(ctx context.Context, tx pgx.Tx, token string, userID uuid.UUID, email string) error
	// OpenSignup runs for a new user created while the instance allows open
	// sign-up and no invite was presented: it creates or joins a team for them.
	OpenSignup(ctx context.Context, tx pgx.Tx, userID uuid.UUID, name string) error
}

// loginMethods is the state shared by the TOTP and OIDC code in this package.
type loginMethods struct {
	mu        sync.RWMutex
	box       *secretbox.Box
	publicURL string
	hooks     SignupHooks
	http      *http.Client
	now       func() time.Time
}

func (m *loginMethods) clock() time.Time {
	m.mu.RLock()
	f := m.now
	m.mu.RUnlock()
	if f != nil {
		return f()
	}
	return time.Now()
}

func (m *loginMethods) client() *http.Client {
	m.mu.RLock()
	c := m.http
	m.mu.RUnlock()
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Configure supplies the secret box (provider client secrets, TOTP secrets) and
// the public URL of the control plane (OIDC redirect URIs). Call once at wiring.
func (s *Service) Configure(box *secretbox.Box, publicURL string) {
	s.methods.mu.Lock()
	defer s.methods.mu.Unlock()
	s.methods.box = box
	s.methods.publicURL = publicURL
}

// SetSignupHooks installs the teams module's sign-up hooks.
func (s *Service) SetSignupHooks(h SignupHooks) {
	s.methods.mu.Lock()
	defer s.methods.mu.Unlock()
	s.methods.hooks = h
}

// SetHTTPClient replaces the client used to talk to identity providers (tests).
func (s *Service) SetHTTPClient(c *http.Client) {
	s.methods.mu.Lock()
	defer s.methods.mu.Unlock()
	s.methods.http = c
}

// SetClock replaces the time source of the TOTP checks (tests).
func (s *Service) SetClock(now func() time.Time) {
	s.methods.mu.Lock()
	defer s.methods.mu.Unlock()
	s.methods.now = now
}

// PublicURL is the configured public URL ("" when unset).
func (s *Service) PublicURL() string {
	s.methods.mu.RLock()
	defer s.methods.mu.RUnlock()
	return s.methods.publicURL
}

func (s *Service) secretBox() (*secretbox.Box, error) {
	s.methods.mu.RLock()
	defer s.methods.mu.RUnlock()
	if s.methods.box == nil {
		return nil, apierr.New(http.StatusServiceUnavailable, "secret_key_missing", "NEBULACTRL_SECRET_KEY is not configured")
	}
	return s.methods.box, nil
}

func (s *Service) signupHooks() SignupHooks {
	s.methods.mu.RLock()
	defer s.methods.mu.RUnlock()
	return s.methods.hooks
}

// InTx runs fn in a transaction, so a caller can record an audit row together
// with the change.
func (s *Service) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return db.InTx(ctx, s.pool, fn)
}
