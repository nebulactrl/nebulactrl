package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A repository without an API reader (Bitbucket, SSH, a token URL) has its
// nebula.toml read by the agent of the service's server: the command names the
// files, carries no secret, and the reply brings the files and the commit.
func TestProjectConfigFilesAreReadByTheAgent(t *testing.T) {
	h := newHarness(t)
	h.setup()
	e := h.newVarEnv("cfgscan")
	online := h.newOnlineServer("builder", 8*gib, gib)
	const token = "pasted_token_canary_135790"
	r := h.do("POST", "/environments/"+e.envID+"/services", "", map[string]any{
		"name": "shop", "sourceType": "git", "serverId": online, "gitToken": token,
		"config": map[string]any{"gitUrl": "https://git.example.org/acme/private.git", "gitBranch": "main", "port": 80},
	})
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	svcID := uuid.MustParse(r.JSON()["id"].(string))
	ctx := context.Background()
	sc, err := services.LoadScopeInternal(ctx, db.New(h.app.Pool), svcID)
	require.NoError(t, err)

	const commit = "0123456789abcdef0123456789abcdef01234567"
	result := agentprotocol.EnvScanResult{
		Outcome: agentprotocol.OutcomeSucceeded, Commit: commit,
		ConfigFiles: map[string][]byte{"apps/web/nebula.toml": []byte("schema = 1\n")},
	}
	seen := h.answerEnvScan(online, result, "")
	got, err := h.deps().ScanConfigFiles(ctx, sc, "release", []string{"nebula.toml", "apps/web/nebula.toml"})
	require.NoError(t, err)
	frames := <-seen
	require.Equal(t, commit, got.Commit)
	require.Equal(t, result.ConfigFiles, got.ConfigFiles)

	var p agentprotocol.EnvScanPayload
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &p))
	require.Equal(t, []string{"nebula.toml", "apps/web/nebula.toml"}, p.ConfigPaths)
	require.True(t, p.ConfigOnly, "a config read does not scan the template env files")
	require.Equal(t, "release", p.Branch, "the bound branch, not the service's")
	require.Equal(t, svcID.String(), p.ServiceID)
	require.True(t, p.HasToken)
	require.NotContains(t, frames[0], token, "the token never rides in the persisted command")
	require.Contains(t, frames[1], token, "it is attached at send time")

	// An empty branch reads the service's own.
	seen = h.answerEnvScan(online, result, "")
	_, err = h.deps().ScanConfigFiles(ctx, sc, "", []string{"nebula.toml"})
	require.NoError(t, err)
	frames = <-seen
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &p))
	require.Equal(t, "main", p.Branch)

	// Paths are validated before anything is enqueued.
	tooMany := make([]string, agentprotocol.ScanMaxConfigPaths+1)
	for i := range tooMany {
		tooMany[i] = "f" + string(rune('a'+i)) + ".toml"
	}
	for _, bad := range [][]string{nil, {"../etc/passwd"}, {"/abs"}, {"a//b"}, tooMany} {
		_, err = h.deps().ScanConfigFiles(ctx, sc, "", bad)
		var ae *apierr.Error
		require.ErrorAs(t, err, &ae, "%v", bad)
		require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
	}

	// An agent failure is an error; an offline server cannot answer at all.
	seen = h.answerEnvScan(online, agentprotocol.EnvScanResult{}, "git fetch: authentication required")
	_, err = h.deps().ScanConfigFiles(ctx, sc, "", []string{"nebula.toml"})
	<-seen
	require.ErrorContains(t, err, "authentication required")

	_, err = h.app.Pool.Exec(ctx, `UPDATE servers SET status='offline' WHERE id = $1`, uuid.MustParse(online))
	require.NoError(t, err)
	_, err = h.deps().ScanConfigFiles(ctx, sc, "", []string{"nebula.toml"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "server_offline", ae.Code)
}
