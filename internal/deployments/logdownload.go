package deployments

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// ActionLogsDownload is the audit action of a deployment log download.
const ActionLogsDownload = "deployment.logs.download"

// logDownloadPage is how many stored lines one database read returns.
const logDownloadPage = 1000

type logDownloadInput struct {
	ID      uuid.UUID `path:"deploymentId"`
	Format  string    `query:"format" enum:"txt,ndjson" default:"txt" doc:"txt (default) or ndjson."`
	Streams string    `query:"streams" maxLength:"64" doc:"Which streams to include: empty for build, deploy and runtime; all to include the debug stream; or a comma-separated list of build, deploy, runtime and debug"`
	From    time.Time `query:"from" doc:"Only lines at or after this time (RFC 3339)."`
	To      time.Time `query:"to" doc:"Only lines before this time (RFC 3339)."`
}

// LogDownload is an opened deployment log download.
type LogDownload struct {
	// Filename is the suggested file name (without a path).
	Filename string
	format   string
	svc      *Service
	id       uuid.UUID
	streams  []string
	from, to time.Time
	redact   func(string) string
}

// OpenLogDownload authorizes (logs:read) and audits a download of the
// deployment's persisted build, deploy and runtime lines.
func (s *Service) OpenLogDownload(ctx context.Context, id uuid.UUID, format, streamsParam string, from, to time.Time) (*LogDownload, error) {
	format, err := services.ParseDownloadFormat(format)
	if err != nil {
		return nil, err
	}
	streams, err := ParseLogStreams(streamsParam)
	if err != nil {
		return nil, err
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return nil, apierr.Unprocessable("validation_failed", "from must be before to")
	}
	d, err := s.loadDeployment(ctx, policy.ScopeLogsRead, id)
	if err != nil {
		return nil, err
	}
	// Stored lines were masked when they were written; mask again with the
	// service's current secrets (a variable added since is covered too). Without
	// a redactor nothing is served.
	redact, err := s.debugScrubber(ctx, d.Dep.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("redactor: %w", err)
	}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionLogsDownload, ResourceType: "deployment", ResourceID: id.String(), ProjectID: &d.ProjectID,
			Metadata: map[string]any{"service": d.ServiceSlug, "format": format, "streams": streams},
		})
	})
	if err != nil {
		return nil, fmt.Errorf("audit log download: %w", err)
	}
	short := id.String()[:8]
	return &LogDownload{
		Filename: services.DownloadFilename(d.ServiceSlug, "deploy-"+short, time.Now(), format),
		format:   format, svc: s, id: id, streams: streams, from: from, to: to, redact: redact,
	}, nil
}

// Format is the file format (txt or ndjson).
func (d *LogDownload) Format() string { return d.format }

// WriteTo writes the lines in the order they were recorded. The persisted log
// is capped by the hub (see MaxLogLines), so the download is bounded too.
func (d *LogDownload) WriteTo(ctx context.Context, out *services.LogDownload) {
	var after int64
	for {
		lines, err := d.svc.readLines(ctx, d.id, after, logDownloadPage, d.streams)
		if err != nil {
			d.svc.Logger.WarnContext(ctx, "deployment log download stopped", "correlation_id", d.id.String(), "err", err)
			out.Note("download stopped: the log could not be read")
			return
		}
		for _, l := range lines {
			after = l.ID
			if (!d.from.IsZero() && l.CreatedAt.Before(d.from)) || (!d.to.IsZero() && !l.CreatedAt.Before(d.to)) {
				continue
			}
			if out.Full() {
				out.Note(fmt.Sprintf("download truncated at %d lines", services.MaxDownloadLines))
				return
			}
			if out.Write(services.DownloadLine{TS: l.CreatedAt, Stream: l.Stream, Line: d.redact(l.Line)}) != nil {
				return // the client went away
			}
		}
		if len(lines) < logDownloadPage {
			return
		}
	}
}

func registerLogDownload(h huma.API, svc *Service) {
	huma.Register(h, api.WithScopes(huma.Operation{
		OperationID: "downloadDeploymentLogs", Method: http.MethodGet, Path: "/deployments/{deploymentId}/logs/download", Tags: []string{"logs"},
		Summary: "Download a deployment's build and deploy log as a file",
		Description: "The persisted lines of the deployment (at most 10,000, plus 500 `debug` lines when asked for) as `format=txt` (default; " +
			"`<time> [<stream>] <line>`, runtime lines without the tag) or `format=ndjson` (`{ts, stream, line}` per line), in the order they were recorded, optionally limited to a " +
			"`from`/`to` time range. Secret values are redacted. Audited as `deployment.logs.download`.",
		Responses: services.DownloadResponses(),
		Errors:    []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, policy.ScopeLogsRead), func(ctx context.Context, in *logDownloadInput) (*huma.StreamResponse, error) {
		dl, err := svc.OpenLogDownload(ctx, in.ID, in.Format, in.Streams, in.From, in.To)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			dl.WriteTo(hctx.Context(), services.NewLogDownload(hctx, dl.Format(), dl.Filename))
		}}, nil
	})
}
