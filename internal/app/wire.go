package app

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/analytics"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/buildplacement"
	"github.com/nebulactrl/nebulactrl/internal/canvas"
	"github.com/nebulactrl/nebulactrl/internal/clone"
	"github.com/nebulactrl/nebulactrl/internal/combine"
	"github.com/nebulactrl/nebulactrl/internal/composeimport"
	"github.com/nebulactrl/nebulactrl/internal/configsync"
	"github.com/nebulactrl/nebulactrl/internal/dashboardaccess"
	"github.com/nebulactrl/nebulactrl/internal/databases"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/deployhooks"
	"github.com/nebulactrl/nebulactrl/internal/deployments"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/downloads"
	"github.com/nebulactrl/nebulactrl/internal/environments"
	"github.com/nebulactrl/nebulactrl/internal/fleet"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/gitssh/deploykeys"
	"github.com/nebulactrl/nebulactrl/internal/importers/coolify"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/logs"
	"github.com/nebulactrl/nebulactrl/internal/maintenance"
	mcpserver "github.com/nebulactrl/nebulactrl/internal/mcp"
	"github.com/nebulactrl/nebulactrl/internal/mesh"
	"github.com/nebulactrl/nebulactrl/internal/meshlinks"
	"github.com/nebulactrl/nebulactrl/internal/networks"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/passwordreset"
	"github.com/nebulactrl/nebulactrl/internal/platform"
	"github.com/nebulactrl/nebulactrl/internal/previews"
	"github.com/nebulactrl/nebulactrl/internal/prodguard"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/projects"
	"github.com/nebulactrl/nebulactrl/internal/proxyroutes"
	"github.com/nebulactrl/nebulactrl/internal/proxyserver"
	"github.com/nebulactrl/nebulactrl/internal/reconciler"
	"github.com/nebulactrl/nebulactrl/internal/registries"
	"github.com/nebulactrl/nebulactrl/internal/releases"
	"github.com/nebulactrl/nebulactrl/internal/scaling"
	"github.com/nebulactrl/nebulactrl/internal/secretmgr"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	agentupgrade "github.com/nebulactrl/nebulactrl/internal/servers/agentupgrade"
	servercleanup "github.com/nebulactrl/nebulactrl/internal/servers/cleanup"
	serverhardening "github.com/nebulactrl/nebulactrl/internal/servers/hardening"
	serverpatching "github.com/nebulactrl/nebulactrl/internal/servers/patching"
	servertunnel "github.com/nebulactrl/nebulactrl/internal/servers/tunnel"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/statuspages"
	"github.com/nebulactrl/nebulactrl/internal/statuspublic"
	"github.com/nebulactrl/nebulactrl/internal/svcalerts"
	"github.com/nebulactrl/nebulactrl/internal/sysupdate"
	"github.com/nebulactrl/nebulactrl/internal/tailnet"
	"github.com/nebulactrl/nebulactrl/internal/tasks"
	"github.com/nebulactrl/nebulactrl/internal/teams"
	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/internal/terminal"
	"github.com/nebulactrl/nebulactrl/internal/tokens"
	"github.com/nebulactrl/nebulactrl/internal/uptime"
	"github.com/nebulactrl/nebulactrl/internal/variables"
	"github.com/nebulactrl/nebulactrl/internal/webui"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	catalogfs "github.com/nebulactrl/nebulactrl/templates"
)

// Modules lists what every domain contributes to the HTTP layer.
type Modules struct {
	// Register attaches huma operations under /api/v1.
	Register []func(huma.API)
	// MountAPI attaches raw chi routes under /api/v1 (authenticated, CSRF-checked).
	MountAPI []func(chi.Router)
	// Mount attaches raw routes at the root (agent endpoints, MCP, install script).
	Mount []func(chi.Router)
	// Fallback serves the embedded SPA for unmatched paths (nil = plain 404).
	Fallback http.Handler
	// MCP is the MCP server; Build attaches it to the assembled API (the
	// generated tools dispatch to it in-process).
	MCP *mcpserver.Handler
	// Status is the public status-page listener's handler (internal/statuspublic),
	// served by cmd/server on NEBULACTRL_STATUS_LISTEN, never on the main listener.
	Status http.Handler
	// Start hooks run from App.Start before the job workers, once the database
	// is migrated. An error aborts the start.
	Start []func(ctx context.Context) error
}

// wire constructs every domain service and returns its HTTP registrations.
// New modules are added here: construct the service from c (Pool, Bus,
// Jobs, Box, …), call c.Jobs.AddWorker / AddPeriodic for its jobs, and append
// its Register / Mount hooks. Constructors must not touch the pool.
func wire(c *Core) Modules {
	var m Modules

	tokenSvc := tokens.NewService(c.Pool, c.Bus)
	// Production guard: MCP principals need a human approval for production
	// changes. Installed process-wide; domain services call prodguard.Check.
	guard := prodguard.New(prodguard.Deps{Pool: c.Pool, Box: c.Box, PublicURL: c.Config.PublicURL, Logger: c.Logger})
	guard.Install()
	projectSvc := projects.NewService(c.Pool, c.Bus)
	envSvc := environments.NewService(c.Pool, c.Bus)
	auditSvc := audit.NewService(c.Pool)

	// Notifications: channels and delivery. Domains emit through notifySvc
	// (notifications.Emit is nil-safe); the sources owned elsewhere are wired
	// below (deployments, hub, scaling, maintenance, backups, certificates).
	notifySvc := notifications.NewService(notifications.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, PublicURL: c.Config.PublicURL, Logger: c.Logger,
	})

	// Teams: members, invites (mailed through the team's email channel or the
	// instance SMTP relay) and sign-up through invites.
	teamSvc := teams.NewService(teams.Deps{
		Pool: c.Pool, Bus: c.Bus, Auth: c.Auth, Box: c.Box, Mailer: teamMailer{notifySvc},
		PublicURL: c.Config.PublicURL, Logger: c.Logger, Jobs: c.Jobs,
	})
	c.Auth.SetSignupHooks(teams.Hooks{S: teamSvc})
	// Forgot password: emailed through the instance relay or a team email channel.
	c.Auth.SetResetMailer(resetMailer{n: notifySvc, t: teamSvc, q: db.New(c.Pool)})

	// Agent hub: durable command dispatch, log routing, enrollment.
	hub := agents.NewHub(c.Pool, c.Bus, c.Jobs, c.Config, c.Logger)
	serverSvc := servers.NewService(c.Pool, c.Bus, hub, c.Config, c.Logger)
	serverSvc.SetBox(c.Box) // seals the team's MaxMind licence key (request-origin geolocation)
	// OCI registries: external (sealed credentials) and the team's slice of the
	// built-in registry (a token for one repository, minted at send time).
	registrySvc := registries.NewService(registries.Deps{Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Dispatcher: hub, Logger: c.Logger, BuiltinURL: c.Config.Instance.RegistryURL})

	// Tailscale workspace integration and the server-level proxy (certificates,
	// upstream policy, tailnet, "Deploy proxy").
	tailscaleSvc := tailscale.NewService(c.Pool, c.Box, c.Jobs, c.Logger)
	serverSvc.SetJobs(c.Jobs)
	serverSvc.SetTailnetDevices(tailscaleSvc)
	// External secret managers (Vault / OpenBao, Infisical): `${{vault:…}}`
	// references are fetched when a command is sent and never stored.
	secretmgrSvc := secretmgr.NewService(c.Pool, c.Box, c.Logger)
	extSecrets := services.NewExtSecrets(secretmgrSvc)
	// Cloudflare: team connection, DNS records of service domains (A/AAAA or tunnel
	// CNAME), per-domain proxy setting and live DNS status. The domain, service,
	// deployment and server packages call it through small hooks.
	cloudflareSvc := cloudflare.NewService(c.Pool, c.Box, c.Jobs, c.Logger)
	cloudflareSvc.SetNotifier(notifySvc)
	serverSvc.SetDNS(cloudflareSvc)
	// Cloudflare Tunnel per server: a cloudflared connector next to the proxy. The
	// Cloudflare integration asks it for the tunnel CNAME of a server, the hub
	// reports the connector's first connection and revoking a server tears it down.
	tunnelSvc := servertunnel.NewService(servertunnel.Deps{
		Pool: c.Pool, Bus: c.Bus, Dispatcher: hub, Jobs: c.Jobs, Cloudflare: servertunnel.Adapter{S: cloudflareSvc}, Logger: c.Logger,
	})
	cloudflareSvc.SetTunnelTargets(tunnelSvc)
	cloudflareSvc.SetDeleteGate(tunnelSvc.DeleteGate)
	hub.SetTunnelObserver(tunnelSvc)
	serverSvc.AddRevokeHook(tunnelSvc.ServerRevoked)
	// Managed tailnet server mesh: server nodes and link nodes joined to the team's
	// tailnet, the ACL generated from them, and the Provider replicas and
	// cross-server service links build on. It sees every heartbeat and every
	// revoked server.
	meshSvc := mesh.NewService(mesh.Deps{Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Dispatcher: hub, Tailscale: tailscaleSvc, Logger: c.Logger})
	hub.SetMeshObserver(meshSvc)
	serverSvc.AddRevokeHook(meshSvc.ServerRevoked)
	proxySvc := proxyserver.NewService(proxyserver.Deps{
		Pool: c.Pool, Bus: c.Bus, Dispatcher: hub, Box: c.Box, Tailscale: tailscaleSvc, Logger: c.Logger, Admins: c.Auth,
		Routes: func(ctx context.Context, q *db.Queries, serverID uuid.UUID) ([]agentprotocol.Route, error) {
			return domains.ServerRoutes(ctx, q, serverID, uuid.Nil)
		},
	})
	// Cloudflare origin lock: a changed IP list, Authenticated Origin Pulls state or
	// per-domain setting re-sends the server's route table (no redeploy).
	cloudflareSvc.SetRoutePusher(proxySvc)
	// The Headscale platform service accepts internal networks and the public address of every
	// server: a server that is added, revoked or changes address re-sends the
	// route tables of the servers that run platform services.
	serverSvc.SetPlatformRoutes(domains.PlatformRouteSync{Routes: proxySvc})
	// DNS-01 certificates: provider credentials, per-server resolvers, wildcard certificates.
	dnsSvc := dns01.NewService(c.Pool, c.Bus, c.Box)
	// Dashboard access: the allow list and DNS-01 certificate of the dashboard's
	// system route. Its stored settings are loaded at start; the installer's
	// DNS-01 provider is set up in the background once the local server is online.
	dashboardSvc := dashboardaccess.NewService(dashboardaccess.Deps{Pool: c.Pool, Config: c.Config, Admins: c.Auth, Routes: proxySvc, Logger: c.Logger})
	statusPages := statuspages.NewService(statuspages.Deps{Pool: c.Pool, Bus: c.Bus, Routes: proxySvc, DNS: cloudflareSvc, Logger: c.Logger})
	// Public status pages: the status listener's handler renders published pages by
	// Host (served by cmd/server on NEBULACTRL_STATUS_LISTEN); it rebuilds its cache
	// on status_page.updated and uptime.state.changed events.
	statusPublic := statuspublic.New(statuspublic.Options{Pages: statusPages, Bus: c.Bus, Logger: c.Logger})
	m.Status = statusPublic
	m.Start = append(m.Start, func(ctx context.Context) error {
		go statusPublic.Watch(ctx)
		return nil
	})
	dashboardSeeder := dashboardaccess.NewSeeder(dashboardaccess.SeederDeps{
		Pool: c.Pool, Service: dashboardSvc, DNS: dnsSvc, Proxy: proxySvc, Config: c.Config, Logger: c.Logger,
	})
	m.Start = append(m.Start, func(ctx context.Context) error {
		if err := dashboardSvc.Load(ctx); err != nil {
			return err
		}
		go dashboardSeeder.Run(ctx)
		return nil
	})

	// Service domain.
	serviceSvc := services.NewService(c.Pool, c.Bus, c.Box, hub, c.Logger)
	serviceSvc.SetExtSecrets(extSecrets)
	variableSvc := variables.NewService(c.Pool, c.Bus, c.Box)
	domainSvc := domains.NewService(c.Pool, c.Bus, c.Jobs, c.Box)
	routeSvc := proxyroutes.NewService(c.Pool, c.Bus, c.Jobs, hub, serviceSvc, c.Logger)
	serviceSvc.SetRouteHook(routeSvc)
	domainSvc.SetDNS(cloudflareSvc)
	serviceSvc.SetDNSHook(cloudflareSvc)
	deploymentSvc := deployments.NewService(deployments.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Dispatcher: hub, Follower: hub, Services: serviceSvc, Logger: c.Logger,
		Tailnet: proxySvc, Notifier: notifySvc, ExtSecrets: extSecrets,
	})
	deploymentSvc.SetDNS(cloudflareSvc)
	deploymentSvc.SetRegistries(registryProvider{registrySvc}) // build servers push images to, and targets pull from, registries
	deploymentSvc.SetResync(hub)
	// Per-service tailnet: a Tailscale/Headscale sidecar per service. It plugs into
	// service config validation, deployments (payload, node key, result) and the
	// agent callbacks (observed state, command results).
	tailnetSvc := tailnet.NewService(tailnet.Deps{Pool: c.Pool, Bus: c.Bus, Dispatcher: hub, Tailscale: tailscaleSvc, Logger: c.Logger})
	serviceSvc.SetTailnetHook(tailnetSvc)
	deploymentSvc.SetServiceTailnet(tailnetSvc)
	// Cross-server service links: canvas edges and variable references to a
	// service on another server open mesh grants (links are added only by explicit
	// actions: a consumer's deploy or a confirmed edge); deployments gate on them.
	meshLinksSvc := meshlinks.NewService(c.Pool, c.Box, c.Bus, meshSvc, c.Logger)
	meshLinksSvc.SetReferenceGrace(c.Config.Timers.MeshLinkGrace)
	serviceSvc.SetMeshLinker(meshLinksSvc)
	deploymentSvc.SetMesh(meshLinksSvc)
	reconcilerSvc := reconciler.New(c.Pool, c.Bus, c.Jobs, deploymentSvc, c.Logger)
	reconcilerSvc.SetGeo(serverSvc)
	// Log history is stored by the agents; this proxies queries to them and keeps
	// the retention settings and the log drains the agents ship to.
	logsSvc := logs.NewService(logs.Deps{Pool: c.Pool, Bus: c.Bus, Box: c.Box, Agents: hub, Redactor: serviceSvc, Secrets: serviceSvc, Logger: c.Logger})
	reconcilerSvc.SetLogs(logsSvc)
	// Service health alerts: a crash, an unhealthy check or a restart loop seen in
	// the observed reports becomes one incident, announced once with the last log
	// lines and closed with a recovery message (thresholds: Team settings → Notifications).
	alertsSvc := svcalerts.NewService(svcalerts.Deps{Pool: c.Pool, Bus: c.Bus, Notifier: notifySvc, Logs: serviceSvc, Logger: c.Logger})
	reconcilerSvc.SetAlerts(alertsSvc)
	// Uptime: the agents' probe results become each service's state, outages and
	// the uptime.down / uptime.up alerts; a monitor's on/off reaches the agents in
	// Reconcile.Uptime.
	uptimeSvc := uptime.NewService(uptime.Deps{Pool: c.Pool, Bus: c.Bus, Notifier: notifySvc, Resync: hub, Logger: c.Logger, Interval: c.Config.Timers.UptimeInterval})
	reconcilerSvc.SetUptime(uptimeSvc)
	hub.SetLogStatusSink(logsSvc)

	// Git providers: connections, webhooks, auto-deploy on push, commit statuses.
	// Deployments pull short-lived clone credentials from it.
	gitSvc := gitproviders.NewService(gitproviders.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Config: c.Config, Services: serviceSvc, Deployer: deploymentSvc, Logger: c.Logger,
	})
	deploymentSvc.SetGitAuth(gitSvc)
	serviceSvc.SetGitLinkHook(gitSvc)
	// SSH deploy keys and pinned host keys of git services with an ssh:// URL.
	deployKeySvc := deploykeys.NewService(c.Pool, c.Bus, c.Box)
	// Deploy hooks: a per-service URL that deploys like a manual deploy (CI, registries).
	deployHookSvc := deployhooks.NewService(deployhooks.Deps{Pool: c.Pool, Jobs: c.Jobs, Config: c.Config, Deployer: deploymentSvc, Logger: c.Logger})
	deploymentSvc.SetSSHKeys(deployKeySvc)
	// .env.example prefill: a git service's template env files are read before
	// its first deploy (git provider API, else a scan-only agent command), when
	// its source changes and after every push to the tracked branch.
	variableSvc.SetEnvTemplates(variables.EnvTemplateDeps{Git: gitSvc, Agent: deploymentSvc, Jobs: c.Jobs, Logger: c.Logger})
	serviceSvc.AddSourceHook(variableSvc)
	gitSvc.SubscribePush(variableSvc.OnPush)
	canvasSvc := canvas.NewService(c.Pool, c.Bus)
	scalingSvc := scaling.NewService(c.Pool, c.Bus, hub, serviceSvc, c.Logger)
	maintenanceSvc := maintenance.NewService(c.Pool, c.Bus, hub, serviceSvc, c.Logger)
	networkSvc := networks.NewService(c.Pool, c.Bus)

	// One-click template catalog (embedded, validated at startup; a bad template is
	// logged and skipped).
	catalog, catalogErrs := templates.LoadFS(catalogfs.FS, "catalog")
	for _, err := range catalogErrs {
		c.Logger.Error("template catalog: invalid template skipped", "err", err)
	}
	databaseSvc := databases.NewService(c.Pool, c.Bus, c.Box, hub, serviceSvc, c.Logger)
	databaseSvc.SetBrokenAfter(c.Config.Timers.ReplicaBrokenAfter)
	databaseSvc.SetExtSecrets(extSecrets)
	databases.RegisterExports()
	databaseSvc.SetReplicas(databases.ReplicaDeps{
		Mesh: meshSvc, Links: meshLinkReconciler{s: meshLinksSvc}, Deployer: deploymentSvc, Jobs: c.Jobs, Resync: hub, Notify: replicaNotifier(notifySvc),
	})
	serviceSvc.SetDatabaseGuard(databaseSvc)
	deploymentSvc.SetDatabaseHook(databaseSvc)
	serverSvc.AddRevokeHook(databaseSvc.ServerRevoked)
	templateSvc := templates.NewService(templates.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Catalog: catalog, Services: serviceSvc,
		Variables: templates.VariablesAdapter{S: variableSvc}, Databases: templates.DatabasesAdapter{S: databaseSvc},
		Ports: templates.PortsAdapter{}, Deploy: deploymentSvc, Materialize: templates.MaterializeWith, Logger: c.Logger,
	})
	// Docker Compose import wizard: a Compose file becomes staged services.
	composeSvc := composeimport.NewService(composeimport.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Services: serviceSvc, Variables: templates.VariablesAdapter{S: variableSvc},
		Databases: databaseSvc, Ports: templates.PortsAdapter{}, Deploy: deploymentSvc, Git: gitSvc, Networks: networkSvc,
		Scaling: scalingSvc, Logger: c.Logger,
	})

	// Coolify import: a Coolify v4 instance becomes staged projects, services and managed databases.
	coolifySvc := coolify.NewService(coolify.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Services: serviceSvc, Databases: databaseSvc, Deploy: deploymentSvc, Git: gitSvc,
		Box: c.Box, Logger: c.Logger,
	})

	// Database backups: destinations, schedules, retention, restores.
	backupSvc := backups.NewService(backups.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Dispatcher: hub, Deployer: deploymentSvc, Provisioner: backups.NewDatabaseProvisioner(databaseSvc),
		Streams: backups.HubStreams{Hub: hub}, Logger: c.Logger, ExtSecrets: extSecrets, DumpMaxBytes: c.Config.DumpMaxBytes,
	})
	// Control-plane backup: a scheduled pg_dump of this database to one
	// instance-wide destination. Runs on the control plane, not through an agent.
	instanceBackupSvc := backups.NewInstanceService(backups.InstanceDeps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Admins: c.Auth, DatabaseURL: c.Config.DatabaseURL,
		BackupDir: c.Config.BackupDir, KeyFingerprint: backups.KeyFingerprint(c.Config.SecretKey), Logger: c.Logger,
	})

	// Cron tasks, one-off commands and their run history.
	taskSvc := tasks.NewService(tasks.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Dispatcher: hub, Redactor: serviceSvc, Notifier: notifySvc, Logger: c.Logger, ExtSecrets: extSecrets,
	})

	// Pull-request previews: an environment per open PR of a connected
	// repository, fed by the git providers' verified PR events.
	previewSvc := previews.NewService(previews.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Config: c.Config, Services: serviceSvc, Databases: databaseSvc,
		Git: previewGit{gitSvc}, Deployer: previewDeployer{deploymentSvc}, Seeder: previewSeeder{taskSvc}, Notifier: notifySvc, Logger: c.Logger,
	})
	previewGit{gitSvc}.subscribe(previewSvc)

	// Deleting a project with everything in it reuses the service delete (with
	// volumes), the preview teardown and the status page detach.
	projectSvc.SetCascade(projects.CascadeDeps{
		DeleteService: func(ctx context.Context, id uuid.UUID, slug string) error {
			return serviceSvc.Delete(ctx, id, services.DeleteInput{Confirm: slug, DeleteVolumes: true})
		},
		TeardownPreview:   previewSvc.Teardown,
		DetachStatusPages: statusPages.DetachProject,
	})

	// Clone a service or an environment, move a service between environments.
	cloneSvc := clone.NewService(clone.Deps{
		Pool: c.Pool, Bus: c.Bus, Box: c.Box, Jobs: c.Jobs, Services: serviceSvc, Databases: databaseSvc, Git: previewGit{gitSvc}, Logger: c.Logger,
	})

	// Combine several git services into one service with several processes.
	combineSvc := combine.NewService(combine.Deps{
		Pool: c.Pool, Bus: c.Bus, Box: c.Box, Jobs: c.Jobs, Services: serviceSvc, Domains: domainSvc, Deployments: deploymentSvc,
		Dispatcher: hub, Logger: c.Logger,
	})

	// Fleet overview and server drains.
	fleetSvc := fleet.NewService(c.Pool, c.Bus, c.Jobs, serverSvc, serviceSvc, deploymentSvc, c.Logger)

	// Traffic analytics: ingest of agent aggregates, rollups, reports.
	analyticsSvc := analytics.NewService(c.Pool, c.Bus, c.Logger)

	// Web terminal and file browser: exec and file access in replica containers,
	// relayed over the agent connection.
	terminalSvc := terminal.NewService(terminal.Deps{Pool: c.Pool, Agents: terminal.HubAgents{Hub: hub}, Config: c.Config, Logger: c.Logger})

	// The hub calls back into the domain for secrets, results and redaction.
	handler := &agentHandler{deps: deploymentSvc, rec: reconcilerSvc, svcs: serviceSvc, scal: scalingSvc, bk: backupSvc, dbs: databaseSvc, proxy: proxySvc, reg: registrySvc}
	handler.tasks = taskSvc
	handler.tn = tailnetSvc
	handler.tunnel = tunnelSvc
	handler.mesh = meshSvc
	// Scheduled Docker cleanup of servers: daily and at the disk alert, never
	// deleting a current or rollback-eligible release's image.
	cleanupSvc := servercleanup.NewService(servercleanup.Deps{Pool: c.Pool, Bus: c.Bus, Dispatcher: hub, Notifier: notifySvc, Logger: c.Logger})
	handler.cleanup = cleanupSvc
	// Host hardening: a weekly Lynis audit of each server and the curated safe
	// fixes (never SSH, the firewall or accounts).
	hardeningSvc := serverhardening.NewService(serverhardening.Deps{Pool: c.Pool, Bus: c.Bus, Dispatcher: hub, Logger: c.Logger})
	handler.hardening = hardeningSvc
	// Agent self-upgrade: agents behind the control plane replace their own
	// binary with the one this control plane serves.
	agentUpgradeSvc := agentupgrade.NewService(agentupgrade.Deps{Pool: c.Pool, Bus: c.Bus, Dispatcher: hub, Binaries: hub, Jobs: c.Jobs, Admins: c.Auth, Logger: c.Logger})
	handler.upgrades = agentUpgradeSvc
	hub.OnHello(agentUpgradeSvc.OnHello)
	// The control plane's own update: the daily release check, the notice and
	// the instance admin's one-click update through the local server's agent.
	updateSvc := sysupdate.NewService(sysupdate.Deps{
		Pool: c.Pool, Dispatcher: hub, Admins: c.Auth, Config: c.Config.Update, Logger: c.Logger,
	})
	handler.update = updateSvc
	m.Start = append(m.Start, updateSvc.Settle)
	// Server patching: pending OS updates and reboot-required per server, with
	// the instance admin's "Apply updates" and "Reboot".
	patchingSvc := serverpatching.NewService(serverpatching.Deps{Pool: c.Pool, Bus: c.Bus, Dispatcher: hub, Notifier: notifySvc, Logger: c.Logger})
	handler.patching = patchingSvc
	hub.SetTaskLogSink(taskSvc)
	hub.SetSecretSource(handler)
	hub.SetResultHandler(handler)
	hub.SetRedactor(handler)
	hub.SetTrafficSink(analyticsSvc)
	hub.SetProbeSink(uptimeSvc)
	hub.AddSourceScanSink(variableSvc)
	hub.SetNotifier(notifySvc)
	scalingSvc.SetNotifier(notifySvc)
	maintenanceSvc.SetNotifier(notifySvc)
	backupSvc.SetNotifier(backupNotifier(notifySvc))
	instanceBackupSvc.SetNotifier(backupNotifier(notifySvc))

	// Instance services: S3 for NebulaCtrl's own data, the image registry and Loki
	// run once beside the control plane; every team gets an isolated slice (made
	// when the team is, kept 30 days when it is deleted). Servers reach them
	// through the agent's outbound link, so the hub hands their streams to the
	// forwarder. Capable agents are told to listen on the local endpoints.
	instSvc, instStart, central := wireInstance(c, hub, notifySvc)
	logsSvc.SetCentralLogs(central)
	teamSvc.SetSlices(instSvc)
	// Backups to the built-in S3: a team's slice for its own backups, the system
	// bucket for the control plane's.
	backupSvc.SetBuiltinStorage(instSvc)
	instanceBackupSvc.SetBuiltinStorage(instSvc, c.Config.Instance.S3URL)
	reconcilerSvc.SetInstanceEndpoints(instSvc.Enabled())
	m.Start = append(m.Start, instStart)

	// Platform service: Headscale, the tailnet control server a team can run for
	// itself, wired into the Tailscale integration.
	platformSvc := platform.NewService(platform.Deps{
		Pool: c.Pool, Bus: c.Bus, Jobs: c.Jobs, Box: c.Box, Services: serviceSvc, Deploy: deploymentSvc,
		Backups: backupSvc, Tailnet: tailscaleSvc, Mesh: meshSvc, Agents: terminal.HubAgents{Hub: hub}, Logger: c.Logger,
	})
	// Deleting a team runs every project, platform service, server and
	// integration of it through its own domain.
	teamSvc.SetTeardown(&teamTeardown{
		q: db.New(c.Pool), projects: projectSvc, platform: platformSvc, servers: serverSvc, pages: statusPages,
		cloudflare: cloudflareSvc, tailscale: tailscaleSvc,
	})

	// nebula.toml: validate, plan, apply and export of a project file onto the
	// services, variables, domains and settings the dashboard edits too.
	configSvc := configsync.NewService(configsync.Deps{
		Pool: c.Pool, Bus: c.Bus, Box: c.Box, Logger: c.Logger, PublicURL: c.Config.PublicURL,
		Services: serviceSvc, Variables: variableSvc, Domains: domainSvc, Databases: databaseSvc, Routes: routeSvc, Networks: networkSvc,
		Scaling: scalingSvc, Tasks: taskSvc, Backups: backupSvc, Uptime: uptimeSvc, Git: gitSvc, Cloudflare: cloudflareSvc, Deploy: deploymentSvc,
		CloneURL: gitSvc.CloneURLFor,
	})
	m.Start = append(m.Start, func(ctx context.Context) error {
		go configSvc.RunPendingLoop(ctx)
		return nil
	})

	// nebula.toml and git: the binding of an environment to a file, the read of
	// the file on push (before the push deploys, so one commit lands code and
	// config together), on source changes and on build checkouts.
	gs := configsync.NewGitSync(configsync.GitSyncDeps{
		Pool: c.Pool, Bus: c.Bus, Repos: configsync.ProviderRepos(gitSvc), Agent: deploymentSvc, Logger: c.Logger,
	})
	gs.SetPlanner(configSvc)
	configSvc.SetBinding(gs)
	gitSvc.SetPushHook(gs)
	serviceSvc.AddSourceHook(gs)
	hub.AddSourceScanSink(gs)

	// Background jobs.
	hub.RegisterJobs(c.Jobs)
	servers.RegisterJobs(c.Jobs)
	servers.RegisterRemovalJob(c.Jobs, serverSvc)
	teams.RegisterJobs(c.Jobs, teamSvc)
	tailscale.RegisterJobs(c.Jobs, tailscaleSvc)
	cloudflare.RegisterJobs(c.Jobs, cloudflareSvc)
	servertunnel.RegisterJobs(c.Jobs, tunnelSvc)
	servercleanup.RegisterJobs(c.Jobs, cleanupSvc)
	serverhardening.RegisterJobs(c.Jobs, hardeningSvc)
	agentupgrade.RegisterJobs(c.Jobs, agentUpgradeSvc)
	sysupdate.RegisterJobs(c.Jobs, updateSvc)
	serverpatching.RegisterJobs(c.Jobs, patchingSvc)
	proxyserver.RegisterJobs(c.Jobs, notifySvc)
	analytics.RegisterJobs(c.Jobs, analyticsSvc)
	uptime.RegisterJobs(c.Jobs, uptimeSvc)
	deployments.RegisterJobs(c.Jobs, deploymentSvc)
	releases.RegisterJobs(c.Jobs, releases.NewService(c.Pool, c.Logger)) // drops release secrets outside the rollback window
	domains.RegisterJobs(c.Jobs, domainSvc, c.Logger)
	reconciler.RegisterJobs(c.Jobs, reconcilerSvc)
	scaling.RegisterJobs(c.Jobs, scalingSvc)
	combine.RegisterJobs(c.Jobs, combineSvc)
	backups.RegisterJobs(c.Jobs, backupSvc)
	databases.RegisterReplicaJobs(c.Jobs, databaseSvc)
	backups.RegisterInstanceJobs(c.Jobs, instanceBackupSvc)
	tasks.RegisterJobs(c.Jobs, taskSvc)
	fleet.RegisterJobs(c.Jobs, fleetSvc)
	variables.RegisterJobs(c.Jobs, variableSvc)
	notifications.RegisterJobs(c.Jobs, notifySvc)
	gitproviders.RegisterJobs(c.Jobs, gitSvc)
	deployhooks.RegisterJobs(c.Jobs, deployHookSvc)
	previews.RegisterJobs(c.Jobs, previewSvc)
	platform.RegisterJobs(c.Jobs, platformSvc)
	mesh.RegisterJobs(c.Jobs, meshSvc)
	platform.RegisterMigrationJobs(c.Jobs, platformSvc)
	meshlinks.RegisterJobs(c.Jobs, meshLinksSvc)
	registries.RegisterJobs(c.Jobs, registrySvc)
	instsvc.RegisterJobs(c.Jobs, instSvc)

	// The CLI binaries, their SHA256SUMS and cli.sh: public, outside /api/v1, like /install.sh.
	downloadsHandler := downloads.New(c.Config.CLIBinariesDir, c.Config.PublicURL, c.Logger)
	m.Mount = append(m.Mount, hub.Mount, gitSvc.Mount, deployHookSvc.Mount, downloadsHandler.Mount)
	// The JSON Schema of nebula.toml: public, with the origin substituted and an ETag.
	schemaHandler := webui.SchemaHandler(projectconfig.Schema(), webui.WithPublicURL(c.Config.PublicURL))
	m.Mount = append(m.Mount, func(r chi.Router) { r.Handle(webui.SchemaPath, schemaHandler) })
	m.MountAPI = append(m.MountAPI, gitSvc.MountAPI, terminalSvc.MountAPI, backupSvc.MountAPI)

	// MCP server (404 while switched off: NEBULACTRL_MCP_ENABLED, else the
	// instance admin's switch in the web UI); same services as REST.
	mcpSwitch := mcpserver.NewSwitch(c.Config, c.Pool, c.Auth)
	m.Start = append(m.Start, mcpSwitch.Load)
	mcpHandler := mcpserver.New(mcpserver.Deps{
		Templates: templateSvc, Backups: backupSvc, Config: c.Config, Logger: c.Logger, Pool: c.Pool, Auth: c.Auth,
		Tasks: taskSvc, ConfigSync: configSvc,
		Projects: projectSvc, Environments: envSvc, Servers: serverSvc, Services: serviceSvc, Deployments: deploymentSvc,
		Variables: variableSvc, Domains: domainSvc, Databases: databaseSvc, Audit: auditSvc, Switch: mcpSwitch,
	})
	m.MCP = mcpHandler
	m.Mount = append(m.Mount, mcpHandler.Mount)
	m.Register = append(m.Register,
		func(h huma.API) { tokens.Register(h, tokenSvc) },
		func(h huma.API) { teams.Register(h, teamSvc, c.Sessions) },
		func(h huma.API) { passwordreset.Register(h, c.Auth, c.Sessions) },
		func(h huma.API) { projects.Register(h, projectSvc) },
		func(h huma.API) { environments.Register(h, envSvc) },
		func(h huma.API) { audit.Register(h, auditSvc) },
		func(h huma.API) { prodguard.Register(h, guard) },
		func(h huma.API) { servers.Register(h, serverSvc) },
		func(h huma.API) { buildplacement.Register(h, buildplacement.NewTeamService(c.Pool)) },
		func(h huma.API) { tailscale.Register(h, tailscaleSvc) },
		func(h huma.API) { secretmgr.Register(h, secretmgrSvc) },
		func(h huma.API) { cloudflare.Register(h, cloudflareSvc) },
		func(h huma.API) { servertunnel.Register(h, tunnelSvc) },
		func(h huma.API) { servercleanup.Register(h, cleanupSvc) },
		func(h huma.API) { serverhardening.Register(h, hardeningSvc) },
		func(h huma.API) { agentupgrade.Register(h, agentUpgradeSvc) },
		func(h huma.API) { sysupdate.Register(h, updateSvc) },
		func(h huma.API) { serverpatching.Register(h, patchingSvc) },
		func(h huma.API) { tailnet.Register(h, tailnetSvc) },
		func(h huma.API) { proxyserver.Register(h, proxySvc) },
		func(h huma.API) { dns01.Register(h, dnsSvc) },
		func(h huma.API) { dashboardaccess.Register(h, dashboardSvc) },
		func(h huma.API) { services.Register(h, serviceSvc) },
		func(h huma.API) { variables.Register(h, variableSvc) },
		func(h huma.API) { domains.Register(h, domainSvc) },
		func(h huma.API) { proxyroutes.Register(h, routeSvc) },
		func(h huma.API) { deployments.Register(h, deploymentSvc) },
		func(h huma.API) { canvas.Register(h, canvasSvc) },
		func(h huma.API) { templates.Register(h, templateSvc) },
		func(h huma.API) { composeimport.Register(h, composeSvc) },
		func(h huma.API) { coolify.Register(h, coolifySvc) },
		func(h huma.API) { scaling.Register(h, scalingSvc) },
		func(h huma.API) { combine.Register(h, combineSvc) },
		func(h huma.API) { maintenance.Register(h, maintenanceSvc) },
		func(h huma.API) { networks.Register(h, networkSvc) },
		func(h huma.API) { backups.Register(h, backupSvc) },
		func(h huma.API) { backups.RegisterVolume(h, backupSvc) },
		func(h huma.API) { backups.RegisterInstance(h, instanceBackupSvc) },
		func(h huma.API) { tasks.Register(h, taskSvc) },
		func(h huma.API) { databases.Register(h, databaseSvc) },
		func(h huma.API) { fleet.Register(h, fleetSvc) },
		func(h huma.API) { analytics.Register(h, analyticsSvc) },
		func(h huma.API) { terminal.Register(h, terminalSvc) },
		func(h huma.API) { notifications.Register(h, notifySvc) },
		func(h huma.API) { svcalerts.Register(h, alertsSvc) },
		func(h huma.API) { uptime.Register(h, uptimeSvc) },
		func(h huma.API) { statuspages.Register(h, statusPages) },
		func(h huma.API) { logs.Register(h, logsSvc) },
		func(h huma.API) { gitproviders.Register(h, gitSvc) },
		func(h huma.API) { deploykeys.Register(h, deployKeySvc) },
		func(h huma.API) { deployhooks.Register(h, deployHookSvc) },
		func(h huma.API) { previews.Register(h, previewSvc) },
		func(h huma.API) { clone.Register(h, cloneSvc) },
		func(h huma.API) { platform.Register(h, platformSvc) },
		func(h huma.API) { mesh.Register(h, meshSvc) },
		func(h huma.API) { meshlinks.Register(h, meshLinksSvc) },
		func(h huma.API) { registries.Register(h, registrySvc) },
		func(h huma.API) { instsvc.Register(h, instSvc) },
		func(h huma.API) { configsync.RegisterHTTP(h, configSvc, gs, envSvc) },
		func(h huma.API) { mcpserver.RegisterSystem(h, c.Config, mcpSwitch) },
	)
	// Embedded web app (nil when the binary was built without `make build`).
	m.Fallback = webui.Handler(webui.WithPublicURL(c.Config.PublicURL))

	return m
}
