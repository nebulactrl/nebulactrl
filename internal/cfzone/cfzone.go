// Package cfzone holds the cached Cloudflare zone list shared by the
// Cloudflare integration and the DNS-01 certificate logic: the zones a team's
// token sees and which zone a hostname lives in. It has no dependencies, so
// both can import it (the integration imports services, which imports dns01).
package cfzone

import (
	"encoding/json"
	"strings"
)

// Zone is a DNS zone the team's Cloudflare token can see.
type Zone struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Paused      bool   `json:"paused"`
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
}

// Match returns the zone a hostname lives in: the zone with the longest name
// that equals the hostname or is one of its parent domains. A wildcard hostname
// (`*.example.com`) is matched by its base.
func Match(zones []Zone, host string) (Zone, bool) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	host = strings.TrimPrefix(host, "*.")
	var best Zone
	found := false
	for _, z := range zones {
		if z.Name == "" {
			continue
		}
		if host == z.Name || strings.HasSuffix(host, "."+z.Name) {
			if !found || len(z.Name) > len(best.Name) {
				best, found = z, true
			}
		}
	}
	return best, found
}

// Decode reads the zone list stored in cloudflare_connections.zones (never nil).
func Decode(raw json.RawMessage) []Zone {
	var zs []Zone
	if len(raw) == 0 {
		return []Zone{}
	}
	if err := json.Unmarshal(raw, &zs); err != nil || zs == nil {
		return []Zone{}
	}
	return zs
}
