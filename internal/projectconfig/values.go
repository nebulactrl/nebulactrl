package projectconfig

import (
	"slices"
	"sort"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Applies says when a field takes effect.
const (
	AppliesStaged    = "staged"
	AppliesImmediate = "immediate"
)

// Service kinds inferred from the source key.
const (
	KindImage    = "image"
	KindGit      = "git"
	KindDatabase = "database"
	KindRoute    = "route"
	KindStack    = "stack"
)

// Resolved is the file resolved for one environment: only_in applied, override
// blocks merged, unset applied, server precedence settled, attachments inlined.
type Resolved struct {
	// Environment is the environment this view was resolved for ("" = base only).
	Environment string
	// Project is the root `project` key ("" when absent).
	Project string
	// Server is the default server name after environment precedence
	// (environments.<e>.server over root server); "" when none.
	Server string
	// Shared is the project-layer shared variables (root [variables]);
	// EnvShared the environment layer ([environments.<e>.variables]).
	Shared, EnvShared map[string]Variable
	// SharedSecrets and EnvSharedSecrets are the declared secret names of the layers.
	SharedSecrets, EnvSharedSecrets []string
	// SharedSecretScope and EnvSharedSecretScope are the scopes of those secrets
	// ("runtime" when not written).
	SharedSecretScope, EnvSharedSecretScope string
	// Services maps slug -> service, only services that exist in this environment.
	Services map[string]*ResolvedService

	index *Index
}

// SortedSlugs lists the service slugs in ascending order.
func (r *Resolved) SortedSlugs() []string {
	out := make([]string, 0, len(r.Services))
	for s := range r.Services {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Pos returns the position of a file key path (the line that set the winning
// value), or the zero Pos when unknown.
func (r *Resolved) Pos(path string) Pos {
	if r == nil || r.index == nil {
		return Pos{}
	}
	return r.index.Pos(path)
}

// ResolvedService is one service of one environment.
type ResolvedService struct {
	Slug string
	// Kind is the inferred kind: git, image, database, route or stack.
	Kind string
	// ServerName is the effective server name (service > environment > root); "" when none.
	ServerName string
	// Def is the merged definition, typed. Presence pointers mean "key written":
	// a non-nil pointer or non-nil slice/map is owned by the file. Attachments are
	// inlined (Def.Stack.Compose and Def.Files[*].Content are set, ComposeFile and
	// Source are nil), and Def.Unset is nil (applied).
	Def *Service
	// Fields maps field id -> value of every ownable atom present in Def.
	Fields map[string]FieldValue
	// Path is the file key path the service is rooted at ("services.web").
	Path string
}

// FieldIDs lists the ids of Fields in ascending order.
func (s *ResolvedService) FieldIDs() []string {
	out := make([]string, 0, len(s.Fields))
	for id := range s.Fields {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// FieldValue is one owned atom of a service.
type FieldValue struct {
	// ID is the field id: config.port, config.healthcheck.path, name, server,
	// links.<slug>, networks.<name>, variables.<KEY>, variables.<KEY>#secret,
	// domains.<host><path>, domains.<host><path>.cloudflare, volumes.<name>,
	// config.files, scale.replicas, scale.autoscaling.<k>, git.<k>, uptime.<k>,
	// tasks.<name>, backups.<key>, config.docker.<jsonName>, config.route.<k>,
	// config.stack.<k>, config.database.<k>, config.processName, config.singleton,
	// config.processes.<name>, processes.<name>.scale.<k>, config.tailnet, generate_domain.
	ID string
	// Path is the file key path ("services.web.health_check.path").
	Path string
	// Value is the normalised typed value; see the Catalog for the Go type of
	// every field. Value types are plain (no unexported fields): int, float64,
	// bool, string, []string and the structs of this file.
	Value any
	// Pos is the position of the line that set the winning value.
	Pos Pos
	// Applies is "staged" or "immediate".
	Applies string
}

// Value types of FieldValue.Value. They carry no unexported fields so that
// Fingerprint gives equal results for equal values.

// Volume is the value of volumes.<name>.
type Volume struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	// Owner is "" (none), "uid" or "uid:gid".
	Owner string `json:"owner,omitempty"`
}

// FileMount is one entry of config.files (the whole set is one field).
type FileMount struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"` // octal, default 0644
	Content string `json:"content"`
}

// GitLink is the value of git.link: the connection and repository of the
// service (connection by name).
type GitLink struct {
	ConnectionName string `json:"connection"`
	Repo           string `json:"repo"`
}

// UpstreamValue is one route upstream: Address, or Service (slug) + Port.
type UpstreamValue struct {
	Address string `json:"address,omitempty"`
	Service string `json:"service,omitempty"`
	Port    int    `json:"port,omitempty"`
}

// DomainRule is the value of domains.<host><path>: the declared rule fields
// only. A nil pointer means "not declared" and keeps the live value.
type DomainRule struct {
	Host         string  `json:"host"`
	PathPrefix   string  `json:"pathPrefix,omitempty"` // "" or "/api" (no trailing slash)
	TargetPort   *int    `json:"targetPort,omitempty"`
	Component    *string `json:"component,omitempty"`
	Process      *string `json:"process,omitempty"`
	StripPrefix  *bool   `json:"stripPrefix,omitempty"`
	AddPrefix    *string `json:"addPrefix,omitempty"`
	Kind         *string `json:"kind,omitempty"`
	RedirectTo   *string `json:"redirectTo,omitempty"`
	RedirectCode *int    `json:"redirectCode,omitempty"`
	PreservePath *bool   `json:"preservePath,omitempty"`
	TLS          *string `json:"tls,omitempty"`
	Certificate  *string `json:"certificate,omitempty"` // uploaded certificate NAME
	DNSResolver  *string `json:"dnsResolver,omitempty"`
	WWWRedirect  *bool   `json:"wwwRedirect,omitempty"`
	// Options is the whole option set when the options table is present
	// (normalised: headers sorted by name). Nil = not declared.
	Options *agentprotocol.RouteOptions `json:"options,omitempty"`
}

// CloudflareRule is the value of domains.<host><path>.cloudflare. A nil field is not declared:
// a nil Proxied leaves an existing name as it is, and the server applies its default (proxied, with
// the documented exceptions) to a name it starts managing.
type CloudflareRule struct {
	Proxied    *bool `json:"proxied,omitempty"`
	OriginLock *bool `json:"originLock,omitempty"`
}

// TaskValue is the value of tasks.<name>, defaults applied.
type TaskValue struct {
	Name           string   `json:"name"`
	Action         string   `json:"action"` // command | restart; default command
	Schedule       string   `json:"schedule"`
	Timezone       string   `json:"timezone"` // default UTC
	Command        []string `json:"command"`
	Enabled        bool     `json:"enabled"`        // default true
	TimeoutSeconds int      `json:"timeoutSeconds"` // default 1800
	Concurrency    string   `json:"concurrency"`    // forbid | allow
}

// ProcessValue is the value of config.processes.<name>: the whole process is
// one unit. A zero field is "not declared": StopGraceSeconds, MemoryMB and CPUs
// then inherit the service's, and HealthCheck takes the platform defaults.
type ProcessValue struct {
	Name             string        `json:"name"`
	Command          []string      `json:"command"`
	Port             int           `json:"port"`
	StopGraceSeconds int           `json:"stopGraceSeconds"`
	Singleton        bool          `json:"singleton"`
	MemoryMB         int           `json:"memoryMb"`
	CPUs             float64       `json:"cpus"`
	HealthCheck      ProcessHealth `json:"healthCheck"`
}

// ProcessHealth is the health check of a process, expressed like the
// config.healthcheck.* values; a zero field takes the platform default.
type ProcessHealth struct {
	Type               string   `json:"type"`
	Path               string   `json:"path"`
	Port               int      `json:"port"`
	IntervalSeconds    int      `json:"intervalSeconds"`
	TimeoutSeconds     int      `json:"timeoutSeconds"`
	StartPeriodSeconds int      `json:"startPeriodSeconds"`
	DeadlineSeconds    int      `json:"deadlineSeconds"`
	SuccessThreshold   int      `json:"successThreshold"`
	Command            []string `json:"command,omitempty"`
}

// processValue is the value of one [processes.<name>] table.
func processValue(name string, p *Process) ProcessValue {
	pv := ProcessValue{
		Name: name, Command: append([]string{}, p.Command...), Port: derefInt(p.Port), StopGraceSeconds: derefInt(p.StopGraceSeconds),
		Singleton: derefBool(p.Singleton), MemoryMB: derefInt(p.MemoryMB), CPUs: derefFloat(p.CPUs),
	}
	if h := p.HealthCheck; h != nil {
		pv.HealthCheck = ProcessHealth{
			Type: derefStr(h.Type), Path: derefStr(h.Path), Port: derefInt(h.Port), IntervalSeconds: derefInt(h.IntervalSeconds),
			TimeoutSeconds: derefInt(h.TimeoutSeconds), StartPeriodSeconds: derefInt(h.StartPeriodSeconds),
			DeadlineSeconds: derefInt(h.DeadlineSeconds), SuccessThreshold: derefInt(h.SuccessThreshold),
			Command: append([]string(nil), h.Command...),
		}
	}
	return pv
}

// Config converts the value to the platform's process, not yet defaulted: the
// service's Config.WithDefaults fills the health check from the process' port.
func (v ProcessValue) Config() services.ProcessConfig {
	h := v.HealthCheck
	return services.ProcessConfig{
		Name: v.Name, StartCommand: v.Command, Port: v.Port, StopGraceSeconds: v.StopGraceSeconds, Singleton: v.Singleton,
		MemoryMB: v.MemoryMB, CPUs: v.CPUs,
		Healthcheck: services.Healthcheck{
			Type: h.Type, Path: h.Path, Port: h.Port, IntervalSeconds: h.IntervalSeconds, TimeoutSeconds: h.TimeoutSeconds,
			StartPeriodSeconds: h.StartPeriodSeconds, DeadlineSeconds: h.DeadlineSeconds, SuccessThreshold: h.SuccessThreshold,
			Command: h.Command,
		},
	}
}

// TailnetValue is the value of config.tailnet: the whole table is one unit, in the
// canonical form of tailnetcfg.Normalize (trimmed, sorted tags, ports sorted by listen
// with the target filled in). A disabled node is the zero value with Enabled false.
// Funnel is not part of it: a file never sets or compares it.
type TailnetValue struct {
	Enabled    bool               `json:"enabled"`
	Hostname   string             `json:"hostname"`
	ExtraTags  []string           `json:"extraTags"`
	Web        bool               `json:"web"`
	TCP        []TailnetPortValue `json:"tcp"`
	HidePublic bool               `json:"hidePublic"`
}

// TailnetPortValue is one forwarded TCP port of a TailnetValue.
type TailnetPortValue struct {
	Listen int  `json:"listen"`
	Target int  `json:"target"`
	TLS    bool `json:"tls"`
}

// tailnetConfigOf is the platform's desired tailnet of a [tailnet] table, not yet normalised.
func tailnetConfigOf(t *Tailnet) tailnetcfg.TailnetConfig {
	cfg := tailnetcfg.TailnetConfig{
		Enabled: derefBool(t.Enabled), Hostname: derefStr(t.Hostname), ExtraTags: t.ExtraTags,
		Web: derefBool(t.Web), HidePublic: derefBool(t.HidePublic),
	}
	for _, p := range t.TCP {
		cfg.TCP = append(cfg.TCP, tailnetcfg.TCPForward{Listen: derefInt(p.Listen), Target: derefInt(p.Target), TLS: derefBool(p.TLS)})
	}
	return cfg
}

// tailnetValue is the value of a [tailnet] table.
func tailnetValue(t *Tailnet) TailnetValue {
	cfg := tailnetConfigOf(t)
	return TailnetValueOf(tailnetcfg.Normalize(&cfg))
}

// TailnetValueOf is the comparable form of a platform tailnet (nil: not on a tailnet).
// Funnel is left out.
func TailnetValueOf(c *tailnetcfg.TailnetConfig) TailnetValue {
	n := tailnetcfg.Normalize(c)
	v := TailnetValue{ExtraTags: []string{}, TCP: []TailnetPortValue{}}
	if n == nil {
		return v
	}
	v.Enabled, v.Hostname, v.Web, v.HidePublic = true, n.Hostname, n.Web, n.HidePublic
	v.ExtraTags = append(v.ExtraTags, n.ExtraTags...)
	for _, f := range n.TCP {
		v.TCP = append(v.TCP, TailnetPortValue{Listen: f.Listen, Target: f.Target, TLS: f.TLS})
	}
	return v
}

// Config converts the value to the platform's desired tailnet; nil when the node is off.
// A file never sets Funnel, so the caller passes the live value to keep.
func (v TailnetValue) Config(funnel bool) *tailnetcfg.TailnetConfig {
	if !v.Enabled {
		return nil
	}
	cfg := &tailnetcfg.TailnetConfig{
		Enabled: true, Hostname: v.Hostname, ExtraTags: slices.Clone(v.ExtraTags), Web: v.Web, Funnel: funnel, HidePublic: v.HidePublic,
	}
	for _, p := range v.TCP {
		cfg.TCP = append(cfg.TCP, tailnetcfg.TCPForward{Listen: p.Listen, Target: p.Target, TLS: p.TLS})
	}
	return tailnetcfg.Normalize(cfg)
}

// BackupValue is the value of backups.<key>, defaults applied.
type BackupValue struct {
	Key             string `json:"key"`
	DestinationName string `json:"destination"`
	Cron            string `json:"cron"`
	Timezone        string `json:"timezone"` // default UTC
	Enabled         bool   `json:"enabled"`  // default true
	RetentionCount  int    `json:"retentionCount"`
	RetentionDays   int    `json:"retentionDays"`
}

// SecretDecl is the value of variables.<KEY>#secret: a declared secret name.
type SecretDecl struct {
	Name  string `json:"name"`
	Scope string `json:"scope"` // runtime | build | both
}

// StackResourceValue is the value of config.stack.resources.<component>.
type StackResourceValue struct {
	Component string  `json:"component"`
	MemoryMB  int     `json:"memoryMb"`
	CPUs      float64 `json:"cpus"`
}

// DatabaseSpec is the value group of config.database.*.
type DatabaseSpec struct {
	Engine, Version, Flavor, DatabaseName, Username string
	AcceptLicense                                   bool
}
