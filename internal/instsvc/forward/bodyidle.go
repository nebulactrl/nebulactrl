package forward

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// defaultBodyIdle is how long a request body may stay silent before the stream
// is cut. ReadHeaderTimeout covers the headers; without this, a client that
// declares a body and then sends nothing holds its stream (and one of the
// instance's few tunnel slots) until the 5 minute idle timeout, or forever
// while the control plane waits for bytes.
const defaultBodyIdle = 60 * time.Second

// withBodyIdleTimeout re-arms the connection's read deadline before every read
// of the request body, so a body that makes no progress for idle fails with a
// timeout. The deadline is cleared when the body ends, leaving the server's own
// header and keep-alive deadlines in charge.
func withBodyIdleTimeout(h http.Handler, idle time.Duration) http.Handler {
	if idle <= 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = &idleBody{rc: r.Body, ctl: http.NewResponseController(w), idle: idle}
		}
		h.ServeHTTP(w, r)
	})
}

type idleBody struct {
	rc   io.ReadCloser
	ctl  *http.ResponseController
	idle time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	_ = b.ctl.SetReadDeadline(time.Now().Add(b.idle))
	n, err := b.rc.Read(p)
	if errors.Is(err, io.EOF) {
		// The body ended well: leave the server's own header and keep-alive
		// deadlines in charge. After any other error the deadline stays as it is
		// (expired after a timeout), so the server's attempt to drain the rest of
		// the body fails at once and it closes the connection instead of waiting.
		_ = b.ctl.SetReadDeadline(time.Time{})
	}
	return n, err
}

func (b *idleBody) Close() error { return b.rc.Close() }
