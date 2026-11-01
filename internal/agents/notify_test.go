package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/notifications"
	"github.com/nebulactrl/nebulactrl/internal/notifications/notificationstest"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestServerEventsAreAnnounced(t *testing.T) {
	h := newHarness(t)
	rec := &notificationstest.Recorder{}
	h.hub.SetNotifier(rec)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	beat := func(pct uint64) { a.write(agentprotocol.TypeHeartbeat, diskBeat(pct)) }

	// The first heartbeat of a freshly enrolled server: it connected.
	beat(50)
	require.Eventually(t, func() bool { return len(rec.OfType(notifications.TypeServerOnline)) == 1 }, 5*time.Second, 20*time.Millisecond)
	on := rec.OfType(notifications.TypeServerOnline)[0]
	require.Equal(t, e.serverID, *on.ServerID)
	require.Equal(t, "edge-1 connected", on.Title)
	require.Equal(t, notifications.SeverityInfo, on.Severity)

	// A heartbeat from a server that is already online is not news.
	beat(50)
	beat(60)
	time.Sleep(150 * time.Millisecond)
	require.Len(t, rec.OfType(notifications.TypeServerOnline), 1)
	require.Empty(t, rec.OfType(notifications.TypeServerDiskAlert))

	// Crossing the threshold: one alert, with the numbers. More heartbeats above it do not repeat it.
	beat(85)
	require.Eventually(t, func() bool { return len(rec.OfType(notifications.TypeServerDiskAlert)) == 1 }, 5*time.Second, 20*time.Millisecond)
	alert := rec.OfType(notifications.TypeServerDiskAlert)[0]
	require.Equal(t, e.serverID, *alert.ServerID)
	require.Equal(t, notifications.SeverityWarning, alert.Severity)
	require.Equal(t, "Server disk is 85% full", alert.Title)
	require.Equal(t, "80%", alert.Fields["Threshold"])
	beat(90)
	time.Sleep(150 * time.Millisecond)
	require.Len(t, rec.OfType(notifications.TypeServerDiskAlert), 1)

	// Silent for 31 seconds: offline, announced once.
	_, err := h.pool.Exec(h.ctx, "UPDATE servers SET last_seen_at = now() - interval '31 seconds' WHERE id = $1", e.serverID)
	require.NoError(t, err)
	n, err := h.hub.SweepOffline(h.ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	off := rec.OfType(notifications.TypeServerOffline)
	require.Len(t, off, 1)
	require.Equal(t, e.serverID, *off[0].ServerID)
	require.Equal(t, notifications.SeverityError, off[0].Severity)
	_, err = h.hub.SweepOffline(h.ctx)
	require.NoError(t, err)
	require.Len(t, rec.OfType(notifications.TypeServerOffline), 1, "an offline server is not announced again")

	// Back again: announced as back online.
	beat(50)
	require.Eventually(t, func() bool { return len(rec.OfType(notifications.TypeServerOnline)) == 2 }, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, "edge-1 is back online", rec.OfType(notifications.TypeServerOnline)[1].Title)
}

func TestServerEventsWithoutNotifierAreHarmless(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeHeartbeat, diskBeat(90))
	require.Eventually(t, func() bool {
		s, err := h.svc.Get(h.ctx, e.serverID)
		return err == nil && s.DiskAlert
	}, 5*time.Second, 20*time.Millisecond)
}
