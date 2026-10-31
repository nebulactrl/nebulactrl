package traffic

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The fixtures are MaxMind's official test databases (see testdata/geoip/README.md).
const (
	cityFixture    = "testdata/geoip/GeoIP2-City-Test.mmdb"
	countryFixture = "testdata/geoip/GeoIP2-Country-Test.mmdb"

	ipLondon   = "81.2.69.142"
	ipMilton   = "216.160.83.56"
	ipLinkping = "89.160.20.112"
	ipBhutan   = "67.43.156.0"
)

func loadGeoDB(t *testing.T, path, level string) *GeoDB {
	t.Helper()
	r, err := openGeoDatabase(path, level)
	require.NoError(t, err)
	g := &GeoDB{}
	g.install(r, level)
	t.Cleanup(g.Close)
	return g
}

func TestGeoDBLocatesCountriesAndCities(t *testing.T) {
	city := loadGeoDB(t, cityFixture, agentprotocol.GeoLevelCity)

	loc, ok := city.Locate(ipLondon)
	require.True(t, ok)
	require.Equal(t, "GB", loc.Country)
	require.Equal(t, "London", loc.City)
	require.InDelta(t, 51.5, loc.Lat, 0.11)
	require.InDelta(t, -0.1, loc.Lon, 0.11)
	require.Equal(t, math.Round(loc.Lat*10)/10, loc.Lat, "coordinates are rounded to 0.1°")
	require.Equal(t, math.Round(loc.Lon*10)/10, loc.Lon)

	loc, ok = city.Locate(ipBhutan)
	require.True(t, ok, "a country without a city record is still located")
	require.Equal(t, "BT", loc.Country)
	require.Empty(t, loc.City)

	// A country database at the country level never reports a city.
	country := loadGeoDB(t, countryFixture, agentprotocol.GeoLevelCountry)
	loc, ok = country.Locate(ipMilton)
	require.True(t, ok)
	require.Equal(t, Location{Country: "US"}, loc)

	// The city database at the country level keeps only the country.
	cityAsCountry := loadGeoDB(t, cityFixture, agentprotocol.GeoLevelCountry)
	loc, ok = cityAsCountry.Locate(ipLondon)
	require.True(t, ok)
	require.Equal(t, Location{Country: "GB"}, loc)
}

func TestGeoDBRejectsPrivateUnparsableAndUnlisted(t *testing.T) {
	g := loadGeoDB(t, cityFixture, agentprotocol.GeoLevelCity)
	for _, ip := range []string{"10.1.2.3", "192.168.0.9", "127.0.0.1", "::1", "169.254.1.1", "0.0.0.0", "", "not-an-ip", "203.0.113.5"} {
		_, ok := g.Locate(ip)
		require.False(t, ok, ip)
	}
	_, ok := (&GeoDB{}).Locate(ipLondon)
	require.False(t, ok, "no database loaded")
	require.Empty(t, (&GeoDB{}).Level())
}

func TestGeoDatabaseEditionMustMatchLevel(t *testing.T) {
	_, err := openGeoDatabase(countryFixture, agentprotocol.GeoLevelCity)
	require.ErrorIs(t, err, errDatabaseType, "a country database cannot serve the city level")
	r, err := openGeoDatabase(cityFixture, agentprotocol.GeoLevelCountry)
	require.NoError(t, err)
	require.NoError(t, r.Close())
}

func geoEvent(ip string, at time.Time) Event {
	return Event{
		Time: at, ServiceID: svcA, Host: "app.localhost", Status: 200, Method: "GET", Path: "/",
		ClientIP: ip, UserAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox/125.0", ContentType: "text/html",
	}
}

func TestAggregatorCountsOriginsWithoutShippingIPs(t *testing.T) {
	a := newAgg()
	a.SetGeo(loadGeoDB(t, cityFixture, agentprotocol.GeoLevelCity))
	at := time.Date(2026, 10, 26, 12, 0, 10, 0, time.UTC)
	ips := []string{ipLondon, ipLondon, ipLondon, ipMilton, ipLinkping, ipBhutan, "10.0.0.7", "198.51.100.77"}
	for _, ip := range ips {
		a.Add(geoEvent(ip, at))
	}
	out := a.Flush(at.Add(2 * time.Minute))
	require.Len(t, out, 1)
	b := out[0]
	require.EqualValues(t, len(ips), b.Requests)
	require.NotNil(t, b.Geo)
	require.Equal(t, map[string]int64{"GB": 3, "US": 1, "SE": 1, "BT": 1}, b.Geo.Countries)
	require.EqualValues(t, 2, b.Geo.Unknown, "a private address and an unlisted one")
	require.EqualValues(t, 1, b.Geo.OtherCities, "the Bhutan request has no city record")
	require.Len(t, b.Geo.Cities, 3)
	require.Equal(t, "London", b.Geo.Cities[0].Name, "busiest city first")
	require.EqualValues(t, 3, b.Geo.Cities[0].N)
	require.Equal(t, "GB", b.Geo.Cities[0].Country)

	// No client address is in the frame (or in anything derived from it).
	frame, err := json.Marshal(agentprotocol.Traffic{Seq: 1, Buckets: out})
	require.NoError(t, err)
	for _, ip := range ips {
		require.NotContains(t, string(frame), ip)
	}
	require.NotContains(t, string(frame), "198.51.100")
}

func TestAggregatorCountryLevelHasNoCities(t *testing.T) {
	a := newAgg()
	a.SetGeo(loadGeoDB(t, countryFixture, agentprotocol.GeoLevelCountry))
	at := time.Date(2026, 10, 26, 12, 0, 10, 0, time.UTC)
	a.Add(geoEvent(ipLondon, at))
	a.Add(geoEvent(ipMilton, at))
	out := a.Flush(at.Add(2 * time.Minute))
	require.Equal(t, map[string]int64{"GB": 1, "US": 1}, out[0].Geo.Countries)
	require.Empty(t, out[0].Geo.Cities)
	require.Zero(t, out[0].Geo.OtherCities)
}

func TestAggregatorWithoutGeoSendsNone(t *testing.T) {
	a := newAgg()
	at := time.Date(2026, 10, 26, 12, 0, 10, 0, time.UTC)
	a.Add(geoEvent(ipLondon, at))
	require.Nil(t, a.Flush(at.Add(2 * time.Minute))[0].Geo, "no database configured")

	a = newAgg()
	a.SetGeo(&GeoDB{}) // configured but nothing downloaded yet
	a.Add(geoEvent(ipLondon, at))
	require.Nil(t, a.Flush(at.Add(2 * time.Minute))[0].Geo, "no database loaded yet")
}

// manyCities places every address in its own "city".
type manyCities struct{}

func (manyCities) Level() string { return agentprotocol.GeoLevelCity }
func (manyCities) Locate(ip string) (Location, bool) {
	var n int
	_, _ = fmt.Sscanf(ip, "192.0.2.%d", &n)
	return Location{Country: "DE", City: fmt.Sprintf("Town %03d", n), Lat: float64(n) / 10, Lon: 1}, true
}

func TestAggregatorCapsCitiesPerMinute(t *testing.T) {
	a := newAgg()
	a.SetGeo(manyCities{})
	at := time.Date(2026, 10, 26, 12, 0, 10, 0, time.UTC)
	const total = 60
	for i := 1; i <= total; i++ {
		a.Add(geoEvent(fmt.Sprintf("192.0.2.%d", i), at))
	}
	a.Add(geoEvent("192.0.2.1", at)) // the first town is the busiest
	g := a.Flush(at.Add(2 * time.Minute))[0].Geo
	require.Len(t, g.Cities, agentprotocol.MaxGeoCitiesPerBucket)
	require.Equal(t, "Town 001", g.Cities[0].Name)
	require.EqualValues(t, 2, g.Cities[0].N)
	var shown int64
	for _, c := range g.Cities {
		shown += c.N
	}
	require.EqualValues(t, total+1, shown+g.OtherCities, "every request is a listed city or in Other")
	require.EqualValues(t, total+1, g.Countries["DE"])
}

// ---- download, verify, swap ----

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(data)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func tarGz(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "GeoLite2-Test_20261020/COPYRIGHT.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg}))
	_, _ = tw.Write([]byte("hi\n"))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}))
	_, err := tw.Write(data)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func newManager(t *testing.T, base string) (*GeoManager, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "geoip")
	m := NewGeoManager(dir, slog.New(slog.DiscardHandler))
	m.DBIPBase, m.MaxMindBase = base, base
	m.Now = func() time.Time { return time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC) }
	t.Cleanup(m.DB().Close)
	return m, dir
}

func mmdbFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".mmdb") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestManagerDownloadsDBIPAndKeepsOnlyTheCurrentDatabase(t *testing.T) {
	country := gz(t, readFixture(t, countryFixture))
	city := gz(t, readFixture(t, cityFixture))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/dbip-country-lite-2026-10.mmdb.gz":
			_, _ = w.Write(country)
		case "/dbip-city-lite-2026-10.mmdb.gz":
			_, _ = w.Write(city)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m, dir := newManager(t, srv.URL)
	ctx := context.Background()

	m.Apply(agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceDBIP, Level: agentprotocol.GeoLevelCountry, RefreshDays: 7}, nil)
	require.NotNil(t, m.Status())
	require.False(t, m.Status().Downloaded, "enabled but nothing downloaded yet")
	require.Equal(t, 7*24*time.Hour, m.syncOnce(ctx))
	require.Equal(t, []string{"dbip-country-2026-10.mmdb"}, mmdbFiles(t, dir))
	require.Equal(t, agentprotocol.GeoLevelCountry, m.DB().Level())
	loc, ok := m.DB().Locate(ipLondon)
	require.True(t, ok)
	require.Equal(t, "GB", loc.Country)
	require.NoFileExists(t, filepath.Join(dir, geoTmpFile))
	st := m.Status()
	require.True(t, st.Downloaded)
	require.Equal(t, agentprotocol.GeoSourceDBIP, st.Source)
	require.Equal(t, agentprotocol.GeoLevelCountry, st.Level)
	require.Equal(t, "2026-10", st.Label)
	require.Positive(t, st.SizeBytes)
	require.NotNil(t, st.CheckedAt)

	// Not due: no request.
	before := hits.Load()
	m.syncOnce(ctx)
	require.Equal(t, before, hits.Load())

	// Due, but already current: a check, no second download of the same file.
	m.mu.Lock()
	m.state.CheckedAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m.mu.Unlock()
	hits.Store(0)
	m.syncOnce(ctx)
	require.EqualValues(t, 0, hits.Load(), "an installed database of this month needs no request")

	// A changed level deletes the country database and downloads the city one.
	m.Apply(agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceDBIP, Level: agentprotocol.GeoLevelCity, RefreshDays: 7}, nil)
	require.Empty(t, mmdbFiles(t, dir), "the old database is deleted at once")
	_, ok = m.DB().Locate(ipLondon)
	require.False(t, ok)
	m.syncOnce(ctx)
	require.Equal(t, []string{"dbip-city-2026-10.mmdb"}, mmdbFiles(t, dir))
	loc, ok = m.DB().Locate(ipLondon)
	require.True(t, ok)
	require.Equal(t, "London", loc.City)

	// Turning geolocation off removes the database entirely.
	m.Apply(agentprotocol.GeoConfig{Enabled: false}, nil)
	require.NoDirExists(t, dir)
	require.Nil(t, m.Status(), "no status while geolocation is off")
	require.Empty(t, m.DB().Level())
}

func TestManagerFallsBackToThePreviousMonthWhenTheNewFileIsNotPublishedYet(t *testing.T) {
	country := gz(t, readFixture(t, countryFixture))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dbip-country-lite-2026-09.mmdb.gz" {
			_, _ = w.Write(country)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	m, dir := newManager(t, srv.URL)
	m.Apply(agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceDBIP, Level: agentprotocol.GeoLevelCountry, RefreshDays: 3}, nil)
	m.syncOnce(context.Background())
	require.Equal(t, []string{"dbip-country-2026-09.mmdb"}, mmdbFiles(t, dir))
}

func TestManagerRejectsACorruptDownloadAndKeepsTheOldDatabase(t *testing.T) {
	country := readFixture(t, countryFixture)
	var corrupt atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := country
		if corrupt.Load() {
			data = append([]byte("not a database "), country[:2000]...)
		}
		_, _ = w.Write(gz(t, data))
	}))
	defer srv.Close()
	m, dir := newManager(t, srv.URL)
	m.Apply(agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceDBIP, Level: agentprotocol.GeoLevelCountry, RefreshDays: 1}, nil)
	m.syncOnce(context.Background())
	require.Equal(t, []string{"dbip-country-2026-10.mmdb"}, mmdbFiles(t, dir))

	// A month later the new file is garbage: verification fails, nothing is swapped.
	corrupt.Store(true)
	m.Now = func() time.Time { return time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) }
	wait := m.syncOnce(context.Background())
	require.Equal(t, geoRetryMax, wait)
	require.NotEmpty(t, m.LastError())
	require.Equal(t, []string{"dbip-country-2026-10.mmdb"}, mmdbFiles(t, dir))
	require.NoFileExists(t, filepath.Join(dir, geoTmpFile))
	_, ok := m.DB().Locate(ipLondon)
	require.True(t, ok, "the previous database keeps serving")
}

func TestManagerRejectsADatabaseOfTheWrongEdition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gz(t, readFixture(t, countryFixture))) // a country database where city is configured
	}))
	defer srv.Close()
	m, dir := newManager(t, srv.URL)
	m.Apply(agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceDBIP, Level: agentprotocol.GeoLevelCity, RefreshDays: 7}, nil)
	m.syncOnce(context.Background())
	require.Empty(t, mmdbFiles(t, dir))
	require.Contains(t, m.LastError(), "edition")
	require.Empty(t, m.DB().Level())
}

func TestManagerDownloadsMaxMindWithBasicAuthAndSkipsUnchangedReleases(t *testing.T) {
	archive := tarGz(t, "GeoLite2-Test_20261020/GeoLite2-Country.mmdb", readFixture(t, countryFixture))
	released := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "123456" || pass != "sekret-license-key" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		require.Equal(t, "/geoip/databases/GeoLite2-Country/download", r.URL.Path)
		require.Equal(t, "tar.gz", r.URL.Query().Get("suffix"))
		w.Header().Set("Last-Modified", released.Format(http.TimeFormat))
		if r.Method == http.MethodGet {
			gets.Add(1)
			_, _ = w.Write(archive)
		}
	}))
	defer srv.Close()
	m, dir := newManager(t, srv.URL)
	cfg := agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceMaxMind, Level: agentprotocol.GeoLevelCountry, RefreshDays: 1}

	// Wrong key: rejected with a clear reason, nothing stored.
	m.Apply(cfg, map[string]string{agentprotocol.SecretGeoMaxMindAccount: "123456", agentprotocol.SecretGeoMaxMindKey: "wrong"})
	m.syncOnce(context.Background())
	require.Contains(t, m.LastError(), "rejected")
	require.Empty(t, mmdbFiles(t, dir))

	// Without credentials at all the agent says so and does not call out.
	m.Apply(cfg, nil)
	m.syncOnce(context.Background())
	require.Contains(t, m.LastError(), "account ID")

	secrets := map[string]string{agentprotocol.SecretGeoMaxMindAccount: "123456", agentprotocol.SecretGeoMaxMindKey: "sekret-license-key"}
	m.Apply(cfg, secrets)
	m.syncOnce(context.Background())
	require.Equal(t, []string{"maxmind-country-20261020.mmdb"}, mmdbFiles(t, dir))
	require.EqualValues(t, 1, gets.Load())
	require.Empty(t, m.LastError())

	// A day later the HEAD check sees the same release: no download.
	m.Now = func() time.Time { return time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC) }
	m.syncOnce(context.Background())
	require.EqualValues(t, 1, gets.Load())

	// The licence key is never written to the state directory.
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		require.NoError(t, err)
		if !info.IsDir() && !strings.HasSuffix(path, ".mmdb") {
			require.NotContains(t, string(readFixture(t, path)), "sekret-license-key")
		}
		return nil
	})
	require.NoError(t, err)
}

func TestManagerRestoresTheInstalledDatabaseAfterARestart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gz(t, readFixture(t, countryFixture)))
	}))
	defer srv.Close()
	m, dir := newManager(t, srv.URL)
	m.Apply(agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceDBIP, Level: agentprotocol.GeoLevelCountry, RefreshDays: 7}, nil)
	m.syncOnce(context.Background())
	m.DB().Close()

	again := NewGeoManager(dir, slog.New(slog.DiscardHandler))
	t.Cleanup(again.DB().Close)
	require.Equal(t, agentprotocol.GeoLevelCountry, again.DB().Level(), "origins keep counting before the first reconcile")
	loc, ok := again.DB().Locate(ipMilton)
	require.True(t, ok)
	require.Equal(t, "US", loc.Country)
}

func TestNormalizeGeoClampsTheAgentSideView(t *testing.T) {
	got := normalizeGeo(agentprotocol.GeoConfig{Enabled: true, Source: "bogus", Level: "street", RefreshDays: 0})
	require.Equal(t, agentprotocol.GeoConfig{Enabled: true, Source: agentprotocol.GeoSourceDBIP, Level: agentprotocol.GeoLevelCountry, RefreshDays: 7}, got)
	require.Equal(t, 90, normalizeGeo(agentprotocol.GeoConfig{RefreshDays: 500}).RefreshDays)
}
