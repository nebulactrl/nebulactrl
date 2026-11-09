package services_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestNormalizeVolumeOwner(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "  ": "", "0": "0", "10001": "10001", "10001:10001": "10001:10001", " 10001:0 ": "10001:0",
		"007:08": "7:8", "2147483647:2147483647": "2147483647:2147483647",
	} {
		got, err := services.NormalizeVolumeOwner(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{
		"nobody", "app:app", "1:app", "-1", "+1", "1.5", ":", "1:", ":1", "1:2:3", "0x10", "2147483648", "1:2147483648", "99999999999999999999",
	} {
		_, err := services.NormalizeVolumeOwner(in)
		requireCode(t, err, "invalid_config")
	}
}

func TestVolumeOwnerOnAddAndUpdate(t *testing.T) {
	f := newTxFixture(t)
	svc := f.imageService(f.prod.ID, "web")

	// A bad owner is a 422 invalid_config and creates nothing.
	_, err := f.svcs.AddVolume(f.ctx, svc.ID, "dist", "/dist", "app:app")
	requireCode(t, err, "invalid_config")
	require.Equal(t, 0, f.count("SELECT count(*) FROM volumes WHERE service_id = $1", svc.ID))

	// A good owner is stored in canonical form and listed.
	v, err := f.svcs.AddVolume(f.ctx, svc.ID, "dist", "/dist", "010001:010001")
	require.NoError(t, err)
	require.Equal(t, "10001:10001", v.Owner)
	require.Equal(t, 1, f.audits("volume.create", v.ID.String()))
	list, err := f.svcs.ListVolumes(f.ctx, svc.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "10001:10001", list[0].Owner)

	// No owner stays empty.
	plain, err := f.svcs.AddVolume(f.ctx, svc.ID, "cache", "/cache", "")
	require.NoError(t, err)
	require.Empty(t, plain.Owner)

	// UpdateVolume stages a change: the generation moves and the change is audited.
	gen := f.row(svc.ID).Generation
	up, err := f.svcs.UpdateVolume(f.ctx, plain.ID, "1000")
	require.NoError(t, err)
	require.Equal(t, "1000", up.Owner)
	require.Equal(t, gen+1, f.row(svc.ID).Generation)
	require.Equal(t, 1, f.audits("volume.update", plain.ID.String()))

	// The same owner again changes nothing.
	_, err = f.svcs.UpdateVolume(f.ctx, plain.ID, "1000")
	require.NoError(t, err)
	require.Equal(t, gen+1, f.row(svc.ID).Generation)
	require.Equal(t, 1, f.audits("volume.update", plain.ID.String()))

	// A bad owner, an unknown volume.
	_, err = f.svcs.UpdateVolume(f.ctx, plain.ID, "root")
	requireCode(t, err, "invalid_config")
	_, err = f.svcs.UpdateVolume(f.ctx, uuid.New(), "1000")
	requireCode(t, err, "not_found")

	// An empty owner clears it.
	cleared, err := f.svcs.UpdateVolume(f.ctx, plain.ID, "")
	require.NoError(t, err)
	require.Empty(t, cleared.Owner)

	// The Tx form validates the same way and rolls back with its transaction.
	err = f.inTx(false, func(tx pgx.Tx) error {
		_, err := f.svcs.SetVolumeOwnerTx(f.ctx, tx, f.scope(tx, svc.ID), "dist", "x")
		return err
	})
	requireCode(t, err, "invalid_config")
	require.NoError(t, f.inTx(false, func(tx pgx.Tx) error {
		_, err := f.svcs.SetVolumeOwnerTx(f.ctx, tx, f.scope(tx, svc.ID), "dist", "0:0")
		return err
	}))
	got, err := f.svcs.ListVolumes(f.ctx, svc.ID)
	require.NoError(t, err)
	for _, g := range got {
		if g.Name == "dist" {
			require.Equal(t, "10001:10001", g.Owner, "rolled back")
		}
	}
}

// The owner travels with the release: a snapshot carries it and the agent's
// volume mounts are built from the snapshot.
func TestVolumeOwnerInSnapshotAndRuntimeParts(t *testing.T) {
	row := db.Volume{Name: "dist", MountPath: "/dist", Owner: "10001:10001"}
	snap := services.SnapshotVolumeOf(row)
	require.Equal(t, "10001:10001", snap.Owner)

	svcID := uuid.MustParse("5e5e5e5e-0000-4000-8000-000000000001")
	parts := services.RuntimePartsOf(svcID, services.Snapshot{Volumes: []services.SnapshotVolume{snap, {Name: "cache", MountPath: "/cache"}}})
	require.Equal(t, []agentprotocol.VolumeMount{
		{Name: agentprotocol.VolumeName(svcID.String(), "dist"), MountPath: "/dist", Owner: "10001:10001"},
		{Name: agentprotocol.VolumeName(svcID.String(), "cache"), MountPath: "/cache"},
	}, parts.Volumes)
}

func TestDiffReleaseVolumeOwner(t *testing.T) {
	f := newTxFixture(t)
	sv := f.imageService(f.prod.ID, "web")
	cfg := services.DecodeConfig(sv.Config)
	rel := f.release(sv.ID, services.Snapshot{Config: cfg, Volumes: []services.SnapshotVolume{
		{Name: "same", MountPath: "/same", Owner: "1:1"},
		{Name: "set", MountPath: "/set"},
		{Name: "changed", MountPath: "/changed", Owner: "1:1"},
		{Name: "cleared", MountPath: "/cleared", Owner: "1:1"},
		{Name: "gone", MountPath: "/gone", Owner: "2:2"},
	}}, nil)
	cur := services.CurrentState{
		Config: cfg, SourceType: services.SourceImage, EnvironmentID: f.prod.ID,
		Volumes: []db.Volume{
			{ServiceID: sv.ID, Name: "same", MountPath: "/same", Owner: "1:1"},
			{ServiceID: sv.ID, Name: "set", MountPath: "/set", Owner: "10001:10001"},
			{ServiceID: sv.ID, Name: "changed", MountPath: "/changed", Owner: "2:2"},
			{ServiceID: sv.ID, Name: "cleared", MountPath: "/cleared"},
			{ServiceID: sv.ID, Name: "new", MountPath: "/new", Owner: "5"},
		},
	}
	changes, err := services.DiffRelease(f.ctx, f.q, f.box(), rel, cur)
	require.NoError(t, err)
	by := changesByField(changes)

	require.NotContains(t, by, "volumes.same")
	require.Equal(t, services.FieldChange{Field: "volumes.set", From: "/set", To: "/set · owner 10001:10001"}, by["volumes.set"])
	require.Equal(t, services.FieldChange{Field: "volumes.changed", From: "/changed · owner 1:1", To: "/changed · owner 2:2"}, by["volumes.changed"])
	require.Equal(t, services.FieldChange{Field: "volumes.cleared", From: "/cleared · owner 1:1", To: "/cleared"}, by["volumes.cleared"])
	require.Equal(t, services.FieldChange{Field: "volumes.new", To: "/new · owner 5"}, by["volumes.new"])
	require.Equal(t, services.FieldChange{Field: "volumes.gone", From: "/gone · owner 2:2"}, by["volumes.gone"])
}
