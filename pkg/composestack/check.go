package composestack

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Violation kinds.
const (
	// KindUnsupported marks something a stack cannot do at all; Unrestricted
	// Compose does not lift it.
	KindUnsupported = "unsupported"
	// KindRestricted marks something the safe allow-list refuses; it is only
	// reported when the policy is not Unrestricted.
	KindRestricted = "restricted"
)

// Violation is one problem of a compose model.
type Violation struct {
	// Service is the compose service name ("" for project-level keys).
	Service string `json:"service,omitempty"`
	// Key is the compose key, for example privileged or volumes[1].
	Key     string `json:"key"`
	Message string `json:"message"`
	Kind    string `json:"kind" enum:"unsupported,restricted"`
}

func (v Violation) String() string {
	if v.Service == "" {
		return fmt.Sprintf("%s: %s", v.Key, v.Message)
	}
	return fmt.Sprintf("service %s: %s: %s", v.Service, v.Key, v.Message)
}

// Policy selects what Check enforces.
type Policy struct {
	// Unrestricted is the server's "Unrestricted" switch (instance admin only):
	// it lifts the allow-list (KindRestricted violations are not reported);
	// KindUnsupported ones remain.
	Unrestricted bool
	// Project is the compose project name (nc-<serviceId8>). A normalised
	// model names every volume and network <project>_<key>; a different name
	// would reach a volume or network outside the stack and is refused. Empty
	// means "no name is acceptable" (the control plane's pre-check of the
	// file as written).
	Project string
}

// reservedLabelPrefixes are managed by Docker Compose, NebulaCtrl and the proxy.
var reservedLabelPrefixes = []string{"com.docker.", "nebulactrl.", "traefik."}

// Check validates a compose model. The result is sorted by service and key.
// An empty result means the stack may run under the policy.
func Check(p *Project, pol Policy) []Violation {
	c := &checker{pol: pol, p: p}
	c.project()
	for _, name := range p.names {
		c.service(name, p.services[name])
	}
	sort.SliceStable(c.out, func(i, j int) bool {
		a, b := c.out[i], c.out[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Key < b.Key
	})
	return c.out
}

type checker struct {
	pol Policy
	p   *Project
	out []Violation
}

func (c *checker) add(svc, key, kind, format string, a ...any) {
	if kind == KindRestricted && c.pol.Unrestricted {
		return
	}
	c.out = append(c.out, Violation{Service: svc, Key: key, Kind: kind, Message: fmt.Sprintf(format, a...)})
}

func (c *checker) restricted(svc, key, format string, a ...any) {
	c.add(svc, key, KindRestricted, format, a...)
}

func (c *checker) unsupported(svc, key, format string, a ...any) {
	c.add(svc, key, KindUnsupported, format, a...)
}

// project checks the top-level sections.
func (c *checker) project() {
	raw := c.p.raw
	if _, ok := raw["include"]; ok {
		c.unsupported("", "include", "include needs other files; a stack is a single compose file")
	}
	if _, ok := raw["models"]; ok {
		c.unsupported("", "models", "compose models run a host-side provider and are not supported")
	}
	for name, v := range asMap(raw["volumes"]) {
		def := asMap(v)
		key := "volumes." + name
		if asBool(def["external"]) {
			c.restricted("", key, "external volumes reach data outside the stack")
		}
		if n := asString(def["name"]); n != "" && n != c.pol.Project+"_"+name {
			c.restricted("", key, "a custom volume name could reach another stack's data; remove name:")
		}
		if d := asString(def["driver"]); d != "" && d != "local" {
			c.restricted("", key, "volume driver %q is not allowed", d)
		}
		if len(asMap(def["driver_opts"])) > 0 {
			c.restricted("", key, "driver_opts can mount host paths or network shares; they are not allowed")
		}
	}
	for name, v := range asMap(raw["networks"]) {
		def := asMap(v)
		key := "networks." + name
		if asBool(def["external"]) {
			c.restricted("", key, "external networks would join networks outside the stack, bypassing environment isolation")
		}
		if n := asString(def["name"]); n != "" && n != c.pol.Project+"_"+name {
			c.restricted("", key, "a custom network name could collide with a managed network; remove name:")
		}
		if d := asString(def["driver"]); d != "" && d != "bridge" {
			c.restricted("", key, "network driver %q is not allowed (host, macvlan and overlay networks are not)", d)
		}
		if len(asMap(def["driver_opts"])) > 0 {
			c.restricted("", key, "network driver_opts are not allowed")
		}
	}
	for _, section := range []string{"secrets", "configs"} {
		for name, v := range asMap(raw[section]) {
			def := asMap(v)
			key := section + "." + name
			if asString(def["file"]) != "" {
				c.restricted("", key, "file: reads a host file; use content: or environment: instead")
			}
			if asBool(def["external"]) {
				c.restricted("", key, "external %s are not available", section)
			}
		}
	}
}

// service checks one service mapping.
func (c *checker) service(name string, sv map[string]any) {
	if !agentprotocol.ComponentNamePattern.MatchString(name) {
		c.unsupported(name, "name", "the service name must be lower case letters, digits, - or _ (it becomes a container name and a DNS alias)")
	}
	if _, ok := sv["build"]; ok {
		c.unsupported(name, "build", "stacks run images only (set image: and build it elsewhere, or use Split into services)")
	}
	if asString(sv["image"]) == "" {
		if _, ok := sv["build"]; !ok && sv["extends"] == nil {
			c.unsupported(name, "image", "the service has no image")
		}
	}
	if _, ok := sv["provider"]; ok {
		c.unsupported(name, "provider", "provider services run a host-side plugin and are not supported")
	}
	if _, ok := sv["env_file"]; ok {
		c.unsupported(name, "env_file", "env_file reads files next to the compose file, which a stack does not have; put the values in the service's variables")
	}
	if ext := asMap(sv["extends"]); ext != nil && asString(ext["file"]) != "" {
		c.unsupported(name, "extends", "extends with a file needs other files; a stack is a single compose file")
	}
	if n, ok := asInt(sv["scale"]); ok && n > 1 {
		c.unsupported(name, "scale", "a stack service runs one container; scale is not supported")
	}
	if dep := asMap(sv["deploy"]); dep != nil {
		if n, ok := asInt(dep["replicas"]); ok && n > 1 {
			c.unsupported(name, "deploy.replicas", "a stack service runs one container; replicas > 1 is not supported")
		}
		for i := range asList(asMap(asMap(dep["resources"])["reservations"])["devices"]) {
			c.restricted(name, fmt.Sprintf("deploy.resources.reservations.devices[%d]", i), "device reservations (GPUs) need an unrestricted server")
		}
	}

	if asBool(sv["privileged"]) {
		c.restricted(name, "privileged", "privileged containers need an unrestricted server")
	}
	if len(asList(sv["devices"])) > 0 {
		c.restricted(name, "devices", "host devices need an unrestricted server")
	}
	if len(asList(sv["device_cgroup_rules"])) > 0 {
		c.restricted(name, "device_cgroup_rules", "device cgroup rules are not allowed")
	}
	if sv["gpus"] != nil {
		c.restricted(name, "gpus", "GPU access needs an unrestricted server")
	}
	if r := asString(sv["runtime"]); r != "" && r != "runc" {
		c.restricted(name, "runtime", "runtime %q needs an unrestricted server", r)
	}
	for _, k := range []string{"cgroup_parent", "storage_opt", "isolation", "credential_spec"} {
		if v, ok := sv[k]; ok && v != nil && fmt.Sprint(v) != "" && fmt.Sprint(v) != "map[]" {
			c.restricted(name, k, "%s is not allowed", k)
		}
	}
	if v := asString(sv["cgroup"]); v == "host" {
		c.restricted(name, "cgroup", "the host cgroup namespace is not allowed")
	}
	if v := asString(sv["uts"]); v == "host" {
		c.restricted(name, "uts", "the host UTS namespace is not allowed")
	}
	if v := asString(sv["userns_mode"]); v == "host" {
		c.restricted(name, "userns_mode", "the host user namespace is not allowed")
	}
	c.namespace(name, "pid", asString(sv["pid"]))
	c.namespace(name, "ipc", asString(sv["ipc"]))
	c.networkMode(name, asString(sv["network_mode"]))

	for _, cap := range stringList(sv["cap_add"]) {
		n := agentprotocol.NormalizeCapability(cap)
		switch {
		case contains(agentprotocol.SafeCapabilities, n):
		case contains(agentprotocol.UnrestrictedCapabilities, n):
			c.restricted(name, "cap_add", "capability %s needs an unrestricted server", n)
		default:
			c.restricted(name, "cap_add", "capability %s is not on the allow-list", n)
		}
	}
	for _, opt := range stringList(sv["security_opt"]) {
		switch k, v := securityOpt(opt); {
		case k == "no-new-privileges":
		case (k == "seccomp" || k == "apparmor") && v == "unconfined":
			c.restricted(name, "security_opt", "security option %q needs an unrestricted server", opt)
		default:
			c.restricted(name, "security_opt", "security option %q is not allowed (only no-new-privileges; seccomp and apparmor unconfined need an unrestricted server)", opt)
		}
	}
	// Sysctls: only the curated list (the namespaced IPC and network
	// parameters of agentprotocol.AllowedSysctls) is safe on a restricted server.
	for _, k := range sortedKeys(sysctlKeys(sv["sysctls"])) {
		if !contains(agentprotocol.AllowedSysctls, k) {
			c.restricted(name, "sysctls", "sysctl %q is not on the allow-list", k)
		}
	}
	for k := range labelMap(sv["labels"]) {
		for _, pfx := range reservedLabelPrefixes {
			if strings.HasPrefix(k, pfx) {
				c.restricted(name, "labels", "label %q is reserved (%s* labels are managed by NebulaCtrl)", k, pfx)
			}
		}
	}
	for i, vf := range stringList(sv["volumes_from"]) {
		if strings.HasPrefix(vf, "container:") {
			c.restricted(name, fmt.Sprintf("volumes_from[%d]", i), "volumes_from a container outside the stack is not allowed")
		}
	}
	for i, e := range asList(sv["volumes"]) {
		c.mount(name, i, e)
	}
	for _, hook := range append(asList(sv["post_start"]), asList(sv["pre_stop"])...) {
		if asBool(asMap(hook)["privileged"]) {
			c.restricted(name, "post_start", "privileged lifecycle hooks are not allowed")
		}
	}
	if blk := asMap(sv["blkio_config"]); blk != nil {
		for _, k := range []string{"weight_device", "device_read_bps", "device_write_bps", "device_read_iops", "device_write_iops"} {
			if len(asList(blk[k])) > 0 {
				c.restricted(name, "blkio_config."+k, "block device limits name host devices and are not allowed")
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// namespace checks pid and ipc: only a component of the same stack, "private"
// and (for ipc) "shareable" are allowed; host and other containers are not.
func (c *checker) namespace(svc, key, v string) {
	switch {
	case v == "" || v == "private" || (key == "ipc" && v == "shareable"):
	case strings.HasPrefix(v, "service:") && c.p.services[strings.TrimPrefix(v, "service:")] != nil:
	default:
		if v == "host" {
			c.restricted(svc, key, "%s: host needs an unrestricted server", key)
			return
		}
		c.restricted(svc, key, "%s: %q is not allowed (only another service of the stack)", key, v)
	}
}

func (c *checker) networkMode(svc, v string) {
	switch {
	case v == "" || v == "none":
	case strings.HasPrefix(v, "service:") && c.p.services[strings.TrimPrefix(v, "service:")] != nil:
	default:
		c.restricted(svc, "network_mode", "network_mode %q is not allowed: services always use the environment network (only none and another service of the stack are)", v)
	}
}

// securityOpt splits a security option ("seccomp=unconfined", "seccomp:unconfined",
// "no-new-privileges") into its name and value.
func securityOpt(o string) (key, val string) {
	o = strings.TrimSpace(o)
	if i := strings.IndexAny(o, "=:"); i >= 0 {
		return o[:i], o[i+1:]
	}
	return o, ""
}

// sysctlKeys reads the names of sysctls written as a mapping or as a list of
// "key=value".
func sysctlKeys(v any) map[string]bool {
	out := map[string]bool{}
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			out[k] = true
		}
	case []any:
		for _, e := range t {
			k, _, _ := strings.Cut(asString(e), "=")
			if k = strings.TrimSpace(k); k != "" {
				out[k] = true
			}
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mount checks one entry of a service's volumes.
func (c *checker) mount(svc string, i int, e any) {
	key := fmt.Sprintf("volumes[%d]", i)
	var typ, source string
	switch t := e.(type) {
	case map[string]any:
		typ, source = asString(t["type"]), asString(t["source"])
	default:
		short := asString(e)
		parts := strings.Split(short, ":")
		if len(parts) == 1 {
			return // anonymous volume
		}
		source = parts[0]
		switch {
		case hasVar(source):
			return // not interpolated: the agent decides on the normalised model
		case strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "~"):
			typ = "bind"
		default:
			typ = "volume"
		}
	}
	switch typ {
	case "bind":
		c.restricted(svc, key, "host bind mounts are not allowed (%s); use a named volume or a config content", shorten(source))
	case "tmpfs":
	case "volume", "":
		if source != "" && !hasVar(source) {
			if _, declared := asMap(c.p.raw["volumes"])[source]; !declared {
				c.restricted(svc, key, "volume %q is not declared in the top-level volumes section", source)
			}
		}
	default:
		c.restricted(svc, key, "mount type %q is not allowed", typ)
	}
}

func shorten(s string) string {
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

// Summary renders violations as a numbered, human-readable list.
func Summary(vs []Violation) string {
	var b strings.Builder
	for i, v := range vs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- " + v.String())
	}
	return b.String()
}

// Restricted returns the violations an Unrestricted server would let through.
func Restricted(vs []Violation) []Violation {
	var out []Violation
	for _, v := range vs {
		if v.Kind == KindRestricted {
			out = append(out, v)
		}
	}
	return out
}
