package services

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// HealthcheckInput is Healthcheck with every field optional; omitted fields
// take their defaults.
type HealthcheckInput struct {
	Type               string `json:"type,omitempty" enum:"http,tcp,docker,exec,none"`
	Path               string `json:"path,omitempty"`
	Port               int    `json:"port,omitempty"`
	IntervalSeconds    int    `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int    `json:"timeoutSeconds,omitempty"`
	StartPeriodSeconds int    `json:"startPeriodSeconds,omitempty"`
	DeadlineSeconds    int    `json:"deadlineSeconds,omitempty"`
	SuccessThreshold   int    `json:"successThreshold,omitempty"`
	// Command is the argv of an exec check, starting with CMD or CMD-SHELL.
	Command []string `json:"command,omitempty"`
}

// ConfigInput is Config with every field optional; omitted fields take their
// defaults. As a PATCH field it replaces the whole config.
type ConfigInput struct {
	Image          string `json:"image,omitempty"`
	GitURL         string `json:"gitUrl,omitempty"`
	GitBranch      string `json:"gitBranch,omitempty"`
	DockerfilePath string `json:"dockerfilePath,omitempty"`
	BuildContext   string `json:"buildContext,omitempty"`
	BuildType      string `json:"buildType,omitempty" doc:"How a git source is built: auto (Dockerfile if present, else Railpack), dockerfile, railpack or starter:<node|python|go|ruby|php|static>" example:"auto"`
	Submodules     string `json:"submodules,omitempty" enum:"auto,on,off" doc:"Git submodules: auto (recursive when the commit has a .gitmodules file; a submodule that cannot be fetched is only a warning; the default), on (always; a failure fails the build) or off. Credentials: the repository's token or deploy key is used for submodules on the same host only; relative URLs and ssh:// submodules of an HTTPS repository on the same host work, other hosts are fetched anonymously over https" example:"auto"`
	// BuildLocation overrides where a git source is built; BuildServerID pins the build server and RegistryID the registry.
	BuildLocation    string           `json:"buildLocation,omitempty" enum:"inherit,target,build_server" doc:"Where a git source is built: inherit (project, then team default), target, or build_server (pushed to a registry; the target pulls by digest)"`
	BuildServerID    *uuid.UUID       `json:"buildServerId,omitempty" doc:"Pin the build server; omitted lets the control plane choose a sticky one"`
	RegistryID       *uuid.UUID       `json:"registryId,omitempty" doc:"Registry the build is pushed to; omitted inherits the project or team default"`
	Port             int              `json:"port,omitempty"`
	StartCommand     []string         `json:"startCommand,omitempty"`
	Healthcheck      HealthcheckInput `json:"healthcheck,omitempty"`
	StopGraceSeconds int              `json:"stopGraceSeconds,omitempty"`
	MemoryMB         int              `json:"memoryMb,omitempty"`
	CPUs             float64          `json:"cpus,omitempty"`
	ImageRetention   int              `json:"imageRetention,omitempty"`
	// ProcessName names the default process (the service's own port, command and health check). Empty keeps "web".
	ProcessName string `json:"processName,omitempty" pattern:"^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$" maxLength:"31" doc:"Name of the default process (the port, start command and health check above). Empty means web. Only meaningful with processes"`
	// Singleton makes the default process run one instance at a time: the old one stops before the new one starts.
	Singleton bool `json:"singleton,omitempty" doc:"The default process never runs two instances at once: the old instance stops before the new one starts, and scaling is off"`
	// Processes are the additional named processes of the service.
	Processes []ProcessInput `json:"processes,omitempty" maxItems:"9" doc:"Additional named processes that share the service's image, variables, volumes and release. On PATCH the array replaces the whole list."`
	// Files are written into the container before it starts (plain text, not secret).
	Files []FileMount `json:"files,omitempty"`
	// OneShot runs the container to completion on every deploy.
	OneShot bool `json:"oneshot,omitempty"`
	// PreDeployCommand (argv, no shell) runs in a container of the new image
	// before the new release starts; a non-zero exit fails the deployment.
	PreDeployCommand []string `json:"preDeployCommand,omitempty"`
	// PreDeployTimeoutSeconds bounds the pre-deploy command (default 600, max 3600).
	PreDeployTimeoutSeconds int `json:"preDeployTimeoutSeconds,omitempty"`
	// PostDeployCommand (argv, no shell) runs in a one-shot container of the new
	// release after the swap; failure is warn-only (the deployment still succeeds).
	PostDeployCommand []string `json:"postDeployCommand,omitempty"`
	// PostDeployTimeoutSeconds bounds the post-deploy command (default 600, max 3600).
	PostDeployTimeoutSeconds int `json:"postDeployTimeoutSeconds,omitempty"`
	// Tailnet puts the service on a Tailscale or Headscale tailnet (staged; applied by the next deployment).
	Tailnet *tailnetcfg.TailnetConfig `json:"tailnet,omitempty"`
	// BlockEgress blocks outbound internet (staged). On PATCH, omitting it keeps the current value.
	BlockEgress *bool `json:"blockEgress,omitempty" doc:"Block outbound internet: the service only joins its environment's internal network. Omit to keep the current value on PATCH."`
	// Docker holds allow-listed container options (staged; applied by the next deployment).
	Docker *agentprotocol.DockerOptions `json:"docker,omitempty" doc:"Typed container options. On every server: capAdd, capDrop, noNewPrivileges, readOnlyRootfs, pidsLimit, hostname (default: the service slug), dns, sysctls, hostSysctls (host requirements, only warned about), user, groupAdd, ulimits, shmSizeMb, extraHosts, labels, init, stopSignal, tmpfs, entrypoint. Only on a server an instance admin made unrestricted: devices, gpus, runtime, privileged, pidHost, seccompUnconfined, apparmorUnconfined and the capabilities NET_ADMIN and SYS_ADMIN (422 requires_unrestricted_server otherwise). Never host networking, ip, restart or log options, host binds or the socket."`
	// Stack is the compose project of a kind=stack service (source compose); the components are derived by the server.
	Stack *StackConfigInput `json:"stack,omitempty" doc:"The compose project of a stack: the compose file, per-component limits and the wait timeout. Components are derived by the server."`
}

// ToConfig converts to a (not yet defaulted) Config.
func (c ConfigInput) ToConfig() Config {
	return Config{
		Image: c.Image, GitURL: c.GitURL, GitBranch: c.GitBranch, DockerfilePath: c.DockerfilePath, BuildContext: c.BuildContext, BuildType: c.BuildType, Submodules: c.Submodules,
		BuildLocation: c.BuildLocation, BuildServerID: c.BuildServerID, RegistryID: c.RegistryID,
		Port: c.Port, StartCommand: c.StartCommand, StopGraceSeconds: c.StopGraceSeconds, MemoryMB: c.MemoryMB, CPUs: c.CPUs,
		ProcessName: c.ProcessName, Singleton: c.Singleton, Processes: processConfigs(c.Processes),
		ImageRetention: c.ImageRetention, Files: c.Files, OneShot: c.OneShot,
		PreDeployCommand: c.PreDeployCommand, PreDeployTimeoutSeconds: c.PreDeployTimeoutSeconds,
		PostDeployCommand: c.PostDeployCommand, PostDeployTimeoutSeconds: c.PostDeployTimeoutSeconds, Tailnet: c.Tailnet,
		BlockEgress: c.BlockEgress != nil && *c.BlockEgress, Docker: c.Docker, Stack: c.Stack.toConfig(),
		Healthcheck: Healthcheck{
			Type: c.Healthcheck.Type, Path: c.Healthcheck.Path, Port: c.Healthcheck.Port, IntervalSeconds: c.Healthcheck.IntervalSeconds,
			TimeoutSeconds: c.Healthcheck.TimeoutSeconds, StartPeriodSeconds: c.Healthcheck.StartPeriodSeconds,
			DeadlineSeconds: c.Healthcheck.DeadlineSeconds, SuccessThreshold: c.Healthcheck.SuccessThreshold, Command: c.Healthcheck.Command,
		},
	}
}

// processConfigs converts the request's processes (nil stays nil: no processes).
func processConfigs(in []ProcessInput) []ProcessConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]ProcessConfig, len(in))
	for i, p := range in {
		out[i] = p.ToProcess()
	}
	return out
}

// RegistryAuthInput carries private-registry credentials (write-only).
type RegistryAuthInput struct {
	Username string `json:"username" maxLength:"256"`
	Password string `json:"password" maxLength:"4096"`
}

func (r *RegistryAuthInput) toAuth() *RegistryAuth {
	if r == nil {
		return nil
	}
	return &RegistryAuth{Username: r.Username, Password: r.Password}
}

type envIDInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
}

type serviceIDInput struct {
	ID uuid.UUID `path:"serviceId"`
}

type listServicesInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	api.PageQuery
}

type createServiceInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          struct {
		Name         string             `json:"name" minLength:"1" maxLength:"100"`
		Kind         string             `json:"kind,omitempty" enum:"app,worker,database,stack"`
		SourceType   string             `json:"sourceType,omitempty" enum:"image,git,compose"`
		Config       ConfigInput        `json:"config,omitempty"`
		ServerID     *uuid.UUID         `json:"serverId,omitempty"`
		GitToken     string             `json:"gitToken,omitempty" doc:"Write-only access token for private HTTPS repositories"`
		RegistryAuth *RegistryAuthInput `json:"registryAuth,omitempty" doc:"Write-only credentials for a private registry"`
	}
}

type serviceOutput struct {
	ETag string `header:"ETag"`
	Body ServiceView
}

type updateServiceInput struct {
	ID      uuid.UUID `path:"serviceId"`
	IfMatch string    `header:"If-Match" doc:"The ETag (generation) from the last GET, e.g. \"3\". Required."`
	Body    struct {
		Name         *string            `json:"name,omitempty" minLength:"1" maxLength:"100"`
		Config       *ConfigInput       `json:"config,omitempty" doc:"Replaces the whole config; omitted fields take defaults"`
		ServerID     *uuid.UUID         `json:"serverId,omitempty"`
		GitToken     *string            `json:"gitToken,omitempty" doc:"Write-only; an empty string clears the token"`
		RegistryAuth *RegistryAuthInput `json:"registryAuth,omitempty" doc:"Write-only; empty username and password clears the credentials"`

		// RenameProcesses is applied with the config; it is not stored.
		RenameProcesses map[string]string `json:"renameProcesses,omitempty" doc:"Write-only: old process name to new name. Renaming keeps the process' replicas setting and its domains; the new name must be a process of the new config and the old one must not be. Applied together with config; not stored"`
	}
}

type stageServiceSourceChangeInput struct {
	ID      uuid.UUID `path:"serviceId"`
	IfMatch string    `header:"If-Match" doc:"The ETag (generation) from the last GET, e.g. \"3\". Required."`
	Body    struct {
		SourceType     string             `json:"sourceType" enum:"image,git" doc:"The source the service moves to"`
		Image          string             `json:"image,omitempty" maxLength:"512" doc:"Required for image: repository[:tag]"`
		RegistryAuth   *RegistryAuthInput `json:"registryAuth,omitempty" doc:"Write-only credentials for a private registry (image)"`
		GitURL         string             `json:"gitUrl,omitempty" maxLength:"2048" doc:"Required for git: the clone URL"`
		GitBranch      string             `json:"gitBranch,omitempty" maxLength:"255" doc:"Required for git"`
		GitToken       string             `json:"gitToken,omitempty" doc:"Write-only access token for a private HTTPS repository (git)"`
		BuildType      string             `json:"buildType,omitempty" doc:"How the repository is built: auto (default), dockerfile, railpack or starter:<node|python|go|ruby|php|static>" example:"auto"`
		DockerfilePath string             `json:"dockerfilePath,omitempty" doc:"Relative to the repository root (default Dockerfile)"`
		BuildContext   string             `json:"buildContext,omitempty" doc:"Relative to the repository root (default .)"`
		Submodules     string             `json:"submodules,omitempty" enum:"auto,on,off"`
	}
}

type deleteServiceInput struct {
	ID            uuid.UUID `path:"serviceId"`
	Confirm       string    `query:"confirm" doc:"Must equal the service slug"`
	DeleteVolumes bool      `query:"deleteVolumes" doc:"Also remove the service's Docker volumes (data is lost)"`
}

type actionOutput struct{ Body ActionResult }

type volumeListOutput struct {
	Body struct {
		Items []VolumeView `json:"items"`
	}
}

type createVolumeInput struct {
	ID   uuid.UUID `path:"serviceId"`
	Body struct {
		Name      string `json:"name" minLength:"1" maxLength:"63"`
		MountPath string `json:"mountPath" minLength:"2" maxLength:"512"`
	}
}

type volumeOutput struct{ Body VolumeView }

type volumeIDInput struct {
	ID uuid.UUID `path:"volumeId"`
}

type linkListOutput struct {
	Body struct {
		Items []LinkView `json:"items"`
	}
}

type createLinkInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Body          struct {
		FromServiceID     uuid.UUID `json:"fromServiceId"`
		ToServiceID       uuid.UUID `json:"toServiceId"`
		AcceptMeshChanges bool      `json:"acceptMeshChanges,omitempty" doc:"Confirm the tailnet devices a link between services on different servers adds; without it the request fails with mesh_confirmation_required when devices would be added"`
	}
}

type linkResultOutput struct{ Body LinkResult }

type previewLinkInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	From          uuid.UUID `query:"from" doc:"The consumer: the service that would hold the variables"`
	To            uuid.UUID `query:"to" doc:"The provider: the service the variables point at"`
}

type linkPreviewOutput struct{ Body LinkPreview }

type linkIDInput struct {
	ID string `path:"linkId" doc:"UUID of a manual link; derived reference links (ref:<from>:<to>) cannot be deleted"`
}

type metricsInput struct {
	ID      uuid.UUID `path:"serviceId"`
	Range   string    `query:"range" default:"1h" enum:"5m,10m,30m,1h,6h,12h,24h,7d,30d"`
	Process string    `query:"process" doc:"Services with processes only: the series and per-replica usage of one process. Without it the series is the service total (every process summed)"`
}

type metricsOutput struct {
	Body struct {
		Points        []ServiceMetricPoint `json:"points"`
		StepSeconds   int                  `json:"stepSeconds" doc:"Bucket width of every point"`
		RetentionDays int                  `json:"retentionDays" doc:"Days of history kept on the service's server; longer ranges only show what is retained"`
	}
}

type serviceLogsInput struct {
	ID        uuid.UUID `path:"serviceId"`
	Tail      int       `query:"tail" default:"200" minimum:"0" maximum:"5000"`
	Follow    bool      `query:"follow" doc:"Stream new lines as server-sent events"`
	Process   string    `query:"process" doc:"Services with processes only: show one process' lines (without it, every process, each line prefixed '<process> | '). Not together with component"`
	Component string    `query:"component" doc:"Compose stacks only: show one component's lines (without it, every component, each line prefixed '<component> | ')"`
}

type envLogsInput struct {
	EnvironmentID uuid.UUID `path:"environmentId"`
	Tail          int       `query:"tail" default:"100" minimum:"0" maximum:"5000"`
	Follow        bool      `query:"follow" doc:"Stream new lines as server-sent events"`
}

func viewOutput(v ServiceView) *serviceOutput {
	return &serviceOutput{ETag: ETag(v.Generation), Body: v}
}

// Register attaches the service endpoints.
func Register(h huma.API, svc *Service) {
	errs := func(extra ...int) []int {
		return append([]int{http.StatusUnauthorized, http.StatusForbidden}, extra...)
	}
	nf, uc := http.StatusNotFound, http.StatusUnprocessableEntity
	RegisterDockerOptions(h, svc)
	RegisterStacks(h, svc)

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listServices", Method: http.MethodGet, Path: "/environments/{environmentId}/services", Tags: []string{"services"},
		Summary: "List an environment's services", Errors: errs(nf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *listServicesInput) (*struct{ Body api.Page[ServiceView] }, error) {
		items, next, err := svc.List(ctx, in.EnvironmentID, in.Cursor, in.Size())
		if err != nil {
			return nil, err
		}
		return &struct{ Body api.Page[ServiceView] }{Body: api.Page[ServiceView]{Items: items, NextCursor: next}}, nil
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "createService", Method: http.MethodPost, Path: "/environments/{environmentId}/services", Tags: []string{"services"},
		Summary: "Create a service", DefaultStatus: http.StatusCreated,
		Description: "Creating a service stages its configuration; nothing runs until it is deployed. " +
			"Managed databases are created with `POST /environments/{environmentId}/databases`. " +
			"A Compose stack is `kind: stack`, `sourceType: compose` with `config.stack.compose`; the server derives `config.stack.components`. " +
			"422 `invalid_compose` (details.violations) when the compose file does not parse, uses something stacks do not support, " +
			"or breaks the safe allow-list while the service's server does not have Unrestricted Compose.",
		Errors: errs(nf, http.StatusConflict, uc),
	}), policy.ScopeProjectsWrite), func(ctx context.Context, in *createServiceInput) (*serviceOutput, error) {
		v, err := svc.Create(ctx, in.EnvironmentID, CreateInput{
			Name: in.Body.Name, Kind: in.Body.Kind, SourceType: in.Body.SourceType, Config: in.Body.Config.ToConfig(),
			ServerID: in.Body.ServerID, GitToken: in.Body.GitToken, RegistryAuth: in.Body.RegistryAuth.toAuth(),
		})
		if err != nil {
			return nil, err
		}
		return viewOutput(v), nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getService", Method: http.MethodGet, Path: "/services/{serviceId}", Tags: []string{"services"},
		Summary: "Get a service", Errors: errs(nf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*serviceOutput, error) {
		v, err := svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return viewOutput(v), nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "updateService", Method: http.MethodPatch, Path: "/services/{serviceId}", Tags: []string{"services"},
		Summary: "Update a service's configuration (staged until deployed)",
		Description: "Requires `If-Match: \"<generation>\"`. A stale value returns 412 `generation_conflict`. " +
			"Every effective change increments the generation; the change takes effect on the next deployment.",
		Errors: errs(nf, http.StatusPreconditionFailed, http.StatusPreconditionRequired, uc),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *updateServiceInput) (*serviceOutput, error) {
		gen, ok, err := ParseETag(in.IfMatch)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierr.New(http.StatusPreconditionRequired, "if_match_required", "send If-Match with the generation from the last GET")
		}
		ui := UpdateInput{Name: in.Body.Name, ServerID: in.Body.ServerID, GitToken: in.Body.GitToken, RegistryAuth: in.Body.RegistryAuth.toAuth(), RenameProcesses: in.Body.RenameProcesses}
		if in.Body.Config != nil {
			c := in.Body.Config.ToConfig()
			ui.Config = &c
			ui.BlockEgress = in.Body.Config.BlockEgress
		}
		v, err := svc.Update(ctx, in.ID, gen, ui)
		if err != nil {
			return nil, err
		}
		return viewOutput(v), nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "stageServiceSourceChange", Method: http.MethodPut, Path: "/services/{serviceId}/source", Tags: []string{"services"},
		Summary: "Change a service's source between a Docker image and a Git repository (staged until deployed)",
		Description: "Requires `If-Match: \"<generation>\"`. Name, domains, variables, volumes, replicas, health check and history are kept. " +
			"The old source's own settings are cleared: image credentials when moving to git; the repository link, access token, deploy key and build settings when moving to an image. " +
			"The next deployment ships the new source (a git source builds first). A connected account is linked afterwards with `PUT /services/{serviceId}/git`. " +
			"Compose stacks, databases and routes cannot change source (422 `not_applicable`); a missing image or repository is 422 `invalid_config`; the service's current type is 422 `source_unchanged`.",
		Errors: errs(nf, http.StatusPreconditionFailed, http.StatusPreconditionRequired, uc),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *stageServiceSourceChangeInput) (*serviceOutput, error) {
		gen, ok, err := ParseETag(in.IfMatch)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierr.New(http.StatusPreconditionRequired, "if_match_required", "send If-Match with the generation from the last GET")
		}
		b := in.Body
		v, err := svc.StageSourceChange(ctx, in.ID, gen, SourceChangeInput{
			SourceType: b.SourceType, Image: b.Image, RegistryAuth: b.RegistryAuth.toAuth(),
			GitURL: b.GitURL, GitBranch: b.GitBranch, GitToken: b.GitToken,
			BuildType: b.BuildType, DockerfilePath: b.DockerfilePath, BuildContext: b.BuildContext, Submodules: b.Submodules,
		})
		if err != nil {
			return nil, err
		}
		return viewOutput(v), nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteService", Method: http.MethodDelete, Path: "/services/{serviceId}", Tags: []string{"services"},
		Summary: "Delete a service", DefaultStatus: http.StatusNoContent,
		Description: "Requires `confirm=<slug>`. A deployed service's container is removed from its server; " +
			"volumes are kept unless `deleteVolumes=true`.",
		Errors: errs(nf, http.StatusConflict, uc),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *deleteServiceInput) (*struct{}, error) {
		return nil, svc.Delete(ctx, in.ID, DeleteInput{Confirm: in.Confirm, DeleteVolumes: in.DeleteVolumes})
	})

	huma.Register(h, api.WithScopes(api.WithIdempotency(huma.Operation{
		OperationID: "restartService", Method: http.MethodPost, Path: "/services/{serviceId}/restart", Tags: []string{"services"},
		Summary: "Restart the running container", DefaultStatus: http.StatusAccepted,
		Description: "Queues a restart on the service's server. It completes when the agent acknowledges it.",
		Errors:      errs(nf, http.StatusConflict, uc),
	}), policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceIDInput) (*actionOutput, error) {
		r, err := svc.Restart(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &actionOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "stopService", Method: http.MethodPost, Path: "/services/{serviceId}/stop", Tags: []string{"services"},
		Summary: "Stop the running container", DefaultStatus: http.StatusAccepted,
		Errors: errs(nf, http.StatusConflict, uc),
	}, policy.ScopeDeploymentsWrite), func(ctx context.Context, in *serviceIDInput) (*actionOutput, error) {
		r, err := svc.Stop(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &actionOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listVolumes", Method: http.MethodGet, Path: "/services/{serviceId}/volumes", Tags: []string{"volumes"},
		Summary: "List a service's volumes", Errors: errs(nf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *serviceIDInput) (*volumeListOutput, error) {
		items, err := svc.ListVolumes(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		out := &volumeListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createVolume", Method: http.MethodPost, Path: "/services/{serviceId}/volumes", Tags: []string{"volumes"},
		Summary: "Attach a named volume (staged until deployed)", DefaultStatus: http.StatusCreated,
		Errors: errs(nf, http.StatusConflict, uc),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *createVolumeInput) (*volumeOutput, error) {
		v, err := svc.AddVolume(ctx, in.ID, in.Body.Name, in.Body.MountPath)
		if err != nil {
			return nil, err
		}
		return &volumeOutput{Body: v}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteVolume", Method: http.MethodDelete, Path: "/volumes/{volumeId}", Tags: []string{"volumes"},
		Summary: "Detach a volume (staged); the Docker volume and its data are kept", DefaultStatus: http.StatusNoContent,
		Errors: errs(nf),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *volumeIDInput) (*struct{}, error) {
		return nil, svc.DeleteVolume(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "listLinks", Method: http.MethodGet, Path: "/environments/{environmentId}/links", Tags: []string{"links"},
		Summary: "List the links between services of an environment", Errors: errs(nf),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *envIDInput) (*linkListOutput, error) {
		items, err := svc.ListLinks(ctx, in.EnvironmentID)
		if err != nil {
			return nil, err
		}
		out := &linkListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "previewLink", Method: http.MethodGet, Path: "/environments/{environmentId}/link-proposals", Tags: []string{"links"},
		Summary: "Preview the variables linking two services would propose",
		Description: "Writes nothing. Returns every reference the consumer (`from`) could hold to reach the provider (`to`), " +
			"the recommended set flagged and each row's status on the consumer, plus warnings and whether an edge already exists. " +
			"Add the chosen ones with PUT /services/{id}/variables; the edge is drawn from those references.",
		Errors: errs(nf, uc),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *previewLinkInput) (*linkPreviewOutput, error) {
		p, err := svc.PreviewLink(ctx, in.EnvironmentID, in.From, in.To)
		if err != nil {
			return nil, err
		}
		return &linkPreviewOutput{Body: p}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "createLink", Method: http.MethodPost, Path: "/environments/{environmentId}/links", Tags: []string{"links"},
		Summary: "Link two services", DefaultStatus: http.StatusCreated,
		Description: "A link is presentation plus review only. The response proposes variables for the source service; " +
			"they are NOT added. Add them with PUT /services/{id}/variables after the user confirms. " +
			"When the two services run on different servers the link also opens a mesh grant (cross-server service link): " +
			"the response's mesh block lists the links and devices; devices need acceptMeshChanges.",
		Errors: errs(nf, http.StatusConflict, uc),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *createLinkInput) (*linkResultOutput, error) {
		var opts []LinkOption
		if in.Body.AcceptMeshChanges {
			opts = append(opts, AcceptMeshChanges())
		}
		r, err := svc.CreateLink(ctx, in.EnvironmentID, in.Body.FromServiceID, in.Body.ToServiceID, opts...)
		if err != nil {
			return nil, err
		}
		return &linkResultOutput{Body: r}, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "deleteLink", Method: http.MethodDelete, Path: "/links/{linkId}", Tags: []string{"links"},
		Summary: "Remove a link", DefaultStatus: http.StatusNoContent, Errors: errs(nf),
	}, policy.ScopeProjectsWrite), func(ctx context.Context, in *linkIDInput) (*struct{}, error) {
		return nil, svc.DeleteLink(ctx, in.ID)
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceMetrics", Method: http.MethodGet, Path: "/services/{serviceId}/metrics", Tags: []string{"metrics"},
		Summary: "CPU and memory of a service's container",
		Description: "At most 300 points of `stepSeconds` each. `range` is one of 5m, 10m, 30m, 1h (default), 6h, 12h, 24h, 7d, 30d; " +
			"longer ranges read downsampled tiers and carry the per-bucket peaks `cpuMax`/`memMax`. Memory is usage minus inactive file cache. " +
			"`retentionDays` is how long the service's server keeps history. " +
			"`process` (services with processes only) narrows the series and the last point's per-replica usage to one process; without it the series is the total of every process. 422 `unknown_process` for a name the service does not have.",
		Errors: errs(nf, uc),
	}, policy.ScopeProjectsRead), func(ctx context.Context, in *metricsInput) (*metricsOutput, error) {
		res, err := svc.Metrics(ctx, in.ID, in.Range, in.Process)
		if err != nil {
			return nil, err
		}
		out := &metricsOutput{}
		out.Body.Points, out.Body.StepSeconds, out.Body.RetentionDays = res.Points, res.StepSeconds, res.RetentionDays
		return out, nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getServiceLogs", Method: http.MethodGet, Path: "/services/{serviceId}/logs", Tags: []string{"logs"},
		Summary: "Runtime logs of a service",
		Description: "Without `follow` returns `{items:[{ts,line}]}`. With `follow=true` streams server-sent events " +
			"`data: {ts,line,serviceId}`. Secret values are redacted. 503 `server_offline` when the server is not connected. " +
			"Services with several processes: `process` shows one process, otherwise every line is prefixed `<process> | `; " +
			"422 `unknown_process` for a name the service does not have, and `component` and `process` cannot be combined.",
		Responses: StreamResponses(), Errors: errs(nf, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable),
	}, policy.ScopeLogsRead), func(ctx context.Context, in *serviceLogsInput) (*huma.StreamResponse, error) {
		ch, cancel, err := svc.SubscribeServiceProcessLogs(ctx, in.ID, in.Tail, in.Follow, in.Component, in.Process)
		if err != nil {
			return nil, err
		}
		return streamLogs(ch, cancel, in.Follow, in.Tail), nil
	})

	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "getEnvironmentLogs", Method: http.MethodGet, Path: "/environments/{environmentId}/logs", Tags: []string{"logs"},
		Summary:     "Merged runtime logs of every service of an environment",
		Description: "Same shape as the service log endpoint; entries also carry `serviceId` and `serviceName`.",
		Responses:   StreamResponses(), Errors: errs(nf, http.StatusConflict, http.StatusServiceUnavailable),
	}, policy.ScopeLogsRead), func(ctx context.Context, in *envLogsInput) (*huma.StreamResponse, error) {
		ch, cancel, err := svc.SubscribeEnvironmentLogs(ctx, in.EnvironmentID, in.Tail, in.Follow)
		if err != nil {
			return nil, err
		}
		return streamLogs(ch, cancel, in.Follow, in.Tail), nil
	})
}

// streamLogs renders a log channel as SSE (follow) or a JSON page (tail).
func streamLogs(ch <-chan LogEntry, cancel func(), follow bool, tail int) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer cancel()
		if !follow {
			items := collectLogs(hctx.Context(), ch, tail, 15*time.Second)
			WriteJSON(hctx, struct {
				Items []LogEntry `json:"items"`
			}{Items: items})
			return
		}
		st, err := NewStream(hctx)
		if err != nil {
			return
		}
		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-hctx.Context().Done():
				return
			case <-ping.C:
				if st.Comment("ping") != nil {
					return
				}
			case e, ok := <-ch:
				if !ok {
					_ = st.Event("end", struct{}{})
					return
				}
				if st.Data(e) != nil {
					return
				}
			}
		}
	}}
}

func collectLogs(ctx context.Context, ch <-chan LogEntry, tail int, wait time.Duration) []LogEntry {
	items := []LogEntry{}
	timer := time.NewTimer(wait)
	defer timer.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-timer.C:
			break loop
		case e, ok := <-ch:
			if !ok {
				break loop
			}
			items = append(items, e)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].TS.Before(items[j].TS) })
	if tail > 0 && len(items) > tail {
		items = items[len(items)-tail:]
	}
	return items
}
