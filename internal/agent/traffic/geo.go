package traffic

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Location is where one client address was placed. City, Lat and Lon are set
// only by a city database; coordinates are rounded to 0.1°.
type Location struct {
	Country string // ISO 3166-1 alpha-2, upper case
	City    string
	Lat     float64
	Lon     float64
}

// GeoLocator places client addresses. *GeoDB implements it. Implementations
// are safe for concurrent use and never retain the address.
type GeoLocator interface {
	// Level is the active level (agentprotocol.GeoLevelCountry or GeoLevelCity),
	// or "" while no database is loaded: the aggregator then counts no origins.
	Level() string
	// Locate places ip; ok is false for private, loopback, unparsable or
	// unlisted addresses.
	Locate(ip string) (Location, bool)
}

// geoRecord is the part of a DB-IP / GeoLite2 record the lookup decodes.
// Decoding into a struct skips every other field without allocating.
type geoRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names struct {
			EN string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

// maxCityNameLen bounds a stored city name.
const maxCityNameLen = 80

// GeoDB is the open GeoIP database. The reader is swapped under a lock so a
// refresh never unmaps a database a lookup is reading.
type GeoDB struct {
	mu    sync.RWMutex
	r     *maxminddb.Reader
	level string
}

// Level implements GeoLocator.
func (g *GeoDB) Level() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.level
}

// Locate implements GeoLocator.
func (g *GeoDB) Locate(ip string) (Location, bool) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return Location{}, false
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || addr.IsMulticast() {
		return Location{}, false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.r == nil {
		return Location{}, false
	}
	res := g.r.Lookup(addr)
	if !res.Found() {
		return Location{}, false
	}
	var rec geoRecord
	if err := res.Decode(&rec); err != nil {
		return Location{}, false
	}
	cc := strings.ToUpper(rec.Country.ISOCode)
	if !validISO2(cc) {
		return Location{}, false
	}
	loc := Location{Country: cc}
	if g.level == agentprotocol.GeoLevelCity {
		if name := strings.TrimSpace(rec.City.Names.EN); name != "" && (rec.Location.Latitude != 0 || rec.Location.Longitude != 0) {
			if len(name) > maxCityNameLen {
				name = name[:maxCityNameLen]
			}
			loc.City, loc.Lat, loc.Lon = name, round01(rec.Location.Latitude), round01(rec.Location.Longitude)
		}
	}
	return loc, true
}

// install swaps in a verified reader and returns the previous one to close.
func (g *GeoDB) install(r *maxminddb.Reader, level string) (old *maxminddb.Reader) {
	g.mu.Lock()
	defer g.mu.Unlock()
	old, g.r, g.level = g.r, r, level
	return old
}

// unload detaches and returns the reader; lookups report nothing afterwards.
func (g *GeoDB) unload() *maxminddb.Reader { return g.install(nil, "") }

// Close releases the database.
func (g *GeoDB) Close() {
	if r := g.unload(); r != nil {
		_ = r.Close()
	}
}

// closeReader closes a detached reader. It takes the write lock so no lookup
// is still reading the mapping (a lookup holds the read lock for its duration).
func (g *GeoDB) closeReader(r *maxminddb.Reader) {
	if r == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_ = r.Close()
}

func validISO2(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

func round01(v float64) float64 { return math.Round(v*10) / 10 }

// errDatabaseType is returned when a verified database is not the edition the
// configured level needs.
var errDatabaseType = errors.New("database edition does not match the configured level")

// openGeoDatabase opens and fully verifies a freshly downloaded database file
// for level. The caller owns the reader on success.
func openGeoDatabase(path, level string) (*maxminddb.Reader, error) {
	r, err := openGeoDatabaseFast(path, level)
	if err != nil {
		return nil, err
	}
	if err := r.Verify(); err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("verify geoip database: %w", err)
	}
	return r, nil
}

// openGeoDatabaseFast opens a database that was verified when it was
// installed (a restart does not re-walk 130 MB): metadata and edition only.
func openGeoDatabaseFast(path, level string) (*maxminddb.Reader, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open geoip database: %w", err)
	}
	if err := checkEdition(r.Metadata.DatabaseType, level); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

// checkEdition accepts City editions for the city level and Country editions
// (or City ones, which also carry the country) for the country level.
func checkEdition(databaseType, level string) error {
	t := strings.ToLower(databaseType)
	isCity := strings.Contains(t, "city")
	isCountry := strings.Contains(t, "country")
	switch {
	case level == agentprotocol.GeoLevelCity && isCity:
		return nil
	case level == agentprotocol.GeoLevelCountry && (isCountry || isCity):
		return nil
	}
	return fmt.Errorf("%w: %q at level %s", errDatabaseType, databaseType, level)
}
