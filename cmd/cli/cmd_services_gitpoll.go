package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// defaultGitPollMinutes is the interval a bare --deploy / --notify-only turns checking on with.
const defaultGitPollMinutes = 5

// gitPollIntervals maps the --every values to minutes.
var gitPollIntervals = map[string]int64{"1m": 1, "5m": 5, "15m": 15, "60m": 60, "1h": 60}

// gitPollChange is what the flags ask for; the zero value only shows the state.
type gitPollChange struct {
	off      bool
	interval int64 // minutes; 0 = keep
	deploy   *bool // nil = keep
}

// set reports whether the flags change the stored setting.
func (g gitPollChange) set() bool { return g.off || g.interval != 0 || g.deploy != nil }

// parseGitPollFlags validates the flag combination of `services git-poll`.
func parseGitPollFlags(every string, deploy, notifyOnly, off bool) (gitPollChange, error) {
	var g gitPollChange
	if deploy && notifyOnly {
		return g, usagef("--deploy and --notify-only exclude each other")
	}
	if off && (every != "" || deploy || notifyOnly) {
		return g, usagef("--off cannot be combined with --every, --deploy or --notify-only")
	}
	g.off = off
	if every != "" {
		m, ok := gitPollIntervals[strings.ToLower(strings.TrimSpace(every))]
		if !ok {
			return g, usagef("--every %q: use 1m, 5m, 15m or 60m", every)
		}
		g.interval = m
	}
	switch {
	case deploy:
		g.deploy = new(true)
	case notifyOnly:
		g.deploy = new(false)
	}
	return g, nil
}

// body is the full-replace PUT body: fields the flags leave out come from the current view.
func (g gitPollChange) body(cur apiclient.GitPollView) apiclient.SetServiceGitPollJSONRequestBody {
	if g.off {
		interval := int64(cur.IntervalMinutes)
		if interval == 0 {
			interval = defaultGitPollMinutes
		}
		return apiclient.SetServiceGitPollJSONRequestBody{
			Enabled:         false,
			IntervalMinutes: new(apiclient.GitPollInputBodyIntervalMinutes(interval)),
			Deploy:          new(cur.Deploy),
		}
	}
	interval, deploy := g.interval, true
	if cur.Enabled {
		if interval == 0 {
			interval = int64(cur.IntervalMinutes)
		}
		deploy = cur.Deploy
	}
	if interval == 0 {
		interval = defaultGitPollMinutes
	}
	if g.deploy != nil {
		deploy = *g.deploy
	}
	return apiclient.SetServiceGitPollJSONRequestBody{
		Enabled:         true,
		IntervalMinutes: new(apiclient.GitPollInputBodyIntervalMinutes(interval)),
		Deploy:          &deploy,
	}
}

func newServicesGitPollCmd(c *cli) *cobra.Command {
	var every string
	var deploy, notifyOnly, off, checkNow bool
	cmd := &cobra.Command{
		Use:   "git-poll <service> [--every 1m|5m|15m|60m] [--notify-only|--deploy] [--off] [--check]",
		Short: "Check a service's branch for new commits on a schedule",
		Args:  exactArgs(1),
		Long: "For app and worker services with a git source: look at the branch head every few minutes and deploy a new\n" +
			"commit, for repositories that cannot send a push webhook. Without flags this shows the state. --every turns\n" +
			"checking on (5m when you only give --deploy or --notify-only); --notify-only shows a new commit without\n" +
			"deploying it; --off stops checking; --check looks at the branch head now (it never deploys) and prints the\n" +
			"result, also while checking is off. A flag you leave out keeps its current value.",
		RunE: func(cmd *cobra.Command, args []string) error {
			change, err := parseGitPollFlags(every, deploy, notifyOnly, off)
			if err != nil {
				return err
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
			var view *apiclient.GitPollView
			if change.set() {
				cur, err := c.api.GetServiceGitPollWithResponse(ctx, id)
				if err := check(cur, err); err != nil {
					return err
				}
				r, err := c.api.SetServiceGitPollWithResponse(ctx, id, change.body(*cur.JSON200))
				if err := check(r, err); err != nil {
					return err
				}
				view = r.JSON200
			}
			switch {
			case checkNow:
				r, err := c.api.CheckServiceGitPollWithResponse(ctx, id)
				if err := check(r, err); err != nil {
					return err
				}
				view = r.JSON200
			case view == nil:
				r, err := c.api.GetServiceGitPollWithResponse(ctx, id)
				if err := check(r, err); err != nil {
					return err
				}
				view = r.JSON200
			}
			if c.asJSON {
				return c.printJSON(view)
			}
			c.info("%s", gitPollLine(*view, time.Now()))
			return nil
		},
	}
	cmd.Flags().StringVar(&every, "every", "", "check this often and turn checking on: 1m, 5m, 15m or 60m")
	cmd.Flags().BoolVar(&deploy, "deploy", false, "deploy a new commit that is found (the default; turns checking on at 5m when it is off)")
	cmd.Flags().BoolVar(&notifyOnly, "notify-only", false, "only show a new commit, do not deploy it (turns checking on at 5m when it is off)")
	cmd.Flags().BoolVar(&off, "off", false, "stop checking for new commits")
	cmd.Flags().BoolVar(&checkNow, "check", false, "look at the branch head now and print the result (never deploys)")
	return cmd
}

// gitPollInterval is the interval as read in a sentence: "minute", "5 min", "hour".
func gitPollInterval(minutes int64) string {
	switch {
	case minutes == 1:
		return "minute"
	case minutes >= 60 && minutes%60 == 0 && minutes/60 == 1:
		return "hour"
	case minutes >= 60 && minutes%60 == 0:
		return fmt.Sprintf("%d hours", minutes/60)
	}
	return fmt.Sprintf("%d min", minutes)
}

// agoText renders how long ago t was, e.g. "just now", "2m ago", "3h ago", "4d ago".
func agoText(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// gitPollLine is the one-line state of a service's new-commit checking.
func gitPollLine(v apiclient.GitPollView, now time.Time) string {
	branch := deref(v.HeadBranch)
	var head string
	if sha := deref(v.HeadSha); sha != "" {
		switch {
		case v.UpdateAvailable && branch != "":
			head = fmt.Sprintf("new commit %s on %s", shortSHA(sha), branch)
		case v.UpdateAvailable:
			head = fmt.Sprintf("new commit %s", shortSHA(sha))
		default:
			head = fmt.Sprintf("head %s (running)", shortSHA(sha))
		}
	}
	var failed string
	if e := deref(v.Error); e != "" {
		failed = "last check failed: " + e
	}
	if !v.Enabled {
		parts := []string{"Not checking for new commits"}
		for _, p := range []string{head, failed} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) == 1 {
			return parts[0] + "."
		}
		return strings.Join(parts, " · ")
	}
	target := "the branch"
	if branch != "" {
		target = branch
	}
	what := "deploys new commits"
	if !v.Deploy {
		what = "shows new commits (no deploy)"
	}
	parts := []string{fmt.Sprintf("Checking %s every %s", target, gitPollInterval(int64(v.IntervalMinutes))), what}
	if v.CheckedAt != nil {
		parts = append(parts, "last checked "+agoText(*v.CheckedAt, now))
	} else {
		parts = append(parts, "not checked yet")
	}
	for _, p := range []string{head, failed} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}
