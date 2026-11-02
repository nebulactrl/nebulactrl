package api

import (
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

func TestPageSizeClampsToTheDocumentedBounds(t *testing.T) {
	for limit, want := range map[int]int{-5: DefaultPageSize, 0: DefaultPageSize, 1: 1, 50: 50, 200: 200, 201: MaxPageSize, 10_000: MaxPageSize} {
		require.Equal(t, want, PageQuery{Limit: limit}.Size(), "limit %d", limit)
	}
}

func TestTimeCursorRoundTripsAtMicrosecondPrecision(t *testing.T) {
	id := uuid.New()
	at := time.Date(2026, 11, 1, 21, 45, 30, 123456789, time.FixedZone("CET", 3600))

	c := EncodeTimeCursor(at, id)
	require.NotContains(t, c, "=", "the cursor is URL-safe without padding")
	got, gotID, ok, err := DecodeTimeCursor(c)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, id, gotID)
	require.Equal(t, time.UTC, got.Location(), "the position is UTC")
	require.True(t, got.Equal(at.Truncate(time.Microsecond)), "got %s, want %s", got, at.Truncate(time.Microsecond))

	// A time before the epoch survives as well.
	old := time.Date(1969, 7, 20, 20, 17, 40, 5000, time.UTC)
	got, _, ok, err = DecodeTimeCursor(EncodeTimeCursor(old, id))
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, got.Equal(old))

	// Different positions never share a cursor.
	require.NotEqual(t, c, EncodeTimeCursor(at.Add(time.Microsecond), id))
	require.NotEqual(t, c, EncodeTimeCursor(at, uuid.New()))
}

func TestEmptyCursorMeansTheFirstPage(t *testing.T) {
	_, id, ok, err := DecodeTimeCursor("")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, uuid.Nil, id)
}

func TestMalformedCursorsAreA400(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, c := range map[string]string{
		"not base64":      "***not base64***",
		"padded base64":   base64.URLEncoding.EncodeToString([]byte("1.x")),
		"no separator":    b64("1700000000000000"),
		"time not number": b64("soon." + uuid.NewString()),
		"time overflow":   b64("99999999999999999999." + uuid.NewString()),
		"id not a uuid":   b64("1700000000000000.not-a-uuid"),
		"empty id":        b64("1700000000000000."),
		"only a dot":      b64("."),
	} {
		_, _, ok, err := DecodeTimeCursor(c)
		require.False(t, ok, name)
		ae, isAPI := apierr.As(err)
		require.True(t, isAPI, "%s: want an API error, got %v", name, err)
		require.Equal(t, http.StatusBadRequest, ae.Status, name)
		require.Equal(t, "invalid_cursor", ae.Code, name)
	}
}
