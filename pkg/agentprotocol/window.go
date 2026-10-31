package agentprotocol

import (
	"context"
	"errors"
	"sync"
)

// ErrWindowClosed is returned by Window.Acquire once the stream ended.
var ErrWindowClosed = errors.New("stream closed")

// Window is the sender side of stream flow control: it admits payload while
// fewer than StreamWindow bytes are unacknowledged. The receiver's
// stream_ack frames call Ack.
type Window struct {
	mu       sync.Mutex
	inflight int
	closed   bool
	wake     chan struct{}
}

// NewWindow returns an empty Window.
func NewWindow() *Window { return &Window{wake: make(chan struct{}, 1)} }

func (w *Window) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Acquire reserves n bytes, waiting for acknowledgements while the window is
// full. A single chunk larger than the window still goes out once nothing is
// in flight. It returns ErrWindowClosed after Close and ctx.Err() on cancel.
func (w *Window) Acquire(ctx context.Context, n int) error {
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return ErrWindowClosed
		}
		if w.inflight == 0 || w.inflight+n <= StreamWindow {
			w.inflight += n
			w.mu.Unlock()
			return nil
		}
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.wake:
		}
	}
}

// Ack credits n acknowledged bytes.
func (w *Window) Ack(n int) {
	w.mu.Lock()
	w.inflight = max(0, w.inflight-n)
	w.mu.Unlock()
	w.signal()
}

// Close fails every current and future Acquire.
func (w *Window) Close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.signal()
}
