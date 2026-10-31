package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// The replica endpoints are called through doJSON with small local types rather
// than the generated client; the CLI never links the server packages.

type replProblem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type replicaPlanOut struct {
	ServerID     string        `json:"serverId"`
	ServerName   string        `json:"serverName"`
	SameServer   bool          `json:"sameServer"`
	DevicesAdded int           `json:"devicesAdded"`
	Blockers     []replProblem `json:"blockers"`
	Warnings     []replProblem `json:"warnings"`
}

type addReplicaOut struct {
	DryRun    bool           `json:"dryRun"`
	Plan      replicaPlanOut `json:"plan"`
	ServiceID string         `json:"serviceId"`
	Status    string         `json:"status"`
}

type replicaMemberOut struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	ServerName  string `json:"serverName"`
	State       string `json:"state"`
	LagBytes    *int64 `json:"lagBytes"`
	ReplayLagMs *int64 `json:"replayLagMs"`
	Message     string `json:"message"`
}

type replicationOut struct {
	Role    string `json:"role"`
	Primary *struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"primary"`
	Replicas    []replicaMemberOut `json:"replicas"`
	Rebuildable bool               `json:"rebuildable"`
	CanPromote  bool               `json:"canPromote"`
	Promotion   *struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Step    string `json:"step"`
		Mode    string `json:"mode"`
		Message string `json:"message"`
	} `json:"promotion"`
}

type promotePlanOut struct {
	Mode          string `json:"mode"`
	LagBytes      *int64 `json:"lagBytes"`
	LagObservedAt string `json:"lagObservedAt"`
	Replica       struct {
		Slug string `json:"slug"`
	} `json:"replica"`
	OldPrimary struct {
		Slug       string `json:"slug"`
		ServerName string `json:"serverName"`
	} `json:"oldPrimary"`
	Consumers []struct {
		Slug         string        `json:"slug"`
		ServerName   string        `json:"serverName"`
		Keys         []string      `json:"keys"`
		DevicesAdded int           `json:"devicesAdded"`
		Warnings     []replProblem `json:"warnings"`
	} `json:"consumers"`
	RebuildReplicas []struct {
		Slug string `json:"slug"`
	} `json:"rebuildReplicas"`
	SchedulesMoving int           `json:"schedulesMoving"`
	Blockers        []replProblem `json:"blockers"`
	Warnings        []replProblem `json:"warnings"`
	DataLossWarning string        `json:"dataLossWarning"`
}

type promoteOut struct {
	DryRun      bool           `json:"dryRun"`
	Plan        promotePlanOut `json:"plan"`
	PromotionID string         `json:"promotionId"`
	Status      string         `json:"status"`
}

// doJSON sends one JSON request to the API and decodes a 2xx answer into out
// (raw, so --json prints exactly what the server said). idem adds an
// Idempotency-Key.
func (c *cli) doJSON(ctx context.Context, method, path string, body any, idem bool, out any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.stream.Server, "/")+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if idem {
		req.Header.Set("Idempotency-Key", *c.key())
	}
	for _, edit := range c.stream.RequestEditors {
		if err := edit(ctx, req); err != nil {
			return nil, err
		}
	}
	resp, err := c.stream.Client.Do(req)
	if err := checkRaw(resp, err); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, fmt.Errorf("decode response: %w", err)
		}
	}
	return raw, nil
}

func newDatabasesReplicasCmd(c *cli) *cobra.Command {
	return group("replicas", "Manage PostgreSQL read replicas",
		newReplicasAddCmd(c), newReplicasLsCmd(c), newReplicasRmCmd(c))
}

func lagText(m replicaMemberOut) string {
	if m.LagBytes == nil {
		return "-"
	}
	s := byteText(*m.LagBytes)
	if m.ReplayLagMs != nil && *m.ReplayLagMs > 0 {
		s += fmt.Sprintf(", %d ms", *m.ReplayLagMs)
	}
	return s
}

func byteText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func printProblems(c *cli, kind string, ps []replProblem) {
	for _, p := range ps {
		c.note("%s: %s (%s)", kind, p.Message, p.Code)
	}
}

func newReplicasAddCmd(c *cli) *cobra.Command {
	var server, name string
	var dryRun, acceptMesh bool
	cmd := &cobra.Command{
		Use:   "add <primary> --server <server> [--name <name>] [--dry-run] [--accept-mesh-changes]",
		Short: "Add a read replica of a PostgreSQL primary",
		Long: "Creates a read replica service in the primary's environment and clones the primary into it. The replica runs on the primary's server or, " +
			"with the server mesh enabled on both, on another server. Replication is asynchronous and manual: nothing is detected or switched automatically. " +
			"--dry-run prints the plan only.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if server == "" {
				return usagef("--server is required")
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
			srv, err := c.resolveServer(ctx, server)
			if err != nil {
				return err
			}
			body := map[string]any{"serverId": srv.Id, "dryRun": dryRun, "acceptMeshChanges": acceptMesh}
			if name != "" {
				body["name"] = name
			}
			var out addReplicaOut
			raw, err := c.doJSON(ctx, http.MethodPost, "/services/"+id+"/database/replicas", body, true, &out)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(json.RawMessage(raw))
			}
			p := out.Plan
			where := "on the primary's server"
			if !p.SameServer {
				where = fmt.Sprintf("on %s over the mesh (adds %d device(s))", p.ServerName, p.DevicesAdded)
			}
			c.info("Read replica %s", where)
			printProblems(c, "blocked", p.Blockers)
			printProblems(c, "warning", p.Warnings)
			if out.DryRun {
				c.info("Dry run only; nothing changed.")
				return nil
			}
			c.info("Replica %s is being created (%s). Follow it with: nebulactrl databases replicas ls %s", out.ServiceID, out.Status, args[0])
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&server, "server", "", "server ID or name the replica runs on")
	f.StringVar(&name, "name", "", "display name (default: <primary>-replica-<n>)")
	f.BoolVar(&dryRun, "dry-run", false, "only print the plan")
	f.BoolVar(&acceptMesh, "accept-mesh-changes", false, "confirm that the replica adds tailnet devices")
	return cmd
}

func newReplicasLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls <primary-or-replica>", Short: "List the read replicas of a database and their lag", Args: exactArgs(1),
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
			var out replicationOut
			raw, err := c.doJSON(ctx, http.MethodGet, "/services/"+id+"/database/replication", nil, false, &out)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(json.RawMessage(raw))
			}
			c.info("role: %s", out.Role)
			if out.Primary != nil {
				c.info("follows: %s", out.Primary.Slug)
			}
			var rows [][]string
			for _, r := range out.Replicas {
				rows = append(rows, []string{r.Slug, orDash(r.ServerName), r.State, lagText(r), r.Message})
			}
			if len(rows) > 0 {
				c.table([]string{"REPLICA", "SERVER", "STATE", "LAG", "MESSAGE"}, rows)
			}
			if p := out.Promotion; p != nil {
				c.info("promotion: %s %s (%s) %s", p.Mode, p.Status, p.Step, p.Message)
			}
			return nil
		},
	}
}

func newReplicasRmCmd(c *cli) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "rm <replica> --yes", Short: "Remove a read replica", Args: exactArgs(1),
		Long: "Deletes the replica's container and data volume, drops its slot, role and pg_hba.conf line on the primary and releases its mesh link.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return usagef("removing a replica deletes its data volume: re-run with --yes")
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
			raw, err := c.doJSON(ctx, http.MethodDelete, "/services/"+id+"/database/replica", nil, true, nil)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(json.RawMessage(raw))
			}
			c.info("Removing replica %s.", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the removal")
	return cmd
}

func newDatabasesPromoteCmd(c *cli) *cobra.Command {
	var yes, acceptLoss bool
	cmd := &cobra.Command{
		Use:   "promote <replica> [--yes] [--accept-data-loss]",
		Short: "Promote a read replica to primary (manual)",
		Long: "Prints the promotion plan. With --yes it promotes: a switchover while the old primary is reachable (it is frozen, stopped and fenced; no data is lost) " +
			"or a forced promote otherwise (needs --accept-data-loss: what the replica had not received is lost). The old primary is fenced and never runs as " +
			"a primary again. Variables that reference it are rewritten as staged changes. Manual: NebulaCtrl does not detect failures or switch automatically.",
		Args: exactArgs(1),
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
			path := "/services/" + id + "/database/promote"
			var out promoteOut
			if !yes {
				raw, err := c.doJSON(ctx, http.MethodPost, path, map[string]any{"dryRun": true}, false, &out)
				if err != nil {
					return err
				}
				if c.asJSON {
					return c.printJSON(json.RawMessage(raw))
				}
				printPromotePlan(c, out.Plan)
				c.info("Run again with --yes to promote.")
				return nil
			}
			svc, err := c.api.GetServiceWithResponse(ctx, id)
			if err := check(svc, err); err != nil {
				return err
			}
			slug := svc.JSON200.Slug
			raw, err := c.doJSON(ctx, http.MethodPost, path, map[string]any{"confirm": slug, "acceptDataLoss": acceptLoss}, true, &out)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(json.RawMessage(raw))
			}
			c.info("Promotion %s started (%s). Follow it with: nebulactrl databases replicas ls %s", out.PromotionID, out.Plan.Mode, args[0])
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&yes, "yes", false, "promote")
	f.BoolVar(&acceptLoss, "accept-data-loss", false, "accept losing what the replica had not received (forced promote)")
	return cmd
}

func printPromotePlan(c *cli, p promotePlanOut) {
	c.info("%s: promote %s over %s (%s)", p.Mode, p.Replica.Slug, p.OldPrimary.Slug, orDash(p.OldPrimary.ServerName))
	if p.LagBytes != nil {
		c.info("last observed lag: %s %s", byteText(*p.LagBytes), p.LagObservedAt)
	}
	for _, cs := range p.Consumers {
		c.info("consumer: %s on %s, variables %s, adds %d device(s)", cs.Slug, orDash(cs.ServerName), strings.Join(cs.Keys, ", "), cs.DevicesAdded)
		printProblems(c, "  warning", cs.Warnings)
	}
	for _, r := range p.RebuildReplicas {
		c.info("rebuild needed: %s", r.Slug)
	}
	if p.SchedulesMoving > 0 {
		c.info("backup schedules moving: %d", p.SchedulesMoving)
	}
	printProblems(c, "blocked", p.Blockers)
	printProblems(c, "warning", p.Warnings)
}

func newDatabasesRebuildCmd(c *cli) *cobra.Command {
	var follow string
	cmd := &cobra.Command{
		Use:   "rebuild <replica-or-fenced-primary> [--follow <new-primary>]",
		Short: "Rebuild a replica, or a fenced old primary as a replica of the new primary",
		Long:  "Clones the primary again into a new data volume; the old volume is kept until it is deleted.",
		Args:  exactArgs(1),
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
			body := map[string]any{}
			if follow != "" {
				fid, err := c.serviceID(ctx, follow)
				if err != nil {
					return err
				}
				body["followServiceId"] = fid
			}
			raw, err := c.doJSON(ctx, http.MethodPost, "/services/"+id+"/database/rebuild", body, true, nil)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(json.RawMessage(raw))
			}
			c.info("Rebuilding %s. Follow it with: nebulactrl databases replicas ls %s", args[0], args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&follow, "follow", "", "the new primary a fenced old primary should follow (default: the replica that was promoted)")
	return cmd
}
