package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Restore of a database from a dump file the control plane relays as a
// stream: dumprestore_request, fs_data frames, then the engine's client runs
// inside the database container reading them from its standard input.

const (
	// dumpIdle is how long the control plane may take to send the next chunk.
	dumpIdle = 3 * time.Minute
	// dumpLogEvery batches progress lines into one event.
	dumpLogEvery = 500 * time.Millisecond
	// dumpLogLines is the most lines in one event.
	dumpLogLines = 100
)

func (e *Engine) dumpRunner() *backup.DumpRunner {
	return &backup.DumpRunner{Docker: e.cfg.Docker}
}

// startDumpRestore runs one dump restore on its own goroutine. It runs on the
// connection's reader goroutine's behalf and therefore only registers the
// stream and returns.
func (e *Engine) startDumpRestore(req agentprotocol.DumpRestoreRequest) {
	ctx, cancel := context.WithCancel(context.Background())
	up := &upload{ch: make(chan agentprotocol.FSData, uploadQueue), cancel: cancel}
	e.term.mu.Lock()
	if e.term.cancels == nil {
		e.term.cancels = map[string]context.CancelFunc{}
	}
	if e.term.uploads == nil {
		e.term.uploads = map[string]*upload{}
	}
	e.term.cancels[req.RequestID] = cancel
	e.term.uploads[req.RequestID] = up
	e.term.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			e.term.mu.Lock()
			delete(e.term.cancels, req.RequestID)
			delete(e.term.uploads, req.RequestID)
			e.term.mu.Unlock()
		}()
		e.handleDumpRestore(ctx, req, up)
	}()
}

// dumpSource is the file as an io.Reader: it takes the chunks of the stream
// and acknowledges each one when it is taken, so the control plane's window
// (and the queue) hold back the sender while the engine's client is slow.
type dumpSource struct {
	ctx   context.Context
	ch    <-chan agentprotocol.FSData
	size  int64
	got   int64
	buf   []byte
	end   bool
	final error
	ack   func(n int)
	idle  time.Duration
}

func (d *dumpSource) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.final != nil {
			return 0, d.final
		}
		if d.end {
			return 0, io.EOF
		}
		timer := time.NewTimer(d.idle)
		select {
		case fd := <-d.ch:
			timer.Stop()
			if fd.Error != "" {
				d.final = errors.New("the upload was canceled: " + fd.Error)
				continue
			}
			if d.got+int64(len(fd.Data)) > d.size {
				d.final = errors.New("the upload carries more data than announced")
				continue
			}
			d.buf = fd.Data
			d.got += int64(len(fd.Data))
			if len(fd.Data) > 0 {
				d.ack(len(fd.Data))
			}
			if fd.EOF {
				d.end = true
				if d.got != d.size {
					d.final = fmt.Errorf("the upload ended after %d of %d bytes", d.got, d.size)
				}
			}
		case <-d.ctx.Done():
			timer.Stop()
			return 0, d.ctx.Err()
		case <-timer.C:
			return 0, errors.New("the upload stalled: no data for " + d.idle.String())
		}
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

// logBatcher sends progress lines to the control plane in batches.
type logBatcher struct {
	mu    sync.Mutex
	lines []string
	send  func([]string)
	stop  chan struct{}
	done  chan struct{}
}

func newLogBatcher(send func([]string)) *logBatcher {
	b := &logBatcher{send: send, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(b.done)
		t := time.NewTicker(dumpLogEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				b.flush()
			case <-b.stop:
				return
			}
		}
	}()
	return b
}

func (b *logBatcher) add(l string) {
	b.mu.Lock()
	b.lines = append(b.lines, l)
	full := len(b.lines) >= dumpLogLines
	b.mu.Unlock()
	if full {
		b.flush()
	}
}

func (b *logBatcher) flush() {
	b.mu.Lock()
	lines := b.lines
	b.lines = nil
	b.mu.Unlock()
	for len(lines) > 0 {
		n := min(len(lines), dumpLogLines)
		b.send(lines[:n])
		lines = lines[n:]
	}
}

// close flushes and stops the batcher.
func (b *logBatcher) close() {
	close(b.stop)
	<-b.done
	b.flush()
}

func (e *Engine) handleDumpRestore(ctx context.Context, req agentprotocol.DumpRestoreRequest, up *upload) {
	send := func(ev agentprotocol.DumpRestoreEvent) bool {
		ev.RequestID = req.RequestID
		return e.stream.Stream(agentprotocol.TypeDumpRestoreEvent, req.RequestID, ev)
	}
	fail := func(code, msg string) { send(agentprotocol.DumpRestoreEvent{Error: msg, Code: code}) }
	if req.Size <= 0 || req.RestoreID == "" {
		fail(agentprotocol.DumpErrBadRequest, "invalid restore request")
		return
	}
	cid, err := e.databaseContainer(ctx, req.Target.ServiceID)
	if err != nil {
		fail(agentprotocol.DumpErrNotRunning, err.Error())
		return
	}
	e.log.Info("restoring a database from a dump file", "service_id", req.Target.ServiceID, "restore_id", req.RestoreID, "engine", req.Target.Engine, "format", req.Format)

	batch := newLogBatcher(func(lines []string) {
		send(agentprotocol.DumpRestoreEvent{Stage: agentprotocol.DumpProgress, Lines: lines})
	})
	defer batch.close()

	src := &dumpSource{
		ctx: ctx, ch: up.ch, size: req.Size, idle: dumpIdle,
		ack: func(n int) {
			e.stream.Stream(agentprotocol.TypeStreamAck, req.RequestID, agentprotocol.StreamAck{StreamID: req.RequestID, Bytes: n})
		},
	}
	derr := e.dumpRunner().Run(ctx, cid, req, req.Secrets, src, func() {
		send(agentprotocol.DumpRestoreEvent{Stage: agentprotocol.DumpReady})
	}, batch.add)
	batch.flush()
	if derr != nil {
		e.log.Warn("dump restore failed", "restore_id", req.RestoreID, "err", derr.Msg)
		fail(derr.Code, derr.Msg)
		return
	}
	send(agentprotocol.DumpRestoreEvent{Stage: agentprotocol.DumpDone, Message: "restored"})
}
