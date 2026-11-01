package deployments

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// A deploy hook's image reference is a one-off override of one release: the
// release source, the deploy payload and the image variable carry it, the
// service's configuration (and so its generation) stays as it was, and the
// deployment has the deploy_hook trigger.
func TestDeployWithImageRefOverridesOneRelease(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	bg := context.Background()
	var genBefore int64
	require.NoError(t, f.pool.QueryRow(bg, "SELECT generation FROM services WHERE id = $1", web.ID).Scan(&genBefore))

	v, err := f.deps.DeployWith(bg, web.ID, TriggerDeployHook, InternalOptions{ImageRef: "traefik/whoami:v1.10.1", Message: "Docker Hub push"})
	require.NoError(t, err)
	require.Equal(t, TriggerDeployHook, v.Trigger)
	require.NoError(t, f.deps.runDeployRef(bg, v.ID, "", "traefik/whoami:v1.10.1"))

	var p agentprotocol.DeployPayload
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p))
	require.NotNil(t, p.Image)
	require.Equal(t, "traefik/whoami:v1.10.1", p.Image.Ref)

	var srcRaw, snapRaw []byte
	var imageRef string
	require.NoError(t, f.pool.QueryRow(bg,
		"SELECT r.source, r.config, r.image_ref FROM releases r JOIN deployments d ON d.release_id = r.id WHERE d.id = $1", v.ID).Scan(&srcRaw, &snapRaw, &imageRef))
	require.Equal(t, "traefik/whoami:v1.10.1", services.DecodeSource(srcRaw).Image, "the release source records the override")
	require.Equal(t, "traefik/whoami:v1.10.1", imageRef)
	require.Equal(t, "traefik/whoami:latest", services.DecodeSnapshot(snapRaw).Config.Image, "the configured image is untouched")

	var genAfter int64
	var cfg []byte
	require.NoError(t, f.pool.QueryRow(bg, "SELECT generation, config FROM services WHERE id = $1", web.ID).Scan(&genAfter, &cfg))
	require.Equal(t, genBefore, genAfter, "no staged change")
	require.Equal(t, "traefik/whoami:latest", services.DecodeConfig(cfg).Image)

	// The next plain deploy goes back to the configured image.
	data, _ := json.Marshal(agentprotocol.DeployResult{Outcome: agentprotocol.OutcomeSucceeded, ImageDigest: "traefik/whoami@sha256:abc"})
	require.NoError(t, f.deps.HandleResult(bg, f.server.ID, uuid.New(), &v.ID, agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
	v2, err := f.deps.DeployWith(bg, web.ID, TriggerDeployHook, InternalOptions{})
	require.NoError(t, err)
	require.NoError(t, f.deps.runDeployRef(bg, v2.ID, "", ""))
	require.NoError(t, json.Unmarshal(f.lastCommand(agentprotocol.KindDeploy).Payload, &p))
	require.Equal(t, "traefik/whoami:latest", p.Image.Ref)
}

func TestDeployWithImageRefRejectsGitServices(t *testing.T) {
	f := newRouteFixture(t)
	web := f.web("api")
	_, err := f.pool.Exec(context.Background(), "UPDATE services SET source_type = 'git' WHERE id = $1", web.ID)
	require.NoError(t, err)
	_, err = f.deps.DeployWith(context.Background(), web.ID, TriggerDeployHook, InternalOptions{ImageRef: "traefik/whoami:v1"})
	var ae *apierr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)
}
