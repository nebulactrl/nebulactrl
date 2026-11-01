// Package systemroutetest installs a system route configuration directly, for
// tests of the packages that read it.
package systemroutetest

import (
	"github.com/nebulactrl/nebulactrl/internal/config"
	"github.com/nebulactrl/nebulactrl/internal/systemroute"
)

// Set installs c as the process-wide system route configuration the way the
// server does: Configure from the installer settings, then SetAccess for the
// stored access settings. The zero Config disables the route. Host must be a
// public hostname (config.DashboardHost rejects localhost and IP literals).
func Set(c systemroute.Config) {
	if c.Host == "" || c.Upstream == "" {
		systemroute.Configure(nil)
		return
	}
	systemroute.Configure(&config.Config{
		PublicURL: "https://" + c.Host,
		Installer: config.Installer{DashboardUpstream: c.Upstream, DashboardAllowCIDRs: c.AllowCIDRs},
	})
	systemroute.SetAccess(c.AllowCIDRs, c.DNSResolver)
}
