package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/hostpty"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Interactive streams: terminal sessions (exec with a TTY) and the file
// browser. They arrive as stream frames (HandleStream), not as durable
// commands: a session lives and dies with the control-plane connection, and
// nothing about it is stored on the agent. The terminal's I/O is never logged.

// StreamSender is the part of the control-plane connection stream handling
// uses. *conn.Client implements it; New picks it up from Config.Sender.
type StreamSender interface {
	// Stream queues a frame of stream id and reports whether it was queued
	// (false while disconnected).
	Stream(typ, id string, body any) bool
}

const (
	// maxTermSessions bounds the concurrent terminal sessions of one agent.
	maxTermSessions = 32
	// autoShell runs bash when the image has it, else sh.
	autoShell = "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"
	// termInputQueue bounds the keystrokes buffered for one session.
	termInputQueue = 256
	// termWaitExit bounds the wait for the exit code after the output ended.
	termWaitExit = 5 * time.Second
)

// creditGate is the sender side of stream flow control: Acquire blocks while
// StreamWindow bytes are unacknowledged.
type creditGate = agentprotocol.Window

func newCreditGate() *creditGate { return agentprotocol.NewWindow() }

// termState is the Engine's registry of live streams.
type termState struct {
	mu       sync.Mutex
	sessions map[string]*termSession
	gates    map[string]*creditGate // download streams by request ID
	uploads  map[string]*upload
	cancels  map[string]context.CancelFunc // file operations by request ID
	// starting holds the IDs of host shells being started (their helper
	// container may exist before the session is registered); the sweep leaves
	// those alone.
	starting map[string]struct{}
	// hostPTY starts a shell on the agent's own pseudo terminal (nil: hostpty).
	// Tests replace it.
	hostPTY func(hostpty.Spec) (docker.TTYSession, error)
}

func (t *termState) session(id string) *termSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[id]
}

// cancelOp cancels a running file operation by request ID.
func (t *termState) cancelOp(id string) {
	t.mu.Lock()
	cancel := t.cancels[id]
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (t *termState) gate(id string) *creditGate {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.sessions[id]; s != nil {
		return s.gate
	}
	return t.gates[id]
}

// closeAll ends every session and file operation.
func (t *termState) closeAll() {
	t.mu.Lock()
	sessions := make([]*termSession, 0, len(t.sessions))
	for _, s := range t.sessions {
		sessions = append(sessions, s)
	}
	cancels := make([]context.CancelFunc, 0, len(t.cancels))
	for _, c := range t.cancels {
		cancels = append(cancels, c)
	}
	t.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
	for _, c := range cancels {
		c()
	}
}

// termSession is one running terminal.
type termSession struct {
	id     string
	e      *Engine
	tty    docker.TTYSession
	ctx    context.Context
	cancel context.CancelFunc
	gate   *creditGate
	ctl    chan termCtl
	once   sync.Once
	// byPeer is set when the control plane (or a disconnect) ended the
	// session, so no exit frame is sent back.
	byPeer bool
	// release, when set, runs once when the session is over (doRelease).
	release     func()
	releaseOnce sync.Once
}

// doRelease runs the session's release function once.
func (s *termSession) doRelease() {
	if s.release != nil {
		s.releaseOnce.Do(s.release)
	}
}

type termCtl struct {
	input  []byte
	resize bool
	cols   int
	rows   int
}

func (s *termSession) push(m termCtl) {
	select {
	case s.ctl <- m:
	default:
		s.e.log.Warn("terminal input queue full; dropping input", "session", s.id)
	}
}

// close ends the session at the control plane's request: it drops the
// attachment (the process gets SIGHUP with its terminal) and stops the pumps.
func (s *termSession) close() { s.end(true) }

// end ends the session once. byPeer says no exit frame is owed to the control
// plane (it ended the session itself, or the connection is gone).
func (s *termSession) end(byPeer bool) {
	s.once.Do(func() {
		s.byPeer = byPeer
		s.cancel()
		s.gate.Close()
		_ = s.tty.Close()
	})
}

func clampTerm(v, def int) int {
	if v <= 0 {
		return def
	}
	return min(max(v, agentprotocol.MinTermSize), agentprotocol.MaxTermSize)
}

// HandleStream implements conn.StreamHandler. It runs on the connection's
// reader goroutine and therefore never blocks.
func (e *Engine) HandleStream(f agentprotocol.Frame) {
	if e.stream == nil {
		return
	}
	switch f.Type {
	case agentprotocol.TypeExecOpen:
		var r agentprotocol.ExecOpen
		if f.Into(&r) == nil {
			go e.openTerm(r)
		}
	case agentprotocol.TypeExecInput:
		var in agentprotocol.ExecInput
		if f.Into(&in) == nil {
			if s := e.term.session(in.SessionID); s != nil {
				s.push(termCtl{input: in.Data})
			}
		}
	case agentprotocol.TypeExecResize:
		var rz agentprotocol.ExecResize
		if f.Into(&rz) == nil {
			if s := e.term.session(rz.SessionID); s != nil {
				s.push(termCtl{resize: true, cols: rz.Cols, rows: rz.Rows})
			}
		}
	case agentprotocol.TypeExecClose:
		// The ID names a terminal session, or a file operation the control
		// plane abandoned.
		var c agentprotocol.ExecClose
		if f.Into(&c) == nil {
			if s := e.term.session(c.SessionID); s != nil {
				s.close()
			} else {
				e.term.cancelOp(c.SessionID)
			}
		}
	case agentprotocol.TypeStreamAck:
		var a agentprotocol.StreamAck
		if f.Into(&a) == nil {
			if g := e.term.gate(a.StreamID); g != nil {
				g.Ack(a.Bytes)
			}
		}
	case agentprotocol.TypeFSRequest:
		var r agentprotocol.FSRequest
		if f.Into(&r) == nil {
			e.startFS(r)
		}
	case agentprotocol.TypeVolFileRequest:
		var r agentprotocol.VolFileRequest
		if f.Into(&r) == nil {
			e.startVolFile(r)
		}
	case agentprotocol.TypeDumpRestoreRequest:
		var r agentprotocol.DumpRestoreRequest
		if f.Into(&r) == nil {
			e.startDumpRestore(r)
		}
	case agentprotocol.TypeFSData:
		var d agentprotocol.FSData
		if f.Into(&d) == nil {
			e.uploadData(d)
		}
	}
}

// Disconnected implements conn.StreamHandler: the connection is gone, so every
// session and file transfer is over.
func (e *Engine) Disconnected() { e.term.closeAll() }

// replicaContainer returns the running container of replica index of a process
// of the service's current deployment ("" is the default process, which also
// matches containers without a process label; replica counts within the
// process). The container must carry the service's managed labels: a stream
// can only ever reach a service's own replicas.
func (e *Engine) replicaContainer(ctx context.Context, serviceID, process string, replica int) (docker.ContainerInfo, error) {
	for _, c := range e.currentProcessReplicas(ctx, serviceID, process) {
		if replicaIndex(c) != replica {
			continue
		}
		if c.Labels[agentprotocol.LabelManaged] != "true" || c.Labels[agentprotocol.LabelService] != serviceID {
			continue
		}
		if c.Running {
			return c, nil
		}
	}
	return docker.ContainerInfo{}, errNoReplica
}

var errNoReplica = errors.New("replica is not running")

// noReplicaMessage tells a user which replica a stream could not reach.
func noReplicaMessage(process string, replica int) string {
	if process == "" {
		return fmt.Sprintf("replica %d is not running", replica)
	}
	return fmt.Sprintf("replica %d of process %q is not running", replica, process)
}

func (e *Engine) terminalDocker() (docker.Terminal, bool) {
	t, ok := e.cfg.Docker.(docker.Terminal)
	return t, ok
}

// openTerm starts a terminal session and pumps its output until it ends.
func (e *Engine) openTerm(req agentprotocol.ExecOpen) {
	if req.Host {
		e.openHostShell(req)
		return
	}
	send := func(typ string, body any) bool { return e.stream.Stream(typ, req.SessionID, body) }
	fail := func(code, msg string) {
		send(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: req.SessionID, Code: code, Error: msg})
	}
	td, ok := e.terminalDocker()
	if !ok {
		fail("failed", "this agent's Docker engine does not support terminals")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctr, err := e.replicaContainer(ctx, req.ServiceID, req.Process, req.Replica)
	if err != nil {
		cancel()
		fail("not_found", noReplicaMessage(req.Process, req.Replica))
		return
	}
	argv := req.Command
	if len(argv) == 0 {
		argv = []string{"sh", "-c", autoShell}
	}
	tty, err := td.ContainerExecTTY(ctx, ctr.ID, docker.TTYSpec{
		Argv: argv, Env: []string{"TERM=" + agentprotocol.DefaultTerm, "COLORTERM=truecolor"},
		Cols: clampTerm(req.Cols, 80), Rows: clampTerm(req.Rows, 24),
	})
	if err != nil {
		cancel()
		fail("failed", "could not start a shell: "+err.Error())
		return
	}
	e.runTerm(ctx, cancel, req, tty, ctr.Name, argv, nil)
}

// runTerm registers a started terminal (a container exec or a host shell),
// tells the control plane it is open and pumps it until it ends. release, when
// set, runs exactly once after the session is over, whichever way it ended
// (the host shell's helper container is removed there).
func (e *Engine) runTerm(ctx context.Context, cancel context.CancelFunc, req agentprotocol.ExecOpen, tty docker.TTYSession, container string, argv []string, release func()) {
	send := func(typ string, body any) bool { return e.stream.Stream(typ, req.SessionID, body) }
	fail := func(code, msg string) {
		send(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: req.SessionID, Code: code, Error: msg})
	}
	s := &termSession{
		id: req.SessionID, e: e, tty: tty, ctx: ctx, cancel: cancel, gate: newCreditGate(),
		ctl: make(chan termCtl, termInputQueue), release: release,
	}
	e.term.mu.Lock()
	if e.term.sessions == nil {
		e.term.sessions = map[string]*termSession{}
	}
	_, dup := e.term.sessions[req.SessionID]
	full := len(e.term.sessions) >= maxTermSessions
	if !dup && !full {
		e.term.sessions[req.SessionID] = s
	}
	e.term.mu.Unlock()
	if dup || full {
		s.close()
		s.doRelease()
		if full {
			fail("failed", "too many terminal sessions on this server")
		} else {
			fail("failed", "session already exists")
		}
		return
	}
	if !send(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: req.SessionID, Container: container, Command: argv}) {
		s.close()
		e.endTerm(s)
		return
	}
	go s.writeLoop()
	s.pump(send)
}

// writeLoop applies input and resizes in order.
func (s *termSession) writeLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case m := <-s.ctl:
			if m.resize {
				rctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
				err := s.tty.Resize(rctx, clampTerm(m.cols, 80), clampTerm(m.rows, 24))
				cancel()
				if err != nil && s.ctx.Err() == nil {
					s.e.log.Debug("terminal resize failed", "session", s.id, "err", err)
				}
				continue
			}
			if _, err := s.tty.Write(m.input); err != nil {
				s.end(false)
				return
			}
		}
	}
}

// pump forwards terminal output under flow control, then reports the exit.
func (s *termSession) pump(send func(typ string, body any) bool) {
	buf := make([]byte, agentprotocol.ExecChunk)
	for {
		n, err := s.tty.Read(buf)
		if n > 0 {
			if s.gate.Acquire(s.ctx, n) != nil {
				break
			}
			if !send(agentprotocol.TypeExecOutput, agentprotocol.ExecOutput{SessionID: s.id, Data: append([]byte(nil), buf[:n]...)}) {
				break
			}
		}
		if err != nil {
			break
		}
	}
	s.end(false) // a no-op when the session was already ended
	if !s.byPeer {
		wctx, cancel := context.WithTimeout(context.Background(), termWaitExit)
		code, err := s.tty.Wait(wctx)
		cancel()
		exit := agentprotocol.ExecExit{SessionID: s.id, ExitCode: code}
		if err != nil {
			exit.ExitCode = -1
			exit.Error = "could not read the exit status: " + err.Error()
		}
		send(agentprotocol.TypeExecExit, exit)
	}
	_ = s.tty.Close()
	s.e.endTerm(s)
}

func (e *Engine) endTerm(s *termSession) {
	e.term.mu.Lock()
	if e.term.sessions[s.id] == s {
		delete(e.term.sessions, s.id)
	}
	e.term.mu.Unlock()
	s.doRelease()
}
