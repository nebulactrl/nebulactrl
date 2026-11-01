package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newDomainsCmd(c *cli) *cobra.Command {
	return group("domains", "Manage service domains",
		newDomainsAddCmd(c), newDomainsLsCmd(c), newDomainsRmCmd(c), newDomainsGenerateCmd(c), newDomainsWWWCmd(c))
}

func dnsLabel(d apiclient.DomainView) string {
	switch {
	case d.DnsOk == nil:
		return "unknown"
	case *d.DnsOk:
		return "ok"
	default:
		return "mismatch"
	}
}

// cloudflareLabel is the Cloudflare DNS state of a domain for the table:
// "-" while the team has no connection or nothing was evaluated yet.
func cloudflareLabel(d apiclient.DomainView) string {
	if d.Dns.Status == "" {
		return "-"
	}
	label := string(d.Dns.Status)
	if d.Dns.Status != apiclient.DomainDNSStatusNotInZone && d.Dns.Proxied {
		label += " (proxied)"
	}
	return label
}

func (c *cli) printDomains(domains []apiclient.DomainView) {
	withProcess := false
	for _, d := range domains {
		withProcess = withProcess || d.Process != ""
	}
	rows := make([][]string, 0, len(domains))
	for _, d := range domains {
		row := []string{d.Hostname, fmt.Sprint(d.TargetPort)}
		if withProcess {
			row = append(row, orDash(d.Process))
		}
		rows = append(rows, append(row, d.Status, dnsLabel(d), cloudflareLabel(d), wwwLabel(d), yesNo(d.Generated), d.Id))
	}
	headers := []string{"HOSTNAME", "PORT"}
	if withProcess {
		headers = append(headers, "PROCESS")
	}
	c.table(append(headers, "STATUS", "DNS", "CLOUDFLARE", "WWW REDIRECT", "GENERATED", "ID"), rows)
}

// wwwLabel describes the www/apex redirect of a domain for the table: the
// counterpart that redirects to it, the domain a redirect row belongs to, or "-".
func wwwLabel(d apiclient.DomainView) string {
	switch {
	case d.WwwRedirect:
		return d.WwwPartner + " → here"
	case d.WwwRedirectFor != "":
		return "→ " + d.WwwRedirectFor
	}
	return "-"
}

func (c *cli) listDomains(ctx context.Context, serviceID string) ([]apiclient.DomainView, error) {
	r, err := c.api.ListDomainsWithResponse(ctx, serviceID)
	if err := check(r, err); err != nil {
		return nil, err
	}
	return itemsOrEmpty(r.JSON200.Items), nil
}

func newDomainsAddCmd(c *cli) *cobra.Command {
	var port int
	var component, process string
	var wwwRedirect bool
	cmd := &cobra.Command{
		Use: "add <service> <hostname>", Short: "Attach a hostname to a service", Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			in := apiclient.CreateDomainJSONRequestBody{Hostname: strings.ToLower(args[1])}
			if port > 0 {
				p := int64(port)
				in.TargetPort = &p
			}
			if component != "" {
				in.Component = &component
			}
			if process != "" {
				in.Process = &process
			}
			if wwwRedirect {
				in.WwwRedirect = &wwwRedirect
			}
			r, err := c.api.CreateDomainWithResponse(ctx, id, in)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Added %s (staged until the next deploy)", r.JSON201.Hostname)
			if wwwRedirect {
				c.info("Also routed: %s redirects (301) to %s. Point an A record for both hostnames at the server.", r.JSON201.WwwPartner, r.JSON201.Hostname)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "target port (default: the service port; Compose stacks: the container port, optional when the component declares one)")
	cmd.Flags().StringVar(&component, "component", "", "Compose stacks: the component (compose service) the domain routes to")
	cmd.Flags().StringVar(&process, "process", "", "services with several processes: the process the domain routes to (default: the default process, or the only one with a port); the target port is the process' port")
	cmd.Flags().BoolVar(&wwwRedirect, "www-redirect", false, "also route the www/apex counterpart and 301-redirect it to this hostname (needs proxy:write; both hostnames get certificates)")
	return cmd
}

func newDomainsLsCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "ls <service>", Short: "List a service's domains", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			ds, err := c.listDomains(ctx, id)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(ds)
			}
			c.printDomains(ds)
			return nil
		},
	}
}

func newDomainsRmCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "rm <service> <hostname|domain-id>", Short: "Remove a domain from a service", Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			ds, err := c.listDomains(ctx, id)
			if err != nil {
				return err
			}
			var target *apiclient.DomainView
			for i := range ds {
				if ds[i].Id == args[1] || strings.EqualFold(ds[i].Hostname, args[1]) {
					target = &ds[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("domain %q not found on this service", args[1])
			}
			r, err := c.api.DeleteDomainWithResponse(ctx, target.Id)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(map[string]any{"deleted": target.Id, "hostname": target.Hostname})
			}
			c.info("Removed %s (staged until the next deploy)", target.Hostname)
			return nil
		},
	}
}

func newDomainsGenerateCmd(c *cli) *cobra.Command {
	var component, process string
	cmd := &cobra.Command{
		Use: "generate <service>", Short: "Generate a domain from the server's base domain", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			id, err := c.serviceID(ctx, args[0])
			if err != nil {
				return err
			}
			params := &apiclient.GenerateDomainParams{}
			if component != "" {
				params.Component = &component
			}
			if process != "" {
				params.Process = &process
			}
			r, err := c.api.GenerateDomainWithResponse(ctx, id, params)
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON201)
			}
			c.info("Generated %s (staged until the next deploy)", r.JSON201.Hostname)
			return nil
		},
	}
	cmd.Flags().StringVar(&component, "component", "", "Compose stacks: the component the generated domain routes to")
	cmd.Flags().StringVar(&process, "process", "", "services with several processes: the process the generated domain routes to")
	return cmd
}
