package dns01

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
)

// HTTP01Warning is the message for a domain whose certificate comes from Let's
// Encrypt HTTP-01 on a server whose proxy listens on httpPort. Let's Encrypt
// validates HTTP-01 on port 80 only, so a proxy on another port (for example
// next to another proxy that holds :80) can never obtain the certificate. It
// returns "" for port 80 (and for an unknown port, 0). The caller decides that
// the domain depends on HTTP-01: TLS mode auto, no DNS-01 resolver, no covering
// wildcard or uploaded certificate, not served through a Cloudflare Tunnel.
func HTTP01Warning(httpPort int, host string) string {
	if httpPort <= 0 || httpPort == 80 {
		return ""
	}
	return fmt.Sprintf("This server's proxy listens on :%d, not :80: Let's Encrypt HTTP-01 cannot validate %s. Use a DNS-01 resolver (Servers, Proxy, DNS-01) for this domain.", httpPort, host)
}

// ServerHTTPPort is the HTTP host port a server's proxy listens on: the port
// the agent reports (0 = none yet), else the applied server setting, else 80.
func ServerHTTPPort(reported int32, applied *int32) int {
	switch {
	case reported > 0:
		return int(reported)
	case applied != nil && *applied > 0:
		return int(*applied)
	}
	return 80
}

// certCovers reports whether any certificate name (exact, or a `*.` wildcard of
// exactly one label) covers host.
func certCovers(names []string, host string) bool {
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

// http01Domains narrows the domains placed on a server to those that depend on
// HTTP-01: the certificate source is ACME, no uploaded certificate covers the
// host and the domain is not served through the server's Cloudflare Tunnel
// (Cloudflare terminates public TLS there).
func http01Domains(ctx context.Context, q *db.Queries, serverID uuid.UUID, rows []db.Domain, certs map[uuid.UUID]DomainCert) ([]db.Domain, error) {
	var cand []db.Domain
	var ids []uuid.UUID
	for _, d := range rows {
		if c, ok := certs[d.ID]; ok && c.Source == SourceACME {
			cand = append(cand, d)
			ids = append(ids, d.ID)
		}
	}
	if len(cand) == 0 {
		return nil, nil
	}
	tunnelled, err := q.ListTunnelledDomainIDs(ctx, db.ListTunnelledDomainIDsParams{DomainIds: ids, ServerID: serverID})
	if err != nil {
		return nil, fmt.Errorf("list tunnelled domains: %w", err)
	}
	skip := make(map[uuid.UUID]bool, len(tunnelled))
	for _, id := range tunnelled {
		skip[id] = true
	}
	uploaded, err := q.ListDesiredCertificates(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("list certificates: %w", err)
	}
	var out []db.Domain
	for _, d := range cand {
		if skip[d.ID] {
			continue
		}
		covered := false
		for _, c := range uploaded {
			if certCovers(c.Domains, d.Hostname) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, d)
		}
	}
	return out, nil
}

// annotateHTTP01 sets DomainCert.Warning on the HTTP-01 domains of servers whose
// proxy does not listen on :80 and that have an ACME contact (without one no
// certificate is requested at all).
func annotateHTTP01(ctx context.Context, q *db.Queries, rows []db.Domain, serverOf map[uuid.UUID]uuid.UUID, certs map[uuid.UUID]DomainCert) error {
	byServer := map[uuid.UUID][]db.Domain{}
	var serverIDs []uuid.UUID
	for _, d := range rows {
		if certs[d.ID].Source != SourceACME {
			continue
		}
		sid, ok := serverOf[d.ID]
		if !ok {
			continue
		}
		if _, seen := byServer[sid]; !seen {
			serverIDs = append(serverIDs, sid)
		}
		byServer[sid] = append(byServer[sid], d)
	}
	if len(serverIDs) == 0 {
		return nil
	}
	infos, err := q.ListServerHTTP01Context(ctx, serverIDs)
	if err != nil {
		return fmt.Errorf("list server proxy ports: %w", err)
	}
	for _, info := range infos {
		port := ServerHTTPPort(info.ReportedHttpPort, info.ProxyAppliedHttpPort)
		if port == 80 || info.AcmeEmail == nil || *info.AcmeEmail == "" {
			continue
		}
		dependent, err := http01Domains(ctx, q, info.ID, byServer[info.ID], certs)
		if err != nil {
			return err
		}
		for _, d := range dependent {
			c := certs[d.ID]
			c.Warning = HTTP01Warning(port, d.Hostname)
			certs[d.ID] = c
		}
	}
	return nil
}

// HTTP01Warnings lists the warnings for the domains placed on srv if its proxy
// listened on httpPort: one per domain that depends on HTTP-01. The server
// proxy plan uses it with the HTTP port a deploy would apply.
func HTTP01Warnings(ctx context.Context, q *db.Queries, srv db.Server, httpPort int) ([]string, error) {
	if httpPort == 80 || srv.AcmeEmail == nil || *srv.AcmeEmail == "" {
		return nil, nil
	}
	rows, err := q.ListServerPlacedDomains(ctx, &srv.ID)
	if err != nil {
		return nil, fmt.Errorf("list server domains: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	wild, err := q.ListServerWildcardCerts(ctx, srv.ID)
	if err != nil {
		return nil, fmt.Errorf("list wildcard certificates: %w", err)
	}
	serverOf := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, d := range rows {
		serverOf[d.ID] = srv.ID
	}
	autos, err := autoChoices(ctx, q, rows, serverOf, []uuid.UUID{srv.ID})
	if err != nil {
		return nil, err
	}
	certs := make(map[uuid.UUID]DomainCert, len(rows))
	for _, d := range rows {
		certs[d.ID] = certFor(d, wild, autos[d.ID], time.Now())
	}
	dependent, err := http01Domains(ctx, q, srv.ID, rows, certs)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(dependent))
	for _, d := range dependent {
		out = append(out, HTTP01Warning(httpPort, d.Hostname))
	}
	return out, nil
}
