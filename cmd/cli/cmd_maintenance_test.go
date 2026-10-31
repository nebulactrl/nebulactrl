package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseETA(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	got, err := parseETA("45m", now)
	require.NoError(t, err)
	require.Equal(t, now.Add(45*time.Minute), got)

	got, err = parseETA("2026-11-02T09:30:00Z", now)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC), got)

	for _, bad := range []string{"soon", "-5m", "0s", ""} {
		_, err = parseETA(bad, now)
		require.Error(t, err, bad)
	}
}
