// Package templates implements the one-click template catalog: the YAML
// template format, static validation, the embedded catalog, and installing a
// template into an environment as staged services, volumes, files, domains,
// ports, links and generated variables.
//
// A template never becomes a Compose project: every template service turns
// into an ordinary NebulaCtrl service on the same server and environment, so
// everything after the install (deploy, rollback, variables, domains, logs) is
// the normal service path.
package templates

// SchemaVersion is the only template schema this build understands.
const SchemaVersion = 1

// Categories of the gallery. IDs are stable; labels are UI (categoryLabels).
const (
	CategoryHomelab      = "homelab"      // homelab, dashboards and networking
	CategoryMedia        = "media"        // media servers, music, photos, books
	CategoryStorage      = "storage"      // files, sync, backups, archives
	CategoryGames        = "games"        // game servers
	CategoryDevtools     = "devtools"     // git, CI, registries, developer utilities
	CategoryAI           = "ai"           // LLM front ends, vector stores, ML tools
	CategoryData         = "data"         // databases, search engines, brokers, backend as a service
	CategoryWeb          = "web"          // CMS, blogs, wikis, shops, forms
	CategoryAnalytics    = "analytics"    // web analytics, uptime and observability
	CategoryAutomation   = "automation"   // workflow automation and integration
	CategoryProductivity = "productivity" // notes, tasks, documents, chat, mail, ERP
	CategorySecurity     = "security"     // identity, SSO, password managers, secrets
)

// Categories lists every valid category id in display order.
var Categories = []string{
	CategoryHomelab, CategoryMedia, CategoryStorage, CategoryGames, CategoryDevtools, CategoryAI,
	CategoryData, CategoryWeb, CategoryAnalytics, CategoryAutomation, CategoryProductivity, CategorySecurity,
}

// Licence classes of Upstream.Class.
const (
	LicensePermissive      = "permissive"
	LicenseCopyleft        = "copyleft"
	LicenseSourceAvailable = "source-available"
	LicenseProprietary     = "proprietary" // closed source, free to run (Plex)
)

// Origin types.
const (
	OriginOriginal = "original" // hand written for NebulaCtrl
	OriginCoolify  = "coolify"  // translated from a Coolify template (Apache-2.0)
	OriginDokploy  = "dokploy"  // translated from a Dokploy blueprint (MIT)
)

// CatalogTemplate is one catalog entry (templates/catalog/<id>.yaml).
type CatalogTemplate struct {
	Schema      int      `yaml:"schema" json:"schema"`
	ID          string   `yaml:"id" json:"id"`
	Revision    int      `yaml:"revision" json:"revision"`
	Name        string   `yaml:"name" json:"name"`
	Summary     string   `yaml:"summary" json:"summary"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Category    string   `yaml:"category" json:"category"`
	Tags        []string `yaml:"tags,omitempty" json:"tags"`
	// Icon is a simple-icons slug; the web falls back to a neutral glyph.
	Icon     string           `yaml:"icon,omitempty" json:"icon,omitempty"`
	Links    TemplateLinks    `yaml:"links" json:"links"`
	Upstream TemplateUpstream `yaml:"upstream" json:"upstream"`
	Origin   *TemplateOrigin  `yaml:"origin,omitempty" json:"origin,omitempty"`
	// Heavy marks a template whose install downloads several GB (large images,
	// models, game files), needs a lot of RAM or takes many minutes to start.
	// The plan warns about it, and the integration sample only deploys it when
	// it is named explicitly: it needs a manual check.
	Heavy bool `yaml:"heavy,omitempty" json:"heavy,omitempty"`
	// RequiresUnrestricted declares that a service of the template uses a
	// Docker option only an unrestricted server allows (devices, GPUs, a runtime,
	// privileged mode, advanced capabilities…). The install is refused on any
	// other server. Validate checks the flag is set exactly when one is used.
	RequiresUnrestricted bool              `yaml:"requiresUnrestricted,omitempty" json:"requiresUnrestricted,omitempty"`
	Requires             TemplateRequires  `yaml:"requires" json:"requires"`
	Inputs               []TemplateInput   `yaml:"inputs,omitempty" json:"inputs"`
	Services             []TemplateService `yaml:"services" json:"services"`
	// Group is the canvas group title (default: the template name).
	Group string `yaml:"group,omitempty" json:"group,omitempty"`
	// Notes are shown after the install (default logins, first steps).
	Notes []string `yaml:"notes,omitempty" json:"notes"`
}

// TemplateLinks are the upstream project's pages.
type TemplateLinks struct {
	Website string `yaml:"website,omitempty" json:"website,omitempty"`
	Docs    string `yaml:"docs,omitempty" json:"docs,omitempty"`
	Source  string `yaml:"source,omitempty" json:"source,omitempty"`
}

// TemplateUpstream is the upstream application's licence.
type TemplateUpstream struct {
	// License is the SPDX id or name, e.g. "AGPL-3.0" or "Sustainable Use License".
	License    string `yaml:"license" json:"license"`
	LicenseURL string `yaml:"licenseUrl,omitempty" json:"licenseUrl,omitempty"`
	// Class is permissive | copyleft | source-available | proprietary.
	Class string `yaml:"class" json:"class"`
}

// TemplateOrigin attributes a translated template to its upstream catalog.
type TemplateOrigin struct {
	Type    string `yaml:"type" json:"type"`
	Path    string `yaml:"path,omitempty" json:"path,omitempty"`
	Commit  string `yaml:"commit,omitempty" json:"commit,omitempty"`
	License string `yaml:"license,omitempty" json:"license,omitempty"`
}

// TemplateRequires are preflight minimums checked against the chosen server.
type TemplateRequires struct {
	MemoryMB int      `yaml:"memoryMb,omitempty" json:"memoryMb"`
	DiskGB   int      `yaml:"diskGb,omitempty" json:"diskGb"`
	Arch     []string `yaml:"arch,omitempty" json:"arch"` // default [amd64, arm64]
}

// TemplateInput is a value the installing user may set; used as ${input:KEY}.
type TemplateInput struct {
	Key         string `yaml:"key" json:"key"`
	Label       string `yaml:"label" json:"label"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Default may contain a generator such as ${{secret(24)}}, which is
	// evaluated once per install when the user leaves the field empty.
	Default  string `yaml:"default,omitempty" json:"default"`
	Required bool   `yaml:"required,omitempty" json:"required"`
	// Secret inputs are rendered as password fields; every variable that uses
	// them is stored as a secret.
	Secret bool `yaml:"secret,omitempty" json:"secret"`
	// Options turns the input into a select.
	Options []string `yaml:"options,omitempty" json:"options,omitempty"`
	// Format is text (default) | email | url | hostname | number. The form
	// renders the matching field and the install rejects values that do not fit.
	Format string `yaml:"format,omitempty" json:"format,omitempty" enum:"text,email,url,hostname,number"`
}

// TemplateService is one service of a template.
type TemplateService struct {
	// Key identifies the service inside the template (lowercase, [a-z0-9-]).
	// References `${{key.VAR}}` are rewritten to the real slug at install time.
	Key  string `yaml:"key" json:"key"`
	Name string `yaml:"name" json:"name"`
	// Kind is app (default) | worker | database.
	Kind string `yaml:"kind,omitempty" json:"kind"`
	// Image is repo:tag; Digest is the pinned multi-arch index digest
	// (sha256:…). The installed service uses repo:tag@digest.
	Image  string `yaml:"image,omitempty" json:"image,omitempty"`
	Digest string `yaml:"digest,omitempty" json:"digest,omitempty"`
	// Database creates a managed database service (engine catalog of the
	// databases package) instead of an image service.
	Database    *TemplateDatabase `yaml:"database,omitempty" json:"database,omitempty"`
	Port        int               `yaml:"port,omitempty" json:"port,omitempty"`
	Command     []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Healthcheck *TemplateHealth   `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	// Env values may use ${input:KEY}, ${{key.VAR}}, ${{VAR}} and generators
	// such as ${{secret(32)}}.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Secrets lists env keys whose values are secret even without a generator.
	Secrets []string         `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Volumes []TemplateVolume `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Files   []TemplateFile   `yaml:"files,omitempty" json:"files,omitempty"`
	// Ports are non-HTTP ports (game servers). Published ports create TCP/UDP
	// routes on the server's proxy.
	Ports   []TemplatePort   `yaml:"ports,omitempty" json:"ports,omitempty"`
	Domains []TemplateDomain `yaml:"domains,omitempty" json:"domains,omitempty"`
	// DependsOn lists service keys that must be deployed (and, for one-shot
	// services, have finished) first.
	DependsOn []string `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	OneShot   bool     `yaml:"oneshot,omitempty" json:"oneshot,omitempty"`
	// Docker holds container options with the same JSON names and validator as
	// the service's Docker options: entrypoint, shm size, ulimits, capabilities,
	// sysctls and so on. Text values may use ${input:KEY}.
	Docker           *DockerBlock    `yaml:"docker,omitempty" json:"docker,omitempty"`
	Resources        *TemplateLimits `yaml:"resources,omitempty" json:"resources,omitempty"`
	StopGraceSeconds int             `yaml:"stopGraceSeconds,omitempty" json:"stopGraceSeconds,omitempty"`
}

// TemplateDatabase is a managed database companion.
type TemplateDatabase struct {
	Engine       string `yaml:"engine" json:"engine"`
	Version      string `yaml:"version" json:"version"`
	DatabaseName string `yaml:"databaseName,omitempty" json:"databaseName,omitempty"`
}

// TemplateHealth is the template spelling of services.Healthcheck.
type TemplateHealth struct {
	Type               string   `yaml:"type" json:"type"` // http | tcp | exec | docker | none
	Path               string   `yaml:"path,omitempty" json:"path,omitempty"`
	Port               int      `yaml:"port,omitempty" json:"port,omitempty"`
	Command            []string `yaml:"command,omitempty" json:"command,omitempty"` // exec: argv run inside the container
	IntervalSeconds    int      `yaml:"intervalSeconds,omitempty" json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int      `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
	StartPeriodSeconds int      `yaml:"startPeriodSeconds,omitempty" json:"startPeriodSeconds,omitempty"`
	DeadlineSeconds    int      `yaml:"deadlineSeconds,omitempty" json:"deadlineSeconds,omitempty"`
	SuccessThreshold   int      `yaml:"successThreshold,omitempty" json:"successThreshold,omitempty"`
}

// TemplateVolume is a named volume.
type TemplateVolume struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

// TemplateFile is a file written into the container before it starts. Contents are
// stored in plain text in the service configuration: never put secrets in them.
type TemplateFile struct {
	Path    string `yaml:"path" json:"path"`
	Mode    string `yaml:"mode,omitempty" json:"mode,omitempty"` // octal, default 0644
	Content string `yaml:"content" json:"content"`
}

// TemplatePort is a non-HTTP container port.
type TemplatePort struct {
	Name     string `yaml:"name" json:"name"`
	Port     int    `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol" json:"protocol"` // tcp | udp
	// Publish exposes the port on the server's public interface through a
	// TCP/UDP proxy route on the same port number.
	Publish bool `yaml:"publish,omitempty" json:"publish"`
}

// TemplateDomain asks for a hostname. Generate uses the server's generated-domain
// scheme; Host is an explicit hostname (may use ${input:KEY}).
type TemplateDomain struct {
	Generate bool   `yaml:"generate,omitempty" json:"generate,omitempty"`
	Host     string `yaml:"host,omitempty" json:"host,omitempty"`
}

// TemplateLimits are resource caps.
type TemplateLimits struct {
	MemoryMB int     `yaml:"memoryMb,omitempty" json:"memoryMb,omitempty"`
	CPUs     float64 `yaml:"cpus,omitempty" json:"cpus,omitempty"`
}
