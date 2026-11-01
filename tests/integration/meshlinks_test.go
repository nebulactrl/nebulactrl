//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agent/proxy"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Cross-server service links over a
// real Headscale tailnet. A consumer app on host2 reaches providers on agent1's
// daemon at `<provider slug>:<meshPort>`; the grants, the link node, the ACL and
// the aliases are all real. The phases run inside TestMeshCrossServer (one mesh
// stack, built once: it takes minutes to bring up).

// deployStagedWait deploys the staged services of an environment (all when ids is
// empty) and waits for every deployment; a consumer's deploy waits for its mesh
// link (up to five minutes), so the timeout is generous.
func (m *meshStack) deployStagedWait(env string, ids ...string) {
	t := m.T
	body := map[string]any{}
	if len(ids) > 0 {
		body["serviceIds"] = ids
	}
	r := m.Do("POST", "/environments/"+env+"/deploy-staged", body)
	require.Equal(t, http.StatusAccepted, r.Status, string(r.Raw))
	for _, it := range r.JSON().Items() {
		d := m.WaitDeployment(it.Str("id"), 12*time.Minute)
		require.Equal(t, "succeeded", d.Str("status"), "deployment: %v\nlogs:\n%s", d, m.DeploymentLogs(d.Str("id")))
	}
}

// planMesh returns the `mesh` section of a deployment plan.
func (m *meshStack) planMesh(serviceID string) J {
	t := m.T
	r := m.Do("POST", "/services/"+serviceID+"/deployments/plan", map[string]any{})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	return r.JSON().Obj("mesh")
}

// serviceExposure returns the single service-purpose exposure of a service.
func (m *meshStack) serviceExposure(serviceID string) meshExposureRow {
	t := m.T
	var got []meshExposureRow
	for _, e := range m.exposures(t, serviceID) {
		if e.Purpose == agentprotocol.MeshPurposeService {
			got = append(got, e)
		}
	}
	require.Lenf(t, got, 1, "service %s: want exactly one service exposure, have %+v", serviceID, m.exposures(t, serviceID))
	return got[0]
}

// containerLogs returns the log of a service's container on a host.
func (m *meshStack) containerLogs(h dockerHost, serviceID string) string {
	c := h.waitContainer(m.T, serviceID)
	out, _ := h.docker("logs", c)
	return out
}

// sortedCopy returns a sorted copy.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// phaseServiceLinks: apps on one server reach a service on another over the mesh.
func (m *meshStack) phaseServiceLinks(t *testing.T) {
	env := m.EnvID
	ctx := context.Background()
	node1 := m.serverNodeIP(t, m.Srv1)

	// Two providers on agent1's daemon that listen on the SAME port: the first keeps
	// the container port on the server node, the second gets a port from 15000-15999.
	mkWhoami := func(name, envID string) Service {
		return m.NewService(SvcSpec{Name: name, ServerID: m.Srv1, Env: envID, Image: "traefik/whoami:latest", Port: 80, Health: map[string]any{"type": "tcp", "deadlineSeconds": 180}})
	}
	who1, who2 := mkWhoami("whoami", env), mkWhoami("whoami2", env)
	for _, w := range []Service{who1, who2} {
		d := m.WaitDeployment(m.Deploy(w.ID), 5*time.Minute)
		require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, m.DeploymentLogs(d.Str("id")))
	}
	// traefik/whoami prints the container's hostname, which the agent sets to the service
	// slug: `Hostname: whoami` and `Hostname: whoami2` tell the two providers apart.
	who1ID, who2ID := who1.Slug+"\n", who2.Slug+"\n"

	// The consumer lives on host2 and references both providers.
	up := fmt.Sprintf("${{%s.NEBULA_PRIVATE_HOST}}:${{%s.PORT}},${{%s.NEBULA_PRIVATE_HOST}}:${{%s.PORT}}", who1.Slug, who1.Slug, who2.Slug, who2.Slug)
	host := m.Host("consumer")
	api := m.NewService(SvcSpec{
		Name: "consumer", ServerID: m.Srv2, Image: m.H2App, Domain: host,
		Vars: map[string]string{"VERSION": "c1", "UP": up, "SECRET_ECHO": "UP"},
	})
	c := host2Client(host)

	// The plan says what the deploy adds, and nothing exists before the deploy: a
	// reference alone never adds a device.
	pm := m.planMesh(api.ID)
	require.Equal(t, 1, pm.Int("devicesAdded"), "one service link node (host2 -> agent1): %v", pm)
	require.Empty(t, pm["blockers"], "mesh is enabled on both servers and both have /dev/net/tun: %v", pm)
	require.Empty(t, m.links(t, env), "planning creates no link")
	require.Zero(t, m.grantCount(t, api.ID))

	// Deploying the consumer is the confirmation: it adds the link and waits for it.
	m.deployStagedWait(env, api.ID)
	waitConsumer(t, c, "/", "c1", 90*time.Second)
	links := m.waitLinks(env, agentprotocol.MeshPurposeService, 1, 3*time.Minute)
	svcLink := links[0]
	require.Equal(t, m.Srv2, svcLink.Server, "the link node runs on the consumer's server")
	require.Equal(t, m.Srv1, svcLink.Remote)
	require.NotEqual(t, node1, svcLink.ipv4(), "a link node is its own tailnet device")

	// Ports: the first provider keeps the container port, the second collides and
	// gets a port from the mesh range; the variable follows.
	e1, e2 := m.serviceExposure(who1.ID), m.serviceExposure(who2.ID)
	require.Equal(t, 80, e1.MeshPort)
	require.GreaterOrEqual(t, e2.MeshPort, agentprotocol.MeshPortMin)
	require.LessOrEqual(t, e2.MeshPort, agentprotocol.MeshPortMax)
	require.Equal(t, 80, e2.ContainerPort)
	wantUP := fmt.Sprintf("%s:80,%s:%d", who1.Slug, who2.Slug, e2.MeshPort)
	require.Truef(t, eventually(30*time.Second, time.Second, func() bool {
		return strings.Contains(m.containerLogs(m.Host2, api.ID), "startup: UP="+wantUP)
	}), "the consumer's UP must resolve to %s:\n%s", wantUP, m.containerLogs(m.Host2, api.ID))

	// The traffic really goes through the mesh: each alias reaches ITS provider.
	b1 := waitFetch(t, c, "http://"+who1.Slug+":80/", "Hostname:", 90*time.Second)
	require.Contains(t, b1, "Hostname: "+who1ID)
	b2 := waitFetch(t, c, fmt.Sprintf("http://%s:%d/", who2.Slug, e2.MeshPort), "Hostname:", 30*time.Second)
	require.Contains(t, b2, "Hostname: "+who2ID)
	// Only the granted ports: another port of the same provider is closed.
	requireFetchFails(t, c, "http://"+who1.Slug+":81/")

	// The ACL: one rule per link, source = the link's IP, destinations = exactly the two
	// granted (server node IP : mesh port) pairs, and nothing else.
	want := []string{fmt.Sprintf("%s:80", node1), fmt.Sprintf("%s:%d", node1, e2.MeshPort)}
	pol := m.waitPolicy("service link rule", func(p hsPolicy) bool {
		return strings.Join(sortedCopy(p.dsts(svcLink.ipv4())), ",") == strings.Join(sortedCopy(want), ",")
	})
	for _, r := range pol.ACLs {
		require.Equal(t, "accept", r.Action)
	}
	require.Empty(t, pol.dsts(node1), "no rule lets a server node reach anything: replication and service traffic come from link nodes")

	// The link node: kernel tailscale in its own netns with exactly NET_ADMIN/NET_RAW and
	// the tun device (nothing else), never the host's network, and the provider slugs as
	// aliases on the environment network.
	ln := agentprotocol.MeshLinkContainer(svcLink.ID)
	require.Equal(t, "true", m.Host2.inspect(t, ln, "{{.State.Running}}"))
	require.NotEqual(t, "host", m.Host2.inspect(t, ln, "{{.HostConfig.NetworkMode}}"))
	require.Equal(t, "false", m.Host2.inspect(t, ln, "{{.HostConfig.Privileged}}"))
	require.Contains(t, m.Host2.inspect(t, ln, "{{json .HostConfig.CapAdd}}"), "NET_ADMIN")
	nets := m.Host2.inspect(t, ln, "{{json .NetworkSettings.Networks}}")
	require.Contains(t, nets, `"`+who1.Slug+`"`)
	require.Contains(t, nets, `"`+who2.Slug+`"`)
	sn := agentprotocol.MeshServerContainer(m.Srv1)
	require.Empty(t, strings.Trim(m.Local.inspect(t, sn, "{{json .HostConfig.CapAdd}}"), "nul[]"), "the server node has no added capability")
	require.Contains(t, m.Local.inspect(t, sn, "{{json .HostConfig.CapDrop}}"), "ALL")
	// Three devices so far: two server nodes and one link node, all tagged.
	hsn := m.HS.nodes(t)
	require.Len(t, hsn, 3)
	for _, n := range hsn {
		require.Truef(t, n.hasTag(meshTag), "node %s is not tagged %s: %+v", n.Name, meshTag, n)
	}
	// The key is single-use: no key travelled in a payload.
	var keyed int
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_commands WHERE payload::text LIKE '%mesh:key:%'`).Scan(&keyed))
	require.Zero(t, keyed, "node keys travel in Command.Secrets only")

	// ---- egress: a consumer that blocks the internet still reaches its providers ----
	blockedHost := m.Host("blocked")
	blocked := m.NewService(SvcSpec{
		Name: "blocked", ServerID: m.Srv2, Image: m.H2App, Domain: blockedHost,
		Vars:   map[string]string{"VERSION": "b1", "UP": fmt.Sprintf("${{%s.NEBULA_PRIVATE_HOST}}:${{%s.PORT}}", who1.Slug, who1.Slug)},
		Config: map[string]any{"blockEgress": true},
	})
	m.deployStagedWait(env, blocked.ID)
	bc := host2Client(blockedHost)
	waitConsumer(t, bc, "/", "b1", 90*time.Second)
	waitFetch(t, bc, "http://"+who1.Slug+":80/", "Hostname: "+who1ID, 90*time.Second)
	requireFetchFails(t, bc, "http://1.1.1.1/")
	// The control: the consumer that does not block egress reaches the internet. A plain
	// HTTP 200 target (1.1.1.1 redirects to https, and the fixture image has no CA bundle).
	code, body, err := fetchVia(c, "http://example.com/")
	require.NoError(t, err)
	require.Truef(t, code == http.StatusOK && strings.HasPrefix(body, "ok 200"), "control: the open consumer must reach the internet, got %d %q", code, body)
	// Still one service link: the second consumer reused it.
	require.Len(t, m.links(t, env), 1)

	// ---- another environment on host2 cannot use the link -----------------------------
	env2 := m.newEnv("other")
	intruderHost := m.Host("intruder")
	intruder := m.NewService(SvcSpec{Name: "intruder", ServerID: m.Srv2, Env: env2, Image: m.H2App, Domain: intruderHost, Vars: map[string]string{"VERSION": "i1"}})
	d := m.WaitDeployment(m.Deploy(intruder.ID), 5*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, m.DeploymentLogs(d.Str("id")))
	ic := host2Client(intruderHost)
	m.intruder = ic
	waitConsumer(t, ic, "/", "i1", 90*time.Second)
	// The alias exists only on the link's environment networks.
	require.Contains(t, requireFetchFails(t, ic, "http://"+who1.Slug+":80/"), "no such host")
	require.Contains(t, requireFetchFails(t, ic, fmt.Sprintf("http://%s:%d/", who2.Slug, e2.MeshPort)), "no such host")
	// No route to a tailnet address either.
	require.NotContains(t, requireFetchFails(t, ic, fmt.Sprintf("http://%s:80/", node1)), "Hostname")
	require.NotContains(t, requireFetchFails(t, ic, "http://"+agentprotocol.MeshReplicationAlias(m.Srv1)+":80/"), "Hostname")

	// A link node of the OTHER environment that somebody made by hand joins the tailnet with
	// a tagged key and DNATs to the same server node: the ACL has no rule for its IP.
	m.handMadeLink(t, env2, node1, ic)
	require.Len(t, m.links(t, env2), 0, "nothing NebulaCtrl-managed exists in the other environment")

	// ---- a variable edit alone never adds a device -----------------------------------------
	who3 := mkWhoami("whoami3", env2)
	d = m.WaitDeployment(m.Deploy(who3.ID), 5*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, m.DeploymentLogs(d.Str("id")))
	m.SetVars(intruder.ID, map[string]string{"UP": "${{" + who3.Slug + ".NEBULA_PRIVATE_HOST}}:${{" + who3.Slug + ".PORT}}"}, nil)
	pm = m.planMesh(intruder.ID)
	require.Equal(t, 1, pm.Int("devicesAdded"), "the plan of the consumer states the device it would add: %v", pm)
	time.Sleep(15 * time.Second)
	require.Empty(t, m.links(t, env2), "a variable edit is not an explicit action: no link, no device")
	require.Equal(t, 3, len(m.HS.nodes(t))-countHandMade(m.HS.nodes(t)), "still two server nodes and one link node")

	// A canvas edge that would add a device needs the explicit confirmation.
	edge := map[string]any{"fromServiceId": intruder.ID, "toServiceId": who3.ID}
	r := m.Do("POST", "/environments/"+env2+"/links", edge)
	require.GreaterOrEqual(t, r.Status, 400, string(r.Raw))
	require.Contains(t, string(r.Raw), "mesh_confirmation_required")
	require.Empty(t, m.links(t, env2))
	edge["acceptMeshChanges"] = true
	r = m.Do("POST", "/environments/"+env2+"/links", edge)
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	edgeID := r.JSON().Obj("link").Str("id")
	env2Links := m.waitLinks(env2, agentprotocol.MeshPurposeService, 1, 4*time.Minute)
	require.Equal(t, 1, m.grantCount(t, intruder.ID), "a canvas edge alone creates the grant")
	var src string
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT source FROM mesh_grants WHERE ref_id::text = $1`, intruder.ID).Scan(&src))
	require.Equal(t, "edge", src)
	// ... and the intruder now reaches ITS provider (whoami3 on the same server pair), not the other env's.
	e3 := m.serviceExposure(who3.ID)
	waitFetch(t, ic, fmt.Sprintf("http://%s:%d/", who3.Slug, e3.MeshPort), "Hostname:", 90*time.Second)
	require.Contains(t, requireFetchFails(t, ic, "http://"+who1.Slug+":80/"), "no such host", "the other environment's providers stay invisible")
	require.NotEqual(t, svcLink.ID, env2Links[0].ID, "links are per environment")
	require.NotEqual(t, svcLink.ipv4(), env2Links[0].ipv4())
	// Removing the edge removes the grant at once (not after a grace period) and the rule goes.
	r = m.Do("DELETE", "/links/"+edgeID, nil)
	require.Contains(t, []int{http.StatusOK, http.StatusNoContent}, r.Status, string(r.Raw))
	require.Truef(t, eventually(2*time.Minute, 2*time.Second, func() bool { return m.grantCount(t, intruder.ID) == 0 }), "the edge grant was not removed")

	// ---- removing a reference removes the grant after the grace period ----------------------
	m.SetVars(api.ID, map[string]string{"UP": fmt.Sprintf("${{%s.NEBULA_PRIVATE_HOST}}:${{%s.PORT}}", who1.Slug, who1.Slug)}, nil)
	m.deployStagedWait(env, api.ID)
	var gone bool
	// The reference grace comes from the harness (NEBULACTRL_MESH_LINK_GRACE, 30 s here, ten
	// minutes in production; in memory): the loop asks for `repair` (the same code path as the
	// 5-minute job) every 5 s, which is far below the grace, until the grant goes.
	require.Truef(t, eventually(3*time.Minute, 5*time.Second, func() bool {
		_ = m.Do("POST", "/environments/"+env+"/mesh/reconcile", map[string]any{"mode": "repair"})
		var n int
		_ = m.Pool.QueryRow(ctx, `SELECT count(*) FROM mesh_grants g JOIN mesh_exposures e ON e.id = g.exposure_id WHERE e.service_id::text = $1 AND g.source = 'reference'`, who2.ID).Scan(&n)
		gone = n == 0
		return gone
	}), "the grant of the removed reference was never removed")
	m.waitPolicy("rule without whoami2", func(p hsPolicy) bool {
		return strings.Join(p.dsts(svcLink.ipv4()), ",") == fmt.Sprintf("%s:80", node1)
	})
	// The next connectivity check is a timeout or a failed name: the path is closed, the other provider still works.
	require.Truef(t, eventually(2*time.Minute, 3*time.Second, func() bool {
		code, _, err := fetchVia(c, fmt.Sprintf("http://%s:%d/", who2.Slug, e2.MeshPort))
		return err == nil && code == http.StatusInternalServerError
	}), "whoami2 stayed reachable after its grant was removed")
	waitFetch(t, c, "http://"+who1.Slug+":80/", "Hostname: "+who1ID, 30*time.Second)

	// ---- moving a provider to the consumer's server releases its exposure ----------------------
	// Put the reference back (and the edge is gone), move whoami2 to host2, deploy it there.
	m.SetVars(api.ID, map[string]string{"UP": up}, nil)
	m.deployStagedWait(env, api.ID)
	waitFetch(t, c, fmt.Sprintf("http://%s:%d/", who2.Slug, m.serviceExposure(who2.ID).MeshPort), "Hostname: "+who2ID, 120*time.Second)
	mv := m.Do("PATCH", "/services/"+who2.ID, map[string]any{"serverId": m.Srv2}, "If-Match", m.serviceETag(who2.ID))
	require.Equal(t, http.StatusOK, mv.Status, string(mv.Raw))
	// (the consumer's restage is asserted after the deploy below)
	d = m.WaitDeployment(m.Deploy(who2.ID), 6*time.Minute)
	require.Equal(t, "succeeded", d.Str("status"), "%v\n%s", d, m.DeploymentLogs(d.Str("id")))
	require.Equal(t, m.Srv2, d.Str("serverId"))
	require.Truef(t, eventually(2*time.Minute, 2*time.Second, func() bool {
		for _, e := range m.exposures(t, who2.ID) {
			if e.Purpose == agentprotocol.MeshPurposeService {
				return false
			}
		}
		return true
	}), "the exposure of the moved provider was not released")
	require.True(t, m.Service(api.ID).Bool("hasStagedChanges"), "the consumer of a provider that moved is restaged")
	m.deployStagedWait(env, api.ID)
	// Same server now: the plain port, no mesh.
	waitFetch(t, c, "http://"+who2.Slug+":80/", "Hostname:", 120*time.Second)
	require.Truef(t, strings.Contains(m.containerLogs(m.Host2, api.ID), fmt.Sprintf("startup: UP=%s:80,%s:80", who1.Slug, who2.Slug)),
		"UP must follow the move to plain ports:\n%s", m.containerLogs(m.Host2, api.ID))
}

// countHandMade counts the nodes the test joined by hand.
func countHandMade(nodes []hsNode) int {
	n := 0
	for _, x := range nodes {
		if strings.HasPrefix(x.Name, "handlink") || strings.HasPrefix(x.GivenName, "handlink") {
			n++
		}
	}
	return n
}

// handMadeLink starts a kernel tailscale container on host2 that joins the tailnet
// with a tagged key, attaches to the environment's network under the alias
// `handlink` and DNATs everything to targetIP (the same shape as a NebulaCtrl link
// node). The ACL has no rule for its IP, so the consumer in that environment must
// not get through: this is the "denied by the ACL" half of environment isolation.
func (m *meshStack) handMadeLink(t *testing.T, envID, targetIP string, consumer *ProxyClient) {
	t.Helper()
	code, raw, err := m.HS.api("POST", "/preauthkey", map[string]any{
		"aclTags": []string{meshTag}, "reusable": false, "ephemeral": true,
		"expiration": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, string(raw))
	key := J{}
	require.NoError(t, json.Unmarshal(raw, &key))
	authKey := key.Obj("preAuthKey").Str("key")
	require.NotEmpty(t, authKey, string(raw))

	netName := agentprotocol.EnvNetwork(envID)
	name := "nebulactrl-itest-handlink"
	_, _ = m.Host2.docker("rm", "-f", name)
	m.Host2.mustDocker(t, "run", "-d", "--name", name, "--network", netName, "--network-alias", "handlink",
		"--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", "--device", "/dev/net/tun", "--sysctl", "net.ipv4.ip_forward=1",
		"-e", "TS_AUTHKEY="+authKey, "-e", "TS_HOSTNAME=handlink", "-e", "TS_STATE_DIR=/tmp/ts", "-e", "TS_USERSPACE=false",
		"-e", "TS_ACCEPT_DNS=false", "-e", "TS_TAILNET_TARGET_IP="+targetIP, "-e", "TS_EXTRA_ARGS=--login-server="+m.HS.LoginURL,
		proxy.TailscaleImage)
	t.Cleanup(func() { _, _ = m.Host2.docker("rm", "-f", name) })
	var hand hsNode
	require.Truef(t, eventually(3*time.Minute, 3*time.Second, func() bool {
		for _, n := range m.HS.nodes(t) {
			if (strings.HasPrefix(n.Name, "handlink") || strings.HasPrefix(n.GivenName, "handlink")) && n.Online && n.ipv4() != "" {
				hand = n
				return true
			}
		}
		return false
	}), "the hand-made link node never came online:\n%s", mustShLoose("docker", "exec", host2Name, "docker", "logs", "--tail", "30", name))
	pol := m.HS.policy(t)
	require.Empty(t, pol.dsts(hand.ipv4()), "NebulaCtrl's policy has no rule for a link node it did not create: %s", pol.Raw)
	// Through the hand-made link: denied (default deny), not merely "not tried".
	for range 3 {
		body := requireFetchFails(t, consumer, "http://handlink:80/")
		require.NotContains(t, body, "Hostname", "the ACL must deny the hand-made link")
	}
}
