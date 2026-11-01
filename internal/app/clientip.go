package app

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nebulactrl/nebulactrl/internal/clientip"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/cloudflare"
)

// cloudflareRangesTTL is how long the Cloudflare range list read for the
// client-IP rules is reused; the refresh job itself runs every few hours.
const cloudflareRangesTTL = time.Minute

// wireClientIP tells the shared client-IP rules which addresses are
// Cloudflare's edge: the list in force on this instance (the stored one, else
// the built-in). Without a pool only the built-in list is known.
func wireClientIP(pool *pgxpool.Pool) {
	var (
		mu       sync.Mutex
		prefixes []netip.Prefix
		loaded   time.Time
	)
	parse := func(l cloudflare.IPList) []netip.Prefix {
		out := make([]netip.Prefix, 0, len(l.V4)+len(l.V6))
		for _, c := range l.CIDRs() {
			if p, err := netip.ParsePrefix(c); err == nil {
				out = append(out, p)
			}
		}
		return out
	}
	current := func() []netip.Prefix {
		mu.Lock()
		defer mu.Unlock()
		if prefixes != nil && time.Since(loaded) < cloudflareRangesTTL {
			return prefixes
		}
		list := cloudflare.Builtin()
		if pool != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if l, err := cloudflare.CurrentIPList(ctx, db.New(pool)); err == nil {
				list = l
			} else if prefixes != nil {
				return prefixes // keep the last good list when the database blips
			}
		}
		prefixes, loaded = parse(list), time.Now()
		return prefixes
	}
	clientip.SetCloudflareRanges(func(a netip.Addr) bool {
		a = a.Unmap()
		for _, p := range current() {
			if p.Contains(a) {
				return true
			}
		}
		return false
	})
}
