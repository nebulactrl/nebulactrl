package projectconfig

import (
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/cronexpr"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/gitssh"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
	"github.com/nebulactrl/nebulactrl/pkg/composestack"
)

var (
	volumeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	taskNameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	repoRe       = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)
	fileModeRe   = regexp.MustCompile(`^0?[0-7]{3,4}$`)
	dnsResolver  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
)

// validArgv returns a message when argv is not a valid argv command.
func validArgv(argv []string) string {
	if err := agentprotocol.ValidTaskCommand(argv); err != nil {
		return err.Error()
	}
	return ""
}

// relPath validates a relative path key, reporting path_escape.
func (c *svcCtx) relPath(key []string, p string) bool {
	if why := checkRelPath(p); why != "" {
		c.err(CodePathEscape, key, "%s: %s", strings.Join(key, "."), why)
		return false
	}
	return true
}

// inlineAttachments resolves compose_file and files.<path>.source into
// Stack.Compose and Files[*].Content and clears the sources.
func (c *svcCtx) inlineAttachments() {
	att := c.v.f.meta.att
	if st := c.def.Stack; st != nil {
		switch {
		case st.Compose != nil && st.ComposeFile != nil:
			c.err(CodeInvalidValue, rp("stack", "compose_file"), "stack takes exactly one of `compose` and `compose_file`")
		case st.Compose == nil && st.ComposeFile == nil:
			c.err(CodeMissingRequired, rp("stack"), "stack needs `compose` (the compose text) or `compose_file` (a path next to nebula.toml)")
		case st.ComposeFile != nil:
			name := *st.ComposeFile
			if c.relPath(rp("stack", "compose_file"), name) {
				data, ok := lookupAttachment(att, name)
				switch {
				case !ok:
					g := c.err(CodeAttachmentMissing, rp("stack", "compose_file"), "the file %q was not supplied with the request", name)
					g.Hint = "send the compose file as an attachment, keyed by the path written here"
				case !utf8.Valid(data):
					c.err(CodeInvalidEncoding, rp("stack", "compose_file"), "%q is not valid UTF-8", name)
				default:
					text := string(data)
					st.Compose = &text
					st.ComposeFile = nil
					if kind, found := ScanSecret(text); found {
						c.err(CodeSecretInFile, rp("stack", "compose_file"), "%q contains what looks like a credential (%s); compose files are stored as plain text", name, kind)
					}
				}
			}
		}
		if st.Compose != nil && len(*st.Compose) > MaxComposeBytes {
			c.err(CodeLimitExceeded, rp("stack", "compose"), "the compose text is %d bytes; at most %d", len(*st.Compose), MaxComposeBytes)
		}
	}
	files := c.def.Files
	total := 0
	for _, p := range sortedKeys(files) {
		f := files[p]
		if f == nil {
			continue
		}
		switch {
		case f.Content != nil && f.Source != nil:
			c.err(CodeInvalidValue, rp("files", p, "source"), "a file takes exactly one of `content` and `source`")
		case f.Content == nil && f.Source == nil:
			c.err(CodeMissingRequired, rp("files", p), "a file needs `content` or `source`")
		case f.Source != nil:
			src := *f.Source
			if !c.relPath(rp("files", p, "source"), src) {
				continue
			}
			data, ok := lookupAttachment(att, src)
			switch {
			case !ok:
				g := c.err(CodeAttachmentMissing, rp("files", p, "source"), "the file %q was not supplied with the request", src)
				g.Hint = "send it as an attachment, keyed by the path written here"
			case !utf8.Valid(data):
				c.err(CodeInvalidEncoding, rp("files", p, "source"), "%q is not valid UTF-8; only text files can be mounted", src)
			default:
				text := string(data)
				f.Content = &text
				f.Source = nil
				if kind, found := ScanSecret(text); found {
					c.err(CodeSecretInFile, rp("files", p, "source"), "%q contains what looks like a credential (%s); files are stored as plain text", src, kind)
				}
			}
		}
		if f.Content != nil {
			total += len(*f.Content)
		}
	}
	if total > MaxFilesTotalBytes {
		c.err(CodeLimitExceeded, rp("files"), "files total %d bytes; at most %d", total, MaxFilesTotalBytes)
	}
}

// checkGit validates [git].
func (c *svcCtx) checkGit() {
	g := c.def.Git
	hasURL, hasConn, hasRepo := g.URL != nil, g.Connection != nil, g.Repo != nil
	switch {
	case hasURL && (hasConn || hasRepo):
		c.err(CodeInvalidValue, rp("git", "url"), "git takes either `url`, or `connection` and `repo`, not both")
	case !hasURL && hasConn != hasRepo:
		c.err(CodeMissingRequired, rp("git"), "git.connection and git.repo go together")
	case !hasURL && !hasConn:
		c.err(CodeMissingRequired, rp("git"), "git needs `url`, or `connection` and `repo`")
	}
	if hasURL {
		c.checkGitURL(*g.URL)
		if g.AutoDeploy != nil {
			c.err(CodeInvalidValue, rp("git", "auto_deploy"), "git.auto_deploy needs `connection` and `repo`; it is not available with `url`")
		}
		if g.WatchPaths != nil {
			c.err(CodeInvalidValue, rp("git", "watch_paths"), "git.watch_paths needs `connection` and `repo`; it is not available with `url`")
		}
	}
	if hasConn && (strings.TrimSpace(*g.Connection) == "" || len(*g.Connection) > 200) {
		c.err(CodeInvalidValue, rp("git", "connection"), "a git connection is named by its name: 1 to 200 characters")
	}
	if hasRepo && !repoRe.MatchString(*g.Repo) {
		c.err(CodeInvalidValue, rp("git", "repo"), "git.repo is owner/name, for example acme/shop")
	}
	switch {
	case g.Branch == nil:
		c.err(CodeMissingRequired, rp("git", "branch"), "git.branch is required")
	case strings.TrimSpace(*g.Branch) == "" || len(*g.Branch) > 255:
		c.err(CodeInvalidValue, rp("git", "branch"), "git.branch must be 1 to 255 characters")
	}
	if g.Submodules != nil {
		switch *g.Submodules {
		case "auto", "on", "off":
		default:
			c.err(CodeInvalidValue, rp("git", "submodules"), "git.submodules must be auto, on or off")
		}
	}
	if g.WatchPaths != nil {
		if len(g.WatchPaths) > MaxWatchPaths {
			c.err(CodeLimitExceeded, rp("git", "watch_paths"), "%d watch paths; at most %d", len(g.WatchPaths), MaxWatchPaths)
		} else if _, err := gitproviders.ValidateWatchPaths(g.WatchPaths); err != nil {
			c.err(CodeInvalidValue, rp("git", "watch_paths"), "git.watch_paths: %s", err)
		}
	}
}

func (c *svcCtx) checkGitURL(raw string) {
	p := rp("git", "url")
	u := strings.TrimSpace(raw)
	switch {
	case u == "" || len(u) > 512 || strings.ContainsAny(u, " \t\r\n"):
		c.err(CodeInvalidValue, p, "git.url must be 1 to 512 characters without whitespace")
		return
	case gitssh.LooksLikeSSH(u):
		if _, err := gitssh.ParseURL(u); err != nil {
			code := CodeInvalidValue
			if strings.Contains(strings.ToLower(err.Error()), "password") {
				code = CodeCredentialInURL
			}
			c.err(code, p, "git.url: %s", err)
		}
		return
	case !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "git://")):
		c.err(CodeInvalidValue, p, "git.url must be an https://, http://, git:// or ssh:// URL, or git@host:org/repo.git")
		return
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		c.err(CodeInvalidValue, p, "git.url is not a valid URL")
		return
	}
	if parsed.User != nil {
		g := c.err(CodeCredentialInURL, p, "git.url contains credentials before the host; credentials are never stored in nebula.toml")
		g.Hint = "use `connection` and `repo`, or store the token or deploy key on the service in the dashboard"
	}
}

// checkBuild validates [build].
func (c *svcCtx) checkBuild() {
	b := c.def.Build
	if b.Type != nil && !agentprotocol.ValidBuildType(*b.Type) {
		c.err(CodeInvalidValue, rp("build", "type"), "build.type must be auto, dockerfile, railpack or starter:<node|python|go|ruby|php|static>")
	}
	if b.Location != nil {
		switch *b.Location {
		case services.BuildLocationInherit, services.BuildLocationTarget, services.BuildLocationBuildServer:
		default:
			c.err(CodeInvalidValue, rp("build", "location"), "build.location must be inherit, target or build_server")
		}
	}
	if b.Dockerfile != nil {
		c.relPath(rp("build", "dockerfile"), *b.Dockerfile)
	}
	if b.Context != nil {
		c.relPath(rp("build", "context"), *b.Context)
	}
	for _, kv := range []struct {
		key string
		val *string
	}{{"server", b.Server}, {"registry", b.Registry}} {
		if kv.val != nil && (strings.TrimSpace(*kv.val) == "" || len(*kv.val) > 200) {
			c.err(CodeInvalidValue, rp("build", kv.key), "build.%s is a name: 1 to 200 characters", kv.key)
		}
	}
}

// dockerOptions converts the model's docker table to the platform's options.
func dockerOptions(d *Docker) *agentprotocol.DockerOptions {
	if d == nil {
		return nil
	}
	o := &agentprotocol.DockerOptions{
		CapAdd: d.CapAdd, CapDrop: d.CapDrop, DNS: d.DNS, Sysctls: d.Sysctls, HostSysctls: nil,
		GroupAdd: d.GroupAdd, ExtraHosts: d.ExtraHosts, Labels: d.Labels, Entrypoint: d.Entrypoint,
	}
	o.NoNewPrivileges = derefBool(d.NoNewPrivileges)
	o.ReadOnlyRootfs = derefBool(d.ReadOnlyRootfs)
	if d.PidsLimit != nil {
		o.PidsLimit = int64(*d.PidsLimit)
	}
	o.Hostname = derefStr(d.Hostname)
	o.User = derefStr(d.User)
	if d.ShmSizeMB != nil {
		o.ShmSizeMB = *d.ShmSizeMB
	}
	o.Init = derefBool(d.Init)
	o.StopSignal = derefStr(d.StopSignal)
	o.Runtime = derefStr(d.Runtime)
	o.Privileged = derefBool(d.Privileged)
	o.PidHost = derefBool(d.PidHost)
	o.SeccompUnconfined = derefBool(d.SeccompUnconfined)
	o.ApparmorUnconfined = derefBool(d.ApparmorUnconfined)
	if len(d.HostSysctls) > 0 {
		o.HostSysctls = make(map[string]int64, len(d.HostSysctls))
		for k, v := range d.HostSysctls {
			o.HostSysctls[k] = int64(v)
		}
	}
	for _, name := range sortedKeys(d.Ulimits) {
		u := d.Ulimits[name]
		if u == nil {
			continue
		}
		o.Ulimits = append(o.Ulimits, agentprotocol.Ulimit{Name: name, Soft: derefInt64(u.Soft), Hard: derefInt64(u.Hard)})
	}
	for _, p := range sortedKeys(d.Tmpfs) {
		t := d.Tmpfs[p]
		if t == nil {
			continue
		}
		m := agentprotocol.TmpfsMount{Path: p, Mode: derefStr(t.Mode)}
		if t.SizeMB != nil {
			m.SizeMB = *t.SizeMB
		}
		o.Tmpfs = append(o.Tmpfs, m)
	}
	for _, dv := range d.Devices {
		o.Devices = append(o.Devices, agentprotocol.DeviceMapping{HostPath: derefStr(dv.HostPath), ContainerPath: derefStr(dv.ContainerPath), Permissions: derefStr(dv.Permissions)})
	}
	if d.GPUs != nil {
		g := &agentprotocol.GPURequest{Driver: derefStr(d.GPUs.Driver), Capabilities: d.GPUs.Capabilities}
		if d.GPUs.Count != nil {
			g.Count = *d.GPUs.Count
		}
		o.GPUs = g
	}
	return o
}

func derefBool(p *bool) bool { return p != nil && *p }

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func (c *svcCtx) checkDocker() {
	d := c.def.Docker
	if d == nil {
		return
	}
	for _, name := range sortedKeys(d.Ulimits) {
		u := d.Ulimits[name]
		if u != nil && (u.Soft == nil || u.Hard == nil) {
			c.err(CodeMissingRequired, rp("docker", "ulimits", name), "docker.ulimits.%s needs both `soft` and `hard`", name)
		}
	}
	for i, dv := range d.Devices {
		if dv.HostPath == nil {
			c.err(CodeMissingRequired, rp("docker", "devices", "["+itoa(i)+"]"), "a device needs `host_path`")
		}
	}
	if d.GPUs != nil && (d.GPUs.Count == nil || d.GPUs.Driver == nil) {
		c.err(CodeMissingRequired, rp("docker", "gpus"), "docker.gpus needs `count` and `driver`")
	}
	if d.PidsLimit != nil && *d.PidsLimit < 0 {
		c.err(CodeInvalidValue, rp("docker", "pids_limit"), "docker.pids_limit must be 0 or more")
	}
	opts := dockerOptions(d)
	if err := opts.Validate(true); err != nil {
		c.err(CodeInvalidValue, rp("docker"), "docker: %s", err)
		return
	}
	if o := opts.Normalized(); o != nil && o.ReadOnlyRootfs {
		var files, mounts []string
		for p := range c.def.Files {
			files = append(files, p)
		}
		for _, m := range c.def.Volumes {
			mounts = append(mounts, m)
		}
		sort.Strings(files)
		sort.Strings(mounts)
		if err := o.ValidateFiles(files, mounts); err != nil {
			c.err(CodeInvalidValue, rp("docker", "read_only_rootfs"), "docker: %s", err)
		}
	}
	for p := range d.Tmpfs {
		if _, clash := c.def.Files[p]; clash {
			c.err(CodeInvalidValue, rp("docker", "tmpfs", p), "docker: tmpfs %s collides with the file mount at the same path", p)
		}
	}
}

func (c *svcCtx) checkVolumes() {
	vols := c.def.Volumes
	if len(vols) > MaxVolumesPerService {
		c.err(CodeLimitExceeded, rp("volumes"), "%d volumes; at most %d", len(vols), MaxVolumesPerService)
	}
	mounts := map[string]string{}
	for _, name := range sortedKeys(vols) {
		mp := vols[name]
		if !volumeNameRe.MatchString(name) {
			c.err(CodeInvalidValue, rp("volumes", name), "volume name %q must match [a-z0-9][a-z0-9_.-]* (at most 63 characters)", name)
		}
		switch {
		case !strings.HasPrefix(mp, "/") || len(mp) > 512 || strings.ContainsRune(mp, 0) || strings.Contains(mp, ".."):
			c.err(CodeInvalidValue, rp("volumes", name), "the mount path of volume %q must be an absolute path without ..", name)
		case mp == "/":
			c.err(CodeInvalidValue, rp("volumes", name), "a volume cannot be mounted at /")
		default:
			if other, dup := mounts[mp]; dup {
				c.err(CodeInvalidValue, rp("volumes", name), "volumes %q and %q are mounted at the same path %s", other, name, mp)
			}
			mounts[mp] = name
		}
	}
}

func (c *svcCtx) checkFiles() {
	files := c.def.Files
	if len(files) > MaxFilesPerService {
		c.err(CodeLimitExceeded, rp("files"), "%d files; at most %d", len(files), MaxFilesPerService)
	}
	for _, p := range sortedKeys(files) {
		f := files[p]
		if f == nil {
			continue
		}
		if !strings.HasPrefix(p, "/") || len(p) > 512 || strings.Contains(p, "..") || strings.ContainsRune(p, 0) || strings.HasSuffix(p, "/") {
			c.err(CodeInvalidValue, rp("files", p), "a file path is an absolute path to a file without ..")
		}
		if f.Mode != nil && !fileModeRe.MatchString(*f.Mode) {
			c.err(CodeInvalidValue, rp("files", p, "mode"), "mode is an octal string such as 0644")
		}
		if f.Content != nil && len(*f.Content) > MaxFileContentBytes {
			c.err(CodeLimitExceeded, rp("files", p), "file %s is %d bytes; at most %d", p, len(*f.Content), MaxFileContentBytes)
		}
	}
}

func (c *svcCtx) checkScale() {
	c.checkScaleTable(rp("scale"), c.def.Scale)
}

// checkScaleTable validates a scale table found at key: the service's own or a process'.
func (c *svcCtx) checkScaleTable(key []string, s *Scale) {
	if s == nil {
		return
	}
	at := func(k ...string) []string { return append(key[:len(key):len(key)], k...) }
	c.intIn(at("replicas"), s.Replicas, 1, 10)
	a := s.Autoscaling
	if a == nil {
		return
	}
	c.intIn(at("autoscaling", "min"), a.Min, 1, 10)
	c.intIn(at("autoscaling", "max"), a.Max, 1, 10)
	c.intIn(at("autoscaling", "target_cpu_percent"), a.TargetCPUPercent, 20, 95)
	if a.Min != nil || a.Max != nil {
		lo, hi := 1, 3
		if a.Min != nil {
			lo = *a.Min
		}
		if a.Max != nil {
			hi = *a.Max
		}
		if hi < lo {
			c.err(CodeInvalidValue, at("autoscaling", "max"), "scale.autoscaling.max (%d) must be at least min (%d)", hi, lo)
		}
	}
}

// checkProcesses validates the additional processes and the service's singleton flag.
func (c *svcCtx) checkProcesses() {
	d := c.def
	procs := d.Processes
	def := services.DefaultProcessName
	if d.ProcessName != nil {
		def = *d.ProcessName
		if !services.ProcessNamePattern.MatchString(def) {
			c.err(CodeInvalidValue, rp("process_name"), "process_name %q must match [a-z0-9]([a-z0-9-]*[a-z0-9])? (at most 31 characters)", def)
		}
	}
	if d.Singleton != nil && *d.Singleton {
		c.checkSingletonScale(rp("scale"), d.Scale, "the service's own process")
	}
	if len(procs) == 0 {
		return
	}
	if len(procs) > MaxProcessesPerService {
		c.err(CodeLimitExceeded, rp("processes"), "%d processes; at most %d besides the service's own", len(procs), MaxProcessesPerService)
	}
	if d.OneShot != nil && *d.OneShot {
		c.err(CodeInvalidValue, rp("processes"), "a one-shot service runs to completion and cannot have processes")
	}
	owner := map[int]string{}
	if d.Port != nil && *d.Port > 0 {
		owner[*d.Port] = def
	}
	for _, name := range sortedKeys(procs) {
		p := procs[name]
		if p == nil {
			continue
		}
		switch {
		case !services.ProcessNamePattern.MatchString(name):
			c.err(CodeInvalidValue, rp("processes", name), "process name %q must match [a-z0-9]([a-z0-9-]*[a-z0-9])? (at most 31 characters)", name)
		case name == def:
			g := c.err(CodeProcessNameReserved, rp("processes", name), "process name %q is the service's own process", name)
			g.Hint = "rename this process, or set process_name on the service to give its own process another name"
		}
		if len(p.Command) == 0 {
			c.err(CodeProcessCommandRequired, rp("processes", name, "command"), "process %q needs `command`", name)
		} else if msg := validArgv(p.Command); msg != "" {
			c.err(CodeInvalidValue, rp("processes", name, "command"), "command: %s", msg)
		} else if i := slices.Index(p.Command, ""); i >= 0 {
			c.err(CodeInvalidValue, rp("processes", name, "command"), "command: element %d is empty", i)
		}
		c.intIn(rp("processes", name, "port"), p.Port, 0, 65535)
		c.intIn(rp("processes", name, "stop_grace_seconds"), p.StopGraceSeconds, 1, 3600)
		if p.MemoryMB != nil && (*p.MemoryMB < 0 || (*p.MemoryMB > 0 && *p.MemoryMB < 6)) {
			c.err(CodeInvalidValue, rp("processes", name, "memory_mb"), "memory_mb is 0 (no limit) or at least 6")
		}
		if p.CPUs != nil && !(*p.CPUs >= 0) {
			c.err(CodeInvalidValue, rp("processes", name, "cpus"), "cpus must be 0 (no limit) or positive")
		}
		c.checkHealthTable(rp("processes", name, "health_check"), p.HealthCheck, p.Port, "process")
		c.checkScaleTable(rp("processes", name, "scale"), p.Scale)
		if p.Singleton != nil && *p.Singleton {
			c.checkSingletonScale(rp("processes", name, "scale"), p.Scale, "process "+strconv.Quote(name))
		}
		if p.Port != nil && *p.Port > 0 {
			if other, taken := owner[*p.Port]; taken {
				c.err(CodeProcessPortConflict, rp("processes", name, "port"), "processes %q and %q both use port %d", name, other, *p.Port)
			} else {
				owner[*p.Port] = name
			}
		}
	}
}

// checkSingletonScale reports a scale table that asks a singleton process for more than one instance.
func (c *svcCtx) checkSingletonScale(key []string, s *Scale, who string) {
	if s == nil {
		return
	}
	at := func(k ...string) []string { return append(key[:len(key):len(key)], k...) }
	if s.Replicas != nil && *s.Replicas > 1 {
		c.err(CodeProcessSingletonScale, at("replicas"), "%s is a singleton and runs exactly one instance; remove scale.replicas or singleton", who)
	}
	if a := s.Autoscaling; a != nil && a.Enabled != nil && *a.Enabled {
		c.err(CodeProcessSingletonScale, at("autoscaling", "enabled"), "%s is a singleton and cannot autoscale; remove scale.autoscaling or singleton", who)
	}
}

func (c *svcCtx) checkUptime() {
	u := c.def.Uptime
	if u == nil {
		return
	}
	c.intIn(rp("uptime", "failure_threshold"), u.FailureThreshold, 1, 20)
	c.intIn(rp("uptime", "recovery_threshold"), u.RecoveryThreshold, 1, 20)
	c.intIn(rp("uptime", "reminder_minutes"), u.ReminderMinutes, 0, 1440)
}

func (c *svcCtx) checkTasks() {
	tasks := c.def.Tasks
	if len(tasks) > MaxTasksPerService {
		c.err(CodeLimitExceeded, rp("tasks"), "%d tasks; at most %d per service", len(tasks), MaxTasksPerService)
	}
	for _, name := range sortedKeys(tasks) {
		t := tasks[name]
		if t == nil {
			continue
		}
		if !taskNameRe.MatchString(name) {
			c.err(CodeInvalidValue, rp("tasks", name), "task name %q must match [a-z0-9][a-z0-9_-]* (at most 63 characters)", name)
		}
		if t.Schedule == nil {
			c.err(CodeMissingRequired, rp("tasks", name, "schedule"), "task %q needs `schedule`", name)
		} else {
			c.checkCron(rp("tasks", name, "schedule"), *t.Schedule, 0)
		}
		if t.Timezone != nil {
			c.checkTimezone(rp("tasks", name, "timezone"), *t.Timezone)
		}
		if len(t.Command) == 0 {
			c.err(CodeMissingRequired, rp("tasks", name, "command"), "task %q needs `command`", name)
		} else if msg := validArgv(t.Command); msg != "" {
			c.err(CodeInvalidValue, rp("tasks", name, "command"), "command: %s", msg)
		}
		c.intIn(rp("tasks", name, "timeout_seconds"), t.TimeoutSeconds, 1, 86400)
		if t.Concurrency != nil && *t.Concurrency != "forbid" && *t.Concurrency != "allow" {
			c.err(CodeInvalidValue, rp("tasks", name, "concurrency"), "concurrency must be forbid or allow")
		}
	}
}

func (c *svcCtx) checkCron(key []string, spec string, minGap time.Duration) {
	if len(spec) > 200 {
		c.err(CodeInvalidValue, key, "the schedule is longer than 200 characters")
		return
	}
	sch, err := cronexpr.Parse(spec)
	if err != nil {
		c.err(CodeInvalidValue, key, "%s", strings.TrimPrefix(err.Error(), "cron: "))
		return
	}
	ref := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := sch.Next(ref)
	if first.IsZero() {
		c.err(CodeInvalidValue, key, "the schedule never fires")
		return
	}
	if minGap > 0 {
		if second := sch.Next(first); !second.IsZero() && second.Sub(first) < minGap {
			c.err(CodeInvalidValue, key, "backups can run at most every %d minutes", int(minGap.Minutes()))
		}
	}
}

func (c *svcCtx) checkTimezone(key []string, tz string) {
	if len(tz) > 64 {
		c.err(CodeInvalidValue, key, "the time zone is longer than 64 characters")
		return
	}
	if _, err := cronexpr.LoadLocation(strings.TrimSpace(tz)); err != nil {
		c.err(CodeInvalidValue, key, "%s", strings.TrimPrefix(err.Error(), "cron: "))
	}
}

func (c *svcCtx) checkBackups() {
	bs := c.def.Backups
	if len(bs) > MaxBackupsPerService {
		c.err(CodeLimitExceeded, rp("backups"), "%d backup schedules; at most %d per service", len(bs), MaxBackupsPerService)
	}
	for _, key := range sortedKeys(bs) {
		b := bs[key]
		if b == nil {
			continue
		}
		if !taskNameRe.MatchString(key) {
			c.err(CodeInvalidValue, rp("backups", key), "backup key %q must match [a-z0-9][a-z0-9_-]* (at most 63 characters)", key)
		}
		if b.Destination == nil {
			c.err(CodeMissingRequired, rp("backups", key, "destination"), "backup %q needs `destination` (the name of a backup destination)", key)
		} else if strings.TrimSpace(*b.Destination) == "" || len(*b.Destination) > 200 {
			c.err(CodeInvalidValue, rp("backups", key, "destination"), "a backup destination is named by its name: 1 to 200 characters")
		}
		if b.Cron == nil {
			c.err(CodeMissingRequired, rp("backups", key, "cron"), "backup %q needs `cron`", key)
		} else {
			c.checkCron(rp("backups", key, "cron"), *b.Cron, 5*time.Minute)
		}
		if b.Timezone != nil {
			c.checkTimezone(rp("backups", key, "timezone"), *b.Timezone)
		}
		c.intIn(rp("backups", key, "retention_count"), b.RetentionCount, 0, 1000)
		c.intIn(rp("backups", key, "retention_days"), b.RetentionDays, 0, 3650)
	}
}

func (c *svcCtx) checkDatabase() {
	d := c.def.Database
	if d.Engine == nil {
		c.err(CodeMissingRequired, rp("database", "engine"), "database.engine is required (one of %s)", strings.Join(engines.IDs(), ", "))
		return
	}
	eng := engines.Lookup(*d.Engine)
	if eng == nil {
		g := c.err(CodeInvalidValue, rp("database", "engine"), "unknown database engine %q (one of %s)", *d.Engine, strings.Join(engines.IDs(), ", "))
		g.Suggestions = nearest(*d.Engine, engines.IDs())
		return
	}
	spec := engines.Spec{Engine: *d.Engine, Version: derefStr(d.Version), Flavor: derefStr(d.Flavor), DatabaseName: derefStr(d.Name), Username: derefStr(d.Username)}.Normalize()
	if err := spec.Validate(); err != nil {
		key := "engine"
		msg := err.Error()
		switch {
		case strings.Contains(msg, "version"):
			key = "version"
		case strings.Contains(msg, "flavor"):
			key = "flavor"
		case strings.Contains(msg, "databaseName"), strings.Contains(msg, "database name"):
			key = "name"
		case strings.Contains(msg, "username"), strings.Contains(msg, "user name"):
			key = "username"
		}
		c.err(CodeInvalidValue, rp("database", key), "database: %s", msg)
	}
	if eng.LicenseClass == engines.LicenseSourceAvailable && !derefBool(d.AcceptLicense) {
		g := c.err(CodeLicenseNotAccepted, rp("database", "accept_license"), "%s is source-available software: %s", eng.Label, eng.LicenseNote)
		g.Hint = "write accept_license = true to acknowledge the licence"
	}
}

// nearest returns the candidates within a small edit distance of s.
func nearest(s string, candidates []string) []string {
	var out []string
	for _, cand := range candidates {
		if damerau(s, cand) <= max(2, len(s)/3) {
			out = append(out, cand)
		}
	}
	sort.Strings(out)
	return out
}

// damerau is the optimal-string-alignment distance.
func damerau(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

func (c *svcCtx) checkRoute() {
	r := c.def.Route
	if r.Protocol == nil {
		c.err(CodeMissingRequired, rp("route", "protocol"), "route.protocol is required: http, tcp or udp")
	}
	if len(r.Upstreams) == 0 {
		c.err(CodeMissingRequired, rp("route", "upstreams"), "route.upstreams needs 1 to %d upstreams", MaxUpstreams)
	}
	if len(r.Upstreams) > MaxUpstreams {
		c.err(CodeLimitExceeded, rp("route", "upstreams"), "%d upstreams; at most %d", len(r.Upstreams), MaxUpstreams)
	}
	for i, u := range r.Upstreams {
		up := rp("route", "upstreams", "["+itoa(i)+"]")
		switch {
		case u.Address != nil && u.Service != nil:
			c.err(CodeInvalidUpstream, up, "an upstream is either `address` or `service` + `port`, not both")
		case u.Address == nil && u.Service == nil:
			c.err(CodeMissingRequired, up, "an upstream needs `address` (host:port) or `service` and `port`")
		case u.Address != nil:
			if u.Port != nil {
				c.err(CodeInvalidUpstream, append(up[:len(up):len(up)], "port"), "`port` belongs to a `service` upstream; write the port inside `address`")
			}
			host, port, err := net.SplitHostPort(*u.Address)
			pn, perr := strconv.Atoi(port)
			if err != nil || host == "" || perr != nil || pn < 1 || pn > 65535 || len(*u.Address) > 300 {
				c.err(CodeInvalidUpstream, append(up[:len(up):len(up)], "address"), "an upstream address is host:port with a port from 1 to 65535")
			}
		default:
			if !slugRe.MatchString(*u.Service) {
				c.err(CodeInvalidSlug, append(up[:len(up):len(up)], "service"), "%q is not a service slug", *u.Service)
			}
			if u.Port == nil {
				c.err(CodeMissingRequired, append(up[:len(up):len(up)], "port"), "a managed upstream needs `port`")
			} else if *u.Port < 1 || *u.Port > 65535 {
				c.err(CodeInvalidValue, append(up[:len(up):len(up)], "port"), "port must be between 1 and 65535")
			}
		}
	}
	if r.ListenPort != nil {
		if err := agentprotocol.ValidateListenPort(*r.ListenPort); err != nil {
			c.err(CodeInvalidValue, rp("route", "listen_port"), "route.listen_port: %s", err)
		}
	}
	if r.ProxyProtocol != nil && (*r.ProxyProtocol < 0 || *r.ProxyProtocol > 2) {
		c.err(CodeInvalidValue, rp("route", "proxy_protocol"), "route.proxy_protocol must be 0, 1 or 2")
	}
	if r.Protocol == nil {
		return
	}
	if verr := c.routeConfig().Validate(); verr != nil {
		key := "protocol"
		m := verr.Error()
		switch {
		case strings.Contains(m, "healthPath"):
			key = "health_path"
		case strings.Contains(m, "scheme"):
			key = "scheme"
		case strings.Contains(m, "listenPort"), strings.Contains(m, "listen"):
			key = "listen_port"
		case strings.Contains(m, "proxyProtocol"):
			key = "proxy_protocol"
		case strings.Contains(m, "allowCidrs"), strings.Contains(m, "openToInternet"):
			key = "open_to_internet"
		case strings.Contains(m, "upstream"):
			key = "upstreams"
		}
		if !c.hasErrorAt(c.abs("route", key)) {
			c.err(CodeInvalidValue, rp("route", key), "route: %s", strings.TrimPrefix(m, "invalid service config: "))
		}
	}
}

func (c *svcCtx) hasErrorAt(path string) bool {
	for _, d := range c.v.s.list {
		if d.Severity == SeverityError && d.Path == path {
			return true
		}
	}
	return false
}

// routeConfig converts the route table to the platform's RouteConfig. Managed
// upstreams get a placeholder service id: only the shape is validated here.
func (c *svcCtx) routeConfig() *services.RouteConfig {
	r := c.def.Route
	cfg := &services.RouteConfig{
		Protocol: derefStr(r.Protocol), Scheme: derefStr(r.Scheme), InsecureSkipVerify: derefBool(r.InsecureSkipVerify),
		ServerName: derefStr(r.ServerName), PassHost: r.PassHost, HealthPath: derefStr(r.HealthPath), VerifySkip: derefBool(r.VerifySkip),
		ListenPort: derefInt(r.ListenPort), AllowCIDRs: r.AllowCIDRs, DenyCIDRs: r.DenyCIDRs,
		OpenToInternet: derefBool(r.OpenToInternet), ProxyProtocol: derefInt(r.ProxyProtocol),
	}
	for _, u := range r.Upstreams {
		up := services.RouteUpstream{Port: derefInt(u.Port)}
		if u.Service != nil {
			id := uuid.New()
			up.ServiceID = &id
		}
		up.Address = derefStr(u.Address)
		cfg.Upstreams = append(cfg.Upstreams, up)
	}
	return cfg
}

func (c *svcCtx) checkStack() {
	st := c.def.Stack
	c.intIn(rp("stack", "wait_seconds"), st.WaitSeconds, 1, 3600)
	for _, comp := range sortedKeys(st.Resources) {
		r := st.Resources[comp]
		if r == nil {
			continue
		}
		if r.MemoryMB != nil && (*r.MemoryMB < 0 || (*r.MemoryMB > 0 && *r.MemoryMB < 6)) {
			c.err(CodeInvalidValue, rp("stack", "resources", comp, "memory_mb"), "memory_mb is 0 (no limit) or at least 6")
		}
		if r.CPUs != nil && !(*r.CPUs >= 0) {
			c.err(CodeInvalidValue, rp("stack", "resources", comp, "cpus"), "cpus must be 0 or more")
		}
	}
	if st.Compose == nil {
		return
	}
	if strings.TrimSpace(*st.Compose) == "" {
		c.err(CodeInvalidValue, rp("stack", "compose"), "the compose text is empty")
		return
	}
	p, err := composestack.Parse([]byte(*st.Compose))
	if err != nil {
		c.err(CodeInvalidValue, rp("stack", "compose"), "the compose file is not valid: %s", err)
		return
	}
	for _, v := range composestack.Check(p, composestack.Policy{Unrestricted: true}) {
		c.err(CodeInvalidValue, rp("stack", "compose"), "the compose file breaks the stack rules: %s", v.String())
	}
	names := map[string]bool{}
	for _, n := range p.Names() {
		names[n] = true
	}
	for _, comp := range sortedKeys(st.Resources) {
		if !names[comp] {
			c.err(CodeInvalidValue, rp("stack", "resources", comp), "stack.resources: %q is not a service of the compose file (services: %s)", comp, strings.Join(p.Names(), ", "))
		}
	}
	for _, key := range sortedKeys(c.def.Domains) {
		dm := c.def.Domains[key]
		if dm != nil && dm.Component != nil && !names[*dm.Component] {
			c.err(CodeInvalidValue, rp("domains", key, "component"), "component %q is not a service of the compose file (services: %s)", *dm.Component, strings.Join(p.Names(), ", "))
		}
	}
}

// platformCheck runs the platform's own config validator as a safety net.
func (c *svcCtx) platformCheck() {
	cfg, src, ok := c.toConfig()
	if !ok {
		return
	}
	if err := cfg.WithDefaults().Validate(src); err != nil {
		c.err(CodeInvalidValue, nil, "%s", fmtErr("the service config is not valid", err))
	}
}

// toConfig builds the platform Config of the service (names stand in for ids).
func (c *svcCtx) toConfig() (services.Config, string, bool) {
	d := c.def
	var cfg services.Config
	if c.kind == "" {
		return cfg, "", false
	}
	cfg.Port = derefInt(d.Port)
	cfg.StartCommand = d.Command
	cfg.OneShot = derefBool(d.OneShot)
	cfg.StopGraceSeconds = derefInt(d.StopGraceSeconds)
	cfg.MemoryMB = derefInt(d.MemoryMB)
	if d.CPUs != nil {
		cfg.CPUs = *d.CPUs
	}
	cfg.ImageRetention = derefInt(d.ImageRetention)
	cfg.BlockEgress = derefBool(d.BlockEgress)
	cfg.ProcessName = derefStr(d.ProcessName)
	cfg.Singleton = derefBool(d.Singleton)
	for _, name := range sortedKeys(d.Processes) {
		if p := d.Processes[name]; p != nil {
			cfg.Processes = append(cfg.Processes, processValue(name, p).Config())
		}
	}
	cfg.Docker = dockerOptions(d.Docker)
	if h := d.HealthCheck; h != nil {
		cfg.Healthcheck = services.Healthcheck{
			Type: derefStr(h.Type), Path: derefStr(h.Path), Port: derefInt(h.Port),
			IntervalSeconds: derefInt(h.IntervalSeconds), TimeoutSeconds: derefInt(h.TimeoutSeconds),
			StartPeriodSeconds: derefInt(h.StartPeriodSeconds), DeadlineSeconds: derefInt(h.DeadlineSeconds),
			SuccessThreshold: derefInt(h.SuccessThreshold), Command: h.Command,
		}
	}
	if h := d.PreDeploy; h != nil {
		cfg.PreDeployCommand, cfg.PreDeployTimeoutSeconds = h.Command, derefInt(h.TimeoutSeconds)
	}
	if h := d.PostDeploy; h != nil {
		cfg.PostDeployCommand, cfg.PostDeployTimeoutSeconds = h.Command, derefInt(h.TimeoutSeconds)
	}
	for _, p := range sortedKeys(d.Files) {
		if f := d.Files[p]; f != nil {
			cfg.Files = append(cfg.Files, services.FileMount{Path: p, Mode: derefStr(f.Mode), Content: derefStr(f.Content)})
		}
	}
	switch c.kind {
	case KindImage:
		cfg.Image = derefStr(d.Image)
		return cfg, services.SourceImage, true
	case KindGit:
		g := d.Git
		cfg.GitURL = derefStr(g.URL)
		if g.URL == nil {
			cfg.GitURL = "https://git.invalid/placeholder.git"
		}
		cfg.GitBranch = derefStr(g.Branch)
		cfg.Submodules = derefStr(g.Submodules)
		if b := d.Build; b != nil {
			cfg.BuildType = derefStr(b.Type)
			cfg.DockerfilePath = derefStr(b.Dockerfile)
			cfg.BuildContext = derefStr(b.Context)
			cfg.BuildLocation = derefStr(b.Location)
		}
		return cfg, services.SourceGit, true
	case KindDatabase:
		spec := engines.Spec{Engine: derefStr(d.Database.Engine), Version: derefStr(d.Database.Version), Flavor: derefStr(d.Database.Flavor), DatabaseName: derefStr(d.Database.Name), Username: derefStr(d.Database.Username)}
		cfg.Database = &spec
		return cfg, services.SourceImage, true
	case KindRoute:
		rc := c.routeConfig()
		cfg.Route = rc
		return cfg, services.SourceExternal, true
	case KindStack:
		if d.Stack.Compose == nil {
			return cfg, "", false
		}
		sc := &services.StackConfig{Compose: *d.Stack.Compose, WaitSeconds: derefInt(d.Stack.WaitSeconds)}
		if len(d.Stack.Resources) > 0 {
			sc.Resources = map[string]agentprotocol.StackResources{}
			for k, r := range d.Stack.Resources {
				if r != nil {
					sc.Resources[k] = agentprotocol.StackResources{MemoryMB: derefInt(r.MemoryMB), CPUs: derefFloat(r.CPUs)}
				}
			}
		}
		cfg.Stack = sc
		cfg.Healthcheck = services.Healthcheck{Type: services.HealthNone}
		return cfg, services.SourceCompose, true
	}
	return cfg, "", false
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
