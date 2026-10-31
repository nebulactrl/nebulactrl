package cleanup

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A restore swaps a volume row to a restored Docker volume and retires the old
// one: both are protected, as are retired volumes of other (deleted) services.
func TestProtectedSetCoversRestoredAndRetiredVolumes(t *testing.T) {
	f := newFixture(t)
	web := f.service("web", "image", `{}`)
	_, err := f.pool.Exec(bg, "INSERT INTO volumes (service_id, name, mount_path, docker_name, restore_label) VALUES ($1,'data','/data',$2,'restored from Jan 12, 03:30'),($1,'logs','/logs','','')",
		web.ID, agentprotocol.VolumeName(web.ID.String(), "data")+"-r260112033000")
	require.NoError(t, err)
	_, err = f.pool.Exec(bg, "INSERT INTO retired_volumes (service_id, volume_name, docker_name, reason) VALUES ($1,'data',$2,'replaced')",
		web.ID, agentprotocol.VolumeName(web.ID.String(), "data"))
	require.NoError(t, err)

	prot, err := f.svc.Protected(bg, f.q, f.server.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		agentprotocol.VolumeName(web.ID.String(), "data") + "-r260112033000", // the restored volume the row mounts
		agentprotocol.VolumeName(web.ID.String(), "logs"),                    // derived name
		agentprotocol.VolumeName(web.ID.String(), "data"),                    // retired
	}, prot.Volumes)
}
