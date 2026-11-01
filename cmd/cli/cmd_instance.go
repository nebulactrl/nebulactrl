package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

// tailnetRanges are Tailscale's address ranges (CGNAT IPv4 and the ULA IPv6
// range every tailnet uses): the "Tailnet only" preset of the dashboard.
var tailnetRanges = []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}

func newInstanceCmd(c *cli) *cobra.Command {
	return group("instance", "Settings of the whole installation (instance admin only)",
		group("dashboard-access", "Who may reach the dashboard and how its certificate is issued",
			newDashboardAccessGetCmd(c), newDashboardAccessSetCmd(c)))
}

func (c *cli) printDashboardAccess(v *apiclient.DashboardAccessView) {
	if !v.Enabled {
		c.info("The dashboard has no system route (no https public URL with a DNS name), so there is nothing to restrict.")
		return
	}
	allow := deref(v.AllowCidrs)
	c.info("Host:         %s", v.Host)
	if len(allow) == 0 {
		c.info("Allow list:   everybody")
	} else {
		c.info("Allow list:   %s", strings.Join(allow, ", "))
	}
	method := "automatic HTTP-01"
	if v.DnsResolver != "" {
		method = "DNS-01 " + v.DnsResolver
	}
	c.info("Certificate:  %s (%s)", method, v.Certificate.Message)
	var providers []string
	for _, r := range deref(v.ResolverAvailable) {
		name := r.Provider
		if !r.Applied {
			name += " (staged: needs Deploy proxy)"
		}
		providers = append(providers, name)
	}
	if len(providers) > 0 {
		c.info("Resolvers:    %s", strings.Join(providers, ", "))
	}
	if v.ViaDashboardRoute {
		c.info("Your address: %s (allowed: %t)", v.ClientIp, v.ClientIpAllowed)
	}
	c.info("Source:       %s", v.Source)
	if v.ProxyDeployRequired {
		c.info("Deploy the proxy of the control-plane server to activate the DNS-01 resolver (Servers, Proxy).")
	}
}

func newDashboardAccessGetCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show the dashboard's allow list and certificate method",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.GetDashboardAccessWithResponse(ctx)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.printDashboardAccess(r.JSON200)
			return nil
		},
	}
}

func newDashboardAccessSetCmd(c *cli) *cobra.Command {
	var allow []string
	var clearAllow, tailnetOnly, http01, confirm bool
	var resolver string
	cmd := &cobra.Command{
		Use:   "set [--allow <cidr>]... [--tailnet-only | --clear-allow] [--dns-resolver <provider> | --http-01] [--confirm-lockout]",
		Short: "Change the dashboard's allow list and certificate method",
		Args:  exactArgs(0),
		Long: "Restricts who may reach the dashboard's hostname (an IP allow list on its route) and chooses how its certificate is issued.\n" +
			"Settings you do not name stay as they are. --allow replaces the list (repeat it or separate CIDRs with commas; IPv4 and IPv6).\n" +
			"--tailnet-only is the list " + strings.Join(tailnetRanges, ", ") + ". Agents on other servers and webhooks reach the control plane\n" +
			"through the same hostname, so keep their addresses in the list.\n" +
			"The change is refused (409 dashboard_lockout) when the list leaves out the address you are connecting from, unless --confirm-lockout is set.\n" +
			"--dns-resolver needs a resolver of that provider on the control-plane server (Servers, Proxy). Run from the host with an SSH tunnel to recover from a lock-out.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if clearAllow && (tailnetOnly || len(allow) > 0) || tailnetOnly && len(allow) > 0 {
				return usagef("use one of --allow, --tailnet-only and --clear-allow")
			}
			if http01 && resolver != "" {
				return usagef("use one of --dns-resolver and --http-01")
			}
			if !clearAllow && !tailnetOnly && len(allow) == 0 && !http01 && resolver == "" {
				return usagef("nothing to change: name --allow, --tailnet-only, --clear-allow, --dns-resolver or --http-01")
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			cur, err := c.api.GetDashboardAccessWithResponse(ctx)
			if err := check(cur, err); err != nil {
				return err
			}
			list := deref(cur.JSON200.AllowCidrs)
			body := apiclient.UpdateDashboardAccessJSONRequestBody{AllowCidrs: &list, DnsResolver: &cur.JSON200.DnsResolver}
			switch {
			case tailnetOnly:
				body.AllowCidrs = &tailnetRanges
			case clearAllow:
				body.AllowCidrs = &[]string{}
			case len(allow) > 0:
				cidrs := splitCIDRs(allow)
				body.AllowCidrs = &cidrs
			}
			switch {
			case http01:
				empty := ""
				body.DnsResolver = &empty
			case resolver != "":
				body.DnsResolver = &resolver
			}
			if confirm {
				body.ConfirmLockout = &confirm
			}
			r, err := c.api.UpdateDashboardAccessWithResponse(ctx, body)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			c.info("Saved.")
			c.printDashboardAccess(r.JSON200)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "CIDR or address allowed to reach the dashboard; replaces the list (repeatable, comma separated)")
	cmd.Flags().BoolVar(&tailnetOnly, "tailnet-only", false, "allow only Tailscale addresses ("+strings.Join(tailnetRanges, ", ")+")")
	cmd.Flags().BoolVar(&clearAllow, "clear-allow", false, "lift the restriction: everybody may reach the dashboard")
	cmd.Flags().StringVar(&resolver, "dns-resolver", "", "issue the certificate through this DNS-01 provider (for example cloudflare)")
	cmd.Flags().BoolVar(&http01, "http-01", false, "issue the certificate automatically over HTTP-01")
	cmd.Flags().BoolVar(&confirm, "confirm-lockout", false, "save even though the list excludes the address you are connecting from")
	return cmd
}

func splitCIDRs(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
