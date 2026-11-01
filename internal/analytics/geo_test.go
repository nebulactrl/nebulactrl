package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func geoBucketAt(f *fixture, at time.Time, requests int64, geo *agentprotocol.GeoBucket) agentprotocol.TrafficBucket {
	b := mkBucket(f.service.String(), "app.example.com", at, requests, nil)
	b.Geo = geo
	return b
}

// seedGeo ingests two minutes of origins: US 16, DE 8, FR 4 located and 2 unknown.
func (f *fixture) seedGeo(cities bool) {
	f.t.Helper()
	m0, m1 := fixedNow.Add(-2*time.Minute), fixedNow.Add(-time.Minute)
	g0 := &agentprotocol.GeoBucket{Countries: map[string]int64{"US": 6, "DE": 3}, Unknown: 1}
	g1 := &agentprotocol.GeoBucket{Countries: map[string]int64{"US": 10, "DE": 5, "FR": 4}, Unknown: 1}
	if cities {
		g0.Cities = []agentprotocol.GeoCity{{Name: "New York", Country: "US", Lat: 40.7, Lon: -74.0, N: 6}, {Name: "Berlin", Country: "DE", Lat: 52.5, Lon: 13.4, N: 3}}
		g1.Cities = []agentprotocol.GeoCity{{Name: "New York", Country: "US", Lat: 40.7, Lon: -74.0, N: 7}, {Name: "Paris", Country: "FR", Lat: 48.9, Lon: 2.4, N: 4}}
		g1.OtherCities = 8 // 3 US + 5 DE without a city record
	}
	require.NoError(f.t, f.svc.IngestTraffic(f.ctx, f.server, agentprotocol.Traffic{Seq: 1, Buckets: []agentprotocol.TrafficBucket{
		geoBucketAt(f, m0, 10, g0), geoBucketAt(f, m1, 20, g1),
	}}))
}

func (f *fixture) setServerGeo(sql string) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(), "UPDATE servers SET "+sql+" WHERE id = $1", f.server)
	require.NoError(f.t, err)
}

func TestReportCountsCountriesAndShares(t *testing.T) {
	f := newFixture(t)
	f.seedGeo(true) // the server counts countries only: city lists are not stored
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "15m"})
	require.NoError(t, err)
	require.NotNil(t, rep.Geo)
	require.Empty(t, rep.GeoReason)
	g := rep.Geo
	require.Equal(t, agentprotocol.GeoLevelCountry, g.Level)
	require.Equal(t, agentprotocol.GeoSourceDBIP, g.Source)
	require.Len(t, g.Attribution, 1)
	require.Equal(t, "https://db-ip.com", g.Attribution[0].URL)
	require.EqualValues(t, 28, g.LocatedRequests)
	require.EqualValues(t, 2, g.UnknownRequests)
	require.Len(t, g.Countries, 3)
	require.Equal(t, GeoCountry{ISO2: "US", Name: "United States", Requests: 16, Share: 16.0 / 28}, g.Countries[0])
	require.Equal(t, "DE", g.Countries[1].ISO2)
	require.Equal(t, "Germany", g.Countries[1].Name)
	require.Equal(t, "FR", g.Countries[2].ISO2)
	var share float64
	for _, c := range g.Countries {
		share += c.Share
	}
	require.InDelta(t, 1, share, 1e-9, "country shares add up to the located requests")
	require.Empty(t, g.Cities, "country level stores no cities")
}

func TestReportListsCitiesAtTheCityLevel(t *testing.T) {
	f := newFixture(t)
	f.setServerGeo("geo_level = 'city'")
	f.seedGeo(true)
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "15m"})
	require.NoError(t, err)
	g := rep.Geo
	require.NotNil(t, g)
	require.Equal(t, agentprotocol.GeoLevelCity, g.Level)
	require.Len(t, g.Cities, 3)
	require.Equal(t, GeoCity{Name: "New York", Country: "US", Lat: 40.7, Lon: -74.0, Requests: 13, Share: 13.0 / 28}, g.Cities[0])
	require.Equal(t, "Paris", g.Cities[1].Name)
	require.EqualValues(t, 4, g.Cities[1].Requests)
	require.Equal(t, "Berlin", g.Cities[2].Name)
}

func TestGeoSurvivesRollupsWithTheSameSums(t *testing.T) {
	f := newFixture(t)
	f.setServerGeo("geo_level = 'city'")
	f.seedGeo(true)
	ctx := context.Background()
	_, err := f.svc.RollupHourly(ctx, fixedNow.Add(-3*time.Hour), fixedNow.Add(time.Minute))
	require.NoError(t, err)
	_, err = f.svc.RollupDaily(ctx, fixedNow.AddDate(0, 0, -1), fixedNow.Add(time.Minute))
	require.NoError(t, err)
	// Idempotent: a second rollup does not double count.
	_, err = f.svc.RollupHourly(ctx, fixedNow.Add(-3*time.Hour), fixedNow.Add(time.Minute))
	require.NoError(t, err)

	for _, rng := range []string{"15m", "7d", "30d"} { // minute, hour and day tier
		rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: rng})
		require.NoError(t, err, rng)
		require.NotNil(t, rep.Geo, rng)
		require.EqualValues(t, 28, rep.Geo.LocatedRequests, rng)
		require.EqualValues(t, 2, rep.Geo.UnknownRequests, rng)
		require.Equal(t, "US", rep.Geo.Countries[0].ISO2, rng)
		require.EqualValues(t, 16, rep.Geo.Countries[0].Requests, rng)
		require.Equal(t, "New York", rep.Geo.Cities[0].Name, rng)
		require.EqualValues(t, 13, rep.Geo.Cities[0].Requests, rng)
	}
	var hourly string
	require.NoError(t, f.pool.QueryRow(ctx, "SELECT geo::text FROM traffic_1h WHERE host = 'app.example.com'").Scan(&hourly))
	require.Contains(t, hourly, `"US": 16`)
}

func TestGeoBucketKeepsTheBusiestCitiesAndConservesRequests(t *testing.T) {
	var g geoSum
	const n = 150
	var located int64
	cities := make([]agentprotocol.GeoCity, n)
	for i := range cities {
		cities[i] = agentprotocol.GeoCity{Name: fmt.Sprintf("Town %03d", i), Country: "DE", Lat: float64(i) / 10, Lon: 1, N: int64(n - i)}
		located += cities[i].N
	}
	g.add(&agentprotocol.GeoBucket{Countries: map[string]int64{"DE": located}, Cities: cities, OtherCities: 5, Unknown: 3})
	b := g.bucket(maxRolledCities)
	require.Len(t, b.Cities, maxRolledCities)
	require.Equal(t, "Town 000", b.Cities[0].Name)
	var inList int64
	for _, c := range b.Cities {
		inList += c.N
	}
	require.EqualValues(t, located+5, inList+b.OtherCities, "cities beyond the cap move to otherCities")
	require.EqualValues(t, 3, b.Unknown)
	require.EqualValues(t, located, b.Countries["DE"])
}

func TestDisabledGeoIsAbsentWithTheReason(t *testing.T) {
	f := newFixture(t)
	f.seedGeo(false)

	f.setServerGeo("geo_enabled = false")
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "15m"})
	require.NoError(t, err)
	require.Nil(t, rep.Geo)
	require.Equal(t, GeoReasonDisabled, rep.GeoReason)
	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"geo":`)
	require.Contains(t, string(raw), `"geoReason":"geo_disabled"`)

	// Frames that arrive while it is off store no origins.
	require.NoError(t, f.svc.IngestTraffic(f.ctx, f.server, agentprotocol.Traffic{Seq: 2, Buckets: []agentprotocol.TrafficBucket{
		geoBucketAt(f, fixedNow.Add(-5*time.Minute), 4, &agentprotocol.GeoBucket{Countries: map[string]int64{"US": 4}}),
	}}))
	var geo string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT geo::text FROM traffic_1m WHERE bucket = $1", fixedNow.Add(-5*time.Minute).Truncate(time.Minute)).Scan(&geo))
	require.JSONEq(t, "{}", geo)

	// Traffic analytics off wins as the reason.
	f.setServerGeo("geo_enabled = true, traffic_analytics_enabled = false")
	rep, err = f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "15m"})
	require.NoError(t, err)
	require.Nil(t, rep.Geo)
	require.Equal(t, GeoReasonTrafficDisabled, rep.GeoReason)
}

func TestEnabledGeoWithoutDataIsEmptyNotAbsent(t *testing.T) {
	f := newFixture(t)
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "15m"})
	require.NoError(t, err)
	require.NotNil(t, rep.Geo)
	require.Empty(t, rep.Geo.Countries)
	raw, err := json.Marshal(rep.Geo)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"level", "source", "attribution", "locatedRequests", "unknownRequests", "countries", "cities"} {
		require.Contains(t, m, k)
		require.NotNil(t, m[k], "%s serializes as [] or a value, never null", k)
	}
}

func TestMixedSourcesListBothAttributions(t *testing.T) {
	f := newFixture(t)
	other, err := f.q.CreateServer(context.Background(), db.CreateServerParams{WorkspaceID: f.ws, Name: "edge-2"})
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), "UPDATE servers SET geo_source = 'maxmind' WHERE id = $1", other.ID)
	require.NoError(t, err)
	rep, err := f.svc.WorkspaceTraffic(f.ctx, Filter{}, Query{Range: "15m"})
	require.NoError(t, err)
	require.NotNil(t, rep.Geo)
	require.Equal(t, "mixed", rep.Geo.Source)
	require.Len(t, rep.Geo.Attribution, 2)
	var urls []string
	for _, a := range rep.Geo.Attribution {
		urls = append(urls, a.URL)
	}
	require.ElementsMatch(t, []string{"https://db-ip.com", "https://www.maxmind.com"}, urls)
}

var ipv4 = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

func TestIngestSanitizesOriginsAndStoresNoAddresses(t *testing.T) {
	f := newFixture(t)
	f.setServerGeo("geo_level = 'city'")
	long := strings.Repeat("x", 200)
	require.NoError(t, f.svc.IngestTraffic(f.ctx, f.server, agentprotocol.Traffic{Seq: 1, Buckets: []agentprotocol.TrafficBucket{
		geoBucketAt(f, fixedNow.Add(-time.Minute), 10, &agentprotocol.GeoBucket{
			Countries: map[string]int64{"US": 5, "us": 9, "ZZZ": 9, "1.": 3, "DE": -4, "FR": 2},
			Cities: []agentprotocol.GeoCity{
				{Name: "Springfield", Country: "US", Lat: 39.801, Lon: -89.644, N: 5},
				{Name: "Nowhere", Country: "US", Lat: 999, Lon: 0, N: 9}, // impossible latitude
				{Name: "", Country: "US", Lat: 1, Lon: 1, N: 9},          // nameless
				{Name: long, Country: "FR", Lat: 48.9, Lon: 2.4, N: 2},
			},
			Unknown: 1,
		}),
	}}))
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "15m"})
	require.NoError(t, err)
	g := rep.Geo
	require.NotNil(t, g)
	require.Len(t, g.Countries, 2, "lower-case, three-letter, malformed and negative entries are dropped")
	require.Equal(t, "US", g.Countries[0].ISO2)
	require.Len(t, g.Cities, 2)
	require.Equal(t, 39.8, g.Cities[0].Lat, "coordinates are rounded to 0.1°")
	require.Equal(t, -89.6, g.Cities[0].Lon)
	require.Len(t, g.Cities[1].Name, maxCityNameLen)

	var dump string
	require.NoError(t, f.pool.QueryRow(context.Background(), "SELECT row_to_json(t)::text FROM traffic_1m t").Scan(&dump))
	require.Empty(t, ipv4.FindAllString(dump, -1), "no client address in the row: %s", dump)
}

func TestMaxMindSourceNeedsNoSchemaChangeToReport(t *testing.T) {
	f := newFixture(t)
	f.setServerGeo("geo_source = 'maxmind'")
	f.seedGeo(false)
	rep, err := f.svc.ServiceTraffic(f.ctx, f.service, Query{Range: "15m"})
	require.NoError(t, err)
	require.Equal(t, agentprotocol.GeoSourceMaxMind, rep.Geo.Source)
	require.Equal(t, "https://www.maxmind.com", rep.Geo.Attribution[0].URL)
}
