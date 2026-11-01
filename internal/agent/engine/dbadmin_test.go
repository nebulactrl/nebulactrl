package engine

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

type execCall struct {
	id    string
	argv  []string
	env   []string
	stdin string
}

func dbCommand(t *testing.T, p agentprotocol.DBAdminPayload, secrets map[string]string) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-db", Kind: agentprotocol.KindDBAdmin, Payload: raw, Secrets: secrets}
}

func TestDBAdminRotateRunsTypedExecWithSecretsOutOfArgv(t *testing.T) {
	r := newRig(t, nil)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	var calls []execCall
	r.dk.onExec = func(id string, argv []string, spec docker.ExecSpec, _, _ io.Writer) (int, error) {
		var stdin string
		if spec.Stdin != nil {
			b, _ := io.ReadAll(spec.Stdin)
			stdin = string(b)
		}
		calls = append(calls, execCall{id: id, argv: argv, env: spec.Env, stdin: stdin})
		return 0, nil
	}
	const newPW = "brandnewpassword12345"
	res := r.eng.HandleCommand(context.Background(), dbCommand(t, agentprotocol.DBAdminPayload{
		ServiceID: svcID, Action: agentprotocol.DBActionRotatePassword, Engine: "postgres", Version: "17", Database: "app", User: "postgres",
	}, map[string]string{
		agentprotocol.SecretDBAdminPassword: "oldpasswordoldpassword", agentprotocol.SecretDBAdminNewPassword: newPW,
	}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Len(t, calls, 1)
	require.Equal(t, ctr.ID, calls[0].id)
	require.Equal(t, "psql", calls[0].argv[0])
	require.NotContains(t, strings.Join(calls[0].argv, " "), newPW)
	require.Contains(t, calls[0].env, "NEBULA_DB_NEW_PASSWORD="+newPW)
	require.Contains(t, calls[0].stdin, "ALTER ROLE")
	require.NotContains(t, string(res.Data), newPW)
}

func TestDBAdminRotateFailureIsRedacted(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	const newPW = "brandnewpassword12345"
	r.dk.onExec = func(_ string, _ []string, _ docker.ExecSpec, _, stderr io.Writer) (int, error) {
		_, _ = stderr.Write([]byte("ERROR: could not set " + newPW))
		return 1, nil
	}
	res := r.eng.HandleCommand(context.Background(), dbCommand(t, agentprotocol.DBAdminPayload{
		ServiceID: svcID, Action: agentprotocol.DBActionRotatePassword, Engine: "postgres", Version: "17", Database: "app", User: "postgres",
	}, map[string]string{agentprotocol.SecretDBAdminNewPassword: newPW}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "exited with code 1")
	require.NotContains(t, res.Error, newPW)
}

func TestDBAdminNeedsRunningContainer(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, false, time.Hour)
	res := r.eng.HandleCommand(context.Background(), dbCommand(t, agentprotocol.DBAdminPayload{
		ServiceID: svcID, Action: agentprotocol.DBActionInfo, Engine: "valkey", Version: "9",
	}, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "not running")
}

func TestDBAdminInfoParsesOutput(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	r.dk.onExec = func(_ string, _ []string, _ docker.ExecSpec, stdout, _ io.Writer) (int, error) {
		_, _ = stdout.Write([]byte("17.6 (Debian 17.6-1)|2048|5\n"))
		return 0, nil
	}
	res := r.eng.HandleCommand(context.Background(), dbCommand(t, agentprotocol.DBAdminPayload{
		ServiceID: svcID, Action: agentprotocol.DBActionInfo, Engine: "postgres", Version: "17", Database: "app", User: "postgres",
	}, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.DBAdminResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, &agentprotocol.DBInfo{Version: "17.6", SizeBytes: 2048, Connections: 5}, out.Info)
}

func TestDBAdminRejectsUnknownAction(t *testing.T) {
	r := newRig(t, nil)
	res := r.eng.HandleCommand(context.Background(), dbCommand(t, agentprotocol.DBAdminPayload{ServiceID: svcID, Action: "drop_database", Engine: "postgres"}, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
}

func upgradeCommand(t *testing.T) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(agentprotocol.DBUpgradePayload{
		ServiceID: svcID, UpgradeID: "aaaaaaaa-0000-4000-8000-000000000009", FromVersion: "17", ToVersion: "18",
		HelperImage: "tianon/postgres-upgrade:17-to-18", OldVolume: "data", NewVolume: "data-pg18", StopGraceSeconds: 1,
	})
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-up", Kind: agentprotocol.KindDBUpgrade, Payload: raw}
}

func TestDBUpgradeStopsDatabaseAndRunsHelperOnBothVolumes(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if strings.HasPrefix(c.Name, "nc-pgupgrade-") {
			c.State, c.Running, c.ExitCode = "exited", false, 0
		}
	}
	res := r.eng.HandleCommand(context.Background(), upgradeCommand(t))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, []string{"nc-pg-d1d1d1d1-0"}, r.dk.stopped, "database stopped")
	require.NotContains(t, r.dk.started, "nc-pg-d1d1d1d1-0", "database stays stopped: the new volume is deployed through a staged change")

	require.Len(t, r.dk.created, 1)
	spec := r.dk.created[0]
	require.Equal(t, "tianon/postgres-upgrade:17-to-18", spec.Image)
	require.Equal(t, []docker.VolumeMount{
		{Volume: agentprotocol.VolumeName(svcID, "data"), Target: "/var/lib/postgresql/old"},
		{Volume: agentprotocol.VolumeName(svcID, "data-pg18"), Target: "/var/lib/postgresql/new"},
	}, spec.Mounts)
	require.Contains(t, spec.Env, "PGDATAOLD=/var/lib/postgresql/old")
	require.Contains(t, spec.Env, "PGDATANEW=/var/lib/postgresql/new/18/docker")
	require.NotContains(t, strings.Join(spec.Env, " "), "POSTGRES_INITDB_ARGS")
	require.Empty(t, r.dk.containersNamed("nc-pgupgrade-"), "helper container removed")
}

func TestDBUpgradeRetriesWithNoChecksumsWhenOldClusterHasNone(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	attempt := 0
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if !strings.HasPrefix(c.Name, "nc-pgupgrade-") {
			return
		}
		attempt++
		c.State, c.Running = "exited", false
		if attempt == 1 {
			c.ExitCode = 1
			r.dk.logText[c.Name] = "old cluster does not use data checksums but the new one does\nFailure, exiting"
		}
	}
	res := r.eng.HandleCommand(context.Background(), upgradeCommand(t))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Len(t, r.dk.created, 2)
	require.Contains(t, r.dk.created[1].Env, "POSTGRES_INITDB_ARGS=--no-data-checksums")
}

func TestDBUpgradeFailureRestartsTheOldDatabase(t *testing.T) {
	r := newRig(t, nil)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if strings.HasPrefix(c.Name, "nc-pgupgrade-") {
			c.State, c.Running, c.ExitCode = "exited", false, 1
			r.dk.logText[c.Name] = "some other pg_upgrade failure"
		}
	}
	res := r.eng.HandleCommand(context.Background(), upgradeCommand(t))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, r.dk.started, "nc-pg-d1d1d1d1-0", "the old database is started again")
	var out agentprotocol.DBUpgradeResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Contains(t, out.Log, "some other pg_upgrade failure")
	require.Len(t, r.dk.created, 1, "no retry without the checksum message")
}

func TestDBUpgradeRejectsUnexpectedHelperImage(t *testing.T) {
	r := newRig(t, nil)
	raw, _ := json.Marshal(agentprotocol.DBUpgradePayload{
		ServiceID: svcID, UpgradeID: "u", FromVersion: "17", ToVersion: "18", HelperImage: "evil/image:1", OldVolume: "data", NewVolume: "data2",
	})
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "x", Kind: agentprotocol.KindDBUpgrade, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Empty(t, r.dk.created)
}

// containersNamed lists the fake containers whose name starts with prefix.
func (f *fakeDocker) containersNamed(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.containers {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c.Name)
		}
	}
	return out
}
