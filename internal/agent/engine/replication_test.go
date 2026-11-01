package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	replPW      = "Zk3nQp9xLw2VbT7yHd4sGm8cRf5aJe1u" // the replication password every test hunts for
	replRole    = "nebula_r_0a1b2c3d"
	replSlot    = "nc_r_0a1b2c3d"
	replCloneID = "cmd-clone"
)

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newReplRig is newRig with the agent's own log captured, so tests can assert
// that no secret reaches it.
func newReplRig(t *testing.T) (*rig, *syncBuf) {
	t.Helper()
	logBuf := &syncBuf{}
	r := &rig{t: t, dk: newFakeDocker(), px: &fakeProxy{}, snd: &fakeSender{}, bld: &fakeBuilder{}}
	e, err := New(Config{
		Docker: r.dk, Proxy: r.px, Builder: r.bld, Sender: r.snd, Health: healthy,
		Logger:     slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		DrainDelay: time.Millisecond, VerifyInterval: time.Millisecond,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
	})
	require.NoError(t, err)
	r.eng = e
	return r, logBuf
}

func (f *fakeDocker) ContainerSetRestartPolicy(_ context.Context, id, policy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	c.RestartMode = policy
	return nil
}

func (f *fakeDocker) createdSpecs() []docker.ContainerSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]docker.ContainerSpec(nil), f.created...)
}

func (f *fakeDocker) volumesGone() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.volsGone...)
}

// ---- db_admin repl_* ----

func replCommand(t *testing.T, action string, repl *agentprotocol.ReplAdmin, secrets map[string]string) agentprotocol.Command {
	t.Helper()
	return dbCommand(t, agentprotocol.DBAdminPayload{
		ServiceID: svcID, Action: action, Engine: "postgres", Version: "17", User: "app", Repl: repl,
	}, secrets)
}

type execLog struct {
	mu    sync.Mutex
	calls []execCall
	users []string
}

func (l *execLog) hook(answer func(argv []string, stdin string) (string, int)) func(string, []string, docker.ExecSpec, io.Writer, io.Writer) (int, error) {
	return func(id string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error) {
		var stdin string
		if spec.Stdin != nil {
			b, _ := io.ReadAll(spec.Stdin)
			stdin = string(b)
		}
		l.mu.Lock()
		l.calls = append(l.calls, execCall{id: id, argv: argv, env: spec.Env, stdin: stdin})
		l.users = append(l.users, spec.User)
		l.mu.Unlock()
		out, code := "", 0
		if answer != nil {
			out, code = answer(argv, stdin)
		}
		if code != 0 {
			_, _ = stderr.Write([]byte(out))
		} else {
			_, _ = stdout.Write([]byte(out))
		}
		return code, nil
	}
}

func (l *execLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

func replResult(t *testing.T, res agentprotocol.ResultFrame) agentprotocol.DBAdminResult {
	t.Helper()
	var out agentprotocol.DBAdminResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	return out
}

func TestReplPrepareRunsHBAThenSQLAndKeepsThePasswordOutOfEverythingButEnv(t *testing.T) {
	r, logBuf := newReplRig(t)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	var el execLog
	r.dk.onExec = el.hook(nil)

	res := r.eng.HandleCommand(context.Background(), replCommand(t, agentprotocol.DBActionReplPrepare,
		&agentprotocol.ReplAdmin{Role: replRole, MaxSlotWALKeepMB: 1024},
		map[string]string{agentprotocol.SecretDBAdminNewPassword: replPW}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)

	require.Len(t, el.calls, 2)
	hba, sql := el.calls[0], el.calls[1]
	require.Equal(t, ctr.ID, hba.id)
	require.Equal(t, []string{"sh", "-c"}, hba.argv[:2])
	require.Contains(t, hba.argv[2], "pg_hba.conf", "the script manages the pg_hba.conf block")
	require.Equal(t, "postgres", el.users[0], "the pg_hba.conf script runs as the postgres user")
	require.Contains(t, hba.env, "NC_HBA_ACTION=add")
	require.Contains(t, hba.env, "NC_HBA_LINE="+engines.HBALine(replRole))
	require.Equal(t, "psql", sql.argv[0])
	require.Contains(t, sql.stdin, "max_slot_wal_keep_size = '1024MB'")
	require.Contains(t, sql.env, "NEBULA_DB_NEW_PASSWORD="+replPW)

	for _, c := range el.calls {
		require.NotContains(t, strings.Join(c.argv, " "), replPW)
		require.NotContains(t, c.stdin, replPW)
	}
	require.NotContains(t, strings.Join(hba.env, " "), replPW)
	require.NotContains(t, string(res.Data), replPW)
	require.NotContains(t, res.Error, replPW)
	require.NotContains(t, logBuf.String(), replPW)
	require.NotContains(t, r.snd.allLogText(), replPW)
}

func TestReplPrepareFailureIsRedacted(t *testing.T) {
	r, _ := newReplRig(t)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	var el execLog
	r.dk.onExec = el.hook(func(argv []string, _ string) (string, int) {
		if argv[0] == "psql" {
			return "ERROR: could not create role with " + replPW, 3
		}
		return "", 0
	})
	res := r.eng.HandleCommand(context.Background(), replCommand(t, agentprotocol.DBActionReplPrepare,
		&agentprotocol.ReplAdmin{Role: replRole}, map[string]string{agentprotocol.SecretDBAdminNewPassword: replPW}))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "exited with code 3")
	require.NotContains(t, res.Error, replPW)
}

func TestReplActionsRunTheirPlansAndReportResults(t *testing.T) {
	const statusJSON = `{"role":"primary","inRecovery":false,"slots":[{"name":"nc_r_0a1b2c3d","active":true,"walStatus":"reserved","safeWalSize":1024}],"standbys":[]}`
	cases := []struct {
		name   string
		action string
		repl   *agentprotocol.ReplAdmin
		answer func(argv []string, stdin string) (string, int)
		// wantExecs is the number of exec calls; lastUser the exec user of the last one.
		wantExecs int
		check     func(t *testing.T, el *execLog, out agentprotocol.DBAdminResult)
	}{
		{
			name: "drop removes the hba line first, then slot and role", action: agentprotocol.DBActionReplDrop,
			repl: &agentprotocol.ReplAdmin{Role: replRole, Slot: replSlot}, wantExecs: 2,
			check: func(t *testing.T, el *execLog, _ agentprotocol.DBAdminResult) {
				require.Contains(t, el.calls[0].env, "NC_HBA_ACTION=remove")
				require.Equal(t, "postgres", el.users[0])
				require.Contains(t, el.calls[1].stdin, "pg_drop_replication_slot")
				require.Contains(t, el.calls[1].env, "NEBULA_REPL_SLOT="+replSlot)
			},
		},
		{
			name: "freeze reports the position", action: agentprotocol.DBActionReplFreeze, wantExecs: 1,
			answer: func([]string, string) (string, int) { return "3\nnebula:lsn:0/5000060\n", 0 },
			check: func(t *testing.T, el *execLog, out agentprotocol.DBAdminResult) {
				require.Equal(t, "0/5000060", out.LSN)
				require.Contains(t, el.calls[0].stdin, "default_transaction_read_only = on")
				require.Contains(t, el.calls[0].stdin, "CHECKPOINT")
			},
		},
		{
			name: "unfreeze", action: agentprotocol.DBActionReplUnfreeze, wantExecs: 1,
			check: func(t *testing.T, el *execLog, _ agentprotocol.DBAdminResult) {
				require.Contains(t, el.calls[0].stdin, "RESET default_transaction_read_only")
			},
		},
		{
			name: "promote waits for the lsn, then clears the replica configuration", action: agentprotocol.DBActionReplPromote,
			repl: &agentprotocol.ReplAdmin{LSN: "0/5000060"}, wantExecs: 2,
			answer: func(argv []string, _ string) (string, int) {
				if argv[0] == "psql" {
					return "nebula:lsn:0/5000100\n", 0
				}
				return "", 0
			},
			check: func(t *testing.T, el *execLog, out agentprotocol.DBAdminResult) {
				require.Equal(t, "0/5000100", out.LSN)
				require.Contains(t, el.calls[0].env, "NEBULA_REPL_LSN=0/5000060")
				require.Contains(t, el.calls[0].stdin, "pg_promote")
				require.Equal(t, []string{"sh", "-c", `set -eu
: > "$PGDATA/nebula.replica.conf"
rm -f "$PGDATA/standby.signal"
`}, el.calls[1].argv)
				require.Equal(t, "postgres", el.users[1])
			},
		},
		{
			name: "reconfigure rewrites the include file, then reloads", action: agentprotocol.DBActionReplReconfigure,
			repl: &agentprotocol.ReplAdmin{Role: replRole, Slot: replSlot, Host: "mesh-0a1b2c3d", Port: 15432}, wantExecs: 2,
			check: func(t *testing.T, el *execLog, _ agentprotocol.DBAdminResult) {
				require.Equal(t, []string{"sh", "-c"}, el.calls[0].argv[:2])
				require.Contains(t, el.calls[0].argv[2], "nebula.replica.conf", "the script rewrites the include file")
				require.Equal(t, "postgres", el.users[0])
				require.Contains(t, el.calls[0].env, "NC_HOST=mesh-0a1b2c3d")
				require.Contains(t, el.calls[0].env, "NC_PORT=15432")
				require.Contains(t, el.calls[1].stdin, "pg_reload_conf")
			},
		},
		{
			name: "status parses the primary side", action: agentprotocol.DBActionReplStatus, wantExecs: 1,
			answer: func([]string, string) (string, int) { return statusJSON + "\n", 0 },
			check: func(t *testing.T, el *execLog, out agentprotocol.DBAdminResult) {
				require.NotNil(t, out.Replication)
				require.Equal(t, agentprotocol.ReplRolePrimary, out.Replication.Role)
				require.Len(t, out.Replication.Slots, 1)
				require.False(t, out.Replication.CheckedAt.IsZero())
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newReplRig(t)
			r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
			var el execLog
			r.dk.onExec = el.hook(c.answer)
			res := r.eng.HandleCommand(context.Background(), replCommand(t, c.action, c.repl, nil))
			require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
			require.Len(t, el.calls, c.wantExecs)
			c.check(t, &el, replResult(t, res))
		})
	}
}

func TestReplActionsRefuseBadInputWithoutExec(t *testing.T) {
	r, _ := newReplRig(t)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	var el execLog
	r.dk.onExec = el.hook(nil)
	for name, c := range map[string]agentprotocol.Command{
		"bad role":       replCommand(t, agentprotocol.DBActionReplPrepare, &agentprotocol.ReplAdmin{Role: "x; DROP ROLE postgres"}, map[string]string{agentprotocol.SecretDBAdminNewPassword: replPW}),
		"no password":    replCommand(t, agentprotocol.DBActionReplPrepare, &agentprotocol.ReplAdmin{Role: replRole}, nil),
		"bad lsn":        replCommand(t, agentprotocol.DBActionReplPromote, &agentprotocol.ReplAdmin{LSN: "0/1'; --"}, nil),
		"unknown action": replCommand(t, "repl_nuke", nil, nil),
		"mysql": dbCommand(t, agentprotocol.DBAdminPayload{
			ServiceID: svcID, Action: agentprotocol.DBActionReplStatus, Engine: "mysql", Version: "8.4",
		}, nil),
	} {
		res := r.eng.HandleCommand(context.Background(), c)
		require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome, name)
	}
	// Only the pg_hba.conf step of "bad role"/"no password" may have run.
	for _, c := range el.calls {
		require.NotEqual(t, "psql", c.argv[0], "no SQL ran for rejected input")
	}
}

func TestReplActionNeedsARunningContainer(t *testing.T) {
	r, _ := newReplRig(t)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, false, time.Hour)
	res := r.eng.HandleCommand(context.Background(), replCommand(t, agentprotocol.DBActionReplStatus, nil, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "not running")
}

func TestExecPlanInRefusesAnyUserButPostgres(t *testing.T) {
	r, _ := newReplRig(t)
	var el execLog
	r.dk.onExec = el.hook(nil)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	for _, user := range []string{"root", "0", "postgres:root", "app"} {
		_, err := r.eng.execPlanIn(context.Background(), ctr.ID, engines.ExecPlan{Argv: []string{"id"}, User: user}, newRedactor(nil, nil), time.Second)
		require.Error(t, err, user)
	}
	require.Zero(t, el.count())
	_, err := r.eng.execPlanIn(context.Background(), ctr.ID, engines.ExecPlan{Argv: []string{"id"}, User: "postgres"}, newRedactor(nil, nil), time.Second)
	require.NoError(t, err)
	require.Equal(t, []string{"postgres"}, el.users)
}

// ---- repl_final_lsn ----

const controlDataClean = `pg_control version number:            1700
Database cluster state:               shut down
Latest checkpoint location:           0/5000028
Latest checkpoint's REDO location:    0/5000028
`

func TestReplFinalLSNReadsTheStoppedVolumeReadOnly(t *testing.T) {
	r, _ := newReplRig(t)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, false, time.Hour)
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if strings.HasPrefix(c.Name, "nc-pgctl-") {
			c.State, c.Running, c.ExitCode = "exited", false, 0
			r.dk.logText[c.Name] = controlDataClean
		}
	}
	res := r.eng.HandleCommand(context.Background(), replCommand(t, agentprotocol.DBActionReplFinalLSN,
		&agentprotocol.ReplAdmin{DataVolume: "data"}, nil))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, "0/5000028", replResult(t, res).FinalLSN)

	specs := r.dk.createdSpecs()
	require.Len(t, specs, 1)
	require.Equal(t, []string{"sh", "-c", engines.ControlDataScript}, specs[0].Entrypoint)
	require.Equal(t, []docker.VolumeMount{{Volume: agentprotocol.VolumeName(svcID, "data"), Target: "/var/lib/postgresql/data", ReadOnly: true}}, specs[0].Mounts)
	require.Equal(t, []string{"ALL"}, specs[0].CapDrop)
	require.False(t, specs[0].Privileged)
	require.Empty(t, r.dk.containersNamed("nc-pgctl-"), "helper removed")
}

func TestReplFinalLSNFailsAfterAnUncleanShutdown(t *testing.T) {
	r, _ := newReplRig(t)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, false, time.Hour)
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if strings.HasPrefix(c.Name, "nc-pgctl-") {
			c.State, c.Running, c.ExitCode = "exited", false, 0
			r.dk.logText[c.Name] = strings.Replace(controlDataClean, "shut down", "in production", 1)
		}
	}
	res := r.eng.HandleCommand(context.Background(), replCommand(t, agentprotocol.DBActionReplFinalLSN,
		&agentprotocol.ReplAdmin{DataVolume: "data"}, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "did not shut down cleanly")
}

func TestReplFinalLSNRefusesARunningDatabase(t *testing.T) {
	r, _ := newReplRig(t)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	res := r.eng.HandleCommand(context.Background(), replCommand(t, agentprotocol.DBActionReplFinalLSN,
		&agentprotocol.ReplAdmin{DataVolume: "data"}, nil))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "still running")
	require.Empty(t, r.dk.createdSpecs())
}

// ---- db_replica_clone ----

func clonePayload() agentprotocol.ReplicaClonePayload {
	return agentprotocol.ReplicaClonePayload{
		DeploymentID: depV2, ServiceID: svcID, Engine: "postgres", Version: "17",
		DataVolume: "data", Host: "mesh-0a1b2c3d", Port: 15432, User: replRole, Slot: replSlot,
		Networks: []string{agentprotocol.EnvNetworkPrefix + "0a1b2c3d", agentprotocol.EnvNetworkPrefix + "0a1b2c3d" + agentprotocol.EnvInternalSuffix},
	}
}

func cloneCommand(t *testing.T, p agentprotocol.ReplicaClonePayload, secrets map[string]string) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return agentprotocol.Command{ID: replCloneID, Kind: agentprotocol.KindDBReplicaClone, Payload: raw, Secrets: secrets}
}

func cloneSecrets() map[string]string {
	return map[string]string{agentprotocol.SecretReplPassword: replPW}
}

// cloneFinishes makes the clone helper exit with code and print lines.
func cloneFinishes(r *rig, code int, lines string) {
	r.dk.onStart = func(c *docker.ContainerInfo) {
		if strings.HasPrefix(c.Name, "nc-replclone-") {
			c.State, c.Running, c.ExitCode = "exited", false, code
			r.dk.logText[c.Name] = lines
		}
	}
}

func TestReplicaCloneRunsTheConstantScriptWithThePasswordOnlyInTheEnvironment(t *testing.T) {
	r, logBuf := newReplRig(t)
	// pg_basebackup -v does not print the password, but a misbehaving tool might.
	cloneFinishes(r, 0, "pg_basebackup: initiating base backup\nstray echo "+replPW+"\npg_basebackup: base backup completed\n")

	res := r.eng.HandleCommand(context.Background(), cloneCommand(t, clonePayload(), cloneSecrets()))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)

	specs := r.dk.createdSpecs()
	require.Len(t, specs, 1)
	cs := specs[0]
	require.Equal(t, "nc-replclone-"+agentprotocol.Short(svcID), cs.Name)
	require.Equal(t, "postgres:17", cs.Image)
	require.Equal(t, []string{"sh", "-c", engines.CloneScript()}, cs.Entrypoint)
	require.Empty(t, cs.Cmd)
	require.ElementsMatch(t, []string{
		"NC_HOST=mesh-0a1b2c3d", "NC_PORT=15432", "NC_USER=" + replRole, "NC_SLOT=" + replSlot, "PGPASSWORD=" + replPW,
	}, cs.Env)
	require.Equal(t, []docker.VolumeMount{{Volume: agentprotocol.VolumeName(svcID, "data"), Target: "/var/lib/postgresql/data"}}, cs.Mounts)
	var nets []string
	for _, n := range cs.Networks {
		nets = append(nets, n.Network)
	}
	require.Equal(t, clonePayload().Networks, nets, "the clone joins the networks the replica will use")
	require.False(t, cs.Privileged)
	require.Equal(t, []string{"ALL"}, cs.CapDrop)
	require.Empty(t, cs.PidMode)
	require.Empty(t, cs.NetworkMode)
	require.Empty(t, cs.Devices)

	require.Empty(t, r.dk.containersNamed("nc-replclone-"), "helper removed")
	require.Empty(t, r.dk.volumesGone(), "a finished clone keeps the volume")

	// The deployment receives the output as deploy log lines on its own stream.
	r.snd.mu.Lock()
	for _, f := range r.snd.logs {
		require.Equal(t, depV2, f.DeploymentID)
		require.Equal(t, agentprotocol.StreamDeploy, f.Stream)
	}
	r.snd.mu.Unlock()
	text := r.snd.allLogText()
	require.Contains(t, text, "initiating base backup")
	require.Contains(t, text, "Clone finished")

	var out agentprotocol.ReplicaCloneResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.NotEmpty(t, out.Message)

	// The secret is in the container environment and nowhere else.
	for _, s := range []string{text, logBuf.String(), string(res.Data), res.Error, strings.Join(cs.Entrypoint, " "), strings.Join(cs.Cmd, " ")} {
		require.NotContains(t, s, replPW)
	}
}

func TestReplicaCloneWithoutDeploymentSendsNoLogFrames(t *testing.T) {
	r, _ := newReplRig(t)
	cloneFinishes(r, 0, "ok\n")
	p := clonePayload()
	p.DeploymentID = ""
	res := r.eng.HandleCommand(context.Background(), cloneCommand(t, p, cloneSecrets()))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Empty(t, r.snd.allLogText())
}

func TestReplicaCloneFailureRemovesTheHalfWrittenVolumeAndRedacts(t *testing.T) {
	r, logBuf := newReplRig(t)
	cloneFinishes(r, 1, "pg_basebackup: error: connection failed for password "+replPW+"\n")
	res := r.eng.HandleCommand(context.Background(), cloneCommand(t, clonePayload(), cloneSecrets()))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "pg_basebackup failed (exit code 1)")
	require.Contains(t, res.Error, "connection failed")
	require.Equal(t, []string{agentprotocol.VolumeName(svcID, "data")}, r.dk.volumesGone())
	require.Empty(t, r.dk.containersNamed("nc-replclone-"))
	for _, s := range []string{res.Error, string(res.Data), logBuf.String(), r.snd.allLogText()} {
		require.NotContains(t, s, replPW)
	}
}

func TestReplicaCloneKeepsANonEmptyVolume(t *testing.T) {
	r, _ := newReplRig(t)
	cloneFinishes(r, cloneNotEmptyExit, "data directory is not empty\n")
	res := r.eng.HandleCommand(context.Background(), cloneCommand(t, clonePayload(), cloneSecrets()))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "not empty")
	require.Empty(t, r.dk.volumesGone(), "a volume that already held data is never deleted")
}

func TestReplicaCloneRefusesBadRequests(t *testing.T) {
	good := clonePayload()
	mut := func(f func(*agentprotocol.ReplicaClonePayload)) agentprotocol.ReplicaClonePayload {
		p := good
		p.Networks = append([]string(nil), good.Networks...)
		f(&p)
		return p
	}
	cases := map[string]struct {
		p       agentprotocol.ReplicaClonePayload
		secrets map[string]string
	}{
		"no password":     {good, nil},
		"mysql":           {mut(func(p *agentprotocol.ReplicaClonePayload) { p.Engine = "mysql"; p.Version = "8.4" }), cloneSecrets()},
		"unknown version": {mut(func(p *agentprotocol.ReplicaClonePayload) { p.Version = "9" }), cloneSecrets()},
		"bad volume":      {mut(func(p *agentprotocol.ReplicaClonePayload) { p.DataVolume = "../etc" }), cloneSecrets()},
		"bad host":        {mut(func(p *agentprotocol.ReplicaClonePayload) { p.Host = "db; reboot" }), cloneSecrets()},
		"bad role":        {mut(func(p *agentprotocol.ReplicaClonePayload) { p.User = "x'y" }), cloneSecrets()},
		"bad slot":        {mut(func(p *agentprotocol.ReplicaClonePayload) { p.Slot = "A B" }), cloneSecrets()},
		"bad port":        {mut(func(p *agentprotocol.ReplicaClonePayload) { p.Port = 0 }), cloneSecrets()},
		"no networks":     {mut(func(p *agentprotocol.ReplicaClonePayload) { p.Networks = nil }), cloneSecrets()},
		"host network":    {mut(func(p *agentprotocol.ReplicaClonePayload) { p.Networks = []string{"host"} }), cloneSecrets()},
		"foreign network": {mut(func(p *agentprotocol.ReplicaClonePayload) { p.Networks = []string{"bridge"} }), cloneSecrets()},
		"no service":      {mut(func(p *agentprotocol.ReplicaClonePayload) { p.ServiceID = "" }), cloneSecrets()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := newReplRig(t)
			res := r.eng.HandleCommand(context.Background(), cloneCommand(t, c.p, c.secrets))
			require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
			require.Empty(t, r.dk.createdSpecs(), "nothing was created")
			require.Empty(t, r.dk.volumesGone())
		})
	}
}

func TestReplicaCloneRefusesWhileTheReplicaRuns(t *testing.T) {
	r, _ := newReplRig(t)
	r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	res := r.eng.HandleCommand(context.Background(), cloneCommand(t, clonePayload(), cloneSecrets()))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "running")
	require.Empty(t, r.dk.createdSpecs())
	require.Empty(t, r.dk.volumesGone())
}

func TestReplicaCloneCancelKillsTheContainerAndDeletesTheVolume(t *testing.T) {
	r, _ := newReplRig(t)
	// The helper never exits by itself: only the cancel ends the clone.
	done := make(chan agentprotocol.ResultFrame, 1)
	go func() {
		done <- r.eng.HandleCommand(context.Background(), cloneCommand(t, clonePayload(), cloneSecrets()))
	}()
	require.Eventually(t, func() bool { return len(r.dk.containersNamed("nc-replclone-")) == 1 }, 5*time.Second, 5*time.Millisecond)

	raw, err := json.Marshal(agentprotocol.CancelDeployPayload{DeploymentID: replCloneID, RequestedBy: "alice"})
	require.NoError(t, err)
	cres := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "cancel-1", Kind: agentprotocol.KindCancelDeploy, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeSucceeded, cres.Outcome, cres.Error)

	select {
	case res := <-done:
		require.Equal(t, agentprotocol.OutcomeCanceled, res.Outcome, res.Error)
		require.Contains(t, res.Error, "half-written")
	case <-time.After(10 * time.Second):
		t.Fatal("the clone did not stop after cancel_deploy")
	}
	require.Empty(t, r.dk.containersNamed("nc-replclone-"), "helper container removed")
	require.Equal(t, []string{agentprotocol.VolumeName(svcID, "data")}, r.dk.volumesGone())
	require.Contains(t, r.snd.allLogText(), "Clone canceled")
}

func TestReplicaCloneTimeoutIsAFailureNotACancel(t *testing.T) {
	r, _ := newReplRig(t)
	p := clonePayload()
	p.TimeoutSeconds = 1
	start := time.Now()
	res := r.eng.HandleCommand(context.Background(), cloneCommand(t, p, cloneSecrets()))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "timed out")
	require.Less(t, time.Since(start), 10*time.Second)
	require.Len(t, r.dk.volumesGone(), 1)
	require.Empty(t, r.dk.containersNamed("nc-replclone-"))
}

// ---- the 30 s poll ----

const (
	pollPrimaryJSON = `{"role":"primary","inRecovery":false,"slots":[{"name":"nc_r_0a1b2c3d","active":true,"walStatus":"reserved","safeWalSize":4294967296}],` +
		`"standbys":[{"applicationName":"nc_r_0a1b2c3d","state":"streaming","syncState":"async","lagBytes":2048,"replayLagMs":12}]}`
	pollReplicaJSON = `{"role":"replica","inRecovery":true,"receiverStatus":"streaming","receiveLsn":"0/5000100","replayLsn":"0/50000F0","replayLagBytes":16}`
)

// pollRig seeds a running database container that is a desired replication member.
func pollRig(t *testing.T, role string, answer string, code int) (*rig, *execLog) {
	t.Helper()
	r, _ := newReplRig(t)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	r.eng.desired[svcID] = desiredEntry{DeploymentID: depV1, Replicas: 1, ContainerNames: []string{ctr.Name}}
	r.eng.repl.setDesired(svcID, &agentprotocol.ReplicationDesired{Role: role, User: "app", Slot: replSlot})
	el := &execLog{}
	r.dk.onExec = el.hook(func([]string, string) (string, int) { return answer, code })
	return r, el
}

func TestPollParsesThePrimarySide(t *testing.T) {
	r, el := pollRig(t, agentprotocol.ReplRolePrimary, pollPrimaryJSON+"\n", 0)
	r.eng.pollReplication(context.Background())

	st := r.eng.repl.statusOf(svcID)
	require.NotNil(t, st)
	require.Equal(t, agentprotocol.ReplRolePrimary, st.Role)
	require.False(t, st.InRecovery)
	require.Empty(t, st.Error)
	require.False(t, st.CheckedAt.IsZero())
	require.Len(t, st.Slots, 1)
	require.Equal(t, "reserved", st.Slots[0].WALStatus)
	require.True(t, st.Slots[0].Active)
	require.EqualValues(t, 4294967296, *st.Slots[0].SafeWALSize)
	require.Len(t, st.Standbys, 1)
	require.Equal(t, "nc_r_0a1b2c3d", st.Standbys[0].ApplicationName)
	require.Equal(t, "streaming", st.Standbys[0].State)
	require.EqualValues(t, 2048, st.Standbys[0].LagBytes)
	require.EqualValues(t, 12, *st.Standbys[0].ReplayLagMs)

	// One constant psql over the local socket: no password anywhere.
	require.Len(t, el.calls, 1)
	require.Equal(t, []string{"psql", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-U", "app", "-d", "postgres"}, el.calls[0].argv)
	require.Equal(t, engines.ReplStatusSQL, el.calls[0].stdin)
	require.Empty(t, el.calls[0].env)
	require.Equal(t, "", el.users[0])

	// observed carries it.
	r.eng.reportObserved(context.Background())
	require.Len(t, r.snd.observed, 1)
	got := r.snd.observed[0].Services[0]
	require.NotNil(t, got.Replication)
	require.Equal(t, st.Standbys, got.Replication.Standbys)
}

func TestPollParsesTheReplicaSide(t *testing.T) {
	r, _ := pollRig(t, agentprotocol.ReplRoleReplica, pollReplicaJSON+"\n", 0)
	r.eng.pollReplication(context.Background())
	st := r.eng.repl.statusOf(svcID)
	require.NotNil(t, st)
	require.Equal(t, agentprotocol.ReplRoleReplica, st.Role)
	require.True(t, st.InRecovery)
	require.Equal(t, "streaming", st.ReceiverStatus)
	require.Equal(t, "0/5000100", st.ReceiveLSN)
	require.Equal(t, "0/50000F0", st.ReplayLSN)
	require.EqualValues(t, 16, st.ReplayLagBytes)
	require.Empty(t, st.Slots)
}

func TestPollRecordsErrorsAsUnknownAndNeverFails(t *testing.T) {
	t.Run("psql fails", func(t *testing.T) {
		r, _ := pollRig(t, agentprotocol.ReplRolePrimary, "FATAL: the database system is starting up", 2)
		r.eng.pollReplication(context.Background())
		st := r.eng.repl.statusOf(svcID)
		require.NotNil(t, st)
		require.Equal(t, agentprotocol.ReplRoleUnknown, st.Role)
		require.Contains(t, st.Error, "exited with code 2")
	})
	t.Run("garbage output", func(t *testing.T) {
		r, _ := pollRig(t, agentprotocol.ReplRolePrimary, "not json\n", 0)
		r.eng.pollReplication(context.Background())
		st := r.eng.repl.statusOf(svcID)
		require.Equal(t, agentprotocol.ReplRoleUnknown, st.Role)
		require.NotEmpty(t, st.Error)
	})
	t.Run("container stopped", func(t *testing.T) {
		r, el := pollRig(t, agentprotocol.ReplRolePrimary, pollPrimaryJSON, 0)
		r.dk.mu.Lock()
		for _, c := range r.dk.containers {
			c.Running, c.State = false, "exited"
		}
		r.dk.mu.Unlock()
		r.eng.pollReplication(context.Background())
		st := r.eng.repl.statusOf(svcID)
		require.Equal(t, agentprotocol.ReplRoleUnknown, st.Role)
		require.Contains(t, st.Error, "not running")
		require.Zero(t, el.count())
	})
	t.Run("invalid desired user", func(t *testing.T) {
		r, el := pollRig(t, agentprotocol.ReplRolePrimary, pollPrimaryJSON, 0)
		r.eng.repl.setDesired(svcID, &agentprotocol.ReplicationDesired{Role: agentprotocol.ReplRolePrimary, User: "a b; DROP"})
		r.eng.pollReplication(context.Background())
		st := r.eng.repl.statusOf(svcID)
		require.Equal(t, agentprotocol.ReplRoleUnknown, st.Role)
		require.Zero(t, el.count(), "a bad user never reaches a command line")
	})
}

func TestPollRunsEveryThirtySecondsAndSkipsBusyServices(t *testing.T) {
	r, el := pollRig(t, agentprotocol.ReplRolePrimary, pollPrimaryJSON, 0)
	ctx := context.Background()
	r.eng.pollReplication(ctx)
	r.eng.pollReplication(ctx)
	require.Equal(t, 1, el.count(), "a member is polled once per period")

	r.eng.repl.mu.Lock()
	r.eng.repl.polled[svcID] = time.Now().Add(-ReplPollEvery - time.Second)
	r.eng.repl.mu.Unlock()
	unlock := r.eng.lockService(svcID) // a promote or deploy is working on it
	r.eng.pollReplication(ctx)
	unlock()
	require.Equal(t, 1, el.count(), "a service with a command in flight is not polled")

	r.eng.repl.mu.Lock()
	r.eng.repl.polled[svcID] = time.Now().Add(-ReplPollEvery - time.Second)
	r.eng.repl.mu.Unlock()
	r.eng.pollReplication(ctx)
	require.Equal(t, 2, el.count())
}

func TestPollHonoursThePollSecondsOfTheRequest(t *testing.T) {
	r, el := pollRig(t, agentprotocol.ReplRolePrimary, pollPrimaryJSON, 0)
	r.eng.repl.setDesired(svcID, &agentprotocol.ReplicationDesired{Role: agentprotocol.ReplRolePrimary, User: "app", PollSeconds: 1})
	r.eng.pollReplication(context.Background())
	time.Sleep(1100 * time.Millisecond)
	r.eng.pollReplication(context.Background())
	require.Equal(t, 2, el.count())
}

func TestReconcileCarriesAndForgetsTheReplicationRequest(t *testing.T) {
	r, _ := newReplRig(t)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	ds := agentprotocol.DesiredService{
		ServiceID: svcID, DeploymentID: depV1, Replicas: 1, ContainerNames: []string{ctr.Name},
		Replication: &agentprotocol.ReplicationDesired{Role: agentprotocol.ReplRoleReplica, User: "app", Slot: replSlot},
	}
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{ds}}))
	r.eng.repl.mu.Lock()
	require.Contains(t, r.eng.repl.desired, svcID)
	r.eng.repl.mu.Unlock()

	// Promoted: the control plane stops sending the request.
	ds.Replication = nil
	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{ds}}))
	r.eng.repl.mu.Lock()
	require.NotContains(t, r.eng.repl.desired, svcID)
	r.eng.repl.mu.Unlock()
	require.Nil(t, r.eng.repl.statusOf(svcID))
}

// ---- fences ----

func fencedFrame(ctr *docker.ContainerInfo, fenced bool) agentprotocol.Reconcile {
	return agentprotocol.Reconcile{Services: []agentprotocol.DesiredService{{
		ServiceID: svcID, DeploymentID: depV1, Replicas: 1, ContainerNames: []string{ctr.Name}, Fenced: fenced,
		Replication: &agentprotocol.ReplicationDesired{Role: agentprotocol.ReplRolePrimary, User: "app"},
	}}}
}

func TestFenceStopsTheDatabaseAndDisablesItsRestartPolicy(t *testing.T) {
	r, _ := newReplRig(t)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	ctr.RestartMode = "unless-stopped"

	require.NoError(t, r.eng.Reconcile(context.Background(), fencedFrame(ctr, true)))
	r.eng.bg.Wait()

	got, ok := r.dk.state(ctr.Name)
	require.True(t, ok, "the data and the container are kept")
	require.False(t, got.Running)
	require.Equal(t, "no", got.RestartMode)
	require.Equal(t, []string{ctr.Name}, r.dk.stopped)
	require.Empty(t, r.dk.started, "the reconcile never starts a fenced service")
	require.Nil(t, r.eng.repl.statusOf(svcID), "a fenced database is not polled")
	r.eng.repl.mu.Lock()
	require.NotContains(t, r.eng.repl.desired, svcID)
	r.eng.repl.mu.Unlock()
}

func TestFenceIsIdempotentAndSurvivesAnotherReconcile(t *testing.T) {
	r, _ := newReplRig(t)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	ctr.RestartMode = "unless-stopped"
	for range 3 {
		require.NoError(t, r.eng.Reconcile(context.Background(), fencedFrame(ctr, true)))
		r.eng.bg.Wait()
	}
	require.Equal(t, []string{ctr.Name}, r.dk.stopped, "stopped once")
	require.Empty(t, r.dk.started)
	require.True(t, r.eng.repl.isFenced(svcID))
}

func TestFencedServiceNeverStartsAsPrimary(t *testing.T) {
	r, _ := newReplRig(t)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	require.NoError(t, r.eng.Reconcile(context.Background(), fencedFrame(ctr, true)))
	r.eng.bg.Wait()
	before := len(r.dk.createdSpecs())

	// deploy
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "fenced")
	// restart
	raw, err := json.Marshal(agentprotocol.ServiceActionPayload{ServiceID: svcID})
	require.NoError(t, err)
	res = r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "r1", Kind: agentprotocol.KindRestartService, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "fenced")

	require.Len(t, r.dk.createdSpecs(), before, "no container was created")
	require.Empty(t, r.dk.started)
	got, _ := r.dk.state(ctr.Name)
	require.False(t, got.Running)
	require.Equal(t, "no", got.RestartMode)
}

func TestFenceEndsWhenTheControlPlaneDropsIt(t *testing.T) {
	// The only way a fence is lifted is the control plane removing it (a failed
	// switchover that restarted the old primary); the agent never un-fences itself.
	r, _ := newReplRig(t)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	require.NoError(t, r.eng.Reconcile(context.Background(), fencedFrame(ctr, true)))
	r.eng.bg.Wait()
	require.True(t, r.eng.repl.isFenced(svcID))

	require.NoError(t, r.eng.Reconcile(context.Background(), fencedFrame(ctr, false)))
	r.eng.bg.Wait()
	require.False(t, r.eng.repl.isFenced(svcID))

	raw, err := json.Marshal(agentprotocol.ServiceActionPayload{ServiceID: svcID})
	require.NoError(t, err)
	res := r.eng.HandleCommand(context.Background(), agentprotocol.Command{ID: "r2", Kind: agentprotocol.KindRestartService, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
}

// A frame is applied on arrival: a command that follows the frame sees its
// fences even while the (slow) Reconcile of the frame has not run yet, and a
// slower, older Reconcile never brings a lifted fence back.
func TestObserveFrameAppliesFencesBeforeTheReconcileRuns(t *testing.T) {
	r, _ := newReplRig(t)
	ctr := r.dk.seed("nc-pg-d1d1d1d1-0", svcID, depV1, true, time.Hour)
	ctx := context.Background()
	fenced, lifted := fencedFrame(ctr, true), fencedFrame(ctr, false)

	r.eng.ObserveFrame(ctx, fenced)
	require.True(t, r.eng.repl.isFenced(svcID), "fenced on arrival, before any Reconcile")
	require.NoError(t, r.eng.Reconcile(ctx, fenced))
	r.eng.bg.Wait()
	require.True(t, r.eng.repl.isFenced(svcID))

	// The control plane lifts the fence (rebuild as a replica) and deploys at once:
	// the lifted frame is observed first, its Reconcile has not started.
	r.eng.ObserveFrame(ctx, lifted)
	require.False(t, r.eng.repl.isFenced(svcID), "the deploy that follows must not see the fence")

	// An older, slower Reconcile of the fenced frame must not re-fence the service.
	require.NoError(t, r.eng.Reconcile(ctx, fenced))
	r.eng.bg.Wait()
	require.False(t, r.eng.repl.isFenced(svcID), "a stale frame never restores a lifted fence")
	require.NoError(t, r.eng.Reconcile(ctx, lifted))
	r.eng.bg.Wait()
	require.False(t, r.eng.repl.isFenced(svcID))
}
