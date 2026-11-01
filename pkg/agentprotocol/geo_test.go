package agentprotocol

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"
)

func TestTrafficBucketGeoRoundTripsAndOmitsWhenOff(t *testing.T) {
	off, err := json.Marshal(TrafficBucket{Host: "a.example.com", Bucket: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(off, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["geo"]; ok {
		t.Fatalf("geo must be omitted while geolocation is off: %s", off)
	}

	in := TrafficBucket{Host: "a.example.com", Geo: &GeoBucket{
		Countries: map[string]int64{"US": 3},
		Cities:    []GeoCity{{Name: "Milton", Country: "US", Lat: 47.3, Lon: -122.3, N: 3}},
		Unknown:   1,
	}}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out TrafficBucket
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Geo == nil || out.Geo.Countries["US"] != 3 || out.Geo.Cities[0].Name != "Milton" || out.Geo.Unknown != 1 {
		t.Fatalf("round trip lost data: %+v", out.Geo)
	}
	// The wire form carries counts and rounded coordinates, never an address.
	if regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`).Match(data) {
		t.Fatalf("frame contains something shaped like an IP address: %s", data)
	}
}

func TestReconcileGeoIsOptionalAndSecretsStaySeparate(t *testing.T) {
	data, err := json.Marshal(Reconcile{Services: []DesiredService{}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["geo"]; ok {
		t.Fatalf("an unset geo setting must be omitted (unchanged on the agent): %s", data)
	}
	withGeo, err := json.Marshal(Reconcile{Geo: &GeoConfig{Enabled: true, Source: GeoSourceDBIP, Level: GeoLevelCountry, RefreshDays: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?i)license|account`).Match(withGeo) {
		t.Fatalf("the geo setting itself carries no credentials: %s", withGeo)
	}
}
