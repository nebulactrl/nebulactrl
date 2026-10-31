package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// restoreView is the part of a dump restore the CLI waits on.
type restoreView = apiclient.DumpRestoreView

func newDatabasesRestoreCmd(c *cli) *cobra.Command {
	var file, name, server, confirm, destination string
	var inPlace, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "restore <database> --file <dump> [--name <name>] [--server <server>] [--in-place --confirm <slug> [--destination <dest>]]",
		Short: "Restore a database from a dump file on this machine",
		Long: "Uploads a dump file and restores it into a managed database: a pg_dump file (custom -Fc, tar -Ft or plain SQL, optionally\n" +
			".gz), a mysqldump / mariadb-dump SQL file (optionally .gz) or a mongodump --archive file. The format is detected from the file.\n" +
			"The file is streamed through the control plane to the server's agent; nothing is stored on the way, and the default limit is 5 GiB.\n\n" +
			"By default the dump is restored into a NEW sibling database with fresh credentials (the original is untouched): point your\n" +
			"apps at it when you are satisfied. With --in-place the database's data is replaced after a safety backup; --confirm must\n" +
			"equal its slug, and --destination names the backup destination of the safety backup (default: the one of its schedule).",
		Example: "  nebulactrl databases restore shop/production/postgres --file shop.pgdump\n" +
			"  nebulactrl databases restore shop/production/postgres --file shop.sql.gz --in-place --confirm postgres --destination s3",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return usagef("--file <dump> is required")
			}
			if inPlace && confirm == "" {
				return usagef("--confirm <database slug> is required with --in-place")
			}
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return err
			}
			if !st.Mode().IsRegular() {
				return usagef("%s is not a regular file", file)
			}
			if st.Size() == 0 {
				return usagef("%s is empty", file)
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			svcID, err := c.serviceID(ctx, args[0])
			if err != nil {
				cancel()
				return err
			}
			base := filepath.Base(file)
			size := st.Size()
			body := apiclient.CreateDumpRestoreJSONRequestBody{FileName: &base, SizeBytes: &size}
			if inPlace {
				mode := apiclient.CreateDumpRestoreInputBodyMode("in_place")
				body.Mode, body.ConfirmSlug = &mode, &confirm
				if destination != "" {
					id, err := c.destinationID(ctx, destination)
					if err != nil {
						cancel()
						return err
					}
					body.DestinationId = &id
				}
			} else {
				mode := apiclient.CreateDumpRestoreInputBodyMode("new")
				body.Mode = &mode
				if name != "" {
					body.Name = &name
				}
				if server != "" {
					body.ServerId = &server
				}
			}
			r, err := c.api.CreateDumpRestoreWithResponse(ctx, svcID, &apiclient.CreateDumpRestoreParams{IdempotencyKey: c.key()}, body)
			cancel()
			if err := check(r, err); err != nil {
				return err
			}
			rv := *r.JSON202
			if inPlace {
				c.note("restore %s: taking a safety backup of the current data first", rv.Id)
			} else {
				c.note("restore %s: creating and starting the new database", rv.Id)
			}

			rv, err = c.waitDumpStatus(cmd.Context(), rv.Id, timeout, nil, "awaiting_upload")
			if err != nil {
				return err
			}
			if rv.TargetServiceId != nil && rv.Mode == "new" {
				c.note("the new database is %s", *rv.TargetServiceId)
			}
			if _, err := c.uploadDump(cmd.Context(), rv.Id, f, base, size); err != nil {
				return err
			}
			if !wait {
				if c.asJSON {
					return c.printJSON(rv)
				}
				c.info("Restore %s is running on the server; its progress is in the database's backup history in the web app", rv.Id)
				return nil
			}
			c.note("upload complete; the database client is restoring the file")
			printed := ""
			final, err := c.waitDumpStatus(cmd.Context(), rv.Id, timeout, &printed, "succeeded")
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(final)
			}
			if final.TargetServiceId != nil && final.Mode == "new" {
				c.info("Restored into the new database %s", *final.TargetServiceId)
			} else {
				c.info("Restored into %s", args[0])
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&file, "file", "", "the dump file to restore (required)")
	f.BoolVar(&inPlace, "in-place", false, "replace the database's data instead of restoring into a new database")
	f.StringVar(&confirm, "confirm", "", "the database's slug (required with --in-place)")
	f.StringVar(&destination, "destination", "", "backup destination (id or name) of the safety backup (--in-place)")
	f.StringVar(&name, "name", "", "name of the new database")
	f.StringVar(&server, "server", "", "server id for the new database")
	f.BoolVar(&wait, "wait", true, "wait for the restore to finish; exit non-zero unless it succeeded (--wait=false returns once the file is uploaded)")
	f.DurationVar(&timeout, "timeout", 2*time.Hour, "give up waiting for the new database, and again for the restore, after this long")
	return cmd
}

// waitDumpStatus polls a dump restore until it reaches want. A failed restore
// is an error. When printed is not nil the restore's log lines are printed
// as they appear.
func (c *cli) waitDumpStatus(parent context.Context, id string, timeout time.Duration, printed *string, want string) (restoreView, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	last := ""
	for {
		rctx, rcancel := c.ctx(ctx)
		r, err := c.api.GetDumpRestoreWithResponse(rctx, id)
		rcancel()
		if err := check(r, err); err != nil {
			if ctx.Err() != nil {
				return restoreView{}, &exitError{1, fmt.Errorf("timed out after %s waiting for restore %s (last status %s); it may still be running", timeout, id, last)}
			}
			return restoreView{}, err
		}
		rv := *r.JSON200
		status := string(rv.Status)
		if printed != nil && rv.Log != nil {
			for _, l := range newLogLines(*printed, *rv.Log) {
				c.note("  %s", l)
				*printed = l
			}
		}
		if status != last {
			if printed != nil || status != "provisioning" {
				c.note("restore %s: %s", id, strings.ReplaceAll(status, "_", " "))
			}
			last = status
		}
		switch {
		case status == "failed":
			if c.asJSON {
				_ = c.printJSON(rv)
			}
			return rv, &exitError{1, fmt.Errorf("restore %s failed: %s", id, deref(rv.Error))}
		case status == want:
			return rv, nil
		case want == "awaiting_upload" && (status == "uploading" || status == "restoring" || status == "succeeded"):
			return rv, &exitError{1, fmt.Errorf("restore %s already received a file (%s)", id, status)}
		}
		select {
		case <-ctx.Done():
			return restoreView{}, &exitError{1, fmt.Errorf("timed out after %s waiting for restore %s (last status %s); it may still be running", timeout, id, last)}
		case <-time.After(c.pollEvery):
		}
	}
}

// newLogLines returns the lines of log after the line last (all when last is
// empty or no longer in the log).
func newLogLines(last, log string) []string {
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if last == "" {
		return lines
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == last {
			return lines[i+1:]
		}
	}
	return lines
}

// progressReader reports how much of the file went out.
type progressReader struct {
	r        io.Reader
	size     int64
	read     int64
	lastShow time.Time
	show     func(read, size int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.show != nil && (time.Since(p.lastShow) >= 2*time.Second || p.read == p.size) {
		p.lastShow = time.Now()
		p.show(p.read, p.size)
	}
	return n, err
}

// uploadDump streams the file to the restore. The request has no overall
// timeout (a large file takes as long as it takes); the control plane ends it
// when the client stalls.
func (c *cli) uploadDump(ctx context.Context, id string, body io.Reader, name string, size int64) (restoreView, error) {
	target := apiBase(c.cfg.URL) + "/dump-restores/" + id + "/upload?fileName=" + url.QueryEscape(name)
	pr := &progressReader{r: body, size: size, show: func(read, size int64) {
		c.note("uploading %s of %s (%d%%)", formatBytes(read), formatBytes(size), read*100/size)
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, pr)
	if err != nil {
		return restoreView{}, err
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "nebulactrl-cli/"+version)
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		// The control plane may have refused the file and closed the connection
		// before the response could be read: ask what it says.
		rctx, cancel := c.ctx(context.WithoutCancel(ctx))
		defer cancel()
		if r, gerr := c.api.GetDumpRestoreWithResponse(rctx, id); gerr == nil && r.JSON200 != nil && r.JSON200.Error != nil && *r.JSON200.Error != "" {
			return restoreView{}, &exitError{1, fmt.Errorf("upload failed: %s", *r.JSON200.Error)}
		}
		return restoreView{}, fmt.Errorf("upload failed: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusAccepted {
		return restoreView{}, problem(res.StatusCode, raw)
	}
	var rv restoreView
	if err := json.Unmarshal(raw, &rv); err != nil {
		return restoreView{}, errors.New("upload finished, but the response was not understood")
	}
	return rv, nil
}

func formatBytes(n int64) string {
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
