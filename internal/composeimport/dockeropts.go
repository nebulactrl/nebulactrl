package composeimport

import (
	"fmt"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// dockerPieces are the container options a Compose service asks for that map
// to NebulaCtrl's Docker options. Each piece is validated alone, so one refused
// value never takes the others down. Pieces that need an unrestricted server
// (devices, gpus, runtime, privileged, pid: host, unconfined seccomp or
// AppArmor, NET_ADMIN and SYS_ADMIN) are parsed like the rest and judged
// against the target server's policy in build, never dropped silently.
type dockerPieces struct {
	caps       []string
	capDrop    []string
	ulimits    []agentprotocol.Ulimit
	shmMB      int
	hosts      []string
	init       bool
	stopSignal string
	tmpfs      []agentprotocol.TmpfsMount
	entrypoint []string

	noNewPrivileges    bool
	seccompUnconfined  bool
	apparmorUnconfined bool
	readOnly           bool
	pidsLimit          int
	hostname           string
	dns                []string
	sysctls            []sysctl
	user               string
	groupAdd           []string
	devices            []agentprotocol.DeviceMapping
	gpus               *agentprotocol.GPURequest
	// gpusKey is the Compose key the GPU request came from, for notes.
	gpusKey    string
	runtime    string
	privileged bool
	pidHost    bool
	// notes are what parsing could not map; build reports them with the rest,
	// so every note of a service carries the same key prefix.
	notes []dockerNote
}

// dockerNote is a review note of a Compose key, relative to the service.
type dockerNote struct{ key, sev, msg string }

// sysctl is one kernel parameter of a service.
type sysctl struct{ Key, Value string }

// dockerTarget is what the Docker options are judged against: the policy of
// the server the services will run on and the files and volumes of the service.
type dockerTarget struct {
	// Unrestricted is the server's switch (DockerPolicy.Unrestricted). False
	// also covers an import where no server was chosen yet.
	Unrestricted bool
	// FileTargets are config file paths (a tmpfs must not cover them).
	FileTargets []string
	// VolumeMounts are the service's volume paths (a read-only root filesystem
	// needs every config file on one of them).
	VolumeMounts []string
}

// parseDockerPieces reads cap_add, cap_drop, security_opt, read_only,
// pids_limit, hostname, dns, sysctls, user, group_add, devices, gpus and
// deploy.resources.reservations.devices, runtime, privileged, pid, ulimits,
// shm_size, extra_hosts, init, stop_signal and tmpfs of a service (the
// entrypoint is parsed with the command). What it cannot map it reports.
func parseDockerPieces(m map[string]any) dockerPieces {
	var p dockerPieces
	note := func(k, sev, msg string) { p.notes = append(p.notes, dockerNote{k, sev, msg}) }
	p.caps = stringList(m["cap_add"])
	p.capDrop = stringList(m["cap_drop"])
	switch u := m["ulimits"].(type) {
	case map[string]any, map[any]any:
		um := asMap(u)
		for _, name := range sortedKeys(um) {
			var soft, hard int
			if n, ok := toInt(um[name]); ok {
				soft, hard = n, n
			} else if lm := asMap(um[name]); lm != nil {
				soft, _ = toInt(lm["soft"])
				hard, _ = toInt(lm["hard"])
			} else {
				note("ulimits."+name, SevInfo, "The ulimit "+name+" could not be read and was skipped.")
				continue
			}
			p.ulimits = append(p.ulimits, agentprotocol.Ulimit{Name: name, Soft: int64(soft), Hard: int64(hard)})
		}
	}
	if v, ok := m["shm_size"]; ok {
		if mb, ok := parseMemoryMB(v); ok {
			p.shmMB = mb
		}
	}
	switch h := m["extra_hosts"].(type) {
	case []any:
		for _, e := range h {
			s, _ := scalar(e)
			if name, ip, ok := strings.Cut(s, "="); ok {
				s = name + ":" + ip
			}
			p.hosts = append(p.hosts, s)
		}
	case map[string]any, map[any]any:
		hm := asMap(h)
		for _, name := range sortedKeys(hm) {
			ip, _ := scalar(hm[name])
			p.hosts = append(p.hosts, name+":"+ip)
		}
	}
	p.init = truthy(m["init"])
	p.stopSignal = asStr(m["stop_signal"])
	for _, t := range stringList(m["tmpfs"]) {
		pth, opts, _ := strings.Cut(t, ":")
		tm := agentprotocol.TmpfsMount{Path: pth}
		for _, o := range strings.Split(opts, ",") {
			k, v, _ := strings.Cut(o, "=")
			switch k {
			case "size":
				if mb, ok := parseMemoryMB(v); ok {
					tm.SizeMB = mb
				}
			case "mode":
				tm.Mode = strings.TrimLeft(v, "0")
				if tm.Mode == "" {
					tm.Mode = "0"
				}
			}
		}
		p.tmpfs = append(p.tmpfs, tm)
	}

	parseSecurityOpt(m["security_opt"], &p, note)
	p.readOnly = truthy(m["read_only"])
	if n, ok := toInt(m["pids_limit"]); ok && n > 0 {
		p.pidsLimit = n
	}
	p.hostname = strings.ToLower(strings.TrimSpace(asStr(m["hostname"])))
	p.dns = stringList(m["dns"])
	p.sysctls = parseSysctls(m["sysctls"])
	p.user = strings.TrimSpace(asStr(m["user"]))
	p.groupAdd = stringList(m["group_add"])
	p.devices = parseDevices(m["devices"], note)
	parseGPUs(m, &p, note)
	p.runtime = strings.TrimSpace(asStr(m["runtime"]))
	p.privileged = truthy(m["privileged"])
	switch pid := strings.TrimSpace(asStr(m["pid"])); pid {
	case "", "private":
	case "host":
		p.pidHost = true
	default:
		note("pid", SevDanger, fmt.Sprintf("pid: %s is not applied: only pid: host (on an unrestricted server) is supported; services never join another container's PID namespace.", pid))
	}
	return p
}

// parseSecurityOpt maps no-new-privileges to the safe option and
// seccomp/apparmor "unconfined" to the unrestricted ones. Profiles, SELinux
// labels and everything else are reported as not applied.
func parseSecurityOpt(v any, p *dockerPieces, note func(k, sev, msg string)) {
	for _, o := range stringList(v) {
		o = strings.TrimSpace(o)
		k, val := o, ""
		if i := strings.IndexAny(o, "=:"); i >= 0 {
			k, val = o[:i], o[i+1:]
		}
		switch k {
		case "no-new-privileges":
			switch strings.ToLower(val) {
			case "", "true":
				p.noNewPrivileges = true
			case "false": // Docker's default
			default:
				note("security_opt", SevWarning, fmt.Sprintf("Not applied: %q is not true or false.", o))
			}
		case "seccomp", "apparmor":
			switch {
			case val == "unconfined" && k == "seccomp":
				p.seccompUnconfined = true
			case val == "unconfined":
				p.apparmorUnconfined = true
			default:
				note("security_opt", SevWarning, fmt.Sprintf("Not applied: custom %s profiles (%s) are not supported; only the default profile or unconfined (which needs an unrestricted server).", k, shortValue(val)))
			}
		default:
			note("security_opt", SevWarning, fmt.Sprintf("Not applied: the security option %q is not supported (SELinux labels and systempaths are never applied).", shortValue(o)))
		}
	}
}

func shortValue(s string) string {
	if len(s) > 40 {
		return s[:37] + "..."
	}
	return s
}

// parseSysctls reads sysctls in map or "key=value" list form, in a stable order.
func parseSysctls(v any) []sysctl {
	var out []sysctl
	switch x := v.(type) {
	case map[string]any, map[any]any:
		m := asMap(x)
		for _, k := range sortedKeys(m) {
			val, _ := scalar(m[k])
			out = append(out, sysctl{k, val})
		}
	case []any:
		for _, e := range x {
			s, _ := scalar(e)
			k, val, _ := strings.Cut(s, "=")
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, sysctl{k, strings.TrimSpace(val)})
			}
		}
	}
	return out
}

// parseDevices reads devices in short ("/dev/a:/dev/b:rwm", "/dev/a:rw") and
// long (source, target, permissions) form. CDI names are reported.
func parseDevices(v any, note func(k, sev, msg string)) []agentprotocol.DeviceMapping {
	var out []agentprotocol.DeviceMapping
	for _, e := range asList(v) {
		var d agentprotocol.DeviceMapping
		var raw string
		if s, ok := e.(string); ok {
			raw = s
			parts := strings.Split(s, ":")
			d.HostPath = strings.TrimSpace(parts[0])
			switch len(parts) {
			case 1:
			case 2:
				if strings.HasPrefix(parts[1], "/") {
					d.ContainerPath = parts[1]
				} else {
					d.Permissions = parts[1]
				}
			case 3:
				d.ContainerPath, d.Permissions = parts[1], parts[2]
			default:
				note("devices", SevDanger, fmt.Sprintf("The device %q could not be read and was not applied.", s))
				continue
			}
		} else if m := asMap(e); m != nil {
			d = agentprotocol.DeviceMapping{HostPath: asStr(m["source"]), ContainerPath: asStr(m["target"]), Permissions: asStr(m["permissions"])}
			raw = d.HostPath
		} else {
			continue
		}
		if !strings.HasPrefix(d.HostPath, "/") {
			note("devices", SevDanger, fmt.Sprintf("Not applied: %q is not a device path (CDI device names are not supported).", raw))
			continue
		}
		out = append(out, d)
	}
	return out
}

// parseGPUs reads the top-level gpus key and the GPU entries of
// deploy.resources.reservations.devices. Only the first request is applied.
func parseGPUs(m map[string]any, p *dockerPieces, note func(k, sev, msg string)) {
	take := func(key string, g *agentprotocol.GPURequest) {
		if p.gpus != nil {
			note(key, SevWarning, "Not applied: only one GPU request is supported, and "+p.gpusKey+" was used.")
			return
		}
		p.gpus, p.gpusKey = g, key
	}
	// gpu reads count, driver and capabilities of one request. gpu itself is the
	// Compose device capability, not a driver capability.
	gpu := func(key string, rm map[string]any) {
		if len(stringList(rm["device_ids"])) > 0 {
			note(key, SevWarning, "Not applied: device_ids pick particular GPUs, which is not supported; use a count.")
			return
		}
		g := &agentprotocol.GPURequest{Count: -1, Driver: strings.ToLower(strings.TrimSpace(asStr(rm["driver"])))}
		if g.Driver == "" {
			g.Driver = "nvidia"
		}
		switch c := strings.ToLower(strings.TrimSpace(asStr(rm["count"]))); c {
		case "", "all":
		default:
			n, ok := toInt(c)
			if !ok {
				note(key, SevWarning, fmt.Sprintf("Not applied: the GPU count %q is not a number or all.", c))
				return
			}
			g.Count = n
		}
		for _, c := range stringList(rm["capabilities"]) {
			if c = strings.ToLower(strings.TrimSpace(c)); c != "gpu" {
				g.Capabilities = append(g.Capabilities, c)
			}
		}
		take(key, g)
	}

	switch g := m["gpus"].(type) {
	case nil:
	case string, int, int64, uint64, float64:
		gpu("gpus", map[string]any{"count": g})
	case []any:
		for _, e := range g {
			if em := asMap(e); em != nil {
				gpu("gpus", em)
			} else {
				gpu("gpus", map[string]any{"count": e})
			}
		}
	default:
		note("gpus", SevWarning, "Not applied: the gpus value could not be read.")
	}

	const resKey = "deploy.resources.reservations.devices"
	for _, e := range asList(asMap(asMap(asMap(m["deploy"])["resources"])["reservations"])["devices"]) {
		em := asMap(e)
		isGPU := false
		for _, c := range stringList(em["capabilities"]) {
			isGPU = isGPU || strings.EqualFold(strings.TrimSpace(c), "gpu")
		}
		if !isGPU {
			note(resKey, SevWarning, "Not applied: only device reservations with the gpu capability are supported.")
			continue
		}
		gpu(resKey, em)
	}
}

// build validates every piece on its own against the target's policy and
// returns the options that pass. A piece that is well-formed but needs an
// unrestricted server on a restricted target is reported ("Needs an
// unrestricted server") and left out; one that is malformed or unsupported is
// reported with the reason.
func (p dockerPieces) build(tg dockerTarget, note func(k, sev, msg string)) *agentprotocol.DockerOptions {
	var out agentprotocol.DockerOptions
	for _, n := range p.notes {
		note(n.key, n.sev, n.msg)
	}

	// ok checks the format of a piece (as if the server were unrestricted),
	// then the server policy. danger raises the severity of a refusal for
	// options that mean host root.
	ok := func(piece agentprotocol.DockerOptions, what, hint string, danger bool) bool {
		if err := piece.Validate(true); err != nil {
			note(what, SevWarning, fmt.Sprintf("Not applied: %v.%s", err, hint))
			return false
		}
		f := piece.UnrestrictedFields()
		if len(f) == 0 {
			return true
		}
		list := strings.Join(f, ", ")
		if tg.Unrestricted {
			note(what, SevWarning, "Applied because the target server is unrestricted: "+list+" gives the container access to the host. Review it before deploying.")
			return true
		}
		sev := SevWarning
		if danger {
			sev = SevDanger
		}
		note(what, sev, "Needs an unrestricted server: "+list+" is not applied. An instance admin has to switch the target server to Unrestricted (Servers → the server); import again then, or set it in the service settings.")
		return false
	}

	seen := map[string]bool{}
	for _, c := range p.caps {
		n := agentprotocol.NormalizeCapability(c)
		if seen["add "+n] {
			continue
		}
		seen["add "+n] = true
		if ok(agentprotocol.DockerOptions{CapAdd: []string{c}}, "cap_add."+c, " Capabilities are limited to a safe list; NET_ADMIN and SYS_ADMIN need an unrestricted server.", false) {
			out.CapAdd = append(out.CapAdd, c)
		}
	}
	for _, c := range p.capDrop {
		n := agentprotocol.NormalizeCapability(c)
		switch {
		case seen["drop "+n]:
			continue
		case seen["add "+n]:
			note("cap_drop."+c, SevWarning, "Not applied: "+n+" is also listed in cap_add.")
			continue
		}
		seen["drop "+n] = true
		if ok(agentprotocol.DockerOptions{CapDrop: []string{c}}, "cap_drop."+c, "", false) {
			out.CapDrop = append(out.CapDrop, c)
		}
	}
	out.NoNewPrivileges = p.noNewPrivileges
	if p.seccompUnconfined && ok(agentprotocol.DockerOptions{SeccompUnconfined: true}, "security_opt", "", true) {
		out.SeccompUnconfined = true
	}
	if p.apparmorUnconfined && ok(agentprotocol.DockerOptions{ApparmorUnconfined: true}, "security_opt", "", true) {
		out.ApparmorUnconfined = true
	}
	if p.readOnly {
		piece := agentprotocol.DockerOptions{ReadOnlyRootfs: true}
		if err := piece.ValidateFiles(tg.FileTargets, tg.VolumeMounts); err != nil {
			note("read_only", SevWarning, "Not applied: "+err.Error()+".")
		} else {
			out.ReadOnlyRootfs = true
		}
	}
	if p.pidsLimit > 0 && ok(agentprotocol.DockerOptions{PidsLimit: int64(p.pidsLimit)}, "pids_limit", "", false) {
		out.PidsLimit = int64(p.pidsLimit)
	}
	if p.hostname != "" && ok(agentprotocol.DockerOptions{Hostname: p.hostname}, "hostname", " The hostname is a single label; the service name is the network alias.", false) {
		out.Hostname = p.hostname
	}
	for _, d := range p.dns {
		switch {
		case seen["dns "+d]:
		case len(out.DNS) >= agentprotocol.MaxDockerDNS:
			note("dns", SevWarning, fmt.Sprintf("Not applied: %s (at most %d dns servers).", d, agentprotocol.MaxDockerDNS))
		case ok(agentprotocol.DockerOptions{DNS: []string{d}}, "dns", "", false):
			out.DNS = append(out.DNS, d)
		}
		seen["dns "+d] = true
	}
	for _, s := range p.sysctls {
		switch {
		case len(out.Sysctls) >= agentprotocol.MaxDockerSysctls:
			note("sysctls."+s.Key, SevWarning, fmt.Sprintf("Not applied: at most %d sysctls.", agentprotocol.MaxDockerSysctls))
		case ok(agentprotocol.DockerOptions{Sysctls: map[string]string{s.Key: s.Value}}, "sysctls."+s.Key, "", false):
			if out.Sysctls == nil {
				out.Sysctls = map[string]string{}
			}
			out.Sysctls[s.Key] = s.Value
		}
	}
	if p.user != "" && ok(agentprotocol.DockerOptions{User: p.user}, "user", "", false) {
		out.User = p.user
	}
	for _, g := range p.groupAdd {
		switch {
		case seen["group "+g]:
		case len(out.GroupAdd) >= agentprotocol.MaxDockerGroupAdd:
			note("group_add", SevWarning, fmt.Sprintf("Not applied: %s (at most %d groups).", g, agentprotocol.MaxDockerGroupAdd))
		case ok(agentprotocol.DockerOptions{GroupAdd: []string{g}}, "group_add", " Only numeric group IDs are supported, not group names.", false):
			out.GroupAdd = append(out.GroupAdd, g)
		}
		seen["group "+g] = true
	}
	for _, d := range p.devices {
		cp := d.ContainerPath
		if cp == "" {
			cp = d.HostPath
		}
		switch {
		case seen["device "+cp]:
			note("devices", SevWarning, "Not applied: "+cp+" is mapped twice.")
		case len(out.Devices) >= agentprotocol.MaxDockerDevices:
			note("devices", SevWarning, fmt.Sprintf("Not applied: %s (at most %d devices).", d.HostPath, agentprotocol.MaxDockerDevices))
		case ok(agentprotocol.DockerOptions{Devices: []agentprotocol.DeviceMapping{d}}, "devices", "", true):
			out.Devices = append(out.Devices, d)
		}
		seen["device "+cp] = true
	}
	if p.gpus != nil && ok(agentprotocol.DockerOptions{GPUs: p.gpus}, p.gpusKey, " GPUs are requested through the NVIDIA Container Toolkit.", false) {
		out.GPUs = p.gpus
	}
	switch {
	case p.runtime == "":
	case strings.EqualFold(p.runtime, "runc"):
		note("runtime", SevInfo, "runtime: runc is Docker's default runtime; nothing to apply.")
	case ok(agentprotocol.DockerOptions{Runtime: p.runtime}, "runtime", "", false):
		out.Runtime = p.runtime
	}
	if p.privileged && ok(agentprotocol.DockerOptions{Privileged: true}, "privileged", "", true) {
		out.Privileged = true
	}
	if p.pidHost && ok(agentprotocol.DockerOptions{PidHost: true}, "pid", "", true) {
		out.PidHost = true
	}

	for _, u := range p.ulimits {
		if ok(agentprotocol.DockerOptions{Ulimits: []agentprotocol.Ulimit{u}}, "ulimits."+u.Name, "", false) {
			out.Ulimits = append(out.Ulimits, u)
		}
	}
	if p.shmMB > 0 && ok(agentprotocol.DockerOptions{ShmSizeMB: p.shmMB}, "shm_size", "", false) {
		out.ShmSizeMB = p.shmMB
	}
	for _, h := range p.hosts {
		if ok(agentprotocol.DockerOptions{ExtraHosts: []string{h}}, "extra_hosts", "", false) {
			out.ExtraHosts = append(out.ExtraHosts, h)
		}
	}
	out.Init = p.init
	if len(p.entrypoint) > 0 && ok(agentprotocol.DockerOptions{Entrypoint: p.entrypoint}, "entrypoint", "", false) {
		out.Entrypoint = p.entrypoint
		note("entrypoint", SevInfo, "The entrypoint override is applied. Like Compose, it replaces the image's entrypoint and its default command, so the start command below is passed to it as arguments.")
	}
	if p.stopSignal != "" && ok(agentprotocol.DockerOptions{StopSignal: p.stopSignal}, "stop_signal", "", false) {
		out.StopSignal = p.stopSignal
	}
	for _, t := range p.tmpfs {
		collides := false
		for _, f := range tg.FileTargets {
			if f == t.Path {
				collides = true
			}
		}
		if collides {
			note("tmpfs", SevWarning, "Not applied: "+t.Path+" is also a config file mount.")
			continue
		}
		if ok(agentprotocol.DockerOptions{Tmpfs: []agentprotocol.TmpfsMount{t}}, "tmpfs", "", false) {
			out.Tmpfs = append(out.Tmpfs, t)
		}
	}
	if err := out.Validate(tg.Unrestricted); err != nil {
		note("docker", SevWarning, "The container options were not applied: "+err.Error()+".")
		return nil
	}
	return out.Normalized()
}

func (p dockerPieces) zero() bool {
	return len(p.caps) == 0 && len(p.notes) == 0 && len(p.capDrop) == 0 && len(p.ulimits) == 0 && p.shmMB == 0 && len(p.hosts) == 0 && !p.init &&
		p.stopSignal == "" && len(p.tmpfs) == 0 && len(p.entrypoint) == 0 && !p.noNewPrivileges && !p.seccompUnconfined &&
		!p.apparmorUnconfined && !p.readOnly && p.pidsLimit == 0 && p.hostname == "" && len(p.dns) == 0 && len(p.sysctls) == 0 &&
		p.user == "" && len(p.groupAdd) == 0 && len(p.devices) == 0 && p.gpus == nil && p.runtime == "" && !p.privileged && !p.pidHost
}
