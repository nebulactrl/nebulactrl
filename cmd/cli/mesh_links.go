package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// envID resolves `<id>` or `project/env` to an environment ID.
func (c *cli) envID(ctx context.Context, ref string) (string, error) {
	if isUUID(ref) {
		return ref, nil
	}
	proj, env, ok := strings.Cut(ref, "/")
	if !ok || proj == "" || env == "" || strings.Contains(env, "/") {
		return "", fmt.Errorf("environment %q: use an environment ID or project/env", ref)
	}
	p, err := c.resolveProject(ctx, proj)
	if err != nil {
		return "", err
	}
	e, err := c.resolveEnv(ctx, p.Id, env)
	if err != nil {
		return "", err
	}
	return e.Id, nil
}

// newMeshLinksCmds are the cross-server service link commands of `mesh`.
func newMeshLinksCmds(c *cli) []*cobra.Command {
	return []*cobra.Command{newMeshLinksCmd(c), newMeshServiceCmd(c), newMeshReconcileCmd(c)}
}

func objects(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func serverName(v any) string {
	m, _ := v.(map[string]any)
	return str(m["name"])
}

func num(v any) string {
	if f, ok := v.(float64); ok {
		return fmt.Sprintf("%d", int64(f))
	}
	return "-"
}

func routeRows(routes []map[string]any) [][]string {
	rows := make([][]string, 0, len(routes))
	for _, r := range routes {
		rows = append(rows, []string{str(r["consumerSlug"]), str(r["provider"]), serverName(r["server"]), num(r["port"]), str(r["state"]), str(r["source"])})
	}
	return rows
}

func newMeshLinksCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "links <project/env>", Short: "Show the cross-server links of an environment: links, grants and routes", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.envID(ctx, args[0])
			if err != nil {
				return err
			}
			v, err := c.meshDo(ctx, http.MethodGet, "/environments/"+id+"/mesh", nil)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			var links [][]string
			for _, l := range objects(v["links"]) {
				links = append(links, []string{serverName(l["from"]) + " -> " + serverName(l["to"]), str(l["purpose"]), str(l["state"]), fmt.Sprint(len(objects(l["grants"])))})
			}
			c.table([]string{"LINK", "PURPOSE", "STATE", "GRANTS"}, links)
			fmt.Fprintln(c.out)
			c.table([]string{"CONSUMER", "PROVIDER", "SERVER", "MESH PORT", "STATE", "SOURCE"}, routeRows(objects(v["routes"])))
			return nil
		},
	}
}

func newMeshServiceCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "service <service>", Short: "Show what a service exposes on the mesh, which providers it reaches and who reaches it", Args: cobra.ExactArgs(1),
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
			v, err := c.meshDo(ctx, http.MethodGet, "/services/"+id+"/mesh", nil)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			var ex [][]string
			for _, e := range objects(v["exposures"]) {
				ex = append(ex, []string{serverName(e["server"]), str(e["purpose"]), num(e["containerPort"]), num(e["meshPort"]), str(e["alias"])})
			}
			c.table([]string{"SERVER", "PURPOSE", "PORT", "MESH PORT", "ALIAS"}, ex)
			fmt.Fprintln(c.out)
			c.table([]string{"CONSUMER", "PROVIDER", "SERVER", "MESH PORT", "STATE", "SOURCE"}, routeRows(objects(v["routes"])))
			fmt.Fprintln(c.out)
			var cons [][]string
			for _, e := range objects(v["consumers"]) {
				cons = append(cons, []string{str(e["slug"]), serverName(e["server"]), str(e["source"]), str(e["state"])})
			}
			c.table([]string{"REACHED BY", "SERVER", "SOURCE", "STATE"}, cons)
			return nil
		},
	}
}

func newMeshReconcileCmd(c *cli) *cobra.Command {
	var mode string
	var accept bool
	cmd := &cobra.Command{
		Use: "reconcile <project/env>", Short: "Create (add) or clean up (repair) the cross-server links of an environment now", Args: cobra.ExactArgs(1),
		Long: "Mode add creates every link the environment's variable references and canvas links need. It adds tailnet devices (tagged devices may be billed by Tailscale), so it needs --accept-mesh-changes.\n" +
			"Mode repair only removes grants whose source is gone; it never adds a device.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if mode != "add" && mode != "repair" {
				return fmt.Errorf("--mode must be add or repair")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.envID(ctx, args[0])
			if err != nil {
				return err
			}
			v, err := c.meshDo(ctx, http.MethodPost, "/environments/"+id+"/mesh/reconcile", map[string]any{"mode": mode, "acceptMeshChanges": accept})
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			fmt.Fprintf(c.out, "Mode %s: %d added, %d removed, %d waiting for their grace period, %s device(s) added.\n",
				mode, len(objects(v["added"])), len(objects(v["removed"])), int(floatOf(v["waiting"])), num(v["devicesAdded"]))
			return nil
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "add", "add or repair")
	cmd.Flags().BoolVar(&accept, "accept-mesh-changes", false, "confirm the tailnet devices mode add creates")
	return cmd
}

func floatOf(v any) float64 {
	f, _ := v.(float64)
	return f
}
