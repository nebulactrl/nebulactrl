package main

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// dockerKeys are the compose service keys dockerOptions maps into the
// template's `docker:` block. A key that does not map (or that a restricted
// server would refuse) is listed as dropped for review.
var dockerKeys = map[string]bool{
	"entrypoint": true, "cap_add": true, "cap_drop": true, "security_opt": true, "read_only": true,
	"pids_limit": true, "hostname": true, "dns": true, "sysctls": true, "user": true, "group_add": true,
	"ulimits": true, "shm_size": true, "extra_hosts": true, "init": true, "tmpfs": true, "stop_signal": true,
}

// unsafeCapAdd reports a cap_add list that a restricted server refuses; such a
// service is not translated (the translator never emits requiresUnrestricted).
func unsafeCapAdd(raw any) bool {
	if raw == nil {
		return false
	}
	probe := agentprotocol.DockerOptions{CapAdd: strList(raw)}
	return probe.Validate(false) != nil
}

// strList reads a compose list, map or single value as strings ("k=v" for maps).
func strList(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			out = append(out, scalar(it))
		}
		return out
	case map[string]any:
		out := make([]string, 0, len(x))
		for _, k := range sortedKeys(x) {
			out = append(out, k+"="+scalar(x[k]))
		}
		return out
	default:
		return []string{scalar(x)}
	}
}

var sizeRe = regexp.MustCompile(`(?i)^\s*([0-9.]+)\s*([kmg]?)b?\s*$`)

// sizeMB converts a compose size ("1gb", "256m", bytes) to MiB.
func sizeMB(s string) int {
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	switch strings.ToLower(m[2]) {
	case "g":
		f *= 1024
	case "k":
		f /= 1024
	case "":
		f /= 1024 * 1024
	}
	if f < 1 {
		return 0
	}
	return int(f)
}

// optionGroups copies one compose-mapped option at a time, so each can be
// validated (and dropped) on its own.
var optionGroups = []struct {
	key  string
	copy func(dst, src *agentprotocol.DockerOptions)
}{
	{"entrypoint", func(d, s *agentprotocol.DockerOptions) { d.Entrypoint = s.Entrypoint }},
	{"cap_add", func(d, s *agentprotocol.DockerOptions) { d.CapAdd = s.CapAdd }},
	{"cap_drop", func(d, s *agentprotocol.DockerOptions) { d.CapDrop = s.CapDrop }},
	{"security_opt", func(d, s *agentprotocol.DockerOptions) { d.NoNewPrivileges = s.NoNewPrivileges }},
	{"read_only", func(d, s *agentprotocol.DockerOptions) { d.ReadOnlyRootfs = s.ReadOnlyRootfs }},
	{"pids_limit", func(d, s *agentprotocol.DockerOptions) { d.PidsLimit = s.PidsLimit }},
	{"hostname", func(d, s *agentprotocol.DockerOptions) { d.Hostname = s.Hostname }},
	{"dns", func(d, s *agentprotocol.DockerOptions) { d.DNS = s.DNS }},
	{"sysctls", func(d, s *agentprotocol.DockerOptions) { d.Sysctls = s.Sysctls }},
	{"user", func(d, s *agentprotocol.DockerOptions) { d.User = s.User }},
	{"group_add", func(d, s *agentprotocol.DockerOptions) { d.GroupAdd = s.GroupAdd }},
	{"ulimits", func(d, s *agentprotocol.DockerOptions) { d.Ulimits = s.Ulimits }},
	{"shm_size", func(d, s *agentprotocol.DockerOptions) { d.ShmSizeMB = s.ShmSizeMB }},
	{"extra_hosts", func(d, s *agentprotocol.DockerOptions) { d.ExtraHosts = s.ExtraHosts }},
	{"init", func(d, s *agentprotocol.DockerOptions) { d.Init = s.Init }},
	{"stop_signal", func(d, s *agentprotocol.DockerOptions) { d.StopSignal = s.StopSignal }},
	{"tmpfs", func(d, s *agentprotocol.DockerOptions) { d.Tmpfs = s.Tmpfs }},
}

// dockerOptions maps the container options of a compose service to the
// template's docker block. What does not map, or does not validate for a
// restricted server, goes to st.dropped so a reviewer sees it.
func (t *translator) dockerOptions(st *svcState) *templates.DockerBlock {
	c := st.c
	raw := c.Raw
	var o agentprotocol.DockerOptions
	drop := func(k, why string) {
		if why != "" {
			k += " (" + why + ")"
		}
		st.dropped = append(st.dropped, k)
	}

	// entrypoint, and `entrypoint: []`, which is expressed as the command's program.
	if v, ok := raw["entrypoint"]; ok {
		var argv []string
		switch e := v.(type) {
		case []any:
			for _, a := range e {
				argv = append(argv, scalar(a))
			}
		case nil:
		default:
			split, err := shellSplit(scalar(e))
			if err != nil {
				drop("entrypoint", err.Error())
			}
			argv = split
		}
		switch {
		case len(argv) > 0:
			for _, a := range argv {
				o.Entrypoint = append(o.Entrypoint, t.hosts(t.expand(a, st, ctxArg), "", st, false))
			}
		case len(c.Command) > 0:
			// Cleared entrypoint: the compose command is the whole argv. Its
			// program moves into the entrypoint; the rest stays the start command.
			o.Entrypoint = []string{t.hosts(t.expand(c.Command[0], st, ctxArg), "", st, false)}
			st.svc.Command = nil
			for _, a := range c.Command[1:] {
				st.svc.Command = append(st.svc.Command, t.hosts(t.expand(a, st, ctxArg), "", st, false))
			}
			st.commandDone = true
			t.review("services."+st.key+".docker.entrypoint", "entrypoint: [] in the source; the command's program moved into the entrypoint")
		default:
			drop("entrypoint", "an empty entrypoint without a command cannot be expressed")
		}
	}

	o.CapAdd = strList(raw["cap_add"])
	o.CapDrop = strList(raw["cap_drop"])
	for _, so := range strList(raw["security_opt"]) {
		switch strings.ReplaceAll(so, "=", ":") {
		case "no-new-privileges", "no-new-privileges:true":
			o.NoNewPrivileges = true
		default:
			drop("security_opt "+so, "not allowed")
		}
	}
	o.ReadOnlyRootfs = raw["read_only"] == true
	if v := scalar(raw["pids_limit"]); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			o.PidsLimit = n
		} else {
			drop("pids_limit", "unlimited or invalid")
		}
	}
	o.Hostname = scalar(raw["hostname"])
	o.DNS = strList(raw["dns"])
	if sc, ok := raw["sysctls"]; ok {
		o.Sysctls = map[string]string{}
		for _, kv := range strList(sc) {
			k, v, _ := strings.Cut(kv, "=")
			o.Sysctls[k] = v
		}
	}
	o.User = scalar(raw["user"])
	o.GroupAdd = strList(raw["group_add"])
	if ul, ok := raw["ulimits"].(map[string]any); ok {
		for _, name := range sortedKeys(ul) {
			var soft, hard int64
			switch x := ul[name].(type) {
			case map[string]any:
				soft, _ = strconv.ParseInt(scalar(x["soft"]), 10, 64)
				hard, _ = strconv.ParseInt(scalar(x["hard"]), 10, 64)
			default:
				soft, _ = strconv.ParseInt(scalar(x), 10, 64)
				hard = soft
			}
			o.Ulimits = append(o.Ulimits, agentprotocol.Ulimit{Name: name, Soft: soft, Hard: hard})
		}
	}
	if v := scalar(raw["shm_size"]); v != "" {
		if o.ShmSizeMB = sizeMB(v); o.ShmSizeMB == 0 {
			drop("shm_size", "unreadable size "+v)
		}
	}
	o.ExtraHosts = strList(raw["extra_hosts"])
	for i, h := range o.ExtraHosts {
		o.ExtraHosts[i] = strings.Replace(h, "=", ":", 1)
	}
	o.Init = raw["init"] == true
	o.StopSignal = scalar(raw["stop_signal"])
	for _, tm := range strList(raw["tmpfs"]) {
		p, opts, _ := strings.Cut(tm, ":")
		mt := agentprotocol.TmpfsMount{Path: p}
		for _, kv := range strings.Split(opts, ",") {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "size":
				mt.SizeMB = sizeMB(v)
			case "mode":
				mt.Mode = v
			}
		}
		o.Tmpfs = append(o.Tmpfs, mt)
	}

	// Keep every option a restricted server accepts; list the rest.
	var keep agentprotocol.DockerOptions
	for _, g := range optionGroups {
		var probe agentprotocol.DockerOptions
		g.copy(&probe, &o)
		if probe.IsZero() {
			continue
		}
		if err := probe.Validate(false); err != nil {
			drop(g.key, err.Error())
			continue
		}
		g.copy(&keep, &o)
	}
	if err := keep.Validate(false); err != nil {
		drop("docker options", err.Error())
		return nil
	}
	if keep.IsZero() {
		return nil
	}
	return &templates.DockerBlock{DockerOptions: keep}
}
