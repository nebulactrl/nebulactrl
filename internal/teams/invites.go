package teams

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// InvitePrefix starts every invite token.
const InvitePrefix = "nci_"

// Invite limits.
const (
	DefaultInviteTTL = 7 * 24 * time.Hour
	MaxInviteTTL     = 90 * 24 * time.Hour
	MaxInviteUses    = 1000
)

var inviteEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// TeamInvite is a pending invite of a team. The token itself is shown once, at
// creation.
type TeamInvite struct {
	ID         uuid.UUID   `json:"id"`
	Email      *string     `json:"email,omitempty" doc:"Absent for link invites"`
	Role       string      `json:"role" enum:"owner,admin,developer,viewer"`
	ProjectIDs []uuid.UUID `json:"projectIds,omitempty"`
	MaxUses    int         `json:"maxUses" doc:"1 = single use"`
	UseCount   int         `json:"useCount"`
	Prefix     string      `json:"prefix" doc:"First characters of the token, to recognise the invite"`
	ExpiresAt  time.Time   `json:"expiresAt"`
	EmailSent  bool        `json:"emailSent"`
	CreatedBy  string      `json:"createdBy,omitempty"`
	CreatedAt  time.Time   `json:"createdAt"`
	Expired    bool        `json:"expired"`
	Exhausted  bool        `json:"exhausted"`
}

func (s *Service) inviteView(i db.TeamInvite, createdBy string) TeamInvite {
	return TeamInvite{
		ID: i.ID, Email: i.Email, Role: i.Role, ProjectIDs: i.ProjectIds, MaxUses: int(i.MaxUses), UseCount: int(i.UseCount),
		Prefix: i.Prefix, ExpiresAt: i.ExpiresAt, EmailSent: i.EmailSentAt != nil, CreatedBy: createdBy, CreatedAt: i.CreatedAt,
		Expired: !i.ExpiresAt.After(s.now()), Exhausted: i.UseCount >= i.MaxUses,
	}
}

// InviteInput describes a new invite.
type InviteInput struct {
	Email      string // empty = link invite
	Role       string
	ProjectIDs []uuid.UUID // nil = every project (developer/viewer only)
	MaxUses    int         // 0 = 1 (email invites are always single use)
	TTL        time.Duration
}

// Delivery tells what happened to the email of an invite.
const (
	DeliverySent          = "sent"           // email handed to the SMTP server
	DeliveryLinkOnly      = "link_only"      // no email was requested
	DeliveryNotConfigured = "not_configured" // no email channel or instance SMTP: share the link
	DeliveryFailed        = "failed"         // SMTP error: share the link, see EmailError
)

// CreatedInvite is the result of creating an invite: the invite, its token and
// link (shown once) and what happened to the email.
type CreatedInvite struct {
	Invite     TeamInvite `json:"invite"`
	Token      string     `json:"token" doc:"The raw invite token. Shown once."`
	Link       string     `json:"link" doc:"Where the invitee accepts: an absolute URL when NEBULACTRL_PUBLIC_URL is set, otherwise a path"`
	Delivery   string     `json:"delivery" enum:"sent,link_only,not_configured,failed"`
	EmailError string     `json:"emailError,omitempty"`
}

func hashInvite(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func newInviteToken() (raw string, hash []byte, prefix string, err error) {
	var b [24]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", nil, "", err
	}
	body := strings.ToLower(inviteEnc.EncodeToString(b[:]))
	raw = InvitePrefix + body
	return raw, hashInvite(raw), body[:8], nil
}

func normalizeInviteEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return "", nil
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || len(e) > 254 {
		return "", apierr.Unprocessable("invalid_email", "enter a valid email address")
	}
	return e, nil
}

func (s *Service) link(token string) string {
	path := "/invite/" + token
	if s.publicURL == "" {
		return path
	}
	return s.publicURL + path
}

// CreateInvite issues an invite to the team (admins and owners; only owners
// may invite owners). With an email it also mails the link: through the team's
// email notification channel if it has one, else the instance SMTP relay, else
// the caller gets the link only (Delivery says which).
func (s *Service) CreateInvite(ctx context.Context, teamID uuid.UUID, in InviteInput) (CreatedInvite, error) {
	p, m, err := s.caller(ctx, teamID)
	if err != nil {
		return CreatedInvite{}, err
	}
	if err := needRole(p, m, adminRoles...); err != nil {
		return CreatedInvite{}, err
	}
	if err := validRole(in.Role); err != nil {
		return CreatedInvite{}, err
	}
	if !canManage(m.Role, in.Role) {
		return CreatedInvite{}, apierr.Forbidden()
	}
	email, err := normalizeInviteEmail(in.Email)
	if err != nil {
		return CreatedInvite{}, err
	}
	maxUses := in.MaxUses
	if maxUses <= 0 {
		maxUses = 1
	}
	if maxUses > MaxInviteUses {
		return CreatedInvite{}, apierr.Unprocessable("invalid_max_uses", fmt.Sprintf("max uses must be 1-%d", MaxInviteUses))
	}
	if email != "" {
		maxUses = 1
	}
	ttl := in.TTL
	if ttl <= 0 {
		ttl = DefaultInviteTTL
	}
	if ttl > MaxInviteTTL {
		return CreatedInvite{}, apierr.Unprocessable("invalid_expiry", "an invite can be valid for at most 90 days")
	}
	raw, hash, prefix, err := newInviteToken()
	if err != nil {
		return CreatedInvite{}, fmt.Errorf("generate invite token: %w", err)
	}
	var (
		row     db.TeamInvite
		teamRow db.Workspace
	)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		projects, err := s.normalizeProjects(ctx, q, teamID, in.Role, in.ProjectIDs)
		if err != nil {
			return err
		}
		if email != "" {
			// Inviting someone who already belongs to the team is a mistake worth
			// surfacing instead of mailing a link that cannot be accepted.
			if u, err := q.GetUserByEmail(ctx, email); err == nil {
				if _, err := q.GetMember(ctx, db.GetMemberParams{WorkspaceID: teamID, UserID: u.ID}); err == nil {
					return apierr.Conflict("already_member", "that person is already a member of this team")
				}
			}
		}
		var em *string
		if email != "" {
			em = &email
		}
		creator := p.UserID
		row, err = q.InsertInvite(ctx, db.InsertInviteParams{
			WorkspaceID: teamID, TokenHash: hash, Prefix: prefix, Email: em, Role: in.Role, ProjectIds: projects,
			MaxUses: int32(maxUses), ExpiresAt: s.now().Add(ttl), CreatedBy: &creator,
		})
		if err != nil {
			return err
		}
		teamRow, err = q.GetWorkspace(ctx, teamID)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "invite.create", ResourceType: "invite", ResourceID: row.ID.String(), WorkspaceID: teamID,
			Metadata: map[string]any{"email": email, "role": in.Role, "maxUses": maxUses, "prefix": prefix, "restricted": projects != nil},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return CreatedInvite{}, err
		}
		return CreatedInvite{}, fmt.Errorf("create invite: %w", err)
	}
	out := CreatedInvite{Token: raw, Link: s.link(raw), Delivery: DeliveryLinkOnly}
	if email != "" {
		out.Delivery, out.EmailError = s.mailInvite(ctx, teamRow, p, row, email, out.Link)
		if out.Delivery == DeliverySent {
			now := s.now()
			row.EmailSentAt = &now
		}
	}
	out.Invite = s.inviteView(row, p.Label)
	s.publish("invite.created", teamID)
	return out, nil
}

func (s *Service) mailInvite(ctx context.Context, team db.Workspace, p *auth.Principal, inv db.TeamInvite, to, link string) (string, string) {
	if s.mailer == nil {
		return DeliveryNotConfigured, ""
	}
	if !strings.HasPrefix(link, "http") {
		return DeliveryNotConfigured, "NEBULACTRL_PUBLIC_URL is not set, so an emailed link would be relative: share the link yourself"
	}
	set, err := s.q.GetInstanceSettings(ctx)
	if err != nil {
		return DeliveryFailed, "could not read the instance settings"
	}
	relay := s.relayFrom(set)
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err = s.mailer.SendInvite(mctx, team.ID, relay, InviteMail{
		To: to, TeamName: team.Name, InviterName: p.Label, Role: inv.Role, Link: link, ExpiresAt: inv.ExpiresAt,
	})
	switch {
	case err == nil:
		if err := s.q.MarkInviteEmailSent(ctx, inv.ID); err != nil {
			s.log.WarnContext(ctx, "mark invite email sent", "err", err)
		}
		return DeliverySent, ""
	case errors.Is(err, ErrNoMailer):
		return DeliveryNotConfigured, ""
	default:
		s.log.WarnContext(ctx, "invite email failed", "team", team.ID, "err", err)
		return DeliveryFailed, err.Error()
	}
}

// Invites lists a team's live invites (admins and owners).
func (s *Service) Invites(ctx context.Context, teamID uuid.UUID) ([]TeamInvite, error) {
	p, m, err := s.caller(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if err := needRole(p, m, adminRoles...); err != nil {
		return nil, err
	}
	rows, err := s.q.ListInvites(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	out := make([]TeamInvite, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.inviteView(db.TeamInvite{
			ID: r.ID, WorkspaceID: r.WorkspaceID, TokenHash: r.TokenHash, Prefix: r.Prefix, Email: r.Email, Role: r.Role,
			ProjectIds: r.ProjectIds, MaxUses: r.MaxUses, UseCount: r.UseCount, ExpiresAt: r.ExpiresAt,
			EmailSentAt: r.EmailSentAt, CreatedBy: r.CreatedBy, RevokedAt: r.RevokedAt, CreatedAt: r.CreatedAt,
		}, r.CreatedByLabel))
	}
	return out, nil
}

// RevokeInvite deletes an invite; its link stops working at once.
func (s *Service) RevokeInvite(ctx context.Context, teamID, inviteID uuid.UUID) error {
	p, m, err := s.caller(ctx, teamID)
	if err != nil {
		return err
	}
	if err := needRole(p, m, adminRoles...); err != nil {
		return err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetInvite(ctx, db.GetInviteParams{ID: inviteID, WorkspaceID: teamID})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("invite")
		}
		if err != nil {
			return err
		}
		if !canManage(m.Role, cur.Role) {
			return apierr.Forbidden()
		}
		if _, err := q.DeleteInvite(ctx, db.DeleteInviteParams{ID: inviteID, WorkspaceID: teamID}); err != nil {
			return err
		}
		email := ""
		if cur.Email != nil {
			email = *cur.Email
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "invite.revoke", ResourceType: "invite", ResourceID: inviteID.String(), WorkspaceID: teamID,
			Metadata: map[string]any{"email": email, "role": cur.Role, "prefix": cur.Prefix},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("revoke invite: %w", err)
	}
	s.publish("invite.revoked", teamID)
	return nil
}

// ---- accepting ------------------------------------------------------------------

// InvitePreview is what an invite link shows before it is accepted.
type InvitePreview struct {
	TeamName  string    `json:"teamName"`
	TeamSlug  string    `json:"teamSlug"`
	Role      string    `json:"role" enum:"owner,admin,developer,viewer"`
	Email     *string   `json:"email,omitempty" doc:"Set when the invite is bound to one email address"`
	ExpiresAt time.Time `json:"expiresAt"`
	// InvitedBy is the display name of the person who created the invite (never
	// their email: a link invite's preview is readable by whoever holds the link).
	InvitedBy string `json:"invitedBy,omitempty" doc:"Display name of the person who created the invite"`
	// There is no "an account exists" flag: a preview must not tell whoever
	// holds a link which email addresses have an account. Accepting answers 409
	// account_exists instead.
}

var (
	errInviteInvalid = apierr.New(404, "invite_invalid", "this invite link is not valid")
	errInviteExpired = apierr.New(410, "invite_expired", "this invite has expired: ask for a new one")
	errInviteUsed    = apierr.New(410, "invite_used", "this invite has already been used")
)

// findInvite returns the invite behind a raw token, whether or not it is still
// usable (revoked and unknown tokens are 404).
func (s *Service) findInvite(ctx context.Context, q *db.Queries, raw string) (db.GetInviteByHashRow, error) {
	if !strings.HasPrefix(raw, InvitePrefix) || len(raw) > 128 {
		return db.GetInviteByHashRow{}, errInviteInvalid
	}
	inv, err := q.GetInviteByHash(ctx, hashInvite(raw))
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, errInviteInvalid
	}
	if err != nil {
		return inv, fmt.Errorf("get invite: %w", err)
	}
	if inv.RevokedAt != nil {
		return inv, errInviteInvalid
	}
	return inv, nil
}

// live fails with 410 when the invite has expired or been used up.
func (s *Service) live(inv db.GetInviteByHashRow) error {
	if !inv.ExpiresAt.After(s.now()) {
		return errInviteExpired
	}
	if inv.UseCount >= inv.MaxUses {
		return errInviteUsed
	}
	return nil
}

// lookupInvite finds a live invite by raw token without consuming it.
func (s *Service) lookupInvite(ctx context.Context, q *db.Queries, raw string) (db.GetInviteByHashRow, error) {
	inv, err := s.findInvite(ctx, q, raw)
	if err != nil {
		return inv, err
	}
	return inv, s.live(inv)
}

// PreviewInvite describes an invite for the public accept page.
func (s *Service) PreviewInvite(ctx context.Context, raw string) (InvitePreview, error) {
	inv, err := s.lookupInvite(ctx, s.q, raw)
	if err != nil {
		return InvitePreview{}, err
	}
	out := InvitePreview{TeamName: inv.TeamName, TeamSlug: inv.TeamSlug, Role: inv.Role, Email: inv.Email, ExpiresAt: inv.ExpiresAt}
	if inv.CreatedBy != nil {
		inviter, err := s.q.GetUser(ctx, *inv.CreatedBy)
		switch {
		case err == nil:
			out.InvitedBy = inviter.Name
		case !errors.Is(err, pgx.ErrNoRows):
			return InvitePreview{}, fmt.Errorf("get inviter: %w", err)
		}
	}
	return out, nil
}

func emailMatches(inv db.GetInviteByHashRow, email string) bool {
	return inv.Email == nil || strings.EqualFold(*inv.Email, strings.TrimSpace(email))
}

// joinTx consumes one use of the invite and adds userID to its team, inside
// tx. It returns the team joined.
func (s *Service) joinTx(ctx context.Context, tx pgx.Tx, raw string, userID uuid.UUID, email string) (db.GetInviteByHashRow, error) {
	q := db.New(tx)
	inv, err := s.findInvite(ctx, q, raw)
	if err != nil {
		return inv, err
	}
	if !emailMatches(inv, email) {
		return inv, apierr.New(403, "invite_email_mismatch", "this invite was sent to a different email address")
	}
	if _, err := q.GetMembership(ctx, db.GetMembershipParams{UserID: userID, WorkspaceID: inv.WorkspaceID}); err == nil {
		return inv, apierr.Conflict("already_member", "you are already a member of this team")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return inv, err
	}
	if err := s.live(inv); err != nil {
		return inv, err
	}
	// Atomic: expired, revoked and used-up invites yield no row, so a single-use
	// link cannot be accepted twice, even concurrently.
	if _, err := q.ConsumeInvite(ctx, inv.ID); errors.Is(err, pgx.ErrNoRows) {
		return inv, errInviteUsed
	} else if err != nil {
		return inv, err
	}
	if _, err := q.InsertInviteUse(ctx, db.InsertInviteUseParams{InviteID: inv.ID, UserID: userID}); err != nil {
		return inv, err
	}
	projects := inv.ProjectIds
	if inv.Role == auth.RoleOwner || inv.Role == auth.RoleAdmin {
		projects = nil
	}
	if _, err := q.InsertMembership(ctx, db.InsertMembershipParams{UserID: userID, WorkspaceID: inv.WorkspaceID, Role: inv.Role, ProjectIds: projects}); err != nil {
		return inv, err
	}
	actor := auth.WithPrincipal(ctx, &auth.Principal{Type: auth.TypeUser, UserID: userID, Label: strings.ToLower(strings.TrimSpace(email)), WorkspaceID: inv.WorkspaceID})
	if err := audit.Record(actor, tx, audit.Entry{
		Action: "member.join", ResourceType: "member", ResourceID: userID.String(), WorkspaceID: inv.WorkspaceID,
		Metadata: map[string]any{"email": email, "role": inv.Role, "invite": inv.Prefix},
	}); err != nil {
		return inv, err
	}
	return inv, nil
}

// AcceptedInvite is the result of accepting an invite.
type AcceptedInvite struct {
	Team           Team `json:"team"`
	CreatedAccount bool `json:"createdAccount" doc:"A new account was created through the invite and signed in"`
	// userID is the account that joined (for starting the session).
	userID uuid.UUID
}

// SignupInput creates an account while accepting an invite.
type SignupInput struct {
	Email    string
	Name     string
	Password string
}

// Accept joins the signed-in caller to the invite's team.
func (s *Service) Accept(ctx context.Context, raw string) (AcceptedInvite, error) {
	p, err := sessionUser(ctx)
	if err != nil {
		return AcceptedInvite{}, err
	}
	u, err := s.auth.UserByID(ctx, p.UserID)
	if err != nil {
		return AcceptedInvite{}, apierr.Unauthorized()
	}
	var inv db.GetInviteByHashRow
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		inv, err = s.joinTx(ctx, tx, raw, u.ID, u.Email)
		return err
	})
	if err != nil {
		return AcceptedInvite{}, s.wrap("accept invite", err)
	}
	s.publish("member.joined", inv.WorkspaceID)
	team, err := s.team(ctx, u.ID, inv.WorkspaceID, inv.WorkspaceID)
	return AcceptedInvite{Team: team, userID: u.ID}, err
}

// AcceptWithSignup creates an account from the invite and joins the team in
// one transaction. An email-bound invite fixes the email; an existing account
// must sign in instead (409 account_exists).
func (s *Service) AcceptWithSignup(ctx context.Context, raw string, in SignupInput) (AcceptedInvite, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 100 {
		return AcceptedInvite{}, apierr.Unprocessable("validation_failed", "name must be 1-100 characters")
	}
	hash, err := s.auth.HashPassword(in.Password)
	if err != nil {
		return AcceptedInvite{}, err
	}
	var (
		inv  db.GetInviteByHashRow
		user db.User
	)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		pre, err := s.lookupInvite(ctx, q, raw)
		if err != nil {
			return err
		}
		if pre.Email != nil {
			if email == "" {
				email = *pre.Email
			}
			if !strings.EqualFold(*pre.Email, email) {
				return apierr.New(403, "invite_email_mismatch", "this invite was sent to a different email address")
			}
		}
		addr, perr := mail.ParseAddress(email)
		if perr != nil || addr.Address != email || len(email) > 254 {
			return apierr.Unprocessable("invalid_email", "enter a valid email address")
		}
		if _, err := q.GetUserByEmail(ctx, email); err == nil {
			return apierr.Conflict("account_exists", "an account with that email already exists: sign in to accept the invite")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		user, err = q.InsertUser(ctx, db.InsertUserParams{Email: email, Name: name, PasswordHash: hash})
		if err != nil {
			return err
		}
		inv, err = s.joinTx(ctx, tx, raw, user.ID, email)
		return err
	})
	if uniqueViolation(err) {
		return AcceptedInvite{}, apierr.Conflict("account_exists", "an account with that email already exists: sign in to accept the invite")
	}
	if err != nil {
		return AcceptedInvite{}, s.wrap("accept invite with sign-up", err)
	}
	s.publish("member.joined", inv.WorkspaceID)
	team, err := s.team(ctx, user.ID, inv.WorkspaceID, inv.WorkspaceID)
	return AcceptedInvite{Team: team, CreatedAccount: true, userID: user.ID}, err
}

func (s *Service) wrap(what string, err error) error {
	if _, ok := apierr.As(err); ok {
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ---- hooks for the login methods (auth.SignupHooks) ------------------------------

// Hooks adapts the Service to auth.SignupHooks, used when someone signs up
// through an identity provider.
type Hooks struct{ S *Service }

var _ auth.SignupHooks = Hooks{}

// CheckInvite validates an invite for an email without consuming it.
func (h Hooks) CheckInvite(ctx context.Context, token, email string) error {
	inv, err := h.S.lookupInvite(ctx, h.S.q, token)
	if err != nil {
		return err
	}
	if !emailMatches(inv, email) {
		return apierr.New(403, "invite_email_mismatch", "this invite was sent to a different email address")
	}
	return nil
}

// AcceptInvite consumes the invite and joins userID, inside tx.
func (h Hooks) AcceptInvite(ctx context.Context, tx pgx.Tx, token string, userID uuid.UUID, email string) error {
	_, err := h.S.joinTx(ctx, tx, token, userID, email)
	return err
}

// OpenSignup gives a user who signed up without an invite their own team,
// provided the instance lets people create teams. Otherwise they have no team
// until someone invites them.
func (h Hooks) OpenSignup(ctx context.Context, tx pgx.Tx, userID uuid.UUID, name string) error {
	q := db.New(tx)
	set, err := q.GetInstanceSettings(ctx)
	if err != nil {
		return err
	}
	if !set.AllowTeamCreation {
		return nil
	}
	label := strings.TrimSpace(name)
	if label == "" {
		label = "My"
	}
	ws, err := auth.NewTeam(ctx, q, label+"'s team", &userID)
	if err != nil {
		return err
	}
	if _, err := q.InsertMembership(ctx, db.InsertMembershipParams{UserID: userID, WorkspaceID: ws.ID, Role: auth.RoleOwner}); err != nil {
		return err
	}
	return nil
}
