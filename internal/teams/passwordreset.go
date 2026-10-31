package teams

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// PasswordResetLink is a password reset link an admin generated for a member.
// The token is shown once and is not stored; the link works once for 30
// minutes. It works without any SMTP setup: the admin hands it over.
type PasswordResetLink struct {
	Link      string    `json:"link" doc:"Where the member sets a new password: an absolute URL when NEBULACTRL_PUBLIC_URL is set, otherwise a path"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// CreatePasswordResetLink issues a reset link for a member of the team. Owners
// and admins only, signed-in users only (a token must not be able to take over
// an account); admins cannot reset an owner. A member who belongs to another
// team, and the instance admin, can be reset only by the instance admin
// (409 member_of_other_teams, 403 instance_admin_account). Members who sign in
// through an identity provider and have no password get 409 no_password. The
// link is audited; using it signs the member out everywhere.
func (s *Service) CreatePasswordResetLink(ctx context.Context, teamID, userID uuid.UUID) (PasswordResetLink, error) {
	p, err := sessionUser(ctx)
	if err != nil {
		return PasswordResetLink{}, err
	}
	_, m, err := s.caller(ctx, teamID)
	if err != nil {
		return PasswordResetLink{}, err
	}
	if err := needRole(p, m, adminRoles...); err != nil {
		return PasswordResetLink{}, err
	}
	if userID == p.UserID {
		return PasswordResetLink{}, apierr.Unprocessable("own_password", "change your own password in Account settings → Security")
	}
	var raw string
	var expires time.Time
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		cur, err := q.GetMember(ctx, db.GetMemberParams{WorkspaceID: teamID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("member")
		}
		if err != nil {
			return err
		}
		if !canManage(m.Role, cur.Role) {
			return apierr.Forbidden()
		}
		if err := s.checkResetTarget(ctx, q, p.UserID, teamID, userID); err != nil {
			return err
		}
		raw, expires, err = s.auth.CreateResetToken(ctx, tx, userID, p.UserID)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "member.password_reset_link", ResourceType: "member", ResourceID: userID.String(), WorkspaceID: teamID,
			Metadata: map[string]any{"email": cur.Email, "expiresAt": expires.UTC().Format(time.RFC3339)},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return PasswordResetLink{}, err
		}
		return PasswordResetLink{}, fmt.Errorf("create password reset link: %w", err)
	}
	link := s.auth.ResetLink(raw)
	if link == "" {
		link = "/reset-password?token=" + raw
	}
	return PasswordResetLink{Link: link, Token: raw, ExpiresAt: expires}, nil
}

// checkResetTarget decides whether the caller may issue a reset link for a
// member. The link lets whoever holds it sign in as that person, so a team admin
// may issue one only for someone whose whole account lives in this team: a
// person who belongs to any other team (or the instance admin) could be taken
// over together with everything they hold in those teams. Those links come from
// the instance admin, or from the operator on the host (nebulactrl-server
// reset-password).
func (s *Service) checkResetTarget(ctx context.Context, q *db.Queries, callerID, teamID, targetID uuid.UUID) error {
	callerAdmin, err := s.auth.IsInstanceAdmin(ctx, callerID)
	if err != nil {
		return err
	}
	if callerAdmin {
		return nil
	}
	targetAdmin, err := s.auth.IsInstanceAdmin(ctx, targetID)
	if err != nil {
		return err
	}
	if targetAdmin {
		return apierr.New(http.StatusForbidden, "instance_admin_account",
			"this is the instance admin's account; its password is reset on the server (nebulactrl-server reset-password)")
	}
	teamIDs, err := q.ListUserTeamIDs(ctx, targetID)
	if err != nil {
		return fmt.Errorf("list member teams: %w", err)
	}
	for _, id := range teamIDs {
		if id != teamID {
			return apierr.Conflict("member_of_other_teams",
				"this person belongs to other teams, so only the instance admin can issue a password reset link for them")
		}
	}
	return nil
}
