package coolify

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// registryHostOf returns the registry host of an image reference ("" for Docker Hub).
func registryHostOf(image string) string {
	first, _, found := strings.Cut(image, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return strings.ToLower(first)
	}
	return ""
}

func parsePorts(s string) []int {
	var out []int
	for _, p := range splitList(s) {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 && n < 65536 {
			out = append(out, n)
		}
	}
	return out
}

var githubRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// gitSource works out the clone URL and owner/name of an application's repository.
func gitSource(app *Application) (cloneURL, repo string) {
	repoField := strings.TrimSpace(app.GitRepository)
	full := strings.TrimSpace(app.GitFullURL)
	switch {
	case strings.HasPrefix(full, "http://") || strings.HasPrefix(full, "https://"):
		cloneURL = strings.TrimSuffix(full, ".git")
	case strings.HasPrefix(repoField, "http://") || strings.HasPrefix(repoField, "https://") || strings.HasPrefix(repoField, "git://"):
		cloneURL = strings.TrimSuffix(repoField, ".git")
	case strings.HasPrefix(repoField, "git@") || strings.HasPrefix(repoField, "ssh://"):
		cloneURL = repoField
	case githubRepoRe.MatchString(repoField):
		cloneURL = "https://github.com/" + strings.TrimSuffix(repoField, ".git")
	default:
		cloneURL = repoField
	}
	if u, err := url.Parse(cloneURL); err == nil && (u.Host == "github.com" || u.Host == "gitlab.com") {
		if p := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"); githubRepoRe.MatchString(p) {
			repo = p
		}
	}
	return cloneURL, repo
}

// cleanBase turns Coolify's base directory ("/", "/apps/web") into a relative path.
func cleanBase(p string) string {
	p = strings.TrimSpace(p)
	p = path.Clean("/" + strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "/"))
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "."
	}
	return p
}

// planApplication maps one Coolify application. Variables are built later, once
// every resource of the environment has its slug.
func (a *analyzer) planApplication(app *Application, rc CoolifyResourceChoice) CoolifyPlanResource {
	name := app.Name
	if rc.Name != nil && strings.TrimSpace(*rc.Name) != "" {
		name = strings.TrimSpace(*rc.Name)
	}
	res := CoolifyPlanResource{
		UUID: app.UUID, Kind: "app", Name: name, Include: true, app: app, choice: rc,
		Domains: []CoolifyPlanDomain{}, Volumes: []CoolifyPlanVolume{}, Env: []CoolifyPlanEnv{}, DependsOn: []string{}, Unsupported: []string{}, Notes: []CoolifyNote{},
		NeedsUnrestricted: []string{},
		hosts:             append([]string{strings.ToLower(app.UUID)}, lowerAll(app.NetworkAliases)...),
	}
	exclude := func(why string) CoolifyPlanResource {
		res.Include, res.IncludeNote = false, why
		return res
	}

	// ---- source ----
	switch {
	case app.BuildPack == "dockercompose" || app.DockerCompose && app.GitRepository == "" && app.RegistryImage == "":
		return exclude("A Docker Compose application. Import its Compose file as a stack: nebulactrl import compose <file> --as-stack (the file is on the application's page in Coolify, or in its repository).")
	case app.BuildPack == "dockerimage" || (app.RegistryImage != "" && app.GitRepository == ""):
		img := strings.TrimSpace(app.RegistryImage)
		if img == "" {
			return exclude("The application is a Docker image app but Coolify returned no image name.")
		}
		tag := strings.TrimSpace(app.RegistryTag)
		switch {
		case strings.Contains(img, "@"):
		case strings.HasPrefix(tag, "sha256:"):
			img += "@" + tag
		case tag == "":
			img += ":latest"
			res.addNote("image", SevWarning, "Coolify has no tag for this image; latest is used. Pin a version before deploying.")
		default:
			img += ":" + tag
		}
		res.Source = CoolifyPlanSource{Type: "image", Image: img, Registry: registryHostOf(img)}
		res.registryHost = registryHostOf(img)
		if res.registryHost == "" {
			res.registryHost = "docker.io"
		}
	case app.BuildPack == "dockerfile" && app.GitRepository == "" && app.InlineDockerfile:
		return exclude("The Dockerfile is stored inside Coolify (no repository). Commit it to a git repository and import again, or build and push an image.")
	default:
		cloneURL, repo := gitSource(app)
		if cloneURL == "" {
			return exclude("The application has neither a git repository nor an image.")
		}
		g := &CoolifyPlanGit{URL: cloneURL, Branch: strings.TrimSpace(app.GitBranch), Repo: repo, BuildContext: ".", DockerfilePath: "Dockerfile"}
		if g.Branch == "" {
			g.Branch = "main"
			res.addNote("branch", SevWarning, "Coolify returned no branch; main is used.")
		}
		switch app.BuildPack {
		case "dockerfile":
			g.BuildType = agentprotocol.BuildDockerfile
			base := cleanBase(app.BaseDirectory)
			loc := strings.TrimPrefix(strings.TrimSpace(app.DockerfileLocation), "/")
			if loc == "" {
				loc = "Dockerfile"
			}
			g.BuildContext = base
			g.DockerfilePath = path.Join(base, loc)
			if base != "." {
				res.addNote("dockerfile", SevInfo, "The Dockerfile path is relative to the repository root here (Coolify's is relative to the base directory): "+g.DockerfilePath+", build context "+base+".")
			}
		default: // nixpacks, railpack, static, unknown
			g.BuildType = agentprotocol.BuildRailpack
			g.BuildContext = cleanBase(app.BaseDirectory)
			g.DockerfilePath = ""
			switch app.BuildPack {
			case "nixpacks":
				res.addNote("build pack", SevWarning, "Coolify builds this with Nixpacks; the service uses Railpack, its successor, which detects most of the same projects. Review the build preview before deploying. NIXPACKS_* variables are not read by Railpack: use the RAILPACK_* equivalents.")
			case "static":
				res.addNote("build pack", SevWarning, "A Coolify static site; the service uses Railpack, which detects static sites. Review the build preview before deploying.")
			case "railpack":
			default:
				res.addNote("build pack", SevWarning, fmt.Sprintf("The build pack %q is unknown here; Railpack is used.", app.BuildPack))
			}
		}
		if sha := strings.TrimSpace(app.GitCommitSHA); sha != "" && !strings.EqualFold(sha, "HEAD") {
			res.addNote("git commit", SevWarning, "The application is pinned to commit "+shortValue(sha)+" in Coolify; NebulaCtrl builds the head of the branch. Pin by deploying that commit, or use a branch.")
		}
		switch {
		case app.PrivateKeyID > 0:
			res.addNote("git access", SevWarning, "The repository is read with a Coolify deploy key (private keys are not exported). Add an SSH deploy key in the service settings (Settings → Source) before deploying.")
		case app.SourceID > 0:
			res.addNote("git access", SevWarning, "The repository is read through a Coolify git app. Connect your git provider in NebulaCtrl and select the connection in the import so builds can clone it.")
		}
		res.Source = CoolifyPlanSource{Type: "git", Git: g}
	}

	// ---- port, kind ----
	ports := parsePorts(app.PortsExposes)
	if len(ports) > 0 {
		res.Port = ports[0]
	}
	if len(ports) > 1 {
		res.addNote("ports", SevInfo, fmt.Sprintf("Coolify exposes %d ports; the first (%d) is the service port.", len(ports), ports[0]))
	}
	if rc.Port != nil {
		res.Port = *rc.Port
	}
	if rc.Kind != nil && *rc.Kind == "worker" {
		res.Kind, res.Port = "worker", 0
	}
	if strings.TrimSpace(app.PortsMappings) != "" {
		res.unsupported("port mappings "+app.PortsMappings, "Published host ports are not part of a service: add a TCP/UDP route for the port in the environment.")
	}

	// ---- health check ----
	res.Health = mapHealth(app, &res)

	// ---- resources ----
	if mb, ok := parseSizeMB(app.LimitsMemory); ok {
		res.MemoryMB = mb
		if mb < 6 {
			res.MemoryMB = 6
		}
	}
	if c, err := strconv.ParseFloat(strings.TrimSpace(app.LimitsCPUs), 64); err == nil && c > 0 {
		res.CPUs = c
	}
	if s := strings.TrimSpace(app.LimitsMemorySwap); s != "" && s != "0" {
		res.unsupported("memory swap limit "+s, "Swap limits are not supported.")
	}
	if s := strings.TrimSpace(app.LimitsReservation); s != "" && s != "0" {
		res.unsupported("memory reservation "+s, "Memory reservations are not supported; the memory limit is applied.")
	}
	if s := strings.TrimSpace(app.LimitsCPUSet); s != "" && s != "0" {
		res.unsupported("CPU set "+s, "CPU pinning is not supported.")
	}
	if app.LimitsCPUShares != 0 && app.LimitsCPUShares != 1024 {
		res.unsupported(fmt.Sprintf("CPU shares %d", app.LimitsCPUShares), "CPU shares are not supported; use the CPU limit.")
	}

	// ---- docker options ----
	unrestricted := a.tg.Server != nil && a.tg.Server.Unrestricted
	if !app.SensitiveVisible {
		a.needSensitive = true
	}
	ro := mapRunOptions(app.CustomDockerRunOptions, unrestricted)
	res.Notes = append(res.Notes, ro.Notes...)
	res.Unsupported = append(res.Unsupported, ro.Unsupported...)
	res.Docker = ro.Docker
	res.NeedsUnrestricted = append(res.NeedsUnrestricted, ro.NeedsUnrestricted...)
	if res.MemoryMB == 0 && ro.MemoryMB > 0 {
		res.MemoryMB = ro.MemoryMB
	}
	if res.CPUs == 0 && ro.CPUs > 0 {
		res.CPUs = ro.CPUs
	}
	if res.Docker != nil && len(res.Docker.Entrypoint) > 0 {
		res.Command = nil
	}

	// ---- commands ----
	if argv := commandArgv(app.PreDeployCommand); len(argv) > 0 {
		if err := agentprotocol.ValidTaskCommand(argv); err != nil {
			res.unsupported("pre-deployment command", err.Error())
		} else {
			res.PreDeploy = argv
			res.addNote("pre-deployment command", SevInfo, "Runs once per deployment in a one-shot container of the new image before the old release is touched (a failure keeps the old release serving). Coolify ran it with sh -c in the running container; shell syntax is kept by running under sh -c.")
		}
	}
	if argv := commandArgv(app.PostDeployCommand); len(argv) > 0 {
		if err := agentprotocol.ValidTaskCommand(argv); err != nil {
			res.unsupported("post-deployment command", err.Error())
		} else {
			res.PostDeploy = argv
			res.addNote("post-deployment command", SevInfo, "Runs once per deployment after the new release is live; a failure only warns.")
		}
	}
	if sc := strings.TrimSpace(app.StartCommand); sc != "" && res.Source.Type == "image" {
		res.unsupported("start command", "Coolify's start command is not used for image services here; set the Start command in the service settings if the image needs one.")
	}
	if res.Source.Type == "git" && res.Source.Git.BuildType == agentprotocol.BuildDockerfile {
		for what, v := range map[string]string{"install command": app.InstallCommand, "build command": app.BuildCommand, "start command": app.StartCommand, "publish directory": app.PublishDirectory} {
			if strings.TrimSpace(v) != "" {
				res.addNote(what, SevInfo, "Coolify ignores the "+what+" for Dockerfile builds; it was not imported.")
			}
		}
	}

	// ---- domains ----
	res.Domains = a.planDomains(app, rc, &res)

	// ---- storage, labels, tasks ----
	for _, v := range app.Volumes {
		switch {
		case v.MountPath == "":
		case v.HostPath != "":
			res.unsupported("bind mount "+v.HostPath+" → "+v.MountPath, "Host bind mounts are not supported; copy the files into a named volume.")
		default:
			res.Volumes = append(res.Volumes, CoolifyPlanVolume{Name: volumeName(v.Name, app.UUID), MountPath: v.MountPath})
		}
	}
	for _, f := range app.Files {
		what := "file mount " + f.MountPath
		if f.IsDirectory {
			what = "directory mount " + f.MountPath
		}
		res.unsupported(what, "Coolify file storages are not imported. Recreate small files under Settings → Config files, or bake them into the image.")
	}
	for _, t := range app.Tasks {
		state := ""
		if !t.Enabled {
			state = " (disabled)"
		}
		res.unsupported("scheduled task "+t.Name+" ("+t.Frequency+")"+state, "Add it under Tasks → Cron.")
	}
	if n := countLines(app.CustomLabels); n > 0 {
		res.unsupported(fmt.Sprintf("%d custom label line(s)", n), "Traefik labels are not imported. Per-domain middlewares (compression, IP allow lists, redirects, basic auth) are set under Domains → Rules.")
	}
	if app.BasicAuth {
		res.unsupported("HTTP basic auth", "Set basic auth per domain under Domains → Rules.")
	}
	if r := strings.TrimSpace(app.Redirect); r != "" && r != "both" {
		res.unsupported("www redirect ("+r+")", "Add a redirect rule to the domain under Domains → Rules.")
	}
	return res
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

var volumeNameClean = regexp.MustCompile(`[^a-z0-9._-]+`)

// volumeName turns a Coolify storage name (usually "<app uuid>-<name>") into a volume name.
func volumeName(name, appUUID string) string {
	n := strings.ToLower(strings.TrimPrefix(name, appUUID+"-"))
	n = strings.Trim(volumeNameClean.ReplaceAllString(n, "-"), "-.")
	if n == "" {
		n = "data"
	}
	if len(n) > 63 {
		n = strings.Trim(n[:63], "-.")
	}
	return n
}

// mapHealth maps Coolify's health check. Coolify runs every check inside the
// container: an HTTP check is curl (or wget) against
// <scheme>://<health_check_host>:<port><path>, by default http://localhost.
// The import runs that same command as an exec check rather than probing the
// container from outside: frameworks with a host allow list (Laravel's trusted
// hosts, Django's ALLOWED_HOSTS, Rails host authorization) answer an outside
// probe, whose Host is the container address, with 400, while they accept
// localhost. The image is known to have curl or wget: Coolify's check passed.
func mapHealth(app *Application, res *CoolifyPlanResource) *CoolifyPlanHealth {
	h := app.Health
	deadline := func() int {
		d := h.StartPeriod + h.Retries*max(h.Interval, 1)
		if d < 30 {
			d = 30
		}
		if d > 3600 {
			d = 3600
		}
		return d
	}
	switch {
	case h.Enabled && h.Type == "cmd" && strings.TrimSpace(h.Command) != "":
		return &CoolifyPlanHealth{Type: "exec", command: []string{"CMD-SHELL", strings.TrimSpace(h.Command)}, IntervalSeconds: max(h.Interval, 1), TimeoutSeconds: max(h.Timeout, 1), StartPeriodSeconds: h.StartPeriod, DeadlineSeconds: deadline()}
	case h.Enabled:
		port := res.Port
		if n, err := strconv.Atoi(strings.TrimSpace(h.Port)); err == nil && n > 0 {
			port = n
		}
		if port == 0 {
			res.addNote("health check", SevWarning, "The health check needs a port and the service has none; the check was set to none.")
			return &CoolifyPlanHealth{Type: "none"}
		}
		if m := strings.ToUpper(h.Method); m != "" && m != "GET" {
			res.addNote("health check", SevWarning, "The health check method "+m+" is not supported; GET is used.")
		}
		if strings.TrimSpace(h.ResponseText) != "" {
			res.addNote("health check", SevWarning, "The expected response text is not checked.")
		}
		if h.ReturnCode != 0 && h.ReturnCode != 200 {
			res.addNote("health check", SevWarning, fmt.Sprintf("Coolify expects status %d; like Coolify's own check, any non-error status passes.", h.ReturnCode))
		}
		return &CoolifyPlanHealth{
			Type: "exec", command: []string{"CMD-SHELL", healthCommand(healthURL(h, port))},
			IntervalSeconds: max(h.Interval, 1), TimeoutSeconds: max(h.Timeout, 1), StartPeriodSeconds: h.StartPeriod, DeadlineSeconds: deadline(),
		}
	case app.CustomHealthcheckFound:
		res.addNote("health check", SevInfo, "No Coolify health check, but the image defines HEALTHCHECK: its status is used.")
		return &CoolifyPlanHealth{Type: "docker"}
	}
	res.addNote("health check", SevInfo, "Coolify has no health check for this application, so none is set (the release counts as healthy once it has run 10 seconds without restarting). Add an HTTP check in the service settings for zero-downtime deploys you can trust.")
	return &CoolifyPlanHealth{Type: "none"}
}

// healthURL is the address Coolify's HTTP check requests inside the container.
func healthURL(h Health, port int) string {
	scheme := "http"
	if strings.EqualFold(h.Scheme, "https") {
		scheme = "https"
	}
	host := strings.TrimSpace(h.Host)
	if host == "" {
		host = "localhost"
	}
	p := strings.TrimSpace(h.Path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + p
}

// healthCommand is Coolify's own in-container check: curl, else wget; an
// error status fails it.
func healthCommand(target string) string {
	q := shellQuote(target)
	return "curl -fsk -o /dev/null " + q + " || wget -q --no-check-certificate -O /dev/null " + q
}

// shellQuote single-quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var autoDomainRe = regexp.MustCompile(`\.(sslip\.io|nip\.io|traefik\.me)$`)

// planDomains maps every fqdn of an application.
func (a *analyzer) planDomains(app *Application, rc CoolifyResourceChoice, res *CoolifyPlanResource) []CoolifyPlanDomain {
	out := []CoolifyPlanDomain{}
	compress := strings.Contains(strings.ToLower(app.CustomLabels), "compress") || strings.Contains(strings.ToLower(app.CustomLabels), "gzip")
	if compress {
		res.addNote("compression", SevInfo, "The Coolify labels enable response compression; it is switched on for the domains.")
	}
	noindex := map[string]bool{}
	for _, n := range app.NoindexDomains {
		noindex[strings.ToLower(strings.TrimSpace(n))] = true
	}
	seen := map[string]bool{}
	for _, raw := range splitList(app.FQDN) {
		d := CoolifyPlanDomain{Enabled: true, ForceHTTPS: true, Compress: compress}
		s := raw
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			d.Host, d.Enabled, d.Note = raw, false, "The domain could not be read."
			out = append(out, d)
			continue
		}
		d.Host = strings.ToLower(u.Hostname())
		if u.Scheme == "http" {
			d.ForceHTTPS = false
			d.Note = "Served over plain http in Coolify; plain http stays allowed."
		}
		if p := u.Port(); p != "" {
			d.Port, _ = strconv.Atoi(p)
		}
		if p := strings.TrimRight(u.Path, "/"); p != "" {
			d.Path = p
		}
		d.Noindex = noindex[d.Host] || noindex[strings.ToLower(strings.TrimSpace(raw))]
		if seen[d.Host+d.Path] {
			continue
		}
		seen[d.Host+d.Path] = true
		if autoDomainRe.MatchString(d.Host) {
			d.Generate, d.Note = true, "An auto-generated Coolify address; a generated NebulaCtrl hostname replaces it."
		} else if h, err := domains.ValidateHostname(d.Host); err != nil {
			d.Enabled, d.Note = false, err.Error()
		} else {
			d.Host = h
		}
		if c, ok := rc.Domains[d.Host]; ok {
			if c.Enabled != nil {
				d.Enabled = *c.Enabled
			}
			if c.Host != nil && strings.TrimSpace(*c.Host) != "" {
				h, err := domains.ValidateHostname(*c.Host)
				if err != nil {
					d.Enabled, d.Note = false, err.Error()
				} else {
					d.Host, d.Generate = h, false
				}
			}
		}
		if d.Enabled && !d.Generate && a.tg.HostTaken != nil && a.tg.HostTaken(d.Host) {
			d.Conflict, d.Enabled = true, false
			d.Note = "The hostname is already used by a NebulaCtrl service (an earlier import?); the domain was left out."
		}
		out = append(out, d)
	}
	return out
}
