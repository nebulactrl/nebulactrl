package engine

import (
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func dumpReq(id string, size int64) agentprotocol.DumpRestoreRequest {
	return agentprotocol.DumpRestoreRequest{
		RequestID: id, RestoreID: "0a1b2c3d-1111-2222-3333-444455556666",
		Target: agentprotocol.BackupTarget{ServiceID: termSvc, Engine: "postgres", Database: "app", User: "nebula"},
		Format: agentprotocol.DumpPGCustom, Size: size, Secrets: map[string]string{agentprotocol.SecretDBPassword: "pw-secret-1"},
	}
}

func dumpEvents(t *testing.T, rec *streamRec, id string) []agentprotocol.DumpRestoreEvent {
	t.Helper()
	var out []agentprotocol.DumpRestoreEvent
	for _, f := range rec.of(agentprotocol.TypeDumpRestoreEvent, id) {
		out = append(out, decode[agentprotocol.DumpRestoreEvent](t, f))
	}
	return out
}

func waitDumpEnd(t *testing.T, rec *streamRec, id string) agentprotocol.DumpRestoreEvent {
	t.Helper()
	var last agentprotocol.DumpRestoreEvent
	require.Eventually(t, func() bool {
		for _, ev := range dumpEvents(t, rec, id) {
			if ev.Error != "" || ev.Stage == agentprotocol.DumpDone {
				last = ev
				return true
			}
		}
		return false
	}, 5e9, 5e6, "no final dump restore event")
	return last
}

func TestDumpRestoreStreamsFileToTheEngineClient(t *testing.T) {
	e, d, rec := termSetup(t)
	var mu sync.Mutex
	var restored []byte
	d.onExec = func(_ string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
		if argv[0] == "pg_restore" {
			b, err := io.ReadAll(spec.Stdin)
			mu.Lock()
			restored = b
			mu.Unlock()
			_, _ = io.WriteString(stderr, "pg_restore: creating TABLE public.t pw-secret-1\n")
			return 0, err
		}
		if strings.Contains(strings.Join(argv, " "), "SELECT 1 FROM pg_database") {
			_, _ = io.WriteString(stdout, "1\n")
		}
		return 0, nil
	}
	payload := []byte("PGDMP-body-of-the-dump")
	e.HandleStream(frameOf(t, agentprotocol.TypeDumpRestoreRequest, "d1", dumpReq("d1", int64(len(payload)))))
	// The agent answers ready before it takes any data.
	require.Eventually(t, func() bool {
		for _, ev := range dumpEvents(t, rec, "d1") {
			if ev.Stage == agentprotocol.DumpReady {
				return true
			}
		}
		return false
	}, 5e9, 5e6)

	e.HandleStream(frameOf(t, agentprotocol.TypeFSData, "d1", agentprotocol.FSData{RequestID: "d1", Data: payload[:8]}))
	e.HandleStream(frameOf(t, agentprotocol.TypeFSData, "d1", agentprotocol.FSData{RequestID: "d1", Data: payload[8:], EOF: true}))
	end := waitDumpEnd(t, rec, "d1")
	require.Empty(t, end.Error)
	require.Equal(t, agentprotocol.DumpDone, end.Stage)

	mu.Lock()
	require.Equal(t, payload, restored)
	mu.Unlock()
	acks := 0
	for _, f := range rec.of(agentprotocol.TypeStreamAck, "d1") {
		acks += decode[agentprotocol.StreamAck](t, f).Bytes
	}
	require.Equal(t, len(payload), acks, "every consumed byte is credited back")

	var lines []string
	for _, ev := range dumpEvents(t, rec, "d1") {
		lines = append(lines, ev.Lines...)
	}
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "creating TABLE public.t")
	require.NotContains(t, joined, "pw-secret-1", "the password never reaches the log")
}

func TestDumpRestoreFailsWhenTheDatabaseIsNotRunning(t *testing.T) {
	e, _, rec := termSetup(t)
	req := dumpReq("d2", 5)
	req.Target.ServiceID = "99999999-9999-9999-9999-999999999999"
	e.HandleStream(frameOf(t, agentprotocol.TypeDumpRestoreRequest, "d2", req))
	end := waitDumpEnd(t, rec, "d2")
	require.Equal(t, agentprotocol.DumpErrNotRunning, end.Code)
}

func TestDumpRestoreAbortedUploadFailsAndLeavesNoTrace(t *testing.T) {
	e, d, rec := termSetup(t)
	var mu sync.Mutex
	var stmts []string
	d.onExec = func(_ string, argv []string, spec docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		if argv[0] == "pg_restore" {
			_, err := io.ReadAll(spec.Stdin) // fails: the upload is canceled
			return 1, err
		}
		mu.Lock()
		stmts = append(stmts, strings.Join(argv, " "))
		mu.Unlock()
		return 0, nil
	}
	e.HandleStream(frameOf(t, agentprotocol.TypeDumpRestoreRequest, "d3", dumpReq("d3", 100)))
	e.HandleStream(frameOf(t, agentprotocol.TypeFSData, "d3", agentprotocol.FSData{RequestID: "d3", Data: []byte("PGDMP")}))
	e.HandleStream(frameOf(t, agentprotocol.TypeFSData, "d3", agentprotocol.FSData{RequestID: "d3", Error: "canceled"}))
	end := waitDumpEnd(t, rec, "d3")
	require.NotEmpty(t, end.Error)
	mu.Lock()
	defer mu.Unlock()
	require.NotContains(t, strings.Join(stmts, "\n"), "RENAME", "an aborted upload must never swap databases")
	require.Contains(t, stmts[len(stmts)-1], "DROP DATABASE IF EXISTS \"nc_restore_")
}

func TestDumpRestoreShortFileFails(t *testing.T) {
	e, d, rec := termSetup(t)
	d.onExec = func(_ string, argv []string, spec docker.ExecSpec, _, _ io.Writer) (int, error) {
		if argv[0] == "pg_restore" {
			_, err := io.ReadAll(spec.Stdin)
			return 1, err
		}
		return 0, nil
	}
	e.HandleStream(frameOf(t, agentprotocol.TypeDumpRestoreRequest, "d4", dumpReq("d4", 100)))
	e.HandleStream(frameOf(t, agentprotocol.TypeFSData, "d4", agentprotocol.FSData{RequestID: "d4", Data: []byte("PGDMP"), EOF: true}))
	end := waitDumpEnd(t, rec, "d4")
	require.Contains(t, end.Error, "ended after 5 of 100 bytes")
}
