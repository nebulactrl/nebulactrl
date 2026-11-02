package analytics

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestRankedKeepsTheTopNAndFoldsTheRestIntoOther(t *testing.T) {
	s := newSum(true)
	// topListLen+3 keys with distinct counts; the three smallest are folded away.
	var folded int64
	for i := range topListLen + 3 {
		k := fmt.Sprintf("/p%02d", i)
		n := int64(1000 - i*10)
		s.tops[dimPaths][k] = &agentprotocol.TopEntry{K: k, N: n, E: 1, B: 100, Ms: 5}
		if i >= topListLen {
			folded += n
		}
	}
	// A merged "Other" from the agents counts towards the folded rest.
	s.tops[dimPaths][agentprotocol.OtherKey] = &agentprotocol.TopEntry{K: agentprotocol.OtherKey, N: 40, E: 2, B: 7, Ms: 1}

	got := s.ranked(dimPaths)
	require.Len(t, got, topListLen+1)
	require.Equal(t, "/p00", got[0].K)
	require.Equal(t, fmt.Sprintf("/p%02d", topListLen-1), got[topListLen-1].K)
	other := got[topListLen]
	require.Equal(t, agentprotocol.OtherKey, other.K, "Other stays last whatever its count")
	require.Equal(t, folded+40, other.N)
	require.EqualValues(t, 3+2, other.E)
	require.EqualValues(t, 300+7, other.B)
	require.InDelta(t, 15+1, other.Ms, 1e-9)
	for i := 1; i < topListLen; i++ {
		require.GreaterOrEqual(t, got[i-1].N, got[i].N)
	}
}

func TestRankedBreaksTiesByKeyAndOmitsAnEmptyOther(t *testing.T) {
	s := newSum(true)
	for _, k := range []string{"b", "c", "a"} {
		s.tops[dimBrowsers][k] = &agentprotocol.TopEntry{K: k, N: 5}
	}
	got := s.ranked(dimBrowsers)
	require.Equal(t, []string{"a", "b", "c"}, []string{got[0].K, got[1].K, got[2].K})
	require.Len(t, got, 3, "no Other entry when nothing was folded")
	require.Empty(t, s.ranked(dimOS))
}

func TestStatusCodesAreSortedAndIgnoreGarbage(t *testing.T) {
	s := newSum(true)
	s.codes = map[string]int64{"200": 50, "404": 7, "301": 7, "oops": 1000, "": 3}
	require.Equal(t, []StatusCount{{Code: 200, N: 50}, {Code: 301, N: 7}, {Code: 404, N: 7}}, s.statusCodes(),
		"largest first, ties by code, non-numeric keys dropped")
	require.Empty(t, newSum(true).statusCodes())
}

func TestCountryNameFallsBackToTheCode(t *testing.T) {
	require.Equal(t, "United States", countryName("US"))
	require.Equal(t, "Germany", countryName("DE"))
	require.Equal(t, "ZZ", countryName("ZZ"), "an unlisted code is shown as is")
	require.Equal(t, "xx", countryName("xx"))
}

func TestSortedCitiesRanksByRequestsThenNameThenCountry(t *testing.T) {
	g := &geoSum{}
	g.add(&agentprotocol.GeoBucket{Cities: []agentprotocol.GeoCity{
		{Name: "Paris", Country: "FR", Lat: 48.9, Lon: 2.4, N: 5},
		{Name: "Paris", Country: "US", Lat: 33.7, Lon: -95.5, N: 5},
		{Name: "Berlin", Country: "DE", Lat: 52.5, Lon: 13.4, N: 5},
		{Name: "Oslo", Country: "NO", Lat: 59.9, Lon: 10.7, N: 9},
	}})
	// The same city again adds up instead of listing twice.
	g.add(&agentprotocol.GeoBucket{Cities: []agentprotocol.GeoCity{{Name: "Berlin", Country: "DE", Lat: 52.5, Lon: 13.4, N: 1}}})

	got := g.sortedCities()
	var names []string
	for _, c := range got {
		names = append(names, fmt.Sprintf("%s/%s/%d", c.Name, c.Country, c.N))
	}
	require.Equal(t, []string{"Oslo/NO/9", "Berlin/DE/6", "Paris/FR/5", "Paris/US/5"}, names)
}

func TestCleanListBoundsWhatAnAgentSends(t *testing.T) {
	long := strings.Repeat("k", maxKeyLen+50)
	in := []agentprotocol.TopEntry{
		{K: "", N: 5},
		{K: long, N: -3, E: -1, B: maxCounterValue * 4, Ms: -9},
		{K: "/ok", N: 2, E: 1, B: 3, Ms: 4.5},
	}
	got := cleanList(in)
	require.Len(t, got, 2, "an empty key is dropped")
	require.Len(t, got[0].K, maxKeyLen, "keys are cut to the maximum length")
	require.Zero(t, got[0].N)
	require.Zero(t, got[0].E)
	require.Equal(t, maxCounterValue, got[0].B, "counters are clamped")
	require.Zero(t, got[0].Ms)
	require.Equal(t, agentprotocol.TopEntry{K: "/ok", N: 2, E: 1, B: 3, Ms: 4.5}, got[1])

	many := make([]agentprotocol.TopEntry, maxTopEntries+10)
	for i := range many {
		many[i] = agentprotocol.TopEntry{K: fmt.Sprintf("/%d", i), N: 1}
	}
	require.Len(t, cleanList(many), maxTopEntries, "a list is cut to TopN plus Other")
	require.Empty(t, cleanList(nil))
}

func TestRankMapLimitsAndOrders(t *testing.T) {
	m := map[string]*agentprotocol.TopEntry{
		"c": {K: "c", N: 3}, "a": {K: "a", N: 3}, "b": {K: "b", N: 9}, "d": {K: "d", N: 1},
	}
	got := rankMap(m, 3)
	require.Equal(t, []string{"b", "a", "c"}, []string{got[0].K, got[1].K, got[2].K}, "count first, then key")
	require.Len(t, rankMap(m, 10), 4)
	require.Empty(t, rankMap(nil, 5))
}

func TestDecodeHLLRejectsGarbage(t *testing.T) {
	require.Nil(t, decodeHLL(nil))
	require.Nil(t, decodeHLL([]byte("not a sketch")))
	sk := decodeHLL(sketch(t, 0, 50))
	require.NotNil(t, sk)
	require.InDelta(t, 50, sk.Estimate(), 3)
}
