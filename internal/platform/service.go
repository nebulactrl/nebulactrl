package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/terminal"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// EventUpdated is published whenever a platform service changes (the web app
// refreshes Team settings → Private network (Headscale)).
const EventUpdated = "platform.updated"

// Audit actions.
const (
	ActionProvision = "platform.provision"
	ActionRemove    = "platform.remove"
	ActionRewire    = "platform.rewire"
)

// Backups is what the platform service needs from the backups domain.
type Backups interface {
	ListDestinations(ctx context.Context) ([]backups.DestinationView, error)
	PutVolumeBackupConfig(ctx context.Context, serviceID uuid.UUID, in backups.VolumeBackupConfigInput) (backups.VolumeBackupConfigView, error)
}

// Tailnet is the Tailscale/Headscale integration.
type Tailnet interface {
	Get(ctx context.Context) (tailscale.TailscaleIntegration, error)
	Put(ctx context.Context, in tailscale.PutInput) (tailscale.TailscaleIntegration, error)
	Delete(ctx context.Context) error
	Test(ctx context.Context) (tailscale.TailscaleTestResult, error)
}

// PolicyApplier writes the managed Headscale policy of a team: the base rules plus
// the generated mesh rules (internal/mesh.Service).
type PolicyApplier interface {
	ApplyPolicy(ctx context.Context, ws uuid.UUID) error
}

// Deployer deploys the staged services of an environment.
type Deployer interface {
	DeployStaged(ctx context.Context, envID uuid.UUID, serviceIDs []uuid.UUID, message string) (deployments.StagedResult, error)
}

// Deps are the collaborators of a Service. Pool may be nil for OpenAPI generation.
type Deps struct {
	Pool     *pgxpool.Pool
	Bus      *events.Bus
	Jobs     *jobs.Manager
	Box      *secretbox.Box
	Services *services.Service
	Deploy   Deployer
	Backups  Backups
	Tailnet  Tailnet
	// Mesh writes the managed Headscale policy (internal/mesh); nil skips it.
	Mesh   PolicyApplier
	Agents terminal.Agents
	Logger *slog.Logger
	// HTTP probes the public URL of the service. Tests replace it.
	HTTP *http.Client
	Now  func() time.Time
}

// Service implements platform services.
type Service struct {
	Deps
	q *db.Queries
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.HTTP == nil {
		d.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	s := &Service{Deps: d}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

func (s *Service) publish(row db.PlatformService) {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(events.Event{Type: EventUpdated, ResourceID: row.ID.String(), WorkspaceID: row.WorkspaceID.String()})
}

// sysCtx is ctx carrying the control plane's own principal for the team: the
// provisioning job and the services it calls authorize it like an administrator,
// and audit rows say "system".
func sysCtx(ctx context.Context, workspaceID uuid.UUID) context.Context {
	return auth.WithPrincipal(ctx, &auth.Principal{
		Type: auth.TypeSystem, WorkspaceID: workspaceID, Label: "Platform services", Scopes: []string{policy.ScopeAdmin},
	})
}

// admin authorizes the caller as a team administrator and returns the team.
func (s *Service) admin(ctx context.Context) (uuid.UUID, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return uuid.Nil, err
	}
	return policy.WorkspaceID(ctx), nil
}

func (s *Service) load(ctx context.Context, id uuid.UUID) (db.PlatformService, error) {
	ws, err := s.admin(ctx)
	if err != nil {
		return db.PlatformService{}, err
	}
	row, err := s.q.GetPlatformService(ctx, db.GetPlatformServiceParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PlatformService{}, apierr.NotFound("platform service")
	}
	if err != nil {
		return db.PlatformService{}, fmt.Errorf("get platform service: %w", err)
	}
	return row, nil
}

// ---- the system project --------------------------------------------------------------

// ensureSystem returns the team's system project and its platform environment,
// creating them on first use.
func (s *Service) ensureSystem(ctx context.Context, q *db.Queries, ws uuid.UUID) (db.Project, db.Environment, error) {
	proj, err := q.GetSystemProject(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		taken, terr := q.ListProjectSlugsLike(ctx, db.ListProjectSlugsLikeParams{WorkspaceID: ws, Base: "nebulactrl-platform"})
		if terr != nil {
			return db.Project{}, db.Environment{}, terr
		}
		proj, err = q.CreateSystemProject(ctx, db.CreateSystemProjectParams{
			WorkspaceID: ws, Name: SystemProjectName, Slug: projects.UniqueSlug("nebulactrl-platform", taken), Description: SystemProjectDesc,
		})
	}
	if err != nil {
		return db.Project{}, db.Environment{}, fmt.Errorf("system project: %w", err)
	}
	envs, err := q.ListEnvironments(ctx, proj.ID)
	if err != nil {
		return db.Project{}, db.Environment{}, err
	}
	for _, e := range envs {
		if e.Slug == SystemEnvSlug {
			return proj, e, nil
		}
	}
	env, err := q.CreateEnvironment(ctx, db.CreateEnvironmentParams{ProjectID: proj.ID, Name: SystemEnvName, Slug: SystemEnvSlug, IsProduction: false})
	if err != nil {
		return db.Project{}, db.Environment{}, fmt.Errorf("system environment: %w", err)
	}
	return proj, env, nil
}

// ---- provisioning ----------------------------------------------------------------------

// ProvisionInput is a "Set up for me" request.
type ProvisionInput struct {
	Kind     string
	ServerID uuid.UUID
	// Hostname is the public hostname (default: a generated one under the
	// server's base domain, else <ip>.sslip.io).
	Hostname string
	// PublicURL overrides https://<hostname> (a proxy on a non-standard port, or
	// plain http for a local test).
	PublicURL string
	// Replace lets Headscale replace an existing Tailscale integration.
	Replace bool
}

// ProvisionResult is the new platform service.
type ProvisionResult struct {
	View PlatformServiceView
}

var hostnameRe = domains.ValidateHostname

// Provision stages a platform service and starts its provisioning job. Nothing
// runs on a server until the job deploys it; the response is 202-shaped.
func (s *Service) Provision(ctx context.Context, in ProvisionInput) (ProvisionResult, error) {
	ws, err := s.admin(ctx)
	if err != nil {
		return ProvisionResult{}, err
	}
	info, ok := InfoFor(in.Kind)
	if !ok {
		return ProvisionResult{}, apierr.Unprocessable("invalid_kind", "kind must be one of "+strings.Join(Kinds, ", "))
	}
	p, err := policy.Authenticated(ctx)
	if err != nil {
		return ProvisionResult{}, err
	}
	var opts options
	if _, err := s.q.GetPlatformServiceByKind(ctx, db.GetPlatformServiceByKindParams{WorkspaceID: ws, Kind: in.Kind}); err == nil {
		return ProvisionResult{}, apierr.Conflict("platform_service_exists", info.Label+" is already set up. Remove it first to set it up again.")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ProvisionResult{}, err
	}

	platformID := uuid.New()

	var row db.PlatformService
	var scopes []services.Scope
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		proj, env, err := s.ensureSystem(ctx, q, ws)
		if err != nil {
			return err
		}
		envScope := services.EnvScope{Env: env, WorkspaceID: ws, ProjectSlug: proj.Slug}

		serverID := in.ServerID
		if serverID == uuid.Nil {
			return apierr.Unprocessable("validation_failed", "serverId is required")
		}
		srv, err := q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID, WorkspaceID: &ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.Unprocessable("server_not_found", "the selected server does not exist")
		}
		if err != nil {
			return err
		}
		if srv.ProxyStatus != nil && *srv.ProxyStatus == "error" {
			return apierr.Unprocessable("proxy_unavailable", "The proxy on "+srv.Name+" is not running, so the service could not be reached.")
		}

		host, generated, err := s.chooseHostname(ctx, q, in, srv, proj, env)
		if err != nil {
			return err
		}
		publicURL, err := resolvePublicURL(in.PublicURL, host)
		if err != nil {
			return err
		}
		if strings.HasPrefix(publicURL, "https://") && (srv.AcmeEmail == nil || *srv.AcmeEmail == "") && !strings.HasSuffix(host, ".localhost") {
			return apierr.Unprocessable("acme_email_required",
				"Set an ACME email on "+srv.Name+" first (Servers, Proxy): without it the proxy serves a self-signed certificate that clients refuse.")
		}
		if !in.Replace {
			if cur, err := s.Tailnet.Get(ctx); err == nil && cur.Configured {
				return apierr.Conflict("integration_exists", "A Tailscale integration already exists. Replace it with the new Headscale server, or keep it.")
			}
		}
		spec, err := build(info.Kind, specInput{Hostname: host, PublicURL: publicURL})
		if err != nil {
			return err
		}

		svc, err := s.Services.CreateInTx(ctx, tx, envScope, services.CreateInput{
			Name: spec.Name, Kind: services.KindApp, SourceType: services.SourceImage, Config: spec.Config, ServerID: &serverID,
		})
		if err != nil {
			return err
		}
		sc := services.ScopeOf(envScope, svc)
		for _, v := range spec.Volumes {
			if _, err := q.InsertVolume(ctx, db.InsertVolumeParams{ServiceID: svc.ID, Name: v.Name, MountPath: v.Mount}); err != nil {
				return fmt.Errorf("insert volume: %w", err)
			}
		}
		for _, v := range spec.Vars {
			w := services.VarWrite{Key: v.Key, Value: v.Value, Secret: v.Secret}
			if v.Secret {
				w.Source = services.SourceGenerated
			}
			if _, err := services.WriteServiceVariable(ctx, q, s.Box, sc, w); err != nil {
				return fmt.Errorf("write %s: %w", v.Key, err)
			}
		}
		// A plain-http public URL is served as such (no redirect to https).
		var routeOpts json.RawMessage
		if strings.HasPrefix(publicURL, "http://") {
			off := false
			if routeOpts, err = json.Marshal(agentprotocol.RouteOptions{ForceHTTPS: &off}); err != nil {
				return fmt.Errorf("encode route options: %w", err)
			}
		}
		dom, err := q.InsertDomain(ctx, db.InsertDomainParams{
			ServiceID: svc.ID, Hostname: host, TargetPort: int32(spec.DomainPort), Generated: generated,
			StatusMessage: domains.DNSGuidance(host, srv.PublicIp), Options: routeOpts,
		})
		if err != nil {
			if services.IsUniqueViolation(err) {
				return apierr.Conflict("domain_conflict", "The hostname "+host+" is already in use.")
			}
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, domains.DNSCheckArgs{DomainID: &dom.ID}, nil); err != nil {
			return fmt.Errorf("enqueue dns check: %w", err)
		}
		if _, err := q.BumpServiceGeneration(ctx, svc.ID); err != nil {
			return err
		}
		scopes = append(scopes, sc)

		row, err = q.InsertPlatformService(ctx, db.InsertPlatformServiceParams{
			ID: platformID, WorkspaceID: ws, Kind: info.Kind, EnvironmentID: &env.ID, ServiceIds: []uuid.UUID{svc.ID}, Hostname: host, PublicUrl: publicURL,
			Options: encodeJSON(withReplace(opts, in.Replace)), CreatedBy: userPtr(p),
		})
		if err != nil {
			if services.IsUniqueViolation(err) {
				return apierr.Conflict("platform_service_exists", info.Label+" is already set up.")
			}
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, ProvisionArgs{ID: row.ID}, nil); err != nil {
			return fmt.Errorf("enqueue provisioning: %w", err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionProvision, ResourceType: "platform_service", ResourceID: row.ID.String(), ProjectID: &proj.ID,
			Metadata: map[string]any{"kind": info.Kind, "server": srv.Name, "hostname": host, "image": spec.Config.Image},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return ProvisionResult{}, err
		}
		return ProvisionResult{}, fmt.Errorf("provision %s: %w", in.Kind, err)
	}
	for _, sc := range scopes {
		services.PublishService(s.Bus, services.EventServiceUpdated, sc)
	}
	s.publish(row)
	v, err := s.view(ctx, row)
	if err != nil {
		return ProvisionResult{}, err
	}
	return ProvisionResult{View: v}, nil
}

func userPtr(p *auth.Principal) *uuid.UUID {
	if p.Type == auth.TypeUser && p.UserID != uuid.Nil {
		return &p.UserID
	}
	return nil
}

// withReplace remembers that the Headscale integration may be replaced.
func withReplace(o options, replace bool) options {
	o.ReplaceIntegration = replace
	return o
}

// chooseHostname validates the requested hostname or generates one.
func (s *Service) chooseHostname(ctx context.Context, q *db.Queries, in ProvisionInput, srv db.Server, proj db.Project, env db.Environment) (string, bool, error) {
	if h := strings.TrimSpace(in.Hostname); h != "" {
		host, err := hostnameRe(h)
		if err != nil {
			return "", false, err
		}
		return host, false, nil
	}
	base, ok := domains.BaseDomain(srv)
	if !ok {
		return "", false, apierr.Unprocessable("no_base_domain",
			"The server has neither a base domain nor a public IP: enter a hostname for the service.")
	}
	slug := projects.Slugify(nameHeadscale, "service")
	first := domains.GenerateHostname(slug, env.Slug, proj.Slug, base)
	host := first
	for i := 2; ; i++ {
		n, err := q.CountDomainsByHostname(ctx, host)
		if err != nil {
			return "", false, err
		}
		if n == 0 {
			return host, true, nil
		}
		if i > 9 {
			return "", false, apierr.Conflict("domain_conflict", "No free generated hostname: enter one.")
		}
		label, rest, _ := strings.Cut(first, ".")
		suffix := fmt.Sprintf("-%d", i)
		if len(label)+len(suffix) > 63 {
			label = strings.Trim(label[:63-len(suffix)], "-")
		}
		host = label + suffix + "." + rest
	}
}

// resolvePublicURL returns the URL clients use: override, else https://host.
func resolvePublicURL(override, host string) (string, error) {
	if strings.TrimSpace(override) == "" {
		return "https://" + host, nil
	}
	u, err := url.Parse(strings.TrimSpace(override))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", apierr.Unprocessable("validation_failed", "publicUrl must be an http(s) URL without credentials or path, e.g. https://s3.example.com")
	}
	if !strings.EqualFold(u.Hostname(), host) {
		return "", apierr.Unprocessable("validation_failed", "publicUrl must use the service's hostname ("+host+")")
	}
	return u.Scheme + "://" + u.Host, nil
}
