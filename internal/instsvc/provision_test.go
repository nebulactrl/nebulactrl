package instsvc

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// snapshot is what must not change when provisioning runs again.
type snapshot struct {
	SliceIDs  map[string]string // team/kind -> id
	Keys      map[string]string // team/kind -> access key
	Secrets   map[string]string // team/kind -> sealed secret (hex)
	SystemKey [2]string
}

func (f *fixture) snapshot(teams ...team) snapshot {
	f.t.Helper()
	ctx := context.Background()
	s := snapshot{SliceIDs: map[string]string{}, Keys: map[string]string{}, Secrets: map[string]string{}}
	for _, tm := range teams {
		for _, kind := range kinds {
			r := f.slice(tm, kind)
			k := tm.Name + "/" + kind
			s.SliceIDs[k], s.Keys[k], s.Secrets[k] = r.ID.String(), r.AccessKey, string(r.SecretEnc)
		}
	}
	ak, sec, _, err := f.svc.SystemAccessKey(ctx)
	require.NoError(f.t, err)
	s.SystemKey = [2]string{ak, sec}
	return s
}

func (f *fixture) wiringCounts() (slices, destinations, registries, drains int) {
	return f.count(`SELECT count(*) FROM team_service_slices`),
		f.count(`SELECT count(*) FROM backup_destinations WHERE kind = 'builtin'`),
		f.count(`SELECT count(*) FROM registries WHERE kind = 'builtin'`),
		f.count(`SELECT count(*) FROM log_drains WHERE builtin`)
}

func TestEnsureCreatesTheServicesAndEveryTeamsSlice(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.Ensure(ctx))

	rows, err := f.q.ListInstanceServices(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for _, r := range rows {
		require.Equal(t, StateReady, r.Status, r.Kind)
		require.Empty(t, r.Message)
	}

	// The system bucket belongs to no team and has its own key.
	ak, secret, bucket, err := f.svc.SystemAccessKey(ctx)
	require.NoError(t, err)
	require.Equal(t, SystemBucket, bucket)
	require.Len(t, ak, 20)
	sys := f.s3.calls(SystemBucket)
	require.Len(t, sys, 1)
	require.Equal(t, uuidNil, sys[0].Slice.TeamID)
	require.Equal(t, ak, sys[0].Slice.AccessKey)
	require.EqualValues(t, 0, sys[0].Quota, "no native quota on the system bucket")
	require.Equal(t, secret, f.s3.secrets[SystemBucket], "the backend can resolve the system secret while it creates the key")

	for _, tm := range []team{f.a, f.b} {
		s3 := f.slice(tm, KindS3)
		require.Equal(t, "nc-"+tm.ID.String(), s3.Ref)
		require.Len(t, s3.AccessKey, 20)
		require.Equal(t, StateReady, s3.State)
		require.NotEmpty(t, s3.SecretEnc)
		require.NotContains(t, string(s3.SecretEnc), f.s3.secrets[s3.Ref], "the secret is sealed")
		calls := f.s3.calls(s3.Ref)
		require.Len(t, calls, 1)
		require.Equal(t, DefaultQuotaBytes, calls[0].Quota, "the default quota is applied natively")
		require.Equal(t, s3.AccessKey, calls[0].Slice.AccessKey)
		require.Len(t, f.s3.secrets[s3.Ref], 40)

		reg := f.slice(tm, KindRegistry)
		require.Equal(t, tm.ID.String()+"/", reg.Ref)
		require.Equal(t, StateReady, reg.State)
		require.Empty(t, reg.AccessKey)
		loki := f.slice(tm, KindLoki)
		require.Equal(t, tm.ID.String(), loki.Ref, "the Loki tenant is the team")
		require.Equal(t, StateReady, loki.State)

		dest, err := f.q.GetBuiltinDestination(ctx, tm.ID)
		require.NoError(t, err)
		require.Equal(t, "builtin", dest.Kind)
		require.Empty(t, dest.AccessKeyEnc, "the built-in destination holds no credentials")
		require.Empty(t, dest.SecretKeyEnc)
		require.Empty(t, dest.Bucket)
		require.Empty(t, dest.Endpoint)
		br, err := f.q.GetBuiltinRegistry(ctx, tm.ID)
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1:18500", br.Host)
		require.Equal(t, tm.ID.String(), br.Namespace)
		require.Empty(t, br.PushAuthEnc)
		drain, err := f.q.GetBuiltinDrain(ctx, tm.ID)
		require.NoError(t, err)
		require.False(t, drain.Enabled, "central logs are off until the team turns them on")
		require.Equal(t, "loki", drain.Kind)
		require.Empty(t, drain.SecretsEnc)
	}

	// The two teams hold different keys and secrets: nothing is shared.
	require.NotEqual(t, f.slice(f.a, KindS3).AccessKey, f.slice(f.b, KindS3).AccessKey)
	require.NotEqual(t, f.s3.secrets["nc-"+f.a.ID.String()], f.s3.secrets["nc-"+f.b.ID.String()])
}

var uuidNil = uuid.Nil

func TestEnsureIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.Ensure(ctx))
	before := f.snapshot(f.a, f.b)
	s, d, r, l := f.wiringCounts()
	require.Equal(t, [4]int{6, 2, 2, 2}, [4]int{s, d, r, l})

	require.NoError(t, f.svc.Ensure(ctx))
	require.NoError(t, f.svc.Ensure(ctx))
	require.Equal(t, before, f.snapshot(f.a, f.b), "keys, secrets and ids survive a re-run")
	s, d, r, l = f.wiringCounts()
	require.Equal(t, [4]int{6, 2, 2, 2}, [4]int{s, d, r, l}, "no duplicate slice or row")

	// A team created later gets its slice on the next run, the others stay as they are.
	c := f.newTeam("Gamma", "gamma@example.com")
	require.NoError(t, f.svc.Ensure(ctx))
	require.Equal(t, StateReady, f.slice(c, KindS3).State)
	require.Equal(t, before.Keys["Alpha/s3"], f.slice(f.a, KindS3).AccessKey)
	s, d, r, l = f.wiringCounts()
	require.Equal(t, [4]int{9, 3, 3, 3}, [4]int{s, d, r, l})
}

func TestEnsureTeamIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.Ensure(ctx))
	before := f.snapshot(f.a, f.b)

	for range 3 {
		require.NoError(t, f.svc.EnsureTeam(ctx, f.a.ID))
	}
	require.Equal(t, before, f.snapshot(f.a, f.b))
	s, d, r, l := f.wiringCounts()
	require.Equal(t, [4]int{6, 2, 2, 2}, [4]int{s, d, r, l})

	// Racing runs for a brand-new team still make one slice per kind.
	c := f.newTeam("Gamma", "gamma@example.com")
	errs := make(chan error, 6)
	for range 6 {
		go func() { errs <- f.svc.EnsureTeam(ctx, c.ID) }()
	}
	for range 6 {
		require.NoError(t, <-errs)
	}
	require.Equal(t, 3, f.count(`SELECT count(*) FROM team_service_slices WHERE team_id = $1`, c.ID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM backup_destinations WHERE workspace_id = $1 AND kind = 'builtin'`, c.ID))
}

func TestEnsureTeamSkipsATeamThatIsBeingDeleted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `UPDATE workspaces SET deletion_state = 'deleting' WHERE id = $1`, f.b.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Ensure(ctx))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM team_service_slices WHERE team_id = $1`, f.b.ID))
	require.NoError(t, f.svc.EnsureTeam(ctx, f.b.ID))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM backup_destinations WHERE workspace_id = $1 AND kind = 'builtin'`, f.b.ID))
	require.Equal(t, 3, f.count(`SELECT count(*) FROM team_service_slices WHERE team_id = $1`, f.a.ID))
}

func TestEnsureRecordsAFailedSliceAndRetriesIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.s3.set(func() { f.s3.failEnsure = true })
	err := f.svc.EnsureTeam(ctx, f.a.ID)
	require.ErrorContains(t, err, "the service refused")

	s3 := f.slice(f.a, KindS3)
	require.Equal(t, StateFailed, s3.State)
	require.Contains(t, s3.Message, "the service refused")
	require.Equal(t, StateReady, f.slice(f.a, KindRegistry).State, "one kind failing does not stop the others")
	_, derr := f.q.GetBuiltinDestination(ctx, f.a.ID)
	require.Error(t, derr, "no destination points at a slice that does not work")
	_, rerr := f.q.GetBuiltinRegistry(ctx, f.a.ID)
	require.NoError(t, rerr)

	// The service recovers: the periodic run finds the team again.
	f.s3.set(func() { f.s3.failEnsure = false })
	require.NoError(t, f.svc.Ensure(ctx))
	s3 = f.slice(f.a, KindS3)
	require.Equal(t, StateReady, s3.State)
	require.Empty(t, s3.Message)
	_, derr = f.q.GetBuiltinDestination(ctx, f.a.ID)
	require.NoError(t, derr)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM team_service_slices WHERE team_id = $1 AND kind = 's3'`, f.a.ID))
}

func TestEnsureShowsAnUnhealthyServiceAndRecovers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.loki.set(func() { f.loki.health = errors.New("loki is not ready") })
	err := f.svc.Ensure(ctx)
	require.ErrorContains(t, err, "loki is not ready")

	loki, err := f.q.GetInstanceService(ctx, KindLoki)
	require.NoError(t, err)
	require.Equal(t, StateFailed, loki.Status)
	require.Equal(t, "loki is not ready", loki.Message)
	s3, err := f.q.GetInstanceService(ctx, KindS3)
	require.NoError(t, err)
	require.Equal(t, StateReady, s3.Status)

	f.loki.set(func() { f.loki.health = nil })
	require.NoError(t, f.svc.Ensure(ctx))
	loki, err = f.q.GetInstanceService(ctx, KindLoki)
	require.NoError(t, err)
	require.Equal(t, StateReady, loki.Status)
	require.Empty(t, loki.Message)
}

func TestEnsureWithoutBackendsDoesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	empty := New(Deps{Pool: f.pool, Box: f.box})
	require.False(t, empty.Enabled())
	require.NoError(t, empty.Ensure(ctx))
	require.NoError(t, empty.EnsureTeam(ctx, f.a.ID))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM team_service_slices`))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM instance_services`))
}

func TestEnsureKeepsAnExistingNameAndUsesAnother(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `INSERT INTO backup_destinations (workspace_id, name, kind) VALUES ($1, 'Built-in backup storage', 'local')`, f.a.ID)
	require.NoError(t, err)
	require.NoError(t, f.svc.Ensure(ctx))
	dest, err := f.q.GetBuiltinDestination(ctx, f.a.ID)
	require.NoError(t, err)
	require.Equal(t, "Built-in backup storage (NebulaCtrl)", dest.Name)
	require.Equal(t, 2, f.count(`SELECT count(*) FROM backup_destinations WHERE workspace_id = $1`, f.a.ID), "the team's own destination is untouched")
}

func TestSlicesResolveOnlyTheirOwnTeam(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.provision()

	// The agent's identity decides the team.
	got, err := f.svc.TeamOfServer(ctx, f.a.Server)
	require.NoError(t, err)
	require.Equal(t, f.a.ID, got)
	got, err = f.svc.TeamOfServer(ctx, f.b.Server)
	require.NoError(t, err)
	require.Equal(t, f.b.ID, got)
	_, err = f.svc.TeamOfServer(ctx, uuidFrom("00000000-0000-0000-0000-0000000000aa"))
	require.ErrorIs(t, err, ErrNoSlice)

	ta, err := f.svc.ForTeam(ctx, f.a.ID)
	require.NoError(t, err)
	require.NotNil(t, ta.S3)
	require.NotNil(t, ta.Registry)
	require.NotNil(t, ta.Loki)
	require.Equal(t, "nc-"+f.a.ID.String(), ta.S3.Ref)
	require.Equal(t, DefaultQuotaBytes, ta.S3.QuotaBytes)
	tb, err := f.svc.ForTeam(ctx, f.b.ID)
	require.NoError(t, err)
	require.NotEqual(t, ta.S3.Ref, tb.S3.Ref)
	require.NotEqual(t, ta.S3.AccessKey, tb.S3.AccessKey)

	// A key belongs to exactly one team, and its secret opens only through its own slice.
	owner, err := f.svc.AccessKeyOwner(ctx, ta.S3.AccessKey)
	require.NoError(t, err)
	require.Equal(t, f.a.ID, owner)
	owner, err = f.svc.AccessKeyOwner(ctx, tb.S3.AccessKey)
	require.NoError(t, err)
	require.Equal(t, f.b.ID, owner)
	_, err = f.svc.AccessKeyOwner(ctx, "NOSUCHKEY")
	require.ErrorIs(t, err, ErrNoSlice)
	_, err = f.svc.AccessKeyOwner(ctx, "")
	require.ErrorIs(t, err, ErrNoSlice)
	sa, err := f.svc.Secret(ctx, *ta.S3)
	require.NoError(t, err)
	sb, err := f.svc.Secret(ctx, *tb.S3)
	require.NoError(t, err)
	require.NotEqual(t, sa, sb)
	require.Equal(t, f.s3.secrets[ta.S3.Ref], sa)
	_, err = f.svc.Secret(ctx, *ta.Registry)
	require.ErrorIs(t, err, ErrNoSlice, "only an s3 slice has a secret")

	// The system key is no team's key.
	ak, _, _, err := f.svc.SystemAccessKey(ctx)
	require.NoError(t, err)
	_, err = f.svc.AccessKeyOwner(ctx, ak)
	require.ErrorIs(t, err, ErrNoSlice)
}

func TestTeamCreationQueuesItsSliceInTheSameTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	queued := func() int {
		return f.count(`SELECT count(*) FROM river_job WHERE kind = 'instsvc_ensure_team' AND args->>'teamId' = $1`, f.a.ID.String())
	}

	// A rolled-back creation leaves no job behind.
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) }) // an early failure must not leave the connection checked out
	require.NoError(t, f.svc.EnqueueEnsureTx(ctx, tx, f.a.ID))
	require.NoError(t, tx.Rollback(ctx))
	require.Equal(t, 0, queued())

	tx, err = f.pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, f.svc.EnqueueEnsureTx(ctx, tx, f.a.ID))
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, 1, queued())

	// Without instance services nothing is queued.
	off := New(Deps{Pool: f.pool, Box: f.box, Jobs: f.jm})
	tx, err = f.pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, off.EnqueueEnsureTx(ctx, tx, f.b.ID))
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, 0, f.count(`SELECT count(*) FROM river_job WHERE kind = 'instsvc_ensure_team' AND args->>'teamId' = $1`, f.b.ID.String()))
}
