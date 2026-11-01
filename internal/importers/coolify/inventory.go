package coolify

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

// Inventory is everything the importer reads from one Coolify instance. It
// holds secrets (variable values, database passwords): it lives only for the
// duration of one plan or import call and is never logged or serialised.
type Inventory struct {
	Version      string
	Projects     []Project
	Applications []Application
	Databases    []Database
	Services     []OneClick
	Servers      []Server
	// Registries are the registry logins the Coolify servers report.
	Registries []RegistryLogin
	// Notes are problems met while reading (an endpoint the token could not
	// reach). They never contain response bodies.
	Notes []string
}

// Project is a Coolify project with its environments.
type Project struct {
	ID           int
	UUID         string
	Name         string
	Description  string
	Environments []Environment
}

// Environment is a Coolify environment.
type Environment struct {
	ID        int
	UUID      string
	Name      string
	ProjectID int
}

// Server is a Coolify server (a host).
type Server struct {
	UUID string
	Name string
	IP   string
}

// RegistryLogin is a registry a Coolify server is logged in to.
type RegistryLogin struct {
	Host     string
	Username string
	UsedBy   []string
}

// OneClick is a Coolify "service": a one-click Compose stack.
type OneClick struct {
	UUID          string
	Name          string
	Type          string
	EnvironmentID int
	// HasCompose is true when the token could read the Compose file.
	HasCompose bool
}

// Health is the Coolify health check of an application.
type Health struct {
	Enabled      bool
	Type         string // http | cmd
	Path         string
	Port         string
	Host         string
	Method       string
	Scheme       string
	ResponseText string
	Command      string
	ReturnCode   int
	Interval     int
	Timeout      int
	Retries      int
	StartPeriod  int
}

// Env is one environment variable of a Coolify resource.
type Env struct {
	Key       string
	Value     string
	RealValue string
	// HasValue is false when the API did not return the value (a token without
	// read:sensitive, or a locked variable).
	HasValue  bool
	Runtime   bool
	Buildtime bool
	Literal   bool
	Multiline bool
	Preview   bool
	Shared    bool
	ShownOnce bool
	Comment   string
}

// Volume is a persistent storage of a Coolify application.
type Volume struct {
	Name      string
	MountPath string
	HostPath  string
	ReadOnly  bool
}

// FileMount is a file storage of a Coolify application (its content is never kept).
type FileMount struct {
	MountPath   string
	IsDirectory bool
}

// Task is a Coolify scheduled task.
type Task struct {
	Name      string
	Frequency string
	Enabled   bool
}

// Application is a Coolify application.
type Application struct {
	ID            int
	UUID          string
	Name          string
	Description   string
	EnvironmentID int

	FQDN           string
	NoindexDomains []string
	Redirect       string
	BasicAuth      bool

	GitRepository string
	GitBranch     string
	GitCommitSHA  string
	GitFullURL    string
	PrivateKeyID  int
	SourceID      int

	RegistryImage string
	RegistryTag   string

	BuildPack          string
	StaticImage        string
	InstallCommand     string
	BuildCommand       string
	StartCommand       string
	PublishDirectory   string
	BaseDirectory      string
	DockerfileLocation string
	InlineDockerfile   bool
	DockerCompose      bool

	PortsExposes   string
	PortsMappings  string
	NetworkAliases []string

	Health                 Health
	CustomHealthcheckFound bool

	LimitsMemory      string
	LimitsMemorySwap  string
	LimitsReservation string
	LimitsCPUs        string
	LimitsCPUSet      string
	LimitsCPUShares   int

	CustomDockerRunOptions string
	PreDeployCommand       string
	PostDeployCommand      string
	CustomLabels           string

	// SensitiveVisible is true when the response carried the fields hidden from
	// tokens without read:sensitive.
	SensitiveVisible bool

	Envs    []Env
	Volumes []Volume
	Files   []FileMount
	Tasks   []Task
}

// Database is a Coolify standalone database.
type Database struct {
	UUID          string
	Name          string
	Type          string // standalone-postgresql, ...
	Image         string
	EnvironmentID int
	IsPublic      bool
	PublicPort    int

	// Credentials (read:sensitive).
	User         string
	Password     string
	RootPassword string
	DBName       string
	InternalURL  string

	CustomDockerRunOptions string
	CustomConfig           bool
	InitScripts            int
	EnableSSL              bool
	LimitsMemory           string
	LimitsCPUs             string
	SensitiveVisible       bool
	Volumes                []Volume
}

func parseProject(o obj) Project {
	p := Project{ID: o.int("id"), UUID: o.str("uuid"), Name: o.str("name"), Description: o.str("description")}
	for _, e := range o.list("environments") {
		p.Environments = append(p.Environments, parseEnvironment(e, p.ID))
	}
	return p
}

func parseEnvironment(o obj, projectID int) Environment {
	e := Environment{ID: o.int("id"), UUID: o.str("uuid"), Name: o.str("name"), ProjectID: o.int("project_id")}
	if e.ProjectID == 0 {
		e.ProjectID = projectID
	}
	return e
}

func parseEnv(o obj) Env {
	e := Env{
		Key: o.str("key"), Value: o.str("value"), RealValue: o.str("real_value"), HasValue: o.has("value") || o.has("real_value"),
		Runtime: o.bool("is_runtime"), Buildtime: o.bool("is_buildtime"), Literal: o.bool("is_literal"), Multiline: o.bool("is_multiline"),
		Preview: o.bool("is_preview"), Shared: o.bool("is_shared"), ShownOnce: o.bool("is_shown_once"), Comment: o.str("comment"),
	}
	if !o.has("is_runtime") && !o.has("is_buildtime") {
		e.Runtime = true
	}
	return e
}

func parseApplication(o obj) Application {
	a := Application{
		ID: o.int("id"), UUID: o.str("uuid"), Name: o.str("name"), Description: o.str("description"), EnvironmentID: o.int("environment_id"),
		FQDN: o.str("fqdn"), NoindexDomains: o.strList("noindex_domains"), Redirect: o.str("redirect"), BasicAuth: o.bool("is_http_basic_auth_enabled"),
		GitRepository: o.str("git_repository"), GitBranch: o.str("git_branch"), GitCommitSHA: o.str("git_commit_sha"), GitFullURL: o.str("git_full_url"),
		PrivateKeyID: o.int("private_key_id"), SourceID: o.int("source_id"),
		RegistryImage: o.str("docker_registry_image_name"), RegistryTag: o.str("docker_registry_image_tag"),
		BuildPack: strings.ToLower(o.str("build_pack")), StaticImage: o.str("static_image"),
		InstallCommand: o.str("install_command"), BuildCommand: o.str("build_command"), StartCommand: o.str("start_command"),
		PublishDirectory: o.str("publish_directory"), BaseDirectory: o.str("base_directory"), DockerfileLocation: o.str("dockerfile_location"),
		InlineDockerfile: strings.TrimSpace(o.str("dockerfile")) != "",
		DockerCompose:    strings.TrimSpace(o.str("docker_compose_raw")) != "" || strings.TrimSpace(o.str("docker_compose")) != "",
		PortsExposes:     o.str("ports_exposes"), PortsMappings: o.str("ports_mappings"), NetworkAliases: o.strList("custom_network_aliases"),
		Health: Health{
			Enabled: o.bool("health_check_enabled"), Type: strings.ToLower(o.str("health_check_type")), Path: o.str("health_check_path"),
			Port: o.str("health_check_port"), Host: o.str("health_check_host"), Method: o.str("health_check_method"), Scheme: o.str("health_check_scheme"),
			ResponseText: o.str("health_check_response_text"), Command: o.str("health_check_command"), ReturnCode: o.int("health_check_return_code"),
			Interval: o.int("health_check_interval"), Timeout: o.int("health_check_timeout"), Retries: o.int("health_check_retries"),
			StartPeriod: o.int("health_check_start_period"),
		},
		CustomHealthcheckFound: o.bool("custom_healthcheck_found"),
		LimitsMemory:           o.str("limits_memory"), LimitsMemorySwap: o.str("limits_memory_swap"), LimitsReservation: o.str("limits_memory_reservation"),
		LimitsCPUs: o.str("limits_cpus"), LimitsCPUSet: o.str("limits_cpuset"), LimitsCPUShares: o.int("limits_cpu_shares"),
		CustomDockerRunOptions: o.str("custom_docker_run_options"), PreDeployCommand: o.str("pre_deployment_command"),
		PostDeployCommand: o.str("post_deployment_command"), CustomLabels: decodeLabels(o.str("custom_labels")),
		SensitiveVisible: o.has("custom_docker_run_options"),
	}
	return a
}

func parseDatabase(o obj) Database {
	d := Database{
		UUID: o.str("uuid"), Name: o.str("name"), Type: strings.ToLower(o.str("database_type")), Image: strings.TrimSpace(o.str("image")),
		EnvironmentID: o.int("environment_id"), IsPublic: o.bool("is_public"), PublicPort: o.int("public_port"),
		InternalURL: o.str("internal_db_url"), CustomDockerRunOptions: o.str("custom_docker_run_options"),
		EnableSSL: o.bool("enable_ssl"), LimitsMemory: o.str("limits_memory"), LimitsCPUs: o.str("limits_cpus"),
		SensitiveVisible: o.has("internal_db_url") || o.has("postgres_password") || o.has("redis_password") || o.has("mysql_password") ||
			o.has("mariadb_password") || o.has("mongo_initdb_root_password") || o.has("clickhouse_admin_password"),
	}
	if scripts, ok := o["init_scripts"].([]any); ok {
		d.InitScripts = len(scripts)
	}
	switch {
	case strings.HasSuffix(d.Type, "postgresql"):
		d.User, d.Password, d.DBName = o.str("postgres_user"), o.str("postgres_password"), o.str("postgres_db")
		d.CustomConfig = strings.TrimSpace(o.str("postgres_conf")) != ""
	case strings.HasSuffix(d.Type, "mysql"):
		d.User, d.Password, d.RootPassword, d.DBName = o.str("mysql_user"), o.str("mysql_password"), o.str("mysql_root_password"), o.str("mysql_database")
		d.CustomConfig = strings.TrimSpace(o.str("mysql_conf")) != ""
	case strings.HasSuffix(d.Type, "mariadb"):
		d.User, d.Password, d.RootPassword, d.DBName = o.str("mariadb_user"), o.str("mariadb_password"), o.str("mariadb_root_password"), o.str("mariadb_database")
		d.CustomConfig = strings.TrimSpace(o.str("mariadb_conf")) != ""
	case strings.HasSuffix(d.Type, "mongodb"):
		d.User, d.Password, d.DBName = o.str("mongo_initdb_root_username"), o.str("mongo_initdb_root_password"), o.str("mongo_initdb_database")
		d.CustomConfig = strings.TrimSpace(o.str("mongo_conf")) != ""
	case strings.HasSuffix(d.Type, "redis"):
		d.Password = o.str("redis_password")
		d.CustomConfig = strings.TrimSpace(o.str("redis_conf")) != ""
	case strings.HasSuffix(d.Type, "keydb"):
		d.Password = o.str("keydb_password")
		d.CustomConfig = strings.TrimSpace(o.str("keydb_conf")) != ""
	case strings.HasSuffix(d.Type, "dragonfly"):
		d.Password = o.str("dragonfly_password")
	case strings.HasSuffix(d.Type, "clickhouse"):
		d.User, d.Password, d.DBName = o.str("clickhouse_admin_user"), o.str("clickhouse_admin_password"), o.str("clickhouse_db")
	}
	return d
}

func parseVolume(o obj) Volume {
	return Volume{Name: o.str("name"), MountPath: o.str("mount_path"), HostPath: o.str("host_path"), ReadOnly: o.bool("is_readonly")}
}

// decodeLabels turns Coolify's custom_labels (stored base64) into text; a value
// that is not base64 is returned as it is.
func decodeLabels(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil && utf8.Valid(raw) && strings.Contains(string(raw), "=") {
		return string(raw)
	}
	return s
}
