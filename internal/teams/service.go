// Package teams implements multi-team access: the teams a user belongs to,
// their members with a role and optional project restriction, invites by email
// or link (including sign-up through an invite) and the instance settings.
//
// A team is a workspace in code ("Team (UI) = workspace (code)"): every
// top-level table carries workspace_id, and the team a request acts in is the
// one stored in the session (or the token's team).
package teams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Deps are the collaborators of the Service.
type Deps struct {
	Pool      *pgxpool.Pool
	Bus       *events.Bus
	Auth      *auth.Service
	Box       *secretbox.Box
	Mailer    Mailer // nil: invites are link-only
	PublicURL string // NEBULACTRL_PUBLIC_URL, the base of invite links
	Logger    *slog.Logger
	Jobs      *jobs.Manager // runs the team deletion
}

// Service implements teams, members, invites and instance settings.
type Service struct {
	pool      *pgxpool.Pool
	q         *db.Queries
	bus       *events.Bus
	auth      *auth.Service
	box       *secretbox.Box
	mailer    Mailer
	publicURL string
	log       *slog.Logger
	now       func() time.Time
	jobs      *jobs.Manager
	teardown  Teardown
	slices    SliceLifecycle
}

// SliceLifecycle is a team's slice of the instance services (internal/instsvc):
// created when the team is, kept 30 days when it is deleted. Both calls run in
// the team's own transaction.
type SliceLifecycle interface {
	// EnqueueEnsureTx queues the provisioning of the new team's slices.
	EnqueueEnsureTx(ctx context.Context, tx pgx.Tx, teamID uuid.UUID) error
	// MarkDeletedTx starts the 30-day clock of the deleted team's slices.
	MarkDeletedTx(ctx context.Context, tx pgx.Tx, teamID uuid.UUID) error
}

// SetSlices plugs in the instance services' slice lifecycle (wiring only; nil
// when the installation has none).
func (s *Service) SetSlices(l SliceLifecycle) { s.slices = l }

// NewService returns a Service (Pool may be nil for OpenAPI generation).
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	s := &Service{
		pool: d.Pool, bus: d.Bus, auth: d.Auth, box: d.Box, mailer: d.Mailer, jobs: d.Jobs,
		publicURL: strings.TrimRight(d.PublicURL, "/"), log: d.Logger.With("component", "teams"), now: time.Now,
	}
	if d.Pool != nil {
		s.q = db.New(d.Pool)
	}
	return s
}

// Team is a team as seen by one of its members.
type Team struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Slug        string      `json:"slug"`
	Role        string      `json:"role" enum:"owner,admin,developer,viewer" doc:"The caller's role in this team"`
	ProjectIDs  []uuid.UUID `json:"projectIds,omitempty" doc:"The caller's project restriction; absent = all projects"`
	MemberCount int         `json:"memberCount"`
	Current     bool        `json:"current" doc:"The team the caller is acting in"`
	CreatedAt   time.Time   `json:"createdAt"`
	// Deletion is set while the team is being deleted or its deletion failed.
	Deletion *TeamDeletion `json:"deletion,omitempty"`
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// principal returns the authenticated caller.
func principal(ctx context.Context) (*auth.Principal, error) { return policy.Authenticated(ctx) }

// sessionUser returns the caller when they are a signed-in user (not a token).
func sessionUser(ctx context.Context) (*auth.Principal, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if p.Type != auth.TypeUser {
		return nil, apierr.Forbidden()
	}
	return p, nil
}

// caller resolves the caller's standing in team (see membershipOf) and applies
// that team's 2FA requirement: a user who has no second factor may do nothing on
// a team that requires one, from whichever team their session is acting in.
func (s *Service) caller(ctx context.Context, teamID uuid.UUID) (*auth.Principal, *auth.Membership, error) {
	p, m, err := s.membershipOf(ctx, teamID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.auth.RequireSecondFactorIn(ctx, p, teamID); err != nil {
		return nil, nil, err
	}
	return p, m, nil
}

// membershipOf resolves the caller's standing in team: their membership (a user may
// manage any of their teams, not only the current one; a token only its own)
// or a 404 for non-members, never revealing the team exists. It does not apply
// the team's 2FA requirement; caller does.
func (s *Service) membershipOf(ctx context.Context, teamID uuid.UUID) (*auth.Principal, *auth.Membership, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, nil, err
	}
	switch p.Type {
	case auth.TypeUser:
		m, err := s.auth.MembershipIn(ctx, p.UserID, teamID)
		if err != nil {
			return nil, nil, err
		}
		if m == nil {
			return nil, nil, apierr.NotFound("team")
		}
		return p, m, nil
	case auth.TypeToken, auth.TypeMCP:
		if p.WorkspaceID != teamID || p.Role == "" {
			return nil, nil, apierr.NotFound("team")
		}
		return p, &auth.Membership{WorkspaceID: teamID, Role: p.Role, ProjectIDs: p.ProjectIDs}, nil
	}
	return nil, nil, apierr.Forbidden()
}

// needRole returns 403 unless the caller's role is one of roles. Tokens must
// also hold the admin scope (it implies every scope their role holds).
func needRole(p *auth.Principal, m *auth.Membership, roles ...string) error {
	ok := false
	for _, r := range roles {
		if m.Role == r {
			ok = true
		}
	}
	if !ok {
		return apierr.Forbidden()
	}
	if p.Type != auth.TypeUser && !policy.HasScope(p, policy.ScopeAdmin) {
		return apierr.Forbidden()
	}
	return nil
}

// adminRoles may manage a team's members and invites.
var adminRoles = []string{auth.RoleOwner, auth.RoleAdmin}

// publish announces a change of team teamID. The event names the team, so a
// deleted team's feed still gets it.
func (s *Service) publish(typ string, teamID uuid.UUID) {
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: typ, ResourceID: teamID.String(), WorkspaceID: teamID.String()})
	}
}

func teamFromRow(id uuid.UUID, name, slug, role string, projects []uuid.UUID, members int, created time.Time, current uuid.UUID, del *TeamDeletion) Team {
	return Team{
		ID: id, Name: name, Slug: slug, Role: role, ProjectIDs: projects, MemberCount: members,
		Current: id == current, CreatedAt: created, Deletion: del,
	}
}

// List returns the caller's teams (the user's, for tokens too), oldest first.
func (s *Service) List(ctx context.Context) ([]Team, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListMyTeams(ctx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	out := make([]Team, 0, len(rows))
	for _, r := range rows {
		out = append(out, teamFromRow(r.ID, r.Name, r.Slug, r.Role, r.ProjectIds, int(r.MemberCount), r.CreatedAt, p.WorkspaceID,
			deletionOf(r.DeletionState, r.DeletionError, r.DeletionStep, r.DeletionStartedAt, r.DeletionForgotten, r.DeletionKeepDns)))
	}
	return out, nil
}

func (s *Service) team(ctx context.Context, userID, teamID, current uuid.UUID) (Team, error) {
	r, err := s.q.GetTeamForUser(ctx, db.GetTeamForUserParams{UserID: userID, ID: teamID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, apierr.NotFound("team")
	}
	if err != nil {
		return Team{}, fmt.Errorf("get team: %w", err)
	}
	return teamFromRow(r.ID, r.Name, r.Slug, r.Role, r.ProjectIds, int(r.MemberCount), r.CreatedAt, current,
		deletionOf(r.DeletionState, r.DeletionError, r.DeletionStep, r.DeletionStartedAt, r.DeletionForgotten, r.DeletionKeepDns)), nil
}

// Get returns one of the caller's teams.
func (s *Service) Get(ctx context.Context, teamID uuid.UUID) (Team, error) {
	p, _, err := s.caller(ctx, teamID)
	if err != nil {
		return Team{}, err
	}
	return s.team(ctx, p.UserID, teamID, p.WorkspaceID)
}

// Create makes a team owned by the caller. Users may create teams unless the
// instance admin disabled it; the instance admin always may. Browser sessions
// and admin-scope tokens create teams (the caller, not the token's team,
// becomes the new team's owner).
func (s *Service) Create(ctx context.Context, name string) (Team, error) {
	p, err := principal(ctx)
	if err == nil && p.Type != auth.TypeUser && (p.Type != auth.TypeToken || !policy.HasScope(p, policy.ScopeAdmin)) {
		err = apierr.Forbidden()
	}
	if err != nil {
		return Team{}, err
	}
	name, err = auth.NormalizeTeamName(name)
	if err != nil {
		return Team{}, err
	}
	var ws db.Workspace
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := s.checkCreationAllowed(ctx, q, p.UserID); err != nil {
			return err
		}
		var err error
		ws, err = auth.NewTeam(ctx, q, name, &p.UserID)
		if err != nil {
			return err
		}
		if _, err := q.InsertMembership(ctx, db.InsertMembershipParams{UserID: p.UserID, WorkspaceID: ws.ID, Role: auth.RoleOwner}); err != nil {
			return err
		}
		// The team's slice of the built-in services is made right after it, so
		// backups work from the first minute.
		if s.slices != nil {
			if err := s.slices.EnqueueEnsureTx(ctx, tx, ws.ID); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "team.create", ResourceType: "team", ResourceID: ws.ID.String(), WorkspaceID: ws.ID,
			Metadata: map[string]any{"name": ws.Name, "slug": ws.Slug},
		})
	})
	if uniqueViolation(err) {
		return Team{}, apierr.Conflict("slug_conflict", "a team with that name was just created; retry")
	}
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return Team{}, err
		}
		return Team{}, fmt.Errorf("create team: %w", err)
	}
	s.publish("team.created", ws.ID)
	return s.team(ctx, p.UserID, ws.ID, p.WorkspaceID)
}

// checkCreationAllowed enforces the instance setting allow_team_creation.
func (s *Service) checkCreationAllowed(ctx context.Context, q *db.Queries, userID uuid.UUID) error {
	set, err := q.GetInstanceSettings(ctx)
	if err != nil {
		return fmt.Errorf("instance settings: %w", err)
	}
	if set.AllowTeamCreation {
		return nil
	}
	first, err := q.FirstUserID(ctx)
	if err != nil {
		return fmt.Errorf("first user: %w", err)
	}
	if first == userID {
		return nil
	}
	return apierr.New(403, "team_creation_disabled", "the instance administrator has disabled creating teams")
}

// Rename changes a team's name (admins and owners). The slug is stable.
func (s *Service) Rename(ctx context.Context, teamID uuid.UUID, name string) (Team, error) {
	p, m, err := s.caller(ctx, teamID)
	if err != nil {
		return Team{}, err
	}
	if err := needRole(p, m, adminRoles...); err != nil {
		return Team{}, err
	}
	name, err = auth.NormalizeTeamName(name)
	if err != nil {
		return Team{}, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).UpdateTeamName(ctx, db.UpdateTeamNameParams{ID: teamID, Name: name}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "team.update", ResourceType: "team", ResourceID: teamID.String(), WorkspaceID: teamID,
			Metadata: map[string]any{"name": name},
		})
	})
	if err != nil {
		return Team{}, fmt.Errorf("rename team: %w", err)
	}
	s.publish("team.updated", teamID)
	return s.team(ctx, p.UserID, teamID, p.WorkspaceID)
}

// Switch checks that the caller is a member of the team and returns it; the
// handler then stores the team in the session. A non-member gets 404.
func (s *Service) Switch(ctx context.Context, teamID uuid.UUID) (Team, error) {
	p, err := sessionUser(ctx)
	if err != nil {
		return Team{}, err
	}
	m, err := s.auth.SwitchTeam(ctx, p.UserID, teamID)
	if err != nil {
		return Team{}, err
	}
	if m == nil {
		return Team{}, apierr.NotFound("team")
	}
	return s.team(ctx, p.UserID, teamID, teamID)
}
