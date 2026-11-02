package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	tmTeamID  = "77777777-1111-4111-8111-777777777777"
	tmTeam2ID = "88888888-1111-4111-8111-888888888888"
	tmTeams   = cfgAPI + "/teams"
	tmMembers = cfgAPI + "/teams/" + tmTeamID + "/members"
	tmInvites = cfgAPI + "/teams/" + tmTeamID + "/invites"
	tmMe      = cfgAPI + "/me"
)

func TestTeamsLsMarksTheCurrentTeamAndWhichTokensAreSaved(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tmTeams, 200, map[string]any{"items": []any{
		map[string]any{"id": tmTeamID, "name": "Acme", "slug": "acme", "role": "owner", "memberCount": 3, "current": true},
		map[string]any{"id": tmTeam2ID, "name": "Side Project", "slug": "side", "role": "viewer", "memberCount": 1, "current": false},
		map[string]any{"id": "99999999-1111-4111-8111-999999999999", "name": "Other", "slug": "other", "role": "admin", "memberCount": 12, "current": false},
	}})

	r := f.runWithConfig(config{Profiles: map[string]string{"side": "nct_side"}}, "teams", "ls")
	require.Equal(t, 0, r.code, r.all())
	for _, h := range []string{"NAME", "SLUG", "ROLE", "MEMBERS", "TOKEN SAVED", "ID"} {
		require.Contains(t, r.stdout, h)
	}
	require.Equal(t, []string{"*", "Acme", "acme", "owner", "3", "yes", tmTeamID}, bkLine(t, r.stdout, "Acme"),
		"the current team is starred and always has a usable token")
	require.Equal(t, []string{"Side", "Project", "side", "viewer", "1", "yes", tmTeam2ID}, bkLine(t, r.stdout, "Side"),
		"a saved profile counts as a saved token")
	require.Equal(t, []string{"Other", "other", "admin", "12", "no", "99999999-1111-4111-8111-999999999999"}, bkLine(t, r.stdout, "Other"))

	r = f.run("--json", "teams", "ls")
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, decode[[]map[string]any](t, r.stdout), 3)

	r = f.run("teams", "ls", "extra")
	require.Equal(t, 2, r.code, "ls takes no arguments")

	f.problem("GET", tmTeams, 401, "unauthorized", "token expired")
	r = f.run("teams", "ls")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "token expired (unauthorized, HTTP 401)")
}

// A Go handler's nil slice arrives as "items": null; --json still prints a list.
func TestTeamAndMemberListsPrintAnEmptyJSONList(t *testing.T) {
	f := newCfgFake(t)
	f.reply("GET", tmTeams, 200, map[string]any{"items": nil})
	r := f.run("--json", "teams", "ls")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, "[]", r.stdout)

	tmMeAnswer(f, tmTeamID)
	f.reply("GET", tmMembers, 200, map[string]any{"items": nil})
	r = f.run("--json", "members", "ls")
	require.Equal(t, 0, r.code, r.all())
	require.JSONEq(t, "[]", r.stdout)
}

func TestTeamsCreateSendsTheNameAndExplainsTokensAreTeamBound(t *testing.T) {
	f := newCfgFake(t)
	f.reply("POST", tmTeams, 201, map[string]any{"id": tmTeamID, "name": "Acme Inc", "slug": "acme-inc"})

	r := f.run("--idempotency-key", "team-key", "teams", "create", "Acme Inc")
	require.Equal(t, 0, r.code, r.all())
	require.Contains(t, r.stdout, "Created team Acme Inc (acme-inc)\n")
	require.Contains(t, r.stdout, "API tokens belong to one team: create a token inside it")
	post := f.only("POST", tmTeams)
	require.Equal(t, map[string]any{"name": "Acme Inc"}, post.json(t))
	require.Equal(t, "team-key", post.header.Get("Idempotency-Key"))

	r = f.run("--json", "teams", "create", "Acme Inc")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "acme-inc", decode[map[string]any](t, r.stdout)["slug"])
	require.NotContains(t, r.stdout, "API tokens belong")

	r = f.run("teams", "create")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "usage: nebulactrl teams create <name>")

	f.problem("POST", tmTeams, 403, "forbidden", "needs an admin-scope token")
	r = f.run("teams", "create", "x")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "needs an admin-scope token (forbidden, HTTP 403)")
}

func TestTeamsSwitchSelectsTheSavedTokenOfThatTeam(t *testing.T) {
	cfgEnv(t)
	path := os.Getenv(envConfig)
	cfgWrite(t, path, `{"url":"https://ctrl.example.com","token":"nct_acme","team":"acme","profiles":{"acme":"nct_acme","side":"nct_side"}}`)

	r := runLocal(t, "teams", "switch", " side ")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Now using team side\n", r.stdout)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved config
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.Equal(t, "side", saved.Team)
	require.Equal(t, "nct_side", saved.Token)
	require.Equal(t, "https://ctrl.example.com", saved.URL, "the control plane stays")
	require.Equal(t, map[string]string{"acme": "nct_acme", "side": "nct_side"}, saved.Profiles, "the other team's token is kept")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the token file stays private")
}

func TestTeamsSwitchWithoutASavedTokenSaysHowToGetOne(t *testing.T) {
	cfgEnv(t)
	path := os.Getenv(envConfig)
	original := `{"url":"https://ctrl.example.com","token":"nct_acme","team":"acme","profiles":{"acme":"nct_acme"}}`
	cfgWrite(t, path, original)

	r := runLocal(t, "teams", "switch", "ghost")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, `no API token saved for team "ghost"`)
	require.Contains(t, r.err, "nebulactrl login --url https://ctrl.example.com --token <token>")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(raw), "a refused switch leaves the config alone")

	r = runLocal(t, "teams", "switch")
	require.Equal(t, 2, r.code)
}

func TestTeamsSwitchRefusesAnUnreadableConfig(t *testing.T) {
	cfgEnv(t)
	cfgWrite(t, os.Getenv(envConfig), `{not json`)
	r := runLocal(t, "teams", "switch", "acme")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "parse "+os.Getenv(envConfig))
}

func tmMeAnswer(f *cfgFake, workspaceID string) {
	f.reply("GET", tmMe, 200, map[string]any{"id": "u-1", "email": "a@example.com", "workspaceId": workspaceID})
}

func TestMembersLsListsTheTokensTeam(t *testing.T) {
	f := newCfgFake(t)
	tmMeAnswer(f, tmTeamID)
	f.reply("GET", tmMembers, 200, map[string]any{"items": []any{
		map[string]any{"userId": "u-1", "name": "Alex", "email": "alex@example.com", "role": "owner", "joinedAt": "2026-01-02T03:04:05Z"},
		map[string]any{"userId": "u-2", "name": "Sam", "email": "sam@example.com", "role": "developer", "joinedAt": "2026-02-02T03:04:05Z", "projectIds": []string{"p1", "p2"}},
	}})

	r := f.run("members", "ls")
	require.Equal(t, 0, r.code, r.all())
	for _, h := range []string{"NAME", "EMAIL", "ROLE", "PROJECTS", "JOINED"} {
		require.Contains(t, r.stdout, h)
	}
	require.Equal(t, []string{"Alex", "alex@example.com", "owner", "all", "projects"}, bkLine(t, r.stdout, "Alex")[:5])
	require.Equal(t, []string{"Sam", "sam@example.com", "developer", "2", "projects"}, bkLine(t, r.stdout, "Sam")[:5],
		"a restricted member shows how many projects it can reach")

	r = f.run("--json", "members", "ls")
	require.Equal(t, 0, r.code, r.all())
	require.Len(t, decode[[]map[string]any](t, r.stdout), 2)
}

func TestMembersNeedATokenThatBelongsToATeam(t *testing.T) {
	f := newCfgFake(t)
	tmMeAnswer(f, "00000000-0000-0000-0000-000000000000")
	r := f.run("members", "ls")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the token belongs to no team")
	require.Empty(t, f.calls("GET", tmMembers))

	tmMeAnswer(f, "")
	r = f.run("members", "invite", "--role", "viewer")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "the token belongs to no team")
	require.Empty(t, f.calls("POST", tmInvites))

	f.problem("GET", tmMe, 401, "unauthorized", "token revoked")
	r = f.run("members", "ls")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "token revoked (unauthorized, HTTP 401)")
}

func TestMembersInviteRequiresARoleAndSendsTheOptions(t *testing.T) {
	f := newCfgFake(t)
	tmMeAnswer(f, tmTeamID)
	f.reply("POST", tmInvites, 201, map[string]any{"id": "inv-1", "link": "https://ctrl.example.com/invite/abc", "delivery": "sent"})

	before := f.total()
	r := f.run("members", "invite", "--email", "new@example.com")
	require.Equal(t, 2, r.code)
	require.Contains(t, r.err, "--role is required (owner, admin, developer or viewer)")
	require.Equal(t, before, f.total())

	r = f.run("--idempotency-key", "inv-key", "members", "invite", "--role", "developer", "--email", "new@example.com", "--max-uses", "5", "--expires-in-days", "14")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "Invite link (shown once): https://ctrl.example.com/invite/abc\nEmail: sent\n", r.stdout)
	post := f.only("POST", tmInvites)
	require.Equal(t, map[string]any{"role": "developer", "email": "new@example.com", "maxUses": float64(5), "expiresInDays": float64(14)}, post.json(t))
	require.Equal(t, "inv-key", post.header.Get("Idempotency-Key"))

	r = f.run("--json", "members", "invite", "--role", "viewer")
	require.Equal(t, 0, r.code, r.all())
	require.Equal(t, "https://ctrl.example.com/invite/abc", decode[map[string]any](t, r.stdout)["link"])
	require.Equal(t, map[string]any{"role": "viewer"}, f.calls("POST", tmInvites)[1].json(t), "a link invite sends only the role")

	f.problem("POST", tmInvites, 403, "forbidden", "only owners may invite owners")
	r = f.run("members", "invite", "--role", "owner")
	require.Equal(t, 1, r.code)
	require.Contains(t, r.err, "only owners may invite owners (forbidden, HTTP 403)")
}
