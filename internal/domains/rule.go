package domains

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/hostclaim"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// RuleInput holds the optional proxy fields of a domain, shared by create and
// update. A nil field keeps the current value (create: the default).
type RuleInput struct {
	PathPrefix  *string `json:"pathPrefix,omitempty" maxLength:"200" doc:"Serve only this path (and below), e.g. /api. Empty = the whole host"`
	StripPrefix *bool   `json:"stripPrefix,omitempty" doc:"Remove the path prefix before forwarding to the service"`
	AddPrefix   *string `json:"addPrefix,omitempty" maxLength:"200" doc:"Prepend this path before forwarding to the service"`
	Kind        *string `json:"kind,omitempty" enum:"serve,redirect" doc:"serve routes to the service; redirect answers with a redirect to redirectTo"`
	RedirectTo  *string `json:"redirectTo,omitempty" maxLength:"253" doc:"Target hostname of a redirect (not a URL)"`
	// RedirectCode is 301, 302, 307 or 308 (default 308).
	RedirectCode *int    `json:"redirectCode,omitempty" enum:"301,302,307,308"`
	PreservePath *bool   `json:"preservePath,omitempty" doc:"A redirect keeps the request path and query (default true)"`
	TLSMode      *string `json:"tlsMode,omitempty" enum:"auto,custom,none" doc:"auto = ACME or the default certificate, custom = an uploaded certificate, none = plain HTTP only"`
	// CertificateID pins an uploaded certificate of the service's server ("" clears it).
	CertificateID *string                     `json:"certificateId,omitempty" doc:"An uploaded certificate of the service's server whose names cover the hostname; empty clears the pin"`
	Options       *agentprotocol.RouteOptions `json:"options,omitempty" doc:"Replaces the whole option set"`
	// DNSResolver names the DNS-01 provider that obtains this domain's certificate ("" = automatic: HTTP-01 or a covering wildcard certificate).
	DNSResolver *string `json:"dnsResolver,omitempty" pattern:"^([a-z0-9][a-z0-9-]{0,39})?$" doc:"DNS-01 provider (a lego provider code) that obtains the certificate; the server must have a resolver for it. Empty = HTTP-01 or a covering wildcard certificate"`
	// WWWRedirect keeps a 301 redirect from the www/apex counterpart of the hostname to it.
	WWWRedirect *bool `json:"wwwRedirect,omitempty" doc:"true creates (false removes) a whole-host 301 redirect from the www/apex counterpart of this hostname (www.<host>, or the host without www.) to this domain. Both hostnames are routed and get certificates; each needs its own DNS record. Needs a whole-host serve domain (422 invalid_www_redirect)"`
}

// Any reports whether any proxy field is set (those need proxy:write).
func (in RuleInput) Any() bool {
	return in.PathPrefix != nil || in.StripPrefix != nil || in.AddPrefix != nil || in.Kind != nil || in.RedirectTo != nil ||
		in.RedirectCode != nil || in.PreservePath != nil || in.TLSMode != nil || in.CertificateID != nil || in.Options != nil || in.DNSResolver != nil ||
		in.WWWRedirect != nil
}

// spec is the resolved routing definition of a domain.
type spec struct {
	Hostname      string
	PathPrefix    string
	StripPrefix   bool
	AddPrefix     string
	Kind          string
	RedirectTo    string
	RedirectCode  int
	PreservePath  bool
	TLSMode       string
	CertificateID *uuid.UUID
	Options       agentprotocol.RouteOptions
	DNSResolver   string
}

func defaultSpec(host string) spec {
	return spec{Hostname: host, Kind: agentprotocol.DomainServe, RedirectCode: 308, PreservePath: true, TLSMode: agentprotocol.TLSAuto}
}

func specOf(d db.Domain) spec {
	sp := spec{
		Hostname: d.Hostname, PathPrefix: d.PathPrefix, StripPrefix: d.StripPrefix, AddPrefix: d.AddPrefix, Kind: d.Kind,
		RedirectCode: int(d.RedirectCode), PreservePath: d.PreservePath, TLSMode: d.TlsMode, CertificateID: d.CertificateID,
		Options: services.DomainOptions(d.Options), DNSResolver: d.DnsResolver,
	}
	if d.RedirectTo != nil {
		sp.RedirectTo = *d.RedirectTo
	}
	return sp
}

// apply overlays in on sp.
func (sp spec) apply(in RuleInput) (spec, error) {
	if in.PathPrefix != nil {
		sp.PathPrefix = *in.PathPrefix
	}
	if in.StripPrefix != nil {
		sp.StripPrefix = *in.StripPrefix
	}
	if in.AddPrefix != nil {
		sp.AddPrefix = *in.AddPrefix
	}
	if in.Kind != nil {
		sp.Kind = *in.Kind
	}
	if in.RedirectTo != nil {
		sp.RedirectTo = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(*in.RedirectTo)), ".")
	}
	if in.RedirectCode != nil {
		sp.RedirectCode = *in.RedirectCode
	}
	if in.PreservePath != nil {
		sp.PreservePath = *in.PreservePath
	}
	if in.TLSMode != nil {
		sp.TLSMode = *in.TLSMode
	}
	if in.CertificateID != nil {
		if *in.CertificateID == "" {
			sp.CertificateID = nil
		} else {
			id, err := uuid.Parse(*in.CertificateID)
			if err != nil {
				return sp, apierr.Unprocessable("invalid_certificate", "certificateId must be a UUID")
			}
			sp.CertificateID = &id
		}
	}
	if in.Options != nil {
		sp.Options = *in.Options
	}
	if in.DNSResolver != nil {
		sp.DNSResolver = *in.DNSResolver
	}
	return sp, nil
}

func (sp spec) optionsJSON() json.RawMessage {
	b, _ := json.Marshal(sp.Options)
	return b
}

func (sp spec) redirectTo() *string {
	if sp.Kind != agentprotocol.DomainRedirect {
		return nil
	}
	return &sp.RedirectTo
}

// validate checks the rule shape and everything that needs the database:
// redirect loops, pinned certificate coverage and the basic-auth user count.
// selfID is the domain being updated (uuid.Nil on create).
func (s *Service) validate(ctx context.Context, q *db.Queries, sc services.Scope, srv *db.Server, sp spec, selfID uuid.UUID) error {
	bad := func(code, format string, a ...any) error {
		return apierr.Unprocessable(code, fmt.Sprintf(format, a...))
	}
	if systemroute.Reserved(sp.Hostname) {
		return apierr.Conflict("domain_conflict", "the hostname "+sp.Hostname+" is the dashboard's own address")
	}
	switch owner, err := q.StatusPageHostnameOwner(ctx, strings.ToLower(sp.Hostname)); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("check status page hostnames: %w", err)
	case owner.WorkspaceID == sc.WorkspaceID:
		// The team's own page: naming it reveals nothing the team cannot see.
		return apierr.Conflict("domain_conflict", "the hostname "+sp.Hostname+" is used by the status page "+owner.Title)
	default:
		return hostclaim.Unavailable("domain_conflict", sp.Hostname)
	}
	if err := s.checkHostClaim(ctx, q, sc.WorkspaceID, sp.Hostname, selfID); err != nil {
		return err
	}
	if err := agentprotocol.ValidPathPrefix(sp.PathPrefix); err != nil {
		return bad("invalid_path_prefix", "%v", err)
	}
	if sp.AddPrefix != "" {
		if err := agentprotocol.ValidPathPrefix(sp.AddPrefix); err != nil {
			return bad("invalid_path_prefix", "addPrefix: %v", err)
		}
	}
	if sp.StripPrefix && sp.PathPrefix == "" {
		return bad("invalid_path_prefix", "stripPrefix needs a pathPrefix")
	}
	switch sp.Kind {
	case agentprotocol.DomainServe:
		if sp.RedirectTo != "" {
			return bad("invalid_redirect", "redirectTo applies to redirect domains only")
		}
	case agentprotocol.DomainRedirect:
		to, err := ValidateHostname(sp.RedirectTo)
		if err != nil {
			return bad("invalid_redirect", "redirectTo must be a valid hostname")
		}
		if to == sp.Hostname {
			return bad("invalid_redirect", "a domain cannot redirect to itself")
		}
		switch sp.RedirectCode {
		case 301, 302, 307, 308:
		default:
			return bad("invalid_redirect", "redirectCode must be 301, 302, 307 or 308")
		}
		if sp.StripPrefix || sp.AddPrefix != "" || !sp.Options.IsZero() {
			return bad("invalid_redirect", "a redirect domain takes no strip/add prefix and no options")
		}
		if sp.PathPrefix == "" {
			if err := s.checkRedirectLoop(ctx, q, sc.WorkspaceID, selfID, sp.Hostname, to); err != nil {
				return err
			}
		}
	default:
		return bad("invalid_kind", "kind must be serve or redirect")
	}
	switch sp.TLSMode {
	case agentprotocol.TLSAuto, agentprotocol.TLSCustom, agentprotocol.TLSNone:
	default:
		return bad("invalid_tls_mode", "tlsMode must be auto, custom or none")
	}
	if err := s.validateDNS01(ctx, q, srv, sp); err != nil {
		return err
	}
	if sp.CertificateID != nil {
		if sp.TLSMode != agentprotocol.TLSCustom {
			return bad("invalid_tls_mode", "pin a certificate only with tlsMode custom")
		}
		if srv == nil {
			return bad("no_server", "assign the service to a server before pinning a certificate")
		}
		cert, err := q.GetDomainCertificate(ctx, db.GetDomainCertificateParams{ID: *sp.CertificateID, ServerID: srv.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return bad("certificate_mismatch", "the certificate does not exist on %s", srv.Name)
		}
		if err != nil {
			return fmt.Errorf("get certificate: %w", err)
		}
		if !CertCovers(cert.Domains, sp.Hostname) {
			return bad("certificate_mismatch", "the certificate does not cover %s", sp.Hostname)
		}
	}
	if err := sp.Options.Validate(); err != nil {
		return bad("invalid_options", "%v", err)
	}
	// A domain served through the server's Cloudflare Tunnel is reached by the
	// connector, never by the visitor: its peer address says nothing about who is
	// asking, so an IP rule on it cannot be enforced (see withTunnelAccess).
	if a := sp.Options.Access; a != nil && (len(a.AllowCIDRs) > 0 || len(a.DenyCIDRs) > 0) && selfID != uuid.Nil && srv != nil {
		tunnelled, err := q.ListTunnelledDomainIDs(ctx, db.ListTunnelledDomainIDsParams{DomainIds: []uuid.UUID{selfID}, ServerID: srv.ID})
		if err != nil {
			return fmt.Errorf("check tunnelled domain: %w", err)
		}
		if len(tunnelled) > 0 {
			return bad("ip_rules_on_tunnel", "%s is served through a Cloudflare Tunnel, which hides the visitor's address: IP allow and deny rules cannot be enforced on it. Remove the IP rules (or turn the tunnel off for this server)", sp.Hostname)
		}
	}
	if sp.Options.BasicAuth {
		if selfID == uuid.Nil {
			return bad("basic_auth_needs_users", "add basic-auth users first, then enable basicAuth")
		}
		n, err := q.CountDomainAuthUsers(ctx, selfID)
		if err != nil {
			return fmt.Errorf("count auth users: %w", err)
		}
		if n == 0 {
			return bad("basic_auth_needs_users", "basicAuth needs at least one user: PUT /domains/{id}/auth-users first")
		}
	}
	return nil
}

// checkRedirectLoop rejects a whole-host redirect host → to that closes a cycle
// with the workspace's other whole-host redirects.
func (s *Service) checkRedirectLoop(ctx context.Context, q *db.Queries, workspaceID, selfID uuid.UUID, host, to string) error {
	rows, err := q.ListWorkspaceRedirectDomains(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("list redirects: %w", err)
	}
	next := map[string]string{}
	for _, r := range rows {
		if r.ID != selfID && r.RedirectTo != nil {
			next[r.Hostname] = *r.RedirectTo
		}
	}
	cur := to
	for range 25 {
		if cur == host {
			return apierr.Unprocessable("redirect_loop", "this redirect would loop: "+host+" → … → "+host)
		}
		n, ok := next[cur]
		if !ok {
			return nil
		}
		cur = n
	}
	return apierr.Unprocessable("redirect_loop", "the redirect chain from "+host+" is too long")
}

// CertCovers reports whether any certificate name (exact or `*.` wildcard of
// exactly one label) covers host.
func CertCovers(names []string, host string) bool {
	host = strings.ToLower(host)
	for _, n := range names {
		n = strings.ToLower(n)
		if n == host {
			return true
		}
		if rest, ok := strings.CutPrefix(n, "*."); ok {
			if label, parent, found := strings.Cut(host, "."); found && label != "" && parent == rest {
				return true
			}
		}
	}
	return false
}

// authorize requires proxy:write (and a proxy-v2 agent) when the input
// touches any proxy field.
func (s *Service) authorize(ctx context.Context, sc services.Scope, srv *db.Server, in RuleInput) error {
	if !in.Any() {
		return nil
	}
	if err := policy.Require(ctx, policy.ScopeProxyWrite, &sc.ProjectID); err != nil {
		return err
	}
	return RequireProxyV2(srv)
}

// changedFields names the fields that differ between two specs (audit metadata).
func changedFields(a, b spec) []string {
	var out []string
	add := func(name string, differ bool) {
		if differ {
			out = append(out, name)
		}
	}
	add("hostname", a.Hostname != b.Hostname)
	add("pathPrefix", a.PathPrefix != b.PathPrefix)
	add("stripPrefix", a.StripPrefix != b.StripPrefix)
	add("addPrefix", a.AddPrefix != b.AddPrefix)
	add("kind", a.Kind != b.Kind)
	add("redirectTo", a.RedirectTo != b.RedirectTo)
	add("redirectCode", a.RedirectCode != b.RedirectCode)
	add("preservePath", a.PreservePath != b.PreservePath)
	add("tlsMode", a.TLSMode != b.TLSMode)
	add("certificateId", !equalUUIDPtr(a.CertificateID, b.CertificateID))
	ja, jb := services.NormalizeOptions(a.optionsJSON()), services.NormalizeOptions(b.optionsJSON())
	add("options", ja != jb)
	add("dnsResolver", a.DNSResolver != b.DNSResolver)
	return slices.Clip(out)
}

func equalUUIDPtr(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
