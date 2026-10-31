package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// ErrNotConfigured is returned when the team has no Cloudflare connection.
var ErrNotConfigured = errors.New("cloudflare integration is not configured")

// TunnelTargets tells which servers sit behind a Cloudflare Tunnel. A domain on
// such a server gets a proxied CNAME to the tunnel instead of A/AAAA records.
type TunnelTargets interface {
	// TunnelTarget returns "<tunnelId>.cfargotunnel.com" and true when the
	// server has a tunnel that is not being removed.
	TunnelTarget(ctx context.Context, serverID uuid.UUID) (cname string, ok bool, err error)
}

// Service is the team-level Cloudflare integration.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	box  *secretbox.Box
	jobs *jobs.Manager
	log  *slog.Logger

	baseURL string
	hc      *http.Client
	now     func() time.Time

	mu         sync.RWMutex
	tunnels    TunnelTargets
	notifier   notifications.Emitter
	deleteGate func(ctx context.Context, ws uuid.UUID) error
	pusher     RoutePusher
	// ipsAPIBase and ipsTextBase override where the IP list is fetched (tests).
	ipsAPIBase, ipsTextBase string

	hostLocks sync.Map // "<workspace>/<hostname>" -> *sync.Mutex
	notified  sync.Map // notification key -> time.Time of the last one
}

// NewService returns a Service (arguments may be nil for OpenAPI generation).
func NewService(pool *pgxpool.Pool, box *secretbox.Box, jm *jobs.Manager, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, q: db.New(pool), box: box, jobs: jm, log: logger.With("component", "cloudflare"), now: time.Now}
}

// SetBaseURL overrides the Cloudflare API base URL (tests).
func (s *Service) SetBaseURL(u string, hc *http.Client) { s.baseURL, s.hc = u, hc }

// SetTunnelTargets plugs in the lookup of a server's tunnel.
func (s *Service) SetTunnelTargets(t TunnelTargets) {
	s.mu.Lock()
	s.tunnels = t
	s.mu.Unlock()
}

// SetNotifier plugs in notifications for failed syncs.
func (s *Service) SetNotifier(n notifications.Emitter) {
	s.mu.Lock()
	s.notifier = n
	s.mu.Unlock()
}

// SetDeleteGate registers a check that may refuse removing the connection
// (the tunnel feature refuses while tunnels exist). It returns an API error.
func (s *Service) SetDeleteGate(fn func(ctx context.Context, ws uuid.UUID) error) {
	s.mu.Lock()
	s.deleteGate = fn
	s.mu.Unlock()
}

func (s *Service) tunnelTargets() TunnelTargets {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tunnels
}

// SetRoutePusher plugs in the route push used when the origin lock changes (a
// domain's lock, an Authenticated Origin Pulls state or Cloudflare's IP list).
func (s *Service) SetRoutePusher(p RoutePusher) {
	s.mu.Lock()
	s.pusher = p
	s.mu.Unlock()
}

func (s *Service) routePusher() RoutePusher {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pusher
}

func (s *Service) emitter() notifications.Emitter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.notifier
}

func (s *Service) newClient(token string) *Client {
	return New(Config{BaseURL: s.baseURL, Token: token, HTTPClient: s.hc})
}

// hostLock serialises record changes of one hostname (a sync job and a fix
// request must not both create the record).
func (s *Service) hostLock(ws uuid.UUID, host string) func() {
	v, _ := s.hostLocks.LoadOrStore(ws.String()+"/"+host, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ---- connection views ----

// CloudflareConnectionView is the API view of the connection. The token is never part of it.
type CloudflareConnectionView struct {
	Configured     bool         `json:"configured"`
	Status         string       `json:"status" enum:",ok,error" doc:"error after Cloudflare rejected the token; empty when not configured"`
	StatusMessage  string       `json:"statusMessage"`
	AccountID      string       `json:"accountId" doc:"Account used for Cloudflare Tunnel calls"`
	AccountName    string       `json:"accountName"`
	Accounts       []AccountRef `json:"accounts" doc:"Accounts the token's zones belong to"`
	TokenExpiresAt *time.Time   `json:"tokenExpiresAt"`
	VerifiedAt     *time.Time   `json:"verifiedAt"`
	ZonesSyncedAt  *time.Time   `json:"zonesSyncedAt"`
	ZoneCount      int          `json:"zoneCount"`
	Warnings       []string     `json:"warnings"`
}

func connectionView(r db.CloudflareConnection) CloudflareConnectionView {
	zones := decodeZones(r.Zones)
	v := CloudflareConnectionView{
		Configured: true, Status: r.Status, StatusMessage: r.StatusMessage, AccountID: r.AccountID, AccountName: r.AccountName,
		Accounts:       accountsOf(zones),
		TokenExpiresAt: r.TokenExpiresAt, VerifiedAt: r.VerifiedAt, ZonesSyncedAt: r.ZonesSyncedAt, ZoneCount: len(zones),
		Warnings: []string{},
	}
	if len(zones) == 0 {
		v.Warnings = append(v.Warnings, "The token sees no zones. Grant it Zone → Zone: Read (and DNS: Edit) on the zones you want NebulaCtrl to manage.")
	}
	if r.TokenExpiresAt != nil && time.Until(*r.TokenExpiresAt) < 14*24*time.Hour {
		if r.TokenExpiresAt.Before(time.Now()) {
			v.Warnings = append(v.Warnings, "The token has expired.")
		} else {
			v.Warnings = append(v.Warnings, "The token expires on "+r.TokenExpiresAt.UTC().Format("2006-01-02")+".")
		}
	}
	return v
}

func emptyConnectionView() CloudflareConnectionView {
	return CloudflareConnectionView{Accounts: []AccountRef{}, Warnings: []string{}}
}

// Get returns the team's connection ({configured:false} when absent).
func (s *Service) Get(ctx context.Context) (CloudflareConnectionView, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return CloudflareConnectionView{}, err
	}
	r, err := s.q.GetCloudflareConnection(ctx, policy.WorkspaceID(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return emptyConnectionView(), nil
	}
	if err != nil {
		return CloudflareConnectionView{}, fmt.Errorf("get cloudflare connection: %w", err)
	}
	return connectionView(r), nil
}

// Zones returns the cached zone list (the zone picker).
func (s *Service) Zones(ctx context.Context) ([]Zone, error) {
	if err := policy.Require(ctx, policy.ScopeProjectsRead, nil); err != nil {
		return nil, err
	}
	r, err := s.q.GetCloudflareConnection(ctx, policy.WorkspaceID(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return []Zone{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get cloudflare connection: %w", err)
	}
	return decodeZones(r.Zones), nil
}

// Connection is the non-secret part of a team's connection, for other features
// (Cloudflare Tunnel) that call the API with the same token.
type Connection struct {
	WorkspaceID uuid.UUID
	AccountID   string
	AccountName string
	Zones       []Zone
}

// Connection returns the team's connection settings (ErrNotConfigured when
// there is none). System-level: no scope check.
func (s *Service) Connection(ctx context.Context, ws uuid.UUID) (Connection, error) {
	r, err := s.q.GetCloudflareConnection(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotConfigured
	}
	if err != nil {
		return Connection{}, fmt.Errorf("get cloudflare connection: %w", err)
	}
	return Connection{
		WorkspaceID: ws, AccountID: r.AccountID, AccountName: r.AccountName, Zones: decodeZones(r.Zones),
	}, nil
}

// ClientFor returns an API client for the team's token (ErrNotConfigured when
// there is none). System-level: no scope check.
func (s *Service) ClientFor(ctx context.Context, ws uuid.UUID) (*Client, error) {
	r, err := s.q.GetCloudflareConnection(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("get cloudflare connection: %w", err)
	}
	return s.clientOf(r)
}

func (s *Service) clientOf(r db.CloudflareConnection) (*Client, error) {
	token, err := s.box.OpenString(secretbox.CloudflareConnectionsToken, r.WorkspaceID.String(), r.TokenEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt cloudflare token: %w", err)
	}
	return s.newClient(token), nil
}

// ---- verify and store ----

// verified is what a token check learned.
type verified struct {
	zones   []Zone
	expires *time.Time
}

// verify checks a token by listing its zones and asking the token endpoint.
func (s *Service) verify(ctx context.Context, c *Client) (verified, error) {
	zones, err := c.ListZones(ctx)
	if err != nil {
		return verified{}, mapAPIError(err)
	}
	account := ""
	if as := accountsOf(zones); len(as) > 0 {
		account = as[0].ID
	}
	v := verified{zones: zones}
	st, verr := c.VerifyToken(ctx, account)
	switch {
	case verr == nil:
		if st.Status != "" && st.Status != "active" {
			return verified{}, apierr.Unprocessable("invalid_token", "Cloudflare reports the token as "+st.Status)
		}
		v.expires = st.ExpiresOn
	case len(zones) == 0 && errors.Is(verr, ErrAuth):
		return verified{}, mapAPIError(verr)
	}
	// Any other verify failure is tolerated: some account-owned tokens cannot
	// call the verify endpoint, and the zone list already proved the token works.
	return v, nil
}

// mapAPIError turns a Cloudflare failure into an API problem without leaking anything sensitive.
func mapAPIError(err error) error {
	if err == nil {
		return nil
	}
	var ae *APIError
	if errors.As(err, &ae) {
		if errors.Is(err, ErrAuth) {
			msg := "Cloudflare rejected the token"
			if ae.Message != "" {
				msg += ": " + ae.Message
			}
			return apierr.Unprocessable("invalid_token", msg+". It needs Zone → Zone: Read and Zone → DNS: Edit.")
		}
		msg := "Cloudflare answered " + fmt.Sprint(ae.Status)
		if ae.Message != "" {
			msg += ": " + ae.Message
		}
		return apierr.New(http.StatusBadGateway, "cloudflare_unavailable", msg)
	}
	return apierr.New(http.StatusBadGateway, "cloudflare_unavailable", "Cloudflare could not be reached")
}

// PutInput creates or updates the connection. An empty Token keeps the stored
// token (one is required when none is stored yet). DNS01CredentialID copies the
// token of a Cloudflare DNS-01 credential instead of typing it again.
type PutInput struct {
	Token           string
	DNS01Credential *uuid.UUID
	AccountID       string
}

// Put stores the connection. A new token is verified against Cloudflare first
// (the zone list must load); the zone list is cached and every domain of the
// team is matched against it.
func (s *Service) Put(ctx context.Context, in PutInput) (CloudflareConnectionView, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return CloudflareConnectionView{}, err
	}
	ws := policy.WorkspaceID(ctx)
	cur, err := s.q.GetCloudflareConnection(ctx, ws)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CloudflareConnectionView{}, fmt.Errorf("get cloudflare connection: %w", err)
	}
	token := strings.TrimSpace(in.Token)
	if token == "" && in.DNS01Credential != nil {
		if token, err = s.dns01Token(ctx, ws, *in.DNS01Credential); err != nil {
			return CloudflareConnectionView{}, err
		}
	}
	if token == "" && !exists {
		return CloudflareConnectionView{}, apierr.Unprocessable("invalid_token", "an API token is required")
	}

	var (
		zones   []Zone
		expires *time.Time
	)
	if token != "" {
		v, err := s.verify(ctx, s.newClient(token))
		if err != nil {
			return CloudflareConnectionView{}, err
		}
		zones, expires = v.zones, v.expires
	} else {
		zones = decodeZones(cur.Zones)
	}
	account, err := pickAccount(zones, in.AccountID, cur.AccountID)
	if err != nil {
		return CloudflareConnectionView{}, err
	}

	var out db.CloudflareConnection
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if token != "" {
			enc, err := s.box.SealString(secretbox.CloudflareConnectionsToken, ws.String(), token)
			if err != nil {
				return fmt.Errorf("seal token: %w", err)
			}
			out, err = q.UpsertCloudflareConnection(ctx, db.UpsertCloudflareConnectionParams{
				WorkspaceID: ws, TokenEnc: enc, AccountID: account.ID, AccountName: account.Name,
				TokenExpiresAt: expires, Zones: encodeZones(zones),
			})
			if err != nil {
				return err
			}
		} else {
			out, err = q.UpdateCloudflareSettings(ctx, db.UpdateCloudflareSettingsParams{
				WorkspaceID: ws, AccountID: account.ID, AccountName: account.Name,
			})
			if err != nil {
				return err
			}
		}
		if err := s.rematch(ctx, tx, out); err != nil {
			return err
		}
		// The audit entry names what changed, never the token.
		return audit.Record(ctx, tx, audit.Entry{
			Action: "integration.cloudflare.update", ResourceType: "integration", ResourceID: ws.String(), WorkspaceID: ws,
			Metadata: map[string]any{
				"tokenChanged": token != "", "accountId": account.ID, "zones": len(zones),
			},
		})
	})
	if err != nil {
		return CloudflareConnectionView{}, fmt.Errorf("store cloudflare connection: %w", err)
	}
	return connectionView(out), nil
}

// pickAccount chooses the account Tunnel calls use: the requested one, else the
// stored one, else the first account of the zones.
func pickAccount(zones []Zone, requested, stored string) (AccountRef, error) {
	accounts := accountsOf(zones)
	if requested != "" {
		for _, a := range accounts {
			if a.ID == requested {
				return a, nil
			}
		}
		return AccountRef{}, apierr.Unprocessable("invalid_account", "the token sees no zone of that account")
	}
	for _, a := range accounts {
		if a.ID == stored {
			return a, nil
		}
	}
	if len(accounts) > 0 {
		return accounts[0], nil
	}
	return AccountRef{ID: stored}, nil
}

// dns01Token reads the token of a Cloudflare DNS-01 credential of the team.
func (s *Service) dns01Token(ctx context.Context, ws, credentialID uuid.UUID) (string, error) {
	c, err := s.q.GetDNSCredential(ctx, db.GetDNSCredentialParams{ID: credentialID, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apierr.NotFound("DNS credential")
	}
	if err != nil {
		return "", fmt.Errorf("get dns credential: %w", err)
	}
	if c.Provider != "cloudflare" {
		return "", apierr.Unprocessable("invalid_credential", "that DNS credential is not a Cloudflare credential")
	}
	plain, err := s.box.OpenString(secretbox.DNSCredentialsSecrets, c.ID.String(), c.SecretsEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt dns credential: %w", err)
	}
	var vals map[string]string
	if err := json.Unmarshal([]byte(plain), &vals); err != nil {
		return "", fmt.Errorf("decode dns credential: %w", err)
	}
	tok := strings.TrimSpace(vals["CF_DNS_API_TOKEN"])
	if tok == "" {
		return "", apierr.Unprocessable("invalid_credential", "that credential has no CF_DNS_API_TOKEN")
	}
	return tok, nil
}

// Test re-verifies the token and refreshes the zone list.
func (s *Service) Test(ctx context.Context) (CloudflareConnectionView, error) {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return CloudflareConnectionView{}, err
	}
	row, err := s.refresh(ctx, policy.WorkspaceID(ctx))
	if errors.Is(err, ErrNotConfigured) {
		return CloudflareConnectionView{}, apierr.NotFound("Cloudflare connection")
	}
	if err != nil {
		return CloudflareConnectionView{}, err
	}
	return connectionView(row), nil
}

// refresh verifies the stored token, caches the zone list and re-matches every
// domain. A rejected token is recorded on the connection.
func (s *Service) refresh(ctx context.Context, ws uuid.UUID) (db.CloudflareConnection, error) {
	cur, err := s.q.GetCloudflareConnection(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.CloudflareConnection{}, ErrNotConfigured
	}
	if err != nil {
		return db.CloudflareConnection{}, fmt.Errorf("get cloudflare connection: %w", err)
	}
	c, err := s.clientOf(cur)
	if err != nil {
		return db.CloudflareConnection{}, err
	}
	v, err := s.verify(ctx, c)
	if err != nil {
		if ae, ok := apierr.As(err); ok && ae.Code == "invalid_token" {
			_ = s.q.SetCloudflareFailed(ctx, db.SetCloudflareFailedParams{WorkspaceID: ws, StatusMessage: ae.Message})
		}
		return db.CloudflareConnection{}, err
	}
	account, _ := pickAccount(v.zones, "", cur.AccountID)
	var out db.CloudflareConnection
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		out, err = q.SetCloudflareVerified(ctx, db.SetCloudflareVerifiedParams{
			WorkspaceID: ws, TokenExpiresAt: v.expires, Zones: encodeZones(v.zones), AccountID: account.ID, AccountName: account.Name,
		})
		if err != nil {
			return err
		}
		return s.rematch(ctx, tx, out)
	})
	if err != nil {
		return db.CloudflareConnection{}, fmt.Errorf("store zones: %w", err)
	}
	return out, nil
}

// Delete removes the connection. With removeRecords the records NebulaCtrl
// created are deleted from Cloudflare first (the removal is refused when that
// fails); otherwise they stay in the zone, now unmanaged.
func (s *Service) Delete(ctx context.Context, removeRecords bool) error {
	if err := policy.Require(ctx, policy.ScopeAdmin, nil); err != nil {
		return err
	}
	ws := policy.WorkspaceID(ctx)
	cur, err := s.q.GetCloudflareConnection(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("Cloudflare connection")
	}
	if err != nil {
		return fmt.Errorf("get cloudflare connection: %w", err)
	}
	s.mu.RLock()
	gate := s.deleteGate
	s.mu.RUnlock()
	if gate != nil {
		if err := gate(ctx, ws); err != nil {
			return err
		}
	}
	removed := 0
	if removeRecords {
		c, err := s.clientOf(cur)
		if err != nil {
			return err
		}
		recs, err := s.q.ListCloudflareRecords(ctx, ws)
		if err != nil {
			return fmt.Errorf("list tracked records: %w", err)
		}
		for _, r := range recs {
			if err := s.deleteTracked(ctx, c, r); err != nil {
				return mapAPIError(err)
			}
			removed++
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.ClearWorkspaceDomainsCloudflare(ctx, ws); err != nil {
			return err
		}
		if err := q.ClearWorkspaceStatusPagesCloudflare(ctx, ws); err != nil {
			return err
		}
		if err := q.DeleteCloudflareRecordsOfWorkspace(ctx, ws); err != nil {
			return err
		}
		if _, err := q.DeleteCloudflareConnection(ctx, ws); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: "integration.cloudflare.delete", ResourceType: "integration", ResourceID: ws.String(), WorkspaceID: ws,
			Metadata: map[string]any{"removeRecords": removeRecords, "recordsRemoved": removed},
		})
	})
	if err != nil {
		return fmt.Errorf("delete cloudflare connection: %w", err)
	}
	return nil
}

// ---- hooks used by other packages ----

func (s *Service) hasConnection(ctx context.Context, q *db.Queries, ws uuid.UUID) (db.CloudflareConnection, bool, error) {
	c, err := q.GetCloudflareConnection(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.CloudflareConnection{}, false, nil
	}
	if err != nil {
		return db.CloudflareConnection{}, false, fmt.Errorf("get cloudflare connection: %w", err)
	}
	return c, true, nil
}

// rematch matches every domain and status page of the team against the
// connection's zones. Names that land in a zone for the first time are managed
// from then on, start proxied unless they cannot, and get a sync job; names
// that fell out of every zone are marked not_in_zone.
func (s *Service) rematch(ctx context.Context, tx pgx.Tx, conn db.CloudflareConnection) error {
	q := db.New(tx)
	zones := decodeZones(conn.Zones)
	doms, err := q.ListCloudflareWorkspaceDomains(ctx, conn.WorkspaceID)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	for _, d := range doms {
		if err := s.rematchName(ctx, tx, &dnsName{Kind: OwnerDomain, ID: d.ID, Host: d.Hostname, ZoneID: d.CfZoneID, ZoneName: d.CfZoneName, Status: d.CfStatus}, zones); err != nil {
			return err
		}
	}
	return s.rematchPages(ctx, tx, conn)
}

// rematchPages matches the status pages of the team against the connection's
// zones (see rematch). The sweep runs it on its own: a page that existed before
// the connection or the feature is matched without waiting for a zone refresh.
func (s *Service) rematchPages(ctx context.Context, tx pgx.Tx, conn db.CloudflareConnection) error {
	zones := decodeZones(conn.Zones)
	pages, err := db.New(tx).ListCloudflareWorkspaceStatusPages(ctx, conn.WorkspaceID)
	if err != nil {
		return fmt.Errorf("list status pages: %w", err)
	}
	for _, p := range pages {
		if err := s.rematchName(ctx, tx, pageName(p), zones); err != nil {
			return err
		}
	}
	return nil
}

// rematchName matches one name against the zones.
func (s *Service) rematchName(ctx context.Context, tx pgx.Tx, n *dnsName, zones []Zone) error {
	q, ow := db.New(tx), s.ownerOf(n.Kind)
	z, ok := MatchZone(zones, n.Host)
	switch {
	case ok && n.ZoneID == "":
		if err := ow.setFirstMatch(ctx, q, n.ID, z, startsProxied(n.Host)); err != nil {
			return err
		}
		return s.enqueueSync(ctx, tx, n.Kind, n.ID)
	case ok && (n.ZoneID != z.ID || n.ZoneName != z.Name):
		if err := ow.setMatch(ctx, q, n.ID, z); err != nil {
			return err
		}
		return s.enqueueSync(ctx, tx, n.Kind, n.ID)
	case !ok && (n.ZoneID != "" || n.Status != StatusNotInZone):
		if err := ow.setMatch(ctx, q, n.ID, Zone{}); err != nil {
			return err
		}
		return ow.setState(ctx, q, n.ID, StatusNotInZone, notInZoneMessage)
	}
	return nil
}

const notInZoneMessage = "The hostname is in none of the zones the Cloudflare token can see, so NebulaCtrl does not manage its DNS."

// DomainCreated matches a new domain against the team's zones, starts it
// proxied where it can be and schedules the first sync. It does nothing
// without a connection.
func (s *Service) DomainCreated(ctx context.Context, tx pgx.Tx, domainID uuid.UUID) error {
	return s.matchDomain(ctx, tx, domainID, true)
}

// DomainRenamed re-matches a domain whose hostname changed. Its old records
// are collected by the next collector run.
func (s *Service) DomainRenamed(ctx context.Context, tx pgx.Tx, domainID uuid.UUID) error {
	return s.matchDomain(ctx, tx, domainID, false)
}

func (s *Service) matchDomain(ctx context.Context, tx pgx.Tx, domainID uuid.UUID, created bool) error {
	q := db.New(tx)
	dom, err := q.GetDomainByID(ctx, domainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get domain: %w", err)
	}
	ws, err := s.domainWorkspace(ctx, q, dom)
	if err != nil || ws == uuid.Nil {
		return err
	}
	return s.matchName(ctx, tx, &dnsName{Kind: OwnerDomain, ID: dom.ID, Workspace: ws, Host: dom.Hostname, ZoneID: dom.CfZoneID}, created)
}

// matchName matches one name against the team's zones: no zone marks it
// not_in_zone, a zone is stored and schedules a sync. A name that had no zone
// (or is new: first) starts proxied unless it cannot. It does nothing without
// a connection.
func (s *Service) matchName(ctx context.Context, tx pgx.Tx, n *dnsName, first bool) error {
	q, ow := db.New(tx), s.ownerOf(n.Kind)
	conn, ok, err := s.hasConnection(ctx, q, n.Workspace)
	if err != nil || !ok {
		return err
	}
	z, found := MatchZone(decodeZones(conn.Zones), n.Host)
	if !found {
		if err := ow.setMatch(ctx, q, n.ID, Zone{}); err != nil {
			return err
		}
		return ow.setState(ctx, q, n.ID, StatusNotInZone, notInZoneMessage)
	}
	if first || n.ZoneID == "" {
		err = ow.setFirstMatch(ctx, q, n.ID, z, startsProxied(n.Host))
	} else {
		err = ow.setMatch(ctx, q, n.ID, z)
	}
	if err != nil {
		return err
	}
	return s.enqueueSync(ctx, tx, n.Kind, n.ID)
}

// StatusPageHostChanged matches a status page's hostname against the team's
// zones after it was set, changed or removed, and schedules the sync of its
// records (and the collector, for the records of the hostname it left). fresh
// says the page had no hostname before: it then starts proxied like a new
// domain. It does nothing without a connection.
func (s *Service) StatusPageHostChanged(ctx context.Context, tx pgx.Tx, pageID uuid.UUID, fresh bool) error {
	q := db.New(tx)
	p, err := q.GetStatusPageByID(ctx, pageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get status page: %w", err)
	}
	if _, ok, err := s.hasConnection(ctx, q, p.WorkspaceID); err != nil || !ok {
		return err
	}
	if p.Hostname == nil || *p.Hostname == "" {
		if p.CfZoneID != "" || p.CfStatus != "" {
			if err := (pageOwner{s}).setMatch(ctx, q, p.ID, Zone{}); err != nil {
				return err
			}
		}
	} else if err := s.matchName(ctx, tx, pageName(p), fresh || p.CfZoneID == ""); err != nil {
		return err
	}
	return s.EnqueueCollect(ctx, tx, p.WorkspaceID)
}

// StatusPageRemoved schedules the collector after status pages of the team were
// deleted: records of hostnames nobody uses any more are removed from Cloudflare.
func (s *Service) StatusPageRemoved(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	return s.DomainsRemoved(ctx, tx, ws)
}

// DomainsRemoved schedules the collector after domains of the team were
// deleted: records of hostnames nobody uses any more are removed from Cloudflare.
func (s *Service) DomainsRemoved(ctx context.Context, tx pgx.Tx, ws uuid.UUID) error {
	_, ok, err := s.hasConnection(ctx, db.New(tx), ws)
	if err != nil || !ok {
		return err
	}
	return s.EnqueueCollect(ctx, tx, ws)
}

// EnqueueSyncServer re-syncs the DNS records of every domain of the services
// placed on the server (its public IP or tunnel changed). The control-plane
// server also carries the status pages: their records follow its public IPs.
func (s *Service) EnqueueSyncServer(ctx context.Context, tx pgx.Tx, ws, serverID uuid.UUID) error {
	q := db.New(tx)
	if _, ok, err := s.hasConnection(ctx, q, ws); err != nil {
		return err
	} else if ok {
		ids, err := q.ListCloudflareServerDomainIDs(ctx, &serverID)
		if err != nil {
			return fmt.Errorf("list server domains: %w", err)
		}
		for _, id := range ids {
			if err := s.EnqueueSyncDomain(ctx, tx, id); err != nil {
				return err
			}
		}
	}
	srv, err := q.GetServerByID(ctx, serverID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !srv.IsControlPlane) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	pages, err := q.ListCloudflareStatusPageIDs(ctx)
	if err != nil {
		return fmt.Errorf("list status pages in zones: %w", err)
	}
	for _, id := range pages {
		if err := s.EnqueueSyncStatusPage(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueSyncService re-syncs the DNS records of a service's domains (it was
// deployed, possibly on another server than before).
func (s *Service) EnqueueSyncService(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID) error {
	q := db.New(tx)
	ids, err := q.ListCloudflareServiceDomainIDs(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("list service domains: %w", err)
	}
	for _, id := range ids {
		if err := s.EnqueueSyncDomain(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
