package backups

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	builtinAccess = "AKIABUILTINSLICE01"
	builtinSecret = "sekret-builtin-slice-key-9876"
	builtinBucket = "nebulactrl-team-bucket"
	gib           = int64(1) << 30
)

// fakeBuiltin is the instance services' view of one team's slice.
type fakeBuiltin struct {
	slice instsvc.Slice
	err   error

	sysAccess, sysSecret, sysBucket string
	sysErr                          error
}

func newFakeBuiltin() *fakeBuiltin {
	return &fakeBuiltin{
		slice: instsvc.Slice{
			ID: uuid.New(), Kind: instsvc.KindS3, Ref: builtinBucket, AccessKey: builtinAccess,
			QuotaBytes: 50 * gib, UsedBytes: 10 * gib, State: instsvc.StateReady,
		},
		sysAccess: "AKIASYSTEMKEY0001", sysSecret: "sekret-system-key-4321", sysBucket: "nebula-backups",
	}
}

func (b *fakeBuiltin) SliceForBackup(_ context.Context, teamID uuid.UUID) (instsvc.Slice, string, error) {
	sl := b.slice
	sl.TeamID = teamID
	if b.err != nil {
		return sl, "", b.err
	}
	return sl, builtinSecret, nil
}

func (b *fakeBuiltin) SystemAccessKey(context.Context) (string, string, string, error) {
	return b.sysAccess, b.sysSecret, b.sysBucket, b.sysErr
}

// builtinDestination inserts the team's builtin destination the way
// instsvc.EnsureTeam does.
func (f *fixture) builtinDestination() db.BackupDestination {
	f.t.Helper()
	d, err := f.q.InsertBuiltinDestination(context.Background(), db.InsertBuiltinDestinationParams{WorkspaceID: f.ws.ID, Name: "Built-in backup storage"})
	require.NoError(f.t, err)
	return d
}

func TestBuiltinDestinationResolvedFromTheSlice(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	d := f.builtinDestination()
	fake := newFakeBuiltin()
	f.svc.SetBuiltinStorage(fake)

	// The view says what it is and leaks neither endpoint, bucket nor keys.
	v, err := f.svc.GetDestination(f.ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, DestBuiltin, v.Kind)
	require.False(t, v.OffSite)
	require.Empty(t, v.Endpoint)
	require.Empty(t, v.Bucket)
	require.False(t, v.AccessKeySet)
	require.False(t, v.SecretKeySet)
	raw, _ := json.Marshal(v)
	for _, leak := range []string{builtinBucket, builtinAccess, builtinSecret, "127.0.0.1"} {
		require.NotContains(t, string(raw), leak)
	}
	list, err := f.svc.ListDestinations(f.ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, DestBuiltin, list[0].Kind)

	// A backup names the team's bucket behind the agent's local endpoint; the
	// payload carries no key, the send-time secrets do.
	b, err := f.svc.CreateBackup(f.ctx, sv.ID, d.ID)
	require.NoError(t, err)
	require.Equal(t, DestBuiltin, b.DestinationKind)
	cmd, _ := f.disp.last()
	require.Equal(t, agentprotocol.KindDBBackup, cmd.Kind)
	var p agentprotocol.DBBackupPayload
	require.NoError(t, json.Unmarshal(cmd.Payload, &p))
	require.Equal(t, agentprotocol.DestS3, p.Destination.Kind, "agents only know s3")
	require.Equal(t, "http://127.0.0.1:18333", p.Destination.Endpoint)
	require.Equal(t, builtinBucket, p.Destination.Bucket)
	require.Equal(t, "us-east-1", p.Destination.Region)
	require.True(t, p.Destination.PathStyle)
	require.True(t, p.Destination.CompatMode)
	require.True(t, strings.HasPrefix(p.ObjectKey, "nebulactrl/shop/"), p.ObjectKey)
	require.NotContains(t, string(cmd.Payload), builtinAccess)
	require.NotContains(t, string(cmd.Payload), builtinSecret)

	secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
	require.NoError(t, err)
	require.Equal(t, builtinAccess, secrets[agentprotocol.SecretBackupAccessKey])
	require.Equal(t, builtinSecret, secrets[agentprotocol.SecretBackupSecretKey])
	require.Equal(t, canaryDBPass, secrets[agentprotocol.SecretDBPassword])
}

func TestBuiltinDestinationFailsClearlyWithoutStorage(t *testing.T) {
	f := newFixture(t)
	sv := f.database("pg")
	d := f.builtinDestination()

	// Nothing wired: a plain 409, no command left behind.
	_, err := f.svc.CreateBackup(f.ctx, sv.ID, d.ID)
	ae, ok := apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 409, ae.Status)
	require.Equal(t, "builtin_storage_unavailable", ae.Code)
	require.Empty(t, f.disp.cmds)
	_, err = f.svc.destinationSecrets(f.ctx, d)
	require.Error(t, err)

	// The team's slice is still being set up.
	fake := newFakeBuiltin()
	fake.err = instsvc.ErrNotReady
	f.svc.SetBuiltinStorage(fake)
	_, err = f.svc.CreateBackup(f.ctx, sv.ID, d.ID)
	ae, ok = apierr.As(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "builtin_storage_not_ready", ae.Code)
	require.Empty(t, f.disp.cmds)

	// No slice at all.
	fake.err = instsvc.ErrNoSlice
	_, err = f.svc.CreateBackup(f.ctx, sv.ID, d.ID)
	ae, ok = apierr.As(err)
	require.True(t, ok)
	require.Equal(t, "builtin_storage_unavailable", ae.Code)

	// A scheduled run records why it did not start instead of failing the tick.
	fake.err = instsvc.ErrNotReady
	require.Empty(t, f.svc.builtinSkipReason(context.Background(), mustDest(t, f, f.s3Destination().ID)), "other kinds never skip")
	require.Contains(t, f.svc.builtinSkipReason(context.Background(), d), "not ready")
}

func TestBuiltinDestinationIsReadOnlyForTeams(t *testing.T) {
	f := newFixture(t)
	d := f.builtinDestination()
	f.svc.SetBuiltinStorage(newFakeBuiltin())

	_, err := f.svc.CreateDestination(f.ctx, DestinationInput{Name: "mine", Kind: DestBuiltin})
	ae, ok := apierr.As(err)
	require.True(t, ok)
	require.Equal(t, 409, ae.Status)
	require.Equal(t, "destination_builtin", ae.Code)

	_, err = f.svc.UpdateDestination(f.ctx, d.ID, DestinationPatch{Name: new("renamed")})
	ae, ok = apierr.As(err)
	require.True(t, ok)
	require.Equal(t, 409, ae.Status)
	require.Equal(t, "destination_builtin", ae.Code)
	_, err = f.svc.UpdateDestination(f.ctx, d.ID, DestinationPatch{Bucket: new("other-bucket"), AccessKey: new("AAAA"), SecretKey: new("BBBB")})
	require.Error(t, err)

	err = f.svc.DeleteDestination(f.ctx, d.ID)
	ae, ok = apierr.As(err)
	require.True(t, ok)
	require.Equal(t, 409, ae.Status)
	require.Equal(t, "destination_builtin", ae.Code)
	require.NotContains(t, ae.Message, builtinBucket)

	got := mustDest(t, f, d.ID)
	require.Equal(t, "Built-in backup storage", got.Name)
	require.Equal(t, DestBuiltin, got.Kind)
	require.Empty(t, got.Bucket)
}

func TestBuiltinFullMessageRewrite(t *testing.T) {
	const want = "Built-in backup storage is full (49.9 GiB of 50.0 GiB). Existing backups are kept."
	full := func(b *fakeBuiltin) { b.slice.State, b.slice.UsedBytes = instsvc.StateFull, 49*gib+gib*9/10 }
	for _, tc := range []struct {
		name  string
		setup func(*fakeBuiltin)
		fail  string
		want  string
	}{
		{"slice full", full, "put object: some unrelated error", want},
		{"quota gate answer", func(b *fakeBuiltin) { b.slice.UsedBytes = 49*gib + gib*9/10 }, "403 QuotaExceeded nebulactrl-quota-exceeded: built-in backup storage is full", want},
		{"native read-only flip", func(b *fakeBuiltin) { b.slice.UsedBytes = 49*gib + gib*9/10 }, "upload: AccessDenied", want},
		{"unrelated failure", nil, "pg_dump: connection refused", "pg_dump: connection refused"},
	} {
		t.Run("db dump/"+tc.name, func(t *testing.T) {
			f := newFixture(t)
			sv := f.database("pg")
			d := f.builtinDestination()
			fake := newFakeBuiltin()
			fake.slice.QuotaBytes = 50 * gib
			if tc.setup != nil {
				tc.setup(fake)
			}
			f.svc.SetBuiltinStorage(fake)
			b, err := f.svc.CreateBackup(f.ctx, sv.ID, d.ID)
			require.NoError(t, err)
			_, cmdID := f.disp.last()
			require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "failed", tc.fail, agentprotocol.DBBackupResult{})))
			got, err := f.svc.GetBackup(f.ctx, b.ID)
			require.NoError(t, err)
			require.Equal(t, StatusFailed, got.Status)
			require.Equal(t, tc.want, got.Error)
			require.Equal(t, NotifyBackupFailed, f.events[len(f.events)-1].Type, "the usual failure event follows")
			require.Equal(t, tc.want, f.events[len(f.events)-1].Message)
		})
	}

	t.Run("volume backup", func(t *testing.T) {
		f := newFixture(t)
		d := f.builtinDestination()
		fake := newFakeBuiltin()
		full(fake)
		f.svc.SetBuiltinStorage(fake)
		app := f.app("web", true)
		f.volConfig(app, DestinationView{ID: d.ID}, nil)
		b, err := f.svc.CreateVolumeBackup(f.ctx, app.ID)
		require.NoError(t, err)
		cmd, cmdID := f.lastCmd(agentprotocol.KindVolumeBackup)
		var p agentprotocol.VolumeBackupPayload
		require.NoError(t, json.Unmarshal(cmd.Payload, &p))
		require.Equal(t, builtinBucket, p.Repo.Destination.Bucket)
		require.Equal(t, "http://127.0.0.1:18333", p.Repo.Destination.Endpoint)
		secrets, err := f.svc.SecretsForCommand(f.ctx, cmd.Kind, cmd.Payload)
		require.NoError(t, err)
		require.Equal(t, builtinSecret, secrets[agentprotocol.SecretBackupSecretKey])
		require.NotEmpty(t, secrets[agentprotocol.SecretResticPassword])

		require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, agentprotocol.OutcomeFailed, "Fatal: unable to save snapshot: AccessDenied", nil)))
		got, err := f.svc.GetVolumeBackup(f.ctx, b.ID)
		require.NoError(t, err)
		require.Equal(t, StatusFailed, got.Status)
		require.Equal(t, want, got.Error)
		require.Equal(t, NotifyBackupFailed, f.events[len(f.events)-1].Type)
	})

	t.Run("external s3 keeps its own message", func(t *testing.T) {
		f := newFixture(t)
		sv := f.database("pg")
		dest := f.s3Destination()
		fake := newFakeBuiltin()
		full(fake)
		f.svc.SetBuiltinStorage(fake)
		b, err := f.svc.CreateBackup(f.ctx, sv.ID, dest.ID)
		require.NoError(t, err)
		_, cmdID := f.disp.last()
		require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "failed", "upload: AccessDenied", agentprotocol.DBBackupResult{})))
		got, _ := f.svc.GetBackup(f.ctx, b.ID)
		require.Equal(t, "upload: AccessDenied", got.Error)
	})

	t.Run("destination test", func(t *testing.T) {
		f := newFixture(t)
		d := f.builtinDestination()
		fake := newFakeBuiltin()
		full(fake)
		f.svc.SetBuiltinStorage(fake)
		dest, err := f.svc.destinationFor(f.ctx, d)
		require.NoError(t, err)
		payload, _ := json.Marshal(agentprotocol.BackupTestPayload{Destination: dest})
		cmdID := uuid.New()
		_, err = f.q.InsertAgentCommand(context.Background(), db.InsertAgentCommandParams{
			ID: cmdID, ServerID: f.server.ID, Kind: agentprotocol.KindBackupTest, Payload: payload, IdempotencyKey: "t-builtin", ExpiresAt: time.Now().Add(time.Hour),
		})
		require.NoError(t, err)
		require.NoError(t, f.svc.HandleResult(f.ctx, cmdID, f.result(cmdID, "failed", "write: AccessDenied", agentprotocol.BackupSimpleResult{})))
		v, err := f.svc.GetDestination(f.ctx, d.ID)
		require.NoError(t, err)
		require.NotNil(t, v.LastTestOK)
		require.False(t, *v.LastTestOK)
		require.Equal(t, want, v.LastTestMessage)
	})
}

func TestQuotaFullMessageUsesHumanSizes(t *testing.T) {
	require.Equal(t,
		"Built-in backup storage is full (512 B of 1.0 KiB). Existing backups are kept.",
		quotaFullMessage(instsvc.Slice{UsedBytes: 512, QuotaBytes: 1024}))
	require.Equal(t,
		"Built-in backup storage is full (50.0 GiB of 50.0 GiB). Existing backups are kept.",
		quotaFullMessage(instsvc.Slice{UsedBytes: 50 * gib, QuotaBytes: 50 * gib}))
}

// The control-plane backup writes to the system bucket straight through the
// control plane's own URL for the S3, and says it is not off-site.
func TestInstanceBackupToBuiltinStorage(t *testing.T) {
	fake, srv := newFakeS3(t)
	e := newInstEnv(t, (&fakeTools{}).tools())

	v, err := e.svc.Get(e.ctx)
	require.NoError(t, err)
	require.False(t, v.NotOffSite, "the default local directory is not flagged as built-in")

	// Not wired: the kind is refused with a plain message.
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{Kind: new(DestBuiltin)})
	requireStatus(t, err, 422)

	b := newFakeBuiltin()
	e.svc.SetBuiltinStorage(b, srv.URL)
	v, err = e.svc.Update(e.ctx, InstanceBackupPatch{
		Enabled: new(true), Kind: new(DestBuiltin), Prefix: new("prod"), RetentionCount: new(2),
	})
	require.NoError(t, err)
	require.Equal(t, DestBuiltin, v.Destination.Kind)
	require.True(t, v.NotOffSite)
	require.False(t, v.Destination.OffSite)
	require.True(t, v.Destination.Configured)
	require.False(t, v.Destination.AccessKeySet)
	require.Empty(t, v.Destination.Bucket)
	require.Empty(t, v.Destination.Endpoint)
	raw, _ := json.Marshal(v)
	require.NotContains(t, string(raw), b.sysSecret)
	require.NotContains(t, string(raw), srv.URL)
	require.Contains(t, string(raw), `"notOffSite":true`)

	// The builtin kind takes no endpoint, bucket or keys of its own.
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{Bucket: new("mine")})
	requireStatus(t, err, 422)
	_, err = e.svc.Update(e.ctx, InstanceBackupPatch{AccessKey: new("AAAA")})
	requireStatus(t, err, 422)

	v, err = e.svc.Test(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, v.Destination.LastTestOK)
	require.True(t, *v.Destination.LastTestOK, v.Destination.LastTestMessage)
	require.Empty(t, fake.keys())

	row := e.runOnce(t)
	require.Equal(t, "succeeded", row.Status, row.Error)
	require.Equal(t, DestBuiltin, row.DestKind)
	require.Contains(t, row.ObjectKey, "prod/nebulactrl-instance/")
	require.Len(t, fake.keys(), 2, "the dump and its manifest: %v", fake.keys())
	fake.mu.Lock()
	for _, a := range fake.auths {
		require.Contains(t, a, b.sysAccess, "signed with the system key")
	}
	fake.mu.Unlock()

	// Switching to a directory clears the flag.
	v, err = e.svc.Update(e.ctx, InstanceBackupPatch{Kind: new("local"), LocalDir: new("nightly")})
	require.NoError(t, err)
	require.False(t, v.NotOffSite)
	require.Equal(t, "local", v.Destination.Kind)
}

func TestInstanceBackupNotOffSiteIsFalseForOtherKinds(t *testing.T) {
	_, srv := newFakeS3(t)
	e := newInstEnv(t, (&fakeTools{}).tools())
	v, err := e.svc.Update(e.ctx, InstanceBackupPatch{
		Kind: new("s3"), Endpoint: new(srv.URL), Region: new("us-east-1"), Bucket: new("nebula-backups"),
		PathStyle: new(true), AccessKey: new("AKIATESTKEY"), SecretKey: new(canaryS3Secret),
	})
	require.NoError(t, err)
	require.False(t, v.NotOffSite)
	require.True(t, v.Destination.OffSite)
	raw, _ := json.Marshal(v)
	require.Contains(t, string(raw), `"notOffSite":false`)
}
