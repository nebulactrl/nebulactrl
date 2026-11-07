package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/teams"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func newMailers(t *testing.T) (resetMailer, teamMailer, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	ws, err := q.CreateWorkspace(context.Background(), "team")
	require.NoError(t, err)
	n := notifications.NewService(notifications.Deps{Pool: pool, Box: box})
	ts := teams.NewService(teams.Deps{Pool: pool, Auth: auth.NewService(pool, nil), Box: box})
	return resetMailer{n: n, t: ts, q: q}, teamMailer{n: n}, pool, ws.ID
}

func TestPasswordResetMailNeedsARelayOrAnEmailChannel(t *testing.T) {
	m, _, pool, _ := newMailers(t)
	q := db.New(pool)
	ctx := context.Background()
	require.False(t, m.Available(ctx), "no relay and no email channel: reset by email is not offered")

	u, err := q.InsertUser(ctx, db.InsertUserParams{Email: "ada@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	err = m.SendPasswordReset(ctx, auth.ResetMail{UserID: u.ID, To: u.Email, Link: "https://nebula.example.com/reset-password?token=x", ExpiresAt: time.Now().Add(time.Hour)})
	require.Error(t, err, "with no relay and no email channel the reset email is refused")

	// With an instance relay configured the feature is on (nothing is sent by asking).
	_, err = pool.Exec(ctx, `UPDATE instance_settings SET smtp_host = 'smtp.example.com', mail_from = 'no-reply@example.com', smtp_port = 587, smtp_tls = 'starttls'`)
	require.NoError(t, err)
	require.True(t, m.Available(ctx))
	r, err := m.relay(ctx)
	require.NoError(t, err)
	require.Equal(t, "smtp.example.com", r.Host)
	require.Equal(t, 587, r.Port)
	require.Equal(t, "starttls", r.TLS)
	require.Equal(t, "no-reply@example.com", r.From)
}

func TestInviteMailWithoutAnyTransportIsReportedAsNotConfigured(t *testing.T) {
	_, m, _, ws := newMailers(t)
	err := m.SendInvite(context.Background(), ws, nil, teams.InviteMail{
		To: "new@example.com", TeamName: "Acme", Role: "developer", Link: "https://nebula.example.com/invite/nci_x", ExpiresAt: time.Now().Add(time.Hour),
	})
	require.ErrorIs(t, err, teams.ErrNoMailer, "the teams service turns this into a link-only invite")
}
