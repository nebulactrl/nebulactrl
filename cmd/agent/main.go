// Command nebulactrl-agent is the per-host NebulaCtrl agent: it enrolls with the
// control plane, keeps an outbound authenticated websocket open, manages the
// host's Traefik proxy and executes deployment commands against the local Docker
// daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/nebulactrl/nebulactrl/internal/agent/build"
	"github.com/nebulactrl/nebulactrl/internal/agent/compose"
	"github.com/nebulactrl/nebulactrl/internal/agent/conn"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/engine"
	"github.com/nebulactrl/nebulactrl/internal/agent/localproxy"
	"github.com/nebulactrl/nebulactrl/internal/agent/loghistory"
	"github.com/nebulactrl/nebulactrl/internal/agent/mesh"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/internal/agent/selfupgrade"
	"github.com/nebulactrl/nebulactrl/internal/agent/stats"
	"github.com/nebulactrl/nebulactrl/internal/agent/store"
	"github.com/nebulactrl/nebulactrl/internal/agent/traffic"
	"github.com/nebulactrl/nebulactrl/internal/agent/tunnel"
	"github.com/nebulactrl/nebulactrl/internal/agent/uptime"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

type options struct {
	URL            string
	Token          string
	StateDir       string
	ProxyAdminURL  string
	ProxyAdminPort int
	ProxyHTTPPort  int
	ProxyHTTPSPort int
	InsecureDev    bool
	BackupDir      string
	AgentContainer string
	NetworkPool    string
	NetworkPrefix  int
	UptimeInterval time.Duration
}

func main() {
	if err := newCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func newCommand() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:   "nebulactrl-agent",
		Short: "NebulaCtrl host agent",
		Long: "nebulactrl-agent connects this Docker host to a NebulaCtrl control plane.\n\n" +
			"Every flag can also be set through the environment as NEBULACTRL_<FLAG> with dashes\n" +
			"replaced by underscores (e.g. --state-dir is NEBULACTRL_STATE_DIR); --token is\n" +
			"NEBULACTRL_ENROLL_TOKEN. Empty environment values are ignored.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			return applyEnv(cmd.Flags())
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := newLogger()
			err := run(cmd.Context(), log, o)
			if errors.Is(err, errUpgradeRestart) || errors.Is(err, selfupgrade.ErrRestart) {
				log.Info("exiting to start the agent binary now on disk")
				os.Exit(exitUpgradeRestart)
			}
			if err != nil {
				log.Error("agent exiting", "err", err)
			}
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.URL, "url", "", "control plane base URL (https://… or wss://…; http/ws only with --insecure-dev) [required]")
	f.StringVar(&o.Token, "token", "", "one-time enrollment token (used only when no identity is stored)")
	f.StringVar(&o.StateDir, "state-dir", "/var/lib/nebulactrl-agent", "directory for agent.db and build workspaces")
	f.StringVar(&o.ProxyAdminURL, "proxy-admin-url", "", "URL of the proxy's admin entrypoint as seen from the agent (default http://127.0.0.1:<proxy-admin-port>; use http://nebulactrl-traefik:9000 when the agent runs in a container on the edge network)")
	f.IntVar(&o.ProxyAdminPort, "proxy-admin-port", agentprotocol.ProxyAdminPort, "host port the proxy admin entrypoint is published on (127.0.0.1 only; not published when --proxy-admin-url targets the proxy container)")
	f.IntVar(&o.ProxyHTTPPort, "proxy-http-port", 80, "host port mapped to the proxy's port 80")
	f.IntVar(&o.ProxyHTTPSPort, "proxy-https-port", 443, "host port mapped to the proxy's port 443 (tcp and udp)")
	f.StringVar(&o.BackupDir, "backup-dir", "", "directory local backup destinations are stored under (default <state-dir>/backups; point it at a mounted disk for more space)")
	f.BoolVar(&o.InsecureDev, "insecure-dev", false, "allow plaintext http/ws control plane URLs (development only)")
	f.StringVar(&o.AgentContainer, "agent-container", "", "name or ID of the container the agent runs in, when it runs in one (default: detected from the hostname); the agent joins the networks it health-checks through")
	f.StringVar(&o.NetworkPool, "network-pool", netpool.DefaultPool, "IPv4 address range (CIDR) the agent allocates the subnets of its environment and shared networks from; must not overlap your LAN, VPN or other Docker networks")
	f.IntVar(&o.NetworkPrefix, "network-prefix-len", netpool.DefaultPrefixLen, "prefix length of each allocated network subnet (24 = 253 containers per network, 256 networks in a /16 pool)")
	f.DurationVar(&o.UptimeInterval, "uptime-interval", agentprotocol.UptimeInterval, "how often the agent probes each service for uptime (at least 1s; a test and diagnostic knob, leave the default in production)")
	cmd.AddCommand(newHardeningCommand())
	return cmd
}

// envName maps a flag name to its environment variable.
func envName(flag string) string {
	if flag == "token" {
		return "NEBULACTRL_ENROLL_TOKEN"
	}
	return "NEBULACTRL_" + strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
}

// applyEnv fills every flag that was not set on the command line from its
// environment variable. Empty values never override defaults.
func applyEnv(fs *pflag.FlagSet) error {
	var firstErr error
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Changed || f.Name == "help" || f.Name == "version" {
			return
		}
		if v := os.Getenv(envName(f.Name)); v != "" {
			if err := fs.Set(f.Name, v); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", envName(f.Name), err)
			}
		}
	})
	return firstErr
}

// hostname is the process hostname: HOSTNAME, else the OS hostname. Inside a
// container it starts the container ID, which is how the agent finds itself.
func hostname() string {
	if h := os.Getenv("HOSTNAME"); h != "" {
		return h
	}
	h, _ := os.Hostname()
	return h
}

func newLogger() *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(os.Getenv("NEBULACTRL_LOG_LEVEL"))); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})).With("service", "nebulactrl-agent")
}

func run(parent context.Context, log *slog.Logger, o options) error {
	runCtx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancelRun := context.WithCancel(runCtx)
	defer cancelRun()
	restart := &restartRequest{cancel: cancelRun}
	// decommission ends the run when the control plane removed this server and
	// the host is empty (upgrade.go, selfhandler).
	decommission := &restartRequest{cancel: cancelRun}

	eps, err := conn.ParseEndpoints(o.URL, o.InsecureDev)
	if err != nil {
		return err
	}
	if o.UptimeInterval < time.Second {
		return fmt.Errorf("--uptime-interval (NEBULACTRL_UPTIME_INTERVAL) must be at least 1s, got %s", o.UptimeInterval)
	}

	networks, err := netpool.New(o.NetworkPool, o.NetworkPrefix)
	if err != nil {
		return err
	}

	eng, err := docker.NewSDKEngine()
	if err != nil {
		return err
	}
	defer eng.Close()
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = eng.Ping(pctx)
	cancel()
	if err != nil {
		return fmt.Errorf("docker daemon unreachable (is Docker installed and running, and is DOCKER_HOST correct?): %w", err)
	}

	st, err := store.Open(o.StateDir)
	if err != nil {
		return err
	}
	defer st.Close()

	px, err := proxy.NewManager(proxy.Config{
		Engine:    eng,
		Store:     st,
		AdminURL:  o.ProxyAdminURL,
		AdminPort: o.ProxyAdminPort,
		HTTPPort:  o.ProxyHTTPPort,
		HTTPSPort: o.ProxyHTTPSPort,
		Logger:    log,
		Pages:     true,
		// Tailnet mode "host": the host's own Tailscale (interface in 100.64.0.0/10).
		HostTailscale: stats.DetectHostTailscale,
	})
	if err != nil {
		return err
	}

	// The Cloudflare Tunnel connector: observed by heartbeats, driven by the engine.
	tun := tunnel.New(eng, log)

	// The server mesh nodes (server node + link nodes): observed by heartbeats,
	// driven by the engine. Link nodes need the TUN device.
	meshMgr := mesh.New(eng, log)
	meshMgr.TunDevice = stats.TunDevice

	// The instance service listeners (127.0.0.1 only): started when the control
	// plane's reconcile asks for them, carried over the agent's own connection.
	var client *conn.Client
	lp := localproxy.New(localproxy.Config{
		Dialer: localproxy.DialerFunc(func(ctx context.Context, service string) (net.Conn, error) {
			return client.OpenTunnel(ctx, service)
		}),
		Logger: log,
	})
	defer lp.Close()

	// Docker Compose runs compose stacks; a host without the plugin still
	// starts the agent (only stack deploys fail, and CapCompose is not offered).
	composeRunner := compose.New()
	// Request-origin geolocation: the control plane's reconcile frames configure
	// the manager, which keeps the GeoIP database in <state-dir>/geoip; its
	// state rides on every heartbeat.
	geo := traffic.NewGeoManager(filepath.Join(o.StateDir, "geoip"), log)
	// Facts are collected before the proxy starts so port80Free/port443Free describe the host;
	// they probe the ports the proxy will publish (a server setting overrides the flags).
	webHTTP, webHTTPS := px.WebPorts()
	collector := stats.New(stats.Options{
		// Observed instance service listeners (offered with CapInstanceTunnel).
		EndpointStatus: lp.Status,
		GeoStatus:      geo.Status,
		Engine:         eng,
		StateDir:       o.StateDir,
		HTTPPort:       webHTTP,
		HTTPSPort:      webHTTPS,
		ProxyStatus:    px.Status,
		// Observed tailnet state (managed node status or host detection).
		TailnetStatus: px.TailnetStatus,
		// Observed DNS-01 wildcard and domain certificates.
		AcmeStatuses: px.AcmeStatuses,
		// Observed Cloudflare Tunnel connector.
		TunnelStatus: tun.Status,
		// Observed mesh nodes.
		MeshStatus: meshMgr.Status,
		// Docker Compose plugin version (CapCompose when >= 2.24).
		ComposeVersion: composeRunner.Version,
		// docker buildx plugin (CapBuildPush) and the host's QEMU emulation.
		BuildxVersion:  build.BuildxVersion,
		AgentContainer: o.AgentContainer,
	})
	// Self-upgrade: offered only where the agent can replace its own binary.
	upgrader, err := selfupgrade.New(o.StateDir, version, eps.Base, nil, log)
	if err != nil {
		log.Warn("agent self-upgrade unavailable", "err", err)
		upgrader = nil
	} else if err := upgrader.Supported(); err != nil {
		log.Info("agent self-upgrade unavailable", "reason", err.Error())
	}
	if upgrader != nil {
		if err := upgrader.Start(); err != nil {
			return err // a restored previous binary: restart to run it
		}
	}
	facts := collector.Facts
	if upgrader != nil {
		facts = func(ctx context.Context) agentprotocol.Facts {
			f := collector.Facts(ctx)
			if upgrader.Supported() == nil {
				f.Capabilities = append(slices.Clone(f.Capabilities), agentprotocol.CapAgentUpgrade)
			}
			f.AgentUpgrade = upgrader.Report()
			return f
		}
	}
	enrollFacts := facts(ctx)

	identity, ok, err := st.Identity()
	if err != nil {
		return fmt.Errorf("read identity: %w", err)
	}
	if ok && identity.Revoked && o.Token == "" {
		return conn.ErrRevoked
	}
	if !ok || identity.Revoked {
		hostname, _ := os.Hostname()
		log.Info("enrolling with control plane", "url", eps.Enroll)
		identity, err = conn.Enroll(ctx, st, eps.Enroll, o.Token, hostname, enrollFacts, nil)
		if err != nil {
			return err
		}
		log.Info("enrolled", "server_id", identity.ServerID, "agent_id", identity.AgentID)
	}

	pxCtx, stopProxy := context.WithCancel(ctx)
	defer stopProxy()
	go px.Run(pxCtx)
	go pruneCommandsLoop(ctx, log, st)

	client = conn.New(conn.Config{
		URL:       eps.Connect,
		Identity:  identity,
		Store:     st,
		Version:   version,
		Facts:     facts,
		Heartbeat: collector.Heartbeat,
		Logger:    log,
	})
	backupDir := o.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(o.StateDir, "backups")
	}
	// Log history and drains: every managed container's output is captured into
	// <state-dir>/logs; the control plane queries it and configures the drains.
	logHistory, err := loghistory.New(eng, o.StateDir, client, log)
	if err != nil {
		return fmt.Errorf("open log history: %w", err)
	}
	// Uptime probes: the engine probes every service each UptimeInterval and
	// records the results; the collector keeps them in the store until the
	// control plane acknowledges them.
	uptimeCollector := uptime.NewCollector(uptime.Config{Buffer: st, Sender: client, Logger: log, Interval: o.UptimeInterval})
	deployer, err := engine.New(engine.Config{
		Docker: eng,
		Proxy:  px,
		Builder: &build.Builder{StateDir: o.StateDir, OnSource: func(req build.Request, repoDir, ctxDir string) {
			// Suggest variables from .env.example-style files (never .env itself)
			// and report the project config file (nebula.toml) of the built commit.
			cfgPaths, cfgFiles := build.ScanConfigFiles(repoDir, ctxDir)
			client.SourceScan(agentprotocol.SourceScan{
				ServiceID: req.ServiceID, DeploymentID: req.DeploymentID, Commit: req.CommitSHA,
				Files: build.ScanEnvTemplates(repoDir, ctxDir), ConfigPaths: cfgPaths, ConfigFiles: cfgFiles,
			})
		}},
		Sender:    client,
		Logger:    log,
		BackupDir: backupDir,
		Geo:       geo,
		Endpoints: lp,
		Tunnel:    tun,
		Mesh:      meshMgr,
		Logs:      logHistory,
		Uptime:    uptimeCollector,
		// UptimeInterval (--uptime-interval): the probe period.
		UptimeInterval: o.UptimeInterval,

		AgentContainer: o.AgentContainer,
		Networks:       networks,
		Hostname:       hostname(),
		// Compose stacks: `docker compose` (the plugin is probed, not required)
		// and the directory the stacks' compose files live under.
		Compose:  composeRunner,
		StackDir: filepath.Join(o.StateDir, agentprotocol.StackDirName),
		// A decommission stops the proxy's repair loop before it removes the proxy.
		StopProxy: stopProxy,
	})
	if err != nil {
		return err
	}
	var (
		_ conn.Handler    = (*selfHandler)(nil)
		_ conn.Reconciler = deployer
	)
	handler := &selfHandler{
		Engine: deployer, up: upgrader, inflight: client.InflightCommands, restart: restart.Do,
		decommissioned: decommission, log: log,
	}
	if upgrader != nil {
		// A freshly installed binary has to prove itself: confirmed once it
		// stays connected, rolled back when it does not.
		go func() {
			if err := upgrader.Watch(ctx, client.Connected); errors.Is(err, selfupgrade.ErrRestart) {
				restart.Do()
			} else if err != nil {
				log.Error("agent upgrade rollback failed", "err", err)
			}
		}()
	}
	client.SetHandler(handler)
	client.SetReconciler(deployer)
	go deployer.Run(ctx)
	go logHistory.Run(ctx)

	// Traffic analytics: aggregate the proxy's access log, ship completed minutes.
	tc := traffic.NewCollector(traffic.Config{Engine: eng, Buffer: st, Sender: client, Container: agentprotocol.ProxyContainer, Logger: log, Geo: geo.DB()})
	client.SetTrafficAckHandler(tc.Ack)
	go tc.Run(ctx)
	client.SetProbesAckHandler(uptimeCollector.Ack)
	go uptimeCollector.Run(ctx)
	go lp.Run(ctx)
	go geo.Run(ctx)

	log.Info("agent starting", "version", version, "state_dir", o.StateDir)
	err = client.Run(ctx)
	switch {
	case decommission.requested.Load():
		// The control plane removed this server and the host is empty: delete
		// the agent itself, whatever ended the connection.
		cancelRun()
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer rcancel()
		exe, _ := os.Executable()
		removal := newSelfRemoval(o.StateDir, backupDir, exe, func(c context.Context) error {
			id := deployer.SelfContainer(c)
			if id == "" {
				return nil
			}
			return eng.ContainerSetRestartPolicy(c, id, "no")
		}, log)
		if rerr := removal.Execute(rctx); rerr != nil {
			log.Error("agent removal incomplete", "err", rerr)
			return rerr
		}
		log.Info("agent removed")
		return nil
	case errors.Is(err, conn.ErrRevoked):
		identity.Revoked = true
		if perr := st.PutIdentity(identity); perr != nil {
			log.Error("mark identity revoked", "err", perr)
		}
		return err
	case restart.requested.Load():
		return errUpgradeRestart
	case ctx.Err() != nil:
		log.Info("agent stopped")
		return nil
	}
	return err
}

// pruneCommandsLoop forgets command dedupe records older than 14 days.
func pruneCommandsLoop(ctx context.Context, log *slog.Logger, st *store.Store) {
	for {
		if n, err := st.PruneCommands(time.Now().Add(-14 * 24 * time.Hour)); err != nil {
			log.Warn("prune commands", "err", err)
		} else if n > 0 {
			log.Info("pruned command records", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}
