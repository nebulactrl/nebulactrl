package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// dockerFlags are the typed Docker options `services docker` can change. The
// first group works on every server; the second only on a server an instance
// admin made unrestricted (`servers unrestricted`).
type dockerFlags struct {
	capDrop, dns, groupAdd, sysctl, device []string
	noNewPrivileges, readOnly              bool
	pidsLimit                              int64
	hostname, user                         string

	gpus, runtime                          string
	privileged, pidHost, seccomp, apparmor bool
	clear                                  bool
}

func (d *dockerFlags) register(f *pflag.FlagSet) {
	f.StringSliceVar(&d.capDrop, "cap-drop", nil, "capabilities to drop, e.g. ALL or NET_RAW (comma-separated; none clears)")
	f.BoolVar(&d.noNewPrivileges, "no-new-privileges", false, "set the no-new-privileges security option (--no-new-privileges=false turns it off)")
	f.BoolVar(&d.readOnly, "read-only", false, "mount the root filesystem read-only (config files must then sit on a volume)")
	f.Int64Var(&d.pidsLimit, "pids-limit", 0, "limit the processes and threads of the container (0 = no limit)")
	f.StringVar(&d.hostname, "hostname", "", "container hostname (default: the service slug; \"default\" resets)")
	f.StringSliceVar(&d.dns, "dns", nil, "DNS resolver IPs, at most 3 (none clears)")
	f.StringArrayVar(&d.sysctl, "sysctl", nil, "namespaced kernel parameter key=value, repeatable (key= removes it)")
	f.StringVar(&d.user, "user", "", "run as uid[:gid] (\"default\" resets to the image's user)")
	f.StringSliceVar(&d.groupAdd, "group-add", nil, "supplementary numeric group IDs (none clears)")

	f.StringArrayVar(&d.device, "device", nil, "[unrestricted server] host device host[:container][:perms], repeatable; none clears")
	f.StringVar(&d.gpus, "gpus", "", "[unrestricted server] NVIDIA GPUs: all or a count (none clears)")
	f.StringVar(&d.runtime, "runtime", "", "[unrestricted server] OCI runtime, e.g. runsc (default resets)")
	f.BoolVar(&d.privileged, "privileged", false, "[unrestricted server] privileged mode: root on the host")
	f.BoolVar(&d.pidHost, "pid-host", false, "[unrestricted server] share the host's PID namespace")
	f.BoolVar(&d.seccomp, "seccomp-unconfined", false, "[unrestricted server] run without the default seccomp profile")
	f.BoolVar(&d.apparmor, "apparmor-unconfined", false, "[unrestricted server] run without the default AppArmor profile")
	f.BoolVar(&d.clear, "clear", false, "remove every Docker option of the service")
}

// parseDevice parses host[:container][:perms] into a device mapping.
func parseDevice(v string) (map[string]any, error) {
	parts := strings.Split(v, ":")
	if len(parts) == 0 || len(parts) > 3 || parts[0] == "" {
		return nil, usagef("--device %q must be host[:container][:perms], for example /dev/dri/renderD128 or /dev/kvm:/dev/kvm:rw", v)
	}
	d := map[string]any{"hostPath": parts[0]}
	switch len(parts) {
	case 2:
		// host:perms or host:container: a permission set never starts with "/".
		if strings.HasPrefix(parts[1], "/") {
			d["containerPath"] = parts[1]
		} else {
			d["permissions"] = parts[1]
		}
	case 3:
		d["containerPath"], d["permissions"] = parts[1], parts[2]
	}
	return d, nil
}

// parseGPUs parses "all" or a count into a gpus request.
func parseGPUs(v string) (map[string]any, error) {
	if v == "all" {
		return map[string]any{"count": -1, "driver": "nvidia"}, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return nil, usagef("--gpus must be all, a number of GPUs or none")
	}
	return map[string]any{"count": n, "driver": "nvidia"}, nil
}

// applyDockerFlags changes the docker object of a service config from the flags
// that were given. It does not validate: the API does, and says why.
func applyDockerFlags(docker map[string]any, fs *pflag.FlagSet, d *dockerFlags) error {
	set := func(key string, v any, empty bool) {
		if empty {
			delete(docker, key)
			return
		}
		docker[key] = v
	}
	list := func(name, key string, vals []string) {
		if fs.Changed(name) {
			clean := make([]string, 0, len(vals))
			for _, v := range vals {
				if v != "" && v != "none" {
					clean = append(clean, v)
				}
			}
			set(key, clean, len(clean) == 0)
		}
	}
	if d.clear {
		for k := range docker {
			delete(docker, k)
		}
	}
	list("cap-drop", "capDrop", d.capDrop)
	list("dns", "dns", d.dns)
	list("group-add", "groupAdd", d.groupAdd)
	boolean := func(name, key string, v bool) {
		if fs.Changed(name) {
			set(key, v, !v)
		}
	}
	boolean("no-new-privileges", "noNewPrivileges", d.noNewPrivileges)
	boolean("read-only", "readOnlyRootfs", d.readOnly)
	boolean("privileged", "privileged", d.privileged)
	boolean("pid-host", "pidHost", d.pidHost)
	boolean("seccomp-unconfined", "seccompUnconfined", d.seccomp)
	boolean("apparmor-unconfined", "apparmorUnconfined", d.apparmor)
	if fs.Changed("pids-limit") {
		set("pidsLimit", d.pidsLimit, d.pidsLimit == 0)
	}
	if fs.Changed("hostname") {
		set("hostname", d.hostname, d.hostname == "" || d.hostname == "default")
	}
	if fs.Changed("user") {
		set("user", d.user, d.user == "" || d.user == "default")
	}
	if fs.Changed("runtime") {
		set("runtime", d.runtime, d.runtime == "" || d.runtime == "default")
	}
	if fs.Changed("sysctl") {
		sys := map[string]any{}
		if cur, ok := docker["sysctls"].(map[string]any); ok {
			sys = cur
		}
		for _, kv := range d.sysctl {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return usagef("--sysctl %q must be key=value (key= removes it)", kv)
			}
			if v == "" {
				delete(sys, k)
			} else {
				sys[k] = v
			}
		}
		set("sysctls", sys, len(sys) == 0)
	}
	if fs.Changed("device") {
		var devs []any
		for _, v := range d.device {
			if v == "none" || v == "" {
				continue
			}
			m, err := parseDevice(v)
			if err != nil {
				return err
			}
			devs = append(devs, m)
		}
		set("devices", devs, len(devs) == 0)
	}
	if fs.Changed("gpus") {
		if d.gpus == "none" || d.gpus == "" {
			delete(docker, "gpus")
		} else {
			g, err := parseGPUs(d.gpus)
			if err != nil {
				return err
			}
			docker["gpus"] = g
		}
	}
	return nil
}

func newServicesDockerCmd(c *cli) *cobra.Command {
	var d dockerFlags
	cmd := &cobra.Command{
		Use:   "docker <service> [flags]",
		Short: "Show or set a service's Docker options (staged until the next deployment)",
		Long: "Without flags, prints the service's Docker options. With flags, changes them and stages the service. The options are typed:\n" +
			"there is no free-text docker run string. Hardening options (--cap-drop ALL, --no-new-privileges, --read-only, --pids-limit),\n" +
			"--hostname, --dns, curated --sysctl, --user and --group-add work on every server.\n\n" +
			"--device, --gpus, --runtime, --privileged, --pid-host, --seccomp-unconfined, --apparmor-unconfined (and the NET_ADMIN and\n" +
			"SYS_ADMIN capabilities, set in the web UI) work only on a server an instance admin made unrestricted\n" +
			"(`nebulactrl servers unrestricted`): they can hand over the host. Other servers answer 422 requires_unrestricted_server.\n" +
			"Capabilities to add, ulimits, shm size, extra hosts, labels, tmpfs and the entrypoint are edited in the web UI.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fs := cmd.Flags()
			changed := false
			fs.Visit(func(*pflag.Flag) { changed = true })
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			cur, err := c.api.GetServiceWithResponse(ctx, id)
			if err := check(cur, err); err != nil {
				return err
			}
			if cur.JSON200.Kind == "stack" {
				return usagef("service %s is a Compose stack; its containers follow the compose file", args[0])
			}
			raw, err := json.Marshal(cur.JSON200.Config)
			if err != nil {
				return err
			}
			var cfg map[string]any
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return err
			}
			docker, _ := cfg["docker"].(map[string]any)
			if docker == nil {
				docker = map[string]any{}
			}
			if !changed {
				return c.printDocker(cur.JSON200.Name, docker)
			}
			if err := applyDockerFlags(docker, fs, &d); err != nil {
				return err
			}
			if len(docker) == 0 {
				delete(cfg, "docker")
			} else {
				cfg["docker"] = docker
			}
			body, err := json.Marshal(map[string]any{"config": cfg})
			if err != nil {
				return err
			}
			etag := fmt.Sprintf("%q", fmt.Sprint(cur.JSON200.Generation))
			r, err := c.api.UpdateServiceWithBodyWithResponse(ctx, id, &apiclient.UpdateServiceParams{IfMatch: &etag}, "application/json", bytes.NewReader(body))
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.info("Updated the Docker options of %s (%s); staged until the next deployment", r.JSON200.Name, r.JSON200.Id)
			return nil
		},
	}
	d.register(cmd.Flags())
	return cmd
}

// printDocker prints the options as JSON (--json) or one key per line.
func (c *cli) printDocker(name string, docker map[string]any) error {
	if c.asJSON {
		return c.printJSON(docker)
	}
	if len(docker) == 0 {
		c.info("%s has no Docker options (its hostname is its slug).", name)
		return nil
	}
	keys := make([]string, 0, len(docker))
	for k := range docker {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, _ := json.Marshal(docker[k])
		c.info("%-20s %s", k, v)
	}
	return nil
}

func newServersUnrestrictedCmd(c *cli) *cobra.Command {
	var on, off bool
	var confirm string
	cmd := &cobra.Command{
		Use:   "unrestricted <server-id|name> [--on --confirm <name> | --off]",
		Short: "Show, enable or disable the Unrestricted switch of a server (instance admin)",
		Long: "By default Compose stacks and services run under safe allow-lists. An unrestricted server lifts them for everything on it:\n" +
			"privileged mode, host networking, devices, GPUs, a custom runtime, the host PID namespace, unconfined seccomp or AppArmor,\n" +
			"NET_ADMIN, SYS_ADMIN, the Docker socket and host bind mounts. Anyone who can deploy there can take over the server, so only\n" +
			"the instance admin can switch it, and enabling needs --confirm with the server's exact name. Disabling does not stop running\n" +
			"containers; the next deploy of something that needs it fails. Without --on or --off, prints the current state.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if on && off {
				return usagef("give --on or --off, not both")
			}
			if on && confirm == "" {
				return usagef("--on needs --confirm <server name>")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			s, err := c.resolveServer(ctx, args[0])
			if err != nil {
				return err
			}
			if !on && !off {
				r, err := c.api.GetServerUnrestrictedWithResponse(ctx, s.Id)
				if err := check(r, err); err != nil {
					return err
				}
				if c.asJSON {
					return c.printJSON(r.JSON200)
				}
				c.info("Server %s: unrestricted=%t (%d stacks, %d services use options that need it)", s.Name, r.JSON200.Unrestricted, r.JSON200.Stacks, r.JSON200.DockerServices)
				return nil
			}
			in := apiclient.SetServerUnrestrictedJSONRequestBody{Unrestricted: on}
			if on {
				in.Confirm = &confirm
			}
			r, err := c.api.SetServerUnrestrictedWithResponse(ctx, s.Id, in)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.info("Server %s: unrestricted=%t", s.Name, r.JSON200.Unrestricted)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&on, "on", false, "make the server unrestricted")
	f.BoolVar(&off, "off", false, "make the server restricted again")
	f.StringVar(&confirm, "confirm", "", "the server's exact name (required with --on)")
	return cmd
}
