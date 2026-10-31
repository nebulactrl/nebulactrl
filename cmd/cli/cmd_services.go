package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newServicesCmd(c *cli) *cobra.Command {
	return group("services", "Manage services",
		newServicesLsCmd(c),
		newServicesCreateCmd(c),
		newServicesUpdateCmd(c),
		newServicesCloneCmd(c),
		newServicesMoveCmd(c),
		newServicesDockerCmd(c),
		newServicesDeployCmd(c),
		newServicesDeployHookCmd(c),
		newServicesRestartCmd(c),
		newServicesScaleCmd(c),
		newServicesMaintenanceCmd(c),
		newServicesLogsCmd(c),
		newServicesDownloadLogsCmd(c),
		newServicesRunCmd(c),
		newServicesShellCmd(c),
		newServicesBuildPlanCmd(c),
		newServicesProcessesCmd(c),
		newServicesCombineCmd(c),
	)
}

func serviceStatus(s apiclient.ServiceView) string {
	switch {
	case s.LatestDeployment.Id == "":
		return "not deployed"
	case s.Observed.State != "" && s.LatestDeployment.Status == "succeeded":
		if s.Observed.Health != "" && s.Observed.Health != "none" {
			return s.Observed.State + " (" + s.Observed.Health + ")"
		}
		return s.Observed.State
	default:
		return s.LatestDeployment.Status
	}
}

// replicasSummary is "ready/desired", e.g. "2/3".
func replicasSummary(s apiclient.ServiceView) string {
	return fmt.Sprintf("%d/%d", s.Scaling.Ready, s.Scaling.Replicas)
}

func sourceSummary(s apiclient.ServiceView) string {
	if s.SourceType == apiclient.ServiceViewSourceTypeGit {
		src := deref(s.Config.GitUrl)
		if b := deref(s.Config.GitBranch); b != "" {
			src += "@" + b
		}
		return src
	}
	return deref(s.Config.Image)
}

func newServicesLsCmd(c *cli) *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use: "ls --project <project> --env <env>", Short: "List the services of an environment", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" || env == "" {
				return usagef("--project and --env are required")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			p, err := c.resolveProject(ctx, project)
			if err != nil {
				return err
			}
			e, err := c.resolveEnv(ctx, p.Id, env)
			if err != nil {
				return err
			}
			svcs, err := c.servicesOf(ctx, e.Id)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(svcs)
			}
			rows := make([][]string, 0, len(svcs))
			for _, s := range svcs {
				rows = append(rows, []string{
					s.Name, s.Slug, string(s.Kind), orDash(sourceSummary(s)), serviceStatus(s), replicasSummary(s),
					orDash(s.Server.Name), yesNo(s.HasStagedChanges), s.Id,
				})
			}
			c.table([]string{"NAME", "SLUG", "KIND", "SOURCE", "STATUS", "REPLICAS", "SERVER", "STAGED", "ID"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project ID, slug or name")
	cmd.Flags().StringVar(&env, "env", "", "environment ID, slug or name")
	return cmd
}

func newServicesCreateCmd(c *cli) *cobra.Command {
	var project, env, name, image, git, branch, server, buildType, location, buildServer, registry string
	var port int
	cmd := &cobra.Command{
		Use:   "create --project <project> --env <env> --name <name> (--image <ref> | --git <url> [--branch <b>]) [--server <server>] [--port <n>]",
		Short: "Create a service (staged until deployed)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" || env == "" || name == "" {
				return usagef("--project, --env and --name are required")
			}
			if (image == "") == (git == "") {
				return usagef("give exactly one of --image or --git (databases: nebulactrl databases create)")
			}
			if (branch != "" || buildType != "" || location != "" || buildServer != "" || registry != "") && git == "" {
				return usagef("--branch, --build-type, --build-location, --build-server and --registry only apply with --git")
			}
			loc := ""
			if location != "" {
				var err error
				if loc, err = buildLocation(location, true); err != nil {
					return err
				}
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			p, err := c.resolveProject(ctx, project)
			if err != nil {
				return err
			}
			e, err := c.resolveEnv(ctx, p.Id, env)
			if err != nil {
				return err
			}
			in := apiclient.CreateServiceInputBody{Name: name}
			cfg := apiclient.ConfigInput{}
			switch {
			case image != "":
				st := apiclient.CreateServiceInputBodySourceTypeImage
				in.SourceType = &st
				cfg.Image = &image
			default:
				st := apiclient.CreateServiceInputBodySourceTypeGit
				in.SourceType = &st
				cfg.GitUrl = &git
				if branch != "" {
					cfg.GitBranch = &branch
				}
				if buildType != "" {
					cfg.BuildType = &buildType
				}
				if loc != "" {
					l := apiclient.ConfigInputBuildLocation(loc)
					cfg.BuildLocation = &l
				}
				if registry != "" {
					cfg.RegistryId = &registry
				}
			}
			if port > 0 {
				portN := int64(port)
				cfg.Port = &portN
			}
			in.Config = &cfg
			if buildServer != "" {
				bs, err := c.resolveServer(ctx, buildServer)
				if err != nil {
					return err
				}
				cfg.BuildServerId = &bs.Id
			}
			if server != "" {
				s, err := c.resolveServer(ctx, server)
				if err != nil {
					return err
				}
				in.ServerId = &s.Id
			}
			r, err := c.api.CreateServiceWithResponse(ctx, e.Id, &apiclient.CreateServiceParams{IdempotencyKey: c.key()}, in)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Created service %s/%s/%s (%s)", p.Slug, e.Slug, r.JSON201.Slug, r.JSON201.Id)
			c.info("Deploy it with: nebulactrl services deploy %s/%s/%s --wait", p.Slug, e.Slug, r.JSON201.Slug)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "project ID, slug or name")
	f.StringVar(&env, "env", "", "environment ID, slug or name")
	f.StringVar(&name, "name", "", "service name")
	f.StringVar(&image, "image", "", "Docker image, e.g. traefik/whoami:latest")
	f.StringVar(&git, "git", "", "Git repository URL (built on the target server, or on a build server)")
	f.StringVar(&branch, "branch", "", "Git branch (default: the repository default)")
	f.StringVar(&buildType, "build-type", "", "how a git source is built: auto (default), dockerfile, railpack or starter:<node|python|go|ruby|php|static>")
	f.StringVar(&server, "server", "", "target server ID or name")
	f.StringVar(&location, "build-location", "", "where the git source is built: inherit (default), target or build-server")
	f.StringVar(&buildServer, "build-server", "", "pin the build server (ID or name); with --build-location build-server")
	f.StringVar(&registry, "registry", "", "registry ID the build is pushed to (default: the project or team registry)")
	f.IntVar(&port, "port", 0, "HTTP/internal port the service listens on (0 = none)")
	return cmd
}

func newServicesDeployCmd(c *cli) *cobra.Command {
	var wait, noCache bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "deploy <service>", Short: "Deploy a service", Args: exactArgs(1),
		Long: "Starts a deployment and returns once it is accepted (HTTP 202). With --wait, polls until the\n" +
			"deployment reaches a terminal status and exits 0 only when it is `succeeded`.\n" +
			"With --no-cache a git service builds this one deployment without any cache (docker build --no-cache --pull,\n" +
			"no reuse of an image a build server already pushed); later deployments build normally.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				cancel()
				return err
			}
			body := apiclient.DeployServiceJSONRequestBody{}
			if noCache {
				body.NoCache = &noCache
			}
			var planLines []string
			if !c.asJSON {
				planLines = c.processPlanLines(ctx, id)
			}
			r, err := c.api.DeployServiceWithResponse(ctx, id, &apiclient.DeployServiceParams{IdempotencyKey: c.key()}, body)
			cancel()
			if err := check(r, err); err != nil {
				return err
			}
			d := r.JSON202
			if !wait {
				if c.asJSON {
					return c.printJSON(d)
				}
				c.info("Deployment %s accepted (%s, %s)", d.Id, d.Status, d.Strategy)
				for _, l := range planLines {
					c.info("%s", l)
				}
				c.info("Follow it with: nebulactrl deployments status %s", d.Id)
				return nil
			}
			c.note("Deployment %s: %s (%s)", d.Id, d.Status, d.Strategy)
			for _, l := range planLines {
				c.note("%s", l)
			}
			return c.waitAndReport(cmd.Context(), d.Id, d.Status, timeout)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the deployment to finish; exit non-zero unless it succeeded")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "build this deployment without cache (git services; docker build --no-cache --pull)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "give up waiting after this long (with --wait)")
	return cmd
}

func newServicesRestartCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "restart <service>", Short: "Restart a service's container", Args: exactArgs(1),
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
			r, err := c.api.RestartServiceWithResponse(ctx, id, &apiclient.RestartServiceParams{IdempotencyKey: c.key()})
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON202)
			}
			c.info("Restart requested (command %s)", r.JSON202.CommandId)
			return nil
		},
	}
}

// parseAutoscale parses "min:max[:target]" (target defaults to 70) or "off".
func parseAutoscale(v string) (*apiclient.AutoscalingPatch, error) {
	if strings.EqualFold(v, "off") {
		return &apiclient.AutoscalingPatch{Enabled: new(false)}, nil
	}
	parts := strings.Split(v, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, usagef("--autoscale takes min:max[:target] (e.g. 1:5:70) or off")
	}
	nums := make([]int64, 3)
	nums[2] = 70
	for i, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return nil, usagef("--autoscale %q: %q is not a number", v, p)
		}
		nums[i] = n
	}
	return &apiclient.AutoscalingPatch{Enabled: new(true), Min: &nums[0], Max: &nums[1], TargetCpuPercent: &nums[2]}, nil
}

func newServicesScaleCmd(c *cli) *cobra.Command {
	var autoscale, process string
	cmd := &cobra.Command{
		Use: "scale <service> [<replicas>] [--autoscale min:max[:target]|off] [--process <name>]", Short: "Set the number of replicas or turn on autoscaling",
		Args: rangeArgs(1, 2),
		Long: "Runs a stateless service (no volumes, an HTTP port) on 1-10 replicas on its server. The change applies\n" +
			"immediately without a deployment and without a rebuild. --autoscale min:max[:target] scales on CPU\n" +
			"(target percent of the replica's CPU limit, default 70); --autoscale off returns to manual scaling.\n" +
			"A service with several processes scales its default process; --process <name> scales another one\n" +
			"(the same as `services processes scale`).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.scale(cmd, args[0], process, args[1:], autoscale)
		},
	}
	cmd.Flags().StringVar(&autoscale, "autoscale", "", "autoscale on CPU between min and max replicas: min:max[:target%] or off")
	cmd.Flags().StringVar(&process, "process", "", "services with several processes: the process to scale (default: the default process)")
	return cmd
}

// logEntry is one runtime log line.
type logEntry struct {
	TS        time.Time `json:"ts"`
	Line      string    `json:"line"`
	ServiceID string    `json:"serviceId,omitempty"`
}

func newServicesLogsCmd(c *cli) *cobra.Command {
	var follow, timestamps bool
	var tail int
	var process string
	cmd := &cobra.Command{
		Use: "logs <service> [--process <name>]", Short: "Show runtime logs of a service", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			id, err := c.serviceID(ctx, args[0])
			cancel()
			if err != nil {
				return err
			}
			params := &apiclient.GetServiceLogsParams{}
			if process != "" {
				params.Process = &process
			}
			if tail > 0 {
				t := int64(tail)
				params.Tail = &t
			}
			if follow {
				f := true
				params.Follow = &f
				return c.followLogs(cmd.Context(), id, params, timestamps)
			}
			ctx, cancel = c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.GetServiceLogsWithResponse(ctx, id, params)
			if err := check(r, err); err != nil {
				return err
			}
			var out struct {
				Items []logEntry `json:"items"`
			}
			if err := json.Unmarshal(r.GetBody(), &out); err != nil {
				return fmt.Errorf("decode logs: %w", err)
			}
			if out.Items == nil {
				out.Items = []logEntry{}
			}
			if c.asJSON {
				return c.printJSON(out.Items)
			}
			for _, e := range out.Items {
				c.printLogLine(e, timestamps)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new lines until interrupted")
	cmd.Flags().IntVar(&tail, "tail", 200, "number of recent lines to show")
	cmd.Flags().BoolVar(&timestamps, "timestamps", false, "prefix each line with its timestamp")
	cmd.Flags().StringVar(&process, "process", "", "services with several processes: only this process' lines (default: every process, each line prefixed '<process> | ')")
	return cmd
}

func (c *cli) printLogLine(e logEntry, timestamps bool) {
	if timestamps {
		fmt.Fprintf(c.out, "%s %s\n", e.TS.UTC().Format(time.RFC3339Nano), e.Line)
		return
	}
	fmt.Fprintln(c.out, e.Line)
}

// followLogs consumes the SSE log stream until `event: end`, EOF or Ctrl-C.
func (c *cli) followLogs(ctx context.Context, serviceID string, params *apiclient.GetServiceLogsParams, timestamps bool) error {
	res, err := c.stream.GetServiceLogs(ctx, serviceID, params)
	if err := checkRaw(res, err); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer res.Body.Close()
	var decodeErr error
	err = readSSE(res.Body, func(ev sseEvent) bool {
		if ev.Name == "end" {
			return false
		}
		if ev.Name != "message" {
			return true
		}
		if c.asJSON {
			fmt.Fprintln(c.out, ev.Data)
			return true
		}
		var e logEntry
		if err := json.Unmarshal([]byte(ev.Data), &e); err != nil {
			decodeErr = fmt.Errorf("decode log event: %w", err)
			return false
		}
		c.printLogLine(e, timestamps)
		return true
	})
	if decodeErr != nil {
		return decodeErr
	}
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("log stream: %w", err)
	}
	return nil
}

// rangeArgs accepts between lo and hi positional arguments.
func rangeArgs(lo, hi int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.RangeArgs(lo, hi)(cmd, args); err != nil {
			return usageError{fmt.Errorf("%w\nusage: %s", err, cmd.UseLine())}
		}
		return nil
	}
}
