package projectconfig

import (
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// FieldSpec describes one ownable atom of a service: the field id used
// in pending changes, ownership and plan output, and the file key that owns it.
// A "*" in ID or Key stands for one dynamic segment (a variable name, a host and
// path, a task name, ...).
type FieldSpec struct {
	// ID is the field id pattern, for example "config.healthcheck.path" or "domains.*".
	ID string
	// Key is the key path relative to the service in the file, for example
	// "health_check.path" or "domains.*.cloudflare".
	Key string
	// GoField names the platform field the value lands in (services.Config
	// unless a package is named): informational, for setters and docs.
	GoField string
	// Applies is "staged" or "immediate".
	Applies string
	// ValueType is the Go type of FieldValue.Value.
	ValueType string
	// Collection marks ids that stand for one member of a keyed collection.
	Collection bool
	// Kinds are the service kinds the field exists for; empty means every kind.
	Kinds []string
	// Default documents the value used when the key is absent on create.
	Default string
	// Unit is the unit of the value (seconds, mb, ...), when it has one.
	Unit string
	// Doc is a one-line description.
	Doc string
}

var (
	rtKinds = []string{KindImage, KindGit}
	dbKinds = []string{KindDatabase}
)

func fs(id, key, goField, applies, typ string, kinds []string) FieldSpec {
	return FieldSpec{ID: id, Key: key, GoField: goField, Applies: applies, ValueType: typ, Kinds: kinds}
}

type dockerField struct{ json, key, typ string }

var dockerFields = []dockerField{
	{"capAdd", "cap_add", "[]string"}, {"capDrop", "cap_drop", "[]string"},
	{"noNewPrivileges", "no_new_privileges", "bool"}, {"readOnlyRootfs", "read_only_rootfs", "bool"},
	{"pidsLimit", "pids_limit", "int64"}, {"hostname", "hostname", "string"}, {"dns", "dns", "[]string"},
	{"sysctls", "sysctls", "map[string]string"}, {"hostSysctls", "host_sysctls", "map[string]int64"},
	{"user", "user", "string"}, {"groupAdd", "group_add", "[]string"},
	{"ulimits", "ulimits", "[]agentprotocol.Ulimit"}, {"shmSizeMb", "shm_size_mb", "int"},
	{"extraHosts", "extra_hosts", "[]string"}, {"labels", "labels", "map[string]string"},
	{"init", "init", "bool"}, {"stopSignal", "stop_signal", "string"},
	{"tmpfs", "tmpfs", "[]agentprotocol.TmpfsMount"}, {"entrypoint", "entrypoint", "[]string"},
	{"devices", "devices", "[]agentprotocol.DeviceMapping"}, {"gpus", "gpus", "agentprotocol.GPURequest"},
	{"runtime", "runtime", "string"}, {"privileged", "privileged", "bool"}, {"pidHost", "pid_host", "bool"},
	{"seccompUnconfined", "seccomp_unconfined", "bool"}, {"apparmorUnconfined", "apparmor_unconfined", "bool"},
}

// DockerFieldUnrestricted lists the config.docker.<jsonName> ids that need an
// unrestricted server (cap_add entries NET_ADMIN/SYS_ADMIN are checked by value).
func DockerFieldUnrestricted(jsonName string) bool {
	switch jsonName {
	case "devices", "gpus", "runtime", "privileged", "pidHost", "seccompUnconfined", "apparmorUnconfined":
		return true
	}
	return false
}

// Fields is the field table: every ownable atom, its file key, platform field
// and apply kind. It is shared and read-only; callers must not modify it.
var Fields = buildCatalog()

func buildCatalog() []FieldSpec {
	var out []FieldSpec
	add := func(f FieldSpec) { out = append(out, f) }
	add(fs("name", "name", "db.Service.Name", AppliesImmediate, "string", nil))
	add(fs("server", "server", "db.Service.ServerID", AppliesStaged, "string", nil))
	add(fs("generate_domain", "generate_domain", "StageDomains.Generate", AppliesStaged, "bool", rtKinds))
	l := fs("links.*", "depends_on", "service_links", AppliesStaged, "string", nil)
	l.Collection = true
	add(l)
	n := fs("networks.*", "networks", "shared network attachment", AppliesStaged, "string", nil)
	n.Collection = true
	add(n)

	add(fs("config.image", "image", "Image", AppliesStaged, "string", []string{KindImage}))
	add(fs("config.port", "port", "Port", AppliesStaged, "int", rtKinds))
	add(fs("config.startCommand", "command", "StartCommand", AppliesStaged, "[]string", rtKinds))
	add(fs("config.oneshot", "one_shot", "OneShot", AppliesStaged, "bool", rtKinds))
	add(fs("config.processName", "process_name", "ProcessName", AppliesStaged, "string", rtKinds))
	add(fs("config.singleton", "singleton", "Singleton", AppliesStaged, "bool", rtKinds))
	add(fs("config.stopGraceSeconds", "stop_grace_seconds", "StopGraceSeconds", AppliesStaged, "int", append(append([]string(nil), rtKinds...), KindDatabase)))
	add(fs("config.memoryMb", "memory_mb", "MemoryMB", AppliesStaged, "int", append(append([]string(nil), rtKinds...), KindDatabase)))
	add(fs("config.cpus", "cpus", "CPUs", AppliesStaged, "float64", append(append([]string(nil), rtKinds...), KindDatabase)))
	add(fs("config.imageRetention", "image_retention", "ImageRetention", AppliesStaged, "int", append(append([]string(nil), rtKinds...), KindDatabase)))
	add(fs("config.blockEgress", "block_egress", "BlockEgress", AppliesStaged, "bool", append(append([]string(nil), rtKinds...), KindDatabase)))

	for _, h := range []struct{ id, key, typ string }{
		{"type", "type", "string"}, {"path", "path", "string"}, {"port", "port", "int"},
		{"intervalSeconds", "interval_seconds", "int"}, {"timeoutSeconds", "timeout_seconds", "int"},
		{"startPeriodSeconds", "start_period_seconds", "int"}, {"deadlineSeconds", "deadline_seconds", "int"},
		{"successThreshold", "success_threshold", "int"}, {"command", "command", "[]string"},
	} {
		add(fs("config.healthcheck."+h.id, "health_check."+h.key, "Healthcheck."+h.id, AppliesStaged, h.typ, rtKinds))
	}
	for _, p := range []struct{ id, key string }{{"preDeploy", "pre_deploy"}, {"postDeploy", "post_deploy"}} {
		add(fs("config."+p.id+"Command", p.key+".command", strings.ToUpper(p.id[:1])+p.id[1:]+"Command", AppliesStaged, "[]string", rtKinds))
		add(fs("config."+p.id+"TimeoutSeconds", p.key+".timeout_seconds", strings.ToUpper(p.id[:1])+p.id[1:]+"TimeoutSeconds", AppliesStaged, "int", rtKinds))
	}

	g := []string{KindGit}
	add(fs("config.gitUrl", "git.url", "GitURL", AppliesStaged, "string", g))
	add(fs("config.gitBranch", "git.branch", "GitBranch", AppliesStaged, "string", g))
	add(fs("config.submodules", "git.submodules", "Submodules", AppliesStaged, "string", g))
	add(fs("git.link", "git.connection", "gitproviders.LinkInput{ConnectionID, Repo}", AppliesImmediate, "GitLink", g))
	add(fs("git.auto_deploy", "git.auto_deploy", "service_git.auto_deploy", AppliesImmediate, "bool", g))
	add(fs("git.watch_paths", "git.watch_paths", "service_git.watch_paths", AppliesImmediate, "[]string", g))
	add(fs("config.buildType", "build.type", "BuildType", AppliesStaged, "string", g))
	add(fs("config.dockerfilePath", "build.dockerfile", "DockerfilePath", AppliesStaged, "string", g))
	add(fs("config.buildContext", "build.context", "BuildContext", AppliesStaged, "string", g))
	add(fs("config.buildLocation", "build.location", "BuildLocation", AppliesStaged, "string", g))
	add(fs("config.buildServerId", "build.server", "BuildServerID", AppliesStaged, "string (server name)", g))
	add(fs("config.registryId", "build.registry", "RegistryID", AppliesStaged, "string (registry name)", g))

	for _, d := range []struct{ id, key, typ string }{
		{"engine", "engine", "string"}, {"version", "version", "string"}, {"flavor", "flavor", "string"},
		{"databaseName", "name", "string"}, {"username", "username", "string"},
	} {
		add(fs("config.database."+d.id, "database."+d.key, "Database."+strings.ToUpper(d.id[:1])+d.id[1:], AppliesStaged, d.typ, dbKinds))
	}

	rk := []string{KindRoute}
	for _, r := range []struct{ id, key, typ string }{
		{"protocol", "protocol", "string"}, {"upstreams", "upstreams", "[]UpstreamValue"}, {"scheme", "scheme", "string"},
		{"insecureSkipVerify", "insecure_skip_verify", "bool"}, {"serverName", "server_name", "string"},
		{"passHost", "pass_host", "bool"}, {"healthPath", "health_path", "string"}, {"verifySkip", "verify_skip", "bool"},
		{"listenPort", "listen_port", "int"}, {"allowCidrs", "allow_cidrs", "[]string"}, {"denyCidrs", "deny_cidrs", "[]string"},
		{"openToInternet", "open_to_internet", "bool"}, {"proxyProtocol", "proxy_protocol", "int"},
	} {
		add(fs("config.route."+r.id, "route."+r.key, "Route."+strings.ToUpper(r.id[:1])+r.id[1:], AppliesStaged, r.typ, rk))
	}
	sk := []string{KindStack}
	add(fs("config.stack.compose", "stack.compose", "Stack.Compose", AppliesStaged, "string", sk))
	add(fs("config.stack.waitSeconds", "stack.wait_seconds", "Stack.WaitSeconds", AppliesStaged, "int", sk))
	sr := fs("config.stack.resources.*", "stack.resources.*", "Stack.Resources[component]", AppliesStaged, "StackResourceValue", sk)
	sr.Collection = true
	add(sr)

	add(fs("config.files", "files", "Files", AppliesStaged, "[]FileMount", rtKinds))
	for _, df := range dockerFields {
		add(fs("config.docker."+df.json, "docker."+df.key, "Docker."+strings.ToUpper(df.json[:1])+df.json[1:], AppliesStaged, df.typ, append(append([]string(nil), rtKinds...), KindDatabase)))
	}

	v := fs("variables.*", "variables.*", "variables.VariableInput", AppliesStaged, "Variable", nil)
	v.Collection = true
	add(v)
	vs := fs("variables.*#secret", "secrets.keys", "variables secret flag", AppliesStaged, "SecretDecl", nil)
	vs.Collection = true
	add(vs)
	d := fs("domains.*", "domains.*", "domains.RuleInput", AppliesStaged, "DomainRule", []string{KindImage, KindGit, KindRoute, KindStack})
	d.Collection = true
	add(d)
	cf := fs("domains.*.cloudflare", "domains.*.cloudflare", "PUT /domains/{id}/dns", AppliesImmediate, "CloudflareRule", []string{KindImage, KindGit, KindRoute, KindStack})
	cf.Collection = true
	add(cf)
	vo := fs("volumes.*", "volumes.*", "services.AddVolume", AppliesStaged, "Volume", rtKinds)
	vo.Collection = true
	add(vo)

	add(fs("scale.replicas", "scale.replicas", "scaling.SetInput.Replicas", AppliesImmediate, "int", rtKinds))
	add(fs("scale.autoscaling.enabled", "scale.autoscaling.enabled", "scaling.AutoscalingPatch.Enabled", AppliesImmediate, "bool", rtKinds))
	add(fs("scale.autoscaling.min", "scale.autoscaling.min", "scaling.AutoscalingPatch.Min", AppliesImmediate, "int", rtKinds))
	add(fs("scale.autoscaling.max", "scale.autoscaling.max", "scaling.AutoscalingPatch.Max", AppliesImmediate, "int", rtKinds))
	add(fs("scale.autoscaling.targetCpuPercent", "scale.autoscaling.target_cpu_percent", "scaling.AutoscalingPatch.TargetCPUPercent", AppliesImmediate, "int", rtKinds))
	pr := fs("config.processes.*", "processes.*", "Processes[name]", AppliesStaged, "ProcessValue", rtKinds)
	pr.Collection = true
	add(pr)
	for _, ps := range []struct{ id, goField, typ string }{
		{"replicas", "scaling.SetInput.Replicas", "int"},
		{"autoscaling.enabled", "scaling.AutoscalingPatch.Enabled", "bool"},
		{"autoscaling.min", "scaling.AutoscalingPatch.Min", "int"},
		{"autoscaling.max", "scaling.AutoscalingPatch.Max", "int"},
		{"autoscaling.targetCpuPercent", "scaling.AutoscalingPatch.TargetCPUPercent", "int"},
	} {
		key := ps.id
		if ps.id == "autoscaling.targetCpuPercent" {
			key = "autoscaling.target_cpu_percent"
		}
		f := fs("processes.*.scale."+ps.id, "processes.*.scale."+key, ps.goField+" (per process)", AppliesImmediate, ps.typ, rtKinds)
		f.Collection = true // one member per process
		add(f)
	}
	for _, u := range []struct{ id, key, typ string }{
		{"enabled", "enabled", "bool"}, {"alerts", "alerts", "bool"}, {"failureThreshold", "failure_threshold", "int"},
		{"recoveryThreshold", "recovery_threshold", "int"}, {"reminderMinutes", "reminder_minutes", "int"},
	} {
		add(fs("uptime."+u.id, "uptime."+u.key, "uptime.MonitorView."+strings.ToUpper(u.id[:1])+u.id[1:], AppliesImmediate, u.typ, nil))
	}
	t := fs("tasks.*", "tasks.*", "tasks.TaskInput", AppliesImmediate, "TaskValue", rtKinds)
	t.Collection = true
	add(t)
	b := fs("backups.*", "backups.*", "backups.ScheduleInput", AppliesImmediate, "BackupValue", dbKinds)
	b.Collection = true
	add(b)
	for i := range out {
		def, ok := fieldDefaults[out[i].ID]
		if !ok {
			// A process' scale keys default like the service's own.
			def, ok = fieldDefaults[strings.TrimPrefix(out[i].ID, "processes.*.")]
		}
		if ok {
			out[i].Default = def
		}
		out[i].Unit = fieldUnit(out[i].ID)
		out[i].Doc = fieldDoc(out[i])
	}
	return out
}

// fieldDefaults documents the value used when a key is absent on create.
var fieldDefaults = map[string]string{
	"name": "the service slug", "generate_domain": "false",
	"config.port": "0", "config.oneshot": "false", "config.stopGraceSeconds": "30", "config.memoryMb": "0",
	"config.cpus": "0", "config.imageRetention": "5", "config.blockEgress": "false",
	"config.processName": "web", "config.singleton": "false",
	"config.healthcheck.type": "http when the service has a port, else none", "config.healthcheck.path": "/",
	"config.healthcheck.port": "the service port", "config.healthcheck.intervalSeconds": "2",
	"config.healthcheck.timeoutSeconds": "3", "config.healthcheck.startPeriodSeconds": "0",
	"config.healthcheck.deadlineSeconds": "120", "config.healthcheck.successThreshold": "2",
	"config.preDeployTimeoutSeconds": "600", "config.postDeployTimeoutSeconds": "600",
	"config.submodules": "auto", "git.auto_deploy": "true", "git.watch_paths": "[]",
	"config.buildType": "auto", "config.dockerfilePath": "Dockerfile", "config.buildContext": ".", "config.buildLocation": "inherit",
	"config.route.scheme": "http", "config.route.passHost": "true", "config.route.insecureSkipVerify": "false",
	"config.route.verifySkip": "false", "config.route.openToInternet": "false", "config.route.proxyProtocol": "0",
	"config.stack.waitSeconds": "300",
	"scale.replicas":           "1", "scale.autoscaling.enabled": "false", "scale.autoscaling.min": "1", "scale.autoscaling.max": "3",
	"scale.autoscaling.targetCpuPercent": "70",
	"uptime.enabled":                     "true", "uptime.alerts": "true", "uptime.failureThreshold": "3", "uptime.recoveryThreshold": "2", "uptime.reminderMinutes": "0",
}

func fieldUnit(id string) string {
	switch {
	case strings.HasSuffix(id, "Seconds") || strings.HasSuffix(id, "seconds"):
		return "seconds"
	case strings.HasSuffix(id, "Mb"):
		return "mb"
	case strings.HasSuffix(id, "Minutes"):
		return "minutes"
	case strings.HasSuffix(id, "Percent"):
		return "percent"
	}
	return ""
}

func fieldDoc(f FieldSpec) string {
	kinds := "every service kind"
	if len(f.Kinds) > 0 {
		kinds = strings.Join(f.Kinds, ", ") + " services"
	}
	return "`" + f.Key + "` (" + f.Applies + "); " + kinds
}

// LookupField finds the spec of a concrete field id (for example
// "variables.NODE_ENV", "domains.shop.acme.com/api.cloudflare" or
// "config.port").
func LookupField(id string) (FieldSpec, bool) {
	for _, f := range Fields {
		if f.ID == id {
			return f, true
		}
	}
	// Dynamic ids: prefer the pattern with the longest suffix so that
	// "domains.*.cloudflare" wins over "domains.*" and "#secret" over "variables.*".
	var best *FieldSpec
	bestSuffix := -1
	for i := range Fields {
		f := &Fields[i]
		pre, suf, ok := strings.Cut(f.ID, "*")
		if !ok {
			continue
		}
		if len(id) > len(pre)+len(suf) && strings.HasPrefix(id, pre) && strings.HasSuffix(id, suf) && len(suf) > bestSuffix {
			best, bestSuffix = f, len(suf)
		}
	}
	if best != nil {
		return *best, true
	}
	return FieldSpec{}, false
}

// assembleFields fills rs.Fields from the merged definition.
func (c *svcCtx) assembleFields() {
	d, rs := c.def, c.rs
	idx := c.v.f.meta.idx
	add := func(id string, key []string, val any, applies string) {
		p := c.abs(key...)
		rs.Fields[id] = FieldValue{ID: id, Path: p, Value: val, Pos: idx.ValuePos(p), Applies: applies}
	}
	st, im := AppliesStaged, AppliesImmediate

	if d.Name != nil {
		add("name", rp("name"), *d.Name, im)
	}
	if rs.ServerName != "" {
		key := rp("server")
		if d.Server == nil {
			p := "server"
			if blk := c.v.env; blk != "" {
				if b := c.v.f.EnvironmentBlocks[blk]; b != nil && b.Server != nil {
					p = PathString("environments", blk, "server")
				}
			}
			rs.Fields["server"] = FieldValue{ID: "server", Path: p, Value: rs.ServerName, Pos: idx.ValuePos(p), Applies: st}
		} else {
			add("server", key, rs.ServerName, st)
		}
	}
	if d.GenerateDomain != nil {
		add("generate_domain", rp("generate_domain"), *d.GenerateDomain, st)
	}
	for i, sl := range d.DependsOn {
		add("links."+sl, rp("depends_on", "["+itoa(i)+"]"), sl, st)
	}
	for i, nn := range d.Networks {
		add("networks."+nn, rp("networks", "["+itoa(i)+"]"), nn, st)
	}
	if d.Image != nil {
		add("config.image", rp("image"), *d.Image, st)
	}
	if d.Port != nil {
		add("config.port", rp("port"), *d.Port, st)
	}
	if d.Command != nil {
		add("config.startCommand", rp("command"), append([]string{}, d.Command...), st)
	}
	if d.OneShot != nil {
		add("config.oneshot", rp("one_shot"), *d.OneShot, st)
	}
	if d.ProcessName != nil {
		add("config.processName", rp("process_name"), *d.ProcessName, st)
	}
	if d.Singleton != nil {
		add("config.singleton", rp("singleton"), *d.Singleton, st)
	}
	if d.StopGraceSeconds != nil {
		add("config.stopGraceSeconds", rp("stop_grace_seconds"), *d.StopGraceSeconds, st)
	}
	if d.MemoryMB != nil {
		add("config.memoryMb", rp("memory_mb"), *d.MemoryMB, st)
	}
	if d.CPUs != nil {
		add("config.cpus", rp("cpus"), *d.CPUs, st)
	}
	if d.ImageRetention != nil {
		add("config.imageRetention", rp("image_retention"), *d.ImageRetention, st)
	}
	if d.BlockEgress != nil {
		add("config.blockEgress", rp("block_egress"), *d.BlockEgress, st)
	}
	if h := d.HealthCheck; h != nil {
		addS := func(id, key string, p *string) {
			if p != nil {
				add("config.healthcheck."+id, rp("health_check", key), *p, st)
			}
		}
		addI := func(id, key string, p *int) {
			if p != nil {
				add("config.healthcheck."+id, rp("health_check", key), *p, st)
			}
		}
		addS("type", "type", h.Type)
		addS("path", "path", h.Path)
		addI("port", "port", h.Port)
		addI("intervalSeconds", "interval_seconds", h.IntervalSeconds)
		addI("timeoutSeconds", "timeout_seconds", h.TimeoutSeconds)
		addI("startPeriodSeconds", "start_period_seconds", h.StartPeriodSeconds)
		addI("deadlineSeconds", "deadline_seconds", h.DeadlineSeconds)
		addI("successThreshold", "success_threshold", h.SuccessThreshold)
		if h.Command != nil {
			add("config.healthcheck.command", rp("health_check", "command"), append([]string{}, h.Command...), st)
		}
	}
	for _, hk := range []struct {
		key, id string
		h       *Hook
	}{{"pre_deploy", "preDeploy", d.PreDeploy}, {"post_deploy", "postDeploy", d.PostDeploy}} {
		if hk.h == nil {
			continue
		}
		if hk.h.Command != nil {
			add("config."+hk.id+"Command", rp(hk.key, "command"), append([]string{}, hk.h.Command...), st)
		}
		if hk.h.TimeoutSeconds != nil {
			add("config."+hk.id+"TimeoutSeconds", rp(hk.key, "timeout_seconds"), *hk.h.TimeoutSeconds, st)
		}
	}
	c.assembleGit()
	c.assembleBuild()
	c.assembleSource()
	c.assembleDocker()
	c.assembleCollections()
}

func (c *svcCtx) addField(id string, key []string, val any, applies string) {
	p := c.abs(key...)
	c.rs.Fields[id] = FieldValue{ID: id, Path: p, Value: val, Pos: c.v.f.meta.idx.ValuePos(p), Applies: applies}
}

func (c *svcCtx) assembleGit() {
	g := c.def.Git
	if g == nil {
		return
	}
	st, im := AppliesStaged, AppliesImmediate
	if g.URL != nil {
		c.addField("config.gitUrl", rp("git", "url"), *g.URL, st)
	}
	if g.Branch != nil {
		c.addField("config.gitBranch", rp("git", "branch"), *g.Branch, st)
	}
	if g.Submodules != nil {
		c.addField("config.submodules", rp("git", "submodules"), *g.Submodules, st)
	}
	if g.Connection != nil && g.Repo != nil {
		c.addField("git.link", rp("git", "connection"), GitLink{ConnectionName: *g.Connection, Repo: *g.Repo}, im)
	}
	if g.AutoDeploy != nil {
		c.addField("git.auto_deploy", rp("git", "auto_deploy"), *g.AutoDeploy, im)
	}
	if g.WatchPaths != nil {
		c.addField("git.watch_paths", rp("git", "watch_paths"), append([]string{}, g.WatchPaths...), im)
	}
}

func (c *svcCtx) assembleBuild() {
	b := c.def.Build
	if b == nil {
		return
	}
	for _, e := range []struct {
		id, key string
		v       *string
	}{
		{"config.buildType", "type", b.Type}, {"config.dockerfilePath", "dockerfile", b.Dockerfile},
		{"config.buildContext", "context", b.Context}, {"config.buildLocation", "location", b.Location},
		{"config.buildServerId", "server", b.Server}, {"config.registryId", "registry", b.Registry},
	} {
		if e.v != nil {
			c.addField(e.id, rp("build", e.key), *e.v, AppliesStaged)
		}
	}
}

func (c *svcCtx) assembleSource() {
	st := AppliesStaged
	if db := c.def.Database; db != nil {
		for _, e := range []struct {
			id, key string
			v       *string
		}{
			{"config.database.engine", "engine", db.Engine}, {"config.database.version", "version", db.Version},
			{"config.database.flavor", "flavor", db.Flavor}, {"config.database.databaseName", "name", db.Name},
			{"config.database.username", "username", db.Username},
		} {
			if e.v != nil {
				c.addField(e.id, rp("database", e.key), *e.v, st)
			}
		}
	}
	if r := c.def.Route; r != nil {
		strs := []struct {
			id, key string
			v       *string
		}{{"protocol", "protocol", r.Protocol}, {"scheme", "scheme", r.Scheme}, {"serverName", "server_name", r.ServerName}, {"healthPath", "health_path", r.HealthPath}}
		for _, e := range strs {
			if e.v != nil {
				c.addField("config.route."+e.id, rp("route", e.key), *e.v, st)
			}
		}
		bools := []struct {
			id, key string
			v       *bool
		}{{"insecureSkipVerify", "insecure_skip_verify", r.InsecureSkipVerify}, {"passHost", "pass_host", r.PassHost}, {"verifySkip", "verify_skip", r.VerifySkip}, {"openToInternet", "open_to_internet", r.OpenToInternet}}
		for _, e := range bools {
			if e.v != nil {
				c.addField("config.route."+e.id, rp("route", e.key), *e.v, st)
			}
		}
		if r.ListenPort != nil {
			c.addField("config.route.listenPort", rp("route", "listen_port"), *r.ListenPort, st)
		}
		if r.ProxyProtocol != nil {
			c.addField("config.route.proxyProtocol", rp("route", "proxy_protocol"), *r.ProxyProtocol, st)
		}
		if r.AllowCIDRs != nil {
			c.addField("config.route.allowCidrs", rp("route", "allow_cidrs"), append([]string{}, r.AllowCIDRs...), st)
		}
		if r.DenyCIDRs != nil {
			c.addField("config.route.denyCidrs", rp("route", "deny_cidrs"), append([]string{}, r.DenyCIDRs...), st)
		}
		if r.Upstreams != nil {
			ups := make([]UpstreamValue, len(r.Upstreams))
			for i, u := range r.Upstreams {
				ups[i] = UpstreamValue{Address: derefStr(u.Address), Service: derefStr(u.Service), Port: derefInt(u.Port)}
			}
			c.addField("config.route.upstreams", rp("route", "upstreams"), ups, st)
		}
	}
	if s := c.def.Stack; s != nil {
		if s.Compose != nil {
			c.addField("config.stack.compose", rp("stack", "compose"), *s.Compose, st)
		}
		if s.WaitSeconds != nil {
			c.addField("config.stack.waitSeconds", rp("stack", "wait_seconds"), *s.WaitSeconds, st)
		}
		for _, comp := range sortedKeys(s.Resources) {
			if r := s.Resources[comp]; r != nil {
				c.addField("config.stack.resources."+comp, rp("stack", "resources", comp),
					StackResourceValue{Component: comp, MemoryMB: derefInt(r.MemoryMB), CPUs: derefFloat(r.CPUs)}, st)
			}
		}
	}
}

// DockerOptionValue returns the platform DockerOptions field value of a
// config.docker.<jsonName> id for a docker table, with capability names
// normalised. ok is false for an unknown name.
func DockerOptionValue(d *Docker, jsonName string) (any, bool) {
	opts := dockerOptions(d)
	if opts == nil {
		opts = &agentprotocol.DockerOptions{}
	}
	src := opts.Normalized()
	if src == nil {
		src = opts
	}
	nz := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return append([]string{}, s...)
	}
	switch jsonName {
	case "capAdd":
		return nz(src.CapAdd), true
	case "capDrop":
		return nz(src.CapDrop), true
	case "noNewPrivileges":
		return src.NoNewPrivileges, true
	case "readOnlyRootfs":
		return src.ReadOnlyRootfs, true
	case "pidsLimit":
		return src.PidsLimit, true
	case "hostname":
		return src.Hostname, true
	case "dns":
		return nz(src.DNS), true
	case "sysctls":
		m := map[string]string{}
		for k, v := range src.Sysctls {
			m[k] = v
		}
		return m, true
	case "hostSysctls":
		m := map[string]int64{}
		for k, v := range src.HostSysctls {
			m[k] = v
		}
		return m, true
	case "user":
		return src.User, true
	case "groupAdd":
		return nz(src.GroupAdd), true
	case "ulimits":
		return append([]agentprotocol.Ulimit{}, src.Ulimits...), true
	case "shmSizeMb":
		return src.ShmSizeMB, true
	case "extraHosts":
		return nz(src.ExtraHosts), true
	case "labels":
		m := map[string]string{}
		for k, v := range src.Labels {
			m[k] = v
		}
		return m, true
	case "init":
		return src.Init, true
	case "stopSignal":
		return src.StopSignal, true
	case "tmpfs":
		return append([]agentprotocol.TmpfsMount{}, src.Tmpfs...), true
	case "entrypoint":
		return nz(src.Entrypoint), true
	case "devices":
		return append([]agentprotocol.DeviceMapping{}, src.Devices...), true
	case "gpus":
		if src.GPUs == nil {
			return agentprotocol.GPURequest{}, true
		}
		g := *src.GPUs
		g.Capabilities = nz(g.Capabilities)
		return g, true
	case "runtime":
		return src.Runtime, true
	case "privileged":
		return src.Privileged, true
	case "pidHost":
		return src.PidHost, true
	case "seccompUnconfined":
		return src.SeccompUnconfined, true
	case "apparmorUnconfined":
		return src.ApparmorUnconfined, true
	}
	return nil, false
}

func (c *svcCtx) assembleDocker() {
	d := c.def.Docker
	if d == nil {
		return
	}
	present := map[string]bool{
		"capAdd": d.CapAdd != nil, "capDrop": d.CapDrop != nil, "noNewPrivileges": d.NoNewPrivileges != nil,
		"readOnlyRootfs": d.ReadOnlyRootfs != nil, "pidsLimit": d.PidsLimit != nil, "hostname": d.Hostname != nil,
		"dns": d.DNS != nil, "sysctls": d.Sysctls != nil, "hostSysctls": d.HostSysctls != nil, "user": d.User != nil,
		"groupAdd": d.GroupAdd != nil, "ulimits": d.Ulimits != nil, "shmSizeMb": d.ShmSizeMB != nil,
		"extraHosts": d.ExtraHosts != nil, "labels": d.Labels != nil, "init": d.Init != nil, "stopSignal": d.StopSignal != nil,
		"tmpfs": d.Tmpfs != nil, "entrypoint": d.Entrypoint != nil, "devices": d.Devices != nil, "gpus": d.GPUs != nil,
		"runtime": d.Runtime != nil, "privileged": d.Privileged != nil, "pidHost": d.PidHost != nil,
		"seccompUnconfined": d.SeccompUnconfined != nil, "apparmorUnconfined": d.ApparmorUnconfined != nil,
	}
	for _, df := range dockerFields {
		if !present[df.json] {
			continue
		}
		if v, ok := DockerOptionValue(d, df.json); ok {
			c.addField("config.docker."+df.json, rp("docker", df.key), v, AppliesStaged)
		}
	}
}

func normTimezone(tz *string) string {
	if tz == nil {
		return "UTC"
	}
	s := strings.TrimSpace(*tz)
	if s == "" || strings.EqualFold(s, "utc") {
		return "UTC"
	}
	return s
}

func (c *svcCtx) assembleCollections() {
	d := c.def
	st, im := AppliesStaged, AppliesImmediate
	if d.Files != nil {
		files := make([]FileMount, 0, len(d.Files))
		for _, p := range sortedKeys(d.Files) {
			f := d.Files[p]
			if f == nil {
				continue
			}
			mode := "0644"
			if f.Mode != nil {
				mode = *f.Mode
			}
			files = append(files, FileMount{Path: p, Mode: mode, Content: derefStr(f.Content)})
		}
		c.addField("config.files", rp("files"), files, st)
	}
	for _, name := range sortedKeys(d.Variables) {
		c.addField("variables."+name, rp("variables", name), d.Variables[name], st)
	}
	if d.Secrets != nil {
		scope := orRuntime(d.Secrets.Scope)
		for i, k := range d.Secrets.Keys {
			c.addField("variables."+k+"#secret", rp("secrets", "keys", "["+itoa(i)+"]"), SecretDecl{Name: k, Scope: scope}, st)
		}
	}
	for _, key := range sortedKeys(d.Domains) {
		dm := d.Domains[key]
		host, prefix, ok := splitDomainKey(key)
		if dm == nil || !ok {
			continue
		}
		rule := DomainRule{
			Host: strings.ToLower(host), PathPrefix: prefix, TargetPort: dm.TargetPort, Component: dm.Component, Process: dm.Process,
			StripPrefix: dm.StripPrefix, AddPrefix: dm.AddPrefix, Kind: dm.Kind, RedirectTo: dm.RedirectTo,
			RedirectCode: dm.RedirectCode, PreservePath: dm.PreservePath, TLS: dm.TLS, Certificate: dm.Certificate,
			DNSResolver: dm.DNSResolver, WWWRedirect: dm.WWWRedirect,
		}
		if dm.Options != nil {
			o := routeOptions(dm.Options)
			rule.Options = &o
		}
		id := "domains." + strings.ToLower(host) + prefix
		c.addField(id, rp("domains", key), rule, st)
		if cf := dm.Cloudflare; cf != nil {
			c.addField(id+".cloudflare", rp("domains", key, "cloudflare"), CloudflareRule{Proxied: cf.Proxied, OriginLock: cf.OriginLock}, im)
		}
	}
	for _, name := range sortedKeys(d.Volumes) {
		c.addField("volumes."+name, rp("volumes", name), Volume{Name: name, MountPath: d.Volumes[name]}, st)
	}
	if s := d.Scale; s != nil {
		if s.Replicas != nil {
			c.addField("scale.replicas", rp("scale", "replicas"), *s.Replicas, im)
		}
		if a := s.Autoscaling; a != nil {
			if a.Enabled != nil {
				c.addField("scale.autoscaling.enabled", rp("scale", "autoscaling", "enabled"), *a.Enabled, im)
			}
			if a.Min != nil {
				c.addField("scale.autoscaling.min", rp("scale", "autoscaling", "min"), *a.Min, im)
			}
			if a.Max != nil {
				c.addField("scale.autoscaling.max", rp("scale", "autoscaling", "max"), *a.Max, im)
			}
			if a.TargetCPUPercent != nil {
				c.addField("scale.autoscaling.targetCpuPercent", rp("scale", "autoscaling", "target_cpu_percent"), *a.TargetCPUPercent, im)
			}
		}
	}
	if u := d.Uptime; u != nil {
		if u.Enabled != nil {
			c.addField("uptime.enabled", rp("uptime", "enabled"), *u.Enabled, im)
		}
		if u.Alerts != nil {
			c.addField("uptime.alerts", rp("uptime", "alerts"), *u.Alerts, im)
		}
		if u.FailureThreshold != nil {
			c.addField("uptime.failureThreshold", rp("uptime", "failure_threshold"), *u.FailureThreshold, im)
		}
		if u.RecoveryThreshold != nil {
			c.addField("uptime.recoveryThreshold", rp("uptime", "recovery_threshold"), *u.RecoveryThreshold, im)
		}
		if u.ReminderMinutes != nil {
			c.addField("uptime.reminderMinutes", rp("uptime", "reminder_minutes"), *u.ReminderMinutes, im)
		}
	}
	for _, name := range sortedKeys(d.Tasks) {
		t := d.Tasks[name]
		if t == nil {
			continue
		}
		tv := TaskValue{Name: name, Schedule: strings.TrimSpace(derefStr(t.Schedule)), Timezone: normTimezone(t.Timezone),
			Command: append([]string{}, t.Command...), Enabled: true, TimeoutSeconds: 1800, Concurrency: "forbid"}
		if t.Enabled != nil {
			tv.Enabled = *t.Enabled
		}
		if t.TimeoutSeconds != nil {
			tv.TimeoutSeconds = *t.TimeoutSeconds
		}
		if t.Concurrency != nil {
			tv.Concurrency = *t.Concurrency
		}
		c.addField("tasks."+name, rp("tasks", name), tv, im)
	}
	for _, name := range sortedKeys(d.Processes) {
		p := d.Processes[name]
		if p == nil {
			continue
		}
		c.addField("config.processes."+name, rp("processes", name), processValue(name, p), st)
		s := p.Scale
		if s == nil {
			continue
		}
		sid := "processes." + name + ".scale."
		if s.Replicas != nil {
			c.addField(sid+"replicas", rp("processes", name, "scale", "replicas"), *s.Replicas, im)
		}
		if a := s.Autoscaling; a != nil {
			if a.Enabled != nil {
				c.addField(sid+"autoscaling.enabled", rp("processes", name, "scale", "autoscaling", "enabled"), *a.Enabled, im)
			}
			if a.Min != nil {
				c.addField(sid+"autoscaling.min", rp("processes", name, "scale", "autoscaling", "min"), *a.Min, im)
			}
			if a.Max != nil {
				c.addField(sid+"autoscaling.max", rp("processes", name, "scale", "autoscaling", "max"), *a.Max, im)
			}
			if a.TargetCPUPercent != nil {
				c.addField(sid+"autoscaling.targetCpuPercent", rp("processes", name, "scale", "autoscaling", "target_cpu_percent"), *a.TargetCPUPercent, im)
			}
		}
	}
	for _, key := range sortedKeys(d.Backups) {
		b := d.Backups[key]
		if b == nil {
			continue
		}
		bv := BackupValue{Key: key, DestinationName: strings.TrimSpace(derefStr(b.Destination)), Cron: strings.TrimSpace(derefStr(b.Cron)),
			Timezone: normTimezone(b.Timezone), Enabled: true, RetentionCount: 7}
		if b.Enabled != nil {
			bv.Enabled = *b.Enabled
		}
		if b.RetentionCount != nil {
			bv.RetentionCount = *b.RetentionCount
		}
		if b.RetentionDays != nil {
			bv.RetentionDays = *b.RetentionDays
		}
		c.addField("backups."+key, rp("backups", key), bv, im)
	}
}
