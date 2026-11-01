package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/hostpty"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	hostSid  = "abcdef12-0000-4000-8000-000000000001"
	hostName = "nebulactrl-hostshell-abcdef12"
	selfName = "nebulactrl-agent1"
)

// helperDocker adds docker.HostShellRunner to the terminal fake: the helper
// container appears in the fake daemon like a real one.
type helperDocker struct {
	*termFake
	hmu     sync.Mutex
	specs   []docker.HostShellSpec
	runErr  error
	nextTTY *fakeTTY
	ttys    []*fakeTTY
}

func (d *helperDocker) RunHostShell(_ context.Context, spec docker.HostShellSpec) (string, docker.TTYSession, error) {
	d.hmu.Lock()
	defer d.hmu.Unlock()
	d.specs = append(d.specs, spec)
	if d.runErr != nil {
		return "", nil, d.runErr
	}
	d.fakeDocker.mu.Lock()
	d.fakeDocker.seq++
	id := fmt.Sprintf("helper%03d", d.fakeDocker.seq)
	d.fakeDocker.containers[id] = &docker.ContainerInfo{
		ID: id, Name: spec.Name, Image: spec.Image, Labels: spec.Labels, State: "running", Running: true, Created: time.Now(),
	}
	d.fakeDocker.mu.Unlock()
	t := d.nextTTY
	if t == nil {
		t = newFakeTTY(true)
	}
	d.nextTTY = nil
	d.ttys = append(d.ttys, t)
	return id, t, nil
}

func (d *helperDocker) helperTTY(i int) *fakeTTY {
	d.hmu.Lock()
	defer d.hmu.Unlock()
	return d.ttys[i]
}

func (d *helperDocker) spec(i int) docker.HostShellSpec {
	d.hmu.Lock()
	defer d.hmu.Unlock()
	return d.specs[i]
}

// hostSetup builds an engine whose agent runs in the container selfName (a
// helper is used) or, when inContainer is false, on the bare host (a pty is used).
func hostSetup(t *testing.T, inContainer bool) (*Engine, *helperDocker, *streamRec) {
	t.Helper()
	d := &helperDocker{termFake: &termFake{fakeDocker: newFakeDocker(), stats: map[string]docker.PathStat{}, archive: map[string][]byte{}}}
	self := d.seed(selfName, "", "", true, time.Hour)
	self.Labels = map[string]string{}
	self.Image, self.ImageID = "nebula-agent1", "sha256:agentimage"
	rec := &streamRec{fakeSender: &fakeSender{}}
	cfg := Config{Docker: d, Proxy: &fakeProxy{}, Sender: rec}
	if inContainer {
		cfg.AgentContainer = selfName
	}
	e, err := New(cfg)
	require.NoError(t, err)
	return e, d, rec
}

func openHost(t *testing.T, e *Engine, rec *streamRec, sid string) agentprotocol.ExecOpened {
	t.Helper()
	e.HandleStream(frameOf(t, agentprotocol.TypeExecOpen, sid, agentprotocol.ExecOpen{SessionID: sid, Host: true, Cols: 100, Rows: 30, Command: []string{"rm", "-rf", "/"}}))
	return decode[agentprotocol.ExecOpened](t, rec.waitFor(t, agentprotocol.TypeExecOpened, sid, 1)[0])
}

func (d *helperDocker) exists(name string) bool {
	_, ok := d.state(name)
	return ok
}

func TestHostShellOnTheBareHostRunsOnAPtyOfItsOwn(t *testing.T) {
	e, d, rec := hostSetup(t, false)
	var got hostpty.Spec
	tty := newFakeTTY(true)
	e.term.hostPTY = func(s hostpty.Spec) (docker.TTYSession, error) { got = s; return tty, nil }

	opened := openHost(t, e, rec, hostSid)
	require.Empty(t, opened.Error)
	require.Equal(t, "host", opened.Container)
	require.Contains(t, []string{hostShellBash, hostShellSh}, got.Argv[0])
	require.Equal(t, []string{got.Argv[0], "-l"}, opened.Command, "a login shell; a client-chosen command is ignored")
	require.Equal(t, 100, got.Cols)
	require.Equal(t, 30, got.Rows)
	env := strings.Join(got.Env, "\n")
	require.Contains(t, env, "TERM=xterm-256color")
	require.Empty(t, d.created, "no helper container on the bare host")
	require.Empty(t, d.specs)

	e.HandleStream(frameOf(t, agentprotocol.TypeExecInput, hostSid, agentprotocol.ExecInput{SessionID: hostSid, Data: []byte("uname -n\n")}))
	out := decode[agentprotocol.ExecOutput](t, rec.waitFor(t, agentprotocol.TypeExecOutput, hostSid, 1)[0])
	require.Equal(t, "echo:uname -n\n", string(out.Data))
	e.HandleStream(frameOf(t, agentprotocol.TypeExecResize, hostSid, agentprotocol.ExecResize{SessionID: hostSid, Cols: 140, Rows: 50}))
	require.Eventually(t, func() bool { return len(tty.sizes()) == 1 }, 2*time.Second, 5*time.Millisecond)

	e.HandleStream(frameOf(t, agentprotocol.TypeExecClose, hostSid, agentprotocol.ExecClose{SessionID: hostSid}))
	require.Eventually(t, tty.isClosed, 2*time.Second, 5*time.Millisecond, "closing the session ends the shell")
	require.Eventually(t, func() bool { return e.term.session(hostSid) == nil }, 2*time.Second, 5*time.Millisecond)
}

func TestHostShellStartFailureIsReported(t *testing.T) {
	e, _, rec := hostSetup(t, false)
	e.term.hostPTY = func(hostpty.Spec) (docker.TTYSession, error) { return nil, errors.New("no ptys left") }
	opened := openHost(t, e, rec, hostSid)
	require.Equal(t, "failed", opened.Code)
	require.Contains(t, opened.Error, "no ptys left")
	require.Nil(t, e.term.session(hostSid))
	require.False(t, e.hostSessionLive(hostSid), "a failed start is not left marked as starting")
}

func TestHostShellInAContainerUsesAPrivilegedNsenterHelper(t *testing.T) {
	e, d, rec := hostSetup(t, true)
	opened := openHost(t, e, rec, hostSid)
	require.Empty(t, opened.Error)
	require.Equal(t, hostName, opened.Container)
	require.Equal(t, nsenterShell, opened.Command)
	require.Equal(t, []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--", "/bin/sh", "-l"}, opened.Command)

	spec := d.spec(0)
	require.Equal(t, hostName, spec.Name)
	require.Equal(t, "sha256:agentimage", spec.Image, "the helper is the agent's own image")
	require.Equal(t, agentprotocol.LabelManagedHostShell, spec.Labels[agentprotocol.LabelManaged])
	require.Equal(t, hostSid, spec.Labels[agentprotocol.LabelHostShellSession])
	require.Contains(t, spec.Env, "TERM=xterm-256color")
	require.Equal(t, 100, spec.Cols)
	require.True(t, d.exists(hostName))

	e.HandleStream(frameOf(t, agentprotocol.TypeExecInput, hostSid, agentprotocol.ExecInput{SessionID: hostSid, Data: []byte("cat /etc/hostname\n")}))
	out := decode[agentprotocol.ExecOutput](t, rec.waitFor(t, agentprotocol.TypeExecOutput, hostSid, 1)[0])
	require.Equal(t, "echo:cat /etc/hostname\n", string(out.Data))
}

func TestHostShellHelperIsRemovedWhenTheSessionEnds(t *testing.T) {
	cases := map[string]func(t *testing.T, e *Engine, d *helperDocker, rec *streamRec){
		"control plane closes it": func(t *testing.T, e *Engine, _ *helperDocker, _ *streamRec) {
			e.HandleStream(frameOf(t, agentprotocol.TypeExecClose, hostSid, agentprotocol.ExecClose{SessionID: hostSid}))
		},
		"the shell exits": func(t *testing.T, e *Engine, d *helperDocker, rec *streamRec) {
			d.helperTTY(0).exitCode = 4
			_ = d.helperTTY(0).Close()
			exit := decode[agentprotocol.ExecExit](t, rec.waitFor(t, agentprotocol.TypeExecExit, hostSid, 1)[0])
			require.Equal(t, 4, exit.ExitCode)
		},
		"the connection drops": func(t *testing.T, e *Engine, _ *helperDocker, _ *streamRec) {
			e.Disconnected()
		},
	}
	for name, end := range cases {
		t.Run(name, func(t *testing.T) {
			e, d, rec := hostSetup(t, true)
			require.Empty(t, openHost(t, e, rec, hostSid).Error)
			require.True(t, d.exists(hostName))
			end(t, e, d, rec)
			require.Eventually(t, func() bool { return !d.exists(hostName) }, 3*time.Second, 5*time.Millisecond, "the helper container is removed")
			require.Eventually(t, func() bool { return e.term.session(hostSid) == nil }, 2*time.Second, 5*time.Millisecond)
			require.Contains(t, d.removed, hostName)
			require.True(t, d.helperTTY(0).isClosed())
		})
	}
}

func TestHostShellHelperFailureLeavesNothingBehind(t *testing.T) {
	e, d, rec := hostSetup(t, true)
	d.runErr = errors.New("cannot start privileged container")
	opened := openHost(t, e, rec, hostSid)
	require.Equal(t, "failed", opened.Code)
	require.Contains(t, opened.Error, "cannot start privileged container")
	require.False(t, d.exists(hostName))
	require.False(t, e.hostSessionLive(hostSid))
}

func TestHostShellDuplicateSessionIsRefused(t *testing.T) {
	e, d, rec := hostSetup(t, true)
	require.Empty(t, openHost(t, e, rec, hostSid).Error)
	// The same session ID again: the second session is refused, its helper
	// removed again, and the first session stays intact.
	e.HandleStream(frameOf(t, agentprotocol.TypeExecOpen, hostSid, agentprotocol.ExecOpen{SessionID: hostSid, Host: true, Cols: 80, Rows: 24}))
	require.Eventually(t, func() bool { return len(rec.of(agentprotocol.TypeExecOpened, hostSid)) == 2 }, 2*time.Second, 5*time.Millisecond)
	second := decode[agentprotocol.ExecOpened](t, rec.of(agentprotocol.TypeExecOpened, hostSid)[1])
	require.Equal(t, "failed", second.Code)
	require.NotNil(t, e.term.session(hostSid))
	require.False(t, d.helperTTY(0).isClosed(), "the first session is untouched")
	require.True(t, d.exists(hostName))
}

func seedHelper(d *helperDocker, sid string, age time.Duration) string {
	name := agentprotocol.HostShellContainerPrefix + sid[:8]
	c := d.seed(name, "", "", true, age)
	c.Labels = map[string]string{
		agentprotocol.LabelManaged:          agentprotocol.LabelManagedHostShell,
		agentprotocol.LabelHostShellSession: sid,
	}
	return name
}

func TestSweepRemovesHelpersOfDeadSessionsOnly(t *testing.T) {
	e, d, rec := hostSetup(t, true)
	require.Empty(t, openHost(t, e, rec, hostSid).Error) // a live session and its helper

	const deadSid = "deadbeef-0000-4000-8000-000000000002"
	const startingSid = "5ca1ab1e-0000-4000-8000-000000000003"
	dead := seedHelper(d, deadSid, time.Minute)
	starting := seedHelper(d, startingSid, time.Second)
	e.term.mu.Lock()
	e.term.starting = map[string]struct{}{startingSid: {}}
	e.term.mu.Unlock()
	// Neither a service container nor the agent itself is a helper.
	service := d.seed("nc-web-22222222-0", termSvc, termDep, true, time.Hour)

	require.NoError(t, e.sweepHostShells(context.Background()))
	require.False(t, d.exists(dead), "no session: the helper goes")
	require.True(t, d.exists(starting), "a session that is starting is left alone")
	require.True(t, d.exists(hostName), "a live session keeps its helper")
	require.True(t, d.exists(service.Name))
	require.True(t, d.exists(selfName))
}

func TestAgentRestartMidSessionLeavesAHelperTheNextAgentSweeps(t *testing.T) {
	// The first agent opens a session and dies with it: the helper stays.
	e1, d, rec := hostSetup(t, true)
	require.Empty(t, openHost(t, e1, rec, hostSid).Error)
	require.True(t, d.exists(hostName))

	// A new agent process (empty session table) on the same daemon.
	e2, err := New(Config{Docker: d, Proxy: &fakeProxy{}, Sender: &streamRec{fakeSender: &fakeSender{}}, AgentContainer: selfName})
	require.NoError(t, err)
	require.False(t, e2.hostSessionLive(hostSid))

	// Its first reconcile (which follows every connect) removes the leftover.
	require.NoError(t, e2.Reconcile(context.Background(), agentprotocol.Reconcile{}))
	require.False(t, d.exists(hostName), "the orphaned helper is removed by the sweep inside Reconcile")
	require.True(t, d.exists(selfName))
}

func TestHostShellWithoutAHelperRunnerFails(t *testing.T) {
	d := &termFake{fakeDocker: newFakeDocker(), stats: map[string]docker.PathStat{}, archive: map[string][]byte{}}
	self := d.seed(selfName, "", "", true, time.Hour)
	self.Image = "nebula-agent1"
	rec := &streamRec{fakeSender: &fakeSender{}}
	e, err := New(Config{Docker: d, Proxy: &fakeProxy{}, Sender: rec, AgentContainer: selfName})
	require.NoError(t, err)
	opened := openHost(t, e, rec, hostSid)
	require.Equal(t, "failed", opened.Code)
	require.Contains(t, opened.Error, "helper container")
}
