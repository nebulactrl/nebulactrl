package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// Log download formats.
const (
	DownloadText   = "txt"
	DownloadNDJSON = "ndjson"
)

// MaxDownloadLines bounds one log download. A longer range is cut and the file
// says so on its last line; the caller narrows the time range.
const MaxDownloadLines = 200_000

// DownloadLine is one line of a log download. Every line is redacted by the
// caller before it gets here.
type DownloadLine struct {
	TS           time.Time `json:"ts"`
	Stream       string    `json:"stream,omitempty"`
	Replica      *int      `json:"replica,omitempty"`
	DeploymentID string    `json:"deploymentId,omitempty"`
	Line         string    `json:"line"`
}

// ParseDownloadFormat validates a `format` query value ("" is txt).
func ParseDownloadFormat(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", DownloadText:
		return DownloadText, nil
	case DownloadNDJSON:
		return DownloadNDJSON, nil
	}
	return "", apierr.Unprocessable("validation_failed", "format must be txt or ndjson")
}

// DownloadFilename names a log download: <slug>-<what>-<UTC time>.<ext>. The
// slug is a DNS-style slug, so the name is safe inside a header.
func DownloadFilename(slug, what string, at time.Time, format string) string {
	return fmt.Sprintf("%s-%s-%s.%s", slug, what, at.UTC().Format("20060102T150405Z"), format)
}

// DownloadResponses documents the file response of the log download endpoints.
func DownloadResponses() map[string]*huma.Response {
	return map[string]*huma.Response{
		"200": {
			Description: "The log as a file (`Content-Disposition: attachment`): plain text, one line per log line, or NDJSON " +
				"(`{ts, stream, line, …}` per line). Secret values are masked. A range longer than the cap ends with a truncation note.",
			Content: map[string]*huma.MediaType{
				"text/plain":           {Schema: &huma.Schema{Type: "string"}},
				"application/x-ndjson": {Schema: &huma.Schema{Type: "string"}},
			},
		},
	}
}

// LogDownload writes a log file from a huma.StreamResponse body.
type LogDownload struct {
	w      io.Writer
	rc     *http.ResponseController
	format string
	lines  int
}

// NewLogDownload prepares hctx for a log file download named filename.
func NewLogDownload(hctx huma.Context, format, filename string) *LogDownload {
	_, w := humachi.Unwrap(hctx)
	ct := "text/plain; charset=utf-8"
	if format == DownloadNDJSON {
		ct = "application/x-ndjson"
	}
	hctx.SetHeader("Content-Type", ct)
	hctx.SetHeader("Content-Disposition", `attachment; filename="`+filename+`"`)
	hctx.SetHeader("Cache-Control", "no-store")
	hctx.SetHeader("X-Content-Type-Options", "nosniff")
	hctx.SetStatus(http.StatusOK)
	d := NewLogDownloadTo(hctx.BodyWriter(), format)
	d.rc = http.NewResponseController(w)
	_ = d.rc.SetWriteDeadline(time.Time{}) // a long range streams for minutes
	return d
}

// NewLogDownloadTo writes a log file to w, with no HTTP response to prepare.
func NewLogDownloadTo(w io.Writer, format string) *LogDownload {
	return &LogDownload{w: w, format: format}
}

// Lines is how many lines were written.
func (d *LogDownload) Lines() int { return d.lines }

// Full reports whether the download reached MaxDownloadLines.
func (d *LogDownload) Full() bool { return d.lines >= MaxDownloadLines }

// Write appends one line.
func (d *LogDownload) Write(l DownloadLine) error {
	l.Line = strings.TrimRight(l.Line, "\r\n")
	l.TS = l.TS.UTC()
	var err error
	if d.format == DownloadNDJSON {
		var b []byte
		if b, err = json.Marshal(l); err == nil {
			_, err = d.w.Write(append(b, '\n'))
		}
	} else {
		prefix := l.TS.Format(time.RFC3339Nano)
		if l.Stream != "" && l.Stream != "runtime" {
			prefix += " [" + l.Stream + "]"
		}
		_, err = io.WriteString(d.w, prefix+" "+l.Line+"\n")
	}
	if err != nil {
		return err
	}
	d.lines++
	if d.lines%500 == 0 {
		d.flush()
	}
	return nil
}

// Note ends the file with a note that is not a log line (a cut range or a read
// error): a comment line in text, a `{"note": …}` object in NDJSON.
func (d *LogDownload) Note(msg string) {
	if d.format == DownloadNDJSON {
		b, _ := json.Marshal(struct {
			Note  string `json:"note"`
			Lines int    `json:"lines"`
		}{msg, d.lines})
		_, _ = d.w.Write(append(b, '\n'))
	} else {
		_, _ = io.WriteString(d.w, "# "+msg+"\n")
	}
	d.flush()
}

// Flush pushes buffered output to the client.
func (d *LogDownload) flush() {
	if d.rc != nil {
		_ = d.rc.Flush()
	}
}
