// Package passwordreset serves the password-reset endpoints: emailing a link,
// setting a new password with it, and whether the login page may offer it.
package passwordreset

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/api"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
)

// Rate limits of the password-reset endpoints. Asking for a link is limited per
// client IP and per email address; the email key is the typed address whether
// or not an account exists, so being limited reveals nothing about accounts.
const (
	resetRequestsPerIPPerHour    = 10
	resetRequestsPerEmailPerHour = 3
	resetConfirmsPerIPPerMinute  = 10
)

type passwordResetStatusOutput struct {
	Body struct {
		Available bool `json:"available" doc:"Whether the login page may offer Forgot password: an email can be sent and the control plane has a public URL"`
	}
}

type passwordResetRequestInput struct {
	Body struct {
		Email string `json:"email" maxLength:"254"`
	}
}

type passwordResetRequestOutput struct {
	Body struct {
		Status string `json:"status" enum:"requested" doc:"Always 'requested': the answer is the same whether or not the account exists"`
	}
}

type passwordResetCheckInput struct {
	Body struct {
		Token string `json:"token" maxLength:"128"`
	}
}

type passwordResetConfirmInput struct {
	Body struct {
		Token           string `json:"token" maxLength:"128"`
		Password        string `json:"password" minLength:"10" maxLength:"256"`
		RevokeAPITokens bool   `json:"revokeApiTokens,omitempty" required:"false" doc:"Also revoke every API token of the account, in every team. Default false: tokens keep working"`
	}
}

type passwordResetConfirmOutput struct {
	Body struct {
		RevokedTokens int                 `json:"revokedTokens" doc:"How many API tokens this reset revoked"`
		KeptTokens    []api.LiveTokenBody `json:"keptTokens" doc:"The API tokens that still work after the reset"`
	}
}

// Register attaches the password-reset endpoints. sessions is the session
// manager whose sessions a completed reset ends.
func Register(h huma.API, svc *auth.Service, sessions *scs.SessionManager) {
	byIP := api.NewIPLimiter(resetRequestsPerIPPerHour, time.Hour)
	byEmail := api.NewIPLimiter(resetRequestsPerEmailPerHour, time.Hour)
	confirms := api.NewIPLimiter(resetConfirmsPerIPPerMinute, time.Minute)

	huma.Register(h, api.Public(huma.Operation{
		OperationID: "getPasswordResetStatus", Method: http.MethodGet, Path: "/auth/password-reset", Tags: []string{"auth"},
		Summary: "Whether password reset by email is available",
	}), func(ctx context.Context, _ *struct{}) (*passwordResetStatusOutput, error) {
		out := &passwordResetStatusOutput{}
		out.Body.Available = svc.PasswordResetAvailable(ctx)
		return out, nil
	})

	huma.Register(h, api.Public(huma.Operation{
		OperationID: "requestPasswordReset", Method: http.MethodPost, Path: "/auth/password-reset/request", Tags: []string{"auth"},
		Summary: "Email a password reset link",
		Description: "Answers 202 whether or not the account exists, so it cannot be used to find out which emails are registered; " +
			"accounts that sign in only through an identity provider get no email. The link works once and expires after 30 minutes. " +
			"Limited to 10 requests per hour per client IP and 3 per hour per email address. " +
			"503 `password_reset_unavailable` when the instance has no SMTP relay or team email channel, or no public URL.",
		DefaultStatus: http.StatusAccepted,
		Errors:        []int{http.StatusTooManyRequests, http.StatusServiceUnavailable},
	}), func(ctx context.Context, in *passwordResetRequestInput) (*passwordResetRequestOutput, error) {
		if !byIP.Allow("ip:"+api.ClientIP(ctx)) || !byEmail.Allow("email:"+strings.ToLower(strings.TrimSpace(in.Body.Email))) {
			return nil, apierr.TooManyRequests("too many password reset requests; try again later")
		}
		if err := svc.RequestPasswordReset(ctx, in.Body.Email); err != nil {
			return nil, err
		}
		out := &passwordResetRequestOutput{}
		out.Body.Status = "requested"
		return out, nil
	})

	huma.Register(h, api.Public(huma.Operation{
		OperationID: "checkPasswordResetToken", Method: http.MethodPost, Path: "/auth/password-reset/check", Tags: []string{"auth"},
		Summary: "Whether a reset link still works",
		Description: "Does not spend the link: the reset page asks when it opens, so a dead link is explained before a new password is typed. " +
			"204 when the link is valid, 400 `invalid_reset_token` for an unknown, expired or used link; shares the confirm limit of 10 attempts per minute per client IP.",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusBadRequest, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *passwordResetCheckInput) (*struct{}, error) {
		if !confirms.Allow(api.ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		return nil, svc.CheckResetToken(ctx, in.Body.Token)
	})

	huma.Register(h, api.Public(huma.Operation{
		OperationID: "confirmPasswordReset", Method: http.MethodPost, Path: "/auth/password-reset/confirm", Tags: []string{"auth"},
		Summary: "Set a new password with a reset link",
		Description: "Single use. Ends every session of the account; two-factor authentication still applies at the next sign-in. " +
			"API tokens keep working unless `revokeApiTokens` is true; the answer lists the ones that still work so the person can see them. " +
			"400 `invalid_reset_token` for an unknown, expired or used link; limited to 10 attempts per minute per client IP.",
		Errors: []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
	}), func(ctx context.Context, in *passwordResetConfirmInput) (*passwordResetConfirmOutput, error) {
		if !confirms.Allow(api.ClientIP(ctx)) {
			return nil, apierr.TooManyRequests("too many attempts; try again in a minute")
		}
		var user *auth.User
		revoked := 0
		err := svc.InTx(ctx, func(tx pgx.Tx) error {
			u, teams, err := svc.ConfirmPasswordReset(ctx, tx, in.Body.Token, in.Body.Password)
			if err != nil {
				return err
			}
			user = u
			if in.Body.RevokeAPITokens {
				if revoked, err = svc.RevokeLiveTokens(ctx, tx, u.ID); err != nil {
					return err
				}
			}
			// The user acts as themselves in the audit trail, once per team they are in.
			actor := auth.WithPrincipal(ctx, &auth.Principal{Type: auth.TypeUser, UserID: u.ID, Label: u.Email})
			for _, team := range teams {
				if err := audit.Record(actor, tx, audit.Entry{
					Action: "user.password_reset", ResourceType: "user", ResourceID: u.ID.String(), WorkspaceID: team,
					Metadata: map[string]any{"email": u.Email, "via": "reset_link", "tokensRevoked": revoked},
				}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if err := auth.RevokeUserSessions(sessions, user.ID); err != nil {
			slog.ErrorContext(ctx, "revoke sessions after password reset", "user_id", user.ID.String(), "error", err)
			return nil, fmt.Errorf("revoke sessions: %w", err)
		}
		kept, err := svc.LiveTokens(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		out := &passwordResetConfirmOutput{}
		out.Body.RevokedTokens = revoked
		out.Body.KeptTokens = api.LiveTokenBodies(kept)
		return out, nil
	})
}
