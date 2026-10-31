package servers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Bounds of the per-server geolocation settings.
const (
	MinGeoRefreshDays = 1
	MaxGeoRefreshDays = 90
)

// geoDeps are the Service dependencies of the geolocation settings.
type geoDeps struct {
	box *secretbox.Box
}

// SetBox installs the secretbox that seals the team's MaxMind licence key.
func (s *Service) SetBox(b *secretbox.Box) { s.geo.box = b }

var (
	maxmindAccountRe = regexp.MustCompile(`^[0-9]{1,20}$`)
	maxmindKeyRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{6,100}$`)
)

// applyGeoUpdate validates the geolocation fields of a PATCH, fills params and
// the audit metadata, and reports whether the agent-visible setting changes
// (so the caller resyncs it).
func (s *Service) applyGeoUpdate(ctx context.Context, cur db.GetServerScopedRow, in UpdateInput, params *db.UpdateServerSettingsParams, changed map[string]any) (bool, error) {
	if in.GeoEnabled == nil && in.GeoSource == nil && in.GeoLevel == nil && in.GeoRefreshDays == nil {
		return false, nil
	}
	source, level, enabled := cur.GeoSource, cur.GeoLevel, cur.GeoEnabled
	if in.GeoSource != nil {
		switch *in.GeoSource {
		case agentprotocol.GeoSourceDBIP, agentprotocol.GeoSourceMaxMind:
			source = *in.GeoSource
		default:
			return false, apierr.Unprocessable("invalid_geo_source", "geoSource must be dbip or maxmind")
		}
		params.GeoSource = &source
		changed["geoSource"] = source
	}
	if in.GeoLevel != nil {
		switch *in.GeoLevel {
		case agentprotocol.GeoLevelCountry, agentprotocol.GeoLevelCity:
			level = *in.GeoLevel
		default:
			return false, apierr.Unprocessable("invalid_geo_level", "geoLevel must be country or city")
		}
		params.GeoLevel = &level
		changed["geoLevel"] = level
	}
	if in.GeoEnabled != nil {
		enabled = *in.GeoEnabled
		params.GeoEnabled = in.GeoEnabled
		changed["geoEnabled"] = enabled
	}
	var err error
	if params.GeoRefreshDays, err = checkRange("geoRefreshDays", in.GeoRefreshDays, MinGeoRefreshDays, MaxGeoRefreshDays); err != nil {
		return false, err
	}
	if params.GeoRefreshDays != nil {
		changed["geoRefreshDays"] = *params.GeoRefreshDays
	}
	if enabled && source == agentprotocol.GeoSourceMaxMind {
		if _, err := s.q.GetGeoCredentials(ctx, cur.WorkspaceID); errors.Is(err, pgx.ErrNoRows) {
			return false, apierr.Unprocessable("maxmind_key_required", "Add your MaxMind account ID and licence key before selecting MaxMind GeoLite2")
		} else if err != nil {
			return false, fmt.Errorf("load geo credentials: %w", err)
		}
	}
	return enabled != cur.GeoEnabled || source != cur.GeoSource || level != cur.GeoLevel ||
		(params.GeoRefreshDays != nil && *params.GeoRefreshDays != cur.GeoRefreshDays), nil
}

// GeoConfigFor returns the agent-facing geolocation setting of a server and,
// for the MaxMind source, the team's credentials as send-time secrets. It is
// called by the reconciler when it builds a reconcile frame (system context:
// no principal), so it takes the workspace from the server row.
func (s *Service) GeoConfigFor(ctx context.Context, serverID uuid.UUID) (*agentprotocol.GeoConfig, map[string]string, error) {
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID})
	if err != nil {
		return nil, nil, fmt.Errorf("load server: %w", err)
	}
	cfg := &agentprotocol.GeoConfig{
		// Geolocation rides on traffic analytics: without the access log there is nothing to look up.
		Enabled: srv.GeoEnabled && srv.TrafficAnalyticsEnabled, Source: srv.GeoSource, Level: srv.GeoLevel, RefreshDays: int(srv.GeoRefreshDays),
	}
	if !cfg.Enabled || cfg.Source != agentprotocol.GeoSourceMaxMind || s.geo.box == nil {
		return cfg, nil, nil
	}
	cred, err := s.q.GetGeoCredentials(ctx, srv.WorkspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return cfg, nil, nil // the agent reports the missing key in its log
	}
	if err != nil {
		return nil, nil, fmt.Errorf("load geo credentials: %w", err)
	}
	key, err := s.geo.box.OpenString(secretbox.GeoCredentialsMaxmindKey, srv.WorkspaceID.String(), cred.MaxmindKeyEnc)
	if err != nil {
		return nil, nil, fmt.Errorf("open maxmind key: %w", err)
	}
	return cfg, map[string]string{
		agentprotocol.SecretGeoMaxMindAccount: cred.MaxmindAccountID,
		agentprotocol.SecretGeoMaxMindKey:     key,
	}, nil
}

// GeoCredentialsStatus tells whether the team has a MaxMind account on file;
// the licence key is never returned.
type GeoCredentialsStatus struct {
	Configured bool       `json:"configured"`
	AccountID  string     `json:"accountId" doc:"MaxMind account ID; empty when none is stored"`
	UpdatedAt  *time.Time `json:"updatedAt"`
	// ServersUsing counts the team's servers that currently use the MaxMind source.
	ServersUsing int `json:"serversUsing"`
}

// GeoCredentials reports the team's MaxMind account (scope servers:read).
func (s *Service) GeoCredentials(ctx context.Context) (GeoCredentialsStatus, error) {
	if err := policy.Require(ctx, policy.ScopeServersRead, nil); err != nil {
		return GeoCredentialsStatus{}, err
	}
	return s.geoStatus(ctx, s.q, policy.WorkspaceID(ctx))
}

func (s *Service) geoStatus(ctx context.Context, q *db.Queries, ws uuid.UUID) (GeoCredentialsStatus, error) {
	out := GeoCredentialsStatus{}
	ids, err := q.ListMaxMindGeoServerIDs(ctx, ws)
	if err != nil {
		return out, fmt.Errorf("list maxmind servers: %w", err)
	}
	out.ServersUsing = len(ids)
	cred, err := q.GetGeoCredentials(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("get geo credentials: %w", err)
	}
	t := cred.UpdatedAt
	out.Configured, out.AccountID, out.UpdatedAt = true, cred.MaxmindAccountID, &t
	return out, nil
}

// SetGeoCredentials stores (or replaces) the team's MaxMind account ID and
// licence key, sealed with secretbox (scope servers:write). Servers on the
// MaxMind source receive the new key with a resync.
func (s *Service) SetGeoCredentials(ctx context.Context, accountID, licenseKey string) (GeoCredentialsStatus, error) {
	if err := policy.Require(ctx, policy.ScopeServersWrite, nil); err != nil {
		return GeoCredentialsStatus{}, err
	}
	accountID, licenseKey = strings.TrimSpace(accountID), strings.TrimSpace(licenseKey)
	if !maxmindAccountRe.MatchString(accountID) {
		return GeoCredentialsStatus{}, apierr.Unprocessable("invalid_maxmind_account", "accountId must be the numeric MaxMind account ID")
	}
	if !maxmindKeyRe.MatchString(licenseKey) {
		return GeoCredentialsStatus{}, apierr.Unprocessable("invalid_maxmind_key", "licenseKey must be a MaxMind licence key (letters, digits, - and _)")
	}
	if s.geo.box == nil {
		return GeoCredentialsStatus{}, errors.New("secretbox is not configured")
	}
	ws := policy.WorkspaceID(ctx)
	sealed, err := s.geo.box.SealString(secretbox.GeoCredentialsMaxmindKey, ws.String(), licenseKey)
	if err != nil {
		return GeoCredentialsStatus{}, fmt.Errorf("seal maxmind key: %w", err)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).UpsertGeoCredentials(ctx, db.UpsertGeoCredentialsParams{
			WorkspaceID: ws, MaxmindAccountID: accountID, MaxmindKeyEnc: sealed,
		}); err != nil {
			return err
		}
		// The key itself is never audited.
		return audit.Record(ctx, tx, audit.Entry{
			Action: "geo.maxmind.set", ResourceType: "geo_credentials", ResourceID: ws.String(), Metadata: map[string]any{"accountId": accountID},
		})
	})
	if err != nil {
		return GeoCredentialsStatus{}, fmt.Errorf("store geo credentials: %w", err)
	}
	s.resyncMaxMind(ctx, ws)
	return s.geoStatus(ctx, s.q, ws)
}

// DeleteGeoCredentials removes the team's MaxMind account (scope
// servers:write). It refuses while a server still uses the MaxMind source.
func (s *Service) DeleteGeoCredentials(ctx context.Context) (GeoCredentialsStatus, error) {
	if err := policy.Require(ctx, policy.ScopeServersWrite, nil); err != nil {
		return GeoCredentialsStatus{}, err
	}
	ws := policy.WorkspaceID(ctx)
	st, err := s.geoStatus(ctx, s.q, ws)
	if err != nil {
		return st, err
	}
	if st.ServersUsing > 0 {
		return st, apierr.Conflict("maxmind_in_use", fmt.Sprintf("%d server(s) use MaxMind GeoLite2; switch them to DB-IP Lite first", st.ServersUsing))
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := db.New(tx).DeleteGeoCredentials(ctx, ws)
		if err != nil || n == 0 {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Action: "geo.maxmind.delete", ResourceType: "geo_credentials", ResourceID: ws.String()})
	})
	if err != nil {
		return st, fmt.Errorf("delete geo credentials: %w", err)
	}
	return s.geoStatus(ctx, s.q, ws)
}

// resyncMaxMind sends a fresh reconcile to every online server of the team on
// the MaxMind source so it picks up the new key.
func (s *Service) resyncMaxMind(ctx context.Context, ws uuid.UUID) {
	if s.hub == nil {
		return
	}
	ids, err := s.q.ListMaxMindGeoServerIDs(ctx, ws)
	if err != nil {
		s.log.WarnContext(ctx, "list maxmind servers", "err", err)
		return
	}
	for _, id := range ids {
		if err := s.hub.Resync(context.WithoutCancel(ctx), id); err != nil {
			s.log.WarnContext(ctx, "resync after maxmind key change", "server_id", id, "err", err)
		}
	}
}
