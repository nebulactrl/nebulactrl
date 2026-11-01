package logs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// ActionServiceLogsDownload is the audit action of a runtime log download.
const ActionServiceLogsDownload = "service.logs.download"

// downloadTimeout bounds one download, however many pages it takes.
const downloadTimeout = 10 * time.Minute

// Download is an opened runtime log download: the first page is already read,
// so every error that can be an ordinary error response is returned by
// OpenDownload, before the first byte of the file.
type Download struct {
	svc       *Service
	serviceID uuid.UUID
	// Filename is the suggested file name (without a path).
	Filename string
	format   string
	p        HistoryParams
	first    LogHistoryPage
}

// DownloadParams selects the range of a download.
type DownloadParams struct {
	From, To  time.Time // zero = open
	Query     string
	Component string
	Process   string
	Format    string // txt (default) or ndjson
}

// OpenDownload authorizes (logs:read), reads the first page of the service's
// stored runtime log in time order and audits the export. The log is the
// server's own history (see ServiceHistory), redacted exactly like the log view.
func (s *Service) OpenDownload(ctx context.Context, serviceID uuid.UUID, dp DownloadParams) (*Download, error) {
	format, err := services.ParseDownloadFormat(dp.Format)
	if err != nil {
		return nil, err
	}
	sc, err := services.LoadScope(ctx, s.q, policy.ScopeLogsRead, serviceID)
	if err != nil {
		return nil, err
	}
	p := HistoryParams{From: dp.From, To: dp.To, Query: dp.Query, Component: dp.Component, Process: dp.Process, Limit: maxHistoryLimit, Order: agentprotocol.LogOrderAsc}
	first, err := s.ServiceHistory(ctx, serviceID, p)
	if err != nil {
		return nil, err
	}
	if !first.Capturing {
		return nil, apierr.Conflict("history_disabled", "log history capture is switched off on this service's server, so there is nothing to download")
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionServiceLogsDownload, ResourceType: "service", ResourceID: serviceID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{
				"service": sc.Service.Slug, "format": format, "from": zeroNil(dp.From), "to": zeroNil(dp.To),
				"query": dp.Query != "", "component": dp.Component, "process": dp.Process,
			},
		})
	})
	if err != nil {
		return nil, fmt.Errorf("audit log download: %w", err)
	}
	return &Download{
		svc: s, serviceID: serviceID, format: format, p: p, first: first,
		Filename: services.DownloadFilename(sc.Service.Slug, "logs", s.now(), format),
	}, nil
}

func zeroNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// Format is the file format (txt or ndjson).
func (d *Download) Format() string { return d.format }

// WriteTo pages through the history and writes every line to out until the
// range is exhausted or services.MaxDownloadLines is reached. A read error
// after the first page ends the file with a note: the bytes already sent
// cannot become an error response.
func (d *Download) WriteTo(ctx context.Context, out *services.LogDownload) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	page := d.first
	for {
		for _, e := range page.Items {
			if out.Full() {
				out.Note(fmt.Sprintf("download truncated at %d lines; narrow the time range (from/to) or the query", services.MaxDownloadLines))
				return
			}
			var replica *int
			if e.Replica > 0 {
				r := e.Replica
				replica = &r
			}
			if err := out.Write(services.DownloadLine{TS: e.TS, Stream: e.Stream, Replica: replica, DeploymentID: e.DeploymentID, Line: e.Line}); err != nil {
				return // the client went away
			}
		}
		if page.NextCursor == "" {
			return
		}
		if ctx.Err() != nil {
			out.Note("download stopped: " + ctx.Err().Error())
			return
		}
		next := d.p
		next.Cursor = page.NextCursor
		var err error
		if page, err = d.svc.ServiceHistory(ctx, d.serviceID, next); err != nil {
			out.Note("download stopped: " + err.Error())
			return
		}
	}
}
