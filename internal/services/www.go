package services

import (
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const wwwPrefix = "www."

// WWWPartner returns the www/apex counterpart of host: www.<host>, or the host
// without its leading "www." (which must leave at least two labels). ok is
// false for wildcards and for hosts with no counterpart name. The result is not
// validated as a hostname (domains.ValidateHostname does that).
func WWWPartner(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.HasPrefix(host, "*.") {
		return "", false
	}
	if rest, ok := strings.CutPrefix(host, wwwPrefix); ok {
		if !strings.Contains(rest, ".") {
			return "", false
		}
		return rest, true
	}
	partner := wwwPrefix + host
	if len(partner) > 253 {
		return "", false
	}
	return partner, true
}

// wwwEligible reports whether d can be the canonical side of a www redirect: a
// whole-host serve domain.
func wwwEligible(d db.Domain) bool {
	return d.Kind == agentprotocol.DomainServe && d.PathPrefix == ""
}

// IsWWWCompanion reports whether redirect is the www/apex redirect of serve:
// a whole-host redirect on serve's partner hostname that points at serve.
func IsWWWCompanion(serve, redirect db.Domain) bool {
	if !wwwEligible(serve) || redirect.Kind != agentprotocol.DomainRedirect || redirect.PathPrefix != "" || redirect.RedirectTo == nil {
		return false
	}
	partner, ok := WWWPartner(serve.Hostname)
	return ok && redirect.Hostname == partner && *redirect.RedirectTo == serve.Hostname
}

// FindWWWCompanion returns the www/apex redirect of serve among the service's domains.
func FindWWWCompanion(siblings []db.Domain, serve db.Domain) (db.Domain, bool) {
	for _, s := range siblings {
		if s.ServiceID == serve.ServiceID && s.ID != serve.ID && IsWWWCompanion(serve, s) {
			return s, true
		}
	}
	return db.Domain{}, false
}

// annotateWWW fills the www fields of the views of rows from the domains of
// the same services (siblings, which include rows).
func annotateWWW(out []DomainView, rows, siblings []db.Domain) {
	byService := map[uuid.UUID][]db.Domain{}
	for _, s := range siblings {
		byService[s.ServiceID] = append(byService[s.ServiceID], s)
	}
	for i, r := range rows {
		if wwwEligible(r) {
			if p, ok := WWWPartner(r.Hostname); ok {
				out[i].WWWPartner = p
				_, out[i].WWWRedirect = FindWWWCompanion(byService[r.ServiceID], r)
			}
			continue
		}
		if r.Kind != agentprotocol.DomainRedirect {
			continue
		}
		for _, s := range byService[r.ServiceID] {
			if s.ID != r.ID && IsWWWCompanion(s, r) {
				out[i].WWWRedirectFor = s.Hostname
				break
			}
		}
	}
}
