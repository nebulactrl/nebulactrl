package gitproviders

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// GitHub App credentials. The user creates the App with the manifest flow
// (https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest):
//
//  1. NebulaCtrl creates a pending connection and sends the browser to
//     /api/v1/git/github/manifest/form?state=…, a page that POSTs the manifest
//     to GitHub (the dashboard's own CSP only allows forms to its origin); the
//     user confirms on github.com.
//  2. GitHub redirects to /api/v1/git/github/manifest/callback?code=…&state=…;
//     the code is exchanged for the App's id, private key and webhook secret.
//  3. The user installs the App on repositories; GitHub redirects to
//     /api/v1/git/github/installed?installation_id=…, which finishes the
//     connection.
//
// A connection whose App was never created restarts at step 1 with a new
// state (RestartGitHubManifest). Clones and API calls use installation access
// tokens (valid one hour), minted from a short-lived App JWT.

const (
	manifestStateTTL = time.Hour
	// installationTokenSlack refreshes cached installation tokens this early.
	installationTokenSlack = 5 * time.Minute
)

// githubAppJWT signs the RS256 JWT that authenticates as the App itself.
func githubAppJWT(cr credentials, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(cr.PEM))
	if block == nil {
		return "", errors.New("the GitHub App private key is not valid PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k8, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("the GitHub App private key is not an RSA key")
		}
		key = rk
	} else {
		return "", errors.New("the GitHub App private key cannot be parsed")
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(), // tolerate clock drift
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": cr.AppID,
	})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// appClient is an API client authenticated as the GitHub App (JWT).
func (s *Service) appClient(c db.GitConnection) (*apiClient, error) {
	cr, err := s.creds(c)
	if err != nil {
		return nil, err
	}
	jwt, err := githubAppJWT(cr, s.now())
	if err != nil {
		return nil, err
	}
	return ghClient(&apiClient{hc: s.hc, base: githubDriver{}.apiBase(c.BaseUrl), authz: "Bearer " + jwt, secrets: []string{jwt}}), nil
}

// mintInstallationToken creates an installation access token. With repoName it
// is restricted to that repository and to read access to its contents (what a
// clone needs); without, it carries every permission the installation granted.
func (s *Service) mintInstallationToken(ctx context.Context, c db.GitConnection, repoName string) (cachedToken, error) {
	m := decodeMeta(c.Meta)
	if m.InstallationID == 0 {
		return cachedToken{}, errors.New("the GitHub App is not installed yet: install it on your account or organization first")
	}
	cl, err := s.appClient(c)
	if err != nil {
		return cachedToken{}, err
	}
	var body any
	if repoName != "" {
		body = map[string]any{"repositories": []string{repoName}, "permissions": map[string]string{"contents": "read"}}
	}
	var out struct {
		Token       string            `json:"token"`
		ExpiresAt   time.Time         `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := cl.do(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(m.InstallationID, 10)+"/access_tokens", nil, body, &out); err != nil {
		return cachedToken{}, fmt.Errorf("mint GitHub installation token: %w", err)
	}
	if out.Token == "" {
		return cachedToken{}, errors.New("GitHub returned no installation token")
	}
	return cachedToken{token: out.Token, expires: out.ExpiresAt, permissions: out.Permissions}, nil
}

// installationToken returns a cached installation token for API calls.
func (s *Service) installationToken(ctx context.Context, c db.GitConnection) (string, error) {
	t, err := s.cachedInstallationToken(ctx, c)
	return t.token, err
}

func (s *Service) cachedInstallationToken(ctx context.Context, c db.GitConnection) (cachedToken, error) {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	if t, ok := s.instTokens[c.ID]; ok && s.now().Add(installationTokenSlack).Before(t.expires) {
		return t, nil
	}
	t, err := s.mintInstallationToken(ctx, c, "")
	if err != nil {
		return cachedToken{}, err
	}
	s.instTokens[c.ID] = t
	return t, nil
}

// ciPermissions are the installation permissions wait-for-CI reads checks with.
var ciPermissions = []struct{ key, label string }{{"checks", "Checks"}, {"statuses", "Commit statuses"}}

// ciReadWarning says why wait-for-CI cannot read the checks of the
// connection's commits: a GitHub App installed before it asked for the Checks
// permission, or one whose new permissions were never accepted. It is empty
// when the checks can be read or the permissions cannot be looked up.
func (s *Service) ciReadWarning(ctx context.Context, c db.GitConnection) string {
	if c.AuthType != AuthGitHubApp {
		return ""
	}
	t, err := s.cachedInstallationToken(ctx, c)
	if err != nil || t.permissions == nil {
		return ""
	}
	var missing []string
	for _, p := range ciPermissions {
		if t.permissions[p.key] == "" {
			missing = append(missing, "'"+p.label+": read'")
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "The GitHub App may not read this repository's CI results, so Wait for CI refuses every push: grant it " +
		strings.Join(missing, " and ") + " in the App's settings (Permissions & events), then accept the change on the installation."
}

func (s *Service) forgetInstallationToken(id uuid.UUID) {
	s.tokMu.Lock()
	delete(s.instTokens, id)
	s.tokMu.Unlock()
}

// appHookTTL is how long the App webhook address read from GitHub is trusted.
const appHookTTL = 5 * time.Minute

type cachedHook struct {
	url string
	at  time.Time
}

// appHookURL reads where GitHub delivers the App's webhooks (cached for appHookTTL).
// https://docs.github.com/en/rest/apps/webhooks#get-a-webhook-configuration-for-an-app
func (s *Service) appHookURL(ctx context.Context, c db.GitConnection) (string, error) {
	s.hookMu.Lock()
	h, ok := s.appHooks[c.ID]
	s.hookMu.Unlock()
	if ok && s.now().Sub(h.at) < appHookTTL {
		return h.url, nil
	}
	cl, err := s.appClient(c)
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := cl.do(ctx, http.MethodGet, "/app/hook/config", nil, nil, &out); err != nil {
		return "", fmt.Errorf("read the GitHub App's webhook configuration: %w", err)
	}
	s.rememberAppHook(c.ID, out.URL)
	return out.URL, nil
}

func (s *Service) rememberAppHook(id uuid.UUID, u string) {
	s.hookMu.Lock()
	s.appHooks[id] = cachedHook{url: u, at: s.now()}
	s.hookMu.Unlock()
}

// syncAppHook points the App's webhook at this instance when GitHub delivers
// elsewhere: the App was created while NEBULACTRL_PUBLIC_URL named another
// address (a port or host that changed since), and GitHub keeps the old one.
// https://docs.github.com/en/rest/apps/webhooks#update-a-webhook-configuration-for-an-app
func (s *Service) syncAppHook(ctx context.Context, c db.GitConnection) error {
	if c.AuthType != AuthGitHubApp || !appCreated(c) || s.publicURL() == "" {
		return nil
	}
	s.hookMu.Lock()
	delete(s.appHooks, c.ID)
	s.hookMu.Unlock()
	got, err := s.appHookURL(ctx, c)
	if err != nil {
		return err
	}
	want := s.hookURL(c.Provider, c.ID)
	if got == want {
		return nil
	}
	cl, err := s.appClient(c)
	if err != nil {
		return err
	}
	if err := cl.do(ctx, http.MethodPatch, "/app/hook/config", nil, map[string]string{"url": want}, nil); err != nil {
		return fmt.Errorf("point the GitHub App's webhook at %s: %w", want, err)
	}
	s.rememberAppHook(c.ID, want)
	s.log.Info("pointed the GitHub App webhook at this instance", "connection", c.Name, "from", got, "to", want)
	return nil
}

// appHookProblem says why GitHub does not deliver the App's events to this
// instance ("" when it does, or when GitHub cannot be asked right now).
func (s *Service) appHookProblem(ctx context.Context, c db.GitConnection) string {
	if c.AuthType != AuthGitHubApp || !appCreated(c) || s.publicURL() == "" {
		return ""
	}
	got, err := s.appHookURL(ctx, c)
	if err != nil {
		s.log.Warn("read GitHub App webhook", "connection", c.Name, "err", err)
		return ""
	}
	want := s.hookURL(c.Provider, c.ID)
	switch got {
	case want:
		return ""
	case "":
		return "The GitHub App has no webhook address, so pushes never arrive. Test the connection in Team settings → Integrations to set it to " + want + "."
	}
	return "GitHub delivers this App's events to " + got + ", not to " + want + ", so pushes never arrive. Test the connection in Team settings → Integrations to correct it (NebulaCtrl also corrects it within the hour)."
}

// ---- manifest flow ----

// ManifestInput starts the GitHub App creation flow.
type ManifestInput struct {
	// Owner is the organization that will own the App (empty: the signed-in user).
	Owner string
	// BaseURL is the web URL of a GitHub Enterprise Server (empty: github.com).
	BaseURL string
	// Name is the connection name (default "GitHub").
	Name string
}

// ManifestStart is what the browser needs to send the manifest to GitHub.
type ManifestStart struct {
	ConnectionID uuid.UUID `json:"connectionId"`
	// FormURL is the NebulaCtrl page that posts the manifest to GitHub: open it
	// in the browser.
	FormURL string `json:"formUrl"`
	// PostURL is the GitHub URL the form posts to.
	PostURL string `json:"postUrl"`
	// Manifest is the JSON sent as the form field "manifest".
	Manifest string `json:"manifest"`
	State    string `json:"state"`
}

func validOwner(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// StartGitHubManifest creates a pending GitHub connection and returns the
// manifest the browser posts to GitHub.
func (s *Service) StartGitHubManifest(ctx context.Context, in ManifestInput) (ManifestStart, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return ManifestStart{}, err
	}
	if err := s.requirePublicURL(); err != nil {
		return ManifestStart{}, err
	}
	base := "https://github.com"
	if strings.TrimSpace(in.BaseURL) != "" {
		var err error
		if base, err = normalizeBaseURL(in.BaseURL); err != nil {
			return ManifestStart{}, err
		}
	}
	owner := strings.TrimSpace(in.Owner)
	if owner != "" && !validOwner(owner) {
		return ManifestStart{}, apierr.Unprocessable("validation_failed", "owner is not a valid GitHub organization name")
	}
	ws := policy.WorkspaceID(ctx)
	name, err := s.uniqueName(ctx, ws, firstNonEmpty(strings.TrimSpace(in.Name), "GitHub"))
	if err != nil {
		return ManifestStart{}, err
	}
	connID := uuid.New()
	secret, err := s.box.SealString(secretbox.GitConnectionsWebhookSecret, connID.String(), randomToken(32)) // replaced by GitHub's secret at conversion
	if err != nil {
		return ManifestStart{}, err
	}
	state := randomToken(24)
	var conn db.GitConnection
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var uid *uuid.UUID
		if p, ok := principalUser(ctx); ok {
			uid = &p
		}
		var err error
		conn, err = q.InsertGitConnection(ctx, db.InsertGitConnectionParams{
			ID: connID, WorkspaceID: ws, Provider: string(GitHub), Name: name, BaseUrl: base, AuthType: AuthGitHubApp, Status: StatusPending,
			StatusMessage: "Waiting for the GitHub App to be created", WebhookSecretEnc: secret, Meta: encodeMeta(meta{Owner: owner}), CreatedBy: uid,
		})
		if err != nil {
			return err
		}
		exp := s.now().Add(manifestStateTTL)
		if err := q.SetGitConnectionPending(ctx, db.SetGitConnectionPendingParams{ID: conn.ID, PendingStateHash: new(hashState(state)), PendingExpiresAt: &exp}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "git.connection.create", ResourceType: "git_connection", ResourceID: conn.ID.String(),
			Metadata: map[string]any{"provider": "github", "name": name, "authType": AuthGitHubApp},
		})
	})
	if err != nil {
		return ManifestStart{}, fmt.Errorf("create github connection: %w", err)
	}
	publish(s.bus, EventConnectionUpdated, conn.WorkspaceID, conn.ID)
	return s.manifestStart(conn, state), nil
}

// RestartGitHubManifest starts the manifest flow again for a GitHub App
// connection whose App was never created (the browser did not reach GitHub,
// or the user left before confirming). The previous state stops working.
func (s *Service) RestartGitHubManifest(ctx context.Context, id uuid.UUID) (ManifestStart, error) {
	c, err := s.loadConnection(ctx, policy.ScopeAdmin, id)
	if err != nil {
		return ManifestStart{}, err
	}
	if c.AuthType != AuthGitHubApp {
		return ManifestStart{}, apierr.Unprocessable("not_applicable", "only GitHub App connections are created from a manifest")
	}
	if appCreated(c) {
		return ManifestStart{}, apierr.Unprocessable("app_exists", "the GitHub App of this connection already exists: install it instead")
	}
	if err := s.requirePublicURL(); err != nil {
		return ManifestStart{}, err
	}
	state := randomToken(24)
	exp := s.now().Add(manifestStateTTL)
	if err := s.q.SetGitConnectionPending(ctx, db.SetGitConnectionPendingParams{ID: c.ID, PendingStateHash: new(hashState(state)), PendingExpiresAt: &exp}); err != nil {
		return ManifestStart{}, err
	}
	return s.manifestStart(c, state), nil
}

// ManifestForm returns the GitHub URL and the manifest the form page of a
// pending flow posts.
func (s *Service) ManifestForm(ctx context.Context, state string) (postURL, manifest string, err error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return "", "", err
	}
	if state == "" {
		return "", "", apierr.BadRequest("invalid_request", "state is required")
	}
	c, err := s.connectionByState(ctx, state)
	if err != nil {
		return "", "", err
	}
	if c.Provider != string(GitHub) || c.AuthType != AuthGitHubApp {
		return "", "", apierr.BadRequest("invalid_state", "this state does not belong to a GitHub App flow")
	}
	if appCreated(c) {
		return "", "", apierr.BadRequest("invalid_state", "this GitHub App was already created: install it from the connection")
	}
	m := s.manifestStart(c, state)
	return m.PostURL, m.Manifest, nil
}

// appCreated reports whether GitHub already converted the connection's manifest.
func appCreated(c db.GitConnection) bool {
	return decodeMeta(c.Meta).AppID != 0 || len(c.CredentialsEnc) > 0
}

// manifestStart builds the manifest of a pending GitHub App connection and
// where it goes. The App name gets a random suffix: GitHub App names are global.
func (s *Service) manifestStart(c db.GitConnection, state string) ManifestStart {
	appName := "NebulaCtrl"
	if u, perr := url.Parse(s.publicURL()); perr == nil && u.Hostname() != "" {
		appName += " " + u.Hostname()
	}
	if len(appName) > 28 {
		appName = appName[:28]
	}
	appName += " " + randomToken(2)
	manifest := map[string]any{
		"name":            appName,
		"url":             s.publicURL(),
		"redirect_url":    s.publicURL() + "/api/v1/git/github/manifest/callback",
		"setup_url":       s.publicURL() + "/api/v1/git/github/installed",
		"setup_on_update": true,
		"public":          false,
		"hook_attributes": map[string]any{"url": s.hookURL(string(GitHub), c.ID), "active": true},
		// Read access to code, metadata and check runs (wait for CI), write access to
		// pull requests (the preview comment) and commit statuses (the deployment status).
		"default_permissions": map[string]string{"checks": "read", "contents": "read", "metadata": "read", "pull_requests": "write", "statuses": "write"},
		"default_events":      []string{"push", "pull_request"},
	}
	raw, _ := json.Marshal(manifest)
	base := trimBase(c.BaseUrl)
	post := base + "/settings/apps/new"
	if owner := decodeMeta(c.Meta).Owner; owner != "" {
		post = base + "/organizations/" + owner + "/settings/apps/new"
	}
	q := "?state=" + url.QueryEscape(state)
	return ManifestStart{
		ConnectionID: c.ID,
		FormURL:      s.publicURL() + "/api/v1/git/github/manifest/form" + q,
		PostURL:      post + q,
		Manifest:     string(raw),
		State:        state,
	}
}

// connectionByState finds the pending connection of a flow in the caller's workspace.
func (s *Service) connectionByState(ctx context.Context, state string) (db.GitConnection, error) {
	c, err := s.q.GetGitConnectionByState(ctx, new(hashState(state)))
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GitConnection{}, apierr.BadRequest("invalid_state", "this connection flow has expired or was already completed; start it again")
	}
	if err != nil {
		return db.GitConnection{}, fmt.Errorf("find connection by state: %w", err)
	}
	if c.WorkspaceID != policy.WorkspaceID(ctx) {
		return db.GitConnection{}, apierr.BadRequest("invalid_state", "this connection flow belongs to another workspace")
	}
	return c, nil
}

// CompleteManifest exchanges the manifest code for the App's credentials and
// returns the GitHub URL where the user installs the App.
func (s *Service) CompleteManifest(ctx context.Context, code, state string) (string, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return "", err
	}
	if code == "" || state == "" {
		return "", apierr.BadRequest("invalid_request", "code and state are required")
	}
	c, err := s.connectionByState(ctx, state)
	if err != nil {
		return "", err
	}
	if c.Provider != string(GitHub) || c.AuthType != AuthGitHubApp {
		return "", apierr.BadRequest("invalid_state", "this state does not belong to a GitHub App flow")
	}
	cl := ghClient(&apiClient{hc: s.hc, base: githubDriver{}.apiBase(c.BaseUrl)})
	var conv struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
		HTMLURL       string `json:"html_url"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := cl.do(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", nil, nil, &conv); err != nil {
		return "", apierr.Unprocessable("manifest_exchange_failed", "GitHub did not accept the manifest code: "+err.Error())
	}
	if conv.ID == 0 || conv.PEM == "" || conv.WebhookSecret == "" {
		return "", apierr.Unprocessable("manifest_exchange_failed", "GitHub returned an incomplete App")
	}
	credsEnc, err := s.sealCreds(c.ID, credentials{AppID: conv.ID, ClientID: conv.ClientID, ClientSecret: conv.ClientSecret, PEM: conv.PEM})
	if err != nil {
		return "", err
	}
	secretEnc, err := s.box.SealString(secretbox.GitConnectionsWebhookSecret, c.ID.String(), conv.WebhookSecret)
	if err != nil {
		return "", err
	}
	m := decodeMeta(c.Meta)
	m.AppID, m.AppSlug, m.HTMLURL, m.ClientID = conv.ID, conv.Slug, conv.HTMLURL, conv.ClientID
	m.Owner = firstNonEmpty(conv.Owner.Login, m.Owner)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.UpdateGitConnection(ctx, db.UpdateGitConnectionParams{
			ID: c.ID, Name: c.Name, BaseUrl: c.BaseUrl, Status: StatusPending, StatusMessage: "GitHub App created: install it to finish",
			AccountLogin: m.Owner, CredentialsEnc: credsEnc, Meta: encodeMeta(m),
		}); err != nil {
			return err
		}
		if err := q.SetGitConnectionWebhookSecret(ctx, db.SetGitConnectionWebhookSecretParams{ID: c.ID, WebhookSecretEnc: secretEnc}); err != nil {
			return err
		}
		exp := s.now().Add(manifestStateTTL) // the installation step reuses the state
		return q.SetGitConnectionPending(ctx, db.SetGitConnectionPendingParams{ID: c.ID, PendingStateHash: new(hashState(state)), PendingExpiresAt: &exp})
	})
	if err != nil {
		return "", fmt.Errorf("store github app: %w", err)
	}
	publish(s.bus, EventConnectionUpdated, c.WorkspaceID, c.ID)
	return trimBase(c.BaseUrl) + "/apps/" + url.PathEscape(conv.Slug) + "/installations/new?state=" + url.QueryEscape(state), nil
}

// CompleteInstallation records the installation of the App and activates the
// connection. state may be empty (some GitHub flows drop it): the workspace's
// newest GitHub App connection still waiting for an installation is used then.
// It returns the id of the connection.
func (s *Service) CompleteInstallation(ctx context.Context, state string, installationID int64, setupAction string) (uuid.UUID, string, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return uuid.Nil, "", err
	}
	var c db.GitConnection
	var err error
	if state != "" {
		c, err = s.connectionByState(ctx, state)
	} else {
		c, err = s.pendingGitHubApp(ctx)
	}
	if err != nil {
		return uuid.Nil, "", err
	}
	if setupAction == "request" {
		msg := "Installation requested: waiting for an organization owner to approve it"
		if err := s.q.SetGitConnectionStatus(ctx, db.SetGitConnectionStatusParams{ID: c.ID, Status: StatusPending, StatusMessage: msg}); err != nil {
			return uuid.Nil, "", err
		}
		publish(s.bus, EventConnectionUpdated, c.WorkspaceID, c.ID)
		return c.ID, msg, nil
	}
	if installationID == 0 {
		if installationID, err = s.discoverInstallation(ctx, c); err != nil {
			return c.ID, "", err
		}
	}
	if err := s.activateInstallation(ctx, c, installationID); err != nil {
		return c.ID, "", err
	}
	return c.ID, "", nil
}

func (s *Service) pendingGitHubApp(ctx context.Context) (db.GitConnection, error) {
	rows, err := s.q.ListGitConnections(ctx, policy.WorkspaceID(ctx))
	if err != nil {
		return db.GitConnection{}, err
	}
	var best *db.GitConnection
	for i := range rows {
		r := rows[i]
		if r.AuthType == AuthGitHubApp && decodeMeta(r.Meta).InstallationID == 0 && len(r.CredentialsEnc) > 0 {
			if best == nil || r.CreatedAt.After(best.CreatedAt) {
				best = &rows[i]
			}
		}
	}
	if best == nil {
		return db.GitConnection{}, apierr.BadRequest("invalid_state", "no GitHub App is waiting to be installed")
	}
	return *best, nil
}

// discoverInstallation finds the App's installation through the API.
func (s *Service) discoverInstallation(ctx context.Context, c db.GitConnection) (int64, error) {
	cl, err := s.appClient(c)
	if err != nil {
		return 0, err
	}
	var out []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := cl.do(ctx, http.MethodGet, "/app/installations", url.Values{"per_page": {"100"}}, nil, &out); err != nil {
		return 0, apierr.Unprocessable("installation_lookup_failed", "Could not list the App's installations: "+err.Error())
	}
	if len(out) == 0 {
		return 0, apierr.Unprocessable("not_installed", "The GitHub App is not installed anywhere yet: install it on your account or organization first.")
	}
	return out[0].ID, nil
}

// activateInstallation stores the installation, checks that it works and makes
// the connection active.
func (s *Service) activateInstallation(ctx context.Context, c db.GitConnection, installationID int64) error {
	m := decodeMeta(c.Meta)
	m.InstallationID = installationID
	c.Meta = encodeMeta(m)
	s.forgetInstallationToken(c.ID)
	status, msg := StatusActive, ""
	if _, err := s.mintInstallationToken(ctx, c, ""); err != nil {
		status, msg = StatusError, err.Error()
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.UpdateGitConnection(ctx, db.UpdateGitConnectionParams{
			ID: c.ID, Name: c.Name, BaseUrl: c.BaseUrl, Status: status, StatusMessage: msg, AccountLogin: c.AccountLogin,
			CredentialsEnc: c.CredentialsEnc, Meta: c.Meta,
		}); err != nil {
			return err
		}
		if err := q.ClearGitConnectionPending(ctx, c.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "git.connection.activate", ResourceType: "git_connection", ResourceID: c.ID.String(),
			Metadata: map[string]any{"provider": "github", "installationId": installationID, "status": status},
		})
	})
	if err != nil {
		return fmt.Errorf("activate github installation: %w", err)
	}
	publish(s.bus, EventConnectionUpdated, c.WorkspaceID, c.ID)
	if status == StatusError {
		return apierr.Unprocessable("installation_failed", "The GitHub App was installed but a token could not be created: "+msg)
	}
	return nil
}

// RefreshInstallation looks the installation up again (for installs that did
// not return to NebulaCtrl) and re-verifies the connection. It also points the
// App's webhook at this instance; a failure there is left to Test and to the
// hourly maintenance, which report and retry it.
func (s *Service) RefreshInstallation(ctx context.Context, id uuid.UUID) error {
	c, err := s.loadConnection(ctx, policy.ScopeAdmin, id)
	if err != nil {
		return err
	}
	if c.AuthType != AuthGitHubApp {
		return apierr.Unprocessable("not_applicable", "only GitHub App connections have an installation")
	}
	iid, err := s.discoverInstallation(ctx, c)
	if err != nil {
		return err
	}
	if err := s.activateInstallation(ctx, c, iid); err != nil {
		return err
	}
	if err := s.syncAppHook(ctx, c); err != nil {
		s.log.Warn("sync GitHub App webhook", "connection", c.Name, "err", err)
	}
	return nil
}
