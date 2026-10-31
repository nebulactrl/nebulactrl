package instsvc

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

func (f *fixture) sliceRows(tm team) int {
	return f.count(`SELECT count(*) FROM team_service_slices WHERE team_id = $1`, tm.ID)
}

func TestMarkDeletedStopsServingAndPurgesAfterThirtyDays(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	key := f.slice(f.a, KindS3).AccessKey
	t0 := f.now
	require.NoError(t, f.svc.MarkDeleted(ctx, f.a.ID))

	rows, err := f.q.ListDeletedTeamSlices(ctx, f.a.ID)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for _, r := range rows {
		require.NotNil(t, r.DeletedAt)
		require.NotNil(t, r.PurgeAt)
		require.True(t, r.PurgeAt.Equal(t0.Add(30*24*time.Hour)), "purge is due 30 days after the deletion")
	}

	// A deleted team's slices are not served; the other team is untouched.
	ts, err := f.svc.ForTeam(ctx, f.a.ID)
	require.NoError(t, err)
	require.Nil(t, ts.S3)
	require.Nil(t, ts.Registry)
	require.Nil(t, ts.Loki)
	_, err = f.svc.AccessKeyOwner(ctx, key)
	require.ErrorIs(t, err, ErrNoSlice, "the deleted team's key no longer opens anything")
	_, err = f.svc.Admit(ctx, f.a.ID, 1)
	require.ErrorIs(t, err, ErrNoSlice)
	tb, err := f.svc.ForTeam(ctx, f.b.ID)
	require.NoError(t, err)
	require.NotNil(t, tb.S3)

	// Marking again keeps the dates.
	f.now = t0.Add(24 * time.Hour)
	require.NoError(t, f.svc.MarkDeleted(ctx, f.a.ID))
	again, err := f.q.ListDeletedTeamSlices(ctx, f.a.ID)
	require.NoError(t, err)
	require.True(t, again[0].PurgeAt.Equal(rows[0].PurgeAt.UTC()) || again[0].PurgeAt.Equal(*rows[0].PurgeAt))

	// Nothing is purged before the day.
	for _, at := range []time.Duration{0, 24 * time.Hour, 29 * 24 * time.Hour, 30*24*time.Hour - time.Second} {
		f.now = t0.Add(at)
		require.NoError(t, f.svc.Purge(ctx))
		require.Equal(t, 3, f.sliceRows(f.a), "at +%s", at)
	}
	require.Empty(t, f.s3.purged)

	f.now = t0.Add(30 * 24 * time.Hour)
	require.NoError(t, f.svc.Purge(ctx))
	require.Equal(t, 0, f.sliceRows(f.a))
	require.Len(t, f.s3.purged, 1)
	require.Equal(t, "nc-"+f.a.ID.String(), f.s3.purged[0].Ref)
	require.Len(t, f.reg.purged, 1)
	require.Equal(t, f.a.ID.String()+"/", f.reg.purged[0].Ref)
	require.Len(t, f.loki.purged, 1)
	require.Equal(t, f.a.ID.String(), f.loki.purged[0].Ref)
	require.Equal(t, 3, f.sliceRows(f.b), "team b keeps everything")
}

func TestPurgeKeepsASliceItCannotPurgeAndRetries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	require.NoError(t, f.svc.MarkDeleted(ctx, f.a.ID))
	f.now = f.now.Add(31 * 24 * time.Hour)

	f.s3.set(func() { f.s3.failPurge = true })
	err := f.svc.Purge(ctx)
	require.ErrorContains(t, err, "purge refused")
	require.Equal(t, 1, f.sliceRows(f.a), "the registry and Loki slices went, the S3 slice stays")
	s3 := f.slice(f.a, KindS3)
	require.Equal(t, StateFailed, s3.State)
	require.Contains(t, s3.Message, "Purge failed")
	require.NotNil(t, s3.DeletedAt)

	f.s3.set(func() { f.s3.failPurge = false })
	require.NoError(t, f.svc.Purge(ctx))
	require.Equal(t, 0, f.sliceRows(f.a))
}

func TestMarkDeletedOfATeamWithoutSlicesIsANoOp(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.MarkDeleted(context.Background(), f.a.ID))
	require.Equal(t, 0, f.sliceRows(f.a))
}

func apiStatus(t *testing.T, err error) (int, string) {
	t.Helper()
	ae, ok := apierr.As(err)
	require.True(t, ok, "want an API error, got %v", err)
	return ae.Status, ae.Code
}

func TestAdminPurgeNowOnlyForDeletedTeams(t *testing.T) {
	f := newFixture(t)
	f.provision()
	admin := f.as(f.a)

	// A team that still exists is never purged.
	_, code := apiStatus(t, f.svc.PurgeTeamSlice(admin, f.b.ID, f.b.Name))
	require.Equal(t, "team_still_exists", code)
	require.Equal(t, 3, f.sliceRows(f.b))

	// A deleted team: its row is gone, its slices wait.
	c := f.newTeam("Gamma", "gamma@example.com")
	require.NoError(t, f.svc.Ensure(context.Background()))
	require.Equal(t, 3, f.sliceRows(c))
	require.NoError(t, f.svc.MarkDeleted(context.Background(), c.ID))
	_, err := f.pool.Exec(context.Background(), `DELETE FROM workspaces WHERE id = $1`, c.ID)
	require.NoError(t, err)

	// Only the instance administrator, and only with the team's name.
	status, code := apiStatus(t, f.svc.PurgeTeamSlice(f.as(f.b), c.ID, c.Name))
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "instance_admin_required", code)
	status, code = apiStatus(t, f.svc.PurgeTeamSlice(admin, c.ID, "wrong"))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, "confirmation_mismatch", code)
	require.Equal(t, 3, f.sliceRows(c))

	// A purge the service refuses leaves the data and says so.
	f.s3.set(func() { f.s3.failPurge = true })
	status, code = apiStatus(t, f.svc.PurgeTeamSlice(admin, c.ID, c.Name))
	require.Equal(t, http.StatusBadGateway, status)
	require.Equal(t, "purge_failed", code)
	require.Equal(t, 1, f.sliceRows(c))
	f.s3.set(func() { f.s3.failPurge = false })

	require.NoError(t, f.svc.PurgeTeamSlice(admin, c.ID, " "+c.Name+" "))
	require.Equal(t, 0, f.sliceRows(c))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM audit_events WHERE action = $1`, ActionSlicePurge))
	status, _ = apiStatus(t, f.svc.PurgeTeamSlice(admin, c.ID, c.Name))
	require.Equal(t, http.StatusNotFound, status, "nothing left to purge")
	require.Equal(t, 3, f.sliceRows(f.a), "other teams are untouched")
}
