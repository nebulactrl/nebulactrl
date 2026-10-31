package services_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// A developer must not be able to point a service that is linked to the team's
// git connection at a server they control: changing the repository or host
// ends the link, so the connection's credentials are never sent there.
func TestChangingTheGitURLToAnotherRepositoryEndsTheConnectionLink(t *testing.T) {
	f := newTxFixture(t)
	hook := &recordingLinkHook{}
	f.svcs.SetGitLinkHook(hook)
	sv := f.gitService(f.staging.ID, "api", services.Config{Port: 3000})
	conn, err := f.q.InsertGitConnection(context.Background(), db.InsertGitConnectionParams{
		ID: uuid.New(), WorkspaceID: f.ws.ID, Provider: "gitea", Name: "acme", BaseUrl: "https://git.example.com", AuthType: "token",
		Status: "active", WebhookSecretEnc: []byte("x"), Meta: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	_, err = f.q.UpsertServiceGit(context.Background(), db.UpsertServiceGitParams{
		ServiceID: sv.ID, ConnectionID: conn.ID, RepoFullName: "acme/shop", AutoDeploy: true, WatchPaths: []string{},
	})
	require.NoError(t, err)

	update := func(gen int64, url string) services.ServiceView {
		t.Helper()
		cfg := services.DecodeConfig(f.row(sv.ID).Config)
		cfg.GitURL = url
		v, err := f.svcs.Update(f.ctx, sv.ID, gen, services.UpdateInput{Config: &cfg})
		require.NoError(t, err)
		return v
	}

	// The same repository over another scheme keeps the link.
	v := update(sv.Generation, "http://git.example.com/acme/shop")
	_, err = f.q.GetServiceGit(context.Background(), sv.ID)
	require.NoError(t, err)
	require.Empty(t, hook.repos)

	// Another host ends it.
	update(v.Generation, "http://attacker.example/x.git")
	_, err = f.q.GetServiceGit(context.Background(), sv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Equal(t, []string{"acme/shop"}, hook.repos)
}
