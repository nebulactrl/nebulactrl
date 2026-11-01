//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/decommission"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// scopedHost is the daemon as the decommission under test may see it: only
// the resources of the test's own service. The harness shares the daemon with
// the developer's containers and the other tests' proxy, which a real
// decommission would remove too.
type scopedHost struct {
	decommission.Docker
	service *atomic.Value
}

func (h scopedHost) mine(labels map[string]string) bool {
	id, _ := h.service.Load().(string)
	return id != "" && labels[agentprotocol.LabelService] == id
}

func (h scopedHost) ContainerList(ctx context.Context, f docker.ListFilter) ([]docker.ContainerInfo, error) {
	all, err := h.Docker.ContainerList(ctx, f)
	var out []docker.ContainerInfo
	for _, c := range all {
		if h.mine(c.Labels) {
			out = append(out, c)
		}
	}
	return out, err
}

func (h scopedHost) NetworkList(context.Context, string, map[string]string) ([]docker.NetworkInfo, error) {
	return nil, nil
}

func (h scopedHost) VolumeList(ctx context.Context) ([]docker.VolumeInfo, error) {
	all, err := h.Docker.VolumeList(ctx)
	var out []docker.VolumeInfo
	for _, v := range all {
		if h.mine(v.Labels) {
			out = append(out, v)
		}
	}
	return out, err
}

// Remove server (clean): the project is deleted, then the server is removed
// with the clean mode. The agent removes the test service's leftovers (a
// labelled container and a labelled volume that nothing else deleted) and
// leaves the unlabelled container next to them; the server row is deleted once
// the agent reports success.
func TestRemoveServerCleansTheHost(t *testing.T) {
	requireTier(t, tierFull)
	app := appFixture(t)
	var svcID atomic.Value
	s := ExclusiveStack(t, StackOptions{DecommissionTune: func(cfg *decommission.Config) {
		cfg.Docker = scopedHost{Docker: cfg.Docker, service: &svcID}
	}})
	ctx := context.Background()

	svc := s.NewService(SvcSpec{Name: "web", Image: app.AppImage, Vars: map[string]string{"VERSION": "v1"}})
	s.DeployOK(svc.ID)
	require.NotEmpty(t, s.RunningContainers(svc.ID))
	svcID.Store(svc.ID)

	// The server cannot go while the service is on it.
	r := s.Do("DELETE", "/servers/"+s.ServerID+"?confirm=itest-host&mode=clean", nil)
	require.Equal(t, http.StatusConflict, r.Status, string(r.Raw))
	require.Equal(t, "server_has_services", r.JSON()["code"])

	// Delete the project with everything in it; the agent removes the service.
	r = s.Do("DELETE", "/projects/"+s.ProjectID+"?cascade=true&confirm=Itest", nil)
	require.Less(t, r.Status, 300, string(r.Raw))
	require.Eventually(t, func() bool { return len(s.Containers(svc.ID)) == 0 }, 90*time.Second, time.Second,
		"the project delete removes the service's containers")

	// Leftovers the project delete did not know about: NebulaCtrl-labelled, and
	// an unlabelled container that must survive.
	suffix := strings.ToLower(s.ServerID[:8])
	leftover, unrelated, volume := "nc-itest-left-"+suffix, "itest-unrelated-"+suffix, "nc-vol-"+suffix+"-left"
	lbl := []string{"--label", agentprotocol.LabelManaged + "=true", "--label", agentprotocol.LabelService + "=" + svc.ID}
	_, err := sh(ctx, "", "docker", append(append([]string{"volume", "create"}, lbl...), volume)...)
	require.NoError(t, err)
	_, err = sh(ctx, "", "docker", append(append([]string{"create", "--name", leftover}, lbl...), "-v", volume+":/data", app.AppImage)...)
	require.NoError(t, err)
	_, err = sh(ctx, "", "docker", "create", "--name", unrelated, app.AppImage)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sh(ctx, "", "docker", "rm", "-f", leftover, unrelated)
		_, _ = sh(ctx, "", "docker", "volume", "rm", "-f", volume)
	})

	// The name must match.
	r = s.Do("DELETE", "/servers/"+s.ServerID+"?confirm=nope&mode=clean", nil)
	require.Equal(t, http.StatusUnprocessableEntity, r.Status, string(r.Raw))

	// Clean removal: accepted, then gone.
	r = s.Do("DELETE", "/servers/"+s.ServerID+"?confirm=itest-host&mode=clean", nil)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	require.Equal(t, "clean", r.JSON()["mode"])
	require.Eventually(t, func() bool {
		return s.Do("GET", "/servers/"+s.ServerID, nil).Status == http.StatusNotFound
	}, 3*time.Minute, time.Second, "the server row goes once the agent reported success")

	// The host has none of NebulaCtrl's containers or volumes of the service,
	// and keeps what is not NebulaCtrl's.
	out, err := sh(ctx, "", "docker", "ps", "-aq", "--filter", "label="+agentprotocol.LabelService+"="+svc.ID)
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(out), "no NebulaCtrl container of the service is left")
	out, err = sh(ctx, "", "docker", "volume", "ls", "-q", "--filter", "label="+agentprotocol.LabelService+"="+svc.ID)
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(out), "no NebulaCtrl volume of the service is left")
	out, err = sh(ctx, "", "docker", "ps", "-aq", "--filter", "name=^"+unrelated+"$")
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(out), "an unlabelled container is not touched")

	var n int
	require.NoError(t, s.Pool.QueryRow(ctx, "SELECT count(*) FROM servers WHERE id = $1", s.ServerID).Scan(&n))
	require.Zero(t, n)
	require.Equal(t, 1, s.auditCount("server.removed"))
}

// auditCount counts the audit rows of an action.
func (s *Stack) auditCount(action string) int {
	var n int
	if err := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action = $1", action).Scan(&n); err != nil {
		s.t.Fatalf("count audit rows: %v", err)
	}
	return n
}
