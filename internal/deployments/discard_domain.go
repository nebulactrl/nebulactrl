package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/domains"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// domainFields are the per-field suffixes of `domains.<host><path>.<field>`.
var domainFields = []string{
	"stripPrefix", "addPrefix", "kind", "redirectTo", "redirectCode", "preservePath", "tlsMode", "certificateId", "dnsResolver", "options", "authUsers", "component", "targetPort",
}

// splitDomainField splits the part after `domains.` into the domain key (host
// plus path prefix) and the changed field ("" for an added/detached domain).
func splitDomainField(rest string) (key, field string) {
	for _, f := range domainFields {
		if k, ok := strings.CutSuffix(rest, "."+f); ok {
			return k, f
		}
	}
	return rest, ""
}

// discardDomain detaches a domain added since the release, re-attaches one that
// was removed, or restores one rule field to its released value.
func (s *Service) discardDomain(ctx context.Context, tx pgx.Tx, q *db.Queries, svc db.Service, snap services.Snapshot, rest string, change *services.FieldChange) error {
	key, field := splitDomainField(rest)
	var snapDom *services.SnapshotDomain
	for i := range snap.Domains {
		if snap.Domains[i].Key() == key {
			snapDom = &snap.Domains[i]
		}
	}
	switch {
	case field == "" && change.To == "added":
		host, path := key, ""
		if snapDom != nil {
			host, path = snapDom.Hostname, snapDom.PathPrefix
		} else if live, err := q.ListServiceDomains(ctx, svc.ID); err == nil {
			for _, d := range live {
				if d.Hostname+d.PathPrefix == key {
					host, path = d.Hostname, d.PathPrefix
				}
			}
		}
		if _, err := q.DeleteServiceDomainByKey(ctx, db.DeleteServiceDomainByKeyParams{ServiceID: svc.ID, Hostname: host, PathPrefix: path}); err != nil {
			return err
		}
	case field == "" || field == "component" || field == "targetPort":
		if snapDom == nil {
			return noPendingChange("domains." + rest)
		}
		cfg := services.DecodeConfig(svc.Config)
		if svc.Kind == services.KindStack {
			if _, ok := cfg.Stack.Component(snapDom.Component); !ok {
				return apierr.Conflict("not_discardable", "the component "+snapDom.Component+" is no longer in the compose file; restore the compose file first")
			}
		} else if _, ok := cfg.Process(snapDom.Component); !ok && svc.Kind != services.KindRoute {
			return apierr.Conflict("not_discardable", "the process "+snapDom.Component+" is no longer in the configuration; restore it first")
		}
		if field != "" {
			// The component and port of a domain are fixed at creation: restoring
			// them means re-attaching the released domain.
			if _, err := q.DeleteServiceDomainByKey(ctx, db.DeleteServiceDomainByKeyParams{ServiceID: svc.ID, Hostname: snapDom.Hostname, PathPrefix: snapDom.PathPrefix}); err != nil {
				return err
			}
		}
		port := snapDom.TargetPort
		if svc.Kind != services.KindRoute && svc.Kind != services.KindStack {
			// An app's domain follows the port of its process: the service port for
			// the default process.
			if p, _ := cfg.Process(snapDom.Component); p.Port > 0 {
				port = p.Port
			}
		}
		p := db.InsertDomainParams{
			ServiceID: svc.ID, Hostname: snapDom.Hostname, TargetPort: int32(port), Component: snapDom.Component, Kind: snapDom.Kind, PathPrefix: snapDom.PathPrefix,
			StripPrefix: snapDom.StripPrefix, AddPrefix: snapDom.AddPrefix, RedirectCode: int32(snapDom.RedirectCode),
			PreservePath: snapDom.PreservePath, TlsMode: snapDom.TLSMode, DnsResolver: snapDom.DNSResolver,
		}
		if snapDom.RedirectTo != "" {
			p.RedirectTo = &snapDom.RedirectTo
		}
		if id, err := uuid.Parse(snapDom.CertificateID); err == nil {
			p.CertificateID = &id
		}
		if snapDom.Options != "" {
			p.Options = json.RawMessage(snapDom.Options)
		}
		dom, err := q.InsertDomain(ctx, p)
		if err != nil {
			if services.IsUniqueViolation(err) {
				return apierr.Conflict("domain_conflict", "the hostname "+snapDom.Hostname+snapDom.PathPrefix+" is already in use")
			}
			return err
		}
		if err := s.Jobs.InsertTx(ctx, tx, domains.DNSCheckArgs{DomainID: &dom.ID}, nil); err != nil {
			return fmt.Errorf("enqueue dns check: %w", err)
		}
	default:
		if snapDom == nil {
			return noPendingChange("domains." + rest)
		}
		if field == "authUsers" {
			return apierr.Conflict("not_discardable", "basic-auth users cannot be restored (passwords are not kept); edit the users directly")
		}
		if err := s.restoreDomainField(ctx, q, svc, *snapDom, field); err != nil {
			return err
		}
	}
	_, err := q.BumpServiceGeneration(ctx, svc.ID)
	return err
}

// restoreDomainField puts one rule field of the live domain back to the
// released value.
func (s *Service) restoreDomainField(ctx context.Context, q *db.Queries, svc db.Service, sd services.SnapshotDomain, field string) error {
	d, err := q.GetServiceDomainByKey(ctx, db.GetServiceDomainByKeyParams{ServiceID: svc.ID, Hostname: sd.Hostname, PathPrefix: sd.PathPrefix})
	if errors.Is(err, pgx.ErrNoRows) {
		return noPendingChange("domains." + sd.Key() + "." + field)
	}
	if err != nil {
		return err
	}
	p := db.UpdateDomainParams{
		ID: d.ID, Hostname: d.Hostname, PathPrefix: d.PathPrefix, StripPrefix: d.StripPrefix, AddPrefix: d.AddPrefix, Kind: d.Kind,
		RedirectTo: d.RedirectTo, RedirectCode: d.RedirectCode, PreservePath: d.PreservePath, TlsMode: d.TlsMode,
		CertificateID: d.CertificateID, Options: d.Options, DnsResolver: d.DnsResolver,
	}
	norm := sd
	if norm.Kind == "" {
		norm.Kind = agentprotocol.DomainServe
	}
	if norm.RedirectCode == 0 {
		norm.RedirectCode = 308
	}
	if norm.TLSMode == "" {
		norm.TLSMode = agentprotocol.TLSAuto
	}
	switch field {
	case "stripPrefix":
		p.StripPrefix = norm.StripPrefix
	case "addPrefix":
		p.AddPrefix = norm.AddPrefix
	case "kind":
		p.Kind = norm.Kind
		if norm.Kind != agentprotocol.DomainRedirect {
			p.RedirectTo = nil
		}
	case "redirectTo":
		if norm.RedirectTo == "" {
			p.RedirectTo = nil
		} else {
			p.RedirectTo = &norm.RedirectTo
		}
	case "redirectCode":
		p.RedirectCode = int32(norm.RedirectCode)
	case "preservePath":
		p.PreservePath = norm.PreservePath
	case "tlsMode":
		p.TlsMode = norm.TLSMode
	case "certificateId":
		p.CertificateID = nil
		if id, err := uuid.Parse(norm.CertificateID); err == nil {
			p.CertificateID = &id
		}
	case "dnsResolver":
		p.DnsResolver = norm.DNSResolver
	case "options":
		p.Options = json.RawMessage("{}")
		if norm.Options != "" {
			p.Options = json.RawMessage(norm.Options)
		}
	default:
		return noPendingChange("domains." + sd.Key() + "." + field)
	}
	_, err = q.UpdateDomain(ctx, p)
	return err
}

// resyncRoutePort re-registers the listen port of a route service from its
// (just discarded) config, keeping route_ports consistent.
func resyncRoutePort(ctx context.Context, q *db.Queries, serviceID uuid.UUID) error {
	row, err := q.LockServiceRow(ctx, serviceID)
	if err != nil {
		return err
	}
	cfg := services.DecodeConfig(row.Config)
	if row.Kind != services.KindRoute || cfg.Route == nil || row.ServerID == nil {
		return nil
	}
	if err := q.DeleteRoutePort(ctx, serviceID); err != nil {
		return err
	}
	if cfg.Route.Protocol == agentprotocol.ProtoHTTP {
		return nil
	}
	return q.InsertRoutePort(ctx, db.InsertRoutePortParams{
		ServiceID: serviceID, ServerID: *row.ServerID, Protocol: cfg.Route.Protocol, ListenPort: int32(cfg.Route.ListenPort),
	})
}
