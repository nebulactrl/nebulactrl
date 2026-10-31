package gitproviders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// OAuth applications (GitLab, Gitea, Forgejo): the user registers an OAuth
// application at their instance with the redirect URI NebulaCtrl shows, then
// pastes its client id and secret. The authorization-code flow yields an access
// token (which expires and is refreshed with the refresh token) acting as the
// authorizing user.
//
// GitLab: https://docs.gitlab.com/api/oauth2/
// Gitea: https://docs.gitea.com/development/oauth2-provider
// Forgejo: https://forgejo.org/docs/latest/user/oauth2-provider/

// oauthRefreshSlack refreshes a token this long before it expires.
const oauthRefreshSlack = time.Minute

func (s *Service) oauthConfig(c db.GitConnection, cr credentials) *oauth2.Config {
	base := trimBase(c.BaseUrl)
	cfg := &oauth2.Config{ClientID: cr.ClientID, ClientSecret: cr.ClientSecret, RedirectURL: s.redirectURI()}
	switch Provider(c.Provider) {
	case GitLab:
		cfg.Endpoint = oauth2.Endpoint{AuthURL: base + "/oauth/authorize", TokenURL: base + "/oauth/token", AuthStyle: oauth2.AuthStyleInParams}
		// api: commit statuses, webhooks and merge request notes; read_repository: clones.
		cfg.Scopes = []string{"api", "read_repository"}
	default: // Gitea and Forgejo: the token carries the authorizing user's access
		cfg.Endpoint = oauth2.Endpoint{AuthURL: base + "/login/oauth/authorize", TokenURL: base + "/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams}
	}
	return cfg
}

func (s *Service) oauthCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, s.hc)
}

func tokenCreds(cr credentials, tok *oauth2.Token) credentials {
	cr.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		cr.RefreshToken = tok.RefreshToken
	}
	cr.ExpiresAt = nil
	if !tok.Expiry.IsZero() {
		e := tok.Expiry
		cr.ExpiresAt = &e
	}
	return cr
}

// oauthToken returns the connection's access token, refreshing it when it is
// about to expire. Refresh tokens rotate, so refreshes are serialized.
func (s *Service) oauthToken(ctx context.Context, c db.GitConnection) (string, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	fresh, err := s.loadConnectionInternal(ctx, c.ID)
	if err != nil {
		return "", err
	}
	cr, err := s.creds(fresh)
	if err != nil {
		return "", err
	}
	if cr.AccessToken == "" {
		return "", errors.New("this connection is not authorized yet: finish the OAuth authorization")
	}
	if cr.ExpiresAt == nil || s.now().Add(oauthRefreshSlack).Before(*cr.ExpiresAt) {
		return cr.AccessToken, nil
	}
	if cr.RefreshToken == "" {
		return "", errors.New("the OAuth authorization expired: reconnect the provider")
	}
	cfg := s.oauthConfig(fresh, cr)
	// The token is stale by our margin: present it as expired so the library refreshes it.
	tok, err := cfg.TokenSource(s.oauthCtx(ctx), &oauth2.Token{AccessToken: cr.AccessToken, RefreshToken: cr.RefreshToken, Expiry: time.Now().Add(-time.Hour)}).Token()
	if err != nil {
		msg := "the OAuth authorization could not be refreshed: reconnect the provider"
		_ = s.q.SetGitConnectionStatus(ctx, db.SetGitConnectionStatusParams{ID: c.ID, Status: StatusError, StatusMessage: msg})
		publish(s.bus, EventConnectionUpdated, c.WorkspaceID, c.ID)
		return "", errors.New(msg)
	}
	cr = tokenCreds(cr, tok)
	enc, err := s.sealCreds(c.ID, cr)
	if err != nil {
		return "", err
	}
	if err := s.q.SetGitConnectionCredentials(ctx, db.SetGitConnectionCredentialsParams{ID: c.ID, CredentialsEnc: enc}); err != nil {
		return "", fmt.Errorf("store refreshed token: %w", err)
	}
	return cr.AccessToken, nil
}

// StartOAuth returns the provider URL where the user authorizes the
// connection's OAuth application (also used to reconnect).
func (s *Service) StartOAuth(ctx context.Context, id uuid.UUID) (string, error) {
	c, err := s.loadConnection(ctx, policy.ScopeAdmin, id)
	if err != nil {
		return "", err
	}
	if c.AuthType != AuthOAuth {
		return "", apierr.Unprocessable("not_applicable", "this connection does not use OAuth")
	}
	if err := s.requirePublicURL(); err != nil {
		return "", err
	}
	cr, err := s.creds(c)
	if err != nil {
		return "", err
	}
	state := randomToken(24)
	exp := s.now().Add(manifestStateTTL)
	if err := s.q.SetGitConnectionPending(ctx, db.SetGitConnectionPendingParams{ID: id, PendingStateHash: new(hashState(state)), PendingExpiresAt: &exp}); err != nil {
		return "", err
	}
	return s.oauthConfig(c, cr).AuthCodeURL(state), nil
}

// CompleteOAuth finishes the authorization-code flow: it exchanges the code,
// stores the tokens and activates the connection. It returns the connection id.
func (s *Service) CompleteOAuth(ctx context.Context, code, state string) (uuid.UUID, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return uuid.Nil, err
	}
	if code == "" || state == "" {
		return uuid.Nil, apierr.BadRequest("invalid_request", "code and state are required")
	}
	c, err := s.connectionByState(ctx, state)
	if err != nil {
		return uuid.Nil, err
	}
	if c.AuthType != AuthOAuth {
		return c.ID, apierr.BadRequest("invalid_state", "this state does not belong to an OAuth flow")
	}
	cr, err := s.creds(c)
	if err != nil {
		return c.ID, err
	}
	tok, err := s.oauthConfig(c, cr).Exchange(s.oauthCtx(ctx), code)
	if err != nil {
		msg := "The provider did not accept the authorization" + oauthFailure(err)
		_ = s.q.SetGitConnectionStatus(ctx, db.SetGitConnectionStatusParams{ID: c.ID, Status: StatusError, StatusMessage: msg})
		publish(s.bus, EventConnectionUpdated, c.WorkspaceID, c.ID)
		return c.ID, apierr.Unprocessable("oauth_exchange_failed", msg)
	}
	cr = tokenCreds(cr, tok)
	account, verr := s.verifyToken(ctx, Provider(c.Provider), c.BaseUrl, cr.AccessToken, AuthOAuth)
	if verr != nil {
		msg := "The authorization was granted but the provider rejected the token: " + verr.Error()
		_ = s.q.SetGitConnectionStatus(ctx, db.SetGitConnectionStatusParams{ID: c.ID, Status: StatusError, StatusMessage: msg})
		publish(s.bus, EventConnectionUpdated, c.WorkspaceID, c.ID)
		return c.ID, apierr.Unprocessable("credentials_invalid", msg)
	}
	enc, err := s.sealCreds(c.ID, cr)
	if err != nil {
		return c.ID, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.UpdateGitConnection(ctx, db.UpdateGitConnectionParams{
			ID: c.ID, Name: c.Name, BaseUrl: c.BaseUrl, Status: StatusActive, StatusMessage: "", AccountLogin: account, CredentialsEnc: enc, Meta: c.Meta,
		}); err != nil {
			return err
		}
		if err := q.ClearGitConnectionPending(ctx, c.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "git.connection.activate", ResourceType: "git_connection", ResourceID: c.ID.String(),
			Metadata: map[string]any{"provider": c.Provider, "account": account},
		})
	})
	if err != nil {
		return c.ID, fmt.Errorf("activate oauth connection: %w", err)
	}
	publish(s.bus, EventConnectionUpdated, c.WorkspaceID, c.ID)
	return c.ID, nil
}

// oauthFailure describes a failed token request without the remote's answer
// (the library copies the whole response body into its error): the HTTP status
// for a provider answer, the transport error otherwise.
func oauthFailure(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.Response != nil {
		return fmt.Sprintf(" (HTTP %d)", re.Response.StatusCode)
	}
	return ": " + err.Error()
}
