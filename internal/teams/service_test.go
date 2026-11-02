package teams

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

const covPassword = "correct horse battery"

// sentMail is one call of fakeMailer.SendInvite.
type sentMail struct {
	team  uuid.UUID
	relay *Relay
	mail  InviteMail
}

// fakeMailer records invite emails and fails on demand.
type fakeMailer struct {
	sent []sentMail
	err  error
}

func (m *fakeMailer) SendInvite(_ context.Context, teamID uuid.UUID, relay *Relay, in InviteMail) error {
	m.sent = append(m.sent, sentMail{team: teamID, relay: relay, mail: in})
	return m.err
}

// tf is a migrated schema with the instance admin (ada, owner of the first
// team) and a Service wired to the real auth service.
type tf struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	au     *auth.Service
	svc    *Service
	mailer *fakeMailer
	box    *secretbox.Box
	now    time.Time
	ada    *auth.User
	team   uuid.UUID
}

func newTf(t *testing.T) *tf {
	t.Helper()
	pool := testutil.NewPool(t)
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	require.NoError(t, err)
	au := auth.NewService(pool, policy.ScopesForRole)
	f := &tf{t: t, pool: pool, q: db.New(pool), au: au, mailer: &fakeMailer{}, box: box, now: time.Now()}
	au.Configure(box, "")
	f.svc = NewService(Deps{Pool: pool, Auth: au, Box: box, Mailer: f.mailer, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))})
	f.svc.now = func() time.Time { return f.now }
	f.ada, err = au.Setup(context.Background(), "ada@example.com", covPassword, "Ada", "Home")
	require.NoError(t, err)
	m, err := f.q.FirstMembership(context.Background(), f.ada.ID)
	require.NoError(t, err)
	f.team = m.WorkspaceID
	return f
}

// withPublicURL makes invite links absolute.
func (f *tf) withPublicURL(u string) { f.svc.publicURL = strings.TrimRight(u, "/") }

// user inserts an account that has a password (so it can be reset) and joins
// it to the first team with role.
func (f *tf) member(email, role string, projects ...uuid.UUID) uuid.UUID {
	f.t.Helper()
	id := f.user(email, "x")
	f.join(f.team, id, role, projects...)
	return id
}

func (f *tf) user(email, passwordHash string) uuid.UUID {
	f.t.Helper()
	u, err := f.q.InsertUser(context.Background(), db.InsertUserParams{Email: email, Name: strings.Split(email, "@")[0], PasswordHash: passwordHash})
	require.NoError(f.t, err)
	return u.ID
}

func (f *tf) join(team, user uuid.UUID, role string, projects ...uuid.UUID) {
	f.t.Helper()
	var ids []uuid.UUID
	if len(projects) > 0 {
		ids = projects
	}
	_, err := f.q.InsertMembership(context.Background(), db.InsertMembershipParams{UserID: user, WorkspaceID: team, Role: role, ProjectIds: ids})
	require.NoError(f.t, err)
}

func (f *tf) project(name string) uuid.UUID {
	f.t.Helper()
	p, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: f.team, Name: name, Slug: strings.ToLower(name)})
	require.NoError(f.t, err)
	return p.ID
}

// as is a signed-in user acting in team.
func (f *tf) as(user, team uuid.UUID) context.Context {
	u, err := f.q.GetUser(context.Background(), user)
	require.NoError(f.t, err)
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeUser, UserID: user, WorkspaceID: team, Label: u.Name, Scopes: policy.AllScopes,
	})
}

func (f *tf) asAda() context.Context { return f.as(f.ada.ID, f.team) }

// token is an API token principal of a member (role as the token's creator).
func (f *tf) token(user uuid.UUID, team uuid.UUID, role string, scopes ...string) context.Context {
	tok := uuid.New()
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Type: auth.TypeToken, UserID: user, TokenID: &tok, WorkspaceID: team, Role: role, Label: "ci", Scopes: scopes,
	})
}

func (f *tf) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(f.t, err)
}

func (f *tf) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (f *tf) audits(action string) int {
	f.t.Helper()
	return f.count(`SELECT count(*) FROM audit_events WHERE action=$1`, action)
}

func (f *tf) roleOf(user uuid.UUID) string {
	f.t.Helper()
	m, err := f.q.GetMembership(context.Background(), db.GetMembershipParams{UserID: user, WorkspaceID: f.team})
	require.NoError(f.t, err)
	return m.Role
}

func (f *tf) isMember(user, team uuid.UUID) bool {
	f.t.Helper()
	_, err := f.q.GetMembership(context.Background(), db.GetMembershipParams{UserID: user, WorkspaceID: team})
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	require.NoError(f.t, err)
	return true
}

func requireAPI(t *testing.T, err error, status int, code string) *apierr.Error {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	require.Equal(t, status, ae.Status, ae.Message)
	require.Equal(t, code, ae.Code, ae.Message)
	return ae
}

func TestCreateTeamMakesTheCallerItsOwner(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleDeveloper)

	team, err := f.svc.Create(f.as(bob, f.team), "  Side Project  ")
	require.NoError(t, err)
	require.Equal(t, "Side Project", team.Name)
	require.Equal(t, "side-project", team.Slug)
	require.Equal(t, auth.RoleOwner, team.Role, "the creator owns the new team")
	require.Equal(t, 1, team.MemberCount)
	require.False(t, team.Current, "creating a team does not switch to it")
	require.True(t, f.isMember(bob, team.ID))
	require.Equal(t, 1, f.audits("team.create"))

	// A second team with the same name gets another slug.
	again, err := f.svc.Create(f.as(bob, f.team), "Side Project")
	require.NoError(t, err)
	require.NotEqual(t, team.Slug, again.Slug)
	require.True(t, strings.HasPrefix(again.Slug, "side-project"))

	for _, bad := range []string{"", "   ", strings.Repeat("n", auth.MaxTeamNameLength+1)} {
		_, err = f.svc.Create(f.asAda(), bad)
		requireAPI(t, err, 422, "validation_failed")
	}
}

func TestCreateTeamCallers(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)

	// An admin-scope token creates a team; the token's creator owns it.
	team, err := f.svc.Create(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeAdmin), "By Token")
	require.NoError(t, err)
	require.True(t, f.isMember(bob, team.ID))
	require.Equal(t, auth.RoleOwner, team.Role)

	// A token without the admin scope, an MCP principal and anonymous callers cannot.
	_, err = f.svc.Create(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeProjectsRead), "Nope")
	requireAPI(t, err, 403, "forbidden")
	mcp := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeMCP, UserID: bob, WorkspaceID: f.team, Role: auth.RoleAdmin, Scopes: []string{policy.ScopeAdmin}})
	_, err = f.svc.Create(mcp, "Nope")
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.Create(context.Background(), "Nope")
	requireAPI(t, err, 401, "unauthorized")
	require.Zero(t, f.count(`SELECT count(*) FROM workspaces WHERE name='Nope'`))
}

func TestCreateTeamHonoursTheInstanceSetting(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleOwner)
	no := false
	_, err := f.svc.UpdateInstance(f.asAda(), InstancePatch{AllowTeamCreation: &no})
	require.NoError(t, err)

	_, err = f.svc.Create(f.as(bob, f.team), "Blocked")
	requireAPI(t, err, 403, "team_creation_disabled")
	require.Zero(t, f.count(`SELECT count(*) FROM workspaces WHERE name='Blocked'`))
	// The instance admin always may.
	_, err = f.svc.Create(f.asAda(), "Allowed")
	require.NoError(t, err)
}

func TestListGetRenameAndSwitchTeams(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleDeveloper)
	other, err := f.svc.Create(f.as(bob, f.team), "Bob's Own")
	require.NoError(t, err)

	// A user's teams, oldest first, the acting one flagged.
	list, err := f.svc.List(f.as(bob, f.team))
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, f.team, list[0].ID)
	require.Equal(t, auth.RoleDeveloper, list[0].Role)
	require.True(t, list[0].Current)
	require.False(t, list[1].Current)
	require.Equal(t, other.ID, list[1].ID)
	require.Equal(t, 2, list[0].MemberCount)
	// The list of a token is its creator's.
	list, err = f.svc.List(f.token(bob, f.team, auth.RoleDeveloper, policy.ScopeProjectsRead))
	require.NoError(t, err)
	require.Len(t, list, 2)
	_, err = f.svc.List(context.Background())
	requireAPI(t, err, 401, "unauthorized")

	// Get: members only; others learn nothing about the team.
	got, err := f.svc.Get(f.as(bob, f.team), f.team)
	require.NoError(t, err)
	require.Equal(t, "Home", got.Name)
	_, err = f.svc.Get(f.as(bob, f.team), uuid.New())
	requireAPI(t, err, 404, "not_found")
	stranger := f.user("eve@example.com", "")
	_, err = f.svc.Get(f.as(stranger, f.team), f.team)
	requireAPI(t, err, 404, "not_found")
	// A token reaches only its own team.
	_, err = f.svc.Get(f.token(bob, f.team, auth.RoleDeveloper, policy.ScopeProjectsRead), other.ID)
	requireAPI(t, err, 404, "not_found")
	_, err = f.svc.Get(f.token(bob, f.team, "", policy.ScopeProjectsRead), f.team)
	requireAPI(t, err, 404, "not_found")

	// Rename: admins and owners; the slug stays.
	renamed, err := f.svc.Rename(f.asAda(), f.team, "  Headquarters ")
	require.NoError(t, err)
	require.Equal(t, "Headquarters", renamed.Name)
	require.Equal(t, got.Slug, renamed.Slug)
	require.Equal(t, 1, f.audits("team.update"))
	_, err = f.svc.Rename(f.as(bob, f.team), f.team, "Mine now")
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.Rename(f.asAda(), f.team, " ")
	requireAPI(t, err, 422, "validation_failed")
	_, err = f.svc.Rename(f.as(stranger, f.team), f.team, "Mine now")
	requireAPI(t, err, 404, "not_found")
	// A token needs the admin scope even when its creator is an owner.
	_, err = f.svc.Rename(f.token(f.ada.ID, f.team, auth.RoleOwner, policy.ScopeProjectsWrite), f.team, "Nope")
	requireAPI(t, err, 403, "forbidden")
	byToken, err := f.svc.Rename(f.token(f.ada.ID, f.team, auth.RoleOwner, policy.ScopeAdmin), f.team, "Via Token")
	require.NoError(t, err)
	require.Equal(t, "Via Token", byToken.Name)

	// Switch: members get the team (as current), non-members 404, tokens are refused.
	sw, err := f.svc.Switch(f.as(bob, f.team), other.ID)
	require.NoError(t, err)
	require.Equal(t, other.ID, sw.ID)
	require.True(t, sw.Current)
	_, err = f.svc.Switch(f.as(stranger, f.team), other.ID)
	requireAPI(t, err, 404, "not_found")
	_, err = f.svc.Switch(f.token(bob, f.team, auth.RoleDeveloper, policy.ScopeAdmin), other.ID)
	requireAPI(t, err, 403, "forbidden")
}

func TestATeamRequiringTwoFactorBlocksMembersWithoutOne(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	f.exec(`UPDATE workspaces SET require_two_factor=true WHERE id=$1`, f.team)

	_, err := f.svc.Get(f.as(bob, f.team), f.team)
	requireAPI(t, err, 403, "two_factor_enrollment_required")
	_, err = f.svc.Members(f.as(bob, f.team), f.team)
	requireAPI(t, err, 403, "two_factor_enrollment_required")
	_, err = f.svc.Rename(f.as(bob, f.team), f.team, "X")
	requireAPI(t, err, 403, "two_factor_enrollment_required")
	_, err = f.svc.CreateInvite(f.as(bob, f.team), f.team, InviteInput{Role: auth.RoleViewer})
	requireAPI(t, err, 403, "two_factor_enrollment_required")

	// A session that proved a second factor passes.
	p, _ := auth.PrincipalFrom(f.as(bob, f.team))
	mfa := *p
	mfa.SessionMFA = true
	_, err = f.svc.Get(auth.WithPrincipal(context.Background(), &mfa), f.team)
	require.NoError(t, err)

	// Acting on someone else is blocked; leaving the team never is.
	err = f.svc.RemoveMember(f.as(bob, f.team), f.team, cy)
	requireAPI(t, err, 403, "two_factor_enrollment_required")
	require.NoError(t, f.svc.RemoveMember(f.as(cy, f.team), f.team, cy))
	require.False(t, f.isMember(cy, f.team))
}

func TestMembersListShowsRolesRestrictionsAndYou(t *testing.T) {
	f := newTf(t)
	p1, p2 := f.project("One"), f.project("Two")
	bob := f.member("bob@example.com", auth.RoleDeveloper, p1, p2)

	list, err := f.svc.Members(f.as(bob, f.team), f.team)
	require.NoError(t, err)
	require.Len(t, list, 2)
	by := map[string]TeamMember{}
	for _, m := range list {
		by[m.Email] = m
	}
	require.Equal(t, auth.RoleOwner, by["ada@example.com"].Role)
	require.Empty(t, by["ada@example.com"].ProjectIDs)
	require.False(t, by["ada@example.com"].You)
	require.True(t, by["bob@example.com"].You)
	require.ElementsMatch(t, []uuid.UUID{p1, p2}, by["bob@example.com"].ProjectIDs)
	require.Equal(t, "bob", by["bob@example.com"].Name)

	stranger := f.user("eve@example.com", "")
	_, err = f.svc.Members(f.as(stranger, f.team), f.team)
	requireAPI(t, err, 404, "not_found")
}

func TestUpdateMemberRolesAndProjectRestrictions(t *testing.T) {
	f := newTf(t)
	p1, p2 := f.project("One"), f.project("Two")
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	dee := f.member("dee@example.com", auth.RoleViewer)
	admin := f.as(bob, f.team)
	role := func(r string) *string { return &r }

	// Change a role.
	m, err := f.svc.UpdateMember(admin, f.team, cy, MemberPatch{Role: role(auth.RoleViewer)})
	require.NoError(t, err)
	require.Equal(t, auth.RoleViewer, m.Role)
	require.Equal(t, auth.RoleViewer, f.roleOf(cy))
	require.Equal(t, "cy@example.com", m.Email)
	require.Equal(t, 1, f.audits("member.update"))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action='member.update' AND metadata->>'previousRole'='developer' AND metadata->>'role'='viewer'`))

	// Restrict to projects (duplicates collapse), then lift the restriction.
	m, err = f.svc.UpdateMember(admin, f.team, cy, MemberPatch{ProjectIDs: []uuid.UUID{p1, p2, p1}, SetProjects: true})
	require.NoError(t, err)
	require.ElementsMatch(t, []uuid.UUID{p1, p2}, m.ProjectIDs)
	require.Equal(t, auth.RoleViewer, m.Role, "a restriction change keeps the role")
	m, err = f.svc.UpdateMember(admin, f.team, cy, MemberPatch{SetProjects: true})
	require.NoError(t, err)
	require.Empty(t, m.ProjectIDs)
	got, err := f.q.GetMember(context.Background(), db.GetMemberParams{WorkspaceID: f.team, UserID: cy})
	require.NoError(t, err)
	require.Nil(t, got.ProjectIds)

	// An empty restriction means "no project at all" and is kept.
	m, err = f.svc.UpdateMember(admin, f.team, cy, MemberPatch{ProjectIDs: []uuid.UUID{}, SetProjects: true})
	require.NoError(t, err)
	require.NotNil(t, m.ProjectIDs)
	require.Empty(t, m.ProjectIDs)

	// A project of another team is refused and nothing changes.
	otherTeam, err := f.svc.Create(f.asAda(), "Elsewhere")
	require.NoError(t, err)
	foreign, err := f.q.CreateProject(context.Background(), db.CreateProjectParams{WorkspaceID: otherTeam.ID, Name: "Far", Slug: "far"})
	require.NoError(t, err)
	_, err = f.svc.UpdateMember(admin, f.team, cy, MemberPatch{ProjectIDs: []uuid.UUID{p1, foreign.ID}, SetProjects: true})
	requireAPI(t, err, 422, "unknown_project")

	// Roles that see everything drop the restriction.
	_, err = f.svc.UpdateMember(admin, f.team, dee, MemberPatch{ProjectIDs: []uuid.UUID{p1}, SetProjects: true})
	require.NoError(t, err)
	m, err = f.svc.UpdateMember(admin, f.team, dee, MemberPatch{Role: role(auth.RoleAdmin)})
	require.NoError(t, err)
	require.Equal(t, auth.RoleAdmin, m.Role)
	require.Empty(t, m.ProjectIDs)

	// Refusals: unknown role, unknown member, a developer, a stranger.
	_, err = f.svc.UpdateMember(admin, f.team, cy, MemberPatch{Role: role("root")})
	requireAPI(t, err, 422, "invalid_role")
	_, err = f.svc.UpdateMember(admin, f.team, uuid.New(), MemberPatch{Role: role(auth.RoleViewer)})
	requireAPI(t, err, 404, "not_found")
	dev := f.as(f.member("fay@example.com", auth.RoleDeveloper), f.team)
	_, err = f.svc.UpdateMember(dev, f.team, cy, MemberPatch{Role: role(auth.RoleViewer)})
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.UpdateMember(f.as(f.user("eve@example.com", ""), f.team), f.team, cy, MemberPatch{Role: role(auth.RoleViewer)})
	requireAPI(t, err, 404, "not_found")

	// Tokens act with the role of their creator and need the admin scope.
	_, err = f.svc.UpdateMember(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeProjectsWrite), f.team, cy, MemberPatch{Role: role(auth.RoleDeveloper)})
	requireAPI(t, err, 403, "forbidden")
	m, err = f.svc.UpdateMember(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeAdmin), f.team, cy, MemberPatch{Role: role(auth.RoleDeveloper)})
	require.NoError(t, err)
	require.Equal(t, auth.RoleDeveloper, m.Role)
}

func TestOnlyOwnersManageOwnersAndTheLastOwnerStays(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	role := func(r string) *string { return &r }

	// An admin can neither change an owner nor hand out ownership.
	_, err := f.svc.UpdateMember(f.as(bob, f.team), f.team, f.ada.ID, MemberPatch{Role: role(auth.RoleViewer)})
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.UpdateMember(f.as(bob, f.team), f.team, cy, MemberPatch{Role: role(auth.RoleOwner)})
	requireAPI(t, err, 403, "forbidden")
	require.Equal(t, auth.RoleOwner, f.roleOf(f.ada.ID))
	require.Equal(t, auth.RoleDeveloper, f.roleOf(cy))

	// The only owner cannot step down.
	_, err = f.svc.UpdateMember(f.asAda(), f.team, f.ada.ID, MemberPatch{Role: role(auth.RoleAdmin)})
	requireAPI(t, err, 409, "last_owner")
	require.Equal(t, auth.RoleOwner, f.roleOf(f.ada.ID))

	// With a second owner the first may step down.
	_, err = f.svc.UpdateMember(f.asAda(), f.team, bob, MemberPatch{Role: role(auth.RoleOwner)})
	require.NoError(t, err)
	_, err = f.svc.UpdateMember(f.asAda(), f.team, f.ada.ID, MemberPatch{Role: role(auth.RoleAdmin)})
	require.NoError(t, err)
	require.Equal(t, auth.RoleAdmin, f.roleOf(f.ada.ID))
	// And the remaining owner is protected again.
	_, err = f.svc.UpdateMember(f.as(bob, f.team), f.team, bob, MemberPatch{Role: role(auth.RoleDeveloper)})
	requireAPI(t, err, 409, "last_owner")
}

func TestRemoveMemberAndLeave(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	dee := f.member("dee@example.com", auth.RoleViewer)
	tok, err := f.q.CreateAPIToken(context.Background(), db.CreateAPITokenParams{
		WorkspaceID: f.team, UserID: cy, Name: "ci", TokenHash: []byte("h"), Prefix: "nct_ab", Scopes: []string{policy.ScopeProjectsRead}, Kind: auth.TokenKindREST,
	})
	require.NoError(t, err)

	// A developer cannot remove anyone else, an unknown member is 404.
	requireAPI(t, f.svc.RemoveMember(f.as(cy, f.team), f.team, dee), 403, "forbidden")
	requireAPI(t, f.svc.RemoveMember(f.as(bob, f.team), f.team, uuid.New()), 404, "not_found")
	// An admin cannot remove an owner.
	requireAPI(t, f.svc.RemoveMember(f.as(bob, f.team), f.team, f.ada.ID), 403, "forbidden")
	require.True(t, f.isMember(f.ada.ID, f.team))
	// A token needs the admin scope.
	requireAPI(t, f.svc.RemoveMember(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeProjectsWrite), f.team, dee), 403, "forbidden")
	require.True(t, f.isMember(dee, f.team))

	// An admin removes a member; the member's tokens stop working.
	require.NoError(t, f.svc.RemoveMember(f.as(bob, f.team), f.team, cy))
	require.False(t, f.isMember(cy, f.team))
	var revoked *time.Time
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT revoked_at FROM api_tokens WHERE id=$1`, tok.ID).Scan(&revoked))
	require.NotNil(t, revoked, "the removed member's token is revoked")
	require.Equal(t, 1, f.audits("member.remove"))
	// The admin-scope token of an admin may remove too.
	require.NoError(t, f.svc.RemoveMember(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeAdmin), f.team, dee))

	// Anyone may leave; that is a different audit entry.
	eve := f.member("eve@example.com", auth.RoleViewer)
	require.NoError(t, f.svc.RemoveMember(f.as(eve, f.team), f.team, eve))
	require.False(t, f.isMember(eve, f.team))
	require.Equal(t, 1, f.audits("member.leave"))

	// The last owner can neither be removed nor leave.
	requireAPI(t, f.svc.RemoveMember(f.asAda(), f.team, f.ada.ID), 409, "last_owner")
	require.True(t, f.isMember(f.ada.ID, f.team))
	// A stranger is told nothing.
	requireAPI(t, f.svc.RemoveMember(f.as(f.user("zed@example.com", ""), f.team), f.team, f.ada.ID), 404, "not_found")
}

func TestInstanceSettingsVisibilityAndUpdate(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)

	// Everyone sees whether team creation is allowed; only the instance admin sees the relay.
	forBob, err := f.svc.GetInstance(f.as(bob, f.team))
	require.NoError(t, err)
	require.True(t, forBob.AllowTeamCreation)
	require.False(t, forBob.CanManage)
	require.False(t, forBob.SMTPConfigured)
	require.Nil(t, forBob.SMTPHost)
	_, err = f.svc.GetInstance(context.Background())
	requireAPI(t, err, 401, "unauthorized")

	host, port, tls, user, from, pw := "smtp.example.com", 2525, "starttls", "mailer", "NebulaCtrl <no-reply@example.com>", "s3cret"
	skip := true
	got, err := f.svc.UpdateInstance(f.asAda(), InstancePatch{
		SMTPHost: &host, SMTPPort: &port, SMTPTLS: &tls, SMTPUsername: &user, SMTPFrom: &from, SMTPPassword: &pw, SMTPInsecureSkipVerify: &skip,
	})
	require.NoError(t, err)
	require.True(t, got.CanManage)
	require.True(t, got.SMTPConfigured)
	require.Equal(t, host, *got.SMTPHost)
	require.Equal(t, port, *got.SMTPPort)
	require.Equal(t, tls, *got.SMTPTLS)
	require.Equal(t, user, *got.SMTPUsername)
	require.Equal(t, from, *got.SMTPFrom)
	require.True(t, *got.SMTPPasswordSet)
	require.True(t, *got.SMTPInsecureSkipVerify)
	require.Equal(t, 1, f.audits("instance.update"))
	// The password is stored sealed, never as given.
	var enc []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT smtp_password_enc FROM instance_settings`).Scan(&enc))
	require.NotEmpty(t, enc)
	require.False(t, bytes.Contains(enc, []byte(pw)))

	// The relay is what mails use, with the password opened again.
	relay, err := f.svc.InstanceRelay(context.Background())
	require.NoError(t, err)
	require.Equal(t, &Relay{Host: host, Port: port, TLS: tls, Username: user, Password: pw, From: from, InsecureSkipVerify: true}, relay)

	// A later patch keeps what it does not mention, password included.
	allow := false
	got, err = f.svc.UpdateInstance(f.asAda(), InstancePatch{AllowTeamCreation: &allow})
	require.NoError(t, err)
	require.False(t, got.AllowTeamCreation)
	require.True(t, *got.SMTPPasswordSet)
	forBob, err = f.svc.GetInstance(f.as(bob, f.team))
	require.NoError(t, err)
	require.False(t, forBob.AllowTeamCreation)
	require.True(t, forBob.SMTPConfigured)
	require.Nil(t, forBob.SMTPHost, "the relay details stay with the instance admin")
	require.Nil(t, forBob.SMTPPasswordSet)

	// An empty password clears it; whitespace around other fields is trimmed.
	empty, spaced := "", "  mail.example.org  "
	got, err = f.svc.UpdateInstance(f.asAda(), InstancePatch{SMTPPassword: &empty, SMTPHost: &spaced})
	require.NoError(t, err)
	require.False(t, *got.SMTPPasswordSet)
	require.Equal(t, "mail.example.org", *got.SMTPHost)
	relay, err = f.svc.InstanceRelay(context.Background())
	require.NoError(t, err)
	require.Empty(t, relay.Password)

	// Without a sender the relay does not count as configured.
	noFrom := ""
	got, err = f.svc.UpdateInstance(f.asAda(), InstancePatch{SMTPFrom: &noFrom})
	require.NoError(t, err)
	require.False(t, got.SMTPConfigured)
	relay, err = f.svc.InstanceRelay(context.Background())
	require.NoError(t, err)
	require.Nil(t, relay)
}

func TestUpdateInstanceValidationAndAuthorization(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleOwner)
	zero, big, ok, bad, tls := 0, 70000, 465, "ssl", "implicit"
	notAddr := "not an address"

	for name, patch := range map[string]InstancePatch{
		"port 0":      {SMTPPort: &zero},
		"port > max":  {SMTPPort: &big},
		"unknown tls": {SMTPTLS: &bad},
		"bad sender":  {SMTPFrom: &notAddr},
	} {
		_, err := f.svc.UpdateInstance(f.asAda(), patch)
		require.Error(t, err, name)
		ae, isAPI := apierr.As(err)
		require.True(t, isAPI, name)
		require.Equal(t, 422, ae.Status, name)
	}
	_, err := f.svc.UpdateInstance(f.asAda(), InstancePatch{SMTPPort: &zero})
	requireAPI(t, err, 422, "invalid_port")
	_, err = f.svc.UpdateInstance(f.asAda(), InstancePatch{SMTPTLS: &bad})
	requireAPI(t, err, 422, "invalid_tls")
	_, err = f.svc.UpdateInstance(f.asAda(), InstancePatch{SMTPFrom: &notAddr})
	requireAPI(t, err, 422, "invalid_email")
	require.Zero(t, f.audits("instance.update"), "refused patches change and record nothing")

	// Valid values go through.
	_, err = f.svc.UpdateInstance(f.asAda(), InstancePatch{SMTPPort: &ok, SMTPTLS: &tls})
	require.NoError(t, err)

	// Not the instance admin: an owner of a team, and a token of the instance admin without the admin scope.
	_, err = f.svc.UpdateInstance(f.as(bob, f.team), InstancePatch{SMTPPort: &ok})
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.UpdateInstance(f.token(f.ada.ID, f.team, auth.RoleOwner, policy.ScopeProjectsRead), InstancePatch{SMTPPort: &ok})
	requireAPI(t, err, 403, "forbidden")
	view, err := f.svc.GetInstance(f.token(f.ada.ID, f.team, auth.RoleOwner, policy.ScopeProjectsRead))
	require.NoError(t, err)
	require.False(t, view.CanManage)
	_, err = f.svc.UpdateInstance(f.token(f.ada.ID, f.team, auth.RoleOwner, policy.ScopeAdmin), InstancePatch{SMTPPort: &ok})
	require.NoError(t, err, "an admin-scope token acts as its creator")
	_, err = f.svc.UpdateInstance(context.Background(), InstancePatch{})
	requireAPI(t, err, 401, "unauthorized")

	// Storing a password needs a secret key.
	noBox := NewService(Deps{Pool: f.pool, Auth: f.au})
	pw := "x"
	_, err = noBox.UpdateInstance(f.asAda(), InstancePatch{SMTPPassword: &pw})
	requireAPI(t, err, 422, "no_secret_key")
}

func TestInstanceRelayToleratesAPasswordThatCannotBeOpened(t *testing.T) {
	f := newTf(t)
	f.exec(`UPDATE instance_settings SET smtp_host='smtp.example.com', smtp_from='a@example.com', smtp_port=587, smtp_tls='starttls', smtp_password_enc=$1`, []byte("garbage-not-a-sealed-value"))
	relay, err := f.svc.InstanceRelay(context.Background())
	require.NoError(t, err)
	require.NotNil(t, relay)
	require.Equal(t, "smtp.example.com", relay.Host)
	require.Empty(t, relay.Password, "an undecryptable password is dropped, not leaked or fatal")

	// No secret box: the relay still has its host, without a password.
	noBox := NewService(Deps{Pool: f.pool, Auth: f.au})
	relay, err = noBox.InstanceRelay(context.Background())
	require.NoError(t, err)
	require.Empty(t, relay.Password)
}

func TestPasswordResetLinkRules(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	admin := f.as(bob, f.team)

	// An admin issues a link for a member who lives only in this team.
	link, err := f.svc.CreatePasswordResetLink(admin, f.team, cy)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(link.Token, auth.ResetTokenPrefix))
	require.Equal(t, "/reset-password?token="+link.Token, link.Link, "without a public URL the link is a path")
	require.WithinDuration(t, time.Now().Add(auth.ResetTTL), link.ExpiresAt, time.Minute)
	require.Equal(t, 1, f.audits("member.password_reset_link"))
	require.Zero(t, f.count(`SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%'||$1||'%'`, link.Token), "the token is never audited")

	f.au.Configure(f.box, "https://nebula.example.com/")
	link, err = f.svc.CreatePasswordResetLink(admin, f.team, cy)
	require.NoError(t, err)
	require.Equal(t, "https://nebula.example.com/reset-password?token="+link.Token, link.Link)

	// The link works once to set a new password.
	require.NoError(t, f.svc.auth.InTx(context.Background(), func(tx pgx.Tx) error {
		_, _, err := f.au.ConfirmPasswordReset(context.Background(), tx, link.Token, "a brand new password")
		return err
	}))

	// Yourself, a stranger to the team, an owner (by an admin), a token.
	_, err = f.svc.CreatePasswordResetLink(admin, f.team, bob)
	requireAPI(t, err, 422, "own_password")
	_, err = f.svc.CreatePasswordResetLink(admin, f.team, uuid.New())
	requireAPI(t, err, 404, "not_found")
	_, err = f.svc.CreatePasswordResetLink(admin, f.team, f.ada.ID)
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.CreatePasswordResetLink(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeAdmin), f.team, cy)
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.CreatePasswordResetLink(f.as(cy, f.team), f.team, bob)
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.CreatePasswordResetLink(f.as(f.user("eve@example.com", ""), f.team), f.team, cy)
	requireAPI(t, err, 404, "not_found")

	// A member without a password signs in through a provider: nothing to reset.
	sso := f.user("sso@example.com", "")
	f.join(f.team, sso, auth.RoleViewer)
	_, err = f.svc.CreatePasswordResetLink(admin, f.team, sso)
	requireAPI(t, err, 409, "no_password")
}

func TestPasswordResetLinkForPeopleOfOtherTeamsIsTheInstanceAdminsCall(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	// cy also belongs to a second team; the instance admin (ada) also belongs to cy's team as a developer.
	other, err := f.svc.Create(f.as(bob, f.team), "Bobs")
	require.NoError(t, err)
	f.join(other.ID, cy, auth.RoleViewer)

	// The admin of one team cannot take over someone who also belongs to another.
	_, err = f.svc.CreatePasswordResetLink(f.as(bob, f.team), f.team, cy)
	requireAPI(t, err, 409, "member_of_other_teams")
	// The instance admin can.
	_, err = f.svc.CreatePasswordResetLink(f.asAda(), f.team, cy)
	require.NoError(t, err)

	// The instance admin's own account is reset on the server, by anyone else.
	f.join(other.ID, f.ada.ID, auth.RoleDeveloper)
	_, err = f.svc.CreatePasswordResetLink(f.as(bob, other.ID), other.ID, f.ada.ID)
	// bob owns "Bobs"; ada is a developer there and the instance admin.
	ae := requireAPI(t, err, 403, "instance_admin_account")
	require.Contains(t, ae.Message, "reset-password")
}
