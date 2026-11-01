package agents_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/servers"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func (h *harness) diskAlertEvents(serverID uuid.UUID) int {
	h.t.Helper()
	var n int
	require.NoError(h.t, h.pool.QueryRow(h.ctx,
		"SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2", agents.DiskAlertAction, serverID.String()).Scan(&n))
	return n
}

func diskBeat(usedPercent uint64) agentprotocol.Heartbeat {
	return agentprotocol.Heartbeat{
		MemUsed: 1, MemTotal: 2, DiskUsed: usedPercent * 10, DiskTotal: 1000, Proxy: agentprotocol.ProxyHealth{Status: agentprotocol.ProxyOK},
	}
}

func TestDiskAlertCrossingEmitsOncePerCrossing(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)

	alertOf := func() bool {
		s, err := h.svc.Get(h.ctx, e.serverID)
		require.NoError(t, err)
		return s.DiskAlert
	}
	beat := func(pct uint64) { a.write(agentprotocol.TypeHeartbeat, diskBeat(pct)) }

	beat(70) // below the default threshold of 80
	require.Eventually(t, func() bool { s, err := h.svc.Get(h.ctx, e.serverID); return err == nil && s.Latest != nil }, 5*time.Second, 20*time.Millisecond)
	require.False(t, alertOf())
	require.Zero(t, h.diskAlertEvents(e.serverID))

	beat(85) // crossing
	require.Eventually(t, alertOf, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 1, h.diskAlertEvents(e.serverID))

	// Still above the threshold: no second event.
	beat(90)
	beat(86)
	time.Sleep(300 * time.Millisecond)
	require.True(t, alertOf())
	require.Equal(t, 1, h.diskAlertEvents(e.serverID), "one audit event per crossing")

	// Hovering just below the threshold does not clear (hysteresis), then a real drop does.
	beat(79)
	time.Sleep(300 * time.Millisecond)
	require.True(t, alertOf())
	beat(60)
	require.Eventually(t, func() bool { return !alertOf() }, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 1, h.diskAlertEvents(e.serverID), "clearing is not an alert")

	// A second crossing is a new event.
	beat(95)
	require.Eventually(t, alertOf, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 2, h.diskAlertEvents(e.serverID))

	var actor, label string
	require.NoError(t, h.pool.QueryRow(h.ctx, "SELECT actor_type, actor_label FROM audit_events WHERE action = $1 LIMIT 1", agents.DiskAlertAction).Scan(&actor, &label))
	require.Equal(t, "system", actor)
}

func TestDiskAlertFollowsThresholdSetting(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	_, err := h.pool.Exec(h.ctx, "UPDATE servers SET disk_alert_threshold_percent = 95 WHERE id = $1", e.serverID)
	require.NoError(t, err)
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeHeartbeat, diskBeat(90))
	require.Eventually(t, func() bool { s, err := h.svc.Get(h.ctx, e.serverID); return err == nil && s.Latest != nil }, 5*time.Second, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	s, err := h.svc.Get(h.ctx, e.serverID)
	require.NoError(t, err)
	require.False(t, s.DiskAlert, "90%% is below a threshold of 95")
	require.Equal(t, 95, s.DiskAlertThresholdPercent)
}

type fakeSink struct {
	mu       sync.Mutex
	attempts int // every IngestTraffic call, failed or not
	got      []agentprotocol.Traffic
	srv      []uuid.UUID
	fail     bool
}

func (f *fakeSink) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeSink) IngestTraffic(_ context.Context, serverID uuid.UUID, t agentprotocol.Traffic) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.fail {
		return errors.New("database unavailable")
	}
	f.got = append(f.got, t)
	f.srv = append(f.srv, serverID)
	return nil
}

func TestTrafficFramesAreSunkWithTheAuthenticatedServerAndAcked(t *testing.T) {
	h := newHarness(t)
	sink := &fakeSink{}
	h.hub.SetTrafficSink(sink)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)

	a.write(agentprotocol.TypeTraffic, agentprotocol.Traffic{Seq: 7, Buckets: []agentprotocol.TrafficBucket{{Host: "a.example.com", Bucket: time.Now().Truncate(time.Minute), Requests: 3}}})
	var ack agentprotocol.TrafficAck
	require.NoError(t, a.next(agentprotocol.TypeTrafficAck).Into(&ack))
	require.EqualValues(t, 7, ack.Seq)
	sink.mu.Lock()
	require.Len(t, sink.got, 1)
	require.Equal(t, e.serverID, sink.srv[0], "the sink gets the server of the authenticated connection, never one named in the frame")
	sink.mu.Unlock()

	// A failing sink withholds the ack so the agent resends.
	sink.mu.Lock()
	sink.fail = true
	sink.mu.Unlock()
	a.write(agentprotocol.TypeTraffic, agentprotocol.Traffic{Seq: 8, Buckets: []agentprotocol.TrafficBucket{{Host: "a.example.com", Requests: 1}}})
	// The hub handles frames asynchronously: wait until it has actually
	// attempted (and failed) seq 8 before the sink recovers.
	require.Eventually(t, func() bool { return sink.attemptCount() == 2 }, 5*time.Second, 5*time.Millisecond)
	sink.mu.Lock()
	sink.fail = false
	sink.mu.Unlock()
	a.write(agentprotocol.TypeTraffic, agentprotocol.Traffic{Seq: 9, Buckets: []agentprotocol.TrafficBucket{{Host: "a.example.com", Requests: 1}}})
	require.NoError(t, a.next(agentprotocol.TypeTrafficAck).Into(&ack))
	require.EqualValues(t, 9, ack.Seq, "seq 8 was not acknowledged")
}

func TestTrafficFramesWithoutASinkAreAcknowledged(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile)
	a.write(agentprotocol.TypeTraffic, agentprotocol.Traffic{Seq: 1})
	var ack agentprotocol.TrafficAck
	require.NoError(t, a.next(agentprotocol.TypeTrafficAck).Into(&ack))
	require.EqualValues(t, 1, ack.Seq)
}

func intp(v int) *int    { return &v }
func boolp(v bool) *bool { return &v }

func TestServerSettingsRoundTripAndValidation(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")

	s, err := h.svc.Get(h.ctx, e.serverID)
	require.NoError(t, err)
	require.Equal(t, 7, s.MetricsRetentionDays)
	require.Equal(t, 80, s.DiskAlertThresholdPercent)
	require.True(t, s.TrafficAnalyticsEnabled)
	require.Equal(t, 7, s.TrafficHourlyRetentionDays)
	require.Equal(t, 90, s.TrafficDailyRetentionDays)
	require.False(t, s.DiskAlert)

	s, err = h.svc.Update(h.ctx, e.serverID, servers.UpdateInput{
		MetricsRetentionDays: intp(30), DiskAlertThresholdPercent: intp(90), TrafficAnalyticsEnabled: boolp(false),
		TrafficHourlyRetentionDays: intp(14), TrafficDailyRetentionDays: intp(180),
	})
	require.NoError(t, err)
	require.Equal(t, 30, s.MetricsRetentionDays)
	require.Equal(t, 90, s.DiskAlertThresholdPercent)
	require.False(t, s.TrafficAnalyticsEnabled)
	require.Equal(t, 14, s.TrafficHourlyRetentionDays)
	require.Equal(t, 180, s.TrafficDailyRetentionDays)

	// Unset fields keep their value.
	s, err = h.svc.Update(h.ctx, e.serverID, servers.UpdateInput{MetricsRetentionDays: intp(2)})
	require.NoError(t, err)
	require.Equal(t, 2, s.MetricsRetentionDays)
	require.Equal(t, 90, s.DiskAlertThresholdPercent)

	for name, in := range map[string]servers.UpdateInput{
		"retention 0":        {MetricsRetentionDays: intp(0)},
		"retention 91":       {MetricsRetentionDays: intp(91)},
		"threshold 49":       {DiskAlertThresholdPercent: intp(49)},
		"threshold 100":      {DiskAlertThresholdPercent: intp(100)},
		"hourly retention 1": {TrafficHourlyRetentionDays: intp(1)},
		"daily retention 6":  {TrafficDailyRetentionDays: intp(6)},
	} {
		_, err := h.svc.Update(h.ctx, e.serverID, in)
		var ae *apierr.Error
		require.True(t, errors.As(err, &ae), name)
		require.Equal(t, 422, ae.Status, name)
	}
}

func TestTogglingTrafficAnalyticsResyncsTheAgent(t *testing.T) {
	h := newHarness(t)
	e := h.enroll("edge-1")
	a := h.dial(e, false)
	a.next(agentprotocol.TypeReconcile) // hello

	_, err := h.svc.Update(h.ctx, e.serverID, servers.UpdateInput{TrafficAnalyticsEnabled: boolp(false)})
	require.NoError(t, err)
	a.next(agentprotocol.TypeReconcile) // resync carrying the new setting
}
