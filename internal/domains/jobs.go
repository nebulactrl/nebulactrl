package domains

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dnscheck"
	"github.com/nebulactrl/nebulactrl/internal/jobs"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DNSCheckInterval is how often every domain is re-checked.
const DNSCheckInterval = 10 * time.Minute

const dnsLookupTimeout = 10 * time.Second // the authoritative servers, then the public resolvers, then the host's

// DNSCheckArgs checks the DNS of one domain, or of every domain when DomainID is nil.
type DNSCheckArgs struct {
	DomainID *uuid.UUID `json:"domainId,omitempty"`
}

// Kind implements river.JobArgs.
func (DNSCheckArgs) Kind() string { return "domain_dns_check" }

// Resolver looks up A records. dnscheck.Resolver asks the zone's authoritative
// nameservers, so the host's cached "no such name" does not show.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// DNSChecker compares a domain's A records with its server's public IP.
type DNSChecker struct {
	q *db.Queries
	// (no event bus: onChange publishes)
	resolver Resolver
	logger   *slog.Logger
	onChange func(ctx context.Context, domainID uuid.UUID)
	// match gives a domain created without Service.create (template install,
	// preview, route service, discard) its Cloudflare zone and first DNS sync.
	match func(ctx context.Context, domainID uuid.UUID) error
}

type dnsWorker struct {
	river.WorkerDefaults[DNSCheckArgs]
	c *DNSChecker
}

// RegisterJobs adds the DNS check worker and its 10-minute sweep.
func RegisterJobs(jm *jobs.Manager, svc *Service, logger *slog.Logger) {
	c := &DNSChecker{q: svc.q, resolver: dnscheck.New(), logger: logger}
	if svc.dns != nil {
		c.match = func(ctx context.Context, id uuid.UUID) error {
			return db.InTx(ctx, svc.pool, func(tx pgx.Tx) error { return svc.dns.DomainCreated(ctx, tx, id) })
		}
	}
	c.onChange = func(ctx context.Context, id uuid.UUID) { svc.publishDomain(ctx, id) }
	jm.AddWorker(func(w *river.Workers) { river.AddWorker(w, &dnsWorker{c: c}) })
	jm.AddPeriodic(river.NewPeriodicJob(
		river.PeriodicInterval(DNSCheckInterval),
		func() (river.JobArgs, *river.InsertOpts) { return DNSCheckArgs{}, nil },
		&river.PeriodicJobOpts{ID: "domain_dns_sweep"},
	))
}

func (svc *Service) publishDomain(ctx context.Context, domainID uuid.UUID) {
	dom, err := svc.q.GetDomainByID(ctx, domainID)
	if err != nil {
		return
	}
	sc, err := services.LoadScopeInternal(ctx, svc.q, dom.ServiceID)
	if err != nil {
		return
	}
	services.Publish(svc.bus, services.EventDomainUpdated, sc.ProjectID, sc.EnvironmentID, domainID)
}

func (w *dnsWorker) Work(ctx context.Context, job *river.Job[DNSCheckArgs]) error {
	if job.Args.DomainID != nil {
		// A check of one domain follows a change or a Recheck: skip cached lookups.
		return w.c.Check(dnscheck.Fresh(ctx), *job.Args.DomainID)
	}
	ids, err := w.c.q.ListAllDomainIDs(ctx)
	if err != nil {
		return fmt.Errorf("list domains: %w", err)
	}
	for _, id := range ids {
		if err := w.c.Check(ctx, id); err != nil {
			w.c.logger.WarnContext(ctx, "domain dns check failed", "domain_id", id, "err", err)
		}
	}
	return nil
}

// Check resolves the domain and records dns_ok / guidance. dns_ok stays null
// when the server has no public IP (or the hostname is local).
func (c *DNSChecker) Check(ctx context.Context, domainID uuid.UUID) error {
	info, err := c.q.GetDomainDNSInfo(ctx, domainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted meanwhile
	}
	if err != nil {
		return fmt.Errorf("load domain: %w", err)
	}
	if c.match != nil && info.Domain.CfZoneID == "" && info.Domain.CfStatus == "" {
		// Never looked at by the DNS integration: match it against the team's zones.
		if err := c.match(ctx, domainID); err != nil {
			c.logger.WarnContext(ctx, "cloudflare zone match failed", "domain_id", domainID.String(), "err", err)
		} else if again, err := c.q.GetDomainDNSInfo(ctx, domainID); err == nil {
			info = again
		}
	}
	host := info.Domain.Hostname
	var ok *bool
	var msg string
	if d := info.Domain; d.CfZoneID != "" {
		// The hostname is in a connected Cloudflare zone: NebulaCtrl keeps its record
		// there and the API, not a lookup, says whether it is right. A proxied record
		// resolves to Cloudflare's addresses, so a lookup says nothing, and the
		// guidance to create a record by hand never applies.
		ok, msg = cloudflareDNS(d)
	} else {
		ok, msg = c.evaluate(ctx, host, info.PublicIp)
	}
	if err := c.q.SetDomainDNS(ctx, db.SetDomainDNSParams{ID: domainID, DnsOk: ok, StatusMessage: msg}); err != nil {
		return fmt.Errorf("store dns result: %w", err)
	}
	changed := (info.Domain.DnsOk == nil) != (ok == nil) || (ok != nil && info.Domain.DnsOk != nil && *ok != *info.Domain.DnsOk)
	if c.onChange != nil && changed {
		c.onChange(ctx, domainID)
	}
	return nil
}

// cloudflareDNS is the DNS result of a domain in a connected Cloudflare zone,
// from the status its record was last synced to.
func cloudflareDNS(d db.Domain) (*bool, string) {
	t, f := true, false
	switch d.CfStatus {
	case "managed", "present":
		if d.CfProxied {
			return &t, "DNS is proxied through Cloudflare."
		}
		return &t, "DNS points to this server through Cloudflare."
	case "conflict":
		msg := d.CfMessage
		if msg == "" {
			msg = "A record that NebulaCtrl did not create is in the way. Replace it with NebulaCtrl's record on the domain's DNS panel."
		}
		return &f, msg
	case "missing", "wrong_target":
		return &f, "NebulaCtrl is setting up this record in your Cloudflare zone " + d.CfZoneName + "."
	}
	return nil, "NebulaCtrl checks this record in your Cloudflare zone " + d.CfZoneName + "."
}

func (c *DNSChecker) evaluate(ctx context.Context, host string, publicIP *string) (*bool, string) {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, "Local hostname: no DNS record is needed."
	}
	if publicIP == nil || strings.TrimSpace(*publicIP) == "" {
		return nil, DNSGuidance(host, nil)
	}
	want := strings.TrimSpace(*publicIP)
	lctx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
	defer cancel()
	// A wildcard host cannot be looked up: ask for a label under it instead.
	addrs, err := c.resolver.LookupIPAddr(lctx, agentprotocol.ProbeHost(host))
	f, t := false, true
	if err != nil {
		return &f, DNSGuidance(host, publicIP) + " (no A record found yet)"
	}
	var got []string
	for _, a := range addrs {
		if a.IP.To4() != nil {
			got = append(got, a.IP.String())
		}
	}
	sort.Strings(got)
	if slices.Contains(got, want) {
		return &t, "DNS points to " + want + "."
	}
	if len(got) == 0 {
		return &f, DNSGuidance(host, publicIP) + " (no A record found yet)"
	}
	return &f, DNSGuidance(host, publicIP) + " (currently resolves to " + strings.Join(got, ", ") + ")"
}
