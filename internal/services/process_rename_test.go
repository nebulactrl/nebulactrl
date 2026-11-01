package services_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func finalizeRenames(t *testing.T, f *txFixture, id uuid.UUID, release services.Config) {
	t.Helper()
	require.NoError(t, f.inTx(true, func(tx pgx.Tx) error {
		return services.FinalizeProcessRenames(f.ctx, tx, f.scope(tx, id), release.WithDefaults())
	}))
}

func domainComponents(t *testing.T, f *txFixture, id uuid.UUID) map[string]string {
	t.Helper()
	rows, err := f.q.ListServiceDomains(context.Background(), id)
	require.NoError(t, err)
	out := map[string]string{}
	for _, d := range rows {
		out[d.Hostname] = d.Component
	}
	return out
}

func TestFinalizeProcessRenamesMovesDomainsOnceTheReleaseHasTheNewName(t *testing.T) {
	f := newTxFixture(t)
	bg := context.Background()
	withPort := func(p *services.ProcessConfig) { p.Port = 4000 }
	staged := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("gateway", withPort), worker("queue")}}
	svc := f.insert(f.prod.ID, "web", staged)
	_, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svc.ID, Hostname: "app.example.com", TargetPort: 3000})
	require.NoError(t, err)
	_, err = f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svc.ID, Hostname: "api.example.com", TargetPort: 4000, Component: "api"})
	require.NoError(t, err)
	require.NoError(t, f.q.UpsertProcessRename(bg, db.UpsertProcessRenameParams{ServiceID: svc.ID, FromProcess: "api", ToProcess: "gateway"}))

	// A release that still has the old name moves nothing and keeps the staged rename.
	old := services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("api", withPort), worker("queue")}}
	finalizeRenames(t, f, svc.ID, old)
	require.Equal(t, map[string]string{"app.example.com": "", "api.example.com": "api"}, domainComponents(t, f, svc.ID))
	renames, err := services.PendingProcessRenames(bg, f.q, svc.ID)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"api": "gateway"}, renames)

	// The release with the new name takes the domains along and clears the rename.
	finalizeRenames(t, f, svc.ID, staged)
	require.Equal(t, map[string]string{"app.example.com": "", "api.example.com": "gateway"}, domainComponents(t, f, svc.ID))
	renames, err = services.PendingProcessRenames(bg, f.q, svc.ID)
	require.NoError(t, err)
	require.Empty(t, renames)

	// Nothing staged: nothing changes.
	finalizeRenames(t, f, svc.ID, staged)
	require.Equal(t, map[string]string{"app.example.com": "", "api.example.com": "gateway"}, domainComponents(t, f, svc.ID))
}

func TestFinalizeProcessRenamesDropsRenamesTheStagedConfigLost(t *testing.T) {
	f := newTxFixture(t)
	bg := context.Background()
	svc := f.insert(f.prod.ID, "web", services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("queue")}})
	_, err := f.q.InsertDomain(bg, db.InsertDomainParams{ServiceID: svc.ID, Hostname: "api.example.com", TargetPort: 4000, Component: "api"})
	require.NoError(t, err)
	require.NoError(t, f.q.UpsertProcessRename(bg, db.UpsertProcessRenameParams{ServiceID: svc.ID, FromProcess: "api", ToProcess: "gateway"}))

	finalizeRenames(t, f, svc.ID, services.Config{Image: "x", Port: 3000, Processes: []services.ProcessConfig{worker("api")}})
	renames, err := services.PendingProcessRenames(bg, f.q, svc.ID)
	require.NoError(t, err)
	require.Empty(t, renames, "the new name is neither staged nor released")
	require.Equal(t, map[string]string{"api.example.com": "api"}, domainComponents(t, f, svc.ID))
}
