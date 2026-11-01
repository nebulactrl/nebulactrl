package backups

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
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

// The upload of a dump file is relayed to the agent as an interactive stream:
// one dumprestore_request, then fs_data frames under the stream window (the
// agent acknowledges a chunk once the engine's client consumed it, so a slow
// restore slows the upload down instead of buffering it), then EOF. Nothing is
// stored on the control plane and at most one window of data is in memory.

const (
	// dumpAgentWait bounds the wait for the agent to answer the request.
	dumpAgentWait = 60 * time.Second
	// dumpStallWait bounds the wait for the agent to take more data.
	dumpStallWait = 2 * time.Minute
	// dumpReadIdle bounds the wait for the client to send more data.
	dumpReadIdle = 2 * time.Minute
	// dumpLogFlush is how often progress is written to the restore.
	dumpLogFlush = 2 * time.Second
	// dumpRestoreTimeout bounds one whole restore.
	dumpRestoreTimeout = 24 * time.Hour
	// maxLogLine clips one progress line.
	maxLogLine = 1000
)

// DumpUpload is the body of an upload request.
type DumpUpload struct {
	FileName string
	// Size is the exact length of Body (the request's Content-Length).
	Size int64
	Body io.Reader
	// ExtendRead, when set, is called with the time the client may take to
	// send more data before every read of Body.
	ExtendRead func(time.Duration)
}

type dumpOutcome struct {
	ok   bool
	msg  string
	code string
}

// dumpPump owns the stream of one dump restore: it routes the agent's frames,
// keeps the restore's log and progress and applies the outcome.
type dumpPump struct {
	s       *Service
	id      uuid.UUID
	rid     string
	st      Stream
	win     *agentprotocol.Window
	secrets []string

	sent     atomic.Int64
	readyCh  chan struct{}
	readyOne sync.Once
	doneCh   chan struct{}
	doneOne  sync.Once
	result   dumpOutcome

	mu       sync.Mutex
	lines    []string
	progress int64 // last bytes value written to the restore
}

func (p *dumpPump) ready() { p.readyOne.Do(func() { close(p.readyCh) }) }

// finish records the outcome once and stops the pump.
func (p *dumpPump) finish(r dumpOutcome) {
	p.doneOne.Do(func() {
		p.result = r
		close(p.doneCh)
	})
}

func (p *dumpPump) outcome() (dumpOutcome, bool) {
	select {
	case <-p.doneCh:
		return p.result, true
	default:
		return dumpOutcome{}, false
	}
}

// clean masks secrets and makes a line safe to store.
func (p *dumpPump) clean(l string) string {
	for _, sec := range p.secrets {
		if len(sec) >= 4 {
			l = strings.ReplaceAll(l, sec, "••••••")
		}
	}
	l = strings.ToValidUTF8(strings.ReplaceAll(l, "\x00", ""), "?")
	if len(l) > maxLogLine {
		l = strings.ToValidUTF8(l[:maxLogLine], "") + "…"
	}
	return strings.TrimRight(l, "\r\n")
}

func (p *dumpPump) addLines(ls ...string) {
	p.mu.Lock()
	for _, l := range ls {
		if l = p.clean(l); l != "" {
			p.lines = append(p.lines, l)
		}
	}
	p.mu.Unlock()
}

// flush writes the buffered log lines and the progress to the restore.
func (p *dumpPump) flush(ctx context.Context) {
	p.mu.Lock()
	lines := p.lines
	p.lines = nil
	last := p.progress
	p.mu.Unlock()
	if len(lines) > 0 {
		var b strings.Builder
		ts := p.s.now().UTC().Format("15:04:05")
		for _, l := range lines {
			b.WriteString(ts + " " + l + "\n")
		}
		if err := p.s.q.AppendDumpRestoreLog(ctx, db.AppendDumpRestoreLogParams{ID: p.id, Log: b.String()}); err != nil {
			p.s.log.WarnContext(ctx, "append dump restore log", "restore_id", p.id, "err", err)
		}
	}
	if n := p.sent.Load(); n != last {
		if err := p.s.q.SetDumpRestoreProgress(ctx, db.SetDumpRestoreProgressParams{ID: p.id, BytesReceived: n}); err == nil {
			p.mu.Lock()
			p.progress = n
			p.mu.Unlock()
		}
	}
}

func (p *dumpPump) onFrame(f agentprotocol.Frame) {
	switch f.Type {
	case agentprotocol.TypeStreamAck:
		var a agentprotocol.StreamAck
		if f.Into(&a) == nil {
			p.win.Ack(a.Bytes)
		}
	case agentprotocol.TypeDumpRestoreEvent:
		var ev agentprotocol.DumpRestoreEvent
		if f.Into(&ev) != nil {
			return
		}
		p.addLines(ev.Lines...)
		switch {
		case ev.Error != "":
			p.finish(dumpOutcome{msg: ev.Error, code: ev.Code})
		case ev.Stage == agentprotocol.DumpReady:
			p.ready()
		case ev.Stage == agentprotocol.DumpDone:
			if ev.Message != "" {
				p.addLines(ev.Message)
			}
			p.finish(dumpOutcome{ok: true})
		}
	}
}

// run routes frames until the restore has an outcome, then applies it. It is
// the only writer of the restore's status after the upload was claimed.
func (p *dumpPump) run(ctx context.Context) {
	defer p.s.dumpActive.Delete(p.id)
	defer p.win.Close()
	tick := time.NewTicker(dumpLogFlush)
	defer tick.Stop()
loop:
	for {
		select {
		case f := <-p.st.Frames():
			p.onFrame(f)
		case <-p.st.Done():
			p.finish(dumpOutcome{msg: "The server went offline during the restore; the transfer was lost. Nothing was switched over: start the restore again."})
		case <-p.doneCh:
			break loop
		case <-tick.C:
			p.flush(ctx)
		case <-ctx.Done():
			p.finish(dumpOutcome{msg: "The restore did not finish within " + dumpRestoreTimeout.String() + "."})
		}
	}
	// Tell the agent to stop (a no-op once it finished), then settle.
	_ = p.st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: p.rid})
	p.st.Close()
	// Frames the agent queued before the outcome (log lines) are in the buffer
	// already; the outcome itself ended the loop.
	p.flush(ctx)
	res := p.result
	if !res.ok && p.sent.Load() == 0 {
		// Nothing was consumed: the target is as it was, so the restore can
		// take another file.
		if err := p.s.q.ReleaseDumpRestoreUpload(ctx, db.ReleaseDumpRestoreUploadParams{ID: p.id, Error: clip(res.msg)}); err != nil {
			p.s.log.ErrorContext(ctx, "release dump restore", "restore_id", p.id, "err", err)
		}
		if r, err := p.s.q.GetDumpRestoreInternal(ctx, p.id); err == nil {
			if sc, err := services.LoadScopeInternal(ctx, p.s.q, r.SourceServiceID); err == nil {
				p.s.publish(sc, EventBackupUpdated, p.id)
			}
		}
		return
	}
	p.s.finishDump(ctx, p.id, res.ok, res.msg)
}

func (p *dumpPump) sendData(d agentprotocol.FSData) error {
	d.RequestID = p.rid
	return p.st.Send(agentprotocol.TypeFSData, d)
}

// agentWait is the wait for the agent's answers.
func (s *Service) agentWait() time.Duration {
	if s.dumpWait > 0 {
		return s.dumpWait
	}
	return dumpAgentWait
}

// dumpAgentError turns the agent's error into the API error of an upload that ended
// before the whole file was accepted.
func dumpAgentError(r dumpOutcome) error {
	switch r.code {
	case agentprotocol.DumpErrNotRunning:
		return apierr.Conflict("database_not_running", r.msg)
	case agentprotocol.DumpErrBadRequest:
		return apierr.Unprocessable("validation_failed", r.msg)
	}
	return apierr.Unprocessable("dump_restore_failed", r.msg)
}

// UploadDump relays an uploaded dump file to the agent of the restore's target
// database. It returns once the whole file reached the agent (status
// restoring); the engine's client may still be working through it, and the
// outcome is applied by a goroutine that outlives the request.
func (s *Service) UploadDump(ctx context.Context, id uuid.UUID, up DumpUpload) (DumpRestoreView, error) {
	r, sc, err := s.loadDump(ctx, policy.ScopeDeploymentsWrite, id)
	if err != nil {
		return DumpRestoreView{}, err
	}
	switch r.Status {
	case RestoreAwaitingUpload:
	case RestoreProvisioning, RestorePreBackup:
		return DumpRestoreView{}, apierr.Conflict("dump_restore_not_ready", "the target is not ready for the file yet ("+r.Status+"); wait until the restore awaits its upload")
	case RestoreUploading, RestoreRestoring:
		return DumpRestoreView{}, apierr.Conflict("dump_restore_in_progress", "a file was already uploaded for this restore")
	default:
		return DumpRestoreView{}, apierr.Conflict("dump_restore_finished", "this restore is already finished; start a new one")
	}
	if up.Size <= 0 {
		return DumpRestoreView{}, apierr.New(411, "length_required", "the upload needs a Content-Length greater than zero")
	}
	if up.Size > s.dumpLimit() {
		return DumpRestoreView{}, tooLarge(s.dumpLimit())
	}
	if s.streams == nil {
		return DumpRestoreView{}, offline()
	}
	targetID := r.SourceServiceID
	if r.TargetServiceID != nil {
		targetID = *r.TargetServiceID
	}
	tsc, err := services.LoadScopeInternal(ctx, s.q, targetID)
	if err != nil {
		return DumpRestoreView{}, fmt.Errorf("load target database: %w", err)
	}
	if tsc.Service.ServerID == nil {
		return DumpRestoreView{}, apierr.Unprocessable("no_server", "the target database is not placed on a server")
	}
	name, online, err := s.serverState(ctx, s.q, tsc.Service.ServerID)
	if err != nil {
		return DumpRestoreView{}, err
	}
	if !online {
		return DumpRestoreView{}, apierr.Conflict("server_offline", "the server "+name+" is offline; the upload was not started")
	}
	if ok, why := databaseRunning(tsc.Service, s.now()); !ok {
		return DumpRestoreView{}, apierr.Conflict("database_not_running", "the upload was not started: "+why)
	}
	tgt, err := s.resolveTarget(ctx, s.q, tsc)
	if err != nil {
		return DumpRestoreView{}, err
	}

	// Look at the start of the file before anything is changed.
	br := bufio.NewReaderSize(up.Body, SniffBytes)
	want := int(min(int64(SniffBytes), up.Size))
	if up.ExtendRead != nil {
		up.ExtendRead(dumpReadIdle)
	}
	prefix, perr := br.Peek(want)
	if len(prefix) < want {
		if perr == nil || errors.Is(perr, io.EOF) {
			return DumpRestoreView{}, apierr.BadRequest("short_body", "the request body is shorter than its Content-Length")
		}
		return DumpRestoreView{}, apierr.BadRequest("upload_interrupted", "the upload was interrupted before the file could be identified")
	}
	kind, err := SniffDump(prefix)
	if err != nil {
		return DumpRestoreView{}, err
	}
	if err := CheckDumpEngine(tgt.Engine, kind); err != nil {
		return DumpRestoreView{}, err
	}

	fileName := cleanFileName(up.FileName)
	if fileName == "" {
		fileName = r.FileName
	}
	_, err = s.q.ClaimDumpRestoreUpload(ctx, db.ClaimDumpRestoreUploadParams{
		ID: r.ID, FileName: fileName, Format: kind.Format, Gzip: kind.Gzip, SizeBytes: up.Size,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DumpRestoreView{}, apierr.Conflict("dump_restore_in_progress", "another upload for this restore started first")
	}
	if err != nil {
		return DumpRestoreView{}, fmt.Errorf("claim upload: %w", err)
	}
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{
			Action: ActionDumpRestoreUpload, ResourceType: "dump_restore", ResourceID: r.ID.String(), ProjectID: &sc.ProjectID,
			Metadata: map[string]any{"service": tsc.Service.Slug, "mode": r.Mode, "file": fileName, "size": up.Size, "format": DumpFormatLabel(kind)},
		})
	}); err != nil {
		_ = s.q.ReleaseDumpRestoreUpload(ctx, db.ReleaseDumpRestoreUploadParams{ID: r.ID, Error: "could not record the upload"})
		return DumpRestoreView{}, fmt.Errorf("audit upload: %w", err)
	}
	s.publish(sc, EventBackupUpdated, r.ID)

	release := func(msg string) {
		_ = s.q.ReleaseDumpRestoreUpload(context.WithoutCancel(ctx), db.ReleaseDumpRestoreUploadParams{ID: r.ID, Error: msg})
		s.publish(sc, EventBackupUpdated, r.ID)
	}
	rid := uuid.NewString()
	st, err := s.streams.OpenStream(*tsc.Service.ServerID, rid)
	if err != nil {
		release("the server is offline")
		return DumpRestoreView{}, offline()
	}
	p := &dumpPump{
		s: s, id: r.ID, rid: rid, st: st, win: agentprotocol.NewWindow(),
		readyCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	if tgt.Password != "" {
		p.secrets = append(p.secrets, tgt.Password)
	}
	req := agentprotocol.DumpRestoreRequest{
		RequestID: rid, RestoreID: r.ID.String(), Target: tgt.proto(targetID.String()), Format: kind.Format, Gzip: kind.Gzip,
		Replace: r.Mode == ModeInPlace, Size: up.Size,
	}
	if tgt.Password != "" {
		req.Secrets = map[string]string{agentprotocol.SecretDBPassword: tgt.Password}
	}
	if err := st.Send(agentprotocol.TypeDumpRestoreRequest, req); err != nil {
		st.Close()
		release("the server is offline")
		return DumpRestoreView{}, offline()
	}
	s.dumpActive.Store(r.ID, struct{}{})
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dumpRestoreTimeout)
	go func() {
		defer cancel()
		p.run(pctx)
	}()

	// The agent prepares the target (and answers ready) before any byte is sent.
	timer := time.NewTimer(s.agentWait())
	defer timer.Stop()
	select {
	case <-p.readyCh:
	case <-p.doneCh:
		return DumpRestoreView{}, s.awaitSettled(ctx, p)
	case <-timer.C:
		p.finish(dumpOutcome{msg: "the server's agent did not answer the restore request; it may be too old to restore dump files (update the agent)", code: agentprotocol.DumpErrFailed})
		return DumpRestoreView{}, s.awaitSettled(ctx, p)
	case <-ctx.Done():
		p.finish(dumpOutcome{msg: "the upload was interrupted"})
		return DumpRestoreView{}, apierr.BadRequest("upload_interrupted", "the upload was interrupted")
	}

	h := sha256.New()
	buf := make([]byte, agentprotocol.FSChunk)
	var sent int64
	abort := func(msg string) {
		_ = p.sendData(agentprotocol.FSData{Error: "canceled"})
		p.finish(dumpOutcome{msg: msg})
	}
	for sent < up.Size {
		if res, done := p.outcome(); done {
			return s.dumpEnded(ctx, p, res)
		}
		if up.ExtendRead != nil {
			up.ExtendRead(dumpReadIdle)
		}
		n, rerr := io.ReadFull(br, buf[:int(min(int64(len(buf)), up.Size-sent))])
		if n > 0 {
			actx, acancel := context.WithTimeout(ctx, dumpStallWait)
			aerr := p.win.Acquire(actx, n)
			acancel()
			if aerr != nil {
				if res, done := p.outcome(); done {
					return s.dumpEnded(ctx, p, res)
				}
				if ctx.Err() != nil {
					abort(fmt.Sprintf("The upload was interrupted after %s of %s; the restore was abandoned. Nothing was switched over.", humanBytes(sent), humanBytes(up.Size)))
					return DumpRestoreView{}, apierr.BadRequest("upload_interrupted", "the upload was interrupted")
				}
				abort(fmt.Sprintf("The server stopped accepting data after %s of %s; the restore was abandoned.", humanBytes(sent), humanBytes(up.Size)))
				return DumpRestoreView{}, apierr.Conflict("upload_interrupted", "the server stopped accepting data")
			}
			h.Write(buf[:n])
			if err := p.sendData(agentprotocol.FSData{Data: append([]byte(nil), buf[:n]...)}); err != nil {
				p.finish(dumpOutcome{msg: "The server went offline during the upload; the transfer was lost. Nothing was switched over: start the restore again."})
				return DumpRestoreView{}, apierr.Conflict("server_offline", "the server went offline during the upload")
			}
			sent += int64(n)
			p.sent.Store(sent)
		}
		if rerr != nil {
			break
		}
	}
	if sent != up.Size {
		abort(fmt.Sprintf("The upload ended after %s of %s; the restore was abandoned. Nothing was switched over.", humanBytes(sent), humanBytes(up.Size)))
		return DumpRestoreView{}, apierr.BadRequest("short_body", "the request body is shorter than its Content-Length")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	p.addLines(fmt.Sprintf("upload complete: %s, sha256 %s", humanBytes(sent), sum))
	// Recorded before EOF: a small dump may be restored before this returns.
	if err := s.q.SetDumpRestoreRestoring(ctx, db.SetDumpRestoreRestoringParams{ID: r.ID, BytesReceived: sent, Sha256: sum}); err != nil {
		s.log.ErrorContext(ctx, "dump restore restoring", "restore_id", r.ID, "err", err)
	}
	if err := p.sendData(agentprotocol.FSData{EOF: true}); err != nil {
		p.finish(dumpOutcome{msg: "The server went offline during the upload; the transfer was lost. Nothing was switched over: start the restore again."})
		return DumpRestoreView{}, apierr.Conflict("server_offline", "the server went offline during the upload")
	}
	s.publish(sc, EventBackupUpdated, r.ID)
	cur, err := s.q.GetDumpRestoreInternal(ctx, r.ID)
	if err != nil {
		return DumpRestoreView{}, fmt.Errorf("reload restore: %w", err)
	}
	return s.dumpView(cur, true), nil
}

// awaitSettled waits for the pump to apply a pre-data failure, then returns
// the API error for it.
func (s *Service) awaitSettled(ctx context.Context, p *dumpPump) error {
	res, _ := p.outcome()
	// The pump releases the restore asynchronously; give it a moment so the
	// client that retries right away finds it awaiting the file again.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, live := s.dumpActive.Load(p.id); !live {
			break
		}
		select {
		case <-ctx.Done():
			return dumpAgentError(res)
		case <-time.After(20 * time.Millisecond):
		}
	}
	return dumpAgentError(res)
}

// dumpEnded answers an upload whose restore ended before the whole file was
// sent: a failure is the error of the request; a success (the engine needed no
// more of the file) is the finished restore.
func (s *Service) dumpEnded(ctx context.Context, p *dumpPump, res dumpOutcome) (DumpRestoreView, error) {
	if !res.ok {
		return DumpRestoreView{}, s.awaitSettled(ctx, p)
	}
	for i := 0; i < 250; i++ {
		if _, live := s.dumpActive.Load(p.id); !live {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	cur, err := s.q.GetDumpRestoreInternal(ctx, p.id)
	if err != nil {
		return DumpRestoreView{}, fmt.Errorf("reload restore: %w", err)
	}
	return s.dumpView(cur, true), nil
}
