package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// whenFlag parses --from / --to: an RFC 3339 time, a date (UTC midnight), or a
// duration meaning that long ago ("90m", "24h").
func whenFlag(name, s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, usagef("--%s %q: use an RFC 3339 time (2026-10-28T09:00:00Z), a date (2026-10-28) or a duration ago (90m, 24h)", name, s)
}

// logFileFlags are the flags the two download commands share.
type logFileFlags struct {
	from, to, format, output string
}

func (f *logFileFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.from, "from", "", "only lines at or after this time: RFC 3339, a date, or a duration ago (24h)")
	cmd.Flags().StringVar(&f.to, "to", "", "only lines before this time (same forms as --from)")
	cmd.Flags().StringVar(&f.format, "format", "txt", "file format: txt or ndjson")
	cmd.Flags().StringVarP(&f.output, "output", "o", "", "write to this file instead of stdout (created with mode 0600)")
}

// query validates the flags and returns the query string shared by the endpoints.
func (f *logFileFlags) query(now time.Time) (url.Values, error) {
	if f.format != "txt" && f.format != "ndjson" {
		return nil, usagef("--format must be txt or ndjson")
	}
	from, err := whenFlag("from", f.from, now)
	if err != nil {
		return nil, err
	}
	to, err := whenFlag("to", f.to, now)
	if err != nil {
		return nil, err
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return nil, usagef("--from must be before --to")
	}
	q := url.Values{"format": {f.format}}
	if !from.IsZero() {
		q.Set("from", from.UTC().Format(time.RFC3339))
	}
	if !to.IsZero() {
		q.Set("to", to.UTC().Format(time.RFC3339))
	}
	return q, nil
}

// download streams GET <api>/<path>?<query> into the output: stdout, or the
// file named by -o. The server redacts secret values; the CLI never sees
// more than the file holds. A file is only created once the server answered
// 200, so a refusal leaves nothing behind.
func (c *cli) download(ctx context.Context, path string, q url.Values, output string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase(c.cfg.URL)+path+"?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("User-Agent", "nebulactrl-cli/"+version)
	res, err := (&http.Client{}).Do(req) // no timeout: a long range streams for minutes
	if err := checkRaw(res, err); err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var out io.Writer = c.out
	if output != "" && output != "-" {
		f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", output, err)
		}
		defer f.Close()
		out = f
	}
	n, err := io.Copy(out, res.Body)
	if err != nil {
		return n, fmt.Errorf("download interrupted after %d bytes: %w", n, err)
	}
	return n, nil
}

func (c *cli) reportDownload(n int64, output string) {
	if output != "" && output != "-" {
		c.note("Saved %d bytes to %s", n, output)
	}
}

func newServicesDownloadLogsCmd(c *cli) *cobra.Command {
	var f logFileFlags
	var grep, component, process string
	cmd := &cobra.Command{
		Use: "download-logs <service>", Short: "Download a service's stored runtime logs as a file", Args: exactArgs(1),
		Long: "Writes the service's stored runtime log (the server keeps 7 days by default), oldest first, as plain text\n" +
			"(`<time> <line>`) or NDJSON. Secret values are masked on the server. At most 200,000 lines: a longer range\n" +
			"ends with a note, so narrow it with --from/--to. Needs the logs:read scope and the service's server online.",
		Example: "  nebulactrl services download-logs acme/production/web --from 24h -o web.log\n" +
			"  nebulactrl services download-logs web --format ndjson --grep 'level:error' > errors.ndjson",
		RunE: func(cmd *cobra.Command, args []string) error {
			q, err := f.query(time.Now())
			if err != nil {
				return err
			}
			if grep != "" {
				q.Set("q", grep)
			}
			if component != "" {
				q.Set("component", component)
			}
			if process != "" {
				q.Set("process", process)
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			id, err := c.serviceID(ctx, args[0])
			cancel()
			if err != nil {
				return err
			}
			n, err := c.download(cmd.Context(), "/services/"+url.PathEscape(id)+"/logs/download", q, f.output)
			if err != nil {
				return err
			}
			c.reportDownload(n, f.output)
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&grep, "grep", "", "only lines matching this log search query (free text, \"phrases\", level:error)")
	cmd.Flags().StringVar(&component, "component", "", "Compose stacks: only this component's lines")
	cmd.Flags().StringVar(&process, "process", "", "services with several processes: only this process' lines (default: every process)")
	return cmd
}

func newDeploymentsDownloadLogsCmd(c *cli) *cobra.Command {
	var f logFileFlags
	var streams string
	cmd := &cobra.Command{
		Use: "download-logs <deployment-id>", Short: "Download a deployment's build and deploy log as a file", Args: exactArgs(1),
		Long: "Writes the deployment's persisted lines (build, deploy and runtime streams; at most 10,000) in the order\n" +
			"they were recorded, as plain text (`<time> [<stream>] <line>`) or NDJSON. Secret values are masked on the\n" +
			"server. --streams all adds the agent's verbose debug trace.",
		Example: "  nebulactrl deployments download-logs 6f1c0d5e-… -o build.log",
		RunE: func(cmd *cobra.Command, args []string) error {
			q, err := f.query(time.Now())
			if err != nil {
				return err
			}
			if streams != "" {
				q.Set("streams", streams)
			}
			if err := c.connect(); err != nil {
				return err
			}
			n, err := c.download(cmd.Context(), "/deployments/"+url.PathEscape(args[0])+"/logs/download", q, f.output)
			if err != nil {
				return err
			}
			c.reportDownload(n, f.output)
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&streams, "streams", "", "all, or a comma-separated list of build, deploy, runtime, debug (default: build, deploy, runtime)")
	return cmd
}
