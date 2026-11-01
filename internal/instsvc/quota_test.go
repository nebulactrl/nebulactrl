package instsvc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGateAdmitsUpToTheQuotaAndDeniesAbove(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	f.setQuota(f.a, 100)
	f.setUsed(f.a, 30)

	r1, err := f.svc.Admit(ctx, f.a.ID, 50) // 30 + 50
	require.NoError(t, err)
	r2, err := f.svc.Admit(ctx, f.a.ID, 20) // 30 + 70 = the quota exactly
	require.NoError(t, err)
	require.EqualValues(t, 70, f.svc.inflightBytes(f.a.ID))

	_, err = f.svc.Admit(ctx, f.a.ID, 1)
	require.ErrorIs(t, err, ErrQuotaExceeded, "one byte over the quota is denied")
	require.Equal(t, StateFull, f.slice(f.a, KindS3).State, "a denied write marks the slice full")
	require.EqualValues(t, 70, f.svc.inflightBytes(f.a.ID), "a denied write holds nothing")

	// release frees the in-flight bytes, once.
	r1()
	r1()
	require.EqualValues(t, 20, f.svc.inflightBytes(f.a.ID))
	r3, err := f.svc.Admit(ctx, f.a.ID, 50) // 30 + 20 + 50
	require.NoError(t, err, "the freed bytes can be admitted again, also while the slice is flagged full")
	_, err = f.svc.Admit(ctx, f.a.ID, 1)
	require.ErrorIs(t, err, ErrQuotaExceeded)
	r2()
	r3()
	require.EqualValues(t, 0, f.svc.inflightBytes(f.a.ID))

	r4, err := f.svc.Admit(ctx, f.a.ID, 70)
	require.NoError(t, err)
	_, err = f.svc.Admit(ctx, f.a.ID, 71)
	require.ErrorIs(t, err, ErrQuotaExceeded)
	r4()

	// An empty write (an unknown or zero length) always fits while there is room.
	r5, err := f.svc.Admit(ctx, f.a.ID, 0)
	require.NoError(t, err)
	r5()
	r6, err := f.svc.Admit(ctx, f.a.ID, -5)
	require.NoError(t, err, "a negative length counts as zero")
	r6()
}

func TestGateIsPerTeam(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	f.setQuota(f.a, 100)
	f.setUsed(f.a, 100)

	_, err := f.svc.Admit(ctx, f.a.ID, 1)
	require.ErrorIs(t, err, ErrQuotaExceeded)
	rel, err := f.svc.Admit(ctx, f.b.ID, 10<<30)
	require.NoError(t, err, "team a being full does not touch team b")
	require.Equal(t, StateReady, f.slice(f.b, KindS3).State)
	require.EqualValues(t, 0, f.svc.inflightBytes(f.a.ID))
	require.EqualValues(t, 10<<30, f.svc.inflightBytes(f.b.ID))
	rel()
}

func TestGateUsesTheEffectiveQuota(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()

	// No quota of its own: the 50 GiB default.
	f.setUsed(f.a, DefaultQuotaBytes-10)
	rel, err := f.svc.Admit(ctx, f.a.ID, 10)
	require.NoError(t, err)
	rel()
	_, err = f.svc.Admit(ctx, f.a.ID, 11)
	require.ErrorIs(t, err, ErrQuotaExceeded)

	// The administrator raises the default: the same write fits now.
	_, err = f.svc.UpdateDefaultQuota(f.as(f.a), 100<<30)
	require.NoError(t, err)
	rel, err = f.svc.Admit(ctx, f.a.ID, 11)
	require.NoError(t, err)
	rel()

	// A quota of the team's own wins over the default.
	f.setQuota(f.a, DefaultQuotaBytes)
	_, err = f.svc.Admit(ctx, f.a.ID, 11)
	require.ErrorIs(t, err, ErrQuotaExceeded)
}

func TestGateRefusesWhatItCannotServe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()

	_, err := f.svc.Admit(ctx, uuid.New(), 1)
	require.ErrorIs(t, err, ErrNoSlice, "a team without a slice")

	require.NoError(t, f.q.SetSliceState(ctx, setState(f.slice(f.a, KindS3).ID, StateFailed, "broken")))
	_, err = f.svc.Admit(ctx, f.a.ID, 1)
	require.ErrorIs(t, err, ErrNotReady)
	require.NotErrorIs(t, err, ErrQuotaExceeded, "a broken slice is not a full one: the forwarder answers 503, not 403")

	require.NoError(t, f.svc.MarkDeleted(ctx, f.b.ID))
	_, err = f.svc.Admit(ctx, f.b.ID, 1)
	require.ErrorIs(t, err, ErrNoSlice, "a deleted team's slice is not served")
}

func TestGateNeverAdmitsMoreThanTheQuotaUnderConcurrency(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	f.setQuota(f.a, 1000)

	var admitted atomic.Int64
	var mu sync.Mutex
	var releases []func()
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := f.svc.Admit(ctx, f.a.ID, 100)
			if err != nil {
				return
			}
			admitted.Add(1)
			mu.Lock()
			releases = append(releases, rel)
			mu.Unlock()
		}()
	}
	wg.Wait()
	require.EqualValues(t, 10, admitted.Load(), "1000 bytes of quota admit exactly ten writes of 100")
	require.EqualValues(t, 1000, f.svc.inflightBytes(f.a.ID))
	for _, rel := range releases {
		rel()
	}
	require.EqualValues(t, 0, f.svc.inflightBytes(f.a.ID))
}

func TestReleaseReadsTheTeamsUsageAgain(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()
	f.svc.refreshDelay = 10 * time.Millisecond
	ref := f.slice(f.a, KindS3).Ref
	f.s3.set(func() { f.s3.usage[ref] = 500 })

	rel, err := f.svc.Admit(ctx, f.a.ID, 100)
	require.NoError(t, err)
	rel()
	require.Eventually(t, func() bool { return f.slice(f.a, KindS3).UsedBytes == 500 }, 5*time.Second, 20*time.Millisecond,
		"the bytes an upload added count as used, they do not vanish between the release and the next usage job")
}
