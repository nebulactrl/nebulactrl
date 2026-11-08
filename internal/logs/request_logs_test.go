package logs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestLogsConfigForRequestLogs(t *testing.T) {
	f := newFixture(t)
	cfgFor := func() *agentprotocol.LogsConfig {
		t.Helper()
		cfg, _, err := f.svc.LogsConfigFor(f.ctx, f.srv1.ID)
		require.NoError(t, err)
		return cfg
	}

	// A fresh server: on, query strings dropped, successful assets not stored,
	// retention of the runtime logs.
	cfg := cfgFor()
	require.NotNil(t, cfg.Requests)
	require.False(t, cfg.Requests.KeepQuery)
	require.False(t, cfg.Requests.KeepAssets)
	require.Equal(t, agentprotocol.DefaultLogRetentionDays, cfg.Requests.Retention.Days)
	require.Equal(t, agentprotocol.DefaultRequestLogMaxServiceBytes, cfg.Requests.Retention.MaxServiceBytes)
	require.Equal(t, agentprotocol.DefaultRequestLogMaxServerBytes, cfg.Requests.Retention.MaxServerBytes)

	on, off := true, false
	in := SettingsInput{Capture: true, RetentionDays: 3, MaxServiceBytes: 64 << 20, MaxServerBytes: 1 << 30, RequestLogsKeepQuery: &on, RequestLogsKeepAssets: &on}
	_, err := f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
	require.NoError(t, err)
	cfg = cfgFor()
	require.NotNil(t, cfg.Requests)
	require.True(t, cfg.Requests.KeepQuery)
	require.True(t, cfg.Requests.KeepAssets)
	require.Equal(t, 3, cfg.Requests.Retention.Days)
	require.Equal(t, agentprotocol.DefaultRequestLogMaxServiceBytes, cfg.Requests.Retention.MaxServiceBytes, "the runtime caps do not apply")

	// Each of the three conditions switches it off.
	in.RequestLogs = &off
	_, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
	require.NoError(t, err)
	require.Nil(t, cfgFor().Requests, "request logs off")

	in.RequestLogs = &on
	in.Capture = false
	_, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
	require.NoError(t, err)
	require.Nil(t, cfgFor().Requests, "log capture off")

	in.Capture = true
	_, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
	require.NoError(t, err)
	require.NotNil(t, cfgFor().Requests)
	f.exec(`UPDATE servers SET traffic_analytics_enabled = false WHERE id = $1`, f.srv1.ID)
	require.Nil(t, cfgFor().Requests, "traffic analytics off")
}

func TestRequestLogSettingsRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.agents.online[f.srv1.ID] = true

	v, err := f.svc.GetSettings(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.True(t, v.RequestLogs)
	require.False(t, v.RequestLogsKeepQuery)
	require.False(t, v.RequestLogsKeepAssets)
	require.Zero(t, v.RequestsTotalBytes)

	// Omitted fields keep what is stored.
	on, off := true, false
	in := SettingsInput{Capture: true, RetentionDays: 7, MaxServiceBytes: 64 << 20, MaxServerBytes: 1 << 30, RequestLogs: &off, RequestLogsKeepQuery: &on, RequestLogsKeepAssets: &on}
	v, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
	require.NoError(t, err)
	require.False(t, v.RequestLogs)
	require.True(t, v.RequestLogsKeepQuery)
	require.True(t, v.RequestLogsKeepAssets)
	in.RequestLogs, in.RequestLogsKeepQuery, in.RequestLogsKeepAssets = nil, nil, nil
	in.RetentionDays = 9
	v, err = f.svc.UpdateSettings(f.ctx, f.srv1.ID, in)
	require.NoError(t, err)
	require.False(t, v.RequestLogs)
	require.True(t, v.RequestLogsKeepQuery)
	require.True(t, v.RequestLogsKeepAssets)
	require.Equal(t, 9, v.RetentionDays)
	require.Len(t, f.agents.resynced, 2, "every change resyncs the agent")

	// The audit trail carries the effective values.
	require.Equal(t, 2, f.count(`SELECT count(*) FROM audit_events WHERE action = $1 AND metadata->>'requestLogsKeepQuery' = 'true' AND metadata->>'requestLogsKeepAssets' = 'true' AND metadata->>'requestLogs' = 'false'`, ActionSettingsUpdate))

	// The agent's request-store usage shows next to the settings.
	require.NoError(t, f.svc.HandleLogStatus(context.Background(), f.srv1.ID, agentprotocol.LogStatus{
		Capturing: true, TotalBytes: 1234, RequestsCapturing: true, RequestsTotalBytes: 5678,
	}))
	v, err = f.svc.GetSettings(f.ctx, f.srv1.ID)
	require.NoError(t, err)
	require.Equal(t, int64(5678), v.RequestsTotalBytes)
	require.NotNil(t, v.Usage)
	require.Equal(t, int64(1234), v.Usage.TotalBytes)
}
