package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// Build servers: where git services are built and which registry the images go to.
// The location resolves service, then project, then team; the three levels share
// the values below.

// buildLocation normalizes a --build-location value (build-server is accepted for build_server).
func buildLocation(v string, allowInherit bool) (string, error) {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(v)), "-", "_") {
	case "target":
		return "target", nil
	case "build_server":
		return "build_server", nil
	case "inherit":
		if allowInherit {
			return "inherit", nil
		}
	}
	if allowInherit {
		return "", usagef("--build-location must be inherit, target or build-server")
	}
	return "", usagef("--build-location must be target or build-server")
}

// buildRole maps --role to the two server flags: accept workloads and build enabled.
func buildRole(role string) (accept, build bool, err error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "deployments":
		return true, false, nil
	case "builds":
		return false, true, nil
	case "both":
		return true, true, nil
	}
	return false, false, usagef("--role must be deployments, builds or both")
}

func newServersUpdateCmd(c *cli) *cobra.Command {
	var role string
	var maxBuilds int
	var allowEmulation, allowPreviews bool
	cmd := &cobra.Command{
		Use:   "update <server-id|name> [--role deployments|builds|both] [--max-builds <n>] [--allow-emulation[=false]] [--allow-previews[=false]]",
		Short: "Set a server's role and build settings",
		Long: "Roles: `deployments` runs services (the default), `builds` only builds images for other servers and pushes them\n" +
			"to a registry, `both` does both. A build server needs an agent that can build and push (`build.push`).\n" +
			"--max-builds caps parallel builds (1-16, default 2; more queue). --allow-emulation lets it build for another\n" +
			"CPU architecture (slow). --allow-previews lets pull request previews build on it (off: previews build on their own server).",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if !f.Changed("role") && !f.Changed("max-builds") && !f.Changed("allow-emulation") && !f.Changed("allow-previews") {
				return usagef("give at least one of --role, --max-builds, --allow-emulation, --allow-previews")
			}
			in := apiclient.UpdateServerInputBody{}
			if f.Changed("role") {
				accept, build, err := buildRole(role)
				if err != nil {
					return err
				}
				in.AcceptWorkloads, in.BuildEnabled = &accept, &build
			}
			if f.Changed("max-builds") {
				if maxBuilds < 1 || maxBuilds > 16 {
					return usagef("--max-builds must be between 1 and 16")
				}
				n := int64(maxBuilds)
				in.BuildMaxConcurrent = &n
			}
			if f.Changed("allow-emulation") {
				in.BuildAllowEmulation = &allowEmulation
			}
			if f.Changed("allow-previews") {
				in.BuildAllowPreviews = &allowPreviews
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
			r, err := c.api.UpdateServerWithResponse(ctx, s.Id, in)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			v := r.JSON200
			c.info("Updated server %s (%s): role %s, max builds %d, emulation %s, previews %s",
				v.Name, v.Id, serverRole(v.AcceptWorkloads, v.BuildEnabled), v.BuildMaxConcurrent, onOff(v.BuildAllowEmulation), onOff(v.BuildAllowPreviews))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&role, "role", "", "deployments, builds or both")
	f.IntVar(&maxBuilds, "max-builds", 0, "parallel builds on this server (1-16)")
	f.BoolVar(&allowEmulation, "allow-emulation", false, "allow cross-architecture builds through emulation")
	f.BoolVar(&allowPreviews, "allow-previews", false, "let pull request previews build here")
	return cmd
}

func serverRole(accept, build bool) string {
	switch {
	case accept && build:
		return "both"
	case build:
		return "builds"
	}
	return "deployments"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func newProjectsUpdateCmd(c *cli) *cobra.Command {
	var name, description, location, server, registry string
	cmd := &cobra.Command{
		Use:   "update <project> [--name <n>] [--description <d>] [--build-location inherit|target|build-server [--build-server <server>] [--registry <id>]]",
		Short: "Rename a project or set where its services are built",
		Long: "--build-location overrides the team default for every git service of the project (a service can override it again).\n" +
			"The placement is replaced as a unit: --build-server pins a build server and --registry names the registry; an\n" +
			"omitted one is cleared (the control plane picks a build server, the team default registry applies).",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if !f.Changed("name") && !f.Changed("description") && !f.Changed("build-location") {
				return usagef("give at least one of --name, --description, --build-location")
			}
			if !f.Changed("build-location") && (f.Changed("build-server") || f.Changed("registry")) {
				return usagef("--build-server and --registry are set together with --build-location")
			}
			in := apiclient.UpdateProjectInputBody{}
			if f.Changed("name") {
				in.Name = &name
			}
			if f.Changed("description") {
				in.Description = &description
			}
			if f.Changed("build-location") {
				loc, err := buildLocation(location, true)
				if err != nil {
					return err
				}
				l := apiclient.UpdateProjectInputBodyBuildLocation(loc)
				in.BuildLocation = &l
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			p, err := c.resolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			if f.Changed("build-location") {
				if server != "" {
					s, err := c.resolveServer(ctx, server)
					if err != nil {
						return err
					}
					in.BuildServerId = &s.Id
				}
				if registry != "" {
					in.RegistryId = &registry
				}
			}
			r, err := c.api.UpdateProjectWithResponse(ctx, p.Id, in)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			v := r.JSON200
			c.info("Updated project %s (%s): build location %s", v.Name, v.Id, v.BuildLocation)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "new name")
	f.StringVar(&description, "description", "", "new description")
	f.StringVar(&location, "build-location", "", "inherit, target or build-server")
	f.StringVar(&server, "build-server", "", "pin the build server (ID or name)")
	f.StringVar(&registry, "registry", "", "registry ID the builds are pushed to")
	return cmd
}

// newServicesUpdateCmd edits the build override of a service. The whole config is
// replaced by a PATCH, so the current config is read, the build keys changed and
// sent back unchanged otherwise.
func newServicesUpdateCmd(c *cli) *cobra.Command {
	var location, server, registry, submodules string
	cmd := &cobra.Command{
		Use:   "update <service> [--build-location inherit|target|build-server] [--build-server <server|none>] [--registry <id|none>] [--submodules auto|on|off]",
		Short: "Set where and how a service is built",
		Long: "The service's own override of the project and team build settings; `inherit` follows them. `none` clears a pin or\n" +
			"registry. `--submodules` sets the git submodule checkout (auto: recursive when the repository has a .gitmodules file; on: always, a failure fails the build; off). The change is staged until the next deployment, like every setting. Compose stacks are edited in the web UI.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if !f.Changed("build-location") && !f.Changed("build-server") && !f.Changed("registry") && !f.Changed("submodules") {
				return usagef("give at least one of --build-location, --build-server, --registry, --submodules")
			}
			if f.Changed("submodules") && submodules != "auto" && submodules != "on" && submodules != "off" {
				return usagef("--submodules must be auto, on or off")
			}
			loc := ""
			if f.Changed("build-location") {
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
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			cur, err := c.api.GetServiceWithResponse(ctx, id)
			if err := check(cur, err); err != nil {
				return err
			}
			if cur.JSON200.Kind == "stack" {
				return usagef("service %s is a Compose stack; edit it in the web UI", args[0])
			}
			raw, err := json.Marshal(cur.JSON200.Config)
			if err != nil {
				return err
			}
			var cfg map[string]any
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return err
			}
			if f.Changed("build-location") {
				cfg["buildLocation"] = loc
			}
			if f.Changed("submodules") {
				if submodules == "auto" {
					delete(cfg, "submodules")
				} else {
					cfg["submodules"] = submodules
				}
			}
			if f.Changed("build-server") {
				switch server {
				case "none", "":
					delete(cfg, "buildServerId")
				default:
					s, err := c.resolveServer(ctx, server)
					if err != nil {
						return err
					}
					cfg["buildServerId"] = s.Id
				}
			}
			if f.Changed("registry") {
				if registry == "none" || registry == "" {
					delete(cfg, "registryId")
				} else {
					cfg["registryId"] = registry
				}
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
			location := "inherit"
			if l, ok := cfg["buildLocation"].(string); ok && l != "" {
				location = l
			}
			c.info("Updated service %s (%s): build location %s, staged until the next deployment", r.JSON200.Name, r.JSON200.Id, location)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&location, "build-location", "", "inherit, target or build-server")
	f.StringVar(&server, "build-server", "", "pin the build server (ID or name; none clears)")
	f.StringVar(&registry, "registry", "", "registry ID (none clears)")
	f.StringVar(&submodules, "submodules", "", "git submodules: auto, on or off")
	return cmd
}

func newBuildSettingsCmd(c *cli) *cobra.Command {
	var location, registry string
	cmd := &cobra.Command{
		Use:   "build-settings [--build-location target|build-server] [--registry <id|none>]",
		Short: "Show or set the team's default build location and registry",
		Long: "Without flags, prints the team default. With flags (administrators only), replaces it. Projects and services can\n" +
			"override it. When no usable build server or registry exists a build falls back to the target server.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			cur, err := c.api.GetTeamBuildSettingsWithResponse(ctx)
			if err := check(cur, err); err != nil {
				return err
			}
			out := cur.JSON200
			if f.Changed("build-location") || f.Changed("registry") {
				in := apiclient.PutTeamSettingsInputBody{BuildLocation: apiclient.PutTeamSettingsInputBodyBuildLocation(out.BuildLocation)}
				if out.RegistryId != "" {
					in.RegistryId = &out.RegistryId
				}
				if f.Changed("build-location") {
					loc, err := buildLocation(location, false)
					if err != nil {
						return err
					}
					in.BuildLocation = apiclient.PutTeamSettingsInputBodyBuildLocation(loc)
				}
				if f.Changed("registry") {
					in.RegistryId = nil
					if registry != "" && registry != "none" {
						in.RegistryId = &registry
					}
				}
				r, err := c.api.PutTeamBuildSettingsWithResponse(ctx, in)
				if err := check(r, err); err != nil {
					return err
				}
				out = r.JSON200
			}
			if c.asJSON {
				return c.printJSON(out)
			}
			c.info("Default build location: %s", out.BuildLocation)
			c.info("Default registry: %s", orDash(out.RegistryId))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&location, "build-location", "", "target or build-server")
	f.StringVar(&registry, "registry", "", "default registry ID (none clears)")
	return cmd
}
