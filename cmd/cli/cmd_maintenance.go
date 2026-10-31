package main

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newServicesMaintenanceCmd(c *cli) *cobra.Command {
	var title, message, eta string
	cmd := &cobra.Command{
		Use:   "maintenance <service> on|off [--title <t>] [--message <m>] [--eta <rfc3339|duration>]",
		Short: "Put a service into maintenance mode, or take it out",
		Args:  exactArgs(2),
		Long: "While on, every domain of the service answers HTTP 503 with a maintenance page and a Retry-After header instead of\n" +
			"reaching the service. It takes effect immediately without a deployment, and it is never staged or rolled back.\n" +
			"--eta takes an RFC 3339 time (2026-11-02T09:30:00Z) or a duration from now (45m, 2h). The page text and ETA are\n" +
			"only used with `on`; `off` keeps the stored text for next time.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var on bool
			switch strings.ToLower(args[1]) {
			case "on":
				on = true
			case "off":
			default:
				return usagef("the second argument is on or off, got %q", args[1])
			}
			if !on && (title != "" || message != "" || eta != "") {
				return usagef("--title, --message and --eta only apply to `on`")
			}
			body := apiclient.SetServiceMaintenanceJSONRequestBody{Enabled: on}
			if title != "" {
				body.Title = &title
			}
			if message != "" {
				body.Message = &message
			}
			if eta != "" {
				t, err := parseETA(eta, time.Now())
				if err != nil {
					return err
				}
				body.Eta = &t
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
			r, err := c.api.SetServiceMaintenanceWithResponse(ctx, id, &apiclient.SetServiceMaintenanceParams{IdempotencyKey: c.key()}, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200.Maintenance)
			}
			m := r.JSON200.Maintenance
			if !m.Enabled {
				c.info("Maintenance mode is off")
				return nil
			}
			if m.Eta != nil {
				c.info("Maintenance mode is on, expected back %s", m.Eta.Local().Format(time.RFC1123))
			} else {
				c.info("Maintenance mode is on")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "page title (default: Down for maintenance)")
	cmd.Flags().StringVar(&message, "message", "", "page message")
	cmd.Flags().StringVar(&eta, "eta", "", "expected return: an RFC 3339 time or a duration from now (45m, 2h)")
	return cmd
}

// parseETA accepts an RFC 3339 time or a Go duration counted from now.
func parseETA(v string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return now.Add(d).UTC().Truncate(time.Second), nil
	}
	return time.Time{}, usagef("--eta %q is neither an RFC 3339 time nor a positive duration like 45m", v)
}
