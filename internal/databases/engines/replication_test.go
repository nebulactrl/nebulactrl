package engines

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "replication", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run go test -update")
	require.Equal(t, string(want), got)
}

func renderPlan(p ExecPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "argv: %q\n", p.Argv)
	fmt.Fprintf(&b, "env: %q\n", p.Env)
	fmt.Fprintf(&b, "user: %q\n", p.User)
	b.WriteString("stdin:\n" + p.Stdin)
	return b.String()
}

func pgSpec() Spec { return Spec{Engine: Postgres, Version: "17", Username: "app"} }

func TestReplicationGoldens(t *testing.T) {
	golden(t, "clone_script", CloneScript())
	golden(t, "reconfigure_script", ReconfigureScript())
	golden(t, "hba_script", HBAScript())
	golden(t, "replica_start_command", strings.Join(ReplicaStartCommand(), "\n")+"\n")
	golden(t, "hba_block", HBABlock("nebula_r_0a1b2c3d"))
	golden(t, "status_sql", ReplStatusSQL)
	golden(t, "control_data_script", ControlDataScript+"\n")

	args := ReplArgs{Role: "nebula_r_0a1b2c3d", Slot: "nc_r_0a1b2c3d", MaxSlotWALKeepMB: 4096,
		LSN: "0/3000060", Host: "mesh-0a1b2c3d", Port: 15432, NewPassword: "abcdefghijklmnopqrstuvwxyz012345"}
	for _, action := range []string{
		agentprotocol.DBActionReplPrepare, agentprotocol.DBActionReplDrop, agentprotocol.DBActionReplFreeze,
		agentprotocol.DBActionReplUnfreeze, agentprotocol.DBActionReplPromote, agentprotocol.DBActionReplReconfigure,
		agentprotocol.DBActionReplStatus, agentprotocol.DBActionReplFinalLSN,
	} {
		p, err := ReplPlan(pgSpec(), action, args)
		require.NoError(t, err, action)
		golden(t, "plan_"+action, renderPlan(p))
	}
	add, err := HBAPlan(pgSpec(), true, args.Role)
	require.NoError(t, err)
	golden(t, "plan_hba_add", renderPlan(add))
	rm, err := HBAPlan(pgSpec(), false, args.Role)
	require.NoError(t, err)
	golden(t, "plan_hba_remove", renderPlan(rm))
	rc, err := ReconfigurePlan(pgSpec(), args)
	require.NoError(t, err)
	golden(t, "plan_reconfigure_file", renderPlan(rc))
	pc, err := PromoteCleanupPlan(pgSpec())
	require.NoError(t, err)
	golden(t, "plan_promote_cleanup", renderPlan(pc))
	env, err := CloneEnv("mesh-0a1b2c3d", 15432, args.Role, args.Slot, "pw-pw-pw-pw-pw-pw")
	require.NoError(t, err)
	golden(t, "clone_env", strings.Join(env, "\n")+"\n")
}

func TestReplPlanRejectsBadInput(t *testing.T) {
	good := ReplArgs{Role: "nebula_r_0a1b2c3d", Slot: "nc_r_0a1b2c3d", NewPassword: "abcdefghijklmnopqrstuvwxyz012345", Host: "db", Port: 5432}
	cases := map[string]struct {
		spec   Spec
		action string
		mut    func(*ReplArgs)
	}{
		"mysql":          {Spec{Engine: MySQL, Version: "8.4"}, agentprotocol.DBActionReplStatus, nil},
		"bad role":       {pgSpec(), agentprotocol.DBActionReplPrepare, func(a *ReplArgs) { a.Role = "x; DROP" }},
		"short password": {pgSpec(), agentprotocol.DBActionReplPrepare, func(a *ReplArgs) { a.NewPassword = "short" }},
		"quote password": {pgSpec(), agentprotocol.DBActionReplPrepare, func(a *ReplArgs) { a.NewPassword = "abcdefghijklmnop'; --" }},
		"huge wal keep":  {pgSpec(), agentprotocol.DBActionReplPrepare, func(a *ReplArgs) { a.MaxSlotWALKeepMB = 1 << 30 }},
		"bad slot":       {pgSpec(), agentprotocol.DBActionReplDrop, func(a *ReplArgs) { a.Slot = "Bad-Slot" }},
		"bad lsn":        {pgSpec(), agentprotocol.DBActionReplPromote, func(a *ReplArgs) { a.LSN = "0/3000060'; --" }},
		"unknown action": {pgSpec(), "repl_nuke", nil},
		"bad user":       {Spec{Engine: Postgres, Version: "17", Username: "a b"}, agentprotocol.DBActionReplStatus, nil},
	}
	for name, c := range cases {
		a := good
		if c.mut != nil {
			c.mut(&a)
		}
		_, err := ReplPlan(c.spec, c.action, a)
		require.Error(t, err, name)
	}
	_, err := HBAPlan(pgSpec(), true, "x y")
	require.Error(t, err)
	_, err = ReconfigurePlan(pgSpec(), ReplArgs{Role: good.Role, Slot: good.Slot, Host: "bad host", Port: 5432})
	require.Error(t, err)
	_, err = ReconfigurePlan(pgSpec(), ReplArgs{Role: good.Role, Slot: good.Slot, Host: "db", Port: 0})
	require.Error(t, err)
	_, err = CloneEnv("db", 5432, good.Role, good.Slot, "")
	require.Error(t, err)
}

func TestPrepareKeepsThePasswordOutOfArgvAndSQL(t *testing.T) {
	const pw = "abcdefghijklmnopqrstuvwxyz012345"
	p, err := ReplPlan(pgSpec(), agentprotocol.DBActionReplPrepare, ReplArgs{Role: "nebula_r_0a1b2c3d", NewPassword: pw, MaxSlotWALKeepMB: 1024})
	require.NoError(t, err)
	require.NotContains(t, strings.Join(p.Argv, " "), pw)
	require.NotContains(t, p.Stdin, pw)
	require.Contains(t, p.Env, envNewPassword+"="+pw)
	require.Contains(t, p.Stdin, "max_slot_wal_keep_size = '1024MB'")
	// Only the fixed exec user is ever planned.
	for _, add := range []bool{true, false} {
		h, err := HBAPlan(pgSpec(), add, "nebula_r_0a1b2c3d")
		require.NoError(t, err)
		require.Equal(t, "postgres", h.User)
	}
}

func shRun(t *testing.T, dir, script string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), env...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"sh", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
}

func TestHBAScriptManagesOneBlockIdempotently(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	hba := filepath.Join(dir, "pg_hba.conf")
	const stock = "local all all trust\nhost all all all scram-sha-256\n"
	require.NoError(t, os.WriteFile(hba, []byte(stock), 0o600))
	run := func(add bool, role string) {
		t.Helper()
		p, err := HBAPlan(pgSpec(), add, role)
		require.NoError(t, err)
		out, err := shRun(t, dir, p.Argv[2], append(p.Env, "PGDATA="+dir)...)
		require.NoError(t, err, out)
	}
	read := func() string {
		b, err := os.ReadFile(hba)
		require.NoError(t, err)
		return string(b)
	}

	run(true, "nebula_r_aaaaaaaa")
	require.Equal(t, stock+HBABlock("nebula_r_aaaaaaaa"), read())
	run(true, "nebula_r_aaaaaaaa") // idempotent
	require.Equal(t, stock+HBABlock("nebula_r_aaaaaaaa"), read())
	run(true, "nebula_r_bbbbbbbb")
	require.Equal(t, stock+HBABegin+"\n"+HBALine("nebula_r_aaaaaaaa")+"\n"+HBALine("nebula_r_bbbbbbbb")+"\n"+HBAEnd+"\n", read())
	run(false, "nebula_r_aaaaaaaa")
	require.Equal(t, stock+HBABlock("nebula_r_bbbbbbbb"), read())
	run(false, "nebula_r_aaaaaaaa") // removing an absent line is a no-op
	require.Equal(t, stock+HBABlock("nebula_r_bbbbbbbb"), read())
	run(false, "nebula_r_bbbbbbbb")
	require.Equal(t, stock, read(), "an empty block disappears with its markers")

	info, err := os.Stat(hba)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = os.Stat(hba + ".nebula-new")
	require.True(t, os.IsNotExist(err))
}

func TestHBAScriptFailsWithoutPgHBA(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	p, err := HBAPlan(pgSpec(), true, "nebula_r_aaaaaaaa")
	require.NoError(t, err)
	out, err := shRun(t, dir, p.Argv[2], append(p.Env, "PGDATA="+dir)...)
	require.Error(t, err)
	require.Contains(t, out, "pg_hba.conf not found")
}

func TestReconfigureAndPromoteCleanupScripts(t *testing.T) {
	needTools(t)
	dir := t.TempDir()
	rc, err := ReconfigurePlan(pgSpec(), ReplArgs{Role: "nebula_r_aaaaaaaa", Slot: "nc_r_aaaaaaaa", Host: "mesh-0a1b2c3d", Port: 15432})
	require.NoError(t, err)
	_, err = shRun(t, dir, rc.Argv[2], append(rc.Env, "PGDATA="+dir)...)
	require.Error(t, err, "no standby.signal: not a replica")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "standby.signal"), nil, 0o600))
	out, err := shRun(t, dir, rc.Argv[2], append(rc.Env, "PGDATA="+dir)...)
	require.NoError(t, err, out)
	conf, err := os.ReadFile(filepath.Join(dir, "nebula.replica.conf"))
	require.NoError(t, err)
	require.Equal(t, "primary_conninfo = 'host=mesh-0a1b2c3d port=15432 user=nebula_r_aaaaaaaa application_name=nc_r_aaaaaaaa'\n"+
		"primary_slot_name = 'nc_r_aaaaaaaa'\n", string(conf))
	require.NotContains(t, string(conf), "password")

	pc, err := PromoteCleanupPlan(pgSpec())
	require.NoError(t, err)
	out, err = shRun(t, dir, pc.Argv[2], "PGDATA="+dir)
	require.NoError(t, err, out)
	conf, err = os.ReadFile(filepath.Join(dir, "nebula.replica.conf"))
	require.NoError(t, err)
	require.Empty(t, conf)
	_, err = os.Stat(filepath.Join(dir, "standby.signal"))
	require.True(t, os.IsNotExist(err))
}

func TestReplicaStartCommandRefusesAnEmptyDataDirectory(t *testing.T) {
	needTools(t)
	cmd := ReplicaStartCommand()
	require.Equal(t, "sh", cmd[0])
	dir, bin := t.TempDir(), t.TempDir()
	stub := "#!/bin/sh\necho \"started with PGPASSWORD=$PGPASSWORD args=$*\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker-entrypoint.sh"), []byte(stub), 0o755))
	env := []string{"PGDATA=" + dir, "POSTGRES_REPLICATION_PASSWORD=repl-secret", "PATH=" + bin + ":" + os.Getenv("PATH")}

	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = append(os.Environ(), env...)
	out, err := c.CombinedOutput()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 3, ee.ExitCode(), "must not initdb a fresh primary: %s", out)
	require.Contains(t, string(out), "rebuild the replica")
	require.NotContains(t, string(out), "started")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "PG_VERSION"), []byte("17\n"), 0o600))
	c = exec.Command(cmd[0], cmd[1:]...)
	c.Env = append(os.Environ(), env...)
	out, err = c.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Equal(t, "started with PGPASSWORD=repl-secret args=postgres\n", string(out))
}

func TestStartCommandOnlyForReplicaGuardsPGVersion(t *testing.T) {
	require.Contains(t, ReplicaStartCommand()[2], `PG_VERSION`)
	require.NotContains(t, ReplicaStartCommand()[2], "initdb")
}

func TestParseReplStatusPrimary(t *testing.T) {
	out := `{"role" : "primary", "inRecovery" : false, "slots" : [{"name" : "nc_r_0a1b2c3d", "active" : true, "walStatus" : "reserved", "safeWalSize" : 4294967296}, {"name" : "nc_r_dead", "active" : false, "walStatus" : null, "safeWalSize" : null}], "standbys" : [{"applicationName" : "nc_r_0a1b2c3d", "state" : "streaming", "syncState" : "async", "lagBytes" : 1024, "replayLagMs" : 3}, {"applicationName" : "pg_basebackup", "state" : "backup", "syncState" : "async", "lagBytes" : 0, "replayLagMs" : null}]}
`
	st, err := ParseReplStatus(out)
	require.NoError(t, err)
	require.Equal(t, agentprotocol.ReplRolePrimary, st.Role)
	require.False(t, st.InRecovery)
	require.Len(t, st.Slots, 2)
	require.Equal(t, "reserved", st.Slots[0].WALStatus)
	require.NotNil(t, st.Slots[0].SafeWALSize)
	require.EqualValues(t, 4294967296, *st.Slots[0].SafeWALSize)
	require.Empty(t, st.Slots[1].WALStatus)
	require.Nil(t, st.Slots[1].SafeWALSize)
	require.Len(t, st.Standbys, 2)
	require.EqualValues(t, 1024, st.Standbys[0].LagBytes)
	require.NotNil(t, st.Standbys[0].ReplayLagMs)
	require.EqualValues(t, 3, *st.Standbys[0].ReplayLagMs)
	require.Nil(t, st.Standbys[1].ReplayLagMs)
}

func TestParseReplStatusReplica(t *testing.T) {
	st, err := ParseReplStatus("{\"role\" : \"replica\", \"inRecovery\" : true, \"receiverStatus\" : \"streaming\", \"receiveLsn\" : \"0/3000148\", \"replayLsn\" : \"0/3000060\", \"replayLagBytes\" : 232}\n")
	require.NoError(t, err)
	require.Equal(t, agentprotocol.ReplRoleReplica, st.Role)
	require.True(t, st.InRecovery)
	require.Equal(t, "streaming", st.ReceiverStatus)
	require.Equal(t, "0/3000148", st.ReceiveLSN)
	require.Equal(t, "0/3000060", st.ReplayLSN)
	require.EqualValues(t, 232, st.ReplayLagBytes)

	st, err = ParseReplStatus(`{"role":"replica","inRecovery":true,"receiverStatus":"stopped","receiveLsn":null,"replayLsn":null,"replayLagBytes":0}`)
	require.NoError(t, err)
	require.Equal(t, "stopped", st.ReceiverStatus)
	require.Empty(t, st.ReceiveLSN)
}

func TestParseReplStatusRejectsGarbage(t *testing.T) {
	for _, out := range []string{"", "t\n", "{not json}\n"} {
		_, err := ParseReplStatus(out)
		require.Error(t, err, out)
	}
}

func TestParseControlData(t *testing.T) {
	out := "pg_control version number:            1700\nDatabase cluster state:               shut down\nLatest checkpoint location:           0/3000060\nLatest checkpoint's REDO location:    0/3000060\n"
	cd, err := ParseControlData(out)
	require.NoError(t, err)
	require.True(t, cd.CleanShutdown())
	require.Equal(t, "0/3000060", cd.CheckpointLSN)

	cd, err = ParseControlData(strings.Replace(out, "shut down", "in production", 1))
	require.NoError(t, err)
	require.False(t, cd.CleanShutdown())

	_, err = ParseControlData("pg_controldata: error: could not open file")
	require.Error(t, err)
}

func TestParseLSN(t *testing.T) {
	lsn, err := ParseLSN("3\nt\nnebula:lsn:0/3000060\n")
	require.NoError(t, err)
	require.Equal(t, "0/3000060", lsn)
	_, err = ParseLSN("t\n")
	require.Error(t, err)
	_, err = ParseLSN("nebula:lsn:garbage\n")
	require.Error(t, err)
}
