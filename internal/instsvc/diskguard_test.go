package instsvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const gib = int64(1) << 30

func TestDiskGuardKeepsTheReserveFree(t *testing.T) {
	now := time.Now()
	g := &diskGuard{usage: func() (uint64, uint64, error) { return uint64(10 * gib), uint64(100 * gib), nil }}

	require.NoError(t, g.check(now, 0), "10 GiB free of 100: above the 5% reserve")
	require.NoError(t, g.check(now, 5*gib), "5 GiB written leaves exactly the reserve")
	require.ErrorIs(t, g.check(now, 6*gib), ErrDiskLow, "a write that would eat into the reserve")

	small := &diskGuard{usage: func() (uint64, uint64, error) { return uint64(3 * gib), uint64(20 * gib), nil }}
	require.NoError(t, small.check(now, 0), "on a small disk the reserve is the 2 GiB floor, not 5%")
	require.ErrorIs(t, small.check(now, 2*gib), ErrDiskLow)

	low := &diskGuard{usage: func() (uint64, uint64, error) { return uint64(4 * gib), uint64(100 * gib), nil }}
	require.ErrorIs(t, low.check(now, 0), ErrDiskLow, "4 GiB free of 100 is under 5%")
}

func TestDiskGuardCachesAndDoesNotBlockOnAReadError(t *testing.T) {
	now := time.Now()
	calls := 0
	free := uint64(50 * gib)
	g := &diskGuard{usage: func() (uint64, uint64, error) { calls++; return free, uint64(100 * gib), nil }}
	require.NoError(t, g.check(now, 0))
	free = 0
	require.NoError(t, g.check(now.Add(time.Second), 0), "the reading is reused inside the cache time")
	require.Equal(t, 1, calls)
	require.ErrorIs(t, g.check(now.Add(diskCacheTTL+time.Second), 0), ErrDiskLow, "and read again after it")

	broken := &diskGuard{usage: func() (uint64, uint64, error) { return 0, 0, errors.New("statfs") }}
	require.NoError(t, broken.check(now, 0), "a disk that cannot be read does not stop every write")
	require.NoError(t, (&diskGuard{}).check(now, 1<<40), "no guard configured, nothing refused")
}

func TestGateRefusesWritesWhenTheDiskIsNearlyFull(t *testing.T) {
	f := newFixture(t)
	f.provision()
	f.setQuota(f.a, 100)
	f.svc.disk.usage = func() (uint64, uint64, error) { return 0, uint64(100 * gib), nil }

	_, err := f.svc.Admit(context.Background(), f.a.ID, 1)
	require.ErrorIs(t, err, ErrDiskLow, "the team has quota left but the disk is full")
	require.EqualValues(t, 0, f.svc.inflightBytes(f.a.ID))
}

func TestRegistryWritesAreRefusedAtTheQuotaAndAfterAPushTheImageCounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	require.Equal(t, 1, f.count(`SELECT count(*) FROM registries WHERE workspace_id = $1 AND kind = 'builtin'`, f.a.ID), "provisioning made the built-in registry")

	require.NoError(t, f.svc.CheckRegistryWrite(ctx, f.a.ID))
	require.NoError(t, f.svc.RecordRegistryPush(ctx, f.a.ID, f.a.ID.String()+"/web", "sha256:aaaa", RegistryQuotaBytes-1))
	require.NoError(t, f.svc.CheckRegistryWrite(ctx, f.a.ID), "one byte under the quota")

	require.NoError(t, f.svc.RecordRegistryPush(ctx, f.a.ID, f.a.ID.String()+"/web", "sha256:bbbb", 1))
	require.ErrorIs(t, f.svc.CheckRegistryWrite(ctx, f.a.ID), ErrQuotaExceeded, "the push is counted at once, not at the next cache refresh")
	require.NoError(t, f.svc.CheckRegistryWrite(ctx, f.b.ID), "another team is unaffected")

	// A hand-pushed row has no service and no build hash: the sweep ages it out.
	require.Equal(t, 2, f.count(`SELECT count(*) FROM registry_images WHERE build_hash = '' AND service_id IS NULL AND state = 'live'`))
	// Pushing the same digest again refreshes it and does not duplicate it.
	require.NoError(t, f.svc.RecordRegistryPush(ctx, f.a.ID, f.a.ID.String()+"/web", "sha256:bbbb", 1))
	require.Equal(t, 2, f.count(`SELECT count(*) FROM registry_images`))
}
