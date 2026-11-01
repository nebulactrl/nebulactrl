package cloudflare

import (
	"encoding/json"
	"sort"

	"github.com/nebulactrl/nebulactrl/internal/cfzone"
)

func sortZones(zs []Zone) {
	sort.Slice(zs, func(i, j int) bool {
		if zs[i].Name != zs[j].Name {
			return zs[i].Name < zs[j].Name
		}
		return zs[i].ID < zs[j].ID
	})
}

// MatchZone returns the zone a hostname lives in: the zone with the longest
// name that equals the hostname or is one of its parent domains. A wildcard
// hostname (`*.example.com`) is matched by its base.
func MatchZone(zones []Zone, host string) (Zone, bool) { return cfzone.Match(zones, host) }

func decodeZones(raw json.RawMessage) []Zone { return cfzone.Decode(raw) }

func encodeZones(zs []Zone) json.RawMessage {
	if zs == nil {
		zs = []Zone{}
	}
	raw, err := json.Marshal(zs)
	if err != nil {
		return json.RawMessage("[]")
	}
	return raw
}

// AccountRef is an account the token's zones belong to.
type AccountRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// accountsOf returns the distinct accounts of the zones, ordered by name.
func accountsOf(zs []Zone) []AccountRef {
	seen := map[string]bool{}
	out := []AccountRef{}
	for _, z := range zs {
		if z.AccountID == "" || seen[z.AccountID] {
			continue
		}
		seen[z.AccountID] = true
		out = append(out, AccountRef{ID: z.AccountID, Name: z.AccountName})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}
