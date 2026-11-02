package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/volbackup"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// errReader yields data, then fails with err.
type errReader struct {
	data []byte
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

// pumpRecorder collects what pumpVolFile sends.
type pumpRecorder struct {
	mu      sync.Mutex
	frames  []any
	types   []string
	resp    []agentprotocol.FSResponse
	sendOK  func(n int) bool // n is the 1-based send; nil = always true
	nSent   int
	dataLen int
}

func (p *pumpRecorder) send(typ string, body any) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nSent++
	p.types = append(p.types, typ)
	p.frames = append(p.frames, body)
	if d, ok := body.(agentprotocol.FSData); ok {
		p.dataLen += len(d.Data)
	}
	return p.sendOK == nil || p.sendOK(p.nSent)
}

func (p *pumpRecorder) respond(r agentprotocol.FSResponse) {
	p.mu.Lock()
	p.resp = append(p.resp, r)
	p.mu.Unlock()
}

func (p *pumpRecorder) datas() []agentprotocol.FSData {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []agentprotocol.FSData
	for _, f := range p.frames {
		if d, ok := f.(agentprotocol.FSData); ok {
			out = append(out, d)
		}
	}
	return out
}

func TestPumpVolFileStreamsReadyDataAndEOF(t *testing.T) {
	r := newRig(t, nil)
	rec := &pumpRecorder{}
	entry := agentprotocol.FSEntry{Name: "a.txt", Size: 5}
	req := agentprotocol.VolFileRequest{RequestID: "rq1", Op: agentprotocol.VolFileRead, Path: "/a.txt"}

	r.eng.pumpVolFile(context.Background(), req, entry, strings.NewReader("hello"), rec.send, rec.respond)

	require.Len(t, rec.resp, 1)
	require.Equal(t, agentprotocol.FSReady, rec.resp[0].Stage)
	require.Equal(t, "/a.txt", rec.resp[0].Path, "the request path is reported")
	require.Equal(t, &entry, rec.resp[0].Entry)

	ds := rec.datas()
	require.Len(t, ds, 2)
	require.Equal(t, agentprotocol.FSData{RequestID: "rq1", Data: []byte("hello")}, ds[0])
	require.Equal(t, agentprotocol.FSData{RequestID: "rq1", EOF: true}, ds[1])

	r.eng.term.mu.Lock()
	defer r.eng.term.mu.Unlock()
	require.Empty(t, r.eng.term.gates, "the credit gate is unregistered when the stream ends")
}

func TestPumpVolFileRegistersItsGateForAcks(t *testing.T) {
	r := newRig(t, nil)
	rec := &pumpRecorder{}
	// Hold the pump inside the read so the gate can be observed.
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		r.eng.pumpVolFile(context.Background(), agentprotocol.VolFileRequest{RequestID: "rq2", Op: agentprotocol.VolFileRead, Path: "/f"}, agentprotocol.FSEntry{}, pr, rec.send, rec.respond)
		close(done)
	}()
	require.Eventually(t, func() bool { return r.eng.term.gate("rq2") != nil }, 2*time.Second, time.Millisecond)
	require.NoError(t, pw.Close())
	<-done
	require.Nil(t, r.eng.term.gate("rq2"))
}

func TestPumpVolFileReportsReadErrors(t *testing.T) {
	r := newRig(t, nil)
	rec := &pumpRecorder{}
	rd := &errReader{data: []byte("partial"), err: errors.New("repository unreachable")}

	r.eng.pumpVolFile(context.Background(), agentprotocol.VolFileRequest{RequestID: "rq3", Path: "/f"}, agentprotocol.FSEntry{}, rd, rec.send, rec.respond)

	ds := rec.datas()
	require.Len(t, ds, 2)
	require.Equal(t, []byte("partial"), ds[0].Data)
	require.Equal(t, "read backup: repository unreachable", ds[1].Error)
	require.False(t, ds[1].EOF, "a failed read must not look like a complete file")
}

func TestPumpVolFileStopsWhenTheStreamIsGone(t *testing.T) {
	r := newRig(t, nil)
	rec := &pumpRecorder{sendOK: func(n int) bool { return n < 1 }} // the first data frame is refused
	rd := &errReader{data: []byte("x"), err: io.EOF}

	r.eng.pumpVolFile(context.Background(), agentprotocol.VolFileRequest{RequestID: "rq4", Path: "/f"}, agentprotocol.FSEntry{}, rd, rec.send, rec.respond)

	require.Len(t, rec.datas(), 1, "no EOF is sent into a closed stream")
	require.False(t, rec.datas()[0].EOF)
}

func TestPumpVolFileStopsWhenTheWindowStaysFull(t *testing.T) {
	r := newRig(t, nil)
	rec := &pumpRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	// More than the window in one go: the second chunk waits for an ack that never comes.
	big := make([]byte, agentprotocol.StreamWindow+agentprotocol.FSChunk)
	done := make(chan struct{})
	go func() {
		r.eng.pumpVolFile(ctx, agentprotocol.VolFileRequest{RequestID: "rq5", Path: "/f"}, agentprotocol.FSEntry{}, &errReader{data: big, err: io.EOF}, rec.send, rec.respond)
		close(done)
	}()
	require.Eventually(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.dataLen >= agentprotocol.StreamWindow
	}, 2*time.Second, time.Millisecond)
	cancel()
	<-done
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Less(t, rec.dataLen, len(big), "the remainder is never sent once the operation is cancelled")
	for _, d := range rec.frames {
		if f, ok := d.(agentprotocol.FSData); ok {
			require.False(t, f.EOF)
		}
	}
}

func volFileReq(id, op, path string) agentprotocol.VolFileRequest {
	return agentprotocol.VolFileRequest{
		RequestID: id, Op: op, ServiceID: svcID, SnapshotID: strings.Repeat("a", 64), Volume: "data", Path: path,
	}
}

func TestVolFileRejectsUnknownOperationAndBadPaths(t *testing.T) {
	e, _, rec := termSetup(t)

	e.HandleStream(frameOf(t, agentprotocol.TypeVolFileRequest, "v1", volFileReq("v1", "chmod", "/")))
	got := decode[agentprotocol.FSResponse](t, rec.waitFor(t, agentprotocol.TypeFSResponse, "v1", 1)[0])
	require.Equal(t, "v1", got.RequestID)
	require.Equal(t, agentprotocol.FSErrFailed, got.Code)
	require.Equal(t, "unknown backup file operation chmod", got.Error)

	// A path that leaves the volume never reaches restic: the typed file error
	// becomes the response's code and message.
	e.HandleStream(frameOf(t, agentprotocol.TypeVolFileRequest, "v2", volFileReq("v2", agentprotocol.VolFileList, "/a/../../etc")))
	got = decode[agentprotocol.FSResponse](t, rec.waitFor(t, agentprotocol.TypeFSResponse, "v2", 1)[0])
	require.Equal(t, agentprotocol.FSErrFailed, got.Code)
	require.NotEmpty(t, got.Error)
	require.Empty(t, got.Entries)

	e.HandleStream(frameOf(t, agentprotocol.TypeVolFileRequest, "v3", volFileReq("v3", agentprotocol.VolFileRead, "relative/path")))
	got = decode[agentprotocol.FSResponse](t, rec.waitFor(t, agentprotocol.TypeFSResponse, "v3", 1)[0])
	require.Equal(t, agentprotocol.FSErrFailed, got.Code)
	require.NotEmpty(t, got.Error)

	// The operation's cancel handle is released once it ends.
	require.Eventually(t, func() bool {
		e.term.mu.Lock()
		defer e.term.mu.Unlock()
		return len(e.term.cancels) == 0
	}, 2*time.Second, time.Millisecond)
}

func TestVolFileFailureWithPlainErrorKeepsItsMessage(t *testing.T) {
	// A list that cannot start its helper (no repository credentials) fails with
	// the runner's own message, as a plain failure.
	e, _, rec := termSetup(t)
	req := volFileReq("v4", agentprotocol.VolFileList, "/")
	e.HandleStream(frameOf(t, agentprotocol.TypeVolFileRequest, "v4", req))
	got := decode[agentprotocol.FSResponse](t, rec.waitFor(t, agentprotocol.TypeFSResponse, "v4", 1)[0])
	require.Equal(t, agentprotocol.FSErrFailed, got.Code)
	require.NotEmpty(t, got.Error)
	require.Empty(t, got.Stage, "a failure is not a ready/done stage")
}

func TestExecCloseCancelsARunningVolFile(t *testing.T) {
	e, _, _ := termSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.term.mu.Lock()
	e.term.cancels = map[string]context.CancelFunc{"v9": cancel}
	e.term.mu.Unlock()

	e.HandleStream(frameOf(t, agentprotocol.TypeExecClose, "v9", agentprotocol.ExecClose{SessionID: "v9"}))
	require.ErrorIs(t, ctx.Err(), context.Canceled)

	// An unknown ID is ignored.
	e.term.cancelOp("nobody")
}

func TestVolumeCommandsRejectMalformedPayloads(t *testing.T) {
	r := newRig(t, nil)
	for kind, want := range map[string]string{
		agentprotocol.KindVolumeBackup:  "invalid volume_backup payload",
		agentprotocol.KindVolumeForget:  "invalid volume_forget payload",
		agentprotocol.KindVolumeRestore: "invalid volume_restore payload",
		agentprotocol.KindVolumeRemove:  "invalid volume_remove payload",
	} {
		res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "c-" + kind, Kind: kind, Payload: json.RawMessage(`{"serviceId": 7}`)})
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, kind)
		require.Contains(t, res.Error, want, kind)
		require.Equal(t, "c-"+kind, res.CommandID)
	}
}

func TestVolumeRemoveCommandRemovesOnlyNebulaVolumes(t *testing.T) {
	r := newRig(t, nil)
	payload, _ := json.Marshal(agentprotocol.VolumeRemovePayload{
		ServiceID: svcID,
		Names:     []string{agentprotocol.VolumePrefix + "abc-data", "postgres_data", agentprotocol.VolumePrefix + "x/../y"},
	})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "rm-1", Kind: agentprotocol.KindVolumeRemove, Payload: payload})

	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, `refusing to remove "postgres_data": not a NebulaCtrl volume`)
	require.Contains(t, res.Error, "x/../y")
	var out agentprotocol.VolumeRemoveResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, agentprotocol.OutcomeFailed, out.Outcome)

	// Only valid names: the command succeeds and the volume is gone (a missing one is fine).
	r.dk.volsGone = nil
	payload, _ = json.Marshal(agentprotocol.VolumeRemovePayload{ServiceID: svcID, Names: []string{agentprotocol.VolumePrefix + "abc-data"}})
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "rm-2", Kind: agentprotocol.KindVolumeRemove, Payload: payload})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, "removed 1 volume(s)", out.Message)
}

func TestVolumeForgetAndRestoreFailuresAreReportedAsFailedResults(t *testing.T) {
	r := newRig(t, nil)
	// Without the repository password the runner refuses before touching docker.
	forget, _ := json.Marshal(agentprotocol.VolumeForgetPayload{ServiceID: svcID})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "f1", Kind: agentprotocol.KindVolumeForget, Payload: forget})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.NotEmpty(t, res.Error)
	require.Equal(t, "f1", res.CommandID)

	restore, _ := json.Marshal(agentprotocol.VolumeRestorePayload{ServiceID: svcID, RestoreID: "rs-1"})
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "r1", Kind: agentprotocol.KindVolumeRestore, Payload: restore})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.NotEmpty(t, res.Error)
}

func TestVolHostServiceContainerPrefersARunningReplica(t *testing.T) {
	r := newRig(t, nil)
	h := volHost{r.eng}

	_, ok := h.ServiceContainer(context.Background(), svcID)
	require.False(t, ok, "no container of the service")

	stopped := r.dk.seed("nc-web-d1d1d1d1-0", svcID, depV1, false, time.Hour)
	got, ok := h.ServiceContainer(context.Background(), svcID)
	require.True(t, ok)
	require.Equal(t, stopped.Name, got.Name, "a stopped replica is better than none")

	run := r.dk.seed("nc-web-d1d1d1d1-1", svcID, depV1, true, time.Hour)
	got, ok = h.ServiceContainer(context.Background(), svcID)
	require.True(t, ok)
	require.Equal(t, run.Name, got.Name, "the running replica wins")

	// A container of another service is never offered.
	_, ok = h.ServiceContainer(context.Background(), "other-service")
	require.False(t, ok)
}

func TestVolHostServiceContainerSkipsUnmanagedContainers(t *testing.T) {
	r := newRig(t, nil)
	c := r.dk.seed(oldCtr, svcID, depV1, true, time.Hour)
	c.Labels[agentprotocol.LabelManaged] = "false"
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV1, Replicas: 1, ContainerNames: []string{oldCtr}}

	_, ok := volHost{r.eng}.ServiceContainer(context.Background(), svcID)
	require.False(t, ok, "only managed containers of the service can be frozen")
}

func TestVolHostLockServiceHonorsContextCancel(t *testing.T) {
	r := newRig(t, nil)
	held := r.eng.lockService(svcID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := volHost{r.eng}.LockService(ctx, svcID, time.Minute)
	require.ErrorIs(t, err, context.Canceled, "a cancelled backup is not reported as busy")
	require.NotErrorIs(t, err, volbackup.ErrBusy)
	held()
	require.Eventually(t, func() bool { return !r.eng.isInflight(svcID) }, 2*time.Second, time.Millisecond)
}
