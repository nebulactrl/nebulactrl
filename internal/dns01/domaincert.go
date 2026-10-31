package dns01

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Certificate sources of a domain.
const (
	SourceWildcard = "wildcard" // a wildcard certificate of the server covers it
	SourceDNS01    = "dns01"    // the domain's own DNS-01 resolver obtains it
	SourceACME     = "acme"     // Let's Encrypt HTTP-01 (state not observed)
	SourceCustom   = "custom"   // an uploaded certificate
	SourceNone     = "none"     // plain HTTP
)

// DomainCert is the certificate state of a domain.
type DomainCert struct {
	Source       string     `json:"source" enum:"wildcard,dns01,acme,custom,none"`
	Status       string     `json:"status" enum:"pending,active,error,unknown" doc:"unknown: this source's certificate is not observed (HTTP-01 and uploaded certificates), or DNS-01 has not started yet (see staged)"`
	Message      string     `json:"message"`
	Resolver     string     `json:"resolver" doc:"DNS-01 provider, for sources wildcard and dns01"`
	Wildcard     string     `json:"wildcard" doc:"The covering wildcard name, e.g. *.example.com"`
	WildcardID   *uuid.UUID `json:"wildcardId"`
	Issuer       string     `json:"issuer"`
	NotAfter     *time.Time `json:"notAfter"`
	DaysToExpiry *int       `json:"daysToExpiry"`
	// Warning is set when the certificate cannot be obtained as configured, for
	// now: the domain relies on HTTP-01 on a server whose proxy does not listen
	// on :80. Empty otherwise.
	Warning string `json:"warning" doc:"Why the certificate cannot be issued as configured (HTTP-01 on a proxy that does not listen on port 80); empty when there is none"`
	// Automatic, Staged and Fallback describe the automatic Cloudflare DNS-01
	// choice (cloudflare_auto.go).
	Automatic bool   `json:"automatic" doc:"The name is in a zone of the connected Cloudflare account, so NebulaCtrl obtains its certificate with DNS-01 through Cloudflare; source is dns01 and resolver cloudflare"`
	Staged    bool   `json:"staged" doc:"Automatic DNS-01 is chosen but the server's proxy does not run the Cloudflare resolver yet: Deploy proxy applies it, and the name keeps its HTTP-01 certificate until then"`
	Fallback  string `json:"fallback" doc:"Why a name in a connected Cloudflare zone uses HTTP-01 instead of DNS-01 (the DNS record has not been written by a sync yet, Cloudflare refused the last DNS change, the server has no ACME email, ...); empty when there is none"`
	// IssuedVia is the ACME storage that holds the certificate the host is served
	// with, read by the agent from the proxy's storage files.
	IssuedVia string `json:"issuedVia" enum:",http-01,dns-01" doc:"Which ACME storage holds the certificate the host is served with, read from the proxy's storage files: http-01 (issued over HTTP-01; Traefik renews it there, also after the name moved to DNS-01) or dns-01. Empty: no certificate yet or not observed. Label a certificate as DNS-01 only when this is dns-01"`
}

// DomainCerts computes the certificate state of domain rows (keyed by domain
// ID): which source serves each domain and, for DNS-01 sources, the observed
// status and expiry.
func DomainCerts(ctx context.Context, q *db.Queries, rows []db.Domain) (map[uuid.UUID]DomainCert, error) {
	out := make(map[uuid.UUID]DomainCert, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	placed, err := q.ListDomainServers(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list domain servers: %w", err)
	}
	serverOf := map[uuid.UUID]uuid.UUID{}
	var serverIDs []uuid.UUID
	for _, p := range placed {
		if p.ServerID != nil {
			serverOf[p.DomainID] = *p.ServerID
			if !slices.Contains(serverIDs, *p.ServerID) {
				serverIDs = append(serverIDs, *p.ServerID)
			}
		}
	}
	var wild map[uuid.UUID][]db.WildcardCert
	autos := map[uuid.UUID]*domainAuto{}
	if len(serverIDs) > 0 {
		all, err := q.ListWildcardCertsForServers(ctx, serverIDs)
		if err != nil {
			return nil, fmt.Errorf("list wildcard certificates: %w", err)
		}
		wild = map[uuid.UUID][]db.WildcardCert{}
		for _, w := range all {
			wild[w.ServerID] = append(wild[w.ServerID], w)
		}
		if autos, err = autoChoices(ctx, q, rows, serverOf, serverIDs); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	for _, d := range rows {
		out[d.ID] = certFor(d, wild[serverOf[d.ID]], autos[d.ID], now)
	}
	stores, err := q.ListDomainCertStores(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list certificate storages: %w", err)
	}
	for _, s := range stores {
		if c, ok := out[s.DomainID]; ok && c.Source != SourceNone && c.Source != SourceCustom && c.Source != SourceWildcard {
			c.IssuedVia = s.IssuedVia
			if c.Automatic && s.IssuedVia == agentprotocol.AcmeIssuedHTTP01 {
				c.Message = "The certificate in use was issued over HTTP-01 and is renewed there; DNS-01 through Cloudflare issues the certificates of names that have none."
			}
			out[s.DomainID] = c
		}
	}
	if err := annotateHTTP01(ctx, q, rows, serverOf, out); err != nil {
		return nil, err
	}
	return out, nil
}

// domainAuto is what the Cloudflare connection decided for one domain on its
// server.
type domainAuto struct {
	choice Choice
	// usable is false when the server cannot run the automatic resolver; why says what is missing.
	usable bool
	why    string
	// applied: the server's proxy runs a Cloudflare resolver.
	applied bool
	// tunnelled domains keep their behaviour: no certificate is requested.
	tunnelled bool
}

// autoChoices evaluates the automatic Cloudflare choice of every placed domain:
// one connection read per team, one tunnel lookup per server.
func autoChoices(ctx context.Context, q *db.Queries, rows []db.Domain, serverOf map[uuid.UUID]uuid.UUID, serverIDs []uuid.UUID) (map[uuid.UUID]*domainAuto, error) {
	srvs, err := q.LoadServerRows(ctx, serverIDs)
	if err != nil {
		return nil, fmt.Errorf("load servers: %w", err)
	}
	bySrv := make(map[uuid.UUID]serverAuto, len(srvs))
	access := map[uuid.UUID]CloudflareAccess{}
	for _, s := range srvs {
		a, ok := access[s.WorkspaceID]
		if !ok {
			if a, err = LoadCloudflare(ctx, q, s.WorkspaceID); err != nil {
				return nil, err
			}
			access[s.WorkspaceID] = a
		}
		bySrv[s.ID] = newServerAuto(s, a)
	}
	idsOf := map[uuid.UUID][]uuid.UUID{}
	for _, d := range rows {
		if sid, ok := serverOf[d.ID]; ok {
			idsOf[sid] = append(idsOf[sid], d.ID)
		}
	}
	tunnelled := map[uuid.UUID]bool{}
	for sid, ids := range idsOf {
		t, err := tunnelledSet(ctx, q, sid, ids)
		if err != nil {
			return nil, err
		}
		for id := range t {
			tunnelled[id] = true
		}
	}
	out := make(map[uuid.UUID]*domainAuto, len(rows))
	for _, d := range rows {
		sa, ok := bySrv[serverOf[d.ID]]
		if !ok || !sa.access.Connected {
			continue
		}
		applied := slices.ContainsFunc(AppliedSnapshot(sa.srv).Resolvers, func(r agentprotocol.DNSResolver) bool { return r.Provider == AutoProvider })
		out[d.ID] = &domainAuto{choice: sa.access.Choose(d.Hostname, Evidence{ZoneID: d.CfZoneID, Status: d.CfStatus, Error: d.CfError}), usable: sa.usable, why: sa.why, applied: applied, tunnelled: tunnelled[d.ID]}
	}
	return out, nil
}

func certFor(d db.Domain, wildcards []db.WildcardCert, auto *domainAuto, now time.Time) DomainCert {
	switch d.TlsMode {
	case agentprotocol.TLSNone:
		return DomainCert{Source: SourceNone, Status: "unknown"}
	case agentprotocol.TLSCustom:
		return DomainCert{Source: SourceCustom, Status: "unknown"}
	}
	for _, w := range wildcards {
		spec := agentprotocol.WildcardCert{Domain: w.Domain, IncludeApex: w.IncludeApex}
		if !agentprotocol.WildcardCovers(spec, d.Hostname) || (d.Generated && !w.CoverGenerated) {
			continue
		}
		c := DomainCert{
			Source: SourceWildcard, Status: w.Status, Message: w.StatusMessage, Resolver: w.Provider, Wildcard: "*." + w.Domain,
			WildcardID: &w.ID, Issuer: w.Issuer, NotAfter: w.NotAfter,
		}
		c.DaysToExpiry = days(w.NotAfter, now)
		return c
	}
	fallback := ""
	if auto != nil && !auto.tunnelled {
		switch {
		case auto.choice.DNS01 && auto.usable:
			c := DomainCert{
				Source: SourceDNS01, Status: d.CertStatus, Message: d.CertMessage, Resolver: AutoProvider, Issuer: d.CertIssuer, NotAfter: d.CertNotAfter,
				Automatic: true,
			}
			switch {
			case !auto.applied:
				// The proxy still serves the name over HTTP-01: nothing is observed yet.
				c.Staged, c.Status, c.Message, c.Issuer, c.NotAfter = true, "unknown", "", "", nil
			case c.Status == "":
				c.Status = agentprotocol.AcmeStatusPending
			}
			c.DaysToExpiry = days(c.NotAfter, now)
			return c
		case auto.choice.DNS01:
			fallback = auto.why
		default:
			fallback = auto.choice.Fallback
		}
	}
	if d.DnsResolver != "" {
		c := DomainCert{
			Source: SourceDNS01, Status: d.CertStatus, Message: d.CertMessage, Resolver: d.DnsResolver, Issuer: d.CertIssuer, NotAfter: d.CertNotAfter,
		}
		if c.Status == "" {
			c.Status = agentprotocol.AcmeStatusPending
		}
		c.DaysToExpiry = days(d.CertNotAfter, now)
		return c
	}
	return DomainCert{Source: SourceACME, Status: "unknown", Fallback: fallback}
}

func days(t *time.Time, now time.Time) *int {
	if t == nil {
		return nil
	}
	d := daysUntil(*t, now)
	return &d
}
