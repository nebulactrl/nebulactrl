package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// firstTeam is the team Setup created for r.user.
func (r *gpaRig) firstTeam() uuid.UUID {
	r.t.Helper()
	m, err := r.q.FirstMembership(context.Background(), r.user.ID)
	require.NoError(r.t, err)
	return m.WorkspaceID
}

// team creates another team owned by nobody and returns its id.
func (r *gpaRig) team(name string) uuid.UUID {
	r.t.Helper()
	ws, err := r.q.CreateWorkspace(context.Background(), name)
	require.NoError(r.t, err)
	return ws.ID
}

func (r *gpaRig) join(u *auth.User, team uuid.UUID, role string, projects ...uuid.UUID) {
	r.t.Helper()
	var ids []uuid.UUID
	if len(projects) > 0 {
		ids = projects
	}
	_, err := r.q.InsertMembership(context.Background(), db.InsertMembershipParams{UserID: u.ID, WorkspaceID: team, Role: role, ProjectIds: ids})
	require.NoError(r.t, err)
}

// apiToken stores an API token for u in team and returns its raw value.
func (r *gpaRig) apiToken(u *auth.User, team uuid.UUID, scopes []string, projects []uuid.UUID, exempt bool, expires *time.Time) (string, db.ApiToken) {
	r.t.Helper()
	raw, hash, prefix, err := auth.GenerateToken()
	require.NoError(r.t, err)
	row, err := r.q.CreateAPIToken(context.Background(), db.CreateAPITokenParams{
		WorkspaceID: team, UserID: u.ID, Name: "ci", TokenHash: hash, Prefix: prefix, Scopes: scopes, ProjectIds: projects,
		ExpiresAt: expires, AllowProductionWithoutApproval: exempt, Kind: auth.TokenKindREST,
	})
	require.NoError(r.t, err)
	return raw, row
}

func TestSetupHappensOnce(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := auth.NewService(pool, policy.ScopesForRole)
	ctx := context.Background()

	needs, err := svc.NeedsSetup(ctx)
	require.NoError(t, err)
	require.True(t, needs)

	_, err = svc.Setup(ctx, "ada@example.com", "short", "Ada", "")
	gpaRequireAPIError(t, err, 422, "weak_password")
	needs, _ = svc.NeedsSetup(ctx)
	require.True(t, needs, "a refused setup creates nothing")

	u, err := svc.Setup(ctx, "  Ada@Example.COM ", gpaPassword, "  Ada  ", "")
	require.NoError(t, err)
	require.Equal(t, "ada@example.com", u.Email)
	require.Equal(t, "Ada", u.Name)
	m, err := svc.MembershipIn(ctx, u.ID, mustFirstTeam(t, svc, u))
	require.NoError(t, err)
	require.Equal(t, auth.RoleOwner, m.Role)
	team, err := db.New(pool).FirstWorkspace(ctx)
	require.NoError(t, err)
	require.Equal(t, auth.DefaultTeamName, team.Name)
	admin, err := svc.IsInstanceAdmin(ctx, u.ID)
	require.NoError(t, err)
	require.True(t, admin)

	needs, err = svc.NeedsSetup(ctx)
	require.NoError(t, err)
	require.False(t, needs)
	_, err = svc.Setup(ctx, "second@example.com", gpaPassword, "Second", "")
	gpaRequireAPIError(t, err, 409, "already_setup")
	other, err := db.New(pool).InsertUser(ctx, db.InsertUserParams{Email: "later@example.com", Name: "L", PasswordHash: "x"})
	require.NoError(t, err)
	admin, err = svc.IsInstanceAdmin(ctx, other.ID)
	require.NoError(t, err)
	require.False(t, admin, "only the first account is the instance admin")
}

func mustFirstTeam(t *testing.T, svc *auth.Service, u *auth.User) uuid.UUID {
	t.Helper()
	p, err := svc.PrincipalForSession(context.Background(), u, nil)
	require.NoError(t, err)
	return p.WorkspaceID
}

func TestSetupAdoptsTheTeamTheInstallerCreated(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := auth.NewService(pool, policy.ScopesForRole)
	ctx := context.Background()

	ws, err := auth.BootstrapTeam(ctx, pool, "Installer team")
	require.NoError(t, err)
	again, err := auth.BootstrapTeam(ctx, pool, "Ignored")
	require.NoError(t, err)
	require.Equal(t, ws.ID, again.ID, "once a team exists nothing more is created")
	require.Equal(t, "Installer team", again.Name)
	_, err = auth.BootstrapTeam(ctx, pool, " ")
	require.NoError(t, err, "an existing team is returned without looking at the name")

	u, err := svc.Setup(ctx, "ada@example.com", gpaPassword, "Ada", "Chosen name")
	require.NoError(t, err)
	p, err := svc.PrincipalForSession(ctx, u, nil)
	require.NoError(t, err)
	require.Equal(t, ws.ID, p.WorkspaceID, "the first user owns the installer's team")
	require.Equal(t, auth.RoleOwner, p.Role)
	got, err := db.New(pool).GetWorkspace(ctx, ws.ID)
	require.NoError(t, err)
	require.Equal(t, "Chosen name", got.Name, "renamed to the name the user chose")
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM workspaces`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestBootstrapTeamRejectsABlankNameOnAnEmptyInstance(t *testing.T) {
	pool := testutil.NewPool(t)
	_, err := auth.BootstrapTeam(context.Background(), pool, "   ")
	gpaRequireAPIError(t, err, 422, "validation_failed")
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM workspaces`).Scan(&n))
	require.Zero(t, n)
}

func TestSetupKeepsTheInstallerTeamNameWhenTheUserChoosesTheSame(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := auth.NewService(pool, policy.ScopesForRole)
	ctx := context.Background()
	ws, err := auth.BootstrapTeam(ctx, pool, "Acme")
	require.NoError(t, err)
	u, err := svc.Setup(ctx, "ada@example.com", gpaPassword, "Ada", "  Acme ")
	require.NoError(t, err)
	p, err := svc.PrincipalForSession(ctx, u, nil)
	require.NoError(t, err)
	require.Equal(t, ws.ID, p.WorkspaceID)
	_, err = svc.Setup(ctx, "ada@example.com", gpaPassword, "Ada", "Acme")
	gpaRequireAPIError(t, err, 409, "already_setup")
}

func TestLoginProfileAndPasswordChange(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()

	u, err := r.svc.Login(ctx, "  ADA@example.com ", gpaPassword)
	require.NoError(t, err)
	require.Equal(t, r.user.ID, u.ID)
	for name, c := range map[string][2]string{
		"wrong password": {"ada@example.com", "not the password"},
		"unknown":        {"nobody@example.com", gpaPassword},
	} {
		_, err = r.svc.Login(ctx, c[0], c[1])
		require.ErrorIs(t, err, auth.ErrInvalidCredentials, name)
	}
	provider := r.newUser("sso@example.com", "")
	_, err = r.svc.Login(ctx, provider.Email, "")
	require.ErrorIs(t, err, auth.ErrInvalidCredentials, "an account without a password never signs in with one")

	got, err := r.svc.UserByID(ctx, r.user.ID)
	require.NoError(t, err)
	require.Equal(t, "ada@example.com", got.Email)
	_, err = r.svc.UserByID(ctx, uuid.New())
	require.ErrorIs(t, err, pgx.ErrNoRows)

	renamed, err := r.svc.UpdateProfile(ctx, r.user.ID, "  Ada Lovelace ")
	require.NoError(t, err)
	require.Equal(t, "Ada Lovelace", renamed.Name)

	gpaRequireAPIError(t, r.svc.ChangePassword(ctx, r.user.ID, gpaPassword, "short"), 422, "weak_password")
	gpaRequireAPIError(t, r.svc.ChangePassword(ctx, r.user.ID, "wrong current password", "a new long password"), 403, "invalid_credentials")
	_, err = r.svc.Login(ctx, "ada@example.com", gpaPassword)
	require.NoError(t, err, "a refused change keeps the old password")
	require.NoError(t, r.svc.ChangePassword(ctx, r.user.ID, gpaPassword, "a new long password"))
	_, err = r.svc.Login(ctx, "ada@example.com", gpaPassword)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
	_, err = r.svc.Login(ctx, "ada@example.com", "a new long password")
	require.NoError(t, err)

	require.NoError(t, r.svc.ConfirmPassword(ctx, r.user.ID, "a new long password"))
	gpaRequireAPIError(t, r.svc.ConfirmPassword(ctx, r.user.ID, gpaPassword), 422, "invalid_password")
	require.NoError(t, r.svc.ConfirmPassword(ctx, provider.ID, ""), "an account without a password passes")
	require.Error(t, r.svc.ConfirmPassword(ctx, uuid.New(), "x"))

	hash, err := r.svc.HashPassword("a perfectly fine password")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hash, "$argon2id$"))
	_, err = r.svc.HashPassword("short")
	gpaRequireAPIError(t, err, 422, "weak_password")
	gpaRequireAPIError(t, auth.ValidatePassword(strings.Repeat("x", 257)), 422, "weak_password")
	require.NoError(t, auth.ValidatePassword(strings.Repeat("x", 256)))
}

func TestPrincipalForSessionFollowsTheSessionsTeam(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	home := r.firstTeam()
	other := r.team("Other")
	bob := r.newUser("bob@example.com", gpaPassword)
	proj, err := r.q.CreateProject(ctx, db.CreateProjectParams{WorkspaceID: other, Name: "P", Slug: "p"})
	require.NoError(t, err)
	r.join(bob, home, auth.RoleViewer)
	r.join(bob, other, auth.RoleDeveloper, proj.ID)

	// The team in the session wins; a developer keeps the project restriction.
	p, err := r.svc.PrincipalForSession(ctx, bob, &other)
	require.NoError(t, err)
	require.Equal(t, auth.TypeUser, p.Type)
	require.Equal(t, other, p.WorkspaceID)
	require.Equal(t, auth.RoleDeveloper, p.Role)
	require.Equal(t, []uuid.UUID{proj.ID}, p.ProjectIDs)
	require.True(t, p.Restricted())
	require.True(t, p.CanSeeProject(proj.ID))
	require.False(t, p.CanSeeProject(uuid.New()))
	require.Equal(t, "bob@example.com", p.Label)
	require.Equal(t, bob.ID, p.ID())
	require.Equal(t, policy.ScopesForRole(auth.RoleDeveloper), p.Scopes)

	// Without one (or a team they left) the oldest membership.
	p, err = r.svc.PrincipalForSession(ctx, bob, nil)
	require.NoError(t, err)
	require.Equal(t, home, p.WorkspaceID)
	require.Equal(t, auth.RoleViewer, p.Role)
	gone := uuid.New()
	p, err = r.svc.PrincipalForSession(ctx, bob, &gone)
	require.NoError(t, err)
	require.Equal(t, home, p.WorkspaceID)
	require.False(t, p.Restricted())
	require.True(t, p.CanSeeProject(uuid.New()))

	// Owners and admins see every project whatever their row says.
	r.exec(`UPDATE memberships SET role='admin', project_ids=$3 WHERE user_id=$1 AND workspace_id=$2`, bob.ID, other, []uuid.UUID{proj.ID})
	p, err = r.svc.PrincipalForSession(ctx, bob, &other)
	require.NoError(t, err)
	require.Nil(t, p.ProjectIDs)

	// A user in no team has no workspace and no scopes.
	loner := r.newUser("loner@example.com", gpaPassword)
	p, err = r.svc.PrincipalForSession(ctx, loner, nil)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, p.WorkspaceID)
	require.Empty(t, p.Scopes)
	require.NotNil(t, p.Scopes)
	require.Empty(t, p.Role)

	// Membership and team switching.
	m, err := r.svc.MembershipIn(ctx, bob.ID, other)
	require.NoError(t, err)
	require.Equal(t, auth.RoleAdmin, m.Role)
	m, err = r.svc.MembershipIn(ctx, loner.ID, other)
	require.NoError(t, err)
	require.Nil(t, m)
	m, err = r.svc.SwitchTeam(ctx, bob.ID, home)
	require.NoError(t, err)
	require.Equal(t, home, m.WorkspaceID)
	m, err = r.svc.SwitchTeam(ctx, loner.ID, home)
	require.NoError(t, err)
	require.Nil(t, m, "a non-member cannot switch into a team")

	// Events are routed by the team that owns the id.
	team, ok := r.svc.EventWorkspace(ctx, proj.ID)
	require.True(t, ok)
	require.Equal(t, other, team)
	_, ok = r.svc.EventWorkspace(ctx, uuid.New())
	require.False(t, ok)
}

func TestAuthenticateBearer(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	home := r.firstTeam()
	bob := r.newUser("bob@example.com", gpaPassword)
	r.join(bob, home, auth.RoleDeveloper)
	proj, err := r.q.CreateProject(ctx, db.CreateProjectParams{WorkspaceID: home, Name: "P", Slug: "p"})
	require.NoError(t, err)

	// A token carries the scopes it was given, cut down to its creator's role.
	raw, row := r.apiToken(bob, home, []string{policy.ScopeProjectsRead, policy.ScopeAdmin + "x", policy.ScopeProjectsWrite}, nil, false, nil)
	p, err := r.svc.AuthenticateBearer(ctx, raw)
	require.NoError(t, err)
	require.Equal(t, auth.TypeToken, p.Type)
	require.Equal(t, bob.ID, p.UserID)
	require.Equal(t, row.ID, *p.TokenID)
	require.Equal(t, home, p.WorkspaceID)
	require.Equal(t, auth.RoleDeveloper, p.Role)
	require.Equal(t, "ci", p.Label)
	require.Equal(t, auth.TokenKindREST, p.TokenKind)
	require.True(t, p.RequireProductionApproval)
	require.False(t, p.AllowProductionWithoutApproval)
	require.Contains(t, p.Scopes, policy.ScopeProjectsRead)
	require.NotContains(t, p.Scopes, policy.ScopeAdmin+"x")
	require.Nil(t, p.ProjectIDs)

	// The admin scope stands for everything the creator's role holds; a viewer's token is no stronger than a viewer.
	adminTok, _ := r.apiToken(bob, home, []string{policy.ScopeAdmin}, nil, false, nil)
	p, err = r.svc.AuthenticateBearer(ctx, adminTok)
	require.NoError(t, err)
	require.ElementsMatch(t, policy.ScopesForRole(auth.RoleDeveloper), p.Scopes)
	r.exec(`UPDATE memberships SET role='viewer' WHERE user_id=$1 AND workspace_id=$2`, bob.ID, home)
	p, err = r.svc.AuthenticateBearer(ctx, adminTok)
	require.NoError(t, err)
	require.ElementsMatch(t, policy.ScopesForRole(auth.RoleViewer), p.Scopes, "a demoted member's token loses power at once")
	r.exec(`UPDATE memberships SET role='developer', project_ids=$3 WHERE user_id=$1 AND workspace_id=$2`, bob.ID, home, []uuid.UUID{proj.ID})
	scoped, _ := r.apiToken(bob, home, []string{policy.ScopeProjectsRead}, []uuid.UUID{proj.ID}, false, nil)
	p, err = r.svc.AuthenticateBearer(ctx, scoped)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{proj.ID}, p.ProjectIDs)

	// The production-guard exemption only holds for an admin, owner or the instance admin.
	exempt, _ := r.apiToken(bob, home, []string{policy.ScopeProjectsRead}, nil, true, nil)
	p, err = r.svc.AuthenticateBearer(ctx, exempt)
	require.NoError(t, err)
	require.True(t, p.RequireProductionApproval, "a developer's exemption is not honoured")
	require.False(t, p.AllowProductionWithoutApproval)
	r.exec(`UPDATE memberships SET role='admin' WHERE user_id=$1 AND workspace_id=$2`, bob.ID, home)
	p, err = r.svc.AuthenticateBearer(ctx, exempt)
	require.NoError(t, err)
	require.True(t, p.AllowProductionWithoutApproval)
	require.False(t, p.RequireProductionApproval)
	adaTok, _ := r.apiToken(r.user, home, []string{policy.ScopeProjectsRead}, nil, true, nil)
	p, err = r.svc.AuthenticateBearer(ctx, adaTok)
	require.NoError(t, err)
	require.True(t, p.AllowProductionWithoutApproval)
	// The instance admin as a mere developer of another team keeps the exemption.
	other := r.team("Other")
	r.join(r.user, other, auth.RoleViewer)
	adaOther, _ := r.apiToken(r.user, other, []string{policy.ScopeProjectsRead}, nil, true, nil)
	p, err = r.svc.AuthenticateBearer(ctx, adaOther)
	require.NoError(t, err)
	require.True(t, p.AllowProductionWithoutApproval)

	// Everything else is the same 401.
	soon := time.Now().Add(time.Hour)
	expiring, _ := r.apiToken(bob, home, []string{policy.ScopeProjectsRead}, nil, false, &soon)
	p, err = r.svc.AuthenticateBearer(ctx, expiring)
	require.NoError(t, err)
	require.WithinDuration(t, soon, *p.TokenExpiresAt, time.Second)
	past := time.Now().Add(-time.Minute)
	expired, _ := r.apiToken(bob, home, []string{policy.ScopeProjectsRead}, nil, false, &past)
	revoked, revokedRow := r.apiToken(bob, home, []string{policy.ScopeProjectsRead}, nil, false, nil)
	r.exec(`UPDATE api_tokens SET revoked_at=now() WHERE id=$1`, revokedRow.ID)
	outsider := r.newUser("out@example.com", gpaPassword)
	notMember, _ := r.apiToken(outsider, home, []string{policy.ScopeProjectsRead}, nil, false, nil)
	for name, tok := range map[string]string{
		"empty": "", "wrong prefix": "xyz_" + strings.Repeat("a", 52), "unknown": auth.TokenPrefix + strings.Repeat("a", 52),
		"too long": auth.TokenPrefix + strings.Repeat("a", 200), "expired": expired, "revoked": revoked, "creator left the team": notMember,
	} {
		_, err = r.svc.AuthenticateBearer(ctx, tok)
		require.ErrorIs(t, err, auth.ErrInvalidToken, name)
	}
}

func TestPrincipalHelpers(t *testing.T) {
	id, tok := uuid.New(), uuid.New()
	require.Equal(t, id, (&auth.Principal{UserID: id}).ID())
	require.Equal(t, tok, (&auth.Principal{UserID: id, TokenID: &tok}).ID(), "a token principal is identified by the token")

	ws, prj := uuid.New(), uuid.New()
	sys := auth.System(ws, prj, "deploys", policy.ScopeProjectsRead)
	require.Equal(t, auth.TypeSystem, sys.Type)
	require.Equal(t, ws, sys.WorkspaceID)
	require.Equal(t, "deploys", sys.Label)
	require.Equal(t, []string{policy.ScopeProjectsRead}, sys.Scopes)
	require.True(t, sys.Restricted())
	require.True(t, sys.CanSeeProject(prj))
	require.False(t, sys.CanSeeProject(uuid.New()))

	_, ok := auth.PrincipalFrom(context.Background())
	require.False(t, ok)
	_, ok = auth.PrincipalFrom(auth.WithPrincipal(context.Background(), nil))
	require.False(t, ok, "a nil principal is no principal")
	got, ok := auth.PrincipalFrom(auth.WithPrincipal(context.Background(), sys))
	require.True(t, ok)
	require.Same(t, sys, got)

	require.True(t, auth.ValidTokenKind(auth.TokenKindMCP))
	require.True(t, auth.ValidTokenKind(auth.TokenKindREST))
	require.False(t, auth.ValidTokenKind(""))
	require.False(t, auth.ValidTokenKind("other"))
}

func TestOperatorRecovery(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	find := func(email string) (*auth.OperatorAccount, error) {
		var acct *auth.OperatorAccount
		err := r.inTx(func(tx pgx.Tx) error {
			var err error
			acct, err = r.svc.OperatorFind(ctx, tx, email)
			return err
		})
		return acct, err
	}

	acct, err := find("  ADA@example.com ")
	require.NoError(t, err)
	require.Equal(t, r.user.ID, acct.ID)
	require.True(t, acct.HasPassword)
	require.Equal(t, []uuid.UUID{r.firstTeam()}, acct.Teams)
	_, err = find("nobody@example.com")
	require.ErrorIs(t, err, auth.ErrNoAccount)

	// A reset link: single use, 30 minutes, nobody recorded as its creator.
	var raw string
	require.NoError(t, r.inTx(func(tx pgx.Tx) error {
		var expires time.Time
		var err error
		raw, expires, err = r.svc.OperatorResetLink(ctx, tx, acct)
		require.WithinDuration(t, r.clock.Add(auth.ResetTTL), expires, time.Second)
		return err
	}))
	require.NoError(t, r.svc.CheckResetToken(ctx, raw))
	require.Equal(t, 1, r.countRows(`SELECT count(*) FROM password_reset_tokens WHERE user_id=$1 AND created_by IS NULL AND used_at IS NULL`, r.user.ID))

	// Setting the password directly kills every outstanding link and the old password.
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { return r.svc.OperatorSetPassword(ctx, tx, acct, "operator chosen password") }))
	require.ErrorIs(t, r.svc.CheckResetToken(ctx, raw), auth.ErrInvalidResetToken)
	_, err = r.svc.Login(ctx, "ada@example.com", "operator chosen password")
	require.NoError(t, err)
	_, err = r.svc.Login(ctx, "ada@example.com", gpaPassword)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
	gpaRequireAPIError(t, r.inTx(func(tx pgx.Tx) error { return r.svc.OperatorSetPassword(ctx, tx, acct, "short") }), 422, "weak_password")

	// Accounts without a password cannot get a link.
	sso := r.newUser("sso@example.com", "")
	ssoAcct, err := find("sso@example.com")
	require.NoError(t, err)
	require.False(t, ssoAcct.HasPassword)
	err = r.inTx(func(tx pgx.Tx) error { _, _, err := r.svc.OperatorResetLink(ctx, tx, ssoAcct); return err })
	require.ErrorIs(t, err, auth.ErrNoPassword)

	// Second factors: what is held, and clearing it.
	empty, err := r.secondFactors(acct)
	require.NoError(t, err)
	require.True(t, empty.None())
	_, codes := r.enrollTOTP(r.user)
	r.insertPasskey(r.user, "Key")
	held, err := r.secondFactors(acct)
	require.NoError(t, err)
	require.True(t, held.TOTP)
	require.Equal(t, 1, held.Passkeys)
	require.Equal(t, len(codes), held.RecoveryCodes)
	require.NotZero(t, held.RecoveryCodes)
	require.False(t, held.None())
	var cleared auth.SecondFactorSummary
	require.NoError(t, r.inTx(func(tx pgx.Tx) error {
		var err error
		cleared, err = r.svc.OperatorClearSecondFactors(ctx, tx, acct)
		return err
	}))
	require.Equal(t, held, cleared, "the summary says what was removed")
	after, err := r.secondFactors(acct)
	require.NoError(t, err)
	require.True(t, after.None())
	has, err := r.q.UserHasSecondFactor(ctx, r.user.ID)
	require.NoError(t, err)
	require.False(t, has)

	// An account whose passkeys are its only way in keeps them.
	r.insertPasskey(sso, "Only")
	err = r.inTx(func(tx pgx.Tx) error { _, err := r.svc.OperatorClearSecondFactors(ctx, tx, ssoAcct); return err })
	require.ErrorIs(t, err, auth.ErrOnlySignInMethod)
	list, err := r.svc.ListPasskeys(ctx, sso.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	// A linked provider is another way in, so they may go.
	p := r.provider(gpaProviderOpts{Kind: auth.KindGitHub})
	_, err = r.q.CreateUserIdentity(ctx, db.CreateUserIdentityParams{UserID: sso.ID, ProviderID: p.ID, Subject: "7", Email: sso.Email})
	require.NoError(t, err)
	require.NoError(t, r.inTx(func(tx pgx.Tx) error { _, err := r.svc.OperatorClearSecondFactors(ctx, tx, ssoAcct); return err }))
	list, err = r.svc.ListPasskeys(ctx, sso.ID)
	require.NoError(t, err)
	require.Empty(t, list)
}

func (r *gpaRig) secondFactors(acct *auth.OperatorAccount) (auth.SecondFactorSummary, error) {
	var out auth.SecondFactorSummary
	err := r.inTx(func(tx pgx.Tx) error {
		var err error
		out, err = r.svc.OperatorSecondFactors(context.Background(), tx, acct)
		return err
	})
	return out, err
}

func (r *gpaRig) countRows(sql string, args ...any) int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func TestOperatorFindOnAnEmptyInstance(t *testing.T) {
	pool := testutil.NewPool(t)
	svc := auth.NewService(pool, policy.ScopesForRole)
	err := svc.InTx(context.Background(), func(tx pgx.Tx) error {
		_, err := svc.OperatorFind(context.Background(), tx, "ada@example.com")
		return err
	})
	require.ErrorIs(t, err, auth.ErrSetupPending)
}

func TestAccountSecurityHelpers(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	home := r.firstTeam()
	other := r.team("Second team")
	r.join(r.user, other, auth.RoleDeveloper)

	// Live tokens across teams; revoking ends them all.
	r.apiToken(r.user, home, []string{policy.ScopeProjectsRead}, nil, false, nil)
	r.apiToken(r.user, other, []string{policy.ScopeProjectsRead}, nil, false, nil)
	past := time.Now().Add(-time.Hour)
	r.apiToken(r.user, home, []string{policy.ScopeProjectsRead}, nil, false, &past)
	_, dead := r.apiToken(r.user, home, []string{policy.ScopeProjectsRead}, nil, false, nil)
	r.exec(`UPDATE api_tokens SET revoked_at=now() WHERE id=$1`, dead.ID)
	live, err := r.svc.LiveTokens(ctx, r.user.ID)
	require.NoError(t, err)
	require.Len(t, live, 2, "expired and revoked tokens are not listed")
	teams := map[uuid.UUID]string{}
	for _, tk := range live {
		teams[tk.TeamID] = tk.TeamName
		require.Equal(t, "ci", tk.Name)
		require.NotEmpty(t, tk.Prefix)
	}
	require.Equal(t, "Second team", teams[other])
	require.Len(t, teams, 2)
	var n int
	require.NoError(t, r.inTx(func(tx pgx.Tx) error {
		var err error
		n, err = r.svc.RevokeLiveTokens(ctx, tx, r.user.ID)
		return err
	}))
	require.Equal(t, 3, n, "every token that was not yet revoked, expired ones included")
	live, err = r.svc.LiveTokens(ctx, r.user.ID)
	require.NoError(t, err)
	require.Empty(t, live)
	bobLive, err := r.svc.LiveTokens(ctx, uuid.New())
	require.NoError(t, err)
	require.Empty(t, bobLive)

	// A reset link can be checked without being spent.
	var raw string
	require.NoError(t, r.inTx(func(tx pgx.Tx) error {
		var err error
		raw, _, err = r.svc.CreateResetToken(ctx, tx, r.user.ID, r.user.ID)
		return err
	}))
	require.NoError(t, r.svc.CheckResetToken(ctx, raw))
	require.NoError(t, r.svc.CheckResetToken(ctx, raw), "checking does not spend it")
	require.NoError(t, r.svc.CheckResetToken(ctx, "  "+raw+"  "))
	require.ErrorIs(t, r.svc.CheckResetToken(ctx, ""), auth.ErrInvalidResetToken)
	require.ErrorIs(t, r.svc.CheckResetToken(ctx, auth.ResetTokenPrefix+"nope"), auth.ErrInvalidResetToken)
	r.advance(auth.ResetTTL + time.Minute)
	require.ErrorIs(t, r.svc.CheckResetToken(ctx, raw), auth.ErrInvalidResetToken, "an expired link says so up front")
}

func TestProviderLinkPendingAndSessionRevocation(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	p := r.oidcProvider(idp, gpaProviderOpts{})

	ctx := r.signedIn(r.user)
	require.False(t, auth.ProviderLinkPending(ctx, r.sm))
	_, err := r.svc.BeginProviderLogin(ctx, r.sm, p.ID, gpaBase, "")
	require.NoError(t, err)
	require.False(t, auth.ProviderLinkPending(ctx, r.sm), "a plain sign-in is no link")
	_, err = r.svc.BeginProviderLink(ctx, r.sm, p.ID, gpaBase, r.user.ID)
	require.NoError(t, err)
	require.True(t, auth.ProviderLinkPending(ctx, r.sm))

	// Ending the other sessions of a user keeps the one that asked.
	keep, other, bobs := r.signedIn(r.user), r.signedIn(r.user), r.signedIn(r.newUser("bob@example.com", gpaPassword))
	// scs' in-memory store only knows sessions that were committed.
	for _, c := range []context.Context{keep, other, bobs} {
		tok, _, err := r.sm.Commit(c)
		require.NoError(t, err)
		require.NotEmpty(t, tok)
	}
	require.NoError(t, auth.RevokeOtherUserSessions(keep, r.sm, r.user.ID))
	_, found, err := r.sm.Store.Find(r.sm.Token(keep))
	require.NoError(t, err)
	require.True(t, found, "the session that changed the password stays")
	_, found, err = r.sm.Store.Find(r.sm.Token(other))
	require.NoError(t, err)
	require.False(t, found, "another browser of the same user is signed out")
	_, found, err = r.sm.Store.Find(r.sm.Token(bobs))
	require.NoError(t, err)
	require.True(t, found, "other users are untouched")
}

func TestTeamTwoFactorBlocking(t *testing.T) {
	r := newGpaRig(t)
	ctx := context.Background()
	home := r.firstTeam()
	r.exec(`UPDATE workspaces SET require_two_factor=true WHERE id=$1`, home)
	bob := r.newUser("bob@example.com", gpaPassword)
	r.join(bob, home, auth.RoleDeveloper)

	required, has, err := r.svc.TeamTwoFactorState(ctx, home, bob.ID)
	require.NoError(t, err)
	require.True(t, required)
	require.False(t, has)
	required, _, err = r.svc.TeamTwoFactorState(ctx, uuid.New(), bob.ID)
	require.NoError(t, err)
	require.False(t, required, "an unknown team requires nothing")

	user := &auth.Principal{Type: auth.TypeUser, UserID: bob.ID, WorkspaceID: home}
	tok := &auth.Principal{Type: auth.TypeToken, UserID: bob.ID, WorkspaceID: home}
	sys := &auth.Principal{Type: auth.TypeSystem, WorkspaceID: home}
	blocked, err := r.svc.SecondFactorBlocked(r.session(), r.sm, user)
	require.NoError(t, err)
	require.True(t, blocked)
	blocked, err = r.svc.SecondFactorBlocked(r.session(), r.sm, tok)
	require.NoError(t, err)
	require.True(t, blocked, "a token of a member without a second factor is held back too")
	for name, p := range map[string]*auth.Principal{"nil": nil, "system": sys, "no team": {Type: auth.TypeUser, UserID: bob.ID}, "no user": {Type: auth.TypeUser, WorkspaceID: home}} {
		blocked, err = r.svc.SecondFactorBlocked(r.session(), r.sm, p)
		require.NoError(t, err, name)
		require.False(t, blocked, name)
	}

	// A session that proved a second factor passes (a token cannot).
	mfaCtx := r.session()
	r.sm.Put(mfaCtx, auth.SessionMFAKey, true)
	blocked, err = r.svc.SecondFactorBlocked(mfaCtx, r.sm, user)
	require.NoError(t, err)
	require.False(t, blocked)
	blocked, err = r.svc.SecondFactorBlocked(mfaCtx, r.sm, tok)
	require.NoError(t, err)
	require.True(t, blocked)

	// The same rule for an action on a given team; a signed MFA principal passes.
	require.ErrorIs(t, r.svc.RequireSecondFactorIn(ctx, user, home), auth.ErrTwoFactorEnrollmentRequired)
	require.NoError(t, r.svc.RequireSecondFactorIn(ctx, user, uuid.New()))
	require.NoError(t, r.svc.RequireSecondFactorIn(ctx, sys, home))
	user.SessionMFA = true
	require.NoError(t, r.svc.RequireSecondFactorIn(ctx, user, home))
	user.SessionMFA = false

	// Enrolling TOTP lifts the block.
	r.enrollTOTP(bob)
	blocked, err = r.svc.SecondFactorBlocked(r.session(), r.sm, user)
	require.NoError(t, err)
	require.False(t, blocked)
	require.NoError(t, r.svc.RequireSecondFactorIn(ctx, user, home))
}

func TestTestIssuerReadsTheDiscoveryDocument(t *testing.T) {
	r := newGpaRig(t)
	idp := newFakeIdP(t)
	got, err := r.svc.TestIssuer(context.Background(), idp.srv.URL)
	require.NoError(t, err)
	require.Equal(t, idp.srv.URL, got.Issuer)
	require.Equal(t, idp.srv.URL+"/token", got.TokenEndpoint)

	_, err = r.svc.TestIssuer(context.Background(), "idp.example/realm")
	require.Error(t, err)

	// The HTTP client used for providers can be replaced.
	called := false
	r.svc.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return http.DefaultTransport.RoundTrip(req)
	})})
	_, err = r.svc.TestIssuer(context.Background(), idp.srv.URL)
	require.NoError(t, err)
	require.True(t, called)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
