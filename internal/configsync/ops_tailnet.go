package configsync

import (
	"strconv"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
)

// tailnetField is config.tailnet: the whole [tailnet] table is one unit. A value is compared
// without Funnel, which a file never sets: set keeps the live Funnel flag and get leaves it out, so
// a service with Funnel on is in sync with a file that says nothing about it.
var tailnetField = cfgField{
	get: func(_ *planCtx, c services.Config) any { return projectconfig.TailnetValueOf(c.Tailnet) },
	set: func(_ *planCtx, c *services.Config, v any) error {
		tv, ok := v.(projectconfig.TailnetValue)
		if !ok {
			return refErr("wrong_type", "tailnet has an unexpected value")
		}
		funnel := c.Tailnet != nil && c.Tailnet.Funnel
		if funnel && tv.Enabled && !tv.Web {
			return refErr("invalid_value", "the service has Funnel on, which publishes the web exposure: write web = true, or turn Funnel off in the dashboard first (a file never changes it)")
		}
		c.Tailnet = tv.Config(funnel)
		return nil
	},
}

// tailnetDisplay renders a tailnet for a plan line: what is exposed, then what is set besides.
func tailnetDisplay(v projectconfig.TailnetValue) string {
	if !v.Enabled {
		return "off"
	}
	parts := []string{"on"}
	if v.Hostname != "" {
		parts = append(parts, "hostname "+v.Hostname)
	}
	if v.Web {
		parts = append(parts, "web")
	}
	for _, p := range v.TCP {
		s := "tcp " + strconv.Itoa(p.Listen)
		if p.Target != p.Listen {
			s += "→" + strconv.Itoa(p.Target)
		}
		if p.TLS {
			s += " tls"
		}
		parts = append(parts, s)
	}
	if len(v.ExtraTags) > 0 {
		parts = append(parts, "tags "+strings.Join(v.ExtraTags, ","))
	}
	if v.HidePublic {
		parts = append(parts, "tailnet only")
	}
	return strings.Join(parts, ", ")
}
