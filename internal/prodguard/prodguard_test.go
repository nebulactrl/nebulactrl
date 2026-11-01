package prodguard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

type fixture struct {
	t      *testing.T
	g      *Guard
	p      *pgxpool.Pool
	q      *db.Queries
	ws     db.Workspace
	user   db.CreateUserRow
	proj   db.Project
	prod   db.Environment
	stage  db.Environment
	svcID  uuid.UUID
	token  uuid.UUID
	offset *time.Duration
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	q := db.New(pool)
	bg := context.Background()
	ws, err := q.CreateWorkspace(bg, "w")
	require.NoError(t, err)
	u, err := q.CreateUser(bg, db.CreateUserParams{WorkspaceID: ws.ID, Email: "a@example.com", Name: "Ada", PasswordHash: "x"})
	require.NoError(t, err)
	prj, err := q.CreateProject(bg, db.CreateProjectParams{WorkspaceID: ws.ID, Name: "Shop", Slug: "shop"})
	require.NoError(t, err)
	prod, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "production", Slug: "production", IsProduction: true})
	require.NoError(t, err)
	stage, err := q.CreateEnvironment(bg, db.CreateEnvironmentParams{ProjectID: prj.ID, Name: "staging", Slug: "staging"})
	require.NoError(t, err)
	box, err := secretbox.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)

	g := New(Deps{Pool: pool, Box: box, PublicURL: "https://nebula.test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	off := new(time.Duration)
	g.now = func() time.Time { return time.Now().Add(*off) }
	g.Install()
	t.Cleanup(Uninstall)
	return &fixture{t: t, g: g, p: pool, q: q, ws: ws, user: u, proj: prj, prod: prod, stage: stage, svcID: uuid.New(), token: uuid.New(), offset: off}
}

func (f *fixture) scope(prod bool, gen int64) ProdScope {
	env := f.stage
	if prod {
		env = f.prod
	}
	return ProdScope{
		WorkspaceID: f.ws.ID, ProjectID: f.proj.ID, EnvironmentID: env.ID, Project: "Shop", Environment: env.Name, IsProduction: prod,
		Summary: "Deploy api to production", Effects: []string{"Pulls image."},
		Targets: []ApprovalTarget{{Kind: "service", ID: f.svcID, Name: "api", Generation: gen}},
	}
}

func (f *fixture) principal(typ string, mut ...func(*auth.Principal)) context.Context {
	tok := f.token
	p := &auth.Principal{
		Type: typ, UserID: f.user.ID, WorkspaceID: f.ws.ID, Role: auth.RoleDeveloper, Label: "ci-token",
		Scopes: policy.ScopesForRole(auth.RoleDeveloper),
	}
	if typ == auth.TypeMCP || typ == auth.TypeToken {
		p.TokenID = &tok
	}
	for _, m := range mut {
		m(p)
	}
	return WithClient(auth.WithPrincipal(context.Background(), p), "claude-code")
}

// person is a signed-in member with the given role.
func (f *fixture) person(role string, mut ...func(*auth.Principal)) context.Context {
	return f.principal(auth.TypeUser, append([]func(*auth.Principal){func(p *auth.Principal) {
		p.Role, p.Scopes, p.Label = role, policy.ScopesForRole(role), "Ada"
	}}, mut...)...)
}

type auditRow struct {
	Action, ActorType string
	Metadata          map[string]any
}

func (f *fixture) auditRows() []auditRow {
	f.t.Helper()
	rows, err := f.p.Query(context.Background(), `SELECT action, actor_type, metadata FROM audit_events WHERE workspace_id = $1 AND action LIKE 'approval.%' ORDER BY id`, f.ws.ID)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var raw []byte
		require.NoError(f.t, rows.Scan(&r.Action, &r.ActorType, &raw))
		require.NoError(f.t, json.Unmarshal(raw, &r.Metadata))
		out = append(out, r)
	}
	require.NoError(f.t, rows.Err())
	return out
}

func (f *fixture) auditActions() []string {
	var out []string
	for _, r := range f.auditRows() {
		out = append(out, r.Action)
	}
	return out
}

func (f *fixture) pool() *pgxpool.Pool { return f.p }

func status(t *testing.T, err error) (int, string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "not an apierr: %v", err)
	return ae.Status, ae.Code
}

func TestGuardMatrix(t *testing.T) {
	f := newFixture(t)
	d := Digest("deploy", f.svcID)
	cases := []struct {
		name     string
		ctx      context.Context
		prod     bool
		required bool
	}{
		{"session user, production", f.person(auth.RoleOwner), true, false},
		{"system, production", auth.WithPrincipal(context.Background(), auth.System(f.ws.ID, f.proj.ID, "git push")), true, false},
		{"token principal that carries no flag (authentication always sets it)", f.principal(auth.TypeToken), true, false},
		{"REST token as authenticated (no exemption), production", f.principal(auth.TypeToken, func(p *auth.Principal) { p.RequireProductionApproval = true }), true, true},
		{"REST token with the exemption, production", f.principal(auth.TypeToken, func(p *auth.Principal) { p.AllowProductionWithoutApproval = true }), true, false},
		{"MCP, production", f.principal(auth.TypeMCP), true, true},
		{"MCP with the knob (allow), production", f.principal(auth.TypeMCP, func(p *auth.Principal) { p.AllowProductionWithoutApproval = true }), true, false},
		{"MCP allow + require: require wins", f.principal(auth.TypeMCP, func(p *auth.Principal) { p.AllowProductionWithoutApproval, p.RequireProductionApproval = true, true }), true, true},
		{"MCP, non-production", f.principal(auth.TypeMCP), false, false},
		{"REST token with require, non-production", f.principal(auth.TypeToken, func(p *auth.Principal) { p.RequireProductionApproval = true }), false, false},
		{"no principal", context.Background(), true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(c.ctx, f.scope(c.prod, 1), "deploy", d)
			if !c.required {
				require.NoError(t, err)
				return
			}
			ar, ok := AsRequired(err)
			require.True(t, ok, "want approval required, got %v", err)
			require.Equal(t, "https://nebula.test/approvals/"+ar.ApprovalID.String(), ar.URL)
			require.Equal(t, "Deploy api to production", ar.Plan.Summary)
			require.WithinDuration(t, time.Now().Add(TTL), ar.ExpiresAt, 5*time.Second)
			st, code := status(t, err)
			require.Equal(t, http.StatusConflict, st)
			require.Equal(t, CodeApprovalRequired, code)
		})
	}
}

func TestFailsClosedWithoutGuard(t *testing.T) {
	f := newFixture(t)
	Uninstall()
	err := Check(f.principal(auth.TypeMCP), f.scope(true, 1), "deploy", Digest("deploy", f.svcID))
	st, code := status(t, err)
	require.Equal(t, http.StatusServiceUnavailable, st)
	require.Equal(t, CodeApprovalUnavailable, code)
	// A person is unaffected.
	require.NoError(t, Check(f.person(auth.RoleOwner), f.scope(true, 1), "deploy", Digest("deploy", f.svcID)))
}

func TestApprovalIsBoundAndSingleUse(t *testing.T) {
	f := newFixture(t)
	mcp := f.principal(auth.TypeMCP)
	d := Digest("deploy", f.svcID, "abc")

	err := Check(mcp, f.scope(true, 3), "deploy", d)
	ar, _ := AsRequired(err)
	require.NotNil(t, ar)
	// Asking again reuses the open request.
	ar2, _ := AsRequired(Check(mcp, f.scope(true, 3), "deploy", d))
	require.Equal(t, ar.ApprovalID, ar2.ApprovalID)

	v, err := f.g.Approve(f.person(auth.RoleDeveloper), ar.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, StatusApproved, v.Status)
	require.Equal(t, "Ada", v.DecidedBy)
	require.Equal(t, "web", v.DecidedVia)

	// Other arguments, another operation or another principal do not use it.
	_, ok := AsRequired(Check(mcp, f.scope(true, 3), "deploy", Digest("deploy", f.svcID, "other")))
	require.True(t, ok, "different arguments need their own approval")
	_, ok = AsRequired(Check(mcp, f.scope(true, 3), "rollback", d))
	require.True(t, ok, "a different operation needs its own approval")
	otherToken := uuid.New()
	_, ok = AsRequired(Check(f.principal(auth.TypeMCP, func(p *auth.Principal) { p.TokenID = &otherToken }), f.scope(true, 3), "deploy", d))
	require.True(t, ok, "another principal needs its own approval")

	// The bound call passes exactly once.
	require.NoError(t, Check(mcp, f.scope(true, 3), "deploy", d))
	_, ok = AsRequired(Check(mcp, f.scope(true, 3), "deploy", d))
	require.True(t, ok, "an approval is single use")

	row, err := f.q.GetApproval(context.Background(), db.GetApprovalParams{ID: ar.ApprovalID, WorkspaceID: f.ws.ID})
	require.NoError(t, err)
	require.Equal(t, StatusUsed, row.Status)
}

func TestApprovalVoidWhenGenerationMoves(t *testing.T) {
	f := newFixture(t)
	mcp := f.principal(auth.TypeMCP)
	d := Digest("deploy", f.svcID)
	ar, _ := AsRequired(Check(mcp, f.scope(true, 1), "deploy", d))
	_, err := f.g.Approve(f.person(auth.RoleOwner), ar.ApprovalID)
	require.NoError(t, err)

	// Someone edited the service after the approval: generation 1 → 2.
	ar2, ok := AsRequired(Check(mcp, f.scope(true, 2), "deploy", d))
	require.True(t, ok, "the approval must not apply to a changed service")
	require.NotEqual(t, ar.ApprovalID, ar2.ApprovalID)
	old, err := f.q.GetApproval(context.Background(), db.GetApprovalParams{ID: ar.ApprovalID, WorkspaceID: f.ws.ID})
	require.NoError(t, err)
	require.Equal(t, StatusStale, old.Status)
	require.Contains(t, f.auditActions(), AuditExpired)
}

func TestApprovalExpires(t *testing.T) {
	f := newFixture(t)
	mcp := f.principal(auth.TypeMCP)
	d := Digest("deploy", f.svcID)
	ar, _ := AsRequired(Check(mcp, f.scope(true, 1), "deploy", d))
	_, err := f.g.Approve(f.person(auth.RoleOwner), ar.ApprovalID)
	require.NoError(t, err)

	*f.offset = TTL + time.Minute
	ar2, ok := AsRequired(Check(mcp, f.scope(true, 1), "deploy", d))
	require.True(t, ok, "an approved request must not outlive its ten minutes")
	require.NotEqual(t, ar.ApprovalID, ar2.ApprovalID)
	old, err := f.q.GetApproval(context.Background(), db.GetApprovalParams{ID: ar.ApprovalID, WorkspaceID: f.ws.ID})
	require.NoError(t, err)
	require.Equal(t, StatusExpired, old.Status)
	require.Contains(t, f.auditActions(), AuditExpired)
}

func TestDeclinedCallStaysRefused(t *testing.T) {
	f := newFixture(t)
	mcp := f.principal(auth.TypeMCP)
	d := Digest("deploy", f.svcID)
	ar, _ := AsRequired(Check(mcp, f.scope(true, 1), "deploy", d))
	v, err := f.g.Decline(f.person(auth.RoleAdmin), ar.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, StatusDeclined, v.Status)

	st, code := status(t, Check(mcp, f.scope(true, 1), "deploy", d))
	require.Equal(t, http.StatusForbidden, st)
	require.Equal(t, CodeApprovalDeclined, code)

	// A declined request lives one minute, not ten: afterwards the call can ask again.
	*f.offset = DeclinedTTL + time.Second
	_, ok := AsRequired(Check(mcp, f.scope(true, 1), "deploy", d))
	require.True(t, ok)

	acts := f.auditActions()
	require.Contains(t, acts, AuditDeclined)
	require.Contains(t, acts, AuditRefused)
}

func TestAuditOutcomes(t *testing.T) {
	f := newFixture(t)
	mcp := f.principal(auth.TypeMCP)
	d := Digest("deploy", f.svcID)

	ar, _ := AsRequired(Check(mcp, f.scope(true, 1), "deploy", d))
	_, err := f.g.Approve(f.person(auth.RoleOwner), ar.ApprovalID)
	require.NoError(t, err)
	require.NoError(t, Check(mcp, f.scope(true, 1), "deploy", d))
	// error: a call that is not a valid guard call is refused and audited.
	require.Error(t, Check(mcp, f.scope(true, 1), "deploy", []byte("short")))

	acts := f.auditActions()
	for _, want := range []string{AuditRequested, AuditApproved, AuditUsed, AuditError} {
		require.Contains(t, acts, want)
	}
	// Rows name the actor: the call is the MCP principal's, the decision the person's.
	rows := f.auditRows()
	for _, r := range rows {
		switch r.Action {
		case AuditRequested, AuditUsed:
			require.Equal(t, "mcp", r.ActorType, r.Action)
			require.Equal(t, "claude-code", r.Metadata["client"])
		case AuditApproved:
			require.Equal(t, "user", r.ActorType)
			require.Equal(t, "web", r.Metadata["via"])
		}
	}
}

func TestCoveredContextBypasses(t *testing.T) {
	f := newFixture(t)
	mcp := Covered(f.principal(auth.TypeMCP))
	require.NoError(t, Check(mcp, f.scope(true, 1), "deploy", Digest("deploy", f.svcID)))
	require.False(t, Applies(mcp, true))
	require.True(t, Applies(f.principal(auth.TypeMCP), true))
	require.False(t, Applies(f.principal(auth.TypeMCP), false))
}

func TestApprovalPermissions(t *testing.T) {
	f := newFixture(t)
	mcp := f.principal(auth.TypeMCP)
	ar, _ := AsRequired(Check(mcp, f.scope(true, 1), "deploy", Digest("deploy", f.svcID)))
	id := ar.ApprovalID

	// Tokens and MCP principals never read or decide approvals.
	for _, ctx := range []context.Context{mcp, f.principal(auth.TypeToken, func(p *auth.Principal) { p.Scopes = policy.AllScopes })} {
		_, err := f.g.Get(ctx, id)
		st, _ := status(t, err)
		require.Equal(t, http.StatusForbidden, st)
		_, err = f.g.Approve(ctx, id)
		st, _ = status(t, err)
		require.Equal(t, http.StatusForbidden, st)
	}

	// A viewer reads the plan but cannot decide.
	viewer := f.person(auth.RoleViewer)
	v, err := f.g.Get(viewer, id)
	require.NoError(t, err)
	require.False(t, v.CanDecide)
	require.Equal(t, "ci-token", v.Requester.TokenName)
	require.Equal(t, "claude-code", v.Requester.ClientName)
	require.Equal(t, auth.TypeMCP, v.Requester.Kind)
	require.Equal(t, "Deploy api to production", v.Plan.Summary)
	_, err = f.g.Approve(viewer, id)
	st, _ := status(t, err)
	require.Equal(t, http.StatusForbidden, st)
	_, err = f.g.Decline(viewer, id)
	st, _ = status(t, err)
	require.Equal(t, http.StatusForbidden, st)

	// Another team, or a member restricted to another project, sees nothing.
	otherWS := f.person(auth.RoleOwner, func(p *auth.Principal) { p.WorkspaceID = uuid.New() })
	_, err = f.g.Get(otherWS, id)
	st, _ = status(t, err)
	require.Equal(t, http.StatusNotFound, st)
	restricted := f.person(auth.RoleDeveloper, func(p *auth.Principal) { p.ProjectIDs = []uuid.UUID{uuid.New()} })
	_, err = f.g.Approve(restricted, id)
	st, _ = status(t, err)
	require.Equal(t, http.StatusNotFound, st)
	_, err = f.g.Get(f.person(auth.RoleDeveloper), uuid.New())
	st, _ = status(t, err)
	require.Equal(t, http.StatusNotFound, st)

	// A developer decides once; a second decision is refused.
	dev := f.person(auth.RoleDeveloper)
	v, err = f.g.Get(dev, id)
	require.NoError(t, err)
	require.True(t, v.CanDecide)
	_, err = f.g.Approve(dev, id)
	require.NoError(t, err)
	_, err = f.g.Decline(dev, id)
	st, code := status(t, err)
	require.Equal(t, http.StatusConflict, st)
	require.Equal(t, CodeApprovalClosed, code)

	// An expired request cannot be decided.
	ar2, _ := AsRequired(Check(mcp, f.scope(true, 1), "deploy", Digest("deploy", f.svcID, "later")))
	*f.offset = TTL + time.Minute
	_, err = f.pool().Exec(context.Background(), "UPDATE approvals SET expires_at = now() - interval '1 minute' WHERE id = $1", ar2.ApprovalID)
	require.NoError(t, err)
	_, err = f.g.Approve(dev, ar2.ApprovalID)
	st, code = status(t, err)
	require.Equal(t, http.StatusConflict, st)
	require.Equal(t, CodeApprovalExpired, code)
	v, err = f.g.Get(dev, ar2.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, StatusExpired, v.Status)
	require.False(t, v.CanDecide)
}

// An approval is only ever given on the dashboard by a signed-in person: the
// record says so, and the requesting principal has no other way to settle it.
func TestApprovalIsDecidedViaTheWebOnly(t *testing.T) {
	f := newFixture(t)
	mcp := f.principal(auth.TypeMCP)
	d := Digest("deploy", f.svcID)
	ar, _ := AsRequired(Check(mcp, f.scope(true, 1), "deploy", d))

	_, err := f.g.Approve(mcp, ar.ApprovalID)
	st, _ := status(t, err)
	require.Equal(t, http.StatusForbidden, st, "the requesting principal cannot approve its own request")
	_, ok := AsRequired(Check(mcp, f.scope(true, 1), "deploy", d))
	require.True(t, ok, "a refused approval must not approve")

	_, err = f.g.Approve(f.person(auth.RoleOwner), ar.ApprovalID)
	require.NoError(t, err)
	row, err := f.q.GetApproval(context.Background(), db.GetApprovalParams{ID: ar.ApprovalID, WorkspaceID: f.ws.ID})
	require.NoError(t, err)
	require.Equal(t, StatusApproved, row.Status)
	require.Equal(t, "web", *row.DecidedVia)
	require.NoError(t, Check(mcp, f.scope(true, 1), "deploy", d))
}

func TestFromProblemRoundTrip(t *testing.T) {
	f := newFixture(t)
	err := Check(f.principal(auth.TypeMCP), f.scope(true, 1), "deploy", Digest("deploy", f.svcID))
	ar, _ := AsRequired(err)
	ae, ok := apierr.As(err)
	require.True(t, ok)
	body, err := json.Marshal(ae)
	require.NoError(t, err)

	back, ok := FromProblem(http.StatusConflict, body)
	require.True(t, ok)
	require.Equal(t, ar.ApprovalID, back.ApprovalID)
	require.Equal(t, ar.URL, back.URL)
	require.Equal(t, ar.Plan, back.Plan)
	_, ok = FromProblem(http.StatusForbidden, body)
	require.False(t, ok)
	_, ok = FromProblem(http.StatusConflict, []byte(`{"code":"deployment_in_progress"}`))
	require.False(t, ok)
}

// Uninstall removes the installed Guard. Check then refuses guarded calls.
func Uninstall() { active.Store(nil) }
