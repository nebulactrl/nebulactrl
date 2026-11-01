package composeimport

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Severity of a Note.
const (
	SevInfo    = "info"    // harmless: ignored on purpose
	SevWarning = "warning" // behaviour that is not carried over
	SevDanger  = "danger"  // never applied and likely to break the service
)

// ComposeNote is one remark about a Compose key: what was not imported and why.
type ComposeNote struct {
	Key      string `json:"key" doc:"The Compose key, e.g. privileged or volumes[0]"`
	Severity string `json:"severity" enum:"info,warning,danger"`
	Message  string `json:"message"`
}

// FileFn reads a file referenced by the Compose project (env_file, bind
// mounted files) by the path as written in the file. found is false when the
// file is not available.
type FileFn func(p string) (content []byte, found bool)

// project is a parsed and interpolated Compose project.
type project struct {
	Name     string
	Services []*cService
	Networks map[string]cNetwork
	Volumes  map[string]cVolume
	// Notes are project-level remarks (include, top-level secrets, ...).
	Notes []ComposeNote
	// NeededFiles are files the project references that FileFn could not supply.
	NeededFiles []ComposeNeededFile
	Interp      *interpolator
}

type cNetwork struct {
	External bool
	Name     string
}

type cVolume struct {
	External bool
	Name     string
	Driver   string
	Opts     map[string]string
}

// ComposeNeededFile is a file the import needs but could not read.
type ComposeNeededFile struct {
	Path    string `json:"path" doc:"The path as written in the Compose file"`
	Purpose string `json:"purpose" enum:"env_file,bind" doc:"What the file is for"`
	Service string `json:"service"`
	// Required is false for env_file entries marked required: false.
	Required bool `json:"required"`
}

type envPair struct {
	Key    string
	Value  string
	Origin string // environment | env_file | dotenv
}

type cPort struct {
	Target    int
	Published int // 0 = not published on the host
	Protocol  string
	HostIP    string
}

type cMount struct {
	Type     string // bind | volume | tmpfs
	Source   string
	Target   string
	ReadOnly bool
}

type cDep struct {
	Name      string
	Condition string
}

type cNet struct {
	Name    string
	Aliases []string
}

type cHealth struct {
	Test        []string
	None        bool
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int
}

type cBuild struct {
	Context    string
	Dockerfile string
	Target     string
	Args       map[string]string
	// URL is set when the context is a git URL.
	URL string
}

type cService struct {
	Key           string
	Image         string
	Build         *cBuild
	Command       []string
	HasCommand    bool
	Entrypoint    []string
	Env           []envPair
	Ports         []cPort
	Expose        []int
	Mounts        []cMount
	DependsOn     []cDep
	Healthcheck   *cHealth
	Profiles      []string
	Networks      []cNet
	NetworkMode   string
	ContainerName string
	Labels        map[string]string
	StopGrace     time.Duration
	MemoryMB      int
	CPUs          float64
	Replicas      int
	Restart       string
	Notes         []ComposeNote
	// Docker are the container options (capabilities, ulimits, ...) before validation.
	Docker dockerPieces
}

// knownIgnored are keys that carry no behaviour NebulaCtrl needs.
var knownIgnored = map[string]string{
	"container_name":  "The service name is the container's network alias; no fixed container name is needed.",
	"domainname":      "Containers are reachable by the service name on the environment network.",
	"restart":         "Services always restart unless stopped.",
	"pull_policy":     "Images are pulled on every deploy.",
	"platform":        "Images are pulled for the server's architecture.",
	"tty":             "Not needed for a background service.",
	"stdin_open":      "Not needed for a background service.",
	"logging":         "Logs are collected by the agent; log drivers are not applied.",
	"annotations":     "Not applied.",
	"external_links":  "Services in the environment reach each other by name.",
	"mem_reservation": "Soft memory limits are not applied.",
	"develop":         "Compose Watch settings are not applied.",
}

// knownUnsupported are keys whose behaviour is lost; the value is the reason.
var knownUnsupported = map[string]string{
	"dns_search":          "DNS search domains are not applied.",
	"working_dir":         "The container uses the image's working directory.",
	"secrets":             "Compose secrets are files; store the value as a secret variable instead.",
	"configs":             "Compose configs are not imported; attach the file in the service settings.",
	"userns_mode":         "User namespace settings are not applied.",
	"uts":                 "UTS namespace settings are not applied.",
	"cgroup_parent":       "Cgroup settings are not applied.",
	"mac_address":         "A fixed MAC address is not applied.",
	"oom_score_adj":       "OOM score settings are not applied.",
	"volumes_from":        "volumes_from is not supported: declare the shared volume on each service.",
	"storage_opt":         "Storage options are not applied.",
	"isolation":           "Isolation settings are not applied.",
	"post_start":          "Lifecycle hooks are not applied.",
	"pre_stop":            "Lifecycle hooks are not applied.",
	"cpu_shares":          "CPU shares are not applied; use a CPU limit.",
	"cpuset":              "CPU pinning is not applied.",
	"blkio_config":        "Block IO limits are not applied.",
	"device_cgroup_rules": "Device rules are not applied.",
	"expose_labels":       "Not applied.",
}

// knownDanger are keys that are never applied and that usually mean the
// service cannot work the same way here.
var knownDanger = map[string]string{
	"ipc":          "Sharing an IPC namespace is never applied.",
	"network_mode": "Host and container network modes are never applied: services always use the environment network.",
}

// handled keys are read by the importer.
var handledKeys = map[string]bool{
	"image": true, "build": true, "command": true, "entrypoint": true, "environment": true, "env_file": true,
	"ports": true, "expose": true, "volumes": true, "depends_on": true, "healthcheck": true, "profiles": true,
	"networks": true, "labels": true, "deploy": true, "stop_grace_period": true, "mem_limit": true, "cpus": true,
	"links": true, "scale": true, "extends": true, "cap_add": true, "ulimits": true, "shm_size": true, "extra_hosts": true,
	"tmpfs": true, "stop_signal": true, "init": true, "cap_drop": true, "security_opt": true, "read_only": true, "pids_limit": true,
	"hostname": true, "dns": true, "sysctls": true, "user": true, "group_add": true, "devices": true, "gpus": true, "runtime": true,
	"privileged": true, "pid": true,
}

// parseProject reads a Compose document. dotenv is the variable set used for
// interpolation; files supplies env_file contents.
func parseProject(src string, dotenv map[string]string, files FileFn) (*project, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return nil, fmt.Errorf("the file is not valid YAML: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the file is not a Compose project (expected a mapping with a services section)")
	}
	var doc map[string]any
	if err := root.Content[0].Decode(&doc); err != nil {
		return nil, fmt.Errorf("the file is not valid YAML: %w", err)
	}

	order := serviceOrder(root.Content[0])
	ip := newInterpolator(dotenv)
	doc = interpolateMap(doc, ip)

	p := &project{Networks: map[string]cNetwork{}, Volumes: map[string]cVolume{}, Interp: ip}
	p.Name, _ = scalar(doc["name"])
	rawSvcs := asMap(doc["services"])
	if len(rawSvcs) == 0 {
		return nil, fmt.Errorf("the file has no services")
	}
	if _, ok := doc["include"]; ok {
		p.Notes = append(p.Notes, ComposeNote{Key: "include", Severity: SevWarning, Message: "include: is not followed. Import the included files separately."})
	}
	for _, k := range []string{"secrets", "configs"} {
		if _, ok := doc[k]; ok {
			p.Notes = append(p.Notes, ComposeNote{Key: k, Severity: SevWarning, Message: "Top-level " + k + " are not imported."})
		}
	}
	for name, v := range asMap(doc["networks"]) {
		m := asMap(v)
		n := cNetwork{Name: name}
		if ext, ok := m["external"]; ok {
			n.External = truthy(ext)
			if em := asMap(ext); len(em) > 0 { // legacy: external: {name: x}
				n.External = true
				if s, ok := scalar(em["name"]); ok && s != "" {
					n.Name = s
				}
			}
		}
		if s, ok := scalar(m["name"]); ok && s != "" {
			n.Name = s
		}
		p.Networks[name] = n
	}
	for name, v := range asMap(doc["volumes"]) {
		m := asMap(v)
		vol := cVolume{Name: name, Driver: asStr(m["driver"]), Opts: map[string]string{}}
		vol.External = truthy(m["external"])
		if s, ok := scalar(m["name"]); ok && s != "" {
			vol.Name = s
		}
		for k, ov := range asMap(m["driver_opts"]) {
			s, _ := scalar(ov)
			vol.Opts[k] = s
		}
		p.Volumes[name] = vol
	}

	resolved := map[string]map[string]any{}
	for key := range rawSvcs {
		m, err := resolveExtends(rawSvcs, key, nil, p)
		if err != nil {
			return nil, err
		}
		resolved[key] = m
	}
	for _, key := range order {
		m, ok := resolved[key]
		if !ok {
			continue
		}
		svc, err := buildService(key, m, p, files)
		if err != nil {
			return nil, err
		}
		p.Services = append(p.Services, svc)
	}
	return p, nil
}

// serviceOrder lists the service keys in file order.
func serviceOrder(root *yaml.Node) []string {
	var keys []string
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "services" {
			continue
		}
		sn := root.Content[i+1]
		for sn.Kind == yaml.AliasNode && sn.Alias != nil {
			sn = sn.Alias
		}
		for j := 0; j+1 < len(sn.Content); j += 2 {
			keys = append(keys, sn.Content[j].Value)
		}
	}
	return keys
}

func interpolateMap(m map[string]any, ip *interpolator) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if strings.HasPrefix(k, "x-") {
			out[k] = v
			continue
		}
		out[k] = interpolateAny(v, ip)
	}
	return out
}

func interpolateAny(v any, ip *interpolator) any {
	switch x := v.(type) {
	case string:
		return ip.Expand(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = interpolateAny(e, ip)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = interpolateAny(e, ip)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[fmt.Sprint(k)] = interpolateAny(e, ip)
		}
		return out
	}
	return v
}

func asMap(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[fmt.Sprint(k)] = e
		}
		return out
	}
	return nil
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asStr(v any) string {
	s, _ := scalar(v)
	return s
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(x)
		return b
	}
	return false
}

// stringList reads a string or a list of strings.
func stringList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := scalar(e); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ---- extends ----

func resolveExtends(all map[string]any, key string, stack []string, p *project) (map[string]any, error) {
	for _, s := range stack {
		if s == key {
			return nil, fmt.Errorf("extends cycle through service %q", key)
		}
	}
	cur := asMap(all[key])
	if cur == nil {
		return map[string]any{}, nil
	}
	ext, ok := cur["extends"]
	if !ok {
		return cur, nil
	}
	var baseName, file string
	switch e := ext.(type) {
	case string:
		baseName = e
	default:
		em := asMap(ext)
		baseName, file = asStr(em["service"]), asStr(em["file"])
	}
	if file != "" {
		p.Notes = append(p.Notes, ComposeNote{Key: key + ".extends", Severity: SevWarning, Message: "extends from another file (" + file + ") is not followed; the service is imported without the inherited settings."})
		out := make(map[string]any, len(cur))
		for k, v := range cur {
			if k != "extends" {
				out[k] = v
			}
		}
		return out, nil
	}
	if _, ok := all[baseName]; !ok {
		return nil, fmt.Errorf("service %q extends %q, which does not exist", key, baseName)
	}
	base, err := resolveExtends(all, baseName, append(stack, key), p)
	if err != nil {
		return nil, err
	}
	return mergeService(base, cur), nil
}

// mergeService overlays child on base like Compose's extends: mappings merge,
// ports/expose/volumes/depends_on/profiles lists concatenate, everything else
// is replaced.
func mergeService(base, child map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(child))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range child {
		if k == "extends" {
			continue
		}
		bv, ok := out[k]
		if !ok {
			out[k] = v
			continue
		}
		switch k {
		case "ports", "expose", "volumes", "env_file", "links", "external_links":
			out[k] = append(append([]any{}, listOf(bv)...), listOf(v)...)
		case "environment", "labels":
			out[k] = mergeKV(bv, v)
		case "depends_on", "networks", "deploy", "healthcheck", "build":
			bm, cm := asMap(bv), asMap(v)
			if bm != nil && cm != nil {
				out[k] = mergeMaps(bm, cm)
			} else if bm == nil && cm != nil {
				out[k] = v
			} else {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	return out
}

func listOf(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	if v == nil {
		return nil
	}
	return []any{v}
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if am, bm := asMap(out[k]), asMap(v); am != nil && bm != nil {
			out[k] = mergeMaps(am, bm)
			continue
		}
		out[k] = v
	}
	return out
}

// mergeKV merges two environment/labels values (maps or KEY=VAL lists).
func mergeKV(a, b any) any {
	out := map[string]any{}
	for _, src := range []any{a, b} {
		for k, v := range kvPairs(src) {
			out[k] = v
		}
	}
	return out
}

// kvPairs reads environment/labels written as a map or as KEY=VALUE lines. A
// bare KEY (list) or a null value maps to nil.
func kvPairs(v any) map[string]any {
	out := map[string]any{}
	if m := asMap(v); m != nil {
		for k, e := range m {
			out[k] = e
		}
		return out
	}
	for _, e := range asList(v) {
		s, ok := scalar(e)
		if !ok {
			continue
		}
		if k, val, has := strings.Cut(s, "="); has {
			out[k] = val
		} else {
			out[s] = nil
		}
	}
	return out
}

// ---- service ----

func buildService(key string, m map[string]any, p *project, files FileFn) (*cService, error) {
	s := &cService{Key: key, Labels: map[string]string{}, Replicas: 1}
	note := func(k, sev, msg string) { s.Notes = append(s.Notes, ComposeNote{Key: k, Severity: sev, Message: msg}) }

	s.Image = strings.TrimSpace(asStr(m["image"]))
	if b, ok := m["build"]; ok {
		s.Build = parseBuild(b)
	}
	if s.Image == "" && s.Build == nil {
		return nil, fmt.Errorf("service %q has neither an image nor a build", key)
	}
	if v, ok := m["command"]; ok && v != nil {
		s.HasCommand = true
		s.Command = commandList(v, key+".command", note)
	}
	if v, ok := m["entrypoint"]; ok && v != nil {
		s.Entrypoint = commandList(v, key+".entrypoint", note)
	}

	// Environment: env_file entries first, then environment (which wins).
	byKey := map[string]int{}
	add := func(k, v, origin string) {
		if i, ok := byKey[k]; ok {
			s.Env[i] = envPair{Key: k, Value: v, Origin: origin}
			return
		}
		byKey[k] = len(s.Env)
		s.Env = append(s.Env, envPair{Key: k, Value: v, Origin: origin})
	}
	for _, ref := range envFileRefs(m["env_file"]) {
		content, found := []byte(nil), false
		if files != nil {
			content, found = files(ref.Path)
		}
		if !found {
			p.NeededFiles = append(p.NeededFiles, ComposeNeededFile{Path: ref.Path, Purpose: "env_file", Service: key, Required: ref.Required})
			continue
		}
		vals, notes := ParseDotenv(string(content), p.Interp.env)
		for _, n := range notes {
			note("env_file "+ref.Path, SevInfo, n)
		}
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			add(k, vals[k], "env_file")
		}
	}
	envKVs := kvPairs(m["environment"])
	envKeys := make([]string, 0, len(envKVs))
	for k := range envKVs {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	if envList := asList(m["environment"]); envList != nil { // keep the file's order for list syntax
		envKeys = envKeys[:0]
		seen := map[string]bool{}
		for _, e := range envList {
			s, _ := scalar(e)
			k, _, _ := strings.Cut(s, "=")
			if !seen[k] {
				seen[k] = true
				envKeys = append(envKeys, k)
			}
		}
	}
	for _, k := range envKeys {
		v := envKVs[k]
		if v == nil {
			// KEY with no value passes through from the shell; the .env file is part of that environment.
			if dv, ok := p.Interp.env[k]; ok {
				add(k, dv, "dotenv")
			} else {
				note("environment."+k, SevInfo, k+" has no value in the Compose file (it comes from the shell environment); it was skipped.")
			}
			continue
		}
		str, ok := scalar(v)
		if !ok {
			note("environment."+k, SevInfo, k+" has a structured value and was skipped.")
			continue
		}
		add(k, str, "environment")
	}

	s.Docker = parseDockerPieces(m)
	s.Docker.entrypoint = s.Entrypoint
	s.Ports, s.Expose = parsePorts(m["ports"], m["expose"], key, note)
	s.Mounts = parseMounts(m["volumes"], key, note)
	s.DependsOn = parseDepends(m["depends_on"], m["links"])
	s.Healthcheck = parseHealth(m["healthcheck"], key, note)
	s.Profiles = stringList(m["profiles"])
	s.Networks, s.NetworkMode = parseNetworks(m["networks"], asStr(m["network_mode"]))
	s.ContainerName = asStr(m["container_name"])
	s.Restart = asStr(m["restart"])
	for k, v := range kvPairs(m["labels"]) {
		s.Labels[k], _ = scalar(v)
	}
	if d, ok := parseDuration(m["stop_grace_period"]); ok {
		s.StopGrace = d
	}
	if mb, ok := parseMemoryMB(m["mem_limit"]); ok {
		s.MemoryMB = mb
	}
	if c, ok := toFloat(m["cpus"]); ok {
		s.CPUs = c
	}
	if n, ok := toInt(m["scale"]); ok && n > 0 {
		s.Replicas = n
	}
	if dep := asMap(m["deploy"]); dep != nil {
		if n, ok := toInt(dep["replicas"]); ok && n > 0 {
			s.Replicas = n
		}
		lim := asMap(asMap(dep["resources"])["limits"])
		if mb, ok := parseMemoryMB(lim["memory"]); ok {
			s.MemoryMB = mb
		}
		if c, ok := toFloat(lim["cpus"]); ok {
			s.CPUs = c
		}
		if mode := asStr(dep["mode"]); mode == "global" {
			note("deploy.mode", SevWarning, "Global mode (one task per node) is not supported; the service runs on the server you choose.")
		}
		for k := range dep {
			switch k {
			case "replicas", "resources", "mode":
			case "labels", "placement", "update_config", "rollback_config", "restart_policy", "endpoint_mode":
				note("deploy."+k, SevInfo, "Swarm deploy setting; not applied.")
			default:
				note("deploy."+k, SevInfo, "Not applied.")
			}
		}
	}

	// Remaining keys.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case handledKeys[k], strings.HasPrefix(k, "x-"):
			continue
		case knownDanger[k] != "":
			if k == "network_mode" {
				// reported below with the mode named
				continue
			}
			note(k, SevDanger, knownDanger[k])
		case knownUnsupported[k] != "":
			note(k, SevWarning, knownUnsupported[k])
		case knownIgnored[k] != "":
			if k == "container_name" || k == "restart" || k == "domainname" {
				continue
			}
			note(k, SevInfo, knownIgnored[k])
		default:
			note(k, SevWarning, "Not recognised; ignored.")
		}
	}
	if s.NetworkMode != "" {
		msg := knownDanger["network_mode"]
		note("network_mode", SevDanger, "network_mode: "+s.NetworkMode+" is not applied. "+strings.TrimPrefix(msg, "Host and container network modes are never applied: "))
	}
	if v, ok := m["entrypoint"]; ok && v != nil && len(s.Entrypoint) == 0 {
		note("entrypoint", SevInfo, "An empty entrypoint (which clears the image's entrypoint) can't be set; the image's own entrypoint is used.")
	}
	return s, nil
}

func commandList(v any, key string, note func(k, sev, msg string)) []string {
	switch x := v.(type) {
	case string:
		out, err := shellSplit(x)
		if err != nil {
			note(key, SevWarning, "The command could not be split into words ("+err.Error()+"); it was kept as one argument.")
			return []string{x}
		}
		return out
	case []any:
		return stringList(x)
	}
	return nil
}

type envFileRef struct {
	Path     string
	Required bool
}

func envFileRefs(v any) []envFileRef {
	var out []envFileRef
	switch x := v.(type) {
	case string:
		out = append(out, envFileRef{Path: x, Required: true})
	case []any:
		for _, e := range x {
			switch ev := e.(type) {
			case string:
				out = append(out, envFileRef{Path: ev, Required: true})
			default:
				m := asMap(ev)
				if pth := asStr(m["path"]); pth != "" {
					req := true
					if r, ok := m["required"]; ok {
						req = truthy(r)
					}
					out = append(out, envFileRef{Path: pth, Required: req})
				}
			}
		}
	}
	return out
}

func parseBuild(v any) *cBuild {
	b := &cBuild{Args: map[string]string{}}
	switch x := v.(type) {
	case string:
		b.Context = x
	default:
		m := asMap(v)
		if m == nil {
			return nil
		}
		b.Context = asStr(m["context"])
		b.Dockerfile = asStr(m["dockerfile"])
		b.Target = asStr(m["target"])
		for k, e := range kvPairs(m["args"]) {
			b.Args[k], _ = scalar(e)
		}
	}
	if b.Context == "" {
		b.Context = "."
	}
	if strings.Contains(b.Context, "://") || strings.HasPrefix(b.Context, "git@") || strings.HasPrefix(b.Context, "github.com/") {
		b.URL = b.Context
	}
	return b
}

func parsePorts(portsV, exposeV any, key string, note func(k, sev, msg string)) ([]cPort, []int) {
	var ports []cPort
	for i, e := range asList(portsV) {
		k := fmt.Sprintf("%s.ports[%d]", key, i)
		switch x := e.(type) {
		case map[string]any, map[any]any:
			m := asMap(x)
			t, ok := toInt(m["target"])
			if !ok {
				note(k, SevInfo, "Port entry without a target was skipped.")
				continue
			}
			pub, _ := toInt(m["published"])
			proto := strings.ToLower(asStr(m["protocol"]))
			if proto == "" {
				proto = "tcp"
			}
			ports = append(ports, cPort{Target: t, Published: pub, Protocol: proto, HostIP: asStr(m["host_ip"])})
		default:
			s, ok := scalar(e)
			if !ok {
				continue
			}
			cp, err := parsePortString(s)
			if err != nil {
				note(k, SevWarning, fmt.Sprintf("Port %q was skipped: %v.", s, err))
				continue
			}
			ports = append(ports, cp...)
		}
	}
	var expose []int
	for _, e := range asList(exposeV) {
		s, _ := scalar(e)
		s, _, _ = strings.Cut(s, "/")
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			expose = append(expose, n)
		}
	}
	return ports, expose
}

// parsePortString reads "[host_ip:][published:]target[/protocol]".
func parsePortString(s string) ([]cPort, error) {
	s = strings.TrimSpace(s)
	proto := "tcp"
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		proto = strings.ToLower(s[i+1:])
		s = s[:i]
	}
	if proto != "tcp" && proto != "udp" {
		return nil, fmt.Errorf("unsupported protocol %q", proto)
	}
	hostIP := ""
	if strings.HasPrefix(s, "[") { // [::1]:8080:80
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return nil, fmt.Errorf("malformed address")
		}
		hostIP = s[1:end]
		s = strings.TrimPrefix(s[end+1:], ":")
	}
	parts := strings.Split(s, ":")
	var pub, target string
	switch {
	case len(parts) == 1:
		target = parts[0]
	case len(parts) == 2:
		pub, target = parts[0], parts[1]
	case len(parts) == 3 && hostIP == "":
		hostIP, pub, target = parts[0], parts[1], parts[2]
	default:
		return nil, fmt.Errorf("malformed port mapping")
	}
	if strings.Contains(target, "-") || strings.Contains(pub, "-") {
		return nil, fmt.Errorf("port ranges are not supported")
	}
	t, err := strconv.Atoi(target)
	if err != nil || t < 1 || t > 65535 {
		return nil, fmt.Errorf("invalid container port")
	}
	p := 0
	if pub != "" {
		if p, err = strconv.Atoi(pub); err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid published port")
		}
	}
	return []cPort{{Target: t, Published: p, Protocol: proto, HostIP: hostIP}}, nil
}

func parseMounts(v any, key string, note func(k, sev, msg string)) []cMount {
	var out []cMount
	for i, e := range asList(v) {
		switch x := e.(type) {
		case map[string]any, map[any]any:
			m := asMap(x)
			mt := cMount{Type: asStr(m["type"]), Source: asStr(m["source"]), Target: asStr(m["target"]), ReadOnly: truthy(m["read_only"])}
			if mt.Type == "" {
				if mt.Source == "" {
					mt.Type = "volume"
				} else {
					mt.Type = classifySource(mt.Source)
				}
			}
			if mt.Target == "" {
				note(fmt.Sprintf("%s.volumes[%d]", key, i), SevInfo, "Mount without a target was skipped.")
				continue
			}
			out = append(out, mt)
		default:
			s, ok := scalar(e)
			if !ok {
				continue
			}
			mt, err := parseMountString(s)
			if err != nil {
				note(fmt.Sprintf("%s.volumes[%d]", key, i), SevWarning, fmt.Sprintf("Mount %q was skipped: %v.", s, err))
				continue
			}
			out = append(out, mt)
		}
	}
	return out
}

func classifySource(src string) string {
	if strings.HasPrefix(src, ".") || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "~") {
		return "bind"
	}
	return "volume"
}

// parseMountString reads "[source:]target[:mode]".
func parseMountString(s string) (cMount, error) {
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		return cMount{Type: "volume", Target: parts[0]}, nil // anonymous volume
	case 2, 3:
		m := cMount{Source: parts[0], Target: parts[1], Type: classifySource(parts[0])}
		if len(parts) == 3 {
			for _, opt := range strings.Split(parts[2], ",") {
				if opt == "ro" {
					m.ReadOnly = true
				}
			}
		}
		if !strings.HasPrefix(m.Target, "/") {
			return cMount{}, fmt.Errorf("the container path must be absolute")
		}
		return m, nil
	}
	return cMount{}, fmt.Errorf("unsupported mount syntax")
}

func parseDepends(dep, links any) []cDep {
	var out []cDep
	seen := map[string]bool{}
	add := func(name, cond string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, cDep{Name: name, Condition: cond})
	}
	switch x := dep.(type) {
	case []any:
		for _, e := range x {
			add(asStr(e), "service_started")
		}
	default:
		m := asMap(dep)
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			cond := asStr(asMap(m[k])["condition"])
			if cond == "" {
				cond = "service_started"
			}
			add(k, cond)
		}
	}
	for _, l := range stringList(links) {
		name, _, _ := strings.Cut(l, ":")
		add(name, "service_started")
	}
	return out
}

func parseHealth(v any, key string, note func(k, sev, msg string)) *cHealth {
	m := asMap(v)
	if m == nil {
		return nil
	}
	h := &cHealth{}
	if truthy(m["disable"]) {
		h.None = true
		return h
	}
	switch t := m["test"].(type) {
	case string:
		if strings.EqualFold(strings.TrimSpace(t), "NONE") {
			h.None = true
			return h
		}
		h.Test = []string{"CMD-SHELL", t}
	case []any:
		l := stringList(t)
		switch {
		case len(l) == 0:
		case l[0] == "NONE":
			h.None = true
			return h
		case l[0] == "CMD" || l[0] == "CMD-SHELL":
			h.Test = l
		default:
			h.Test = append([]string{"CMD"}, l...)
		}
	}
	if len(h.Test) < 2 {
		return nil // inherits the image's HEALTHCHECK
	}
	h.Interval, _ = parseDuration(m["interval"])
	h.Timeout, _ = parseDuration(m["timeout"])
	h.StartPeriod, _ = parseDuration(m["start_period"])
	h.Retries, _ = toInt(m["retries"])
	return h
}

func parseNetworks(v any, mode string) ([]cNet, string) {
	var out []cNet
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, cNet{Name: asStr(e)})
		}
	default:
		m := asMap(v)
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			out = append(out, cNet{Name: k, Aliases: stringList(asMap(m[k])["aliases"])})
		}
	}
	return out, mode
}

// dir returns the directory part of a slash path ("" for none).
func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}
