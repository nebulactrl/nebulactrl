package dashboardaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DNS is the part of dns01.Service the seeder uses.
type DNS interface {
	ListCredentials(ctx context.Context, serverID *uuid.UUID) ([]dns01.DNSCredential, error)
	CreateCredential(ctx context.Context, in dns01.CredentialInput) (dns01.DNSCredential, error)
	GetServerState(ctx context.Context, serverID uuid.UUID) (dns01.ServerDNS01, error)
	SetResolver(ctx context.Context, serverID uuid.UUID, provider string, in dns01.ResolverInput) (dns01.ServerDNS01, error)
}

// ProxyDeployer sends "Deploy proxy" (proxyserver.Service).
type ProxyDeployer interface {
	Deploy(ctx context.Context, serverID uuid.UUID, reprovision bool) (proxyserver.ProxyDeployResult, error)
}

// Seeder timings and limits.
const (
	seedInitialBackoff = 5 * time.Second
	seedMaxBackoff     = 5 * time.Minute
	// seedDeployGrace is how long a sent Deploy proxy gets to report before the
	// seeder looks at the result (and, failing that, sends another).
	seedDeployGrace = 2 * time.Minute
	// seedMaxDeploys bounds the automatic proxy deploys: after that the admin
	// deploys from Servers, Proxy.
	seedMaxDeploys = 3
)

// credentialPrefix names the seeded credential "dashboard-<provider>".
const credentialPrefix = "dashboard-"

// tokenVariable is the credential variable that carries the installer's token
// per provider (the single-token providers config.DashboardDNSProviders lists).
var tokenVariable = map[string]string{agentprotocol.DNSCloudflare: "CF_DNS_API_TOKEN"}

// Seeder gives the dashboard a DNS-01 certificate when the installer's
// environment names a provider and carries its token (NEBULACTRL_DASHBOARD_DNS_RESOLVER,
// NEBULACTRL_DASHBOARD_DNS_TOKEN). Once the control-plane server exists and is
// online with proxy.v2 it creates the provider credential (sealed through the
// DNS-01 credential store, scoped to that server, named dashboard-<provider>),
// sets the server's resolver, deploys the proxy and, only when the proxy has
// applied the resolver, makes the dashboard's route name it. Until then the
// dashboard keeps serving its automatic HTTP-01 certificate.
//
// It acts as a system principal with the admin scope of the server's team (the
// credential store and the proxy deploy check their usual policies), retries
// with backoff and never logs or stores the token anywhere but the sealed
// credential. It does nothing once an admin has edited the settings: from then
// on the installer's environment is ignored.
type Seeder struct {
	pool  *pgxpool.Pool
	q     *db.Queries
	svc   *Service
	dns   DNS
	proxy ProxyDeployer
	inst  config.Installer
	log   *slog.Logger

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) bool

	deploys    int
	lastDeploy time.Time
}

// SeederDeps are the collaborators of a Seeder.
type SeederDeps struct {
	Pool    *pgxpool.Pool
	Service *Service
	DNS     DNS
	Proxy   ProxyDeployer
	Config  *config.Config
	Logger  *slog.Logger
}

// NewSeeder returns a Seeder.
func NewSeeder(d SeederDeps) *Seeder {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Seeder{
		pool: d.Pool, q: db.New(d.Pool), svc: d.Service, dns: d.DNS, proxy: d.Proxy, log: d.Logger.With("component", "dashboardaccess"),
		now: time.Now, sleep: sleep,
	}
	if d.Config != nil {
		s.inst = d.Config.Installer
	}
	return s
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Enabled reports whether the installer asked for a DNS-01 certificate.
func (s *Seeder) Enabled() bool {
	return s.inst.DashboardDNSResolver != "" && s.inst.DashboardDNSToken != ""
}

// Run steps until the resolver is live (or there is nothing to do), waiting
// with exponential backoff between steps.
func (s *Seeder) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	backoff := seedInitialBackoff
	for {
		done, err := s.Step(ctx)
		if err != nil && ctx.Err() == nil {
			// The error never contains the token: it names variables, not values.
			s.log.WarnContext(ctx, "dashboard DNS-01 certificate setup", "provider", s.inst.DashboardDNSResolver, "err", err, "retry_in", backoff.String())
		}
		if done {
			return
		}
		if !s.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, seedMaxBackoff)
	}
}

// systemContext is the principal the seeder acts as.
func systemContext(ctx context.Context, srv db.Server) context.Context {
	return auth.WithPrincipal(ctx, &auth.Principal{
		Type: auth.TypeSystem, WorkspaceID: srv.WorkspaceID, Label: "Installer", Scopes: []string{policy.ScopeAdmin},
	})
}

// Step advances the setup by one move and reports whether nothing is left to do.
func (s *Seeder) Step(ctx context.Context) (done bool, err error) {
	provider := s.inst.DashboardDNSResolver
	row, err := s.q.GetDashboardAccess(ctx)
	if err == nil && row.EditedAt != nil {
		return true, nil // an admin owns the settings
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("load dashboard access: %w", err)
	}
	srv, err := s.q.GetControlPlaneServer(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		exists, xerr := s.q.ControlPlaneServerExists(ctx)
		if xerr != nil {
			return false, fmt.Errorf("check control-plane server: %w", xerr)
		}
		return exists, nil // exists but not returned: revoked, never recreated
	}
	if err != nil {
		return false, fmt.Errorf("load control-plane server: %w", err)
	}
	if srv.Status != "online" || !proxyserver.SupportsProxyV2(srv) {
		return false, nil // the agent has not enrolled and reported yet
	}
	if srv.AcmeEmail == nil || *srv.AcmeEmail == "" {
		return false, errors.New("the control-plane server has no ACME email (NEBULACTRL_ACME_EMAIL): DNS-01 needs a Let's Encrypt contact address")
	}
	ctx = systemContext(ctx, srv)

	credID, err := s.ensureCredential(ctx, srv, provider)
	if err != nil {
		return false, err
	}
	st, err := s.dns.GetServerState(ctx, srv.ID)
	if err != nil {
		return false, fmt.Errorf("read the server's DNS-01 state: %w", err)
	}
	res := findResolver(st, provider)
	if res == nil || res.Change == dns01.ChangeRemove {
		if _, err := s.dns.SetResolver(ctx, srv.ID, provider, dns01.ResolverInput{CredentialID: credID}); err != nil {
			return false, fmt.Errorf("set the %s resolver: %w", provider, err)
		}
		return false, nil
	}
	if res.Change != dns01.ChangeNone {
		return false, s.deploy(ctx, srv)
	}
	// The proxy runs the resolver: the route may name it now.
	changed, err := s.svc.resolverLive(ctx, srv, provider)
	if err != nil {
		return false, fmt.Errorf("switch the dashboard to DNS-01: %w", err)
	}
	if changed {
		s.log.InfoContext(ctx, "the dashboard's certificate now uses DNS-01", "provider", provider)
	}
	return true, nil
}

func findResolver(st dns01.ServerDNS01, provider string) *dns01.DNSResolverView {
	for i := range st.Resolvers {
		// The automatic Cloudflare resolver (names in a connected zone) is not the
		// dashboard's: the dashboard's is a resolver of its own credential.
		if st.Resolvers[i].Provider == provider && !st.Resolvers[i].Automatic {
			return &st.Resolvers[i]
		}
	}
	return nil
}

// ensureCredential returns the id of the credential named dashboard-<provider>
// on the server, creating it (once: an existing credential is never changed, so
// a token an admin rotated in the dashboard is not reverted by the installer's).
func (s *Seeder) ensureCredential(ctx context.Context, srv db.Server, provider string) (uuid.UUID, error) {
	name := credentialPrefix + provider
	list, err := s.dns.ListCredentials(ctx, &srv.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list DNS credentials: %w", err)
	}
	for _, c := range list {
		if c.Name == name && c.Provider == provider {
			return c.ID, nil
		}
	}
	envName, ok := tokenVariable[provider]
	if !ok {
		return uuid.Nil, fmt.Errorf("provider %q is not supported for the dashboard", provider)
	}
	c, err := s.dns.CreateCredential(ctx, dns01.CredentialInput{
		Name: name, Provider: provider, ServerID: &srv.ID, Values: map[string]string{envName: s.inst.DashboardDNSToken},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create the %s DNS credential: %w", provider, err)
	}
	s.log.InfoContext(ctx, "created the dashboard's DNS credential", "provider", provider, "credential", name)
	return c.ID, nil
}

// deploy sends Deploy proxy for the staged resolver, at most seedMaxDeploys
// times and not again while a sent one may still be reporting.
func (s *Seeder) deploy(ctx context.Context, srv db.Server) error {
	if !s.lastDeploy.IsZero() && s.now().Sub(s.lastDeploy) < seedDeployGrace {
		return nil
	}
	if s.deploys >= seedMaxDeploys {
		return fmt.Errorf("the proxy did not apply the resolver after %d deploys: check the agent, then run Deploy proxy under Servers, Proxy", s.deploys)
	}
	_, err := s.proxy.Deploy(ctx, srv.ID, false)
	if ae, ok := apierr.As(err); ok && ae.Status == 409 {
		return nil // a deploy is in progress; look again next time
	}
	if err != nil {
		return fmt.Errorf("deploy the proxy: %w", err)
	}
	s.deploys++
	s.lastDeploy = s.now()
	s.log.InfoContext(ctx, "deploying the proxy to activate the dashboard's DNS-01 resolver", "attempt", s.deploys)
	return nil
}
