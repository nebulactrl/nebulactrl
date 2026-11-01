package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Limits of the stored and reported origin lists.
const (
	// maxGeoCountries bounds the countries of one stored row (ISO 3166 has ~250).
	maxGeoCountries = 300
	// maxRolledCities is how many cities an hourly or daily row keeps; the rest
	// of its located requests move to otherCities.
	maxRolledCities = 100
	// reportCities and reportCountries bound the lists of a report.
	reportCities    = 50
	reportCountries = 300
	maxCityNameLen  = 80
)

// Reasons a report has no geo section.
const (
	// GeoReasonDisabled: geolocation is off on every server the report reads.
	GeoReasonDisabled = "geo_disabled"
	// GeoReasonTrafficDisabled: traffic analytics is off, so there is no access log to look up.
	GeoReasonTrafficDisabled = "traffic_disabled"
)

// GeoAttribution is the credit the database licences require wherever the
// data is shown (DB-IP Lite: CC BY 4.0; GeoLite2: MaxMind EULA).
type GeoAttribution struct {
	Source string `json:"source" enum:"dbip,maxmind"`
	Text   string `json:"text"`
	URL    string `json:"url"`
}

var attributions = map[string]GeoAttribution{
	agentprotocol.GeoSourceDBIP:    {Source: agentprotocol.GeoSourceDBIP, Text: "IP Geolocation by DB-IP", URL: "https://db-ip.com"},
	agentprotocol.GeoSourceMaxMind: {Source: agentprotocol.GeoSourceMaxMind, Text: "This product includes GeoLite2 data created by MaxMind", URL: "https://www.maxmind.com"},
}

// GeoCountry is the request count of one country.
type GeoCountry struct {
	ISO2     string  `json:"iso2"`
	Name     string  `json:"name"`
	Requests int64   `json:"requests"`
	Share    float64 `json:"share" doc:"Fraction (0-1) of the located requests"`
}

// GeoCity is the request count of one city.
type GeoCity struct {
	Name     string  `json:"name"`
	Country  string  `json:"country" doc:"ISO 3166-1 alpha-2 code"`
	Lat      float64 `json:"lat" doc:"Rounded to 0.1°"`
	Lon      float64 `json:"lon" doc:"Rounded to 0.1°"`
	Requests int64   `json:"requests"`
	Share    float64 `json:"share" doc:"Fraction (0-1) of the located requests"`
}

// Geo says where the requests of a window came from. IPs are never stored:
// the agent counts countries and cities locally and ships only the counts.
type Geo struct {
	Level       string           `json:"level" enum:"country,city" doc:"city when any server in scope counts cities"`
	Source      string           `json:"source" enum:"dbip,maxmind,mixed"`
	Attribution []GeoAttribution `json:"attribution" doc:"Credit to show next to the data (one entry per source in use)"`
	// LocatedRequests are the requests placed in a country; UnknownRequests
	// those the database could not place (private or unlisted addresses).
	LocatedRequests int64        `json:"locatedRequests"`
	UnknownRequests int64        `json:"unknownRequests"`
	Countries       []GeoCountry `json:"countries"`
	Cities          []GeoCity    `json:"cities"`
}

// geoSum accumulates the origins of rows (exact for counts, top-N for cities).
type geoSum struct {
	countries   map[string]int64
	cities      map[geoKey]*agentprotocol.GeoCity
	otherCities int64
	unknown     int64
}

type geoKey struct {
	name, country string
	lat, lon      float64
}

func (g *geoSum) add(b *agentprotocol.GeoBucket) {
	if len(b.Countries) == 0 && len(b.Cities) == 0 && b.Unknown == 0 && b.OtherCities == 0 {
		return
	}
	if g.countries == nil {
		g.countries = map[string]int64{}
		g.cities = map[geoKey]*agentprotocol.GeoCity{}
	}
	for c, n := range b.Countries {
		g.countries[c] += n
	}
	for _, c := range b.Cities {
		k := geoKey{c.Name, c.Country, c.Lat, c.Lon}
		cur := g.cities[k]
		if cur == nil {
			cp := c
			g.cities[k] = &cp
			continue
		}
		cur.N += c.N
	}
	g.otherCities += b.OtherCities
	g.unknown += b.Unknown
}

func (g *geoSum) empty() bool { return g == nil || (len(g.countries) == 0 && g.unknown == 0) }

// sortedCities returns the cities by request count, descending.
func (g *geoSum) sortedCities() []agentprotocol.GeoCity {
	out := make([]agentprotocol.GeoCity, 0, len(g.cities))
	for _, c := range g.cities {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Country < out[j].Country
	})
	return out
}

// bucket returns the wire/storage form with at most maxCities cities; the
// located requests of the rest are added to OtherCities.
func (g *geoSum) bucket(maxCities int) agentprotocol.GeoBucket {
	if g.empty() {
		return agentprotocol.GeoBucket{}
	}
	b := agentprotocol.GeoBucket{Unknown: g.unknown, OtherCities: g.otherCities}
	if len(g.countries) > 0 {
		b.Countries = make(map[string]int64, len(g.countries))
		for k, v := range g.countries {
			b.Countries[k] = v
		}
	}
	list := g.sortedCities()
	if len(list) > maxCities {
		for _, c := range list[maxCities:] {
			b.OtherCities += c.N
		}
		list = list[:maxCities]
	}
	b.Cities = list
	return b
}

func marshalGeo(b agentprotocol.GeoBucket) []byte {
	if len(b.Countries) == 0 && len(b.Cities) == 0 && b.Unknown == 0 && b.OtherCities == 0 {
		return []byte("{}")
	}
	data, err := json.Marshal(b)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func decodeGeo(raw []byte) agentprotocol.GeoBucket {
	var b agentprotocol.GeoBucket
	if len(raw) > 2 {
		_ = json.Unmarshal(raw, &b)
	}
	return b
}

func validISO2(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

func round01(v float64) float64 { return math.Round(v*10) / 10 }

// cleanGeo validates an agent-reported origin section. level is the level the
// server is configured for: city lists are dropped at the country level.
func cleanGeo(in *agentprotocol.GeoBucket, level string) agentprotocol.GeoBucket {
	var out agentprotocol.GeoBucket
	if in == nil || level == "" {
		return out
	}
	out.Unknown, out.OtherCities = clampCounter(in.Unknown), clampCounter(in.OtherCities)
	for c, n := range in.Countries {
		if len(out.Countries) >= maxGeoCountries {
			break
		}
		if !validISO2(c) || n <= 0 {
			continue
		}
		if out.Countries == nil {
			out.Countries = map[string]int64{}
		}
		out.Countries[c] = clampCounter(n)
	}
	if level != agentprotocol.GeoLevelCity {
		out.OtherCities = 0
		return out
	}
	for _, c := range in.Cities {
		if len(out.Cities) >= agentprotocol.MaxGeoCitiesPerBucket {
			break
		}
		name := strings.TrimSpace(c.Name)
		if name == "" || !validISO2(c.Country) || c.N <= 0 || math.Abs(c.Lat) > 90 || math.Abs(c.Lon) > 180 ||
			math.IsNaN(c.Lat) || math.IsNaN(c.Lon) {
			continue
		}
		if len(name) > maxCityNameLen {
			name = name[:maxCityNameLen]
		}
		out.Cities = append(out.Cities, agentprotocol.GeoCity{
			Name: name, Country: c.Country, Lat: round01(c.Lat), Lon: round01(c.Lon), N: clampCounter(c.N),
		})
	}
	return out
}

// geoSettings summarizes the geolocation settings of the servers a report reads.
type geoSettings struct {
	enabled bool
	level   string
	sources []string // distinct, sorted
	reason  string   // why !enabled
}

// geoSettingsFor reads the settings of the servers a report covers (the
// caller's workspace only). Geolocation is on for a report when it is on for
// at least one of them.
func (s *Service) geoSettingsFor(ctx context.Context, serverIDs []uuid.UUID) (geoSettings, error) {
	st := geoSettings{level: agentprotocol.GeoLevelCountry, reason: GeoReasonDisabled}
	if len(serverIDs) == 0 {
		return st, nil
	}
	rows, err := s.q.ListServersGeo(ctx, db.ListServersGeoParams{ServerIds: serverIDs, WorkspaceID: policy.WorkspaceID(ctx)})
	if err != nil {
		return st, fmt.Errorf("list server geo settings: %w", err)
	}
	seen := map[string]bool{}
	trafficOff := len(rows) > 0
	for _, r := range rows {
		if r.TrafficAnalyticsEnabled {
			trafficOff = false
		}
		if !r.TrafficAnalyticsEnabled || !r.GeoEnabled {
			continue
		}
		st.enabled = true
		if r.GeoLevel == agentprotocol.GeoLevelCity {
			st.level = agentprotocol.GeoLevelCity
		}
		if !seen[r.GeoSource] {
			seen[r.GeoSource] = true
			st.sources = append(st.sources, r.GeoSource)
		}
	}
	sort.Strings(st.sources)
	switch {
	case st.enabled:
		st.reason = ""
	case trafficOff:
		st.reason = GeoReasonTrafficDisabled
	}
	return st, nil
}

// buildGeo turns the window total into the report section.
func buildGeo(st geoSettings, g *geoSum) *Geo {
	out := &Geo{Level: st.level, Countries: []GeoCountry{}, Cities: []GeoCity{}}
	switch len(st.sources) {
	case 1:
		out.Source = st.sources[0]
	default:
		out.Source = "mixed"
	}
	out.Attribution = make([]GeoAttribution, 0, len(st.sources))
	for _, src := range st.sources {
		if a, ok := attributions[src]; ok {
			out.Attribution = append(out.Attribution, a)
		}
	}
	if g == nil {
		return out
	}
	out.UnknownRequests = g.unknown
	for _, n := range g.countries {
		out.LocatedRequests += n
	}
	share := func(n int64) float64 {
		if out.LocatedRequests == 0 {
			return 0
		}
		return float64(n) / float64(out.LocatedRequests)
	}
	for c, n := range g.countries {
		out.Countries = append(out.Countries, GeoCountry{ISO2: c, Name: countryName(c), Requests: n, Share: share(n)})
	}
	sort.Slice(out.Countries, func(i, j int) bool {
		if out.Countries[i].Requests != out.Countries[j].Requests {
			return out.Countries[i].Requests > out.Countries[j].Requests
		}
		return out.Countries[i].ISO2 < out.Countries[j].ISO2
	})
	if len(out.Countries) > reportCountries {
		out.Countries = out.Countries[:reportCountries]
	}
	for _, c := range g.sortedCities() {
		if len(out.Cities) >= reportCities {
			break
		}
		out.Cities = append(out.Cities, GeoCity{Name: c.Name, Country: c.Country, Lat: c.Lat, Lon: c.Lon, Requests: c.N, Share: share(c.N)})
	}
	return out
}
