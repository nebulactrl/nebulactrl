package app_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/app"
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

// ---- first-run setup without NEBULACTRL_SETUP_TOKEN ----

func TestFirstRunGeneratesASetupTokenWhenNoneIsConfigured(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := &config.Config{SecretKey: bytes.Repeat([]byte{7}, 32), Listen: ":0", PublicURL: "https://ctrl.example.com"}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	token, err := app.EnsureSetupToken(context.Background(), cfg, pool, logger)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Equal(t, token, cfg.Installer.SetupToken)
	require.Contains(t, logs.String(), token, "the token is written to the server log")
	require.Contains(t, logs.String(), "https://ctrl.example.com/setup?token="+token)

	a, err := app.Build(cfg, pool, logger)
	require.NoError(t, err)
	t.Cleanup(a.Close)
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	h := &harness{t: t, srv: srv, app: a, client: &http.Client{Jar: jar}}

	st := h.do("GET", "/setup", "", nil)
	require.Equal(t, true, st.JSON()["needsSetup"])
	require.Equal(t, true, st.JSON()["tokenRequired"])
	body := map[string]any{"email": "admin@example.com", "password": testPassword, "name": "Admin"}
	r := h.do("POST", "/setup", "", body)
	require.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	require.Equal(t, "setup_token_invalid", r.JSON()["code"])
	body["setupToken"] = token
	r = h.do("POST", "/setup", "", body)
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))

	// Once an admin exists nothing is generated: there is nothing left to guard.
	again := &config.Config{SecretKey: cfg.SecretKey}
	token, err = app.EnsureSetupToken(context.Background(), again, pool, logger)
	require.NoError(t, err)
	require.Empty(t, token)
	require.Empty(t, again.Installer.SetupToken)
}

func TestConfiguredSetupTokenIsKept(t *testing.T) {
	pool := testutil.NewPool(t)
	cfg := &config.Config{}
	cfg.Installer.SetupToken = "from-the-installer"
	token, err := app.EnsureSetupToken(context.Background(), cfg, pool, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.Empty(t, token)
	require.Equal(t, "from-the-installer", cfg.Installer.SetupToken)
}

// ---- limits are keyed on the visitor, not on the proxy ----

func TestLoginLimitIsPerVisitorBehindTheProxy(t *testing.T) {
	h := newHarness(t)
	h.setup()
	anon := h.otherBrowser()
	fromA := []string{"X-Forwarded-For", "198.51.100.10"}
	fromB := []string{"X-Forwarded-For", "198.51.100.20"}

	// One visitor spraying different accounts is limited ...
	for i := range 10 {
		r := anon.do("POST", "/auth/login", "", map[string]any{"email": fmt.Sprintf("nobody%d@example.com", i), "password": "wrong password"}, fromA...)
		require.Equal(t, 401, r.Status, string(r.Raw))
	}
	limited := anon.do("POST", "/auth/login", "", map[string]any{"email": "nobody-else@example.com", "password": "wrong password"}, fromA...)
	require.Equal(t, 429, limited.Status, string(limited.Raw))
	// ... and another visitor behind the same proxy is not: the proxy's own
	// address is never the key.
	other := anon.do("POST", "/auth/login", "", map[string]any{"email": "admin@example.com", "password": testPassword}, fromB...)
	require.Equal(t, 200, other.Status, string(other.Raw))
}

func TestLoginLimitIsPerAccountWhateverTheAddress(t *testing.T) {
	h := newHarness(t)
	h.setup()
	anon := h.otherBrowser()
	for i := range 10 {
		r := anon.do("POST", "/auth/login", "", map[string]any{"email": "admin@example.com", "password": "wrong password"},
			"X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1))
		require.Equal(t, 401, r.Status, string(r.Raw))
	}
	// An 11th address finds the account locked, even with the right password.
	r := anon.do("POST", "/auth/login", "", map[string]any{"email": "admin@example.com", "password": testPassword}, "X-Forwarded-For", "198.51.100.99")
	require.Equal(t, 429, r.Status, string(r.Raw))
	// Other accounts are unaffected.
	r = anon.do("POST", "/auth/login", "", map[string]any{"email": "someone-else@example.com", "password": "x"}, "X-Forwarded-For", "198.51.100.99")
	require.Equal(t, 401, r.Status, string(r.Raw))
}

func TestVisitorWrittenForwardedEntriesDoNotResetTheLimit(t *testing.T) {
	// The test server's peer is loopback, which stands for the proxy: only the
	// last entry, the address the proxy itself saw, is the visitor. Rotating the
	// entries before it (which the visitor wrote) must not reset the limit.
	h := newHarness(t)
	h.setup()
	anon := h.otherBrowser()
	for i := range 10 {
		r := anon.do("POST", "/auth/login", "", map[string]any{"email": fmt.Sprintf("n%d@example.com", i), "password": "wrong password"},
			"X-Forwarded-For", fmt.Sprintf("10.9.9.%d, 198.51.100.50", i)) // forged leading entries rotate; the last one is the proxy's
		require.Equal(t, 401, r.Status, string(r.Raw))
	}
	r := anon.do("POST", "/auth/login", "", map[string]any{"email": "n-last@example.com", "password": "wrong password"},
		"X-Forwarded-For", "10.7.7.7, 198.51.100.50")
	require.Equal(t, 429, r.Status, string(r.Raw), "rotating the entries the visitor wrote does not reset the limit")
}

// ---- password change and reset ----

func TestChangingThePasswordEndsOtherSessionsAndKeepsTokens(t *testing.T) {
	h := newHarness(t)
	h.setup()
	laptop, phone := h.otherBrowser(), h.otherBrowser()
	require.Equal(t, 200, laptop.login("admin@example.com", testPassword).Status)
	require.Equal(t, 200, phone.login("admin@example.com", testPassword).Status)
	tok := h.createToken([]string{"projects:read"}, nil)

	const next = "a brand new passphrase"
	r := h.do("POST", "/me/password", "", map[string]any{"currentPassword": testPassword, "newPassword": next})
	require.Equal(t, 200, r.Status, string(r.Raw))
	require.Equal(t, float64(0), r.JSON()["revokedTokens"])
	kept := r.JSON()["keptTokens"].([]any)
	require.Len(t, kept, 1, "the tokens that still work are shown")
	require.Equal(t, "test", kept[0].(map[string]any)["name"])
	require.NotContains(t, string(r.Raw), tok, "never the secret")

	// This browser stays signed in; every other session ended; tokens keep working.
	require.Equal(t, 200, h.do("GET", "/me", "", nil).Status)
	require.Equal(t, 401, laptop.do("GET", "/me", "", nil).Status)
	require.Equal(t, 401, phone.do("GET", "/me", "", nil).Status)
	require.Equal(t, 200, h.do("GET", "/projects", tok, nil).Status)
	require.Equal(t, 200, h.otherBrowser().login("admin@example.com", next).Status)

	// The person can choose to revoke the tokens too.
	r = h.do("POST", "/me/password", "", map[string]any{"currentPassword": next, "newPassword": testPassword, "revokeApiTokens": true})
	require.Equal(t, 200, r.Status, string(r.Raw))
	require.Equal(t, float64(1), r.JSON()["revokedTokens"])
	require.Empty(t, r.JSON()["keptTokens"])
	require.Equal(t, 401, h.do("GET", "/projects", tok, nil).Status)
	require.Equal(t, 200, h.do("GET", "/me", "", nil).Status)
}

func TestWrongCurrentPasswordsAreLimited(t *testing.T) {
	h := newHarness(t)
	h.setup()
	for range 10 {
		r := h.do("POST", "/me/password", "", map[string]any{"currentPassword": "not the password", "newPassword": "another passphrase"})
		require.Equal(t, 403, r.Status, string(r.Raw))
	}
	r := h.do("POST", "/me/password", "", map[string]any{"currentPassword": testPassword, "newPassword": "another passphrase"})
	require.Equal(t, 429, r.Status, string(r.Raw))
}

func TestResetKeepsTokensUnlessTheUserChoosesAndTheLinkCanBeChecked(t *testing.T) {
	h := newHarness(t) // no SMTP: the admin hands the link over
	h.setup()
	team := h.teamID()
	keep := h.memberOf(team, "developer", "keep@example.com")
	drop := h.memberOf(team, "developer", "drop@example.com")
	for _, m := range []*harness{keep, drop} {
		r := m.do("POST", "/tokens", "", map[string]any{"name": "ci", "scopes": []string{"projects:read"}})
		require.Equal(t, 201, r.Status, string(r.Raw))
	}
	link := func(m *harness) string {
		r := h.do("POST", "/teams/"+team+"/members/"+m.userID()+"/password-reset", "", nil)
		require.Equal(t, 201, r.Status, string(r.Raw))
		return r.JSON()["token"].(string)
	}
	anon := h.otherBrowser()

	tokKeep := link(keep)
	// Checking a link does not spend it; a bad one is refused the same way confirm does.
	for i := 0; i < 2; i++ {
		require.Equal(t, 204, anon.do("POST", "/auth/password-reset/check", "", map[string]any{"token": tokKeep}).Status)
	}
	bad := anon.do("POST", "/auth/password-reset/check", "", map[string]any{"token": "ncr_nonsense"})
	require.Equal(t, 400, bad.Status, string(bad.Raw))
	require.Equal(t, "invalid_reset_token", bad.JSON()["code"])

	r := anon.do("POST", "/auth/password-reset/confirm", "", map[string]any{"token": tokKeep, "password": "a brand new passphrase"})
	require.Equal(t, 200, r.Status, string(r.Raw))
	require.Equal(t, float64(0), r.JSON()["revokedTokens"])
	kept := r.JSON()["keptTokens"].([]any)
	require.Len(t, kept, 1, "the tokens that still work are shown")
	require.Equal(t, "ci", kept[0].(map[string]any)["name"])
	require.Equal(t, 400, anon.do("POST", "/auth/password-reset/check", "", map[string]any{"token": tokKeep}).Status, "a used link is dead")

	tokDrop := link(drop)
	r = anon.do("POST", "/auth/password-reset/confirm", "", map[string]any{"token": tokDrop, "password": "a brand new passphrase", "revokeApiTokens": true})
	require.Equal(t, 200, r.Status, string(r.Raw))
	require.Equal(t, float64(1), r.JSON()["revokedTokens"])
	require.Empty(t, r.JSON()["keptTokens"])
}

// ---- reset links for people who belong to other teams ----

func TestResetLinksForMembersOfOtherTeamsComeFromTheInstanceAdminOnly(t *testing.T) {
	h := newHarness(t) // the instance admin owns the first team
	h.setup()
	team := h.teamID()
	boss := h.memberOf(team, "owner", "boss@example.com") // an owner who is not the instance admin
	solo := h.memberOf(team, "developer", "solo@example.com")

	// A person whose account lives only in this team: the owner may reset them.
	require.Equal(t, 201, boss.do("POST", "/teams/"+team+"/members/"+solo.userID()+"/password-reset", "", nil).Status)

	// A person who also belongs to another team: only the instance admin may.
	victim := h.seedAccount("victim@else.example.com", "Victim's team")
	tok := h.inviteLink(team, "developer", nil)
	require.Equal(t, 200, victim.do("POST", "/invites/"+tok+"/accept", "", map[string]any{}).Status)
	refused := boss.do("POST", "/teams/"+team+"/members/"+victim.userID()+"/password-reset", "", nil)
	require.Equal(t, 409, refused.Status, string(refused.Raw))
	require.Equal(t, "member_of_other_teams", refused.JSON()["code"])
	require.Equal(t, 201, h.do("POST", "/teams/"+team+"/members/"+victim.userID()+"/password-reset", "", nil).Status)

	// Nobody but the operator takes over the instance admin.
	admin := boss.do("POST", "/teams/"+team+"/members/"+h.userID()+"/password-reset", "", nil)
	require.Equal(t, 403, admin.Status, string(admin.Raw))
	require.Equal(t, "instance_admin_account", admin.JSON()["code"])
}

// ---- a team's 2FA applies from any team ----

func TestTeamTwoFactorAppliesToTeamActionsFromAnotherTeam(t *testing.T) {
	h := newHarness(t)
	clock := newTestClock()
	h.app.Auth.SetClock(clock.Now)
	h.setup()
	team := h.teamID()
	dev := h.memberOf(team, "admin", "dev@example.com")
	// The member has a team of their own to act from.
	mine := dev.do("POST", "/teams", "", map[string]any{"name": "Dev's own team"})
	require.Equal(t, 201, mine.Status, string(mine.Raw))
	require.Equal(t, 200, dev.do("POST", "/teams/"+mine.JSON()["id"].(string)+"/switch", "", nil).Status)
	require.Equal(t, mine.JSON()["id"], dev.teamID(), "the session acts in the other team")

	// Before the requirement, the member administers the team from there.
	require.Equal(t, 200, dev.do("GET", "/teams/"+team+"/members", "", nil).Status)

	h.enrolTOTP(clock)
	require.Equal(t, 200, h.do("PUT", "/teams/"+team+"/security", "", map[string]any{"require2fa": true}).Status)

	// Now every action on that team is refused, whichever team the session is in.
	for name, r := range map[string]resp{
		"members": dev.do("GET", "/teams/"+team+"/members", "", nil),
		"invite":  dev.do("POST", "/teams/"+team+"/invites", "", map[string]any{"role": "developer"}),
		"rename":  dev.do("PATCH", "/teams/"+team, "", map[string]any{"name": "Taken over"}),
	} {
		require.Equal(t, 403, r.Status, name+": "+string(r.Raw))
		require.Equal(t, "two_factor_enrollment_required", r.JSON()["code"], name)
	}
	// Their own team is unaffected, and they can still leave the team that blocks them.
	require.Equal(t, 200, dev.do("GET", "/teams/"+mine.JSON()["id"].(string)+"/members", "", nil).Status)
	require.Equal(t, 204, dev.do("DELETE", "/teams/"+team+"/members/"+dev.userID(), "", nil).Status)
}

// ---- invite previews do not tell which emails have an account ----

func TestInvitePreviewDoesNotRevealAccounts(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	h.seedAccount("existing@example.com", "Existing's team")
	known := h.inviteLink(team, "developer", map[string]any{"email": "existing@example.com"})
	unknown := h.inviteLink(team, "developer", map[string]any{"email": "nobody@example.com"})
	a := h.otherBrowser().do("GET", "/invites/"+known, "", nil)
	b := h.otherBrowser().do("GET", "/invites/"+unknown, "", nil)
	require.Equal(t, 200, a.Status, string(a.Raw))
	require.Equal(t, 200, b.Status, string(b.Raw))
	// Same shape for an address with an account and one without: nothing in the
	// answer tells them apart.
	require.NotContains(t, a.JSON(), "accountExists")
	require.NotContains(t, b.JSON(), "accountExists")
	require.Equal(t, len(a.JSON()), len(b.JSON()))
}
