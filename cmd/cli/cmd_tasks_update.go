package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// inZone renders t in the IANA zone tz (UTC when the name is empty or unknown).
func inZone(t time.Time, tz string) time.Time {
	if loc, err := time.LoadLocation(tz); err == nil && tz != "" {
		return t.In(loc)
	}
	return t.UTC()
}

func newTasksUpdateCmd(c *cli) *cobra.Command {
	var schedule, timezone string
	var enable, disable bool
	cmd := &cobra.Command{
		Use: "update <service> <task> [--schedule <cron>] [--timezone <zone>] [--enable|--disable]", Short: "Change a cron task's schedule, time zone or whether it runs", Args: exactArgs(2),
		Long: "Changing the schedule or the time zone recomputes the next run from now; the new next run is printed.\n" +
			"The time zone is an IANA name such as Europe/Berlin (UTC when never set).\n\n" +
			"--disable stops the schedule from starting runs and keeps the task, its history and its settings; --enable starts it again.\n" +
			"Give at most one of them.",
		Example: "  nebulactrl tasks update acme/production/web nightly --timezone Europe/Berlin\n" +
			"  nebulactrl tasks update acme/production/web nightly --disable",
		RunE: func(cmd *cobra.Command, args []string) error {
			if enable && disable {
				return usagef("give --enable or --disable, not both")
			}
			if schedule == "" && timezone == "" && !enable && !disable {
				return usagef("give --schedule, --timezone, --enable or --disable")
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
			t, err := c.taskByRef(ctx, id, args[1])
			if err != nil {
				return err
			}
			body := apiclient.UpdateServiceTaskJSONRequestBody{}
			if schedule != "" {
				body.Schedule = &schedule
			}
			if timezone != "" {
				body.Timezone = &timezone
			}
			if enable || disable {
				body.Enabled = &enable
			}
			r, err := c.api.UpdateServiceTaskWithResponse(ctx, t.Id, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			v := r.JSON200
			c.info("Task %q: %s in %s", v.Name, v.Schedule, v.Timezone)
			if !v.Enabled {
				c.info("Disabled: it does not run on schedule (`nebulactrl tasks update --enable` turns it on).")
			} else if v.NextRunAt != nil {
				c.info("Next run:  %s", inZone(*v.NextRunAt, v.Timezone).Format("2006-01-02 15:04 MST"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&schedule, "schedule", "", "new cron expression")
	cmd.Flags().StringVar(&timezone, "timezone", "", "new IANA time zone")
	cmd.Flags().BoolVar(&enable, "enable", false, "turn the task on, so its schedule starts runs")
	cmd.Flags().BoolVar(&disable, "disable", false, "turn the task off; it is kept and does not run on schedule")
	return cmd
}
