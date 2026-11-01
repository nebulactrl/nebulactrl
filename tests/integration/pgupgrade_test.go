//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/engine"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// envPGUpgrade enables the pg_upgrade test: it pulls tianon/postgres-upgrade
// (about 550 MB), so it is not part of the default suite.
const envPGUpgrade = "NEBULACTRL_TEST_PGUPGRADE"

type nopProxy struct{}

func (nopProxy) Apply([]agentprotocol.Route) error                  { return nil }
func (nopProxy) Current() []agentprotocol.Route                     { return nil }
func (nopProxy) Probe(context.Context, string, string) (int, error) { return 200, nil }
func (nopProxy) SetACMEEmail(string)                                {}

type nopSender struct{}

func (nopSender) Event(agentprotocol.EventFrame)  {}
func (nopSender) Log(agentprotocol.LogFrame)      {}
func (nopSender) LogEnd(agentprotocol.LogEnd)     {}
func (nopSender) Stats(agentprotocol.Stats)       {}
func (nopSender) Observed(agentprotocol.Observed) {}

// The agent converts a PostgreSQL 17 volume to 18 with pg_upgrade into a new
// volume, keeps the old volume, and the data survives.
func TestPGUpgradeConvertsVolume(t *testing.T) {
	requireTier(t, tierFull)
	if os.Getenv(envPGUpgrade) != "1" {
		t.Skipf("set %s=1 to run (pulls tianon/postgres-upgrade)", envPGUpgrade)
	}
	eng := requireDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	svc := uuid.NewString()
	oldVol := agentprotocol.VolumeName(svc, "data")
	newVol := agentprotocol.VolumeName(svc, "data-pg18")
	dbName := "nc-pgupgrade-test-db"
	chkName := "nc-pgupgrade-test-chk"
	cleanup := func() {
		cctx, c := context.WithTimeout(context.Background(), 2*time.Minute)
		defer c()
		for _, n := range []string{dbName, chkName} {
			_, _ = sh(cctx, "", "docker", "rm", "-f", n)
		}
		_, _ = sh(cctx, "", "docker", "volume", "rm", "-f", oldVol, newVol)
	}
	cleanup()
	t.Cleanup(cleanup)

	// A running PostgreSQL 17 database with a row in it.
	_, err := sh(ctx, "", "docker", "run", "-d", "--name", dbName,
		"--label", agentprotocol.LabelManaged+"=true", "--label", agentprotocol.LabelService+"="+svc,
		"--label", agentprotocol.LabelDeployment+"="+uuid.NewString(),
		"-v", oldVol+":/var/lib/postgresql/data", "-e", "POSTGRES_PASSWORD=pw", "postgres:17")
	require.NoError(t, err)
	require.Truef(t, eventually(60*time.Second, time.Second, func() bool {
		_, err := sh(ctx, "", "docker", "exec", dbName, "pg_isready", "-h", "127.0.0.1", "-U", "postgres")
		return err == nil
	}), "postgres 17 never became ready")
	_, err = sh(ctx, "", "docker", "exec", dbName, "psql", "-U", "postgres", "-c", "create table t(a int); insert into t values (42);")
	require.NoError(t, err)

	e, err := engine.New(engine.Config{Docker: eng, Proxy: nopProxy{}, Sender: nopSender{}})
	require.NoError(t, err)
	raw, err := json.Marshal(agentprotocol.DBUpgradePayload{
		ServiceID: svc, UpgradeID: uuid.NewString(), FromVersion: "17", ToVersion: "18",
		HelperImage: "tianon/postgres-upgrade:17-to-18", OldVolume: "data", NewVolume: "data-pg18", StopGraceSeconds: 10,
	})
	require.NoError(t, err)
	res := e.HandleCommand(ctx, agentprotocol.Command{ID: "up-1", Kind: agentprotocol.KindDBUpgrade, Payload: raw})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, "%s\n%s", res.Error, res.Data)

	info, err := eng.ContainerInspect(ctx, dbName)
	require.NoError(t, err)
	require.False(t, info.Running, "the old database stays stopped until the staged change is deployed")

	// PostgreSQL 18 starts on the new volume with the data; the old volume is intact.
	_, err = sh(ctx, "", "docker", "run", "-d", "--name", chkName, "-v", newVol+":/var/lib/postgresql", "-e", "POSTGRES_PASSWORD=pw", "postgres:18")
	require.NoError(t, err)
	require.Truef(t, eventually(60*time.Second, time.Second, func() bool {
		_, err := sh(ctx, "", "docker", "exec", chkName, "pg_isready", "-h", "127.0.0.1", "-U", "postgres")
		return err == nil
	}), "postgres 18 never became ready on the upgraded volume")
	out, err := sh(ctx, "", "docker", "exec", chkName, "psql", "-U", "postgres", "-At", "-c", "select a from t")
	require.NoError(t, err)
	require.Equal(t, "42", strings.TrimSpace(out))
	ver, err := sh(ctx, "", "docker", "exec", chkName, "psql", "-U", "postgres", "-At", "-c", "show server_version")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(ver, "18."), ver)
	vols, err := sh(ctx, "", "docker", "volume", "ls", "-q")
	require.NoError(t, err)
	require.Contains(t, vols, oldVol)
}
