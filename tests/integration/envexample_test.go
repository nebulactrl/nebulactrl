//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnvExampleKeysArePrefilledBeforeTheFirstDeploy: a git repository whose
// .env.example lists DATABASE_URL, SECRET_KEY=changeme and APP_NAME=demo, in an
// environment with a managed Postgres. Before the service exists, the create
// preview reads the file on the server (a depth-1 fetch of the git:// fixture,
// nothing checked out, nothing deployed) and proposes a reference to the
// database's DATABASE_URL, a generated secret and the example's APP_NAME. The
// accepted keys are staged with the service; a pasted token never reaches the
// database.
func TestEnvExampleKeysArePrefilledBeforeTheFirstDeploy(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	g := gitServer(t)
	s := SharedStack(t)
	pg := s.createPostgres("pg")

	servicesBefore := len(s.Do("GET", "/environments/"+s.EnvID+"/services", nil).JSON().Items())

	// 1. The preview: no service, no deployment, no variables yet.
	r := s.Do("POST", "/environments/"+s.EnvID+"/env-template-preview", map[string]any{
		"gitUrl": g.URL, "branch": "main", "serverId": s.ServerID, "gitToken": canary,
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	p := r.JSON()
	require.Equal(t, "found", p.Str("status"), "%v", p)
	require.Equal(t, "agent", p.Str("method"), "a git:// URL has no file API: the server reads it")
	require.Equal(t, g.CommitSHA, p.Str("commit"), "the files are read from the branch head")
	byKey := map[string]J{}
	for _, it := range p.Items() {
		byKey[it.Str("key")] = it
	}
	require.Len(t, byKey, 3, "%v", p)
	require.Equal(t, "${{"+pg.Slug+".DATABASE_URL}}", byKey["DATABASE_URL"].Str("value"),
		"the managed Postgres of the environment fills DATABASE_URL")
	require.Equal(t, "Postgres connection string", byKey["DATABASE_URL"].Str("comment"))
	require.Equal(t, "${{secret(32)}}", byKey["SECRET_KEY"].Str("value"), "a placeholder secret becomes a generated one")
	require.True(t, byKey["SECRET_KEY"].Bool("proposedSecret"))
	require.Equal(t, "demo", byKey["APP_NAME"].Str("value"))
	require.False(t, byKey["APP_NAME"].Bool("needsInput"))
	fileList, _ := p["files"].([]any)
	require.Len(t, fileList, 1)
	require.Equal(t, ".env.example", J(fileList[0].(map[string]any)).Str("path"))

	require.Len(t, s.Do("GET", "/environments/"+s.EnvID+"/services", nil).JSON().Items(), servicesBefore, "a preview creates nothing")
	var scans int
	require.NoError(t, s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_commands WHERE kind = 'env_scan' AND payload::text LIKE '%'||$1||'%'`, g.URL).Scan(&scans))
	require.Positive(t, scans, "the read went through the agent")
	require.NotContains(t, s.dumpAllTables(), canary, "the pasted token is used for the read only, never stored")

	// A branch that does not exist is reported, not raised.
	r = s.Do("POST", "/environments/"+s.EnvID+"/env-template-preview", map[string]any{
		"gitUrl": g.URL, "branch": "no-such-branch", "serverId": s.ServerID,
	})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "unavailable", r.JSON().Str("status"), "%v", r.JSON())

	// 2. Create the service with the accepted keys staged.
	svc := s.NewService(SvcSpec{Name: "prefilled", GitURL: g.URL})
	vars := []map[string]any{
		{"key": "DATABASE_URL", "value": byKey["DATABASE_URL"].Str("value"), "source": "suggested"},
		{"key": "SECRET_KEY", "value": byKey["SECRET_KEY"].Str("value"), "isSecret": true, "source": "suggested"},
		{"key": "APP_NAME", "value": byKey["APP_NAME"].Str("value"), "source": "suggested"},
	}
	r = s.Do("PUT", "/services/"+svc.ID+"/variables", map[string]any{"variables": vars})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	staged := map[string]J{}
	for _, it := range s.Do("GET", "/services/"+svc.ID+"/variables", nil).JSON().Items() {
		staged[it.Str("key")] = it
	}
	require.Equal(t, "demo", staged["APP_NAME"].Str("value"))
	require.True(t, staged["SECRET_KEY"].Bool("isSecret"))
	require.Nil(t, staged["SECRET_KEY"]["value"], "a secret's value is never returned")
	require.Equal(t, "generated", staged["SECRET_KEY"].Str("source"))
	require.Empty(t, staged["DATABASE_URL"].Str("problem"), "the reference to the database resolves")

	// 3. Reading the service's repository again offers nothing that is set already.
	r = s.Do("POST", "/services/"+svc.ID+"/env-template-scan", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, "found", r.JSON().Str("status"), "%v", r.JSON())
	require.Equal(t, g.CommitSHA, r.JSON().Str("commit"))
	require.Empty(t, r.JSON()["items"], "all three keys are staged")
	r = s.Do("GET", "/services/"+svc.ID+"/suggested-variables", nil)
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	require.Equal(t, g.CommitSHA, r.JSON().Str("commit"), "the stored files carry the commit they came from")

	// Nothing was deployed by any of this.
	require.Empty(t, s.Do("GET", "/services/"+svc.ID+"/deployments", nil).JSON().Items())
}
