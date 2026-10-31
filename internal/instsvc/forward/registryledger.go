package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// RegistryLedger is what the registry forwarder needs to bound a team's use of
// the shared registry disk. instsvc.Service implements it; a Slices that does
// is picked up by New (Deps.Ledger overrides).
type RegistryLedger interface {
	// CheckRegistryWrite refuses a write (instsvc.ErrQuotaExceeded when the
	// team's registry quota is used up, instsvc.ErrDiskLow when the host disk is
	// nearly full).
	CheckRegistryWrite(ctx context.Context, team uuid.UUID) error
	// RecordRegistryPush records a manifest the team pushed (repository without
	// the registry host, digest, bytes the manifest references).
	RecordRegistryPush(ctx context.Context, team uuid.UUID, repository, digest string, size int64) error
}

// maxManifestCapture bounds the manifest body kept to read the sizes in it
// (Distribution's own limit for a manifest is 4 MiB).
const maxManifestCapture = 4 << 20

// captureBody keeps the first max bytes that pass through it.
type captureBody struct {
	rc  io.ReadCloser
	buf bytes.Buffer
	max int
}

func (c *captureBody) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if room := c.max - c.buf.Len(); room > 0 && n > 0 {
		c.buf.Write(p[:min(n, room)])
	}
	return n, err
}

func (c *captureBody) Close() error { return c.rc.Close() }

// statusRecorder remembers the status the proxy answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// manifestBytes sums the sizes a manifest (image manifest or index) lists: its
// config, layers or child manifests. A body that does not parse counts as 0.
func manifestBytes(body []byte) int64 {
	var m struct {
		Config    struct{ Size int64 }   `json:"config"`
		Layers    []struct{ Size int64 } `json:"layers"`
		Blobs     []struct{ Size int64 } `json:"blobs"`
		Manifests []struct{ Size int64 } `json:"manifests"`
	}
	if json.Unmarshal(body, &m) != nil {
		return 0
	}
	total := max(m.Config.Size, 0)
	for _, l := range m.Layers {
		total += max(l.Size, 0)
	}
	for _, l := range m.Blobs {
		total += max(l.Size, 0)
	}
	for _, l := range m.Manifests {
		total += max(l.Size, 0)
	}
	return total
}

// recordPush writes the ledger row of a manifest that was just accepted.
func (f *Forwarder) recordPush(ctx context.Context, team uuid.UUID, repository, digest string, body []byte) {
	if f.d.Ledger == nil || digest == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := f.d.Ledger.RecordRegistryPush(ctx, team, repository, digest, manifestBytes(body)); err != nil {
		f.log.Warn("record registry push", "team_id", team.String(), "repository", repository, "err", err)
	}
}
