package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/compose/composetest"
	"github.com/nebulactrl/nebulactrl/internal/agent/decommission"
	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// volumeDocker is the fake daemon plus the volume list a decommission needs.
type volumeDocker struct {
	*fakeDocker
	vmu      sync.Mutex
	volumes  map[string]docker.VolumeInfo
	refuseRM map[string]bool
}

func newVolumeDocker() *volumeDocker {
	return &volumeDocker{fakeDocker: newFakeDocker(), volumes: map[string]docker.VolumeInfo{}, refuseRM: map[string]bool{}}
}

func (d *volumeDocker) addVolume(name string, labels map[string]string) {
	d.vmu.Lock()
	defer d.vmu.Unlock()
	d.volumes[name] = docker.VolumeInfo{Name: name, Driver: "local", Labels: labels}
}

func (d *volumeDocker) VolumeList(context.Context) ([]docker.VolumeInfo, error) {
	d.vmu.Lock()
	defer d.vmu.Unlock()
	out := make([]docker.VolumeInfo, 0, len(d.volumes))
	for _, v := range d.volumes {
		out = append(out, v)
	}
	return out, nil
}

func (d *volumeDocker) RemoveVolume(_ context.Context, name string, _ bool) error {
	d.vmu.Lock()
	defer d.vmu.Unlock()
	if d.refuseRM[name] {
		return errors.New("volume is in use")
	}
	delete(d.volumes, name)
	return nil
}

func (d *volumeDocker) volumeNames() []string {
	d.vmu.Lock()
	defer d.vmu.Unlock()
	out := make([]string, 0, len(d.volumes))
	for n := range d.volumes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (d *volumeDocker) containerNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.containers))
	for _, c := range d.containers {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

func (d *volumeDocker) networkNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.nets))
	for n := range d.nets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// put adds a container with the given labels.
func (d *volumeDocker) put(name string, running bool, labels map[string]string) docker.ContainerInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	c := &docker.ContainerInfo{ID: name + "-id", Name: name, Labels: labels, Created: time.Now()}
	if running {
		c.State, c.Running = "running", true
	} else {
		c.State = "exited"
	}
	d.containers[c.ID] = c
	return *c
}

func decommissionCmd(t *testing.T) agentprotocol.Command {
	t.Helper()
	raw, err := json.Marshal(agentprotocol.DecommissionPayload{ServerID: "srv-1"})
	require.NoError(t, err)
	return agentprotocol.Command{ID: "cmd-1", Kind: agentprotocol.KindDecommission, Payload: raw}
}

func managed(extra ...string) map[string]string {
	l := map[string]string{agentprotocol.LabelManaged: "true"}
	for i := 0; i+1 < len(extra); i += 2 {
		l[extra[i]] = extra[i+1]
	}
	return l
}

func newDecommissionEngine(t *testing.T, d *volumeDocker, stopProxy func()) *Engine {
	t.Helper()
	e, err := New(Config{
		Docker: d, Proxy: &fakeProxy{}, Sender: &fakeSender{},
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen), DrainDelay: time.Millisecond,
		StopProxy: stopProxy,
	})
	require.NoError(t, err)
	return e
}

// A decommission removes what carries a NebulaCtrl label or one of the agent's
// fixed infrastructure names, and nothing else.
func TestDecommissionRemovesOnlyWhatIsNebulaCtrls(t *testing.T) {
	d := newVolumeDocker()
	// NebulaCtrl's: services (running and stopped), a database upgrade helper, a
	// volume-backup helper, a task container, the proxy and its siblings by
	// name (the agent created them), a compose stack component.
	d.put("nc-web-aaaaaaaa-0", true, managed(agentprotocol.LabelService, "svc-1"))
	d.put("nc-old-bbbbbbbb-0", false, managed(agentprotocol.LabelService, "svc-2"))
	d.put("pg-upgrade-helper", true, map[string]string{agentprotocol.LabelManaged: "pg-upgrade", agentprotocol.LabelService: "svc-3"})
	d.put("vol-backup-helper", false, map[string]string{agentprotocol.LabelHelper: agentprotocol.HelperBackup})
	d.put(agentprotocol.ProxyContainer, true, nil)
	d.put(agentprotocol.PagesContainer, true, nil)
	d.put(agentprotocol.TunnelContainer, false, managed())
	d.put("nc-stack-cccccccc-web", true, map[string]string{
		agentprotocol.LabelManaged: "true", agentprotocol.LabelService: "svc-4", decommission.ComposeProjectLabel: "nc-cccccccc",
	})
	// Not NebulaCtrl's: a user's containers, one named like ours without the
	// prefix, one with another vendor's labels.
	d.put("user-postgres", true, nil)
	d.put("nc-lookalike", true, map[string]string{"com.example.owner": "someone"})
	d.put("traefik", true, map[string]string{"traefik.enable": "true"})

	for _, n := range []string{"nebulactrl-edge", "nebulactrl-env-aaaaaaaa", "nebulactrl-env-bbbbbbbb-int", "labelled-net"} {
		labels := managed()
		if n == "labelled-net" {
			labels = map[string]string{agentprotocol.LabelEnvironment: "env-1"}
		}
		require.NoError(t, d.EnsureNetwork(context.Background(), n, labels))
	}
	require.NoError(t, d.EnsureNetwork(context.Background(), "user-net", nil))
	require.NoError(t, d.EnsureNetwork(context.Background(), "compose_default", map[string]string{"com.docker.compose.project": "compose"}))

	d.addVolume("nc-vol-aaaaaaaa-data", managed(agentprotocol.LabelService, "svc-1"))
	d.addVolume("nc-vol-bbbbbbbb-unlabelled", nil)
	d.addVolume(agentprotocol.ProxyACMEVolume, nil)
	d.addVolume(agentprotocol.TailscaleStateVolume, managed())
	d.addVolume("user-data", nil)
	d.addVolume("nebulactrl-dev_postgres-data", map[string]string{"com.docker.compose.project": "nebulactrl-dev"})

	var stopped int
	cr := &composetest.Fake{}
	e := newDecommissionEngine(t, d, func() { stopped++ })
	e.cfg.Compose = cr

	res := e.HandleCommand(context.Background(), decommissionCmd(t))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	var out agentprotocol.DecommissionResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Empty(t, out.Failed)
	require.Equal(t, 8, out.Containers)
	require.Equal(t, 4, out.Networks)
	require.Equal(t, 4, out.Volumes)

	require.Equal(t, []string{"nc-lookalike", "traefik", "user-postgres"}, d.containerNames())
	require.Equal(t, []string{"compose_default", "user-net"}, d.networkNames())
	require.Equal(t, []string{"nebulactrl-dev_postgres-data", "user-data"}, d.volumeNames())
	downs := cr.Of("down")
	require.Len(t, downs, 1, "the stack's own networks and volumes go with `compose down --volumes`")
	require.Equal(t, "nc-cccccccc", downs[0].Project.Name)
	require.True(t, downs[0].Down.RemoveVolumes)
	require.Equal(t, 1, stopped, "the proxy's repair loop is stopped first")

	// A host that was emptied is not refilled: commands and reconcile frames
	// are refused.
	require.True(t, e.Decommissioning())
	dep := e.HandleCommand(context.Background(), agentprotocol.Command{ID: "cmd-2", Kind: agentprotocol.KindDeploy, Payload: json.RawMessage(`{}`)})
	require.Equal(t, agentprotocol.OutcomeFailed, dep.Outcome)
	require.Contains(t, dep.Error, "being removed")
	require.NoError(t, e.Reconcile(context.Background(), agentprotocol.Reconcile{}))
	require.Equal(t, []string{"nc-lookalike", "traefik", "user-postgres"}, d.containerNames())

	// Running it again finds nothing: it is idempotent.
	again := e.HandleCommand(context.Background(), decommissionCmd(t))
	require.Equal(t, agentprotocol.OutcomeSucceeded, again.Outcome, again.Error)
	require.NoError(t, json.Unmarshal(again.Data, &out))
	require.Zero(t, out.Containers+out.Networks+out.Volumes)
}

// The container the agent itself runs in survives, and leaves the networks it
// had joined.
func TestDecommissionKeepsTheAgentsOwnContainer(t *testing.T) {
	d := newVolumeDocker()
	self := d.put("agent", true, managed())
	d.put("nc-web-aaaaaaaa-0", true, managed(agentprotocol.LabelService, "svc-1"))
	require.NoError(t, d.EnsureNetwork(context.Background(), "nebulactrl-env-aaaaaaaa", managed()))
	require.NoError(t, d.NetworkConnect(context.Background(), "nebulactrl-env-aaaaaaaa", "agent", nil))

	e := newDecommissionEngine(t, d, nil)
	e.cfg.AgentContainer = self.Name

	res := e.HandleCommand(context.Background(), decommissionCmd(t))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, []string{"agent"}, d.containerNames())
	require.Empty(t, d.networkNames(), "the network is gone once the agent left it")
}

// What cannot be removed fails the command and gives the host back to the
// agent: nothing stays blocked.
func TestDecommissionFailureReportsAndResumes(t *testing.T) {
	d := newVolumeDocker()
	d.put("nc-web-aaaaaaaa-0", true, managed(agentprotocol.LabelService, "svc-1"))
	d.addVolume("nc-vol-aaaaaaaa-data", managed())
	d.refuseRM["nc-vol-aaaaaaaa-data"] = true
	// Somebody else's container sits on one of our networks.
	require.NoError(t, d.EnsureNetwork(context.Background(), "nebulactrl-env-aaaaaaaa", managed()))
	d.put("user-tool", true, nil)
	require.NoError(t, d.NetworkConnect(context.Background(), "nebulactrl-env-aaaaaaaa", "user-tool", nil))

	e := newDecommissionEngine(t, d, nil)
	res := e.HandleCommand(context.Background(), decommissionCmd(t))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	var out agentprotocol.DecommissionResult
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Equal(t, 1, out.Containers)
	require.Len(t, out.Failed, 2)
	kinds := []string{out.Failed[0].Kind, out.Failed[1].Kind}
	sort.Strings(kinds)
	require.Equal(t, []string{"network", "volume"}, kinds)
	require.Contains(t, res.Error, "nc-vol-aaaaaaaa-data")
	require.False(t, e.Decommissioning(), "a failed run lets the agent work again")
	require.Equal(t, []string{"user-tool"}, d.containerNames())
}

func TestDecommissionNeedsASupportingDaemonAndAValidPayload(t *testing.T) {
	r := newRig(t, nil) // the plain fake daemon cannot list volumes
	res := r.eng.HandleCommand(context.Background(), decommissionCmd(t))
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, res.Error, "cannot list volumes")
	require.False(t, r.eng.Decommissioning())

	bad := agentprotocol.Command{ID: "cmd-3", Kind: agentprotocol.KindDecommission, Payload: json.RawMessage(`{`)}
	require.Equal(t, agentprotocol.OutcomeFailed, r.eng.HandleCommand(context.Background(), bad).Outcome)
}
