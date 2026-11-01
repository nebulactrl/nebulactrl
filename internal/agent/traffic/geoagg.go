package traffic

import (
	"sort"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// maxLiveCities bounds the distinct cities one accumulator tracks; further
// cities of the minute count as OtherCities, so an address scan cannot grow memory.
const maxLiveCities = 2000

// SetGeo enables request-origin counting with g. Call it before the first Add.
func (a *Aggregator) SetGeo(g GeoLocator) { a.geo = g }

// origin is the geolocation of one request, resolved before the aggregator lock
// is taken (the lookup is the expensive part).
type origin struct {
	active  bool // geolocation is on: the request is counted in some geo bucket
	located bool
	level   string
	loc     Location
}

// locate resolves the origin of a client address. The address is used here
// and nowhere else: only the country and city leave this function.
func (a *Aggregator) locate(ip string) origin {
	if a.geo == nil {
		return origin{}
	}
	level := a.geo.Level()
	if level == "" {
		return origin{}
	}
	loc, ok := a.geo.Locate(ip)
	return origin{active: true, located: ok, level: level, loc: loc}
}

type cityKey struct {
	name, country string
	lat, lon      float64
}

// geoAcc counts the origins of one (minute, service, host) accumulator.
type geoAcc struct {
	countries   map[string]int64
	cities      map[cityKey]int64
	otherCities int64
	unknown     int64
}

// count adds one request.
func (g *geoAcc) count(o origin) {
	if !o.located {
		g.unknown++
		return
	}
	if g.countries == nil {
		g.countries = map[string]int64{}
	}
	g.countries[o.loc.Country]++
	if o.level != agentprotocol.GeoLevelCity {
		return
	}
	if o.loc.City == "" {
		g.otherCities++
		return
	}
	k := cityKey{o.loc.City, o.loc.Country, o.loc.Lat, o.loc.Lon}
	if g.cities == nil {
		g.cities = map[cityKey]int64{}
	}
	if _, ok := g.cities[k]; !ok && len(g.cities) >= maxLiveCities {
		g.otherCities++
		return
	}
	g.cities[k]++
}

// bucket returns the wire form: the busiest cities, the rest in OtherCities.
func (g *geoAcc) bucket() *agentprotocol.GeoBucket {
	if g == nil {
		return nil
	}
	b := &agentprotocol.GeoBucket{Unknown: g.unknown, OtherCities: g.otherCities}
	if len(g.countries) > 0 {
		b.Countries = make(map[string]int64, len(g.countries))
		for k, v := range g.countries {
			b.Countries[k] = v
		}
	}
	if len(g.cities) > 0 {
		list := make([]agentprotocol.GeoCity, 0, len(g.cities))
		for k, n := range g.cities {
			list = append(list, agentprotocol.GeoCity{Name: k.name, Country: k.country, Lat: k.lat, Lon: k.lon, N: n})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].N != list[j].N {
				return list[i].N > list[j].N
			}
			if list[i].Name != list[j].Name {
				return list[i].Name < list[j].Name
			}
			return list[i].Country < list[j].Country
		})
		if len(list) > agentprotocol.MaxGeoCitiesPerBucket {
			for _, c := range list[agentprotocol.MaxGeoCitiesPerBucket:] {
				b.OtherCities += c.N
			}
			list = list[:agentprotocol.MaxGeoCitiesPerBucket]
		}
		b.Cities = list
	}
	return b
}
