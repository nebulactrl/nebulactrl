package cfzone

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchLongestSuffix(t *testing.T) {
	zones := []Zone{{ID: "1", Name: "example.com"}, {ID: "2", Name: "apps.example.com"}, {ID: "3", Name: "other.org"}}
	for host, want := range map[string]string{
		"web.apps.example.com": "2",
		"WEB.example.com.":     "1",
		"example.com":          "1",
		"*.apps.example.com":   "2",
	} {
		z, ok := Match(zones, host)
		require.True(t, ok, host)
		require.Equal(t, want, z.ID, host)
	}
	for _, host := range []string{"notexample.com", "x.unknown.dev", ""} {
		_, ok := Match(zones, host)
		require.False(t, ok, host)
	}
}

func TestDecodeNeverReturnsNil(t *testing.T) {
	require.Equal(t, []Zone{}, Decode(nil))
	require.Equal(t, []Zone{}, Decode(json.RawMessage(`null`)))
	require.Equal(t, []Zone{}, Decode(json.RawMessage(`{`)))
	zs := Decode(json.RawMessage(`[{"id":"1","name":"a.com"},{"id":"2","name":"b.com"}]`))
	require.Len(t, zs, 2)
	require.Equal(t, "b.com", zs[1].Name)
}
