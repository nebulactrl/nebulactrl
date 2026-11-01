package coolify

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Severities of a review note.
const (
	SevInfo    = "info"
	SevWarning = "warning"
	SevDanger  = "danger"
)

// CoolifyNote is a review note about one thing the importer mapped, changed or could not map.
type CoolifyNote struct {
	Key      string `json:"key" doc:"What the note is about: a field, option or variable name"`
	Severity string `json:"severity" enum:"info,warning,danger"`
	Message  string `json:"message"`
}

// runFlag is one option of a docker run command line.
type runFlag struct {
	Name  string // long name without dashes
	Value string
}

// runBools are the flags that take no value; a space-separated word after
// them is not their value (docker run semantics).
var runBools = map[string]bool{
	"init": true, "privileged": true, "read-only": true, "rm": true, "detach": true, "interactive": true, "tty": true,
	"oom-kill-disable": true, "no-healthcheck": true, "publish-all": true, "sig-proxy": true, "quiet": true,
}

var runShort = map[byte]string{
	'u': "user", 'h': "hostname", 'm': "memory", 'c': "cpu-shares", 'e': "env", 'v': "volume", 'p': "publish", 'l': "label",
	'w': "workdir", 'i': "interactive", 't': "tty", 'd': "detach", 'P': "publish-all", 'q': "quiet",
}

// parseRunFlags reads the flags of a `docker run` option string. Words that are
// not flags (an image or a command pasted by mistake) are returned separately.
func parseRunFlags(s string) (flags []runFlag, stray []string, err error) {
	words, err := shellWords(s)
	if err != nil {
		return nil, nil, err
	}
	isFlag := func(w string) bool { return len(w) > 1 && w[0] == '-' && !isNumber(w) }
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case !isFlag(w):
			stray = append(stray, w)
		case strings.HasPrefix(w, "--"):
			name, val, inline := strings.Cut(w[2:], "=")
			if !runBools[name] && !inline && i+1 < len(words) && !isFlag(words[i+1]) {
				val = words[i+1]
				i++
			}
			flags = append(flags, runFlag{Name: name, Value: val})
		default:
			// Short flags: -it, -u 1000, -u=1000, -m512m.
			body := w[1:]
			for j := 0; j < len(body); j++ {
				name, ok := runShort[body[j]]
				if !ok {
					flags = append(flags, runFlag{Name: "-" + string(body[j])})
					continue
				}
				if runBools[name] {
					flags = append(flags, runFlag{Name: name})
					continue
				}
				val := strings.TrimPrefix(body[j+1:], "=")
				if val == "" && i+1 < len(words) && !isFlag(words[i+1]) {
					val = words[i+1]
					i++
				}
				flags = append(flags, runFlag{Name: name, Value: val})
				break
			}
		}
	}
	return flags, stray, nil
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// runUnsupported are options NebulaCtrl never takes from a user (or that
// belong elsewhere), with the reason shown in the review.
var runUnsupported = map[string]string{
	"network": "networks are managed by the platform (environment network, shared networks)", "net": "networks are managed by the platform",
	"restart": "services always restart unless stopped", "publish": "ports are published with TCP/UDP routes or domains", "expose": "the service port is set in the service settings",
	"volume": "use Volumes in the service settings", "mount": "use Volumes in the service settings", "env": "use Variables", "env-file": "use Variables",
	"label": "container labels are managed by the platform", "name": "container names are managed by the platform",
	"health-cmd": "set the health check in the service settings", "health-interval": "set the health check in the service settings",
	"health-timeout": "set the health check in the service settings", "health-retries": "set the health check in the service settings",
	"health-start-period": "set the health check in the service settings", "no-healthcheck": "set the health check type to none in the service settings",
	"log-driver": "logging is managed by the platform", "log-opt": "logging is managed by the platform",
	"ip": "fixed container addresses are not supported", "ip6": "fixed container addresses are not supported", "mac-address": "not supported",
	"cgroup-parent": "not supported", "cpuset-cpus": "CPU pinning is not supported", "cpu-shares": "CPU shares are not supported (use the CPU limit)",
	"memory-swap": "swap limits are not supported", "memory-reservation": "memory reservations are not supported", "oom-kill-disable": "not supported",
	"oom-score-adj": "not supported", "ipc": "IPC namespaces are not shared", "uts": "not supported", "userns": "not supported", "cgroupns": "not supported",
	"workdir": "the working directory comes from the image", "link": "legacy links are not supported (services reach each other by name)",
	"dns-search": "not supported", "dns-option": "not supported", "stop-timeout": "set Stop grace period in the service settings",
	"interactive": "interactive containers (stdin/tty) are not supported", "tty": "interactive containers (stdin/tty) are not supported",
	"cpu-period": "not supported", "cpu-quota": "not supported", "blkio-weight": "not supported", "device-read-bps": "not supported", "device-write-bps": "not supported",
	"security-opt": "", "storage-opt": "not supported", "volumes-from": "not supported", "publish-all": "not supported", "domainname": "not supported",
	"platform": "not supported", "pull": "not supported", "isolation": "not supported", "kernel-memory": "not supported", "annotation": "not supported",
}

// runIgnorable flags change nothing for a managed container.
var runIgnorable = map[string]bool{"rm": true, "detach": true, "quiet": true, "sig-proxy": true}

// runResult is what a docker run option string maps to.
type runResult struct {
	Docker *agentprotocol.DockerOptions
	// MemoryMB and CPUs come from --memory and --cpus; the caller uses them
	// only when the application has no limit of its own.
	MemoryMB int
	CPUs     float64
	Notes    []CoolifyNote
	// Unsupported lists the options that were not applied, with the reason.
	Unsupported []string
	// NeedsUnrestricted names what needs an unrestricted server, applied or not.
	NeedsUnrestricted []string
}

// mapRunOptions maps Coolify's custom_docker_run_options to typed Docker
// options. Each option is validated on its own against the target server's
// policy, so one refusal never takes the others down. Nothing is dropped
// silently: whatever is not applied is listed with the reason.
func mapRunOptions(raw string, unrestricted bool) runResult {
	var res runResult
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return res
	}
	note := func(key, sev, msg string) {
		res.Notes = append(res.Notes, CoolifyNote{Key: key, Severity: sev, Message: msg})
	}
	unsupported := func(flag, why string) {
		msg := "--" + flag
		if why != "" {
			msg += ": " + why
		}
		res.Unsupported = append(res.Unsupported, msg)
		note("docker run options", SevWarning, "Not imported: "+msg+".")
	}
	flags, stray, err := parseRunFlags(raw)
	if err != nil {
		note("docker run options", SevWarning, "The custom Docker options could not be read ("+err.Error()+") and were not imported. Set the options you need in Settings → Docker options.")
		res.Unsupported = append(res.Unsupported, "all options: "+err.Error())
		return res
	}
	for _, s := range stray {
		note("docker run options", SevInfo, fmt.Sprintf("The word %q is not an option and was ignored.", shortValue(s)))
	}

	var out agentprotocol.DockerOptions
	// ok validates one piece as if the server were unrestricted, then applies the server's policy.
	ok := func(piece agentprotocol.DockerOptions, what string, danger bool) bool {
		if err := piece.Validate(true); err != nil {
			note(what, SevWarning, fmt.Sprintf("Not applied: %v.", err))
			res.Unsupported = append(res.Unsupported, what+": "+err.Error())
			return false
		}
		f := piece.UnrestrictedFields()
		if len(f) == 0 {
			return true
		}
		res.NeedsUnrestricted = append(res.NeedsUnrestricted, f...)
		if unrestricted {
			note(what, SevWarning, "Applied because the target server is unrestricted: "+strings.Join(f, ", ")+" gives the container access to the host. Review it before deploying.")
			return true
		}
		sev := SevWarning
		if danger {
			sev = SevDanger
		}
		note(what, sev, "Needs an unrestricted server: "+strings.Join(f, ", ")+" is not applied. An instance admin has to switch the target server to Unrestricted (Servers → the server) and the import has to be run again, or set it in the service settings afterwards.")
		res.Unsupported = append(res.Unsupported, what+": needs an unrestricted server")
		return false
	}

	seen := map[string]bool{}
	for _, f := range flags {
		name, val := f.Name, strings.TrimSpace(f.Value)
		if runIgnorable[name] {
			continue
		}
		needVal := func() bool {
			if val == "" {
				note("--"+name, SevWarning, "The option has no value and was ignored.")
				return false
			}
			return true
		}
		switch name {
		case "entrypoint":
			if !needVal() {
				continue
			}
			argv := entrypointArgv(f.Value)
			if len(argv) == 0 {
				note("--entrypoint", SevWarning, "The entrypoint could not be read and was not applied.")
				res.Unsupported = append(res.Unsupported, "entrypoint")
				continue
			}
			if ok(agentprotocol.DockerOptions{Entrypoint: argv}, "entrypoint", false) {
				out.Entrypoint = argv
				note("entrypoint", SevInfo, "The entrypoint override is applied as the exec-form argv ["+quoteForNote(argv)+"]. It replaces the image's entrypoint and its default command, so the service's start command is left empty.")
			}
		case "cap-add":
			for _, c := range splitList(val) {
				if !seen["add "+c] {
					seen["add "+c] = true
					if ok(agentprotocol.DockerOptions{CapAdd: []string{c}}, "cap-add "+c, false) {
						out.CapAdd = append(out.CapAdd, c)
					}
				}
			}
		case "cap-drop":
			for _, c := range splitList(val) {
				if !seen["drop "+c] {
					seen["drop "+c] = true
					if ok(agentprotocol.DockerOptions{CapDrop: []string{c}}, "cap-drop "+c, false) {
						out.CapDrop = append(out.CapDrop, c)
					}
				}
			}
		case "security-opt":
			mapSecurityOpt(val, &out, ok, unsupported)
		case "sysctl":
			k, v, found := strings.Cut(val, "=")
			if !found {
				unsupported("sysctl "+shortValue(val), "use key=value")
				continue
			}
			if out.Sysctls == nil {
				out.Sysctls = map[string]string{}
			}
			if ok(agentprotocol.DockerOptions{Sysctls: map[string]string{k: v}}, "sysctl "+k, false) {
				out.Sysctls[k] = v
			}
		case "ulimit":
			u, err := parseUlimit(val)
			if err != nil {
				unsupported("ulimit "+shortValue(val), err.Error())
				continue
			}
			if ok(agentprotocol.DockerOptions{Ulimits: []agentprotocol.Ulimit{u}}, "ulimit "+u.Name, false) {
				out.Ulimits = append(out.Ulimits, u)
			}
		case "shm-size":
			mb, found := parseSizeMB(val)
			if !found {
				unsupported("shm-size "+shortValue(val), "not a size")
				continue
			}
			if ok(agentprotocol.DockerOptions{ShmSizeMB: mb}, "shm-size", false) {
				out.ShmSizeMB = mb
			}
		case "add-host":
			h := strings.Replace(val, "=", ":", 1)
			if ok(agentprotocol.DockerOptions{ExtraHosts: []string{h}}, "add-host", false) {
				out.ExtraHosts = append(out.ExtraHosts, h)
			}
		case "init":
			if f.Value != "" && strings.EqualFold(f.Value, "false") {
				continue
			}
			out.Init = true
		case "tmpfs":
			t, err := parseTmpfs(val, note)
			if err != nil {
				unsupported("tmpfs "+shortValue(val), err.Error())
				continue
			}
			if ok(agentprotocol.DockerOptions{Tmpfs: []agentprotocol.TmpfsMount{t}}, "tmpfs "+t.Path, false) {
				out.Tmpfs = append(out.Tmpfs, t)
			}
		case "hostname":
			if needVal() && ok(agentprotocol.DockerOptions{Hostname: strings.ToLower(val)}, "hostname", false) {
				out.Hostname = strings.ToLower(val)
			}
		case "dns":
			if needVal() && ok(agentprotocol.DockerOptions{DNS: []string{val}}, "dns", false) {
				out.DNS = append(out.DNS, val)
			}
		case "user":
			if needVal() && ok(agentprotocol.DockerOptions{User: val}, "user", false) {
				out.User = val
			}
		case "group-add":
			if needVal() && ok(agentprotocol.DockerOptions{GroupAdd: []string{val}}, "group-add", false) {
				out.GroupAdd = append(out.GroupAdd, val)
			}
		case "device":
			d := parseDevice(val)
			if ok(agentprotocol.DockerOptions{Devices: []agentprotocol.DeviceMapping{d}}, "device "+d.HostPath, true) {
				out.Devices = append(out.Devices, d)
			}
		case "gpus":
			g, err := parseGPUs(val)
			if err != nil {
				unsupported("gpus "+shortValue(val), err.Error())
				continue
			}
			if ok(agentprotocol.DockerOptions{GPUs: g}, "gpus", false) {
				out.GPUs = g
			}
		case "privileged":
			if f.Value != "" && strings.EqualFold(f.Value, "false") {
				continue
			}
			if ok(agentprotocol.DockerOptions{Privileged: true}, "privileged", true) {
				out.Privileged = true
			}
		case "pid":
			if val == "host" {
				if ok(agentprotocol.DockerOptions{PidHost: true}, "pid", true) {
					out.PidHost = true
				}
			} else {
				unsupported("pid "+shortValue(val), "only --pid=host (on an unrestricted server) is supported")
			}
		case "runtime":
			switch {
			case !needVal():
			case strings.EqualFold(val, "runc"):
				note("runtime", SevInfo, "runtime runc is Docker's default; nothing to apply.")
			case ok(agentprotocol.DockerOptions{Runtime: val}, "runtime", false):
				out.Runtime = val
			}
		case "pids-limit":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n <= 0 {
				unsupported("pids-limit "+shortValue(val), "only a positive limit is supported")
				continue
			}
			if ok(agentprotocol.DockerOptions{PidsLimit: n}, "pids-limit", false) {
				out.PidsLimit = n
			}
		case "read-only":
			if f.Value != "" && strings.EqualFold(f.Value, "false") {
				continue
			}
			out.ReadOnlyRootfs = true
		case "stop-signal":
			if needVal() && ok(agentprotocol.DockerOptions{StopSignal: strings.ToUpper(val)}, "stop-signal", false) {
				out.StopSignal = strings.ToUpper(val)
			}
		case "memory":
			if mb, found := parseSizeMB(val); found {
				res.MemoryMB = mb
				note("--memory", SevInfo, "Used as the memory limit because the application has none set in Coolify.")
			} else {
				unsupported("memory "+shortValue(val), "not a size")
			}
		case "cpus":
			if c, err := strconv.ParseFloat(val, 64); err == nil && c > 0 {
				res.CPUs = c
				note("--cpus", SevInfo, "Used as the CPU limit because the application has none set in Coolify.")
			} else {
				unsupported("cpus "+shortValue(val), "not a number")
			}
		default:
			why, known := runUnsupported[name]
			if known && why == "" {
				why = "not supported"
			}
			if !known {
				why = "not recognised"
			}
			unsupported(name, why)
		}
	}
	if out.ReadOnlyRootfs && len(out.Tmpfs) == 0 {
		note("read-only", SevWarning, "The root filesystem is read-only: every writable path of the image has to be a volume or a tmpfs, or the container fails to start.")
	}
	if err := out.Validate(true); err != nil {
		note("docker run options", SevWarning, "The container options were not applied: "+err.Error()+".")
		res.Unsupported = append(res.Unsupported, "all options: "+err.Error())
		return res
	}
	if !unrestricted {
		// Whatever ok() let through is safe for a restricted server; Validate(false) is the final word.
		if err := out.Validate(false); err != nil {
			note("docker run options", SevWarning, "The container options were not applied: "+err.Error()+".")
			return res
		}
	}
	res.Docker = out.Normalized()
	res.NeedsUnrestricted = uniqueStrings(res.NeedsUnrestricted)
	return res
}

func mapSecurityOpt(val string, out *agentprotocol.DockerOptions, ok func(agentprotocol.DockerOptions, string, bool) bool, unsupported func(string, string)) {
	norm := strings.Replace(val, ":", "=", 1)
	switch strings.ToLower(norm) {
	case "no-new-privileges", "no-new-privileges=true":
		out.NoNewPrivileges = true
	case "seccomp=unconfined":
		if ok(agentprotocol.DockerOptions{SeccompUnconfined: true}, "security-opt seccomp=unconfined", true) {
			out.SeccompUnconfined = true
		}
	case "apparmor=unconfined":
		if ok(agentprotocol.DockerOptions{ApparmorUnconfined: true}, "security-opt apparmor=unconfined", true) {
			out.ApparmorUnconfined = true
		}
	default:
		unsupported("security-opt "+shortValue(val), "only no-new-privileges and seccomp/apparmor=unconfined are supported")
	}
}

// entrypointArgv reads a --entrypoint value: Docker takes one string and
// Coolify's Compose conversion splits it shell-style, so "a b c" is [a b c]; a
// JSON array is taken as it is.
func entrypointArgv(v string) []string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "[") {
		var arr []string
		if json.Unmarshal([]byte(v), &arr) == nil && len(arr) > 0 {
			return arr
		}
	}
	argv, err := shellWords(v)
	if err != nil {
		return nil
	}
	return argv
}

func parseUlimit(v string) (agentprotocol.Ulimit, error) {
	name, limits, found := strings.Cut(v, "=")
	if !found {
		return agentprotocol.Ulimit{}, fmt.Errorf("use name=soft[:hard]")
	}
	name = strings.ToLower(name)
	if name != "nofile" && name != "memlock" {
		return agentprotocol.Ulimit{}, fmt.Errorf("only nofile and memlock are supported (use --pids-limit for processes)")
	}
	softS, hardS, hasHard := strings.Cut(limits, ":")
	soft, err := strconv.ParseInt(softS, 10, 64)
	if err != nil {
		return agentprotocol.Ulimit{}, fmt.Errorf("not a number")
	}
	hard := soft
	if hasHard {
		if hard, err = strconv.ParseInt(hardS, 10, 64); err != nil {
			return agentprotocol.Ulimit{}, fmt.Errorf("not a number")
		}
	}
	return agentprotocol.Ulimit{Name: name, Soft: soft, Hard: hard}, nil
}

// parseSizeMB reads a Docker size ("512m", "2g", "1gb", "65536k", bytes) as MiB, rounded up.
func parseSizeMB(v string) (int, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || v == "0" {
		return 0, false
	}
	v = strings.TrimSuffix(v, "b")
	var mult float64
	switch {
	case strings.HasSuffix(v, "k"):
		mult, v = 1.0/1024, strings.TrimSuffix(v, "k")
	case strings.HasSuffix(v, "m"):
		mult, v = 1, strings.TrimSuffix(v, "m")
	case strings.HasSuffix(v, "g"):
		mult, v = 1024, strings.TrimSuffix(v, "g")
	case strings.HasSuffix(v, "t"):
		mult, v = 1024*1024, strings.TrimSuffix(v, "t")
	default:
		mult = 1.0 / (1024 * 1024) // plain bytes
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	mb := int(math.Ceil(n * mult))
	if mb < 1 {
		mb = 1
	}
	return mb, true
}

func parseTmpfs(v string, note func(key, sev, msg string)) (agentprotocol.TmpfsMount, error) {
	path, opts, _ := strings.Cut(v, ":")
	if path == "" {
		return agentprotocol.TmpfsMount{}, fmt.Errorf("no path")
	}
	t := agentprotocol.TmpfsMount{Path: path}
	for _, o := range splitList(opts) {
		k, val, _ := strings.Cut(o, "=")
		switch k {
		case "size":
			if mb, ok := parseSizeMB(val); ok {
				t.SizeMB = mb
			}
		case "mode":
			t.Mode = strings.TrimLeft(val, "0")
			if t.Mode == "" {
				t.Mode = "0"
			}
		case "rw", "ro", "noexec", "nosuid", "nodev", "exec", "suid", "dev":
			note("tmpfs "+path, SevInfo, "The mount flag "+k+" is not applied (tmpfs mounts use the platform defaults).")
		default:
			note("tmpfs "+path, SevInfo, "The tmpfs option "+k+" is not applied.")
		}
	}
	return t, nil
}

func parseDevice(v string) agentprotocol.DeviceMapping {
	parts := strings.Split(v, ":")
	d := agentprotocol.DeviceMapping{HostPath: parts[0]}
	switch len(parts) {
	case 2:
		if isPerm(parts[1]) {
			d.Permissions = parts[1]
		} else {
			d.ContainerPath = parts[1]
		}
	case 3:
		d.ContainerPath, d.Permissions = parts[1], parts[2]
	}
	return d
}

func isPerm(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != 'r' && r != 'w' && r != 'm' {
			return false
		}
	}
	return true
}

func parseGPUs(v string) (*agentprotocol.GPURequest, error) {
	v = strings.Trim(strings.TrimSpace(v), `"'`)
	switch {
	case v == "all":
		return &agentprotocol.GPURequest{Count: -1, Driver: "nvidia"}, nil
	case strings.HasPrefix(v, "device="):
		ids := splitList(strings.TrimPrefix(v, "device="))
		if len(ids) == 0 {
			return nil, fmt.Errorf("no device ids")
		}
		return &agentprotocol.GPURequest{Count: len(ids), Driver: "nvidia"}, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("use all, a count or device=<ids>")
	}
	return &agentprotocol.GPURequest{Count: n, Driver: "nvidia"}, nil
}

func shortValue(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
