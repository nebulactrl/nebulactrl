package services

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Config is the desired configuration of a service, stored as services.config JSONB.
type Config struct {
	Image          string `json:"image,omitempty"`
	GitURL         string `json:"gitUrl,omitempty"`
	GitBranch      string `json:"gitBranch,omitempty"`
	DockerfilePath string `json:"dockerfilePath,omitempty"` // default "Dockerfile"
	BuildContext   string `json:"buildContext,omitempty"`   // default "."
	// BuildType selects how a git source is built: auto (the Dockerfile if the
	// repository has one, else Railpack; the default), dockerfile, railpack or
	// starter:<node|python|go|ruby|php|static>.
	BuildType string `json:"buildType,omitempty"`
	// Submodules is how a git source's submodules are checked out: "" or auto
	// (recursively, when the commit has a .gitmodules file; a submodule that
	// cannot be fetched is a warning), on (always; a failure fails the build) or
	// off. Credentials follow the repository's own: its token or deploy key is
	// used only for submodules on the same host.
	Submodules string `json:"submodules,omitempty"`
	// BuildLocation overrides where a git source is built: inherit (the
	// project, then the team default), target (on the deploying server) or
	// build_server (on a build server, pushed to a registry). BuildServerID
	// pins the build server and RegistryID the registry. Only the effective
	// location of the deploying release matters; a rollback of a registry
	// release needs no build server.
	BuildLocation    string      `json:"buildLocation,omitempty"`
	BuildServerID    *uuid.UUID  `json:"buildServerId,omitempty"`
	RegistryID       *uuid.UUID  `json:"registryId,omitempty"`
	Port             int         `json:"port,omitempty"` // 0 = no HTTP
	StartCommand     []string    `json:"startCommand,omitempty"`
	Healthcheck      Healthcheck `json:"healthcheck"`
	StopGraceSeconds int         `json:"stopGraceSeconds"` // default 30, 1..3600
	MemoryMB         int         `json:"memoryMb,omitempty"`
	CPUs             float64     `json:"cpus,omitempty"`
	ImageRetention   int         `json:"imageRetention"` // default 5
	// ProcessName names the default process (the service's own Port/StartCommand/Healthcheck...).
	// "" means "web". Meaningful only when Processes is non-empty, but always stored as given.
	ProcessName string `json:"processName,omitempty"`
	// Singleton: the default process never runs two instances at once (stop old, then start new;
	// replicas forced to 1). Allowed on any service.
	Singleton bool `json:"singleton,omitempty"`
	// Processes are the additional named processes, sorted by Name (WithDefaults sorts).
	// Empty = a plain single-process service.
	Processes []ProcessConfig `json:"processes,omitempty"`
	// Files are written into the container before it first starts (templates).
	Files []FileMount `json:"files,omitempty"`
	// OneShot runs the container to completion on every deploy (exit 0 = success).
	OneShot bool `json:"oneshot,omitempty"`
	// Route is the configuration of a kind=route service (nil for all others).
	Route *RouteConfig `json:"route,omitempty"`
	// Database marks a managed database (kind=database). Image, port, start
	// command, health check and data volume are derived from it.
	Database *engines.Spec `json:"database,omitempty"`
	// PreDeployCommand (argv, no shell) runs once per deployment in a container
	// of the new image with the service's environment, after the image is ready
	// and before any container of the new release starts (migrations). A
	// non-zero exit fails the deployment and the previous release keeps
	// serving. It does not run for rollbacks or reconcile redeploys.
	PreDeployCommand []string `json:"preDeployCommand,omitempty"`
	// PreDeployTimeoutSeconds bounds the pre-deploy command (default 600, max 3600).
	PreDeployTimeoutSeconds int `json:"preDeployTimeoutSeconds,omitempty"`
	// PostDeployCommand (argv, no shell) runs once per deployment in a one-shot
	// container of the new release, with the service's environment, after the
	// new release is live and the previous one retired (cache warm-up, search
	// reindex). Failure is warn-only: the deployment still succeeds, with a
	// warning event and a notification. It does not run for rollbacks or
	// reconcile redeploys.
	PostDeployCommand []string `json:"postDeployCommand,omitempty"`
	// PostDeployTimeoutSeconds bounds the post-deploy command (default 600, max 3600).
	PostDeployTimeoutSeconds int `json:"postDeployTimeoutSeconds,omitempty"`
	// Tailnet puts the service on a Tailscale or Headscale tailnet through a
	// per-service sidecar (nil = not on a tailnet). Staged like every setting.
	Tailnet *tailnetcfg.TailnetConfig `json:"tailnet,omitempty"`
	// BlockEgress blocks outbound internet: the service attaches only to its
	// environment's internal network (no gateway). Every other service of the
	// environment still reaches it. Managed databases default to true. A
	// service that blocks egress cannot join shared networks.
	BlockEgress bool `json:"blockEgress,omitempty"`
	// Docker holds the typed container options (hardening, hostname, dns,
	// sysctls, user, ulimits, shm size, ...; devices, GPUs and host-level
	// options only on unrestricted servers). Release config, so staged.
	Docker *agentprotocol.DockerOptions `json:"docker,omitempty"`
	// Stack is the Docker Compose project of a kind=stack service (nil for all others).
	Stack *StackConfig `json:"stack,omitempty"`
}

// Healthcheck configures how the agent decides a new container is healthy.
// The agent probes from outside the container, so images need no curl/wget.
type Healthcheck struct {
	Type               string `json:"type"`               // http | tcp | docker | none
	Path               string `json:"path,omitempty"`     // default "/"
	Port               int    `json:"port,omitempty"`     // default Config.Port
	IntervalSeconds    int    `json:"intervalSeconds"`    // default 2
	TimeoutSeconds     int    `json:"timeoutSeconds"`     // default 3
	StartPeriodSeconds int    `json:"startPeriodSeconds"` // default 0
	DeadlineSeconds    int    `json:"deadlineSeconds"`    // default 120
	SuccessThreshold   int    `json:"successThreshold"`   // default 2
	// Command is the argv of an exec health check (CMD or CMD-SHELL first).
	Command []string `json:"command,omitempty"`
}

// Build locations (Config.BuildLocation; the same values name a project's placement).
const (
	BuildLocationInherit     = "inherit"
	BuildLocationTarget      = "target"
	BuildLocationBuildServer = "build_server"
)

// Health check types.
const (
	HealthHTTP   = "http"
	HealthTCP    = "tcp"
	HealthDocker = "docker"
	HealthExec   = "exec" // Docker runs Command inside the container
	HealthNone   = "none"
)

// Source types.
const (
	SourceImage    = "image"
	SourceGit      = "git"
	SourceExternal = "external" // route services: nothing is built or pulled
	SourceCompose  = "compose"  // stacks: the compose file in Config.Stack
)

// Service kinds.
const (
	KindApp      = "app"
	KindWorker   = "worker"
	KindDatabase = "database"
	KindRoute    = "route" // external upstream or tcp/udp exposure; no container
	KindStack    = "stack" // a Docker Compose project run natively by the agent
)

// ErrInvalidConfig wraps every validation failure.
var ErrInvalidConfig = errors.New("invalid service config")

// WithDefaults returns a copy with every zero-valued defaultable field filled.
func (c Config) WithDefaults() Config {
	if c.DockerfilePath == "" {
		c.DockerfilePath = "Dockerfile"
	}
	if c.BuildContext == "" {
		c.BuildContext = "."
	}
	if c.BuildType == "" {
		c.BuildType = agentprotocol.BuildAuto
	}
	if c.Submodules == agentprotocol.SubmodulesAuto {
		c.Submodules = "" // auto is the unset value: no spurious pending change
	}
	if c.BuildLocation == "" {
		c.BuildLocation = BuildLocationInherit
	}
	if c.StopGraceSeconds == 0 {
		c.StopGraceSeconds = DefaultStopGraceSeconds
	}
	if len(c.PreDeployCommand) > 0 && c.PreDeployTimeoutSeconds == 0 {
		c.PreDeployTimeoutSeconds = agentprotocol.DefaultPreDeployTimeoutSeconds
	}
	if len(c.PostDeployCommand) > 0 && c.PostDeployTimeoutSeconds == 0 {
		c.PostDeployTimeoutSeconds = agentprotocol.DefaultPostDeployTimeoutSeconds
	}
	if c.ImageRetention == 0 {
		c.ImageRetention = 5
	}
	c.Stack = c.Stack.withDefaults()
	c.Tailnet = tailnetcfg.Normalize(c.Tailnet)
	c.Docker = c.Docker.Normalized()
	if c.Database != nil {
		spec := c.Database.Normalize()
		c.Database = &spec
		c.Image, c.Port, c.StartCommand = spec.Image(), spec.Port(), spec.StartCommand()
		if c.Healthcheck.Type == "" || c.Healthcheck.Type == HealthHTTP {
			c.Healthcheck.Type = HealthTCP
		}
	}
	c.Healthcheck = c.Healthcheck.withDefaults(c.Port)
	if len(c.Processes) > 0 {
		procs := slices.Clone(c.Processes)
		for i := range procs {
			procs[i].Healthcheck = procs[i].Healthcheck.withDefaults(procs[i].Port)
		}
		slices.SortFunc(procs, func(a, b ProcessConfig) int { return strings.Compare(a.Name, b.Name) })
		c.Processes = procs
	}
	return c
}

// withDefaults fills the zero-valued fields of a health check of a process
// (the service itself or a named one) that listens on port (0 = no HTTP).
func (h Healthcheck) withDefaults(port int) Healthcheck {
	if h.Type == "" {
		if port > 0 {
			h.Type = HealthHTTP
		} else {
			h.Type = HealthNone
		}
	}
	if h.Path == "" && h.Type == HealthHTTP {
		h.Path = "/"
	}
	if h.Port == 0 && (h.Type == HealthHTTP || h.Type == HealthTCP) {
		h.Port = port
	}
	if h.IntervalSeconds == 0 {
		h.IntervalSeconds = 2
	}
	if h.TimeoutSeconds == 0 {
		h.TimeoutSeconds = 3
	}
	if h.DeadlineSeconds == 0 {
		h.DeadlineSeconds = 120
	}
	if h.SuccessThreshold == 0 {
		h.SuccessThreshold = 2
	}
	return h
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, a...))
}

// Validate checks a defaulted config for the given source type.
func (c Config) Validate(sourceType string) error {
	if c.Database != nil {
		if sourceType != SourceImage {
			return invalid("managed databases use image sources")
		}
		if err := c.Database.Validate(); err != nil {
			return invalid("database: %s", err)
		}
	}
	if c.Stack != nil && sourceType != SourceCompose {
		return invalid("stack only applies to compose sources")
	}
	switch c.BuildLocation {
	case "", BuildLocationInherit, BuildLocationTarget, BuildLocationBuildServer:
	default:
		return invalid("buildLocation must be inherit, target or build_server")
	}
	switch sourceType {
	case SourceImage:
		if strings.TrimSpace(c.Image) == "" {
			return invalid("image is required for image sources")
		}
		if strings.ContainsAny(c.Image, " \t\n") {
			return invalid("image must not contain whitespace")
		}
	case SourceGit:
		if c.GitURL == "" {
			return invalid("gitUrl is required for git sources")
		}
		switch {
		case gitssh.LooksLikeSSH(c.GitURL):
			if _, err := gitssh.ParseURL(c.GitURL); err != nil {
				return invalid("gitUrl: %s", err)
			}
		case !(strings.HasPrefix(c.GitURL, "https://") || strings.HasPrefix(c.GitURL, "http://") || strings.HasPrefix(c.GitURL, "git://")):
			return invalid("gitUrl must be an https://, http://, git:// or ssh:// URL (or git@host:org/repo.git)")
		case gitURLHasCredentials(c.GitURL):
			return invalid("%s", gitURLCredentialsHint)
		}
		if c.GitBranch == "" {
			return invalid("gitBranch is required for git sources")
		}
		if !agentprotocol.ValidBuildType(c.BuildType) {
			return invalid("buildType must be auto, dockerfile, railpack or starter:<node|python|go|ruby|php|static>")
		}
		if !agentprotocol.ValidSubmodules(c.Submodules) {
			return invalid("submodules must be auto, on or off")
		}
		if err := ValidateRelPath("dockerfilePath", c.DockerfilePath); err != nil {
			return err
		}
		if err := ValidateRelPath("buildContext", c.BuildContext); err != nil {
			return err
		}
	case SourceCompose:
		if err := c.validateStack(); err != nil {
			return err
		}
	case SourceExternal:
		if c.BlockEgress {
			return invalid("route services have no container: blockEgress does not apply")
		}
		if err := c.Route.Validate(); err != nil {
			return err
		}
	default:
		return invalid("unknown source type %q", sourceType)
	}
	if c.Port < 0 || c.Port > 65535 {
		return invalid("port must be between 0 and 65535")
	}
	if c.StopGraceSeconds < 1 || c.StopGraceSeconds > 3600 {
		return invalid("stopGraceSeconds must be between 1 and 3600")
	}
	if c.ImageRetention < 1 || c.ImageRetention > 50 {
		return invalid("imageRetention must be between 1 and 50")
	}
	if c.MemoryMB < 0 {
		return invalid("memoryMb must not be negative")
	}
	if c.MemoryMB > 0 && c.MemoryMB < 6 {
		return invalid("memoryMb must be at least 6")
	}
	if c.CPUs < 0 {
		return invalid("cpus must not be negative")
	}
	if len(c.Processes) > 0 && sourceType != SourceImage && sourceType != SourceGit {
		return invalid("processes need an image or git source")
	}
	if err := c.validateProcesses(); err != nil {
		return err
	}
	if err := c.validatePreDeploy(); err != nil {
		return err
	}
	if err := c.validatePostDeploy(); err != nil {
		return err
	}
	if err := c.validateDocker(); err != nil {
		return err
	}
	if err := c.validateTemplateFields(); err != nil {
		return err
	}
	if err := c.validateTailnet(); err != nil {
		return err
	}
	return validateHealthcheck(c.Healthcheck, 0, "")
}

// validateHealthcheck checks a defaulted health check. port is the port of the
// process it belongs to: a check without its own port falls back to it (the
// service itself passes 0: WithDefaults has already done the fallback). label
// prefixes the messages ("" or `process "worker": `).
func validateHealthcheck(h Healthcheck, port int, label string) error {
	checkPort := h.Port
	if checkPort == 0 {
		checkPort = port
	}
	switch h.Type {
	case HealthHTTP:
		if checkPort <= 0 {
			return invalid("%shttp health checks need a port", label)
		}
		if !strings.HasPrefix(h.Path, "/") {
			return invalid("%shealthcheck.path must start with /", label)
		}
	case HealthTCP:
		if checkPort <= 0 {
			return invalid("%stcp health checks need a port", label)
		}
	case HealthDocker, HealthNone:
	case HealthExec:
		if len(h.Command) < 2 || (h.Command[0] != "CMD" && h.Command[0] != "CMD-SHELL") {
			return invalid("%sexec health checks need a command starting with CMD or CMD-SHELL", label)
		}
	default:
		return invalid("%sunknown healthcheck type %q", label, h.Type)
	}
	if h.Port < 0 || h.Port > 65535 {
		return invalid("%shealthcheck.port must be between 0 and 65535", label)
	}
	if h.IntervalSeconds < 1 || h.TimeoutSeconds < 1 || h.DeadlineSeconds < 1 || h.SuccessThreshold < 1 || h.StartPeriodSeconds < 0 {
		return invalid("%shealthcheck timings must be positive", label)
	}
	if h.DeadlineSeconds > 3600 {
		return invalid("%shealthcheck.deadlineSeconds must be at most 3600", label)
	}
	return nil
}

// ValidateRelPath rejects absolute paths and any path containing "..".
func ValidateRelPath(field, p string) error {
	if p == "" {
		return invalid("%s must not be empty", field)
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || strings.Contains(p, "\x00") {
		return invalid("%s must be a relative path", field)
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return invalid("%s must not contain ..", field)
		}
	}
	if strings.HasPrefix(path.Clean(p), "..") {
		return invalid("%s must not escape the repository", field)
	}
	return nil
}

// HealthcheckSpec converts to the agent protocol representation.
func (h Healthcheck) Spec() agentprotocol.HealthcheckSpec {
	return agentprotocol.HealthcheckSpec{
		Type: h.Type, Path: h.Path, Port: h.Port,
		IntervalSeconds: h.IntervalSeconds, TimeoutSeconds: h.TimeoutSeconds,
		StartPeriodSeconds: h.StartPeriodSeconds, DeadlineSeconds: h.DeadlineSeconds,
		SuccessThreshold: h.SuccessThreshold, Command: h.Command,
	}
}

// validatePreDeploy checks the pre-deploy command.
func (c Config) validatePreDeploy() error {
	if len(c.PreDeployCommand) == 0 {
		if c.PreDeployTimeoutSeconds != 0 {
			return invalid("preDeployTimeoutSeconds needs a preDeployCommand")
		}
		return nil
	}
	if err := agentprotocol.ValidTaskCommand(c.PreDeployCommand); err != nil {
		return invalid("preDeployCommand: %s", err)
	}
	if c.PreDeployTimeoutSeconds < 1 || c.PreDeployTimeoutSeconds > agentprotocol.MaxPreDeployTimeoutSeconds {
		return invalid("preDeployTimeoutSeconds must be between 1 and %d", agentprotocol.MaxPreDeployTimeoutSeconds)
	}
	if c.OneShot {
		return invalid("a one-shot service is itself a command; it has no pre-deploy command")
	}
	return nil
}

// validatePostDeploy checks the post-deploy command.
func (c Config) validatePostDeploy() error {
	if len(c.PostDeployCommand) == 0 {
		if c.PostDeployTimeoutSeconds != 0 {
			return invalid("postDeployTimeoutSeconds needs a postDeployCommand")
		}
		return nil
	}
	if err := agentprotocol.ValidTaskCommand(c.PostDeployCommand); err != nil {
		return invalid("postDeployCommand: %s", err)
	}
	if c.PostDeployTimeoutSeconds < 1 || c.PostDeployTimeoutSeconds > agentprotocol.MaxPostDeployTimeoutSeconds {
		return invalid("postDeployTimeoutSeconds must be between 1 and %d", agentprotocol.MaxPostDeployTimeoutSeconds)
	}
	if c.OneShot {
		return invalid("a one-shot service is itself a command; it has no post-deploy command")
	}
	return nil
}
