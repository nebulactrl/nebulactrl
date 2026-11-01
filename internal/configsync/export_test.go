package configsync

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/internal/variables"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ---------------------------------------------------------------------------
// In-memory building blocks (no database).

func exID(n byte) uuid.UUID {
	var u uuid.UUID
	u[15] = n
	return u
}

func exTestRefs() *refs {
	return &refs{
		Servers:      []db.Server{{ID: exID(1), Name: "fra-1"}, {ID: exID(2), Name: "builder"}},
		Connections:  []db.GitConnection{{ID: exID(3), Name: "github-acme"}},
		Registries:   []db.Registry{{ID: exID(4), Name: "ghcr"}},
		Destinations: []db.BackupDestination{{ID: exID(5), Name: "offsite"}},
		Networks:     []db.SharedNetwork{{ID: exID(6), Name: "backplane", ServerID: exID(1)}},
		Certs:        []db.Certificate{{ID: exID(7), Name: "wildcard", ServerID: exID(1)}},
		Cloudflare:   true,
	}
}

func exSvc(slug, kind, source string, cfg services.Config) *liveService {
	return &liveService{
		Row: db.Service{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("export/"+slug)), Name: slug, Slug: slug, Kind: kind, SourceType: source, Replicas: 1},
		Cfg: cfg.WithDefaults(), Vars: map[string]liveVar{}, Tasks: map[string]db.ServiceTask{},
		Scale: services.DefaultAutoscaling(), Owned: map[string]string{}, Pending: map[string]json.RawMessage{},
	}
}

func exImageSvc(slug string, cfg services.Config) *liveService {
	return exSvc(slug, services.KindApp, services.SourceImage, cfg)
}

func exLiveEnv(r *refs, svcs ...*liveService) *liveEnv {
	le := &liveEnv{
		Env:      services.EnvScope{Env: db.Environment{Slug: "production"}},
		Project:  db.Project{Slug: "shop"},
		Services: map[string]*liveService{}, Refs: r,
		SharedProject: map[string]liveVar{}, SharedEnv: map[string]liveVar{}, OwnedShared: map[string]string{},
	}
	for _, s := range svcs {
		le.Services[s.Row.Slug] = s
		le.Order = append(le.Order, s.Row.Slug)
	}
	return le
}

func exExport(t *testing.T, live *liveEnv, defaults bool) (string, *exportView) {
	t.Helper()
	v, err := newExporter(live, defaults).resolve()
	require.NoError(t, err)
	text := projectconfig.Render(v.Res, projectconfig.RenderOptions{Origin: "https://nebula.test", Comments: v.Comments, NotRepresentable: v.NotRepresentable})
	return string(text), v
}

// exStructural fails on diagnostics that mean the export wrote a key or a type the format does not know.
func exStructural(t *testing.T, text string, diags projectconfig.Diagnostics) {
	t.Helper()
	for _, d := range diags {
		switch d.Code {
		case projectconfig.CodeTOMLSyntax, projectconfig.CodeUnknownKey, projectconfig.CodeWrongType, projectconfig.CodeLimitExceeded,
			projectconfig.CodeInvalidEncoding, projectconfig.CodeFileTooLarge:
			t.Fatalf("the export is not valid nebula.toml: %s\n%s", d.Render(""), text)
		}
	}
}

func exParseResolve(t *testing.T, text, env string) (*projectconfig.File, *projectconfig.Resolved) {
	t.Helper()
	f, idx, diags := projectconfig.Parse([]byte(text), nil)
	require.NotNil(t, f, diags.RenderText(""))
	exStructural(t, text, diags)
	res, _ := f.Resolve(env, idx)
	require.NotNil(t, res)
	return f, res
}

// ---- every Config field ----

// exampleValue is a non-default value of a catalog field and the config it needs around it.
type exampleValue struct {
	value any
	prep  func(c *services.Config)
}

func exWithCommand(c *services.Config) {
	c.PreDeployCommand = []string{"echo", "pre"}
	c.PostDeployCommand = []string{"echo", "post"}
}

var exExamples = map[string]exampleValue{
	"config.image":            {value: "nginx:2"},
	"config.port":             {value: 8080},
	"config.startCommand":     {value: []string{"bun", "run", "worker"}},
	"config.oneshot":          {value: true},
	"config.stopGraceSeconds": {value: 45},
	"config.memoryMb":         {value: 512},
	"config.cpus":             {value: 1.5},
	"config.imageRetention":   {value: 9},
	"config.blockEgress":      {value: true},

	"config.processName": {value: "app"},
	"config.singleton":   {value: true},
	// The process id is expanded with the sample name "web": the default process is renamed so the names differ.
	"config.processes.web": {
		value: projectconfig.ProcessValue{
			Name: "web", Command: []string{"bin/queue", "--verbose"}, Port: 9100, StopGraceSeconds: 90, Singleton: true, MemoryMB: 256, CPUs: 0.5,
			HealthCheck: projectconfig.ProcessHealth{Type: "tcp", IntervalSeconds: 9},
		},
		prep: func(c *services.Config) { c.ProcessName = "app" },
	},

	"config.healthcheck.type":               {value: "tcp"},
	"config.healthcheck.path":               {value: "/ready"},
	"config.healthcheck.port":               {value: 9090},
	"config.healthcheck.intervalSeconds":    {value: 9},
	"config.healthcheck.timeoutSeconds":     {value: 7},
	"config.healthcheck.startPeriodSeconds": {value: 11},
	"config.healthcheck.deadlineSeconds":    {value: 200},
	"config.healthcheck.successThreshold":   {value: 4},
	"config.healthcheck.command":            {value: []string{"CMD", "true"}},

	"config.preDeployCommand":         {value: []string{"bun", "run", "db:migrate"}},
	"config.preDeployTimeoutSeconds":  {value: 300, prep: exWithCommand},
	"config.postDeployCommand":        {value: []string{"bun", "run", "warm"}},
	"config.postDeployTimeoutSeconds": {value: 120, prep: exWithCommand},

	"config.gitUrl":         {value: "https://git.example.com/acme/other.git"},
	"config.gitBranch":      {value: "develop"},
	"config.submodules":     {value: "on"},
	"config.buildType":      {value: "dockerfile"},
	"config.dockerfilePath": {value: "apps/web/Dockerfile"},
	"config.buildContext":   {value: "apps/web"},
	"config.buildLocation":  {value: "build_server"},
	"config.buildServerId":  {value: "builder"},
	"config.registryId":     {value: "ghcr"},

	"config.database.engine":       {value: "mysql", prep: func(c *services.Config) { c.Database = &engines.Spec{} }},
	"config.database.version":      {value: "16"},
	"config.database.flavor":       {value: "postgis"},
	"config.database.databaseName": {value: "shopdb"},
	"config.database.username":     {value: "shopuser"},

	"config.route.protocol":           {value: "tcp"},
	"config.route.upstreams":          {value: []projectconfig.UpstreamValue{{Address: "10.0.0.2:8080"}, {Service: "api", Port: 9000}}},
	"config.route.scheme":             {value: "https"},
	"config.route.insecureSkipVerify": {value: true},
	"config.route.serverName":         {value: "printer.lan"},
	"config.route.passHost":           {value: false},
	"config.route.healthPath":         {value: "/up"},
	"config.route.verifySkip":         {value: true},
	"config.route.listenPort":         {value: 25000},
	"config.route.allowCidrs":         {value: []string{"10.0.0.0/8"}},
	"config.route.denyCidrs":          {value: []string{"10.1.0.0/16"}},
	"config.route.openToInternet":     {value: true},
	"config.route.proxyProtocol":      {value: 2},

	"config.stack.compose":             {value: "services:\n  web:\n    image: nginx:2\n"},
	"config.stack.waitSeconds":         {value: 120},
	"config.stack.resources.web":       {value: projectconfig.StackResourceValue{Component: "web", MemoryMB: 128, CPUs: 0.5}},
	"config.files":                     {value: []projectconfig.FileMount{{Path: "/etc/app/app.conf", Mode: "0600", Content: "x = 1\n"}}},
	"config.docker.capAdd":             {value: []string{"NET_BIND_SERVICE"}},
	"config.docker.capDrop":            {value: []string{"ALL"}},
	"config.docker.noNewPrivileges":    {value: true},
	"config.docker.readOnlyRootfs":     {value: true},
	"config.docker.pidsLimit":          {value: int64(512)},
	"config.docker.hostname":           {value: "web"},
	"config.docker.dns":                {value: []string{"1.1.1.1"}},
	"config.docker.sysctls":            {value: map[string]string{"net.core.somaxconn": "1024"}},
	"config.docker.hostSysctls":        {value: map[string]int64{"vm.max_map_count": 262144}},
	"config.docker.user":               {value: "1000:1000"},
	"config.docker.groupAdd":           {value: []string{"44"}},
	"config.docker.ulimits":            {value: []agentprotocol.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}}},
	"config.docker.shmSizeMb":          {value: 256},
	"config.docker.extraHosts":         {value: []string{"db.local:10.0.0.5"}},
	"config.docker.labels":             {value: map[string]string{"team": "shop"}},
	"config.docker.init":               {value: true},
	"config.docker.stopSignal":         {value: "SIGQUIT"},
	"config.docker.tmpfs":              {value: []agentprotocol.TmpfsMount{{Path: "/tmp", SizeMB: 64, Mode: "1777"}}},
	"config.docker.entrypoint":         {value: []string{"/entrypoint.sh"}},
	"config.docker.devices":            {value: []agentprotocol.DeviceMapping{{HostPath: "/dev/dri/renderD128", ContainerPath: "/dev/dri/gpu0", Permissions: "rw"}}},
	"config.docker.gpus":               {value: agentprotocol.GPURequest{Count: 1, Driver: "nvidia", Capabilities: []string{"compute"}}},
	"config.docker.runtime":            {value: "runsc"},
	"config.docker.privileged":         {value: true},
	"config.docker.pidHost":            {value: true},
	"config.docker.seccompUnconfined":  {value: true},
	"config.docker.apparmorUnconfined": {value: true},
}

const exCompose = "services:\n  web:\n    image: nginx:1\n"

// exBase is the config of a service of the given kind, with its row kind and source type.
func exBase(kind string) (cfg services.Config, rowKind, source string) {
	switch kind {
	case projectconfig.KindGit:
		return services.Config{GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 80}, services.KindApp, services.SourceGit
	case projectconfig.KindDatabase:
		return services.Config{Database: &engines.Spec{Engine: "postgres"}}, services.KindDatabase, services.SourceImage
	case projectconfig.KindRoute:
		return services.Config{Route: &services.RouteConfig{Protocol: "http", Upstreams: []services.RouteUpstream{{Address: "10.0.0.1:80"}}}}, services.KindRoute, services.SourceExternal
	case projectconfig.KindStack:
		return services.Config{Stack: &services.StackConfig{Compose: exCompose}}, services.KindStack, services.SourceCompose
	}
	return services.Config{Image: "nginx:1", Port: 80}, services.KindApp, services.SourceImage
}

// Every catalog field of services.Config survives export: set it to a non-default value through
// the planner's own setter, export, parse, and the planner must see the live value in the file.
// A field the exporter forgets fails here.
func TestExportConfigFieldsRoundTrip(t *testing.T) {
	seen := 0
	for _, spec := range projectconfig.Catalog() {
		if !strings.HasPrefix(spec.ID, "config.") {
			continue
		}
		id := strings.ReplaceAll(spec.ID, "*", "web")
		seen++
		t.Run(id, func(t *testing.T) {
			ex, ok := exExamples[id]
			require.True(t, ok, "catalog field %s has no example in exExamples: add one (and export it)", id)
			kind := projectconfig.KindImage
			if len(spec.Kinds) > 0 {
				kind = spec.Kinds[0]
			}
			cfg, rowKind, source := exBase(kind)
			live := exLiveEnv(exTestRefs(), exImageSvc("api", services.Config{Image: "api:1", Port: 9000}))
			pc := &planCtx{live: live, res: &projectconfig.Resolved{}}
			if ex.prep != nil {
				ex.prep(&cfg)
			}
			f, ok := lookupCfgField(id)
			require.True(t, ok, "no planner setter for %s", id)
			require.NoError(t, f.set(pc, &cfg, ex.value))

			ls := exSvc("web", rowKind, source, cfg)
			live = exLiveEnv(exTestRefs(), ls, live.Services["api"])
			pc = &planCtx{live: live, res: &projectconfig.Resolved{}}
			text, _ := exExport(t, live, false)
			_, res := exParseResolve(t, text, "")
			rs := res.Services["web"]
			require.NotNil(t, rs, text)
			fv, ok := rs.Fields[id]
			require.True(t, ok, "the export dropped %s:\n%s", id, text)

			pc.res = res
			desired, err := configOps{}.desired(pc, ls, fv)
			require.NoError(t, err)
			got, present := configOps{}.live(pc, ls, id, "")
			require.True(t, present)
			require.Equal(t, got.FP, desired.FP, "the file does not say what is live for %s:\n%s", id, text)
		})
	}
	require.Greater(t, seen, 60)
}

// A new field of services.Config must be exported, or named as not representable here.
func TestExportCoversEveryConfigField(t *testing.T) {
	handled := map[string]bool{
		"Image": true, "GitURL": true, "GitBranch": true, "DockerfilePath": true, "BuildContext": true, "BuildType": true,
		"Submodules": true, "BuildLocation": true, "BuildServerID": true, "RegistryID": true, "Port": true, "StartCommand": true,
		"Healthcheck": true, "StopGraceSeconds": true, "MemoryMB": true, "CPUs": true, "ImageRetention": true, "Files": true,
		"OneShot": true, "Route": true, "Database": true, "PreDeployCommand": true, "PreDeployTimeoutSeconds": true,
		"PostDeployCommand": true, "PostDeployTimeoutSeconds": true, "BlockEgress": true, "Docker": true, "Stack": true,
		"Tailnet":     true, // not representable: listed as such
		"ProcessName": true, "Singleton": true, "Processes": true,
	}
	rt := reflect.TypeOf(services.Config{})
	for i := range rt.NumField() {
		require.True(t, handled[rt.Field(i).Name], "services.Config.%s is neither exported nor listed as not representable", rt.Field(i).Name)
	}
}

// ---- everything that is not services.Config ----

func exRichEnv() *liveEnv {
	r := exTestRefs()
	web := exSvc("web", services.KindApp, services.SourceGit, services.Config{
		GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 3000, BuildType: "dockerfile",
		BuildServerID: new(exID(2)), RegistryID: new(exID(4)), BuildLocation: "build_server",
		Healthcheck: services.Healthcheck{Type: "exec", Command: []string{"CMD", "true"}},
		Files:       []services.FileMount{{Path: "/etc/app/app.conf", Mode: "0600", Content: "listen = 3000\n"}},
	})
	web.Row.Name = "Web"
	web.Row.ServerID = new(exID(1))
	web.Row.Replicas = 2
	web.Scale = services.Autoscaling{Enabled: true, Min: 2, Max: 5, TargetCPUPercent: 60}
	web.Git = &db.ServiceGit{ServiceID: web.Row.ID, ConnectionID: exID(3), RepoFullName: "acme/shop", AutoDeploy: false, WatchPaths: []string{"apps/web/**"}}
	web.GitConn = "github-acme"
	web.Links = []string{"db"}
	web.Networks = []db.ListServicesNetworksRow{{NetworkID: exID(6), NetworkName: "backplane"}}
	web.Volumes = []db.Volume{{Name: "uploads", MountPath: "/app/uploads"}}
	web.Vars["NODE_ENV"] = liveVar{Key: "NODE_ENV", Template: "production"}
	web.Vars["API_TOKEN"] = liveVar{Key: "API_TOKEN", Secret: true, Opaque: true}
	web.Tasks["digest"] = db.ServiceTask{Name: "digest", Schedule: "0 7 * * 1-5", Timezone: "Europe/Berlin", Command: json.RawMessage(`["bun","run","digest"]`), Enabled: true, TimeoutSeconds: 900, Concurrency: "forbid"}
	web.Monitor = &db.UptimeMonitor{Enabled: true, Alerts: true, FailureThreshold: 3, RecoveryThreshold: 2, ReminderMinutes: 60}
	web.Owned["generate_domain"] = "x"
	serve := db.Domain{ID: uuid.New(), ServiceID: web.Row.ID, Hostname: "shop.example.com", TargetPort: 3000, Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto", CfZoneID: "z1", CfProxied: false, CfOriginLock: true}
	www := db.Domain{ID: uuid.New(), ServiceID: web.Row.ID, Hostname: "www.shop.example.com", Kind: "redirect", RedirectTo: new("shop.example.com"), RedirectCode: 308, PreservePath: true, TlsMode: "auto"}
	web.Domains = []db.Domain{serve, www}

	dbs := exSvc("db", services.KindDatabase, services.SourceImage, services.Config{Database: &engines.Spec{Engine: "postgres"}, BlockEgress: true})
	dbs.Row.ServerID = new(exID(1))
	dest, key := exID(5), "nightly"
	dbs.Backups = []db.BackupSchedule{{ID: uuid.New(), DestinationID: dest, Cron: "0 2 * * *", Timezone: "UTC", Enabled: true, RetentionCount: 14, RetentionDays: 30, ConfigKey: &key}}

	printer := exSvc("printer", services.KindRoute, services.SourceExternal, services.Config{Route: &services.RouteConfig{
		Protocol: "http", Upstreams: []services.RouteUpstream{{Address: "192.168.1.20:631"}, {ServiceID: new(web.Row.ID), Port: 3000}},
	}})
	printer.Row.ServerID = new(exID(1))
	printer.Domains = []db.Domain{{ID: uuid.New(), ServiceID: printer.Row.ID, Hostname: "print.example.com", Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto"}}

	stack := exSvc("blog", services.KindStack, services.SourceCompose, services.Config{Stack: &services.StackConfig{
		Compose: exCompose, Resources: map[string]agentprotocol.StackResources{"web": {MemoryMB: 256}},
	}})
	stack.Row.ServerID = new(exID(1))
	stack.Vars["SITE"] = liveVar{Key: "SITE", Template: "blog"}
	stack.Domains = []db.Domain{{ID: uuid.New(), ServiceID: stack.Row.ID, Hostname: "blog.example.com", Component: "web", Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto"}}
	jobs := exSvc("jobs", services.KindApp, services.SourceImage, services.Config{
		Image: "ghcr.io/acme/jobs:2", Port: 8080, ProcessName: "app", Singleton: true, StopGraceSeconds: 45,
		Processes: []services.ProcessConfig{
			{Name: "worker", StartCommand: []string{"bundle", "exec", "sidekiq"}, StopGraceSeconds: 120, MemoryMB: 512, CPUs: 1},
			{Name: "clock", StartCommand: []string{"bundle", "exec", "clockwork", "clock.rb"}, Singleton: true},
			{
				Name: "metrics", StartCommand: []string{"bin/metrics"}, Port: 9100,
				Healthcheck: services.Healthcheck{Type: "http", Path: "/up", DeadlineSeconds: 60},
			},
		},
	})
	jobs.Row.ServerID = new(exID(1))
	workerScale, err := json.Marshal(services.Autoscaling{Enabled: true, Min: 2, Max: 6, TargetCPUPercent: 65})
	if err != nil {
		panic(err)
	}
	jobs.ProcScales = []db.ServiceProcessScale{{ServiceID: jobs.Row.ID, Process: "worker", Replicas: 2, Autoscaling: workerScale}}
	jobs.Domains = []db.Domain{{ID: uuid.New(), ServiceID: jobs.Row.ID, Hostname: "metrics.example.com", TargetPort: 9100, Component: "metrics", Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto"}}
	return exLiveEnv(r, web, dbs, printer, stack, jobs)
}

// Every field id of the catalog that is not a services.Config field is produced for some service.
func TestExportCoversTheCatalog(t *testing.T) {
	text, _ := exExport(t, exRichEnv(), true)
	_, res := exParseResolve(t, text, "")
	produced := map[string]bool{}
	for _, rs := range res.Services {
		for id := range rs.Fields {
			if spec, ok := projectconfig.LookupField(id); ok {
				produced[spec.ID] = true
			}
		}
	}
	for _, spec := range projectconfig.Catalog() {
		if strings.HasPrefix(spec.ID, "config.") {
			continue
		}
		require.True(t, produced[spec.ID], "no exported service carries %s\n%s", spec.ID, text)
	}
}

func TestExportRichEnvironmentIsDeterministicAndSecretFree(t *testing.T) {
	first, v := exExport(t, exRichEnv(), false)
	for range 10 {
		again, _ := exExport(t, exRichEnv(), false)
		require.Equal(t, first, again)
	}
	require.True(t, strings.HasPrefix(first, "#:schema https://nebula.test/schema/nebula.toml.json\n"))
	require.NotContains(t, first, "\r")
	require.Empty(t, v.NotRepresentable)

	f, res := exParseResolve(t, first, "")
	_ = f
	// The one server everything runs on is the file's default.
	require.Equal(t, "fra-1", res.Server)
	require.Equal(t, "fra-1", res.Services["web"].ServerName)
	// Names, not ids.
	require.NotContains(t, first, exID(1).String())
	require.Equal(t, "github-acme", exDeref(res.Services["web"].Def.Git.Connection))
	require.Equal(t, "builder", exDeref(res.Services["web"].Def.Build.Server))
	require.Equal(t, "ghcr", exDeref(res.Services["web"].Def.Build.Registry))
	// A managed upstream is the slug and the port.
	ups := res.Services["printer"].Def.Route.Upstreams
	require.Len(t, ups, 2)
	require.Equal(t, "192.168.1.20:631", exDeref(ups[0].Address))
	require.Equal(t, "web", exDeref(ups[1].Service))
	// The www companion is www_redirect on the serve domain, not a domain of its own.
	doms := res.Services["web"].Def.Domains
	require.Len(t, doms, 1)
	require.True(t, *doms["shop.example.com"].WWWRedirect)
	require.False(t, *doms["shop.example.com"].Cloudflare.Proxied, "a DNS-only name is written: proxied is the default")
	require.Nil(t, doms["shop.example.com"].Cloudflare.OriginLock, "defaults are omitted")
	// database credentials default: block_egress true is the default of a database.
	require.Nil(t, res.Services["db"].Def.BlockEgress)
	require.Equal(t, "nightly", func() string { k := sortedKeys(res.Services["db"].Def.Backups); return k[0] }())
}

func exDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestExportOmitsDefaultsUnlessAskedOrOwned(t *testing.T) {
	mk := func() *liveEnv {
		return exLiveEnv(exTestRefs(), exImageSvc("web", services.Config{Image: "nginx:1", Port: 80}))
	}

	_, v := exExport(t, mk(), false)
	d := v.Res.Services["web"].Def
	require.Equal(t, "nginx:1", exDeref(d.Image))
	require.NotNil(t, d.Port)
	for name, absent := range map[string]bool{
		"name": d.Name == nil, "stop_grace": d.StopGraceSeconds == nil, "retention": d.ImageRetention == nil, "memory": d.MemoryMB == nil,
		"cpus": d.CPUs == nil, "block_egress": d.BlockEgress == nil, "one_shot": d.OneShot == nil, "health_check": d.HealthCheck == nil,
		"pre_deploy": d.PreDeploy == nil, "uptime": d.Uptime == nil, "scale": d.Scale == nil, "docker": d.Docker == nil, "variables": d.Variables == nil,
	} {
		require.True(t, absent, "%s is a default and must not be written", name)
	}

	_, v = exExport(t, mk(), true)
	d = v.Res.Services["web"].Def
	require.Equal(t, 30, *d.StopGraceSeconds)
	require.Equal(t, 5, *d.ImageRetention)
	require.Equal(t, "web", exDeref(d.Name))
	require.Equal(t, "http", exDeref(d.HealthCheck.Type))
	require.Equal(t, "/", exDeref(d.HealthCheck.Path))
	require.Equal(t, 120, *d.HealthCheck.DeadlineSeconds)
	require.Equal(t, 3, *d.Uptime.FailureThreshold)
	require.Equal(t, 2, *d.Uptime.RecoveryThreshold)
	require.Equal(t, 0, *d.Uptime.ReminderMinutes)
	require.Equal(t, 1, *d.Scale.Replicas)
	require.Equal(t, false, *d.Scale.Autoscaling.Enabled)

	// Owned keys are written even when they equal their default.
	live := mk()
	live.Services["web"].Owned["config.stopGraceSeconds"] = "fp"
	live.Services["web"].Owned["uptime.failureThreshold"] = "fp"
	_, v = exExport(t, live, false)
	d = v.Res.Services["web"].Def
	require.Equal(t, 30, *d.StopGraceSeconds)
	require.Equal(t, 3, *d.Uptime.FailureThreshold)
	require.Nil(t, d.Uptime.ReminderMinutes)
	require.Nil(t, d.ImageRetention)

	// A value that differs is written without anything asked.
	live = mk()
	live.Services["web"].Cfg.StopGraceSeconds = 60
	live.Services["web"].Cfg.Healthcheck.DeadlineSeconds = 90
	_, v = exExport(t, live, false)
	d = v.Res.Services["web"].Def
	require.Equal(t, 60, *d.StopGraceSeconds)
	require.Equal(t, 90, *d.HealthCheck.DeadlineSeconds)
	require.Nil(t, d.HealthCheck.Type)
	require.Nil(t, d.HealthCheck.Path)
}

func TestExportDatabaseDefaultsAndCredentials(t *testing.T) {
	dbs := exSvc("db", services.KindDatabase, services.SourceImage, services.Config{Database: &engines.Spec{Engine: "postgres"}, BlockEgress: true})
	spec := dbs.Cfg.Database
	for _, k := range []string{"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB"} {
		dbs.Vars[k] = liveVar{Key: k, Secret: k == "POSTGRES_PASSWORD", Template: "${{secret(24)}}"}
	}
	dbs.Vars["POOL_SIZE"] = liveVar{Key: "POOL_SIZE", Template: "10"}
	dbs.Vars["FROM_TEMPLATE"] = liveVar{Key: "FROM_TEMPLATE", Template: "x", Source: services.SourceTemplate}
	text, _ := exExport(t, exLiveEnv(exTestRefs(), dbs), false)
	_, res := exParseResolve(t, text, "")
	d := res.Services["db"].Def
	require.Equal(t, "postgres", exDeref(d.Database.Engine))
	require.Equal(t, spec.Version, exDeref(d.Database.Version))
	require.Nil(t, d.Image, "derived")
	require.Nil(t, d.Port)
	require.Nil(t, d.HealthCheck)
	require.Nil(t, d.BlockEgress, "a database blocks egress by default")
	require.Len(t, d.Variables, 1)
	require.Equal(t, "10", d.Variables["POOL_SIZE"].Value)
	require.NotContains(t, text, "POSTGRES_")

	dbs.Cfg.BlockEgress = false
	text, _ = exExport(t, exLiveEnv(exTestRefs(), dbs), false)
	_, res = exParseResolve(t, text, "")
	require.False(t, *res.Services["db"].Def.BlockEgress)
}

func TestExportVariablesAndSecrets(t *testing.T) {
	rotate := 90
	ghToken := "ghp_" + strings.Repeat("aB3", 12)
	lvs := map[string]liveVar{
		"NODE_ENV":          {Key: "NODE_ENV", Template: "production"},
		"APP_URL":           {Key: "APP_URL", Template: "https://${{shared.HOST}}"},
		"STRIPE_SECRET_KEY": {Key: "STRIPE_SECRET_KEY", Secret: true, Opaque: true},
		"SESSION_SECRET":    {Key: "SESSION_SECRET", Secret: true, Template: "${{secret(48)}}"},
		"MIXED":             {Key: "MIXED", Secret: true, Template: "pre-${{secret(8)}}"},
		"DB_REF":            {Key: "DB_REF", Secret: true, Template: "${{db.PASSWORD}}"},
		"REQUEST_ID":        {Key: "REQUEST_ID", Secret: true, Template: "${{uuid()}}"},
		"LEGACY_TOKEN":      {Key: "LEGACY_TOKEN", Template: "legacy-token-value-42"},
		"WEBHOOK":           {Key: "WEBHOOK", Template: ghToken},
		"BUILD_FLAG":        {Key: "BUILD_FLAG", Template: "1", Scope: "build", Comment: "read by the bundler"},
		"ROTATING":          {Key: "ROTATING", Template: "x", Rotate: &rotate},
		"EMPTY_SECRET":      {Key: "EMPTY_SECRET", Secret: true},
		"PORT":              {Key: "PORT", Template: "8080"},
		"NEBULA_INTERNAL":   {Key: "NEBULA_INTERNAL", Template: "x"},
	}
	x := newExporter(exLiveEnv(exTestRefs()), false)
	out := x.variables(lvs, nil, "services.web.secrets", "web")

	require.Equal(t, map[string]projectconfig.Variable{
		"NODE_ENV":       {Value: "production"},
		"APP_URL":        {Value: "https://${{shared.HOST}}"},
		"SESSION_SECRET": {Value: "${{secret(48)}}"},
		"DB_REF":         {Value: "${{db.PASSWORD}}"},
		"REQUEST_ID":     {Value: "${{uuid()}}"},
		"BUILD_FLAG":     {Value: "1", Scope: "build", Comment: "read by the bundler"},
		"ROTATING":       {Value: "x", RotateEveryDays: &rotate},
	}, out.Vars)
	// One generator call is its own template; everything else secret is a name. The flag of a
	// secret that is not a secret generator is kept through the name.
	require.Equal(t, []string{"DB_REF", "EMPTY_SECRET", "LEGACY_TOKEN", "MIXED", "REQUEST_ID", "STRIPE_SECRET_KEY", "WEBHOOK"}, out.Keys)
	require.Equal(t, "runtime", out.Scope)

	var flagged []string
	for _, d := range x.warns {
		require.Equal(t, "secret_flag_differs", d.Code)
		require.Equal(t, projectconfig.SeverityWarning, d.Severity)
		require.Equal(t, "services.web.secrets.keys", d.Path)
		require.NotContains(t, d.Message, "legacy-token-value-42")
		require.NotContains(t, d.Message, ghToken)
		flagged = append(flagged, strings.Fields(d.Message)[0])
	}
	sort.Strings(flagged)
	require.Equal(t, []string{"LEGACY_TOKEN", "WEBHOOK"}, flagged)

	lines := x.comments["services.web.secrets"]
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], "generated; template not exported")
	require.Contains(t, lines[0], "MIXED")
	require.NotContains(t, lines[0], "SESSION_SECRET")
}

func TestExportSecretScopes(t *testing.T) {
	build := map[string]liveVar{
		"A": {Key: "A", Secret: true, Opaque: true, Scope: "build"},
		"B": {Key: "B", Secret: true, Opaque: true, Scope: "build"},
	}
	x := newExporter(exLiveEnv(exTestRefs()), false)
	out := x.variables(build, nil, "services.web.secrets", "web")
	require.Equal(t, "build", out.Scope)
	require.Empty(t, x.notRep)

	mixed := map[string]liveVar{
		"A": {Key: "A", Secret: true, Opaque: true, Scope: "build"},
		"B": {Key: "B", Secret: true, Opaque: true},
	}
	x = newExporter(exLiveEnv(exTestRefs()), false)
	out = x.variables(mixed, nil, "services.web.secrets", "web")
	require.Equal(t, "runtime", out.Scope)
	require.Equal(t, []string{"A", "B"}, out.Keys)
	require.Len(t, x.notRep["web"], 1)
	require.Contains(t, x.notRep["web"][0], "A (build)")
	require.True(t, x.warns.HasCode("not_representable"))
}

func TestExportSharedLayersAndSingleServer(t *testing.T) {
	web := exImageSvc("web", services.Config{Image: "nginx:1", Port: 80})
	web.Row.ServerID = new(exID(1))
	worker := exImageSvc("worker", services.Config{Image: "worker:1"})
	worker.Row.ServerID = new(exID(1))
	live := exLiveEnv(exTestRefs(), web, worker)
	live.SharedProject["APP_NAME"] = liveVar{Key: "APP_NAME", Template: "Acme"}
	live.SharedProject["ORG_SECRET"] = liveVar{Key: "ORG_SECRET", Secret: true, Opaque: true}
	live.SharedEnv["APP_NAME"] = liveVar{Key: "APP_NAME", Template: "Acme (prod)", Comment: "env override"}

	text, v := exExport(t, live, false)
	require.Equal(t, "fra-1", v.Res.Server)
	require.Nil(t, v.Res.Services["web"].Def.Server)
	f, res := exParseResolve(t, text, "production")
	require.Equal(t, "Acme", res.Shared["APP_NAME"].Value)
	require.Equal(t, []string{"ORG_SECRET"}, res.SharedSecrets)
	require.Equal(t, "Acme (prod)", res.EnvShared["APP_NAME"].Value)
	require.Equal(t, "env override", res.EnvShared["APP_NAME"].Comment)
	require.Equal(t, []string{"production"}, f.Environments())

	// A second server: every service names its own.
	worker.Row.ServerID = new(exID(2))
	_, v = exExport(t, live, false)
	require.Equal(t, "", v.Res.Server)
	require.Equal(t, "fra-1", exDeref(v.Res.Services["web"].Def.Server))
	require.Equal(t, "builder", exDeref(v.Res.Services["worker"].Def.Server))

	// A service without a server: no default either.
	worker.Row.ServerID = nil
	_, v = exExport(t, live, false)
	require.Equal(t, "", v.Res.Server)
	require.Equal(t, "fra-1", exDeref(v.Res.Services["web"].Def.Server))
	require.Nil(t, v.Res.Services["worker"].Def.Server)
}

func TestExportBackupKeys(t *testing.T) {
	dbs := exSvc("db", services.KindDatabase, services.SourceImage, services.Config{Database: &engines.Spec{Engine: "postgres"}})
	nightly, schedule1 := "nightly", "schedule-1"
	mk := func(key *string, cron string) db.BackupSchedule {
		return db.BackupSchedule{ID: uuid.New(), DestinationID: exID(5), Cron: cron, Timezone: "Europe/Berlin", Enabled: false, RetentionCount: 3, RetentionDays: 10, ConfigKey: key}
	}
	dbs.Backups = []db.BackupSchedule{mk(nil, "0 1 * * *"), mk(&nightly, "0 2 * * *"), mk(nil, "0 3 * * *"), mk(&schedule1, "0 4 * * *")}
	_, v := exExport(t, exLiveEnv(exTestRefs(), dbs), false)
	got := v.Res.Services["db"].Def.Backups
	// The explicit key schedule-1 is taken: the two keyless schedules get schedule-2 and schedule-3, in creation order.
	require.Equal(t, []string{"nightly", "schedule-1", "schedule-2", "schedule-3"}, sortedKeys(got))
	require.Equal(t, "0 4 * * *", exDeref(got["schedule-1"].Cron))
	require.Equal(t, "0 1 * * *", exDeref(got["schedule-2"].Cron))
	require.Equal(t, "0 3 * * *", exDeref(got["schedule-3"].Cron))
	b := got["nightly"]
	require.Equal(t, "offsite", exDeref(b.Destination))
	require.Equal(t, "Europe/Berlin", exDeref(b.Timezone))
	require.False(t, *b.Enabled)
	require.Equal(t, 3, *b.RetentionCount)
	require.Equal(t, 10, *b.RetentionDays)
}

func TestExportDomains(t *testing.T) {
	web := exImageSvc("web", services.Config{Image: "nginx:1", Port: 3000})
	opts := agentprotocol.RouteOptions{
		ForceHTTPS: new(false), Compress: true, Noindex: true, SecurityHeaders: "strict",
		RequestHeaders:  []agentprotocol.Header{{Name: "A-Header", Value: "1"}, {Name: "B-Header", Value: "2"}},
		ResponseHeaders: []agentprotocol.Header{{Name: "X-Frame-Options", Value: "DENY"}},
		CORS:            &agentprotocol.CORS{Origins: []string{"https://app.example.com"}, Methods: []string{"GET"}, Credentials: true, MaxAgeSeconds: 600},
		Redirects:       []agentprotocol.Redirect{{Regex: "^/old/(.*)", Replacement: "/new/${1}", Permanent: true}, {Regex: "^/a", Replacement: "/b"}},
		RateLimit:       &agentprotocol.RateLimit{Average: 100, PeriodSeconds: 1, Burst: 200, TrustedProxyDepth: 1},
		Access:          &agentprotocol.Access{AllowCIDRs: []string{"203.0.113.0/24"}, DenyCIDRs: []string{"198.51.100.0/24"}, TrustedProxyDepth: 1, RejectStatus: 404},
		BasicAuth:       true, LargeUploads: true,
	}
	raw, err := json.Marshal(opts)
	require.NoError(t, err)
	row := func(host string) db.Domain {
		return db.Domain{ID: uuid.New(), ServiceID: web.Row.ID, Hostname: host, Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto"}
	}
	main := row("shop.example.com")
	main.TargetPort = 3000
	main.Options = raw
	api := row("shop.example.com")
	api.PathPrefix, api.StripPrefix, api.AddPrefix, api.TargetPort = "/api", true, "/v1", 4000
	www := row("www.shop.example.com")
	www.Kind, www.RedirectTo = "redirect", new("shop.example.com")
	gen := row("web-1.apps.test")
	gen.Generated = true
	old := row("old.example.com")
	old.Kind, old.RedirectTo, old.RedirectCode, old.PreservePath = "redirect", new("shop.example.com"), 301, false
	cert := exID(7)
	secure := row("secure.example.com")
	secure.TlsMode, secure.CertificateID, secure.DnsResolver = "custom", &cert, "cloudflare"
	web.Domains = []db.Domain{main, api, www, gen, old, secure}

	text, v := exExport(t, exLiveEnv(exTestRefs(), web), false)
	_, res := exParseResolve(t, text, "")
	doms := res.Services["web"].Def.Domains
	require.Equal(t, []string{"old.example.com", "secure.example.com", "shop.example.com", "shop.example.com/api"}, sortedKeys(doms))
	require.Empty(t, v.NotRepresentable)

	require.Nil(t, doms["shop.example.com"].TargetPort, "the service port is the default")
	require.True(t, *doms["shop.example.com"].WWWRedirect)
	require.Equal(t, 4000, *doms["shop.example.com/api"].TargetPort)
	require.True(t, *doms["shop.example.com/api"].StripPrefix)
	require.Equal(t, "/v1", exDeref(doms["shop.example.com/api"].AddPrefix))
	require.Equal(t, "redirect", exDeref(doms["old.example.com"].Kind))
	require.Equal(t, "shop.example.com", exDeref(doms["old.example.com"].RedirectTo))
	require.Equal(t, 301, *doms["old.example.com"].RedirectCode)
	require.False(t, *doms["old.example.com"].PreservePath)
	require.Equal(t, "custom", exDeref(doms["secure.example.com"].TLS))
	require.Equal(t, "wildcard", exDeref(doms["secure.example.com"].Certificate))
	require.Equal(t, "cloudflare", exDeref(doms["secure.example.com"].DNSResolver))

	// The option set is the stored one, exactly, as the planner compares it.
	rule, ok := res.Services["web"].Fields["domains.shop.example.com"].Value.(projectconfig.DomainRule)
	require.True(t, ok)
	require.NotNil(t, rule.Options)
	require.Equal(t, services.NormalizeOptions(raw), optionsText(rule.Options))

	// Headers the file form cannot say exactly are reported.
	unsorted := opts
	unsorted.RequestHeaders = []agentprotocol.Header{{Name: "B", Value: "1"}, {Name: "A", Value: "2"}}
	unsortedRaw, err := json.Marshal(unsorted)
	require.NoError(t, err)
	web.Domains[0].Options = unsortedRaw
	_, v = exExport(t, exLiveEnv(exTestRefs(), web), false)
	require.Len(t, v.NotRepresentable["web"], 1)
	require.Contains(t, v.NotRepresentable["web"][0], "request_headers")
}

func TestExportCloudflareOnlyWhenConnectedAndDifferent(t *testing.T) {
	mk := func(connected bool, proxied, lock bool) map[string]*projectconfig.Domain {
		web := exImageSvc("web", services.Config{Image: "nginx:1", Port: 80})
		web.Domains = []db.Domain{{ID: uuid.New(), ServiceID: web.Row.ID, Hostname: "a.example.com", Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto",
			CfZoneID: "z", CfProxied: proxied, CfOriginLock: lock}}
		r := exTestRefs()
		r.Cloudflare = connected
		_, v := exExport(t, exLiveEnv(r, web), false)
		return v.Res.Services["web"].Def.Domains
	}
	require.Nil(t, mk(true, true, true)["a.example.com"].Cloudflare, "the defaults of a zone are not written")
	require.Nil(t, mk(false, false, false)["a.example.com"].Cloudflare, "no connection: the plan would refuse the key")
	cf := mk(true, false, false)["a.example.com"].Cloudflare
	require.NotNil(t, cf)
	require.False(t, *cf.Proxied, "a DNS-only name is written because proxied is the default")
	require.False(t, *cf.OriginLock)
}

func TestExportNeverWritesCredentials(t *testing.T) {
	const (
		password = "hunter2-very-secret"
		pem      = "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----\n"
		stripe   = "sk_live_Planted0123456789abcdef"
	)
	web := exSvc("web", services.KindApp, services.SourceGit, services.Config{
		GitURL: "https://bob:" + password + "@git.example.com/acme/shop.git", GitBranch: "main", Port: 80,
		Files: []services.FileMount{{Path: "/etc/key.pem", Content: pem}, {Path: "/etc/ok.conf", Content: "a = 1\n"}},
	})
	web.Vars["STRIPE_SECRET_KEY"] = liveVar{Key: "STRIPE_SECRET_KEY", Template: stripe}
	web.Vars["OTHER"] = liveVar{Key: "OTHER", Template: stripe}
	web.Vars["API_TOKEN"] = liveVar{Key: "API_TOKEN", Secret: true, Opaque: true}
	blog := exSvc("blog", services.KindStack, services.SourceCompose, services.Config{Stack: &services.StackConfig{
		Compose: "services:\n  web:\n    image: nginx\n    environment:\n      KEY: " + stripe + "\n",
	}})
	live := exLiveEnv(exTestRefs(), web, blog)
	live.SharedProject["SHARED_PASSWORD"] = liveVar{Key: "SHARED_PASSWORD", Template: password}
	text, v := exExport(t, live, true)

	blob, err := json.Marshal(v.Warnings)
	require.NoError(t, err)
	for _, secret := range []string{password, "PRIVATE KEY", "MIIEpAIBAAKCAQEA", stripe} {
		require.NotContains(t, text, secret)
		require.NotContains(t, string(blob), secret)
	}
	d := v.Res.Services["web"].Def
	require.Equal(t, "https://git.example.com/acme/shop.git", exDeref(d.Git.URL), "the userinfo is stripped")
	require.Equal(t, []string{"/etc/ok.conf"}, sortedKeys(d.Files))
	require.Equal(t, []string{"API_TOKEN", "OTHER", "STRIPE_SECRET_KEY"}, d.Secrets.Keys)
	require.Nil(t, v.Res.Services["blog"].Def.Stack.Compose)
	require.Equal(t, []string{"SHARED_PASSWORD"}, v.Res.SharedSecrets)
	require.Len(t, v.NotRepresentable["web"], 2)
	require.Len(t, v.NotRepresentable["blog"], 1)
}

func TestExportNotRepresentable(t *testing.T) {
	web := exImageSvc("web", services.Config{Image: "nginx:1", Port: 80, Tailnet: &tailnetcfg.TailnetConfig{Enabled: true, Web: true}})
	dbs := exSvc("db", services.KindDatabase, services.SourceImage, services.Config{Database: &engines.Spec{Engine: "postgres", Replica: &engines.ReplicaSpec{}}})
	rt := exSvc("tcp", services.KindRoute, services.SourceExternal, services.Config{Route: &services.RouteConfig{
		Protocol: "tcp", Upstreams: []services.RouteUpstream{{Address: "10.0.0.1:5432"}}, PublicDatabase: true, OpenToInternet: true,
		Scheme: "", ListenPort: 5432,
	}})
	rt.Row.ServerID = new(exID(1))
	text, v := exExport(t, exLiveEnv(exTestRefs(), web, dbs, rt), false)
	require.Equal(t, []string{"tailnet"}, v.NotRepresentable["web"])
	require.Equal(t, []string{"database.replica"}, v.NotRepresentable["db"])
	require.Equal(t, []string{"route.publicDatabase"}, v.NotRepresentable["tcp"])
	require.Contains(t, text, "# not representable in schema 1: tailnet\n")
	require.Contains(t, text, "# not representable in schema 1: route.publicDatabase\n")
	require.Len(t, v.Warnings, 3)
	for _, d := range v.Warnings {
		require.Equal(t, "not_representable", d.Code)
		require.Equal(t, projectconfig.SeverityInfo, d.Severity)
	}
}

func TestExportAmbiguousNamesFail(t *testing.T) {
	cases := map[string]func(r *refs, s *liveService){
		"server": func(r *refs, s *liveService) {
			r.Servers = append(r.Servers, db.Server{ID: exID(50), Name: "FRA-1"})
			s.Row.ServerID = new(exID(1))
		},
		"git connection": func(r *refs, s *liveService) {
			r.Connections = append(r.Connections, db.GitConnection{ID: exID(51), Name: "github-acme"})
			s.Git = &db.ServiceGit{ConnectionID: exID(3), RepoFullName: "a/b"}
		},
		"registry": func(r *refs, s *liveService) {
			r.Registries = append(r.Registries, db.Registry{ID: exID(52), Name: "GHCR"})
			s.Cfg.RegistryID = new(exID(4))
		},
		"backup destination": func(r *refs, s *liveService) {
			r.Destinations = append(r.Destinations, db.BackupDestination{ID: exID(53), Name: "Offsite"})
			s.Row.Kind, s.Row.SourceType = services.KindDatabase, services.SourceImage
			s.Cfg.Database = &engines.Spec{Engine: "postgres"}
			s.Backups = []db.BackupSchedule{{ID: uuid.New(), DestinationID: exID(5), Cron: "0 2 * * *"}}
		},
		"shared network": func(r *refs, s *liveService) {
			r.Networks = append(r.Networks, db.SharedNetwork{ID: exID(54), Name: "BACKPLANE", ServerID: exID(2)})
			s.Networks = []db.ListServicesNetworksRow{{NetworkName: "backplane"}}
		},
		"certificate": func(r *refs, s *liveService) {
			r.Certs = append(r.Certs, db.Certificate{ID: exID(55), Name: "Wildcard", ServerID: exID(1)})
			cert := exID(7)
			s.Domains = []db.Domain{{ID: uuid.New(), ServiceID: s.Row.ID, Hostname: "a.example.com", Kind: "serve", TlsMode: "custom", CertificateID: &cert}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := exTestRefs()
			svc := exSvc("web", services.KindApp, services.SourceGit, services.Config{GitURL: "https://git.example.com/a/b.git", GitBranch: "main", Port: 80})
			mutate(r, svc)
			_, err := newExporter(exLiveEnv(r, svc), false).resolve()
			ae, ok := apierr.As(err)
			require.True(t, ok, "expected an API error, got %v", err)
			require.Equal(t, "ambiguous_reference", ae.Code)
			require.Contains(t, ae.Message, name)
		})
	}
}

func TestExportServiceWithoutOptionalSectionsIsMinimal(t *testing.T) {
	text, _ := exExport(t, exLiveEnv(exTestRefs(), exImageSvc("web", services.Config{Image: "nginx:1"})), false)
	require.True(t, strings.HasPrefix(text, "#:schema https://nebula.test/schema/nebula.toml.json\nschema = 1\nproject = \"shop\"\n"), text)
	require.Contains(t, text, "[services.web]\nimage = \"nginx:1\"\n")
	require.Equal(t, 1, strings.Count(text, "\n["), "only the service table is written:\n"+text)
	require.NotContains(t, text, "server")
}

// ---- processes ----

// exJobs exports the rich environment and returns the resolved jobs service: the default process
// "app" with the extra processes worker (scaled), clock (singleton) and metrics (own port).
func exJobs(t *testing.T, defaults bool) (string, *projectconfig.ResolvedService) {
	t.Helper()
	text, _ := exExport(t, exRichEnv(), defaults)
	_, res := exParseResolve(t, text, "")
	rs := res.Services["jobs"]
	require.NotNil(t, rs, text)
	return text, rs
}

func TestExportProcesses(t *testing.T) {
	text, rs := exJobs(t, false)
	d := rs.Def
	for _, want := range []string{
		"process_name = \"app\"", "singleton = true",
		"[services.jobs.processes.clock]", "[services.jobs.processes.metrics]", "[services.jobs.processes.worker]",
	} {
		require.Contains(t, text, want)
	}
	require.Equal(t, "app", exDeref(d.ProcessName))
	require.NotNil(t, d.Singleton)
	require.True(t, *d.Singleton)
	require.Equal(t, []string{"clock", "metrics", "worker"}, sortedKeys(d.Processes), "the processes are tables keyed by name")

	// The command is always written; the rest only when it differs from what the platform fills in.
	worker := d.Processes["worker"]
	require.Equal(t, []string{"bundle", "exec", "sidekiq"}, worker.Command)
	require.Equal(t, 120, *worker.StopGraceSeconds)
	require.Equal(t, 512, *worker.MemoryMB)
	require.InDelta(t, 1.0, *worker.CPUs, 0)
	require.Nil(t, worker.Port)
	require.Nil(t, worker.Singleton)
	require.Nil(t, worker.HealthCheck, "a process without a port has the default health check (none)")
	require.NotNil(t, worker.Scale)
	require.Equal(t, 2, *worker.Scale.Replicas)
	au := worker.Scale.Autoscaling
	require.NotNil(t, au)
	require.True(t, *au.Enabled)
	require.Equal(t, 2, *au.Min)
	require.Equal(t, 6, *au.Max)
	require.Equal(t, 65, *au.TargetCPUPercent)

	clock := d.Processes["clock"]
	require.Equal(t, []string{"bundle", "exec", "clockwork", "clock.rb"}, clock.Command)
	require.True(t, *clock.Singleton)
	require.Nil(t, clock.Port)
	require.Nil(t, clock.StopGraceSeconds, "0 inherits the service's: nothing to write")
	require.Nil(t, clock.MemoryMB)
	require.Nil(t, clock.CPUs)
	require.Nil(t, clock.HealthCheck)
	require.Nil(t, clock.Scale, "a process that was never scaled writes no scale")

	// Only the health check keys that differ from the defaults of the process' own port.
	metrics := d.Processes["metrics"]
	require.Equal(t, 9100, *metrics.Port)
	require.NotNil(t, metrics.HealthCheck)
	require.Equal(t, "/up", exDeref(metrics.HealthCheck.Path))
	require.Equal(t, 60, *metrics.HealthCheck.DeadlineSeconds)
	require.Nil(t, metrics.HealthCheck.Type, "http is the default of a process with a port")
	require.Nil(t, metrics.HealthCheck.Port, "the health check port is the process' own")
	require.Nil(t, metrics.HealthCheck.IntervalSeconds)
	require.Nil(t, metrics.Scale)

	// The domain of a process names it, and its target port is the process' own.
	dom := d.Domains["metrics.example.com"]
	require.NotNil(t, dom)
	require.Equal(t, "metrics", exDeref(dom.Process))
	require.Nil(t, dom.Component, "component is for compose stacks")
	require.Nil(t, dom.TargetPort, "the target port defaults to the process' port")
}

func TestExportProcessesWithDefaults(t *testing.T) {
	_, rs := exJobs(t, true)
	clock := rs.Def.Processes["clock"]
	require.NotNil(t, clock.Port)
	require.Zero(t, *clock.Port)
	require.NotNil(t, clock.Singleton)
	require.True(t, *clock.Singleton)
	require.NotNil(t, clock.MemoryMB)
	require.NotNil(t, clock.CPUs)
	require.Nil(t, clock.StopGraceSeconds, "the file's minimum is 1: a process that inherits writes nothing")
	require.NotNil(t, clock.HealthCheck)
	require.Equal(t, "none", exDeref(clock.HealthCheck.Type))
	require.Equal(t, 2, *clock.HealthCheck.IntervalSeconds)
	require.NotNil(t, clock.Scale)
	require.Equal(t, 1, *clock.Scale.Replicas, "every scale key of every process is written when defaults are asked for")
	require.False(t, *clock.Scale.Autoscaling.Enabled)

	metrics := rs.Def.Processes["metrics"]
	require.Equal(t, "http", exDeref(metrics.HealthCheck.Type))
	require.Equal(t, 9100, *metrics.HealthCheck.Port)
	require.Equal(t, "app", exDeref(rs.Def.ProcessName))
}

func TestExportProcessNameIsWrittenOnlyWhenItIsNotTheDefault(t *testing.T) {
	mk := func(name string) *liveEnv {
		return exLiveEnv(exTestRefs(), exImageSvc("api", services.Config{
			Image: "nginx:1", Port: 80, ProcessName: name,
			Processes: []services.ProcessConfig{{Name: "queue", StartCommand: []string{"bin/queue"}}},
		}))
	}
	for _, name := range []string{"", "web"} {
		text, _ := exExport(t, mk(name), false)
		require.NotContains(t, text, "process_name", "the default name %q is not written", name)
		require.NotContains(t, text, "singleton")
		_, res := exParseResolve(t, text, "")
		require.Equal(t, []string{"queue"}, sortedKeys(res.Services["api"].Def.Processes))
	}
	text, _ := exExport(t, mk("app"), false)
	require.Contains(t, text, "process_name = \"app\"")
	text, _ = exExport(t, mk(""), true)
	require.Contains(t, text, "process_name = \"web\"", "the default is written when defaults are asked for")
	require.Contains(t, text, "singleton = false")

	// A service without processes writes none of the keys, even with defaults.
	plain, _ := exExport(t, exLiveEnv(exTestRefs(), exImageSvc("api", services.Config{Image: "nginx:1", Port: 80})), false)
	require.NotContains(t, plain, "processes")
	require.NotContains(t, plain, "process_name")
}

// Databases, routes and stacks have no processes: nothing is exported for them.
func TestExportProcessKeysAreForRuntimeKindsOnly(t *testing.T) {
	text, _ := exExport(t, exRichEnv(), true)
	_, res := exParseResolve(t, text, "")
	for _, slug := range []string{"db", "printer", "blog"} {
		d := res.Services[slug].Def
		require.Nil(t, d.Processes, slug)
		require.Nil(t, d.ProcessName, slug)
		require.Nil(t, d.Singleton, slug)
	}
}

// The exported processes are the live ones: every process field the file produces is the value the
// planner reads from the service, so planning the file changes nothing.
func TestExportProcessesRoundTrip(t *testing.T) {
	for name, defaults := range map[string]bool{"plain": false, "with defaults": true} {
		t.Run(name, func(t *testing.T) {
			live := exRichEnv()
			text, _ := exExport(t, live, defaults)
			_, res := exParseResolve(t, text, "")
			rs := res.Services["jobs"]
			require.NotNil(t, rs, text)
			ls := live.Services["jobs"]
			pc := &planCtx{live: live, res: res, rs: rs}

			seen := map[string]bool{}
			for id, fv := range rs.Fields {
				isProcess := strings.HasPrefix(id, "config.process") || id == "config.singleton" || strings.HasPrefix(id, "processes.")
				if !isProcess && !(strings.HasPrefix(id, "domains.") && !defaults) {
					continue
				}
				ops := opsFor(id)
				require.NotNil(t, ops, id)
				want, err := ops.desired(pc, ls, fv)
				require.NoError(t, err, id)
				got, present := ops.live(pc, ls, id, want.Proj)
				require.True(t, present, id)
				require.Equal(t, got.FP, want.FP, "the file does not say what is live for %s:\n%s", id, text)
				seen[id] = true
			}
			for _, id := range []string{
				"config.processName", "config.singleton", "config.processes.worker", "config.processes.clock", "config.processes.metrics",
				"processes.worker.scale.replicas", "processes.worker.scale.autoscaling.enabled", "processes.worker.scale.autoscaling.min",
				"processes.worker.scale.autoscaling.max", "processes.worker.scale.autoscaling.targetCpuPercent",
			} {
				require.True(t, seen[id], "the export dropped %s:\n%s", id, text)
			}
			if !defaults {
				require.True(t, seen["domains.metrics.example.com"])
				require.False(t, seen["processes.clock.scale.replicas"], "a process that was never scaled owns no scale key")
			} else {
				require.True(t, seen["processes.clock.scale.replicas"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Database: export against live state built through the use cases, then plan the file.

type exFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *db.Queries
	ctx     context.Context
	box     *secretbox.Box
	vars    *variables.Service
	svc     *Service
	ws      db.Workspace
	project db.Project
	prod    db.Environment
	staging db.Environment
	server  db.Server
	conn    db.GitConnection
	dest    db.BackupDestination
	network db.SharedNetwork
}

const (
	exPlantedStripe = "sk_live_PlantedStripeValue0123456789"
	exPlantedOrg    = "org-planted-token-9f8e7d6c5b4a"
	exPlantedLegacy = "legacy-token-value-42"
)

func newExFixture(t *testing.T) *exFixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	user, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	project, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: project.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	staging, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: project.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	server, err := q.CreateServer(bg, db.CreateServerParams{WorkspaceID: ws.ID, Name: "fra-1"})
	require.NoError(t, err)
	_, err = pool.Exec(bg, "UPDATE servers SET status = 'online' WHERE id = $1", server.ID)
	require.NoError(t, err)
	conn, err := q.InsertGitConnection(bg, db.InsertGitConnectionParams{
		ID: uuid.New(), WorkspaceID: ws.ID, Provider: "gitea", Name: "gitea-acme", BaseUrl: "https://git.example.com", AuthType: "token",
		Status: "active", WebhookSecretEnc: []byte("x"), Meta: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	dest, err := q.InsertBackupDestination(bg, db.InsertBackupDestinationParams{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "offsite", Kind: "local", LocalDir: "/var/backups",
	})
	require.NoError(t, err)
	network, err := q.InsertSharedNetwork(bg, db.InsertSharedNetworkParams{WorkspaceID: ws.ID, ServerID: server.ID, Name: "backplane", CreatedBy: &user.ID})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	svc := NewService(Deps{Pool: pool, Box: box, PublicURL: "https://nebula.test"})
	return &exFixture{
		t: t, pool: pool, q: q, box: box, vars: variables.NewService(pool, events.NewBus(), box), svc: svc,
		ws: ws, project: project, prod: prod, staging: staging, server: server, conn: conn, dest: dest, network: network,
		ctx: auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: user.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "owner@example.com", Scopes: policy.AllScopes}),
	}
}

func (f *exFixture) insert(env db.Environment, slug, name, kind, source string, cfg services.Config) db.Service {
	f.t.Helper()
	raw, err := json.Marshal(cfg.WithDefaults())
	require.NoError(f.t, err)
	sv, err := f.q.InsertService(context.Background(), db.InsertServiceParams{
		ID: uuid.New(), EnvironmentID: env.ID, Name: name, Slug: slug, Kind: kind, SourceType: source, Config: raw, ServerID: &f.server.ID,
	})
	require.NoError(f.t, err)
	return sv
}

func (f *exFixture) put(id uuid.UUID, in ...variables.VariableInput) {
	f.t.Helper()
	_, err := f.vars.Put(f.ctx, id, in)
	require.NoError(f.t, err)
}

func exVar(key, value string) variables.VariableInput {
	return variables.VariableInput{Key: key, Value: &value}
}

func exSecretVar(key, value string) variables.VariableInput {
	in := exVar(key, value)
	in.IsSecret = new(true)
	return in
}

func (f *exFixture) domain(svc db.Service, host string, mutate func(p *db.InsertDomainParams)) db.Domain {
	f.t.Helper()
	p := db.InsertDomainParams{
		ServiceID: svc.ID, Hostname: host, Kind: "serve", RedirectCode: 308, PreservePath: true, TlsMode: "auto", Options: json.RawMessage(`{}`),
	}
	if mutate != nil {
		mutate(&p)
	}
	d, err := f.q.InsertDomain(context.Background(), p)
	require.NoError(f.t, err)
	return d
}

// production builds the live state of the production environment.
func (f *exFixture) production() {
	bg := context.Background()
	env := f.prod

	web := f.insert(env, "web", "Web", services.KindApp, services.SourceImage, services.Config{
		Image: "ghcr.io/acme/web:1.4.2", Port: 3000, StopGraceSeconds: 45, MemoryMB: 512,
		Healthcheck:      services.Healthcheck{Type: "http", Path: "/healthz", DeadlineSeconds: 90},
		Files:            []services.FileMount{{Path: "/etc/app/app.conf", Mode: "0600", Content: "listen = 3000\n"}},
		PreDeployCommand: []string{"bun", "run", "db:migrate"}, PreDeployTimeoutSeconds: 300,
		Docker: &agentprotocol.DockerOptions{
			CapAdd: []string{"NET_BIND_SERVICE"}, NoNewPrivileges: true, Hostname: "web", DNS: []string{"1.1.1.1"},
			Labels: map[string]string{"team": "shop"}, PidsLimit: 512, User: "1000:1000", Init: true, StopSignal: "SIGQUIT",
			Tmpfs:   []agentprotocol.TmpfsMount{{Path: "/tmp", SizeMB: 64, Mode: "1777"}},
			Ulimits: []agentprotocol.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}},
		},
	})
	worker := f.insert(env, "worker", "Worker", services.KindWorker, services.SourceImage, services.Config{
		Image: "ghcr.io/acme/worker:1.4.2", StartCommand: []string{"bun", "run", "worker"}, MemoryMB: 256, CPUs: 0.5,
	})
	site := f.insert(env, "site", "Site", services.KindApp, services.SourceGit, services.Config{
		GitURL: "https://git.example.com/acme/shop.git", GitBranch: "main", Port: 8080, BuildType: "dockerfile",
		DockerfilePath: "apps/site/Dockerfile", Submodules: "on",
	})
	database := f.insert(env, "db", "Database", services.KindDatabase, services.SourceImage, services.Config{
		Database: &engines.Spec{Engine: "postgres"}, BlockEgress: true, MemoryMB: 1024,
	})
	route := f.insert(env, "printer", "Office printer", services.KindRoute, services.SourceExternal, services.Config{
		Route: &services.RouteConfig{Protocol: "http", Upstreams: []services.RouteUpstream{{Address: "192.168.1.20:631"}, {ServiceID: &web.ID, Port: 3000}}, HealthPath: "/"},
	})

	// Shared layers, written through the shared-variable use case.
	_, err := f.vars.PutShared(f.ctx, variables.LayerProject, f.project.ID, []variables.SharedVariableInput{
		{Key: "APP_NAME", Value: new("Acme Shop")},
		{Key: "ORG_API_TOKEN", Value: new(exPlantedOrg), IsSecret: new(true)},
	})
	require.NoError(f.t, err)
	_, err = f.vars.PutShared(f.ctx, variables.LayerEnvironment, env.ID, []variables.SharedVariableInput{{Key: "REGION", Value: new("eu")}})
	require.NoError(f.t, err)

	// Variables, through the variable use case.
	f.put(database.ID, exVar("POSTGRES_USER", "shop"), exVar("POSTGRES_DB", "shop"), exVar("POSTGRES_PASSWORD", "${{secret(24)}}"), exVar("DB_POOL_SIZE", "10"))
	f.put(web.ID,
		exVar("NODE_ENV", "production"),
		exVar("APP_URL", "https://shop.acme.com"),
		exVar("SITE_NAME", "${{shared.APP_NAME}}"),
		exVar("DB_USER", "${{db.POSTGRES_USER}}"),
		exVar("SESSION_SECRET", "${{secret(48)}}"),
		exVar("MIXED", "prefix-${{secret(8)}}"),
		exSecretVar("STRIPE_SECRET_KEY", exPlantedStripe),
		exVar("LEGACY_TOKEN", exPlantedLegacy),
	)
	build := exVar("BUILD_FLAG", "1")
	build.Scope, build.Comment = new("build"), new("read by the bundler")
	f.put(site.ID, build, exVar("SITE_ENV", "production"))
	f.put(worker.ID, exVar("QUEUE_CONCURRENCY", "8"))

	// Domains.
	f.domain(web, "shop.acme.com", func(p *db.InsertDomainParams) {
		p.TargetPort = 3000
		raw, err := json.Marshal(agentprotocol.RouteOptions{Compress: true, SecurityHeaders: "strict",
			RequestHeaders: []agentprotocol.Header{{Name: "X-Forwarded-Prefix", Value: "/app"}}})
		require.NoError(f.t, err)
		p.Options = raw
	})
	f.domain(web, "www.shop.acme.com", func(p *db.InsertDomainParams) {
		p.Kind, p.RedirectTo = "redirect", new("shop.acme.com")
	})
	f.domain(web, "web-1.apps.example.com", func(p *db.InsertDomainParams) { p.Generated = true; p.TargetPort = 3000 })
	f.domain(web, "shop.acme.com", func(p *db.InsertDomainParams) {
		p.PathPrefix, p.StripPrefix, p.TargetPort = "/api", true, 3000
	})
	f.domain(route, "print.acme.com", func(p *db.InsertDomainParams) {
		raw, err := json.Marshal(agentprotocol.RouteOptions{Access: &agentprotocol.Access{AllowCIDRs: []string{"203.0.113.0/24"}}})
		require.NoError(f.t, err)
		p.Options = raw
	})
	// Domains of a git service too.
	f.domain(site, "docs.acme.com", func(p *db.InsertDomainParams) { p.TargetPort = 8080 })

	// Volumes, links, networks, git link.
	_, err = f.q.InsertVolume(bg, db.InsertVolumeParams{ServiceID: web.ID, Name: "uploads", MountPath: "/app/uploads"})
	require.NoError(f.t, err)
	_, err = f.q.InsertLink(bg, db.InsertLinkParams{EnvironmentID: env.ID, FromServiceID: web.ID, ToServiceID: database.ID})
	require.NoError(f.t, err)
	_, err = f.q.AttachServiceNetwork(bg, db.AttachServiceNetworkParams{ServiceID: web.ID, NetworkID: f.network.ID})
	require.NoError(f.t, err)
	_, err = f.q.UpsertServiceGit(bg, db.UpsertServiceGitParams{
		ServiceID: site.ID, ConnectionID: f.conn.ID, RepoFullName: "acme/shop", AutoDeploy: false, WatchPaths: []string{"apps/site/**", "!**/*.md"},
	})
	require.NoError(f.t, err)

	// Scale, uptime, tasks.
	_, err = f.pool.Exec(bg, "UPDATE services SET replicas = 2 WHERE id = $1", worker.ID)
	require.NoError(f.t, err)
	_, err = f.q.SetServiceAutoscaling(bg, db.SetServiceAutoscalingParams{ID: worker.ID, Autoscaling: json.RawMessage(`{"enabled":true,"min":2,"max":5,"targetCpuPercent":60}`)})
	require.NoError(f.t, err)
	_, err = f.q.UpsertUptimeMonitor(bg, db.UpsertUptimeMonitorParams{
		ServiceID: web.ID, WorkspaceID: f.ws.ID, Enabled: true, Alerts: true, FailureThreshold: 5, RecoveryThreshold: 2, ReminderMinutes: 60,
	})
	require.NoError(f.t, err)
	_, err = f.q.UpsertUptimeMonitor(bg, db.UpsertUptimeMonitorParams{
		ServiceID: database.ID, WorkspaceID: f.ws.ID, Enabled: true, Alerts: false, FailureThreshold: 3, RecoveryThreshold: 2, ReminderMinutes: 0,
	})
	require.NoError(f.t, err)
	_, err = f.q.InsertServiceTask(bg, db.InsertServiceTaskParams{
		ServiceID: web.ID, Name: "digest", Schedule: "0 7 * * 1-5", Timezone: "Europe/Berlin", Command: json.RawMessage(`["bun","run","jobs:digest"]`),
		Enabled: true, TimeoutSeconds: 900, Concurrency: "forbid",
	})
	require.NoError(f.t, err)

	// Backup schedules of the database: one with a key, one created in the dashboard (no key).
	first, err := f.q.InsertBackupSchedule(bg, db.InsertBackupScheduleParams{
		WorkspaceID: f.ws.ID, ServiceID: database.ID, DestinationID: f.dest.ID, Cron: "0 2 * * *", Timezone: "Europe/Berlin", Enabled: true, RetentionCount: 14, RetentionDays: 30,
	})
	require.NoError(f.t, err)
	require.NoError(f.t, f.q.SetBackupScheduleConfigKey(bg, db.SetBackupScheduleConfigKeyParams{ID: first.ID, ConfigKey: new("nightly")}))
	_, err = f.q.InsertBackupSchedule(bg, db.InsertBackupScheduleParams{
		WorkspaceID: f.ws.ID, ServiceID: database.ID, DestinationID: f.dest.ID, Cron: "0 * * * *", Timezone: "UTC", Enabled: false, RetentionCount: 7, RetentionDays: 0,
	})
	require.NoError(f.t, err)
}

// staging builds a smaller second environment with its own values.
func (f *exFixture) stagingEnv() {
	web := f.insert(f.staging, "web", "Web", services.KindApp, services.SourceImage, services.Config{Image: "ghcr.io/acme/web:next", Port: 3000})
	f.put(web.ID, exVar("NODE_ENV", "staging"), exVar("APP_URL", "https://shop.staging.acme.com"))
	f.domain(web, "shop.staging.acme.com", func(p *db.InsertDomainParams) { p.TargetPort = 3000 })
	_, err := f.vars.PutShared(f.ctx, variables.LayerEnvironment, f.staging.ID, []variables.SharedVariableInput{{Key: "REGION", Value: new("us")}})
	require.NoError(f.t, err)
}

// exPlansToZero fails unless planning the file against the environment changes nothing.
func (f *exFixture) plansToZero(file, env string) Plan {
	f.t.Helper()
	pl, err := f.svc.Plan(f.ctx, f.project.ID, PlanRequest{Environment: env, Input: Input{File: file}})
	require.NoError(f.t, err)
	require.False(f.t, pl.Diagnostics.HasErrors(), "the exported file does not plan cleanly:\n%s\n%s", pl.Diagnostics.RenderText(""), file)
	flagged := map[string]bool{} // variables the export declared secret although they are plain live: the documented exception
	for _, d := range pl.Diagnostics {
		if d.Severity == projectconfig.SeverityWarning {
			require.Equal(f.t, "secret_flag_differs", d.Code, "unexpected warning: %s\n%s", d.Render(""), file)
			flagged[d.Message] = true
		}
	}
	// The first apply marks a credential-looking plain variable secret, "the one documented exception to plans
	// to zero". Such a service plans exactly the staged "mark as secret" change of each flagged variable and nothing else.
	updated, tolerated := 0, 0
	for _, s := range pl.Services {
		if len(s.Changes) == 0 {
			require.Equal(f.t, ActionUnchanged, s.Action, "service %s", s.Slug)
			continue
		}
		for _, c := range s.Changes {
			key, ok := strings.CutSuffix(strings.TrimPrefix(c.Field, "variables."), "#secret")
			require.True(f.t, ok && strings.HasPrefix(c.Field, "variables."), "service %s: %+v\n%s", s.Slug, s.Changes, file)
			require.Equal(f.t, AppliesStaged, c.Applies)
			require.Equal(f.t, ReasonAdopt, c.Reason)
			hit := false
			for msg := range flagged {
				hit = hit || strings.Contains(msg, key)
			}
			require.True(f.t, hit, "variable %s changes but no secret_flag_differs warning names it", key)
			tolerated++
		}
		require.Equal(f.t, ActionUpdate, s.Action, "service %s", s.Slug)
		updated++
	}
	require.Zero(f.t, pl.Summary.Create)
	require.Equal(f.t, updated, pl.Summary.Update)
	require.Equal(f.t, tolerated, pl.Summary.Staged)
	require.Zero(f.t, pl.Summary.Immediate)
	require.Empty(f.t, pl.Shared.Project, "%+v", pl.Shared.Project)
	require.Empty(f.t, pl.Shared.Environment, "%+v", pl.Shared.Environment)
	return pl
}

// secretValues lists the stored values of every secret variable of the project, opened with the box.
func (f *exFixture) secretValues() []string {
	f.t.Helper()
	bg := context.Background()
	var out []string
	envs, err := f.q.ListEnvironments(bg, f.project.ID)
	require.NoError(f.t, err)
	for _, e := range envs {
		rows, err := f.q.ListEnvironmentServicesAll(bg, e.ID)
		require.NoError(f.t, err)
		for _, r := range rows {
			vars, err := f.q.ListServiceVariables(bg, r.ID)
			require.NoError(f.t, err)
			for _, v := range vars {
				if !v.IsSecret {
					continue
				}
				val, err := f.box.OpenString(secretbox.ServiceVariablesValue, secretbox.RowID(v.ServiceID.String(), v.Key), v.ValueEnc)
				require.NoError(f.t, err)
				out = append(out, val)
			}
		}
	}
	return append(out, exPlantedStripe, exPlantedOrg, exPlantedLegacy)
}

func TestExportPlansToZero(t *testing.T) {
	f := newExFixture(t)
	f.production()

	res, err := f.svc.Export(f.ctx, f.project.ID, "production", "", false, false)
	require.NoError(t, err)
	file := res.File

	// Shape.
	require.True(t, strings.HasPrefix(file, "#:schema https://nebula.test/schema/nebula.toml.json\nschema = 1\nproject = \"shop\"\nserver = \"fra-1\"\n"), file)
	for _, want := range []string{
		"[services.web]", "[services.worker]", "[services.site]", "[services.db.database]", "[services.printer.route]",
		"www_redirect = true", `[services.web.domains."shop.acme.com/api"]`, "[services.db.backups.nightly]", "[services.db.backups.schedule-1]",
		"[services.web.tasks.digest]", "depends_on = [\"db\"]", "networks = [\"backplane\"]", "[services.web.volumes]", `"/etc/app/app.conf"`,
		"SESSION_SECRET = \"${{secret(48)}}\"", "[environments.production.variables]",
	} {
		require.Contains(t, file, want)
	}
	// "\nPOSTGRES_" is a variable definition of the database's own (platform-set) variables; the reference ${{db.POSTGRES_USER}} is a legitimate value.
	for _, not := range []string{"web-1.apps.example.com", "www.shop.acme.com\"]", f.server.ID.String(), f.conn.ID.String(), "\nPOSTGRES_"} {
		require.NotContains(t, file, not)
	}

	// No secret value anywhere.
	blob, err := json.Marshal(res.Warnings)
	require.NoError(t, err)
	for _, secret := range f.secretValues() {
		if len(secret) < 8 {
			continue
		}
		require.NotContains(t, file, secret)
		require.NotContains(t, string(blob), secret)
	}

	// The only finding is the credential-looking plain variable, by name.
	require.NotEmpty(t, res.Warnings)
	for _, w := range res.Warnings {
		require.Equal(t, "secret_flag_differs", w.Code, w.Render(""))
		require.Contains(t, w.Message, "LEGACY_TOKEN")
	}

	// plan(export(live)) == nothing.
	pl := f.plansToZero(file, "production")
	require.Equal(t, 4, pl.Summary.Unchanged, "all but web, whose credential-looking variable the first apply marks secret")
	require.Equal(t, 1, pl.Summary.Update)

	// Deterministic.
	again, err := f.svc.Export(f.ctx, f.project.ID, "production", "", false, false)
	require.NoError(t, err)
	require.Equal(t, file, again.File)
	require.Equal(t, res.Warnings, again.Warnings)

	// The environment may be named by id, and the empty name is refused while there are two.
	byID, err := f.svc.Export(f.ctx, f.project.ID, f.prod.ID.String(), "", false, false)
	require.NoError(t, err)
	require.Equal(t, file, byID.File)
	_, err = f.svc.Export(f.ctx, f.project.ID, "", "", false, false)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, CodeEnvironmentRequired, ae.Code)
}

func TestExportWithDefaultsPlansToZero(t *testing.T) {
	f := newExFixture(t)
	f.production()
	plain, err := f.svc.Export(f.ctx, f.project.ID, "production", "", false, false)
	require.NoError(t, err)
	res, err := f.svc.Export(f.ctx, f.project.ID, "production", "", false, true)
	require.NoError(t, err)
	require.Greater(t, len(res.File), len(plain.File), "defaults are written when asked for")
	require.Contains(t, res.File, "stop_grace_seconds = 30")
	f.plansToZero(res.File, "production")
}

func TestExportAllFactorsEnvironments(t *testing.T) {
	f := newExFixture(t)
	f.production()
	f.stagingEnv()

	res, err := f.svc.Export(f.ctx, f.project.ID, "", "", true, false)
	require.NoError(t, err)
	file := res.File

	// production is the base; staging only holds what differs; services of one environment say so.
	require.Contains(t, file, "[environments.staging")
	require.NotContains(t, file, "[environments.production.services")
	require.Contains(t, file, `only_in = ["production"]`)

	parsed, idx, diags := projectconfig.Parse([]byte(file), nil)
	require.NotNil(t, parsed)
	require.False(t, diags.HasErrors(), diags.RenderText(""))
	prodView, _ := parsed.Resolve("production", idx)
	stagingView, _ := parsed.Resolve("staging", idx)
	require.Equal(t, []string{"db", "printer", "site", "web", "worker"}, prodView.SortedSlugs())
	require.Equal(t, []string{"web"}, stagingView.SortedSlugs())
	require.Equal(t, "ghcr.io/acme/web:next", exDeref(stagingView.Services["web"].Def.Image))
	require.Equal(t, "ghcr.io/acme/web:1.4.2", exDeref(prodView.Services["web"].Def.Image))
	require.Equal(t, "staging", stagingView.Services["web"].Def.Variables["NODE_ENV"].Value)
	require.Equal(t, "us", stagingView.EnvShared["REGION"].Value)
	require.Equal(t, "eu", prodView.EnvShared["REGION"].Value)
	require.Equal(t, "Acme Shop", stagingView.Shared["APP_NAME"].Value)
	require.Empty(t, stagingView.Services["web"].Def.Files, "the staging service has no files: the base ones are unset")

	// Each environment plans to zero against its own live state.
	f.plansToZero(file, "production")
	f.plansToZero(file, "staging")

	// base can be named.
	named, err := f.svc.Export(f.ctx, f.project.ID, "", "staging", true, false)
	require.NoError(t, err)
	require.NotEqual(t, file, named.File)
	f.plansToZero(named.File, "production")
	f.plansToZero(named.File, "staging")
	_, err = f.svc.Export(f.ctx, f.project.ID, "", "nope", true, false)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, CodeUnknownEnvironment, ae.Code)
}

func TestExportNeedsTheReadScope(t *testing.T) {
	f := newExFixture(t)
	f.production()
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, WorkspaceID: f.ws.ID, Role: auth.RoleOwner, Scopes: []string{}})
	_, err := f.svc.Export(ctx, f.project.ID, "production", "", false, false)
	require.Error(t, err)
}

// A service with processes exports to a file that plans to nothing, with and without defaults: the
// processes, their scale rows and the process of each domain all come back as they are live.
func TestExportProcessesPlansToZero(t *testing.T) {
	f := newExFixture(t)
	bg := context.Background()
	jobs := f.insert(f.prod, "jobs", "Jobs", services.KindApp, services.SourceImage, services.Config{
		Image: "ghcr.io/acme/jobs:2", Port: 8080, ProcessName: "app", Singleton: true, StopGraceSeconds: 45,
		Processes: []services.ProcessConfig{
			{Name: "worker", StartCommand: []string{"bundle", "exec", "sidekiq"}, StopGraceSeconds: 120, MemoryMB: 512, CPUs: 1},
			{Name: "mailer", StartCommand: []string{"bin/mailer"}},
			{Name: "clock", StartCommand: []string{"bundle", "exec", "clockwork", "clock.rb"}, Singleton: true},
			{
				Name: "metrics", StartCommand: []string{"bin/metrics"}, Port: 9100,
				Healthcheck: services.Healthcheck{Type: "http", Path: "/up", DeadlineSeconds: 60},
			},
		},
	})
	// Per-process scale lives in its own table: worker runs two replicas within custom bounds, mailer autoscales.
	_, err := f.q.SetProcessAutoscaling(bg, db.SetProcessAutoscalingParams{
		ServiceID: jobs.ID, Process: "worker", Autoscaling: json.RawMessage(`{"enabled":false,"min":2,"max":6,"targetCpuPercent":65}`),
	})
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "UPDATE service_process_scale SET replicas = 2 WHERE service_id = $1 AND process = 'worker'", jobs.ID)
	require.NoError(t, err)
	_, err = f.q.SetProcessAutoscaling(bg, db.SetProcessAutoscalingParams{
		ServiceID: jobs.ID, Process: "mailer", Autoscaling: json.RawMessage(`{"enabled":true,"min":1,"max":4,"targetCpuPercent":70}`),
	})
	require.NoError(t, err)
	f.domain(jobs, "app.acme.com", func(p *db.InsertDomainParams) { p.TargetPort = 8080 })
	f.domain(jobs, "metrics.acme.com", func(p *db.InsertDomainParams) { p.TargetPort, p.Component = 9100, "metrics" })

	plain, err := f.svc.Export(f.ctx, f.project.ID, "production", "", false, false)
	require.NoError(t, err)
	for _, want := range []string{
		"process_name = \"app\"", "singleton = true",
		"[services.jobs.processes.worker]", "[services.jobs.processes.mailer]", "[services.jobs.processes.clock]", "[services.jobs.processes.metrics]",
		`process = "metrics"`,
	} {
		require.Contains(t, plain.File, want)
	}
	pl := f.plansToZero(plain.File, "production")
	require.Equal(t, 1, pl.Summary.Unchanged)

	all, err := f.svc.Export(f.ctx, f.project.ID, "production", "", false, true)
	require.NoError(t, err)
	require.Greater(t, len(all.File), len(plain.File))
	pl = f.plansToZero(all.File, "production")
	require.Equal(t, 1, pl.Summary.Unchanged)
}
