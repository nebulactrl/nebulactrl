//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The mesh tests: the mesh and Postgres read replicas across two servers over a
// REAL tailnet.
//
//	agent1 (in-process, the local daemon)  --- Headscale (container) ---  agent2 (host2, docker-in-docker)
//
// Needs NEBULACTRL_TEST_MULTI=1 and NEBULACTRL_TEST_MESH=1. The mesh stack takes
// minutes to bring up, so one test builds it once and runs the phases as
// subtests, cheapest and least destructive first; the last phases stop agent1
// and revoke its server.
//
// What this machine cannot show: both "servers"
// share one Docker VM, so the tunnel is always a direct path on one bridge and
// DERP fallback, NAT traversal, WAN latency/MTU and hosts with ufw/firewalld are
// not exercised; Tailscale SaaS (OAuth, ACL API, tag ownership, device quota) is
// not involved; Headscale runs without TLS; link nodes inside docker-in-docker
// need /dev/net/tun (the fixture creates it and fails with the node log when
// kernel tailscale cannot start); clone time of a large database is not measured.
func TestMeshCrossServer(t *testing.T) {
	m := newMeshStack(t)

	m.phase(t, "nodes_and_acl", m.phaseNodes)
	m.phase(t, "service_links", m.phaseServiceLinks)
	m.phase(t, "replication", m.phaseReplication)
	m.phase(t, "grant_removed", m.phaseGrantRemoved)
	m.phase(t, "switchover_consumers_follow", m.phaseSwitchoverFollow)
	m.phase(t, "forced_promote", m.phaseForcedPromote)
	m.phase(t, "revoke_server", m.phaseRevoke)
}

// ---- phase: server nodes, managed ACL ------------------------------------------

func (m *meshStack) phaseNodes(t *testing.T) {
	ctx := context.Background()
	// The tailnet: two tagged server nodes, online, with tailnet IPs; and nothing else yet.
	require.Truef(t, eventually(2*time.Minute, 2*time.Second, func() bool {
		return m.Do("GET", "/mesh", nil).JSON().Str("aclState") == "applied"
	}), "the managed ACL was not applied: %s", m.Do("GET", "/mesh", nil).Raw)
	view := m.Do("GET", "/mesh", nil).JSON()
	require.True(t, view.Bool("enabled"))
	require.Equal(t, "managed", view.Str("aclMode"))
	require.False(t, view.Bool("paused"))
	require.Equal(t, "headscale", view.Str("provider"))

	nodes := m.HS.nodes(t)
	require.Len(t, nodes, 2, "one server node per server and no link node before a link is needed")
	for _, n := range nodes {
		require.True(t, n.Online, "%+v", n)
		require.Truef(t, n.hasTag(meshTag), "node %s must carry the mesh tag: %+v", n.Name, n)
		require.NotEmpty(t, n.ipv4())
	}
	ip1, ip2 := m.serverNodeIP(t, m.Srv1), m.serverNodeIP(t, m.Srv2)
	require.NotEqual(t, ip1, ip2)
	require.ElementsMatch(t, []string{ip1, ip2}, []string{nodes[0].ipv4(), nodes[1].ipv4()}, "the control plane's view matches the tailnet's")

	// Default deny: the policy is explicit, carries the mesh tag owner and no rule yet
	// (replication and service rules appear with their first grant).
	pol := m.HS.policy(t)
	require.Contains(t, pol.Raw, `"acls"`)
	require.Contains(t, pol.TagOwners, meshTag)
	require.Empty(t, pol.dsts(ip1))
	require.Empty(t, pol.dsts(ip2))

	// The nodes are containers on each daemon: userspace, no capability, not the host network.
	for _, tc := range []struct {
		h  dockerHost
		id string
	}{{m.Local, m.Srv1}, {m.Host2, m.Srv2}} {
		name := agentprotocol.MeshServerContainer(tc.id)
		require.Equal(t, "true", tc.h.inspect(t, name, "{{.State.Running}}"), "%s on %s", name, tc.h.name)
		require.NotEqual(t, "host", tc.h.inspect(t, name, "{{.HostConfig.NetworkMode}}"))
		require.Equal(t, "false", tc.h.inspect(t, name, "{{.HostConfig.Privileged}}"))
		require.Contains(t, tc.h.inspect(t, name, "{{json .HostConfig.CapDrop}}"), "ALL")
		require.Contains(t, tc.h.inspect(t, name, "{{.Config.Env}}"), "TS_USERSPACE=true")
	}

	// A key is minted only when a node is provisioned and never stored or logged.
	var keyed int
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_commands WHERE payload::text LIKE '%mesh:key:%' OR result::text LIKE '%mesh:key:%'`).Scan(&keyed))
	require.Zero(t, keyed)
	var audited int
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action IN ('mesh.enable', 'mesh.node.provision')`).Scan(&audited))
	require.GreaterOrEqual(t, audited, 2)

	// Default deny reaches the netmap: with no rule, Headscale shows the server nodes no peers
	// (replication and service traffic come from link nodes, never between server nodes), and
	// the connectivity check from agent1 to agent2 does not connect.
	require.Empty(t, m.meshNode(m.Srv1)["peers"], "no rule, no visible peer: %v", m.meshNode(m.Srv1))
	chk := m.Do("POST", "/servers/"+m.Srv1+"/mesh/check", map[string]any{"peerServerId": m.Srv2})
	require.Equal(t, http.StatusOK, chk.Status, string(chk.Raw))
	require.NotContains(t, string(chk.Raw), agentprotocol.MeshCheckConnected, "the ACL has no rule between server nodes")

	// The advisory snippet and the preview are derived from the same state.
	pv := m.Do("POST", "/mesh/acl/preview", map[string]any{})
	require.Equal(t, http.StatusOK, pv.Status, string(pv.Raw))
}

// ---- phase: Postgres replica across servers ---------------------------------------

// replicationExposure is the replication-purpose exposure of a primary.
func (m *meshStack) replicationExposure(t *testing.T, primaryID string) meshExposureRow {
	t.Helper()
	var got []meshExposureRow
	for _, e := range m.exposures(t, primaryID) {
		if e.Purpose == agentprotocol.MeshPurposeReplication {
			got = append(got, e)
		}
	}
	require.Lenf(t, got, 1, "primary %s: want one replication exposure, have %+v", primaryID, m.exposures(t, primaryID))
	return got[0]
}

// hostNetworksInternal lists a host2 container's networks and whether each is internal.
func (m *meshStack) hostNetworksInternal(t *testing.T, h dockerHost, container string) map[string]bool {
	t.Helper()
	names := strings.Fields(h.inspect(t, container, `{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}`))
	out := map[string]bool{}
	for _, n := range names {
		out[n] = strings.TrimSpace(h.mustDocker(t, "network", "inspect", "-f", "{{.Internal}}", n)) == "true"
	}
	return out
}

func (m *meshStack) phaseReplication(t *testing.T) {
	require.NotNil(t, m.intruder, "the service_links phase provides the other-environment client")
	ctx := context.Background()
	env := m.EnvID
	node1 := m.serverNodeIP(t, m.Srv1)

	pga := m.createDB("pga", "postgres", "17")
	m.deployStagedWait(env, pga.ID)
	waitPostgres(t, m.Stack, pga.ID)
	pgExec(t, m.Stack, pga.ID, "CREATE TABLE notes (id int primary key, body text); INSERT INTO notes SELECT g, 'row ' || g FROM generate_series(1, 2000) g")

	// Without the explicit confirmation a replica on another server does not add a device.
	nodev := m.addReplicaRaw(pga.ID, map[string]any{"name": "pga-r", "serverId": m.Srv2})
	require.GreaterOrEqual(t, nodev.Status, 400, string(nodev.Raw))
	require.Contains(t, string(nodev.Raw), "mesh_confirmation_required")
	require.Nil(t, m.replicaByName(pga.ID, "pga-r"))
	require.Empty(t, m.replicationLinks(t))
	plan := m.addReplicaRaw(pga.ID, map[string]any{"name": "pga-r", "serverId": m.Srv2, "dryRun": true})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	require.NotContains(t, string(plan.Raw), "mesh_server_required", "mesh is enabled on both servers")

	rep := m.addReplica(pga, m.Srv2, "pga-r", map[string]any{"acceptMeshChanges": true})
	it := m.waitReplica(pga.ID, rep.ID, 12*time.Minute, replStreaming)
	require.Equal(t, m.Srv2, it.Str("serverId"))

	// It really is a standby on host2, fed through the mesh alias and the exposure's port.
	require.Equal(t, "t", m.Host2.psql(t, rep.ID, "SELECT pg_is_in_recovery()"))
	require.Equal(t, "f", m.Local.psql(t, pga.ID, "SELECT pg_is_in_recovery()"))
	ex := m.replicationExposure(t, pga.ID)
	require.Equal(t, 5432, ex.ContainerPort)
	require.GreaterOrEqual(t, ex.MeshPort, agentprotocol.MeshPortMin)
	require.LessOrEqual(t, ex.MeshPort, agentprotocol.MeshPortMax)
	require.Equal(t, m.Srv1, ex.ServerID)
	alias := agentprotocol.MeshReplicationAlias(m.Srv1)
	require.Equal(t, fmt.Sprintf("%s:%d", alias, ex.MeshPort), m.Host2.psql(t, rep.ID, "SELECT sender_host || ':' || sender_port FROM pg_stat_wal_receiver"),
		"the WAL receiver must dial the replication link's alias and the exposure's mesh port")
	require.Equal(t, "2000", m.Host2.psql(t, rep.ID, "SELECT count(*) FROM notes"))
	pgExec(t, m.Stack, pga.ID, "INSERT INTO notes SELECT g, 'more ' || g FROM generate_series(2001, 2100) g")
	require.Truef(t, eventually(60*time.Second, time.Second, func() bool {
		out, err := m.Host2.tryPsql(rep.ID, "SELECT count(*) FROM notes")
		return err == nil && out == "2100"
	}), "writes on agent1 did not reach the replica on host2")
	slot, _ := m.replicaIdentity(t, rep.ID)
	require.Equal(t, "1", pgExec(t, m.Stack, pga.ID, "SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming' AND application_name = '"+slot+"'"))
	// Lag is observed across servers too.
	require.Truef(t, eventually(90*time.Second, 2*time.Second, func() bool {
		it = m.replicaItem(pga.ID, rep.ID)
		_, ok := it["lagBytes"]
		return ok && it.Str("observedAt") != ""
	}), "no lag observed: %v", it)

	// One replication link (host2 -> agent1), one grant, one rule: link IP -> server node IP : mesh port only.
	links := m.replicationLinks(t)
	require.Len(t, links, 1)
	rl := links[0]
	require.Equal(t, m.Srv2, rl.Server)
	require.Equal(t, m.Srv1, rl.Remote)
	require.Equal(t, agentprotocol.MeshStateOnline, rl.State)
	require.Equal(t, 1, m.grantCount(t, rep.ID))
	pol := m.waitPolicy("replication rule", func(p hsPolicy) bool {
		return strings.Join(p.dsts(rl.ipv4()), ",") == fmt.Sprintf("%s:%d", node1, ex.MeshPort)
	})
	require.Empty(t, pol.dsts(node1))

	// Where a service link of the same environment exists (service_links phase), the two
	// purposes are two nodes with two source IPs, and the service link cannot reach the
	// replication port.
	for _, l := range m.links(t, env) {
		if l.Purpose != agentprotocol.MeshPurposeService {
			continue
		}
		require.NotEqual(t, rl.ID, l.ID)
		require.NotEqual(t, rl.ipv4(), l.ipv4(), "replication and service links are separate devices")
		require.NotContains(t, pol.dsts(l.ipv4()), fmt.Sprintf("%s:%d", node1, ex.MeshPort), "a service link must not reach a replication port")
		require.NotContains(t, pol.dsts(rl.ipv4()), fmt.Sprintf("%s:5432", node1), "a replication link must not reach a service port")
	}

	// The replica has no internet: every network of the container is internal.
	rc := m.Host2.waitContainer(t, rep.ID)
	for name, internal := range m.hostNetworksInternal(t, m.Host2, rc) {
		require.Truef(t, internal, "replica on host2 is attached to non-internal network %s", name)
	}
	// ... yet it reached the primary through the link node on that internal network.
	require.Contains(t, m.Host2.inspect(t, agentprotocol.MeshLinkContainer(rl.ID), "{{json .NetworkSettings.Networks}}"), alias)

	// Another environment on host2 neither resolves the alias nor routes to the exposure.
	require.Contains(t, requireFetchFails(t, m.intruder, fmt.Sprintf("http://%s:%d/", alias, ex.MeshPort)), "no such host")
	body := requireFetchFails(t, m.intruder, fmt.Sprintf("http://%s:%d/", node1, ex.MeshPort))
	require.NotContains(t, body, "malformed", "the exposure must not be reachable from another environment")

	// The replication password stays out of payloads, rows and logs.
	pw := strings.TrimSpace(m.Host2.mustDocker(t, "inspect", "-f", `{{range .Config.Env}}{{println .}}{{end}}`, rc))
	var repw string
	for _, line := range strings.Split(pw, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "POSTGRES_REPLICATION_PASSWORD="); ok {
			repw = v
		}
	}
	require.Len(t, repw, 32)
	var leaked int
	require.NoError(t, m.Pool.QueryRow(ctx,
		"SELECT count(*) FROM agent_commands WHERE payload::text LIKE '%' || $1 || '%' OR result::text LIKE '%' || $1 || '%'", repw).Scan(&leaked))
	require.Zero(t, leaked)
	require.Zero(t, strings.Count(m.dumpAllTables("sessions", "audit_events", "service_variables", "variable_versions", "database_rotations"), repw))

	// An app on host2 reads the local replica and writes through the mesh to the primary on agent1.
	rapiHost := m.Host("rapi")
	rapi := m.NewService(SvcSpec{
		Name: "rapi", ServerID: m.Srv2, Image: m.H2App, Domain: rapiHost,
		Vars: map[string]string{
			"VERSION":      "r1",
			"DATABASE_URL": "${{" + pga.Slug + ".DATABASE_WRITE_URL}}",
			"RO_URL":       "${{" + pga.Slug + ".DATABASE_READ_URL}}",
		},
	})
	m.deployStagedWait(env, rapi.ID)
	rc2 := host2Client(rapiHost)
	waitConsumer(t, rc2, "/", "r1", 90*time.Second)
	waitPgq(t, rc2, "RO_URL", "select pg_is_in_recovery()::text", "true", 60*time.Second)
	waitPgq(t, rc2, "DATABASE_URL", "select pg_is_in_recovery()::text", "false", 60*time.Second)
	waitPgq(t, rc2, "DATABASE_URL", "INSERT INTO notes VALUES (9001, 'through the mesh') RETURNING 'ok'::text", "ok", 60*time.Second)
	require.Equal(t, "1", pgExec(t, m.Stack, pga.ID, "SELECT count(*) FROM notes WHERE id = 9001"), "the write reached the primary on agent1")
	require.Truef(t, eventually(60*time.Second, time.Second, func() bool {
		out, err := m.Host2.tryPsql(rep.ID, "SELECT count(*) FROM notes WHERE id = 9001")
		return err == nil && out == "1"
	}), "and replicated back to the replica on host2")
	_, _, err := pgq(rc2, "RO_URL", "INSERT INTO notes VALUES (9002, 'nope') RETURNING 'ok'::text")
	require.NoError(t, err)
	require.Equal(t, "0", pgExec(t, m.Stack, pga.ID, "SELECT count(*) FROM notes WHERE id = 9002"))
}

// replicationLinks returns the replication-purpose links of the stack's environment.
func (m *meshStack) replicationLinks(t *testing.T) []meshLinkRow {
	t.Helper()
	var out []meshLinkRow
	for _, l := range m.links(t, m.EnvID) {
		if l.Purpose == agentprotocol.MeshPurposeReplication {
			out = append(out, l)
		}
	}
	return out
}

// ---- phase: the grant is removed -----------------------------------------------------

// With the grant gone and the ACL re-applied, a NEW connection through the link is
// denied (Tailscale's filter only checks new connections, so an established WAL
// stream is not cut: the replica container is restarted to force a reconnect),
// and the replica goes broken instead of staying "streaming".
func (m *meshStack) phaseGrantRemoved(t *testing.T) {
	ctx := context.Background()
	env := m.EnvID
	node1 := m.serverNodeIP(t, m.Srv1)
	pgb := m.createDB("pgb", "postgres", "17")
	m.deployStagedWait(env, pgb.ID)
	waitPostgres(t, m.Stack, pgb.ID)
	pgExec(t, m.Stack, pgb.ID, "CREATE TABLE notes (id int primary key, body text); INSERT INTO notes SELECT g, 'row ' || g FROM generate_series(1, 500) g")
	rep := m.addReplica(pgb, m.Srv2, "pgb-r", map[string]any{"acceptMeshChanges": true})
	m.waitReplica(pgb.ID, rep.ID, 12*time.Minute, replStreaming)
	ex := m.replicationExposure(t, pgb.ID)
	alias := agentprotocol.MeshReplicationAlias(m.Srv1)
	slot, _ := m.replicaIdentity(t, rep.ID)
	rl := m.replicationLinks(t)
	require.Len(t, rl, 1, "both replicas of this stack share one replication link")
	dst := fmt.Sprintf("%s:%d", node1, ex.MeshPort)
	m.waitPolicy("rule for pgb", func(p hsPolicy) bool { return contains(p.dsts(rl[0].ipv4()), dst) })

	// Control: the connection works while the grant exists.
	rc := m.Host2.waitContainer(t, rep.ID)
	_, err := m.Host2.exec(rc, "pg_isready", "-h", alias, "-p", fmt.Sprint(ex.MeshPort), "-t", "5")
	require.NoError(t, err, "pg_isready through the link must succeed while the grant exists")

	// Remove the grant behind the control plane's back, then apply the ACL (what the job
	// does on every change): the rule for this port disappears.
	tag, err := m.Pool.Exec(ctx, `DELETE FROM mesh_grants WHERE ref_id::text = $1`, rep.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
	ap := m.Do("POST", "/mesh/acl/apply", map[string]any{})
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, ap.Status, string(ap.Raw))
	m.waitPolicy("rule for pgb removed", func(p hsPolicy) bool { return !contains(p.dsts(rl[0].ipv4()), dst) })
	require.Truef(t, eventually(60*time.Second, 2*time.Second, func() bool {
		_, err := m.Host2.exec(rc, "pg_isready", "-h", alias, "-p", fmt.Sprint(ex.MeshPort), "-t", "4")
		return err != nil
	}), "a new connection through the link must be denied once the grant is gone")

	// A restart forces the walreceiver to reconnect: it cannot, the primary loses the
	// standby, and the control plane says so.
	m.Host2.mustDocker(t, "restart", rc)
	require.Truef(t, eventually(3*time.Minute, 3*time.Second, func() bool {
		return pgExec(t, m.Stack, pgb.ID, "SELECT count(*) FROM pg_stat_replication WHERE application_name = '"+slot+"'") == "0"
	}), "the standby of pgb is still connected to the primary")
	// BrokenAfter is 60 s here (NEBULACTRL_REPLICA_BROKEN_AFTER; 2 minutes in production) and
	// the agent polls every 30 s.
	it := m.waitReplica(pgb.ID, rep.ID, 6*time.Minute, "broken")
	require.NotEmpty(t, it.Str("message"), "a broken replica says why")

	// The other replica is unaffected: its grant and rule are intact.
	var pgaRep string
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT r.service_id::text FROM database_replicas r JOIN services p ON p.id = r.primary_service_id WHERE p.slug = 'pga' AND p.environment_id::text = $1`, env).Scan(&pgaRep))
	var pgaID string
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT id::text FROM services WHERE slug = 'pga' AND environment_id::text = $1`, env).Scan(&pgaID))
	require.Equal(t, replStreaming, m.replicaItem(pgaID, pgaRep).Str("state"), "an unrelated replica keeps streaming")
}

// ---- promote helpers ------------------------------------------------------------------

// promote starts a Promote. The plan of a switchover that has to open a link says
// mesh_confirmation_required: the explicit action carries the confirmation.
func (m *meshStack) promote(replica Service, extra map[string]any) Resp {
	body := map[string]any{"confirm": replica.Slug}
	for k, v := range extra {
		body[k] = v
	}
	r := m.Do("POST", "/services/"+replica.ID+"/database/promote", body)
	if r.Status >= 400 && strings.Contains(string(r.Raw), "mesh_confirmation_required") {
		body["acceptMeshChanges"] = true
		r = m.Do("POST", "/services/"+replica.ID+"/database/promote", body)
	}
	return r
}

// waitPromotion waits for the latest promotion of a replica to finish.
func (m *meshStack) waitPromotion(replicaID string, timeout time.Duration) (status, step, msg string) {
	t := m.T
	require.Truef(t, eventually(timeout, 2*time.Second, func() bool {
		if err := m.Pool.QueryRow(context.Background(),
			`SELECT status, step, message FROM database_promotions WHERE replica_service_id::text = $1 ORDER BY started_at DESC LIMIT 1`, replicaID).Scan(&status, &step, &msg); err != nil {
			return false
		}
		return status != "running"
	}), "the promotion of %s did not finish within %s: %s/%s %s", replicaID, timeout, status, step, msg)
	return status, step, msg
}

// ---- phase: a switchover moves the primary across servers; consumers follow -------------------

func (m *meshStack) phaseSwitchoverFollow(t *testing.T) {
	ctx := context.Background()
	env := m.EnvID
	var pgaID, repID, pgaSlug, repSlug string
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT s.id::text, s.slug FROM services s WHERE s.environment_id::text = $1 AND s.slug = 'pga'`, env).Scan(&pgaID, &pgaSlug))
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT r.service_id::text, s.slug FROM database_replicas r JOIN services s ON s.id = r.service_id WHERE r.primary_service_id::text = $1`, pgaID).Scan(&repID, &repSlug))
	pga, rep := Service{ID: pgaID, Slug: pgaSlug}, Service{ID: repID, Slug: repSlug}

	// A consumer on agent1, next to the primary: no mesh involved yet.
	capi, cc := m.startApp("capi", m.Srv1, env, map[string]string{"DATABASE_URL": "${{" + pga.Slug + ".DATABASE_URL}}"}, nil)
	waitPgq(t, cc, "DATABASE_URL", "select pg_is_in_recovery()::text", "false", 60*time.Second)

	plan := m.Do("POST", "/services/"+rep.ID+"/database/promote", map[string]any{"dryRun": true})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	require.Equal(t, "switchover", plan.JSON().Obj("plan").Str("mode"), string(plan.Raw))
	require.Contains(t, string(plan.Raw), capi.Slug, "the plan lists the consumer on the other server")
	require.NotContains(t, strings.ToLower(string(plan.Raw)), "failover")

	pgExec(t, m.Stack, pga.ID, "INSERT INTO notes VALUES (9101, 'last write on the old primary')")
	pr := m.promote(rep, nil)
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, pr.Status, string(pr.Raw))
	status, step, msg := m.waitPromotion(rep.ID, 6*time.Minute)
	require.Equal(t, "succeeded", status, "promotion %s: %s", step, msg)

	// The new primary lives on host2: writable, no committed write lost; the old one is fenced on agent1.
	require.Equal(t, "f", m.Host2.psql(t, rep.ID, "SELECT pg_is_in_recovery()"))
	require.Equal(t, "1", m.Host2.psql(t, rep.ID, "SELECT count(*) FROM notes WHERE id = 9101"), "a switchover loses no committed write")
	require.Empty(t, m.Local.containerOf(pga.ID), "the old primary is stopped")
	var fenced int
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM database_fences WHERE service_id::text = $1`, pga.ID).Scan(&fenced))
	require.Equal(t, 1, fenced)

	// The replication path of pga is released (its exposure and grant go); pgb's replica keeps the link.
	var left int
	require.Truef(t, eventually(2*time.Minute, 2*time.Second, func() bool {
		require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM mesh_exposures WHERE service_id::text = $1 AND purpose = 'replication'`, pga.ID).Scan(&left))
		return left == 0
	}), "the replication exposure of the old primary was not released")

	// The consumer on agent1 is restaged: its reference now points at the new primary, which
	// is on ANOTHER server, so the deploy opens a service link agent1 -> host2 and waits for it.
	require.True(t, m.Service(capi.ID).Bool("hasStagedChanges"))
	pm := m.planMesh(capi.ID)
	require.GreaterOrEqual(t, pm.Int("devicesAdded"), 0, "%v", pm)
	m.deployStagedWait(env, capi.ID)
	waitConsumer(t, cc, "/", "capi", 90*time.Second)
	var follow meshLinkRow
	for _, l := range m.links(t, env) {
		if l.Purpose == agentprotocol.MeshPurposeService && l.Server == m.Srv1 && l.Remote == m.Srv2 {
			follow = l
		}
	}
	require.NotEmpty(t, follow.ID, "the consumer's link follows the primary to host2: %+v", m.links(t, env))
	require.Equal(t, agentprotocol.MeshStateOnline, follow.State)
	waitPgq(t, cc, "DATABASE_URL", "select pg_is_in_recovery()::text", "false", 90*time.Second)
	waitPgq(t, cc, "DATABASE_URL", "INSERT INTO notes VALUES (9102, 'written from agent1 through the mesh') RETURNING 'ok'::text", "ok", 60*time.Second)
	require.Equal(t, "1", m.Host2.psql(t, rep.ID, "SELECT count(*) FROM notes WHERE id = 9102"))
	// The rule for the new link: agent1's link node -> host2's server node, the new primary's mesh port only.
	node2 := m.serverNodeIP(t, m.Srv2)
	m.waitPolicy("rule for the followed link", func(p hsPolicy) bool {
		for _, d := range p.dsts(follow.ipv4()) {
			if !strings.HasPrefix(d, node2+":") {
				return false
			}
		}
		return len(p.dsts(follow.ipv4())) > 0
	})
}

// ---- phase: forced promote with agent1 stopped ----------------------------------------------------

func (m *meshStack) phaseForcedPromote(t *testing.T) {
	ctx := context.Background()
	env := m.EnvID
	pgc := m.createDB("pgc", "postgres", "17")
	m.deployStagedWait(env, pgc.ID)
	waitPostgres(t, m.Stack, pgc.ID)
	pgExec(t, m.Stack, pgc.ID, "CREATE TABLE notes (id int primary key, body text); INSERT INTO notes SELECT g, 'row ' || g FROM generate_series(1, 300) g")
	rep := m.addReplica(pgc, m.Srv2, "pgc-r", map[string]any{"acceptMeshChanges": true})
	m.waitReplica(pgc.ID, rep.ID, 12*time.Minute, replStreaming)
	require.Truef(t, eventually(60*time.Second, time.Second, func() bool {
		out, err := m.Host2.tryPsql(rep.ID, "SELECT count(*) FROM notes")
		return err == nil && out == "300"
	}), "the replica did not clone the primary")

	// The server of the primary dies: stop its agent and its database.
	primaryCID := m.Local.containerOf(pgc.ID)
	require.NotEmpty(t, primaryCID)
	m.Agent.Stop()
	m.Local.mustDocker(t, "stop", primaryCID)
	require.Truef(t, eventually(3*time.Minute, 3*time.Second, func() bool {
		return m.Do("GET", "/servers/"+m.Srv1, nil).JSON().Str("status") == "offline"
	}), "agent1's server never went offline: %s", m.Do("GET", "/servers/"+m.Srv1, nil).Raw)

	// The plan is forced, and data loss has to be accepted explicitly.
	plan := m.Do("POST", "/services/"+rep.ID+"/database/promote", map[string]any{"dryRun": true})
	require.Equal(t, http.StatusOK, plan.Status, string(plan.Raw))
	require.Equal(t, "forced", plan.JSON().Obj("plan").Str("mode"), string(plan.Raw))
	no := m.promote(rep, nil)
	require.Equal(t, http.StatusUnprocessableEntity, no.Status, string(no.Raw))
	require.Contains(t, string(no.Raw), "data_loss_not_accepted")
	require.Equal(t, "t", m.Host2.psql(t, rep.ID, "SELECT pg_is_in_recovery()"), "nothing happened without the acceptance")

	yes := m.promote(rep, map[string]any{"acceptDataLoss": true})
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, yes.Status, string(yes.Raw))
	status, step, msg := m.waitPromotion(rep.ID, 6*time.Minute)
	require.Equal(t, "succeeded", status, "promotion %s: %s", step, msg)
	require.Equal(t, "f", m.Host2.psql(t, rep.ID, "SELECT pg_is_in_recovery()"))
	m.Host2.psql(t, rep.ID, "INSERT INTO notes VALUES (9201, 'first write on the new primary')")
	// The fence was persisted without the dead server's help.
	var fenced int
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM database_fences WHERE service_id::text = $1`, pgc.ID).Scan(&fenced))
	require.Equal(t, 1, fenced)

	// The old primary's host comes back before anything told it: Docker starts the container.
	// When agent1 reconnects, the reconcile carries the fence: the container is stopped and its
	// restart policy is set to no.
	m.Local.mustDocker(t, "start", primaryCID)
	m.StartAgent()
	m.WaitServerOnline(3 * time.Minute)
	require.Truef(t, eventually(2*time.Minute, 2*time.Second, func() bool {
		return m.Local.inspect(t, primaryCID, "{{.State.Running}}") == "false"
	}), "the fenced primary kept running after agent1 reconnected")
	require.Equal(t, "no", m.Local.inspect(t, primaryCID, "{{.HostConfig.RestartPolicy.Name}}"))
	time.Sleep(20 * time.Second)
	require.Equal(t, "false", m.Local.inspect(t, primaryCID, "{{.State.Running}}"), "a fenced primary never runs again")
}

// ---- phase: revoking a server removes its nodes from the tailnet -----------------------------------------

func (m *meshStack) phaseRevoke(t *testing.T) {
	ctx := context.Background()
	node1 := m.serverNodeIP(t, m.Srv1)
	// Every tailnet address that belongs to agent1's server: its node and every link node that
	// runs on it or targets it.
	gone := map[string]bool{node1: true}
	for _, l := range m.links(t, "") {
		if l.Server == m.Srv1 || l.Remote == m.Srv1 {
			gone[l.ipv4()] = true
		}
	}
	before := m.HS.nodes(t)
	var present int
	for _, n := range before {
		if gone[n.ipv4()] {
			present++
		}
	}
	require.GreaterOrEqual(t, present, 2, "agent1's node and at least one link node are in the tailnet: %+v", before)

	r := m.Do("POST", "/servers/"+m.Srv1+"/revoke", map[string]any{})
	require.Contains(t, []int{http.StatusOK, http.StatusNoContent, http.StatusAccepted}, r.Status, string(r.Raw))
	require.Truef(t, eventually(4*time.Minute, 3*time.Second, func() bool {
		for _, n := range m.HS.nodes(t) {
			if gone[n.ipv4()] {
				return false
			}
		}
		return true
	}), "the nodes of the revoked server are still in the tailnet: %+v", m.HS.nodes(t))

	// The ACL no longer mentions the revoked server's addresses.
	m.waitPolicy("no rule for the revoked server", func(p hsPolicy) bool {
		for _, rule := range p.ACLs {
			for _, s := range append(append([]string{}, rule.Src...), rule.Dst...) {
				for ip := range gone {
					if s == ip || strings.HasPrefix(s, ip+":") {
						return false
					}
				}
			}
		}
		return true
	})
	var n int
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM server_mesh WHERE server_id::text = $1`, m.Srv1).Scan(&n))
	require.Zero(t, n, "the mesh node row of the revoked server is gone")
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM mesh_links WHERE server_id::text = $1 OR remote_server_id::text = $1`, m.Srv1).Scan(&n))
	require.Zero(t, n, "links to or from the revoked server are dropped")
	require.NoError(t, m.Pool.QueryRow(ctx, `SELECT count(*) FROM mesh_exposures WHERE server_id::text = $1`, m.Srv1).Scan(&n))
	require.Zero(t, n, "its exposures are dropped")
	// host2's own node survives.
	require.Equal(t, agentprotocol.MeshStateOnline, m.meshNode(m.Srv2).Str("state"))
}
