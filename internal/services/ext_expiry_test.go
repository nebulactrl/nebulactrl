package services

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Fetched external values do not stay in memory until the next restart: they
// expire after extTTL, and the generation moves so cached mask lists are rebuilt.
func TestExtValuesExpire(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	e := newExtValues()
	e.now = func() time.Time { return now }
	svc, other := uuid.New(), uuid.New()

	e.remember(svc, []string{"old-secret"})
	now = now.Add(extTTL / 2)
	e.remember(svc, []string{"new-secret"})
	e.remember(other, []string{"other-secret"})
	vals, gen := e.values(svc)
	require.ElementsMatch(t, []string{"old-secret", "new-secret"}, vals)

	// The first batch is older than the TTL, the second is not.
	now = now.Add(extTTL/2 + time.Minute)
	vals, gen2 := e.values(svc)
	require.Equal(t, []string{"new-secret"}, vals)
	require.Greater(t, gen2, gen, "an expiry invalidates cached mask lists")

	// Everything expires; nothing is left behind.
	now = now.Add(extTTL)
	vals, gen3 := e.values(svc)
	require.Empty(t, vals)
	require.Greater(t, gen3, gen2)
	vals, _ = e.values(other)
	require.Empty(t, vals)
	require.Empty(t, e.batches)

	// A quiet period without expiry leaves the generation alone.
	e.remember(svc, []string{"fresh"})
	g := e.generation()
	require.Equal(t, g, e.generation())
}
