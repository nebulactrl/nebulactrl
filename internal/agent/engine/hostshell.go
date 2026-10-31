package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/hostpty"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The host shell (opt-in per server, gated by the control plane): an
// interactive shell on the server itself, as the agent's user (root).
//
//   - An agent installed on the host spawns the shell on a pseudo terminal of
//     its own.
//   - An agent that runs in a container (the dev stack, compose) cannot see the
//     host; it starts a short-lived helper container from its own image
//     (--privileged --pid=host) whose command enters PID 1's namespaces with
//     nsenter and runs a login shell there.
//
// The helper is named nebulactrl-hostshell-<sessionId8>, labelled
// nebulactrl.managed=hostshell and removed when the session ends, however it
// ends. If the agent dies mid-session, the daemon removes the helper once its
// stdin is gone (AutoRemove + StdinOnce) and sweepHostShells removes what is
// left. The session's input and output are relayed, never logged.

const (
	// hostShellReleaseTimeout bounds removing a helper container.
	hostShellReleaseTimeout = time.Minute
	// hostShellBash and hostShellSh are the host's shells, in order of preference.
	hostShellBash = "/bin/bash"
	hostShellSh   = "/bin/sh"
	// HostShellSweepInterval is how often helpers of dead sessions are removed
	// (they are also swept on every reconcile).
	HostShellSweepInterval = time.Minute
)

// nsenterShell is the helper container's command: enter every namespace of the
// host's PID 1 and run a login shell there.
var nsenterShell = []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", hostShellSh, "-l"}

// openHostShell starts a host shell session and pumps it until it ends.
func (e *Engine) openHostShell(req agentprotocol.ExecOpen) {
	send := func(typ string, body any) bool { return e.stream.Stream(typ, req.SessionID, body) }
	fail := func(msg string) {
		send(agentprotocol.TypeExecOpened, agentprotocol.ExecOpened{SessionID: req.SessionID, Code: "failed", Error: msg})
	}
	// Mark the session as starting before anything is created: the sweep must
	// not remove a helper whose session is about to be registered.
	e.term.mu.Lock()
	if e.term.starting == nil {
		e.term.starting = map[string]struct{}{}
	}
	e.term.starting[req.SessionID] = struct{}{}
	e.term.mu.Unlock()
	defer func() {
		e.term.mu.Lock()
		delete(e.term.starting, req.SessionID)
		e.term.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	tty, name, argv, release, err := e.startHostShell(ctx, req)
	if err != nil {
		cancel()
		e.log.Warn("host shell could not start", "session", req.SessionID, "err", err)
		fail("could not start a host shell: " + err.Error())
		return
	}
	e.log.Info("host shell opened", "session", req.SessionID, "mode", name)
	e.runTerm(ctx, cancel, req, tty, name, argv, release)
}

// startHostShell starts the shell and returns it with the name reported to the
// control plane (the helper container's name, or "host"), the argv that runs
// and the function that cleans up after the session.
func (e *Engine) startHostShell(ctx context.Context, req agentprotocol.ExecOpen) (docker.TTYSession, string, []string, func(), error) {
	cols, rows := clampTerm(req.Cols, 80), clampTerm(req.Rows, 24)
	self := e.selfContainer(ctx)
	if self == "" {
		return e.startDirectShell(cols, rows)
	}
	return e.startHelperShell(ctx, self, req.SessionID, cols, rows)
}

// hostShellBinary is the host's bash when it has one, else sh.
func hostShellBinary() string {
	if st, err := os.Stat(hostShellBash); err == nil && !st.IsDir() {
		return hostShellBash
	}
	return hostShellSh
}

// startDirectShell spawns the shell on a pseudo terminal of the agent itself.
func (e *Engine) startDirectShell(cols, rows int) (docker.TTYSession, string, []string, func(), error) {
	shell := hostShellBinary()
	argv := []string{shell, "-l"}
	spec := hostpty.Spec{Argv: argv, Env: hostpty.Env(agentprotocol.DefaultTerm, shell), Dir: hostpty.HomeDir(), Cols: cols, Rows: rows}
	start := e.term.hostPTY
	if start == nil {
		start = func(s hostpty.Spec) (docker.TTYSession, error) {
			sess, err := hostpty.Start(s)
			if err != nil {
				return nil, err
			}
			return sess, nil
		}
	}
	tty, err := start(spec)
	if err != nil {
		return nil, "", nil, nil, err
	}
	return tty, "host", argv, nil, nil
}

// startHelperShell runs the helper container of an agent that itself runs in
// the container self.
func (e *Engine) startHelperShell(ctx context.Context, self, sessionID string, cols, rows int) (docker.TTYSession, string, []string, func(), error) {
	runner, ok := e.cfg.Docker.(docker.HostShellRunner)
	if !ok {
		return nil, "", nil, nil, errors.New("this agent's Docker engine cannot run a helper container")
	}
	info, err := e.cfg.Docker.ContainerInspect(ctx, self)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("inspect the agent's own container: %w", err)
	}
	image := info.ImageID
	if image == "" {
		image = info.Image
	}
	if image == "" {
		return nil, "", nil, nil, errors.New("the agent's own image is unknown")
	}
	name := agentprotocol.HostShellContainerPrefix + sessionID[:min(8, len(sessionID))]
	id, tty, err := runner.RunHostShell(ctx, docker.HostShellSpec{
		Name: name, Image: image, Argv: nsenterShell, Cols: cols, Rows: rows,
		Env: []string{"TERM=" + agentprotocol.DefaultTerm, "COLORTERM=truecolor"},
		Labels: map[string]string{
			agentprotocol.LabelManaged:          agentprotocol.LabelManagedHostShell,
			agentprotocol.LabelHostShellSession: sessionID,
		},
	})
	if err != nil {
		return nil, "", nil, nil, err
	}
	release := func() { e.removeHelper(id, name) }
	return tty, name, nsenterShell, release, nil
}

// removeHelper force-removes a helper container, whatever state the session's
// context is in. A container that is already gone (the daemon removed it on
// exit) is fine.
func (e *Engine) removeHelper(id, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), hostShellReleaseTimeout)
	defer cancel()
	if err := e.cfg.Docker.ContainerRemove(ctx, id, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
		e.log.Warn("remove host shell helper", "container", name, "err", err)
	}
}

// sweepHostShells removes helper containers whose session is not running:
// those left behind when the agent died mid-session, or a session that never
// got registered.
func (e *Engine) sweepHostShells(ctx context.Context) error {
	cs, err := e.cfg.Docker.ContainerList(ctx, docker.ListFilter{
		All: true, Labels: map[string]string{agentprotocol.LabelManaged: agentprotocol.LabelManagedHostShell},
	})
	if err != nil {
		return fmt.Errorf("sweep: list host shell helpers: %w", err)
	}
	var errs []error
	for _, c := range cs {
		sid := c.Labels[agentprotocol.LabelHostShellSession]
		if e.hostSessionLive(sid) {
			continue
		}
		e.log.Info("removing orphaned host shell helper", "container", c.Name)
		if err := e.cfg.Docker.ContainerRemove(ctx, c.ID, true, false); err != nil && !errors.Is(err, docker.ErrNotFound) {
			errs = append(errs, fmt.Errorf("sweep: remove %s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}

// hostSessionLive reports whether a host shell session is running or starting.
func (e *Engine) hostSessionLive(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	e.term.mu.Lock()
	defer e.term.mu.Unlock()
	if _, ok := e.term.sessions[sessionID]; ok {
		return true
	}
	_, starting := e.term.starting[sessionID]
	return starting
}
