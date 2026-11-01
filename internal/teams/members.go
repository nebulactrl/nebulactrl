package teams

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// TeamMember is one member of a team.
type TeamMember struct {
	UserID     uuid.UUID   `json:"userId"`
	Name       string      `json:"name"`
	Email      string      `json:"email"`
	Role       string      `json:"role" enum:"owner,admin,developer,viewer"`
	ProjectIDs []uuid.UUID `json:"projectIds,omitempty" doc:"Absent = every project of the team"`
	JoinedAt   time.Time   `json:"joinedAt"`
	You        bool        `json:"you"`
}

func memberFrom(userID uuid.UUID, name, email, role string, projects []uuid.UUID, joined time.Time, me uuid.UUID) TeamMember {
	return TeamMember{UserID: userID, Name: name, Email: email, Role: role, ProjectIDs: projects, JoinedAt: joined, You: userID == me}
}

// Members lists a team's members. Every member may see the list.
func (s *Service) Members(ctx context.Context, teamID uuid.UUID) ([]TeamMember, error) {
	p, _, err := s.caller(ctx, teamID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListMembers(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	out := make([]TeamMember, 0, len(rows))
	for _, r := range rows {
		out = append(out, memberFrom(r.UserID, r.Name, r.Email, r.Role, r.ProjectIds, r.JoinedAt, p.UserID))
	}
	return out, nil
}

// MemberPatch is a partial member update.
type MemberPatch struct {
	Role *string
	// ProjectIDs replaces the project restriction when SetProjects is true; a
	// nil slice with SetProjects lifts the restriction.
	ProjectIDs  []uuid.UUID
	SetProjects bool
}

// canManage reports whether a caller of role may hand out or change `role`:
// only owners manage owners.
func canManage(callerRole, targetRole string) bool {
	if callerRole == auth.RoleOwner {
		return true
	}
	return callerRole == auth.RoleAdmin && targetRole != auth.RoleOwner
}

// normalizeProjects validates a project restriction against the team and
// drops it for roles that always see everything.
func (s *Service) normalizeProjects(ctx context.Context, q *db.Queries, teamID uuid.UUID, role string, ids []uuid.UUID) ([]uuid.UUID, error) {
	if role == auth.RoleOwner || role == auth.RoleAdmin {
		return nil, nil
	}
	if ids == nil {
		return nil, nil
	}
	uniq := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(uniq, id) {
			uniq = append(uniq, id)
		}
	}
	if len(uniq) > 0 {
		n, err := q.CountTeamProjects(ctx, db.CountTeamProjectsParams{WorkspaceID: teamID, Ids: uniq})
		if err != nil {
			return nil, fmt.Errorf("check projects: %w", err)
		}
		if int(n) != len(uniq) {
			return nil, apierr.Unprocessable("unknown_project", "one or more projects do not exist in this team")
		}
	}
	return uniq, nil
}

func validRole(role string) error {
	if !auth.ValidRole(role) {
		return apierr.Unprocessable("invalid_role", "role must be owner, admin, developer or viewer")
	}
	return nil
}

// UpdateMember changes a member's role and/or project restriction. Admins
// manage everyone but owners; owners manage everyone. The last owner can never
// be demoted.
func (s *Service) UpdateMember(ctx context.Context, teamID, userID uuid.UUID, patch MemberPatch) (TeamMember, error) {
	p, m, err := s.caller(ctx, teamID)
	if err != nil {
		return TeamMember{}, err
	}
	if err := needRole(p, m, adminRoles...); err != nil {
		return TeamMember{}, err
	}
	if patch.Role != nil {
		if err := validRole(*patch.Role); err != nil {
			return TeamMember{}, err
		}
	}
	var out db.GetMemberRow
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockOwners(ctx, teamID); err != nil {
			return err
		}
		cur, err := q.GetMember(ctx, db.GetMemberParams{WorkspaceID: teamID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("member")
		}
		if err != nil {
			return err
		}
		role := cur.Role
		if patch.Role != nil {
			role = *patch.Role
		}
		if !canManage(m.Role, cur.Role) || !canManage(m.Role, role) {
			return apierr.Forbidden()
		}
		if cur.Role == auth.RoleOwner && role != auth.RoleOwner {
			if err := lastOwnerGuard(ctx, q, teamID); err != nil {
				return err
			}
		}
		projects := cur.ProjectIds
		if patch.SetProjects {
			projects = patch.ProjectIDs
		}
		projects, err = s.normalizeProjects(ctx, q, teamID, role, projects)
		if err != nil {
			return err
		}
		if _, err := q.UpdateMembership(ctx, db.UpdateMembershipParams{WorkspaceID: teamID, UserID: userID, Role: role, ProjectIds: projects}); err != nil {
			return err
		}
		out = cur
		out.Role, out.ProjectIds = role, projects
		return audit.Record(ctx, tx, audit.Entry{
			Action: "member.update", ResourceType: "member", ResourceID: userID.String(), WorkspaceID: teamID,
			Metadata: map[string]any{
				"email": cur.Email, "role": role, "previousRole": cur.Role, "restricted": projects != nil,
			},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return TeamMember{}, err
		}
		return TeamMember{}, fmt.Errorf("update member: %w", err)
	}
	s.publish("member.updated", teamID)
	return memberFrom(out.UserID, out.Name, out.Email, out.Role, out.ProjectIds, out.JoinedAt, p.UserID), nil
}

// lastOwnerGuard fails with 409 when the team has a single owner. The owner
// rows are locked by the caller (LockOwners), so two concurrent demotions
// cannot both pass.
func lastOwnerGuard(ctx context.Context, q *db.Queries, teamID uuid.UUID) error {
	owners, err := q.LockOwners(ctx, teamID)
	if err != nil {
		return err
	}
	if len(owners) <= 1 {
		return apierr.Conflict("last_owner", "a team needs at least one owner: make someone else an owner first")
	}
	return nil
}

// RemoveMember removes a member (admins and owners; owners only for owners) or
// lets a user leave a team they belong to. The last owner cannot be removed or
// leave. The removed member's API tokens in this team stop working.
func (s *Service) RemoveMember(ctx context.Context, teamID, userID uuid.UUID) error {
	// Leaving a team is never blocked by the team's 2FA requirement (a person who
	// cannot enrol must be able to walk away); acting on someone else is.
	p, m, err := s.membershipOf(ctx, teamID)
	if err != nil {
		return err
	}
	self := p.Type == auth.TypeUser && p.UserID == userID
	if !self {
		if err := s.auth.RequireSecondFactorIn(ctx, p, teamID); err != nil {
			return err
		}
		if err := needRole(p, m, adminRoles...); err != nil {
			return err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.LockOwners(ctx, teamID); err != nil {
			return err
		}
		cur, err := q.GetMember(ctx, db.GetMemberParams{WorkspaceID: teamID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("member")
		}
		if err != nil {
			return err
		}
		if !self && !canManage(m.Role, cur.Role) {
			return apierr.Forbidden()
		}
		if cur.Role == auth.RoleOwner {
			if err := lastOwnerGuard(ctx, q, teamID); err != nil {
				return err
			}
		}
		if _, err := q.DeleteMembership(ctx, db.DeleteMembershipParams{WorkspaceID: teamID, UserID: userID}); err != nil {
			return err
		}
		if err := q.RevokeUserTokensInTeam(ctx, db.RevokeUserTokensInTeamParams{WorkspaceID: teamID, UserID: userID}); err != nil {
			return err
		}
		action := "member.remove"
		if self {
			action = "member.leave"
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: action, ResourceType: "member", ResourceID: userID.String(), WorkspaceID: teamID,
			Metadata: map[string]any{"email": cur.Email, "role": cur.Role},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("remove member: %w", err)
	}
	s.publish("member.removed", teamID)
	return nil
}
