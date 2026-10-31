// Package logs batches and streams log lines from the agent to the control
// plane: deploy/build output, and runtime container logs for subscriptions.
package logs

import (
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// FlushInterval is the longest a line waits before being sent.
	FlushInterval = 200 * time.Millisecond
	// MaxLines is the most lines in one frame.
	MaxLines = 200
	// MaxLineBytes truncates pathological lines.
	MaxLineBytes = 16 << 10
)

// Batcher accumulates lines and emits them as LogFrames: every FlushInterval
// or as soon as MaxLines are pending, whichever comes first. Safe for
// concurrent use. Emit is never called concurrently with itself, so frames
// keep their order.
type Batcher struct {
	base agentprotocol.LogFrame // identifiers + stream; Lines filled per frame
	emit func(agentprotocol.LogFrame)
	rep  func(string) string // optional redaction applied to every line

	emitMu sync.Mutex // serializes emit

	mu      sync.Mutex
	pending []agentprotocol.LogLine
	closed  bool
	stop    chan struct{}
	done    chan struct{}
}

// NewBatcher starts a batcher. base carries the frame identity (ServiceID,
// DeploymentID/StreamID, Stream). redact, when non-nil, is applied to every
// line before it is queued. Close must be called to flush and stop.
func NewBatcher(base agentprotocol.LogFrame, emit func(agentprotocol.LogFrame), redact func(string) string) *Batcher {
	b := &Batcher{base: base, emit: emit, rep: redact, stop: make(chan struct{}), done: make(chan struct{})}
	go b.loop()
	return b
}

func (b *Batcher) loop() {
	defer close(b.done)
	t := time.NewTicker(FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.flush()
		case <-b.stop:
			return
		}
	}
}

// Add queues one line stamped with the current time.
func (b *Batcher) Add(text string) { b.AddAt(time.Now(), text) }

// AddAt queues one line with an explicit timestamp (zero = now). Embedded
// newlines split the text into several lines.
func (b *Batcher) AddAt(ts time.Time, text string) {
	if ts.IsZero() {
		ts = time.Now()
	}
	text = strings.TrimRight(text, "\r\n")
	if strings.ContainsRune(text, '\n') {
		for _, part := range strings.Split(text, "\n") {
			b.AddAt(ts, part)
		}
		return
	}
	if len(text) > MaxLineBytes {
		text = text[:MaxLineBytes] + "… [line truncated]"
	}
	if b.rep != nil {
		text = b.rep(text)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.pending = append(b.pending, agentprotocol.LogLine{TS: ts, Text: text})
	full := len(b.pending) >= MaxLines
	b.mu.Unlock()
	if full {
		b.flush()
	}
}

func (b *Batcher) flush() {
	b.emitMu.Lock()
	defer b.emitMu.Unlock()
	for {
		b.mu.Lock()
		n := len(b.pending)
		if n == 0 {
			b.mu.Unlock()
			return
		}
		if n > MaxLines {
			n = MaxLines
		}
		lines := make([]agentprotocol.LogLine, n)
		copy(lines, b.pending[:n])
		b.pending = b.pending[n:]
		b.mu.Unlock()

		f := b.base
		f.Lines = lines
		b.emit(f)
	}
}

// Close flushes remaining lines and stops the timer. Idempotent.
func (b *Batcher) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()
	close(b.stop)
	<-b.done
	b.flush()
}
