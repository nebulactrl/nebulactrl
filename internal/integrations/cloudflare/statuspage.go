package cloudflare

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
)

// The DNS of a status page's hostname: the same status, settings and fix as a
// service domain's (dns.go), for a page of the caller's team. Reads need
// projects:read; changes need what changing a status page needs: the admin
// scope and a token not restricted to projects.

// loadStatusPage authorizes the call and returns the page's name (404 for pages
// of other teams, 422 while the page has no hostname).
func (s *Service) loadStatusPage(ctx context.Context, write bool, id uuid.UUID) (*dnsName, error) {
	scope := policy.ScopeProjectsRead
	if write {
		scope = policy.ScopeAdmin
	}
	if err := policy.Require(ctx, scope, nil); err != nil {
		return nil, err
	}
	p, ok := auth.PrincipalFrom(ctx)
	if !ok {
		return nil, apierr.Unauthorized()
	}
	if write && p.Restricted() {
		return nil, apierr.Forbidden()
	}
	row, err := s.q.GetStatusPage(ctx, db.GetStatusPageParams{ID: id, WorkspaceID: p.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.NotFound("status page")
	}
	if err != nil {
		return nil, fmt.Errorf("get status page: %w", err)
	}
	if row.Hostname == nil || *row.Hostname == "" {
		return nil, apierr.Unprocessable("no_hostname", "the page has no hostname yet")
	}
	return pageName(row), nil
}

// StatusPageStatus classifies the page's hostname against the zone right now (no changes).
func (s *Service) StatusPageStatus(ctx context.Context, pageID uuid.UUID) (DNSStatus, error) {
	n, err := s.loadStatusPage(ctx, false, pageID)
	if err != nil {
		return DNSStatus{}, err
	}
	return s.statusOf(ctx, OwnerStatusPage, n.ID)
}

// SetStatusPageSettings changes the proxy and origin lock settings of the
// page's DNS, then syncs. A failed sync is reported in the status (and
// retried by a job); the setting is kept.
func (s *Service) SetStatusPageSettings(ctx context.Context, pageID uuid.UUID, in SettingsInput) (DNSStatus, error) {
	n, err := s.loadStatusPage(ctx, true, pageID)
	if err != nil {
		return DNSStatus{}, err
	}
	return s.setSettings(ctx, n, in)
}

// FixStatusPage makes the zone match the page: it creates or corrects the
// records of the control-plane server's public IPs. A record NebulaCtrl did not
// create is only replaced with adopt (409 adoption_required otherwise).
func (s *Service) FixStatusPage(ctx context.Context, pageID uuid.UUID, adopt bool) (DNSStatus, error) {
	n, err := s.loadStatusPage(ctx, true, pageID)
	if err != nil {
		return DNSStatus{}, err
	}
	return s.fix(ctx, n, adopt)
}
