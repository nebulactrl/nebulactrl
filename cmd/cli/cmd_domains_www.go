package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
)

func newDomainsWWWCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use: "www <service> <hostname|domain-id> <on|off>", Short: "Turn the www/apex redirect of a domain on or off", Args: exactArgs(3),
		Long: "`on` routes the www/apex counterpart of the domain (www.<host>, or the host without its www.) and answers it with\n" +
			"a 301 redirect to the domain, keeping the path and query. Both hostnames get certificates and each needs its own\n" +
			"DNS record. `off` removes the redirect. The change is staged until the next deploy. Needs proxy:write; the domain\n" +
			"must be a whole-host serve domain without a path prefix.",
		Example: "  nebulactrl domains www acme/production/web example.com on",
		RunE: func(cmd *cobra.Command, args []string) error {
			var on bool
			switch strings.ToLower(args[2]) {
			case "on":
				on = true
			case "off":
			default:
				return usagef("the last argument must be on or off")
			}
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
			svc, err := c.api.GetServiceWithResponse(ctx, id)
			if err := check(svc, err); err != nil {
				return err
			}
			etag := fmt.Sprintf("%q", fmt.Sprint(svc.JSON200.Generation))
			body := fmt.Sprintf(`{"wwwRedirect":%t}`, on)
			r, err := c.api.UpdateDomainWithBodyWithResponse(ctx, target.Id, &apiclient.UpdateDomainParams{IfMatch: &etag}, "application/json", strings.NewReader(body))
			if err := check(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(r.JSON200)
			}
			d := r.JSON200
			switch {
			case d.WwwRedirect:
				c.info("%s redirects (301) to %s (staged until the next deploy). Point an A record for both hostnames at the server.", d.WwwPartner, d.Hostname)
			default:
				c.info("No www redirect for %s (staged until the next deploy)", d.Hostname)
			}
			return nil
		},
	}
}
