package agentprotocol

import "time"

// Request-origin geolocation wire types. The agent looks every access-log
// client IP up in a local GeoIP database inside the traffic aggregator and
// ships only per-minute counts per country (and per city at the city level).
// Client IPs are never shipped; city coordinates are rounded to 0.1°.

// Geolocation sources.
const (
	// GeoSourceDBIP is DB-IP Lite (CC BY 4.0, no key needed).
	GeoSourceDBIP = "dbip"
	// GeoSourceMaxMind is MaxMind GeoLite2 (needs an account ID and licence key).
	GeoSourceMaxMind = "maxmind"
)

// Geolocation levels.
const (
	// GeoLevelCountry counts requests per ISO 3166-1 alpha-2 country (~8 MB database).
	GeoLevelCountry = "country"
	// GeoLevelCity also counts requests per city (~130 MB database).
	GeoLevelCity = "city"
)

// Send-time secret keys of the MaxMind credentials, carried in Reconcile.Secrets.
const (
	SecretGeoMaxMindAccount = "geo.maxmind.account_id"
	SecretGeoMaxMindKey     = "geo.maxmind.license_key"
)

// GeoConfig is a server's geolocation setting. The control plane sends it in
// every Reconcile; the agent keeps its database in step. Enabled=false makes
// the agent delete the database and stop counting origins.
type GeoConfig struct {
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source" enum:"dbip,maxmind"`
	Level       string `json:"level" enum:"country,city"`
	RefreshDays int    `json:"refreshDays"`
}

// GeoStatus is what the agent reports about its GeoIP database with every
// heartbeat (nil while geolocation is off). The control plane shows it next
// to the setting so "once the database has downloaded" can be checked.
type GeoStatus struct {
	// Downloaded is true once a database of the configured source and level is installed.
	Downloaded bool `json:"downloaded"`
	// Source and Level are those of the installed database (empty until it is downloaded).
	Source string `json:"source,omitempty" enum:"dbip,maxmind"`
	Level  string `json:"level,omitempty" enum:"country,city"`
	// Label is the release: DB-IP YYYY-MM, MaxMind the build date YYYYMMDD.
	Label string `json:"label,omitempty"`
	// SizeBytes is the database file's size on disk.
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// BuildTime is when the database's publisher built it.
	BuildTime *time.Time `json:"buildTime,omitempty"`
	// CheckedAt is when the agent last confirmed the database is current.
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// Error is why the last download or check failed ("" when healthy).
	Error string `json:"error,omitempty"`
}

// MaxGeoCitiesPerBucket is how many cities one minute bucket lists; the rest
// of its located requests are counted in GeoBucket.OtherCities.
const MaxGeoCitiesPerBucket = 20

// GeoCity is the request count of one city in one minute.
type GeoCity struct {
	Name    string  `json:"name"`
	Country string  `json:"country"` // ISO 3166-1 alpha-2
	Lat     float64 `json:"lat"`     // rounded to 0.1°
	Lon     float64 `json:"lon"`     // rounded to 0.1°
	N       int64   `json:"n"`
}

// GeoBucket holds the request origins of one TrafficBucket. Every request of
// the bucket lands in exactly one of Countries (located), Unknown (private,
// loopback or unlisted addresses).
type GeoBucket struct {
	// Countries maps ISO 3166-1 alpha-2 codes to request counts.
	Countries map[string]int64 `json:"countries,omitempty"`
	// Cities lists the busiest cities (city level only), at most MaxGeoCitiesPerBucket.
	Cities []GeoCity `json:"cities,omitempty"`
	// OtherCities counts located requests of cities beyond the cap or without a city record.
	OtherCities int64 `json:"otherCities,omitempty"`
	// Unknown counts requests whose address the database does not locate.
	Unknown int64 `json:"unknown,omitempty"`
}
