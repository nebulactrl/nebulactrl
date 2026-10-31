package gitproviders

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/netguard"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// Deployer starts deployments for pushes. *deployments.Service implements it.
type Deployer interface {
	DeployInternal(ctx context.Context, serviceID uuid.UUID, trigger string, commit *deployments.CommitInfo, message string) (deployments.DeploymentView, error)
}

// Deps are the collaborators of Service.
type Deps struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Jobs     *jobs.Manager
	Box      *secretbox.Box
	Config   *config.Config
	Services *services.Service
	Deployer Deployer
	// HTTP is the client for provider APIs. The default (20 s timeout) refuses
	// loopback, private, link-local and metadata destinations, since a
	// connection's base URL is set by a team admin (see netguard). Tests inject
	// their own.
	HTTP   *http.Client
	Logger *slog.Logger
	// PushHookWait is how long a push waits for its PushHook (default
	// PushHookWait). Tests shorten it.
	PushHookWait time.Duration
}

// Service implements git connections, links, webhooks and auto-deploy.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	bus  *events.Bus
	jobs *jobs.Manager
	box  *secretbox.Box
	cfg  *config.Config
	svcs *services.Service
	dep  Deployer
	hc   *http.Client
	log  *slog.Logger
	now  func() time.Time

	// pushHook runs before a push deploys (SetPushHook); pushHookWait is how
	// long the push waits for it (PushHookWait when zero).
	pushHook     PushHook
	pushHookWait time.Duration

	subMu    sync.RWMutex
	pushSubs []func(context.Context, PushEvent)
	prSubs   []func(context.Context, PullRequestEvent)
	wg       sync.WaitGroup // in-flight subscriber callbacks

	tokMu      sync.Mutex
	instTokens map[uuid.UUID]cachedToken // GitHub installation tokens for API calls
	refreshMu  sync.Mutex                // serializes OAuth refreshes (refresh tokens rotate)
	statusMu   sync.Mutex                // serializes commit status reports

	rejectMu       sync.Mutex
	rejectLimiters map[uuid.UUID]*rate.Limiter
}

type cachedToken struct {
	token   string
	expires time.Time
}

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.HTTP == nil {
		d.HTTP = netguard.NewClient(20 * time.Second)
	}
	if d.Config == nil {
		d.Config = &config.Config{}
	}
	s := &Service{
		pool: d.Pool, bus: d.Bus, jobs: d.Jobs, box: d.Box, cfg: d.Config, svcs: d.Services, dep: d.Deployer,
		hc: d.HTTP, log: d.Logger.With("component", "gitproviders"), now: time.Now, pushHookWait: d.PushHookWait,
		instTokens: map[uuid.UUID]cachedToken{}, rejectLimiters: map[uuid.UUID]*rate.Limiter{},
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// SetDeployer sets the deployment starter (the deployments service is built
// after this one's collaborators in wire.go).
func (s *Service) SetDeployer(d Deployer) { s.dep = d }

// SubscribePush registers fn to be called, in its own goroutine, for every
// verified push after it was persisted and matched to services.
func (s *Service) SubscribePush(fn func(ctx context.Context, ev PushEvent)) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.pushSubs = append(s.pushSubs, fn)
}

// SubscribePullRequest registers fn to be called, in its own goroutine, for
// every verified pull / merge request event (opened, synchronize, reopened,
// closed, merged).
func (s *Service) SubscribePullRequest(fn func(ctx context.Context, ev PullRequestEvent)) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.prSubs = append(s.prSubs, fn)
}

// Wait blocks until every subscriber callback started so far has returned.
func (s *Service) Wait() { s.wg.Wait() }

// fire runs fn detached from the request that delivered the event.
func (s *Service) fire(fn func(ctx context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("git event subscriber panicked", "panic", fmt.Sprint(r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		fn(ctx)
	}()
}

// ---- helpers ----

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("gitproviders: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

func (s *Service) publicURL() string { return strings.TrimRight(s.cfg.PublicURL, "/") }

func (s *Service) requirePublicURL() error {
	if s.publicURL() == "" {
		return apierr.Unprocessable("public_url_required", "Set NEBULACTRL_PUBLIC_URL to the address git providers can reach NebulaCtrl at (it is the webhook and OAuth callback address).")
	}
	return nil
}

// hookURL is where a provider delivers webhooks of the connection.
func (s *Service) hookURL(provider string, id uuid.UUID) string {
	return s.publicURL() + "/hooks/git/" + provider + "/" + id.String()
}

func (s *Service) redirectURI() string { return s.publicURL() + "/api/v1/git/oauth/callback" }

// settingsURL is the UI page flows return to: Team settings → Integrations, with the
// outcome in the query (git_connected, git_notice or git_error).
func (s *Service) settingsURL(q url.Values) string {
	return s.publicURL() + "/settings/integrations?" + q.Encode()
}

func (s *Service) loadConnection(ctx context.Context, scope string, id uuid.UUID) (db.GitConnection, error) {
	if err := policy.Require(ctx, scope, nil); err != nil {
		return db.GitConnection{}, err
	}
	ws := policy.WorkspaceID(ctx)
	c, err := s.q.GetGitConnection(ctx, db.GetGitConnectionParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GitConnection{}, apierr.NotFound("git connection")
	}
	if err != nil {
		return db.GitConnection{}, fmt.Errorf("load git connection: %w", err)
	}
	return c, nil
}

// loadConnectionInternal loads a connection without authorization (webhooks,
// jobs and subscribers that already hold a verified connection id).
func (s *Service) loadConnectionInternal(ctx context.Context, id uuid.UUID) (db.GitConnection, error) {
	c, err := s.q.GetGitConnectionInternal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GitConnection{}, apierr.NotFound("git connection")
	}
	if err != nil {
		return db.GitConnection{}, fmt.Errorf("load git connection: %w", err)
	}
	return c, nil
}

func (s *Service) creds(c db.GitConnection) (credentials, error) {
	var out credentials
	if len(c.CredentialsEnc) == 0 {
		return out, nil
	}
	plain, err := s.box.Open(secretbox.GitConnectionsCredentials, c.ID.String(), c.CredentialsEnc)
	if err != nil {
		return out, fmt.Errorf("decrypt git connection credentials: %w", err)
	}
	defer secretbox.Wipe(plain)
	if err := json.Unmarshal(plain, &out); err != nil {
		return out, fmt.Errorf("decode git connection credentials: %w", err)
	}
	return out, nil
}

// sealCreds seals credentials for the connection with the given id (known
// before the row is inserted).
func (s *Service) sealCreds(id uuid.UUID, c credentials) ([]byte, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	defer secretbox.Wipe(raw)
	return s.box.Seal(secretbox.GitConnectionsCredentials, id.String(), raw)
}

func (s *Service) webhookSecret(c db.GitConnection) (string, error) {
	return s.box.OpenString(secretbox.GitConnectionsWebhookSecret, c.ID.String(), c.WebhookSecretEnc)
}

func decodeMeta(raw json.RawMessage) meta {
	var m meta
	_ = json.Unmarshal(raw, &m)
	return m
}

func encodeMeta(m meta) json.RawMessage {
	raw, _ := json.Marshal(m)
	return raw
}

// accessToken returns a token good for API calls of the connection.
func (s *Service) accessToken(ctx context.Context, c db.GitConnection) (string, error) {
	switch c.AuthType {
	case AuthToken:
		cr, err := s.creds(c)
		if err != nil {
			return "", err
		}
		return cr.Token, nil
	case AuthOAuth:
		return s.oauthToken(ctx, c)
	case AuthGitHubApp:
		return s.installationToken(ctx, c)
	}
	return "", errNoAPI
}

// api returns the driver and an authenticated API client of the connection.
func (s *Service) api(ctx context.Context, c db.GitConnection) (driver, *apiClient, error) {
	drv := driverFor(Provider(c.Provider))
	if drv == nil || !drv.hasAPI() {
		return nil, nil, errNoAPI
	}
	tok, err := s.accessToken(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	cl := &apiClient{hc: s.hc, base: drv.apiBase(c.BaseUrl), secrets: []string{tok}}
	if tok != "" {
		cl.authz = drv.authHeader(tok, c.AuthType)
	}
	return drv, cl, nil
}

// rejectLimiter allows a connection to log a few rejected deliveries per
// minute, so unauthenticated noise cannot flood the delivery log.
func (s *Service) rejectLimiter(id uuid.UUID) *rate.Limiter {
	s.rejectMu.Lock()
	defer s.rejectMu.Unlock()
	l, ok := s.rejectLimiters[id]
	if !ok {
		l = rate.NewLimiter(rate.Every(6*time.Second), 10)
		s.rejectLimiters[id] = l
	}
	return l
}

func publish(bus *events.Bus, typ string, workspaceID, id uuid.UUID) {
	if bus != nil {
		bus.Publish(events.Event{Type: typ, ResourceID: id.String(), WorkspaceID: workspaceID.String()})
	}
}

// Event types published for the web UI.
const (
	EventConnectionUpdated = "git.connection.updated"
	EventDelivery          = "git.delivery.created"
)
