package tailscale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// ErrNotConfigured is returned when the workspace has no Tailscale integration.
var ErrNotConfigured = errors.New("tailscale integration is not configured")

// ErrWrongProvider is returned by operations that only exist for the Tailscale
// OAuth provider (per-server egress, the Tailscale device list) when the
// workspace uses Headscale.
var ErrWrongProvider = errors.New("this operation needs the Tailscale (OAuth) provider")

// DefaultTag is the tag a new integration mints keys with.
const DefaultTag = "tag:nebula-svc"

var (
	tagRe     = regexp.MustCompile(`^tag:[a-zA-Z][a-zA-Z0-9-]{0,62}$`)
	tailnetRe = regexp.MustCompile(`^(-|[A-Za-z0-9][A-Za-z0-9.@_-]{0,127})$`)
	clientRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
)

// Service is the workspace-level Tailscale integration.
type Service struct {
	pool    *pgxpool.Pool
	q       *db.Queries
	box     *secretbox.Box
	jobs    *jobs.Manager
	log     *slog.Logger
	baseURL string
	hc      *http.Client
	now     func() time.Time
}

// NewService returns a Service (pool may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, box *secretbox.Box, jm *jobs.Manager, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, q: db.New(pool), box: box, jobs: jm, log: logger.With("component", "tailscale"), now: time.Now}
}

// SetBaseURL overrides the Tailscale API base URL (tests).
func (s *Service) SetBaseURL(u string, hc *http.Client) { s.baseURL, s.hc = u, hc }

// TailscaleLastTest is the stored result of the last connection test.
type TailscaleLastTest struct {
	At    *time.Time `json:"at"`
	OK    *bool      `json:"ok"`
	Error string     `json:"error,omitempty"`
}

// TailscaleIntegration is the API view. The secret is never part of it.
type TailscaleIntegration struct {
	Configured   bool              `json:"configured"`
	Provider     string            `json:"provider" enum:"tailscale_oauth,headscale"`
	Tailnet      string            `json:"tailnet" doc:"Tailnet name, or - for the tailnet of the OAuth client"`
	ClientID     string            `json:"clientId"`
	SecretSet    bool              `json:"secretSet"`
	DefaultTags  []string          `json:"defaultTags"`
	AllowedTags  []string          `json:"allowedTags"`
	HTTPSEnabled *bool             `json:"httpsEnabled" doc:"Whether the tailnet has HTTPS certificates enabled (known after a successful test)"`
	LastTest     TailscaleLastTest `json:"lastTest"`
	// LoginServer, HeadscaleUser and HeadscaleCredential apply to the headscale provider.
	LoginServer         string `json:"loginServer" doc:"Headscale control URL (headscale provider)"`
	HeadscaleUser       string `json:"headscaleUser" doc:"Headscale user that owns minted pre-auth keys (empty = tags-only keys)"`
	HeadscaleCredential string `json:"headscaleCredential" enum:"apikey,preauth" doc:"apikey: NebulaCtrl mints single-use pre-auth keys and deletes nodes; preauth: one reusable tagged pre-auth key for every sidecar"`
}

func view(r db.TailnetIntegration) TailscaleIntegration {
	return TailscaleIntegration{
		Configured: true, Provider: r.Provider, Tailnet: r.Tailnet, ClientID: r.ClientID, SecretSet: len(r.SecretEnc) > 0,
		DefaultTags: nonNil(r.DefaultTags), AllowedTags: nonNil(r.AllowedTags), HTTPSEnabled: r.HttpsEnabled,
		LastTest:    TailscaleLastTest{At: r.LastTestAt, OK: r.LastTestOk, Error: deref(r.LastTestError)},
		LoginServer: r.LoginServer, HeadscaleUser: r.HeadscaleUser, HeadscaleCredential: r.HeadscaleCredential,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func emptyView() TailscaleIntegration {
	return TailscaleIntegration{
		Provider: ProviderTailscaleOAuth, Tailnet: "-", DefaultTags: []string{DefaultTag}, AllowedTags: []string{DefaultTag},
		HeadscaleCredential: HeadscaleAPIKey,
	}
}

// Get returns the workspace's integration ({configured:false} when absent).
func (s *Service) Get(ctx context.Context) (TailscaleIntegration, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return TailscaleIntegration{}, err
	}
	r, err := s.q.GetTailnetIntegration(ctx, policy.WorkspaceID(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return emptyView(), nil
	}
	if err != nil {
		return TailscaleIntegration{}, fmt.Errorf("get tailnet integration: %w", err)
	}
	return view(r), nil
}

// PutInput creates or updates the integration. An empty ClientSecret keeps the
// stored secret (it is required when none is stored yet). ClientSecret is the
// OAuth client secret, the Headscale API key or the Headscale pre-auth key.
type PutInput struct {
	Provider     string
	Tailnet      string
	ClientID     string
	ClientSecret string
	DefaultTags  []string
	AllowedTags  []string
	// Headscale only.
	LoginServer         string
	HeadscaleUser       string
	HeadscaleCredential string
}

func normalizeTags(field string, tags []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if !tagRe.MatchString(t) {
			return nil, apierr.Unprocessable("invalid_tag", fmt.Sprintf("%s: %q is not a valid tag (use tag:name with letters, digits and hyphens)", field, t))
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 || len(out) > 20 {
		return nil, apierr.Unprocessable("invalid_tag", field+" needs 1-20 tags")
	}
	return out, nil
}

// Put stores the integration. Nothing contacts Tailscale until Test or first use.
func (s *Service) Put(ctx context.Context, in PutInput) (TailscaleIntegration, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return TailscaleIntegration{}, err
	}
	ws := policy.WorkspaceID(ctx)
	if in.Provider == "" {
		in.Provider = ProviderTailscaleOAuth
	}
	in.LoginServer, in.HeadscaleUser = strings.TrimSpace(in.LoginServer), strings.TrimSpace(in.HeadscaleUser)
	switch in.Provider {
	case ProviderTailscaleOAuth:
		in.LoginServer, in.HeadscaleUser, in.HeadscaleCredential = "", "", HeadscaleAPIKey
		in.Tailnet = strings.TrimSpace(in.Tailnet)
		if in.Tailnet == "" {
			in.Tailnet = "-"
		}
		if !tailnetRe.MatchString(in.Tailnet) {
			return TailscaleIntegration{}, apierr.Unprocessable("invalid_tailnet", "tailnet must be - or a tailnet name")
		}
		in.ClientID = strings.TrimSpace(in.ClientID)
		if !clientRe.MatchString(in.ClientID) {
			return TailscaleIntegration{}, apierr.Unprocessable("invalid_client_id", "clientId is required")
		}
	case ProviderHeadscale:
		in.Tailnet, in.ClientID = "-", ""
		ls, err := ValidLoginServer(in.LoginServer)
		if err != nil {
			return TailscaleIntegration{}, apierr.Unprocessable("invalid_login_server", err.Error())
		}
		in.LoginServer = ls
		if in.HeadscaleCredential == "" {
			in.HeadscaleCredential = HeadscaleAPIKey
		}
		if in.HeadscaleCredential != HeadscaleAPIKey && in.HeadscaleCredential != HeadscalePreauth {
			return TailscaleIntegration{}, apierr.Unprocessable("invalid_credential", "headscaleCredential must be apikey or preauth")
		}
		if len(in.HeadscaleUser) > 128 || strings.ContainsAny(in.HeadscaleUser, " \t\r\n/") {
			return TailscaleIntegration{}, apierr.Unprocessable("invalid_headscale_user", "headscaleUser must be a Headscale user name without spaces")
		}
		if in.HeadscaleCredential == HeadscalePreauth {
			in.HeadscaleUser = ""
		}
	default:
		return TailscaleIntegration{}, apierr.Unprocessable("invalid_provider", "provider must be tailscale_oauth or headscale")
	}
	def, err := normalizeTags("defaultTags", orDefault(in.DefaultTags))
	if err != nil {
		return TailscaleIntegration{}, err
	}
	allowed, err := normalizeTags("allowedTags", orDefault(in.AllowedTags))
	if err != nil {
		return TailscaleIntegration{}, err
	}
	for _, t := range def {
		if !slices.Contains(allowed, t) {
			return TailscaleIntegration{}, apierr.Unprocessable("invalid_tag", fmt.Sprintf("default tag %s must also be in allowedTags", t))
		}
	}
	if len(in.ClientSecret) > 512 {
		return TailscaleIntegration{}, apierr.Unprocessable("invalid_client_secret", "clientSecret is too long")
	}

	var out db.TailnetIntegration
	var old db.TailnetIntegration
	hadOld := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		o, err := q.GetTailnetIntegration(ctx, ws)
		switch {
		case err == nil:
			old, hadOld = o, true
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if hadOld {
			// The mesh is built on this tailnet: another tailnet or OAuth client would
			// orphan its nodes, and the mesh tag must stay allowed.
			meshTag := ""
			if ms, err := q.GetMeshSettings(ctx, ws); err == nil {
				meshTag = ms.MeshTag
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			identityChanged := old.Provider != in.Provider || old.Tailnet != in.Tailnet || old.LoginServer != in.LoginServer || old.ClientID != in.ClientID
			if identityChanged || (meshTag != "" && !slices.Contains(allowed, meshTag)) {
				if err := guardMeshInUse(ctx, q, ws, "changed"); err != nil {
					return err
				}
			}
		}
		sealed := []byte{} // empty = keep the stored secret
		if in.ClientSecret != "" {
			if sealed, err = s.box.SealString(secretbox.TailnetIntegrationsSecret, ws.String(), in.ClientSecret); err != nil {
				return fmt.Errorf("seal client secret: %w", err)
			}
		} else if !hadOld {
			return apierr.Unprocessable("invalid_client_secret", "clientSecret is required")
		} else if old.Provider != in.Provider {
			return apierr.Unprocessable("invalid_client_secret", "changing the provider requires a new secret")
		} else if old.ClientID != in.ClientID {
			return apierr.Unprocessable("invalid_client_secret", "changing clientId requires the new clientSecret")
		} else if in.Provider == ProviderHeadscale && (old.LoginServer != in.LoginServer || old.HeadscaleCredential != in.HeadscaleCredential) {
			return apierr.Unprocessable("invalid_client_secret", "changing the login server or the credential kind requires the new secret")
		}
		if out, err = q.UpsertTailnetIntegration(ctx, db.UpsertTailnetIntegrationParams{
			WorkspaceID: ws, Provider: in.Provider, Tailnet: in.Tailnet, ClientID: in.ClientID, SecretEnc: sealed, DefaultTags: def, AllowedTags: allowed,
			LoginServer: in.LoginServer, HeadscaleUser: in.HeadscaleUser, HeadscaleCredential: in.HeadscaleCredential,
		}); err != nil {
			return err
		}
		changed := []string{}
		if !hadOld || old.Provider != in.Provider {
			changed = append(changed, "provider")
		}
		if !hadOld || old.Tailnet != in.Tailnet {
			changed = append(changed, "tailnet")
		}
		if !hadOld || old.ClientID != in.ClientID {
			changed = append(changed, "clientId")
		}
		if in.ClientSecret != "" {
			changed = append(changed, "clientSecret")
		}
		if !hadOld || old.LoginServer != in.LoginServer {
			changed = append(changed, "loginServer")
		}
		if !hadOld || old.HeadscaleUser != in.HeadscaleUser {
			changed = append(changed, "headscaleUser")
		}
		if !hadOld || old.HeadscaleCredential != in.HeadscaleCredential {
			changed = append(changed, "headscaleCredential")
		}
		if !hadOld || !slices.Equal(old.DefaultTags, def) {
			changed = append(changed, "defaultTags")
		}
		if !hadOld || !slices.Equal(old.AllowedTags, allowed) {
			changed = append(changed, "allowedTags")
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "integration.tailscale.update", ResourceType: "integration", ResourceID: "tailscale",
			Metadata: map[string]any{"changed": changed},
		})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return TailscaleIntegration{}, err
		}
		return TailscaleIntegration{}, fmt.Errorf("save tailnet integration: %w", err)
	}
	if hadOld {
		s.forget(old)
	}
	return view(out), nil
}

func orDefault(tags []string) []string {
	if len(tags) == 0 {
		return []string{DefaultTag}
	}
	return tags
}

// Delete removes the integration. It is refused while a server uses a managed tailnet.
func (s *Service) Delete(ctx context.Context) error {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return err
	}
	ws := policy.WorkspaceID(ctx)
	var old db.TailnetIntegration
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		o, err := q.GetTailnetIntegration(ctx, ws)
		if errors.Is(err, pgx.ErrNoRows) {
			return apierr.NotFound("tailscale integration")
		}
		if err != nil {
			return err
		}
		old = o
		n, err := q.CountManagedTailnetServers(ctx, ws)
		if err != nil {
			return err
		}
		if n > 0 {
			return apierr.Conflict("integration_in_use", fmt.Sprintf("%d server(s) use a managed tailnet; switch them to off or host mode and deploy the proxy first", n))
		}
		ns, err := q.CountTailnetServices(ctx, ws)
		if err != nil {
			return err
		}
		if ns > 0 {
			return apierr.Conflict("integration_in_use", fmt.Sprintf("%d service(s) are on the tailnet; disable their tailnet and deploy first", ns))
		}
		if err := guardMeshInUse(ctx, q, ws, "removed"); err != nil {
			return err
		}
		if _, err := q.DeleteTailnetIntegration(ctx, ws); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Action: "integration.tailscale.delete", ResourceType: "integration", ResourceID: "tailscale"})
	})
	if err != nil {
		if _, ok := apierr.As(err); ok {
			return err
		}
		return fmt.Errorf("delete tailnet integration: %w", err)
	}
	s.forget(old)
	return nil
}

func (s *Service) config(r db.TailnetIntegration) (Config, error) {
	secret, err := s.box.OpenString(secretbox.TailnetIntegrationsSecret, r.WorkspaceID.String(), r.SecretEnc)
	if err != nil {
		return Config{}, fmt.Errorf("decrypt tailscale client secret: %w", err)
	}
	return Config{BaseURL: s.baseURL, Tailnet: r.Tailnet, ClientID: r.ClientID, ClientSecret: secret, HTTPClient: s.hc}, nil
}

func (s *Service) forget(r db.TailnetIntegration) {
	if r.Provider != ProviderTailscaleOAuth {
		return
	}
	if cfg, err := s.config(r); err == nil {
		ForgetCredentials(cfg)
	}
}

// headscalePreauthTest checks a Headscale server whose credential is a pre-auth
// key: there is no API access, so only reachability can be verified.
func headscalePreauthTest(ctx context.Context, hc *http.Client, loginServer string) TailscaleTestRun {
	var res TailscaleTestRun
	if err := HealthCheck(ctx, hc, loginServer); err != nil {
		res.Checks = append(res.Checks, TailscaleCheck{ID: "credentials", Message: err.Error()})
		return res
	}
	res.OK = true
	res.Checks = append(res.Checks,
		TailscaleCheck{ID: "credentials", OK: true, Message: "The Headscale server is reachable. The pre-auth key itself can only be checked by registering a node."},
		TailscaleCheck{ID: "https", OK: true, Skipped: true, Message: "Headscale has no Serve HTTPS or Funnel: services expose plain HTTP and raw TCP over WireGuard."},
	)
	return res
}

// load returns the integration row and a Tailscale API client for it
// (ErrNotConfigured, or ErrWrongProvider for Headscale).
func (s *Service) load(ctx context.Context, ws uuid.UUID) (db.TailnetIntegration, *Client, error) {
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.TailnetIntegration{}, nil, ErrNotConfigured
	}
	if err != nil {
		return db.TailnetIntegration{}, nil, fmt.Errorf("get tailnet integration: %w", err)
	}
	if r.Provider != ProviderTailscaleOAuth {
		return r, nil, ErrWrongProvider
	}
	cfg, err := s.config(r)
	if err != nil {
		return db.TailnetIntegration{}, nil, err
	}
	return r, New(cfg), nil
}

// Configured reports whether the workspace has a Tailscale (OAuth) integration,
// which is what per-server tailnet egress needs (no scope check: used by the
// server proxy views, which only expose the boolean). A Headscale integration
// serves per-service tailnets only, so it does not count.
func (s *Service) Configured(ctx context.Context, ws uuid.UUID) (bool, error) {
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && r.Provider == ProviderTailscaleOAuth, err
}

// TailscaleTestResult is the API view of a connection test.
type TailscaleTestResult struct {
	OK     bool             `json:"ok"`
	Checks []TailscaleCheck `json:"checks"`
}

// Test runs the connection test and stores its result.
func (s *Service) Test(ctx context.Context) (TailscaleTestResult, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return TailscaleTestResult{}, err
	}
	ws := policy.WorkspaceID(ctx)
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return TailscaleTestResult{}, apierr.NotFound("tailscale integration")
	}
	if err != nil {
		return TailscaleTestResult{}, fmt.Errorf("get tailnet integration: %w", err)
	}
	var res TailscaleTestRun
	if r.Provider == ProviderHeadscale {
		hc, _, err := s.headscaleClient(r)
		if err != nil {
			return TailscaleTestResult{}, err
		}
		if r.HeadscaleCredential == HeadscalePreauth {
			res = headscalePreauthTest(ctx, s.hc, r.LoginServer)
		} else {
			res = hc.Test(ctx, r.DefaultTags)
		}
	} else {
		_, c, err := s.load(ctx, ws)
		if err != nil {
			return TailscaleTestResult{}, err
		}
		res = c.Test(ctx, r.DefaultTags)
	}
	errMsg := ""
	for _, ch := range res.Checks {
		if !ch.OK {
			errMsg = ch.Message
			break
		}
	}
	var msgPtr *string
	if errMsg != "" {
		msgPtr = &errMsg
	}
	if err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.New(tx).RecordTailnetIntegrationTest(ctx, db.RecordTailnetIntegrationTestParams{
			WorkspaceID: ws, LastTestOk: &res.OK, LastTestError: msgPtr, HttpsEnabled: res.HTTPSEnabled,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "integration.tailscale.test", ResourceType: "integration", ResourceID: "tailscale", Metadata: map[string]any{"ok": res.OK},
		})
	}); err != nil {
		return TailscaleTestResult{}, fmt.Errorf("record tailnet test: %w", err)
	}
	return TailscaleTestResult{OK: res.OK, Checks: res.Checks}, nil
}

// Devices lists the tailnet's devices for the picker.
func (s *Service) Devices(ctx context.Context) ([]TailscaleDevice, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return nil, err
	}
	ws := policy.WorkspaceID(ctx)
	row, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.Unprocessable("tailnet_not_configured", "connect Tailscale in Team settings → Integrations first")
	}
	if err != nil {
		return nil, fmt.Errorf("get tailnet integration: %w", err)
	}
	if row.Provider == ProviderHeadscale {
		if row.HeadscaleCredential != HeadscaleAPIKey {
			return nil, apierr.Unprocessable("not_applicable", "a Headscale pre-auth key cannot list nodes; use an API key")
		}
		hc, _, err := s.headscaleClient(row)
		if err != nil {
			return nil, err
		}
		nodes, err := hc.Nodes(ctx)
		if err != nil {
			return nil, upstreamError(err)
		}
		out := make([]TailscaleDevice, 0, len(nodes))
		for _, n := range nodes {
			name := n.GivenName
			if name == "" {
				name = n.Name
			}
			out = append(out, TailscaleDevice{ID: n.ID, Name: name, Hostname: n.Name, Addresses: n.IPs, Tags: n.Tags, Online: n.Online})
		}
		return out, nil
	}
	_, c, err := s.load(ctx, ws)
	if err != nil {
		return nil, err
	}
	devs, err := c.ListDevices(ctx)
	if err != nil {
		return nil, upstreamError(err)
	}
	return devs, nil
}

// upstreamError turns a Tailscale failure into a 502 problem without leaking anything sensitive.
func upstreamError(err error) error {
	if errors.Is(err, ErrAuth) {
		return apierr.New(http.StatusBadGateway, "tailscale_auth_failed", err.Error())
	}
	return apierr.New(http.StatusBadGateway, "tailscale_unavailable", err.Error())
}

// MintAuthKey mints a single-use key for ws with the integration's default
// tags. It is a system-level helper (no scope check) used at command send time
// by the per-server egress node, which is Tailscale (OAuth) only.
func (s *Service) MintAuthKey(ctx context.Context, ws uuid.UUID, expirySeconds int, description string) (string, error) {
	r, c, err := s.load(ctx, ws)
	if err != nil {
		return "", err
	}
	k, err := c.MintAuthKey(ctx, r.DefaultTags, expirySeconds, description)
	if err != nil {
		return "", err
	}
	return k.Key, nil
}

// DeleteDevice removes a device from the tailnet. It is a no-op when the
// integration is gone or the device no longer exists.
func (s *Service) DeleteDevice(ctx context.Context, ws uuid.UUID, deviceID string) error {
	return s.DeleteNode(ctx, ws, NodeRef{DeviceID: deviceID})
}

// EnqueueDeleteDevice schedules the removal of a device (retried with backoff).
func (s *Service) EnqueueDeleteDevice(ctx context.Context, tx pgx.Tx, ws uuid.UUID, deviceID string) error {
	return s.EnqueueDeleteNode(ctx, tx, ws, NodeRef{DeviceID: deviceID})
}

// Settings are the non-secret facts of the integration that per-service
// tailnets need to build a sidecar.
type Settings struct {
	// Provider is the agent-side provider: tailscale or headscale.
	Provider    string
	LoginServer string
	// Credential is the Headscale credential kind (apikey or preauth).
	Credential   string
	DefaultTags  []string
	AllowedTags  []string
	HTTPSEnabled *bool
}

// Settings returns the integration's non-secret settings of ws
// (ErrNotConfigured when there is none). System-level: no scope check.
func (s *Service) Settings(ctx context.Context, ws uuid.UUID) (Settings, error) {
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrNotConfigured
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get tailnet integration: %w", err)
	}
	st := Settings{Provider: "tailscale", DefaultTags: nonNil(r.DefaultTags), AllowedTags: nonNil(r.AllowedTags), HTTPSEnabled: r.HttpsEnabled}
	if r.Provider == ProviderHeadscale {
		st.Provider, st.LoginServer, st.Credential = "headscale", r.LoginServer, r.HeadscaleCredential
	}
	return st, nil
}

// MintRequest asks for a single-use node key.
type MintRequest struct {
	Tags        []string
	Expiry      time.Duration
	Description string
}

// NodeKey is a minted node key. ID identifies it for revocation ("" for a
// reusable pre-auth key, which is never revoked).
type NodeKey struct {
	ID  string
	Key string
}

func (s *Service) headscaleClient(r db.TailnetIntegration) (*HeadscaleClient, string, error) {
	secret, err := s.box.OpenString(secretbox.TailnetIntegrationsSecret, r.WorkspaceID.String(), r.SecretEnc)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt headscale secret: %w", err)
	}
	return NewHeadscale(HeadscaleConfig{LoginServer: r.LoginServer, APIKey: secret, User: r.HeadscaleUser, HTTPClient: s.hc}), secret, nil
}

// MintNodeKey mints the key a service sidecar registers with. For Tailscale and
// a Headscale API key it is single-use, pre-authorized, non-ephemeral and
// tagged; for a Headscale pre-auth credential it is the stored reusable key.
func (s *Service) MintNodeKey(ctx context.Context, ws uuid.UUID, req MintRequest) (NodeKey, error) {
	if len(req.Tags) == 0 {
		return NodeKey{}, errors.New("a node key needs at least one tag")
	}
	if req.Expiry <= 0 {
		req.Expiry = time.Hour
	}
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return NodeKey{}, ErrNotConfigured
	}
	if err != nil {
		return NodeKey{}, fmt.Errorf("get tailnet integration: %w", err)
	}
	if r.Provider == ProviderHeadscale {
		hc, secret, err := s.headscaleClient(r)
		if err != nil {
			return NodeKey{}, err
		}
		if r.HeadscaleCredential == HeadscalePreauth {
			return NodeKey{Key: secret}, nil
		}
		k, err := hc.MintPreAuthKey(ctx, req.Tags, req.Expiry)
		if err != nil {
			return NodeKey{}, err
		}
		return NodeKey{ID: k.ID, Key: k.Key}, nil
	}
	_, c, err := s.load(ctx, ws)
	if err != nil {
		return NodeKey{}, err
	}
	k, err := c.MintAuthKey(ctx, req.Tags, int(req.Expiry.Seconds()), req.Description)
	if err != nil {
		return NodeKey{}, err
	}
	return NodeKey{ID: k.ID, Key: k.Key}, nil
}

// RevokeNodeKey revokes an unused node key (best effort; a key that is gone is fine).
func (s *Service) RevokeNodeKey(ctx context.Context, ws uuid.UUID, keyID string) error {
	if keyID == "" {
		return nil
	}
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get tailnet integration: %w", err)
	}
	if r.Provider == ProviderHeadscale {
		if r.HeadscaleCredential != HeadscaleAPIKey {
			return nil
		}
		hc, _, err := s.headscaleClient(r)
		if err != nil {
			return err
		}
		return hc.ExpirePreAuthKey(ctx, keyID)
	}
	_, c, err := s.load(ctx, ws)
	if err != nil {
		return err
	}
	return c.DeleteKey(ctx, keyID)
}

// NodeRef identifies a node to remove. Tailscale deletes by DeviceID (the
// stable node id). Headscale's node ids differ from it, so the node is found by
// one of its tailnet addresses, else by its host name.
type NodeRef struct {
	DeviceID string   `json:"deviceId,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
}

func (n NodeRef) empty() bool { return n.DeviceID == "" && len(n.IPs) == 0 && n.Hostname == "" }

// DeleteNode removes a node from the tailnet. It is a no-op when the
// integration is gone, the node no longer exists or (Headscale pre-auth
// credential) the provider offers no way to delete it.
func (s *Service) DeleteNode(ctx context.Context, ws uuid.UUID, ref NodeRef) error {
	if ref.empty() {
		return nil
	}
	r, err := s.q.GetTailnetIntegration(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get tailnet integration: %w", err)
	}
	if r.Provider == ProviderHeadscale {
		if r.HeadscaleCredential != HeadscaleAPIKey {
			return nil
		}
		hc, _, err := s.headscaleClient(r)
		if err != nil {
			return err
		}
		nodes, err := hc.Nodes(ctx)
		if err != nil {
			return err
		}
		n, ok := FindNode(nodes, ref.IPs, ref.Hostname)
		if !ok {
			return nil
		}
		return hc.DeleteNode(ctx, n.ID)
	}
	if ref.DeviceID == "" {
		return nil
	}
	_, c, err := s.load(ctx, ws)
	if err != nil {
		return err
	}
	if err := c.DeleteDevice(ctx, ref.DeviceID); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// EnqueueDeleteNode schedules the removal of a node (retried with backoff).
func (s *Service) EnqueueDeleteNode(ctx context.Context, tx pgx.Tx, ws uuid.UUID, ref NodeRef) error {
	if ref.empty() || s.jobs == nil {
		return nil
	}
	return s.jobs.InsertTx(ctx, tx, TailnetDeleteDeviceArgs{WorkspaceID: ws, DeviceID: ref.DeviceID, IPs: ref.IPs, Hostname: ref.Hostname}, nil)
}
