package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// terminalStatuses are the final deployment states (internal/deployments).
var terminalStatuses = map[string]bool{
	"succeeded": true, "failed": true, "canceled": true, "rolled_back": true, "rollback_failed": true,
}

func newDeploymentsCmd(c *cli) *cobra.Command {
	return group("deployments", "Inspect and roll back deployments",
		newDeploymentsLsCmd(c),
		newDeploymentsStatusCmd(c),
		newDeploymentsRollbackCmd(c),
		newDeploymentsCancelCmd(c),
		newDeploymentsDownloadLogsCmd(c),
	)
}

func releaseSummary(s apiclient.ReleaseSource) string {
	switch s.Type {
	case "git":
		sha := deref(s.CommitSha)
		if len(sha) > 7 {
			sha = sha[:7]
		}
		return "git " + sha
	default:
		ref := deref(s.Image)
		if d := deref(s.Digest); d != "" {
			// The agent may report repo@sha256:…; only the digest part is useful here.
			if i := strings.LastIndex(d, "@"); i >= 0 {
				d = d[i+1:]
			}
			if len(d) > 19 { // sha256: + 12 hex
				d = d[:19]
			}
			ref += "@" + d
		}
		return orDash(ref)
	}
}

func newDeploymentsLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls <service>", Short: "List the deployments of a service", Args: exactArgs(1),
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
			all, err := listAll(func(cur *string) (page[apiclient.DeploymentView], error) {
				r, err := c.api.ListDeploymentsWithResponse(ctx, id, &apiclient.ListDeploymentsParams{Cursor: cur, Limit: pageLimit()})
				if err := check(r, err); err != nil {
					return page[apiclient.DeploymentView]{}, err
				}
				return page[apiclient.DeploymentView]{r.JSON200.Items, r.JSON200.NextCursor}, nil
			})
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(all)
			}
			rows := make([][]string, 0, len(all))
			for _, d := range all {
				rows = append(rows, []string{d.Id, d.Status, string(d.Trigger), string(d.Strategy), releaseSummary(d.Release.Source), ts(d.CreatedAt)})
			}
			c.table([]string{"ID", "STATUS", "TRIGGER", "STRATEGY", "RELEASE", "CREATED"}, rows)
			return nil
		},
	}
}

func newDeploymentsStatusCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "status <deployment-id>", Short: "Show a deployment and its step timeline", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.GetDeploymentWithResponse(ctx, args[0])
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			d := r.JSON200
			c.info("Deployment  %s", d.Id)
			c.info("Service     %s", d.ServiceId)
			c.info("Status      %s", d.Status)
			if d.StatusMessage != "" {
				c.info("Message     %s", d.StatusMessage)
			}
			if d.WaitingFor.ServerId != "" {
				since := ""
				if d.WaitingFor.OfflineSince != nil {
					since = " (offline since " + ts(*d.WaitingFor.OfflineSince) + ")"
				}
				c.info("Waiting     for %s%s", d.WaitingFor.ServerName, since)
			}
			c.info("Strategy    %s", d.Strategy)
			c.info("Trigger     %s (%s)", d.Trigger, d.ActorType)
			c.info("Release     %s  %s", d.ReleaseId, releaseSummary(d.Release.Source))
			c.info("Created     %s", ts(d.CreatedAt))
			c.info("Started     %s", tsPtr(d.StartedAt))
			c.info("Finished    %s", tsPtr(d.FinishedAt))
			ev, err := c.api.ListDeploymentEventsWithResponse(ctx, d.Id)
			if err := check(ev, err); err != nil {
				return err
			}
			if items := itemsOrEmpty(ev.JSON200.Items); len(items) > 0 {
				c.info("")
				rows := make([][]string, 0, len(items))
				for _, e := range items {
					rows = append(rows, []string{ts(e.CreatedAt), e.Step, e.Status, e.Message})
				}
				c.table([]string{"TIME", "STEP", "STATUS", "MESSAGE"}, rows)
			}
			return nil
		},
	}
}

func newDeploymentsCancelCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "cancel <deployment-id>", Short: "Cancel a running deployment", Args: exactArgs(1),
		Long: "Cancels a deployment at whatever step it has reached. Before the agent started it the deployment ends as canceled at once.\n" +
			"Once the agent runs it, the agent is asked to abort: a build is killed, the new containers are removed, routes are restored and a stopped previous release is started again, so the current release keeps serving.\n" +
			"The deployment ends as canceled when the agent reports back (check with `deployments status`). Once the new release is live it is too late and the deployment finishes.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.CancelDeploymentWithResponse(ctx, args[0], &apiclient.CancelDeploymentParams{IdempotencyKey: c.key()})
			if err := check(r, err); err != nil {
				return err
			}
			d := r.JSON200
			if c.asJSON {
				return c.printJSON(d)
			}
			if d.Status == "canceled" {
				c.info("Deployment %s canceled", d.Id)
			} else {
				c.info("Cancel requested for deployment %s (%s); the agent is stopping it", d.Id, d.Status)
			}
			return nil
		},
	}
}

func newDeploymentsRollbackCmd(c *cli) *cobra.Command {
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "rollback <deployment-id>", Short: "Redeploy the release of a previous deployment", Args: exactArgs(1),
		Long: "Creates a new deployment that points at the release of the given deployment.\n" +
			"Application image and configuration only. Databases, volumes and external services are not rolled back.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			r, err := c.api.RollbackDeploymentWithResponse(ctx, args[0], &apiclient.RollbackDeploymentParams{IdempotencyKey: c.key()})
			cancel()
			if err := check(r, err); err != nil {
				return err
			}
			d := r.JSON202
			c.note("%s", d.Disclaimer)
			if !wait {
				if c.asJSON {
					return c.printJSON(d)
				}
				c.info("Rollback deployment %s accepted (%s, release %s)", d.Id, d.Status, releaseSummary(d.Release.Source))
				return nil
			}
			c.note("Deployment %s: %s (%s)", d.Id, d.Status, d.Strategy)
			return c.waitAndReport(cmd.Context(), d.Id, d.Status, timeout)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the rollback to finish; exit non-zero unless it succeeded")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "give up waiting after this long (with --wait)")
	return cmd
}

// waitAndReport polls a deployment until it is terminal, reports the result
// and returns nil only when the deployment succeeded.
func (c *cli) waitAndReport(parent context.Context, id, last string, timeout time.Duration) error {
	ctx := parent
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}
	lastMsg, lastWait := "", ""
	failures := 0
	for {
		d, err := c.getDeployment(ctx, id)
		switch {
		case err == nil:
			failures = 0
		case ctx.Err() != nil:
			if parent.Err() != nil {
				return &exitError{130, fmt.Errorf("interrupted; deployment %s keeps running on the server", id)}
			}
			return &exitError{1, fmt.Errorf("timed out after %s waiting for deployment %s (last status %s); it may still be running", timeout, id, last)}
		default:
			var ae *apiError
			failures++
			if (errors.As(err, &ae) && ae.Status < http.StatusInternalServerError) || failures > 3 {
				return err
			}
		}
		if err == nil {
			wait := ""
			if d.WaitingFor.ServerId != "" {
				wait = "waiting for " + d.WaitingFor.ServerName
			}
			if d.Status != last || d.StatusMessage != lastMsg || wait != lastWait {
				line := "Deployment " + id + ": " + d.Status
				if wait != "" {
					line += " (" + wait + ")"
				}
				if d.StatusMessage != "" {
					line += " - " + d.StatusMessage
				}
				c.note("%s", line)
				last, lastMsg, lastWait = d.Status, d.StatusMessage, wait
			}
			if terminalStatuses[d.Status] {
				if c.asJSON {
					if err := c.printJSON(d); err != nil {
						return err
					}
				}
				if d.Status == "succeeded" {
					c.info("Deployment %s succeeded", id)
					return nil
				}
				msg := "deployment " + id + " " + d.Status
				if d.StatusMessage != "" {
					msg += ": " + d.StatusMessage
				}
				return &exitError{1, errors.New(msg)}
			}
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return &exitError{130, fmt.Errorf("interrupted; deployment %s keeps running on the server", id)}
			}
			return &exitError{1, fmt.Errorf("timed out after %s waiting for deployment %s (last status %s); it may still be running", timeout, id, last)}
		case <-time.After(c.pollEvery):
		}
	}
}

func (c *cli) getDeployment(ctx context.Context, id string) (*apiclient.DeploymentView, error) {
	rctx, cancel := c.ctx(ctx)
	defer cancel()
	r, err := c.api.GetDeploymentWithResponse(rctx, id)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return r.JSON200, nil
}
