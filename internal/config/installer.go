package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Installer holds the settings only the installer (deploy/install) sets. All of
// them are optional: a control plane started by hand leaves them empty and
// runs without the installer's local server, dashboard route and access rules.
type Installer struct {
	// SetupToken (NEBULACTRL_SETUP_TOKEN) must accompany POST /setup while no
	// user exists, so nobody who finds the dashboard first can claim the admin
	// account. Empty means setup needs no token.
	SetupToken string
	// DashboardUpstream (NEBULACTRL_DASHBOARD_UPSTREAM, host:port on the edge
	// network, for example nebulactrl:8080) is where the control-plane host's
	// proxy sends the dashboard's hostname. Empty disables the system route.
	DashboardUpstream string
	// StatusUpstream (NEBULACTRL_STATUS_UPSTREAM, host:port on the edge network;
	// default nebulactrl:8081) is where the control-plane host's proxy sends the
	// hostnames of published status pages: the status listener
	// (NEBULACTRL_STATUS_LISTEN), never the dashboard listener. A leading
	// "http://" is accepted and dropped.
	StatusUpstream string
	// LocalEnrollToken (NEBULACTRL_LOCAL_ENROLL_TOKEN) is the enrollment token
	// the installer gave the agent on the control-plane host. When the first
	// team exists the control plane creates that host's server row for it.
	LocalEnrollToken string
	// LocalServerName and LocalServerIP describe that server
	// (NEBULACTRL_LOCAL_SERVER_NAME, NEBULACTRL_LOCAL_SERVER_IP).
	LocalServerName string
	LocalServerIP   string
	// ACMEEmail (NEBULACTRL_ACME_EMAIL) is the Let's Encrypt contact of that
	// server, which also gets the dashboard's certificate.
	ACMEEmail string
	// DashboardAllowCIDRs (NEBULACTRL_DASHBOARD_ALLOW_CIDRS, comma separated
	// CIDRs or addresses, IPv4 and IPv6) restricts who may reach the dashboard's
	// hostname on the control-plane host's proxy. Empty means no allow list.
	// Like the two settings below it only SEEDS the dashboard access row (see
	// package dashboardaccess): once an instance admin edits the settings in the
	// dashboard, the database row wins and this value is ignored.
	DashboardAllowCIDRs []string
	// DashboardDNSResolver (NEBULACTRL_DASHBOARD_DNS_RESOLVER) is the DNS-01
	// provider code that issues the dashboard's certificate; today only
	// "cloudflare". Empty means automatic HTTP-01.
	DashboardDNSResolver string
	// DashboardDNSToken (NEBULACTRL_DASHBOARD_DNS_TOKEN or
	// NEBULACTRL_DASHBOARD_DNS_TOKEN_FILE) is that provider's API token (for
	// cloudflare the CF_DNS_API_TOKEN value). It is only ever sealed into a DNS
	// credential, never logged or returned.
	DashboardDNSToken string
}

// DashboardDNSProviders are the providers NEBULACTRL_DASHBOARD_DNS_RESOLVER
// accepts: those whose credential is a single API token.
var DashboardDNSProviders = []string{agentprotocol.DNSCloudflare}

// DefaultStatusUpstream is the status listener as the control-plane host's
// proxy reaches it on the installer's edge network.
const DefaultStatusUpstream = "nebulactrl:8081"

// ParseCIDRList splits a comma separated list of CIDRs or addresses and checks
// it with the same rules as a route's access list (at most
// agentprotocol.MaxAccessCIDRs entries, IPv4 or IPv6). Blank entries are
// ignored and duplicates dropped.
func ParseCIDRList(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		c := strings.TrimSpace(part)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	if err := agentprotocol.ValidateAccessLists(out, nil, false); err != nil {
		return nil, err
	}
	return out, nil
}

func loadInstaller(getenv func(string) string) (Installer, error) {
	t := func(k string) string { return strings.TrimSpace(getenv(k)) }
	var errs []error
	inst := Installer{
		SetupToken:        t("NEBULACTRL_SETUP_TOKEN"),
		DashboardUpstream: t("NEBULACTRL_DASHBOARD_UPSTREAM"),
		StatusUpstream:    strings.TrimRight(strings.TrimPrefix(t("NEBULACTRL_STATUS_UPSTREAM"), "http://"), "/"),
		LocalEnrollToken:  t("NEBULACTRL_LOCAL_ENROLL_TOKEN"),
		LocalServerName:   t("NEBULACTRL_LOCAL_SERVER_NAME"),
		LocalServerIP:     t("NEBULACTRL_LOCAL_SERVER_IP"),
		ACMEEmail:         t("NEBULACTRL_ACME_EMAIL"),
	}
	if inst.StatusUpstream == "" {
		inst.StatusUpstream = DefaultStatusUpstream
	} else if h, p, err := net.SplitHostPort(inst.StatusUpstream); err != nil || h == "" || p == "" {
		errs = append(errs, fmt.Errorf("NEBULACTRL_STATUS_UPSTREAM %q must be host:port", inst.StatusUpstream))
	}
	cidrs, err := ParseCIDRList(t("NEBULACTRL_DASHBOARD_ALLOW_CIDRS"))
	if err != nil {
		errs = append(errs, fmt.Errorf("NEBULACTRL_DASHBOARD_ALLOW_CIDRS: %w", err))
	}
	inst.DashboardAllowCIDRs = cidrs
	inst.DashboardDNSResolver = strings.ToLower(t("NEBULACTRL_DASHBOARD_DNS_RESOLVER"))
	token, _, err := secretValue(getenv, "NEBULACTRL_DASHBOARD_DNS_TOKEN")
	if err != nil {
		errs = append(errs, err)
	}
	inst.DashboardDNSToken = token
	switch {
	case inst.DashboardDNSResolver == "" && token == "":
	case inst.DashboardDNSResolver == "":
		errs = append(errs, errors.New("NEBULACTRL_DASHBOARD_DNS_TOKEN is set without NEBULACTRL_DASHBOARD_DNS_RESOLVER"))
	case !slices.Contains(DashboardDNSProviders, inst.DashboardDNSResolver):
		errs = append(errs, fmt.Errorf("NEBULACTRL_DASHBOARD_DNS_RESOLVER %q is not supported; use one of: %s", inst.DashboardDNSResolver, strings.Join(DashboardDNSProviders, ", ")))
	case token == "":
		errs = append(errs, errors.New("NEBULACTRL_DASHBOARD_DNS_RESOLVER needs NEBULACTRL_DASHBOARD_DNS_TOKEN (or NEBULACTRL_DASHBOARD_DNS_TOKEN_FILE)"))
	}
	return inst, errors.Join(errs...)
}

// DashboardHost returns the hostname the dashboard is served on through the
// system route: the host of an https PublicURL that is a DNS name (not an IP
// address, not localhost). Empty when the public URL is not such a URL.
func (c *Config) DashboardHost() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return ""
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return ""
	}
	return h
}
