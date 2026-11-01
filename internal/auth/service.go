package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
)

// MinPasswordLength is enforced on setup and password changes.
const MinPasswordLength = 10

// ErrInvalidCredentials is returned for a bad email/password pair.
var ErrInvalidCredentials = apierr.New(401, "invalid_credentials", "invalid email or password")

// setupLockID serialises concurrent first-run setups.
const setupLockID int64 = 0x4e656275_6c61 // "Nebula"

// User is the public view of a user.
type User struct {
	ID        uuid.UUID
	Email     string
	Name      string
	CreatedAt time.Time
}

func userFrom(u db.User) *User {
	return &User{ID: u.ID, Email: u.Email, Name: u.Name, CreatedAt: u.CreatedAt}
}

// Service holds authentication logic. It does not depend on policy: callers
// that need authorization (e.g. token management) live elsewhere.
type Service struct {
	pool       *pgxpool.Pool
	q          *db.Queries
	roleScopes RoleScopes
	params     *argon2id.Params
	// dummyHash is compared against when the user does not exist so that login
	// timing does not reveal which emails are registered.
	dummyHash string
	// methods holds the secret box, public URL and sign-up hooks used by the
	// login methods (TOTP, OIDC); see methods.go.
	methods loginMethods
	// reset is the password-reset mailer (see passwordreset.go); guarded by
	// methods.mu.
	reset resetState
}

// NewService returns a Service. roleScopes maps a membership role to the
// scopes of browser sessions and the ceiling of API tokens. pool may be nil
// for OpenAPI generation.
func NewService(pool *pgxpool.Pool, roleScopes RoleScopes) *Service {
	s := &Service{pool: pool, q: db.New(pool), roleScopes: roleScopes, params: argon2id.DefaultParams}
	s.dummyHash, _ = argon2id.CreateHash("nebulactrl-dummy-password", s.params)
	return s
}

// NeedsSetup reports whether no users exist yet.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.q.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	return n == 0, nil
}

// DefaultTeamName names the first team when setup does not choose one.
const DefaultTeamName = "NebulaCtrl"

// Setup creates the first user and the first team (which the user owns). The
// first user is the instance admin. teamName may be empty (DefaultTeamName).
// It fails with 409 already_setup once any user exists.
func (s *Service) Setup(ctx context.Context, email, password, name, teamName string) (*User, error) {
	email = normalizeEmail(email)
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	if strings.TrimSpace(teamName) == "" {
		teamName = DefaultTeamName
	}
	hash, err := argon2id.CreateHash(password, s.params)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	var user *User
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", setupLockID); err != nil {
			return err
		}
		q := s.q.WithTx(tx)
		n, err := q.CountUsers(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return apierr.Conflict("already_setup", "NebulaCtrl is already set up")
		}
		u, err := q.InsertUser(ctx, db.InsertUserParams{Email: email, Name: strings.TrimSpace(name), PasswordHash: hash})
		if err != nil {
			return err
		}
		ws, err := adoptOrCreateFirstTeam(ctx, q, teamName, &u.ID)
		if err != nil {
			return err
		}
		if _, err := q.InsertMembership(ctx, db.InsertMembershipParams{UserID: u.ID, WorkspaceID: ws.ID, Role: RoleOwner}); err != nil {
			return err
		}
		user = userFrom(u)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// Login verifies credentials.
func (s *Service) Login(ctx context.Context, email, password string) (*User, error) {
	u, err := s.q.GetUserByEmail(ctx, normalizeEmail(email))
	if errors.Is(err, pgx.ErrNoRows) {
		_, _ = argon2id.ComparePasswordAndHash(password, s.dummyHash)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if u.PasswordHash == "" {
		// An account that signs in through a provider or a passkey has no
		// password: spend the same time as for a wrong one, and never accept.
		_, _ = argon2id.ComparePasswordAndHash(password, s.dummyHash)
		return nil, ErrInvalidCredentials
	}
	ok, err := argon2id.ComparePasswordAndHash(password, u.PasswordHash)
	if err != nil || !ok {
		return nil, ErrInvalidCredentials
	}
	return userFrom(u), nil
}

// UserByID loads a user, or returns pgx.ErrNoRows.
func (s *Service) UserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	u, err := s.q.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return userFrom(u), nil
}

// UpdateProfile changes the user's display name.
func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID, name string) (*User, error) {
	u, err := s.q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{ID: id, Name: strings.TrimSpace(name)})
	if err != nil {
		return nil, err
	}
	return userFrom(u), nil
}

// ChangePassword verifies the current password and sets a new one.
func (s *Service) ChangePassword(ctx context.Context, id uuid.UUID, current, next string) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	u, err := s.q.GetUser(ctx, id)
	if err != nil {
		return err
	}
	ok, err := argon2id.ComparePasswordAndHash(current, u.PasswordHash)
	if err != nil || !ok {
		return apierr.New(403, "invalid_credentials", "current password is incorrect")
	}
	hash, err := argon2id.CreateHash(next, s.params)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: id, PasswordHash: hash})
}

// RoleScopes maps a team role to the scopes it grants. policy.ScopesForRole is
// the implementation; auth cannot import policy.
type RoleScopes func(role string) []string

// PrincipalForSession builds the principal of a browser-session user in the
// team stored in the session (teamID, nil when none). When the user is no
// longer a member of that team it falls back to their oldest team; a user in
// no team gets a principal without a workspace or scopes (they can still
// create a team or accept an invite).
func (s *Service) PrincipalForSession(ctx context.Context, u *User, teamID *uuid.UUID) (*Principal, error) {
	p := &Principal{Type: TypeUser, UserID: u.ID, Label: u.Email, Scopes: []string{}}
	m, err := s.membershipFor(ctx, u.ID, teamID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return p, nil
	}
	p.WorkspaceID, p.Role = m.WorkspaceID, m.Role
	p.Scopes = s.scopesFor(m.Role)
	if m.Role != RoleOwner && m.Role != RoleAdmin {
		p.ProjectIDs = m.ProjectIDs
	}
	return p, nil
}

// ErrInvalidToken is returned for unknown, malformed, revoked or expired tokens.
var ErrInvalidToken = apierr.New(401, "invalid_token", "invalid or expired API token")

// ErrMCPTokenOnREST refuses an MCP token on /api/v1: it works only on /mcp,
// where the production guard and the deny list apply.
var ErrMCPTokenOnREST = apierr.New(403, "token_kind_mismatch", "this is an MCP token: it works only on /mcp. Create a REST API token (Account → API tokens) for the REST API or the CLI")

// ErrRESTTokenOnMCP refuses a REST or CLI token on /mcp.
var ErrRESTTokenOnMCP = apierr.New(403, "token_kind_mismatch", "this is a REST API token: it works only on /api/v1. Create an MCP token (Account → API tokens → MCP) for agents")

// AuthenticateBearer resolves a raw "nct_…" API token to a principal. The
// token's scopes are intersected with the creator's current role in the
// token's team and its projects with the creator's project restriction, so a
// demoted or removed member's tokens lose power at once.
func (s *Service) AuthenticateBearer(ctx context.Context, raw string) (*Principal, error) {
	if !strings.HasPrefix(raw, TokenPrefix) || len(raw) > 128 {
		return nil, ErrInvalidToken
	}
	t, err := s.q.GetAPITokenByHash(ctx, HashToken(raw))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("lookup token: %w", err)
	}
	now := time.Now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && !t.ExpiresAt.After(now)) {
		return nil, ErrInvalidToken
	}
	m, err := s.membershipFor(ctx, t.UserID, &t.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if m == nil || m.WorkspaceID != t.WorkspaceID {
		return nil, ErrInvalidToken
	}
	// Best-effort, throttled in SQL; never fail authentication on it.
	_ = s.q.TouchAPIToken(ctx, t.ID)
	// The exemption from the production guard is only honoured while the
	// creator is still the instance admin or a team Owner/Admin, like scopes (a
	// demoted member's token is guarded again at once).
	exempt := false
	if t.AllowProductionWithoutApproval {
		exempt = RoleRank(m.Role) >= RoleRank(RoleAdmin)
		if !exempt {
			if exempt, err = s.IsInstanceAdmin(ctx, t.UserID); err != nil {
				return nil, err
			}
		}
	}
	id := t.ID
	return &Principal{
		Type:           TypeToken,
		UserID:         t.UserID,
		TokenID:        &id,
		WorkspaceID:    t.WorkspaceID,
		Role:           m.Role,
		Label:          t.Name,
		Scopes:         EffectiveScopes(t.Scopes, s.scopesFor(m.Role)),
		ProjectIDs:     EffectiveProjects(t.ProjectIds, m),
		TokenKind:      t.Kind,
		TokenExpiresAt: t.ExpiresAt,

		AllowProductionWithoutApproval: exempt,
		RequireProductionApproval:      !exempt,
	}, nil
}

// ValidatePassword enforces the password policy.
func ValidatePassword(p string) error {
	if len(p) < MinPasswordLength {
		return apierr.Unprocessable("weak_password", fmt.Sprintf("password must be at least %d characters", MinPasswordLength))
	}
	if len(p) > 256 {
		return apierr.Unprocessable("weak_password", "password must be at most 256 characters")
	}
	return nil
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
