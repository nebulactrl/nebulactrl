package teams

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// fakeSlices records the slice lifecycle calls a team's creation and deletion make.
type fakeSlices struct {
	ensured, deleted []uuid.UUID
	failEnsure       error
	failDelete       error
}

func (f *fakeSlices) EnqueueEnsureTx(_ context.Context, tx pgx.Tx, id uuid.UUID) error {
	if tx == nil {
		return errors.New("not in a transaction")
	}
	f.ensured = append(f.ensured, id)
	return f.failEnsure
}

func (f *fakeSlices) MarkDeletedTx(_ context.Context, tx pgx.Tx, id uuid.UUID) error {
	if tx == nil {
		return errors.New("not in a transaction")
	}
	f.deleted = append(f.deleted, id)
	return f.failDelete
}

func TestCreatingATeamQueuesItsSliceAndRollsBackWithIt(t *testing.T) {
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ctx := context.Background()
	home, err := q.CreateWorkspace(ctx, "Home")
	require.NoError(t, err)
	u, err := q.CreateUser(ctx, db.CreateUserParams{WorkspaceID: home.ID, Email: "o@example.com", Name: "O", PasswordHash: "x"})
	require.NoError(t, err)
	caller := auth.WithPrincipal(ctx, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, WorkspaceID: home.ID, Scopes: policy.AllScopes})

	svc := NewService(Deps{Pool: pool, Auth: auth.NewService(pool, policy.ScopesForRole), Logger: slog.Default()})
	slices := &fakeSlices{}
	svc.SetSlices(slices)

	team, err := svc.Create(caller, "Fresh")
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{team.ID}, slices.ensured, "the slice is queued for exactly the new team")

	// When queuing fails the team is not created: both happen in one transaction.
	slices.failEnsure = errors.New("queue is down")
	_, err = svc.Create(caller, "Ghost")
	require.Error(t, err)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM workspaces WHERE name = 'Ghost'`).Scan(&n))
	require.Equal(t, 0, n)
}

func TestDeletingATeamStartsTheSliceClockAtomically(t *testing.T) {
	f := newDeleteFixture(t)
	slices := &fakeSlices{}
	f.svc.SetSlices(slices)

	// A step that fails first does not touch the slices.
	f.td.failOn = "platform"
	require.NoError(t, f.svc.RunDeletion(context.Background(), f.team.ID))
	require.Empty(t, slices.deleted)
	require.True(t, f.exists(t))

	// A slice error keeps the team: it is not deleted without its slices marked.
	f.td.failOn = ""
	slices.failDelete = errors.New("db hiccup")
	_, err := f.q.StartTeamDeletion(context.Background(), db.StartTeamDeletionParams{ID: f.team.ID})
	require.NoError(t, err)
	require.NoError(t, f.svc.RunDeletion(context.Background(), f.team.ID))
	require.True(t, f.exists(t), "the team stays until its slices could be marked")

	slices.failDelete = nil
	_, err = f.q.StartTeamDeletion(context.Background(), db.StartTeamDeletionParams{ID: f.team.ID})
	require.NoError(t, err)
	require.NoError(t, f.svc.RunDeletion(context.Background(), f.team.ID))
	require.False(t, f.exists(t))
	require.Equal(t, f.team.ID, slices.deleted[len(slices.deleted)-1])
}
