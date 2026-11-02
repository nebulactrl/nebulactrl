package localserver

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const enrollToken = "nbl_local_enroll_token"

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type rig struct {
	t    *testing.T
	pool *pgxpool.Pool
	bus  *events.Bus
	e    *Ensurer
	now  time.Time
}

func newRig(t *testing.T, tweak func(*config.Installer)) *rig {
	t.Helper()
	pool := testutil.NewPool(t)
	inst := config.Installer{LocalEnrollToken: enrollToken}
	if tweak != nil {
		tweak(&inst)
	}
	r := &rig{t: t, pool: pool, bus: events.NewBus(), now: t0}
	r.e = New(pool, r.bus, &config.Config{Installer: inst}, nil)
	r.e.now = func() time.Time { return r.now }
	return r
}

func (r *rig) count(sql string, args ...any) int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

type serverRow struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	PublicIP    *string
	ACMEEmail   *string
	Status      string
}

func (r *rig) server() serverRow {
	r.t.Helper()
	var s serverRow
	require.NoError(r.t, r.pool.QueryRow(context.Background(),
		`SELECT id, workspace_id, name, public_ip, acme_email, status FROM servers WHERE is_control_plane`).
		Scan(&s.ID, &s.WorkspaceID, &s.Name, &s.PublicIP, &s.ACMEEmail, &s.Status))
	return s
}

func (r *rig) tokenExpiry() time.Time {
	r.t.Helper()
	var exp time.Time
	require.NoError(r.t, r.pool.QueryRow(context.Background(),
		`SELECT expires_at FROM enrollment_tokens WHERE token_hash = $1`, agents.HashEnrollmentToken(enrollToken)).Scan(&exp))
	return exp
}

func TestDisabledWithoutAnEnrollmentToken(t *testing.T) {
	r := newRig(t, func(i *config.Installer) { i.LocalEnrollToken = "" })
	require.False(t, r.e.Enabled())

	done, err := r.e.Ensure(context.Background())
	require.NoError(t, err)
	require.True(t, done, "nothing to do")
	require.Zero(t, r.count(`SELECT count(*) FROM workspaces`), "not even the team is created")
	require.Zero(t, r.count(`SELECT count(*) FROM servers`))

	r.e.Run(context.Background()) // returns at once instead of polling
}

func TestEnsureCreatesTheTeamServerTokenAndAuditEntry(t *testing.T) {
	r := newRig(t, nil)
	require.True(t, r.e.Enabled())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feed := r.bus.Subscribe(ctx)

	done, err := r.e.Ensure(ctx)
	require.NoError(t, err)
	require.False(t, done, "the agent has yet to enroll")

	var wsName string
	require.NoError(t, r.pool.QueryRow(ctx, `SELECT name FROM workspaces`).Scan(&wsName))
	require.Equal(t, auth.DefaultTeamName, wsName, "the first team exists before any user does")

	srv := r.server()
	require.Equal(t, DefaultName, srv.Name)
	require.Equal(t, "pending", srv.Status)
	require.Nil(t, srv.PublicIP)
	require.Nil(t, srv.ACMEEmail)
	require.Equal(t, 1, r.count(`SELECT count(*) FROM workspaces WHERE id = $1`, srv.WorkspaceID))

	require.Equal(t, t0.Add(agents.EnrollmentTokenTTL), r.tokenExpiry().UTC())
	require.Equal(t, 1, r.count(`SELECT count(*) FROM enrollment_tokens WHERE server_id = $1 AND used_at IS NULL AND created_by IS NULL`, srv.ID))

	require.Equal(t, 1, r.count(`SELECT count(*) FROM audit_events WHERE action = 'server.enroll_token.create' AND resource_type = 'server' AND resource_id = $1 AND workspace_id = $2 AND metadata @> '{"controlPlane": true, "by": "installer", "name": "control-plane"}'`, srv.ID.String(), srv.WorkspaceID))

	select {
	case ev := <-feed:
		require.Equal(t, events.Event{Type: "server.updated", ResourceID: srv.ID.String()}, ev)
	case <-time.After(5 * time.Second):
		t.Fatal("no server.updated event")
	}
}

func TestEnsureUsesTheInstallersNameAddressAndEmail(t *testing.T) {
	r := newRig(t, func(i *config.Installer) {
		i.LocalServerName = "  edge-1  "
		i.LocalServerIP = " 203.0.113.5 "
		i.ACMEEmail = " ops@example.com "
	})
	_, err := r.e.Ensure(context.Background())
	require.NoError(t, err)

	srv := r.server()
	require.Equal(t, "edge-1", srv.Name)
	require.Equal(t, "203.0.113.5", *srv.PublicIP)
	require.Equal(t, "ops@example.com", *srv.ACMEEmail)
}

func TestEnsureIgnoresAnAddressThatIsNotAnIP(t *testing.T) {
	r := newRig(t, func(i *config.Installer) { i.LocalServerIP = "not-an-ip" })
	_, err := r.e.Ensure(context.Background())
	require.NoError(t, err)
	require.Nil(t, r.server().PublicIP)
}

func TestEnsureAdoptsAnExistingTeamAndRecordsTheFirstUserAsTokenCreator(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	q := db.New(r.pool)
	ws, err := q.CreateWorkspace(ctx, "Acme")
	require.NoError(t, err)
	user, err := q.CreateUser(ctx, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)

	_, err = r.e.Ensure(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, r.count(`SELECT count(*) FROM workspaces`), "no second team")
	srv := r.server()
	require.Equal(t, ws.ID, srv.WorkspaceID)
	require.Equal(t, 1, r.count(`SELECT count(*) FROM enrollment_tokens WHERE server_id = $1 AND created_by = $2`, srv.ID, user.ID))
}

func TestEnsureKeepsAnUnredeemedTokenAliveWithoutDuplicatingAnything(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	_, err := r.e.Ensure(ctx)
	require.NoError(t, err)
	first := r.server()

	r.now = t0.Add(45 * time.Minute)
	done, err := r.e.Ensure(ctx)
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, 1, r.count(`SELECT count(*) FROM servers`))
	require.Equal(t, 1, r.count(`SELECT count(*) FROM enrollment_tokens`))
	require.Equal(t, first.ID, r.server().ID)
	require.Equal(t, r.now.Add(agents.EnrollmentTokenTTL), r.tokenExpiry().UTC(), "the token is good for another hour from now")
	require.Equal(t, 1, r.count(`SELECT count(*) FROM audit_events WHERE action = 'server.enroll_token.create'`), "only the creation is audited")
}

func TestEnsureLeavesARedeemedOrRevokedTokenAlone(t *testing.T) {
	for name, mark := range map[string]string{
		"redeemed": `UPDATE enrollment_tokens SET used_at = now()`,
		"revoked":  `UPDATE enrollment_tokens SET revoked_at = now()`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			ctx := context.Background()
			_, err := r.e.Ensure(ctx)
			require.NoError(t, err)
			_, err = r.pool.Exec(ctx, mark)
			require.NoError(t, err)

			r.now = t0.Add(time.Hour)
			done, err := r.e.Ensure(ctx)
			require.NoError(t, err)
			require.False(t, done, "the agent's enrollment is on its way")
			require.Equal(t, 1, r.count(`SELECT count(*) FROM enrollment_tokens`), "no second token")
			require.Equal(t, t0.Add(agents.EnrollmentTokenTTL), r.tokenExpiry().UTC(), "the expiry is not extended")
		})
	}
}

func TestEnsureRecreatesATokenThatWasCleanedAway(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	_, err := r.e.Ensure(ctx)
	require.NoError(t, err)
	srv := r.server()
	_, err = r.pool.Exec(ctx, `DELETE FROM enrollment_tokens`)
	require.NoError(t, err)

	r.now = t0.Add(3 * time.Hour)
	done, err := r.e.Ensure(ctx)
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, 1, r.count(`SELECT count(*) FROM servers`))
	require.Equal(t, 1, r.count(`SELECT count(*) FROM enrollment_tokens WHERE server_id = $1`, srv.ID))
	require.Equal(t, r.now.Add(agents.EnrollmentTokenTTL), r.tokenExpiry().UTC())
}

func TestEnsureIsDoneOnceTheAgentEnrolledOrTheServerWasRevoked(t *testing.T) {
	for _, status := range []string{"online", "offline", "revoked"} {
		t.Run(status, func(t *testing.T) {
			r := newRig(t, nil)
			ctx := context.Background()
			_, err := r.e.Ensure(ctx)
			require.NoError(t, err)
			_, err = r.pool.Exec(ctx, `UPDATE servers SET status = $1 WHERE is_control_plane`, status)
			require.NoError(t, err)

			done, err := r.e.Ensure(ctx)
			require.NoError(t, err)
			require.True(t, done)
			require.Equal(t, 1, r.count(`SELECT count(*) FROM servers`), "a revoked control-plane server is never recreated")
		})
	}
}

func TestEnsureFailsWithTheStepThatFailed(t *testing.T) {
	r := newRig(t, nil)
	r.pool.Close()
	done, err := r.e.Ensure(context.Background())
	require.ErrorContains(t, err, "bootstrap team")
	require.False(t, done)
}

func TestEnsureReportsAFailureToCreateTheServer(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	// The team exists, but the server row cannot be written.
	_, err := db.New(r.pool).CreateWorkspace(ctx, "Acme")
	require.NoError(t, err)
	_, err = r.pool.Exec(ctx, `ALTER TABLE servers ADD CONSTRAINT servers_closed CHECK (false) NOT VALID`)
	require.NoError(t, err)

	done, err := r.e.Ensure(ctx)
	require.ErrorContains(t, err, "create control-plane server")
	require.False(t, done)
	require.Zero(t, r.count(`SELECT count(*) FROM enrollment_tokens`), "the token goes with the server or not at all")
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
	on  func(string)
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.buf.Write(p)
	s := b.buf.String()
	b.mu.Unlock()
	if b.on != nil {
		b.on(s)
	}
	return len(p), nil
}

func TestRunStopsWhenTheContextEnds(t *testing.T) {
	r := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.e.Run(ctx) // returns instead of waiting for the next poll
	require.Zero(t, r.count(`SELECT count(*) FROM servers`), "a cancelled context creates nothing")
}

func TestRunReturnsOnceTheServerIsEnrolled(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	_, err := r.e.Ensure(ctx)
	require.NoError(t, err)
	_, err = r.pool.Exec(ctx, `UPDATE servers SET status = 'online'`)
	require.NoError(t, err)

	r.e.Run(ctx) // would block on the poll ticker if it did not see the enrolled server
}

func TestRunCreatesTheServerOnItsFirstPass(t *testing.T) {
	r := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	// The creation is logged; stopping at that log line makes the run end after exactly one pass.
	logs := &syncBuf{on: func(s string) {
		if bytes.Contains([]byte(s), []byte("waiting for its agent to enroll")) {
			cancel()
		}
	}}
	r.e.log = slog.New(slog.NewTextHandler(logs, nil))
	r.e.Run(ctx)
	require.Equal(t, 1, r.count(`SELECT count(*) FROM servers WHERE is_control_plane AND status = 'pending'`))
}

func TestRunLogsAFailingPassAndKeepsPolling(t *testing.T) {
	r := newRig(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	logs := &syncBuf{on: func(s string) {
		if bytes.Contains([]byte(s), []byte("ensure the control-plane server")) {
			cancel()
		}
	}}
	r.e.log = slog.New(slog.NewTextHandler(logs, nil))
	r.pool.Close()

	r.e.Run(ctx)
	logs.mu.Lock()
	defer logs.mu.Unlock()
	require.Contains(t, logs.buf.String(), "level=WARN")
	require.Contains(t, logs.buf.String(), "bootstrap team")
}
