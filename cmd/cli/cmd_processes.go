package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// defaultProcessName is the name of the service's own process when its
// config does not name it.
const defaultProcessName = "web"

// A service runs one default process (its own port, start command and health
// check) plus any number of named processes that share its image, variables,
// volumes and release. The named processes live in config.processes; the
// commands here read the service, change that list and PATCH the whole config
// back with If-Match, like `services update`.

func newServicesProcessesCmd(c *cli) *cobra.Command {
	cmd := group("processes", "Manage the processes of a service",
		newProcessesLsCmd(c),
		newProcessesSetCmd(c),
		newProcessesRmCmd(c),
		newProcessesRenameCmd(c),
		newProcessesScaleCmd(c),
	)
	cmd.Long = "A service has a default process (its own port, start command and health check, named web unless set otherwise)\n" +
		"and any number of named processes (a queue worker, a scheduler) that run the same image with their own command.\n" +
		"They share one build, one set of variables and one release. Adding, changing and removing a process is staged\n" +
		"until the next deployment; replicas and autoscaling apply immediately."
	return cmd
}

// processViewsOf returns the processes of a service, the default first. A
// service without named processes has one: itself.
func processViewsOf(s apiclient.ServiceView) []apiclient.ProcessView {
	if s.Processes != nil && len(*s.Processes) > 0 {
		return *s.Processes
	}
	name := defaultProcessName
	if s.Config.ProcessName != nil && *s.Config.ProcessName != "" {
		name = *s.Config.ProcessName
	}
	return []apiclient.ProcessView{{
		Name:             name,
		Default:          true,
		Port:             deref(s.Config.Port),
		Command:          s.Config.StartCommand,
		Singleton:        deref(s.Config.Singleton),
		StopGraceSeconds: s.Config.StopGraceSeconds,
		Scaling:          s.Scaling,
		Observed: apiclient.ObservedProcessView{
			Name: name, State: s.Observed.State, Health: s.Observed.Health,
			ReplicasReady: s.Observed.ReplicasReady, ReplicasDesired: s.Observed.ReplicasDesired,
		},
	}}
}

func processState(p apiclient.ProcessView) string {
	switch {
	case p.Removed:
		return "removed (until the next deploy)"
	case p.Observed.State == "" && p.Pending:
		return "staged"
	case p.Observed.State == "":
		return "-"
	case p.Observed.Health != "" && p.Observed.Health != "none":
		return p.Observed.State + " (" + p.Observed.Health + ")"
	}
	return p.Observed.State
}

func commandLabel(cmd *[]string) string {
	if cmd == nil || len(*cmd) == 0 {
		return "-"
	}
	return strings.Join(*cmd, " ")
}

func newProcessesLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls <service>", Short: "List the processes of a service", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			r, err := c.api.GetServiceWithResponse(ctx, id)
			if err := check(r, err); err != nil {
				return err
			}
			ps := processViewsOf(*r.JSON200)
			if c.asJSON {
				return c.printJSON(ps)
			}
			rows := make([][]string, 0, len(ps))
			for _, p := range ps {
				port := "-"
				if p.Port > 0 {
					port = strconv.FormatInt(p.Port, 10)
				}
				name := p.Name
				if p.Singleton {
					name += " (singleton)"
				}
				rows = append(rows, []string{
					name, yesNo(p.Default), port, commandLabel(p.Command),
					fmt.Sprintf("%d/%d", p.Scaling.Ready, p.Scaling.Replicas), processState(p),
				})
			}
			c.table([]string{"NAME", "DEFAULT", "PORT", "COMMAND", "REPLICAS", "STATE"}, rows)
			return nil
		},
	}
}

// healthFlags are the health check flags of `processes set`.
type healthFlags struct {
	typ                                                string
	path                                               string
	port                                               int
	interval, timeout, startPeriod, deadline, successN int
	command                                            []string
}

func (h *healthFlags) add(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&h.typ, "health-type", "", "health check: none, http, tcp or exec")
	f.StringVar(&h.path, "health-path", "", "health check HTTP path")
	f.IntVar(&h.port, "health-port", 0, "health check port (default: the process' port)")
	f.IntVar(&h.interval, "health-interval", 0, "seconds between health checks")
	f.IntVar(&h.timeout, "health-timeout", 0, "seconds one health check may take")
	f.IntVar(&h.startPeriod, "health-start-period", 0, "seconds after start during which failures do not count")
	f.IntVar(&h.deadline, "health-deadline", 0, "seconds the process has to become healthy")
	f.IntVar(&h.successN, "health-success-threshold", 0, "consecutive successes that make the process healthy")
	f.StringArrayVar(&h.command, "health-command", nil, "exec health check command, repeat per argument (CMD or CMD-SHELL first)")
}

var healthChanged = []string{
	"health-type", "health-path", "health-port", "health-interval", "health-timeout",
	"health-start-period", "health-deadline", "health-success-threshold", "health-command",
}

// apply writes the changed health flags into t["healthcheck"].
func (h *healthFlags) apply(f interface{ Changed(string) bool }, t map[string]any) error {
	touched := false
	for _, n := range healthChanged {
		touched = touched || f.Changed(n)
	}
	if !touched {
		return nil
	}
	hc, _ := t["healthcheck"].(map[string]any)
	if hc == nil {
		hc = map[string]any{}
	}
	ints := []struct {
		flag, key string
		v         int
	}{
		{"health-port", "port", h.port}, {"health-interval", "intervalSeconds", h.interval}, {"health-timeout", "timeoutSeconds", h.timeout},
		{"health-start-period", "startPeriodSeconds", h.startPeriod}, {"health-deadline", "deadlineSeconds", h.deadline},
		{"health-success-threshold", "successThreshold", h.successN},
	}
	if f.Changed("health-type") {
		hc["type"] = h.typ
	}
	if f.Changed("health-path") {
		hc["path"] = h.path
	}
	for _, i := range ints {
		if !f.Changed(i.flag) {
			continue
		}
		if i.v < 0 {
			return usagef("--%s must not be negative", i.flag)
		}
		hc[i.key] = i.v
	}
	if f.Changed("health-command") {
		hc["command"] = h.command
	}
	t["healthcheck"] = hc
	return nil
}

// asInt reads a JSON number of a decoded config.
func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// adjustHealthPort keeps the health check of a process on its port when the
// port changes: an http or tcp check that watched the old port follows the new
// one, and a process that gains its first port gets an http check.
func adjustHealthPort(t map[string]any, oldPort, newPort int) {
	hc, _ := t["healthcheck"].(map[string]any)
	if hc == nil {
		return // a new process: the server fills the defaults from the port
	}
	typ, _ := hc["type"].(string)
	switch {
	case (typ == "" || typ == "none") && oldPort == 0 && newPort > 0:
		hc["type"] = "http"
		hc["port"] = newPort
		if p, _ := hc["path"].(string); p == "" {
			hc["path"] = "/"
		}
	case (typ == "http" || typ == "tcp") && asInt(hc["port"]) == oldPort:
		if newPort == 0 {
			hc["type"] = "none"
			delete(hc, "port")
		} else {
			hc["port"] = newPort
		}
	}
}

// cfgProcesses returns the named processes of a decoded config.
func cfgProcesses(cfg map[string]any) []map[string]any {
	raw, _ := cfg["processes"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func setCfgProcesses(cfg map[string]any, ps []map[string]any) {
	if len(ps) == 0 {
		delete(cfg, "processes")
		return
	}
	l := make([]any, len(ps))
	for i, p := range ps {
		l[i] = p
	}
	cfg["processes"] = l
}

func processNameOf(cfg map[string]any) string {
	if s, _ := cfg["processName"].(string); s != "" {
		return s
	}
	return defaultProcessName
}

func processNames(cfg map[string]any) []string {
	names := []string{processNameOf(cfg)}
	for _, p := range cfgProcesses(cfg) {
		if n, _ := p["name"].(string); n != "" {
			names = append(names, n)
		}
	}
	return names
}

func findProcess(ps []map[string]any, name string) int {
	return slices.IndexFunc(ps, func(p map[string]any) bool { n, _ := p["name"].(string); return n == name })
}

// loadServiceConfig reads a service and its config as a generic map, so keys
// this client does not know survive the round trip.
func (c *cli) loadServiceConfig(ctx context.Context, ref string) (string, *apiclient.ServiceView, map[string]any, error) {
	id, err := c.serviceID(ctx, ref)
	if err != nil {
		return "", nil, nil, err
	}
	cur, err := c.api.GetServiceWithResponse(ctx, id)
	if err := check(cur, err); err != nil {
		return "", nil, nil, err
	}
	if cur.JSON200.Kind == "stack" {
		return "", nil, nil, usagef("service %s is a Compose stack and has no processes", ref)
	}
	raw, err := json.Marshal(cur.JSON200.Config)
	if err != nil {
		return "", nil, nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", nil, nil, err
	}
	return id, cur.JSON200, cfg, nil
}

// putConfig replaces the whole config of a service (If-Match the generation
// that was read); rename maps old process names to new ones.
func (c *cli) putConfig(ctx context.Context, id string, cur *apiclient.ServiceView, cfg map[string]any, rename map[string]string) (*apiclient.ServiceView, error) {
	body := map[string]any{"config": cfg}
	if len(rename) > 0 {
		body["renameProcesses"] = rename
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	etag := fmt.Sprintf("%q", fmt.Sprint(cur.Generation))
	r, err := c.api.UpdateServiceWithBodyWithResponse(ctx, id, &apiclient.UpdateServiceParams{IfMatch: &etag}, "application/json", bytes.NewReader(raw))
	if err := check(r, err); err != nil {
		return nil, err
	}
	return r.JSON200, nil
}

func unknownProcess(cfg map[string]any, name string) error {
	return usagef("the service has no process %q (processes: %s)", name, strings.Join(processNames(cfg), ", "))
}

// commandFlag resolves the start command from --command (repeated), --command-json
// or the arguments after `--`; exactly one source may be given.
func commandFlag(argv []string, argvSet bool, js string, jsSet bool, trailing []string) ([]string, bool, error) {
	sources := 0
	for _, b := range []bool{argvSet, jsSet, len(trailing) > 0} {
		if b {
			sources++
		}
	}
	switch {
	case sources == 0:
		return nil, false, nil
	case sources > 1:
		return nil, false, usagef("give the command once: --command (repeated), --command-json or arguments after --")
	}
	cmd := argv
	switch {
	case jsSet:
		if err := json.Unmarshal([]byte(js), &cmd); err != nil {
			return nil, false, usagef("--command-json must be a JSON array of strings: %v", err)
		}
	case len(trailing) > 0:
		cmd = trailing
	}
	if len(cmd) == 0 {
		return nil, false, usagef("the start command needs at least one element")
	}
	for _, a := range cmd {
		if a == "" {
			return nil, false, usagef("the start command must not contain an empty element")
		}
	}
	return cmd, true, nil
}

func newProcessesSetCmd(c *cli) *cobra.Command {
	var (
		command                   []string
		commandJSON, newName      string
		port, stopGrace, memoryMB int
		cpus                      float64
		singleton, isDefault      bool
		h                         healthFlags
	)
	cmd := &cobra.Command{
		Use:   "set <service> [<name>] [--command ARG... | --command-json '[...]'] [--port N] [--stop-grace-seconds N] [--singleton[=false]] [--memory-mb N] [--cpus F] [--health-type ...] [--default] [--name NEW]",
		Short: "Add a process or change one (staged until the next deployment)",
		Long: "Adds the process <name> when the service has none of that name (a command is required), otherwise changes the\n" +
			"settings you give and keeps the rest. The command is an argv list, no shell: repeat --command per element, give\n" +
			"--command-json '[\"bin/rails\",\"sidekiq\"]', or put it after `--`.\n\n" +
			"--default (or the default process' own name) edits the service's own process: its command, port, health check,\n" +
			"stop grace, limits and singleton flag. --name renames the default process. Other processes are renamed with\n" +
			"`processes rename`. Singleton: never two instances at once; the old one stops before the new one starts.",
		Example: "  nebulactrl services processes set acme/production/web worker --command bundle --command exec --command sidekiq --stop-grace-seconds 120\n" +
			"  nebulactrl services processes set web scheduler --singleton -- php artisan schedule:work\n" +
			"  nebulactrl services processes set web --default --port 3000",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var trailing []string
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				trailing = args[dash:]
				args = args[:dash]
			}
			if len(args) < 1 || len(args) > 2 {
				return usagef("expected <service> and a process name\nusage: %s", cmd.UseLine())
			}
			f := cmd.Flags()
			argv, haveCmd, err := commandFlag(command, f.Changed("command"), commandJSON, f.Changed("command-json"), trailing)
			if err != nil {
				return err
			}
			changed := haveCmd || f.Changed("name")
			for _, n := range append([]string{"port", "stop-grace-seconds", "singleton", "memory-mb", "cpus"}, healthChanged...) {
				changed = changed || f.Changed(n)
			}
			for n, v := range map[string]int{"port": port, "stop-grace-seconds": stopGrace, "memory-mb": memoryMB} {
				if f.Changed(n) && v < 0 {
					return usagef("--%s must not be negative", n)
				}
			}
			if f.Changed("port") && port > 65535 {
				return usagef("--port must be between 0 and 65535")
			}
			if f.Changed("cpus") && cpus < 0 {
				return usagef("--cpus must not be negative")
			}
			if f.Changed("health-type") {
				if h.typ != "none" && h.typ != "http" && h.typ != "tcp" && h.typ != "exec" {
					return usagef("--health-type must be none, http, tcp or exec")
				}
			}
			name := ""
			if len(args) == 2 {
				name = args[1]
			}
			if name == "" && !isDefault {
				return usagef("give the process <name>, or --default for the service's own process")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, cur, cfg, err := c.loadServiceConfig(ctx, args[0])
			if err != nil {
				return err
			}
			defName := processNameOf(cfg)
			if isDefault && name != "" && name != defName {
				return usagef("--default edits the default process %q, not %q", defName, name)
			}
			isDef := isDefault || name == defName
			if f.Changed("name") && !isDef {
				return usagef("--name renames the default process; rename %q with `processes rename`", name)
			}
			if f.Changed("name") && newName == "" {
				return usagef("--name must not be empty")
			}

			var target map[string]any
			var list []map[string]any
			created := false
			if isDef {
				target = cfg
				name = defName
			} else {
				list = cfgProcesses(cfg)
				if i := findProcess(list, name); i >= 0 {
					target = list[i]
				} else {
					if !haveCmd {
						return usagef("a new process needs its start command: --command, --command-json or arguments after --")
					}
					target = map[string]any{"name": name}
					list = append(list, target)
					created = true
				}
			}
			if !changed && !created {
				return usagef("give at least one setting to change")
			}
			if haveCmd {
				target["startCommand"] = argv
			}
			if f.Changed("port") {
				old := asInt(target["port"])
				if port == 0 {
					delete(target, "port")
				} else {
					target["port"] = port
				}
				adjustHealthPort(target, old, port)
			}
			if f.Changed("stop-grace-seconds") {
				target["stopGraceSeconds"] = stopGrace
			}
			if f.Changed("singleton") {
				target["singleton"] = singleton
			}
			if f.Changed("memory-mb") {
				target["memoryMb"] = memoryMB
			}
			if f.Changed("cpus") {
				target["cpus"] = cpus
			}
			if err := h.apply(f, target); err != nil {
				return err
			}
			if isDef {
				if f.Changed("name") {
					if newName == defaultProcessName {
						delete(cfg, "processName")
					} else {
						cfg["processName"] = newName
					}
					name = newName
				}
			} else {
				setCfgProcesses(cfg, list)
			}
			v, err := c.putConfig(ctx, id, cur, cfg, nil)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			verb := "Updated"
			if created {
				verb = "Added"
			}
			c.info("%s process %s of %s (%s), staged until the next deployment", verb, name, v.Name, v.Id)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&command, "command", nil, "start command, one element per flag (argv, no shell)")
	f.StringVar(&commandJSON, "command-json", "", "start command as a JSON array of strings")
	f.IntVar(&port, "port", 0, "HTTP port the process listens on (0: none); unique across the service's processes")
	f.IntVar(&stopGrace, "stop-grace-seconds", 0, "seconds between the stop signal and the kill (0: the service's value)")
	f.BoolVar(&singleton, "singleton", false, "never run two instances at once (--singleton=false to allow it)")
	f.IntVar(&memoryMB, "memory-mb", 0, "memory limit in MB (0: the service's limit)")
	f.Float64Var(&cpus, "cpus", 0, "CPU limit (0: the service's limit)")
	f.BoolVar(&isDefault, "default", false, "edit the service's own (default) process")
	f.StringVar(&newName, "name", "", "with --default: rename the default process")
	h.add(cmd)
	return cmd
}

func newProcessesRmCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rm <service> <name>", Short: "Remove a process (staged until the next deployment)", Args: exactArgs(2),
		Long: "Removes a named process from the service's configuration. The running process stops at the next deployment.\n" +
			"A process that still has a domain cannot be removed: remove the domain first. The default process is the service\n" +
			"itself and cannot be removed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, cur, cfg, err := c.loadServiceConfig(ctx, args[0])
			if err != nil {
				return err
			}
			name := args[1]
			if name == processNameOf(cfg) {
				return usagef("%q is the default process, the service itself; it cannot be removed", name)
			}
			list := cfgProcesses(cfg)
			i := findProcess(list, name)
			if i < 0 {
				return unknownProcess(cfg, name)
			}
			setCfgProcesses(cfg, slices.Delete(list, i, i+1))
			v, err := c.putConfig(ctx, id, cur, cfg, nil)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			c.info("Removed process %s from %s (%s), staged until the next deployment", name, v.Name, v.Id)
			return nil
		},
	}
}

func newProcessesRenameCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rename <service> <old> <new>", Short: "Rename a process (staged until the next deployment)", Args: exactArgs(3),
		Long: "Renames a process and keeps its replica setting and its domains. The default process is renamed too. A renamed\n" +
			"process is a new set of containers: the old ones stop at the next deployment.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, cur, cfg, err := c.loadServiceConfig(ctx, args[0])
			if err != nil {
				return err
			}
			from, to := args[1], args[2]
			if from == to {
				return usagef("the process is already named %q", to)
			}
			if slices.Contains(processNames(cfg), to) {
				return usagef("the service already has a process %q", to)
			}
			var rename map[string]string
			if from == processNameOf(cfg) {
				if to == defaultProcessName {
					delete(cfg, "processName")
				} else {
					cfg["processName"] = to
				}
			} else {
				list := cfgProcesses(cfg)
				i := findProcess(list, from)
				if i < 0 {
					return unknownProcess(cfg, from)
				}
				list[i]["name"] = to
				setCfgProcesses(cfg, list)
				rename = map[string]string{from: to}
			}
			v, err := c.putConfig(ctx, id, cur, cfg, rename)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			c.info("Renamed process %s to %s on %s (%s), staged until the next deployment", from, to, v.Name, v.Id)
			return nil
		},
	}
}

func newProcessesScaleCmd(c *cli) *cobra.Command {
	var autoscale string
	cmd := &cobra.Command{
		Use: "scale <service> <name> [<replicas>] [--autoscale min:max[:target]|off]", Short: "Set the replicas or autoscaling of one process",
		Args: rangeArgs(2, 3),
		Long: "Like `services scale`, for one process: runs it on 1-10 replicas, or scales it on CPU. The change applies\n" +
			"immediately without a deployment. A process with volumes or files on the service, or a singleton, runs one instance.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.scale(cmd, args[0], args[1], args[2:], autoscale)
		},
	}
	cmd.Flags().StringVar(&autoscale, "autoscale", "", "autoscale on CPU between min and max replicas: min:max[:target%] or off")
	return cmd
}

// scale is `services scale` and `services processes scale`: process "" is the
// default process through the original endpoint.
func (c *cli) scale(cmd *cobra.Command, service, process string, rest []string, autoscale string) error {
	if (len(rest) == 1) == (autoscale != "") {
		return usagef("give either <replicas> or --autoscale")
	}
	var replicas *int64
	var as *apiclient.AutoscalingPatch
	if len(rest) == 1 {
		n, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil || n < 1 || n > 10 {
			return usagef("replicas must be a number from 1 to 10")
		}
		replicas = &n
	} else {
		a, err := parseAutoscale(autoscale)
		if err != nil {
			return err
		}
		as = a
	}
	if err := c.connect(); err != nil {
		return err
	}
	ctx, cancel := c.ctx(cmd.Context())
	defer cancel()
	id, err := c.serviceID(ctx, service)
	if err != nil {
		return err
	}
	var v *apiclient.ServiceView
	if process == "" {
		r, err := c.api.SetServiceScalingWithResponse(ctx, id, &apiclient.SetServiceScalingParams{IdempotencyKey: c.key()},
			apiclient.SetServiceScalingJSONRequestBody{Replicas: replicas, Autoscaling: as})
		if err := check(r, err); err != nil {
			return err
		}
		v = r.JSON200
	} else {
		r, err := c.api.SetProcessScalingWithResponse(ctx, id, process, &apiclient.SetProcessScalingParams{IdempotencyKey: c.key()},
			apiclient.SetProcessScalingJSONRequestBody{Replicas: replicas, Autoscaling: as})
		if err := check(r, err); err != nil {
			return err
		}
		v = r.JSON200
	}
	if c.asJSON {
		return c.printJSON(v)
	}
	sc, prefix := v.Scaling, ""
	if process != "" {
		prefix = "Process " + process + ": "
		for _, p := range processViewsOf(*v) {
			if p.Name == process {
				sc = p.Scaling
			}
		}
	}
	if sc.Autoscaling.Enabled {
		c.info("%sAutoscaling %d-%d replicas at %d%% CPU; running %d (%d ready)", prefix, sc.Autoscaling.Min, sc.Autoscaling.Max, sc.Autoscaling.TargetCpuPercent, sc.Replicas, sc.Ready)
	} else {
		c.info("%sReplicas: %d desired, %d ready", prefix, sc.Replicas, sc.Ready)
	}
	return nil
}

// processPlanLines describes how a deployment would roll a service's
// processes, from the deployment plan; nothing for a single-process service or
// when the plan is unavailable (the deployment itself reports a refusal).
func (c *cli) processPlanLines(ctx context.Context, serviceID string) []string {
	r, err := c.api.PlanDeploymentWithResponse(ctx, serviceID)
	if err != nil || r.JSON200 == nil || r.JSON200.Processes == nil || len(*r.JSON200.Processes) < 2 {
		return nil
	}
	ps := slices.Clone(*r.JSON200.Processes)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Order < ps[j].Order })
	lines := make([]string, 0, len(ps))
	for _, p := range ps {
		parts := []string{"no port"}
		if p.Port > 0 {
			parts[0] = fmt.Sprintf("port %d", p.Port)
		}
		parts = append(parts, string(p.Strategy), fmt.Sprintf("%d replica(s)", p.Replicas))
		if p.Singleton {
			parts = append(parts, "singleton")
		}
		if p.WillChange {
			parts = append(parts, "changes")
		} else {
			parts = append(parts, "unchanged")
		}
		name := p.Name
		if p.Default {
			name += " (default)"
		}
		lines = append(lines, fmt.Sprintf("  %d. %s: %s", p.Order, name, strings.Join(parts, ", ")))
	}
	return lines
}
