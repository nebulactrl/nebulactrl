package app_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/alexedwards/argon2id"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// ---- helpers ----

// otherBrowser returns a harness view with its own cookie jar.
func (h *harness) otherBrowser() *harness {
	h.t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(h.t, err)
	c := *h
	c.client = &http.Client{Jar: jar}
	return &c
}

// teamID returns the id of the team the session currently acts in.
func (h *harness) teamID() string {
	h.t.Helper()
	r := h.do("GET", "/me", "", nil)
	require.Equal(h.t, 200, r.Status, string(r.Raw))
	return r.JSON()["workspaceId"].(string)
}

func (h *harness) role() string {
	h.t.Helper()
	r := h.do("GET", "/me", "", nil)
	require.Equal(h.t, 200, r.Status, string(r.Raw))
	role, _ := r.JSON()["principal"].(map[string]any)["role"].(string)
	return role
}

// inviteLink creates an invite in team and returns the raw token.
func (h *harness) inviteLink(team, role string, extra map[string]any) string {
	h.t.Helper()
	body := map[string]any{"role": role}
	for k, v := range extra {
		body[k] = v
	}
	r := h.do("POST", "/teams/"+team+"/invites", "", body)
	require.Equal(h.t, 201, r.Status, string(r.Raw))
	tok := r.JSON()["token"].(string)
	require.True(h.t, strings.HasPrefix(tok, "nci_"))
	require.Contains(h.t, r.JSON()["link"], "/invite/"+tok)
	return tok
}

// joinNewMember signs a brand-new account up through an invite link and
// returns a harness acting as that account.
func (h *harness) joinNewMember(token, email, name string) *harness {
	h.t.Helper()
	b := h.otherBrowser()
	r := b.do("POST", "/invites/"+token+"/accept", "", map[string]any{"email": email, "name": name, "password": testPassword})
	require.Equal(h.t, 200, r.Status, string(r.Raw))
	require.Equal(h.t, true, r.JSON()["createdAccount"])
	return b
}

// memberOf signs up a new member of team with role.
func (h *harness) memberOf(team, role, email string) *harness {
	h.t.Helper()
	return h.joinNewMember(h.inviteLink(team, role, nil), email, strings.Split(email, "@")[0])
}

// seedAccount creates a user who owns a team of their own (no invite) and
// signs them in on a fresh browser.
func (h *harness) seedAccount(email, teamName string) *harness {
	h.t.Helper()
	ctx := context.Background()
	q := db.New(h.app.Pool)
	ws, err := q.CreateWorkspace(ctx, teamName)
	require.NoError(h.t, err)
	hash, err := argon2id.CreateHash(testPassword, argon2id.DefaultParams)
	require.NoError(h.t, err)
	_, err = q.CreateUser(ctx, db.CreateUserParams{WorkspaceID: ws.ID, Email: email, Name: strings.Split(email, "@")[0], PasswordHash: hash})
	require.NoError(h.t, err)
	b := h.otherBrowser()
	r := b.do("POST", "/auth/login", "", map[string]any{"email": email, "password": testPassword})
	require.Equal(h.t, 200, r.Status, string(r.Raw))
	return b
}

func (h *harness) userID() string {
	h.t.Helper()
	return h.do("GET", "/me", "", nil).JSON()["id"].(string)
}

func (h *harness) firstEnv(project string) string {
	h.t.Helper()
	r := h.do("GET", "/projects/"+project+"/environments", "", nil)
	require.Equal(h.t, 200, r.Status, string(r.Raw))
	return r.JSON()["items"].([]any)[0].(map[string]any)["id"].(string)
}

func ids(items []any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

// ---- setup, listing, switching ----

func TestTeamsSetupCreatesFirstTeamOwnedByFirstUser(t *testing.T) {
	h := newHarness(t)
	h.setup()

	me := h.do("GET", "/me", "", nil).JSON()
	require.Equal(t, true, me["instanceAdmin"])
	require.Equal(t, "owner", me["principal"].(map[string]any)["role"])

	items := h.do("GET", "/teams", "", nil).JSON()["items"].([]any)
	require.Len(t, items, 1)
	team := items[0].(map[string]any)
	require.Equal(t, me["workspaceId"], team["id"])
	require.Equal(t, "owner", team["role"])
	require.Equal(t, true, team["current"])
	require.Equal(t, "nebulactrl", team["slug"])
	require.Equal(t, float64(1), team["memberCount"])
}

func TestTeamsSwitchRequiresMembership(t *testing.T) {
	h := newHarness(t)
	h.setup()
	teamA := h.teamID()

	outsider := h.seedAccount("outsider@example.com", "Outsider team")
	outsiderTeam := outsider.teamID()
	require.NotEqual(t, teamA, outsiderTeam)

	// Not a member of A: 404 for both switch and read, indistinguishable from a
	// team that does not exist.
	r := outsider.do("POST", "/teams/"+teamA+"/switch", "", nil)
	require.Equal(t, 404, r.Status, string(r.Raw))
	missing := outsider.do("POST", "/teams/"+uuid.NewString()+"/switch", "", nil)
	require.Equal(t, 404, missing.Status)
	require.Equal(t, r.JSON()["detail"], missing.JSON()["detail"])
	require.Equal(t, 404, outsider.do("GET", "/teams/"+teamA, "", nil).Status)
	require.Equal(t, outsiderTeam, outsider.teamID(), "a refused switch must not change the session team")

	// A member of both teams switches freely and the principal follows.
	tok := h.inviteLink(teamA, "viewer", nil)
	require.Equal(t, 200, outsider.do("POST", "/invites/"+tok+"/accept", "", nil).Status)
	require.Equal(t, teamA, outsider.teamID(), "accepting an invite makes the team current")
	require.Equal(t, "viewer", outsider.role())
	require.Len(t, outsider.do("GET", "/teams", "", nil).JSON()["items"], 2)

	sw := outsider.do("POST", "/teams/"+outsiderTeam+"/switch", "", nil)
	require.Equal(t, 200, sw.Status, string(sw.Raw))
	require.Equal(t, outsiderTeam, outsider.teamID())
	require.Equal(t, "owner", outsider.role())
}

func TestTeamsCreateTeamAndIsolation(t *testing.T) {
	h := newHarness(t)
	h.setup()
	teamA := h.teamID()
	h.createProject("Alpha")

	r := h.do("POST", "/teams", "", map[string]any{"name": "Second Team"})
	require.Equal(t, 201, r.Status, string(r.Raw))
	teamB := r.JSON()["id"].(string)
	require.Equal(t, "owner", r.JSON()["role"])
	require.Equal(t, "second-team", r.JSON()["slug"])
	require.Equal(t, teamA, h.teamID(), "creating a team does not switch to it")

	require.Equal(t, 200, h.do("POST", "/teams/"+teamB+"/switch", "", nil).Status)
	require.Empty(t, h.do("GET", "/projects", "", nil).JSON()["items"], "a new team starts empty")
	require.Empty(t, h.do("GET", "/servers", "", nil).JSON()["items"])

	// Rename needs admin/owner; delete needs the slug typed.
	require.Equal(t, 200, h.do("PATCH", "/teams/"+teamB, "", map[string]any{"name": "Renamed"}).Status)
	require.Equal(t, 422, h.do("DELETE", "/teams/"+teamB+"?confirm=wrong", "", nil).Status)
	r = h.do("DELETE", "/teams/"+teamB+"?confirm=second-team", "", nil)
	require.Equal(t, 202, r.Status, string(r.Raw))
	require.Equal(t, "deleting", r.JSON()["deletion"].(map[string]any)["state"])
	// The job (not running in this harness; internal/teams tests run it) deletes
	// the team. Meanwhile a second request is refused.
	r = h.do("DELETE", "/teams/"+teamB+"?confirm=second-team", "", nil)
	require.Equal(t, 409, r.Status, string(r.Raw))
	require.Equal(t, "deletion_in_progress", r.JSON()["code"])
	_, err := h.app.Pool.Exec(context.Background(), "DELETE FROM workspaces WHERE id = $1", teamB)
	require.NoError(t, err)
	// The session falls back to the user's remaining team.
	require.Equal(t, teamA, h.teamID())
	require.Len(t, h.do("GET", "/projects", "", nil).JSON()["items"], 1)
}

func TestTeamsDeletionPlanCountsServers(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	require.Equal(t, 201, h.do("POST", "/servers/enrollment-tokens", "", map[string]any{"name": "srv"}).Status)
	r := h.do("GET", "/teams/"+team+"/deletion-plan", "", nil)
	require.Equal(t, 200, r.Status, string(r.Raw))
	require.EqualValues(t, 1, r.JSON()["servers"].(map[string]any)["offline"])
	require.Equal(t, false, r.JSON()["hostsControlPlane"])
}

func TestTeamsCreationDisabledForNonAdmins(t *testing.T) {
	h := newHarness(t)
	h.setup() // first user = instance admin
	team := h.teamID()
	member := h.memberOf(team, "developer", "dev@example.com")

	// Any member can create a team while creation is allowed.
	require.Equal(t, 201, member.do("POST", "/teams", "", map[string]any{"name": "Dev's own"}).Status)

	// Only the instance admin changes the setting.
	inst := member.do("GET", "/instance", "", nil).JSON()
	require.Equal(t, true, inst["allowTeamCreation"])
	require.Equal(t, false, inst["canManage"])
	require.NotContains(t, inst, "smtpHost", "SMTP details are for the instance admin only")
	require.Equal(t, 403, member.do("PATCH", "/instance", "", map[string]any{"allowTeamCreation": false}).Status)

	off := h.do("PATCH", "/instance", "", map[string]any{"allowTeamCreation": false, "smtpHost": "smtp.example.com", "smtpFrom": "NebulaCtrl <no-reply@example.com>", "smtpPassword": "hunter2-hunter2"})
	require.Equal(t, 200, off.Status, string(off.Raw))
	require.Equal(t, false, off.JSON()["allowTeamCreation"])
	require.Equal(t, true, off.JSON()["canManage"])
	require.Equal(t, true, off.JSON()["smtpConfigured"])
	require.Equal(t, true, off.JSON()["smtpPasswordSet"])
	require.NotContains(t, string(off.Raw), "hunter2", "the SMTP password is write-only")

	denied := member.do("POST", "/teams", "", map[string]any{"name": "Another"})
	require.Equal(t, 403, denied.Status, string(denied.Raw))
	require.Equal(t, "team_creation_disabled", denied.JSON()["code"])
	// The instance admin is exempt.
	require.Equal(t, 201, h.do("POST", "/teams", "", map[string]any{"name": "Admin may"}).Status)
}

// ---- roles ----

func TestTeamsRoleMatrix(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	svc, _ := h.newService("web")

	viewer := h.memberOf(team, "viewer", "viewer@example.com")
	dev := h.memberOf(team, "developer", "dev@example.com")
	admin := h.memberOf(team, "admin", "admin2@example.com")
	require.Equal(t, "viewer", viewer.role())
	require.Equal(t, "developer", dev.role())
	require.Equal(t, "admin", admin.role())

	t.Run("viewer is read-only", func(t *testing.T) {
		require.Equal(t, 200, viewer.do("GET", "/projects", "", nil).Status)
		require.Equal(t, 200, viewer.do("GET", "/services/"+svc, "", nil).Status)
		require.Equal(t, 200, viewer.do("GET", "/servers", "", nil).Status)
		require.Equal(t, 200, viewer.do("GET", "/teams/"+team+"/members", "", nil).Status)
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"POST", "/projects", map[string]any{"name": "x"}},
			{"POST", "/services/" + svc + "/deployments", map[string]any{}},
			{"POST", "/services/" + svc + "/restart", nil},
			{"POST", "/servers/enrollment-tokens", map[string]any{"name": "x"}},
			{"GET", "/audit-events", nil},
			{"GET", "/teams/" + team + "/invites", nil},
			{"POST", "/teams/" + team + "/invites", map[string]any{"role": "viewer"}},
		} {
			r := viewer.do(c.method, c.path, "", c.body)
			require.Equal(t, 403, r.Status, "%s %s: %s", c.method, c.path, string(r.Raw))
		}
	})

	t.Run("developer deploys but manages neither servers nor members", func(t *testing.T) {
		require.Equal(t, 201, dev.do("POST", "/projects", "", map[string]any{"name": "Dev project"}).Status)
		require.Equal(t, 202, dev.do("POST", "/services/"+svc+"/deployments", "", map[string]any{}).Status)
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"POST", "/servers/enrollment-tokens", map[string]any{"name": "x"}},
			{"GET", "/audit-events", nil},
			{"GET", "/teams/" + team + "/invites", nil},
			{"POST", "/teams/" + team + "/invites", map[string]any{"role": "viewer"}},
			{"PATCH", "/teams/" + team, map[string]any{"name": "x"}},
		} {
			r := dev.do(c.method, c.path, "", c.body)
			require.Equal(t, 403, r.Status, "%s %s: %s", c.method, c.path, string(r.Raw))
		}
		members := dev.do("GET", "/teams/"+team+"/members", "", nil).JSON()["items"].([]any)
		require.Len(t, members, 4)
		viewerID := viewer.userID()
		r := dev.do("PATCH", "/teams/"+team+"/members/"+viewerID, "", map[string]any{"role": "admin"})
		require.Equal(t, 403, r.Status, string(r.Raw))
		r = dev.do("DELETE", "/teams/"+team+"/members/"+viewerID, "", nil)
		require.Equal(t, 403, r.Status, string(r.Raw))
		// Leaving is always allowed for oneself (not the last owner).
	})

	t.Run("admin manages everyone except owners", func(t *testing.T) {
		require.Equal(t, 200, admin.do("GET", "/audit-events", "", nil).Status)
		require.Equal(t, 201, admin.do("POST", "/servers/enrollment-tokens", "", map[string]any{"name": "srv-admin"}).Status)
		ownerID := h.userID()
		viewerID := viewer.userID()
		// Promote a viewer to developer, but never to or from owner.
		require.Equal(t, 200, admin.do("PATCH", "/teams/"+team+"/members/"+viewerID, "", map[string]any{"role": "developer"}).Status)
		require.Equal(t, 403, admin.do("PATCH", "/teams/"+team+"/members/"+viewerID, "", map[string]any{"role": "owner"}).Status)
		require.Equal(t, 403, admin.do("PATCH", "/teams/"+team+"/members/"+ownerID, "", map[string]any{"role": "viewer"}).Status)
		require.Equal(t, 403, admin.do("DELETE", "/teams/"+team+"/members/"+ownerID, "", nil).Status)
		require.Equal(t, 403, admin.do("POST", "/teams/"+team+"/invites", "", map[string]any{"role": "owner"}).Status)
		require.Equal(t, 201, admin.do("POST", "/teams/"+team+"/invites", "", map[string]any{"role": "viewer"}).Status)
		// The change took effect on the member's next request.
		require.Equal(t, "developer", viewer.role())
		require.Equal(t, 403, admin.do("DELETE", "/teams/"+team+"?confirm=nebulactrl", "", nil).Status, "only owners delete a team")
	})
}

func TestTeamsPerProjectRestrictionReturns404Outside(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	a := h.createProject("Project A")
	b := h.createProject("Project B")
	envB := h.firstEnv(b)

	dev := h.memberOf(team, "developer", "dev@example.com")
	require.Len(t, dev.do("GET", "/projects", "", nil).JSON()["items"], 2)

	r := h.do("PATCH", "/teams/"+team+"/members/"+dev.userID(), "", map[string]any{"projectIds": []string{a}})
	require.Equal(t, 200, r.Status, string(r.Raw))
	require.Equal(t, []any{a}, r.JSON()["projectIds"])

	require.Equal(t, []string{a}, ids(dev.do("GET", "/projects", "", nil).JSON()["items"].([]any)))
	require.Equal(t, 200, dev.do("GET", "/projects/"+a+"/environments", "", nil).Status)
	for _, c := range []struct{ method, path string }{
		{"GET", "/projects/" + b},
		{"GET", "/projects/" + b + "/environments"},
		{"POST", "/projects/" + b + "/environments"},
		{"GET", "/environments/" + envB + "/services"},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]any{"name": "x"}
		}
		got := dev.do(c.method, c.path, "", body)
		require.Equal(t, 404, got.Status, "%s %s: %s", c.method, c.path, string(got.Raw))
	}
	// A restricted member cannot mint projects or unrestricted tokens.
	require.Equal(t, 403, dev.do("POST", "/projects", "", map[string]any{"name": "C"}).Status)

	// Tokens inherit the restriction even when created without one... they must
	// name projects inside it.
	tok := dev.do("POST", "/tokens", "", map[string]any{"name": "t", "scopes": []string{"projects:read"}, "projectIds": []string{a}})
	require.Equal(t, 201, tok.Status, string(tok.Raw))
	raw := tok.JSON()["token"].(string)
	require.Equal(t, 404, h.do("GET", "/projects/"+b+"/environments", raw, nil).Status)
	require.Equal(t, 200, h.do("GET", "/projects/"+a+"/environments", raw, nil).Status)

	// Owners and admins cannot be restricted: the restriction is dropped.
	r = h.do("PATCH", "/teams/"+team+"/members/"+dev.userID(), "", map[string]any{"role": "admin"})
	require.Equal(t, 200, r.Status, string(r.Raw))
	require.NotContains(t, r.JSON(), "projectIds")
	require.Len(t, dev.do("GET", "/projects", "", nil).JSON()["items"], 2)

	// Lifting the restriction.
	require.Equal(t, 200, h.do("PATCH", "/teams/"+team+"/members/"+dev.userID(), "", map[string]any{"role": "developer", "projectIds": []string{a}}).Status)
	lifted := h.do("PATCH", "/teams/"+team+"/members/"+dev.userID(), "", map[string]any{"allProjects": true})
	require.Equal(t, 200, lifted.Status, string(lifted.Raw))
	require.Len(t, dev.do("GET", "/projects", "", nil).JSON()["items"], 2)

	// Unknown projects are rejected.
	bad := h.do("PATCH", "/teams/"+team+"/members/"+dev.userID(), "", map[string]any{"projectIds": []string{uuid.NewString()}})
	require.Equal(t, 422, bad.Status, string(bad.Raw))
}

// ---- cross-team isolation ----

func TestTeamsCrossTeamIsolation(t *testing.T) {
	a := newHarness(t)
	a.setup()
	svc, serverID := a.newService("web")
	projectID := a.do("GET", "/services/"+svc, "", nil).JSON()["projectId"].(string)
	adminTok := a.createToken([]string{"admin"}, nil)
	tokenID := a.do("GET", "/tokens", "", nil).JSON()["items"].([]any)[0].(map[string]any)["id"].(string)
	teamA := a.teamID()

	b := a.seedAccount("b@example.com", "Team B")
	teamB := b.teamID()
	bProject := b.createProject("B project")

	// Everything of team A is a 404 for team B's owner.
	for _, c := range []struct{ method, path string }{
		{"GET", "/projects/" + projectID},
		{"GET", "/projects/" + projectID + "/environments"},
		{"GET", "/services/" + svc},
		{"POST", "/services/" + svc + "/deployments"},
		{"GET", "/servers/" + serverID},
		{"DELETE", "/tokens/" + tokenID},
		{"GET", "/teams/" + teamA},
		{"GET", "/teams/" + teamA + "/members"},
		{"GET", "/teams/" + teamA + "/invites"},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]any{}
		}
		r := b.do(c.method, c.path, "", body)
		require.Equal(t, 404, r.Status, "%s %s: %s", c.method, c.path, string(r.Raw))
	}
	require.Equal(t, []string{bProject}, ids(b.do("GET", "/projects", "", nil).JSON()["items"].([]any)))
	require.Empty(t, b.do("GET", "/servers", "", nil).JSON()["items"])
	require.Empty(t, b.do("GET", "/tokens", "", nil).JSON()["items"], "team A's tokens are not team B's")
	for _, ev := range b.do("GET", "/audit-events", "", nil).JSON()["items"].([]any) {
		require.NotContains(t, ev.(map[string]any)["action"], "service.", "team A's audit log must not leak")
	}

	// Tokens belong to one team: A's token sees nothing of B.
	require.Equal(t, 404, a.do("GET", "/projects/"+bProject, adminTok, nil).Status)
	require.Equal(t, 404, a.do("GET", "/teams/"+teamB, adminTok, nil).Status)
	bTok := b.createToken([]string{"admin"}, nil)
	require.Equal(t, 404, a.do("GET", "/services/"+svc, bTok, nil).Status)
	require.Equal(t, 404, a.do("GET", "/servers/"+serverID, bTok, nil).Status)

	// And the other way round.
	require.Equal(t, 404, a.do("GET", "/projects/"+bProject, "", nil).Status)
}

// ---- members ----

func TestTeamsLastOwnerIsProtected(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	me := h.userID()

	r := h.do("PATCH", "/teams/"+team+"/members/"+me, "", map[string]any{"role": "admin"})
	require.Equal(t, 409, r.Status, string(r.Raw))
	require.Equal(t, "last_owner", r.JSON()["code"])
	r = h.do("DELETE", "/teams/"+team+"/members/"+me, "", nil)
	require.Equal(t, 409, r.Status, string(r.Raw))
	require.Equal(t, "last_owner", r.JSON()["code"])

	// With a second owner the first can step down and leave.
	second := h.memberOf(team, "owner", "owner2@example.com")
	require.Equal(t, "owner", second.role())
	require.Equal(t, 200, h.do("PATCH", "/teams/"+team+"/members/"+me, "", map[string]any{"role": "admin"}).Status)
	require.Equal(t, 409, second.do("DELETE", "/teams/"+team+"/members/"+second.userID(), "", nil).Status)
	require.Equal(t, 204, h.do("DELETE", "/teams/"+team+"/members/"+me, "", nil).Status, "a member may leave")
	require.Equal(t, 404, h.do("GET", "/teams/"+team, "", nil).Status)
}

func TestTeamsRemovedMemberLosesAccessAndTokens(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	h.createToken([]string{"projects:read"}, nil) // the owner's own token, invisible to the developer
	dev := h.memberOf(team, "developer", "dev@example.com")
	raw := dev.do("POST", "/tokens", "", map[string]any{"name": "ci", "scopes": []string{"projects:read", "deployments:write"}}).JSON()["token"].(string)
	require.Equal(t, 200, h.do("GET", "/projects", raw, nil).Status)

	// A token never exceeds its creator: an admin scope asked for by a
	// developer is cut down to the developer's scopes.
	capped := dev.do("POST", "/tokens", "", map[string]any{"name": "wide", "scopes": []string{"admin"}})
	require.Equal(t, 201, capped.Status, string(capped.Raw))
	require.NotContains(t, capped.JSON()["scopes"], "admin")
	require.Contains(t, capped.JSON()["scopes"], "deployments:write")
	wide := capped.JSON()["token"].(string)
	require.Equal(t, 403, h.do("GET", "/tokens", wide, nil).Status)
	require.Equal(t, 403, h.do("POST", "/servers/enrollment-tokens", wide, map[string]any{"name": "x"}).Status)

	// A developer sees only their own tokens; the owner sees both.
	require.Len(t, dev.do("GET", "/tokens", "", nil).JSON()["items"], 2)
	require.Len(t, h.do("GET", "/tokens", "", nil).JSON()["items"], 3, "the owner (admin scope) sees the whole team's tokens")

	require.Equal(t, 204, h.do("DELETE", "/teams/"+team+"/members/"+dev.userID(), "", nil).Status)
	r := h.do("GET", "/projects", raw, nil)
	require.Equal(t, 401, r.Status, string(r.Raw))
	require.Equal(t, "invalid_token", r.JSON()["code"])
}

func TestTeamsTokenScopesFollowCreatorsCurrentRole(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	adminMember := h.memberOf(team, "admin", "admin2@example.com")
	tok := adminMember.do("POST", "/tokens", "", map[string]any{"name": "ci", "scopes": []string{"admin"}}).JSON()["token"].(string)
	require.Equal(t, 201, h.do("POST", "/projects", tok, map[string]any{"name": "P"}).Status)
	require.Equal(t, 200, h.do("GET", "/audit-events", tok, nil).Status)

	// Demoting the creator demotes the token on its very next use.
	require.Equal(t, 200, h.do("PATCH", "/teams/"+team+"/members/"+adminMember.userID(), "", map[string]any{"role": "viewer"}).Status)
	require.Equal(t, 200, h.do("GET", "/projects", tok, nil).Status)
	require.Equal(t, 403, h.do("POST", "/projects", tok, map[string]any{"name": "Q"}).Status)
	require.Equal(t, 403, h.do("GET", "/audit-events", tok, nil).Status)
	me := h.do("GET", "/me", tok, nil).JSON()["principal"].(map[string]any)
	require.Equal(t, "viewer", me["role"])
}

// ---- invites ----

func TestTeamsInviteSignupSingleUseAndExpiry(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()

	tok := h.inviteLink(team, "developer", nil)
	prev := h.otherBrowser().do("GET", "/invites/"+tok, "", nil)
	require.Equal(t, 200, prev.Status, string(prev.Raw))
	require.Equal(t, "developer", prev.JSON()["role"])
	require.Equal(t, "NebulaCtrl", prev.JSON()["teamName"])
	require.NotContains(t, prev.JSON(), "email")

	// Sign-up needs a real password.
	weak := h.otherBrowser().do("POST", "/invites/"+tok+"/accept", "", map[string]any{"email": "new@example.com", "name": "New", "password": "short"})
	require.Equal(t, 422, weak.Status, string(weak.Raw))
	// ...and an anonymous call without credentials is refused, not joined.
	require.Equal(t, 401, h.otherBrowser().do("POST", "/invites/"+tok+"/accept", "", map[string]any{}).Status)

	newbie := h.joinNewMember(tok, "new@example.com", "New")
	require.Equal(t, team, newbie.teamID(), "sign-up via invite signs the user in on the team")
	require.Equal(t, "developer", newbie.role())
	require.Equal(t, 200, newbie.do("GET", "/projects", "", nil).Status)
	// They can sign in again later with the chosen password.
	again := h.otherBrowser().do("POST", "/auth/login", "", map[string]any{"email": "new@example.com", "password": testPassword})
	require.Equal(t, 200, again.Status, string(again.Raw))

	// Single use: the link is spent.
	used := h.otherBrowser().do("POST", "/invites/"+tok+"/accept", "", map[string]any{"email": "other@example.com", "name": "O", "password": testPassword})
	require.Equal(t, 410, used.Status, string(used.Raw))
	require.Equal(t, "invite_used", used.JSON()["code"])
	require.Equal(t, 410, h.otherBrowser().do("GET", "/invites/"+tok, "", nil).Status)

	// Expiry.
	exp := h.inviteLink(team, "viewer", nil)
	_, err := h.app.Pool.Exec(context.Background(), "UPDATE team_invites SET expires_at = now() - interval '1 minute' WHERE prefix = $1", exp[4:12])
	require.NoError(t, err)
	r := h.otherBrowser().do("GET", "/invites/"+exp, "", nil)
	require.Equal(t, 410, r.Status, string(r.Raw))
	require.Equal(t, "invite_expired", r.JSON()["code"])
	r = h.otherBrowser().do("POST", "/invites/"+exp+"/accept", "", map[string]any{"email": "late@example.com", "name": "L", "password": testPassword})
	require.Equal(t, 410, r.Status)

	// Garbage tokens are a plain 404.
	require.Equal(t, 404, h.otherBrowser().do("GET", "/invites/nci_nope", "", nil).Status)
	require.Equal(t, 404, h.otherBrowser().do("GET", "/invites/not-a-token", "", nil).Status)
}

func TestTeamsMultiUseInviteAndRevoke(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()

	tok := h.inviteLink(team, "viewer", map[string]any{"maxUses": 2})
	h.joinNewMember(tok, "one@example.com", "One")
	h.joinNewMember(tok, "two@example.com", "Two")
	r := h.otherBrowser().do("POST", "/invites/"+tok+"/accept", "", map[string]any{"email": "three@example.com", "name": "Three", "password": testPassword})
	require.Equal(t, 410, r.Status, string(r.Raw))

	// Revoking kills the link at once.
	live := h.inviteLink(team, "viewer", nil)
	list := h.do("GET", "/teams/"+team+"/invites", "", nil).JSON()["items"].([]any)
	var inviteID string
	for _, it := range list {
		m := it.(map[string]any)
		if m["prefix"] == live[4:12] {
			inviteID = m["id"].(string)
		}
	}
	require.NotEmpty(t, inviteID)
	require.Equal(t, 204, h.do("DELETE", "/teams/"+team+"/invites/"+inviteID, "", nil).Status)
	require.Equal(t, 404, h.otherBrowser().do("GET", "/invites/"+live, "", nil).Status)
	require.Equal(t, 404, h.do("DELETE", "/teams/"+team+"/invites/"+inviteID, "", nil).Status)
}

func TestTeamsEmailInviteIsBoundToItsAddress(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()

	r := h.do("POST", "/teams/"+team+"/invites", "", map[string]any{"email": "Invitee@Example.com", "role": "viewer", "maxUses": 5})
	require.Equal(t, 201, r.Status, string(r.Raw))
	require.Equal(t, "not_configured", r.JSON()["delivery"], "no email channel and no instance SMTP: the link is returned instead")
	tok := r.JSON()["token"].(string)
	inv := r.JSON()["invite"].(map[string]any)
	require.Equal(t, "invitee@example.com", inv["email"])
	require.Equal(t, float64(1), inv["maxUses"], "email invites are single use")

	prev := h.otherBrowser().do("GET", "/invites/"+tok, "", nil)
	require.Equal(t, "invitee@example.com", prev.JSON()["email"])
	require.NotContains(t, string(prev.Raw), "accountExists", "a preview must not say which emails have an account")

	wrong := h.otherBrowser().do("POST", "/invites/"+tok+"/accept", "", map[string]any{"email": "someone@else.example", "name": "X", "password": testPassword})
	require.Equal(t, 403, wrong.Status, string(wrong.Raw))
	require.Equal(t, "invite_email_mismatch", wrong.JSON()["code"])

	// The bound address is the default when none is sent.
	ok := h.otherBrowser().do("POST", "/invites/"+tok+"/accept", "", map[string]any{"name": "Invitee", "password": testPassword})
	require.Equal(t, 200, ok.Status, string(ok.Raw))

	// Inviting a member again is refused; an existing account must sign in.
	dup := h.do("POST", "/teams/"+team+"/invites", "", map[string]any{"email": "invitee@example.com", "role": "viewer"})
	require.Equal(t, 409, dup.Status, string(dup.Raw))
	require.Equal(t, "already_member", dup.JSON()["code"])

	other := h.seedAccount("existing@example.com", "Existing's team")
	tok2 := h.do("POST", "/teams/"+team+"/invites", "", map[string]any{"email": "existing@example.com", "role": "developer"}).JSON()["token"].(string)
	prevExisting := h.otherBrowser().do("GET", "/invites/"+tok2, "", nil)
	require.NotContains(t, string(prevExisting.Raw), "accountExists", "a preview must not say which emails have an account")
	signup := h.otherBrowser().do("POST", "/invites/"+tok2+"/accept", "", map[string]any{"name": "E", "password": testPassword})
	require.Equal(t, 409, signup.Status, string(signup.Raw))
	require.Equal(t, "account_exists", signup.JSON()["code"])

	// Signed in as another address: refused. As the right one: joined.
	tok3 := h.do("POST", "/teams/"+team+"/invites", "", map[string]any{"email": "someone-else@example.com", "role": "developer"}).JSON()["token"].(string)
	require.Equal(t, 403, other.do("POST", "/invites/"+tok3+"/accept", "", nil).Status)
	acc := other.do("POST", "/invites/"+tok2+"/accept", "", nil)
	require.Equal(t, 200, acc.Status, string(acc.Raw))
	require.Equal(t, false, acc.JSON()["createdAccount"])
	require.Equal(t, team, other.teamID())
	require.Equal(t, 409, other.do("POST", "/invites/"+tok2+"/accept", "", nil).Status)
}

func TestTeamsInviteCannotGrantMoreThanTheInviterMay(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	project := h.createProject("P")

	restricted := h.inviteLink(team, "developer", map[string]any{"projectIds": []string{project}})
	dev := h.joinNewMember(restricted, "dev@example.com", "Dev")
	require.Equal(t, []string{project}, ids(dev.do("GET", "/projects", "", nil).JSON()["items"].([]any)))

	// Invites to unknown projects and invalid roles are rejected.
	require.Equal(t, 422, h.do("POST", "/teams/"+team+"/invites", "", map[string]any{"role": "developer", "projectIds": []string{uuid.NewString()}}).Status)
	require.Equal(t, 422, h.do("POST", "/teams/"+team+"/invites", "", map[string]any{"role": "god"}).Status)
}
