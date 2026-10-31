// Package projectconfig is the format core of nebula.toml: strict parsing with
// positions, environment resolution (merge, unset, only_in), static validation,
// diagnostics, the JSON Schema, fingerprints and the deterministic renderer.
//
// The package is pure: no database, no network, no file system. Anything the
// file refers to outside itself (compose files, file sources) is handed in as
// Attachments by the caller.
package projectconfig

// SchemaVersion is the only value of the root `schema` key.
const SchemaVersion = 1

// MaxFileBytes is the largest accepted nebula.toml.
const MaxFileBytes = 256 << 10

// Limits of the format. They are enforced by Parse/Resolve with
// limit_exceeded; the JSON Schema repeats the ones it can express.
const (
	MaxAttachments         = 8
	MaxAttachmentBytes     = 256 << 10
	MaxAttachmentsTotal    = 1 << 20
	MaxServices            = 64
	MaxVariablesPerLayer   = 500
	MaxDomainsPerService   = 50
	MaxFilesPerService     = 16
	MaxFileContentBytes    = 64 << 10
	MaxFilesTotalBytes     = 256 << 10
	MaxTasksPerService     = 20
	MaxProcessesPerService = 9 // named processes; the service's own process makes ten
	MaxBackupsPerService   = 20
	MaxVolumesPerService   = 32
	MaxWatchPaths          = 50
	MaxUpstreams           = 20
	MaxEnvironments        = 32
	MaxTableDepth          = 10
	MaxStringBytes         = 64 << 10
	MaxUnsetPaths          = 200
	MaxSecretKeys          = 500
	MaxComposeBytes        = 256 << 10
	maxDocumentCollection  = 4096 // sanity bound for any map/array of the document
)

// File is the typed model of a nebula.toml document exactly as written
// (before environment resolution). Scalars are pointers and collections are
// nil when absent, so "key present" is representable: a present key is owned
// by the file. The JSON Schema is generated from the tags below:
// `toml` is the key name, `json` repeats it, `doc`/`example` feed the schema's
// description/examples, and `default`, `enum`, `minimum`, `maximum`, `pattern`,
// `minLength`, `maxLength`, `maxItems` become the matching schema keywords.
type File struct {
	Schema            *int                    `toml:"schema" json:"schema" doc:"Version of the nebula.toml format. Only 1 exists." example:"1" enum:"1"`
	SchemaURL         *string                 `toml:"$schema" json:"$schema,omitempty" doc:"Editor hint pointing at the JSON Schema of this file. Ignored by NebulaCtrl." example:"https://nebula.example.com/schema/nebula.toml.json"`
	Project           *string                 `toml:"project" json:"project,omitempty" doc:"Slug of the project this file describes. Checked against the project it is applied to; a mismatch is a blocker." example:"acme-shop" pattern:"^[a-z0-9][a-z0-9-]{0,62}$"`
	Server            *string                 `toml:"server" json:"server,omitempty" doc:"Default server, by name, for every service that does not name its own." example:"fra-1" minLength:"1" maxLength:"200"`
	Variables         map[string]Variable     `toml:"variables" json:"variables,omitempty" doc:"Project-layer shared variables. Services read them as ${{shared.NAME}}. A value is a string using the variable macro grammar, or a table with metadata." example:"APP_NAME = \"Acme Shop\""`
	Secrets           *Secrets                `toml:"secrets" json:"secrets,omitempty" doc:"Names of project-layer shared secrets. Values are never written in the file."`
	Services          map[string]*Service     `toml:"services" json:"services,omitempty" doc:"The services of the project, keyed by service slug." example:"services.web"`
	EnvironmentBlocks map[string]*Environment `toml:"environments" json:"environments,omitempty" doc:"Per-environment overrides, keyed by environment slug. Environments are never created or deleted by the file." example:"environments.staging"`

	meta *fileMeta // parse state (tree, positions, attachments); nil for a File not built by Parse
}

// Environment is one [environments.<slug>] block.
type Environment struct {
	Server    *string             `toml:"server" json:"server,omitempty" doc:"Default server, by name, of this environment. Overrides the root server." example:"fra-staging" minLength:"1" maxLength:"200"`
	Variables map[string]Variable `toml:"variables" json:"variables,omitempty" doc:"Environment-layer shared variables. They override the project layer for this environment only."`
	Secrets   *Secrets            `toml:"secrets" json:"secrets,omitempty" doc:"Names of environment-layer shared secrets."`
	Services  map[string]*Service `toml:"services" json:"services,omitempty" doc:"Overrides of services for this environment. Keys merge over the base service; the source key is optional." example:"environments.staging.services.web"`
}

// Secrets declares secret names. The values are set in the dashboard, the CLI
// or a secret manager; the file never carries them.
type Secrets struct {
	Keys  []string `toml:"keys" json:"keys,omitempty" doc:"Variable names that are secret. The file has no value for them." example:"[\"STRIPE_SECRET_KEY\"]" maxItems:"500"`
	Scope *string  `toml:"scope" json:"scope,omitempty" doc:"Where the listed secrets are available: runtime, build or both." example:"runtime" enum:"runtime,build,both" default:"runtime"`
}

// Variable is one variable entry: a plain string (shorthand for {value}) or a
// table with metadata. An entry is a leaf for merging.
type Variable struct {
	Value           string `toml:"value" json:"value" doc:"The value, using the variable macro grammar: plain text, references like ${{db.DATABASE_URL}}, generators like ${{secret(32)}}." example:"production" maxLength:"65536"`
	Scope           string `toml:"scope" json:"scope,omitempty" doc:"Where the variable is available: runtime, build or both (build and both need a git service)." example:"runtime" enum:"runtime,build,both" default:"runtime"`
	Comment         string `toml:"comment" json:"comment,omitempty" doc:"Free-text note shown next to the variable." example:"read by the bundler" maxLength:"1000"`
	RotateEveryDays *int   `toml:"rotate_every_days" json:"rotate_every_days,omitempty" doc:"Reminder interval, in days, to rotate the value. 0 or absent means no reminder." example:"90" minimum:"0"`
	Public          bool   `toml:"public" json:"public,omitempty" doc:"Asserts that a literal value is not a credential, which silences the literal_secret check for this variable. It is not stored." example:"false" default:"false"`
}

// Service is a [services.<slug>] table, and also the body of an
// [environments.<env>.services.<slug>] override (where Unset is allowed and
// the source key is optional). Field order is the order Render writes keys:
// scalars first, then sub-tables.
type Service struct {
	Name             *string  `toml:"name" json:"name,omitempty" doc:"Display name of the service. Defaults to the slug." example:"Web" minLength:"1" maxLength:"120"`
	Server           *string  `toml:"server" json:"server,omitempty" doc:"Server, by name, the service runs on. Overrides the environment and root server. Required for route services." example:"fra-1" minLength:"1" maxLength:"200"`
	OnlyIn           []string `toml:"only_in" json:"only_in,omitempty" doc:"Environment slugs in which this service exists in the file. Absent means every environment. Not allowed in environment overrides." example:"[\"production\"]"`
	DependsOn        []string `toml:"depends_on" json:"depends_on,omitempty" doc:"Slugs of services that deploy before this one. References in variables add dependencies on their own." example:"[\"db\"]"`
	Unset            []string `toml:"unset" json:"unset,omitempty" doc:"Environment overrides only: TOML key paths removed from the merged service. The removed keys are simply not managed by the file in that environment; nothing live is deleted." example:"[\"scale\", \"domains.\\\"shop.acme.com\\\"\"]" maxItems:"200"`
	Networks         []string `toml:"networks" json:"networks,omitempty" doc:"Names of shared networks to attach the service to. The file never creates networks." example:"[\"backplane\"]"`
	GenerateDomain   *bool    `toml:"generate_domain" json:"generate_domain,omitempty" doc:"Attach the platform-generated hostname on the server's base domain when the service has no domain." example:"true" default:"false"`
	Image            *string  `toml:"image" json:"image,omitempty" doc:"Source key: container image to run, as repo[:tag][@digest]. Registry credentials are never written in the file." example:"ghcr.io/acme/web:1.4.2" minLength:"1" maxLength:"512" pattern:"^\\S+$"`
	Port             *int     `toml:"port" json:"port,omitempty" doc:"TCP port the service listens on for HTTP. 0 or absent makes it a worker: no HTTP health check and no domain." example:"3000" minimum:"0" maximum:"65535"`
	Command          []string `toml:"command" json:"command,omitempty" doc:"Start command as an argv array, run without a shell. An empty array clears it and uses the image's CMD." example:"[\"bun\", \"run\", \"worker\"]" minItems:"0" maxItems:"64"`
	ProcessName      *string  `toml:"process_name" json:"process_name,omitempty" doc:"Name of the service's own process (port, command, health_check, stop_grace_seconds, memory_mb, cpus). Only matters when the service has processes: it is the name logs, scaling and domains use for it." example:"web" pattern:"^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$" default:"web"`
	Singleton        *bool    `toml:"singleton" json:"singleton,omitempty" doc:"The service's own process never runs two instances at once: the old container stops before the new one starts, and scaling is off." example:"false" default:"false"`
	OneShot          *bool    `toml:"one_shot" json:"one_shot,omitempty" doc:"Run the container to completion on every deploy. Excludes pre_deploy and post_deploy and a port." example:"false" default:"false"`
	StopGraceSeconds *int     `toml:"stop_grace_seconds" json:"stop_grace_seconds,omitempty" doc:"Seconds a container gets to stop before it is killed." example:"30" minimum:"1" maximum:"3600" default:"30"`
	MemoryMB         *int     `toml:"memory_mb" json:"memory_mb,omitempty" doc:"Memory limit in MiB. 0 means no limit; otherwise at least 6." example:"512" minimum:"0"`
	CPUs             *float64 `toml:"cpus" json:"cpus,omitempty" doc:"CPU limit in cores. 0 means no limit." example:"1.5" minimum:"0"`
	ImageRetention   *int     `toml:"image_retention" json:"image_retention,omitempty" doc:"How many previous images are kept on the server." example:"5" minimum:"1" maximum:"50" default:"5"`
	BlockEgress      *bool    `toml:"block_egress" json:"block_egress,omitempty" doc:"Block outbound internet access. The service then only reaches its environment's internal network and cannot join shared networks." example:"false" default:"false"`

	Git         *Git         `toml:"git" json:"git,omitempty" doc:"Source key: build the service from a git repository."`
	Build       *Build       `toml:"build" json:"build,omitempty" doc:"How a git service is built."`
	Database    *Database    `toml:"database" json:"database,omitempty" doc:"Source key: a managed database. The keys are create-only."`
	Route       *Route       `toml:"route" json:"route,omitempty" doc:"Source key: an external route to an upstream the platform does not run, or a TCP/UDP exposure."`
	Stack       *Stack       `toml:"stack" json:"stack,omitempty" doc:"Source key: a Docker Compose project run as one service."`
	HealthCheck *HealthCheck `toml:"health_check" json:"health_check,omitempty" doc:"How a new container is judged healthy. Each key is owned individually."`
	PreDeploy   *Hook        `toml:"pre_deploy" json:"pre_deploy,omitempty" doc:"Command run before the new release starts. A non-zero exit fails the deploy."`
	PostDeploy  *Hook        `toml:"post_deploy" json:"post_deploy,omitempty" doc:"Command run after the new release is live. A failure only warns."`
	Docker      *Docker      `toml:"docker" json:"docker,omitempty" doc:"Typed container options. Each key is owned individually. Devices, GPUs, runtime and host-level options need an unrestricted server."`
	Scale       *Scale       `toml:"scale" json:"scale,omitempty" doc:"Replicas and autoscaling. Applied immediately."`
	Uptime      *Uptime      `toml:"uptime" json:"uptime,omitempty" doc:"Uptime monitor settings. Applied immediately."`
	Secrets     *Secrets     `toml:"secrets" json:"secrets,omitempty" doc:"Names of secret variables of this service. Values are never written in the file."`

	Variables map[string]Variable `toml:"variables" json:"variables,omitempty" doc:"Variables of the service, keyed by name. A value is a string using the variable macro grammar, or a table with metadata."`
	Domains   map[string]*Domain  `toml:"domains" json:"domains,omitempty" doc:"Domains of the service, keyed by hostname or hostname/path-prefix." example:"\"shop.acme.com\""`
	Volumes   map[string]string   `toml:"volumes" json:"volumes,omitempty" doc:"Persistent volumes: name = absolute mount path. Detaching a volume never deletes its data." example:"uploads = \"/app/uploads\""`
	Files     map[string]*Files   `toml:"files" json:"files,omitempty" doc:"Config files written into the container before it starts, keyed by absolute path." example:"\"/etc/app/app.conf\""`
	Tasks     map[string]*Task    `toml:"tasks" json:"tasks,omitempty" doc:"Cron tasks that run in the service's image, keyed by task name. Applied immediately." example:"digest"`
	Processes map[string]*Process `toml:"processes" json:"processes,omitempty" doc:"Additional processes that run from the same image, variables, volumes and release as the service, keyed by process name. The service's own port and command are its default process. A process is owned as a whole: a key you leave out takes its default. Processes deploy together; scale is applied immediately." example:"worker"`
	Backups   map[string]*Backup  `toml:"backups" json:"backups,omitempty" doc:"Backup schedules of a database, keyed by a stable key. Applied immediately." example:"nightly"`
}

// Git is the [services.<slug>.git] table.
type Git struct {
	URL        *string  `toml:"url" json:"url,omitempty" doc:"Clone URL (https, http, git, ssh or git@host:org/repo.git). Credentials in the URL are rejected. Exclusive with connection and repo." example:"https://github.com/acme/shop.git" maxLength:"512"`
	Connection *string  `toml:"connection" json:"connection,omitempty" doc:"Name of a git connection (Team settings → Integrations). Credentials stay in the connection. Needs repo." example:"github-acme" minLength:"1" maxLength:"200"`
	Repo       *string  `toml:"repo" json:"repo,omitempty" doc:"Repository as owner/name on the connection." example:"acme/shop" pattern:"^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$"`
	Branch     *string  `toml:"branch" json:"branch,omitempty" doc:"Branch to build." example:"main" minLength:"1" maxLength:"255"`
	Submodules *string  `toml:"submodules" json:"submodules,omitempty" doc:"How submodules are checked out: auto, on or off." example:"auto" enum:"auto,on,off" default:"auto"`
	AutoDeploy *bool    `toml:"auto_deploy" json:"auto_deploy,omitempty" doc:"Deploy on every push to the branch. Needs connection. Applied immediately." example:"true" default:"true"`
	WatchPaths []string `toml:"watch_paths" json:"watch_paths,omitempty" doc:"Monorepo filter: only pushes touching these globs deploy; a leading ! excludes. Replaces the whole list. Needs connection. Applied immediately." example:"[\"apps/web/**\", \"!**/*.md\"]" maxItems:"50"`
}

// Build is the [services.<slug>.build] table of a git service.
type Build struct {
	Type       *string `toml:"type" json:"type,omitempty" doc:"Build method: auto, dockerfile, railpack or starter:<node|python|go|ruby|php|static>." example:"dockerfile" pattern:"^(auto|dockerfile|railpack|starter:(node|python|go|ruby|php|static))$" default:"auto"`
	Dockerfile *string `toml:"dockerfile" json:"dockerfile,omitempty" doc:"Path of the Dockerfile, relative to the repository root." example:"apps/web/Dockerfile" minLength:"1" maxLength:"512" default:"Dockerfile"`
	Context    *string `toml:"context" json:"context,omitempty" doc:"Build context directory, relative to the repository root." example:"." minLength:"1" maxLength:"512" default:"."`
	Location   *string `toml:"location" json:"location,omitempty" doc:"Where the build runs: inherit, target (the deploying server) or build_server." example:"inherit" enum:"inherit,target,build_server" default:"inherit"`
	Server     *string `toml:"server" json:"server,omitempty" doc:"Name of the build server when location is build_server." example:"builder-1" minLength:"1" maxLength:"200"`
	Registry   *string `toml:"registry" json:"registry,omitempty" doc:"Name of the registry that receives images built on a build server." example:"ghcr" minLength:"1" maxLength:"200"`
}

// HealthCheck is the [services.<slug>.health_check] table.
type HealthCheck struct {
	Type               *string  `toml:"type" json:"type,omitempty" doc:"Probe type: http, tcp, docker, exec or none. Defaults to http when the service has a port, else none." example:"http" enum:"http,tcp,docker,exec,none"`
	Path               *string  `toml:"path" json:"path,omitempty" doc:"Path probed by an http check. Must start with /." example:"/healthz" pattern:"^/" default:"/"`
	Port               *int     `toml:"port" json:"port,omitempty" doc:"Port probed by an http or tcp check. Defaults to the service port." example:"3000" minimum:"0" maximum:"65535"`
	IntervalSeconds    *int     `toml:"interval_seconds" json:"interval_seconds,omitempty" doc:"Seconds between probes." example:"2" minimum:"1" default:"2"`
	TimeoutSeconds     *int     `toml:"timeout_seconds" json:"timeout_seconds,omitempty" doc:"Seconds before one probe counts as failed." example:"3" minimum:"1" default:"3"`
	StartPeriodSeconds *int     `toml:"start_period_seconds" json:"start_period_seconds,omitempty" doc:"Seconds after start during which failures are ignored." example:"0" minimum:"0" default:"0"`
	DeadlineSeconds    *int     `toml:"deadline_seconds" json:"deadline_seconds,omitempty" doc:"Seconds the deploy waits for the service to become healthy." example:"90" minimum:"1" maximum:"3600" default:"120"`
	SuccessThreshold   *int     `toml:"success_threshold" json:"success_threshold,omitempty" doc:"Consecutive successful probes needed." example:"2" minimum:"1" default:"2"`
	Command            []string `toml:"command" json:"command,omitempty" doc:"argv of an exec check; the first element is CMD or CMD-SHELL." example:"[\"CMD\", \"true\"]" minItems:"2"`
}

// Hook is a [services.<slug>.pre_deploy] or post_deploy table.
type Hook struct {
	Command        []string `toml:"command" json:"command,omitempty" doc:"The command as an argv array, run without a shell." example:"[\"bun\", \"run\", \"db:migrate\"]" minItems:"1" maxItems:"64"`
	TimeoutSeconds *int     `toml:"timeout_seconds" json:"timeout_seconds,omitempty" doc:"Seconds before the command is killed. Needs command in the same table." example:"300" minimum:"1" maximum:"3600" default:"600"`
}

// Database is the [services.<slug>.database] table. Create-only: on an
// existing database any difference is an immutable_field blocker.
type Database struct {
	Engine        *string `toml:"engine" json:"engine,omitempty" doc:"Database engine." example:"postgres" enum:"postgres,mysql,mariadb,valkey,redis,mongodb,clickhouse"`
	Version       *string `toml:"version" json:"version,omitempty" doc:"Engine version. Defaults to the engine's default version." example:"17" maxLength:"64"`
	Flavor        *string `toml:"flavor" json:"flavor,omitempty" doc:"Engine flavor, where the engine has more than one." example:"" maxLength:"64"`
	Name          *string `toml:"name" json:"name,omitempty" doc:"Name of the initial database. Defaults to the engine's default." example:"shop" maxLength:"63"`
	Username      *string `toml:"username" json:"username,omitempty" doc:"Name of the application user. Defaults to the engine's default." example:"shop" maxLength:"63"`
	AcceptLicense *bool   `toml:"accept_license" json:"accept_license,omitempty" doc:"Acknowledge the licence of a source-available engine. Required for those engines. Not stored." example:"false" default:"false"`
}

// Route is the [services.<slug>.route] table of a route service.
type Route struct {
	Protocol           *string    `toml:"protocol" json:"protocol,omitempty" doc:"Route protocol: http, tcp or udp. Create-only." example:"http" enum:"http,tcp,udp"`
	Upstreams          []Upstream `toml:"upstreams" json:"upstreams,omitempty" doc:"Upstreams, 1 to 20. Each is { address = \"host:port\" } or { service = \"slug\", port = 1234 }. Replaces the list." example:"[{ address = \"192.168.1.20:631\" }]" minItems:"1" maxItems:"20"`
	Scheme             *string    `toml:"scheme" json:"scheme,omitempty" doc:"Upstream scheme of an http route." example:"http" enum:"http,https" default:"http"`
	InsecureSkipVerify *bool      `toml:"insecure_skip_verify" json:"insecure_skip_verify,omitempty" doc:"Do not verify the TLS certificate of https upstreams." example:"false" default:"false"`
	ServerName         *string    `toml:"server_name" json:"server_name,omitempty" doc:"TLS server name used to verify https upstreams." example:"printer.lan" maxLength:"253"`
	PassHost           *bool      `toml:"pass_host" json:"pass_host,omitempty" doc:"Forward the original Host header to the upstream." example:"true" default:"true"`
	HealthPath         *string    `toml:"health_path" json:"health_path,omitempty" doc:"Path probed to verify the upstream. Must start with /." example:"/" maxLength:"200" pattern:"^/"`
	VerifySkip         *bool      `toml:"verify_skip" json:"verify_skip,omitempty" doc:"Skip the post-apply verification of an upstream that legitimately answers with 5xx." example:"false" default:"false"`
	ListenPort         *int       `toml:"listen_port" json:"listen_port,omitempty" doc:"Host port a tcp or udp route listens on." example:"25565" minimum:"1024" maximum:"65535"`
	AllowCIDRs         []string   `toml:"allow_cidrs" json:"allow_cidrs,omitempty" doc:"CIDRs allowed to reach a tcp route. UDP routes cannot be restricted." example:"[\"203.0.113.0/24\"]" maxItems:"200"`
	DenyCIDRs          []string   `toml:"deny_cidrs" json:"deny_cidrs,omitempty" doc:"CIDRs refused even when allow_cidrs admits them." example:"[\"198.51.100.0/24\"]" maxItems:"200"`
	OpenToInternet     *bool      `toml:"open_to_internet" json:"open_to_internet,omitempty" doc:"Publish a tcp or udp route to everyone. Required when allow_cidrs is empty." example:"false" default:"false"`
	ProxyProtocol      *int       `toml:"proxy_protocol" json:"proxy_protocol,omitempty" doc:"PROXY protocol version sent to a tcp upstream: 0, 1 or 2." example:"0" enum:"0,1,2" default:"0"`
}

// Upstream is one entry of route.upstreams: an external address, or a managed
// service with a port.
type Upstream struct {
	Address *string `toml:"address" json:"address,omitempty" doc:"External upstream as host:port." example:"192.168.1.20:631" maxLength:"300"`
	Service *string `toml:"service" json:"service,omitempty" doc:"Slug of a managed service of the same environment and server." example:"web" pattern:"^[a-z0-9][a-z0-9-]{0,62}$"`
	Port    *int    `toml:"port" json:"port,omitempty" doc:"Port of the managed service." example:"3000" minimum:"1" maximum:"65535"`
}

// Stack is the [services.<slug>.stack] table of a Docker Compose project.
type Stack struct {
	Compose     *string                   `toml:"compose" json:"compose,omitempty" doc:"The compose file as text. Exactly one of compose and compose_file." example:"services:\n  web:\n    image: nginx" minLength:"1" maxLength:"262144"`
	ComposeFile *string                   `toml:"compose_file" json:"compose_file,omitempty" doc:"Path of the compose file relative to nebula.toml; the caller supplies its content as an attachment." example:"docker-compose.yml" minLength:"1" maxLength:"512"`
	WaitSeconds *int                      `toml:"wait_seconds" json:"wait_seconds,omitempty" doc:"Seconds a deploy waits for every component to become healthy." example:"300" minimum:"1" maximum:"3600" default:"300"`
	Resources   map[string]*StackResource `toml:"resources" json:"resources,omitempty" doc:"Per-component limits, keyed by the compose service name. The compose file's own limits win."`
}

// StackResource limits one component of a stack.
type StackResource struct {
	MemoryMB *int     `toml:"memory_mb" json:"memory_mb,omitempty" doc:"Memory limit of the component in MiB. 0 means no limit; otherwise at least 6." example:"256" minimum:"0"`
	CPUs     *float64 `toml:"cpus" json:"cpus,omitempty" doc:"CPU limit of the component in cores." example:"0.5" minimum:"0"`
}

// Domain is one [services.<slug>.domains."<host>[/<path-prefix>]"] table.
type Domain struct {
	TargetPort   *int    `toml:"target_port" json:"target_port,omitempty" doc:"Port of the service this domain targets. Defaults to the service port, or to the port of the process the domain routes to." example:"3000" minimum:"1" maximum:"65535"`
	Component    *string `toml:"component" json:"component,omitempty" doc:"Compose component this domain targets. Required for stacks." example:"web" maxLength:"63"`
	Process      *string `toml:"process" json:"process,omitempty" doc:"Process of a service with processes that this domain routes to. Absent means the service's own process, or the only process with a port. Apps only; stacks use component." example:"web" pattern:"^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$"`
	StripPrefix  *bool   `toml:"strip_prefix" json:"strip_prefix,omitempty" doc:"Remove the path prefix before forwarding." example:"false" default:"false"`
	AddPrefix    *string `toml:"add_prefix" json:"add_prefix,omitempty" doc:"Prefix added to the path before forwarding." example:"/v1" maxLength:"200"`
	Kind         *string `toml:"kind" json:"kind,omitempty" doc:"serve the service or redirect to another host." example:"serve" enum:"serve,redirect" default:"serve"`
	RedirectTo   *string `toml:"redirect_to" json:"redirect_to,omitempty" doc:"Target hostname of a redirect. Required when kind is redirect." example:"www.acme.com" maxLength:"253"`
	RedirectCode *int    `toml:"redirect_code" json:"redirect_code,omitempty" doc:"HTTP status of a redirect." example:"308" enum:"301,302,307,308" default:"308"`
	PreservePath *bool   `toml:"preserve_path" json:"preserve_path,omitempty" doc:"Keep the path and query when redirecting." example:"true" default:"true"`
	TLS          *string `toml:"tls" json:"tls,omitempty" doc:"Certificate mode: auto, custom or none." example:"auto" enum:"auto,custom,none" default:"auto"`
	Certificate  *string `toml:"certificate" json:"certificate,omitempty" doc:"Name of an uploaded certificate of the service's server. Needs tls = custom." example:"wildcard-acme" minLength:"1" maxLength:"200"`
	DNSResolver  *string `toml:"dns_resolver" json:"dns_resolver,omitempty" doc:"DNS-01 provider code that obtains this domain's certificate." example:"cloudflare" pattern:"^[a-z0-9][a-z0-9-]{0,39}$"`
	WWWRedirect  *bool   `toml:"www_redirect" json:"www_redirect,omitempty" doc:"Also serve www.<host> as a redirect to the host." example:"true" default:"false"`

	Options    *RouteOptions `toml:"options" json:"options,omitempty" doc:"Proxy options of the domain. The whole table is one unit: present means it replaces the live option set."`
	Cloudflare *Cloudflare   `toml:"cloudflare" json:"cloudflare,omitempty" doc:"Cloudflare settings of the hostname. NebulaCtrl always manages the DNS record when the hostname's zone is in the team's connected Cloudflare account. Applied immediately; needs a Cloudflare connection."`
}

// Cloudflare is [services.<slug>.domains.<host>.cloudflare]. NebulaCtrl always manages DNS for a
// hostname whose zone is in the connected Cloudflare account, so there is no key to turn that off.
type Cloudflare struct {
	Proxied    *bool `toml:"proxied" json:"proxied,omitempty" doc:"Route traffic through the Cloudflare proxy (orange cloud). Absent means true for a name NebulaCtrl starts managing, except a wildcard name or a non-HTTP(S) port, which default to DNS only; a name behind a Cloudflare Tunnel is always proxied. Absent never changes a name that already exists: it keeps its current setting. Write false to stay DNS only." example:"true" default:"true"`
	OriginLock *bool `toml:"origin_lock" json:"origin_lock,omitempty" doc:"Lock the origin so only Cloudflare can reach it." example:"false"`
}

// RouteOptions is [services.<slug>.domains.<host>.options].
type RouteOptions struct {
	ForceHTTPS      *bool             `toml:"force_https" json:"force_https,omitempty" doc:"Redirect plain HTTP to HTTPS." example:"true" default:"true"`
	Compress        *bool             `toml:"compress" json:"compress,omitempty" doc:"Compress responses." example:"true"`
	Noindex         *bool             `toml:"noindex" json:"noindex,omitempty" doc:"Send X-Robots-Tag: noindex, nofollow." example:"false"`
	SecurityHeaders *string           `toml:"security_headers" json:"security_headers,omitempty" doc:"Security header set: basic or strict (adds HSTS)." example:"strict" enum:"basic,strict"`
	RequestHeaders  map[string]string `toml:"request_headers" json:"request_headers,omitempty" doc:"Headers added to requests, name = value." example:"X-Forwarded-Prefix = \"/app\""`
	ResponseHeaders map[string]string `toml:"response_headers" json:"response_headers,omitempty" doc:"Headers added to responses, name = value." example:"X-Frame-Options = \"DENY\""`
	CORS            *CORS             `toml:"cors" json:"cors,omitempty" doc:"Cross-origin response headers."`
	Redirects       []Redirect        `toml:"redirects" json:"redirects,omitempty" doc:"Ordered regex redirects." maxItems:"50"`
	RateLimit       *RateLimit        `toml:"rate_limit" json:"rate_limit,omitempty" doc:"Request rate limit."`
	Access          *Access           `toml:"access" json:"access,omitempty" doc:"IP allow and deny lists."`
	BasicAuth       *bool             `toml:"basic_auth" json:"basic_auth,omitempty" doc:"Require basic authentication. Enables only: users and passwords are never in the file." example:"true"`
	LargeUploads    *bool             `toml:"large_uploads" json:"large_uploads,omitempty" doc:"Allow request bodies that take minutes to send, for example container registry layer uploads." example:"false"`
}

// CORS is options.cors.
type CORS struct {
	Origins       []string `toml:"origins" json:"origins,omitempty" doc:"Allowed origins, or *." example:"[\"https://app.acme.com\"]" minItems:"1"`
	Methods       []string `toml:"methods" json:"methods,omitempty" doc:"Allowed methods." example:"[\"GET\", \"POST\"]"`
	Headers       []string `toml:"headers" json:"headers,omitempty" doc:"Allowed request headers." example:"[\"Authorization\"]"`
	ExposeHeaders []string `toml:"expose_headers" json:"expose_headers,omitempty" doc:"Response headers exposed to the browser." example:"[\"X-Request-Id\"]"`
	Credentials   *bool    `toml:"credentials" json:"credentials,omitempty" doc:"Allow credentials." example:"false"`
	MaxAgeSeconds *int     `toml:"max_age_seconds" json:"max_age_seconds,omitempty" doc:"How long browsers cache the preflight answer." example:"600" minimum:"0"`
}

// Redirect is one options.redirects entry.
type Redirect struct {
	Regex       *string `toml:"regex" json:"regex,omitempty" doc:"Go regular expression matched against the request URL." example:"^https://acme.com/old/(.*)"`
	Replacement *string `toml:"replacement" json:"replacement,omitempty" doc:"Replacement, with ${1} style groups." example:"https://acme.com/new/${1}"`
	Permanent   *bool   `toml:"permanent" json:"permanent,omitempty" doc:"Answer 301 instead of 302." example:"true"`
}

// RateLimit is options.rate_limit.
type RateLimit struct {
	Average           *int `toml:"average" json:"average,omitempty" doc:"Requests allowed per period on average." example:"100" minimum:"1"`
	PeriodSeconds     *int `toml:"period_seconds" json:"period_seconds,omitempty" doc:"Length of the period in seconds." example:"1" minimum:"1"`
	Burst             *int `toml:"burst" json:"burst,omitempty" doc:"Maximum burst above the average." example:"200" minimum:"1"`
	TrustedProxyDepth *int `toml:"trusted_proxy_depth" json:"trusted_proxy_depth,omitempty" doc:"Number of trusted proxies in front, so the real client IP is limited." example:"0" minimum:"0"`
}

// Access is options.access.
type Access struct {
	AllowCIDRs        []string `toml:"allow_cidrs" json:"allow_cidrs,omitempty" doc:"CIDRs allowed. Empty means everyone not denied." example:"[\"203.0.113.0/24\"]" maxItems:"200"`
	DenyCIDRs         []string `toml:"deny_cidrs" json:"deny_cidrs,omitempty" doc:"CIDRs refused first." example:"[\"198.51.100.0/24\"]" maxItems:"200"`
	TrustedProxyDepth *int     `toml:"trusted_proxy_depth" json:"trusted_proxy_depth,omitempty" doc:"Number of trusted proxies in front, so the real client IP is matched." example:"0" minimum:"0"`
	RejectStatus      *int     `toml:"reject_status" json:"reject_status,omitempty" doc:"HTTP status for refused clients." example:"403" minimum:"400" maximum:"599"`
}

// Files is one [services.<slug>.files."<absolute path>"] table (a config file
// mounted into the container). The Go name avoids the clash with the map key.
type Files struct {
	Mode    *string `toml:"mode" json:"mode,omitempty" doc:"File mode as an octal string." example:"0644" pattern:"^0?[0-7]{3,4}$" default:"0644"`
	Content *string `toml:"content" json:"content,omitempty" doc:"File text. Stored in the service config as plain text: never put secrets here. Exactly one of content and source." example:"listen = 8080\n" maxLength:"65536"`
	Source  *string `toml:"source" json:"source,omitempty" doc:"Path, relative to nebula.toml, of a file whose content is used; the caller supplies it as an attachment." example:"config/app.conf" minLength:"1" maxLength:"512"`
}

// Scale is [services.<slug>.scale].
type Scale struct {
	Replicas    *int         `toml:"replicas" json:"replicas,omitempty" doc:"Number of replicas." example:"2" minimum:"1" maximum:"10" default:"1"`
	Autoscaling *Autoscaling `toml:"autoscaling" json:"autoscaling,omitempty" doc:"Autoscaling on CPU."`
}

// Autoscaling is scale.autoscaling.
type Autoscaling struct {
	Enabled          *bool `toml:"enabled" json:"enabled,omitempty" doc:"Scale replicas automatically." example:"false" default:"false"`
	Min              *int  `toml:"min" json:"min,omitempty" doc:"Fewest replicas." example:"1" minimum:"1" maximum:"10" default:"1"`
	Max              *int  `toml:"max" json:"max,omitempty" doc:"Most replicas. At least min." example:"3" minimum:"1" maximum:"10" default:"3"`
	TargetCPUPercent *int  `toml:"target_cpu_percent" json:"target_cpu_percent,omitempty" doc:"Average CPU utilisation the autoscaler aims for." example:"70" minimum:"20" maximum:"95" default:"70"`
}

// Uptime is [services.<slug>.uptime].
type Uptime struct {
	Enabled           *bool `toml:"enabled" json:"enabled,omitempty" doc:"Probe the service." example:"true" default:"true"`
	Alerts            *bool `toml:"alerts" json:"alerts,omitempty" doc:"Send notifications for outages." example:"true" default:"true"`
	FailureThreshold  *int  `toml:"failure_threshold" json:"failure_threshold,omitempty" doc:"Consecutive failed checks that open an outage." example:"3" minimum:"1" maximum:"20" default:"3"`
	RecoveryThreshold *int  `toml:"recovery_threshold" json:"recovery_threshold,omitempty" doc:"Consecutive successful checks that close an outage." example:"2" minimum:"1" maximum:"20" default:"2"`
	ReminderMinutes   *int  `toml:"reminder_minutes" json:"reminder_minutes,omitempty" doc:"Minutes between reminders of an ongoing outage. 0 disables reminders." example:"60" minimum:"0" maximum:"1440" default:"0"`
}

// Process is [services.<slug>.processes.<name>], an additional process of a
// service. It shares the service's image, variables, volumes and release.
type Process struct {
	Command          []string `toml:"command" json:"command,omitempty" doc:"The process command as an argv array, run without a shell in the service's image." example:"[\"bundle\", \"exec\", \"sidekiq\"]" minItems:"1" maxItems:"64"`
	Port             *int     `toml:"port" json:"port,omitempty" doc:"TCP port the process listens on for HTTP. 0 or absent means no HTTP port. Ports are unique across the service's processes." example:"3001" minimum:"0" maximum:"65535"`
	StopGraceSeconds *int     `toml:"stop_grace_seconds" json:"stop_grace_seconds,omitempty" doc:"Seconds a container of this process gets to stop before it is killed. Absent means the service's value." example:"120" minimum:"1" maximum:"3600"`
	Singleton        *bool    `toml:"singleton" json:"singleton,omitempty" doc:"The process never runs two instances at once: the old container stops before the new one starts, and scaling is off." example:"true" default:"false"`
	MemoryMB         *int     `toml:"memory_mb" json:"memory_mb,omitempty" doc:"Memory limit of the process in MiB. 0 or absent means the service's limit; otherwise at least 6." example:"512" minimum:"0"`
	CPUs             *float64 `toml:"cpus" json:"cpus,omitempty" doc:"CPU limit of the process in cores. 0 or absent means the service's limit." example:"1.5" minimum:"0"`

	HealthCheck *HealthCheck `toml:"health_check" json:"health_check,omitempty" doc:"How a new container of this process is judged healthy. Same keys as the service's health_check; the default port is the process's own."`
	Scale       *Scale       `toml:"scale" json:"scale,omitempty" doc:"Replicas and autoscaling of this process. Applied immediately."`
}

// Task is [services.<slug>.tasks.<name>], a cron task.
type Task struct {
	Schedule       *string  `toml:"schedule" json:"schedule,omitempty" doc:"Cron expression with 5 fields, or @hourly, @daily, @weekly, @monthly, @yearly." example:"0 7 * * 1-5" minLength:"1" maxLength:"200"`
	Timezone       *string  `toml:"timezone" json:"timezone,omitempty" doc:"IANA time zone of the schedule." example:"Europe/Berlin" maxLength:"64" default:"UTC"`
	Command        []string `toml:"command" json:"command,omitempty" doc:"The command as an argv array, run without a shell in the service's image." example:"[\"bun\", \"run\", \"jobs:digest\"]" minItems:"1" maxItems:"64"`
	Enabled        *bool    `toml:"enabled" json:"enabled,omitempty" doc:"Run the task on schedule." example:"true" default:"true"`
	TimeoutSeconds *int     `toml:"timeout_seconds" json:"timeout_seconds,omitempty" doc:"Seconds before a run is killed." example:"900" minimum:"1" maximum:"86400" default:"1800"`
	Concurrency    *string  `toml:"concurrency" json:"concurrency,omitempty" doc:"forbid skips a run while the previous one is still running; allow starts it anyway." example:"forbid" enum:"forbid,allow" default:"forbid"`
}

// Backup is [services.<slug>.backups.<key>], a database backup schedule.
type Backup struct {
	Destination    *string `toml:"destination" json:"destination,omitempty" doc:"Name of the backup destination." example:"offsite-s3" minLength:"1" maxLength:"200"`
	Cron           *string `toml:"cron" json:"cron,omitempty" doc:"Cron expression with 5 fields, or @hourly, @daily, @weekly, @monthly, @yearly. At most one run per 5 minutes." example:"0 2 * * *" minLength:"1" maxLength:"200"`
	Timezone       *string `toml:"timezone" json:"timezone,omitempty" doc:"IANA time zone of the schedule." example:"Europe/Berlin" maxLength:"64" default:"UTC"`
	Enabled        *bool   `toml:"enabled" json:"enabled,omitempty" doc:"Run the schedule." example:"true" default:"true"`
	RetentionCount *int    `toml:"retention_count" json:"retention_count,omitempty" doc:"How many backups to keep. 0 means no limit." example:"14" minimum:"0" maximum:"1000" default:"7"`
	RetentionDays  *int    `toml:"retention_days" json:"retention_days,omitempty" doc:"How many days to keep backups. 0 means no limit." example:"30" minimum:"0" maximum:"3650" default:"0"`
}

// Docker is [services.<slug>.docker]. Each key is owned individually.
type Docker struct {
	CapAdd             []string           `toml:"cap_add" json:"cap_add,omitempty" doc:"Linux capabilities to add. NET_ADMIN and SYS_ADMIN need an unrestricted server." example:"[\"NET_BIND_SERVICE\"]"`
	CapDrop            []string           `toml:"cap_drop" json:"cap_drop,omitempty" doc:"Linux capabilities to drop, or ALL." example:"[\"ALL\"]"`
	NoNewPrivileges    *bool              `toml:"no_new_privileges" json:"no_new_privileges,omitempty" doc:"Set the no-new-privileges security option." example:"true"`
	ReadOnlyRootfs     *bool              `toml:"read_only_rootfs" json:"read_only_rootfs,omitempty" doc:"Mount the root filesystem read-only." example:"true"`
	PidsLimit          *int               `toml:"pids_limit" json:"pids_limit,omitempty" doc:"Maximum number of processes and threads." example:"512" minimum:"0"`
	Hostname           *string            `toml:"hostname" json:"hostname,omitempty" doc:"Container hostname, an RFC 1123 label." example:"web" maxLength:"63"`
	DNS                []string           `toml:"dns" json:"dns,omitempty" doc:"Resolver IP addresses, at most 3." example:"[\"1.1.1.1\"]" maxItems:"3"`
	Sysctls            map[string]string  `toml:"sysctls" json:"sysctls,omitempty" doc:"Namespaced kernel parameters from the allowed list." example:"\"net.core.somaxconn\" = \"1024\""`
	HostSysctls        map[string]int     `toml:"host_sysctls" json:"host_sysctls,omitempty" doc:"Kernel parameters the application needs on the host, as minimum values. The plan warns when the host is lower." example:"\"vm.max_map_count\" = 262144"`
	User               *string            `toml:"user" json:"user,omitempty" doc:"User to run as, uid[:gid] or names." example:"1000:1000" maxLength:"128"`
	GroupAdd           []string           `toml:"group_add" json:"group_add,omitempty" doc:"Supplementary numeric group IDs." example:"[\"44\"]"`
	Ulimits            map[string]*Ulimit `toml:"ulimits" json:"ulimits,omitempty" doc:"Resource limits keyed nofile or memlock." example:"nofile = { soft = 65536, hard = 65536 }"`
	ShmSizeMB          *int               `toml:"shm_size_mb" json:"shm_size_mb,omitempty" doc:"Size of /dev/shm in MiB." example:"256" minimum:"0"`
	ExtraHosts         []string           `toml:"extra_hosts" json:"extra_hosts,omitempty" doc:"/etc/hosts entries as name:ip or name:host-gateway." example:"[\"db.local:10.0.0.5\"]"`
	Labels             map[string]string  `toml:"labels" json:"labels,omitempty" doc:"Container labels. com.docker.*, nebulactrl.* and traefik.* are reserved." example:"\"team\" = \"shop\""`
	Init               *bool              `toml:"init" json:"init,omitempty" doc:"Run a minimal init as PID 1." example:"true"`
	StopSignal         *string            `toml:"stop_signal" json:"stop_signal,omitempty" doc:"Signal used to stop the container." example:"SIGQUIT" maxLength:"32"`
	Tmpfs              map[string]*Tmpfs  `toml:"tmpfs" json:"tmpfs,omitempty" doc:"In-memory filesystems keyed by absolute path." example:"\"/tmp\" = { size_mb = 64 }"`
	Entrypoint         []string           `toml:"entrypoint" json:"entrypoint,omitempty" doc:"Replaces the image's ENTRYPOINT, as an argv array." example:"[\"/entrypoint.sh\"]" minItems:"1" maxItems:"64"`
	Devices            []Device           `toml:"devices" json:"devices,omitempty" doc:"Host devices mapped into the container. Unrestricted servers only." example:"[{ host_path = \"/dev/dri/renderD128\" }]"`
	GPUs               *GPUs              `toml:"gpus" json:"gpus,omitempty" doc:"GPU request. Unrestricted servers only."`
	Runtime            *string            `toml:"runtime" json:"runtime,omitempty" doc:"OCI runtime configured in the daemon. Unrestricted servers only." example:"runsc"`
	Privileged         *bool              `toml:"privileged" json:"privileged,omitempty" doc:"Give the container host-root equivalent rights. Unrestricted servers only." example:"false"`
	PidHost            *bool              `toml:"pid_host" json:"pid_host,omitempty" doc:"Share the host's PID namespace. Unrestricted servers only." example:"false"`
	SeccompUnconfined  *bool              `toml:"seccomp_unconfined" json:"seccomp_unconfined,omitempty" doc:"Remove the default syscall filter. Unrestricted servers only." example:"false"`
	ApparmorUnconfined *bool              `toml:"apparmor_unconfined" json:"apparmor_unconfined,omitempty" doc:"Remove the default AppArmor profile. Unrestricted servers only." example:"false"`
}

// Ulimit is docker.ulimits.<name>.
type Ulimit struct {
	Soft *int64 `toml:"soft" json:"soft,omitempty" doc:"Soft limit; -1 means unlimited." example:"65536" minimum:"-1"`
	Hard *int64 `toml:"hard" json:"hard,omitempty" doc:"Hard limit; -1 means unlimited." example:"65536" minimum:"-1"`
}

// Tmpfs is docker.tmpfs.<path>.
type Tmpfs struct {
	SizeMB *int    `toml:"size_mb" json:"size_mb,omitempty" doc:"Size in MiB." example:"64" minimum:"1"`
	Mode   *string `toml:"mode" json:"mode,omitempty" doc:"Octal mode." example:"1777" pattern:"^[0-7]{3,4}$"`
}

// Device is one docker.devices entry.
type Device struct {
	HostPath      *string `toml:"host_path" json:"host_path,omitempty" doc:"Device node on the host, under /dev." example:"/dev/dri/renderD128" pattern:"^/dev/"`
	ContainerPath *string `toml:"container_path" json:"container_path,omitempty" doc:"Where it appears in the container. Defaults to the host path." example:"/dev/dri/renderD128"`
	Permissions   *string `toml:"permissions" json:"permissions,omitempty" doc:"Subset of rwm." example:"rw" pattern:"^[rwm]{1,3}$"`
}

// GPUs is docker.gpus.
type GPUs struct {
	Count        *int     `toml:"count" json:"count,omitempty" doc:"Number of GPUs, or -1 for all." example:"1" minimum:"-1"`
	Driver       *string  `toml:"driver" json:"driver,omitempty" doc:"GPU driver. Only nvidia." example:"nvidia" enum:"nvidia"`
	Capabilities []string `toml:"capabilities" json:"capabilities,omitempty" doc:"Driver capabilities, for example compute and utility." example:"[\"compute\"]"`
}
