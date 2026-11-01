package domains

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/hostclaim"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// wwwRedirectCode is the status of the www/apex redirect: permanent, so
// browsers and search engines settle on the canonical host.
const wwwRedirectCode = 301

// A www redirect is not a separate object: it is a whole-host redirect domain
// on the service whose hostname is the www/apex counterpart of a serve domain
// and whose target is that domain (services.IsWWWCompanion). Both hostnames are
// therefore routed (the proxy requests a certificate for each), the pending
// changes, release snapshots and discard keep working per domain row, and the
// toggle only creates, keeps in step or removes that row.

type wwwAction int

const (
	wwwNone wwwAction = iota
	wwwCreate
	wwwSync
	wwwDelete
)

func (a wwwAction) String() string {
	switch a {
	case wwwCreate:
		return "enabled"
	case wwwSync:
		return "synced"
	case wwwDelete:
		return "disabled"
	}
	return ""
}

// wwwPlan is what a create or update has to do about the www redirect.
type wwwPlan struct {
	action    wwwAction
	spec      spec // the redirect domain's rule (create, sync)
	companion db.Domain
}

// wwwSpec derives the rule of the redirect domain for the canonical (serve)
// domain canon and validates it like any redirect domain: loops, certificate
// coverage and DNS-01 resolver. existing is the current redirect rule, whose
// status code and path handling are kept.
func (s *Service) wwwSpec(ctx context.Context, q *db.Queries, sc services.Scope, srv *db.Server, canon spec, existing *spec, selfID uuid.UUID) (spec, error) {
	bad := func(msg string) error { return apierr.Unprocessable("invalid_www_redirect", msg) }
	if canon.Kind != agentprotocol.DomainServe || canon.PathPrefix != "" {
		return spec{}, bad("a www redirect needs a whole-host serve domain without a path prefix; turn the redirect off first")
	}
	raw, ok := services.WWWPartner(canon.Hostname)
	if !ok {
		return spec{}, bad(canon.Hostname + " has no www/apex counterpart (wildcards and bare labels cannot redirect)")
	}
	partner, err := ValidateHostname(raw)
	if err != nil {
		return spec{}, bad(canon.Hostname + " has no valid www/apex counterpart: " + err.Error())
	}
	cs := defaultSpec(partner)
	if existing != nil {
		cs = *existing
	} else {
		cs.RedirectCode = wwwRedirectCode
	}
	cs.Hostname, cs.Kind, cs.PathPrefix = partner, agentprotocol.DomainRedirect, ""
	cs.RedirectTo = canon.Hostname
	// The counterpart's certificate follows the canonical domain: the same TLS
	// mode, pinned certificate and DNS-01 provider.
	cs.TLSMode, cs.CertificateID, cs.DNSResolver = canon.TLSMode, canon.CertificateID, canon.DNSResolver
	if err := s.validate(ctx, q, sc, srv, cs, selfID); err != nil {
		return spec{}, err
	}
	return cs, nil
}

// planWWW decides what the request means for the redirect of the canonical
// domain whose new rule is canon. want is the request's wwwRedirect (nil =
// leave as is); a redirect that exists is kept in step with the domain.
func (s *Service) planWWW(ctx context.Context, q *db.Queries, sc services.Scope, srv *db.Server, canon spec, want *bool, comp db.Domain, have bool) (wwwPlan, error) {
	on := have
	if want != nil {
		on = *want
	}
	switch {
	case !on && !have:
		return wwwPlan{}, nil
	case !on:
		return wwwPlan{action: wwwDelete, companion: comp}, nil
	}
	var existing *spec
	if have {
		e := specOf(comp)
		existing = &e
	}
	cs, err := s.wwwSpec(ctx, q, sc, srv, canon, existing, comp.ID)
	if err != nil {
		return wwwPlan{}, err
	}
	if !have {
		return wwwPlan{action: wwwCreate, spec: cs}, nil
	}
	if len(changedFields(*existing, cs)) == 0 {
		return wwwPlan{}, nil
	}
	return wwwPlan{action: wwwSync, spec: cs, companion: comp}, nil
}

// insertCompanion creates the redirect domain of canon inside the caller's
// transaction. It routes to the same component and port as canon so a
// Compose stack's route table stays one route per component.
func (s *Service) insertCompanion(ctx context.Context, tx pgx.Tx, sc services.Scope, canon db.Domain, cs spec, publicIP *string) error {
	q := db.New(tx)
	comp, err := q.InsertDomain(ctx, db.InsertDomainParams{
		ServiceID: sc.Service.ID, Hostname: cs.Hostname, TargetPort: canon.TargetPort, Generated: canon.Generated,
		StatusMessage: DNSGuidance(cs.Hostname, publicIP), Kind: cs.Kind, RedirectTo: cs.redirectTo(),
		RedirectCode: int32(cs.RedirectCode), PreservePath: cs.PreservePath, TlsMode: cs.TLSMode, CertificateID: cs.CertificateID,
		Options: cs.optionsJSON(), DnsResolver: cs.DNSResolver, Component: canon.Component,
	})
	if services.IsUniqueViolation(err) {
		return hostclaim.Unavailable("domain_conflict", cs.Hostname)
	}
	if err != nil {
		return fmt.Errorf("insert www redirect: %w", err)
	}
	if err := s.jm.InsertTx(ctx, tx, DNSCheckArgs{DomainID: &comp.ID}, nil); err != nil {
		return fmt.Errorf("enqueue dns check: %w", err)
	}
	if s.dns != nil {
		if err := s.dns.DomainCreated(ctx, tx, comp.ID); err != nil {
			return fmt.Errorf("dns hook: %w", err)
		}
	}
	return nil
}

// syncCompanion rewrites the redirect domain to cs (a renamed or re-certified canonical domain).
func (s *Service) syncCompanion(ctx context.Context, tx pgx.Tx, comp db.Domain, cs spec, publicIP *string) error {
	hostChanged := comp.Hostname != cs.Hostname
	_, err := db.New(tx).UpdateDomain(ctx, db.UpdateDomainParams{
		ID: comp.ID, Hostname: cs.Hostname, Kind: cs.Kind, RedirectTo: cs.redirectTo(), RedirectCode: int32(cs.RedirectCode),
		PreservePath: cs.PreservePath, TlsMode: cs.TLSMode, CertificateID: cs.CertificateID, Options: cs.optionsJSON(),
		DnsResolver: cs.DNSResolver, ResetDns: hostChanged, StatusMessage: DNSGuidance(cs.Hostname, publicIP),
	})
	if services.IsUniqueViolation(err) {
		return hostclaim.Unavailable("domain_conflict", cs.Hostname)
	}
	if err != nil {
		return fmt.Errorf("update www redirect: %w", err)
	}
	if hostChanged {
		if err := s.jm.InsertTx(ctx, tx, DNSCheckArgs{DomainID: &comp.ID}, nil); err != nil {
			return fmt.Errorf("enqueue dns check: %w", err)
		}
		if s.dns != nil {
			if err := s.dns.DomainRenamed(ctx, tx, comp.ID); err != nil {
				return fmt.Errorf("dns hook: %w", err)
			}
		}
	}
	return nil
}

// deleteCompanion removes the redirect domain.
func (s *Service) deleteCompanion(ctx context.Context, tx pgx.Tx, sc services.Scope, comp db.Domain) error {
	if _, err := db.New(tx).DeleteDomain(ctx, comp.ID); err != nil {
		return fmt.Errorf("delete www redirect: %w", err)
	}
	if s.dns != nil {
		if err := s.dns.DomainsRemoved(ctx, tx, sc.WorkspaceID); err != nil {
			return fmt.Errorf("dns hook: %w", err)
		}
	}
	return nil
}
