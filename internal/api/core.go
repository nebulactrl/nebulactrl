package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/time/rate"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// Session keys: the signed-in user and the team they are currently acting in.
const (
	sessionUserKey = auth.SessionUserKey
	sessionTeamKey = "teamId"
)

func sessionUserID(ctx context.Context, s *scs.SessionManager) *uuid.UUID {
	return sessionUUID(ctx, s, sessionUserKey)
}

func sessionTeamID(ctx context.Context, s *scs.SessionManager) *uuid.UUID {
	return sessionUUID(ctx, s, sessionTeamKey)
}

func sessionUUID(ctx context.Context, s *scs.SessionManager, key string) *uuid.UUID {
	raw := s.GetString(ctx, key)
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	return &id
}

// StartUserSession signs userID in on the current request's session (rotating
// the token) acting in teamID; uuid.Nil leaves the team to be resolved from
// the user's memberships. Used by login, setup and invite sign-up.
func StartUserSession(ctx context.Context, s *scs.SessionManager, userID, teamID uuid.UUID) error {
	if err := s.RenewToken(ctx); err != nil {
		return fmt.Errorf("renew session: %w", err)
	}
	s.Put(ctx, sessionUserKey, userID.String())
	if teamID == uuid.Nil {
		s.Remove(ctx, sessionTeamKey)
	} else {
		s.Put(ctx, sessionTeamKey, teamID.String())
	}
	return nil
}

// SetSessionTeam switches the current session to teamID. The caller has
// already verified the membership.
func SetSessionTeam(ctx context.Context, s *scs.SessionManager, teamID uuid.UUID) {
	s.Put(ctx, sessionTeamKey, teamID.String())
}

// core holds the endpoints that belong to the HTTP layer itself.
type core struct {
	auth     *auth.Service
	sessions *scs.SessionManager
	bus      *events.Bus
	logins   *ipLimiter
	scope    *eventScope
	// accountLogins counts wrong passwords per account (and wrong current
	// passwords per user), so a spray from many addresses is slowed too.
	accountLogins *ipLimiter
	// setupToken guards first-run setup: NEBULACTRL_SETUP_TOKEN or the one the server generated and logged at start. Empty only in tests.
	setupToken string
}

// ---- types ----

// UserBody is the public view of a user. WorkspaceID is the current team's id
// (the team the caller acts in; the nil UUID when the user has no team).
type UserBody struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	WorkspaceID uuid.UUID `json:"workspaceId"`
	CreatedAt   time.Time `json:"createdAt"`
}

// PrincipalBody describes how the caller authenticated.
type PrincipalBody struct {
	Type       string      `json:"type" enum:"user,token,mcp"`
	Label      string      `json:"label"`
	TokenID    *uuid.UUID  `json:"tokenId,omitempty"`
	Role       string      `json:"role,omitempty" enum:"owner,admin,developer,viewer" doc:"The caller's role in the current team (the creator's role for tokens); absent without a team"`
	Scopes     []string    `json:"scopes"`
	ProjectIDs []uuid.UUID `json:"projectIds,omitempty" doc:"Absent when the principal can access all projects"`
}

// MeBody is the response of GET /me and of login/setup.
type MeBody struct {
	UserBody
	InstanceAdmin bool          `json:"instanceAdmin" doc:"The first user: manages instance settings"`
	Principal     PrincipalBody `json:"principal"`
}

type meOutput struct{ Body MeBody }

type setupStatusOutput struct {
	Body struct {
		NeedsSetup bool `json:"needsSetup"`
		// TokenRequired is true while setup is open and a setup token guards it: POST /setup must carry it.
		TokenRequired bool `json:"tokenRequired"`
	}
}

type setupInput struct {
	Body struct {
		Email    string `json:"email" format:"email" maxLength:"254"`
		Password string `json:"password" minLength:"10" maxLength:"256"`
		Name     string `json:"name" minLength:"1" maxLength:"100"`
		TeamName string `json:"teamName,omitempty" maxLength:"80" doc:"Name of the first team; default NebulaCtrl"`
		// SetupToken is required while setup is open (the installer's setup URL carries it; a server without NEBULACTRL_SETUP_TOKEN logs a generated one).
		SetupToken string `json:"setupToken,omitempty" maxLength:"128" doc:"The one-time setup token: printed by the installer, or written to the server log at start when none was configured"`
	}
}

type loginInput struct {
	Body struct {
		Email    string `json:"email" maxLength:"254"`
		Password string `json:"password" maxLength:"256"`
	}
}

type updateMeInput struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"100"`
	}
}

type changePasswordOutput struct {
	Body struct {
		RevokedTokens int             `json:"revokedTokens" doc:"How many API tokens this change revoked"`
		KeptTokens    []LiveTokenBody `json:"keptTokens" doc:"The API tokens that still work after the change"`
	}
}

type changePasswordInput struct {
	Body struct {
		CurrentPassword string `json:"currentPassword" maxLength:"256"`
		NewPassword     string `json:"newPassword" minLength:"10" maxLength:"256"`
		RevokeAPITokens bool   `json:"revokeApiTokens,omitempty" required:"false" doc:"Also revoke every API token of the account, in every team. Default false: tokens keep working"`
	}
}

func (c *core) meBody(ctx context.Context, p *auth.Principal, u *auth.User) MeBody {
	pb := PrincipalBody{Type: p.Type, Label: p.Label, TokenID: p.TokenID, Role: p.Role, Scopes: p.Scopes, ProjectIDs: p.ProjectIDs}
	if pb.Scopes == nil {
		pb.Scopes = []string{}
	}
	admin, err := c.auth.IsInstanceAdmin(ctx, u.ID)
	if err != nil {
		slog.ErrorContext(ctx, "instance admin lookup", "error", err)
	}
	return MeBody{
		UserBody:      UserBody{ID: u.ID, Email: u.Email, Name: u.Name, WorkspaceID: p.WorkspaceID, CreatedAt: u.CreatedAt},
		InstanceAdmin: admin,
		Principal:     pb,
	}
}

// startSession signs u in, acting in their oldest team.
func (c *core) startSession(ctx context.Context, u *auth.User) (MeBody, error) {
	if err := StartUserSession(ctx, c.sessions, u.ID, uuid.Nil); err != nil {
		return MeBody{}, err
	}
	p, err := c.auth.PrincipalForSession(ctx, u, nil)
	if err != nil {
		return MeBody{}, err
	}
	if p.WorkspaceID != uuid.Nil {
		SetSessionTeam(ctx, c.sessions, p.WorkspaceID)
	}
	return c.meBody(ctx, p, u), nil
}

// ---- registration ----

func (c *core) register(h huma.API) {
	huma.Register(h, Public(huma.Operation{
		OperationID: "getSetupStatus", Method: http.MethodGet, Path: "/setup", Tags: []string{"setup"},
		Summary: "Whether first-run setup is still required",
	}), func(ctx context.Context, _ *struct{}) (*setupStatusOutput, error) {
		needs, err := c.auth.NeedsSetup(ctx)
		if err != nil {
			return nil, err
		}
		out := &setupStatusOutput{}
		out.Body.NeedsSetup = needs
		out.Body.TokenRequired = needs && c.setupToken != ""
		return out, nil
	})

	huma.Register(h, Public(huma.Operation{
		OperationID: "setup", Method: http.MethodPost, Path: "/setup", Tags: []string{"setup"},
		Summary:       "Create the workspace and first user, and sign in",
		Description:   "Only works while no users exist; afterwards returns 409 `already_setup`. First-run setup needs a one-time setup token: the installer sets `NEBULACTRL_SETUP_TOKEN`, and a server started without one generates a token and writes it to its log at start. The matching `setupToken` is required (403 `setup_token_invalid`).",
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *setupInput) (*meOutput, error) {
		if c.setupToken != "" {
			if !c.logins.Allow(ClientIP(ctx)) {
				return nil, apierr.TooManyRequests("too many setup attempts; try again in a minute")
			}
			if needs, err := c.auth.NeedsSetup(ctx); err == nil && needs &&
				subtle.ConstantTimeCompare([]byte(in.Body.SetupToken), []byte(c.setupToken)) != 1 {
				return nil, apierr.New(http.StatusForbidden, "setup_token_invalid", "the setup token is missing or wrong; use the setup URL the installer printed")
			}
		}
		u, err := c.auth.Setup(ctx, in.Body.Email, in.Body.Password, in.Body.Name, in.Body.TeamName)
		if err != nil {
			return nil, err
		}
		body, err := c.startSession(ctx, u)
		if err != nil {
			return nil, err
		}
		return &meOutput{Body: body}, nil
	})

	huma.Register(h, Public(huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: "/auth/login", Tags: []string{"auth"},
		Summary:     "Sign in with email and password (sets the session cookie)",
		Description: "Limited to 10 attempts per minute per client IP, and to 10 wrong passwords per 10 minutes per account (429 `too_many_requests`).",
		Errors:      []int{http.StatusUnauthorized, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *loginInput) (*meOutput, error) {
		if !c.logins.Allow(ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many login attempts; try again in a minute")
		}
		acct := "email:" + strings.ToLower(strings.TrimSpace(in.Body.Email))
		if !c.accountLogins.Peek(acct) {
			return nil, apierr.TooManyRequests("too many failed sign-in attempts for this account; try again in a few minutes")
		}
		u, err := c.auth.Login(ctx, in.Body.Email, in.Body.Password)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				c.accountLogins.Allow(acct) // only wrong passwords count against the account
			}
			return nil, err
		}
		if err := c.beginSecondFactor(ctx, u); err != nil {
			return nil, err
		}
		body, err := c.startSession(ctx, u)
		if err != nil {
			return nil, err
		}
		return &meOutput{Body: body}, nil
	})

	huma.Register(h, Public(huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: "/auth/logout", Tags: []string{"auth"},
		Summary: "Destroy the current session", DefaultStatus: http.StatusNoContent,
	}), func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		if err := c.sessions.Destroy(ctx); err != nil {
			return nil, fmt.Errorf("destroy session: %w", err)
		}
		return nil, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "getMe", Method: http.MethodGet, Path: "/me", Tags: []string{"me"},
		Summary: "The authenticated user and principal",
		Errors:  []int{http.StatusUnauthorized},
	}), func(ctx context.Context, _ *struct{}) (*meOutput, error) {
		p, err := policy.Authenticated(ctx)
		if err != nil {
			return nil, err
		}
		u, err := c.auth.UserByID(ctx, p.UserID)
		if err != nil {
			return nil, apierr.Unauthorized()
		}
		return &meOutput{Body: c.meBody(ctx, p, u)}, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "updateMe", Method: http.MethodPatch, Path: "/me", Tags: []string{"me"},
		Summary: "Update the signed-in user's profile", Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}), func(ctx context.Context, in *updateMeInput) (*meOutput, error) {
		p, err := policy.Authenticated(ctx)
		if err != nil {
			return nil, err
		}
		if p.Type != auth.TypeUser {
			return nil, apierr.Forbidden()
		}
		u, err := c.auth.UpdateProfile(ctx, p.UserID, in.Body.Name)
		if err != nil {
			return nil, err
		}
		return &meOutput{Body: c.meBody(ctx, p, u)}, nil
	})

	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "changePassword", Method: http.MethodPost, Path: "/me/password", Tags: []string{"me"},
		Summary:     "Change the signed-in user's password",
		Description: "Ends every other session of the account and keeps the current one. API tokens keep working unless `revokeApiTokens` is true; the answer lists the ones that still work so the person can see them. Wrong current passwords are limited to 10 per 10 minutes.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusUnprocessableEntity},
	}), func(ctx context.Context, in *changePasswordInput) (*changePasswordOutput, error) {
		p, err := policy.Authenticated(ctx)
		if err != nil {
			return nil, err
		}
		if p.Type != auth.TypeUser {
			return nil, apierr.Forbidden()
		}
		key := "user:" + p.UserID.String()
		if !c.accountLogins.Peek(key) {
			return nil, apierr.TooManyRequests("too many wrong passwords; try again in a few minutes")
		}
		if err := c.auth.ChangePassword(ctx, p.UserID, in.Body.CurrentPassword, in.Body.NewPassword); err != nil {
			if ae, ok := apierr.As(err); ok && ae.Code == "invalid_credentials" {
				c.accountLogins.Allow(key) // count the wrong current password
			}
			return nil, err
		}
		// The new password ends every other session; this browser keeps its own,
		// under a fresh token.
		if err := auth.RevokeOtherUserSessions(ctx, c.sessions, p.UserID); err != nil {
			return nil, fmt.Errorf("revoke other sessions: %w", err)
		}
		if err := c.sessions.RenewToken(ctx); err != nil {
			return nil, fmt.Errorf("renew session: %w", err)
		}
		revoked := 0
		if err := c.auth.InTx(ctx, func(tx pgx.Tx) error {
			if in.Body.RevokeAPITokens {
				var err error
				if revoked, err = c.auth.RevokeLiveTokens(ctx, tx, p.UserID); err != nil {
					return err
				}
			}
			if p.WorkspaceID == uuid.Nil {
				return nil // a user in no team has no team audit trail
			}
			return recordAudit(ctx, tx, "user.password_change", "user", p.UserID.String(),
				map[string]any{"revokeApiTokens": in.Body.RevokeAPITokens, "tokensRevoked": revoked})
		}); err != nil {
			return nil, err
		}
		kept, err := c.auth.LiveTokens(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		out := &changePasswordOutput{}
		out.Body.RevokedTokens = revoked
		out.Body.KeptTokens = LiveTokenBodies(kept)
		return out, nil
	})

	c.registerLoginMethods(h)
	c.registerEvents(h)
}

// ---- GET /events (SSE) ----

func (c *core) registerEvents(h huma.API) {
	huma.Register(h, WithScopes(huma.Operation{
		OperationID: "streamEvents", Method: http.MethodGet, Path: "/events", Tags: []string{"events"},
		Summary: "Server-sent invalidation feed",
		Description: "Each message is `data: {\"type\",\"projectId\",\"environmentId\",\"resourceId\"}`. " +
			"A comment line is sent every 15 seconds as a heartbeat. Clients refetch the named resources.",
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Event stream",
				Content:     map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{Type: "string"}}},
			},
		},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, policy.ScopeProjectsRead), func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
			return nil, err
		}
		p, _ := auth.PrincipalFrom(ctx)
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			sse, err := NewSSE(hctx)
			if err != nil {
				return
			}
			ch := c.bus.Subscribe(hctx.Context())
			ping := time.NewTicker(15 * time.Second)
			defer ping.Stop()
			for {
				select {
				case <-hctx.Context().Done():
					return
				case <-ping.C:
					if sse.Comment("ping") != nil {
						return
					}
				case ev, ok := <-ch:
					if !ok {
						return
					}
					if !eventVisible(p, ev) || !c.scope.deliver(hctx.Context(), p, ev) {
						continue
					}
					if sse.Data(ev) != nil {
						return
					}
				}
			}
		}}, nil
	})
}

func eventVisible(p *auth.Principal, ev events.Event) bool {
	if !p.Restricted() {
		return true
	}
	id, err := uuid.Parse(ev.ProjectID)
	return err == nil && p.CanSeeProject(id)
}

// SSE writes a text/event-stream response. Use it from huma.StreamResponse
// bodies and raw handlers.
type SSE struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// NewSSE prepares the response for streaming and sends an initial comment so
// proxies and clients see the stream open immediately.
func NewSSE(hctx huma.Context) (*SSE, error) {
	_, w := humachi.Unwrap(hctx)
	hctx.SetHeader("Content-Type", "text/event-stream")
	hctx.SetHeader("Cache-Control", "no-cache")
	hctx.SetHeader("Connection", "keep-alive")
	hctx.SetHeader("X-Accel-Buffering", "no")
	hctx.SetStatus(http.StatusOK)
	s := &SSE{w: w, rc: http.NewResponseController(w)}
	_ = s.rc.SetWriteDeadline(time.Time{})
	if err := s.Comment("connected"); err != nil {
		return nil, err
	}
	return s, nil
}

// Data sends v as a JSON `data:` message.
func (s *SSE) Data(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Comment sends an SSE comment line (used for heartbeats).
func (s *SSE) Comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	return s.rc.Flush()
}

// ---- login rate limiting ----

type ipLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	entries map[string]*ipEntry
	lastGC  time.Time
}

type ipEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// newIPLimiter allows n events per interval per key.
func newIPLimiter(n int, interval time.Duration) *ipLimiter {
	return &ipLimiter{limit: rate.Every(interval / time.Duration(n)), burst: n, entries: map[string]*ipEntry{}}
}

// Allow consumes one event for key.
func (l *ipLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) > 5*time.Minute {
		for k, e := range l.entries {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(l.entries, k)
			}
		}
		l.lastGC = now
	}
	e, ok := l.entries[key]
	if !ok {
		e = &ipEntry{lim: rate.NewLimiter(l.limit, l.burst)}
		l.entries[key] = e
	}
	e.seen = now
	return e.lim.Allow()
}

// Peek reports whether key still has an event to spend, without spending one.
// A key never seen has its full allowance.
func (l *ipLimiter) Peek(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return true
	}
	return e.lim.TokensAt(time.Now()) >= 1
}
