package gitproviders

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

func principalUser(ctx context.Context) (uuid.UUID, bool) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || p.UserID == uuid.Nil {
		return uuid.Nil, false
	}
	return p.UserID, true
}

// normalizeBaseURL validates a provider web URL and strips trailing slashes.
func normalizeBaseURL(raw string) (string, error) {
	raw = trimBase(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", apierr.Unprocessable("validation_failed", "baseUrl must be an http(s) URL like https://git.example.com")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

func (s *Service) uniqueName(ctx context.Context, ws uuid.UUID, base string) (string, error) {
	rows, err := s.q.ListGitConnections(ctx, ws)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, r := range rows {
		taken[strings.ToLower(r.Name)] = true
	}
	name := base
	for i := 2; taken[strings.ToLower(name)]; i++ {
		name = base + " " + strconv.Itoa(i)
	}
	return name, nil
}

// ---- views ----

// GitHubAppView describes a GitHub App connection.
type GitHubAppView struct {
	AppSlug        string `json:"appSlug"`
	AppID          int64  `json:"appId"`
	InstallationID int64  `json:"installationId"`
	// AppURL is the App's settings page on GitHub.
	AppURL string `json:"appUrl"`
	// InstallURL is where the App is (re)installed on more repositories.
	InstallURL string `json:"installUrl"`
}

// OAuthView describes an OAuth connection.
type OAuthView struct {
	ClientID    string `json:"clientId"`
	RedirectURI string `json:"redirectUri" doc:"Register this as the OAuth application's redirect URI"`
	Authorized  bool   `json:"authorized"`
}

// ConnectionView is the API representation of a git connection. No credential
// or secret is ever part of it.
type ConnectionView struct {
	ID            uuid.UUID `json:"id"`
	Provider      Provider  `json:"provider" enum:"github,gitlab,gitea,forgejo,bitbucket"`
	Name          string    `json:"name"`
	BaseURL       string    `json:"baseUrl"`
	AuthType      string    `json:"authType" enum:"github_app,oauth,token,webhook_only"`
	Status        string    `json:"status" enum:"pending,active,error"`
	StatusMessage string    `json:"statusMessage"`
	AccountLogin  string    `json:"accountLogin"`
	// WebhookURL is where the provider delivers events (empty until
	// NEBULACTRL_PUBLIC_URL is set).
	WebhookURL string `json:"webhookUrl"`
	// ManualWebhook is true when the user must create the webhook by hand
	// (Bitbucket); the secret was shown when the connection was created.
	ManualWebhook bool `json:"manualWebhook"`
	// CanListRepos is true when the repository picker works (an API integration
	// with usable credentials).
	CanListRepos     bool           `json:"canListRepos"`
	SupportsPreviews bool           `json:"supportsPreviews"`
	GitHub           *GitHubAppView `json:"github"`
	OAuth            *OAuthView     `json:"oauth"`
	ServiceCount     int            `json:"serviceCount"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
}

func (s *Service) connectionView(ctx context.Context, c db.GitConnection) (ConnectionView, error) {
	n, err := s.q.CountServiceGitForConnection(ctx, c.ID)
	if err != nil {
		return ConnectionView{}, fmt.Errorf("count linked services: %w", err)
	}
	drv := driverFor(Provider(c.Provider))
	v := ConnectionView{
		ID: c.ID, Provider: Provider(c.Provider), Name: c.Name, BaseURL: c.BaseUrl, AuthType: c.AuthType, Status: c.Status,
		StatusMessage: c.StatusMessage, AccountLogin: c.AccountLogin, ManualWebhook: c.Provider == string(Bitbucket),
		SupportsPreviews: c.Provider != string(Bitbucket), ServiceCount: int(n), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if s.publicURL() != "" {
		v.WebhookURL = s.hookURL(c.Provider, c.ID)
	}
	m := decodeMeta(c.Meta)
	switch c.AuthType {
	case AuthGitHubApp:
		g := &GitHubAppView{AppSlug: m.AppSlug, AppID: m.AppID, InstallationID: m.InstallationID}
		if m.AppSlug != "" {
			g.AppURL = firstNonEmpty(m.HTMLURL, trimBase(c.BaseUrl)+"/apps/"+m.AppSlug)
			g.InstallURL = trimBase(c.BaseUrl) + "/apps/" + m.AppSlug + "/installations/new"
		}
		v.GitHub = g
	case AuthOAuth:
		cr, err := s.creds(c)
		if err != nil {
			return ConnectionView{}, err
		}
		v.OAuth = &OAuthView{ClientID: m.ClientID, RedirectURI: s.redirectURI(), Authorized: cr.AccessToken != ""}
	}
	v.CanListRepos = c.Status == StatusActive && drv != nil && drv.hasAPI() && c.AuthType != AuthWebhookOnly
	return v, nil
}

// ListConnections returns the workspace's git connections.
func (s *Service) ListConnections(ctx context.Context) ([]ConnectionView, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return nil, err
	}
	rows, err := s.q.ListGitConnections(ctx, policy.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list git connections: %w", err)
	}
	out := make([]ConnectionView, 0, len(rows))
	for _, r := range rows {
		v, err := s.connectionView(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetConnection returns one connection.
func (s *Service) GetConnection(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	c, err := s.loadConnection(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return ConnectionView{}, err
	}
	return s.connectionView(ctx, c)
}

// ---- create / update / delete ----

// CreateConnectionInput connects a GitLab, Gitea, Forgejo or Bitbucket account.
type CreateConnectionInput struct {
	Provider Provider
	Name     string
	// BaseURL is the instance's web URL. Required for Gitea and Forgejo (they
	// are self-hosted); defaults to https://gitlab.com and https://bitbucket.org.
	BaseURL string
	// AuthType is token (an access token) or oauth (an OAuth application). A
	// Bitbucket connection is webhook_only, or token when Token is given (to
	// clone private repositories).
	AuthType string
	Token    string
	// ClientID and ClientSecret are the OAuth application's credentials.
	ClientID     string
	ClientSecret string
}

// CreateConnectionResult is the response of a connection create.
type CreateConnectionResult struct {
	Connection ConnectionView `json:"connection"`
	// WebhookSecret signs the connection's webhooks. It is returned once, here;
	// only Bitbucket needs it (the webhook is created by hand there).
	WebhookSecret string `json:"webhookSecret"`
	// AuthorizeURL is where the user authorizes an OAuth connection.
	AuthorizeURL string `json:"authorizeUrl"`
}

func defaultBaseURL(p Provider) string {
	switch p {
	case GitLab:
		return "https://gitlab.com"
	case Bitbucket:
		return "https://bitbucket.org"
	}
	return ""
}

// CreateConnection connects a non-GitHub provider.
func (s *Service) CreateConnection(ctx context.Context, in CreateConnectionInput) (CreateConnectionResult, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return CreateConnectionResult{}, err
	}
	p := in.Provider
	switch {
	case !p.Valid():
		return CreateConnectionResult{}, apierr.Unprocessable("validation_failed", "unknown provider")
	case p == GitHub:
		return CreateConnectionResult{}, apierr.Unprocessable("use_github_app", "GitHub connects through a GitHub App: use the GitHub App flow (POST /git/github/manifest)")
	}
	base := strings.TrimSpace(in.BaseURL)
	if base == "" {
		base = defaultBaseURL(p)
	}
	if base == "" {
		return CreateConnectionResult{}, apierr.Unprocessable("validation_failed", "baseUrl is required: "+string(p)+" is self-hosted, so NebulaCtrl needs your instance's URL")
	}
	base, err := normalizeBaseURL(base)
	if err != nil {
		return CreateConnectionResult{}, err
	}
	authType := in.AuthType
	if p == Bitbucket {
		authType = AuthWebhookOnly
		if in.Token != "" {
			authType = AuthToken
		}
	}
	if authType != AuthToken && authType != AuthOAuth && authType != AuthWebhookOnly {
		return CreateConnectionResult{}, apierr.Unprocessable("validation_failed", "authType must be token or oauth")
	}
	if authType == AuthWebhookOnly && p != Bitbucket {
		return CreateConnectionResult{}, apierr.Unprocessable("validation_failed", "authType must be token or oauth")
	}

	var cr credentials
	account, status, statusMsg := "", StatusActive, ""
	m := meta{}
	var authorizeState string
	switch authType {
	case AuthToken:
		tok := strings.TrimSpace(in.Token)
		if tok == "" {
			return CreateConnectionResult{}, apierr.Unprocessable("validation_failed", "token is required")
		}
		cr.Token = tok
		if p != Bitbucket {
			if account, err = s.verifyToken(ctx, p, base, tok, AuthToken); err != nil {
				return CreateConnectionResult{}, apierr.Unprocessable("credentials_invalid", "The provider rejected the token: "+err.Error())
			}
		}
	case AuthOAuth:
		if err := s.requirePublicURL(); err != nil {
			return CreateConnectionResult{}, err
		}
		if strings.TrimSpace(in.ClientID) == "" || strings.TrimSpace(in.ClientSecret) == "" {
			return CreateConnectionResult{}, apierr.Unprocessable("validation_failed", "clientId and clientSecret of the OAuth application are required")
		}
		cr.ClientID, cr.ClientSecret = strings.TrimSpace(in.ClientID), strings.TrimSpace(in.ClientSecret)
		m.ClientID = cr.ClientID
		status, statusMsg = StatusPending, "Waiting for you to authorize NebulaCtrl"
		authorizeState = randomToken(24)
	}
	ws := policy.WorkspaceID(ctx)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = string(p) + " (" + hostOf(base) + ")"
	}
	if n := len(name); n > 80 {
		return CreateConnectionResult{}, apierr.Unprocessable("validation_failed", "name must be at most 80 characters")
	}
	connID := uuid.New()
	secret := randomToken(32)
	secretEnc, err := s.box.SealString(secretbox.GitConnectionsWebhookSecret, connID.String(), secret)
	if err != nil {
		return CreateConnectionResult{}, err
	}
	var credsEnc []byte
	if authType != AuthWebhookOnly {
		if credsEnc, err = s.sealCreds(connID, cr); err != nil {
			return CreateConnectionResult{}, err
		}
	}
	var conn db.GitConnection
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var uid *uuid.UUID
		if u, ok := principalUser(ctx); ok {
			uid = &u
		}
		var err error
		conn, err = q.InsertGitConnection(ctx, db.InsertGitConnectionParams{
			ID: connID, WorkspaceID: ws, Provider: string(p), Name: name, BaseUrl: base, AuthType: authType, Status: status, StatusMessage: statusMsg,
			AccountLogin: account, CredentialsEnc: credsEnc, WebhookSecretEnc: secretEnc, Meta: encodeMeta(m), CreatedBy: uid,
		})
		if err != nil {
			return err
		}
		if authorizeState != "" {
			exp := s.now().Add(manifestStateTTL)
			if err := q.SetGitConnectionPending(ctx, db.SetGitConnectionPendingParams{ID: conn.ID, PendingStateHash: new(hashState(authorizeState)), PendingExpiresAt: &exp}); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "git.connection.create", ResourceType: "git_connection", ResourceID: conn.ID.String(),
			Metadata: map[string]any{"provider": string(p), "name": name, "authType": authType, "baseUrl": base},
		})
	})
	if services.IsUniqueViolation(err) {
		return CreateConnectionResult{}, apierr.Conflict("name_conflict", "a git connection with that name already exists")
	}
	if err != nil {
		return CreateConnectionResult{}, fmt.Errorf("create git connection: %w", err)
	}
	v, err := s.connectionView(ctx, conn)
	if err != nil {
		return CreateConnectionResult{}, err
	}
	res := CreateConnectionResult{Connection: v, WebhookSecret: secret}
	if authorizeState != "" {
		res.AuthorizeURL = s.oauthConfig(conn, cr).AuthCodeURL(authorizeState)
	}
	publish(s.bus, EventConnectionUpdated, conn.WorkspaceID, conn.ID)
	return res, nil
}

// verifyToken calls the provider with a token and returns the account's login.
func (s *Service) verifyToken(ctx context.Context, p Provider, base, token, authType string) (string, error) {
	drv := driverFor(p)
	if drv == nil || !drv.hasAPI() {
		return "", errNoAPI
	}
	cl := &apiClient{hc: s.hc, base: drv.apiBase(base), authz: drv.authHeader(token, authType), secrets: []string{token}}
	if p == GitHub {
		ghClient(cl)
	}
	vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return drv.currentUser(vctx, cl)
}

// UpdateConnectionInput changes a connection. Nil fields stay as they are.
type UpdateConnectionInput struct {
	Name *string
	// Token replaces the access token of a token connection (validated first).
	Token *string
	// ClientSecret replaces the OAuth application's client secret.
	ClientSecret *string
}

// UpdateConnection renames a connection or replaces its credentials.
func (s *Service) UpdateConnection(ctx context.Context, id uuid.UUID, in UpdateConnectionInput) (ConnectionView, error) {
	c, err := s.loadConnection(ctx, policy.ScopeAdmin, id)
	if err != nil {
		return ConnectionView{}, err
	}
	name, credsEnc, account, status, statusMsg := c.Name, c.CredentialsEnc, c.AccountLogin, c.Status, c.StatusMessage
	var changed []string
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 80 {
			return ConnectionView{}, apierr.Unprocessable("validation_failed", "name must be 1-80 characters")
		}
		if n != c.Name {
			name = n
			changed = append(changed, "name")
		}
	}
	if in.Token != nil {
		if c.AuthType != AuthToken && !(c.Provider == string(Bitbucket) && c.AuthType == AuthWebhookOnly) {
			return ConnectionView{}, apierr.Unprocessable("not_applicable", "only token connections have an access token")
		}
		tok := strings.TrimSpace(*in.Token)
		if tok == "" {
			return ConnectionView{}, apierr.Unprocessable("validation_failed", "token must not be empty")
		}
		if c.Provider != string(Bitbucket) {
			if account, err = s.verifyToken(ctx, Provider(c.Provider), c.BaseUrl, tok, AuthToken); err != nil {
				return ConnectionView{}, apierr.Unprocessable("credentials_invalid", "The provider rejected the token: "+err.Error())
			}
		}
		if credsEnc, err = s.sealCreds(id, credentials{Token: tok}); err != nil {
			return ConnectionView{}, err
		}
		status, statusMsg = StatusActive, ""
		changed = append(changed, "token")
	}
	if in.ClientSecret != nil {
		if c.AuthType != AuthOAuth {
			return ConnectionView{}, apierr.Unprocessable("not_applicable", "only OAuth connections have a client secret")
		}
		cr, err := s.creds(c)
		if err != nil {
			return ConnectionView{}, err
		}
		cr.ClientSecret = strings.TrimSpace(*in.ClientSecret)
		if cr.ClientSecret == "" {
			return ConnectionView{}, apierr.Unprocessable("validation_failed", "clientSecret must not be empty")
		}
		if credsEnc, err = s.sealCreds(id, cr); err != nil {
			return ConnectionView{}, err
		}
		changed = append(changed, "clientSecret")
	}
	if len(changed) == 0 {
		return s.connectionView(ctx, c)
	}
	authType := c.AuthType
	if in.Token != nil && authType == AuthWebhookOnly {
		authType = AuthToken
	}
	var row db.GitConnection
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		if row, err = q.UpdateGitConnection(ctx, db.UpdateGitConnectionParams{
			ID: id, Name: name, BaseUrl: c.BaseUrl, Status: status, StatusMessage: statusMsg, AccountLogin: account, CredentialsEnc: credsEnc, Meta: c.Meta,
		}); err != nil {
			return err
		}
		if authType != c.AuthType {
			if err := q.SetGitConnectionAuthType(ctx, db.SetGitConnectionAuthTypeParams{ID: id, AuthType: authType}); err != nil {
				return err
			}
			row.AuthType = authType
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "git.connection.update", ResourceType: "git_connection", ResourceID: id.String(),
			Metadata: map[string]any{"fields": changed}, // field names only, never a secret
		})
	})
	if services.IsUniqueViolation(err) {
		return ConnectionView{}, apierr.Conflict("name_conflict", "a git connection with that name already exists")
	}
	if err != nil {
		return ConnectionView{}, fmt.Errorf("update git connection: %w", err)
	}
	publish(s.bus, EventConnectionUpdated, c.WorkspaceID, id)
	return s.connectionView(ctx, row)
}

// TestConnection re-verifies the stored credentials against the provider and
// records the outcome on the connection.
func (s *Service) TestConnection(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	c, err := s.loadConnection(ctx, policy.ScopeAdmin, id)
	if err != nil {
		return ConnectionView{}, err
	}
	status, msg, account := StatusActive, "", c.AccountLogin
	switch {
	case c.AuthType == AuthWebhookOnly || (c.Provider == string(Bitbucket)):
		status, msg = StatusActive, ""
	case c.Status == StatusPending && c.AuthType == AuthOAuth:
		status, msg = StatusPending, c.StatusMessage
	default:
		drv, cl, aerr := s.api(ctx, c)
		if aerr == nil {
			tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if c.AuthType == AuthGitHubApp {
				_, _, aerr = drv.listRepos(tctx, cl, "", 1)
			} else {
				var login string
				if login, aerr = drv.currentUser(tctx, cl); aerr == nil && login != "" {
					account = login
				}
			}
			cancel()
		}
		if aerr != nil {
			status, msg = StatusError, aerr.Error()
		}
	}
	if err := s.q.SetGitConnectionStatus(ctx, db.SetGitConnectionStatusParams{ID: id, Status: status, StatusMessage: msg}); err != nil {
		return ConnectionView{}, err
	}
	c.Status, c.StatusMessage = status, msg
	if account != c.AccountLogin {
		if _, err := s.q.UpdateGitConnection(ctx, db.UpdateGitConnectionParams{
			ID: id, Name: c.Name, BaseUrl: c.BaseUrl, Status: status, StatusMessage: msg, AccountLogin: account, CredentialsEnc: c.CredentialsEnc, Meta: c.Meta,
		}); err != nil {
			return ConnectionView{}, err
		}
		c.AccountLogin = account
	}
	publish(s.bus, EventConnectionUpdated, c.WorkspaceID, id)
	return s.connectionView(ctx, c)
}

// DeleteConnection removes a connection. A connection with linked services is
// refused unless force is set, which unlinks them (their repository stays in
// their config, so they keep building with their own token, if any). Webhooks
// NebulaCtrl registered are removed on a best-effort basis. A GitHub App stays
// installed at GitHub.
func (s *Service) DeleteConnection(ctx context.Context, id uuid.UUID, force bool) error {
	c, err := s.loadConnection(ctx, policy.ScopeAdmin, id)
	if err != nil {
		return err
	}
	n, err := s.q.CountServiceGitForConnection(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 && !force {
		return apierr.Conflict("connection_in_use", fmt.Sprintf("%d service(s) use this connection; unlink them or confirm the removal", n)).
			WithDetails(map[string]any{"services": n})
	}
	s.removeAllWebhooks(ctx, c)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).DeleteGitConnection(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "git.connection.delete", ResourceType: "git_connection", ResourceID: id.String(),
			Metadata: map[string]any{"provider": c.Provider, "name": c.Name, "unlinkedServices": n},
		})
	})
	if err != nil {
		return fmt.Errorf("delete git connection: %w", err)
	}
	s.forgetInstallationToken(id)
	publish(s.bus, EventConnectionUpdated, c.WorkspaceID, id)
	return nil
}

// RotateWebhookSecret issues a new webhook secret for a Bitbucket connection
// (whose webhook is created by hand) and returns it once.
func (s *Service) RotateWebhookSecret(ctx context.Context, id uuid.UUID) (string, error) {
	c, err := s.loadConnection(ctx, policy.ScopeAdmin, id)
	if err != nil {
		return "", err
	}
	if c.Provider != string(Bitbucket) {
		return "", apierr.Unprocessable("not_applicable", "webhook secrets of this provider are managed automatically")
	}
	secret := randomToken(32)
	enc, err := s.box.SealString(secretbox.GitConnectionsWebhookSecret, id.String(), secret)
	if err != nil {
		return "", err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).SetGitConnectionWebhookSecret(ctx, db.SetGitConnectionWebhookSecretParams{ID: id, WebhookSecretEnc: enc}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "git.connection.webhook_secret.rotate", ResourceType: "git_connection", ResourceID: id.String(),
			Metadata: map[string]any{"provider": c.Provider},
		})
	})
	if err != nil {
		return "", fmt.Errorf("rotate webhook secret: %w", err)
	}
	return secret, nil
}

// ---- delivery log ----

// WebhookDeliveryView is one webhook delivery of the log.
type WebhookDeliveryView struct {
	ID           int64             `json:"id"`
	DeliveryID   string            `json:"deliveryId"`
	Event        string            `json:"event"`
	Outcome      string            `json:"outcome" enum:"processed,ignored,rejected,replayed,error"`
	HTTPStatus   int               `json:"httpStatus"`
	Summary      string            `json:"summary"`
	Repo         string            `json:"repo"`
	Branch       string            `json:"branch"`
	CommitSHA    string            `json:"commitSha"`
	Headers      map[string]string `json:"headers" doc:"Request headers; signatures and tokens are redacted"`
	PayloadBytes int               `json:"payloadBytes"`
	Deployments  []uuid.UUID       `json:"deployments" doc:"Deployments the delivery started"`
	ReceivedAt   time.Time         `json:"receivedAt"`
}

// ListDeliveries returns a connection's delivery log, newest first.
func (s *Service) ListDeliveries(ctx context.Context, id uuid.UUID, cursor string, limit int) ([]WebhookDeliveryView, string, error) {
	if _, err := s.loadConnection(ctx, policy.ScopeAdmin, id); err != nil {
		return nil, "", err
	}
	params := db.ListGitDeliveriesParams{ConnectionID: id, RowLimit: int32(limit + 1)}
	if cursor != "" {
		before, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || before <= 0 {
			return nil, "", apierr.BadRequest("invalid_cursor", "invalid cursor")
		}
		params.BeforeID = &before
	}
	rows, err := s.q.ListGitDeliveries(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("list git deliveries: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	out := make([]WebhookDeliveryView, 0, len(rows))
	for _, r := range rows {
		out = append(out, webhookDeliveryView(r))
	}
	return out, next, nil
}

func webhookDeliveryView(r db.GitDelivery) WebhookDeliveryView {
	hdr := map[string]string{}
	_ = jsonUnmarshal(r.Headers, &hdr)
	deps := r.Deployments
	if deps == nil {
		deps = []uuid.UUID{}
	}
	return WebhookDeliveryView{
		ID: r.ID, DeliveryID: r.DeliveryID, Event: r.Event, Outcome: r.Outcome, HTTPStatus: int(r.HttpStatus), Summary: r.Summary,
		Repo: r.RepoFullName, Branch: r.Branch, CommitSHA: r.CommitSha, Headers: hdr, PayloadBytes: int(r.PayloadBytes),
		Deployments: deps, ReceivedAt: r.ReceivedAt,
	}
}
