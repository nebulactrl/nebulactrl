package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// meshDo calls a mesh endpoint with the stored token and decodes the JSON answer
// into a generic value (the mesh views are rendered as tables or raw JSON).
func (c *cli) meshDo(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase(c.cfg.URL)+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("User-Agent", "nebulactrl-cli/"+version)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", *c.key())
	}
	resp, err := (&http.Client{Timeout: requestTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, problem(resp.StatusCode, raw)
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("unreadable answer: %w", err)
		}
	}
	return out, nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func newMeshCmd(c *cli) *cobra.Command {
	return group("mesh", "Manage the server mesh (a tailnet of your servers)",
		append([]*cobra.Command{newMeshStatusCmd(c), newMeshEnableCmd(c), newMeshDisableCmd(c), newMeshCheckCmd(c), newMeshReprovisionCmd(c)}, newMeshLinksCmds(c)...)...)
}

func newMeshStatusCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Show the mesh: nodes, links and the ACL state", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			v, err := c.meshDo(ctx, http.MethodGet, "/mesh", nil)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			fmt.Fprintf(c.out, "Provider: %s   ACL: %s (%s)   Paused: %v\n", str(v["provider"]), str(v["aclMode"]), str(v["aclState"]), v["paused"])
			if m := str(v["aclMessage"]); m != "" {
				fmt.Fprintln(c.out, m)
			}
			var rows [][]string
			nodes, _ := v["nodes"].([]any)
			for _, n := range nodes {
				m, _ := n.(map[string]any)
				ips, _ := m["ips"].([]any)
				ip := ""
				if len(ips) > 0 {
					ip = str(ips[0])
				}
				rows = append(rows, []string{str(m["serverName"]), str(m["state"]), orDash(ip), orDash(str(m["fqdn"]))})
			}
			c.table([]string{"SERVER", "STATE", "MESH IP", "FQDN"}, rows)
			return nil
		},
	}
}

func newMeshEnableCmd(c *cli) *cobra.Command {
	var accept, dry bool
	cmd := &cobra.Command{
		Use: "enable <server>", Short: "Join a server to the mesh (adds one tagged tailnet device)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			srv, err := c.resolveServer(ctx, args[0])
			if err != nil {
				return err
			}
			path := "/servers/" + srv.Id + "/mesh"
			if dry {
				path += "?dryRun=true"
			}
			v, err := c.meshDo(ctx, http.MethodPut, path, map[string]any{"enabled": true, "acceptMeshChanges": accept})
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			plan, _ := v["plan"].(map[string]any)
			fmt.Fprintf(c.out, "Devices added: %v\n", plan["devicesAdded"])
			if ws, ok := plan["warnings"].([]any); ok {
				for _, w := range ws {
					fmt.Fprintln(c.out, "warning:", str(w))
				}
			}
			if !dry {
				fmt.Fprintln(c.out, "The mesh node is being provisioned.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&accept, "accept-mesh-changes", false, "confirm that this adds tailnet devices")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "print the plan without changing anything")
	return cmd
}

func newMeshDisableCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "disable <server>", Short: "Remove a server from the mesh", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			srv, err := c.resolveServer(ctx, args[0])
			if err != nil {
				return err
			}
			v, err := c.meshDo(ctx, http.MethodPut, "/servers/"+srv.Id+"/mesh", map[string]any{"enabled": false})
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			fmt.Fprintln(c.out, "The mesh node is being removed.")
			return nil
		},
	}
}

func newMeshCheckCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "check <server>", Short: "Test whether the server's mesh node reaches the other servers", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			srv, err := c.resolveServer(ctx, args[0])
			if err != nil {
				return err
			}
			v, err := c.meshDo(ctx, http.MethodPost, "/servers/"+srv.Id+"/mesh/check", map[string]any{})
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			var rows [][]string
			res, _ := v["results"].([]any)
			for _, r := range res {
				m, _ := r.(map[string]any)
				rows = append(rows, []string{orDash(str(m["peerServerId"])), str(m["outcome"]), orDash(str(m["message"]))})
			}
			c.table([]string{"PEER", "OUTCOME", "MESSAGE"}, rows)
			return nil
		},
	}
}

func newMeshReprovisionCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "reprovision <server>", Short: "Re-create the server's mesh node (new device and tailnet IP)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			srv, err := c.resolveServer(ctx, args[0])
			if err != nil {
				return err
			}
			v, err := c.meshDo(ctx, http.MethodPost, "/servers/"+srv.Id+"/mesh/reprovision", nil)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(v)
			}
			fmt.Fprintln(c.out, "The mesh node is being re-provisioned.")
			return nil
		},
	}
}
