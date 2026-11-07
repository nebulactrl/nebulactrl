package teams

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

func (f *tf) invite(in InviteInput) CreatedInvite {
	f.t.Helper()
	c, err := f.svc.CreateInvite(f.asAda(), f.team, in)
	require.NoError(f.t, err)
	return c
}

func (f *tf) expire(inviteID uuid.UUID) {
	f.t.Helper()
	f.exec(`UPDATE team_invites SET expires_at = now() - interval '1 hour' WHERE id=$1`, inviteID)
}

func TestCreateLinkInviteDefaultsAndStorage(t *testing.T) {
	f := newTf(t)
	p1 := f.project("One")

	c := f.invite(InviteInput{Role: auth.RoleDeveloper, ProjectIDs: []uuid.UUID{p1, p1}})
	require.True(t, strings.HasPrefix(c.Token, InvitePrefix))
	require.Equal(t, "/invite/"+c.Token, c.Link, "a path when no public URL is set")
	require.Equal(t, DeliveryLinkOnly, c.Delivery)
	require.Empty(t, c.EmailError)
	inv := c.Invite
	require.Nil(t, inv.Email)
	require.Equal(t, auth.RoleDeveloper, inv.Role)
	require.Equal(t, []uuid.UUID{p1}, inv.ProjectIDs)
	require.Equal(t, 1, inv.MaxUses, "single use by default")
	require.Zero(t, inv.UseCount)
	require.False(t, inv.EmailSent)
	require.False(t, inv.Expired)
	require.False(t, inv.Exhausted)
	require.Equal(t, strings.TrimPrefix(c.Token, InvitePrefix)[:8], inv.Prefix)
	require.WithinDuration(t, f.now.Add(DefaultInviteTTL), inv.ExpiresAt, time.Second)
	require.Equal(t, "Ada", inv.CreatedBy)

	// Only the hash is stored, never the token.
	require.Equal(t, 1, f.count(`SELECT count(*) FROM team_invites WHERE token_hash=$1`, hashInvite(c.Token)))
	require.Zero(t, f.count(`SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%'||$1||'%'`, c.Token))
	require.Equal(t, 1, f.audits("invite.create"))

	f.withPublicURL("https://nebula.example.com/")
	c = f.invite(InviteInput{Role: auth.RoleViewer, MaxUses: 5, TTL: 24 * time.Hour})
	require.Equal(t, "https://nebula.example.com/invite/"+c.Token, c.Link)
	require.Equal(t, 5, c.Invite.MaxUses)
	require.WithinDuration(t, f.now.Add(24*time.Hour), c.Invite.ExpiresAt, time.Second)

	// Two tokens never collide.
	require.NotEqual(t, c.Token, f.invite(InviteInput{Role: auth.RoleViewer}).Token)

	// Owners and admins get everything: a restriction is dropped.
	c = f.invite(InviteInput{Role: auth.RoleAdmin, ProjectIDs: []uuid.UUID{p1}})
	require.Empty(t, c.Invite.ProjectIDs)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action='invite.create' AND metadata->>'role'='admin' AND metadata->>'restricted'='false'`))
}

func TestCreateInviteValidationAndPermissions(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	create := func(ctx context.Context, in InviteInput) error {
		_, err := f.svc.CreateInvite(ctx, f.team, in)
		return err
	}

	requireAPI(t, create(f.asAda(), InviteInput{Role: "root"}), 422, "invalid_role")
	requireAPI(t, create(f.asAda(), InviteInput{Role: ""}), 422, "invalid_role")
	requireAPI(t, create(f.asAda(), InviteInput{Role: auth.RoleViewer, Email: "not-an-email"}), 422, "invalid_email")
	requireAPI(t, create(f.asAda(), InviteInput{Role: auth.RoleViewer, Email: "Name <a@b.example>"}), 422, "invalid_email")
	requireAPI(t, create(f.asAda(), InviteInput{Role: auth.RoleViewer, Email: strings.Repeat("a", 250) + "@b.example"}), 422, "invalid_email")
	requireAPI(t, create(f.asAda(), InviteInput{Role: auth.RoleViewer, MaxUses: MaxInviteUses + 1}), 422, "invalid_max_uses")
	requireAPI(t, create(f.asAda(), InviteInput{Role: auth.RoleViewer, TTL: MaxInviteTTL + time.Second}), 422, "invalid_expiry")
	requireAPI(t, create(f.asAda(), InviteInput{Role: auth.RoleDeveloper, ProjectIDs: []uuid.UUID{uuid.New()}}), 422, "unknown_project")
	require.Zero(t, f.count(`SELECT count(*) FROM team_invites`), "refused invites are not stored")

	// The limits themselves are fine.
	require.NoError(t, create(f.asAda(), InviteInput{Role: auth.RoleViewer, MaxUses: MaxInviteUses, TTL: MaxInviteTTL}))
	// Negative uses count as the default.
	c, err := f.svc.CreateInvite(f.asAda(), f.team, InviteInput{Role: auth.RoleViewer, MaxUses: -3, TTL: -time.Hour})
	require.NoError(t, err)
	require.Equal(t, 1, c.Invite.MaxUses)
	require.WithinDuration(t, f.now.Add(DefaultInviteTTL), c.Invite.ExpiresAt, time.Second)

	// Only owners hand out ownership; developers and viewers hand out nothing.
	requireAPI(t, create(f.as(bob, f.team), InviteInput{Role: auth.RoleOwner}), 403, "forbidden")
	require.NoError(t, create(f.as(bob, f.team), InviteInput{Role: auth.RoleAdmin}))
	require.NoError(t, create(f.asAda(), InviteInput{Role: auth.RoleOwner}))
	requireAPI(t, create(f.as(cy, f.team), InviteInput{Role: auth.RoleViewer}), 403, "forbidden")
	requireAPI(t, create(f.as(f.user("eve@example.com", ""), f.team), InviteInput{Role: auth.RoleViewer}), 404, "not_found")
	// Tokens need the admin scope.
	requireAPI(t, create(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeProjectsWrite), InviteInput{Role: auth.RoleViewer}), 403, "forbidden")
	require.NoError(t, create(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeAdmin), InviteInput{Role: auth.RoleViewer}))
	requireAPI(t, create(context.Background(), InviteInput{Role: auth.RoleViewer}), 401, "unauthorized")
}

func TestEmailInviteIsSingleUseNormalisedAndRefusesExistingMembers(t *testing.T) {
	f := newTf(t)
	f.withPublicURL("https://nebula.example.com")
	f.member("bob@example.com", auth.RoleViewer)

	c := f.invite(InviteInput{Email: "  New.Person@Example.COM ", Role: auth.RoleDeveloper, MaxUses: 50})
	require.Equal(t, "new.person@example.com", *c.Invite.Email)
	require.Equal(t, 1, c.Invite.MaxUses, "an email invite is for one person")

	// Someone already in the team cannot be invited again, and nothing is mailed.
	mailsBefore := len(f.mailer.sent)
	_, err := f.svc.CreateInvite(f.asAda(), f.team, InviteInput{Email: "BOB@example.com", Role: auth.RoleViewer})
	requireAPI(t, err, 409, "already_member")
	require.Len(t, f.mailer.sent, mailsBefore)
	// An account that exists but is not in this team can be invited.
	f.user("eve@example.com", "")
	_, err = f.svc.CreateInvite(f.asAda(), f.team, InviteInput{Email: "eve@example.com", Role: auth.RoleViewer})
	require.NoError(t, err)
}

func TestInviteEmailDelivery(t *testing.T) {
	f := newTf(t)
	in := InviteInput{Email: "new@example.com", Role: auth.RoleDeveloper}

	// No mailer at all: the link is the only way.
	f.svc.mailer = nil
	c := f.invite(in)
	require.Equal(t, DeliveryNotConfigured, c.Delivery)
	require.Empty(t, c.EmailError)
	require.False(t, c.Invite.EmailSent)
	f.svc.mailer = f.mailer

	// A mailer but no public URL: the emailed link would be relative.
	c = f.invite(in)
	require.Equal(t, DeliveryNotConfigured, c.Delivery)
	require.Contains(t, c.EmailError, "NEBULACTRL_PUBLIC_URL")
	require.Empty(t, f.mailer.sent)

	// Sent: the mailer gets the content, the invite is marked, and the relay is the instance's.
	f.withPublicURL("https://nebula.example.com")
	host, from, tls := "smtp.example.com", "NebulaCtrl <no-reply@example.com>", "implicit"
	_, err := f.svc.UpdateInstance(f.asAda(), InstancePatch{SMTPHost: &host, MailFrom: &from, SMTPTLS: &tls})
	require.NoError(t, err)
	c = f.invite(in)
	require.Equal(t, DeliverySent, c.Delivery)
	require.True(t, c.Invite.EmailSent)
	require.Len(t, f.mailer.sent, 1)
	got := f.mailer.sent[0]
	require.Equal(t, f.team, got.team)
	require.Equal(t, "new@example.com", got.mail.To)
	require.Equal(t, "Home", got.mail.TeamName)
	require.Equal(t, "Ada", got.mail.InviterName)
	require.Equal(t, auth.RoleDeveloper, got.mail.Role)
	require.Equal(t, c.Link, got.mail.Link)
	require.Equal(t, c.Invite.ExpiresAt, got.mail.ExpiresAt)
	require.Equal(t, host, got.relay.Host)
	require.Equal(t, from, got.relay.From)
	listed, err := f.svc.Invites(f.asAda(), f.team)
	require.NoError(t, err)
	var persisted bool
	for _, i := range listed {
		if i.ID == c.Invite.ID {
			persisted = i.EmailSent
		}
	}
	require.True(t, persisted, "the sent time is stored")

	// With no instance relay the mailer is still asked (the team may have a channel), with a nil relay.
	empty := ""
	_, err = f.svc.UpdateInstance(f.asAda(), InstancePatch{SMTPHost: &empty})
	require.NoError(t, err)
	c = f.invite(in)
	require.Equal(t, DeliverySent, c.Delivery)
	require.Nil(t, f.mailer.sent[1].relay)

	// The mailer says there is no channel: not configured, nothing marked.
	f.mailer.err = ErrNoMailer
	c = f.invite(in)
	require.Equal(t, DeliveryNotConfigured, c.Delivery)
	require.False(t, c.Invite.EmailSent)

	// The SMTP server refuses: failed, with the reason, and the invite still exists.
	f.mailer.err = errors.New("535 authentication failed")
	c = f.invite(in)
	require.Equal(t, DeliveryFailed, c.Delivery)
	require.Equal(t, "535 authentication failed", c.EmailError)
	require.False(t, c.Invite.EmailSent)
	require.Equal(t, c.Link, "https://nebula.example.com/invite/"+c.Token, "the link is returned either way")
	require.Equal(t, 1, f.count(`SELECT count(*) FROM team_invites WHERE token_hash=$1`, hashInvite(c.Token)))

	// A link invite never touches the mailer.
	n := len(f.mailer.sent)
	require.Equal(t, DeliveryLinkOnly, f.invite(InviteInput{Role: auth.RoleViewer}).Delivery)
	require.Len(t, f.mailer.sent, n)
}

func TestListAndRevokeInvites(t *testing.T) {
	f := newTf(t)
	bob := f.member("bob@example.com", auth.RoleAdmin)
	cy := f.member("cy@example.com", auth.RoleDeveloper)
	dev := f.invite(InviteInput{Role: auth.RoleDeveloper, MaxUses: 2})
	owner := f.invite(InviteInput{Role: auth.RoleOwner})
	used := f.invite(InviteInput{Role: auth.RoleViewer})
	old := f.invite(InviteInput{Role: auth.RoleViewer})
	f.exec(`UPDATE team_invites SET use_count = max_uses WHERE id=$1`, used.Invite.ID)
	f.expire(old.Invite.ID)

	list, err := f.svc.Invites(f.as(bob, f.team), f.team)
	require.NoError(t, err)
	require.Len(t, list, 4)
	by := map[uuid.UUID]TeamInvite{}
	for _, i := range list {
		by[i.ID] = i
	}
	require.False(t, by[dev.Invite.ID].Expired || by[dev.Invite.ID].Exhausted)
	require.True(t, by[used.Invite.ID].Exhausted)
	require.True(t, by[old.Invite.ID].Expired)
	require.Equal(t, "Ada", by[dev.Invite.ID].CreatedBy, "who created it")
	require.Equal(t, 2, by[dev.Invite.ID].MaxUses)

	_, err = f.svc.Invites(f.as(cy, f.team), f.team)
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.Invites(f.as(f.user("eve@example.com", ""), f.team), f.team)
	requireAPI(t, err, 404, "not_found")
	_, err = f.svc.Invites(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeProjectsRead), f.team)
	requireAPI(t, err, 403, "forbidden")

	// Revoking: admins revoke what they could create, not owner invites.
	requireAPI(t, f.svc.RevokeInvite(f.as(bob, f.team), f.team, owner.Invite.ID), 403, "forbidden")
	requireAPI(t, f.svc.RevokeInvite(f.as(cy, f.team), f.team, dev.Invite.ID), 403, "forbidden")
	requireAPI(t, f.svc.RevokeInvite(f.as(bob, f.team), f.team, uuid.New()), 404, "not_found")
	requireAPI(t, f.svc.RevokeInvite(f.token(bob, f.team, auth.RoleAdmin, policy.ScopeProjectsRead), f.team, dev.Invite.ID), 403, "forbidden")
	require.NoError(t, f.svc.RevokeInvite(f.as(bob, f.team), f.team, dev.Invite.ID))
	require.NoError(t, f.svc.RevokeInvite(f.asAda(), f.team, owner.Invite.ID))
	require.Equal(t, 2, f.audits("invite.revoke"))
	_, err = f.svc.PreviewInvite(context.Background(), dev.Token)
	requireAPI(t, err, 404, "invite_invalid")
	list, err = f.svc.Invites(f.asAda(), f.team)
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Revoking twice is a 404, and an invite of another team cannot be reached.
	requireAPI(t, f.svc.RevokeInvite(f.asAda(), f.team, dev.Invite.ID), 404, "not_found")
	elsewhere, err := f.svc.Create(f.asAda(), "Elsewhere")
	require.NoError(t, err)
	requireAPI(t, f.svc.RevokeInvite(f.asAda(), elsewhere.ID, used.Invite.ID), 404, "not_found")
}

func TestPreviewInvite(t *testing.T) {
	f := newTf(t)
	c := f.invite(InviteInput{Email: "new@example.com", Role: auth.RoleViewer})
	ctx := context.Background()

	p, err := f.svc.PreviewInvite(ctx, c.Token)
	require.NoError(t, err)
	require.Equal(t, "Home", p.TeamName)
	require.Equal(t, "home", p.TeamSlug)
	require.Equal(t, auth.RoleViewer, p.Role)
	require.Equal(t, "new@example.com", *p.Email)
	require.Equal(t, "Ada", p.InvitedBy, "the inviter's name, never their email")
	require.WithinDuration(t, c.Invite.ExpiresAt, p.ExpiresAt, time.Second)

	// A creator who left no row behind still previews.
	link := f.invite(InviteInput{Role: auth.RoleViewer})
	f.exec(`UPDATE team_invites SET created_by=NULL WHERE id=$1`, link.Invite.ID)
	p, err = f.svc.PreviewInvite(ctx, link.Token)
	require.NoError(t, err)
	require.Empty(t, p.InvitedBy)
	require.Nil(t, p.Email)

	for _, token := range map[string]string{
		"wrong prefix": "nct_" + strings.TrimPrefix(c.Token, InvitePrefix),
		"empty":        "",
		"unknown":      InvitePrefix + strings.Repeat("a", 39),
		"too long":     InvitePrefix + strings.Repeat("a", 200),
	} {
		_, err = f.svc.PreviewInvite(ctx, token)
		requireAPI(t, err, 404, "invite_invalid")
	}

	f.exec(`UPDATE team_invites SET use_count = max_uses WHERE id=$1`, c.Invite.ID)
	_, err = f.svc.PreviewInvite(ctx, c.Token)
	requireAPI(t, err, 410, "invite_used")
	f.expire(c.Invite.ID)
	_, err = f.svc.PreviewInvite(ctx, c.Token)
	requireAPI(t, err, 410, "invite_expired")
	f.exec(`UPDATE team_invites SET revoked_at = now() WHERE id=$1`, c.Invite.ID)
	_, err = f.svc.PreviewInvite(ctx, c.Token)
	requireAPI(t, err, 404, "invite_invalid")
}

func TestAcceptInviteJoinsTheSignedInUser(t *testing.T) {
	f := newTf(t)
	p1 := f.project("One")
	_, err := f.svc.Create(f.asAda(), "Second")
	require.NoError(t, err)
	eve := f.user("eve@example.com", "")
	c := f.invite(InviteInput{Role: auth.RoleDeveloper, ProjectIDs: []uuid.UUID{p1}})

	res, err := f.svc.Accept(f.as(eve, uuid.Nil), c.Token)
	require.NoError(t, err)
	require.Equal(t, f.team, res.Team.ID)
	require.Equal(t, auth.RoleDeveloper, res.Team.Role)
	require.True(t, res.Team.Current, "the joined team becomes the current one")
	require.Equal(t, []uuid.UUID{p1}, res.Team.ProjectIDs)
	require.False(t, res.CreatedAccount)
	require.Equal(t, eve, res.userID)
	require.Equal(t, auth.RoleDeveloper, f.roleOf(eve))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM team_invite_uses WHERE invite_id=$1 AND user_id=$2`, c.Invite.ID, eve))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action='member.join' AND workspace_id=$1 AND metadata->>'invite'=$2`, f.team, c.Invite.Prefix))

	// A single-use invite cannot be used again, by anyone.
	_, err = f.svc.Accept(f.as(f.user("zed@example.com", ""), uuid.Nil), c.Token)
	requireAPI(t, err, 410, "invite_used")
	// Nor by someone who already joined (told first: they are a member).
	_, err = f.svc.Accept(f.as(eve, uuid.Nil), c.Token)
	requireAPI(t, err, 409, "already_member")

	// Unknown tokens, tokens (API) as callers and anonymous callers.
	_, err = f.svc.Accept(f.as(eve, uuid.Nil), InvitePrefix+"nope")
	requireAPI(t, err, 404, "invite_invalid")
	_, err = f.svc.Accept(f.token(eve, f.team, auth.RoleDeveloper, policy.ScopeAdmin), c.Token)
	requireAPI(t, err, 403, "forbidden")
	_, err = f.svc.Accept(context.Background(), c.Token)
	requireAPI(t, err, 401, "unauthorized")
	// A signed-in session whose user no longer exists is signed out.
	ghost := auth.WithPrincipal(context.Background(), &auth.Principal{Type: auth.TypeUser, UserID: uuid.New()})
	_, err = f.svc.Accept(ghost, c.Token)
	requireAPI(t, err, 401, "unauthorized")
}

func TestAcceptInviteForOwnersAndAdminsDropsProjectRestrictions(t *testing.T) {
	f := newTf(t)
	p1 := f.project("One")
	c := f.invite(InviteInput{Role: auth.RoleAdmin, ProjectIDs: []uuid.UUID{p1}})
	eve := f.user("eve@example.com", "")
	res, err := f.svc.Accept(f.as(eve, uuid.Nil), c.Token)
	require.NoError(t, err)
	require.Empty(t, res.Team.ProjectIDs)
	require.Equal(t, auth.RoleAdmin, res.Team.Role)
}

func TestAcceptInviteBoundToAnEmail(t *testing.T) {
	f := newTf(t)
	c := f.invite(InviteInput{Email: "eve@example.com", Role: auth.RoleViewer})
	mallory := f.user("mallory@example.com", "")
	_, err := f.svc.Accept(f.as(mallory, uuid.Nil), c.Token)
	requireAPI(t, err, 403, "invite_email_mismatch")
	require.False(t, f.isMember(mallory, f.team))
	require.Zero(t, f.count(`SELECT use_count FROM team_invites WHERE id=$1`, c.Invite.ID), "a mismatch does not burn the invite")

	eve := f.user("Eve@Example.com", "")
	_, err = f.svc.Accept(f.as(eve, uuid.Nil), c.Token)
	require.NoError(t, err, "the address is compared without regard to case")
	require.True(t, f.isMember(eve, f.team))
}

func TestMultiUseInviteAndExpiryAndRevocationAtAccept(t *testing.T) {
	f := newTf(t)
	c := f.invite(InviteInput{Role: auth.RoleViewer, MaxUses: 2})
	a, b, d := f.user("a@example.com", ""), f.user("b@example.com", ""), f.user("d@example.com", "")
	_, err := f.svc.Accept(f.as(a, uuid.Nil), c.Token)
	require.NoError(t, err)
	_, err = f.svc.Accept(f.as(b, uuid.Nil), c.Token)
	require.NoError(t, err)
	_, err = f.svc.Accept(f.as(d, uuid.Nil), c.Token)
	requireAPI(t, err, 410, "invite_used")
	require.Equal(t, 2, f.count(`SELECT use_count FROM team_invites WHERE id=$1`, c.Invite.ID))
	require.False(t, f.isMember(d, f.team))

	exp := f.invite(InviteInput{Role: auth.RoleViewer})
	f.expire(exp.Invite.ID)
	_, err = f.svc.Accept(f.as(d, uuid.Nil), exp.Token)
	requireAPI(t, err, 410, "invite_expired")

	rev := f.invite(InviteInput{Role: auth.RoleViewer})
	require.NoError(t, f.svc.RevokeInvite(f.asAda(), f.team, rev.Invite.ID))
	_, err = f.svc.Accept(f.as(d, uuid.Nil), rev.Token)
	requireAPI(t, err, 404, "invite_invalid")
}

func TestAcceptWithSignupCreatesTheAccountAndJoins(t *testing.T) {
	f := newTf(t)
	c := f.invite(InviteInput{Role: auth.RoleDeveloper})

	res, err := f.svc.AcceptWithSignup(context.Background(), c.Token, SignupInput{Email: "  New@Example.com ", Name: "  Nina  ", Password: covPassword})
	require.NoError(t, err)
	require.True(t, res.CreatedAccount)
	require.Equal(t, f.team, res.Team.ID)
	require.Equal(t, auth.RoleDeveloper, res.Team.Role)
	u, err := f.q.GetUserByEmail(context.Background(), "new@example.com")
	require.NoError(t, err)
	require.Equal(t, "Nina", u.Name)
	require.Equal(t, u.ID, res.userID)
	require.NotEmpty(t, u.PasswordHash)
	require.NotContains(t, u.PasswordHash, covPassword)
	require.True(t, f.isMember(u.ID, f.team))
	// The new user can sign in with the password they chose.
	signedIn, err := f.au.Login(context.Background(), "new@example.com", covPassword)
	require.NoError(t, err)
	require.Equal(t, u.ID, signedIn.ID)
	// The single use is spent.
	_, err = f.svc.AcceptWithSignup(context.Background(), c.Token, SignupInput{Email: "other@example.com", Name: "O", Password: covPassword})
	requireAPI(t, err, 410, "invite_used")
	require.Zero(t, f.count(`SELECT count(*) FROM users WHERE email='other@example.com'`), "no account is left behind by a refused sign-up")
}

func TestAcceptWithSignupRefusals(t *testing.T) {
	f := newTf(t)
	ctx := context.Background()
	link := f.invite(InviteInput{Role: auth.RoleViewer, MaxUses: 10})
	bound := f.invite(InviteInput{Email: "eve@example.com", Role: auth.RoleViewer})
	users := func() int { return f.count(`SELECT count(*) FROM users`) }
	before := users()

	for name, tc := range map[string]struct {
		token string
		in    SignupInput
		code  string
	}{
		"no name":         {link.Token, SignupInput{Email: "a@example.com", Name: " ", Password: covPassword}, "validation_failed"},
		"name too long":   {link.Token, SignupInput{Email: "a@example.com", Name: strings.Repeat("n", 101), Password: covPassword}, "validation_failed"},
		"weak password":   {link.Token, SignupInput{Email: "a@example.com", Name: "A", Password: "short"}, "weak_password"},
		"bad email":       {link.Token, SignupInput{Email: "nope", Name: "A", Password: covPassword}, "invalid_email"},
		"no email":        {link.Token, SignupInput{Name: "A", Password: covPassword}, "invalid_email"},
		"display email":   {link.Token, SignupInput{Email: "A <a@example.com>", Name: "A", Password: covPassword}, "invalid_email"},
		"existing":        {link.Token, SignupInput{Email: "ada@example.com", Name: "A", Password: covPassword}, "account_exists"},
		"existing (case)": {link.Token, SignupInput{Email: "ADA@example.com", Name: "A", Password: covPassword}, "account_exists"},
		"wrong address":   {bound.Token, SignupInput{Email: "mallory@example.com", Name: "M", Password: covPassword}, "invite_email_mismatch"},
		"unknown invite":  {InvitePrefix + "nope", SignupInput{Email: "a@example.com", Name: "A", Password: covPassword}, "invite_invalid"},
	} {
		_, err := f.svc.AcceptWithSignup(ctx, tc.token, tc.in)
		require.Error(t, err, name)
		ae, ok := apierr.As(err)
		require.True(t, ok, name)
		require.Equal(t, tc.code, ae.Code, name)
	}
	require.Equal(t, before, users(), "refused sign-ups create no account")
	require.Zero(t, f.count(`SELECT use_count FROM team_invites WHERE id=$1`, link.Invite.ID))

	// An invite bound to an email supplies it when the form leaves it out.
	res, err := f.svc.AcceptWithSignup(ctx, bound.Token, SignupInput{Name: "Eve", Password: covPassword})
	require.NoError(t, err)
	require.True(t, res.CreatedAccount)
	_, err = f.q.GetUserByEmail(ctx, "eve@example.com")
	require.NoError(t, err)

	// Expired and revoked invites create nothing either.
	exp := f.invite(InviteInput{Role: auth.RoleViewer})
	f.expire(exp.Invite.ID)
	_, err = f.svc.AcceptWithSignup(ctx, exp.Token, SignupInput{Email: "late@example.com", Name: "L", Password: covPassword})
	requireAPI(t, err, 410, "invite_expired")
	require.Zero(t, f.count(`SELECT count(*) FROM users WHERE email='late@example.com'`))
}

func TestSignupHooks(t *testing.T) {
	f := newTf(t)
	h := Hooks{S: f.svc}
	ctx := context.Background()
	bound := f.invite(InviteInput{Email: "eve@example.com", Role: auth.RoleDeveloper})
	link := f.invite(InviteInput{Role: auth.RoleViewer})

	// CheckInvite validates without consuming.
	require.NoError(t, h.CheckInvite(ctx, bound.Token, "Eve@example.com"))
	requireAPI(t, h.CheckInvite(ctx, bound.Token, "mallory@example.com"), 403, "invite_email_mismatch")
	require.NoError(t, h.CheckInvite(ctx, link.Token, "anyone@example.com"))
	requireAPI(t, h.CheckInvite(ctx, InvitePrefix+"nope", "a@example.com"), 404, "invite_invalid")
	f.expire(link.Invite.ID)
	requireAPI(t, h.CheckInvite(ctx, link.Token, "anyone@example.com"), 410, "invite_expired")
	require.Zero(t, f.count(`SELECT COALESCE(sum(use_count),0) FROM team_invites`))

	// AcceptInvite consumes it and joins the user, in the caller's transaction.
	eve := f.user("eve@example.com", "")
	require.NoError(t, f.svc.auth.InTx(ctx, func(tx pgx.Tx) error {
		return h.AcceptInvite(ctx, tx, bound.Token, eve, "eve@example.com")
	}))
	require.Equal(t, auth.RoleDeveloper, f.roleOf(eve))
	require.Equal(t, 1, f.count(`SELECT use_count FROM team_invites WHERE id=$1`, bound.Invite.ID))
	// When the transaction rolls back, the use and the membership go with it.
	zed := f.user("zed@example.com", "")
	c2 := f.invite(InviteInput{Role: auth.RoleViewer})
	boom := errors.New("later step failed")
	err := f.svc.auth.InTx(ctx, func(tx pgx.Tx) error {
		if err := h.AcceptInvite(ctx, tx, c2.Token, zed, "zed@example.com"); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.False(t, f.isMember(zed, f.team))
	require.Zero(t, f.count(`SELECT use_count FROM team_invites WHERE id=$1`, c2.Invite.ID))
}

func TestOpenSignupGivesTheNewUserATeamWhenAllowed(t *testing.T) {
	f := newTf(t)
	h := Hooks{S: f.svc}
	ctx := context.Background()
	named := f.user("nina@example.com", "")
	require.NoError(t, f.svc.auth.InTx(ctx, func(tx pgx.Tx) error { return h.OpenSignup(ctx, tx, named, "  Nina ") }))
	teams, err := f.q.ListMyTeams(ctx, named)
	require.NoError(t, err)
	require.Len(t, teams, 1)
	require.Equal(t, "Nina's team", teams[0].Name)
	require.Equal(t, auth.RoleOwner, teams[0].Role)

	nameless := f.user("anon@example.com", "")
	require.NoError(t, f.svc.auth.InTx(ctx, func(tx pgx.Tx) error { return h.OpenSignup(ctx, tx, nameless, "   ") }))
	teams, err = f.q.ListMyTeams(ctx, nameless)
	require.NoError(t, err)
	require.Len(t, teams, 1)
	require.Equal(t, "My's team", teams[0].Name)

	// Team creation disabled: no team until someone invites them.
	no := false
	_, err = f.svc.UpdateInstance(f.asAda(), InstancePatch{AllowTeamCreation: &no})
	require.NoError(t, err)
	locked := f.user("locked@example.com", "")
	require.NoError(t, f.svc.auth.InTx(ctx, func(tx pgx.Tx) error { return h.OpenSignup(ctx, tx, locked, "Locked") }))
	teams, err = f.q.ListMyTeams(ctx, locked)
	require.NoError(t, err)
	require.Empty(t, teams)
}
