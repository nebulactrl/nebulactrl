package deployments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/releases"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *routeFixture) setSecretVar(svc uuid.UUID, key, value string) {
	f.t.Helper()
	enc, err := f.deps.Box.SealString(secretbox.ServiceVariablesValue, secretbox.RowID(svc.String(), key), value)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), `INSERT INTO service_variables (service_id, key, value_enc, is_secret) VALUES ($1, $2, $3, true)
		ON CONFLICT (service_id, key) DO UPDATE SET value_enc = EXCLUDED.value_enc`, svc, key, enc)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(context.Background(), `UPDATE services SET generation = generation + 1 WHERE id = $1`, svc)
	require.NoError(f.t, err)
}

func (f *routeFixture) releaseEnvSet(depID uuid.UUID) bool {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM releases r JOIN deployments d ON d.release_id = r.id WHERE d.id = $1 AND r.env_enc IS NOT NULL`, depID).Scan(&n))
	return n == 1
}

// A rollback to a release whose variable values were pruned (outside the
// rollback window) must not start the old image with an empty environment: it
// resolves the service's CURRENT variables, says so in the timeline and the
// response, and refuses when they do not resolve.
func TestRollbackToPrunedReleaseUsesCurrentVariables(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	ctx := context.Background()
	// The seeded first deployment has no finish time; give it one so the
	// retention window orders by real times.
	_, err := f.pool.Exec(ctx, `UPDATE deployments SET finished_at = now() - interval '1 hour' WHERE service_id = $1`, web.ID)
	require.NoError(t, err)
	setConfig(t, f, web.ID, `{"image":"traefik/whoami:latest","port":80,"imageRetention":2}`)

	f.setSecretVar(web.ID, "API_KEY", "key-one")
	dep1, _ := f.deployWeb(web.ID)
	f.setSecretVar(web.ID, "API_KEY", "key-two")
	dep2, _ := f.deployWeb(web.ID)
	f.setSecretVar(web.ID, "API_KEY", "key-three")
	dep3, _ := f.deployWeb(web.ID)
	require.True(t, f.releaseEnvSet(dep1))

	n, err := releases.NewService(f.pool, nil).Prune(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "only the release outside the window of 2 is pruned (the seeded one never had an env)")
	require.False(t, f.releaseEnvSet(dep1))
	require.True(t, f.releaseEnvSet(dep2))
	require.True(t, f.releaseEnvSet(dep3))

	// The current variables do not resolve: the rollback fails and changes nothing.
	f.setSecretVar(web.ID, "API_KEY", "key-four ${{ghost.PORT}}")
	rb, err := f.deps.Rollback(f.ctx, dep1)
	require.NoError(t, err)
	require.Contains(t, rb.Disclaimer, releases.PrunedNotice)
	require.NoError(t, f.deps.runDeploy(ctx, rb.ID))
	var status, msg string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT status, status_message FROM deployments WHERE id = $1`, rb.ID).Scan(&status, &msg))
	require.Equal(t, "failed", status)
	require.Contains(t, msg, "removed after the rollback window")
	require.False(t, f.releaseEnvSet(dep1), "a refused rollback does not restore an environment")

	// With resolvable variables the rollback deploys the old image with them.
	f.setSecretVar(web.ID, "API_KEY", "key-five")
	rb, err = f.deps.Rollback(f.ctx, dep1)
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeploy(ctx, rb.ID))
	require.True(t, f.releaseEnvSet(dep1), "the release records the environment it is deployed with")

	cmd := f.lastCommand(agentprotocol.KindDeploy)
	secrets, err := f.deps.SecretsForCommand(ctx, uuid.New(), cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, "key-five", secrets[agentprotocol.SecretEnvPrefix+"API_KEY"], "the current value, not key-one")
	var p agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Contains(t, p.EnvKeys, "API_KEY")
	require.NotContains(t, string(cmd.Payload), "key-five", "no value rides the persisted command payload")

	var notices int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM deployment_events WHERE deployment_id = $1 AND message = $2`, rb.ID, releases.PrunedNotice).Scan(&notices))
	require.Equal(t, 1, notices, "the timeline says the current variables were used")
}
