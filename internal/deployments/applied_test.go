package deployments

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (f *routeFixture) generations(id uuid.UUID) (generation, applied int64) {
	f.t.Helper()
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		"SELECT generation, applied_generation FROM services WHERE id = $1", id).Scan(&generation, &applied))
	return generation, applied
}

// A git push of a production service deploys the new commit with
// the configuration and variables of the running release. Staged changes (a
// repository and branch switch, a port, a secret) are not shipped and stay
// staged.
func TestGitPushOfProductionServiceDeploysTheAppliedStateOnly(t *testing.T) {
	f := newBuildFixture(t) // the fixture environment is production
	bg := context.Background()
	sv := f.gitService("api")
	f.setSecretVar(sv.ID, "API_KEY", "applied")
	f.buildAndDeploy(sv, sha1, dig1)

	staged, err := json.Marshal(services.Config{GitURL: "git://127.0.0.1:1/evil.git", GitBranch: "agent-tmp", Port: 9999}.WithDefaults())
	require.NoError(t, err)
	setConfig(t, f.routeFixture, sv.ID, string(staged))
	f.setSecretVar(sv.ID, "STAGED_SECRET", "x")
	gen, applied := f.generations(sv.ID)
	require.Greater(t, gen, applied)

	v, err := f.deps.DeployWith(bg, sv.ID, TriggerGitPush, InternalOptions{Commit: &CommitInfo{SHA: sha2, Branch: "main"}})
	require.NoError(t, err)
	var depGeneration int64
	require.NoError(t, f.pool.QueryRow(bg, "SELECT generation FROM deployments WHERE id = $1", v.ID).Scan(&depGeneration))
	require.Equal(t, applied, depGeneration, "the push deploys the applied generation")

	require.NoError(t, f.deps.runDeployRef(bg, v.ID, sha2, ""))
	var bp agentprotocol.BuildImagePayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindBuildImage).Payload, &bp))
	require.Equal(t, "git://127.0.0.1:1/app.git", bp.Git.URL, "the applied repository, not the staged one")
	require.Equal(t, sha2, bp.Git.CommitSHA)

	f.finishBuild(v.ID, agentprotocol.OutcomeSucceeded, dig2)
	require.NoError(t, f.deps.runDeployOpts(bg, v.ID, "", "", true))
	var dp agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.last(agentprotocol.KindDeploy).Payload, &dp))
	require.Equal(t, 8080, dp.Port, "the applied port")
	require.Contains(t, dp.EnvKeys, "API_KEY")
	require.NotContains(t, dp.EnvKeys, "STAGED_SECRET", "the staged variable is not shipped")

	f.finishDeploy(v.ID, "registry.example.com/shop-api@"+dig2)
	gen2, applied2 := f.generations(sv.ID)
	require.Equal(t, applied, applied2, "the staged changes stay staged")
	require.Equal(t, gen, gen2)
	var raw []byte
	require.NoError(t, f.pool.QueryRow(bg, "SELECT config FROM services WHERE id = $1", sv.ID).Scan(&raw))
	require.Equal(t, 9999, services.DecodeConfig(raw).Port, "and the live configuration is untouched")
}

// Test entry points of runDeployOpts: runDeploy deploys the pending revision,
// runDeployRef pins a commit or an override image.
func (s *Service) runDeploy(ctx context.Context, depID uuid.UUID) error {
	return s.runDeployRef(ctx, depID, "", "")
}

func (s *Service) runDeployRef(ctx context.Context, depID uuid.UUID, commitSHA, overrideImage string) error {
	return s.runDeployOpts(ctx, depID, commitSHA, overrideImage, false)
}
