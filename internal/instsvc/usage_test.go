package instsvc

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/notifications"
)

func (f *fixture) setS3Usage(tm team, used int64) {
	f.t.Helper()
	ref := f.slice(tm, KindS3).Ref
	f.s3.set(func() { f.s3.usage[ref] = used })
}

func (f *fixture) eventsOf(typ string) []notifications.Event {
	var out []notifications.Event
	for _, e := range f.notes.all() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestUsageJobAlertsOncePerCrossingAndTracksTheState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	// The control-plane host belongs to team b: the instance administrator's
	// copy of every alert goes there.
	_, err := f.q.CreateControlPlaneServer(ctx, db.CreateControlPlaneServerParams{WorkspaceID: f.b.ID, Name: "cp"})
	require.NoError(t, err)
	f.setQuota(f.a, 1000)

	step := func(used int64) db.TeamServiceSlice {
		f.setS3Usage(f.a, used)
		require.NoError(t, f.svc.Usage(ctx))
		return f.slice(f.a, KindS3)
	}

	r := step(790)
	require.EqualValues(t, 790, r.UsedBytes)
	require.Equal(t, StateReady, r.State)
	require.Empty(t, f.notes.all(), "79% is silent")

	r = step(800)
	require.EqualValues(t, 80, r.AlertLevel)
	warn := f.eventsOf(notifications.TypeStorageQuotaWarning)
	require.Len(t, warn, 2, "one for the team, one copy for the control-plane team")
	require.Equal(t, f.a.ID, warn[0].WorkspaceID)
	require.Equal(t, f.b.ID, warn[1].WorkspaceID)
	require.Equal(t, notifications.SeverityWarning, warn[0].Severity)
	require.Contains(t, warn[1].Title, "Alpha", "the instance administrator's copy names the team")

	step(900)
	require.Len(t, f.eventsOf(notifications.TypeStorageQuotaWarning), 2, "once per crossing, not per run")

	r = step(1000)
	require.Equal(t, StateFull, r.State)
	require.EqualValues(t, 100, r.AlertLevel)
	full := f.eventsOf(notifications.TypeStorageQuotaFull)
	require.Len(t, full, 2)
	require.Equal(t, notifications.SeverityError, full[0].Severity)
	require.Contains(t, full[0].Body, "Existing backups are kept")

	step(1100)
	require.Len(t, f.eventsOf(notifications.TypeStorageQuotaFull), 2)

	// Space is freed: ready again, and the level only resets below 75%.
	r = step(780)
	require.Equal(t, StateReady, r.State)
	require.EqualValues(t, 80, r.AlertLevel, "back under the quota but still above 75%: no new alert")
	r = step(700)
	require.EqualValues(t, 0, r.AlertLevel)
	require.Len(t, f.notes.all(), 4)

	r = step(850)
	require.EqualValues(t, 80, r.AlertLevel)
	require.Len(t, f.eventsOf(notifications.TypeStorageQuotaWarning), 4, "a second crossing alerts again")

	// Nothing an alert carries is a credential.
	key := f.slice(f.a, KindS3).AccessKey
	ref := f.slice(f.a, KindS3).Ref
	for _, e := range f.notes.all() {
		for _, text := range append([]string{e.Title, e.Body}, mapValues(e.Fields)...) {
			require.NotContains(t, text, key)
			require.NotContains(t, text, ref)
		}
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestUsageAlertGoesToTheTeamOnlyWhenItOwnsTheControlPlane(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	_, err := f.q.CreateControlPlaneServer(ctx, db.CreateControlPlaneServerParams{WorkspaceID: f.a.ID, Name: "cp"})
	require.NoError(t, err)
	f.setQuota(f.a, 1000)
	f.setS3Usage(f.a, 850)
	require.NoError(t, f.svc.Usage(ctx))
	require.Len(t, f.notes.all(), 1, "no second copy to the same team")
}

func TestUsageOfOneTeamDoesNotMoveAnother(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	f.setS3Usage(f.a, 123)
	f.setS3Usage(f.b, 456)
	require.NoError(t, f.svc.Usage(ctx))
	require.EqualValues(t, 123, f.slice(f.a, KindS3).UsedBytes)
	require.EqualValues(t, 456, f.slice(f.b, KindS3).UsedBytes)
	require.EqualValues(t, 0, f.slice(f.a, KindRegistry).UsedBytes)
}

func TestUsageSkipsSlicesThatAreNotReady(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	require.NoError(t, f.q.SetSliceState(ctx, setState(f.slice(f.a, KindS3).ID, StateFailed, "x")))
	f.setS3Usage(f.a, 999)
	require.NoError(t, f.svc.Usage(ctx))
	require.EqualValues(t, 0, f.slice(f.a, KindS3).UsedBytes)
	require.Equal(t, StateFailed, f.slice(f.a, KindS3).State)
}

func TestAlertLevelFor(t *testing.T) {
	for _, c := range []struct {
		used, quota int64
		cur, want   int
	}{
		{0, 100, 0, 0}, {79, 100, 0, 0}, {80, 100, 0, 80}, {99, 100, 0, 80}, {100, 100, 0, 100}, {150, 100, 80, 100},
		{90, 100, 100, 80}, {76, 100, 80, 80}, {76, 100, 100, 80}, {76, 100, 0, 0}, {74, 100, 80, 0}, {5, 0, 80, 80},
	} {
		require.Equal(t, c.want, alertLevelFor(c.used, c.quota, c.cur), "used=%d quota=%d cur=%d", c.used, c.quota, c.cur)
	}
}

func TestFormatBytes(t *testing.T) {
	require.Equal(t, "0 B", formatBytes(0))
	require.Equal(t, "1.0 KiB", formatBytes(1024))
	require.Equal(t, "42.5 GiB", formatBytes(int64(42.5*float64(1<<30))))
	require.True(t, strings.HasSuffix(formatBytes(50<<30), "GiB"))
}
