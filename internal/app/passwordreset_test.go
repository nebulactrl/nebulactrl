package app_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// resetBox records the reset emails the control plane sends.
type resetBox struct {
	mu   sync.Mutex
	sent []auth.ResetMail
}

func (b *resetBox) Available(context.Context) bool { return true }

func (b *resetBox) SendPasswordReset(_ context.Context, m auth.ResetMail) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, m)
	return nil
}

func (b *resetBox) mails() []auth.ResetMail {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]auth.ResetMail(nil), b.sent...)
}

// withMail gives the harness a public URL and a recording mailer: reset by
// email is then available.
func (h *harness) withMail() *resetBox {
	h.t.Helper()
	h.app.Auth.Configure(h.app.Box, "https://ctrl.example.com")
	b := &resetBox{}
	h.app.Auth.SetResetMailer(b)
	return b
}

func (b *resetBox) await(t *testing.T, n int) []auth.ResetMail {
	t.Helper()
	require.Eventually(t, func() bool { return len(b.mails()) >= n }, 3*time.Second, 5*time.Millisecond)
	return b.mails()
}

func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	_, tok, ok := strings.Cut(link, "?token=")
	require.True(t, ok, link)
	return tok
}

func TestForgotPasswordIsOffUntilEmailCanBeSent(t *testing.T) {
	h := newHarness(t)
	h.setup()
	anon := h.otherBrowser()

	st := anon.do("GET", "/auth/password-reset", "", nil)
	require.Equal(t, 200, st.Status, string(st.Raw))
	require.Equal(t, false, st.JSON()["available"], "no SMTP: the login page says to ask an admin")
	r := anon.do("POST", "/auth/password-reset/request", "", map[string]any{"email": "admin@example.com"})
	require.Equal(t, 503, r.Status, string(r.Raw))
	require.Equal(t, "password_reset_unavailable", r.JSON()["code"])

	h.withMail()
	require.Equal(t, true, anon.do("GET", "/auth/password-reset", "", nil).JSON()["available"])
}

func TestForgotPasswordFlowSignsEveryDeviceOut(t *testing.T) {
	h := newHarness(t)
	h.setup()
	box := h.withMail()
	laptop, phone := h.otherBrowser(), h.otherBrowser()
	require.Equal(t, 200, laptop.login("admin@example.com", testPassword).Status)
	require.Equal(t, 200, phone.login("admin@example.com", testPassword).Status)
	require.Equal(t, 200, h.do("GET", "/me", "", nil).Status)
	apiTok := h.createToken([]string{"projects:read"}, nil)

	anon := h.otherBrowser()
	req := anon.do("POST", "/auth/password-reset/request", "", map[string]any{"email": "Admin@Example.com"})
	require.Equal(t, 202, req.Status, string(req.Raw))
	require.Equal(t, "requested", req.JSON()["status"])
	mails := box.await(t, 1)
	require.Equal(t, "admin@example.com", mails[0].To)
	require.True(t, strings.HasPrefix(mails[0].Link, "https://ctrl.example.com/reset-password?token="))
	tok := tokenFromLink(t, mails[0].Link)

	// A weak password is refused and keeps the link.
	weak := anon.do("POST", "/auth/password-reset/confirm", "", map[string]any{"token": tok, "password": "tiny"})
	require.Equal(t, 422, weak.Status, string(weak.Raw))

	ok := anon.do("POST", "/auth/password-reset/confirm", "", map[string]any{"token": tok, "password": "a brand new passphrase"})
	require.Equal(t, 200, ok.Status, string(ok.Raw))
	require.EqualValues(t, 0, ok.JSON()["revokedTokens"], "tokens are kept unless the person asks")
	kept, _ := ok.JSON()["keptTokens"].([]any)
	require.Len(t, kept, 1, string(ok.Raw))
	require.Equal(t, "test", kept[0].(map[string]any)["name"])

	// Single use.
	again := anon.do("POST", "/auth/password-reset/confirm", "", map[string]any{"token": tok, "password": "yet another passphrase"})
	require.Equal(t, 400, again.Status, string(again.Raw))
	require.Equal(t, "invalid_reset_token", again.JSON()["code"])

	// Every session of the account ended, the old password is dead, the new one works.
	for name, b := range map[string]*harness{"first browser": h, "laptop": laptop, "phone": phone} {
		require.Equal(t, 401, b.do("GET", "/me", "", nil).Status, name)
	}
	require.Equal(t, 401, anon.login("admin@example.com", testPassword).Status)
	require.Equal(t, 200, anon.login("admin@example.com", "a brand new passphrase").Status)
	require.Equal(t, 200, h.otherBrowser().do("GET", "/projects", apiTok, nil).Status, "the API token the answer listed still works")

	// The reset is in the team's audit trail, as the member's own act.
	audit := anon.do("GET", "/audit-events", "", nil)
	require.Equal(t, 200, audit.Status, string(audit.Raw))
	var found bool
	for _, it := range audit.JSON()["items"].([]any) {
		e := it.(map[string]any)
		if e["action"] == "user.password_reset" {
			found = true
			require.Equal(t, "user", e["actorType"])
			require.NotContains(t, e["metadata"], "token")
		}
	}
	require.True(t, found, string(audit.Raw))
}

// Unknown addresses, accounts without a password and real accounts all get the
// same answer: the endpoint cannot be used to list who has an account.
func TestForgotPasswordAnswersTheSameForEveryone(t *testing.T) {
	h := newHarness(t)
	h.setup()
	box := h.withMail()
	ctx := context.Background()
	_, err := db.New(h.app.Pool).InsertUser(ctx, db.InsertUserParams{Email: "sso@example.com", Name: "SSO"}) // no password
	require.NoError(t, err)
	anon := h.otherBrowser()

	var bodies []string
	for _, email := range []string{"admin@example.com", "nobody@example.com", "sso@example.com", "not-an-email"} {
		r := anon.do("POST", "/auth/password-reset/request", "", map[string]any{"email": email})
		require.Equal(t, 202, r.Status, email+": "+string(r.Raw))
		bodies = append(bodies, string(r.Raw))
	}
	for _, b := range bodies[1:] {
		require.Equal(t, bodies[0], b)
	}
	box.await(t, 1)
	time.Sleep(150 * time.Millisecond)
	mails := box.mails()
	require.Len(t, mails, 1, "only the account with a password got an email")
	require.Equal(t, "admin@example.com", mails[0].To)
}

func TestForgotPasswordIsRateLimitedPerEmailAndIPWithoutLeaking(t *testing.T) {
	h := newHarness(t)
	h.setup()
	h.withMail()
	anon := h.otherBrowser()

	// Per email: three an hour, the same for a real and an unknown address.
	for _, email := range []string{"admin@example.com", "nobody@example.com"} {
		for i := range 3 {
			r := anon.do("POST", "/auth/password-reset/request", "", map[string]any{"email": email})
			require.Equal(t, 202, r.Status, email+string(rune('0'+i)))
		}
		r := anon.do("POST", "/auth/password-reset/request", "", map[string]any{"email": strings.ToUpper(email)})
		require.Equal(t, 429, r.Status, email+": "+string(r.Raw))
	}

	// Per client IP: ten an hour across addresses, limited requests included.
	// Eight were used above.
	for i := range 2 {
		r := anon.do("POST", "/auth/password-reset/request", "", map[string]any{"email": strings.Repeat("a", i+1) + "@example.com"})
		require.Equal(t, 202, r.Status, string(r.Raw))
	}
	r := anon.do("POST", "/auth/password-reset/request", "", map[string]any{"email": "fresh@example.com"})
	require.Equal(t, 429, r.Status, string(r.Raw))
}

func TestPasswordResetConfirmIsRateLimited(t *testing.T) {
	h := newHarness(t)
	h.setup()
	anon := h.otherBrowser()
	var last resp
	for range 11 {
		last = anon.do("POST", "/auth/password-reset/confirm", "", map[string]any{"token": "ncr_" + strings.Repeat("a", 52), "password": "a brand new passphrase"})
	}
	require.Equal(t, 429, last.Status, string(last.Raw))
}

func TestForgotPasswordNeverMailsProviderOnlyAccountsAndConfirmRefusesThem(t *testing.T) {
	h := newHarness(t)
	h.setup()
	box := h.withMail()
	sso, err := db.New(h.app.Pool).InsertUser(context.Background(), db.InsertUserParams{Email: "sso@example.com", Name: "SSO"})
	require.NoError(t, err)
	require.Equal(t, 202, h.otherBrowser().do("POST", "/auth/password-reset/request", "", map[string]any{"email": "sso@example.com"}).Status)
	time.Sleep(100 * time.Millisecond)
	require.Empty(t, box.mails())
	var n int
	require.NoError(t, h.app.Pool.QueryRow(context.Background(), `SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, sso.ID).Scan(&n))
	require.Zero(t, n)
}

// ---- admin generated links ----

func TestAdminResetLinkForAMember(t *testing.T) {
	h := newHarness(t) // no SMTP: the link is handed over
	h.setup()
	team := h.teamID()
	dev := h.memberOf(team, "developer", "dev@example.com")
	devID := dev.userID()
	admin := h.memberOf(team, "admin", "boss@example.com")
	other := h.seedAccount("someone@else.example.com", "Else")
	require.Equal(t, 200, dev.do("GET", "/me", "", nil).Status)
	devTok := dev.createToken([]string{"projects:read"}, nil)

	// Developers cannot generate links; nobody can for themselves or for non-members.
	require.Equal(t, 403, dev.do("POST", "/teams/"+team+"/members/"+h.userID()+"/password-reset", "", nil).Status)
	self := h.do("POST", "/teams/"+team+"/members/"+h.userID()+"/password-reset", "", nil)
	require.Equal(t, 422, self.Status, string(self.Raw))
	require.Equal(t, 404, h.do("POST", "/teams/"+team+"/members/"+other.userID()+"/password-reset", "", nil).Status, "not a member of this team")
	// An admin cannot take over an owner.
	require.Equal(t, 403, admin.do("POST", "/teams/"+team+"/members/"+h.userID()+"/password-reset", "", nil).Status)

	r := h.do("POST", "/teams/"+team+"/members/"+devID+"/password-reset", "", nil)
	require.Equal(t, 201, r.Status, string(r.Raw))
	tok := r.JSON()["token"].(string)
	require.True(t, strings.HasPrefix(tok, "ncr_"))
	require.Equal(t, "/reset-password?token="+tok, r.JSON()["link"], "a path when no public URL is set")
	require.NotEmpty(t, r.JSON()["expiresAt"])

	// An admin may reset a developer; the link works once and signs them out.
	require.Equal(t, 201, admin.do("POST", "/teams/"+team+"/members/"+devID+"/password-reset", "", nil).Status)
	anon := h.otherBrowser()
	done := anon.do("POST", "/auth/password-reset/confirm", "", map[string]any{"token": tok, "password": "a brand new passphrase", "revokeApiTokens": true})
	require.Equal(t, 200, done.Status, string(done.Raw))
	require.EqualValues(t, 1, done.JSON()["revokedTokens"])
	require.Empty(t, done.JSON()["keptTokens"], string(done.Raw))
	require.Equal(t, 400, anon.do("POST", "/auth/password-reset/confirm", "", map[string]any{"token": tok, "password": "a brand new passphrase"}).Status)
	require.Equal(t, 401, dev.do("GET", "/me", "", nil).Status, "their session ended")
	require.Equal(t, 401, h.otherBrowser().do("GET", "/projects", devTok, nil).Status, "their API token ended with the reset")
	require.Equal(t, 200, anon.login("dev@example.com", "a brand new passphrase").Status)

	// Audited in the team, with no token in it.
	audit := h.do("GET", "/audit-events", "", nil)
	var links int
	for _, it := range audit.JSON()["items"].([]any) {
		e := it.(map[string]any)
		if e["action"] == "member.password_reset_link" {
			links++
		}
	}
	require.Equal(t, 2, links)
	require.NotContains(t, string(audit.Raw), tok)

	// With a public URL the link is absolute.
	h.withMail()
	abs := h.do("POST", "/teams/"+team+"/members/"+devID+"/password-reset", "", nil)
	require.True(t, strings.HasPrefix(abs.JSON()["link"].(string), "https://ctrl.example.com/reset-password?token="))
}

func TestAdminResetLinkRefusesProviderOnlyMembersAndTokens(t *testing.T) {
	h := newHarness(t)
	h.setup()
	team := h.teamID()
	ctx := context.Background()
	q := db.New(h.app.Pool)
	sso, err := q.InsertUser(ctx, db.InsertUserParams{Email: "sso@example.com", Name: "SSO"})
	require.NoError(t, err)
	_, err = q.InsertMembership(ctx, db.InsertMembershipParams{UserID: sso.ID, WorkspaceID: uuid.MustParse(team), Role: "developer"})
	require.NoError(t, err)

	r := h.do("POST", "/teams/"+team+"/members/"+sso.ID.String()+"/password-reset", "", nil)
	require.Equal(t, 409, r.Status, string(r.Raw))
	require.Equal(t, "no_password", r.JSON()["code"])

	// A token, even a full-access one, cannot mint account takeover links.
	dev := h.memberOf(team, "developer", "dev@example.com")
	tok := h.createToken([]string{"admin"}, nil)
	require.Equal(t, 403, h.do("POST", "/teams/"+team+"/members/"+dev.userID()+"/password-reset", tok, nil).Status)
}
