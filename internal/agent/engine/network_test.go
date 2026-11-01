package engine

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/internal/agent/health"
	"github.com/nebulactrl/nebulactrl/internal/agent/netpool"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	netEnv    = "nebulactrl-env-e0e0e0e0"
	netInt    = "nebulactrl-env-e0e0e0e0-int"
	sharedID  = "f1f1f1f1-0000-4000-8000-000000000009"
	sharedNet = "nebulactrl-shared-f1f1f1f1"
	netAlias  = "web.production.shop"
)

// newNetRig is newRig with a Config tweak (agent container, hostname, ...).
func newNetRig(t *testing.T, hf HealthFunc, tweak func(*Config)) *rig {
	t.Helper()
	r := &rig{t: t, dk: newFakeDocker(), px: &fakeProxy{}, snd: &fakeSender{}, bld: &fakeBuilder{}}
	if hf == nil {
		hf = healthy
	}
	cfg := Config{
		Docker: r.dk, Proxy: r.px, Builder: r.bld, Sender: r.snd, Health: hf,
		DrainDelay: time.Millisecond, VerifyInterval: time.Millisecond,
		Networks: hermeticPool(t, netpool.DefaultPool, netpool.DefaultPrefixLen),
	}
	if tweak != nil {
		tweak(&cfg)
	}
	e, err := New(cfg)
	require.NoError(t, err)
	r.eng = e
	return r
}

// addrRecorder is a HealthFunc that records the addresses of every check.
type addrRecorder struct {
	mu    sync.Mutex
	addrs [][]string
}

func (a *addrRecorder) wait(_ context.Context, t health.Target) health.Result {
	a.mu.Lock()
	a.addrs = append(a.addrs, append([]string(nil), t.Addrs...))
	a.mu.Unlock()
	return health.Result{Healthy: true}
}

func netNames(specNets []docker.NetworkAttachment) []string {
	out := make([]string, len(specNets))
	for i, n := range specNets {
		out[i] = n.Network
	}
	return out
}

func requireNoEdge(t *testing.T, spec docker.ContainerSpec) {
	t.Helper()
	require.NotEmpty(t, spec.Networks, "%s joins a network", spec.Name)
	for _, n := range spec.Networks {
		require.NotEqual(t, agentprotocol.EdgeNetwork, n.Network, "%s must not join the shared edge network", spec.Name)
	}
}

func TestAppTaskAndSidecarContainersNeverJoinTheEdgeNetwork(t *testing.T) {
	r := tailnetRig(t, tnOnline)
	p := preDeployPayload()
	p.Tailnet = tailnetSpec()
	exitWith(r, preCtr, 0) // the pre-deploy container exits right away

	res, dr := r.deploy(p, map[string]string{agentprotocol.SecretServiceTailnetKey: tnKey, "env:DB_URL": "postgres://db"})
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	exitWith(r, taskCtr, 0)
	tres := r.eng.HandleCommand(context.Background(), taskCmd(t, basicTask(), map[string]string{"env:API_KEY": "k", "env:MODE": "m"}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, tres.Outcome, tres.Error)

	names := map[string]bool{}
	for _, spec := range r.dk.created {
		names[spec.Name] = true
		requireNoEdge(t, spec)
	}
	require.True(t, names[preCtr], "pre-deploy container")
	require.True(t, names[newCtr], "app container")
	require.True(t, names[tnCtr], "tailnet sidecar")
	require.True(t, names[taskCtr], "task container")
	require.NotContains(t, r.dk.netsEnsured, agentprotocol.EdgeNetwork, "the deploy engine never creates the edge network")
}

func TestBlockEgressJoinsOnlyTheInternalNetwork(t *testing.T) {
	rec := &addrRecorder{}
	r := newNetRig(t, rec.wait, nil)
	p := basePayload()
	p.BlockEgress = true
	p.SharedNetworks = []agentprotocol.SharedNetworkAttachment{{Name: sharedNet, Alias: netAlias}}

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	spec := r.dk.created[0]
	require.Equal(t, []docker.NetworkAttachment{{Network: netInt, Aliases: []string{"web"}}}, spec.Networks,
		"only the internal network; a shared bridge would give the service a route out again")
	require.True(t, r.dk.nets[netInt].internal, "the internal network is created with internal: true")
	require.NotContains(t, r.dk.netsEnsured, netEnv, "the environment network is not needed by this service")
	require.NotContains(t, r.dk.netsEnsured, sharedNet)
	require.NotContains(t, r.dk.netsEnsured, agentprotocol.EdgeNetwork)

	own := r.px.applies[0][1]
	require.Equal(t, []string{netInt}, own.Networks, "the proxy joins the internal network to reach it")

	require.Len(t, rec.addrs, 1)
	info, _ := r.dk.state(newCtr)
	require.Equal(t, info.Networks[netInt].IP, rec.addrs[0][0], "probed on the internal network IP")
	require.Equal(t, newCtr, rec.addrs[0][len(rec.addrs[0])-1], "DNS name is the fallback")
}

func TestEnvWithABlockedServiceJoinsBothNetworks(t *testing.T) {
	rec := &addrRecorder{}
	r := newNetRig(t, rec.wait, nil)
	p := basePayload()
	p.EnvHasEgressBlocked = true

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	spec := r.dk.created[0]
	require.Equal(t, []docker.NetworkAttachment{
		{Network: netEnv, Aliases: []string{"web"}},
		{Network: netInt, Aliases: []string{"web"}},
	}, spec.Networks, "the alias is on both networks so the blocked service can still be reached")
	require.False(t, r.dk.nets[netEnv].internal)
	require.True(t, r.dk.nets[netInt].internal)

	own := r.px.applies[0][1]
	require.Equal(t, []string{netEnv}, own.Networks, "the proxy needs only the environment network")

	info, _ := r.dk.state(newCtr)
	require.Equal(t, info.Networks[netEnv].IP, rec.addrs[0][0], "probes use the primary (environment) network")
}

func TestSharedNetworkIsJoinedWithItsAlias(t *testing.T) {
	r := newNetRig(t, nil, nil)
	p := basePayload()
	p.SharedNetworks = []agentprotocol.SharedNetworkAttachment{{Name: agentprotocol.SharedNetworkName(sharedID), Alias: netAlias}}

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	require.Equal(t, []docker.NetworkAttachment{
		{Network: netEnv, Aliases: []string{"web"}},
		{Network: sharedNet, Aliases: []string{netAlias}},
	}, r.dk.created[0].Networks)
	require.Contains(t, r.dk.netsEnsured, sharedNet, "a shared network is created on first attachment")
	require.False(t, r.dk.nets[sharedNet].internal)
	require.Equal(t, []string{netEnv}, r.px.applies[0][1].Networks, "routes never need a shared network")
}

func TestProbeIPComesFromTheEnvironmentNetwork(t *testing.T) {
	rec := &addrRecorder{}
	r := newNetRig(t, rec.wait, nil)

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	info, _ := r.dk.state(newCtr)
	require.Len(t, rec.addrs, 1)
	require.Equal(t, []string{info.Networks[netEnv].IP, newCtr}, rec.addrs[0])
	require.NotEmpty(t, info.Networks[netEnv].IP)
}

func TestProbeIPsPreferOrderAndSkipMissingNetworks(t *testing.T) {
	info := docker.ContainerInfo{Networks: map[string]docker.ContainerNetwork{
		netEnv: {IP: "10.0.0.5"}, netInt: {IP: "10.1.0.5"}, agentprotocol.EdgeNetwork: {IP: "10.2.0.5"},
	}}
	require.Equal(t, []string{"10.1.0.5"}, probeIPs(info, netInt))
	require.Equal(t, []string{"10.0.0.5", "10.1.0.5"}, probeIPs(info, netEnv, "nebulactrl-env-gone", netInt))
	require.Empty(t, probeIPs(info, "nebulactrl-env-gone"), "the edge network is never probed implicitly")
}

func TestNetPlanAttachments(t *testing.T) {
	plan := netPlan{Alias: "web", EnvironmentID: envID}
	require.Equal(t, []docker.NetworkAttachment{{Network: netEnv, Aliases: []string{"web"}}}, plan.Attachments())
	require.Equal(t, []string{netEnv}, plan.RouteNetworks())
	require.Equal(t, netEnv, plan.ProbeNetwork())

	plan.BlockEgress = true
	require.Equal(t, []docker.NetworkAttachment{{Network: netInt, Aliases: []string{"web"}}}, plan.Attachments())
	require.Equal(t, []string{netInt}, plan.RouteNetworks())
	require.Equal(t, netInt, plan.ProbeNetwork())

	task := netPlan{EnvironmentID: envID, EnvHasEgressBlocked: true}
	require.Equal(t, []docker.NetworkAttachment{{Network: netEnv}, {Network: netInt}}, task.Attachments(), "a task has no alias")
}

func TestTaskOfABlockedServiceJoinsOnlyTheInternalNetwork(t *testing.T) {
	r := newNetRig(t, nil, nil)
	r.previous()
	exitWith(r, taskCtr, 0)
	p := basicTask()
	p.BlockEgress = true

	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, map[string]string{"env:API_KEY": "k", "env:MODE": "m"}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, []docker.NetworkAttachment{{Network: netInt}}, r.dk.created[0].Networks)
	require.True(t, r.dk.nets[netInt].internal)
}

func TestTaskInAnEnvWithABlockedServiceJoinsBothNetworks(t *testing.T) {
	r := newNetRig(t, nil, nil)
	r.previous()
	exitWith(r, taskCtr, 0)
	p := basicTask()
	p.EnvHasEgressBlocked = true

	res := r.eng.HandleCommand(context.Background(), taskCmd(t, p, map[string]string{"env:API_KEY": "k", "env:MODE": "m"}))
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, res.Error)
	require.Equal(t, []string{netEnv, netInt}, netNames(r.dk.created[0].Networks))
}

func TestScaleUpKeepsTheNetworkPlanOfTheSet(t *testing.T) {
	r := newNetRig(t, nil, nil)
	p := basePayload()
	p.BlockEgress = true
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	res, dr = r.scale(p, 2)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Len(t, r.dk.created, 2)
	require.Equal(t, r.dk.created[0].Networks, r.dk.created[1].Networks)
	require.Equal(t, []string{netInt}, r.px.Current()[len(r.px.Current())-1].Networks)

	// A replica rebuilt from its labels (converge) keeps the plan too.
	c, _ := r.dk.state(newCtr)
	rebuilt, ok := payloadFromContainer(c)
	require.True(t, ok)
	require.True(t, rebuilt.BlockEgress)
}

// ---- a containerised agent ----

const agentCtr = "nc-agent-dev"

func seedAgent(r *rig, id string) {
	r.dk.mu.Lock()
	defer r.dk.mu.Unlock()
	r.dk.containers[id] = &docker.ContainerInfo{
		ID: id, Name: agentCtr, State: "running", Running: true,
		Networks: map[string]docker.ContainerNetwork{agentprotocol.EdgeNetwork: {IP: "10.2.0.2"}},
	}
}

func TestContainerisedAgentJoinsTheProbedNetworkAndLeavesAgain(t *testing.T) {
	r := newNetRig(t, nil, func(c *Config) { c.AgentContainer = agentCtr })
	seedAgent(r, "agent-id")

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	require.Equal(t, []string{netEnv + "<-" + agentCtr}, r.dk.connects, "joined the environment network to probe")
	require.Equal(t, []string{netEnv + "<-" + agentCtr}, r.dk.disconnects, "left it when the probe finished")
	require.Equal(t, []string{agentprotocol.EdgeNetwork}, r.dk.networksOf(agentCtr))
}

func TestContainerisedAgentProbesBlockedServicesThroughTheInternalNetwork(t *testing.T) {
	r := newNetRig(t, nil, func(c *Config) { c.AgentContainer = agentCtr })
	seedAgent(r, "agent-id")
	p := basePayload()
	p.BlockEgress = true

	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{netInt + "<-" + agentCtr}, r.dk.connects)
	require.Equal(t, []string{netInt + "<-" + agentCtr}, r.dk.disconnects)
}

func TestAgentDetectsItsContainerFromTheHostname(t *testing.T) {
	r := newNetRig(t, nil, func(c *Config) { c.Hostname = "agent-id" })
	seedAgent(r, "agent-id")

	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{netEnv + "<-" + agentCtr}, r.dk.connects)
}

func TestBareHostAgentNeverAttachesItself(t *testing.T) {
	// The hostname names no container: the agent runs on the host and reaches
	// bridge IPs directly.
	r := newNetRig(t, nil, func(c *Config) { c.Hostname = "my-laptop" })
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Empty(t, r.dk.connects)
	require.Empty(t, r.dk.disconnects)

	// A container that merely carries the hostname as its name is not the agent.
	r2 := newNetRig(t, nil, func(c *Config) { c.Hostname = agentCtr })
	seedAgent(r2, "agent-id")
	res, dr = r2.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Empty(t, r2.dk.connects)
}

func TestAgentLeavesNetworksItHoldsWithoutAProbe(t *testing.T) {
	r := newNetRig(t, nil, func(c *Config) { c.AgentContainer = agentCtr })
	seedAgent(r, "agent-id")
	r.dk.mu.Lock()
	r.dk.containers["agent-id"].Networks[netEnv] = docker.ContainerNetwork{IP: "10.0.0.9"}
	r.dk.mu.Unlock()

	require.NoError(t, r.eng.Reconcile(context.Background(), agentprotocol.Reconcile{}))
	require.Equal(t, []string{netEnv + "<-" + agentCtr}, r.dk.disconnects, "a crashed run's leftover membership is dropped")
	require.Equal(t, []string{agentprotocol.EdgeNetwork}, r.dk.networksOf(agentCtr))
}

// ---- orphan network sweep and reconcile ----

func addNet(r *rig, name string, internal bool, age time.Duration) {
	r.dk.mu.Lock()
	defer r.dk.mu.Unlock()
	if r.dk.nets == nil {
		r.dk.nets = map[string]*fakeNet{}
	}
	r.dk.nets[name] = &fakeNet{
		internal: internal, labels: map[string]string{agentprotocol.LabelManaged: "true"}, created: time.Now().Add(-age),
	}
}

func TestSweepRemovesUnusedEnvironmentInternalAndSharedNetworks(t *testing.T) {
	r := newNetRig(t, nil, nil)
	addNet(r, "nebulactrl-env-aaaaaaaa", false, time.Hour)
	addNet(r, "nebulactrl-env-aaaaaaaa-int", true, time.Hour)
	addNet(r, "nebulactrl-shared-bbbbbbbb", false, time.Hour)
	addNet(r, "nebulactrl-env-cccccccc", false, time.Hour) // in use below
	addNet(r, "nebulactrl-env-dddddddd", false, 10*time.Second)
	addNet(r, agentprotocol.EdgeNetwork, false, time.Hour)
	r.dk.seed("nc-web-x-0", "svc", "dep", true, time.Hour).Networks = map[string]docker.ContainerNetwork{"nebulactrl-env-cccccccc": {IP: "10.0.0.2"}}

	require.NoError(t, r.eng.sweepNetworks(context.Background()))
	require.ElementsMatch(t, []string{"nebulactrl-env-aaaaaaaa", "nebulactrl-env-aaaaaaaa-int", "nebulactrl-shared-bbbbbbbb"}, r.dk.netsRemoved)
	require.Contains(t, r.dk.nets, "nebulactrl-env-cccccccc", "a network with a container stays")
	require.Contains(t, r.dk.nets, "nebulactrl-env-dddddddd", "a network created moments ago may be about to get its first container")
	require.Contains(t, r.dk.nets, agentprotocol.EdgeNetwork, "the edge network is never swept")
}

// Networks with the nc-env-* / nc-shared-* names are swept like any other
// unused managed network; ones with a container stay.
func TestSweepRemovesUnusedLegacyNamedNetworks(t *testing.T) {
	r := newNetRig(t, nil, nil)
	addNet(r, "nc-env-aaaaaaaa", false, time.Hour)
	addNet(r, "nc-env-aaaaaaaa-int", true, time.Hour)
	addNet(r, "nc-shared-bbbbbbbb", false, time.Hour)
	addNet(r, "nc-env-cccccccc", false, time.Hour) // in use below
	r.dk.seed("nc-web-x-0", "svc", "dep", true, time.Hour).Networks = map[string]docker.ContainerNetwork{"nc-env-cccccccc": {IP: "10.0.0.2"}}

	require.NoError(t, r.eng.sweepNetworks(context.Background()))
	require.ElementsMatch(t, []string{"nc-env-aaaaaaaa", "nc-env-aaaaaaaa-int", "nc-shared-bbbbbbbb"}, r.dk.netsRemoved)
	require.Contains(t, r.dk.nets, "nc-env-cccccccc")
}

func TestSweepGraceIsSixtySecondsAndConfigurable(t *testing.T) {
	r := newNetRig(t, nil, nil)
	require.Equal(t, 60*time.Second, r.eng.cfg.NetworkMinAge)
	require.Equal(t, 10*time.Minute, r.eng.cfg.OrphanMinAge, "containers keep their own grace")
	addNet(r, "nebulactrl-env-aaaaaaaa", false, 50*time.Second) // a deploy may be about to use it
	addNet(r, "nebulactrl-env-bbbbbbbb", false, 61*time.Second)
	addNet(r, "nebulactrl-env-cccccccc-int", true, 3*time.Minute)

	require.NoError(t, r.eng.sweepNetworks(context.Background()))
	require.ElementsMatch(t, []string{"nebulactrl-env-bbbbbbbb", "nebulactrl-env-cccccccc-int"}, r.dk.netsRemoved)
	require.Contains(t, r.dk.nets, "nebulactrl-env-aaaaaaaa")

	r2 := newNetRig(t, nil, func(c *Config) { c.NetworkMinAge = 5 * time.Minute })
	addNet(r2, "nebulactrl-env-aaaaaaaa", false, 3*time.Minute)
	require.NoError(t, r2.eng.sweepNetworks(context.Background()))
	require.Empty(t, r2.dk.netsRemoved, "NetworkMinAge overrides the default")
}

func TestSweepKeepsTheGraceOfANetworkJustCreatedByADeploy(t *testing.T) {
	r := newNetRig(t, nil, nil)
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	r.dk.mu.Lock()
	for _, c := range r.dk.containers { // empty the network again, as a stopped service would
		c.Networks = nil
	}
	r.dk.mu.Unlock()

	require.NoError(t, r.eng.sweepNetworks(context.Background()))
	require.Empty(t, r.dk.netsRemoved, "the network is seconds old")
	r.dk.mu.Lock()
	r.dk.nets[netEnv].created = time.Now().Add(-2 * time.Minute)
	r.dk.mu.Unlock()
	require.NoError(t, r.eng.sweepNetworks(context.Background()))
	require.Equal(t, []string{netEnv}, r.dk.netsRemoved)
}

func TestNetworksAreCreatedWithSubnetsFromThePool(t *testing.T) {
	r := newNetRig(t, nil, nil)
	p := basePayload()
	p.EnvHasEgressBlocked = true
	p.SharedNetworks = []agentprotocol.SharedNetworkAttachment{{Name: sharedNet, Alias: netAlias}}
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)

	pool := netip.MustParsePrefix("10.213.0.0/16")
	subnets := map[string]bool{}
	for _, name := range []string{netEnv, netInt, sharedNet} {
		n := r.dk.nets[name]
		require.NotNil(t, n, name)
		sub := netip.MustParsePrefix(n.subnet)
		require.True(t, pool.Contains(sub.Addr()), "%s: %s is outside the pool", name, n.subnet)
		require.Equal(t, 24, sub.Bits())
		require.False(t, subnets[n.subnet], "%s shares a subnet", name)
		subnets[n.subnet] = true
	}
	require.NotContains(t, r.dk.nets, agentprotocol.EdgeNetwork, "the edge network is not the engine's")
}

func TestNetworkPoolIsConfigurableAndExhaustionFailsTheDeployNamingTheFlag(t *testing.T) {
	alloc := hermeticPool(t, "10.213.0.0/24", 25) // two subnets
	r := newNetRig(t, nil, func(c *Config) { c.Networks = alloc })
	p := basePayload()
	p.EnvHasEgressBlocked = true // env + internal network: the pool is full
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, "10.213.0.0/25", r.dk.nets[netEnv].subnet)
	require.Equal(t, "10.213.0.128/25", r.dk.nets[netInt].subnet)

	p2 := basePayload()
	p2.EnvironmentID = "e1e1e1e1-0000-4000-8000-000000000002"
	p2.DeploymentID = "d3d3d3d3-0000-4000-8000-000000000003"
	res, dr = r.deploy(p2, nil)
	require.Equal(t, agentprotocol.OutcomeFailed, res.Outcome)
	require.Contains(t, dr.Message, "--network-pool")
}

func TestAnExistingNetworkIsReusedWhateverItsSubnet(t *testing.T) {
	r := newNetRig(t, nil, nil)
	addNet(r, netEnv, false, time.Hour)
	r.dk.nets[netEnv].subnet = "172.31.0.0/16"
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, "172.31.0.0/16", r.dk.nets[netEnv].subnet)
}

func TestAPoolSubnetTheDaemonRefusesAsOverlappingIsSkipped(t *testing.T) {
	r := newNetRig(t, nil, nil)
	r.dk.refuseSubnets = []string{"10.213.0.0/24"}
	res, dr := r.deploy(basePayload(), nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, "10.213.1.0/24", r.dk.nets[netEnv].subnet)
}

func TestReconcileAttachesRunningReplicasToTheDesiredNetworks(t *testing.T) {
	r := newNetRig(t, nil, nil)
	p := basePayload()
	res, dr := r.deploy(p, nil)
	require.Equal(t, agentprotocol.OutcomeSucceeded, res.Outcome, dr.Message)
	require.Equal(t, []string{netEnv}, r.dk.networksOf(newCtr))

	// Another service of the environment started blocking egress.
	desired := desiredFor(p, 1)
	desired.Services[0].EnvHasEgressBlocked = true
	require.NoError(t, r.eng.Reconcile(context.Background(), desired))

	require.Equal(t, []string{netEnv, netInt}, r.dk.networksOf(newCtr))
	require.Equal(t, []string{netInt + "<-" + newCtr}, r.dk.connects)
	require.True(t, r.dk.nets[netInt].internal)
	c, _ := r.dk.state(newCtr)
	require.Equal(t, []string{"web"}, c.Networks[netInt].Aliases, "the same alias on the internal network")
}
