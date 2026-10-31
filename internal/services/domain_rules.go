package services

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/dns01"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// DomainOptions decodes domains.options (corrupt JSON yields no options).
func DomainOptions(raw json.RawMessage) agentprotocol.RouteOptions {
	var o agentprotocol.RouteOptions
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o
}

// NormalizeOptions returns the canonical compact JSON of an options document
// ("" when no option is set), so snapshots and live rows compare equal.
func NormalizeOptions(raw json.RawMessage) string {
	o := DomainOptions(raw)
	if o.IsZero() {
		return ""
	}
	b, _ := json.Marshal(o)
	return string(b)
}

// ToDomainView converts a domain row. AuthUsers is empty: DomainViews fills it.
func ToDomainView(d db.Domain) DomainView {
	v := DomainView{
		ID: d.ID, Hostname: d.Hostname, TargetPort: int(d.TargetPort), Component: d.Component, Generated: d.Generated, Status: d.Status,
		StatusMessage: d.StatusMessage, DNSOk: d.DnsOk, DNSCheckedAt: d.DnsCheckedAt, CreatedAt: d.CreatedAt,
		PathPrefix: d.PathPrefix, StripPrefix: d.StripPrefix, AddPrefix: d.AddPrefix, Kind: d.Kind,
		RedirectCode: int(d.RedirectCode), PreservePath: d.PreservePath, TLSMode: d.TlsMode, CertificateID: d.CertificateID,
		Options: DomainOptions(d.Options), AuthUsers: []string{}, DNSResolver: d.DnsResolver,
	}
	if d.CfZoneID != "" || d.CfStatus != "" {
		v.DNS = &DomainDNS{
			Provider: "cloudflare", ZoneID: d.CfZoneID, ZoneName: d.CfZoneName, Manage: d.CfZoneID != "", Proxied: d.CfProxied,
			Status: d.CfStatus, Message: d.CfMessage, Error: d.CfError, CheckedAt: d.CfCheckedAt,
		}
	}
	if d.RedirectTo != nil {
		v.RedirectTo = *d.RedirectTo
	}
	return v
}

// DomainViews renders domain rows (same order) including their basic-auth user names.
// An app's process lands in DomainView.Process; Component stays the compose component of stacks.
func DomainViews(ctx context.Context, q *db.Queries, rows []db.Domain) ([]DomainView, error) {
	out := make([]DomainView, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	users, err := DomainAuthUsernames(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	certs, err := dns01.DomainCerts(ctx, q, rows)
	if err != nil {
		return nil, err
	}
	siblings, err := q.ListDomainsForServices(ctx, serviceIDsOf(rows))
	if err != nil {
		return nil, fmt.Errorf("list sibling domains: %w", err)
	}
	// domains.component is a compose component for a stack and a process name for
	// every other service: the view says which.
	var kinds map[uuid.UUID]string
	for _, r := range rows {
		if r.Component == "" {
			continue
		}
		svcs, err := q.ListServicesIncludingDeleted(ctx, serviceIDsOf(rows))
		if err != nil {
			return nil, fmt.Errorf("list services: %w", err)
		}
		kinds = make(map[uuid.UUID]string, len(svcs))
		for _, s := range svcs {
			kinds[s.ID] = s.Kind
		}
		break
	}
	for i, r := range rows {
		out[i] = ToDomainView(r)
		if r.Component != "" && kinds[r.ServiceID] != KindStack {
			out[i].Process, out[i].Component = r.Component, ""
		}
		if u := users[r.ID]; len(u) > 0 {
			out[i].AuthUsers = u
		}
		if c, ok := certs[r.ID]; ok {
			out[i].Cert = &c
		}
	}
	annotateWWW(out, rows, siblings)
	return out, nil
}

func serviceIDsOf(rows []db.Domain) []uuid.UUID {
	var ids []uuid.UUID
	for _, r := range rows {
		if !slices.Contains(ids, r.ServiceID) {
			ids = append(ids, r.ServiceID)
		}
	}
	return ids
}

// DomainAuthUsernames returns the basic-auth user names per domain.
func DomainAuthUsernames(ctx context.Context, q *db.Queries, domainIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	rows, err := q.ListAuthUsernamesForDomains(ctx, domainIDs)
	if err != nil {
		return nil, fmt.Errorf("list auth users: %w", err)
	}
	out := map[uuid.UUID][]string{}
	for _, r := range rows {
		out[r.DomainID] = append(out[r.DomainID], r.Username)
	}
	return out, nil
}

// SnapshotDomainOf freezes a domain row for a release snapshot.
func SnapshotDomainOf(d db.Domain, authUsers []string) SnapshotDomain {
	s := SnapshotDomain{
		Hostname: d.Hostname, TargetPort: int(d.TargetPort), Component: d.Component, PathPrefix: d.PathPrefix, StripPrefix: d.StripPrefix,
		AddPrefix: d.AddPrefix, Kind: d.Kind, RedirectCode: int(d.RedirectCode), PreservePath: d.PreservePath, TLSMode: d.TlsMode,
		Options: NormalizeOptions(d.Options), AuthUsers: authUsers, DNSResolver: d.DnsResolver,
	}
	if d.RedirectTo != nil {
		s.RedirectTo = *d.RedirectTo
	}
	if d.CertificateID != nil {
		s.CertificateID = d.CertificateID.String()
	}
	return s
}

// Key identifies a domain across snapshot and live rows: host plus path prefix.
func (d SnapshotDomain) Key() string { return d.Hostname + d.PathPrefix }

// normalized fills the defaults of snapshots written before proxy v2.
func (d SnapshotDomain) normalized() SnapshotDomain {
	if d.Kind == "" {
		d.Kind = agentprotocol.DomainServe
	}
	if d.RedirectCode == 0 {
		d.RedirectCode = 308
	}
	if d.TLSMode == "" {
		d.TLSMode = agentprotocol.TLSAuto
	}
	if d.Kind != agentprotocol.DomainRedirect {
		d.RedirectTo, d.RedirectCode, d.PreservePath = "", 308, false
	}
	return d
}

// diffDomains lists the domain changes between a release snapshot and the live
// rows: `domains.<host><path>` added/attached, and `domains.<host><path>.<field>`
// for changed rule fields (options as compact JSON, auth users as names).
func diffDomains(snap []SnapshotDomain, cur []db.Domain, users map[uuid.UUID][]string) []FieldChange {
	old := map[string]SnapshotDomain{}
	for _, d := range snap {
		old[d.Key()] = d
	}
	var out []FieldChange
	live := map[string]bool{}
	for _, row := range cur {
		now := SnapshotDomainOf(row, users[row.ID])
		key := now.Key()
		live[key] = true
		prev, ok := old[key]
		if !ok {
			out = append(out, FieldChange{Field: "domains." + key, From: "", To: "added"})
			continue
		}
		a, b := prev.normalized(), now.normalized()
		field := func(name, from, to string) {
			if from != to {
				out = append(out, FieldChange{Field: "domains." + key + "." + name, From: from, To: to})
			}
		}
		if b.Component != "" || a.Component != "" {
			field("component", a.Component, b.Component)
			field("targetPort", strconv.Itoa(a.TargetPort), strconv.Itoa(b.TargetPort))
		}
		field("stripPrefix", strconv.FormatBool(a.StripPrefix), strconv.FormatBool(b.StripPrefix))
		field("addPrefix", a.AddPrefix, b.AddPrefix)
		field("kind", a.Kind, b.Kind)
		field("redirectTo", a.RedirectTo, b.RedirectTo)
		if b.Kind == agentprotocol.DomainRedirect {
			field("redirectCode", strconv.Itoa(a.RedirectCode), strconv.Itoa(b.RedirectCode))
			field("preservePath", strconv.FormatBool(a.PreservePath), strconv.FormatBool(b.PreservePath))
		}
		field("tlsMode", a.TLSMode, b.TLSMode)
		field("certificateId", a.CertificateID, b.CertificateID)
		field("dnsResolver", a.DNSResolver, b.DNSResolver)
		field("options", a.Options, b.Options)
		sa, sb := slices.Clone(a.AuthUsers), slices.Clone(b.AuthUsers)
		sort.Strings(sa)
		sort.Strings(sb)
		field("authUsers", strings.Join(sa, ", "), strings.Join(sb, ", "))
	}
	for _, d := range snap {
		if !live[d.Key()] {
			out = append(out, FieldChange{Field: "domains." + d.Key(), From: "attached", To: ""})
		}
	}
	return out
}
